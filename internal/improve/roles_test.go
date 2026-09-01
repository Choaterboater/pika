package improve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/workrec"
)

// messageRunner writes a final message and otherwise touches nothing: the
// shape an explorer or a reviewer has, because both are read-only roles.
// edit is the misbehaving case, and it is a parameter rather than a second
// type so the runner that proves the refusal is the same runner that
// proves the happy path.
type messageRunner struct {
	message string
	edit    string
}

func (r messageRunner) Run(_ context.Context, root, _, outputPath string) error {
	if r.edit != "" {
		if err := os.WriteFile(filepath.Join(root, r.edit), []byte("unsolicited\n"), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(outputPath, []byte(r.message), 0o600)
}

// Runtime is omp, so a bundle written by one of these is distinguishable
// from the builder's: the filename carries it, which is the whole point of
// naming the message files after the runtime that wrote them.
func (r messageRunner) Runtime() string { return "omp" }

func explorerRole(runner Runner) *Role {
	return &Role{Name: "explorer", Agent: "explorer", Runner: runner}
}

func reviewerRole(runner Runner) *Role {
	return &Role{Name: "reviewer", Agent: "reviewer", Runner: runner}
}

// roleConfig is a run that fails its baseline, gets repaired by the
// builder, and passes the recheck — the world in which the two optional
func roleConfig(root string, builder Runner) Config {
	// Queued rather than fixed: the baseline has to fail for there to be
	// anything to repair, and the recheck has to pass for the run to
	// deliver. A single report cannot be both.
	reports := []*verify.Report{failingBaseline(), passingLadder()}
	return Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := reports[0]
			if len(reports) > 1 {
				reports = reports[1:]
			}
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: builder},
	}
}

// A run with no builder has nothing to hand the working tree to. The
// refusal names the role, because a run that failed for a missing reviewer
// reads very differently from one that failed for a missing builder.
func TestRunWithoutABuilderIsRefused(t *testing.T) {
	root := fixtureRepository(t)
	_, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check:  func() (*verify.Report, error) { return failingBaseline(), nil },
	})
	if err == nil {
		t.Fatal("a run with no builder ran")
	}
	if !strings.Contains(err.Error(), "a builder runner is required") {
		t.Errorf("error = %q, want it to name the missing builder", err)
	}
}

// The explorer's product is a section of the builder's prompt. If it does
// not get there, the phase cost an agent run and bought nothing — so this
// asserts the file the builder was actually handed, not a copy of what the
// explorer wrote.
func TestExplorerFindingsReachTheBuilderPrompt(t *testing.T) {
	root := fixtureRepository(t)
	cfg := roleConfig(root, repairRunner{path: "fixed.txt", body: "verified fix\n"})
	cfg.Explorer = explorerRole(messageRunner{message: "EXPLORER-MARKER: the loader lives in internal/config\n"})

	result, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	prompt, err := os.ReadFile(result.Handoff.PromptPath)
	if err != nil {
		t.Fatalf("read builder prompt: %v", err)
	}
	got := string(prompt)
	if !strings.Contains(got, "## Explorer findings") {
		t.Errorf("builder prompt has no explorer section:\n%s", got)
	}
	if !strings.Contains(got, "EXPLORER-MARKER") {
		t.Errorf("builder prompt does not carry the explorer's message:\n%s", got)
	}
	// The phase is stamped and the agent is recorded: a run that spawned
	// two agents must be able to say so afterwards.
	rec := runRecord(t, root, result.WorkID)
	if got := phaseNames(rec); !contains(got, workrec.PhaseExplore) {
		t.Errorf("phases = %v, want explore among them", got)
	}
	if len(rec.Agents) != 2 || rec.Agents[0].Role != "explorer" || rec.Agents[0].Runtime != "omp" {
		t.Fatalf("agents = %+v, want the explorer first", rec.Agents)
	}
	// And the bundle it wrote is under handoff/explore, not beside the
	// builder's.
	if _, err := os.Stat(filepath.Join(root, ".project", "state", "work", result.WorkID, "handoff", "explore", "omp-last-message.md")); err != nil {
		t.Errorf("explore bundle missing: %v", err)
	}
}

// An explorer that edits the tree is doing the builder's work, from a
// prompt the builder never saw, in a run that will then verify and commit
// the result. Refusing is the only way the tree stays the builder's.
func TestAnExplorerThatChangesTheTreeIsRefused(t *testing.T) {
	root := fixtureRepository(t)
	cfg := roleConfig(root, repairRunner{path: "fixed.txt", body: "verified fix\n"})
	cfg.Explorer = explorerRole(messageRunner{message: "found it\n", edit: "unsolicited.txt"})

	if _, err := Run(context.Background(), cfg); err == nil {
		t.Fatal("an explorer that edited the tree was accepted")
	} else if !strings.Contains(err.Error(), "changed the working tree") {
		t.Errorf("error = %q, want the read-only refusal", err)
	}
}

// A reviewer that edits fixed.txt — the file the builder already
// legitimately changed — names no new path, so requireNoNewChanges's
// old set-comparison alone would have missed it: fixed.txt was already
// in "before". This is the bug the content snapshot closes: a reviewer
// silently overwriting an already-changed file must be refused exactly
// like one that adds a new one, not committed as though it were the
// builder's own verified content.
func TestAReviewerThatEditsAnAlreadyChangedFileIsRefused(t *testing.T) {
	root := fixtureRepository(t)
	cfg := roleConfig(root, repairRunner{path: "fixed.txt", body: "verified fix\n"})
	cfg.Reviewer = reviewerRole(messageRunner{message: "looks fine\n", edit: "fixed.txt"})

	if _, err := Run(context.Background(), cfg); err == nil {
		t.Fatal("a reviewer that edited an already-changed file was accepted")
	} else if !strings.Contains(err.Error(), "modified already-changed files") {
		t.Errorf("error = %q, want the read-only refusal naming the mutated file", err)
	}
}

// A review is advisory: it is recorded, and it does not gate the commit.
// The commit landing is the assertion — a reviewer that could block a green
// ladder would be a second gate that is not deterministic, which is the
// thing M1 was built to avoid.
func TestReviewIsRecordedAndNeverGatesTheCommit(t *testing.T) {
	root := fixtureRepository(t)
	cfg := roleConfig(root, repairRunner{path: "fixed.txt", body: "verified fix\n"})
	cfg.Reviewer = reviewerRole(messageRunner{message: "REVIEW-MARKER: nothing blocking; the fix is covered by the new test.\n"})

	result, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Commit == "" {
		t.Fatal("the run produced no commit: the review gated it")
	}
	rec := runRecord(t, root, result.WorkID)
	if got := phaseNames(rec); !contains(got, workrec.PhaseReview) {
		t.Errorf("phases = %v, want review among them", got)
	}
	if len(rec.Agents) != 2 || rec.Agents[1].Role != "reviewer" || rec.Agents[1].Runtime != "omp" {
		t.Fatalf("agents = %+v, want the reviewer last", rec.Agents)
	}
	// The review is a phase but not a stage the recheck depends on: it
	// runs after the recheck passed, so the order is recheck then review.
	names := phaseNames(rec)
	recheck, review := indexOf(names, workrec.PhaseRecheck), indexOf(names, workrec.PhaseReview)
	if recheck < 0 || review < 0 || review < recheck {
		t.Errorf("phases = %v, want recheck before review", names)
	}
	if _, err := os.Stat(result.Review.ResultPath); err != nil {
		t.Errorf("review message missing at %s: %v", result.Review.ResultPath, err)
	}
}

// A contract that names no explorer and no reviewer runs exactly as every
// milestone before M6 ran: the same four phases, in the same order.
func TestADefaultContractStampsTheSameFourPhases(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), roleConfig(root, repairRunner{path: "fixed.txt", body: "verified fix\n"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rec := runRecord(t, root, result.WorkID)
	want := []string{workrec.PhaseBaseline, workrec.PhaseHandoff, workrec.PhaseRecheck, workrec.PhaseDeliver}
	got := phaseNames(rec)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("phases = %v, want %v", got, want)
	}
	if len(rec.Agents) != 1 || rec.Agents[0].Role != "builder" {
		t.Errorf("agents = %+v, want the builder alone", rec.Agents)
	}
}

// The receipt attests what ran, so a three-agent run has to name three
// agents — and the review that goes with them has to say it was advisory.
func TestReceiptNamesEveryAgentTheRunSpawned(t *testing.T) {
	root := fixtureAdoptedRepository(t)
	cfg := roleConfig(root, repairRunner{path: "fixed.txt", body: "verified fix\n"})
	cfg.Explorer = explorerRole(messageRunner{message: "the loader lives in internal/config\n"})
	cfg.Reviewer = reviewerRole(messageRunner{message: "REVIEW-MARKER: nothing blocking.\n"})

	result, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	receipt, _ := readReceipt(t, root, result.WorkID)
	if len(receipt.Roles) != 3 {
		t.Fatalf("roles = %+v, want three", receipt.Roles)
	}
	wantRoles := []string{"explorer", "builder", "reviewer"}
	for i, want := range wantRoles {
		if receipt.Roles[i].Role != want {
			t.Errorf("roles[%d].role = %q, want %q", i, receipt.Roles[i].Role, want)
		}
	}
	if receipt.Roles[0].Runtime != "omp" || receipt.Roles[1].Runtime != "codex" {
		t.Errorf("roles = %+v, want the explorer on omp and the builder on codex", receipt.Roles)
	}
	if len(receipt.Review) != 1 {
		t.Fatalf("review = %+v, want one finding", receipt.Review)
	}
	if receipt.Review[0].Disposition != "advisory: recorded, not a gate" {
		t.Errorf("disposition = %q, want the advisory disposition", receipt.Review[0].Disposition)
	}
	if !strings.Contains(receipt.Review[0].Finding, "REVIEW-MARKER") {
		t.Errorf("finding = %q, want the reviewer's own words", receipt.Review[0].Finding)
	}
	if receipt.Review[0].Agent != "reviewer" {
		t.Errorf("review agent = %q, want reviewer", receipt.Review[0].Agent)
	}
}

func contains(haystack []string, needle string) bool {
	return indexOf(haystack, needle) >= 0
}

func indexOf(haystack []string, needle string) int {
	for i, s := range haystack {
		if s == needle {
			return i
		}
	}
	return -1
}
