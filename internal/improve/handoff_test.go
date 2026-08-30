package improve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/verify"
)

// The bundle used to be minted at `.project/state/handoffs/<unixnano>/`: a
// path with no run identity that nothing in the repository ever read back.
// CreateHandoff now writes into the directory it is handed, so the run record
// that owns the run also owns its bundle.
func TestCreateHandoffWritesBundleIntoTheGivenDirectory(t *testing.T) {
	root := fixtureRepository(t)
	bundle := recordBundleDir(t, root)
	report := &verify.Report{Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail, OutputTail: "unexpected semicolon"}}}

	handoff, err := CreateHandoff(context.Background(), root, bundle, report, &recordingRunner{response: "fixed lint"})
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Dir != bundle {
		t.Fatalf("handoff.Dir = %q, want the directory it was given %q", handoff.Dir, bundle)
	}
	for name, got := range map[string]string{
		"checks-before.json":    handoff.ReportPath,
		"prompt.md":             handoff.PromptPath,
		"codex-last-message.md": handoff.ResultPath,
	} {
		want := filepath.Join(bundle, name)
		if got != want {
			t.Fatalf("%s path = %q, want %q", name, got, want)
		}
		info, err := os.Stat(want)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, want 0600: bundle files are private", name, info.Mode().Perm())
		}
	}
	info, err := os.Stat(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("bundle dir mode = %v, want 0700", info.Mode().Perm())
	}
	legacy := filepath.Join(root, ".project", "state", "handoffs")
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) = %v, want not-exist: CreateHandoff must mint no directory of its own", legacy, err)
	}
}

// The raw Codex message is the only unredacted file the bundle ever holds.
// It must not outlive the redaction that consumes it.
func TestCreateHandoffRemovesTheRawLastMessage(t *testing.T) {
	root := fixtureRepository(t)
	bundle := recordBundleDir(t, root)
	report := &verify.Report{Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}}

	if _, err := CreateHandoff(context.Background(), root, bundle, report, &recordingRunner{response: "token sk-abcdefghijklmnopqrstuvwx"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(bundle)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := []string{"checks-before.json", "codex-last-message.md", "prompt.md"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("bundle contains %v, want exactly %v: the raw message must not survive redaction", names, want)
	}
}

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

	handoff, err := CreateHandoff(context.Background(), root, recordBundleDir(t, root), report, runner)
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

	handoff, err := CreateHandoff(context.Background(), root, recordBundleDir(t, root), report, runner)
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
	handoff, err := CreateHandoff(context.Background(), root, recordBundleDir(t, root), report, failingMessageRunner{})
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

// Warnings are not repair work. The refusal happens before the bundle
// directory is created, so a run with nothing to fix leaves nothing behind.
func TestCreateHandoffRefusesAReportWithNoFailedGates(t *testing.T) {
	root := fixtureRepository(t)
	bundle := recordBundleDir(t, root)
	report := &verify.Report{
		Gates:    []verify.GateResult{{ID: "lint", Status: verify.StatusPass}},
		Warnings: []string{"review-only large file warning"},
	}

	_, err := CreateHandoff(context.Background(), root, bundle, report, &recordingRunner{})
	if !errors.Is(err, ErrNoActionableFindings) {
		t.Fatalf("CreateHandoff error = %v, want ErrNoActionableFindings", err)
	}
	if _, err := os.Stat(bundle); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) = %v, want not-exist: a refused handoff must create no bundle", bundle, err)
	}
}

// The bundle directory has no default on purpose: a caller that forgets it
// must fail loudly rather than quietly resurrect the unidentified bundle.
func TestCreateHandoffRequiresABundleDirectory(t *testing.T) {
	root := fixtureRepository(t)
	report := &verify.Report{Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}}

	_, err := CreateHandoff(context.Background(), root, "   ", report, &recordingRunner{})
	if err == nil || !strings.Contains(err.Error(), "handoff bundle directory is required") {
		t.Fatalf("CreateHandoff error = %v, want a missing-bundle-directory refusal", err)
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

// recordBundleDir names a bundle the way a run record does: inside the run's
// own directory. Task 4 passes (*workrec.Handle).HandoffDir() here; this
// package deliberately does not import workrec, so the layout stays the
// record's business and the handoff's only contract is "write here".
func recordBundleDir(t *testing.T, root string) string {
	t.Helper()
	return filepath.Join(root, ".project", "state", "work", "2026-08-30-repair-a1b2", "handoff")
}
