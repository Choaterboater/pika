# pika do routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `pika do ["<goal>"]`, a command that deterministically dispatches to `adopt`, `improve`, or `work` based on whether a live contract exists, whether an unapplied draft exists, and whether a goal was given — with zero model call and zero new failure surface, per `docs/superpowers/specs/2026-09-01-pika-do-routing-design.md`.

**Architecture:** One new file, `cmd/pika/do.go`, registered in `main.go`'s existing `commands` table like every other command. `runDo` parses its own flags/positional, resolves the root via the existing `resolveRoot` helper, stats the two state-file paths directly (never `Root.Origin()`, which collapses to `"explicit"` under `--root` regardless of contents), and dispatches by calling the target command's own registered `run` function via `lookup(name)` — the exact mechanism `main.go`'s top-level `dispatch` already uses. `do` adds no new package, schema, or model call.

**Tech Stack:** Go stdlib `flag`, `os`, `path/filepath`; existing internal packages `internal/repopath` (already imported by other `cmd/pika` files) — no new dependency.

## Global Constraints

- No new contract schema, no new adapter, no new model-call machinery (spec §2.4, §3).
- The command surface moves from 19 to 20 exactly once; `do` gains no flags beyond `--branch`/`--agent`/`--json`/`--root` (spec §3, "No new commands beyond `do` itself").
- Routing never inspects `Root.Origin()` for the governance decision — only direct stats on `root.Contract()`/`root.ContractDraft()` (spec §4, §5.1).
- `do --json`'s stdout is byte-identical to running the dispatched command directly with `--json`; the routing rationale goes to stderr only (spec §5.2).
- Usage errors exit 2; every other exit code is the dispatched command's own, verbatim (spec §5.3).

---

### Task 1: Register `do` and handle usage errors

**Files:**
- Create: `cmd/pika/do.go`
- Modify: `cmd/pika/main.go:181` (insert a new `commands` entry after `work`, before `resume`)
- Test: `cmd/pika/do_test.go`

**Interfaces:**
- Consumes: `resolveRoot(explicit string) (*repopath.Root, error)` (`cmd/pika/root.go:23`), `rootFlagUsage` (`cmd/pika/root.go:11`), `fail(jsonOut bool, stdout, stderr io.Writer, name, code, message string) int` (`cmd/pika/main.go:70`), `codeUsage`/`codeConfig` (`cmd/pika/main.go:30-31`).
- Produces: `runDo(args []string, stdin io.Reader, stdout, stderr io.Writer) int` — the registered handler every later task in this plan extends. `doUsage` (string constant) — the one-line synopsis printed beside a usage error, matching `workUsage`'s role at `cmd/pika/work.go:18`.

- [ ] **Step 1: Write the failing tests**

```go
// cmd/pika/do_test.go
package main

import (
	"bytes"
	"strings"
	"testing"
)

func doOut(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runDo(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestDoRejectsUnknownFlag(t *testing.T) {
	code, _, stderrOut := doOut(t, "--unknown")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderrOut, "flag provided but not defined") {
		t.Errorf("stderr = %q, want it to name the unknown flag", stderrOut)
	}
}

// Two positionals is almost always an unquoted goal. Taking the first
// word and routing on it would dispatch against a goal nobody wrote, so
// the whole invocation is refused instead — the same rule `pika work`
// enforces at cmd/pika/work.go:62-68.
func TestDoRejectsMoreThanOneGoal(t *testing.T) {
	code, _, stderrOut := doOut(t, "add", "a", "feature")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderrOut, "one quoted string") {
		t.Errorf("stderr = %q, want the unquoted-goal refusal", stderrOut)
	}
}

// `pika do "$GOAL"` with GOAL unset is an empty positional, not a
// missing one — it must be refused the same way an empty `pika work`
// goal is, at cmd/pika/work.go:69-75.
func TestDoRejectsAnEmptyGoal(t *testing.T) {
	code, _, stderrOut := doOut(t, "   ")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderrOut, "empty") {
		t.Errorf("stderr = %q, want the empty-goal refusal", stderrOut)
	}
}

// Every registered command must accept the --root flag its usage string
// advertises (cmd/pika/root_test.go:71-92 already checks this for every
// entry in `commands`, so registering do there is what this proves).
func TestDoIsRegistered(t *testing.T) {
	c, ok := lookup("do")
	if !ok {
		t.Fatal(`lookup("do") found nothing: register it in commands`)
	}
	if !strings.Contains(c.usage, "--root <dir>") {
		t.Errorf("usage = %q, want it to advertise --root", c.usage)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/pika/... -run TestDo -v`
Expected: FAIL — `runDo` and `lookup("do")` do not exist yet (build failure naming the undefined symbol).

- [ ] **Step 3: Write the minimal implementation**

```go
// cmd/pika/do.go
package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// doUsage is the one-line synopsis printed beside a usage error, matching
// workUsage's role at cmd/pika/work.go:18.
const doUsage = `usage: pika do ["<goal>"] [--branch <name>] [--agent <name>] [--json] [--root <dir>]`

// runDo implements `pika do ["<goal>"] [--branch <name>] [--agent <name>]
// [--json] [--root <dir>]`: it dispatches to the correct existing
// command for the repository's current state instead of requiring the
// operator to already know which one applies (design spec
// docs/superpowers/specs/2026-09-01-pika-do-routing-design.md).
//
// Exit codes: 2 for a usage error; every other code is whichever of
// adopt/improve/work got dispatched to, returned verbatim.
func runDo(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("do", flag.ContinueOnError)
	fs.SetOutput(stderr)
	branch := fs.String("branch", defaultImproveBranch, "local branch for the verified commit")
	agent := fs.String("agent", "builder", "contract agent name")
	jsonOut := fs.Bool("json", false, "emit the dispatched command's result as JSON")
	rootFlag := fs.String("root", "", rootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// stdlib flag stops at the first non-flag argument, so the goal is
	// consumed between two parses the way work, explain, status and
	// resume all consume theirs (cmd/pika/work.go:47-61).
	rest := fs.Args()
	var goal string
	if len(rest) > 0 {
		goal = rest[0]
		if err := fs.Parse(rest[1:]); err != nil {
			return 2
		}
		if fs.NArg() != 0 {
			return doUsageError(*jsonOut, stdout, stderr,
				fmt.Sprintf("unexpected argument %q; the goal is one quoted string", fs.Arg(0)))
		}
		if strings.TrimSpace(goal) == "" {
			return doUsageError(*jsonOut, stdout, stderr, "the goal is empty; state the work in one quoted string")
		}
	}
	_, _, _, _ = branch, agent, jsonOut, rootFlag // wired to dispatch in Task 2-4
	return 0
}

// doUsageError reports a wrong invocation of do and adds the synopsis
// for a human. With --json the envelope is the whole answer, so the
// synopsis is not printed and stderr stays empty — matching
// workUsageError at cmd/pika/work.go:114-123.
func doUsageError(jsonOut bool, stdout, stderr io.Writer, message string) int {
	if !jsonOut {
		fmt.Fprintln(stderr, doUsage)
	}
	return fail(jsonOut, stdout, stderr, "do", codeUsage, message)
}
```

Also modify `cmd/pika/main.go`: insert a new entry into the `commands` slice, immediately after the `"work"` entry (`cmd/pika/main.go:169-174`) and before `"resume"` (`cmd/pika/main.go:175-180`):

```go
	{
		name:    "do",
		summary: "route a stated goal (or none) to adopt, improve, or work, from repository state",
		usage:   `pika do ["<goal>"] [--branch <name>] [--agent <name>] [--json] [--root <dir>]`,
		run:     runDo,
	},
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/pika/... -run TestDo -v`
Expected: PASS — all four tests green.

- [ ] **Step 5: Commit**

```bash
git add cmd/pika/do.go cmd/pika/do_test.go cmd/pika/main.go
git commit -m "feat(cmd): register pika do, parse goal and flags"
```

---

### Task 2: Dispatch to `adopt` when neither state file exists

**Files:**
- Modify: `cmd/pika/do.go`
- Test: `cmd/pika/do_test.go`

**Interfaces:**
- Consumes: `lookup(name string) (command, bool)` (`cmd/pika/main.go:202`), `os.Stat` on `root.Contract()` / `root.ContractDraft()` (`internal/repopath/repopath.go:95-100`).
- Produces: `dispatch(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) int` — a small helper this and every later task in this file reuse.

- [ ] **Step 1: Write the failing test**

```go
// cmd/pika/do_test.go — append
import (
	"os"
	"path/filepath"
)

// A bare directory — no contract, no draft, no git even — is the
// ungoverned case: do must dispatch to adopt, which writes the two
// draft proposal files. This is the same fixture shape
// TestEveryCommandAcceptsRootFlag already uses for "no contract, no
// git" (cmd/pika/root_test.go:71-76).
func TestDoDispatchesToAdoptWhenUngoverned(t *testing.T) {
	dir := t.TempDir()
	code, _, stderrOut := doOut(t, "--root", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (adopt on a bare directory succeeds); stderr: %s", code, stderrOut)
	}
	if _, err := os.Stat(filepath.Join(dir, ".project", "contract.yaml.draft")); err != nil {
		t.Errorf("draft contract missing: adopt was not actually dispatched: %v", err)
	}
	if !strings.Contains(stderrOut, "adopt") {
		t.Errorf("stderr = %q, want the routing rationale to name adopt", stderrOut)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/pika/... -run TestDoDispatchesToAdoptWhenUngoverned -v`
Expected: FAIL — exit is 0 already (the Task 1 stub always returns 0), but the draft file is missing because nothing dispatches yet.

- [ ] **Step 3: Write the minimal implementation**

Replace the `_, _, _, _ = branch, agent, jsonOut, rootFlag` placeholder line in `runDo` (`cmd/pika/do.go`) with:

```go
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "do", codeConfig, err.Error())
	}
	_, contractErr := os.Stat(root.Contract())
	_, draftErr := os.Stat(root.ContractDraft())
	contractExists := contractErr == nil
	draftExists := draftErr == nil

	switch {
	case contractExists:
		// wired in Task 4
	case draftExists:
		// wired in Task 3
	default:
		fmt.Fprintln(stderr, "routing: no live contract or draft, dispatching to adopt")
		return dispatch("adopt", passthroughArgs(*jsonOut, *rootFlag), stdin, stdout, stderr)
	}
	return 0
```

Add the `os` import to `cmd/pika/do.go`'s import block, and add the shared helpers at the bottom of the file:

```go
// dispatch runs a registered command's own handler directly — the same
// call main.go's top-level dispatch makes (cmd/pika/main.go:216-231).
// do never re-implements adopt/improve/work's own logic; it only
// decides which one to call and with what argv.
func dispatch(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c, ok := lookup(name)
	if !ok {
		// Unreachable outside a typo in this file: name is always one of
		// the three literal command names below.
		fmt.Fprintf(stderr, "pika do: internal error: no such command %q\n", name)
		return 1
	}
	return c.run(args, stdin, stdout, stderr)
}

// passthroughArgs builds adopt's argv: adopt takes --json and --root
// only (cmd/pika/adopt.go:20-27), never --branch/--agent.
func passthroughArgs(jsonOut bool, rootVal string) []string {
	var out []string
	if jsonOut {
		out = append(out, "--json")
	}
	if rootVal != "" {
		out = append(out, "--root", rootVal)
	}
	return out
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/pika/... -run TestDoDispatchesToAdoptWhenUngoverned -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/pika/do.go cmd/pika/do_test.go
git commit -m "feat(cmd): do dispatches to adopt when ungoverned"
```

---

### Task 3: Print guidance, dispatch nothing, when only a draft exists

**Files:**
- Modify: `cmd/pika/do.go`
- Test: `cmd/pika/do_test.go`

**Interfaces:**
- Consumes: `root.ContractDraft() string` (`internal/repopath/repopath.go:100`).
- Produces: nothing new — this branch never calls `dispatch`.

- [ ] **Step 1: Write the failing test**

```go
// cmd/pika/do_test.go — append

// An unapplied draft is not an error state, and adopt.Preview never
// checks for one (internal/adopt/adopt.go:240-244) — re-running adopt
// here would silently regenerate the draft the operator may already
// have reviewed. do must print guidance instead of dispatching.
func TestDoPrintsGuidanceWhenOnlyADraftExists(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	draftPath := filepath.Join(dir, ".project", "contract.yaml.draft")
	if err := os.WriteFile(draftPath, []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdoutOut, _ := doOut(t, "--root", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0: an unapplied draft is not an error", code)
	}
	if !strings.Contains(stdoutOut, draftPath) {
		t.Errorf("stdout = %q, want it to name the draft path", stdoutOut)
	}
	if !strings.Contains(stdoutOut, "pika apply") {
		t.Errorf("stdout = %q, want it to suggest `pika apply`", stdoutOut)
	}
	// Nothing was dispatched: the draft's bytes are untouched, proving
	// adopt never ran and regenerated it.
	got, err := os.ReadFile(draftPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "placeholder" {
		t.Errorf("draft = %q, want it untouched", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/pika/... -run TestDoPrintsGuidanceWhenOnlyADraftExists -v`
Expected: FAIL — the current `case draftExists:` branch does nothing but fall through to `return 0`, so stdout is empty.

- [ ] **Step 3: Write the minimal implementation**

Replace the `case draftExists:` line's `// wired in Task 3` comment in `runDo` (`cmd/pika/do.go`) with:

```go
	case draftExists:
		fmt.Fprintf(stdout, "a draft already exists at %s — review it and run `pika apply`, or re-run `pika adopt` to regenerate it\n", root.ContractDraft())
		return 0
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/pika/... -run TestDoPrintsGuidanceWhenOnlyADraftExists -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/pika/do.go cmd/pika/do_test.go
git commit -m "feat(cmd): do prints guidance instead of re-adopting an unapplied draft"
```

---

### Task 4: Dispatch to `improve` or `work` when governed

**Files:**
- Modify: `cmd/pika/do.go`
- Test: `cmd/pika/do_test.go`

**Interfaces:**
- Consumes: `improveFixture(t *testing.T) (string, *repopath.Root)` (`cmd/pika/improve_test.go:253` — a clean, adopted, committed repository whose ladder passes), `workrec.List` / `workrec.KindFeature` (`internal/workrec`, already imported by `cmd/pika/work_test.go:10`).
- Produces: the complete `runDo` routing logic — no further tasks modify `do.go`'s routing switch.

- [ ] **Step 1: Write the failing tests**

```go
// cmd/pika/do_test.go — append
import "github.com/Choaterboater/pika/internal/workrec"

// improveFixture's baseline is green (cmd/pika/improve_test.go:250-252),
// so with no goal, do must dispatch to improve, and improve's own
// green-baseline short-circuit (internal/improve/improve.go:667-687)
// means it exits 0 with no run record at all — the clean, deterministic
// "nothing to repair" outcome.
func TestDoDispatchesToImproveWhenGovernedWithNoGoal(t *testing.T) {
	dir, root := improveFixture(t)
	code, _, stderrOut := doOut(t, "--root", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (green baseline, nothing to repair); stderr: %s", code, stderrOut)
	}
	if !strings.Contains(stderrOut, "improve") {
		t.Errorf("stderr = %q, want the routing rationale to name improve", stderrOut)
	}
	runs, err := workrec.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("records = %d, want none: a green baseline creates no run", len(runs))
	}
}

// Mirrors TestWorkCarriesTheGoalIntoTheHandoffPrompt
// (cmd/pika/work_test.go:121-153): the fixture configures no agent, so
// the run stops exactly where the agent would be spawned, with the
// branch, record and prompt already on disk.
func TestDoDispatchesToWorkWhenGovernedWithAGoal(t *testing.T) {
	dir, root := improveFixture(t)
	const goal = "add a /healthz endpoint that returns 200"

	code, _, stderrOut := doOut(t, goal, "--root", dir)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (the fixture configures no agent); stderr: %s", code, stderrOut)
	}
	if !strings.Contains(stderrOut, "work") {
		t.Errorf("stderr = %q, want the routing rationale to name work", stderrOut)
	}
	runs, err := workrec.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("records = %d, want the single run do just started", len(runs))
	}
	rec := runs[0]
	if rec.Kind != workrec.KindFeature {
		t.Errorf("kind = %q, want %q", rec.Kind, workrec.KindFeature)
	}
	if rec.Goal != goal {
		t.Errorf("recorded goal = %q, want %q", rec.Goal, goal)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/pika/... -run TestDoDispatchesTo -v`
Expected: FAIL — the `case contractExists:` branch does nothing but fall through to `return 0`; neither `improve` nor `work` is ever invoked, so no run record is created for the goal case (test two fails on `len(runs) != 1`).

- [ ] **Step 3: Write the minimal implementation**

Replace the `case contractExists:` line's `// wired in Task 4` comment in `runDo` (`cmd/pika/do.go`) with:

```go
	case contractExists:
		if goal == "" {
			fmt.Fprintln(stderr, "routing: no goal given, dispatching to improve")
			return dispatch("improve", passthroughArgs(*jsonOut, *rootFlag, *branch, *agent), stdin, stdout, stderr)
		}
		fmt.Fprintln(stderr, "routing: a goal was given, dispatching to work")
		return dispatch("work", append([]string{goal}, passthroughArgs(*jsonOut, *rootFlag, *branch, *agent)...), stdin, stdout, stderr)
```

`passthroughArgs` needs a second, variadic-flags form: replace its single definition with two overloads is not idiomatic Go, so extend the existing function's signature instead — update both its definition and Task 2's call site:

```go
// passthroughArgs builds the dispatched command's argv. adopt takes
// --json and --root only (cmd/pika/adopt.go:20-27); improve and work
// additionally take --branch and --agent (cmd/pika/improve.go:162-165;
// cmd/pika/work.go:43-46) — callers pass branch/agent as "" to omit
// them for adopt.
func passthroughArgs(jsonOut bool, rootVal, branchVal, agentVal string) []string {
	var out []string
	if branchVal != "" {
		out = append(out, "--branch", branchVal)
	}
	if agentVal != "" {
		out = append(out, "--agent", agentVal)
	}
	if jsonOut {
		out = append(out, "--json")
	}
	if rootVal != "" {
		out = append(out, "--root", rootVal)
	}
	return out
}
```

And update Task 2's `adopt` call site to match the new signature: `passthroughArgs(*jsonOut, *rootFlag, "", "")`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/pika/... -run TestDo -v`
Expected: PASS — every test in `do_test.go` green.

- [ ] **Step 5: Commit**

```bash
git add cmd/pika/do.go cmd/pika/do_test.go
git commit -m "feat(cmd): do dispatches to improve or work when governed"
```

---

### Task 5: Prove `--root` correctness does not depend on `Origin()`

**Files:**
- Test: `cmd/pika/do_test.go`

**Interfaces:**
- Consumes: everything Task 4 already produced. This task adds no new production code — it is the regression test spec §4/§6 calls for, proving the design decision (stat the two paths directly, never branch on `Root.Origin()`) actually holds.

- [ ] **Step 1: Write the failing test — expected to already pass; this step proves it**

```go
// cmd/pika/do_test.go — append

// repopath.At (what --root uses) tags Origin() "explicit" unconditionally
// and never inspects the directory (internal/repopath/repopath.go:66-79).
// A routing decision keyed on Origin() instead of a direct stat would
// treat every --root invocation as ungoverned regardless of whether a
// live contract sits there — this proves it does not, by running from a
// working directory discovery would resolve completely differently.
func TestDoWithExplicitRootIgnoresTheWorkingDirectory(t *testing.T) {
	dir, _ := improveFixture(t) // governed, green
	elsewhere := t.TempDir()    // no contract, no draft, no git
	t.Chdir(elsewhere)

	code, _, stderrOut := doOut(t, "--root", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (green baseline via explicit --root); stderr: %s", code, stderrOut)
	}
	if !strings.Contains(stderrOut, "improve") {
		t.Errorf("stderr = %q, want routing to improve — Origin() would say \"explicit\" either way,"+
			" so this only passes if do stats root.Contract() directly", stderrOut)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./cmd/pika/... -run TestDoWithExplicitRootIgnoresTheWorkingDirectory -v`
Expected: PASS already, since Task 2's implementation stats `root.Contract()`/`root.ContractDraft()` directly and never reads `root.Origin()`. A FAIL here would mean a regression was introduced between Task 2 and now — if it fails, re-read `runDo` and remove any dependency on `Origin()`.

- [ ] **Step 3: N/A — no implementation change expected**

- [ ] **Step 4: N/A — Step 2 is the verification**

- [ ] **Step 5: Commit**

```bash
git add cmd/pika/do_test.go
git commit -m "test(cmd): pin that do's routing survives an explicit --root"
```

---

### Task 6: Prove `--json` output is byte-identical to the dispatched command's own

**Files:**
- Test: `cmd/pika/do_test.go`

**Interfaces:**
- Consumes: `envelope` / `cliout` JSON shape already used elsewhere in this package's tests (e.g. `internal/e2e/e2e_init_test.go:175-177`'s `unwrap`; within `cmd/pika` itself, decode with `encoding/json` directly against the known field names).

- [ ] **Step 1: Write the failing test**

```go
// cmd/pika/do_test.go — append
import "encoding/json"

// do's --json output must be the dispatched command's own envelope,
// unmodified — a caller parsing it sees "command":"improve", not
// "command":"do", because that is what actually ran.
func TestDoJSONOutputIsTheDispatchedCommandsOwnEnvelope(t *testing.T) {
	dir, _ := improveFixture(t)
	code, stdoutOut, stderrOut := doOut(t, "--root", dir, "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderrOut)
	}
	var env struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
	}
	if err := json.Unmarshal([]byte(stdoutOut), &env); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\n%s", err, stdoutOut)
	}
	if env.Command != "improve" {
		t.Errorf(`envelope "command" = %q, want "improve"`, env.Command)
	}
	if !env.OK {
		t.Errorf("envelope ok = false, want true")
	}
	// The routing rationale must never land in the JSON stream itself.
	if strings.Contains(stdoutOut, "routing:") {
		t.Errorf("stdout contains the routing rationale, want it on stderr only:\n%s", stdoutOut)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails or passes**

Run: `go test ./cmd/pika/... -run TestDoJSONOutputIsTheDispatchedCommandsOwnEnvelope -v`
Expected: PASS already — Task 4's `dispatch` call forwards `stdout`/`stderr` straight through to `runImprove`, which writes its own envelope to `stdout` and nothing else; the routing line in `runDo` was written to `stderr` in every task so far. A FAIL here means the routing rationale was accidentally written to `stdout` somewhere — fix by routing every `fmt.Fprintln`/`fmt.Fprintf` rationale call in `runDo` to `stderr`, never `stdout`.

- [ ] **Step 3: N/A unless Step 2 failed**

- [ ] **Step 4: N/A unless Step 2 failed**

- [ ] **Step 5: Commit**

```bash
git add cmd/pika/do_test.go
git commit -m "test(cmd): pin that do --json passes through the dispatched envelope untouched"
```

---

### Task 7: End-to-end test against the real built binary

**Files:**
- Create: `internal/e2e/e2e_do_test.go`

**Interfaces:**
- Consumes: `runCLI(t *testing.T, dir string, wantExit int, args ...string) string` (`internal/e2e/e2e_init_test.go:215`), `copyFixtureTree(t *testing.T, fixture, dst string)` (`internal/e2e/e2e_adopt_test.go:15`, fixture `"go-mod"` already used by `internal/e2e/e2e_apply_test.go:36`).

- [ ] **Step 1: Write the failing tests**

```go
// internal/e2e/e2e_do_test.go
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EDoRoutesToAdopt closes the ungoverned path through the real
// binary: `pika do` on a fresh checkout writes the same adoption drafts
// `pika adopt` itself would.
func TestE2EDoRoutesToAdopt(t *testing.T) {
	dir := t.TempDir()
	copyFixtureTree(t, "go-mod", dir)

	runCLI(t, dir, 0, "do")

	for _, p := range []string{".project/contract.yaml.draft", ".project/profiles.lock.draft"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("do did not dispatch to adopt: %s missing: %v", p, err)
		}
	}
}

// TestE2EDoRoutesToWork closes the governed-with-a-goal path: adopt then
// apply first (matching TestE2EAdoptApply's own setup at
// internal/e2e/e2e_apply_test.go:34-54). A freshly adopted contract
// configures no builder agent, so the lifecycle still creates the
// branch and record (proving `work`, not `improve`, ran — a repair run
// would have stopped silently on the green-or-red baseline with no
// branch at all, per internal/improve/improve.go:667-687) and then
// stops exactly where the builder would be invoked, printed by
// printRunResult's exact "stopped on branch" wording
// (cmd/pika/improve.go:239) — the identical outcome
// TestWorkCarriesTheGoalIntoTheHandoffPrompt already gets from the
// equivalent in-process fixture (cmd/pika/work_test.go:126-128).
func TestE2EDoRoutesToWork(t *testing.T) {
	dir := t.TempDir()
	copyFixtureTree(t, "go-mod", dir)
	runCLI(t, dir, 0, "adopt")
	runCLI(t, dir, 0, "apply")

	out := runCLI(t, dir, 1, "do", "add a health check endpoint")
	if !strings.Contains(out, "stopped on branch") {
		t.Fatalf("do with a goal did not appear to run the work lifecycle:\n%s", out)
	}
}
```

- [ ] **Step 2: Run the tests to verify they pass**

Run: `go test ./internal/e2e/... -run TestE2EDoRoutes -v`
Expected: PASS — both `runDo` (Tasks 1-6) and the fixture behavior this relies on already exist; this task only adds coverage through the real binary, so a FAIL here means something about the *real* binary's behavior differs from the in-process tests, not that new code is needed. If `TestE2EDoRoutesToWork`'s exit code or text differs from what is written above, run `go build -o /tmp/pika-e2e-check ./cmd/pika && cd <fresh go-mod copy> && /tmp/pika-e2e-check adopt && /tmp/pika-e2e-check apply && /tmp/pika-e2e-check do "add a health check endpoint"` by hand, read the actual output, and correct the test to match reality — never adjust the assertion to hide an actual routing bug.

- [ ] **Step 3: Commit**

```bash
git add internal/e2e/e2e_do_test.go
git commit -m "test(e2e): pika do routes to adopt and to work through the real binary"
```

---

### Task 8: Document `pika do`

**Files:**
- Modify: `docs/guides/usage.md`

**Interfaces:**
- Consumes: nothing new. This task reads the finished command's own `--help`/usage output as its source of truth, not a re-description from memory.

- [ ] **Step 1: Read the section documenting `adopt`/`improve`/`work` today**

Run: `grep -n "^## " docs/guides/usage.md` to find the exact numbered section headers those three commands live under, and read that whole section before writing — the new text must sit next to them and match their existing tone, not invent a new style.

- [ ] **Step 2: Write the new section**

Insert a new subsection immediately after the `work` section (exact heading number depends on Step 1's findings — follow the existing numbering, do not renumber sections that come after it), covering:
- The command's purpose in one sentence: routes to `adopt`, `improve`, or `work` from repository state so the operator does not have to already know which applies.
- The exact three-way decision from spec §5.1, written in prose (no live contract or draft → `adopt`; only a draft → guidance, nothing runs; a live contract with no goal → `improve`; a live contract with a goal → `work "<goal>"`).
- One line stating what it deliberately does not do: no model call, no goal-content classification, no `skills` routing (spec §3) — link the design spec: `[2026-09-01-pika-do-routing-design.md](../superpowers/specs/2026-09-01-pika-do-routing-design.md)`.
- The exact command surface: `pika do ["<goal>"] [--branch <name>] [--agent <name>] [--json] [--root <dir>]`.

- [ ] **Step 3: Verify the doc renders correctly and cites real paths**

Run: `grep -n "pika-do-routing-design" docs/guides/usage.md` to confirm the relative link path resolves (the specs directory is `docs/superpowers/specs/`, one level up and over from `docs/guides/`).

- [ ] **Step 4: No test to run — this is a documentation-only task**

- [ ] **Step 5: Commit**

```bash
git add docs/guides/usage.md
git commit -m "docs(guides): document pika do"
```

---

## Final verification (run once, after Task 8)

```bash
gofmt -l .                    # expect no output
go build ./...                # expect success
go vet ./...                  # expect no output
go test ./... -count=1        # expect every package ok
```

Then build the binary and run pika's own ladder against this repository:

```bash
go build -o /tmp/pika-do-verify ./cmd/pika
/tmp/pika-do-verify check --all --root "$(pwd)" --json
```

Expect `"pass": true`, 6/6 gates, and no new warnings beyond the pre-existing `file-size-review` set (spec §7, item 6).
