# pika M1.5 — Ergonomics and Authority Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the pika kernel runnable from any subdirectory, self-describing, self-diagnosing, and safely authorizable by a generated capability envelope — with zero LLM calls and zero new dependencies.

**Architecture:** Five new internal packages (`repopath`, `cliout`, `doctor`, `explain`, `authorize`, `changed`) plus a table-driven dispatcher in `cmd/pika`. `repopath` is threaded as a parameter, never a global. Existing packages keep their shapes; the only edits to them are replacing hardcoded path strings, adding two fields to naming rules, wiring `KindExec`, and deleting the `--changed` warning.

**Tech Stack:** Go 1.26, stdlib only. `github.com/goccy/go-yaml` and `github.com/santhosh-tekuri/jsonschema/v6` remain the only direct dependencies. Tests use stdlib `testing`.

**Spec:** [docs/superpowers/specs/2026-08-29-pika-m1-5-ergonomics-design.md](../specs/2026-08-29-pika-m1-5-ergonomics-design.md)

## Global Constraints

- `go.mod` MUST still declare exactly two direct dependencies at the end of this milestone. Adding an HTTP client, CLI framework, or provider SDK fails review.
- `CGO_ENABLED=0 go build ./...` MUST succeed (spec §18).
- No code path may call a model. `pika check --ci` stays provably LLM-free (design spec §16).
- Every command supports `--json` (design spec §8.1) and every `--json` payload uses the `cliout` envelope `{"schema":1,"command":…,"ok":…}`.
- Exit codes everywhere: `0` success, `1` failure, `2` usage or configuration error.
- Deny-by-default: authorization always precedes any filesystem or process effect.
- Commit after every task. Conventional commits, imperative subjects.
- Run only the tests named in each task. The full suite runs once, in Task 12.
- Windows compatibility: no new `syscall` use; path handling goes through `filepath`.

---

### Task 1: `internal/repopath` — root discovery and the path table

**Files:**
- Create: `internal/repopath/repopath.go`
- Test: `internal/repopath/repopath_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `repopath.Find(start string) (*Root, error)`, `repopath.At(dir string) (*Root, error)`, and on `*Root`: `Dir() string`, `Origin() string`, `Contract() string`, `ContractDraft() string`, `Lock() string`, `LockDraft() string`, `Exceptions() string`, `StateDir() string`, `Envelope() string`, `Board() string`, `EvidenceDir() string`, `Review() string`, `Join(parts ...string) string`. Every later task consumes these.

- [ ] **Step 1: Write the failing test**

```go
// internal/repopath/repopath_test.go
package repopath

import (
	"os"
	"path/filepath"
	"testing"
)

func mkdirAll(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	return p
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestFindWalksUpToContract(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".project", "contract.yaml"), "schema: 1\n")
	nested := mkdirAll(t, root, "internal", "deep", "deeper")

	got, err := Find(nested)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Dir() != root {
		t.Fatalf("Dir() = %q, want %q", got.Dir(), root)
	}
	if got.Origin() != OriginContract {
		t.Fatalf("Origin() = %q, want %q", got.Origin(), OriginContract)
	}
}

func TestFindPrefersContractOverDraftOverGit(t *testing.T) {
	tests := []struct {
		name       string
		seed       []string
		wantOrigin string
	}{
		{"contract", []string{".project/contract.yaml"}, OriginContract},
		{"draft", []string{".project/contract.yaml.draft"}, OriginDraft},
		{"git", []string{".git/HEAD"}, OriginGit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, rel := range tc.seed {
				writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), "x\n")
			}
			nested := mkdirAll(t, root, "a", "b")
			got, err := Find(nested)
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			if got.Dir() != root {
				t.Fatalf("Dir() = %q, want %q", got.Dir(), root)
			}
			if got.Origin() != tc.wantOrigin {
				t.Fatalf("Origin() = %q, want %q", got.Origin(), tc.wantOrigin)
			}
		})
	}
}

func TestFindContractBeatsGitAtSameLevel(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref\n")
	writeFile(t, filepath.Join(root, ".project", "contract.yaml"), "schema: 1\n")

	got, err := Find(root)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Origin() != OriginContract {
		t.Fatalf("Origin() = %q, want %q", got.Origin(), OriginContract)
	}
}

func TestFindNearestWins(t *testing.T) {
	outer := t.TempDir()
	writeFile(t, filepath.Join(outer, ".project", "contract.yaml"), "schema: 1\n")
	inner := mkdirAll(t, outer, "sub")
	writeFile(t, filepath.Join(inner, ".project", "contract.yaml"), "schema: 1\n")

	got, err := Find(mkdirAll(t, inner, "x"))
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Dir() != inner {
		t.Fatalf("Dir() = %q, want nearest %q", got.Dir(), inner)
	}
}

func TestFindFallsBackToStartDir(t *testing.T) {
	root := t.TempDir()
	nested := mkdirAll(t, root, "no", "markers")

	got, err := Find(nested)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Dir() != nested {
		t.Fatalf("Dir() = %q, want %q", got.Dir(), nested)
	}
	if got.Origin() != OriginCWD {
		t.Fatalf("Origin() = %q, want %q", got.Origin(), OriginCWD)
	}
}

func TestAtRejectsNonDirectory(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "file.txt")
	writeFile(t, f, "x\n")

	if _, err := At(f); err == nil {
		t.Fatal("At(file) = nil error, want error")
	}
	if _, err := At(filepath.Join(root, "missing")); err == nil {
		t.Fatal("At(missing) = nil error, want error")
	}
}

func TestPathAccessors(t *testing.T) {
	root := t.TempDir()
	r, err := At(root)
	if err != nil {
		t.Fatalf("At: %v", err)
	}
	cases := map[string]string{
		r.Contract():      filepath.Join(root, ".project", "contract.yaml"),
		r.ContractDraft(): filepath.Join(root, ".project", "contract.yaml.draft"),
		r.Lock():          filepath.Join(root, ".project", "profiles.lock"),
		r.LockDraft():     filepath.Join(root, ".project", "profiles.lock.draft"),
		r.Exceptions():    filepath.Join(root, ".project", "exceptions.yaml"),
		r.StateDir():      filepath.Join(root, ".project", "state"),
		r.Envelope():      filepath.Join(root, ".project", "state", "envelope.yaml"),
		r.Board():         filepath.Join(root, ".project", "state", "board.jsonl"),
		r.EvidenceDir():   filepath.Join(root, ".project", "evidence"),
		r.Review():        filepath.Join(root, "review", "adoption-review.md"),
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/repopath/`
Expected: FAIL — package does not compile, `Find`, `At`, `OriginContract` undefined.

- [ ] **Step 3: Implement**

```go
// Package repopath resolves the repository root and owns every path
// beneath .project. Before this package the root was the process working
// directory in every command and the .project path strings were
// duplicated across six packages.
package repopath

import (
	"fmt"
	"os"
	"path/filepath"
)

// Origin records which marker resolved the root, so `pika doctor` can
// explain the answer rather than assert it.
const (
	OriginContract = "contract"
	OriginDraft    = "draft"
	OriginGit      = "git"
	OriginCWD      = "cwd"
	OriginExplicit = "explicit"
)

// Root is a resolved repository root. It is immutable and safe to share.
type Root struct {
	dir    string
	origin string
}

// markers are probed in priority order at each level of the walk: an
// adopted repository beats a mid-adoption one, which beats a bare git
// checkout.
var markers = []struct {
	rel    string
	origin string
}{
	{filepath.Join(".project", "contract.yaml"), OriginContract},
	{filepath.Join(".project", "contract.yaml.draft"), OriginDraft},
	{".git", OriginGit},
}

// Find walks up from start and returns the nearest directory carrying a
// marker. When no ancestor carries one it returns start itself with
// OriginCWD: an unadopted directory is a valid, reportable state, not an
// error.
func Find(start string) (*Root, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return nil, fmt.Errorf("repopath: %w", err)
	}
	dir := abs
	for {
		for _, m := range markers {
			if _, err := os.Stat(filepath.Join(dir, m.rel)); err == nil {
				return &Root{dir: dir, origin: m.origin}, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return &Root{dir: abs, origin: OriginCWD}, nil
}

// At binds an explicit root, bypassing discovery. It is what --root uses.
func At(dir string) (*Root, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("repopath: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("repopath: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repopath: %s is not a directory", abs)
	}
	return &Root{dir: abs, origin: OriginExplicit}, nil
}

// Dir is the absolute repository root.
func (r *Root) Dir() string { return r.dir }

// Origin names the marker that resolved Dir.
func (r *Root) Origin() string { return r.origin }

// Join builds an absolute path beneath the root from slash-free parts.
func (r *Root) Join(parts ...string) string {
	return filepath.Join(append([]string{r.dir}, parts...)...)
}

// The durable spine (design spec §6). These replace the string literals
// previously duplicated in check, initcmd, apply, adopt, gate1, and
// envelope.
func (r *Root) Contract() string      { return r.Join(".project", "contract.yaml") }
func (r *Root) ContractDraft() string { return r.Contract() + ".draft" }
func (r *Root) Lock() string          { return r.Join(".project", "profiles.lock") }
func (r *Root) LockDraft() string     { return r.Lock() + ".draft" }
func (r *Root) Exceptions() string    { return r.Join(".project", "exceptions.yaml") }
func (r *Root) StateDir() string      { return r.Join(".project", "state") }
func (r *Root) Envelope() string      { return r.Join(".project", "state", "envelope.yaml") }
func (r *Root) Board() string         { return r.Join(".project", "state", "board.jsonl") }
func (r *Root) EvidenceDir() string   { return r.Join(".project", "evidence") }
func (r *Root) Review() string        { return r.Join("review", "adoption-review.md") }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/repopath/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/repopath/
git commit -m "feat: repository root discovery and centralized .project path table"
```

---

### Task 2: `internal/cliout` — one JSON envelope for every command

**Files:**
- Create: `internal/cliout/cliout.go`
- Test: `internal/cliout/cliout_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `cliout.Write(w io.Writer, command string, ok bool, result any) error`, `cliout.WriteError(w io.Writer, command, code, message string) error`, and `type Envelope struct { Schema int; Command string; OK bool; Result json.RawMessage; Error *ErrorBody }` with `ErrorBody{Code, Message string}`. Tasks 3, 5, 6, 7 and 9 consume `Write`; Task 4 consumes `WriteError`.

- [ ] **Step 1: Write the failing test**

```go
// internal/cliout/cliout_test.go
package cliout

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWriteWrapsResult(t *testing.T) {
	var buf bytes.Buffer
	type report struct {
		Gates int `json:"gates"`
	}
	if err := Write(&buf, "check", true, report{Gates: 6}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Schema != 1 {
		t.Errorf("Schema = %d, want 1", env.Schema)
	}
	if env.Command != "check" {
		t.Errorf("Command = %q, want %q", env.Command, "check")
	}
	if !env.OK {
		t.Error("OK = false, want true")
	}
	if env.Error != nil {
		t.Errorf("Error = %+v, want nil", env.Error)
	}
	var got report
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got.Gates != 6 {
		t.Errorf("Result.Gates = %d, want 6", got.Gates)
	}
}

func TestWriteIsIndentedAndNewlineTerminated(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, "doctor", true, map[string]int{"a": 1}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Error("output is not newline-terminated")
	}
	if !strings.Contains(out, "\n  \"command\": \"doctor\"") {
		t.Errorf("output is not 2-space indented:\n%s", out)
	}
}

func TestWriteErrorOmitsResult(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteError(&buf, "authorize", "usage", "unknown scope \"wide\""); err != nil {
		t.Fatalf("WriteError: %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.OK {
		t.Error("OK = true, want false")
	}
	if env.Error == nil || env.Error.Code != "usage" {
		t.Fatalf("Error = %+v, want code \"usage\"", env.Error)
	}
	if len(env.Result) != 0 {
		t.Errorf("Result = %s, want empty", env.Result)
	}
}

func TestWriteNilResultOmitsField(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, "help", true, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if strings.Contains(buf.String(), "\"result\"") {
		t.Errorf("nil result emitted a result field:\n%s", buf.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cliout/`
Expected: FAIL — `Write`, `WriteError`, `Envelope` undefined.

- [ ] **Step 3: Implement**

```go
// Package cliout is the single JSON writer for every pika command.
// Before it, check emitted compact JSON, adopt/apply/init emitted
// indented JSON, and init built its payload inside internal/initcmd.
// Agents parse this surface; it cannot be per-command folklore.
package cliout

import (
	"encoding/json"
	"fmt"
	"io"
)

// Schema is the envelope version. Bump only on a breaking shape change.
const Schema = 1

// ErrorBody reports a usage or configuration failure.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Envelope is the shape of every --json payload. Result carries the
// command's own report type unchanged, so existing report structs keep
// their shape and only gain nesting.
type Envelope struct {
	Schema  int             `json:"schema"`
	Command string          `json:"command"`
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ErrorBody      `json:"error,omitempty"`
}

// Write emits a successful or failed command result. ok=false implies the
// caller returns exit 1.
func Write(w io.Writer, command string, ok bool, result any) error {
	env := Envelope{Schema: Schema, Command: command, OK: ok}
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("cliout: marshal result: %w", err)
		}
		env.Result = raw
	}
	return encode(w, env)
}

// WriteError emits a usage or configuration failure; the caller returns
// exit 2.
func WriteError(w io.Writer, command, code, message string) error {
	return encode(w, Envelope{
		Schema:  Schema,
		Command: command,
		OK:      false,
		Error:   &ErrorBody{Code: code, Message: message},
	})
}

func encode(w io.Writer, env Envelope) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(env); err != nil {
		return fmt.Errorf("cliout: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/cliout/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cliout/
git commit -m "feat: shared JSON envelope for command output"
```

---

### Task 3: Table-driven dispatcher, `pika help`, and the argv hijack fix

**Files:**
- Modify: `cmd/pika/main.go` (full rewrite, currently 36 lines)
- Create: `cmd/pika/help.go`
- Test: `cmd/pika/main_test.go`

**Interfaces:**
- Consumes: `runInit`, `runCheck`, `runAdopt`, `runApply`, `runMCP` — existing signatures are `func(args []string, stdout, stderr io.Writer) int` except `runMCP`, which is `func(args []string, stdin io.Reader, stdout, stderr io.Writer) int`.
- Produces: `type command struct{ name, summary, usage string; run runFunc }`, `var commands []command`, `func dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer) int`, `func lookup(name string) (command, bool)`. Tasks 5, 6 and 7 register into `commands`.

Normalize every command onto one signature so the table has a single type. `runInit`, `runCheck`, `runAdopt` and `runApply` gain an ignored `stdin io.Reader` parameter.

- [ ] **Step 1: Write the failing test**

```go
// cmd/pika/main_test.go
package main

import (
	"bytes"
	"strings"
	"testing"
)

func dispatchArgs(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := dispatch(args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func TestBareInvocationPrintsCommandTable(t *testing.T) {
	code, out, _ := dispatchArgs(t)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, name := range []string{"init", "check", "adopt", "apply", "mcp", "help"} {
		if !strings.Contains(out, name) {
			t.Errorf("command table is missing %q:\n%s", name, out)
		}
	}
}

func TestHelpForOneCommand(t *testing.T) {
	code, out, _ := dispatchArgs(t, "help", "check")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "--changed") {
		t.Errorf("help check omitted its flags:\n%s", out)
	}
}

func TestHelpForUnknownCommandExits2(t *testing.T) {
	code, _, errb := dispatchArgs(t, "help", "bogus")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb, "bogus") {
		t.Errorf("stderr did not name the unknown command:\n%s", errb)
	}
}

func TestUnknownCommandExits2(t *testing.T) {
	code, _, errb := dispatchArgs(t, "frobnicate")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb, "frobnicate") {
		t.Errorf("stderr did not name the unknown command:\n%s", errb)
	}
}

func TestVersionOnlyAsFirstArgument(t *testing.T) {
	code, out, _ := dispatchArgs(t, "--version")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("--version printed nothing")
	}
}

// Regression: main.go:13-18 scanned every argument, so `pika check
// --version` printed the version instead of running check. A free-form
// string flag valued "version" hit the same trap.
func TestVersionIsNotHijackedFromFlagPosition(t *testing.T) {
	code, out, _ := dispatchArgs(t, "check", "--version")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (unknown flag for check)", code)
	}
	if strings.Contains(out, "pika ") {
		t.Errorf("version output leaked from a flag position:\n%s", out)
	}
}

func TestEveryCommandHasSummaryAndUsage(t *testing.T) {
	for _, c := range commands {
		if strings.TrimSpace(c.summary) == "" {
			t.Errorf("command %q has no summary", c.name)
		}
		if strings.TrimSpace(c.usage) == "" {
			t.Errorf("command %q has no usage", c.name)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/pika/ -run 'TestBare|TestHelp|TestUnknown|TestVersion|TestEveryCommand'`
Expected: FAIL — `dispatch` and `commands` undefined.

- [ ] **Step 3: Implement the dispatcher**

```go
// Command pika is the root entrypoint for the pika CLI.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Choaterboater/pika/internal/version"
)

// runFunc is the one signature every command implements. stdin is passed
// to all commands so the table stays uniform; only mcp reads it.
type runFunc func(args []string, stdin io.Reader, stdout, stderr io.Writer) int

// command is one registered subcommand. summary and usage are rendered by
// `pika help`, so the help text cannot drift from the registered set.
type command struct {
	name    string
	summary string
	usage   string
	run     runFunc
}

// commands is the registry. Adding a command here is the only step needed
// to make it dispatchable and documented.
var commands = []command{
	{
		name:    "init",
		summary: "create a contract and scaffold for a new repository",
		usage:   "pika init [--profile <lang>]... [--name <name>] [--module <path>] [--force] [--json] [--root <dir>]",
		run:     runInit,
	},
	{
		name:    "adopt",
		summary: "inventory an existing repository and draft a contract",
		usage:   "pika adopt [--json] [--root <dir>]",
		run:     runAdopt,
	},
	{
		name:    "apply",
		summary: "promote adoption drafts into a live contract transactionally",
		usage:   "pika apply [--json] [--root <dir>]",
		run:     runApply,
	},
	{
		name:    "check",
		summary: "run the verification ladder",
		usage:   "pika check [--all|--changed|--ci] [--json] [--contract <path>] [--root <dir>]",
		run:     runCheck,
	},
	{
		name:    "mcp",
		summary: "serve the kernel to agents over stdio JSON-RPC",
		usage:   "pika mcp [--root <dir>]",
		run:     runMCP,
	},
	{
		name:    "help",
		summary: "describe pika or one command",
		usage:   "pika help [<command>]",
		run:     runHelp,
	},
}

func lookup(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// dispatch routes one invocation. --version is honored only in the first
// argument position: scanning every argument (the pre-M1.5 behavior) made
// `pika check --version` print the version, and would have broken any
// command taking a free-form string.
func dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeOverview(stdout)
		return 0
	}
	switch args[0] {
	case "--version", "-version", "version":
		fmt.Fprintln(stdout, version.String())
		return 0
	}
	c, ok := lookup(args[0])
	if !ok {
		fmt.Fprintf(stderr, "pika: unknown command %q\n\n", args[0])
		writeOverview(stderr)
		return 2
	}
	return c.run(args[1:], stdin, stdout, stderr)
}

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
```

- [ ] **Step 4: Implement help rendering**

```go
// cmd/pika/help.go
package main

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// writeOverview renders the command table. It is generated from
// `commands`, so it can never describe a command that does not exist or
// omit one that does.
func writeOverview(w io.Writer) {
	fmt.Fprintln(w, "pika — a provider-neutral project operating system kernel")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "usage: pika <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range commands {
		fmt.Fprintf(tw, "  %s\t%s\n", c.name, c.summary)
	}
	tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "run \"pika help <command>\" for a command's flags")
	fmt.Fprintln(w, "run \"pika --version\" for the version")
}

// runHelp implements `pika help [<command>]`.
func runHelp(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeOverview(stdout)
		return 0
	}
	if len(args) > 1 {
		fmt.Fprintf(stderr, "pika help: unexpected argument %q\n", args[1])
		return 2
	}
	c, ok := lookup(args[0])
	if !ok {
		fmt.Fprintf(stderr, "pika help: unknown command %q\n\n", args[0])
		writeOverview(stderr)
		return 2
	}
	fmt.Fprintf(stdout, "%s\n\n%s\n", c.summary, c.usage)
	return 0
}
```

- [ ] **Step 5: Adapt the five existing command signatures**

In `cmd/pika/init.go`, `check.go`, `adopt.go` and `apply.go`, change each signature to accept and ignore stdin:

```go
func runInit(args []string, _ io.Reader, stdout, stderr io.Writer) int {
```

In `cmd/pika/mcp.go`, reorder to match:

```go
func runMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
```

Update the existing per-command tests in `cmd/pika/*_test.go` that call these directly: pass `strings.NewReader("")` as the new second argument.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./cmd/pika/`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add cmd/pika/
git commit -m "feat: table-driven command dispatch with generated help; fix argv version hijack"
```

---

### Task 4: Thread `--root` through every command

**Files:**
- Modify: `cmd/pika/init.go`, `check.go`, `adopt.go`, `apply.go`, `mcp.go`
- Modify: `internal/envelope/envelope.go` (`Load` takes an explicit root)
- Modify: `internal/checks/gate1.go` (drop `lockRelPath`, use the passed root)
- Modify: `internal/mcp/server.go` (envelope load call site)
- Test: `cmd/pika/root_test.go`

**Interfaces:**
- Consumes: `repopath.Find`, `repopath.At` (Task 1); `cliout.WriteError` (Task 2).
- Produces: `func resolveRoot(explicit string) (*repopath.Root, error)` in package `main`, used by every command. `envelope.Load(root, path string) (*Envelope, error)` replaces the directory-arithmetic form.

- [ ] **Step 1: Write the failing test**

```go
// cmd/pika/root_test.go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRootFindsAncestorContract(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".project", "contract.yaml"), []byte("schema: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	got, err := resolveRoot("")
	if err != nil {
		t.Fatalf("resolveRoot: %v", err)
	}
	// macOS temp dirs are symlinked via /var -> /private/var; compare
	// resolved forms.
	wantAbs, _ := filepath.EvalSymlinks(root)
	gotAbs, _ := filepath.EvalSymlinks(got.Dir())
	if gotAbs != wantAbs {
		t.Fatalf("Dir() = %q, want %q", gotAbs, wantAbs)
	}
}

func TestResolveRootExplicitOverride(t *testing.T) {
	other := t.TempDir()
	t.Chdir(t.TempDir())

	got, err := resolveRoot(other)
	if err != nil {
		t.Fatalf("resolveRoot: %v", err)
	}
	wantAbs, _ := filepath.EvalSymlinks(other)
	gotAbs, _ := filepath.EvalSymlinks(got.Dir())
	if gotAbs != wantAbs {
		t.Fatalf("Dir() = %q, want %q", gotAbs, wantAbs)
	}
}

func TestResolveRootRejectsMissingDir(t *testing.T) {
	if _, err := resolveRoot(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("resolveRoot(missing) = nil error, want error")
	}
}

// The whole point of Task 1: check must work from a subdirectory.
func TestCheckRunsFromSubdirectory(t *testing.T) {
	root := t.TempDir()
	writeMinimalProject(t, root) // helper defined in check_test.go
	nested := filepath.Join(root, "internal", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	var out, errb bytes.Buffer
	code := runCheck([]string{"--all", "--json"}, strings.NewReader(""), &out, &errb)
	if code == 2 {
		t.Fatalf("check from subdirectory returned usage error: %s", errb.String())
	}
	if !strings.Contains(out.String(), "\"command\": \"check\"") {
		t.Errorf("check did not emit its report from a subdirectory:\n%s", out.String())
	}
}
```

Add `writeMinimalProject(t *testing.T, root string)` to `cmd/pika/check_test.go` if no equivalent helper exists: it writes `.project/contract.yaml` selecting `core@1`, a matching `.project/profiles.lock` via `profiles.WriteLock`, and `.project/exceptions.yaml` containing `{}`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/pika/ -run TestResolveRoot`
Expected: FAIL — `resolveRoot` undefined.

- [ ] **Step 3: Implement `resolveRoot` and register the flag**

```go
// cmd/pika/root.go
package main

import (
	"os"

	"github.com/Choaterboater/pika/internal/repopath"
)

// resolveRoot binds the repository root for one command invocation. An
// explicit --root bypasses discovery; otherwise the root is discovered by
// walking up from the working directory.
func resolveRoot(explicit string) (*repopath.Root, error) {
	if explicit != "" {
		return repopath.At(explicit)
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return repopath.Find(wd)
}
```

In each of `init.go`, `check.go`, `adopt.go`, `apply.go`, `mcp.go`, register the flag and resolve before doing any work:

```go
	rootFlag := fs.String("root", "", "repository root (default: discovered from the working directory)")
	// ... after fs.Parse and the NArg check:
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "pika check: %v\n", err)
		return 2
	}
```

Then replace each hardcoded `"."` and each `.project/...` literal with the `root` accessors:

- `check.go`: delete `const defaultContractPath`; `repoRoot := "."` becomes `root.Dir()`; the default contract path becomes `root.Contract()`. `--contract`, when given a relative path, resolves against `root.Dir()`.
- `adopt.go`: `adopt.Preview(root.Dir())`.
- `apply.go`: `apply.Run(apply.RunOptions{Dir: root.Dir()})`.
- `init.go`: `Dir: root.Dir()`.
- `mcp.go`: `mcp.Serve(root.Dir(), stdin, stdout, stderr)`.
- `internal/checks/gate1.go`: delete `const lockRelPath` and derive the lock path from the `repoRoot` parameter already passed to `checkLock`.

- [ ] **Step 4: Fix the envelope's inferred root**

`envelope.Load` currently derives the repo root by three `filepath.Dir` calls on the envelope path (`envelope.go:238`), which silently mis-roots `fs_read` if the file ever moves. Change the signature:

```go
// Load reads and validates the envelope at path, binding it to an
// explicit repository root. The root is supplied by the caller rather
// than inferred from the path, so relocating the envelope cannot
// silently change what fs_read authorizes.
func Load(repoRoot, path string) (*Envelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	env, err := Validate(data)
	if err != nil {
		return nil, err
	}
	// NewEnvelope returns *Envelope only; it has no error result.
	return NewEnvelope(env, repoRoot), nil
}
```

Update the single call site in `internal/mcp/server.go:734` to pass the server's `repoRoot`, and update `internal/envelope/envelope_test.go` for the new signature.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/pika/ ./internal/envelope/ ./internal/checks/ ./internal/mcp/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/pika/ internal/envelope/ internal/checks/ internal/mcp/
git commit -m "feat: --root flag and discovered repository root across all commands"
```

---

### Task 5: `pika doctor`

**Files:**
- Create: `internal/doctor/doctor.go`
- Test: `internal/doctor/doctor_test.go`
- Create: `cmd/pika/doctor.go`
- Modify: `cmd/pika/main.go` (register in `commands`)

**Interfaces:**
- Consumes: `repopath.Root` (Task 1), `cliout.Write` (Task 2), `contract.Load`, `profiles.Resolve`, `profiles.ReadLock`, `checks.LoadExceptions`, `envelope.Load`, `verify.FromProfiles`.
- Produces: `doctor.Run(root *repopath.Root) *Report`, `type Report struct{ Root string; Origin string; Findings []Finding; OK bool }`, `type Finding struct{ ID, Severity, Detail, Remediation string }`, and the severity constants `SeverityOK`, `SeverityWarn`, `SeverityError`.

- [ ] **Step 1: Write the failing test**

```go
// internal/doctor/doctor_test.go
package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Choaterboater/pika/internal/repopath"
)

func findingByID(t *testing.T, rep *Report, id string) Finding {
	t.Helper()
	for _, f := range rep.Findings {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no finding %q in %+v", id, rep.Findings)
	return Finding{}
}

func TestUnadoptedRepositoryIsReportedNotFailed(t *testing.T) {
	root, err := repopath.At(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rep := Run(root)

	f := findingByID(t, rep, "contract")
	if f.Severity != SeverityError {
		t.Errorf("contract severity = %q, want %q", f.Severity, SeverityError)
	}
	if f.Remediation == "" {
		t.Error("contract finding has no remediation")
	}
	if rep.OK {
		t.Error("OK = true for an unadopted repository")
	}
	// doctor itself must not panic or bail: it reports every category
	// even when the contract is missing.
	for _, id := range []string{"root", "contract", "lock", "envelope", "git"} {
		findingByID(t, rep, id)
	}
}

func TestHealthyProjectReportsOK(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}

	rep := Run(root)
	for _, f := range rep.Findings {
		if f.Severity == SeverityError {
			t.Errorf("unexpected error finding %q: %s", f.ID, f.Detail)
		}
	}
	if !rep.OK {
		t.Error("OK = false for a healthy project")
	}
	if findingByID(t, rep, "root").Detail == "" {
		t.Error("root finding does not report how the root was resolved")
	}
}

func TestDriftedLockIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	lock := filepath.Join(dir, ".project", "profiles.lock")
	if err := os.WriteFile(lock, []byte(`{"digest":"deadbeef","packs":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	root, _ := repopath.At(dir)

	if got := findingByID(t, Run(root), "lock").Severity; got != SeverityError {
		t.Fatalf("lock severity = %q, want %q", got, SeverityError)
	}
}

func TestMissingEnvelopeIsAWarningNotAnError(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	root, _ := repopath.At(dir)

	f := findingByID(t, Run(root), "envelope")
	if f.Severity != SeverityWarn {
		t.Fatalf("envelope severity = %q, want %q", f.Severity, SeverityWarn)
	}
	if f.Remediation == "" {
		t.Error("envelope finding must point at pika authorize")
	}
}

// Check.Hint is resolved today and read by nobody. doctor is its first
// consumer: an undiscovered slot must surface the pack's suggestion.
func TestUndiscoveredGateSurfacesPackHint(t *testing.T) {
	dir := t.TempDir()
	writeHealthyTypeScriptProject(t, dir)
	root, _ := repopath.At(dir)

	f := findingByID(t, Run(root), "gate.lint")
	if f.Severity != SeverityWarn {
		t.Errorf("gate.lint severity = %q, want %q", f.Severity, SeverityWarn)
	}
	if f.Remediation == "" {
		t.Fatal("gate.lint carries no hint")
	}
}
```

Write `writeHealthyProject` and `writeHealthyTypeScriptProject` helpers in the same test file: each creates `.project/contract.yaml` (selecting `core@1`, and `core@1`+`typescript@1` respectively), writes the matching lock with `profiles.WriteLock`, and writes `.project/exceptions.yaml` containing `{}`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/doctor/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

```go
// Package doctor diagnoses a repository's pika health without mutating
// anything and without executing any gate command. It answers the
// question "why did that not work" that previously required reading
// kernel source.
package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/Choaterboater/pika/internal/checks"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/envelope"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/version"
)

// Severity levels. Only SeverityError affects the exit code; a warning is
// a review signal, matching gate 1's severity model.
const (
	SeverityOK    = "ok"
	SeverityWarn  = "warn"
	SeverityError = "error"
)

// Finding is one diagnosed fact.
type Finding struct {
	ID          string `json:"id"`
	Severity    string `json:"severity"`
	Detail      string `json:"detail"`
	Remediation string `json:"remediation,omitempty"`
}

// Report is the doctor result.
type Report struct {
	Root     string    `json:"root"`
	Origin   string    `json:"origin"`
	Findings []Finding `json:"findings"`
	OK       bool      `json:"ok"`
}

func (r *Report) add(id, severity, detail, remediation string) {
	r.Findings = append(r.Findings, Finding{
		ID: id, Severity: severity, Detail: detail, Remediation: remediation,
	})
	if severity == SeverityError {
		r.OK = false
	}
}

// Run diagnoses the repository. It never returns an error: a broken
// repository is the thing being reported, not a reason to fail.
func Run(root *repopath.Root) *Report {
	rep := &Report{Root: root.Dir(), Origin: root.Origin(), OK: true}
	rep.add("root", SeverityOK,
		fmt.Sprintf("%s (resolved by %s)", root.Dir(), root.Origin()), "")

	c := checkContract(rep, root)
	resolved := checkProfiles(rep, root, c)
	checkExceptions(rep, root)
	checkEnvelope(rep, root)
	checkGates(rep, c, resolved)
	checkGit(rep)
	return rep
}

func checkContract(rep *Report, root *repopath.Root) *contract.Contract {
	c, err := contract.Load(root.Contract())
	if err != nil {
		if os.IsNotExist(err) {
			rep.add("contract", SeverityError, "no contract at "+root.Contract(),
				"run \"pika init\" for a new project or \"pika adopt\" for an existing one")
			return nil
		}
		rep.add("contract", SeverityError, err.Error(),
			"fix the contract, then re-run \"pika doctor\"")
		return nil
	}
	if err := version.Check(c.Schema); err != nil {
		rep.add("contract", SeverityError, err.Error(),
			"upgrade the pika binary; this contract targets a newer schema")
		return c
	}
	rep.add("contract", SeverityOK,
		fmt.Sprintf("schema %d, profiles %v", c.Schema, c.Profiles), "")
	return c
}

func checkProfiles(rep *Report, root *repopath.Root, c *contract.Contract) *profiles.Resolved {
	if c == nil {
		rep.add("lock", SeverityError, "not checked: no contract", "")
		return nil
	}
	resolved, err := profiles.Resolve(c.Profiles)
	if err != nil {
		rep.add("profiles", SeverityError, err.Error(),
			"correct the profiles list in the contract")
		return nil
	}
	if _, err := profiles.ReadLock(root.Lock()); err != nil {
		rep.add("lock", SeverityError, err.Error(),
			"re-run \"pika init --force\" or \"pika apply\" to regenerate the lock")
		return resolved
	}
	// checkLock is gate 1's implementation; reuse it so doctor and check
	// can never disagree about lock health.
	if err := checks.CheckLock(root.Dir(), c); err != nil {
		rep.add("lock", SeverityError, err.Error(),
			"regenerate the lock; the pinned digests no longer match the embedded packs")
		return resolved
	}
	rep.add("lock", SeverityOK, "pinned digests match the embedded registry", "")
	return resolved
}

func checkExceptions(rep *Report, root *repopath.Root) {
	if _, err := checks.LoadExceptions(root.Dir()); err != nil {
		rep.add("exceptions", SeverityError, err.Error(),
			"fix .project/exceptions.yaml; unverifiable records must not widen the rules")
		return
	}
	rep.add("exceptions", SeverityOK, "exceptions record loads", "")
}

func checkEnvelope(rep *Report, root *repopath.Root) {
	path := root.Envelope()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		rep.add("envelope", SeverityWarn, "no capability envelope",
			"run \"pika authorize --scope project\"; without it every mutating MCP tool is denied")
		return
	}
	env, err := envelope.Load(root.Dir(), path)
	if err != nil {
		rep.add("envelope", SeverityError, err.Error(),
			"fix or regenerate the envelope with \"pika authorize --force\"")
		return
	}
	granted := grantedKinds(env)
	rep.add("envelope", SeverityOK, "grants: "+granted, "")
}

func grantedKinds(env *envelope.Envelope) string {
	out, err := json.Marshal(env.Env.Allow)
	if err != nil {
		return "unreadable"
	}
	return string(out)
}

// checkGates reports each slot's resolved command, or the pack's hint
// when the slot is an undiscovered sentinel — Check.Hint's first
// consumer. It never executes a gate.
func checkGates(rep *Report, c *contract.Contract, resolved *profiles.Resolved) {
	if c == nil || resolved == nil {
		return
	}
	hints := map[string][]string{
		"format":    resolved.Checks.Format.Hint,
		"lint":      resolved.Checks.Lint.Hint,
		"typecheck": resolved.Checks.Typecheck.Hint,
		"test":      resolved.Checks.Test.Hint,
		"smoke":     resolved.Checks.Smoke.Hint,
	}
	gates, err := verify.FromProfiles(resolved.Checks, c.Commands)
	if err != nil {
		rep.add("gates", SeverityError, err.Error(),
			"correct the commands map in the contract")
		return
	}
	for _, g := range gates {
		id := "gate." + g.ID
		if g.SkipReason != "" {
			remediation := "no command discovered and the pack offers no hint"
			if h := hints[g.ID]; len(h) > 0 {
				remediation = fmt.Sprintf("set commands.%s in the contract, for example %q", g.ID, join(h))
			}
			rep.add(id, SeverityWarn, g.SkipReason, remediation)
			continue
		}
		if _, err := exec.LookPath(g.Cmd[0]); err != nil {
			rep.add(id, SeverityWarn,
				fmt.Sprintf("%s: %s is not on PATH", join(g.Cmd), g.Cmd[0]),
				"install the toolchain, or this gate cannot run here")
			continue
		}
		rep.add(id, SeverityOK, join(g.Cmd), "")
	}
}

func checkGit(rep *Report) {
	if _, err := exec.LookPath("git"); err != nil {
		rep.add("git", SeverityWarn, "git is not on PATH",
			"\"pika check --changed\" will fall back to running every gate")
		return
	}
	rep.add("git", SeverityOK, "git is available", "")
}

func join(argv []string) string {
	out := ""
	for i, a := range argv {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
```

Export gate 1's lock check so doctor reuses it rather than reimplementing: in `internal/checks/gate1.go` rename `checkLock` to `CheckLock` and update its call site at `gate1.go:43`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/doctor/ ./internal/checks/`
Expected: PASS

- [ ] **Step 5: Wire the command**

```go
// cmd/pika/doctor.go
package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/Choaterboater/pika/internal/cliout"
	"github.com/Choaterboater/pika/internal/doctor"
)

// runDoctor implements `pika doctor`. Exit 0 when nothing is
// error-severity, 1 when something is, 2 on usage error.
func runDoctor(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit the report as JSON")
	rootFlag := fs.String("root", "", "repository root (default: discovered from the working directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "pika doctor: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "pika doctor: %v\n", err)
		return 2
	}

	rep := doctor.Run(root)
	if *jsonOut {
		if err := cliout.Write(stdout, "doctor", rep.OK, rep); err != nil {
			fmt.Fprintf(stderr, "pika doctor: %v\n", err)
			return 2
		}
	} else {
		printDoctorReport(rep, stdout)
	}
	if !rep.OK {
		return 1
	}
	return 0
}

func printDoctorReport(rep *doctor.Report, stdout io.Writer) {
	fmt.Fprintf(stdout, "root  %s (%s)\n\n", rep.Root, rep.Origin)
	for _, f := range rep.Findings {
		if f.ID == "root" {
			continue
		}
		fmt.Fprintf(stdout, "%-5s %-14s %s\n", f.Severity, f.ID, f.Detail)
		if f.Remediation != "" {
			fmt.Fprintf(stdout, "      %-14s → %s\n", "", f.Remediation)
		}
	}
}
```

Register in `commands` (`cmd/pika/main.go`), after `check`:

```go
	{
		name:    "doctor",
		summary: "diagnose contract, lock, envelope, gates, and toolchain",
		usage:   "pika doctor [--json] [--root <dir>]",
		run:     runDoctor,
	},
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./cmd/pika/ ./internal/doctor/`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/doctor/ internal/checks/ cmd/pika/
git commit -m "feat: pika doctor diagnoses contract, lock, envelope, gates, and toolchain"
```

---

### Task 6: Rule rationale and `pika explain`

**Files:**
- Modify: `internal/profiles/registry.go` (`namingSpec`, `NamingRule`, the resolve path)
- Modify: `internal/profiles/packs/core@1.yaml` (four rules gain `rationale` and `remediation`)
- Create: `internal/explain/explain.go`
- Test: `internal/explain/explain_test.go`
- Create: `cmd/pika/explain.go`
- Modify: `cmd/pika/main.go`
- Modify: `internal/checks/gate1.go` (`CheckLock` verifies the top-level digest)

**Interfaces:**
- Consumes: `profiles.Resolve`, `profiles.NamingRule` (now with `Rationale`, `Remediation`), `repopath.Root`, `cliout.Write`.
- Produces: `explain.Lookup(id string, resolved *profiles.Resolved) (*Entry, error)`, `type Entry struct{ ID, Kind, Owner, Severity, Matches, Rationale, Remediation, Exception string }`, `explain.KnownIDs(resolved *profiles.Resolved) []string`.

- [ ] **Step 1: Write the failing test**

```go
// internal/explain/explain_test.go
package explain

import (
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/profiles"
)

func resolveCore(t *testing.T) *profiles.Resolved {
	t.Helper()
	r, err := profiles.Resolve([]string{profiles.CoreRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return r
}

// Design spec goal 10: every rule is explainable. A rule that cannot
// explain itself must not ship.
func TestEveryResolvedNamingRuleIsExplainable(t *testing.T) {
	resolved := resolveCore(t)
	if len(resolved.NamingRules) == 0 {
		t.Fatal("core resolved no naming rules")
	}
	for _, r := range resolved.NamingRules {
		e, err := Lookup(r.RuleID, resolved)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", r.RuleID, err)
		}
		if strings.TrimSpace(e.Rationale) == "" {
			t.Errorf("rule %q has no rationale", r.RuleID)
		}
		if strings.TrimSpace(e.Remediation) == "" {
			t.Errorf("rule %q has no remediation", r.RuleID)
		}
		if strings.TrimSpace(e.Owner) == "" {
			t.Errorf("rule %q names no owning pack", r.RuleID)
		}
		if strings.TrimSpace(e.Exception) == "" {
			t.Errorf("rule %q shows no exception record", r.RuleID)
		}
	}
}

func TestExplainNamingRuleDetail(t *testing.T) {
	e, err := Lookup("naming-catch-all", resolveCore(t))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if e.Kind != KindNamingRule {
		t.Errorf("Kind = %q, want %q", e.Kind, KindNamingRule)
	}
	if e.Severity != "error" {
		t.Errorf("Severity = %q, want %q", e.Severity, "error")
	}
	if !strings.Contains(e.Matches, "utils") {
		t.Errorf("Matches does not mention the banned segments: %q", e.Matches)
	}
	if !strings.Contains(e.Exception, "naming-catch-all") {
		t.Errorf("Exception record does not carry the rule id: %q", e.Exception)
	}
}

func TestExplainGateID(t *testing.T) {
	e, err := Lookup("typecheck", resolveCore(t))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if e.Kind != KindGate {
		t.Errorf("Kind = %q, want %q", e.Kind, KindGate)
	}
}

func TestExplainErrorCode(t *testing.T) {
	e, err := Lookup("envelope_denied", resolveCore(t))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if e.Kind != KindErrorCode {
		t.Errorf("Kind = %q, want %q", e.Kind, KindErrorCode)
	}
	if !strings.Contains(e.Remediation, "authorize") {
		t.Errorf("envelope_denied does not point at pika authorize: %q", e.Remediation)
	}
}

func TestUnknownIDListsKnownIDs(t *testing.T) {
	resolved := resolveCore(t)
	if _, err := Lookup("no-such-rule", resolved); err == nil {
		t.Fatal("Lookup(unknown) = nil error, want error")
	}
	ids := KnownIDs(resolved)
	if len(ids) == 0 {
		t.Fatal("KnownIDs returned nothing")
	}
	for _, want := range []string{"naming-catch-all", "typecheck", "envelope_denied"} {
		found := false
		for _, id := range ids {
			if id == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("KnownIDs omits %q", want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/explain/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Add rationale and remediation to the pack format**

In `internal/profiles/registry.go`, extend both rule structs:

```go
// namingSpec is the pack-side form of a naming rule. Rationale and
// Remediation are required for `pika explain`: a rule that cannot explain
// itself is a rule nobody can act on (design spec goal 10).
type namingSpec struct {
	RuleID      string   `yaml:"rule-id"`
	Severity    string   `yaml:"severity"`
	Scope       string   `yaml:"scope"`
	Pattern     string   `yaml:"pattern"`
	Banned      []string `yaml:"banned"`
	Exempt      []string `yaml:"exempt-stems"`
	Rationale   string   `yaml:"rationale"`
	Remediation string   `yaml:"remediation"`
}
```

```go
type NamingRule struct {
	RuleID      string
	Severity    string
	Scope       string
	Pattern     string
	Banned      []string
	Exempt      []string
	Rationale   string
	Remediation string
}
```

Copy both new fields wherever a `namingSpec` becomes a `NamingRule` in `Resolve`.

In `internal/profiles/packs/core@1.yaml`, add the two keys to each of the four rules (`naming-kebab-case`, `naming-catch-all`, `file-size-review`, `generated-owner`). For example:

```yaml
    - rule-id: naming-catch-all
      severity: error
      scope: path-segments
      banned: [utils, helpers, common, misc, manager]
      rationale: >-
        A catch-all name states no domain responsibility, so the file
        accumulates unrelated code and no owner boundary survives review
        (design spec §6.2).
      remediation: >-
        Rename the path to the responsibility it actually owns, or record
        an exception in .project/exceptions.yaml with a rationale, an
        owner, and the condition under which it is revisited.
```

- [ ] **Step 4: Make `CheckLock` verify the top-level digest**

The lock's top-level `digest` is written by `profiles.WriteLock` and never verified (`checks/gate1.go:66-103` compares per-pack digests only). A field that is written and never checked is worse than no field. In `CheckLock`, after the per-pack comparison, compare the stored top-level digest against `profiles.PackDigest()` and return an error naming both values on mismatch.

- [ ] **Step 5: Implement explain**

```go
// Package explain answers "what is this id and what do I do about it" for
// naming rules, verification gates, and MCP error codes — design spec
// goal 10, every rule explainable.
package explain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Choaterboater/pika/internal/profiles"
)

// Entry kinds.
const (
	KindNamingRule = "naming-rule"
	KindGate       = "gate"
	KindErrorCode  = "error-code"
)

// Entry is one explained id.
type Entry struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Owner       string `json:"owner,omitempty"`
	Severity    string `json:"severity,omitempty"`
	Matches     string `json:"matches,omitempty"`
	Rationale   string `json:"rationale"`
	Remediation string `json:"remediation"`
	Exception   string `json:"exception,omitempty"`
}

// gateEntries explains the verification ladder's rungs (design spec §12.6).
var gateEntries = map[string]Entry{
	"contract":  {Kind: KindGate, Rationale: "Rung 1: contract schema ceiling, exceptions record, profile lock, and the naming projection.", Remediation: "Run \"pika doctor\" to see which sub-check failed."},
	"format":    {Kind: KindGate, Rationale: "Rung 2: the stack formatter.", Remediation: "Set commands.format in the contract, or install the pack's suggested formatter."},
	"lint":      {Kind: KindGate, Rationale: "Rung 2: the stack linter.", Remediation: "Set commands.lint in the contract."},
	"typecheck": {Kind: KindGate, Rationale: "Rung 2: compilation and type checking.", Remediation: "Set commands.typecheck in the contract."},
	"test":      {Kind: KindGate, Rationale: "Rung 3: affected behavioral tests.", Remediation: "Set commands.test in the contract."},
	"smoke":     {Kind: KindGate, Rationale: "Rung 4: a real-surface smoke scenario.", Remediation: "Set commands.smoke in the contract; a skipped smoke gate means no real surface was exercised."},
}

// errorEntries explains the MCP server's closed error-code set.
var errorEntries = map[string]Entry{
	"invalid_params":   {Kind: KindErrorCode, Rationale: "The tool arguments failed validation.", Remediation: "Correct the arguments against the tool's input schema from tools/list."},
	"envelope_denied":  {Kind: KindErrorCode, Rationale: "The capability envelope does not grant this operation. Every mutating tool is deny-by-default.", Remediation: "Run \"pika authorize --scope project\" and retry."},
	"contract_invalid": {Kind: KindErrorCode, Rationale: "The contract failed strict parsing or schema validation.", Remediation: "Run \"pika doctor\" for the specific violation."},
	"already_adopted":  {Kind: KindErrorCode, Rationale: "A committed contract already exists, so adoption would overwrite a live project.", Remediation: "Use \"pika check\" instead; adoption is for unadopted repositories."},
	"unavailable":      {Kind: KindErrorCode, Rationale: "The tool is registered for discoverability but not implemented in this build.", Remediation: "No action available; the capability lands in a later milestone."},
	"internal":         {Kind: KindErrorCode, Rationale: "The kernel failed unexpectedly.", Remediation: "Re-run with the failing input; if it reproduces, this is a pika defect."},
}

// Lookup resolves one id across all three namespaces.
func Lookup(id string, resolved *profiles.Resolved) (*Entry, error) {
	for _, r := range resolved.NamingRules {
		if r.RuleID != id {
			continue
		}
		e := Entry{
			ID:          r.RuleID,
			Kind:        KindNamingRule,
			Owner:       ownerOf(resolved, r.RuleID),
			Severity:    r.Severity,
			Matches:     matchSummary(r),
			Rationale:   r.Rationale,
			Remediation: r.Remediation,
			Exception:   exceptionRecord(r.RuleID),
		}
		return &e, nil
	}
	if e, ok := gateEntries[id]; ok {
		e.ID = id
		return &e, nil
	}
	if e, ok := errorEntries[id]; ok {
		e.ID = id
		return &e, nil
	}
	return nil, fmt.Errorf("explain: unknown id %q", id)
}

// KnownIDs lists every explainable id, sorted, for the unknown-id message.
func KnownIDs(resolved *profiles.Resolved) []string {
	ids := make([]string, 0, len(resolved.NamingRules)+len(gateEntries)+len(errorEntries))
	for _, r := range resolved.NamingRules {
		ids = append(ids, r.RuleID)
	}
	for id := range gateEntries {
		ids = append(ids, id)
	}
	for id := range errorEntries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ownerOf names the pack layer that contributed a rule.
func ownerOf(resolved *profiles.Resolved, ruleID string) string {
	for _, layer := range resolved.Layers {
		for _, r := range layer.Pack.Naming.Rules {
			if r.RuleID == ruleID {
				return layer.Name + "@" + layer.Version
			}
		}
	}
	return ""
}

func matchSummary(r profiles.NamingRule) string {
	var parts []string
	if r.Scope != "" {
		parts = append(parts, "scope "+r.Scope)
	}
	if r.Pattern != "" {
		parts = append(parts, "pattern "+r.Pattern)
	}
	if len(r.Banned) > 0 {
		parts = append(parts, "banned "+strings.Join(r.Banned, ", "))
	}
	if len(r.Exempt) > 0 {
		parts = append(parts, "exempt "+strings.Join(r.Exempt, ", "))
	}
	return strings.Join(parts, "; ")
}

// exceptionRecord shows the exact waiver the operator would record. The
// four fields are mandatory (design spec §5.3).
func exceptionRecord(ruleID string) string {
	return strings.Join([]string{
		"# .project/exceptions.yaml",
		ruleID + ":",
		"  - path: <repo-relative path>",
		"    rationale: <why this path must keep its name>",
		"    owner: <who accepts this>",
		"    review: <the condition that reopens this decision>",
	}, "\n")
}
```

Note the `namingSpec` field is unexported, so `ownerOf` reading `layer.Pack.Naming.Rules` requires `Naming.Rules` to be accessible from `internal/explain`. It is: the field is exported on an exported struct and `namingSpec`'s own fields are read only through the resolved `NamingRule`. If the compiler rejects the access because `namingSpec` is unexported, add an exported accessor `func (p Pack) NamingRuleIDs() []string` to `registry.go` and match on that instead.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/explain/ ./internal/profiles/ ./internal/checks/`
Expected: PASS. `internal/profiles` golden and snapshot tests will fail on the rotated digest — regenerate the expected digests in `internal/profiles/contract_snapshot_test.go` and any lock fixtures, which is the accepted cost recorded in spec §7.

- [ ] **Step 7: Wire the command**

Create `cmd/pika/explain.go` following `doctor.go`: flags `--json` and `--root`; require exactly one positional id (zero or two-plus is exit 2); load the contract to resolve profiles, falling back to `[]string{profiles.CoreRef}` when there is no contract so `explain` works in an unadopted directory; on unknown id print the error plus `KnownIDs` to stderr and return 2. Register in `commands` after `doctor`:

```go
	{
		name:    "explain",
		summary: "explain a naming rule, gate, or error code",
		usage:   "pika explain <rule-id> [--json] [--root <dir>]",
		run:     runExplain,
	},
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./cmd/pika/ ./internal/explain/`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/profiles/ internal/explain/ internal/checks/ cmd/pika/
git commit -m "feat: pika explain with rule rationale and remediation; verify the lock's top-level digest"
```

---

### Task 7: `pika authorize`

**Files:**
- Create: `internal/authorize/authorize.go`
- Test: `internal/authorize/authorize_test.go`
- Create: `cmd/pika/authorize.go`
- Modify: `cmd/pika/main.go`

**Interfaces:**
- Consumes: `repopath.Root`, `contract.Load`, `profiles.Resolve`, `verify.FromProfiles`, `envelope.Validate`, `cliout.Write`.
- Produces: `authorize.Build(opts Options) (*envelope.Env, error)`, `type Options struct{ Root *repopath.Root; Scope string; Network, Credential, GitHub []string }`, `authorize.Render(env *envelope.Env) ([]byte, error)`, and the scope constants `ScopeRead`, `ScopeProject`, `ScopeRepo`.

- [ ] **Step 1: Write the failing test**

```go
// internal/authorize/authorize_test.go
package authorize

import (
	"testing"

	"github.com/Choaterboater/pika/internal/envelope"
	"github.com/Choaterboater/pika/internal/repopath"
)

func projectRoot(t *testing.T) *repopath.Root {
	t.Helper()
	dir := t.TempDir()
	writeGoProject(t, dir) // contract selecting core@1 + go@1, plus a matching lock
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadScopeGrantsNothingMutating(t *testing.T) {
	env, err := Build(Options{Root: projectRoot(t), Scope: ScopeRead})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(env.Allow.FSWrite) != 0 {
		t.Errorf("read scope granted fs_write: %v", env.Allow.FSWrite)
	}
	if len(env.Allow.Exec) != 0 {
		t.Errorf("read scope granted exec: %v", env.Allow.Exec)
	}
}

func TestProjectScopeGrantsProjectPathsOnly(t *testing.T) {
	env, err := Build(Options{Root: projectRoot(t), Scope: ScopeProject})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := map[string]bool{".project": true, "docs": true, "review": true}
	if len(env.Allow.FSWrite) != len(want) {
		t.Fatalf("fs_write = %v, want exactly %v", env.Allow.FSWrite, want)
	}
	for _, p := range env.Allow.FSWrite {
		if !want[p] {
			t.Errorf("unexpected fs_write grant %q", p)
		}
	}
}

func TestRepoScopeGrantsRepoWide(t *testing.T) {
	env, err := Build(Options{Root: projectRoot(t), Scope: ScopeRepo})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(env.Allow.FSWrite) != 1 || env.Allow.FSWrite[0] != "." {
		t.Fatalf("fs_write = %v, want [\".\"]", env.Allow.FSWrite)
	}
}

// exec must match what check will actually run, or authorization is
// theater.
func TestExecMatchesResolvedGates(t *testing.T) {
	env, err := Build(Options{Root: projectRoot(t), Scope: ScopeProject})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	found := false
	for _, e := range env.Allow.Exec {
		if e == "go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("exec = %v, want it to include \"go\" (the go@1 gates' argv[0])", env.Allow.Exec)
	}
	for _, e := range env.Allow.Exec {
		if e == "*" {
			t.Fatal("authorize generated a bare * exec grant")
		}
	}
}

func TestNetworkCredentialGitHubNeverImplicit(t *testing.T) {
	for _, scope := range []string{ScopeRead, ScopeProject, ScopeRepo} {
		env, err := Build(Options{Root: projectRoot(t), Scope: scope})
		if err != nil {
			t.Fatalf("Build(%s): %v", scope, err)
		}
		if len(env.Allow.Network) != 0 || len(env.Allow.Credential) != 0 || len(env.Allow.GitHub) != 0 {
			t.Errorf("scope %s implicitly granted network/credential/github", scope)
		}
		if len(env.Allow.Budget) != 0 {
			t.Errorf("scope %s wrote a budget ceiling that nothing enforces", scope)
		}
	}
}

func TestExplicitGrantsAreHonored(t *testing.T) {
	env, err := Build(Options{
		Root:    projectRoot(t),
		Scope:   ScopeProject,
		Network: []string{"proxy.golang.org"},
		GitHub:  []string{"pull_request:write"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(env.Allow.Network) != 1 || env.Allow.Network[0] != "proxy.golang.org" {
		t.Errorf("network = %v", env.Allow.Network)
	}
	if len(env.Allow.GitHub) != 1 {
		t.Errorf("github = %v", env.Allow.GitHub)
	}
}

// The generated document must survive the kernel's own strict validator.
func TestRenderedEnvelopeValidates(t *testing.T) {
	env, err := Build(Options{Root: projectRoot(t), Scope: ScopeProject})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	data, err := Render(env)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if _, err := envelope.Validate(data); err != nil {
		t.Fatalf("generated envelope failed envelope.Validate: %v\n%s", err, data)
	}
}

func TestUnknownScopeIsAnError(t *testing.T) {
	if _, err := Build(Options{Root: projectRoot(t), Scope: "wide"}); err == nil {
		t.Fatal("Build(unknown scope) = nil error, want error")
	}
}

func TestProjectScopeRequiresAContract(t *testing.T) {
	root, err := repopath.At(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(Options{Root: root, Scope: ScopeProject}); err == nil {
		t.Fatal("Build without a contract = nil error, want error")
	}
	if _, err := Build(Options{Root: root, Scope: ScopeRead}); err != nil {
		t.Fatalf("read scope must work without a contract: %v", err)
	}
}
```

Write `writeGoProject(t, dir)` in the test file: `.project/contract.yaml` selecting `core@1` and `go@1` with an empty `commands` map, plus the matching lock via `profiles.WriteLock`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/authorize/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

```go
// Package authorize generates a capability envelope from a declared
// intent. Before it, an operator had to hand-author
// .project/state/envelope.yaml or every mutating MCP tool returned
// envelope_denied — the single largest barrier to handing pika to an
// agent.
package authorize

import (
	"fmt"
	"sort"

	"github.com/goccy/go-yaml"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/envelope"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/verify"
)

// Scopes, narrowest first.
const (
	ScopeRead    = "read"
	ScopeProject = "project"
	ScopeRepo    = "repo"
)

// projectPaths are the directories pika itself owns (design spec §6).
var projectPaths = []string{".project", "docs", "review"}

// Options declares the intent an envelope is generated from.
type Options struct {
	Root       *repopath.Root
	Scope      string
	Network    []string
	Credential []string
	GitHub     []string
}

// Build produces the envelope for the declared scope. Nothing beyond the
// scope's own grants and the explicit lists is ever granted: budget is
// deliberately never written, because no code compares spend against it,
// and a ceiling that is never enforced is a lie.
func Build(opts Options) (*envelope.Env, error) {
	env := &envelope.Env{
		Schema:           1,
		RollbackBoundary: "repository",
	}
	switch opts.Scope {
	case ScopeRead:
	case ScopeProject:
		env.Allow.FSWrite = append([]string(nil), projectPaths...)
	case ScopeRepo:
		env.Allow.FSWrite = []string{"."}
	default:
		return nil, fmt.Errorf("authorize: unknown scope %q (want %s, %s, or %s)",
			opts.Scope, ScopeRead, ScopeProject, ScopeRepo)
	}

	if opts.Scope != ScopeRead {
		execs, err := gateBinaries(opts.Root)
		if err != nil {
			return nil, err
		}
		env.Allow.Exec = execs
	}

	env.Allow.Network = dedupe(opts.Network)
	env.Allow.Credential = dedupe(opts.Credential)
	env.Allow.GitHub = dedupe(opts.GitHub)
	return env, nil
}

// gateBinaries collects argv[0] of every gate the contract will actually
// run, so authorization matches execution rather than guessing.
func gateBinaries(root *repopath.Root) ([]string, error) {
	c, err := contract.Load(root.Contract())
	if err != nil {
		return nil, fmt.Errorf("authorize: %w (run \"pika init\" or \"pika adopt\" first)", err)
	}
	resolved, err := profiles.Resolve(c.Profiles)
	if err != nil {
		return nil, fmt.Errorf("authorize: %w", err)
	}
	gates, err := verify.FromProfiles(resolved.Checks, c.Commands)
	if err != nil {
		return nil, fmt.Errorf("authorize: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, g := range gates {
		if len(g.Cmd) == 0 || g.Cmd[0] == "*" {
			continue
		}
		if seen[g.Cmd[0]] {
			continue
		}
		seen[g.Cmd[0]] = true
		out = append(out, g.Cmd[0])
	}
	sort.Strings(out)
	return out, nil
}

// Render serializes the envelope to the YAML document written to
// .project/state/envelope.yaml.
func Render(env *envelope.Env) ([]byte, error) {
	data, err := yaml.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("authorize: %w", err)
	}
	header := "# Generated by \"pika authorize\". Local-only: .project/state/ is gitignored.\n" +
		"# Deny-by-default: anything absent here is refused.\n"
	return append([]byte(header), data...), nil
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/authorize/`
Expected: PASS

- [ ] **Step 5: Wire the command**

Create `cmd/pika/authorize.go` following `doctor.go`. Flags: `--scope` (default `project`), `--network`, `--credential`, `--github` (each repeatable, reusing the `profileFlags` repeatable-string type from `cmd/pika/init.go:12-20`), `--force`, `--json`, `--root`.

Behavior:
1. Build the envelope; a build error is exit 2.
2. Print the rendered document to stdout for review.
3. If the envelope file exists and `--force` was not given: report what would change and return 1 without writing.
4. `os.MkdirAll(root.StateDir(), 0o755)`, then write `root.Envelope()` with mode `0o600` — a capability grant is not world-readable.
5. Re-read and `envelope.Load(root.Dir(), root.Envelope())` to prove what landed is valid; a failure here is exit 1.

Register in `commands` after `explain`:

```go
	{
		name:    "authorize",
		summary: "generate the capability envelope agents need",
		usage:   "pika authorize [--scope read|project|repo] [--network <host>]... [--credential <name>]... [--github <scope>]... [--force] [--json] [--root <dir>]",
		run:     runAuthorize,
	},
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./cmd/pika/ ./internal/authorize/`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/authorize/ cmd/pika/
git commit -m "feat: pika authorize generates the capability envelope from a declared scope"
```

---

### Task 8: Close envelope enforcement on exec

**Files:**
- Modify: `internal/mcp/server.go` (`authorizeWrite` → `authorize`, gate `run_checks`, add the `unavailable` code)
- Test: `internal/mcp/server_test.go`
- Modify: `internal/e2e/e2e_init_test.go` (tool-name and error-code assertions)

**Interfaces:**
- Consumes: `envelope.KindExec`, `envelope.KindFSWrite`, `authorize.Build` (Task 7, in tests only).
- Produces: `func (s *server) authorize(kind, target string) *toolError` replacing `authorizeWrite` (which returns `*toolError`, not `error`); new error code constant `errUnavailable = "unavailable"`.

- [ ] **Step 1: Write the failing test**

```go
// append to internal/mcp/server_test.go

// run_checks spawns contract-declared subprocesses. Before M1.5 it did so
// with no exec authorization at all, while propose_decision needed
// permission to append a log line — the security gradient was inverted.
func TestRunChecksDeniedWithoutExecGrant(t *testing.T) {
	dir := t.TempDir()
	writeProjectWithRealGate(t, dir) // contract whose test gate is a real argv
	writeEnvelope(t, dir, "schema: 1\nallow:\n  fs_write: [.project]\n")

	resp := callTool(t, dir, "run_checks", map[string]any{"scope": "all"})
	if !strings.Contains(resp, "envelope_denied") {
		t.Fatalf("run_checks ran an unauthorized command:\n%s", resp)
	}
}

func TestRunChecksAllowedWithGeneratedEnvelope(t *testing.T) {
	dir := t.TempDir()
	writeProjectWithRealGate(t, dir)
	writeGeneratedEnvelope(t, dir) // authorize.Build + Render at scope project

	resp := callTool(t, dir, "run_checks", map[string]any{"scope": "all"})
	if strings.Contains(resp, "envelope_denied") {
		t.Fatalf("a generated project envelope failed to authorize its own gates:\n%s", resp)
	}
}

func TestApplyPlanReportsUnavailableNotInternal(t *testing.T) {
	dir := t.TempDir()
	writeProjectWithRealGate(t, dir)
	writeGeneratedEnvelope(t, dir)

	resp := callTool(t, dir, "apply_plan", map[string]any{})
	if !strings.Contains(resp, "unavailable") {
		t.Fatalf("apply_plan did not report the unavailable code:\n%s", resp)
	}
	if strings.Contains(resp, "\"internal\"") {
		t.Errorf("apply_plan still reports internal, which is indistinguishable from a real failure:\n%s", resp)
	}
}
```

Reuse the existing test helpers in `server_test.go` for spawning a session and calling a tool; add `writeProjectWithRealGate` and `writeGeneratedEnvelope` alongside them.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/mcp/ -run 'TestRunChecks|TestApplyPlan'`
Expected: FAIL — `run_checks` succeeds without an exec grant, and `apply_plan` reports `internal`.

- [ ] **Step 3: Implement**

Generalize the authorization helper at `internal/mcp/server.go:733`:

```go
// authorize is the single authorization choke point. Every mutating or
// executing tool passes through it before any filesystem or process
// effect, so denial is always fail-closed.
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
```

Replace every `authorizeWrite(p)` call with `authorize(envelope.KindFSWrite, p)`.

In `toolRunChecks` (`server.go:453`), after the gate list is built and before `verify.Run`, authorize each executable gate:

```go
	for _, g := range gates {
		if len(g.Cmd) == 0 {
			continue // in-process gate or skip; nothing is spawned
		}
		if terr := s.authorize(envelope.KindExec, strings.Join(g.Cmd, " ")); terr != nil {
			return nil, terr
		}
	}
```

Add the new code to the closed set at `server.go:55-61` and use it in `toolApplyPlan`:

```go
	errUnavailable = "unavailable"
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/mcp/`
Expected: PASS

- [ ] **Step 5: Update the duplicated assertions**

The expected tool-name list is hardcoded in two places (`internal/mcp/server_test.go:305` and `internal/e2e/e2e_init_test.go:467`). No tool is added or removed here, but the envelope-denied matrix at `e2e_init_test.go:500-538` now must include `run_checks`. Update it, and add a case asserting the `unavailable` code for `apply_plan`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/mcp/ ./internal/e2e/`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/mcp/ internal/e2e/
git commit -m "fix: authorize exec before run_checks spawns gates; distinguish unavailable from internal"
```

---

### Task 9: Real `check --changed`

**Files:**
- Create: `internal/changed/changed.go`
- Test: `internal/changed/changed_test.go`
- Modify: `internal/verify/verify.go` (delete the reserved warning; add a scope-skip reason)
- Modify: `cmd/pika/check.go` (flag help; wire the changed set)
- Test: `internal/verify/verify_test.go`

**Interfaces:**
- Consumes: `contract.Contract` (for `Packages`), `repopath.Root`.
- Produces: `changed.Files(root *repopath.Root) (*Set, error)`, `type Set struct{ Paths []string; Degraded bool; Reason string }`, `func (s *Set) SelectsPackage(pkgRoot string) bool`, `func (s *Set) Empty() bool`.

- [ ] **Step 1: Write the failing test**

```go
// internal/changed/changed_test.go
package changed

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Choaterboater/pika/internal/repopath"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestNoGitDegradesLoudly(t *testing.T) {
	root, err := repopath.At(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := Files(root)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if !set.Degraded {
		t.Fatal("Degraded = false outside a git repository")
	}
	if set.Reason == "" {
		t.Error("degradation carries no reason")
	}
}

func TestWorkingTreeChangesAreDetected(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, filepath.Join(dir, "a.txt"), "one\n")
	gitCommitAll(t, dir, "init")
	writeFile(t, filepath.Join(dir, "b.txt"), "two\n")

	root, _ := repopath.At(dir)
	set, err := Files(root)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if set.Degraded {
		t.Fatalf("unexpected degradation: %s", set.Reason)
	}
	found := false
	for _, p := range set.Paths {
		if p == "b.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Paths = %v, want it to include b.txt", set.Paths)
	}
}

func TestSelectsPackageByPrefix(t *testing.T) {
	set := &Set{Paths: []string{"apps/api/main.go"}}
	if !set.SelectsPackage("apps/api") {
		t.Error("SelectsPackage(apps/api) = false")
	}
	if set.SelectsPackage("apps/web") {
		t.Error("SelectsPackage(apps/web) = true, want false")
	}
	// Prefix matching must be path-segment aware, not string-prefix.
	if set.SelectsPackage("apps/ap") {
		t.Error("SelectsPackage(apps/ap) matched a partial segment")
	}
}

func TestEmptySetIsNotDegraded(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, filepath.Join(dir, "a.txt"), "one\n")
	gitCommitAll(t, dir, "init")

	root, _ := repopath.At(dir)
	set, err := Files(root)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if set.Degraded {
		t.Fatalf("clean tree reported as degraded: %s", set.Reason)
	}
	if !set.Empty() {
		t.Fatalf("Paths = %v, want empty", set.Paths)
	}
}
```

Add a local `writeFile` helper as in Task 1.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/changed/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

```go
// Package changed resolves the set of repository-relative paths modified
// relative to the merge base, so `pika check --changed` can narrow the
// ladder. Degradation is always explicit: narrowing verification by
// accident is the one failure mode that lets a regression through, so
// every uncertain case falls back to running everything.
package changed

import (
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/Choaterboater/pika/internal/repopath"
)

// Set is the resolved change set. Degraded means the set could not be
// computed and every gate must run.
type Set struct {
	Paths    []string `json:"paths"`
	Degraded bool     `json:"degraded"`
	Reason   string   `json:"reason,omitempty"`
}

// Empty reports whether nothing changed. A clean tree is not degraded.
func (s *Set) Empty() bool { return !s.Degraded && len(s.Paths) == 0 }

// SelectsPackage reports whether any changed path lies within pkgRoot.
// Matching is path-segment aware: "apps/ap" never matches "apps/api".
func (s *Set) SelectsPackage(pkgRoot string) bool {
	if s.Degraded {
		return true
	}
	clean := path.Clean(strings.ReplaceAll(pkgRoot, "\\", "/"))
	if clean == "." || clean == "" {
		return len(s.Paths) > 0
	}
	prefix := clean + "/"
	for _, p := range s.Paths {
		if p == clean || strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// Files computes the change set: everything differing from the merge base
// with the upstream default branch, plus staged and working-tree changes.
func Files(root *repopath.Root) (*Set, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return degraded("git is not on PATH"), nil
	}
	if out, err := git(root.Dir(), "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		return degraded("not inside a git work tree"), nil
	}
	if out, err := git(root.Dir(), "rev-parse", "--is-shallow-repository"); err == nil && strings.TrimSpace(out) == "true" {
		return degraded("shallow clone: no reliable merge base"), nil
	}

	seen := map[string]bool{}
	// Staged and unstaged changes always count.
	for _, args := range [][]string{
		{"diff", "--name-only", "--no-renames"},
		{"diff", "--name-only", "--no-renames", "--cached"},
		{"ls-files", "--others", "--exclude-standard"},
	} {
		out, err := git(root.Dir(), args...)
		if err != nil {
			return degraded("git " + strings.Join(args, " ") + " failed"), nil
		}
		collect(seen, out)
	}
	// Committed changes since the merge base, when one exists.
	if base, err := mergeBase(root.Dir()); err == nil && base != "" {
		out, err := git(root.Dir(), "diff", "--name-only", "--no-renames", base+"...HEAD")
		if err != nil {
			return degraded("git diff against the merge base failed"), nil
		}
		collect(seen, out)
	}

	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return &Set{Paths: paths}, nil
}

// mergeBase finds the fork point against the upstream tracking branch,
// falling back to origin's default branch. An empty result is not an
// error: a repository with one branch and no remote legitimately has no
// merge base, and the staged/working-tree diffs still apply.
func mergeBase(dir string) (string, error) {
	for _, ref := range []string{"@{upstream}", "origin/HEAD", "origin/main"} {
		if out, err := git(dir, "merge-base", "HEAD", ref); err == nil {
			return strings.TrimSpace(out), nil
		}
	}
	return "", nil
}

func collect(seen map[string]bool, out string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			seen[line] = true
		}
	}
}

func degraded(reason string) *Set {
	return &Set{Degraded: true, Reason: reason}
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/changed/`
Expected: PASS

- [ ] **Step 5: Wire it into check and delete the reserved warning**

In `internal/verify/verify.go`, delete the `--changed is reserved; M1 runs all gates` warning at lines 139-142 and update the `Scope` doc comment at 80-83, which currently states Changed is treated as All.

In `cmd/pika/check.go`:

- change the `--changed` flag help from `"changed-scope verification (reserved; M1 runs all gates)"` to `"run gates for packages touched since the merge base"`;
- when `scope == verify.Changed`, call `changed.Files(root)`;
- if the set is degraded, append a warning naming the reason and run every gate;
- if the contract declares no packages, any non-empty set runs every gate and an empty set skips gates 2-5;
- if packages are declared, mark a gate skipped with reason `"no changed files in scope"` when no `contract.Packages` root is selected;
- gate 1 (`contract`) always runs.

Add a verify test asserting the reserved warning is gone and that a scope skip carries the new reason, distinct from the discovery and cascade reasons.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/verify/ ./internal/changed/ ./cmd/pika/`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/changed/ internal/verify/ cmd/pika/
git commit -m "feat: changed-scope verification from a real git diff with explicit degradation"
```

---

### Task 10: Populate contract commands from pack hints

**Files:**
- Modify: `internal/initcmd/init.go` (`contractCommands`, around lines 314-319)
- Modify: `internal/apply/apply.go` (same policy when promoting drafts)
- Test: `internal/initcmd/init_test.go`
- Modify: `internal/initcmd/testdata/golden/*/.project/contract.yaml` (five golden trees)

**Interfaces:**
- Consumes: `profiles.Check.Hint`.
- Produces: `func commandsFromChecks(cs profiles.CheckSet, lookPath func(string) (string, error)) map[string]string` in `internal/initcmd`, injectable so golden tests stay deterministic across machines.

- [ ] **Step 1: Write the failing test**

```go
// append to internal/initcmd/init_test.go

// A fresh TypeScript repo used to pass `pika check` with all five gates
// skipped: typescript@1 declares every slot discovery-only, so the report
// was green while nothing was verified.
func TestCommandsPopulatedFromHintsWhenToolPresent(t *testing.T) {
	resolved, err := profiles.Resolve([]string{profiles.CoreRef, "typescript@1"})
	if err != nil {
		t.Fatal(err)
	}
	present := func(string) (string, error) { return "/usr/bin/stub", nil }

	got := commandsFromChecks(resolved.Checks, present)
	if got["test"] != "npm test" {
		t.Errorf("commands[test] = %q, want %q", got["test"], "npm test")
	}
	if got["lint"] != "npm run lint" {
		t.Errorf("commands[lint] = %q, want %q", got["lint"], "npm run lint")
	}
}

func TestCommandsOmittedWhenToolAbsent(t *testing.T) {
	resolved, err := profiles.Resolve([]string{profiles.CoreRef, "typescript@1"})
	if err != nil {
		t.Fatal(err)
	}
	absent := func(string) (string, error) { return "", errors.New("not found") }

	if got := commandsFromChecks(resolved.Checks, absent); len(got) != 0 {
		t.Fatalf("commands = %v, want empty when no tool is on PATH", got)
	}
}

// A slot with a real cmd (not a hint) is the pack's own command and must
// not be duplicated into the contract.
func TestExplicitPackCommandsAreNotCopied(t *testing.T) {
	resolved, err := profiles.Resolve([]string{profiles.CoreRef, "go@1"})
	if err != nil {
		t.Fatal(err)
	}
	present := func(string) (string, error) { return "/usr/bin/stub", nil }

	if got := commandsFromChecks(resolved.Checks, present)["test"]; got != "" {
		t.Errorf("commands[test] = %q; go@1 already declares cmd, the contract must not duplicate it", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/initcmd/ -run TestCommands`
Expected: FAIL — `commandsFromChecks` undefined.

- [ ] **Step 3: Implement**

```go
// commandsFromChecks fills contract.commands from pack hints for slots
// that are discovery sentinels whose suggested tool is actually present.
// Without this a fresh repository can pass `pika check` with every gate
// skipped — green while verifying nothing.
//
// lookPath is injected so golden tests stay deterministic regardless of
// what the authoring machine has installed.
func commandsFromChecks(cs profiles.CheckSet, lookPath func(string) (string, error)) map[string]string {
	slots := []struct {
		id    string
		check profiles.Check
	}{
		{"format", cs.Format},
		{"lint", cs.Lint},
		{"typecheck", cs.Typecheck},
		{"test", cs.Test},
		{"smoke", cs.Smoke},
	}
	out := map[string]string{}
	for _, s := range slots {
		// An explicit pack command already runs; duplicating it into the
		// contract would just create a second place to keep in sync.
		if len(s.check.Cmd) > 0 || !s.check.Discovery {
			continue
		}
		if len(s.check.Hint) == 0 {
			continue
		}
		if _, err := lookPath(s.check.Hint[0]); err != nil {
			continue
		}
		out[s.id] = strings.Join(s.check.Hint, " ")
	}
	return out
}
```

Call it from the existing contract-authoring path in `initcmd.Run` (replacing the discovery-skipping logic at `init.go:314-319`) with `exec.LookPath` as the real implementation, and apply the same policy in `internal/apply/apply.go` when promoting a draft contract. List every populated slot in the init manifest.

- [ ] **Step 4: Make the golden tests deterministic**

The golden trees embed `contract.yaml`, so hint population changes their content and `PATH` varies per machine. In `internal/initcmd/init_test.go`, inject a fixed `lookPath` that reports every tool present, and regenerate the five golden `contract.yaml` files to match. Record in the test file's comment why the stub exists.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/initcmd/ ./internal/apply/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/initcmd/ internal/apply/
git commit -m "feat: populate contract commands from pack hints when the tool is present"
```

---

### Task 11: pika adopts pika

**Files:**
- Create: `.project/contract.yaml`, `.project/profiles.lock`, `.project/exceptions.yaml` (generated, then committed)
- Create: `AGENTS.md`, `.github/workflows/ci.yml`, `review/adoption-review.md` (generated)
- Modify: `.gitignore`
- Modify: `README.md`, `docs/guides/usage.md` (document the five new commands)

**Interfaces:**
- Consumes: the whole binary.
- Produces: pika's own committed contract, and CI that runs `pika check --ci`.

- [ ] **Step 1: Build and adopt**

```bash
go build -o /tmp/pika ./cmd/pika
/tmp/pika adopt
```

Read `review/adoption-review.md`. Decide each proposed naming exception: rename, or record it with a rationale, an owner, and a review condition. `internal/` package names are single words by Go convention and will need recorded exceptions rather than renames.

- [ ] **Step 2: Apply**

```bash
/tmp/pika apply
/tmp/pika doctor
/tmp/pika check --all
```

Expected: `doctor` reports no error-severity findings except a missing envelope warning; `check --all` passes. Fix real findings; do not weaken a rule to make it pass.

- [ ] **Step 3: Verify the CI workflow invokes the kernel**

`.github/workflows/ci.yml` must build the binary and run `pika check --ci` on this repository, plus `go test ./... -count=1` and `CGO_ENABLED=0 go build ./...`. Confirm the workflow file the templates produced does this; extend it if it does not.

- [ ] **Step 4: Confirm state is gitignored**

```bash
git status --short --ignored .project/
```

Expected: `.project/state/` ignored; contract, lock and exceptions tracked.

- [ ] **Step 5: Document the new commands**

Add `doctor`, `explain`, `authorize`, `help`, and the `--root` flag to the command table in `README.md`, and add a section for each to `docs/guides/usage.md` in the existing style. Document the `authorize` scope table and state plainly that `.project/state/envelope.yaml` is local-only and never committed. Note the dot-segment naming exemption (spec §7) in `AGENTS.md`.

- [ ] **Step 6: Commit**

```bash
git add .project/ .github/ AGENTS.md review/ .gitignore README.md docs/
git commit -m "chore: adopt pika with pika; document the M1.5 command surface"
```

---

### Task 12: Full verification

**Files:** none modified unless a failure demands it.

- [ ] **Step 1: Full suite**

Run: `go test ./... -count=1`
Expected: PASS. Toolchain-absent skips are acceptable and are reported honestly by `toolchainAbsent` (`internal/e2e/e2e_init_test.go:58-81`).

- [ ] **Step 2: CGO-free build**

Run: `CGO_ENABLED=0 go build ./...`
Expected: success.

- [ ] **Step 3: Dependency floor**

Run: `go mod tidy && git diff --exit-code go.mod go.sum`
Expected: no diff. If `go.mod` gained a direct dependency, remove it — the constraint is two.

- [ ] **Step 4: Smoke the real binary from a subdirectory**

```bash
go build -o /tmp/pika ./cmd/pika
cd internal/verify
/tmp/pika doctor
/tmp/pika explain naming-catch-all
/tmp/pika check --changed
cd -
```

Expected: `doctor` resolves the repository root as the pika root, not `internal/verify`; `explain` prints rationale and remediation; `--changed` reports a real change set or a named degradation. This is the milestone's core ergonomic claim, exercised against the real binary.

- [ ] **Step 5: Smoke the authorize-to-MCP path**

```bash
/tmp/pika authorize --scope project
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_checks","arguments":{"scope":"all"}}}' \
  | /tmp/pika mcp
```

Expected: no `envelope_denied`. A generated envelope must authorize the gates it was generated from; if it does not, `authorize` and enforcement disagree and Task 7 or 8 is wrong.

- [ ] **Step 6: Commit any fixes**

```bash
git add -A
git commit -m "fix: M1.5 verification findings"
```

---

## Self-Review

**Spec coverage.** §6.1→Task 1; §6.2→Task 3; §6.3→Task 2; §6.4→Task 5; §6.5→Task 6; §6.6→Task 7; §6.7→Task 8; §6.8→Task 9; §6.9→Task 10; §6.10→Task 11; §7 digest decision→Task 6 Step 4, dot-segment decision→Task 11 Step 5; §9 testing→distributed across tasks; §10 completion→Task 12.

**Type consistency.** `repopath.Root` accessors are defined once in Task 1 and consumed unchanged in Tasks 4-11. `cliout.Write(w, command, ok, result)` keeps its argument order everywhere. `doctor.Run(root) *Report` and `explain.Lookup(id, resolved) (*Entry, error)` match every call. `authorize.Build(Options) (*envelope.Env, error)` and `Render(*envelope.Env) ([]byte, error)` are consistent between Task 7's implementation and Task 8's test helper. `changed.Files(root) (*Set, error)` matches its use in Task 9 Step 5. `envelope.Load` gains its `repoRoot` parameter in Task 4 and every later reference uses the two-argument form. `checks.CheckLock` is exported in Task 5 and consumed by Task 6 Step 4.

**Ordering.** Tasks 1 and 2 have no dependencies. Task 3 depends on neither but must precede Task 4, which threads `--root` through the signatures Task 3 normalized. Tasks 5, 6 and 7 each depend on 1, 2 and 4. Task 8 depends on 7 for its test helper. Tasks 9 and 10 are independent of 5-8 and could run in parallel with them, though they touch `cmd/pika/check.go` and `internal/initcmd` respectively — no overlap. Task 11 depends on everything. Task 12 is last.

**Known cost, accepted.** Task 6 rotates the registry digest, so `internal/profiles` snapshot fixtures and any lock fixtures regenerate. Task 10 rewrites all five golden `contract.yaml` files. Both are recorded in spec §7 and §9 and are the two places where a careless implementer will assume a test is broken rather than fixture-stale.
