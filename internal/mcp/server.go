// Package mcp implements the agent-facing MCP surface (spec §8.2): a
// hand-rolled MCP server speaking JSON-RPC 2.0 over stdin/stdout, one
// message per line. No SDK: the M1 surface (initialize, tools/list,
// tools/call, ping) is small enough that the loop is cheaper and stricter
// than an SDK dependency.
//
// Tool results use the {ok, data?, error?{code,message}} envelope with
// stable error codes. tools/call failures keep the protocol layer healthy:
// the stable envelope rides in error.data, never as a process exit. Every
// tool that mutates the filesystem or spawns a process checks the
// capability envelope (.project/state/envelope.yaml) first — a missing or
// invalid envelope denies (fail-closed). Tools that only read the
// repository stay fail-open. The asymmetry with `pika check`, which a
// human runs in their own shell and which therefore needs no envelope, is
// deliberate: an agent is authorized, an operator authorizes.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Choaterboater/pika/internal/adopt"
	"github.com/Choaterboater/pika/internal/checks"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/discover"
	"github.com/Choaterboater/pika/internal/envelope"
	"github.com/Choaterboater/pika/internal/evidence"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/version"
)

// protocolVersion is the MCP protocol generation this server speaks.
const protocolVersion = "2024-11-05"

// JSON-RPC protocol error codes: the reserved parse/request/method/params
// codes plus the server-error range for tool execution failures.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeToolError      = -32000
)

// Stable tool error codes (spec §8.2: stable error codes). They appear in
// error.data.error.code for tools/call failures and error.data.code for
// protocol parameter errors; the set is closed and never localized.
const (
	errInvalidParams   = "invalid_params"
	errEnvelopeDenied  = "envelope_denied"
	errContractInvalid = "contract_invalid"
	errAlreadyAdopted  = "already_adopted"
	errUnavailable     = "unavailable"
	errInternal        = "internal"
)

// Repository-relative kernel locations this package reads and writes
// (spec §5.2 layout).
const (
	envelopePath  = ".project/state/envelope.yaml"
	contractPath  = ".project/contract.yaml"
	boardPath     = ".project/state/board.jsonl"
	evidenceDir   = ".project/evidence"
	contractDraft = ".project/contract.yaml.draft"
	lockDraft     = ".project/profiles.lock.draft"
)

// applyPlanNote explains why the listed tool can never be executed in M1:
// the transactional kernel exists, but no consumer contract for change-set
// application has shipped, so exposing a callable apply would be a policy
// decision the kernel does not yet make.
const applyPlanNote = "apply_plan is unavailable in M1: the transactional apply step has no consumer contract yet; use preview_plan for the read-only adoption preview"

// toolError is the stable {code,message} pair for tool failures.
type toolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func toolErrf(code, format string, args ...any) *toolError {
	return &toolError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Error lets a *toolError travel through an ordinary error return and be
// recovered with errors.As. adopt.Preview's exec authorizer is the first
// caller that needs it: the callback contract is error, but the agent is
// owed the stable code, not a flattened string.
func (e *toolError) Error() string { return e.Code + ": " + e.Message }

// toolResult is the MCP tool result envelope: {ok, data?, error?}.
type toolResult struct {
	OK    bool           `json:"ok"`
	Data  map[string]any `json:"data,omitempty"`
	Error *toolError     `json:"error,omitempty"`
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    *toolResult `json:"data,omitempty"`
}

// tool is one registered MCP tool.
type tool struct {
	name        string
	description string
	inputSchema map[string]any
	handler     func(*server, json.RawMessage) (map[string]any, *toolError)
}

// descriptor is the tools/list projection of a tool: no handler field.
type descriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// tools is the M1 registry (spec §8.2 kernel-backed subset). apply_plan is
// listed for discoverability but its handler always fails with a clear
// message until the M2 board supplies the consumer contract.
var tools = []tool{
	{
		name:        "inspect_repo",
		description: "Discover the repository inventory: packages, detected languages and kinds, existing check commands, git and workflow state.",
		inputSchema: schemaObj(nil),
		handler:     (*server).toolInspectRepo,
	},
	{
		name:        "read_contract",
		description: "Load .project/contract.yaml (or path) and resolve its profiles against the embedded pack registry.",
		inputSchema: schemaObj(map[string]any{
			"path": map[string]any{"type": "string", "description": "contract path; default .project/contract.yaml"},
		}),
		handler: (*server).toolReadContract,
	},
	{
		name:        "preview_plan",
		description: "Run the adoption preview: write the two .draft proposal files (.project/contract.yaml.draft, .project/profiles.lock.draft) and run each discovered check command once to record a baseline. Never touches tracked files; needs fs_write for the drafts and exec for every discovered command.",
		inputSchema: schemaObj(nil),
		handler:     (*server).toolPreviewPlan,
	},
	{
		name:        "run_checks",
		description: "Run the verification ladder (contract gate plus profile and discovered check commands). Args: {scope: all|changed|ci} (default all).",
		inputSchema: schemaObj(map[string]any{
			"scope": map[string]any{"type": "string", "enum": []string{"all", "changed", "ci"}},
		}),
		handler: (*server).toolRunChecks,
	},
	{
		name:        "acquire_scope",
		description: "Acquire an exclusive write lease on a repository-relative path. The capability envelope is the authority: only paths declared under allow.fs_write are granted.",
		inputSchema: schemaObj(map[string]any{
			"path": map[string]any{"type": "string", "description": "repository-relative path to lease"},
		}, "path"),
		handler: (*server).toolAcquireScope,
	},
	{
		name:        "release_scope",
		description: "Release a previously acquired write lease on a repository-relative path.",
		inputSchema: schemaObj(map[string]any{
			"path": map[string]any{"type": "string"},
		}, "path"),
		handler: (*server).toolReleaseScope,
	},
	{
		name:        "publish_evidence",
		description: "Build a redacted, schema-validated work receipt from the provided receipt input and write it under .project/evidence. Redaction is enforced inside the kernel.",
		inputSchema: schemaObj(map[string]any{
			"receipt": map[string]any{"type": "object", "description": "evidence ReceiptInput, snake_case JSON keys"},
			"path":    map[string]any{"type": "string", "description": "receipt target; default .project/evidence/<work_id>.json"},
		}, "receipt"),
		handler: (*server).toolPublishEvidence,
	},
	{
		name:        "propose_decision",
		description: "Append a decision record to the M1 state board (.project/state/board.jsonl).",
		inputSchema: schemaObj(map[string]any{
			"title":     map[string]any{"type": "string"},
			"rationale": map[string]any{"type": "string"},
		}, "title"),
		handler: (*server).toolProposeDecision,
	},
	{
		name:        "record_sources",
		description: "Append source references to the M1 state board (.project/state/board.jsonl).",
		inputSchema: schemaObj(map[string]any{
			"sources": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, "sources"),
		handler: (*server).toolRecordSources,
	},
	{
		name:        "apply_plan",
		description: applyPlanNote,
		inputSchema: schemaObj(nil),
		handler:     (*server).toolApplyPlan,
	},
}

// schemaObj builds a JSON Schema object schema with the given properties
// (possibly nil) and required property names.
func schemaObj(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	if len(required) == 0 {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required}
}

// server is one stdio session's configuration. Requests are processed
// sequentially, so the server carries no mutable state.
type server struct {
	repoRoot string
}

// Serve runs the MCP stdio JSON-RPC loop against repoRoot until stdin EOF
// (clean shutdown). Requests are handled sequentially — single-flight, no
// goroutines in M1. A malformed line yields a JSON-RPC parse-error response
// and never terminates the session. Only JSON-RPC messages are written to
// out; diagnostics belong on errOut.
func Serve(repoRoot string, in io.Reader, out, errOut io.Writer) error {
	s := &server{repoRoot: repoRoot}
	r := bufio.NewReader(in)
	w := bufio.NewWriter(out)
	for {
		line, err := r.ReadString('\n')
		if strings.TrimSpace(line) != "" {
			s.dispatch(line, w)
			if flushErr := w.Flush(); flushErr != nil {
				return fmt.Errorf("mcp: write response: %w", flushErr)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("mcp: read request: %w", err)
		}
	}
}

// dispatch handles one request line.
func (s *server) dispatch(line string, w io.Writer) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		// Recoverable parse failure: answer with a JSON-RPC error object and
		// keep the session alive (spec: recoverable parse failures do not
		// terminate the server).
		s.respond(w, rpcResponse{ID: json.RawMessage("null"), Error: &rpcError{
			Code:    codeParseError,
			Message: "parse error: request line is not a valid JSON-RPC message",
		}})
		return
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return // notification: no response
	}
	if req.Method == "" {
		s.respond(w, rpcResponse{ID: req.ID, Error: paramsError("invalid request: missing method")})
		return
	}
	switch req.Method {
	case "initialize":
		s.respond(w, rpcResponse{ID: req.ID, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "pika", "version": version.String()},
		}})
	case "ping":
		s.respond(w, rpcResponse{ID: req.ID, Result: map[string]any{}})
	case "tools/list":
		list := make([]descriptor, 0, len(tools))
		for _, tl := range tools {
			list = append(list, descriptor{Name: tl.name, Description: tl.description, InputSchema: tl.inputSchema})
		}
		s.respond(w, rpcResponse{ID: req.ID, Result: map[string]any{"tools": list}})
	case "tools/call":
		s.callTool(req, w)
	default:
		s.respond(w, rpcResponse{ID: req.ID, Error: &rpcError{
			Code:    codeMethodNotFound,
			Message: fmt.Sprintf("method not found: %s", req.Method),
		}})
	}
}

// callTool dispatches one tools/call request. Tool failures stay inside the
// protocol: the stable error envelope rides in error.data, never as a
// process exit.
func (s *server) callTool(req rpcRequest, w io.Writer) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		s.respond(w, rpcResponse{ID: req.ID, Error: paramsError("tools/call requires {name: string, arguments: object}")})
		return
	}
	for i := range tools {
		if tools[i].name != params.Name {
			continue
		}
		data, terr := tools[i].handler(s, params.Arguments)
		if terr != nil {
			s.respond(w, rpcResponse{ID: req.ID, Error: &rpcError{
				Code:    codeToolError,
				Message: terr.Message,
				Data:    &toolResult{OK: false, Error: terr},
			}})
			return
		}
		s.respond(w, rpcResponse{ID: req.ID, Result: &toolResult{OK: true, Data: data}})
		return
	}
	s.respond(w, rpcResponse{ID: req.ID, Error: paramsError("unknown tool %q", params.Name)})
}

// paramsError builds a -32602 body carrying the stable invalid_params code.
func paramsError(format string, args ...any) *rpcError {
	msg := fmt.Sprintf(format, args...)
	return &rpcError{
		Code:    codeInvalidParams,
		Message: msg,
		Data:    &toolResult{OK: false, Error: &toolError{Code: errInvalidParams, Message: msg}},
	}
}

// --- tool handlers ---

// toolInspectRepo implements inspect_repo: the discovery inventory.
// Read-only and fail-open without an envelope.
func (s *server) toolInspectRepo(_ json.RawMessage) (map[string]any, *toolError) {
	inv, err := discover.Discover(s.repoRoot)
	if err != nil {
		return nil, toolErrf(errInternal, "discover: %v", err)
	}
	return map[string]any{"inventory": inv}, nil
}

// toolReadContract implements read_contract: contract.Load plus the
// resolved profile composition.
func (s *server) toolReadContract(args json.RawMessage) (map[string]any, *toolError) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, toolErrf(errInvalidParams, "read_contract arguments: %v", err)
	}
	path := contractPath
	if params.Path != "" {
		// The path argument must name a file inside the repository: reads
		// stay inside the repo root, so no argument can exfiltrate files
		// from outside it.
		rel, err := contract.NormalizeRepoPath(params.Path)
		if err != nil {
			return nil, toolErrf(errInvalidParams, "read_contract path: %v", err)
		}
		path = rel
	}
	c, err := contract.Load(filepath.Join(s.repoRoot, filepath.FromSlash(path)))
	if err != nil {
		return nil, toolErrf(errContractInvalid, "%v", err)
	}
	resolved, err := profiles.Resolve(c.Profiles)
	if err != nil {
		return nil, toolErrf(errContractInvalid, "%v", err)
	}
	return map[string]any{
		"contract": c,
		"profiles": summarizeProfiles(resolved),
	}, nil
}

// resolvedProfiles is the read_contract projection of profiles.Resolved:
// layer identities and the effective command per verification slot.
type resolvedProfiles struct {
	Selected []string          `json:"selected"`
	Layers   []resolvedLayer   `json:"layers"`
	Checks   map[string]string `json:"checks"`
}

type resolvedLayer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source"`
}

func summarizeProfiles(r *profiles.Resolved) resolvedProfiles {
	out := resolvedProfiles{Checks: map[string]string{}}
	for _, l := range r.Layers {
		out.Selected = append(out.Selected, l.Name+"@"+l.Version)
		out.Layers = append(out.Layers, resolvedLayer{Name: l.Name, Version: l.Version, Source: l.Source})
	}
	slots := map[string]profiles.Check{
		"format":    r.Checks.Format,
		"lint":      r.Checks.Lint,
		"typecheck": r.Checks.Typecheck,
		"test":      r.Checks.Test,
		"smoke":     r.Checks.Smoke,
	}
	for slot, c := range slots {
		if len(c.Cmd) > 0 {
			out.Checks[slot] = strings.Join(c.Cmd, " ")
		}
	}
	return out
}

// toolPreviewPlan implements preview_plan: the adoption inventory, which
// writes exactly the two draft files and — because a baseline is the point
// of an adoption preview — runs every check command discovery finds in the
// repository, once each. Both effects are authorized before either
// happens: fs_write for the drafts, exec for every command the preview
// would spawn. An envelope granting writes and no exec cannot make this
// server spawn a process it found lying in the repository.
func (s *server) toolPreviewPlan(_ json.RawMessage) (map[string]any, *toolError) {
	for _, target := range []string{contractDraft, lockDraft} {
		if terr := s.authorize(envelope.KindFSWrite, target); terr != nil {
			return nil, terr
		}
	}
	if _, err := os.Stat(filepath.Join(s.repoRoot, filepath.FromSlash(contractPath))); err == nil {
		return nil, toolErrf(errAlreadyAdopted, "%s already exists: repository already adopted; run_checks verifies it", contractPath)
	}
	// adopt.Preview decides which commands it will spawn and asks here
	// before running any of them; a denial aborts the preview whole, the
	// same all-or-nothing rule run_checks applies to its gates.
	report, err := adopt.Preview(s.repoRoot, adopt.WithExecAuthorizer(func(commands [][]string) error {
		for _, argv := range commands {
			if terr := s.authorize(envelope.KindExec, strings.Join(argv, " ")); terr != nil {
				return terr
			}
		}
		return nil
	}))
	if err != nil {
		var denied *toolError
		if errors.As(err, &denied) {
			return nil, denied
		}
		return nil, toolErrf(errInternal, "preview: %v", err)
	}
	return map[string]any{
		"detectedProfiles": report.DetectedProfiles,
		"conventions":      len(report.ConventionMap),
		"conflicts":        report.Conflicts,
		"proposedChanges":  report.ProposedChanges,
		"baselineChecks":   report.BaselineChecks,
		"drafts":           []string{contractDraft, lockDraft},
	}, nil
}

// toolRunChecks implements run_checks: the same ladder as `pika
// check` (contract gate plus profile and discovered commands), scoped by
// args. Read-only with respect to the repository, but not with respect to
// the machine: every gate with an argv spawns a process, so each one is
// authorized as exec before anything runs.
func (s *server) toolRunChecks(args json.RawMessage) (map[string]any, *toolError) {
	var params struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, toolErrf(errInvalidParams, "run_checks arguments: %v", err)
	}
	scope := verify.All
	switch strings.ToLower(params.Scope) {
	case "", "all":
		scope = verify.All
	case "changed":
		scope = verify.Changed
	case "ci":
		scope = verify.CI
	default:
		return nil, toolErrf(errInvalidParams, "scope must be one of all, changed, ci; got %q", params.Scope)
	}
	c, err := contract.Load(filepath.Join(s.repoRoot, filepath.FromSlash(contractPath)))
	if err != nil {
		return nil, toolErrf(errContractInvalid, "%v", err)
	}
	resolved, err := profiles.Resolve(c.Profiles)
	if err != nil {
		return nil, toolErrf(errContractInvalid, "%v", err)
	}
	// Rung 1 (spec §12.6): the shared kernel gate — identical to the
	// check command by construction.
	var gate1Warnings []string
	gates := verify.CheckSet{{
		ID: "contract",
		Func: func(context.Context) (int, string) {
			exit, output, warnings := checks.Gate1(s.repoRoot, c, resolved)
			gate1Warnings = warnings
			return exit, output
		},
	}}
	ordered, err := verify.FromProfiles(resolved.Checks, c.Commands)
	if err != nil {
		return nil, toolErrf(errContractInvalid, "%v", err)
	}
	gates = append(gates, ordered...)
	// Every gate that carries an argv spawns a real process, so the
	// envelope must authorize it before a single one runs — one denial
	// fails the whole call rather than half-executing the ladder. Gates
	// with an empty Cmd are the in-process contract gate or recorded
	// discovery skips; they spawn nothing and need no exec grant.
	for _, g := range gates {
		if len(g.Cmd) == 0 {
			continue
		}
		if terr := s.authorize(envelope.KindExec, strings.Join(g.Cmd, " ")); terr != nil {
			return nil, terr
		}
	}
	// The gates must run in the repository the server was pointed at
	// (--root, or the discovered root), never the server process's own
	// working directory — mirroring check.go.
	report, err := verify.Run(context.Background(), gates, scope, verify.WithDir(s.repoRoot))
	if err != nil {
		return nil, toolErrf(errInternal, "verify: %v", err)
	}
	report.Warnings = append(report.Warnings, gate1Warnings...)
	return map[string]any{"report": report}, nil
}

// toolAcquireScope implements acquire_scope: an envelope-backed lease over
// one declared path. The envelope is the authority — the lease is granted
// exactly when the envelope declares fs_write for the path; a denial is the
// stable envelope_denied code, never a policy decision made here.
func (s *server) toolAcquireScope(args json.RawMessage) (map[string]any, *toolError) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.Path == "" {
		return nil, toolErrf(errInvalidParams, "acquire_scope requires {path: string}")
	}
	rel, err := contract.NormalizeRepoPath(params.Path)
	if err != nil {
		return nil, toolErrf(errInvalidParams, "acquire_scope path: %v", err)
	}
	if terr := s.authorize(envelope.KindFSWrite, rel); terr != nil {
		return nil, terr
	}
	if err := s.appendBoard(map[string]any{"type": "scope_lease", "action": "acquire", "path": rel}); err != nil {
		return nil, toolErrf(errInternal, "append board: %v", err)
	}
	return map[string]any{"path": rel, "granted": true}, nil
}

// toolReleaseScope implements release_scope; the same envelope gate applies.
func (s *server) toolReleaseScope(args json.RawMessage) (map[string]any, *toolError) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.Path == "" {
		return nil, toolErrf(errInvalidParams, "release_scope requires {path: string}")
	}
	rel, err := contract.NormalizeRepoPath(params.Path)
	if err != nil {
		return nil, toolErrf(errInvalidParams, "release_scope path: %v", err)
	}
	if terr := s.authorize(envelope.KindFSWrite, rel); terr != nil {
		return nil, terr
	}
	if err := s.appendBoard(map[string]any{"type": "scope_lease", "action": "release", "path": rel}); err != nil {
		return nil, toolErrf(errInternal, "append board: %v", err)
	}
	return map[string]any{"path": rel, "released": true}, nil
}

// receiptJSON mirrors evidence.ReceiptInput with snake_case JSON keys — the
// tool-level wire shape an agent publishes. ReceiptInput itself carries no
// JSON tags (it is an internal, pre-sanitization type), so the mapping
// lives here and Build still enforces the redaction invariant end to end.
type receiptJSON struct {
	WorkID           string            `json:"work_id"`
	ContractVersion  string            `json:"contract_version"`
	ProfileLock      profileLockJSON   `json:"profile_lock"`
	Commit           string            `json:"commit"`
	Tree             string            `json:"tree"`
	Roles            []roleJSON        `json:"roles"`
	ChangedFiles     []changedFileJSON `json:"changed_files"`
	Commands         []commandJSON     `json:"commands"`
	SurfaceScenario  surfaceJSON       `json:"surface_scenario"`
	BaselineFailures []string          `json:"baseline_failures"`
	Regressions      []string          `json:"regressions"`
	Review           []reviewJSON      `json:"review"`
	DocsImpact       []string          `json:"docs_impact"`
	Completion       completionJSON    `json:"completion"`
}

type profileLockJSON struct {
	Digest string              `json:"digest"`
	Packs  map[string]packJSON `json:"packs"`
}

type packJSON struct {
	Version string `json:"version"`
	Source  string `json:"source"`
	Digest  string `json:"digest"`
}

type roleJSON struct {
	Role        string `json:"role"`
	Runtime     string `json:"runtime"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Substituted bool   `json:"substituted"`
}

type changedFileJSON struct {
	Path      string `json:"path"`
	Ownership string `json:"ownership"`
}

type commandJSON struct {
	Cmd        string `json:"cmd"`
	Exit       int    `json:"exit"`
	DurationMs int64  `json:"duration_ms"`
	Output     string `json:"output"`
}

type surfaceJSON struct {
	Ran         bool   `json:"ran"`
	Description string `json:"description"`
}

type reviewJSON struct {
	Agent       string `json:"agent"`
	Finding     string `json:"finding"`
	Disposition string `json:"disposition"`
}

type completionJSON struct {
	Complete bool   `json:"complete"`
	Reason   string `json:"reason"`
}

// input converts the wire shape to the kernel's ReceiptInput.
func (r receiptJSON) input() evidence.ReceiptInput {
	in := evidence.ReceiptInput{
		WorkID:           r.WorkID,
		ContractVersion:  r.ContractVersion,
		ProfileLock:      evidence.ProfileLockInput{Digest: r.ProfileLock.Digest},
		Commit:           r.Commit,
		Tree:             r.Tree,
		SurfaceScenario:  evidence.SurfaceScenarioInput{Ran: r.SurfaceScenario.Ran, Description: r.SurfaceScenario.Description},
		BaselineFailures: r.BaselineFailures,
		Regressions:      r.Regressions,
		DocsImpact:       r.DocsImpact,
		Completion:       evidence.CompletionInput{Complete: r.Completion.Complete, Reason: r.Completion.Reason},
	}
	if r.ProfileLock.Packs != nil {
		in.ProfileLock.Packs = map[string]evidence.PackInput{}
		for name, p := range r.ProfileLock.Packs {
			in.ProfileLock.Packs[name] = evidence.PackInput{Version: p.Version, Source: p.Source, Digest: p.Digest}
		}
	}
	for _, role := range r.Roles {
		in.Roles = append(in.Roles, evidence.RoleInput{Role: role.Role, Runtime: role.Runtime, Provider: role.Provider, Model: role.Model, Substituted: role.Substituted})
	}
	for _, f := range r.ChangedFiles {
		in.ChangedFiles = append(in.ChangedFiles, evidence.ChangedFileInput{Path: f.Path, Ownership: f.Ownership})
	}
	for _, cmd := range r.Commands {
		in.Commands = append(in.Commands, evidence.CommandInput{Cmd: cmd.Cmd, Exit: cmd.Exit, DurationMs: cmd.DurationMs, Output: cmd.Output})
	}
	for _, rev := range r.Review {
		in.Review = append(in.Review, evidence.ReviewInput{Agent: rev.Agent, Finding: rev.Finding, Disposition: rev.Disposition})
	}
	return in
}

// toolPublishEvidence implements publish_evidence: evidence.Build (redaction
// and schema validation enforced inside the kernel) followed by the atomic
// evidence.Write.
func (s *server) toolPublishEvidence(args json.RawMessage) (map[string]any, *toolError) {
	var params struct {
		Path    string          `json:"path"`
		Receipt json.RawMessage `json:"receipt"`
	}
	if err := json.Unmarshal(args, &params); err != nil || len(params.Receipt) == 0 {
		return nil, toolErrf(errInvalidParams, "publish_evidence requires {receipt: object, path?: string}")
	}
	var rj receiptJSON
	if err := json.Unmarshal(params.Receipt, &rj); err != nil {
		return nil, toolErrf(errInvalidParams, "receipt: %v", err)
	}
	receipt, err := evidence.Build(rj.input())
	if err != nil {
		// Build validates work-id shape, required fields, and the receipt
		// schema; a rejected receipt is caller input error.
		return nil, toolErrf(errInvalidParams, "invalid receipt input: %v", err)
	}
	target := params.Path
	if target == "" {
		target = evidenceDir + "/" + receipt.WorkID + ".json"
	}
	rel, err := contract.NormalizeRepoPath(target)
	if err != nil {
		return nil, toolErrf(errInvalidParams, "publish_evidence path: %v", err)
	}
	if terr := s.authorize(envelope.KindFSWrite, rel); terr != nil {
		return nil, terr
	}
	if err := evidence.Write(filepath.Join(s.repoRoot, filepath.FromSlash(rel)), receipt); err != nil {
		return nil, toolErrf(errInternal, "write receipt: %v", err)
	}
	return map[string]any{"path": rel, "workId": receipt.WorkID}, nil
}

// toolProposeDecision implements propose_decision: an append-only decision
// record on the M1 state board (single-writer; the M2 board replaces it).
func (s *server) toolProposeDecision(args json.RawMessage) (map[string]any, *toolError) {
	var params struct {
		Title     string `json:"title"`
		Rationale string `json:"rationale"`
	}
	if err := json.Unmarshal(args, &params); err != nil || strings.TrimSpace(params.Title) == "" {
		return nil, toolErrf(errInvalidParams, "propose_decision requires a non-empty title")
	}
	if terr := s.authorize(envelope.KindFSWrite, boardPath); terr != nil {
		return nil, terr
	}
	if err := s.appendBoard(map[string]any{"type": "decision", "title": params.Title, "rationale": params.Rationale}); err != nil {
		return nil, toolErrf(errInternal, "append board: %v", err)
	}
	return map[string]any{"recorded": true}, nil
}

// toolRecordSources implements record_sources: append-only source records.
func (s *server) toolRecordSources(args json.RawMessage) (map[string]any, *toolError) {
	var params struct {
		Sources []string `json:"sources"`
	}
	if err := json.Unmarshal(args, &params); err != nil || len(params.Sources) == 0 {
		return nil, toolErrf(errInvalidParams, "record_sources requires a non-empty sources array")
	}
	if terr := s.authorize(envelope.KindFSWrite, boardPath); terr != nil {
		return nil, terr
	}
	if err := s.appendBoard(map[string]any{"type": "sources", "sources": params.Sources}); err != nil {
		return nil, toolErrf(errInternal, "append board: %v", err)
	}
	return map[string]any{"recorded": true}, nil
}

// toolApplyPlan is listed for discoverability but never executable in M1.
// The refusal is errUnavailable, not errInternal: "this build will never
// do it" and "the kernel just failed" call for different agent behavior,
// and an agent should not have to match on a message to tell them apart.
func (s *server) toolApplyPlan(_ json.RawMessage) (map[string]any, *toolError) {
	return nil, toolErrf(errUnavailable, "%s", applyPlanNote)
}

// authorize is the single authorization choke point. Every tool that
// mutates the filesystem or spawns a process passes through it before the
// effect happens, so denial is always fail-closed. The envelope at
// .project/state/envelope.yaml is the authority; a missing or invalid
// envelope denies. The server's own repoRoot is what the envelope is
// bound to, so moving the envelope file cannot widen or narrow the
// authorized scope.
func (s *server) authorize(kind, target string) *toolError {
	env, err := envelope.Load(s.repoRoot, filepath.Join(s.repoRoot, filepath.FromSlash(envelopePath)))
	if err != nil {
		return toolErrf(errEnvelopeDenied, "no usable capability envelope (%v): %s of %s denied", err, kind, target)
	}
	if !env.Allows(envelope.Operation{Kind: kind, Target: target}) {
		return toolErrf(errEnvelopeDenied, "%s not authorized for %q; run \"pika authorize\"", kind, target)
	}
	return nil
}

// appendBoard appends one JSON record to the M1 state board. M1 is
// single-writer — the MCP server processes requests sequentially — so the
// append is an unlocked file append; the M2 coordination board replaces
// this file.
func (s *server) appendBoard(record map[string]any) error {
	record["ts"] = time.Now().UTC().Format(time.RFC3339)
	path := filepath.Join(s.repoRoot, filepath.FromSlash(boardPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// respond writes exactly one JSON-RPC response line. A response that cannot
// marshal (impossible by construction, but never wedges the loop) falls
// back to a minimal internal error.
func (s *server) respond(w io.Writer, resp rpcResponse) {
	resp.JSONRPC = "2.0"
	bs, err := json.Marshal(resp)
	if err != nil {
		bs, _ = json.Marshal(rpcResponse{ID: resp.ID, Error: &rpcError{Code: codeToolError, Message: "internal: response encoding failed"}})
	}
	fmt.Fprintf(w, "%s\n", bs)
}
