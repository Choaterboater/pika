// Package improve implements the local, verified repair loop used by
// `pika handoff` and `pika improve`.
package improve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Choaterboater/pika/internal/redact"
	"github.com/Choaterboater/pika/internal/verify"
)

const handoffStateDir = ".project/state/handoffs"

// ErrNoActionableFindings indicates that a check report has no failed gates
// to give an agent. Warnings intentionally do not count as repair work.
var ErrNoActionableFindings = errors.New("improve: no actionable failed check gates")

// Runner is the narrow external-agent boundary. Tests use a runner that
// writes a deterministic final message, while production uses CodexRunner.
type Runner interface {
	Run(ctx context.Context, root, promptPath, outputPath string) error
}

// CodexRunner starts a non-interactive Codex session restricted to the target
// repository's writable workspace. It deliberately has no bypass mode.
type CodexRunner struct {
	Binary string
	Model  string
	Effort string
}

// Run invokes `codex exec` with the prompt supplied on stdin. The final agent
// message is written by Codex itself to outputPath for the local run bundle.
func (r CodexRunner) Run(ctx context.Context, root, promptPath, outputPath string) error {
	binary := r.Binary
	if binary == "" {
		binary = "codex"
	}
	prompt, err := os.Open(promptPath)
	if err != nil {
		return fmt.Errorf("open handoff prompt: %w", err)
	}
	defer prompt.Close()
	cmd := exec.CommandContext(ctx, binary, r.args(root, outputPath)...)
	cmd.Stdin = prompt
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("codex handoff: %w", err)
	}
	return nil
}

func (r CodexRunner) args(root, outputPath string) []string {
	args := []string{"exec", "-c", "sandbox_workspace_write.network_access=false"}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	if r.Effort != "" {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", r.Effort))
	}
	return append(args, "--sandbox", "workspace-write", "--approve-for-me", "--cd", root, "--output-last-message", outputPath, "-")
}

// Handoff identifies the private, redacted files created for an agent run.
// None of these paths may be committed: `.project/state` is local-only.
type Handoff struct {
	Dir        string `json:"dir"`
	ReportPath string `json:"reportPath"`
	PromptPath string `json:"promptPath"`
	ResultPath string `json:"resultPath"`
}

// CreateHandoff writes a private redacted report and a repair-only prompt,
// then runs the supplied agent. It never puts warning-only findings into the
// prompt: documented exceptions and review signals are not destructive work.
func CreateHandoff(ctx context.Context, root string, report *verify.Report, runner Runner) (Handoff, error) {
	if report == nil {
		return Handoff{}, errors.New("improve: check report is required")
	}
	if runner == nil {
		return Handoff{}, errors.New("improve: agent runner is required")
	}
	failed := failedGates(report)
	if len(failed) == 0 {
		return Handoff{}, ErrNoActionableFindings
	}
	stateBefore, err := currentGitState(ctx, root)
	if err != nil {
		return Handoff{}, err
	}
	dir := filepath.Join(root, handoffStateDir, fmt.Sprintf("%d", time.Now().UTC().UnixNano()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Handoff{}, fmt.Errorf("create handoff directory: %w", err)
	}
	handoff := Handoff{
		Dir:        dir,
		ReportPath: filepath.Join(dir, "checks-before.json"),
		PromptPath: filepath.Join(dir, "prompt.md"),
		ResultPath: filepath.Join(dir, "codex-last-message.md"),
	}
	rawResultPath := filepath.Join(dir, "codex-last-message.raw")
	defer os.Remove(rawResultPath)
	reportJSON, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return Handoff{}, fmt.Errorf("encode check report: %w", err)
	}
	if err := os.WriteFile(handoff.ReportPath, []byte(redact.Apply(string(reportJSON))+"\n"), 0o600); err != nil {
		return Handoff{}, fmt.Errorf("write check report: %w", err)
	}
	prompt := buildPrompt(failed)
	if err := os.WriteFile(handoff.PromptPath, []byte(redact.Apply(prompt)), 0o600); err != nil {
		return Handoff{}, fmt.Errorf("write handoff prompt: %w", err)
	}
	runErr := runner.Run(ctx, root, handoff.PromptPath, rawResultPath)
	redactErr := redactResult(rawResultPath, handoff.ResultPath)
	if runErr != nil {
		if redactErr != nil && !errors.Is(redactErr, os.ErrNotExist) {
			return handoff, fmt.Errorf("%w; redact Codex final message: %v", runErr, redactErr)
		}
		return handoff, runErr
	}
	if redactErr != nil {
		return handoff, redactErr
	}
	stateAfter, err := currentGitState(ctx, root)
	if err != nil {
		return handoff, err
	}
	if stateBefore != stateAfter {
		return handoff, errors.New("improve: agent changed Git state; changes left for manual inspection")
	}
	return handoff, nil
}

func redactResult(rawPath, resultPath string) error {
	result, err := os.ReadFile(rawPath)
	if err != nil {
		return fmt.Errorf("codex handoff did not write final message: %w", err)
	}
	if err := os.WriteFile(resultPath, []byte(redact.Apply(string(result))), 0o600); err != nil {
		return fmt.Errorf("redact codex final message: %w", err)
	}
	return nil
}

type gitState struct {
	Head   string
	Branch string
	Refs   string
}

func currentGitState(ctx context.Context, root string) (gitState, error) {
	if err := ensureNoPendingGitOperation(ctx, root); err != nil {
		return gitState{}, err
	}
	head, err := gitValue(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return gitState{}, err
	}
	branch, err := gitValue(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return gitState{}, err
	}
	refs, err := gitValue(ctx, root, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return gitState{}, err
	}
	return gitState{Head: head, Branch: branch, Refs: refs}, nil
}

func ensureNoPendingGitOperation(ctx context.Context, root string) error {
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		path, err := gitValue(ctx, root, "rev-parse", "--git-path", name)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("improve: pending Git operation at %s", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("improve: inspect Git operation %s: %w", name, err)
		}
	}
	return nil
}

func gitValue(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("improve: read Git state: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func failedGates(report *verify.Report) []verify.GateResult {
	failed := make([]verify.GateResult, 0)
	for _, gate := range report.Gates {
		if gate.Status == verify.StatusFail {
			failed = append(failed, gate)
		}
	}
	return failed
}

func buildPrompt(failed []verify.GateResult) string {
	var b strings.Builder
	b.WriteString("You are the builder in a verified Pika repair run. Investigate and fix only the failed Pika check gates below. Work in the current repository. Do not run git commit, git merge, git rebase, git push, or any GitHub command; Pika verifies and commits approved changes itself. Do not change vendor assets, public filenames, or generated outputs merely to silence a warning; those must be represented by a documented Pika exception when intentional. Run focused tests while repairing.\n\n")
	b.WriteString("## Actionable failed gates\n")
	for _, gate := range failed {
		fmt.Fprintf(&b, "\n### %s\n", gate.ID)
		if gate.Reason != "" {
			fmt.Fprintf(&b, "%s\n", gate.Reason)
		}
		if gate.OutputTail != "" {
			fmt.Fprintf(&b, "```text\n%s\n```\n", gate.OutputTail)
		}
	}
	return b.String()
}
