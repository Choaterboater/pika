package loop

// The turn loop against the scripted provider: one prompt in, tool calls
// executed, a final message out, usage accumulated, and the two runaway
// guards and the retry policy holding. Every run here talks to the fake
// and nothing else.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoopRunsOneToolCallAndFinishes scripts the canonical exchange:
// turn 1 asks to read a file, turn 2 answers with text. It asserts the
// tool actually ran — the second request must carry the file's content
// as a tool_result under the assistant's tool_use turn — and that the
// final message lands where the run asked for it, with the transcript
// beside it.
func TestLoopRunsOneToolCallAndFinishes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello from the repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := newScriptedProvider(t,
		anthropicToolUse("call-1", "read_file", `{"path":"note.txt"}`, 10, 5),
		anthropicText("the note says hello", 20, 7),
	)

	_, out, err := loopRun(t, p, root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the final message was not written to the output path: %v", err)
	}
	if got := string(final); got != "the note says hello" {
		t.Errorf("final message = %q, want the response's text", got)
	}

	got := p.received()
	if len(got) != 2 {
		t.Fatalf("the provider saw %d requests, want 2", len(got))
	}
	// Turn 1 carries the prompt and the tool set under the resolved
	// default model.
	turn1 := got[0].body
	if got := turn1["model"]; got != "claude-sonnet-4-5" {
		t.Errorf("turn 1 model = %v, want the provider's default", got)
	}
	if got := turn1["system"]; got != systemPrompt {
		t.Errorf("turn 1 system = %v, want the fixed kernel prompt", got)
	}
	msgs1 := wireMessages(t, turn1)
	m0 := wireMessage(t, msgs1, 0)
	if c0, _ := m0["content"].([]any); len(c0) != 1 || c0[0].(map[string]any)["text"] != "do the thing" {
		t.Errorf("turn 1 first message = %v, want the prompt", c0)
	}
	// Turn 2 proves the tool ran and the turns were appended correctly:
	// the assistant's tool_use turn, then a user message whose
	// tool_result carries the file's content under the call's id.
	msgs2 := wireMessages(t, got[1].body)
	if len(msgs2) != 3 {
		t.Fatalf("turn 2 carried %d messages, want prompt + assistant + result = 3", len(msgs2))
	}
	assistant := wireMessage(t, msgs2, 1)
	if assistant["role"] != "assistant" {
		t.Errorf("turn 2 message 1 role = %v, want assistant", assistant["role"])
	}
	ca, _ := assistant["content"].([]any)
	if len(ca) != 1 || ca[0].(map[string]any)["type"] != "tool_use" ||
		ca[0].(map[string]any)["id"] != "call-1" || ca[0].(map[string]any)["name"] != "read_file" {
		t.Errorf("turn 2 assistant turn = %v, want the read_file tool_use", ca)
	}
	result := wireMessage(t, msgs2, 2)
	if result["role"] != "user" {
		t.Errorf("turn 2 message 2 role = %v, want user", result["role"])
	}
	cr, _ := result["content"].([]any)
	if len(cr) != 1 || cr[0].(map[string]any)["type"] != "tool_result" ||
		cr[0].(map[string]any)["tool_use_id"] != "call-1" ||
		cr[0].(map[string]any)["content"] != "hello from the repo\n" {
		t.Errorf("turn 2 tool result = %v, want the file's content under call-1", cr)
	}

	// The transcript sits beside the final message and records the whole
	// exchange plus what the run spent.
	transcript, err := os.ReadFile(filepath.Join(filepath.Dir(out), "pika-transcript.json"))
	if err != nil {
		t.Fatalf("no transcript beside the final message: %v", err)
	}
	for _, want := range []string{"do the thing", "call-1", "read_file", "hello from the repo", "the note says hello"} {
		if !strings.Contains(string(transcript), want) {
			t.Errorf("transcript does not record %q:\n%s", want, transcript)
		}
	}
}

// TestLoopAccumulatesUsage runs two calls with scripted usage and
// asserts Usage() sums calls, input and output tokens across them.
func TestLoopAccumulatesUsage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := newScriptedProvider(t,
		anthropicToolUse("call-1", "read_file", `{"path":"note.txt"}`, 100, 50),
		anthropicText("done", 200, 70),
	)

	r, _, err := loopRun(t, p, root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls, in, out := r.Usage()
	if calls != 2 || in != 300 || out != 120 {
		t.Errorf("Usage() = (%d, %d, %d), want (2, 300, 120)", calls, in, out)
	}
}

// TestLoopRefusesTheTurnLimit scripts a provider that always asks for
// another tool call. The loop must refuse at the turn limit, naming it,
// after exactly maxTurns requests — a model stuck in a tool loop is a
// defect to surface, not a budget to tune.
func TestLoopRefusesTheTurnLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := make([]scriptedResponse, 0, maxTurns+1)
	for i := 0; i < maxTurns+1; i++ {
		script = append(script, anthropicToolUse("call-1", "read_file", `{"path":"note.txt"}`, 1, 1))
	}
	p := newScriptedProvider(t, script...)

	_, _, err := loopRun(t, p, root)
	if err == nil || !strings.Contains(err.Error(), "turn limit reached (40)") {
		t.Fatalf("Run error = %v, want the turn limit named", err)
	}
	if got := len(p.received()); got != maxTurns {
		t.Errorf("the provider saw %d requests, want exactly %d", got, maxTurns)
	}
}

// TestLoopRefusesTheTokenLimit scripts one turn whose usage pushes the
// run past the token cap. The next turn must refuse before a second
// request is ever made.
func TestLoopRefusesTheTokenLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := newScriptedProvider(t,
		anthropicToolUse("call-1", "read_file", `{"path":"note.txt"}`, 300_000, 150_000),
		anthropicText("never reached", 1, 1),
	)

	_, _, err := loopRun(t, p, root)
	if err == nil || !strings.Contains(err.Error(), "token limit reached (400000)") {
		t.Fatalf("Run error = %v, want the token limit named", err)
	}
	if got := len(p.received()); got != 1 {
		t.Errorf("the provider saw %d requests, want 1: the limit refuses before the next call", got)
	}
}

// TestLoopRetriesOn429AndServerError scripts a 429, then a 500, then a
// good response. Both are retryable — a retry replays no effects, only
// the request — so the run must deliver after exactly three requests.
// The two backoff sleeps (1s, 2s) are the test's cost of proving it.
func TestLoopRetriesOn429AndServerError(t *testing.T) {
	root := t.TempDir()
	p := newScriptedProvider(t,
		scriptedResponse{status: 429, body: `{"error":{"message":"slow down"}}`},
		scriptedResponse{status: 500, body: `{"error":{"message":"boom"}}`},
		anthropicText("made it through", 10, 5),
	)

	_, out, err := loopRun(t, p, root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(p.received()); got != 3 {
		t.Errorf("the provider saw %d requests, want 3 (429, 500, 200)", got)
	}
	final, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the final message was not written: %v", err)
	}
	if got := string(final); got != "made it through" {
		t.Errorf("final message = %q, want the response after the retries", got)
	}
}

// TestLoopDoesNotRetryOn4xx scripts a 401. A 4xx is a fact to surface
// verbatim — status and redacted body — never a reason to retry, so the
// provider must see exactly one request.
func TestLoopDoesNotRetryOn4xx(t *testing.T) {
	root := t.TempDir()
	p := newScriptedProvider(t,
		scriptedResponse{status: 401, body: `{"error":{"message":"invalid api key"}}`},
	)

	_, _, err := loopRun(t, p, root)
	if err == nil {
		t.Fatal("Run succeeded against a 401")
	}
	for _, want := range []string{"pika loop: anthropic 401", "invalid api key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry %q", err, want)
		}
	}
	if got := len(p.received()); got != 1 {
		t.Errorf("the provider saw %d requests, want 1: a 4xx is never retried", got)
	}
}

// TestNewRunnerRefusesNoProvider: a loop with no provider is a loop that
// cannot pick a client, refused before any request.
func TestNewRunnerRefusesNoProvider(t *testing.T) {
	_, err := NewRunner("builder", "", "", "")
	if err == nil || !strings.Contains(err.Error(), `agent "builder" declares runtime pika with no provider`) {
		t.Fatalf("NewRunner error = %v, want the no-provider refusal", err)
	}
}

// TestNewRunnerRefusesAnUnknownProvider names the provider and the three
// the loop speaks.
func TestNewRunnerRefusesAnUnknownProvider(t *testing.T) {
	_, err := NewRunner("builder", "gemini", "", "")
	if err == nil {
		t.Fatal("NewRunner accepted an unknown provider")
	}
	for _, want := range []string{`declares provider "gemini"`, "anthropic, openai and openrouter"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry %q", err, want)
		}
	}
}

// TestNewRunnerRefusesAMissingKey: the key comes from pika's own
// environment, never the contract, so its absence is a refusal naming
// the canonical variable. The env var is set to empty rather than unset
// so the test does not depend on what the host happens to export.
func TestNewRunnerRefusesAMissingKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	_, err := NewRunner("builder", "anthropic", "", "")
	if err == nil {
		t.Fatal("NewRunner accepted a provider with no key in the environment")
	}
	for _, want := range []string{`provider "anthropic" needs`, "ANTHROPIC_API_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry %q", err, want)
		}
	}
}

// TestTranscriptIsRedacted pins the loop's one direct use of redact: a
// credential-shaped string that went to the provider raw — here the
// canned final text — must land in the persisted transcript as a
// placeholder.
func TestTranscriptIsRedacted(t *testing.T) {
	root := t.TempDir()
	const secret = "AKIAIOSFODNN7EXAMPLE"
	p := newScriptedProvider(t, anthropicText("done; token "+secret+" was used", 10, 5))

	_, out, err := loopRun(t, p, root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	transcript, err := os.ReadFile(filepath.Join(filepath.Dir(out), "pika-transcript.json"))
	if err != nil {
		t.Fatalf("no transcript beside the final message: %v", err)
	}
	if strings.Contains(string(transcript), secret) {
		t.Errorf("the transcript persisted a credential verbatim:\n%s", transcript)
	}
	if !strings.Contains(string(transcript), "<redacted:aws-key>") {
		t.Errorf("the transcript carries no redaction placeholder:\n%s", transcript)
	}

	// The transcript is the messages plus the usage, indented, and the
	// usage is the run's own counters.
	var decoded transcriptFile
	if err := json.Unmarshal(transcript, &decoded); err != nil {
		t.Fatalf("the transcript is not the transcriptFile shape: %v\n%s", err, transcript)
	}
	if decoded.Usage.Calls != 1 {
		t.Errorf("transcript usage calls = %d, want 1", decoded.Usage.Calls)
	}
}
