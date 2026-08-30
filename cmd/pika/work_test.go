package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/workrec"
)

func workOut(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runWork(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestWorkRejectsUnknownFlag(t *testing.T) {
	code, _, stderrOut := workOut(t, "--unknown")
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr: %s", code, stderrOut)
	}
	if !strings.Contains(stderrOut, "flag provided but not defined") {
		t.Fatalf("stderr = %q, want flag error", stderrOut)
	}
}

// `pika work` takes exactly one goal. None leaves nothing to ask an
// agent for. Two is an operator who forgot the quotes, and taking the
// first word as the whole goal would spawn an agent against a goal
// nobody wrote — the one input a feature run cannot recover from being
// wrong about.
func TestWorkRequiresExactlyOneGoal(t *testing.T) {
	dir, _ := statusFixture(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "none", args: []string{"--root", dir}, want: "a goal is required"},
		{name: "two", args: []string{"add a health endpoint", "and metrics", "--root", dir}, want: "unexpected argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderrOut := workOut(t, tc.args...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stderr: %s", code, stderrOut)
			}
			if !strings.Contains(stderrOut, tc.want) || !strings.Contains(stderrOut, workUsage) {
				t.Fatalf("stderr = %q, want %q and the synopsis", stderrOut, tc.want)
			}
		})
	}
}

// An empty or whitespace-only goal is the same dead end as no goal at
// all, and it is the one a shell hands over silently: `pika work
// "$GOAL"` with GOAL unset is an empty positional, not a missing one.
// Passing it through would branch, spawn an agent and give it a prompt
// whose Goal section states nothing.
func TestWorkRejectsAnEmptyGoal(t *testing.T) {
	dir, _ := statusFixture(t)
	for _, goal := range []string{"", "   \t "} {
		code, _, stderrOut := workOut(t, goal, "--root", dir)
		if code != 2 {
			t.Fatalf("goal %q: exit = %d, want 2; stderr: %s", goal, code, stderrOut)
		}
		if !strings.Contains(stderrOut, "goal is empty") {
			t.Fatalf("goal %q: stderr = %q, want the empty-goal refusal", goal, stderrOut)
		}
	}
}

// stdlib flag stops at the first non-flag argument, so `pika work --json
// "<goal>"` and `pika work "<goal>" --json` have to mean the same thing.
// explain, status and resume all consume their positional between two
// parses; work does the same rather than inventing a second convention.
//
// Both orderings run in a directory that is not a git checkout, so the
// lifecycle refuses before it creates a record: no agent, no branch, no
// mutation. What is being asserted is that --json was parsed at all —
// an unparsed --json would put prose on stdout, and envelopeOf would
// fail to read an envelope out of it.
func TestWorkAcceptsFlagsOnEitherSideOfTheGoal(t *testing.T) {
	dir, _ := statusFixture(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "after the goal", args: []string{"add a health endpoint", "--json", "--root", dir}},
		{name: "before the goal", args: []string{"--json", "--root", dir, "add a health endpoint"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, stderrOut := workOut(t, tc.args...)
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stdout: %s stderr: %s", code, out, stderrOut)
			}
			env := envelopeOf(t, []byte(out), "work")
			if env.OK {
				t.Errorf("ok = true on a run that stopped:\n%s", out)
			}
		})
	}
}

// The goal is the entire input for work the ladder cannot describe, so
// the one thing `pika work` must get right is that the words the
// operator typed reach the agent. This runs the real command against a
// real repository and reads the prompt back out of the run record.
//
// The fixture's contract configures no agent, so the run stops exactly
// where the agent would be spawned: the branch, the bundle and the
// prompt are already on disk and no Codex process ever exists. That is
// what makes the assertion possible without a runtime — createHandoff
// writes prompt.md before it calls the runner.
//
// The green baseline is load-bearing too. A repair run would have
// stopped there; feature work goes on to the agent, because a passing
// ladder says nothing about whether a goal has been met.
func TestWorkCarriesTheGoalIntoTheHandoffPrompt(t *testing.T) {
	dir, root := improveFixture(t)
	const goal = "add a /healthz endpoint that returns 200"

	code, stdout, stderrOut := workOut(t, goal, "--root", dir)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (the fixture configures no agent); stdout: %s stderr: %s", code, stdout, stderrOut)
	}
	runs, err := workrec.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("records = %d, want the single run work just started", len(runs))
	}
	rec := runs[0]
	if rec.Kind != workrec.KindFeature {
		t.Errorf("kind = %q, want %q: `pika work` is feature work", rec.Kind, workrec.KindFeature)
	}
	if rec.Goal != goal {
		t.Errorf("recorded goal = %q, want %q", rec.Goal, goal)
	}
	if rec.Branch == "" {
		t.Error("branch is empty: a feature run does not stop on a green baseline")
	}
	prompt, err := os.ReadFile(filepath.Join(dir, ".project", "state", "work", rec.WorkID, "handoff", "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), goal) {
		t.Fatalf("prompt = %q, want it to state the goal %q", prompt, goal)
	}
}
