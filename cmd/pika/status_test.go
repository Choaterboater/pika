package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/workrec"
)

// statusFixture is a repository root that has never run anything. It is
// deliberately not a git checkout: status reads records off disk and
// executes nothing, so needing a working tree would be a dependency it
// does not have.
func statusFixture(t *testing.T) (string, *repopath.Root) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, root
}

// seedRun writes one run record and stamps record.json with savedAt,
// which is the clock workrec.List orders by. Stamping it explicitly is
// what makes the newest-first assertion about ordering rather than about
// how fast the test machine created two directories.
func seedRun(t *testing.T, root *repopath.Root, rec workrec.Record, savedAt time.Time) string {
	t.Helper()
	handle, err := workrec.Create(root, rec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(handle.Dir(), "record.json")
	if err := os.Chtimes(path, savedAt, savedAt); err != nil {
		t.Fatal(err)
	}
	return path
}

func statusOut(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runStatus(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestStatusRejectsUnknownFlag(t *testing.T) {
	code, _, stderrOut := statusOut(t, "--unknown")
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr: %s", code, stderrOut)
	}
	if !strings.Contains(stderrOut, "flag provided but not defined") {
		t.Fatalf("stderr = %q, want flag error", stderrOut)
	}
}

// The listing is ordered by when each record was last saved, so the run
// an operator just finished is the one at the top of the screen.
func TestStatusListsRunsNewestFirst(t *testing.T) {
	dir, root := statusFixture(t)
	now := time.Now()
	seedRun(t, root, workrec.Record{
		WorkID:  "20260829-repair-aaaa1111",
		Kind:    workrec.KindRepair,
		Phase:   workrec.PhaseDeliver,
		Branch:  "chore/pika-improve",
		Commit:  "c0ffee1",
		Outcome: workrec.OutcomeComplete,
	}, now.Add(-2*time.Hour))
	seedRun(t, root, workrec.Record{
		WorkID:  "20260830-feature-bbbb2222",
		Kind:    workrec.KindFeature,
		Goal:    "add a status command",
		Phase:   workrec.PhaseHandoff,
		Outcome: workrec.OutcomeBlocked,
	}, now.Add(-1*time.Hour))

	code, stdoutOut, stderrOut := statusOut(t, "--root", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderrOut)
	}
	newer := strings.Index(stdoutOut, "20260830-feature-bbbb2222")
	older := strings.Index(stdoutOut, "20260829-repair-aaaa1111")
	if newer < 0 || older < 0 {
		t.Fatalf("listing = %q, want both runs", stdoutOut)
	}
	if newer > older {
		t.Fatalf("listing = %q, want the run saved most recently first", stdoutOut)
	}
	for _, want := range []string{workrec.OutcomeComplete, workrec.OutcomeBlocked, "chore/pika-improve"} {
		if !strings.Contains(stdoutOut, want) {
			t.Errorf("listing = %q, want it to carry %q", stdoutOut, want)
		}
	}
}

// The detail view is what an operator opens when the listing row is not
// enough: it has to carry the run's whole chronology, not just its head.
func TestStatusDetailReportsPhasesOutcomeBranchAndCommit(t *testing.T) {
	dir, root := statusFixture(t)
	id := "20260830-repair-aaaa1111"
	at := time.Date(2026, 8, 30, 18, 22, 4, 0, time.UTC)
	seedRun(t, root, workrec.Record{
		WorkID:     id,
		Kind:       workrec.KindRepair,
		Phase:      workrec.PhaseDeliver,
		Branch:     "chore/pika-improve",
		BaseCommit: "base123",
		Commit:     "c0ffee1",
		Role:       "builder",
		Runtime:    "codex",
		Outcome:    workrec.OutcomeComplete,
		Phases: []workrec.PhaseStamp{
			{Phase: workrec.PhaseBaseline, At: at},
			{Phase: workrec.PhaseHandoff, At: at.Add(time.Minute)},
			{Phase: workrec.PhaseRecheck, At: at.Add(2 * time.Minute)},
			{Phase: workrec.PhaseDeliver, At: at.Add(3 * time.Minute)},
		},
	}, time.Now())

	code, stdoutOut, stderrOut := statusOut(t, "--root", dir, id)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderrOut)
	}
	want := []string{
		id, "chore/pika-improve", "base123", "c0ffee1", "builder", "codex",
		workrec.OutcomeComplete,
		workrec.PhaseBaseline, workrec.PhaseHandoff, workrec.PhaseRecheck, workrec.PhaseDeliver,
		"2026-08-30T18:22:04Z",
	}
	for _, w := range want {
		if !strings.Contains(stdoutOut, w) {
			t.Errorf("detail = %q, want it to carry %q", stdoutOut, w)
		}
	}
}

// An id nobody can look up is a dead end, and the operator's next move
// is to check what they typed — so the refusal has to repeat it back.
func TestStatusUnknownWorkIDExitsTwoNamingIt(t *testing.T) {
	dir, _ := statusFixture(t)
	const missing = "20260830-repair-dddd4444"

	code, _, stderrOut := statusOut(t, "--root", dir, missing)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr: %s", code, stderrOut)
	}
	if !strings.Contains(stderrOut, missing) {
		t.Fatalf("stderr = %q, want it to name %q", stderrOut, missing)
	}
}

// A repository that has never run `pika improve` has no runs, and that
// is a valid state rather than a failure. Reporting it as an error would
// make `pika status` unusable in exactly the repository where an
// operator is most likely to try it first.
func TestStatusOnARepositoryWithNoRunsSucceeds(t *testing.T) {
	dir, _ := statusFixture(t)

	code, stdoutOut, stderrOut := statusOut(t, "--root", dir, "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderrOut)
	}
	var payload struct {
		Runs []workrec.Record `json:"runs"`
	}
	env := resultOf(t, []byte(stdoutOut), "status", &payload)
	if !env.OK {
		t.Fatalf("ok = false, want true: an empty history is not a failure:\n%s", stdoutOut)
	}
	// nil would mean the payload emitted `null`, which a consumer has to
	// special-case; an empty history is an empty list.
	if payload.Runs == nil {
		t.Fatalf("runs = null, want an empty list:\n%s", stdoutOut)
	}
	if len(payload.Runs) != 0 {
		t.Fatalf("runs = %+v, want none", payload.Runs)
	}
}

// workrec.List fails the whole listing on one damaged record — report,
// never repair. status does not soften that into a filtered listing:
// showing the readable runs and silently dropping the broken one would
// print a page that looked complete while concealing the corruption.
// What the operator gets instead is nothing, and the path to look at.
func TestStatusReportsACorruptRecordByPathAndListsNothing(t *testing.T) {
	dir, root := statusFixture(t)
	seedRun(t, root, workrec.Record{
		WorkID:  "20260830-repair-aaaa1111",
		Kind:    workrec.KindRepair,
		Outcome: workrec.OutcomeComplete,
	}, time.Now())
	damaged := seedRun(t, root, workrec.Record{
		WorkID: "20260830-repair-eeee5555",
		Kind:   workrec.KindRepair,
	}, time.Now())
	if err := os.WriteFile(damaged, []byte("{not a record"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdoutOut, stderrOut := statusOut(t, "--root", dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stdout: %s stderr: %s", code, stdoutOut, stderrOut)
	}
	if !strings.Contains(stderrOut, damaged) {
		t.Fatalf("stderr = %q, want it to name the offending file %q", stderrOut, damaged)
	}
	if strings.Contains(stdoutOut, "20260830-repair-aaaa1111") {
		t.Fatalf("stdout = %q, want no listing at all: a partial listing hides the damage", stdoutOut)
	}
}

// A record with a phase and no outcome is either a run still in flight
// or a run whose terminal save never landed. On disk those two are the
// same bytes, so status must report the ambiguity rather than resolve
// it: claiming "running" about a run that already died is how an
// operator ends up waiting on nothing.
func TestStatusDoesNotClaimAnUnsettledRunIsStillRunning(t *testing.T) {
	dir, root := statusFixture(t)
	id := "20260830-repair-aaaa1111"
	seedRun(t, root, workrec.Record{
		WorkID: id,
		Kind:   workrec.KindRepair,
		Phase:  workrec.PhaseHandoff,
		Branch: "chore/pika-improve",
	}, time.Now())

	for _, args := range [][]string{{"--root", dir}, {"--root", dir, id}} {
		code, stdoutOut, stderrOut := statusOut(t, args...)
		if code != 0 {
			t.Fatalf("pika status %v: exit = %d, want 0; stderr: %s", args, code, stderrOut)
		}
		if !strings.Contains(stdoutOut, unsettledOutcome) {
			t.Errorf("pika status %v: output = %q, want %q", args, stdoutOut, unsettledOutcome)
		}
		if strings.Contains(stdoutOut, "running") {
			t.Errorf("pika status %v: output = %q, asserts a state the record cannot prove", args, stdoutOut)
		}
	}

	// --json says the same thing by saying nothing: no outcome field at
	// all, rather than a verdict pika invented for the consumer.
	_, stdoutOut, _ := statusOut(t, "--root", dir, id, "--json")
	var payload struct {
		Run map[string]json.RawMessage `json:"run"`
	}
	resultOf(t, []byte(stdoutOut), "status", &payload)
	if _, ok := payload.Run["outcome"]; ok {
		t.Fatalf("run = %v, want no outcome field on a record that has none", payload.Run)
	}
	if _, ok := payload.Run["work_id"]; !ok {
		t.Fatalf("run = %v, want the record's own fields", payload.Run)
	}
}

// Both shapes travel in the standard envelope, so an agent parses one
// surface whether it asked for the listing or for one run.
func TestStatusJSONUsesTheEnvelope(t *testing.T) {
	dir, root := statusFixture(t)
	id := "20260830-repair-aaaa1111"
	seedRun(t, root, workrec.Record{
		WorkID:  id,
		Kind:    workrec.KindRepair,
		Phase:   workrec.PhaseDeliver,
		Branch:  "chore/pika-improve",
		Commit:  "c0ffee1",
		Outcome: workrec.OutcomeComplete,
	}, time.Now())

	var listing struct {
		Runs []workrec.Record `json:"runs"`
	}
	_, stdoutOut, stderrOut := statusOut(t, "--root", dir, "--json")
	// envelopeOf inside resultOf asserts schema and command: "status".
	env := resultOf(t, []byte(stdoutOut), "status", &listing)
	if !env.OK {
		t.Fatalf("ok = false, want true; stderr: %s", stderrOut)
	}
	if len(listing.Runs) != 1 || listing.Runs[0].WorkID != id {
		t.Fatalf("runs = %+v, want the one seeded run", listing.Runs)
	}

	var detail struct {
		Run workrec.Record `json:"run"`
	}
	_, stdoutOut, _ = statusOut(t, "--root", dir, id, "--json")
	if env := resultOf(t, []byte(stdoutOut), "status", &detail); !env.OK {
		t.Fatalf("ok = false, want true:\n%s", stdoutOut)
	}
	if detail.Run.Commit != "c0ffee1" || detail.Run.Outcome != workrec.OutcomeComplete {
		t.Fatalf("run = %+v, want the seeded record", detail.Run)
	}
}

// The exit-2 paths carry their reason inside the envelope too, so an
// agent never has to parse prose to learn its invocation was wrong.
func TestStatusJSONErrorsTravelInTheEnvelope(t *testing.T) {
	dir, _ := statusFixture(t)
	const missing = "20260830-repair-dddd4444"

	code, stdoutOut, stderrOut := statusOut(t, "--root", dir, missing, "--json")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if stderrOut != "" {
		t.Fatalf("stderr = %q, want empty: the envelope is the whole answer", stderrOut)
	}
	env := envelopeOf(t, []byte(stdoutOut), "status")
	if env.OK || env.Error == nil {
		t.Fatalf("envelope = %+v, want an error body", env)
	}
	if !strings.Contains(env.Error.Message, missing) {
		t.Fatalf("message = %q, want it to name %q", env.Error.Message, missing)
	}
}
