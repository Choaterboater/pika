package loop

// The fake provider every test in this package talks to, and the helpers
// that drive a Runner against it. It is the only provider contact any
// test makes: an httptest server on loopback, reached through the
// provider's base-URL environment override, with a dummy key in the
// canonical env var. No test in this package — and so no test in the
// tree — speaks to a live provider, needs a credential, or leaves
// 127.0.0.1, which is what keeps `go test ./...` and `pika check --ci`
// provably LLM-free with a loop in the tree.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// capturedRequest is one request the fake provider received, with the
// body decoded so a test can assert the wire shape the client emitted.
type capturedRequest struct {
	method string
	path   string
	header http.Header
	body   map[string]any
}

// scriptedResponse is one reply in the script: the status to send, the
// raw body to answer with, and any headers to set (Retry-After is the
// one the retry policy reads).
type scriptedResponse struct {
	status  int
	body    string
	headers map[string]string
}

// scriptedProvider is the fake provider: an httptest server that records
// every request and replies with the next scripted response.
type scriptedProvider struct {
	t        *testing.T
	mu       sync.Mutex
	requests []capturedRequest
	script   []scriptedResponse
	srv      *httptest.Server
}

// newScriptedProvider starts the fake provider with the given script and
// stops it at test cleanup.
func newScriptedProvider(t *testing.T, script ...scriptedResponse) *scriptedProvider {
	t.Helper()
	p := &scriptedProvider{t: t, script: script}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	return p
}

// serve records the request and answers with the next scripted response.
// A request past the end of the script is a test bug and is reported as
// one, with a 500 the client will surface.
func (p *scriptedProvider) serve(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var body map[string]any
	if len(data) > 0 {
		if err := json.Unmarshal(data, &body); err != nil {
			http.Error(w, "request body is not JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	p.mu.Lock()
	p.requests = append(p.requests, capturedRequest{
		method: r.Method,
		path:   r.URL.Path,
		header: r.Header.Clone(),
		body:   body,
	})
	n := len(p.requests)
	p.mu.Unlock()
	if n > len(p.script) {
		p.t.Errorf("request %d arrived after the %d-response script ran out", n, len(p.script))
		http.Error(w, "script exhausted", http.StatusInternalServerError)
		return
	}
	res := p.script[n-1]
	for k, v := range res.headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(res.status)
	_, _ = io.WriteString(w, res.body)
}

// received returns the requests the provider has seen so far, in order.
func (p *scriptedProvider) received() []capturedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]capturedRequest(nil), p.requests...)
}

// anthropicText scripts a final Anthropic response: text, no tool call,
// so the loop ends on the turn that receives it.
func anthropicText(text string, in, out int) scriptedResponse {
	body, err := json.Marshal(map[string]any{
		"content":     []map[string]any{{"type": "text", "text": text}},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": in, "output_tokens": out},
	})
	if err != nil {
		panic(err)
	}
	return scriptedResponse{status: http.StatusOK, body: string(body)}
}

// anthropicToolUse scripts an Anthropic response asking for one tool
// call, so the loop executes it and comes back for another turn.
func anthropicToolUse(id, name, input string, in, out int) scriptedResponse {
	body, err := json.Marshal(map[string]any{
		"content": []map[string]any{{
			"type":  "tool_use",
			"id":    id,
			"name":  name,
			"input": json.RawMessage(input),
		}},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": in, "output_tokens": out},
	})
	if err != nil {
		panic(err)
	}
	return scriptedResponse{status: http.StatusOK, body: string(body)}
}

// loopRun drives one Runner against a scripted Anthropic provider: the
// dummy key in the canonical env var, the base-URL override pointed at
// the fake, and a prompt in a side directory outside the repository.
// The key env var is set before NewRunner because that is when the key
// is read; the base-URL override would take effect set any time before
// Run, which is the seam's whole design. It returns the runner (for
// Usage), the path the final message was written to, and Run's error.
func loopRun(t *testing.T, p *scriptedProvider, root string) (*Runner, string, error) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "dummy-test-key")
	t.Setenv("ANTHROPIC_BASE_URL", p.srv.URL)
	r, err := NewRunner("builder", "anthropic", "", "")
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	side := t.TempDir()
	prompt := filepath.Join(side, "prompt.md")
	if err := os.WriteFile(prompt, []byte("do the thing"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(side, "pika-last-message.md")
	runErr := r.Run(context.Background(), root, prompt, out)
	return r, out, runErr
}
