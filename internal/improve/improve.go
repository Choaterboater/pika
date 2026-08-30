package improve

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Choaterboater/pika/internal/verify"
)

// ErrDirtyTree prevents Pika from mixing an automated repair with work the
// caller has not committed yet.
var ErrDirtyTree = errors.New("improve: working tree must be clean")

// ErrNoChanges prevents a misleading empty commit after an agent run.
var ErrNoChanges = errors.New("improve: Codex made no changes to commit")

// CheckFunc runs Pika's deterministic ladder and returns its full report.
// The command layer supplies the same in-process check engine used by
// `pika check`; tests provide real, controlled reports.
type CheckFunc func() (*verify.Report, error)

// Config configures a single improvement transaction.
type Config struct {
	Root   string
	Branch string
	Check  CheckFunc
	Runner Runner
}

// Result is the complete local outcome. Any error return may still include a
// branch, handoff bundle, and baseline report so the caller can inspect the
// uncommitted state without Pika concealing it.
type Result struct {
	Branch       string         `json:"branch,omitempty"`
	Commit       string         `json:"commit,omitempty"`
	ChangedFiles []string       `json:"changedFiles,omitempty"`
	Handoff      Handoff        `json:"handoff,omitempty"`
	ChecksBefore *verify.Report `json:"checksBefore,omitempty"`
	ChecksAfter  *verify.Report `json:"checksAfter,omitempty"`
}

// Run executes the safe local repair loop. It intentionally contains no
// network, push, PR, or merge operation: the successful commit remains on the
// local branch for the caller to review and publish separately.
func Run(ctx context.Context, cfg Config) (Result, error) {
	if strings.TrimSpace(cfg.Root) == "" {
		return Result{}, errors.New("improve: repository root is required")
	}
	if strings.TrimSpace(cfg.Branch) == "" {
		return Result{}, errors.New("improve: branch is required")
	}
	if cfg.Check == nil {
		return Result{}, errors.New("improve: check function is required")
	}
	if dirty, err := runGit(ctx, cfg.Root, "status", "--porcelain"); err != nil {
		return Result{}, err
	} else if strings.TrimSpace(dirty) != "" {
		return Result{}, ErrDirtyTree
	}

	before, err := cfg.Check()
	if err != nil {
		return Result{}, fmt.Errorf("improve: baseline checks: %w", err)
	}
	if before == nil {
		return Result{}, errors.New("improve: baseline checks returned no report")
	}
	result := Result{ChecksBefore: before}
	if before.Pass {
		return result, nil
	}
	if cfg.Runner == nil {
		return result, errors.New("improve: agent runner is required for failed checks")
	}
	if _, err := runGit(ctx, cfg.Root, "switch", "-c", cfg.Branch); err != nil {
		return result, err
	}
	result.Branch = cfg.Branch
	// Task 4 replaces this with the run record's own (*workrec.Handle).HandoffDir().
	bundleDir := filepath.Join(cfg.Root, handoffStateDir, fmt.Sprintf("%d", time.Now().UTC().UnixNano()))
	handoff, err := CreateHandoff(ctx, cfg.Root, bundleDir, before, cfg.Runner)
	result.Handoff = handoff
	if err != nil {
		return result, err
	}
	state, err := currentGitState(ctx, cfg.Root)
	if err != nil {
		return result, err
	}
	if state.Branch != cfg.Branch {
		return result, fmt.Errorf("improve: expected branch %q after handoff, found %q", cfg.Branch, state.Branch)
	}
	if _, err := runGit(ctx, cfg.Root, "reset"); err != nil {
		return result, err
	}
	after, err := cfg.Check()
	if err != nil {
		return result, fmt.Errorf("improve: post-handoff checks: %w", err)
	}
	if after == nil {
		return result, errors.New("improve: post-handoff checks returned no report")
	}
	result.ChecksAfter = after
	if !after.Pass {
		return result, errors.New("improve: post-handoff checks failed; changes left uncommitted")
	}
	changed, err := runGit(ctx, cfg.Root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return result, err
	}
	result.ChangedFiles = changePaths(statusPaths(changed))
	if len(result.ChangedFiles) == 0 {
		return result, ErrNoChanges
	}
	state, err = currentGitState(ctx, cfg.Root)
	if err != nil {
		return result, err
	}
	if state.Branch != cfg.Branch {
		return result, fmt.Errorf("improve: expected branch %q before commit, found %q", cfg.Branch, state.Branch)
	}
	addArgs := append([]string{"add", "--"}, result.ChangedFiles...)
	if _, err := runGit(ctx, cfg.Root, addArgs...); err != nil {
		return result, err
	}
	if _, err := runGit(ctx, cfg.Root, "commit", "-m", "chore: improve verified findings"); err != nil {
		return result, err
	}
	commit, err := runGit(ctx, cfg.Root, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	result.Commit = strings.TrimSpace(commit)
	return result, nil
}

func changePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path != handoffStateDir && !strings.HasPrefix(path, handoffStateDir+"/") {
			out = append(out, path)
		}
	}
	return out
}

func runGit(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("improve: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func statusPaths(value string) []string {
	var out []string
	for _, line := range strings.Split(value, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if before, after, renamed := strings.Cut(path, " -> "); renamed {
			path = after
			_ = before
		}
		if path != "" {
			out = append(out, path)
		}
	}
	return out
}
