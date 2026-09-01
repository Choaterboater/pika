package adopt

import (
	"fmt"
	"os"
	"path"
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
	for _, w := range rep.Warnings {
		fmt.Fprintf(b, "- **Warning:** %s\n", w)
	}
	fmt.Fprintf(b, "- The machine-readable proposals live in `.project/contract.yaml.draft` and `.project/profiles.lock.draft` (dot-folders are hidden by default in Finder and Explorer — this file is the plain-language copy).\n\n")
	fmt.Fprintf(b, "## Conventions (%d checked against the core profile)\n\n", len(rep.ConventionMap))
	fmt.Fprintf(b, "| Convention | Status | Detail |\n|---|---|---|\n")
	for _, c := range rep.ConventionMap {
		fmt.Fprintf(b, "| `%s` | %s | %s |\n", c.Name, c.Status, escapeDetail(c.Detail))
	}
	b.WriteString("\n")
	renderBaseline(b, rep)

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
	// "your files were kept" is not true of every skip: a kernel-owned
	// file skipped because it already matches the rendered template is
	// the kernel's, not the operator's. The heading says what holds for
	// all of them; the per-line reason says which case each one is.
	fmt.Fprintf(b, "## Skipped (%d — left on disk as apply found them)\n\n", len(data.Skipped))
	for _, s := range data.Skipped {
		fmt.Fprintf(b, "- `%s` — %s\n", s.Path, escapeDetail(s.Reason))
	}
	b.WriteString("\n")
}

// renderExceptions prints every exception adopt recorded in full — rule,
// path, reason, owner and review condition. The short form this replaced
// listed only path and rule, which made an auto-recorded waiver something
// an operator approved without ever reading it. `pika apply` is where a
// human accepts these records, so this is the page that has to carry
// them.
func renderExceptions(b *strings.Builder, exceptions []checks.Exception) {
	if len(exceptions) == 0 {
		b.WriteString("## Exceptions\n\nNone — no naming deviations were recorded.\n\n")
		return
	}
	fmt.Fprintf(b, "## Exceptions (%d recorded naming deviations)\n\n", len(exceptions))
	b.WriteString("`pika adopt` wrote these records into `.project/exceptions.yaml`; each waives one naming rule for one path, and each is keyed to that exact path — a path added later is not covered and will still fail gate 1. Approving `pika apply` accepts every record below, so read the reasons first: keep the record, or rename the path to satisfy the rule and delete the record.\n\n")
	renderExceptionSummary(b, exceptions)
	for _, ex := range exceptions {
		fmt.Fprintf(b, "- `%s` — rule `%s`\n", ex.Path, ex.RuleID)
		fmt.Fprintf(b, "  - reason: %s\n", escapeDetail(ex.Reason))
		fmt.Fprintf(b, "  - owner: %s\n", escapeDetail(ex.Owner))
		fmt.Fprintf(b, "  - review condition: %s\n", escapeDetail(ex.ReviewCondition))
	}
	b.WriteString("\n")
}

// renderExceptionSummary prints a count-and-grouping header above the
// full exceptions list: how many, under which rules, and concentrated
// in which directories. sindresorhus/got records 21 catch-all
// exceptions; apple/swift-argument-parser records several hundred — a
// reviewer facing either has no way to see the shape of what they are
// approving from a bare list of that length.
//
// The full list is never truncated or replaced: silently eliding
// waivers is precisely the failure c73f368 exists to remove, and a
// summary that stood in for the list would recreate it in a new place.
// This stands above the list, not instead of it.
func renderExceptionSummary(b *strings.Builder, exceptions []checks.Exception) {
	byRule := map[string]int{}
	byDir := map[string]int{}
	for _, ex := range exceptions {
		byRule[ex.RuleID]++
		dir := path.Dir(ex.Path)
		if dir == "." {
			dir = "(repository root)"
		}
		byDir[dir]++
	}
	b.WriteString("By rule:\n\n")
	for _, rule := range sortedCountKeys(byRule) {
		fmt.Fprintf(b, "- `%s`: %d\n", rule, byRule[rule])
	}
	b.WriteString("\nBy directory:\n\n")
	for _, dir := range sortedCountKeys(byDir) {
		if dir == "(repository root)" {
			fmt.Fprintf(b, "- %s: %d\n", dir, byDir[dir])
			continue
		}
		fmt.Fprintf(b, "- `%s/`: %d\n", dir, byDir[dir])
	}
	b.WriteString("\n")
}

// sortedCountKeys returns a map's keys sorted, so the summary renders
// deterministically across repeated runs like every other list here.
func sortedCountKeys(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// renderBaseline prints the outcome of every discovered and autofilled
// check command, run once before anything is proposed, so the operator
// learns which gates will be red immediately after apply before
// approving the draft — the exact fact `pika adopt`'s own terminal
// output already states (cmd/pika/adopt.go's printAdoptReport), which
// this must agree with word for word: a repository whose baseline
// typecheck already fails prints "baseline is not green: typecheck is
// failing before adoption, and that gate will fail after apply" on
// the terminal, and review/adoption-review.md — the artifact adopt
// itself calls "the plain-language copy of this report" and the one
// that survives a scrolled-away terminal — used to say nothing about
// it at all.
func renderBaseline(b *strings.Builder, rep *Report) {
	if len(rep.BaselineChecks) == 0 {
		return
	}
	b.WriteString("## Baseline (discovered and autofilled checks, run before any change)\n\n")
	b.WriteString("| Verb | Command | Status | Exit |\n|---|---|---|---|\n")
	var failed []string
	for _, c := range rep.BaselineChecks {
		exit := fmt.Sprintf("%d", c.Exit)
		if c.Exit < 0 {
			exit = "—"
		}
		fmt.Fprintf(b, "| `%s` | `%s` | %s | %s |\n", c.Verb, escapeDetail(c.Command), c.Status, exit)
		if c.Status != "pass" {
			failed = append(failed, c.Verb)
		}
	}
	b.WriteString("\n")
	if len(failed) > 0 {
		verbWord, gateWord := "is", "that gate will fail"
		if len(failed) > 1 {
			verbWord, gateWord = "are", "those gates will fail"
		}
		fmt.Fprintf(b, "**Baseline is not green:** %s %s failing before adoption, and %s after apply.\n\n",
			strings.Join(failed, ", "), verbWord, gateWord)
	}
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

// renderNextStep's APPLIED branch lists every path this apply actually
// wrote, plus review/ itself — never a fixed list. The bug this fixes:
// a hand-maintained "commit these four paths" sentence silently fell out
// of sync the moment apply grew a fifth kind of write (M5's skill
// projections, AGENTS.md/CLAUDE.md and .agents/skills/), and a commit
// instruction that omits kernel-owned files it just created would leave
// them untracked if an operator followed it literally. data.Applied is
// the same list renderApplied already prints under "## Applied" above,
// so the two sections can never drift from each other again.
func renderNextStep(b *strings.Builder, data ReviewData) {
	if data.Status == ReviewApplied {
		b.WriteString("## Next step\n\nRun `pika check --all` to verify the applied contract, then commit these together:\n\n")
		b.WriteString("- `review/`\n")
		for _, ch := range data.Applied {
			fmt.Fprintf(b, "- `%s`\n", ch.Path)
		}
		b.WriteString("\n")
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
