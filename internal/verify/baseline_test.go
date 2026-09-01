package verify

import (
	"os"
	"path/filepath"
	"testing"
)

// mkBaselinePath returns a not-yet-existing baseline path under a fresh
// temp dir, with its .project/state parent already created — the shape
// WriteRecordedBaseline expects to write into (it never creates
// directories itself; that is repopath's and the caller's concern, the
// same division every other .project/state writer in this repository
// keeps).
func mkBaselinePath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".project", "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "baseline.json")
}

// A repository that has never recorded a baseline is not broken: every
// failure in it is new by definition, and LoadRecordedBaseline must say
// so by returning a nil baseline and a nil error, not by erroring on an
// ordinary absence.
func TestLoadRecordedBaselineMissingFileIsNilNotError(t *testing.T) {
	path := mkBaselinePath(t)
	b, err := LoadRecordedBaseline(path)
	if err != nil {
		t.Fatalf("LoadRecordedBaseline: %v", err)
	}
	if b != nil {
		t.Fatalf("baseline = %+v, want nil for a repository that never recorded one", b)
	}
	if b.Known("format") {
		t.Fatal("a nil baseline must not claim to know any gate")
	}
}

// The round trip WriteRecordedBaseline promises: what was written is
// what LoadRecordedBaseline reads back, and Known answers correctly for
// both a recorded gate and one that was never in the set.
func TestWriteRecordedBaselineRoundTrips(t *testing.T) {
	path := mkBaselinePath(t)
	if err := WriteRecordedBaseline(path, []string{"format", "test"}); err != nil {
		t.Fatalf("WriteRecordedBaseline: %v", err)
	}
	b, err := LoadRecordedBaseline(path)
	if err != nil {
		t.Fatalf("LoadRecordedBaseline: %v", err)
	}
	if b == nil {
		t.Fatal("baseline = nil, want the one just written")
	}
	if !b.Known("format") || !b.Known("test") {
		t.Fatalf("baseline = %+v, want both format and test known", b)
	}
	if b.Known("lint") {
		t.Fatalf("baseline = %+v, want lint unknown: it was never recorded", b)
	}
	if b.RecordedAt.IsZero() {
		t.Error("RecordedAt is zero, want the time of the write")
	}
}

// Recording is a full replacement, not an accumulation: a second
// WriteRecordedBaseline call with a different set must not still know
// gates from the first call. An operator who baselines twice is judging
// the current failures each time, not building a permanent exemption
// list that only ever grows.
func TestWriteRecordedBaselineReplacesNotAccumulates(t *testing.T) {
	path := mkBaselinePath(t)
	if err := WriteRecordedBaseline(path, []string{"format", "lint"}); err != nil {
		t.Fatalf("first WriteRecordedBaseline: %v", err)
	}
	if err := WriteRecordedBaseline(path, []string{"test"}); err != nil {
		t.Fatalf("second WriteRecordedBaseline: %v", err)
	}
	b, err := LoadRecordedBaseline(path)
	if err != nil {
		t.Fatalf("LoadRecordedBaseline: %v", err)
	}
	if b.Known("format") || b.Known("lint") {
		t.Fatalf("baseline = %+v, want the first recording gone after the second", b)
	}
	if !b.Known("test") {
		t.Fatalf("baseline = %+v, want test known from the second recording", b)
	}
}

// A malformed file is a fact to report, not a default to fall back on —
// the same discipline every other durable-state reader in this
// repository follows.
func TestLoadRecordedBaselineRejectsUnreadableFile(t *testing.T) {
	path := mkBaselinePath(t)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRecordedBaseline(path); err == nil {
		t.Fatal("LoadRecordedBaseline accepted an unreadable file")
	}
}
