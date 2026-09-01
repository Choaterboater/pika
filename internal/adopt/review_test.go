package adopt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/checks"
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
		"## Baseline",
		"| `lint` | `make lint` | fail |",
		"**Baseline is not green:**",
		"lint",
		"failing before adoption",
		"after apply.",
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
		// The defect this pins: "Next step" used to be a hand-written
		// four-path sentence that never mentioned skill projections, so
		// an operator following it literally would leave AGENTS.md
		// untracked. It must now name every path Applied actually wrote.
		"commit these together",
		"- `review/`",
		"- `AGENTS.md`",
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

// A reviewer facing a large adoption — real ones have recorded several
// hundred exceptions — has no way to see the shape of what they are
// approving from a bare list. The count-and-grouping header must say
// how many, under which rules, and concentrated in which directories,
// and the full list beneath it must still be there in full: eliding it
// would recreate exactly the failure c73f368 exists to remove.
func TestRenderExceptionsSummarizesCountAndShape(t *testing.T) {
	exceptions := []checks.Exception{
		{RuleID: "naming-catch-all", Path: "src/utils/helpers.ts", Reason: "r", Owner: "pika adopt", ReviewCondition: "c"},
		{RuleID: "naming-catch-all", Path: "src/utils/common.ts", Reason: "r", Owner: "pika adopt", ReviewCondition: "c"},
		{RuleID: "naming-catch-all", Path: "lib/manager.go", Reason: "r", Owner: "pika adopt", ReviewCondition: "c"},
		{RuleID: "naming-kebab-case", Path: "README_OLD.md", Reason: "r", Owner: "pika adopt", ReviewCondition: "c"},
	}
	var b strings.Builder
	renderExceptions(&b, exceptions)
	out := b.String()
	for _, want := range []string{
		"naming-catch-all`: 3",
		"naming-kebab-case`: 1",
		"src/utils/`: 2",
		"lib/`: 1",
		"repository root",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
	for _, path := range []string{"src/utils/helpers.ts", "src/utils/common.ts", "lib/manager.go", "README_OLD.md"} {
		if !strings.Contains(out, "`"+path+"`") {
			t.Errorf("full per-exception list must still name %q:\n%s", path, out)
		}
	}
}

// A single-rule, single-directory adoption still gets the header: the
// summary is not withheld just because there is only one row to show,
// which would make its absence a signal an operator has to learn to
// read.
func TestRenderExceptionsSummarizesEvenASingleException(t *testing.T) {
	var b strings.Builder
	renderExceptions(&b, []checks.Exception{
		{RuleID: "naming-catch-all", Path: "src/utils", Reason: "r", Owner: "alice", ReviewCondition: "c"},
	})
	out := b.String()
	if !strings.Contains(out, "naming-catch-all`: 1") {
		t.Errorf("summary missing rule count:\n%s", out)
	}
}
