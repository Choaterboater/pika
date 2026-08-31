package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/improve"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/workrec"
)

func TestImproveRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runImprove([]string{"--unknown"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr = %q, want flag error", stderr.String())
	}
}

func TestHandoffRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runHandoff([]string{"--unknown"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr = %q, want flag error", stderr.String())
	}
}

// `pika handoff` used to mint `.project/state/handoffs/<unixnano>`: a
// bundle named after the clock, with no run identity, that nothing in the
// repository could read back. A standalone handoff is a short run, so it
// now gets the same durable record as `pika improve` and its bundle lives
// inside that record.
func TestHandoffRecordsTheRunThatOwnsItsBundle(t *testing.T) {
	dir, root := handoffFixture(t)
	report := &verify.Report{Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail, OutputTail: "unexpected semicolon"}}}

	handoff, workID, err := recordedHandoff(context.Background(), root, "builder", report, respondingRunner{"fixed lint"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, ".project", "state", "work", workID, "handoff"); handoff.Dir != want {
		t.Fatalf("bundle = %q, want the run record's own %q", handoff.Dir, want)
	}
	legacy := filepath.Join(dir, ".project", "state", "handoffs")
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) = %v, want not-exist: the anonymous bundle location is gone", legacy, err)
	}
	rec := openRecord(t, root, workID)
	if rec.Phase != workrec.PhaseHandoff || rec.Outcome != workrec.OutcomeComplete {
		t.Fatalf("phase = %q outcome = %q, want handoff and complete", rec.Phase, rec.Outcome)
	}
	if got := len(rec.Phases); got != 2 {
		t.Fatalf("phases = %+v, want the baseline it was given and the handoff it ran", rec.Phases)
	}
	if rec.Role != "builder" || rec.Runtime != "codex" {
		t.Fatalf("record = %+v, want the role and runtime that actually ran", rec)
	}
	if rec.Baseline == nil || len(rec.Baseline.Gates) != 1 {
		t.Fatalf("record baseline = %+v, want the report the handoff was built from", rec.Baseline)
	}
}

// A handoff whose agent failed is a blocked run, and the reason an
// operator needs is the agent's own error.
func TestHandoffRecordsBlockedWhenTheAgentFails(t *testing.T) {
	_, root := handoffFixture(t)
	report := &verify.Report{Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}}

	_, workID, err := recordedHandoff(context.Background(), root, "builder", report, refusingRunner{})
	if err == nil {
		t.Fatal("recordedHandoff error = nil, want the agent failure")
	}
	if workID == "" {
		t.Fatal("workID is empty: a failed handoff must still name its run")
	}
	rec := openRecord(t, root, workID)
	if rec.Outcome != workrec.OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", rec.Outcome)
	}
	if rec.Reason != err.Error() || !strings.Contains(rec.Reason, "codex refused") {
		t.Fatalf("reason = %q, want the returned error verbatim %q", rec.Reason, err.Error())
	}
	if rec.Phase != workrec.PhaseBaseline {
		t.Fatalf("phase = %q, want baseline: the handoff never completed", rec.Phase)
	}
}

func handoffFixture(t *testing.T) (string, *repopath.Root) {
	t.Helper()
	dir := t.TempDir()
	gitFixtureRepo(t, dir)
	gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", "initial")
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, root
}

func openRecord(t *testing.T, root *repopath.Root, workID string) workrec.Record {
	t.Helper()
	handle, err := workrec.Open(root, workID)
	if err != nil {
		t.Fatal(err)
	}
	return handle.Record()
}

// The work id is the only handle an operator has on the run that just
// finished: it names the record under .project/state/work, it names the
// receipt under .project/evidence, and it is the argument to `pika
// status`. --json carried it from the moment it existed; the default
// text output did not, which left anyone running the default invocation
// with exactly the unnamable run the durable record exists to abolish.
func TestImproveTextOutputNamesTheRunItStarted(t *testing.T) {
	dir, root := improveFixture(t)

	var stdout, stderr bytes.Buffer
	if code := runImprove([]string{"--root", dir}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0; stdout: %s stderr: %s", code, stdout.String(), stderr.String())
	}
	runs, err := workrec.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("records = %d, want the single run improve just started", len(runs))
	}
	if want := "run " + runs[0].WorkID; !strings.Contains(stdout.String(), want) {
		t.Fatalf("text output = %q, want it to name %q", stdout.String(), want)
	}
}

// Only the green-baseline branch above is reachable without spawning a
// real agent, so the other two are exercised directly. Every branch is
// one reformat away from dropping the id again — which is exactly how it
// went missing while --json kept carrying it.
func TestImproveTextBranchesAllNameTheRun(t *testing.T) {
	cases := []struct {
		name   string
		result improve.Result
		err    error
	}{
		{
			name:   "baseline passed",
			result: improve.Result{WorkID: "20260830-repair-aaaaaaaa", ChecksBefore: &verify.Report{Pass: true}},
		},
		{
			name:   "verified fixes committed",
			result: improve.Result{WorkID: "20260830-repair-bbbbbbbb", Branch: defaultImproveBranch, Commit: "abc1234", ChecksBefore: &verify.Report{}},
		},
		{
			name:   "stopped on branch",
			result: improve.Result{WorkID: "20260830-feature-cccccccc", Branch: defaultImproveBranch, Handoff: improve.Handoff{Dir: "/tmp/bundle"}},
			err:    errors.New("improve: post-handoff checks failed; changes left uncommitted"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			printRunResult(&stdout, "improve", tc.result, tc.err)
			if want := "; run " + tc.result.WorkID; !strings.Contains(stdout.String(), want) {
				t.Fatalf("output = %q, want the run clause %q", stdout.String(), want)
			}
		})
	}
}

// A run refused before its record exists has no id at all: improve.Run
// returns a zero Result. Printing the clause anyway would emit a bare
// `run ` — an anonymous run wearing the costume of a named one — so this
// exit says plainly that nothing was created.
func TestImproveTextRefusalBeforeTheRunStartedNamesNoRun(t *testing.T) {
	var stdout bytes.Buffer
	printRunResult(&stdout, "improve", improve.Result{}, improve.ErrDirtyTree)

	got := stdout.String()
	if strings.Contains(got, "; run ") {
		t.Fatalf("output = %q, want no run clause: the run never started", got)
	}
	if !strings.Contains(got, "refused before the run started") {
		t.Fatalf("output = %q, want it to say nothing was created", got)
	}
}

// Regression, and the reason this predicate had to change. The printer
// decided "nothing was attempted" from result.ChecksBefore.Pass, which
// is the deciding fact for repair work only: the lifecycle returns early
// on a green baseline just when the kind is repair, so a delivered
// FEATURE run keeps ChecksBefore.Pass == true. It was reported at exit 0
// as "baseline checks passed; no branch or handoff created" — plausible
// prose about a run that had branched, spawned an agent and committed.
//
// It was unreachable while the CLI never set Kind. `pika work` sets it,
// so this is now a live path, and the predicate is what the run DID: a
// run that has a branch attempted work. internal/improve/receipt.go
// names the same predicate attemptedWork, so the two agree by
// construction rather than by coincidence.
func TestDeliveredFeatureRunIsNotReportedAsNoBranch(t *testing.T) {
	var stdout bytes.Buffer
	printRunResult(&stdout, "work", improve.Result{
		WorkID:       "20260830-feature-deadbeef",
		Branch:       defaultImproveBranch,
		Commit:       "abc1234",
		ChangedFiles: []string{"internal/api/health.go"},
		ChecksBefore: &verify.Report{Pass: true},
		ChecksAfter:  &verify.Report{Pass: true},
	}, nil)

	got := stdout.String()
	if strings.Contains(got, "no branch") {
		t.Fatalf("output = %q: a run that branched, ran an agent and committed was reported as creating no branch", got)
	}
	for _, want := range []string{defaultImproveBranch, "abc1234", "internal/api/health.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("output = %q, want it to name %q", got, want)
		}
	}
}

// The mirror image: a run that stopped before it ever branched must not
// be reported as a no-op either. Branch alone cannot tell those apart —
// both have none — so the error is read first, and the branch clause
// degrades to a dash rather than printing "stopped on branch ".
func TestRunStoppedBeforeBranchingIsNotReportedAsNoOp(t *testing.T) {
	var stdout bytes.Buffer
	printRunResult(&stdout, "improve", improve.Result{WorkID: "20260830-repair-eeeeeeee"},
		errors.New("improve: baseline checks: check: no contract"))

	got := stdout.String()
	if strings.Contains(got, "no branch or handoff created") {
		t.Fatalf("output = %q: the run stopped, it did not find nothing to do", got)
	}
	if !strings.Contains(got, "stopped on branch -") {
		t.Fatalf("output = %q, want the absent branch shown as a dash", got)
	}
}

// improveFixture is a clean, adopted, committed repository whose ladder
// passes, which is the one end-to-end path `pika improve` can take
// without spawning an agent.
func improveFixture(t *testing.T) (string, *repopath.Root) {
	t.Helper()
	dir := t.TempDir()
	gitFixtureRepo(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `schema: 1
project:
  name: fixture
  topology: single
profiles: [core@1]
packages:
  api:
    root: apps/api
    profiles: [core@1]
github:
  merge: squash
evidence:
  publish: sanitized
commands:
  test: "true"
`
	if err := os.WriteFile(filepath.Join(dir, ".project", "contract.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := profiles.WriteLock(filepath.Join(dir, ".project", "profiles.lock"), []string{"core@1"}); err != nil {
		t.Fatal(err)
	}
	// improve refuses a dirty tree, so the fixture is committed and the
	// private state directory the run is about to write is ignored.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".project/state/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "fixture")
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, root
}

type respondingRunner struct{ response string }

func (r respondingRunner) Run(_ context.Context, _, _, outputPath string) error {
	return os.WriteFile(outputPath, []byte(r.response), 0o600)
}

type refusingRunner struct{}

func (refusingRunner) Run(_ context.Context, _, _, outputPath string) error {
	if err := os.WriteFile(outputPath, []byte("partial work\n"), 0o600); err != nil {
		return err
	}
	return errors.New("codex refused")
}

// The stopped report is the whole of what an operator gets from a failed
// run, and it was wrong about where the run stopped in two separate
// ways: `stopped on branch -` for a run that had not branched yet, and
// `handoff: ` with nothing after it for one that had no bundle. M3 and
// M4 both closed defects of exactly that kind — a report that is wrong
// about where it stopped is worse than no report — so this pins both
// printers over every shape a stopped run can have.
func TestStoppedReportNeverPrintsAnEmptyBranchOrAnEmptyHandoffPath(t *testing.T) {
	printers := []struct {
		name  string
		print func(*bytes.Buffer, improve.Result, error)
	}{
		{"improve", func(out *bytes.Buffer, r improve.Result, err error) { printRunResult(out, "improve", r, err) }},
		{"resume", func(out *bytes.Buffer, r improve.Result, err error) { printResumeResult(out, r, err) }},
	}
	cases := []struct {
		name   string
		result improve.Result
		branch string
	}{
		{
			// The reported run: it died in the handoff, before it had a
			// branch of its own or a bundle to point at.
			name:   "stopped before branching, with no bundle",
			result: improve.Result{WorkID: "20260830-repair-aaaaaaaa", StoppedOn: "main"},
			branch: "main",
		},
		{
			name: "stopped on the branch it created",
			result: improve.Result{
				WorkID: "20260830-repair-bbbbbbbb", Branch: defaultImproveBranch,
				StoppedOn: defaultImproveBranch, Handoff: improve.Handoff{Dir: "/tmp/bundle"},
			},
			branch: defaultImproveBranch,
		},
		{
			// The two branches disagree, and Git's is the true one: the
			// agent switched away during its handoff.
			name: "the agent left the repository somewhere else",
			result: improve.Result{
				WorkID: "20260830-feature-cccccccc", Branch: defaultImproveBranch,
				StoppedOn: "main", Handoff: improve.Handoff{Dir: "/tmp/bundle"},
			},
			branch: "main",
		},
	}
	for _, printer := range printers {
		for _, tc := range cases {
			t.Run(printer.name+"/"+tc.name, func(t *testing.T) {
				var stdout bytes.Buffer
				printer.print(&stdout, tc.result, errors.New("improve: post-handoff checks failed"))
				got := stdout.String()

				if !strings.Contains(got, "stopped on branch "+tc.branch+";") {
					t.Errorf("output = %q, want it to name the branch %q the run was actually on", got, tc.branch)
				}
				if strings.Contains(got, "stopped on branch -") {
					t.Errorf("output = %q: the branch is known, so the placeholder is a lie", got)
				}
				for _, line := range strings.Split(got, "\n") {
					if strings.TrimSpace(line) == "handoff:" {
						t.Errorf("output = %q: an empty bundle path reads as a value that got lost", got)
					}
				}
				if want := "handoff: " + tc.result.Handoff.Dir; tc.result.Handoff.Dir != "" && !strings.Contains(got, want) {
					t.Errorf("output = %q, want %q", got, want)
				}
				if tc.result.Handoff.Dir == "" && strings.Contains(got, "handoff:") {
					t.Errorf("output = %q, want no handoff line at all: this run made no bundle", got)
				}
			})
		}
	}
}
