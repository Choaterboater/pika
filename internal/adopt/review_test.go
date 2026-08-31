package adopt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readReview(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ReviewPath)))
	if err != nil {
		t.Fatalf("read %s: %v", ReviewPath, err)
	}
	return string(data)
}

// TestPreviewWritesReviewBundle pins the visible, plain-language review
// bundle adopt writes next to the drafts: every section a non-YAML
// reader needs, at the repo root (not under the hidden .project/).
func TestPreviewWritesReviewBundle(t *testing.T) {
	root := messyFixture(t)
	if _, err := Preview(root); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	review := readReview(t, root)
	for _, want := range []string{
		"# Adoption review",
		"Status: **PROPOSED**",
		"## What was found",
		"core@1, go@1",
		"## Conventions",
		"## Conflicts",
		"## Proposed changes",
		"- [ ] create `AGENTS.md`",
		"## Exceptions",
		"- `MyNotes.md` — rule `naming-kebab-case`",
		"keep the record, or rename the path to satisfy the rule and delete the record",
		"## Next step",
		"Run `pika apply`",
	} {
		if !strings.Contains(review, want) {
			t.Errorf("review bundle missing %q\n---\n%s", want, review)
		}
	}

	// Deviating paths must appear so the human sees what deviates.
	if !strings.Contains(review, "`utils/`") && !strings.Contains(review, "`utils/helpers.go`") {
		t.Errorf("review bundle does not list the deviating utils paths\n%s", review)
	}

	// The exceptions section renders exactly once, from the report's
	// proposed exceptions — never a contradictory "None" after the
	// real table.
	if got := strings.Count(review, "## Exceptions"); got != 1 {
		t.Errorf("exceptions section rendered %d times, want 1\n%s", got, review)
	}
	if strings.Contains(review, "None — no naming deviations were recorded") {
		t.Errorf("bundle claims no exceptions although the report proposes them:\n%s", review)
	}
}

// TestReviewDeterministic pins byte-identical bundles across repeated
// renders: no timestamps, sorted lists.
func TestReviewDeterministic(t *testing.T) {
	root := messyFixture(t)
	rep, err := Preview(root)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	first := readReview(t, root)
	if err := WriteReview(root, ReviewData{Status: ReviewProposed, Report: rep}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if second := readReview(t, root); first != second {
		t.Error("review bundle is not deterministic across rewrites")
	}

	// The APPLIED rewrite is deterministic too, and carries the apply
	// outcome instead of the proposal.
	applied := ReviewData{
		Status:     ReviewApplied,
		Exceptions: rep.Exceptions,
		Applied:    []ReviewChange{{Action: "create", Path: "AGENTS.md"}},
		Skipped:    []ReviewSkip{{Path: "README.md", Reason: "already exists; kept the user's version"}},
		Gate1Pass:  true,
	}
	if err := WriteReview(root, applied); err != nil {
		t.Fatalf("WriteReview APPLIED: %v", err)
	}
	got := readReview(t, root)
	if err := WriteReview(root, applied); err != nil {
		t.Fatalf("rewrite APPLIED: %v", err)
	}
	if again := readReview(t, root); got != again {
		t.Error("APPLIED review bundle is not deterministic across rewrites")
	}
	for _, want := range []string{
		"Status: **APPLIED**",
		"- [x] create `AGENTS.md`",
		"## Skipped",
		"`README.md` — already exists",
		"## Gate 1",
		"Pass — no findings.",
		"pika check --all",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("APPLIED bundle missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "Run `pika apply`") {
		t.Errorf("APPLIED bundle still proposes applying:\n%s", got)
	}
}

// TestReviewGate1FailureReported pins honest gate-1 reporting in the
// APPLIED bundle: a failing gate is shown verbatim, not softened.
func TestReviewGate1FailureReported(t *testing.T) {
	root := t.TempDir()
	err := WriteReview(root, ReviewData{
		Status:     ReviewApplied,
		Gate1Pass:  false,
		Gate1Lines: []string{"naming-catch-all: utils/ uses a banned catch-all name"},
	})
	if err != nil {
		t.Fatalf("WriteReview: %v", err)
	}
	review := readReview(t, root)
	if !strings.Contains(review, "FAIL") || !strings.Contains(review, "naming-catch-all: utils/") {
		t.Errorf("gate-1 failure not reported honestly:\n%s", review)
	}
}
