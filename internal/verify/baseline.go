package verify

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"time"
)

// RecordedBaseline is a local, advisory record of which gates were
// failing when an operator deliberately recorded it — never a way to
// make a red ladder green. Pass and exit status are decided by every
// gate in the run, exactly as before; a RecordedBaseline only lets a
// later `pika check` tell an operator which of today's failures it has
// already seen, rather than leaving every red run looking identical to
// every other one regardless of whether anything actually changed.
//
// The name is deliberately not Baseline: Report already declares an
// unrelated Baseline field (spec's cross-run comparison for work/improve
// receipts, populated by internal/workrec, not by this package's own
// Run), and internal/adopt has a third, also-unrelated BaselineChecks.
// Reusing the bare word here, in the one package that already has one of
// the other two sitting in the same file, would hand a future reader
// three things named alike and meaning differently.
//
// It lives at .project/state/baseline.json: local, gitignored, and
// disposable in the same sense every other file under .project/state
// already is. Losing it costs nothing but the annotation; nothing that
// depends on correctness reads it.
type RecordedBaseline struct {
	RecordedAt time.Time `json:"recorded_at"`
	Gates      []string  `json:"gates"`
}

// LoadRecordedBaseline reads the baseline at path. A repository that has
// never recorded one returns a nil *RecordedBaseline and a nil error:
// absence is the ordinary starting state, not a failure to report.
func LoadRecordedBaseline(path string) (*RecordedBaseline, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("verify: read %s: %w", path, err)
	}
	var b RecordedBaseline
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("verify: %s is not a readable baseline: %w", path, err)
	}
	return &b, nil
}

// WriteRecordedBaseline replaces the baseline at path with exactly the
// given gate IDs, sorted for a stable, diffable file. It is a full
// replacement rather than an accumulation on purpose: an operator who
// records a baseline is judging the failures in front of them right
// now, and a baseline that silently absorbed whatever failed on every
// later run would stop distinguishing anything by the second use.
func WriteRecordedBaseline(path string, gateIDs []string) error {
	sorted := append([]string(nil), gateIDs...)
	sort.Strings(sorted)
	data, err := json.MarshalIndent(RecordedBaseline{RecordedAt: time.Now().UTC(), Gates: sorted}, "", "  ")
	if err != nil {
		return fmt.Errorf("verify: encode baseline: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("verify: write %s: %w", path, err)
	}
	// os.WriteFile only applies perm when it creates the file, so
	// replacing a previously-recorded baseline could otherwise leave it
	// however it was last chmodded; set it explicitly instead of
	// assuming the write settled it (the same reasoning authorize.go's
	// envelope write already follows).
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("verify: chmod %s: %w", path, err)
		}
	}
	return nil
}

// Known reports whether id was part of the recorded baseline. A nil
// *RecordedBaseline — nothing ever recorded — knows nothing, which is
// the correct answer rather than a special case: every failure is new
// when there is no baseline to compare against.
func (b *RecordedBaseline) Known(id string) bool {
	if b == nil {
		return false
	}
	for _, g := range b.Gates {
		if g == id {
			return true
		}
	}
	return false
}
