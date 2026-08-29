package adopt

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Choaterboater/pika/internal/checks"
)

// ReviewPath is the human-readable adoption review bundle's location
// relative to the repository root. It lives at the repo root — not
// under the dot-folder .project/ — because it is written for humans
// scrolling a file manager, not for tooling: the machine-readable
// proposals stay in the two .draft files under .project/.
const ReviewPath = "review/adoption-review.md"

// Review statuses: what the bundle says about the adoption's stage.
const (
	ReviewProposed = "PROPOSED" // drafts exist; nothing applied yet
	ReviewApplied  = "APPLIED"  // a successful `pika apply` committed the drafts
)

// ReviewChange is one change recorded in an APPLIED review bundle.
type ReviewChange struct {
	Action string // create | write | delete | move
	Path   string // repository-relative, "/"-separated
}

// ReviewSkip is one proposed file an APPLIED review bundle reports as
// left untouched.
type ReviewSkip struct {
	Path   string
	Reason string
}

// ReviewData is the content source for the review bundle. A PROPOSED
// bundle renders from the adoption Report; an APPLIED bundle renders
// from the apply outcome plus the exceptions the draft contract
// carried (durable state — apply never re-runs discovery). Every
// rendering is deterministic: no timestamps, sorted paths, so
// repeated runs produce byte-identical files.
type ReviewData struct {
	Status     string             // ReviewProposed or ReviewApplied
	Report     *Report            // adoption inventory; required for PROPOSED
	Exceptions []checks.Exception // deviations recorded in the contract
	Applied    []ReviewChange     // APPLIED only
	Skipped    []ReviewSkip       // APPLIED only
	Gate1Pass  bool               // APPLIED only: gate 1 outcome on the applied contract
	Gate1Lines []string           // APPLIED only: gate 1 output and warnings
}

// WriteReview renders and writes review/adoption-review.md under
// repoRoot, creating the review directory when needed.
func WriteReview(repoRoot string, data ReviewData) error {
	full := filepath.Join(repoRoot, filepath.FromSlash(ReviewPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("adopt: create %s: %w", ReviewPath, err)
	}
	if err := os.WriteFile(full, renderReview(data), 0o644); err != nil {
		return fmt.Errorf("adopt: write %s: %w", ReviewPath, err)
	}
	return nil
}

// renderReview produces the bundle's markdown. Determinism is a
// property tests pin: no clocks, and every list renders in the sorted
// order its source carries. The exceptions section renders exactly
// once, from the adoption report's proposed exceptions for a PROPOSED
// bundle and from the draft contract's recorded exceptions for an
// APPLIED one.
func renderReview(data ReviewData) []byte {
	var b strings.Builder
	b.WriteString("# Adoption review\n\n")
	if data.Status == ReviewApplied {
		b.WriteString("Status: **APPLIED** — the adoption drafts were promoted into a live contract.\n\n")
		renderApplied(&b, data)
	} else {
		b.WriteString("Status: **PROPOSED** — nothing is applied yet; only drafts exist.\n\n")
		renderProposed(&b, data)
	}
	exceptions := data.Exceptions
	if data.Status != ReviewApplied && data.Report != nil {
		exceptions = data.Report.Exceptions
	}
	renderExceptions(&b, exceptions)
	renderGate1(&b, data)
	renderNextStep(&b, data)
	return []byte(b.String())
}

func renderProposed(b *strings.Builder, data ReviewData) {
	rep := data.Report
	fmt.Fprintf(b, "## What was found\n\n")
	fmt.Fprintf(b, "- Detected profiles: %s\n", orList(joinProfiles(rep)))
	fmt.Fprintf(b, "- Packages: %d\n", len(rep.Inventory.Packages))
	fmt.Fprintf(b, "- The machine-readable proposals live in `.project/contract.yaml.draft` and `.project/profiles.lock.draft` (dot-folders are hidden by default in Finder and Explorer — this file is the plain-language copy).\n\n")

	fmt.Fprintf(b, "## Conventions (%d checked against the core profile)\n\n", len(rep.ConventionMap))
	fmt.Fprintf(b, "| Convention | Status | Detail |\n|---|---|---|\n")
	for _, c := range rep.ConventionMap {
		fmt.Fprintf(b, "| `%s` | %s | %s |\n", c.Name, c.Status, escapeDetail(c.Detail))
	}
	b.WriteString("\n")

	if len(rep.Conflicts) == 0 {
		b.WriteString("## Conflicts\n\nNone — no convention needs a human decision.\n\n")
	} else {
		fmt.Fprintf(b, "## Conflicts (%d — need a human decision before applying)\n\n", len(rep.Conflicts))
		for _, c := range rep.Conflicts {
			fmt.Fprintf(b, "- `%s` breaks rule `%s`: %s\n", c.Path, c.RuleID, escapeDetail(c.Detail))
		}
		b.WriteString("\n")
	}
	if len(rep.ProposedChanges) == 0 {
		b.WriteString("## Proposed changes\n\nNone — the repository already matches the core profile.\n\n")
		return
	}

	fmt.Fprintf(b, "## Proposed changes (%d)\n\n", len(rep.ProposedChanges))
	for _, ch := range rep.ProposedChanges {
		fmt.Fprintf(b, "- [ ] %s `%s` — %s\n", ch.Action, ch.Path, escapeDetail(ch.Detail))
	}
	b.WriteString("\n")
}

func renderApplied(b *strings.Builder, data ReviewData) {
	if len(data.Applied) == 0 {
		b.WriteString("## Applied\n\nNothing — every proposal was already satisfied.\n\n")
	} else {
		fmt.Fprintf(b, "## Applied (%d)\n\n", len(data.Applied))
		for _, ch := range data.Applied {
			fmt.Fprintf(b, "- [x] %s `%s`\n", ch.Action, ch.Path)
		}
		b.WriteString("\n")
	}
	if len(data.Skipped) == 0 {
		return
	}
	fmt.Fprintf(b, "## Skipped (%d — your files were kept)\n\n", len(data.Skipped))
	for _, s := range data.Skipped {
		fmt.Fprintf(b, "- `%s` — %s\n", s.Path, escapeDetail(s.Reason))
	}
	b.WriteString("\n")
}

func renderExceptions(b *strings.Builder, exceptions []checks.Exception) {
	if len(exceptions) == 0 {
		b.WriteString("## Exceptions\n\nNone — no naming deviations were recorded.\n\n")
		return
	}
	fmt.Fprintf(b, "## Exceptions (%d recorded naming deviations)\n\n", len(exceptions))
	fmt.Fprintf(b, "| Path | Rule | Suggested action |\n|---|---|---|\n")
	for _, ex := range exceptions {
		fmt.Fprintf(b, "| `%s` | `%s` | keep as an exception, or rename the path to satisfy the rule |\n", ex.Path, ex.RuleID)
	}
	b.WriteString("\n")
}

func renderGate1(b *strings.Builder, data ReviewData) {
	if data.Status != ReviewApplied {
		return
	}
	b.WriteString("## Gate 1 on the applied contract\n\n")
	if data.Gate1Pass && len(data.Gate1Lines) == 0 {
		b.WriteString("Pass — no findings.\n\n")
		return
	}
	if data.Gate1Pass {
		b.WriteString("Pass, with warnings:\n\n```\n")
	} else {
		b.WriteString("FAIL — the applied state is valid but carries findings; resolve them before relying on `pika check`:\n\n```\n")
	}
	for _, line := range data.Gate1Lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("```\n\n")
}

func renderNextStep(b *strings.Builder, data ReviewData) {
	if data.Status == ReviewApplied {
		b.WriteString("## Next step\n\nRun `pika check --all` to verify the applied contract, then commit `review/`, `.project/contract.yaml`, `.project/profiles.lock`, and `.project/exceptions.yaml` together.\n")
		return
	}
	b.WriteString("## Next step\n\nRun `pika apply` to promote the drafts into a live contract. Apply is transactional: files you already have are kept (create-if-missing), and any failure rolls the repository back to its exact pre-state.\n")
}

// joinProfiles renders the report's detected profiles for prose.
func joinProfiles(rep *Report) string {
	return strings.Join(rep.DetectedProfiles, ", ")
}

// orList renders an empty prose value.
func orList(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

// escapeDetail keeps table cells and list items single-line: markdown
// rows must not embed the newlines some convention details carry.
func escapeDetail(s string) string {
	if !strings.ContainsAny(s, "\n\r") {
		return s
	}
	lines := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	sort.Strings(lines)
	return strings.Join(lines, "; ")
}
