package e2e

// The built-in loop end to end: `pika work` whose builder is the eighth
// runtime, running in the pika process itself against a scripted
// Anthropic-shaped provider on loopback.
//
// FAKE_AGENT_* is not involved anywhere here — the loop is not a
// fixture, it is production code running in the binary under test. The
// boundary that needs faking is the provider's HTTP API, and that is an
// httptest server the test owns, reached through ANTHROPIC_BASE_URL with
// a dummy key in ANTHROPIC_API_KEY. It is the only provider contact any
// test makes, which is what keeps `pika check --ci` provably LLM-free
// with a loop in the tree: no model, no credential, no network beyond
// 127.0.0.1.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// loopBuilderAgent is the contract entry a loop run resolves: the eighth
// runtime, with no binary, provider anthropic. The contract does not
// name a model, so the run resolves the provider's default — the
// contract surface a loop run needs is exactly runtime + provider.
const loopBuilderAgent = "agents:\n  builder:\n    runtime: pika\n    provider: anthropic\n"

// loopProvider is the scripted Anthropic-shaped provider the loop tests
// run against: an httptest server that records every request and replies
// with the next scripted body.
type loopProvider struct {
	t        *testing.T
	mu       sync.Mutex
	requests []map[string]any
	script   []string
	srv      *httptest.Server
}

// newLoopProvider starts the fake provider with the given response
// bodies, answered in order, and stops it at test cleanup.
func newLoopProvider(t *testing.T, script ...string) *loopProvider {
	t.Helper()
	p := &loopProvider{t: t, script: script}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(data, &body); err != nil {
			http.Error(w, "request body is not JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		p.requests = append(p.requests, body)
		n := len(p.requests)
		p.mu.Unlock()
		if n > len(p.script) {
			p.t.Errorf("request %d arrived after the %d-response script ran out", n, len(p.script))
			http.Error(w, "script exhausted", http.StatusInternalServerError)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, p.script[n-1])
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// env is the two variables that point a pika subprocess at the fake:
// the canonical key var with a dummy value, and the base-URL override.
func (p *loopProvider) env() []string {
	return []string{
		"ANTHROPIC_API_KEY=dummy-test-key",
		"ANTHROPIC_BASE_URL=" + p.srv.URL,
	}
}

// received returns the request bodies the provider has seen, in order.
func (p *loopProvider) received() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.requests...)
}

// anthropicToolUseBody scripts a turn asking for one tool call.
func anthropicToolUseBody(id, name, input string) string {
	return `{"content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}],` +
		`"stop_reason":"tool_use","usage":{"input_tokens":120,"output_tokens":40}}`
}

// anthropicTextBody scripts a final turn: text, no tool call.
func anthropicTextBody(text string) string {
	q, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	return `{"content":[{"type":"text","text":` + string(q) + `}],` +
		`"stop_reason":"end_turn","usage":{"input_tokens":300,"output_tokens":60}}`
}

// loopRepo scaffolds the same repository workRepo does, with the loop as
// the builder instead of a harness binary.
func loopRepo(t *testing.T) string {
	t.Helper()
	dir := scaffoldRepo(t, "go")
	const scaffolded = "agents: {}\n"
	path := filepath.Join(dir, ".project", "contract.yaml")
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bs), scaffolded) {
		t.Fatalf("%s no longer scaffolds %q:\n%s", path, scaffolded, bs)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(bs), scaffolded, loopBuilderAgent, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, dir)
	return dir
}

// loopEditPath and loopEditContent are the edit the scripted provider
// tells the builder to make. As with the harness fixture, a markdown
// file keeps the scaffold's go@1 ladder green through the recheck.
const (
	loopEditPath    = "NOTES.md"
	loopEditContent = "# Notes\n\nWritten by the run's loop.\n"
)

// loopGoal is the goal every loop run here is given.
const loopGoal = "record a NOTES.md the ladder can verify"

// loopRecord mirrors the durable run record in the fields the loop tests
// read: the agents the run spawned, with the usage the loop reports.
type loopRecord struct {
	Agents []struct {
		Role      string `json:"role"`
		Agent     string `json:"agent"`
		Runtime   string `json:"runtime"`
		Calls     int    `json:"calls"`
		TokensIn  int    `json:"tokens_in"`
		TokensOut int    `json:"tokens_out"`
	} `json:"agents"`
}

// readLoopRecord loads the run's record.json from .project/state/.
func readLoopRecord(t *testing.T, dir, workID string) loopRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".project", "state", "work", workID, "record.json"))
	if err != nil {
		t.Fatalf("the run left no durable record: %v", err)
	}
	var rec loopRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("the run's record is not JSON: %v\n%s", err, raw)
	}
	return rec
}

// readLoopTranscript loads the bundle's pika-transcript.json and decodes
// the parts the tests assert on: every message's calls and results.
func readLoopTranscript(t *testing.T, dir, workID string) (string, loopTranscript) {
	t.Helper()
	path := filepath.Join(dir, ".project", "state", "work", workID, "handoff", "pika-transcript.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the handoff bundle holds no pika-transcript.json: %v", err)
	}
	var tr loopTranscript
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatalf("the transcript is not JSON: %v\n%s", err, raw)
	}
	return string(raw), tr
}

// loopTranscript is the transcript's shape as an outside reader sees it.
type loopTranscript struct {
	Messages []struct {
		Role  string `json:"role"`
		Parts []struct {
			Text string `json:"text"`
			Call *struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"call"`
			Result *struct {
				ID      string `json:"id"`
				Output  string `json:"output"`
				IsError bool   `json:"is_error"`
			} `json:"result"`
		} `json:"parts"`
	} `json:"messages"`
	Usage struct {
		Calls int `json:"calls"`
		In    int `json:"in"`
		Out   int `json:"out"`
	} `json:"usage"`
}

// resultFor finds the recorded tool result for one call id.
func (tr loopTranscript) resultFor(id string) (output string, isError, found bool) {
	for _, m := range tr.Messages {
		for _, p := range m.Parts {
			if p.Result != nil && p.Result.ID == id {
				return p.Result.Output, p.Result.IsError, true
			}
		}
	}
	return "", false, false
}

// TestWorkWithALoopBuilder runs the whole feature lifecycle with the
// built-in loop as the builder: the scripted provider asks for the edit
// on turn 1 and answers with final text on turn 2, and everything the
// lifecycle attests — the verified commit, the durable record, the
// receipt — must say the loop did it.
func TestWorkWithALoopBuilder(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	dir := loopRepo(t)
	p := newLoopProvider(t,
		anthropicToolUseBody("call-1", "write_file",
			`{"path":"`+loopEditPath+`","content":"# Notes\n\nWritten by the run's loop.\n"}`),
		anthropicTextBody("Added NOTES.md as asked."),
	)

	out := runCLIEnv(t, dir, p.env(), 0, "work", loopGoal, "--json")
	env := unwrap(t, out, "work")
	if !env.OK {
		t.Fatalf("work reported not ok:\n%s", out)
	}
	var result workResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("parse work result: %v\n%s", err, out)
	}
	if result.Commit == "" {
		t.Fatal("work reported no commit")
	}

	// The edit is in the delivered commit.
	if got := git(t, dir, "show", result.Commit+":"+loopEditPath); got != strings.TrimSpace(loopEditContent) {
		t.Errorf("the committed %s = %q, want the loop's edit", loopEditPath, got)
	}

	// The provider saw exactly the two scripted turns, and the second
	// carries the write_file tool result — proof the tool ran inside the
	// pika process, not in some fixture.
	got := p.received()
	if len(got) != 2 {
		t.Fatalf("the provider saw %d requests, want 2", len(got))
	}
	turn2, err := json.Marshal(got[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(turn2), `"tool_result"`) || !strings.Contains(string(turn2), "wrote "+loopEditPath) {
		t.Errorf("turn 2 does not carry the write_file result:\n%s", turn2)
	}

	// The bundle holds the transcript beside the usual files.
	raw, tr := readLoopTranscript(t, dir, result.WorkID)
	if tr.Usage.Calls != 2 {
		t.Errorf("transcript usage calls = %d, want 2:\n%s", tr.Usage.Calls, raw)
	}

	// The record names the loop and what it spent.
	rec := readLoopRecord(t, dir, result.WorkID)
	if len(rec.Agents) != 1 {
		t.Fatalf("the run recorded %d agents, want the builder alone", len(rec.Agents))
	}
	builder := rec.Agents[0]
	if builder.Role != "builder" || builder.Runtime != "pika" {
		t.Errorf("record agent = %s/%s, want builder on runtime pika", builder.Role, builder.Runtime)
	}
	if builder.Calls == 0 || builder.TokensIn == 0 || builder.TokensOut == 0 {
		t.Errorf("record usage = calls %d, in %d, out %d; the loop must report what the run spent",
			builder.Calls, builder.TokensIn, builder.TokensOut)
	}

	// The receipt's roles name the runtime and the provider.
	receipt := readReceipt(t, dir, result.WorkID)
	if len(receipt.Roles) != 1 || receipt.Roles[0].Role != "builder" ||
		receipt.Roles[0].Runtime != "pika" || receipt.Roles[0].Provider != "anthropic" {
		t.Errorf("receipt roles = %+v, want the loop builder with provider anthropic", receipt.Roles)
	}
}

// TestLoopBuilderRunsACommand proves the unrestricted-exec posture end
// to end: the scripted provider's first turn is a run_command, and the
// transcript must record the tool result — the marker that the command
// ran, since a command's argv cannot write one. The canned final text
// carries a credential-shaped string so the transcript's redaction is
// asserted on the same artifact.
func TestLoopBuilderRunsACommand(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	const secret = "AKIAIOSFODNN7EXAMPLE"
	dir := loopRepo(t)
	p := newLoopProvider(t,
		anthropicToolUseBody("call-1", "run_command", `{"command":"go test ./..."}`),
		anthropicToolUseBody("call-2", "write_file",
			`{"path":"`+loopEditPath+`","content":"# Notes\n\nWritten by the run's loop.\n"}`),
		anthropicTextBody("Ran the suite; token "+secret+" must never be persisted."),
	)

	out := runCLIEnv(t, dir, p.env(), 0, "work", loopGoal, "--json")
	env := unwrap(t, out, "work")
	if !env.OK {
		t.Fatalf("work reported not ok:\n%s", out)
	}
	var result workResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("parse work result: %v\n%s", err, out)
	}
	if result.Commit == "" {
		t.Fatal("work reported no commit")
	}

	// The transcript records the run_command call and its result: the
	// command ran, its output came back, and a failing command would
	// have surfaced here as the ladder's verdict does everywhere.
	raw, tr := readLoopTranscript(t, dir, result.WorkID)
	output, isError, found := tr.resultFor("call-1")
	if !found {
		t.Fatalf("the transcript records no result for the run_command call:\n%s", raw)
	}
	if isError {
		t.Errorf("the run_command result is an error: %q", output)
	}
	if !strings.Contains(output, "test") {
		t.Errorf("the run_command result does not look like `go test ./...` output: %q", output)
	}

	// The transcript is the one artifact the loop persists itself, so it
	// is the artifact that must be redacted.
	if strings.Contains(raw, secret) {
		t.Errorf("the transcript persisted a credential verbatim:\n%s", raw)
	}
	if !strings.Contains(raw, "<redacted:aws-key>") {
		t.Errorf("the transcript carries no redaction placeholder:\n%s", raw)
	}
}
