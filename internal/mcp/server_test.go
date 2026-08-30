package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Choaterboater/pika/internal/authorize"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
)

// session drives one stdio MCP server over real OS pipes, mirroring how an
// MCP client talks to `pika mcp`.
type session struct {
	t    *testing.T
	inW  *os.File
	outR *os.File
	r    *bufio.Reader
	done chan error
}

func startServer(t *testing.T, root string) *session {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	s := &session{t: t, inW: inW, outR: outR, r: bufio.NewReader(outR), done: make(chan error, 1)}
	go func() { s.done <- Serve(root, inR, outW, io.Discard) }()
	t.Cleanup(func() {
		inW.Close()
		select {
		case err := <-s.done:
			if err != nil {
				t.Errorf("server exit: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("server did not exit after stdin EOF")
		}
		outR.Close()
	})
	return s
}

func (s *session) send(line string) map[string]any {
	s.t.Helper()
	if _, err := s.inW.WriteString(line + "\n"); err != nil {
		s.t.Fatalf("write request: %v", err)
	}
	return s.recv()
}

func (s *session) recv() map[string]any {
	s.t.Helper()
	line, err := s.r.ReadString('\n')
	if err != nil {
		s.t.Fatalf("read response: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		s.t.Fatalf("response is not a JSON object: %q: %v", line, err)
	}
	if resp["jsonrpc"] != "2.0" {
		s.t.Fatalf("response jsonrpc = %v, want 2.0", resp["jsonrpc"])
	}
	return resp
}

func (s *session) request(id int, method string, params map[string]any) map[string]any {
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	return s.send(mustJSON(req))
}

// callTool wraps a tools/call request with name/arguments framing.
func (s *session) callTool(id int, name string, args map[string]any) map[string]any {
	return s.request(id, "tools/call", map[string]any{"name": name, "arguments": args})
}

func (s *session) initialize() map[string]any {
	return s.request(0, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0"},
	})
}

// wantResult asserts a successful envelope response and returns result.
func wantResult(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	if errObj, ok := resp["error"]; ok {
		t.Fatalf("expected success, got error: %v", errObj)
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %v", resp["result"])
	}
	if res["ok"] != true {
		t.Fatalf("result.ok = %v, want true", res["ok"])
	}
	return res
}

// wantToolError asserts a failed tools/call: JSON-RPC code -32000 with the
// stable string code in data.error.code, and returns the error object.
func wantToolError(t *testing.T, resp map[string]any, wantCode string) map[string]any {
	t.Helper()
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error, got %v", resp)
	}
	code, _ := errObj["code"].(float64)
	if code != -32000 {
		t.Fatalf("error.code = %v, want -32000", errObj["code"])
	}
	data, ok := errObj["data"].(map[string]any)
	if !ok {
		t.Fatalf("error.data = %v, want envelope object", errObj["data"])
	}
	if data["ok"] != false {
		t.Fatalf("data.ok = %v, want false", data["ok"])
	}
	te, ok := data["error"].(map[string]any)
	if !ok {
		t.Fatalf("data.error = %v, want {code,message}", data["error"])
	}
	if te["code"] != wantCode {
		t.Fatalf("error code = %v, want %q", te["code"], wantCode)
	}
	if msg, _ := te["message"].(string); msg == "" {
		t.Fatal("error message must be non-empty")
	}
	return errObj
}

// wantRPCError asserts a protocol-level JSON-RPC error with the given code.
func wantRPCError(t *testing.T, resp map[string]any, wantCode float64) map[string]any {
	t.Helper()
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error, got %v", resp)
	}
	if code, _ := errObj["code"].(float64); code != wantCode {
		t.Fatalf("error.code = %v, want %v", errObj["code"], wantCode)
	}
	if _, ok := errObj["message"].(string); !ok {
		t.Fatalf("error.message missing: %v", errObj)
	}
	return errObj
}

// --- fixtures ---

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureRepo builds a minimal Go repository: enough for discovery, plus an
// optional contract and envelope.
func fixtureRepo(t *testing.T, contract, envelopeYAML string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/fixture\n\ngo 1.21\n")
	if contract != "" {
		writeFile(t, root, ".project/contract.yaml", contract)
		// Gate 1 validates the profile lock, so a contract fixture pins
		// the same selection the contract declares.
		if err := profiles.WriteLock(filepath.Join(root, ".project", "profiles.lock"), []string{"core@1"}); err != nil {
			t.Fatal(err)
		}
	}
	if envelopeYAML != "" {
		writeFile(t, root, ".project/state/envelope.yaml", envelopeYAML)
	}
	return root
}

const minContract = `schema: 1
project:
  name: fixture
  topology: single
profiles:
  - core@1
commands:
  test: go version
evidence:
  publish: sanitized
github:
  merge: squash
extensions: {}
`

// envelopeWith builds a valid envelope document granting the fs_write paths.
func envelopeYAML(paths ...string) string {
	return "schema: 1\nallow:\n  fs_write: [" + strings.Join(paths, ", ") + "]\n"
}

// writeGeneratedEnvelope writes the envelope `pika authorize --scope
// <scope>` would generate for the repository at root, with any explicit
// --exec grants. Using the real generator rather than a hand-written
// document is the point: it proves what authorize grants and what this
// package enforces are the same thing, and it fails the moment the two
// drift.
func writeGeneratedEnvelope(t *testing.T, root, scope string, execGrants ...string) {
	t.Helper()
	r, err := repopath.At(root)
	if err != nil {
		t.Fatal(err)
	}
	env, _, err := authorize.Build(authorize.Options{Root: r, Scope: scope, Exec: execGrants})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := authorize.Render(env)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, ".project/state/envelope.yaml", string(doc))
}

func evidenceArgs() map[string]any {
	return map[string]any{
		"receipt": map[string]any{
			"work_id":          "20260828-mcp-test-a1b2",
			"contract_version": "1.0.0",
			"profile_lock": map[string]any{
				"digest": "sha256:abcdef0123456789",
				"packs": map[string]any{
					"core": map[string]any{"version": "1", "source": "embedded", "digest": "sha256:0011"},
				},
			},
			"commit": "34b828f",
			"tree":   "tree/9aa2",
			"roles": []any{
				map[string]any{"role": "implementer", "runtime": "omp", "provider": "openrouter", "model": "glm"},
			},
			"changed_files": []any{
				map[string]any{"path": "internal/mcp/server.go", "ownership": "kernel"},
			},
			"commands": []any{
				map[string]any{"cmd": "go test ./...", "exit": 0, "duration_ms": 1200,
					"output": "ok\tsk-ant-api03-abcdefghij0123456789ABCDE\t1.2s"},
			},
			"surface_scenario":  map[string]any{"ran": true, "description": "ran pika check --all locally"},
			"baseline_failures": []any{},
			"regressions":       []any{},
			"review": []any{
				map[string]any{"agent": "reviewer", "finding": "error wrapped twice", "disposition": "fixed"},
			},
			"docs_impact": []any{},
			"completion":  map[string]any{"complete": true, "reason": "all gates green"},
		},
	}
}

func TestInitializeToolsListAndRoundtrip(t *testing.T) {
	root := fixtureRepo(t, "", envelopeYAML(".project"))
	s := startServer(t, root)

	// initialize
	resp := s.initialize()
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize: expected result, got %v", resp)
	}
	if res["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion = %v, want 2024-11-05", res["protocolVersion"])
	}
	caps, _ := res["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("capabilities = %v, want tools capability", caps)
	}
	info, ok := res["serverInfo"].(map[string]any)
	if !ok || info["name"] != "pika" {
		t.Fatalf("serverInfo = %v, want name pika", res["serverInfo"])
	}
	if id, ok := resp["id"].(float64); !ok || id != 0 {
		t.Fatalf("response id = %v, want 0", resp["id"])
	}

	// ping
	if resp := s.request(1, "ping", nil); resp["result"] == nil {
		t.Fatalf("ping: expected result, got %v", resp)
	}

	// tools/list: assert the exact tool set.
	resp = s.request(2, "tools/list", nil)
	res = resp["result"].(map[string]any)
	tools, ok := res["tools"].([]any)
	if !ok {
		t.Fatalf("tools = %v, want a list", res["tools"])
	}
	got := map[string]bool{}
	for _, entry := range tools {
		tool, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("tool entry %v is not an object", tool)
		}
		name, _ := tool["name"].(string)
		if name == "" {
			t.Fatalf("tool entry missing name: %v", tool)
		}
		got[name] = true
		if desc, _ := tool["description"].(string); desc == "" {
			t.Errorf("tool %q missing description", name)
		}
		if _, ok := tool["inputSchema"]; !ok {
			t.Errorf("tool %q missing inputSchema", name)
		}
	}
	want := []string{
		"inspect_repo", "read_contract", "preview_plan", "run_checks",
		"acquire_scope", "release_scope", "publish_evidence",
		"propose_decision", "record_sources", "apply_plan",
	}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(tools), len(want))
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("tools/list missing %q", w)
		}
	}

	// tools/call inspect_repo — fail-open read tool.
	res = wantResult(t, s.callTool(3, "inspect_repo", nil))
	inv, ok := res["data"].(map[string]any)["inventory"].(map[string]any)
	if !ok {
		t.Fatalf("inspect_repo data.inventory missing: %v", res["data"])
	}
	pkgs, ok := inv["Packages"].([]any)
	if !ok || len(pkgs) != 1 {
		t.Fatalf("inventory packages = %v, want 1 go package", res["data"])
	}

	// tools/call publish_evidence with a fixture receipt.
	res = wantResult(t, s.callTool(4, "publish_evidence", evidenceArgs()))
	data := res["data"].(map[string]any)
	if data["path"] != ".project/evidence/20260828-mcp-test-a1b2.json" {
		t.Fatalf("publish path = %v", res["data"])
	}
}

func TestPublishEvidenceRedactsAndWrites(t *testing.T) {
	root := fixtureRepo(t, "", envelopeYAML(".project"))
	s := startServer(t, root)
	s.initialize()

	res := wantResult(t, s.callTool(1, "publish_evidence", evidenceArgs()))
	path := filepath.Join(root, ".project", "evidence", "20260828-mcp-test-a1b2.json")
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("receipt not written: %v", err)
	}
	body := string(bs)
	raw := "sk-ant-api03-abcdefghij0123456789ABCDE"
	if strings.Contains(body, raw) {
		t.Fatal("receipt on disk contains the raw credential")
	}
	if !strings.Contains(body, "<redacted:") {
		t.Fatal("receipt output was not redacted")
	}
	if !strings.Contains(fmt.Sprintf("%v", res["data"]), "20260828-mcp-test-a1b2") {
		t.Fatalf("publish_evidence data missing work id: %v", res["data"])
	}
}

// A receipt issued by the component that ran the gates is evidence; one
// supplied by the agent whose work it attests is a claim. evidence.Write
// renames over an existing target without complaint — improve.issueReceipt
// depends on exactly that to fill the file it claimed with O_EXCL — so the
// refusal has to live at this end. Without it an agent can replace a
// kernel-issued receipt with its own, which is the substitution this
// milestone exists to prevent.
//
// The assertion is on the bytes, not the return value: the guarantee is
// about the file. The second receipt therefore differs from the first, so
// a silent overwrite cannot pass by writing identical content.
func TestPublishEvidenceRefusesToOverwriteAnExistingReceipt(t *testing.T) {
	root := fixtureRepo(t, "", envelopeYAML(".project"))
	s := startServer(t, root)
	s.initialize()

	wantResult(t, s.callTool(1, "publish_evidence", evidenceArgs()))
	path := filepath.Join(root, ".project", "evidence", "20260828-mcp-test-a1b2.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("first receipt not written: %v", err)
	}

	substitute := evidenceArgs()
	receipt := substitute["receipt"].(map[string]any)
	receipt["commit"] = "deadbee"
	receipt["completion"] = map[string]any{"complete": true, "reason": "substituted by the agent"}
	errObj := wantToolError(t, s.callTool(2, "publish_evidence", substitute), "invalid_params")
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "already exists") {
		t.Errorf("refusal message = %q, want it to name the existing receipt", msg)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("receipt gone after the refused publish: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("receipt was replaced:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if strings.Contains(string(after), "deadbee") {
		t.Fatal("the agent's substituted receipt reached disk")
	}

	// A refusal that got as far as evidence.Write would leave its temp
	// file behind in the evidence directory.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".evidence-") {
			t.Fatalf("refused publish left a temp file: %s", e.Name())
		}
	}

	// The guarantee is about an occupied path, not about publishing: a
	// fresh path still works.
	fresh := evidenceArgs()
	fresh["receipt"].(map[string]any)["work_id"] = "20260828-mcp-test-c3d4"
	res := wantResult(t, s.callTool(3, "publish_evidence", fresh))
	if res["data"].(map[string]any)["path"] != ".project/evidence/20260828-mcp-test-c3d4.json" {
		t.Fatalf("fresh publish path = %v", res["data"])
	}
	if _, err := os.Stat(filepath.Join(root, ".project", "evidence", "20260828-mcp-test-c3d4.json")); err != nil {
		t.Fatalf("fresh receipt not written: %v", err)
	}
}

func TestFailOpenReadsFailClosedMutations(t *testing.T) {
	// No envelope file at all, but a valid contract for the read tools.
	root := fixtureRepo(t, minContract, "")
	s := startServer(t, root)
	s.initialize()

	// Fail-open reads: these touch nothing outside the repository.
	if resp := s.callTool(1, "inspect_repo", nil); resp["result"] == nil {
		t.Fatalf("inspect_repo must work without an envelope, got %v", resp)
	}
	if resp := s.callTool(2, "read_contract", nil); resp["result"] == nil {
		t.Fatalf("read_contract must work without an envelope, got %v", resp)
	}

	// Fail-closed effects. run_checks belongs here, not above: it reads
	// the repository but spawns the contract's commands, and this
	// contract's test gate is a real argv.
	for i, name := range []string{"preview_plan", "run_checks", "acquire_scope", "release_scope", "publish_evidence", "propose_decision", "record_sources"} {
		resp := s.callTool(10+i, name, toolArgs(name))
		wantToolError(t, resp, "envelope_denied")
	}
}

func TestEnvelopeDeniedCodesAndAllowance(t *testing.T) {
	root := fixtureRepo(t, "", envelopeYAML(".project", "src"))
	s := startServer(t, root)
	s.initialize()

	// Mutations allowed by the envelope.
	if res := wantResult(t, s.callTool(1, "acquire_scope", map[string]any{"path": "src/pkg"})); res["data"] == nil {
		t.Fatalf("acquire_scope data missing: %v", res)
	}
	data := wantResult(t, s.callTool(2, "propose_decision", map[string]any{"title": "use tabs"}))["data"].(map[string]any)
	if data["recorded"] != true {
		t.Fatalf("propose_decision data = %v, want recorded:true", data)
	}
	data = wantResult(t, s.callTool(3, "record_sources", map[string]any{"sources": []any{"https://example.com/spec"}}))["data"].(map[string]any)
	if data["recorded"] != true {
		t.Fatalf("record_sources data = %v, want recorded:true", data)
	}
	if res := wantResult(t, s.callTool(4, "release_scope", map[string]any{"path": "src/pkg"})); res["data"] == nil {
		t.Fatalf("release_scope data missing: %v", res)
	}

	// Board records were appended.
	bs, err := os.ReadFile(filepath.Join(root, ".project", "state", "board.jsonl"))
	if err != nil {
		t.Fatalf("board.jsonl: %v", err)
	}
	if lines := strings.Count(strings.TrimSpace(string(bs)), "\n") + 1; lines != 4 {
		t.Fatalf("board.jsonl has %d lines, want 4 (acquire, decision, sources, release)", lines)
	}
	if !strings.Contains(string(bs), `"type":"decision"`) {
		t.Fatalf("board.jsonl missing decision record: %s", bs)
	}

	// Undeclared path still denied.
	resp := s.callTool(5, "acquire_scope", map[string]any{"path": "undeclared/x"})
	wantToolError(t, resp, "envelope_denied")

	// preview_plan on an adopted repository: already_adopted.
	writeFile(t, root, ".project/contract.yaml", minContract)
	resp = s.callTool(6, "preview_plan", nil)
	wantToolError(t, resp, "already_adopted")
}

func TestPreviewPlanProducesDrafts(t *testing.T) {
	root := fixtureRepo(t, "", envelopeYAML(".project"))
	s := startServer(t, root)
	s.initialize()

	res := wantResult(t, s.callTool(1, "preview_plan", nil))
	data := res["data"].(map[string]any)
	for _, key := range []string{"detectedProfiles", "conventions", "conflicts", "proposedChanges", "drafts"} {
		if _, ok := data[key]; !ok {
			t.Fatalf("preview_plan data missing %q: %v", key, data)
		}
	}
	for _, draft := range []string{".project/contract.yaml.draft", ".project/profiles.lock.draft"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(draft))); err != nil {
			t.Errorf("preview_plan did not write %s: %v", draft, err)
		}
	}
	// Committed contract must not have been created.
	if _, err := os.Stat(filepath.Join(root, ".project", "contract.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview_plan wrote a committed contract: %v", err)
	}
}

// discoverableCheckRepo is a repository whose only interesting property is
// that discovery finds a check command in it: a root Makefile with a test
// target becomes ExistingChecks{"test": "make test"}, which adopt.Preview
// spawns to record its baseline.
func discoverableCheckRepo(t *testing.T, envelopeDoc string) string {
	t.Helper()
	root := fixtureRepo(t, "", envelopeDoc)
	writeFile(t, root, "Makefile", "test:\n\t@echo baseline\n")
	return root
}

// preview_plan runs every discovered check command once to record a
// baseline. Before this was authorized, an envelope granting writes under
// .project and no exec at all still let an agent make pika spawn whatever
// commands happened to be lying in the repository — the same inverted
// gradient run_checks had.
func TestPreviewPlanDeniedWithoutExecGrant(t *testing.T) {
	root := discoverableCheckRepo(t, envelopeYAML(".project"))
	s := startServer(t, root)
	s.initialize()

	resp := s.callTool(1, "preview_plan", nil)
	errObj := wantToolError(t, resp, "envelope_denied")
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "make test") {
		t.Errorf("denial message = %q, want it to name the denied command", msg)
	}
	// The remediation has to name the flag, not just the command: an
	// agent told to "run pika authorize" with no way to express the
	// grant is an agent in a loop.
	if !strings.Contains(msg, `pika authorize --exec "make test"`) {
		t.Errorf("denial message = %q, want the exact invocation that grants it", msg)
	}
	// A denial must cost the repository nothing: no draft, no baseline.
	for _, draft := range []string{".project/contract.yaml.draft", ".project/profiles.lock.draft"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(draft))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("denied preview_plan wrote %s: %v", draft, err)
		}
	}
}

// The mirror image: granting exec for exactly the discovered command lets
// the preview run it, so the grant an operator is told to make is the one
// the tool asks for.
func TestPreviewPlanAllowedWithExecGrant(t *testing.T) {
	doc := "schema: 1\nallow:\n  fs_write: [.project]\n  exec: [\"make test\"]\n"
	root := discoverableCheckRepo(t, doc)
	s := startServer(t, root)
	s.initialize()

	res := wantResult(t, s.callTool(1, "preview_plan", nil))
	data := res["data"].(map[string]any)
	baseline, ok := data["baselineChecks"].([]any)
	if !ok || len(baseline) != 1 {
		t.Fatalf("baselineChecks = %v, want the one discovered command", data["baselineChecks"])
	}
	if got := baseline[0].(map[string]any)["command"]; got != "make test" {
		t.Errorf("baseline command = %v, want %q", got, "make test")
	}
	for _, draft := range []string{".project/contract.yaml.draft", ".project/profiles.lock.draft"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(draft))); err != nil {
			t.Errorf("preview_plan did not write %s: %v", draft, err)
		}
	}
}

// The whole loop, through the real generator: an unadopted repository
// with a discovered check command, an envelope produced by
// `pika authorize --scope project --exec "make test"`, and a preview_plan
// that runs. This is the assertion that authorize can express the grant
// preview_plan demands — the gap that made the canonical
// envelope_denied remediation unusable before a contract exists.
func TestPreviewPlanAllowedWithGeneratedExecGrant(t *testing.T) {
	root := discoverableCheckRepo(t, "")
	writeGeneratedEnvelope(t, root, authorize.ScopeProject, "make test")
	s := startServer(t, root)
	s.initialize()

	res := wantResult(t, s.callTool(1, "preview_plan", nil))
	data := res["data"].(map[string]any)
	baseline, ok := data["baselineChecks"].([]any)
	if !ok || len(baseline) != 1 {
		t.Fatalf("baselineChecks = %v, want the one discovered command", data["baselineChecks"])
	}
	// Without the explicit grant the same generated envelope must still
	// deny: the scope alone never authorizes a discovered command.
	other := discoverableCheckRepo(t, "")
	writeGeneratedEnvelope(t, other, authorize.ScopeProject)
	s2 := startServer(t, other)
	s2.initialize()
	wantToolError(t, s2.callTool(1, "preview_plan", nil), "envelope_denied")
}

func TestRunChecksReport(t *testing.T) {
	contract := "schema: 1\nproject:\n  name: fixture\n  topology: single\nprofiles:\n  - core@1\ncommands:\n  test: go version\nevidence:\n  publish: sanitized\ngithub:\n  merge: squash\n"
	root := fixtureRepo(t, contract, "")
	writeGeneratedEnvelope(t, root, authorize.ScopeProject)
	s := startServer(t, root)
	s.initialize()

	res := wantResult(t, s.callTool(1, "run_checks", map[string]any{"scope": "all"}))
	rep, ok := res["data"].(map[string]any)["report"].(map[string]any)
	if !ok {
		t.Fatalf("run_checks data = %v, want a report", res["data"])
	}
	if rep["pass"] != true {
		t.Fatalf("report = %v, want pass", rep)
	}

	// Invalid scope name is invalid_params at the tool level.
	resp := s.callTool(2, "run_checks", map[string]any{"scope": "bogus"})
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected tool error for bad scope, got %v", resp)
	}
	data := errObj["data"].(map[string]any)
	if data["error"].(map[string]any)["code"] != "invalid_params" {
		t.Fatalf("bad scope code = %v, want invalid_params", data)
	}
}

// run_checks spawns the same command gates `pika check` does, and must
// spawn them in the server's repoRoot. Before --root was threaded into
// `pika mcp`, repoRoot was always the server process's own working
// directory, so an unbound cmd.Dir was harmless by construction; once
// repoRoot can differ, an unbound cmd.Dir silently verifies the wrong
// tree and reports it as the checked repository's result.
func TestRunChecksRunsGatesInRepoRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the POSIX /bin/pwd")
	}
	// /bin/pwd is the physical-path binary, not a shell builtin: it
	// reports the process's real working directory rather than an
	// inherited $PWD. Contract commands are split on whitespace and
	// exec'd with no shell, so a bare argv is what fits here.
	contract := "schema: 1\nproject:\n  name: fixture\n  topology: single\nprofiles:\n  - core@1\ncommands:\n  test: /bin/pwd\nevidence:\n  publish: sanitized\ngithub:\n  merge: squash\n"
	root := fixtureRepo(t, contract, "")
	writeGeneratedEnvelope(t, root, authorize.ScopeProject)
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	processDir, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatal(err)
	}
	if want == processDir {
		t.Fatalf("fixture root %q equals the test process directory; the test proves nothing", want)
	}

	s := startServer(t, root)
	s.initialize()
	res := wantResult(t, s.callTool(1, "run_checks", map[string]any{"scope": "all"}))
	rep, ok := res["data"].(map[string]any)["report"].(map[string]any)
	if !ok {
		t.Fatalf("run_checks data = %v, want a report", res["data"])
	}
	gates, ok := rep["gates"].([]any)
	if !ok {
		t.Fatalf("report gates = %v, want a list", rep["gates"])
	}
	var got string
	var found bool
	for _, raw := range gates {
		g, ok := raw.(map[string]any)
		if !ok || g["id"] != "test" {
			continue
		}
		found = true
		if g["status"] != "pass" {
			t.Fatalf("test gate = %v, want pass", g)
		}
		got = strings.TrimSpace(fmt.Sprint(g["outputTail"]))
	}
	if !found {
		t.Fatalf("no test gate in report %v", rep)
	}
	// The gate reports its directory as the kernel names it; resolve
	// both sides so a symlinked temp dir is not mistaken for a miss.
	resolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("gate reported directory %q: %v", got, err)
	}
	if resolved != want {
		t.Fatalf("gate ran in %q, want the server repoRoot %q (process dir is %q)", resolved, want, processDir)
	}
}

// run_checks spawns contract-declared subprocesses. Before M1.5 it did so
// with no exec authorization at all, while propose_decision needed
// permission to append a log line — the security gradient was inverted.
func TestRunChecksDeniedWithoutExecGrant(t *testing.T) {
	// minContract's test gate is a real argv ("go version"), so a gate
	// really is spawned here; the envelope grants writes and nothing else.
	root := fixtureRepo(t, minContract, envelopeYAML(".project"))
	s := startServer(t, root)
	s.initialize()

	resp := s.callTool(1, "run_checks", map[string]any{"scope": "all"})
	wantToolError(t, resp, "envelope_denied")
}

// The generated envelope must authorize exactly the gates the same
// contract will run: if authorize and enforcement disagree on the shape of
// an exec target, `pika authorize` produces a file that denies its own
// repository's checks.
func TestRunChecksAllowedWithGeneratedEnvelope(t *testing.T) {
	root := fixtureRepo(t, minContract, "")
	writeGeneratedEnvelope(t, root, authorize.ScopeProject)
	s := startServer(t, root)
	s.initialize()

	res := wantResult(t, s.callTool(1, "run_checks", map[string]any{"scope": "all"}))
	rep, ok := res["data"].(map[string]any)["report"].(map[string]any)
	if !ok {
		t.Fatalf("run_checks data = %v, want a report", res["data"])
	}
	if rep["pass"] != true {
		t.Fatalf("report = %v, want pass", rep)
	}
}

// The read scope authorizing no checks is a product decision, not an
// accident of the fixture: it grants no exec entries at all, so the real
// generated artifact — not a hand-written stand-in — must deny a
// run_checks that spawns a gate.
func TestRunChecksDeniedWithGeneratedReadScopeEnvelope(t *testing.T) {
	// minContract's test gate is a real argv, so a gate really is
	// spawned and the exec question really is asked.
	root := fixtureRepo(t, minContract, "")
	writeGeneratedEnvelope(t, root, authorize.ScopeRead)
	s := startServer(t, root)
	s.initialize()

	resp := s.callTool(1, "run_checks", map[string]any{"scope": "all"})
	wantToolError(t, resp, "envelope_denied")
}

// A gate with no argv is the in-process contract gate or a recorded
// discovery skip: it spawns nothing, so it must not need an exec grant.
// Otherwise a repository whose profile is discovery-only could never run
// its own checks over MCP.
func TestRunChecksNeedsNoExecGrantForInProcessGates(t *testing.T) {
	// No commands block at all: only the in-process contract gate and
	// the profile's discovery sentinels survive into the gate list.
	contract := "schema: 1\nproject:\n  name: fixture\n  topology: single\nprofiles:\n  - core@1\nevidence:\n  publish: sanitized\ngithub:\n  merge: squash\nextensions: {}\n"
	root := fixtureRepo(t, contract, envelopeYAML(".project"))
	s := startServer(t, root)
	s.initialize()

	res := wantResult(t, s.callTool(1, "run_checks", map[string]any{"scope": "all"}))
	if res["data"].(map[string]any)["report"] == nil {
		t.Fatalf("run_checks without any spawning gate must not need an exec grant: %v", res)
	}
}

func TestReadContractIncludesResolvedProfiles(t *testing.T) {
	contract := "schema: 1\nproject:\n  name: fixture\n  topology: single\nprofiles:\n  - core@1\nevidence:\n  publish: sanitized\ngithub:\n  merge: squash\nextensions: {}\n"
	root := fixtureRepo(t, contract, "")
	s := startServer(t, root)
	s.initialize()

	res := wantResult(t, s.callTool(1, "read_contract", nil))
	data := res["data"].(map[string]any)
	c, ok := data["contract"].(map[string]any)
	if !ok || c["project"].(map[string]any)["name"] != "fixture" {
		t.Fatalf("read_contract contract = %v", data)
	}
	pr, ok := data["profiles"].(map[string]any)
	if !ok {
		t.Fatalf("read_contract missing resolved profiles: %v", data)
	}
	layers, ok := pr["layers"].([]any)
	if !ok || len(layers) != 1 {
		t.Fatalf("resolved layers = %v, want 1 core layer", pr["layers"])
	}
}

func TestReadContractRejectsPathTraversal(t *testing.T) {
	contract := "schema: 1\nproject:\n  name: fixture\n  topology: single\nprofiles:\n  - core@1\nevidence:\n  publish: sanitized\ngithub:\n  merge: squash\nextensions: {}\n"
	root := fixtureRepo(t, contract, "")
	// A secret outside any contract location: even if the tool read it, no
	// content may come back through a path argument.
	writeFile(t, root, ".ssh/id_rsa", "SECRET-KEY-MATERIAL")
	s := startServer(t, root)
	s.initialize()

	for _, path := range []string{"../../.ssh/id_rsa", ".contracts/../../etc/passwd", "/etc/passwd"} {
		resp := s.callTool(1, "read_contract", map[string]any{"path": path})
		errObj := wantToolError(t, resp, "invalid_params")
		if msg, _ := errObj["message"].(string); strings.Contains(msg, "SECRET-KEY-MATERIAL") {
			t.Fatalf("path %q leaked file contents", path)
		}
	}
}

func TestReadContractMissingContractCode(t *testing.T) {
	root := fixtureRepo(t, "", "")
	s := startServer(t, root)
	s.initialize()

	resp := s.callTool(1, "read_contract", nil)
	wantToolError(t, resp, "contract_invalid")
}

func TestStableProtocolErrorCodes(t *testing.T) {
	root := fixtureRepo(t, "", "")
	s := startServer(t, root)
	s.initialize()

	// Unknown method → -32601.
	resp := s.request(1, "bogus/method", nil)
	wantRPCError(t, resp, -32601)

	// Bad params (missing tool name) → -32602 + invalid_params.
	resp = s.request(2, "tools/call", map[string]any{})
	errObj := wantRPCError(t, resp, -32602)
	data := errObj["data"].(map[string]any)
	if data["error"].(map[string]any)["code"] != "invalid_params" {
		t.Fatalf("data.error.code = %v, want invalid_params", data)
	}

	// Unknown tool → -32602 + invalid_params.
	resp = s.callTool(3, "nonexistent_tool", nil)
	errObj = wantRPCError(t, resp, -32602)
	data = errObj["data"].(map[string]any)
	if data["error"].(map[string]any)["code"] != "invalid_params" {
		t.Fatalf("unknown tool code = %v, want invalid_params", data)
	}

	// Malformed line between valid lines must not kill the session.
	if _, err := s.inW.WriteString("{not json\n"); err != nil {
		t.Fatal(err)
	}
	resp = s.recv()
	wantRPCError(t, resp, -32700)
	resp = s.request(4, "ping", nil)
	if resp["result"] == nil {
		t.Fatalf("session must survive a malformed line, got %v", resp)
	}

	// tools/call apply_plan is listed but unavailable in M1. The code is
	// "unavailable", never "internal": an agent must be able to tell a
	// permanent absence from a transient kernel failure without reading
	// the message.
	resp = s.callTool(5, "apply_plan", nil)
	wantToolError(t, resp, "unavailable")
}

func TestApplyPlanNotExposedAsExecutable(t *testing.T) {
	root := fixtureRepo(t, "", "")
	s := startServer(t, root)
	s.initialize()

	resp := s.request(1, "tools/list", nil)
	res := resp["result"].(map[string]any)
	for _, tl := range res["tools"].([]any) {
		tool := tl.(map[string]any)
		if tool["name"] == "apply_plan" {
			desc, _ := tool["description"].(string)
			if !strings.Contains(desc, "unavailable") {
				t.Fatalf("apply_plan description must mark it unavailable: %q", desc)
			}
		}
	}
	resp = s.callTool(2, "apply_plan", nil)
	errObj := wantToolError(t, resp, "unavailable")
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "apply_plan") {
		t.Fatalf("apply_plan error should name the tool: %v", errObj["message"])
	}
	if code, _ := errObj["code"].(string); code == "internal" {
		t.Errorf("apply_plan reports internal, indistinguishable from a real kernel failure: %v", errObj)
	}
}

func TestAcquireScopeBadPath(t *testing.T) {
	root := fixtureRepo(t, "", envelopeYAML(".project"))
	s := startServer(t, root)
	s.initialize()

	resp := s.callTool(1, "acquire_scope", map[string]any{"path": "../outside"})
	wantToolError(t, resp, "invalid_params")
}

func TestCleanShutdownOnEOF(t *testing.T) {
	root := fixtureRepo(t, "", "")
	s := startServer(t, root)
	s.initialize()
	// Closing stdin (session cleanup) must make Serve return nil.
}

// TestBoardAppendsAreRedacted pins that the state board is redacted at
// the instant it is written. Every string on it came from an agent, and
// an agent pastes what it was looking at — a failing command line, an
// environment dump. board.jsonl is append-only local state, so a secret
// that lands there stays there.
func TestBoardAppendsAreRedacted(t *testing.T) {
	const (
		oauthKey  = "sk-ant-api03-abcdefghij0123456789ABCDE"
		githubPAT = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
		pemHeader = "-----BEGIN RSA PRIVATE KEY-----"
	)
	root := fixtureRepo(t, "", envelopeYAML(".project", "src"))
	s := startServer(t, root)
	s.initialize()

	wantResult(t, s.callTool(1, "propose_decision", map[string]any{
		"title":     "rotate the deploy key " + githubPAT,
		"rationale": "the old one leaked:\n" + pemHeader,
	}))
	wantResult(t, s.callTool(2, "record_sources", map[string]any{
		"sources": []any{"vault entry " + oauthKey, "docs/keys.md"},
	}))
	wantResult(t, s.callTool(3, "acquire_scope", map[string]any{"path": "src"}))

	bs, err := os.ReadFile(filepath.Join(root, ".project", "state", "board.jsonl"))
	if err != nil {
		t.Fatalf("board.jsonl: %v", err)
	}
	board := string(bs)
	for _, secret := range []string{oauthKey, githubPAT, pemHeader} {
		if strings.Contains(board, secret) {
			t.Errorf("board.jsonl still carries %q:\n%s", secret, board)
		}
	}
	// Redacted, not gutted: the board is what a later agent reads to
	// learn what was decided, so the prose around the placeholder has to
	// survive.
	for _, keep := range []string{"rotate the deploy key ", "docs/keys.md", `"path":"src"`} {
		if !strings.Contains(board, keep) {
			t.Errorf("board.jsonl lost %q:\n%s", keep, board)
		}
	}
	for _, kind := range []string{"<redacted:oauth>", "<redacted:github-token>", "<redacted:pem-header>"} {
		if !strings.Contains(board, kind) {
			t.Errorf("board.jsonl is missing %s:\n%s", kind, board)
		}
	}
}

// toolArgs returns minimal valid arguments per tool for the fail-closed test.
func toolArgs(name string) map[string]any {
	switch name {
	case "acquire_scope", "release_scope":
		return map[string]any{"path": "src"}
	case "propose_decision":
		return map[string]any{"title": "t"}
	case "record_sources":
		return map[string]any{"sources": []string{"a"}}
	case "publish_evidence":
		return evidenceArgs()
	default:
		return map[string]any{}
	}
}

func mustJSON(v any) string {
	bs, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(bs)
}
