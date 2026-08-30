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

	"github.com/Choaterboater/pika/internal/verify"
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

func TestRunDoesNotCommitAgentStagedPrivateState(t *testing.T) {
	root := fixtureRepository(t)
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}},
		{Pass: true},
	}
	_, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Runner: stagingRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := gitOutput(t, root, "show", "--format=", "--name-only", "HEAD")
	if strings.Contains(files, ".project/state") || !strings.Contains(files, "fixed.txt") {
		t.Fatalf("committed files = %q, want fixed.txt without private state", files)
	}
}

func fixtureRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, "init", "-q", "-b", "main")
	gitRun(t, root, "config", "user.name", "Pika Test")
	gitRun(t, root, "config", "user.email", "pika@example.test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".project/state/\n"), 0o644); err != nil {
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

type stagingRunner struct{}

func (stagingRunner) Run(_ context.Context, root, _, outputPath string) error {
	if err := os.WriteFile(filepath.Join(root, "fixed.txt"), []byte("fixed\n"), 0o644); err != nil {
		return err
	}
	statePath := filepath.Join(root, ".project", "state", "handoffs", "private.txt")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(statePath, []byte("private\n"), 0o600); err != nil {
		return err
	}
	cmd := exec.Command("git", "add", "-f", ".project/state/handoffs/private.txt")
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
