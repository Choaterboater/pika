package main

import (
	"bytes"
	"os"
	"path/filepath"
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
