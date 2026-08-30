package workrec

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/verify"
)

func testRoot(t *testing.T) *repopath.Root {
	t.Helper()
	root, err := repopath.At(t.TempDir())
	if err != nil {
		t.Fatalf("repopath.At: %v", err)
	}
	return root
}

func sampleRecord(id string) Record {
	return Record{
		WorkID:     id,
		Goal:       "make the ladder green",
		Kind:       KindRepair,
		Phase:      PhaseBaseline,
		Branch:     "pika/" + id,
		BaseCommit: "abc1234",
		Role:       "implementer",
		Runtime:    "claude",
	}
}

func mustCreate(t *testing.T, root *repopath.Root, id string) *Handle {
	t.Helper()
	h, err := Create(root, sampleRecord(id))
	if err != nil {
		t.Fatalf("Create(%s): %v", id, err)
	}
	return h
}

// entryNames lists the plain-file names directly inside dir.
func fileNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestCreateRefusesExistingID(t *testing.T) {
	root := testRoot(t)
	const id = "20260830-durable-work-7f3a"

	h := mustCreate(t, root, id)
	rec := sampleRecord(id)
	rec.Phase = PhaseRecheck
	if err := h.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	second := sampleRecord(id)
	second.Goal = "clobber the first run"
	second.Phase = PhaseBaseline
	if _, err := Create(root, second); err == nil {
		t.Fatal("Create on an existing id: want error, got nil")
	} else if !strings.Contains(err.Error(), id) {
		t.Errorf("Create error %q does not name the work id", err)
	}

	// The refusal must not have touched the existing record.
	reopened, err := Open(root, id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := reopened.Record()
	if got.Phase != PhaseRecheck || got.Goal != "make the ladder green" {
		t.Fatalf("refused Create overwrote the record: %+v", got)
	}
}

func TestCreateRejectsUnusableWorkID(t *testing.T) {
	root := testRoot(t)
	for _, id := range []string{"", "../escape", "not a work id", "20260830-Durable-7F3A"} {
		if _, err := Create(root, sampleRecord(id)); err == nil {
			t.Errorf("Create(%q): want error, got nil", id)
		}
		if _, err := Open(root, id); err == nil {
			t.Errorf("Open(%q): want error, got nil", id)
		}
	}
}

func TestDirIsUnderStateAndNamedByWorkID(t *testing.T) {
	root := testRoot(t)
	const id = "20260830-durable-work-7f3a"
	h := mustCreate(t, root, id)

	want := filepath.Join(root.StateDir(), "work", id)
	if h.Dir() != want {
		t.Fatalf("Dir() = %q, want %q", h.Dir(), want)
	}
	if fi, err := os.Stat(h.Dir()); err != nil || !fi.IsDir() {
		t.Fatalf("run dir not a directory: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(h.Dir(), "handoff")); err != nil || !fi.IsDir() {
		t.Fatalf("handoff dir missing: %v", err)
	}
	if h.HandoffDir() != filepath.Join(want, "handoff") {
		t.Fatalf("HandoffDir() = %q", h.HandoffDir())
	}
	if _, err := os.Stat(filepath.Join(h.Dir(), "record.json")); err != nil {
		t.Fatalf("record.json missing after Create: %v", err)
	}

	// Open resolves the same directory.
	reopened, err := Open(root, id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if reopened.Dir() != want {
		t.Fatalf("Open().Dir() = %q, want %q", reopened.Dir(), want)
	}
}

// TestSaveIsAtomicAcrossPhases proves the property resume depends on:
// the record.json byte-range is never observably partial. The rename hook
// runs at the only instant a partial file could exist, and asserts that
// (a) the target still parses completely as the previous phase, (b) the
// temp file already holds the complete next phase, and (c) the rename is
// intra-directory, which is what makes it atomic.
func TestSaveIsAtomicAcrossPhases(t *testing.T) {
	root := testRoot(t)
	const id = "20260830-durable-work-7f3a"
	h := mustCreate(t, root, id)

	baseline := sampleRecord(id)
	baseline.Phase = PhaseBaseline
	baseline.Baseline = &verify.Report{Summary: verify.Summary{Pass: 3}}
	baseline.Phases = []PhaseStamp{{Phase: PhaseBaseline, At: time.Unix(1000, 0).UTC()}}
	if err := h.Save(baseline); err != nil {
		t.Fatalf("Save baseline: %v", err)
	}

	next := baseline
	next.Phase = PhaseRecheck
	next.Commit = "def5678"
	next.Recheck = &verify.Report{Summary: verify.Summary{Pass: 4}}
	next.Phases = append(append([]PhaseStamp{}, baseline.Phases...),
		PhaseStamp{Phase: PhaseRecheck, At: time.Unix(2000, 0).UTC()})

	observed := 0
	orig := renameFile
	renameFile = func(oldpath, newpath string) error {
		observed++
		if filepath.Dir(oldpath) != filepath.Dir(newpath) {
			t.Errorf("rename crosses directories: %s -> %s", oldpath, newpath)
		}
		// The target must still be the complete previous phase.
		var before Record
		bs, err := os.ReadFile(newpath)
		if err != nil {
			t.Fatalf("read target mid-save: %v", err)
		}
		if err := json.Unmarshal(bs, &before); err != nil {
			t.Fatalf("target is partial mid-save: %v", err)
		}
		if before.Phase != PhaseBaseline || before.Commit != "" || before.Recheck != nil {
			t.Errorf("target mutated before rename: %+v", before)
		}
		// The temp file must already be the complete next phase.
		var staged Record
		bs, err = os.ReadFile(oldpath)
		if err != nil {
			t.Fatalf("read temp mid-save: %v", err)
		}
		if err := json.Unmarshal(bs, &staged); err != nil {
			t.Fatalf("temp file is partial at rename: %v", err)
		}
		if staged.Phase != PhaseRecheck || staged.Commit != "def5678" || staged.Recheck == nil {
			t.Errorf("temp file incomplete at rename: %+v", staged)
		}
		return orig(oldpath, newpath)
	}
	t.Cleanup(func() { renameFile = orig })

	if err := h.Save(next); err != nil {
		t.Fatalf("Save recheck: %v", err)
	}
	if observed != 1 {
		t.Fatalf("Save performed %d renames, want exactly 1", observed)
	}

	// A successful save leaves record.json and nothing else.
	if names := fileNames(t, h.Dir()); len(names) != 1 || names[0] != "record.json" {
		t.Fatalf("stray files after Save: %v", names)
	}

	reopened, err := Open(root, id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := reopened.Record()
	if got.Phase != PhaseRecheck || got.Commit != "def5678" {
		t.Fatalf("reopened record is not the committed phase: %+v", got)
	}
	if got.Recheck == nil || got.Recheck.Summary.Pass != 4 || got.Baseline == nil || got.Baseline.Summary.Pass != 3 {
		t.Fatalf("embedded reports did not round-trip: %+v", got)
	}
	if len(got.Phases) != 2 || got.Phases[1].Phase != PhaseRecheck {
		t.Fatalf("phase stamps did not round-trip: %+v", got.Phases)
	}
}

// TestSaveFailureLeavesLastPhaseIntact is the crash half of atomicity: a
// save that dies at the rename must leave the previously committed phase
// readable and must not leave a temp file behind.
func TestSaveFailureLeavesLastPhaseIntact(t *testing.T) {
	root := testRoot(t)
	const id = "20260830-durable-work-7f3a"
	h := mustCreate(t, root, id)

	baseline := sampleRecord(id)
	baseline.Phase = PhaseBaseline
	if err := h.Save(baseline); err != nil {
		t.Fatalf("Save baseline: %v", err)
	}

	boom := errors.New("simulated crash at rename")
	orig := renameFile
	renameFile = func(string, string) error { return boom }
	next := baseline
	next.Phase = PhaseDeliver
	next.Outcome = OutcomeComplete
	err := h.Save(next)
	renameFile = orig
	if !errors.Is(err, boom) {
		t.Fatalf("Save error = %v, want the rename failure", err)
	}

	if names := fileNames(t, h.Dir()); len(names) != 1 || names[0] != "record.json" {
		t.Fatalf("failed Save left temp files behind: %v", names)
	}
	reopened, err := Open(root, id)
	if err != nil {
		t.Fatalf("Open after failed Save: %v", err)
	}
	if got := reopened.Record(); got.Phase != PhaseBaseline || got.Outcome != "" {
		t.Fatalf("failed Save was partially observable: %+v", got)
	}
}

func TestTruncatedRecordIsReportedNotRepaired(t *testing.T) {
	root := testRoot(t)
	const id = "20260830-durable-work-7f3a"
	h := mustCreate(t, root, id)

	path := filepath.Join(h.Dir(), "record.json")
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	truncated := full[:len(full)/2]
	if err := os.WriteFile(path, truncated, 0o644); err != nil {
		t.Fatalf("truncate record: %v", err)
	}

	if _, err := Open(root, id); err == nil {
		t.Fatal("Open on a truncated record: want error, got nil")
	} else if !strings.Contains(err.Error(), path) {
		t.Errorf("Open error %q does not name %s", err, path)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read record after Open: %v", err)
	}
	if string(after) != string(truncated) {
		t.Fatalf("Open repaired a corrupt record; file changed:\n%s", after)
	}
	if names := fileNames(t, h.Dir()); len(names) != 1 || names[0] != "record.json" {
		t.Fatalf("Open wrote extra files: %v", names)
	}

	// List surfaces the same fact rather than skipping the damaged run.
	if _, err := List(root); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("List error = %v, want an error naming %s", err, path)
	}
}

func TestOpenReportsWorkIDMismatch(t *testing.T) {
	root := testRoot(t)
	const id = "20260830-durable-work-7f3a"
	h := mustCreate(t, root, id)

	path := filepath.Join(h.Dir(), "record.json")
	rec := sampleRecord("20260830-other-run-0001")
	bs, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, bs, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Open(root, id); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Open error = %v, want an error naming %s", err, path)
	}
}

func TestOpenMissingRunIsAnError(t *testing.T) {
	root := testRoot(t)
	if _, err := Open(root, "20260830-never-created-0001"); err == nil {
		t.Fatal("Open on a missing run: want error, got nil")
	}
}

// TestLeftoverTempFileIsIgnored covers the crashed-save residue: a temp
// file must confuse neither Open nor List, at the run level or beside the
// run directories.
func TestLeftoverTempFileIsIgnored(t *testing.T) {
	root := testRoot(t)
	const id = "20260830-durable-work-7f3a"
	h := mustCreate(t, root, id)

	if err := os.WriteFile(filepath.Join(h.Dir(), tempPrefix+"9182734"), []byte("{\"phase\":\"trun"), 0o600); err != nil {
		t.Fatalf("plant run temp: %v", err)
	}
	workDir := filepath.Join(root.StateDir(), "work")
	if err := os.WriteFile(filepath.Join(workDir, tempPrefix+"5551212"), []byte("garbage"), 0o600); err != nil {
		t.Fatalf("plant work temp: %v", err)
	}

	reopened, err := Open(root, id)
	if err != nil {
		t.Fatalf("Open with leftover temp: %v", err)
	}
	if reopened.Record().Phase != PhaseBaseline {
		t.Fatalf("leftover temp changed the read record: %+v", reopened.Record())
	}
	recs, err := List(root)
	if err != nil {
		t.Fatalf("List with leftover temp: %v", err)
	}
	if len(recs) != 1 || recs[0].WorkID != id {
		t.Fatalf("List = %+v, want the single real run", recs)
	}
}

func TestListIsNewestFirst(t *testing.T) {
	root := testRoot(t)
	ids := []string{
		"20260828-oldest-run-0001",
		"20260829-middle-run-0002",
		"20260830-newest-run-0003",
	}
	// Saved oldest-last so creation order cannot accidentally produce the
	// expected answer.
	for _, id := range ids {
		mustCreate(t, root, id)
	}
	stamps := map[string]time.Time{
		ids[0]: time.Unix(1_700_000_000, 0),
		ids[1]: time.Unix(1_700_000_100, 0),
		ids[2]: time.Unix(1_700_000_200, 0),
	}
	for id, ts := range stamps {
		p := filepath.Join(root.StateDir(), "work", id, "record.json")
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}

	recs, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make([]string, len(recs))
	for i, r := range recs {
		got[i] = r.WorkID
	}
	want := []string{ids[2], ids[1], ids[0]}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("List order = %v, want %v", got, want)
	}
}

func TestListTiesBreakOnWorkIDDescending(t *testing.T) {
	root := testRoot(t)
	ids := []string{"20260830-alpha-run-0001", "20260830-bravo-run-0002"}
	for _, id := range ids {
		mustCreate(t, root, id)
	}
	ts := time.Unix(1_700_000_000, 0)
	for _, id := range ids {
		p := filepath.Join(root.StateDir(), "work", id, "record.json")
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}
	recs, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 2 || recs[0].WorkID != ids[1] || recs[1].WorkID != ids[0] {
		t.Fatalf("tie order = %+v, want %s then %s", recs, ids[1], ids[0])
	}
}

func TestListOnEmptyStateIsEmpty(t *testing.T) {
	root := testRoot(t)
	recs, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("List = %+v, want empty", recs)
	}
}

func TestSaveRefusesForeignWorkID(t *testing.T) {
	root := testRoot(t)
	const id = "20260830-durable-work-7f3a"
	h := mustCreate(t, root, id)

	rec := sampleRecord("20260830-someone-else-0001")
	if err := h.Save(rec); err == nil {
		t.Fatal("Save with a mismatched work id: want error, got nil")
	}
	reopened, err := Open(root, id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if reopened.Record().WorkID != id {
		t.Fatalf("mismatched Save leaked into the record: %+v", reopened.Record())
	}
}

// TestWorkIDSuffixWidthIsNotAssumed pins the variable-width suffix:
// NewWorkID now mints 8 hex digits and 4-hex ids minted earlier stay
// valid, so nothing here may slice an id at a fixed offset.
func TestWorkIDSuffixWidthIsNotAssumed(t *testing.T) {
	root := testRoot(t)
	ids := []string{
		"20260830-durable-work-7f3a",
		"20260830-durable-work-7f3a91c2",
	}
	for _, id := range ids {
		h := mustCreate(t, root, id)
		if filepath.Base(h.Dir()) != id {
			t.Fatalf("Dir() = %q, want a directory named %q", h.Dir(), id)
		}
		reopened, err := Open(root, id)
		if err != nil {
			t.Fatalf("Open(%s): %v", id, err)
		}
		if reopened.Record().WorkID != id {
			t.Fatalf("round-trip lost the id: %+v", reopened.Record())
		}
	}

	recs, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != len(ids) {
		t.Fatalf("List returned %d records, want %d: %+v", len(recs), len(ids), recs)
	}
	seen := map[string]bool{}
	for _, r := range recs {
		seen[r.WorkID] = true
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("List dropped %s", id)
		}
	}
}

// TestRecordDoesNotShareItsPhaseSliceWithTheHandle pins the read-modify-
// save loop tasks 4-8 run: read the record, append a phase, save it back.
// If Record() handed out the handle's own Phases slice, that loop would
// write through the handle's cache — silently, since nothing on this path
// returns an error. Two failure modes, both exercised here: writing to an
// element the handle still counts as its own, and two derived records
// appending onto one shared backing array so the second clobbers the
// first.
func TestRecordDoesNotShareItsPhaseSliceWithTheHandle(t *testing.T) {
	root := testRoot(t)
	const id = "20260830-durable-work-7f3a"
	h := mustCreate(t, root, id)

	// Spare capacity is the point: an append lands inside the backing
	// array rather than allocating a fresh one.
	rec := h.Record()
	rec.Phases = append(make([]PhaseStamp, 0, 4), PhaseStamp{Phase: PhaseBaseline, At: time.Unix(1, 0).UTC()})
	if err := h.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Mutating an element of a record obtained from Record() must not
	// reach the handle's cache.
	a := h.Record()
	a.Phases[0].Note = "scribbled by the caller"
	if got := h.Record().Phases[0].Note; got != "" {
		t.Errorf("caller write reached the handle's cache: Phases[0].Note = %q, want empty", got)
	}

	// Two independent read-modify cycles must not share an append slot.
	a = h.Record()
	a.Phases = append(a.Phases, PhaseStamp{Phase: PhaseHandoff})
	b := h.Record()
	b.Phases = append(b.Phases, PhaseStamp{Phase: PhaseRecheck})
	if a.Phases[1].Phase != PhaseHandoff {
		t.Errorf("second append clobbered the first: a.Phases[1].Phase = %q, want %q", a.Phases[1].Phase, PhaseHandoff)
	}
	if b.Phases[1].Phase != PhaseRecheck {
		t.Errorf("b.Phases[1].Phase = %q, want %q", b.Phases[1].Phase, PhaseRecheck)
	}
	if n := len(h.Record().Phases); n != 1 {
		t.Errorf("appends changed the handle's own record: len(Phases) = %d, want 1", n)
	}
}

// TestSaveDoesNotAliasTheCallersPhaseSlice is the inbound half of the
// invariant TestRecordDoesNotShareItsPhaseSliceWithTheHandle pins on the
// way out. The lifecycle's read-modify-save loop keeps the record it
// handed to Save in a local variable; if Save stored that value as-is,
// the local would still share its Phases backing array with the handle's
// cache. An in-place element write through that local — no reallocating
// append to hide it — would then edit the handle's record without
// touching record.json, and Record() would report the phantom edit as
// durable with no error anywhere.
func TestSaveDoesNotAliasTheCallersPhaseSlice(t *testing.T) {
	root := testRoot(t)
	const id = "20260830-durable-work-7f3a"
	h := mustCreate(t, root, id)

	rec := h.Record()
	rec.Phases = append(rec.Phases, PhaseStamp{Phase: PhaseBaseline, At: time.Unix(1, 0).UTC()})
	if err := h.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The caller still holds rec. Editing an element in place must not
	// reach the handle, which now speaks for what is on disk.
	rec.Phases[0].Note = "scribbled after Save"
	if got := h.Record().Phases[0].Note; got != "" {
		t.Errorf("post-Save caller write reached the handle's cache: Phases[0].Note = %q, want empty", got)
	}

	// And the cache must still agree with record.json, which never saw
	// the note.
	reopened, err := Open(root, id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := reopened.Record().Phases[0].Note; got != "" {
		t.Errorf("on-disk record changed without a Save: Phases[0].Note = %q, want empty", got)
	}
}

// TestSaveAdoptsCacheEvenWhenDirectorySyncFails pins the ordering inside
// Save: the cache is adopted between the rename and the directory fsync.
// The rename is the instant record.json becomes the new content, so once
// it succeeds the cache must speak for the new record even if the fsync
// then fails — that error says the rename may not survive a crash, not
// that it did not happen. Moving the assignment after the fsync would
// leave Record() reporting a phase the file has already moved past.
func TestSaveAdoptsCacheEvenWhenDirectorySyncFails(t *testing.T) {
	root := testRoot(t)
	const id = "20260830-durable-work-7f3a"
	h := mustCreate(t, root, id)

	boom := errors.New("simulated fsync failure")
	orig := syncDir
	syncDir = func(string) error { return boom }
	next := sampleRecord(id)
	next.Phase = PhaseDeliver
	next.Outcome = OutcomeComplete
	err := h.Save(next)
	syncDir = orig

	if !errors.Is(err, boom) {
		t.Fatalf("Save error = %v, want the directory fsync failure", err)
	}
	if got := h.Record(); got.Phase != PhaseDeliver || got.Outcome != OutcomeComplete {
		t.Errorf("cache is stale after a committed rename: Phase = %q, Outcome = %q, want %q/%q",
			got.Phase, got.Outcome, PhaseDeliver, OutcomeComplete)
	}
	// The rename did commit, so disk must agree with the cache.
	reopened, err := Open(root, id)
	if err != nil {
		t.Fatalf("Open after failed fsync: %v", err)
	}
	if got := reopened.Record(); got.Phase != PhaseDeliver || got.Outcome != OutcomeComplete {
		t.Errorf("record.json did not take the rename: %+v", got)
	}
}
