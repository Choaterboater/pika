package loop

// The wire shapes. These tests pin what each client actually puts on the
// wire — headers, model, system placement, the message mapping for text,
// tool call and tool result, the tool definitions, and the reasoning
// control that is present only when the contract sets an effort — and
// what it reads back. The shapes are written from the providers'
// documented formats; a scripted provider that answers with the
// documented response shape is the closest the suite can get to a real
// provider without spending a key.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

// conversation is the neutral three-turn exchange both shape tests
// project: a user text, an assistant turn with text and a tool call, and
// the tool result coming back.
func conversation() []message {
	return []message{
		{role: "user", parts: []part{{text: "hello"}}},
		{role: "assistant", parts: []part{
			{text: "looking"},
			{call: &toolCall{id: "c1", name: "read_file", input: json.RawMessage(`{"path":"a.go"}`)}},
		}},
		{role: "user", parts: []part{{result: &toolResult{id: "c1", output: "package a"}}}},
	}
}

// wantHeader asserts one request header.
func wantHeader(t *testing.T, req capturedRequest, key, want string) {
	t.Helper()
	if got := req.header.Get(key); got != want {
		t.Errorf("header %s = %q, want %q", key, got, want)
	}
}

// wireMessages unwraps a request body's messages array.
func wireMessages(t *testing.T, body map[string]any) []any {
	t.Helper()
	msgs, ok := body["messages"].([]any)
	if !ok {
		t.Fatalf("request body carries no messages array: %v", body)
	}
	return msgs
}

// wireMessage is one entry of a messages array, as a map.
func wireMessage(t *testing.T, msgs []any, i int) map[string]any {
	t.Helper()
	if i >= len(msgs) {
		t.Fatalf("request carried %d messages, want at least %d", len(msgs), i+1)
	}
	m, ok := msgs[i].(map[string]any)
	if !ok {
		t.Fatalf("message %d is %T, want an object", i, msgs[i])
	}
	return m
}

// wantToolNames asserts the request's tool definitions, in order.
func wantToolNames(t *testing.T, tools any, schemaKey string) {
	t.Helper()
	list, ok := tools.([]any)
	if !ok {
		t.Fatalf("request body carries no tools array: %v", tools)
	}
	var names []string
	for _, entry := range list {
		tool, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("tool entry is %T, want an object", entry)
		}
		name, _ := tool["name"].(string)
		if fn, ok := tool["function"].(map[string]any); ok {
			// OpenAI nests the name and the schema under "function".
			name, _ = fn["name"].(string)
			if _, ok := fn[schemaKey]; !ok {
				t.Errorf("tool %q carries no %s", name, schemaKey)
			}
		} else if _, ok := tool[schemaKey]; !ok {
			t.Errorf("tool %q carries no %s", name, schemaKey)
		}
		names = append(names, name)
	}
	if want := []string{"read_file", "write_file", "run_command"}; !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

func TestAnthropicRequestShape(t *testing.T) {
	// The documented response carries one text block, one tool_use block
	// and a usage record; the client must read all three back.
	const reply = `{"content":[{"type":"text","text":"hi"},` +
		`{"type":"tool_use","id":"t1","name":"write_file","input":{"path":"x"}}],` +
		`"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`

	complete := func(t *testing.T, effort string) (capturedRequest, response) {
		p := newScriptedProvider(t, scriptedResponse{status: http.StatusOK, body: reply})
		cl := anthropicClient{name: "anthropic", key: "test-key", baseURL: p.srv.URL}
		resp, err := cl.complete(context.Background(), request{
			system:   "SYS",
			messages: conversation(),
			tools:    toolSet(),
			model:    "claude-test",
			effort:   effort,
		})
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		got := p.received()
		if len(got) != 1 {
			t.Fatalf("the provider saw %d requests, want 1", len(got))
		}
		return got[0], resp
	}

	t.Run("with effort", func(t *testing.T) {
		req, resp := complete(t, "high")
		if req.method != http.MethodPost || req.path != "/v1/messages" {
			t.Errorf("request = %s %s, want POST /v1/messages", req.method, req.path)
		}
		wantHeader(t, req, "x-api-key", "test-key")
		wantHeader(t, req, "anthropic-version", "2023-06-01")
		wantHeader(t, req, "content-type", "application/json")

		body := req.body
		if got := body["model"]; got != "claude-test" {
			t.Errorf("model = %v, want claude-test", got)
		}
		if got := body["system"]; got != "SYS" {
			t.Errorf("system = %v, want SYS", got)
		}
		// Thinking raises max_tokens, which must exceed its budget.
		if got := body["max_tokens"]; got != float64(anthropicMaxTokensThinking) {
			t.Errorf("max_tokens = %v, want %d", got, anthropicMaxTokensThinking)
		}
		thinking, ok := body["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("thinking = %v, want an object when effort is set", body["thinking"])
		}
		if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(thinkingBudgets["high"]) {
			t.Errorf("thinking = %v, want enabled with the high budget %d", thinking, thinkingBudgets["high"])
		}

		msgs := wireMessages(t, body)
		if len(msgs) != 3 {
			t.Fatalf("messages = %d, want 3", len(msgs))
		}
		// User text stays a text block under its own role.
		m0 := wireMessage(t, msgs, 0)
		if m0["role"] != "user" {
			t.Errorf("message 0 role = %v, want user", m0["role"])
		}
		c0, _ := m0["content"].([]any)
		if len(c0) != 1 || c0[0].(map[string]any)["type"] != "text" || c0[0].(map[string]any)["text"] != "hello" {
			t.Errorf("message 0 content = %v, want one text block", c0)
		}
		// The assistant turn is its text block then its tool_use block,
		// with the input decoded back to an object.
		m1 := wireMessage(t, msgs, 1)
		if m1["role"] != "assistant" {
			t.Errorf("message 1 role = %v, want assistant", m1["role"])
		}
		c1, _ := m1["content"].([]any)
		if len(c1) != 2 {
			t.Fatalf("message 1 content = %v, want text then tool_use", c1)
		}
		if c1[0].(map[string]any)["type"] != "text" || c1[0].(map[string]any)["text"] != "looking" {
			t.Errorf("message 1 text block = %v", c1[0])
		}
		use := c1[1].(map[string]any)
		if use["type"] != "tool_use" || use["id"] != "c1" || use["name"] != "read_file" {
			t.Errorf("message 1 tool_use block = %v", use)
		}
		input, ok := use["input"].(map[string]any)
		if !ok || input["path"] != "a.go" {
			t.Errorf("tool_use input = %v, want the decoded object", use["input"])
		}
		// A tool result is a user message with a tool_result block; a
		// non-error result omits is_error.
		m2 := wireMessage(t, msgs, 2)
		if m2["role"] != "user" {
			t.Errorf("message 2 role = %v, want user", m2["role"])
		}
		c2, _ := m2["content"].([]any)
		if len(c2) != 1 {
			t.Fatalf("message 2 content = %v, want one tool_result block", c2)
		}
		result := c2[0].(map[string]any)
		if result["type"] != "tool_result" || result["tool_use_id"] != "c1" || result["content"] != "package a" {
			t.Errorf("message 2 tool_result block = %v", result)
		}
		if _, present := result["is_error"]; present {
			t.Errorf("a non-error tool_result carries is_error: %v", result)
		}

		wantToolNames(t, body["tools"], "input_schema")

		// And the response mapped back: text, the call, and usage.
		if !slices.Equal(resp.text, []string{"hi"}) {
			t.Errorf("response text = %v, want [hi]", resp.text)
		}
		if len(resp.calls) != 1 || resp.calls[0].id != "t1" || resp.calls[0].name != "write_file" {
			t.Errorf("response calls = %+v, want the scripted write_file", resp.calls)
		}
		if string(resp.calls[0].input) != `{"path":"x"}` {
			t.Errorf("call input = %s, want the re-encoded object", resp.calls[0].input)
		}
		if resp.usage.in != 10 || resp.usage.out != 5 {
			t.Errorf("usage = %+v, want in 10 out 5", resp.usage)
		}
	})

	t.Run("without effort", func(t *testing.T) {
		req, _ := complete(t, "")
		body := req.body
		if _, present := body["thinking"]; present {
			t.Errorf("thinking is present with no effort set: %v", body["thinking"])
		}
		if got := body["max_tokens"]; got != float64(anthropicMaxTokens) {
			t.Errorf("max_tokens = %v, want %d", got, anthropicMaxTokens)
		}
	})

	t.Run("error tool result", func(t *testing.T) {
		// The complement of the non-error case above: a failed tool
		// puts is_error on the wire, true, so the provider sees the
		// failure rather than an ordinary result.
		p := newScriptedProvider(t, scriptedResponse{status: http.StatusOK, body: reply})
		cl := anthropicClient{name: "anthropic", key: "test-key", baseURL: p.srv.URL}
		_, err := cl.complete(context.Background(), request{
			system: "SYS",
			messages: []message{
				{role: "user", parts: []part{{result: &toolResult{id: "c1", output: "no such file", isError: true}}}},
			},
			tools: toolSet(),
			model: "claude-test",
		})
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		got := p.received()
		if len(got) != 1 {
			t.Fatalf("the provider saw %d requests, want 1", len(got))
		}
		msgs := wireMessages(t, got[0].body)
		m0 := wireMessage(t, msgs, 0)
		c0, _ := m0["content"].([]any)
		if len(c0) != 1 {
			t.Fatalf("message 0 content = %v, want one tool_result block", c0)
		}
		result := c0[0].(map[string]any)
		if result["type"] != "tool_result" || result["tool_use_id"] != "c1" || result["content"] != "no such file" {
			t.Errorf("error tool_result block = %v", result)
		}
		if result["is_error"] != true {
			t.Errorf("an error tool_result carries is_error = %v, want true", result["is_error"])
		}
	})
}

func TestOpenAIRequestShape(t *testing.T) {
	// The documented response carries a message with content and one
	// tool call, plus a usage record.
	const reply = `{"choices":[{"message":{"content":"answer","tool_calls":[` +
		`{"id":"t9","type":"function","function":{"name":"run_command","arguments":"{\"command\":\"ls\"}"}}` +
		`]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`

	complete := func(t *testing.T, effort string) (capturedRequest, response) {
		p := newScriptedProvider(t, scriptedResponse{status: http.StatusOK, body: reply})
		cl := openaiClient{name: "openai", key: "test-key", baseURL: p.srv.URL}
		resp, err := cl.complete(context.Background(), request{
			system:   "SYS",
			messages: conversation(),
			tools:    toolSet(),
			model:    "gpt-test",
			effort:   effort,
		})
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		got := p.received()
		if len(got) != 1 {
			t.Fatalf("the provider saw %d requests, want 1", len(got))
		}
		return got[0], resp
	}

	t.Run("with effort", func(t *testing.T) {
		req, resp := complete(t, "medium")
		if req.method != http.MethodPost || req.path != "/chat/completions" {
			t.Errorf("request = %s %s, want POST /chat/completions", req.method, req.path)
		}
		wantHeader(t, req, "Authorization", "Bearer test-key")
		wantHeader(t, req, "content-type", "application/json")

		body := req.body
		if got := body["model"]; got != "gpt-test" {
			t.Errorf("model = %v, want gpt-test", got)
		}
		if got := body["reasoning_effort"]; got != "medium" {
			t.Errorf("reasoning_effort = %v, want medium verbatim", got)
		}

		msgs := wireMessages(t, body)
		if len(msgs) != 4 {
			t.Fatalf("messages = %d, want 4: system, user, assistant, tool", len(msgs))
		}
		// The system prompt leads as its own message.
		m0 := wireMessage(t, msgs, 0)
		if m0["role"] != "system" || m0["content"] != "SYS" {
			t.Errorf("message 0 = %v, want the leading system message", m0)
		}
		m1 := wireMessage(t, msgs, 1)
		if m1["role"] != "user" || m1["content"] != "hello" {
			t.Errorf("message 1 = %v, want the user text", m1)
		}
		// The assistant turn is one message: joined text as content, the
		// call as a function tool_call with arguments as a raw string.
		m2 := wireMessage(t, msgs, 2)
		if m2["role"] != "assistant" || m2["content"] != "looking" {
			t.Errorf("message 2 = %v, want the assistant turn", m2)
		}
		calls, ok := m2["tool_calls"].([]any)
		if !ok || len(calls) != 1 {
			t.Fatalf("message 2 tool_calls = %v, want one call", m2["tool_calls"])
		}
		call := calls[0].(map[string]any)
		fn, _ := call["function"].(map[string]any)
		if call["id"] != "c1" || call["type"] != "function" ||
			fn["name"] != "read_file" || fn["arguments"] != `{"path":"a.go"}` {
			t.Errorf("tool_call = %v, want the function call with raw arguments", call)
		}
		// The tool result is its own tool message, following the
		// assistant's.
		m3 := wireMessage(t, msgs, 3)
		if m3["role"] != "tool" || m3["tool_call_id"] != "c1" || m3["content"] != "package a" {
			t.Errorf("message 3 = %v, want the tool result message", m3)
		}

		wantToolNames(t, body["tools"], "parameters")

		if !slices.Equal(resp.text, []string{"answer"}) {
			t.Errorf("response text = %v, want [answer]", resp.text)
		}
		if len(resp.calls) != 1 || resp.calls[0].id != "t9" || resp.calls[0].name != "run_command" {
			t.Errorf("response calls = %+v, want the scripted run_command", resp.calls)
		}
		if string(resp.calls[0].input) != `{"command":"ls"}` {
			t.Errorf("call input = %s, want the decoded arguments", resp.calls[0].input)
		}
		if resp.usage.in != 7 || resp.usage.out != 3 {
			t.Errorf("usage = %+v, want in 7 out 3", resp.usage)
		}
	})

	t.Run("without effort", func(t *testing.T) {
		req, _ := complete(t, "")
		if _, present := req.body["reasoning_effort"]; present {
			t.Errorf("reasoning_effort is present with no effort set: %v", req.body["reasoning_effort"])
		}
	})
}
