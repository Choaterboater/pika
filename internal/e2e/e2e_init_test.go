// Package e2e wires the whole M1 kernel end to end through the real
// binary: `pika init` into a temp directory, `pika check
// --all` across the five language profiles, the adoption roundtrip,
// local-vs-CI report parity, and a full MCP session over real stdio
// pipes (spec §12.6, §13, §8.2).
//
// The pika binary is built once per test run with CGO_ENABLED=0.
// Language profiles whose verification slots carry real commands skip
// with a clear reason when their toolchain is absent: CI matrices vary
// and a missing toolchain is an honest skip, never a failure.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/adopt"
	"github.com/Choaterboater/pika/internal/contract"
)

// binPath is the pika binary built once by TestMain.
var binPath string

// fakeCodexDir is the directory holding the fake agent binary, built
// once by TestMain under the name pika spawns. The work-lifecycle tests
// put it at the front of the child's PATH; see testdata/fakecodex.
var fakeCodexDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pctl-e2e-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: temp dir:", err)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, "pika")
	build := exec.Command("go", "build", "-o", binPath, "../../cmd/pika")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build pika: %v\n%s", err, out)
		os.Exit(1)
	}
	// The agent boundary is a binary looked up on PATH, so faking it is
	// building one. It lives beside the pika binary and dies with it.
	fakeCodexDir = filepath.Join(dir, "agent")
	if err := os.MkdirAll(fakeCodexDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: agent dir:", err)
		os.Exit(1)
	}
	agent := exec.Command("go", "build", "-o", filepath.Join(fakeCodexDir, exeName("codex")), "./testdata/fakecodex")
	agent.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := agent.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build fake codex: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// exeName is what an executable is called on this platform. PATH lookup
// on Windows will not find a file without the suffix.
func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// languages lists the V1 profiles in spec §5.4 order.
var languages = []string{"go", "typescript", "python", "swift", "rust"}

// toolchainAbsent reports why the language's real check gates cannot run
// on this machine, or "" when every contract command is runnable. go is
// always present because the tests run under `go test`.
func toolchainAbsent(lang string) string {
	switch lang {
	case "typescript":
		// No typescript@1 hint is autofillable: every one delegates to a
		// package.json script or an npx download the scaffold does not
		// provide, so the contract names no command and all five slots
		// skip. Nothing to install, and nothing that can fail.
		return ""
	case "go":
		if _, err := exec.LookPath("go"); err != nil {
			return "go not in PATH"
		}
	case "python":
		// The contract's test gate command is the probe: a `python`
		// interpreter without pytest is as unusable as no python at all.
		if err := exec.Command("python", "-m", "pytest", "--version").Run(); err != nil {
			return "the contract test-gate command `python -m pytest` is not runnable"
		}
	case "rust":
		if _, err := exec.LookPath("cargo"); err != nil {
			return "cargo not in PATH"
		}
	case "swift":
		if _, err := exec.LookPath("swift"); err != nil {
			return "swift not in PATH"
		}
	}
	return ""
}

// envelope mirrors the cliout envelope every --json payload carries. It
// is declared here rather than imported so these tests assert the wire
// shape an outside consumer actually sees, not the producer's own type.
type envelope struct {
	Schema  int             `json:"schema"`
	Command string          `json:"command"`
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// unwrap asserts the discriminators every --json payload shares and
// returns the command's own result. A consumer that cannot read schema,
// command, and ok before knowing the shape is exactly what cliout
// exists to prevent.
func unwrap(t *testing.T, out, command string) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("%s --json did not print a cliout envelope: %v\n%s", command, err, out)
	}
	if env.Schema != 1 {
		t.Errorf("schema = %d, want 1:\n%s", env.Schema, out)
	}
	if env.Command != command {
		t.Errorf("command = %q, want %q:\n%s", env.Command, command, out)
	}
	return env
}

// checkReport mirrors the JSON check report (verify.Report) the binary
// nests under the envelope's result for `check --json`.
type checkReport struct {
	Gates []struct {
		ID     string   `json:"id"`
		Cmd    []string `json:"cmd"`
		Exit   int      `json:"exit"`
		Status string   `json:"status"`
		Reason string   `json:"reason"`
	} `json:"gates"`
	Summary struct {
		Pass int `json:"pass"`
		Fail int `json:"fail"`
		Skip int `json:"skip"`
	} `json:"summary"`
	Warnings    []string `json:"warnings"`
	DurationMs  int64    `json:"durationMs"`
	Pass        bool     `json:"pass"`
	Regressions []struct {
		Gate   string `json:"gate"`
		Detail string `json:"detail"`
	} `json:"regressions"`
}

// runCLI runs the built binary in dir and asserts the exit code.
func runCLI(t *testing.T, dir string, wantExit int, args ...string) string {
	t.Helper()
	return runCLIEnv(t, dir, nil, wantExit, args...)
}

// runCLIEnv is runCLI with extra environment entries appended to the
// test process's own. Only the work lifecycle needs it: pika spawns its
// agent as a binary looked up on PATH, so faking that boundary means
// changing the child's PATH.
func runCLIEnv(t *testing.T, dir string, env []string, wantExit int, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("pika %v: %v", args, err)
	}
	if code != wantExit {
		t.Fatalf("pika %v in %s: exit %d, want %d\nstdout: %s\nstderr: %s", args, dir, code, wantExit, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// scaffoldRepo runs `pika init --profile <lang>` into a fresh temp
// directory and returns its path.
func scaffoldRepo(t *testing.T, lang string) string {
	t.Helper()
	dir := t.TempDir()
	runCLI(t, dir, 0, "init", "--profile", lang)
	return dir
}

// parseCheckReport unwraps the envelope printed by `check --json` and
// returns the report nested under its result. The envelope's ok must
// agree with the report's own pass: ok is the boolean the exit code is
// derived from, so a payload where the two disagree would send an agent
// down the wrong branch.
func parseCheckReport(t *testing.T, out string) *checkReport {
	t.Helper()
	env := unwrap(t, out, "check")
	var rep checkReport
	if err := json.Unmarshal(env.Result, &rep); err != nil {
		t.Fatalf("parse check result: %v\noutput: %s", err, out)
	}
	if env.OK != rep.Pass {
		t.Fatalf("envelope ok = %v but report pass = %v:\n%s", env.OK, rep.Pass, out)
	}
	return &rep
}

// wantGateIDs is the full gate set of an initialized single-profile
// repository: the contract gate plus the five verification slots.
var wantGateIDs = []string{"contract", "format", "lint", "typecheck", "test", "smoke"}

// TestE2EInitCheckAllLanguages runs the full loop per language: init into
// a temp dir, contract.Load, `check --all --json` exit 0 with no failed
// gate, then the adopt-style roundtrip — Preview on the initialized repo
// must refuse with the already-adopted error and write nothing.
func TestE2EInitCheckAllLanguages(t *testing.T) {
	for _, lang := range languages {
		t.Run(lang, func(t *testing.T) {
			if reason := toolchainAbsent(lang); reason != "" {
				t.Skipf("toolchain absent: %s", reason)
			}
			dir := scaffoldRepo(t, lang)

			// The scaffolded contract loads through the real loader.
			c, err := contract.Load(filepath.Join(dir, ".project", "contract.yaml"))
			if err != nil {
				t.Fatalf("contract.Load on scaffolded contract: %v", err)
			}
			if c.Schema != 1 {
				t.Fatalf("contract schema = %d, want 1", c.Schema)
			}

			// check --all: exit 0, every gate green or honestly skipped.
			out := runCLI(t, dir, 0, "check", "--all", "--json")
			rep := parseCheckReport(t, out)
			if !rep.Pass {
				t.Fatalf("check --all did not pass:\n%s", out)
			}
			if len(rep.Gates) != len(wantGateIDs) {
				t.Fatalf("gate count = %d, want %d:\n%s", len(rep.Gates), len(wantGateIDs), out)
			}
			for i, gate := range rep.Gates {
				if gate.ID != wantGateIDs[i] {
					t.Fatalf("gate %d = %q, want %q:\n%s", i, gate.ID, wantGateIDs[i], out)
				}
				if gate.Status == "fail" {
					t.Fatalf("gate %s failed:\n%s", gate.ID, out)
				}
			}
			if rep.Gates[0].Status != "pass" {
				t.Fatalf("contract gate status = %q, want pass:\n%s", rep.Gates[0].Status, out)
			}
			// The scaffold must not trip its own kebab gate: every
			// ecosystem-conventional top-level name is exempt.
			for _, w := range rep.Warnings {
				if strings.Contains(w, "naming-kebab-case") {
					t.Errorf("check emitted a kebab-case warning on the scaffold: %s", w)
				}
			}

			// State lives under .project/state/ (spec §14.2) and must be
			// gitignored by the scaffold.
			gi, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
			if err != nil {
				t.Fatalf("scaffold .gitignore: %v", err)
			}
			if !strings.Contains(string(gi), ".project/state/") {
				t.Errorf(".gitignore = %q, want it to ignore .project/state/", gi)
			}

			// Adopt-style roundtrip: a committed contract makes the repo
			// already adopted. Preview must refuse read-only — no drafts.
			inventory, err := adopt.Preview(dir)
			if err == nil {
				t.Fatal("adopt.Preview on initialized repo: want already-adopted error, got nil")
			}
			if inventory != nil {
				t.Fatal("adopt.Preview on initialized repo returned a report alongside an error")
			}
			if !strings.Contains(err.Error(), "already adopted") {
				t.Fatalf("adopt.Preview error = %v, want already-adopted", err)
			}
			for _, draft := range []string{".project/contract.yaml.draft", ".project/profiles.lock.draft"} {
				if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(draft))); !os.IsNotExist(statErr) {
					t.Errorf("adopt refusal wrote %s (stat err %v)", draft, statErr)
				}
			}
		})
	}
}

// TestE2EParityLocalVsCI runs `check --all` and `check --ci` on the same
// fixture and asserts identical gate sets and pass/fail status: CI is
// --all plus no interactive prompts, so the reports may differ only in
// timing fields and captured gate output.
func TestE2EParityLocalVsCI(t *testing.T) {
	dir := scaffoldRepo(t, "go") // go runs everywhere the suite runs

	local := parseCheckReport(t, runCLI(t, dir, 0, "check", "--all", "--json"))
	ci := parseCheckReport(t, runCLI(t, dir, 0, "check", "--ci", "--json"))

	type gateKey struct {
		id     string
		status string
		exit   int
		cmd    string
	}
	key := func(gates []struct {
		ID     string   `json:"id"`
		Cmd    []string `json:"cmd"`
		Exit   int      `json:"exit"`
		Status string   `json:"status"`
		Reason string   `json:"reason"`
	}) []gateKey {
		out := make([]gateKey, 0, len(gates))
		for _, g := range gates {
			out = append(out, gateKey{g.ID, g.Status, g.Exit, strings.Join(g.Cmd, " ")})
		}
		return out
	}
	if !slices.Equal(key(local.Gates), key(ci.Gates)) {
		t.Fatalf("gate sets differ between --all and --ci:\n--all: %+v\n--ci:  %+v", key(local.Gates), key(ci.Gates))
	}
	if local.Summary != ci.Summary {
		t.Fatalf("summaries differ: --all %+v, --ci %+v", local.Summary, ci.Summary)
	}
	if local.Pass != ci.Pass {
		t.Fatalf("pass differ: --all %v, --ci %v", local.Pass, ci.Pass)
	}
	// --ci adds its scope banner warning ("--ci implies --all; ...") on
	// top of the gate-1 warnings; the gate set, outcomes, and gate-1
	// warnings must still be identical modulo timing and gate output.
	const ciScopeWarning = "--ci implies --all; no interactive prompts are possible in check"
	ciWarnings := slices.DeleteFunc(slices.Clone(ci.Warnings), func(w string) bool {
		return w == ciScopeWarning
	})
	if !slices.Equal(local.Warnings, ciWarnings) {
		t.Fatalf("warnings differ: --all %v, --ci %v", local.Warnings, ci.Warnings)
	}
	if !local.Pass {
		t.Fatalf("fixture should pass: --all report %+v", local)
	}
}

// --- MCP session over real stdio pipes ---

// mcpSession drives one spawned `pika mcp` process through its
// line-delimited JSON-RPC stdio protocol. Requests and responses go over
// real os.Pipes created by exec; the server answers one line per request
// (single-flight), so the client reads synchronously.
type mcpSession struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr strings.Builder
	nextID int
}

// startMCP spawns `pika mcp` with repoRoot as its working
// directory. Cleanup closes stdin (clean EOF shutdown) and reaps.
func startMCP(t *testing.T, repoRoot string) *mcpSession {
	t.Helper()
	cmd := exec.Command(binPath, "mcp")
	cmd.Dir = repoRoot
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	s := &mcpSession{t: t, cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	cmd.Stderr = &s.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start pika mcp: %v", err)
	}
	t.Cleanup(func() {
		stdin.Close()
		_ = cmd.Wait() // no-op after the test's own Wait; reaps otherwise
	})
	return s
}

// request sends one JSON-RPC request and returns the decoded response.
func (s *mcpSession) request(method string, params any) map[string]any {
	s.t.Helper()
	s.nextID++
	req := map[string]any{"jsonrpc": "2.0", "id": s.nextID, "method": method}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		s.t.Fatalf("marshal request %s: %v", method, err)
	}
	if _, err := fmt.Fprintf(s.stdin, "%s\n", b); err != nil {
		s.t.Fatalf("write request %s: %v\nstderr: %s", method, err, s.stderr.String())
	}
	line, err := s.stdout.ReadString('\n')
	if err != nil {
		s.t.Fatalf("read response for %s: %v\nstderr: %s", method, err, s.stderr.String())
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		s.t.Fatalf("decode response for %s: %v\nline: %s", method, err, line)
	}
	if resp["id"] != float64(s.nextID) {
		s.t.Fatalf("response id = %v, want %d (method %s)", resp["id"], s.nextID, method)
	}
	return resp
}

// call sends tools/call for the named tool. It fails the test on any
// protocol-level error and returns (result, toolError) — exactly one of
// the two is non-nil.
func (s *mcpSession) call(name string, args any) (map[string]any, map[string]any) {
	s.t.Helper()
	resp := s.request("tools/call", map[string]any{"name": name, "arguments": args})
	if errObj, ok := resp["error"].(map[string]any); ok {
		return nil, errObj
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		s.t.Fatalf("tools/call %s: no result and no error:\n%v", name, resp)
	}
	if ok, _ := result["ok"].(bool); !ok {
		s.t.Fatalf("tools/call %s: result.ok is not true:\n%v", name, result)
	}
	return result, nil
}

// toolErrorCode extracts the stable error code from a tool-failure
// response ({error:{data:{error:{code}}}}).
func toolErrorCode(t *testing.T, errObj map[string]any) string {
	t.Helper()
	data, ok := errObj["data"].(map[string]any)
	if !ok {
		t.Fatalf("tool error carries no data envelope: %v", errObj)
	}
	terr, ok := data["error"].(map[string]any)
	if !ok {
		t.Fatalf("tool error data carries no error envelope: %v", errObj)
	}
	code, _ := terr["code"].(string)
	return code
}

// evidenceReceipt returns a schema-valid receipt body for publish_evidence
// (it must pass evidence.Build so the exercise reaches the envelope gate).
func evidenceReceipt() map[string]any {
	return map[string]any{
		"work_id":          "20260828-e2e-envelope-a1b2",
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
			map[string]any{"path": "README.md", "ownership": "kernel"},
		},
		"commands": []any{
			map[string]any{"cmd": "go test ./...", "exit": 0, "duration_ms": 1200, "output": "ok"},
		},
		"surface_scenario":  map[string]any{"ran": true, "description": "ran pika check --all locally"},
		"baseline_failures": []any{},
		"regressions":       []any{},
		"review": []any{
			map[string]any{"agent": "reviewer", "finding": "none", "disposition": "closed"},
		},
		"docs_impact": []any{},
		"completion":  map[string]any{"complete": true, "reason": "all gates green"},
	}
}

// TestE2EMCPSession runs one full agent session against the real binary
// over real stdio pipes on an initialized go repository: initialize,
// tools/list, the envelope-denied matrix for every tool — reads included
// since M3 — while no envelope exists, the unavailable apply_plan, then
// `pika authorize` followed by the kernel read tools and a green
// run_checks, and a clean EOF shutdown with exit 0. A second session on a
// fresh non-adopted repository proves preview_plan is denied there too.
func TestE2EMCPSession(t *testing.T) {
	if reason := toolchainAbsent("go"); reason != "" {
		t.Skipf("toolchain absent: %s", reason) // run_checks executes the go test gate
	}
	repo := scaffoldRepo(t, "go")
	s := startMCP(t, repo)

	// initialize handshake
	resp := s.request("initialize", map[string]any{"protocolVersion": "2024-11-05"})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize: no result:\n%v", resp)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion = %v, want 2024-11-05", result["protocolVersion"])
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok || info["name"] != "pika" {
		t.Fatalf("serverInfo = %v, want name pika", result["serverInfo"])
	}

	// tools/list: the closed M1 registry.
	resp = s.request("tools/list", nil)
	result = resp["result"].(map[string]any)
	list, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list: no tools array:\n%v", resp)
	}
	got := map[string]bool{}
	for _, entry := range list {
		name, _ := entry.(map[string]any)["name"].(string)
		got[name] = true
	}
	for _, name := range []string{
		"inspect_repo", "read_contract", "preview_plan", "run_checks",
		"acquire_scope", "release_scope", "publish_evidence",
		"propose_decision", "record_sources", "apply_plan",
	} {
		if !got[name] {
			t.Errorf("tools/list missing %q", name)
		}
	}

	// The envelope-denied matrix: every tool is fail-closed while
	// .project/state/envelope.yaml is absent, and the denial runs before
	// any repository inspection or write. Reads are in this matrix as of
	// M3: enumerating a repository is a capability an agent is granted,
	// not a neutral act it may perform unauthorized.
	for _, name := range []string{"inspect_repo", "read_contract"} {
		_, errObj := s.call(name, map[string]any{})
		if errObj == nil {
			t.Fatalf("%s without envelope: want envelope_denied error", name)
		}
		if code := toolErrorCode(t, errObj); code != "envelope_denied" {
			t.Fatalf("%s error code = %q, want envelope_denied", name, code)
		}
	}

	// The rest of the matrix: the tools with a filesystem or process
	// effect, denied before anything is discovered or written.
	_, errObj := s.call("preview_plan", map[string]any{})
	if errObj == nil {
		t.Fatal("preview_plan without envelope: want envelope_denied error")
	}
	if code := toolErrorCode(t, errObj); code != "envelope_denied" {
		t.Fatalf("preview_plan error code = %q, want envelope_denied", code)
	}
	_, errObj = s.call("publish_evidence", map[string]any{"receipt": evidenceReceipt()})
	if errObj == nil {
		t.Fatal("publish_evidence without envelope: want envelope_denied error")
	}
	if code := toolErrorCode(t, errObj); code != "envelope_denied" {
		t.Fatalf("publish_evidence error code = %q, want envelope_denied", code)
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".project", "evidence")); !os.IsNotExist(statErr) {
		t.Errorf("denied publish wrote evidence (stat err %v)", statErr)
	}
	_, errObj = s.call("acquire_scope", map[string]any{"path": "README.md"})
	if errObj == nil {
		t.Fatal("acquire_scope without envelope: want envelope_denied error")
	}
	if code := toolErrorCode(t, errObj); code != "envelope_denied" {
		t.Fatalf("acquire_scope error code = %q, want envelope_denied", code)
	}
	// run_checks needs more than the read grant every tool now needs: it
	// spawns the contract's gate commands, so it needs an exec grant
	// exactly as a write needs an fs_write grant.
	_, errObj = s.call("run_checks", map[string]any{"scope": "all"})
	if errObj == nil {
		t.Fatal("run_checks without envelope: want envelope_denied error")
	}
	if code := toolErrorCode(t, errObj); code != "envelope_denied" {
		t.Fatalf("run_checks error code = %q, want envelope_denied", code)
	}
	// apply_plan is listed but never executable in this build. Its code
	// is "unavailable", never "internal": permanent absence and a
	// transient kernel failure must be distinguishable without matching
	// on the message.
	_, errObj = s.call("apply_plan", map[string]any{})
	if errObj == nil {
		t.Fatal("apply_plan: want an unavailable error")
	}
	if code := toolErrorCode(t, errObj); code != "unavailable" {
		t.Fatalf("apply_plan error code = %q, want unavailable", code)
	}

	// `pika authorize` generates the envelope an agent session runs
	// under. Using the real command here is the end-to-end proof that
	// what authorize grants is what the MCP server enforces: the
	// already_adopted check below needs the fs_write grants, and the
	// run_checks call after it needs the exec grants.
	runCLI(t, repo, 0, "authorize", "--scope", "project")
	_, errObj = s.call("preview_plan", map[string]any{})
	if errObj == nil {
		t.Fatal("preview_plan on adopted repo with envelope: want already_adopted error")
	}
	if code := toolErrorCode(t, errObj); code != "already_adopted" {
		t.Fatalf("preview_plan error code = %q, want already_adopted", code)
	}

	// The kernel read tools, run after `pika authorize` because since M3
	// that is the ordering the server enforces: with no envelope both
	// were denied above, and the operator's one command is what makes
	// them work.
	_, errObj = s.call("inspect_repo", map[string]any{})
	if errObj != nil {
		t.Fatalf("inspect_repo failed: %v", errObj)
	}

	// read_contract: the scaffolded contract plus resolved profiles.
	result, errObj = s.call("read_contract", map[string]any{})
	if errObj != nil {
		t.Fatalf("read_contract failed: %v", errObj)
	}
	data := result["data"].(map[string]any)
	if data["contract"] == nil {
		t.Fatalf("read_contract returned no contract:\n%v", result)
	}
	profiles, ok := data["profiles"].(map[string]any)
	if !ok {
		t.Fatalf("read_contract returned no profiles:\n%v", result)
	}
	selected := fmt.Sprint(profiles["selected"])
	if !strings.Contains(selected, "go@1") || !strings.Contains(selected, "core@1") {
		t.Fatalf("resolved profiles = %v, want core@1 and go@1", profiles["selected"])
	}

	// run_checks: the same ladder as check --all, green here.
	result, errObj = s.call("run_checks", map[string]any{"scope": "all"})
	if errObj != nil {
		t.Fatalf("run_checks failed: %v", errObj)
	}
	data = result["data"].(map[string]any)
	report, ok := data["report"].(map[string]any)
	if !ok {
		t.Fatalf("run_checks returned no report:\n%v", result)
	}
	if pass, _ := report["pass"].(bool); !pass {
		t.Fatalf("run_checks report did not pass:\n%v", report)
	}
	for _, gate := range report["gates"].([]any) {
		g := gate.(map[string]any)
		if g["status"] == "fail" {
			t.Fatalf("run_checks gate %v failed:\n%v", g["id"], report)
		}
	}

	// Clean EOF shutdown: close stdin, the server exits 0.
	s.stdin.Close()
	if err := s.cmd.Wait(); err != nil {
		t.Fatalf("mcp server did not exit cleanly on EOF: %v\nstderr: %s", err, s.stderr.String())
	}

	// Second session on a fresh non-adopted repository: preview_plan is a
	// mutating tool, so without an envelope it is denied before anything
	// is discovered or written.
	empty := t.TempDir()
	s2 := startMCP(t, empty)
	s2.request("initialize", map[string]any{"protocolVersion": "2024-11-05"})
	_, errObj = s2.call("preview_plan", map[string]any{})
	if errObj == nil {
		t.Fatal("preview_plan on non-adopted repo without envelope: want envelope_denied")
	}
	if code := toolErrorCode(t, errObj); code != "envelope_denied" {
		t.Fatalf("preview_plan error code = %q, want envelope_denied", code)
	}
	for _, draft := range []string{".project/contract.yaml.draft", ".project/profiles.lock.draft"} {
		if _, statErr := os.Stat(filepath.Join(empty, filepath.FromSlash(draft))); !os.IsNotExist(statErr) {
			t.Errorf("denied preview wrote %s (stat err %v)", draft, statErr)
		}
	}
	s2.stdin.Close()
	if err := s2.cmd.Wait(); err != nil {
		t.Fatalf("second mcp server did not exit cleanly on EOF: %v\nstderr: %s", err, s2.stderr.String())
	}
}

// TestE2EHumanCheckNeedsNoEnvelopeButMCPDoes pins the deliberate asymmetry
// between the two front doors of the same verification ladder. A human
// running `pika check` in their own shell has already authorized
// themselves by typing the command, so the CLI must keep working with no
// envelope on disk. An agent driving run_checks over MCP has not, so that
// path is fail-closed. Both directions are asserted here because an
// asymmetry nobody names is an asymmetry that rots into a bug: tighten the
// CLI and the tool becomes unusable, loosen MCP and the envelope stops
// being the boundary it exists to be.
func TestE2EHumanCheckNeedsNoEnvelopeButMCPDoes(t *testing.T) {
	if reason := toolchainAbsent("go"); reason != "" {
		t.Skipf("toolchain absent: %s", reason) // the go gates really spawn
	}
	repo := scaffoldRepo(t, "go")
	envelopeFile := filepath.Join(repo, ".project", "state", "envelope.yaml")
	if _, err := os.Stat(envelopeFile); !os.IsNotExist(err) {
		t.Fatalf("a scaffolded repository must have no envelope (stat err %v)", err)
	}

	// Direction 1: the human CLI runs the whole ladder, no envelope.
	out := runCLI(t, repo, 0, "check", "--all", "--json")
	rep := parseCheckReport(t, out)
	if !rep.Pass {
		t.Fatalf("check --all without an envelope did not pass: %s", out)
	}
	var ranGate bool
	for _, g := range rep.Gates {
		if g.ID == "test" && g.Status == "pass" {
			ranGate = true
		}
	}
	if !ranGate {
		t.Fatalf("check --all did not actually run the test gate: %s", out)
	}

	// Direction 2: the same ladder over MCP, same repository, same
	// missing envelope — denied.
	s := startMCP(t, repo)
	s.request("initialize", map[string]any{"protocolVersion": "2024-11-05"})
	_, errObj := s.call("run_checks", map[string]any{"scope": "all"})
	if errObj == nil {
		t.Fatal("run_checks over MCP without an envelope: want envelope_denied")
	}
	if code := toolErrorCode(t, errObj); code != "envelope_denied" {
		t.Fatalf("run_checks error code = %q, want envelope_denied", code)
	}
	s.stdin.Close()
	if err := s.cmd.Wait(); err != nil {
		t.Fatalf("mcp server did not exit cleanly on EOF: %v\nstderr: %s", err, s.stderr.String())
	}
}
