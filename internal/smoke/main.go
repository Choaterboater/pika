// Command smoke is pika's rung-5 gate: the real-surface smoke test the
// verification ladder runs on every `pika check` (spec §12.6).
//
// It builds the pika binary from this module and drives it, as a
// subprocess, through the lifecycle an operator actually performs —
// init, check, improve, improve again over the branch the first run
// left, skills install and tamper, a corrupted lock, doctor — inside
// temp repositories that are removed when it ends. Every claim is about
// what the built binary did: an exit code, a payload it printed, a
// commit it made, a file on disk.
//
// It replaced `go run ./cmd/pika version`, which was rung 5 until today.
// That command printed a constant and could not fail, so the gate was a
// gate in name only, and every defect closed on 2026-08-30 — an agent
// invocation the real `codex` rejected before reading a byte, a leftover
// branch that killed every later run, a lock remedy that corrupted a
// correct repository — shipped behind a green ladder. None was findable
// by reading. All were found by running the product once.
//
// It makes no model call and touches no network: the agent boundary is
// internal/e2e's fake `codex`, put on PATH under the name pika spawns.
// So `pika check --ci` stays provably LLM-free with this gate in it.
//
// Run it directly:
//
//	go run ./internal/smoke
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

func main() {
	os.Exit(run(os.Stdout, os.Stderr))
}

// step is one lifecycle step: what it proves, whether this machine can
// run it, and the assertions themselves.
type step struct {
	// id names the step in the gate's output and in its failure.
	id string
	// proves is the one-line claim the step establishes. It is printed
	// beside every PASS so a green run says what it verified rather than
	// how many things it did.
	proves string
	// absent reports why this machine cannot run the step, or "" when it
	// can. A named skip is honest; a step that quietly passes because
	// its prerequisite is missing is the defect this program exists to
	// stop shipping.
	absent func() string
	run    func(*harness) error
}

// steps is the lifecycle, in the order an operator walks it.
var steps = []step{
	{
		id:     "init",
		proves: "a freshly initialized repository is green",
		run:    stepInit,
	},
	{
		id:     "improve",
		proves: "a failed gate is repaired by the agent and committed on a verified recheck",
		absent: gitAbsent,
		run:    stepImprove,
	},
	{
		id:     "improve-again",
		proves: "the branch a run leaves behind neither poisons the next run nor is written over",
		absent: gitAbsent,
		run:    stepImproveAgain,
	},
	{
		id:     "roles",
		proves: "one run spawns a claude builder and an omp reviewer, and the advisory review does not gate the commit",
		absent: gitAbsent,
		run:    stepRoles,
	},
	{
		id:     "skills",
		proves: "an installed projection is green, and a hand-edited kernel region fails check by name",
		run:    stepSkills,
	},
	{
		id:     "lock",
		proves: "a digest disagreement names both causes, and `pika version` supplies the comparison that settles it",
		run:    stepLock,
	},
	{
		id:     "doctor",
		proves: "`pika doctor` exits 0 on a healthy repository",
		run:    stepDoctor,
	},
}

// run is the whole gate. It returns the process exit code.
func run(stdout, stderr io.Writer) int {
	started := time.Now()
	if why := toolchainAbsent(); why != "" {
		fmt.Fprintf(stdout, "SKIP smoke: %s\n", why)
		return 0
	}
	h, err := newHarness()
	if err != nil {
		fmt.Fprintf(stderr, "smoke: harness: %v\n", err)
		return 1
	}
	defer func() {
		if err := h.close(); err != nil {
			fmt.Fprintf(stderr, "smoke: %v\n", err)
		}
	}()
	fmt.Fprintf(stdout, "smoke: built pika from %s into %s\n", h.root, h.dir)

	err = runSteps(steps, h, stdout)
	took := time.Since(started).Round(time.Millisecond)
	if err != nil {
		fmt.Fprintf(stderr, "\nsmoke: FAILED after %s\n%v\n", took, err)
		return 1
	}
	fmt.Fprintf(stdout, "smoke: %d steps in %s\n", len(steps), took)
	return 0
}

// runSteps executes the lifecycle in order and stops at the first step
// that fails.
//
// It stops rather than continuing because the steps are a lifecycle: a
// repository whose `init` is broken has nothing to say about `improve`,
// and a wall of consequential failures buries the one finding. Within a
// step it is the other way round — every assertion runs, and all of the
// disagreements are reported together.
func runSteps(steps []step, h *harness, out io.Writer) error {
	for _, s := range steps {
		if s.absent != nil {
			if why := s.absent(); why != "" {
				fmt.Fprintf(out, "SKIP %-14s %s\n", s.id, why)
				continue
			}
		}
		started := time.Now()
		err := s.run(h)
		took := time.Since(started).Round(time.Millisecond)
		if err != nil {
			fmt.Fprintf(out, "FAIL %-14s %9s\n", s.id, took)
			return fmt.Errorf("step %q, which should prove that %s:\n%w", s.id, s.proves, err)
		}
		fmt.Fprintf(out, "PASS %-14s %9s  %s\n", s.id, took, s.proves)
	}
	return nil
}

// toolchainAbsent reports why no step can run here.
//
// Both tools are prerequisites of the whole gate rather than of one
// step: `go` builds the binary under test, and every scaffolded
// repository's format gate is `gofmt -l .`, so without them there is
// nothing to smoke rather than something failing. `go` is normally
// present by construction — this program is started by `go run` — but a
// GOROOT without gofmt on PATH is a real configuration and it must say
// so rather than report a red format gate that is the machine's fault.
func toolchainAbsent() string {
	for _, tool := range []string{"go", "gofmt"} {
		if _, err := exec.LookPath(tool); err != nil {
			return tool + " is not in PATH, so no scaffolded repository's ladder can run here"
		}
	}
	return ""
}

// gitAbsent reports why the repair lifecycle cannot be exercised here.
// It branches, commits and reads HEAD, so without git there is nothing
// to test.
func gitAbsent() string {
	if _, err := exec.LookPath("git"); err != nil {
		return "git is not in PATH, so no run can branch or commit here"
	}
	return ""
}
