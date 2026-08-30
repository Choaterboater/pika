package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
