package main

// The built-in loop: one run whose builder is not a subprocess at all.
//
// Every other step that spawns an agent puts the fake harness binary on
// PATH. This step cannot — the eighth runtime has no binary — so the
// boundary it fakes is the provider's HTTP API instead: a scripted
// Anthropic-shaped server on loopback, reached through the same
// base-URL environment override the loop's tests use, with a dummy key
// in the canonical variable. No model, no credential, no network beyond
// 127.0.0.1, which is what keeps the gate — and so `pika check --ci` —
// provably LLM-free with the loop in it.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// loopAgent names the loop as the builder. The contract names no model,
// so the run resolves the provider's default: the whole contract surface
// a loop run needs is runtime + provider.
const loopAgent = "agents:\n  builder:\n    runtime: pika\n    provider: anthropic\n"

// loopGoal is the goal the run is given.
const loopGoal = "add a NOTES.md the ladder can verify"

// The edit the scripted provider tells the builder to make: a markdown
// file the scaffold's go@1 ladder stays green through.
const (
	loopPath    = "NOTES.md"
	loopContent = "# Notes\n\nWritten by the run's loop.\n"
)

// loopProvider is the scripted Anthropic-shaped provider the step runs
// against: an httptest server that replies with the next scripted body.
type loopProvider struct {
	mu       sync.Mutex
	requests int
	script   []string
	srv      *httptest.Server
}

// newLoopProvider starts the fake provider with the given response
// bodies, answered in order.
func newLoopProvider(script ...string) *loopProvider {
	p := &loopProvider{script: script}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.requests++
		n := p.requests
		p.mu.Unlock()
		if n > len(p.script) {
			http.Error(w, "script exhausted", http.StatusInternalServerError)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, p.script[n-1])
	}))
	return p
}

// env is the two variables that point a pika subprocess at the fake: the
// canonical key var with a dummy value, and the base-URL override.
func (p *loopProvider) env() []string {
	return []string{
		"ANTHROPIC_API_KEY=dummy-test-key",
		"ANTHROPIC_BASE_URL=" + p.srv.URL,
	}
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

func stepLoop(h *harness) error {
	c := &check{}
	dir, _, err := h.scaffold("loop")
	if err != nil {
		return err
	}
	doc, err := readRepo(dir, ".project/contract.yaml")
	if err != nil {
		return err
	}
	if !strings.Contains(doc, scaffoldedAgents) {
		c.failf("`pika init` no longer scaffolds %q, so this step cannot name its builder:\n%s",
			scaffoldedAgents, doc)
		return c.err()
	}
	if err := writeRepo(dir, ".project/contract.yaml", strings.Replace(doc, scaffoldedAgents, loopAgent, 1)); err != nil {
		return err
	}
	if _, err := initGit(dir); err != nil {
		return err
	}

	// Turn 1 asks for the edit, turn 2 answers with final text: the
	// whole exchange a delivered run needs.
	provider := newLoopProvider(
		anthropicToolUseBody("call-1", "write_file",
			`{"path":"`+loopPath+`","content":"# Notes\n\nWritten by the run's loop.\n"}`),
		anthropicTextBody("Added NOTES.md as asked."),
	)
	defer provider.srv.Close()

	r, err := h.run(dir, provider.env(), "work", loopGoal, "--json")
	if err != nil {
		return err
	}
	wantEqual(c, "`pika work` exit code on a loop-builder run", r.exit, 0)

	var workID, commit string
	{
		env, ok := decodeEnvelope(c, r, "work")
		if !ok {
			return c.err()
		}
		var res struct {
			WorkID string `json:"workId"`
			Commit string `json:"commit"`
		}
		if !decodeResult(c, env, r, "work", &res) {
			return c.err()
		}
		c.truef(res.Commit != "", "`pika work` delivered no commit with the loop as its builder\n%s", r)
		workID, commit = res.WorkID, res.Commit
	}
	if workID == "" {
		c.failf("`pika work` reported no work id, so there is no record to read\n%s", r)
		return c.err()
	}

	// The edit is in the commit pika says it made — not merely at the
	// branch's tip: the loop's write_file ran inside the pika process
	// and the ladder verified the result.
	if commit != "" {
		head, err := git(dir, "rev-parse", improveBranch)
		if err != nil {
			return err
		}
		wantEqual(c, "the head of "+improveBranch+" against the commit `pika work` reported", head, commit)
		got, err := git(dir, "show", commit+":"+loopPath)
		if err != nil {
			return err
		}
		wantEqual(c, "the content of "+loopPath+" in the delivered commit",
			strings.TrimRight(got, "\n"), strings.TrimRight(loopContent, "\n"))
	}

	// The record names the loop: the builder ran on runtime pika, and it
	// reported what the run spent, which no subprocess runtime can.
	raw, err := readRepo(dir, ".project/state/work/"+workID+"/record.json")
	if err != nil {
		c.failf("the run left no durable record: %v\n%s", err, r)
		return c.err()
	}
	var rec runRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		c.failf("the run's record is not JSON: %v\n%s", err, quoteBlock("record", raw))
		return c.err()
	}
	if len(rec.Agents) != 1 {
		c.failf("the run recorded %d agents, want the builder alone\n%s", len(rec.Agents), quoteBlock("record", raw))
		return c.err()
	}
	wantEqual(c, "the runtime the run's record names for its builder", rec.Agents[0].Runtime, "pika")
	c.truef(rec.Agents[0].Calls > 0,
		"the run's record reports no usage for the loop builder, which is the one runtime that can know it\n%s",
		quoteBlock("record", raw))
	return c.err()
}
