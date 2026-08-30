package improve

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/verify"
)

func TestCreateHandoffWritesOnlyFailedGatesToPrompt(t *testing.T) {
	root := fixtureRepository(t)
	report := &verify.Report{
		Gates: []verify.GateResult{
			{ID: "lint", Status: verify.StatusFail, OutputTail: "unexpected semicolon"},
			{ID: "test", Status: verify.StatusSkip, Reason: "skipped: gate lint failed"},
		},
		Warnings: []string{"review-only large file warning"},
	}
	runner := &recordingRunner{response: "fixed lint"}

	handoff, err := CreateHandoff(context.Background(), root, report, runner)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := os.ReadFile(handoff.PromptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "unexpected semicolon") {
		t.Fatalf("prompt missing failed gate detail: %s", prompt)
	}
	if strings.Contains(string(prompt), "review-only large file warning") {
		t.Fatalf("prompt must not ask Codex to repair warnings: %s", prompt)
	}
	result, err := os.ReadFile(handoff.ResultPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != "fixed lint" {
		t.Fatalf("result = %q, want Codex response", result)
	}
	if runner.root != root || runner.promptPath != handoff.PromptPath {
		t.Fatalf("runner received root=%q prompt=%q, want root=%q prompt=%q", runner.root, runner.promptPath, root, handoff.PromptPath)
	}
}

func TestCreateHandoffRedactsCodexFinalMessage(t *testing.T) {
	root := fixtureRepository(t)
	report := &verify.Report{Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}}
	runner := &recordingRunner{response: "token sk-abcdefghijklmnopqrstuvwx at /Users/alice/private"}

	handoff, err := CreateHandoff(context.Background(), root, report, runner)
	if err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(handoff.ResultPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result), "sk-abcdefghijklmnopqrstuvwx") || strings.Contains(string(result), "/Users/alice/") {
		t.Fatalf("Codex result leaked sensitive text: %q", result)
	}
	if !strings.Contains(string(result), "<redacted:api-key>") || !strings.Contains(string(result), "<redacted:user-path>") {
		t.Fatalf("Codex result did not record redactions: %q", result)
	}
}

func TestCreateHandoffRedactsFinalMessageWhenRunnerFails(t *testing.T) {
	root := fixtureRepository(t)
	report := &verify.Report{Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}}
	handoff, err := CreateHandoff(context.Background(), root, report, failingMessageRunner{})
	if err == nil {
		t.Fatal("CreateHandoff error = nil, want runner failure")
	}
	result, readErr := os.ReadFile(handoff.ResultPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(result), "sk-abcdefghijklmnopqrstuvwx") || !strings.Contains(string(result), "<redacted:api-key>") {
		t.Fatalf("failed handoff result = %q, want redacted output", result)
	}
}

func TestCodexRunnerArgsUseConfiguredModelAndEffort(t *testing.T) {
	args := (CodexRunner{Binary: "codex", Model: "gpt-5.6-sol", Effort: "high"}).args("/repo", "/tmp/result.md")
	joined := strings.Join(args, "\n")
	for _, want := range []string{"--model\ngpt-5.6-sol", `model_reasoning_effort="high"`, "sandbox_workspace_write.network_access=false"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args = %q, missing %q", args, want)
		}
	}
}

type recordingRunner struct {
	root       string
	promptPath string
	response   string
}

func (r *recordingRunner) Run(_ context.Context, root, promptPath, outputPath string) error {
	r.root = root
	r.promptPath = promptPath
	return os.WriteFile(outputPath, []byte(r.response), 0o600)
}

type failingMessageRunner struct{}

func (failingMessageRunner) Run(_ context.Context, _, _, outputPath string) error {
	if err := os.WriteFile(outputPath, []byte("token sk-abcdefghijklmnopqrstuvwx"), 0o600); err != nil {
		return err
	}
	return errors.New("Codex failed")
}
