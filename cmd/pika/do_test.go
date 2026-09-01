package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/workrec"
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

// With --json, the draft-exists path must produce a proper cliout
// envelope like every other do exit path, not the human prose guidance
// — the same contract TestDoJSONOutputIsTheDispatchedCommandsOwnEnvelope
// proves for the dispatched-command paths.
func TestDoPrintsJSONEnvelopeWhenOnlyADraftExists(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	draftPath := filepath.Join(dir, ".project", "contract.yaml.draft")
	if err := os.WriteFile(draftPath, []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdoutOut, stderrOut := doOut(t, "--root", dir, "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0: an unapplied draft is not an error; stderr: %s", code, stderrOut)
	}
	var env struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Result  struct {
			Draft string `json:"draft"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdoutOut), &env); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\n%s", err, stdoutOut)
	}
	if env.Command != "do" {
		t.Errorf(`envelope "command" = %q, want "do"`, env.Command)
	}
	if !env.OK {
		t.Errorf("envelope ok = false, want true")
	}
	if env.Result.Draft != draftPath {
		t.Errorf(`envelope "result.draft" = %q, want %q`, env.Result.Draft, draftPath)
	}
	if strings.Contains(stdoutOut, "review it and run") {
		t.Errorf("stdout contains the human-prose guidance, want JSON only:\n%s", stdoutOut)
	}
}

// improveFixture's baseline is green (cmd/pika/improve_test.go:250-252),
// so with no goal, do must dispatch to improve, and improve's own
// green-baseline short-circuit (internal/improve/improve.go:679-681)
// returns before a branch is ever created — the clean, deterministic
// "nothing to repair" outcome. improve.Run still creates and finalizes
// the run's record before the short-circuit is reached (improve.go:291,
// 901-908), so exactly one complete, branchless repair record remains.
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
	if len(runs) != 1 {
		t.Fatalf("records = %d, want the single no-op run improve recorded", len(runs))
	}
	rec := runs[0]
	if rec.Kind != workrec.KindRepair {
		t.Errorf("kind = %q, want %q", rec.Kind, workrec.KindRepair)
	}
	if rec.Outcome != workrec.OutcomeComplete {
		t.Errorf("outcome = %q, want %q", rec.Outcome, workrec.OutcomeComplete)
	}
	if rec.Branch != "" {
		t.Errorf("branch = %q, want none: the short-circuit returns before a branch is claimed", rec.Branch)
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
