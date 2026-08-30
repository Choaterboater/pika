package improve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/workrec"
)

func TestRunCommitsOnlyAfterVerifiedRecheck(t *testing.T) {
	root := fixtureRepository(t)
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail, OutputTail: "repair this"}}},
		{Pass: true, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusPass}}},
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Runner: repairRunner{path: "fixed.txt", body: "verified fix\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ChecksBefore.Pass || !result.ChecksAfter.Pass {
		t.Fatalf("checks before=%+v after=%+v, want failing baseline and passing recheck", result.ChecksBefore, result.ChecksAfter)
	}
	if result.Branch != "chore/pika-improve" || result.Commit == "" {
		t.Fatalf("result = %+v, want branch and commit", result)
	}
	if got := gitOutput(t, root, "branch", "--show-current"); got != "chore/pika-improve" {
		t.Fatalf("branch = %q, want chore/pika-improve", got)
	}
	if got := gitOutput(t, root, "show", "--format=%s", "--no-patch", "HEAD"); got != "chore: improve verified findings" {
		t.Fatalf("commit subject = %q", got)
	}
	if got := gitOutput(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("status = %q, want clean after verified commit", got)
	}
}

func TestRunGreenBaselineDoesNotRequireAgentOrCreateBranch(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			return &verify.Report{Pass: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Branch != "" || result.Commit != "" || result.Handoff.Dir != "" {
		t.Fatalf("result = %+v, want no branch, handoff, or commit", result)
	}
	if got := gitOutput(t, root, "branch", "--show-current"); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
}

func TestRunRefusesDirtyTreeBeforeChecks(t *testing.T) {
	root := fixtureRepository(t)
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			t.Fatal("checks must not run on a dirty tree")
			return nil, nil
		},
	})
	if !errors.Is(err, ErrDirtyTree) {
		t.Fatalf("error = %v, want ErrDirtyTree", err)
	}
}

func TestRunLeavesFailedRecheckUncommitted(t *testing.T) {
	root := fixtureRepository(t)
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}},
		{Pass: false, Gates: []verify.GateResult{{ID: "test", Status: verify.StatusFail}}},
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Runner: repairRunner{path: "fixed.txt", body: "needs review\n"},
	})
	if err == nil || !strings.Contains(err.Error(), "post-handoff checks failed") {
		t.Fatalf("error = %v, want failed recheck", err)
	}
	if result.Commit != "" || result.Branch != "chore/pika-improve" {
		t.Fatalf("result = %+v, want branch without commit", result)
	}
	if got := gitOutput(t, root, "status", "--porcelain"); !strings.Contains(got, "fixed.txt") {
		t.Fatalf("status = %q, want uncommitted agent edit", got)
	}
}

func TestRunRejectsAgentCreatedCommitBeforeRecheck(t *testing.T) {
	root := fixtureRepository(t)
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}},
		{Pass: true},
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Runner: committingRunner{},
	})
	if err == nil || !strings.Contains(err.Error(), "changed Git state") {
		t.Fatalf("error = %v, want agent commit refusal", err)
	}
	if result.ChecksAfter != nil {
		t.Fatalf("post-handoff checks ran after agent commit: %+v", result.ChecksAfter)
	}
}

func TestRunRejectsAgentBranchSwitchBeforeRecheck(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			return &verify.Report{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}}, nil
		},
		Runner: switchingRunner{},
	})
	if err == nil || !strings.Contains(err.Error(), "Git state") {
		t.Fatalf("error = %v, want branch-switch refusal", err)
	}
	if result.Branch != "chore/pika-improve" {
		t.Fatalf("result branch = %q", result.Branch)
	}
}

func TestRunRejectsAgentRewriteOfAnotherBranch(t *testing.T) {
	root := fixtureRepository(t)
	gitRun(t, root, "commit", "--allow-empty", "-qm", "second")
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			return &verify.Report{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}}, nil
		},
		Runner: rewritingRunner{},
	})
	if err == nil || !strings.Contains(err.Error(), "Git state") {
		t.Fatalf("error = %v, want ref-rewrite refusal", err)
	}
	if result.ChecksAfter != nil {
		t.Fatalf("post-handoff checks ran after ref rewrite: %+v", result.ChecksAfter)
	}
}

func TestRunRejectsPendingMergeState(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			return &verify.Report{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}}, nil
		},
		Runner: pendingMergeRunner{},
	})
	if err == nil || !strings.Contains(err.Error(), "pending Git operation") {
		t.Fatalf("error = %v, want pending merge refusal", err)
	}
	if result.ChecksAfter != nil {
		t.Fatalf("post-handoff checks ran with pending merge: %+v", result.ChecksAfter)
	}
}

// The bundle moved out of `.project/state/handoffs` and into the run
// record, so a filter naming the retired directory stopped covering it.
// The fixture here deliberately does NOT gitignore `.project/state`: once
// it is ignored, Git never offers the record or the bundle to a commit at
// all, and this test would pass no matter what changePaths filtered. An
// un-ignored state directory is the only world in which the filter is the
// thing standing between Pika's private state and the commit.
func TestRunDoesNotCommitAgentStagedPrivateState(t *testing.T) {
	root := fixtureRepositoryWithoutStateIgnore(t)
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}},
		{Pass: true},
	}
	runner := &stagingRunner{}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Without this the test could keep passing against the retired path
	// while the real bundle went uncovered.
	wantPrefix := ".project/state/work/" + result.WorkID + "/handoff/"
	if !strings.HasPrefix(runner.staged, wantPrefix) {
		t.Fatalf("agent staged %q, want a path under %q: the filter is only proven at the bundle's real location", runner.staged, wantPrefix)
	}
	files := gitOutput(t, root, "show", "--format=", "--name-only", "HEAD")
	if strings.Contains(files, ".project/state") || !strings.Contains(files, "fixed.txt") {
		t.Fatalf("committed files = %q, want fixed.txt without private state", files)
	}
	for _, path := range result.ChangedFiles {
		if strings.HasPrefix(path, ".project/state") {
			t.Fatalf("changed files = %v, want nothing under .project/state", result.ChangedFiles)
		}
	}
}

// A run that is interrupted is only recoverable if every transition
// reached the disk before the next one started. This asserts the whole
// history, not just its head: a record that jumped from baseline to
// deliver would be a record `pika resume` cannot trust.
func TestRunRecordsEveryPhaseTransition(t *testing.T) {
	root := fixtureRepository(t)
	baseCommit := gitOutput(t, root, "rev-parse", "HEAD")
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail, OutputTail: "repair this"}}},
		{Pass: true, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusPass}}},
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Runner: repairRunner{path: "fixed.txt", body: "verified fix\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkID == "" {
		t.Fatal("result.WorkID is empty: a run the caller cannot name is the state M2 removes")
	}
	rec := runRecord(t, root, result.WorkID)
	want := []string{workrec.PhaseBaseline, workrec.PhaseHandoff, workrec.PhaseRecheck, workrec.PhaseDeliver}
	if got := phaseNames(rec); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("phases = %v, want %v", got, want)
	}
	if rec.Phase != workrec.PhaseDeliver || rec.Outcome != workrec.OutcomeComplete {
		t.Fatalf("phase = %q outcome = %q, want deliver and complete", rec.Phase, rec.Outcome)
	}
	if rec.Kind != workrec.KindRepair {
		t.Fatalf("kind = %q, want %q by default", rec.Kind, workrec.KindRepair)
	}
	if rec.Branch != "chore/pika-improve" || rec.Commit != result.Commit || rec.BaseCommit != baseCommit {
		t.Fatalf("record = %+v, want branch chore/pika-improve, commit %s, base commit %s", rec, result.Commit, baseCommit)
	}
	if rec.Baseline == nil || rec.Baseline.Pass {
		t.Fatalf("record baseline = %+v, want the failing baseline report", rec.Baseline)
	}
	if rec.Recheck == nil || !rec.Recheck.Pass {
		t.Fatalf("record recheck = %+v, want the passing recheck report", rec.Recheck)
	}
	for i := 1; i < len(rec.Phases); i++ {
		if rec.Phases[i].At.Before(rec.Phases[i-1].At) {
			t.Fatalf("phase %q stamped before %q: the history must be ordered", rec.Phases[i].Phase, rec.Phases[i-1].Phase)
		}
	}
}

// Repair work with nothing to repair is finished, and a finished run is
// recorded as such — with no branch, because there was nothing to put on
// one.
func TestGreenBaselineRecordsCompleteWithoutBranching(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			return &verify.Report{Pass: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := runRecord(t, root, result.WorkID)
	if got, want := phaseNames(rec), []string{workrec.PhaseBaseline}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("phases = %v, want %v", got, want)
	}
	if rec.Outcome != workrec.OutcomeComplete || rec.Reason != "" {
		t.Fatalf("outcome = %q reason = %q, want complete with no reason", rec.Outcome, rec.Reason)
	}
	if rec.Branch != "" || rec.Commit != "" {
		t.Fatalf("record = %+v, want no branch and no commit", rec)
	}
	if rec.Baseline == nil || !rec.Baseline.Pass {
		t.Fatalf("record baseline = %+v, want the green baseline report", rec.Baseline)
	}
	if got := gitOutput(t, root, "branch", "--show-current"); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
}

// The single place the two kinds diverge. A green ladder means repair
// work is done; it says nothing about whether a goal has been met, so
// feature work goes to the agent with the goal as its work statement and
// then through the same recheck and commit as any repair.
func TestFeatureKindProceedsToHandoffOnGreenBaseline(t *testing.T) {
	root := fixtureRepository(t)
	const goal = "add a CHANGELOG entry for the release"
	checks := []*verify.Report{{Pass: true}, {Pass: true}}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "feat/pika-work",
		Kind:   workrec.KindFeature,
		Goal:   goal,
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Runner: repairRunner{path: "CHANGELOG.md", body: "# Changelog\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := runRecord(t, root, result.WorkID)
	want := []string{workrec.PhaseBaseline, workrec.PhaseHandoff, workrec.PhaseRecheck, workrec.PhaseDeliver}
	if got := phaseNames(rec); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("phases = %v, want %v: a green ladder must not end feature work", got, want)
	}
	if rec.Kind != workrec.KindFeature || rec.Goal != goal {
		t.Fatalf("record = %+v, want feature kind carrying the goal", rec)
	}
	if result.Branch != "feat/pika-work" || result.Commit == "" {
		t.Fatalf("result = %+v, want a commit on the feature branch", result)
	}
	prompt, err := os.ReadFile(result.Handoff.PromptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), goal) {
		t.Fatalf("prompt = %s, want the goal as the work statement", prompt)
	}
	if want := filepath.Join(root, ".project", "state", "work", result.WorkID, "handoff"); result.Handoff.Dir != want {
		t.Fatalf("bundle = %q, want the run record's own %q", result.Handoff.Dir, want)
	}
}

// A blocked run's record is the only place an operator learns why. The
// reason is the error verbatim, and the branch the agent's work was left
// on is recorded even though the handoff never completed.
func TestAgentFailureRecordsBlockedWithReason(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			return &verify.Report{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}}, nil
		},
		Runner: failingMessageRunner{},
	})
	if err == nil {
		t.Fatal("Run error = nil, want the agent failure")
	}
	rec := runRecord(t, root, result.WorkID)
	if rec.Outcome != workrec.OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", rec.Outcome)
	}
	if rec.Reason != err.Error() {
		t.Fatalf("reason = %q, want the returned error verbatim %q", rec.Reason, err.Error())
	}
	if !strings.Contains(rec.Reason, "Codex failed") {
		t.Fatalf("reason = %q, want the agent's own failure", rec.Reason)
	}
	if rec.Phase != workrec.PhaseBaseline {
		t.Fatalf("phase = %q, want baseline: the handoff phase never completed", rec.Phase)
	}
	if rec.Branch != "chore/pika-improve" {
		t.Fatalf("record branch = %q, want the branch the run left behind", rec.Branch)
	}
	if got := gitOutput(t, root, "branch", "--list", "chore/pika-improve"); got == "" {
		t.Fatal("the branch the record names does not exist")
	}
}

// A refusal that happens before the run does anything must leave nothing
// behind. A directory of empty runs would make every real record harder
// to trust.
func TestDirtyTreeRefusalWritesNoRecord(t *testing.T) {
	root := fixtureRepository(t)
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			t.Fatal("checks must not run on a dirty tree")
			return nil, nil
		},
	})
	if !errors.Is(err, ErrDirtyTree) {
		t.Fatalf("error = %v, want ErrDirtyTree", err)
	}
	if result.WorkID != "" {
		t.Fatalf("result.WorkID = %q, want none: the run never started", result.WorkID)
	}
	work := filepath.Join(root, ".project", "state", "work")
	if _, err := os.Stat(work); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) = %v, want not-exist", work, err)
	}
	runs, err := workrec.List(repoRoot(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("workrec.List = %+v, want no runs", runs)
	}
}

func repoRoot(t *testing.T, root string) *repopath.Root {
	t.Helper()
	resolved, err := repopath.At(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// runRecord reads back what the run durably wrote, never what it returned
// in memory: the record is the artifact under test.
func runRecord(t *testing.T, root, workID string) workrec.Record {
	t.Helper()
	handle, err := workrec.Open(repoRoot(t, root), workID)
	if err != nil {
		t.Fatal(err)
	}
	return handle.Record()
}

func phaseNames(rec workrec.Record) []string {
	names := make([]string, 0, len(rec.Phases))
	for _, stamp := range rec.Phases {
		names = append(names, stamp.Phase)
	}
	return names
}

// fixtureRepository builds an adopted repository: one that gitignores
// Pika's private state, as `pika init` leaves it.
func fixtureRepository(t *testing.T) string {
	t.Helper()
	return newFixture(t, ".project/state/\n")
}

// fixtureRepositoryWithoutStateIgnore builds one that does not, so the
// run record and the handoff bundle are offered to Git like any other
// untracked file.
func fixtureRepositoryWithoutStateIgnore(t *testing.T) string {
	t.Helper()
	return newFixture(t, "")
}

func newFixture(t *testing.T, gitignore string) string {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, "init", "-q", "-b", "main")
	gitRun(t, root, "config", "user.name", "Pika Test")
	gitRun(t, root, "config", "user.email", "pika@example.test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "README.md", ".gitignore")
	gitRun(t, root, "commit", "-qm", "initial")
	return root
}

func gitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}

type repairRunner struct {
	path string
	body string
}

type committingRunner struct{}

func (committingRunner) Run(_ context.Context, root, _, outputPath string) error {
	if err := os.WriteFile(filepath.Join(root, "agent.txt"), []byte("not allowed\n"), 0o644); err != nil {
		return err
	}
	cmd := exec.Command("git", "add", "agent.txt")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w: %s", err, output)
	}
	cmd = exec.Command("git", "commit", "-m", "agent commit")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, output)
	}
	return os.WriteFile(outputPath, []byte("committed\n"), 0o600)
}

type switchingRunner struct{}

func (switchingRunner) Run(_ context.Context, root, _, outputPath string) error {
	cmd := exec.Command("git", "switch", "main")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git switch: %w: %s", err, output)
	}
	return os.WriteFile(outputPath, []byte("switched\n"), 0o600)
}

// stagingRunner is the agent that force-adds Pika's own private state.
// It finds the bundle from the prompt it was handed rather than from a
// hard-coded path, so it stages wherever the run record actually put the
// bundle and cannot silently go on testing a location Pika retired.
type stagingRunner struct {
	staged string
}

func (r *stagingRunner) Run(_ context.Context, root, promptPath, outputPath string) error {
	if err := os.WriteFile(filepath.Join(root, "fixed.txt"), []byte("fixed\n"), 0o644); err != nil {
		return err
	}
	statePath := filepath.Join(filepath.Dir(promptPath), "private.txt")
	if err := os.WriteFile(statePath, []byte("private\n"), 0o600); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, statePath)
	if err != nil {
		return err
	}
	r.staged = filepath.ToSlash(rel)
	cmd := exec.Command("git", "add", "-f", "--", r.staged)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add private state: %w: %s", err, output)
	}
	return os.WriteFile(outputPath, []byte("staged private state\n"), 0o600)
}

type rewritingRunner struct{}

func (rewritingRunner) Run(_ context.Context, root, _, outputPath string) error {
	cmd := exec.Command("git", "branch", "-f", "main", "HEAD~1")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git branch -f: %w: %s", err, output)
	}
	return os.WriteFile(outputPath, []byte("rewrote main\n"), 0o600)
}

type pendingMergeRunner struct{}

func (pendingMergeRunner) Run(_ context.Context, root, _, outputPath string) error {
	cmd := exec.Command("git", "rev-parse", "--git-path", "MERGE_HEAD")
	cmd.Dir = root
	path, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git merge path: %w", err)
	}
	mergePath := strings.TrimSpace(string(path))
	if !filepath.IsAbs(mergePath) {
		mergePath = filepath.Join(root, mergePath)
	}
	if err := os.WriteFile(mergePath, []byte("pending\n"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(outputPath, []byte("merge pending\n"), 0o600)
}

func (r repairRunner) Run(_ context.Context, root, _, outputPath string) error {
	if err := os.WriteFile(filepath.Join(root, r.path), []byte(r.body), 0o644); err != nil {
		return err
	}
	return os.WriteFile(outputPath, []byte("repaired\n"), 0o600)
}
