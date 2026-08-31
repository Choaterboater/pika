package loop

import (
	"context"
	"encoding/json"
)

// anthropicClient speaks Anthropic Messages: POST {baseURL}/v1/messages.
type anthropicClient struct {
	name    string // provider name, for error messages
	key     string
	baseURL string
}

// thinkingBudgets maps the contract's effort levels to Anthropic's
// extended-thinking budgets. thinking is sent only when effort is set,
// and max_tokens must exceed its budget — hence the two max_tokens
// values.
var thinkingBudgets = map[string]int{"low": 1024, "medium": 4096, "high": 16384}

const (
	anthropicMaxTokens         = 16384
	anthropicMaxTokensThinking = 32768
)

func (c anthropicClient) complete(ctx context.Context, req request) (response, error) {
	body := anthropicRequest{
		Model:     req.model,
		MaxTokens: anthropicMaxTokens,
		System:    req.system,
		Messages:  anthropicMessages(req.messages),
		Tools:     anthropicTools(req.tools),
	}
	if req.effort != "" {
		if budget, ok := thinkingBudgets[req.effort]; ok {
			body.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: budget}
			body.MaxTokens = anthropicMaxTokensThinking
		}
	}
	var out anthropicResponse
	err := doJSON(ctx, "POST", c.baseURL+"/v1/messages", map[string]string{
		"x-api-key":         c.key,
		"anthropic-version": "2023-06-01",
		"content-type":      "application/json",
	}, body, &out)
	if err != nil {
		return response{}, wrapProviderError(c.name, err)
	}
	resp := response{usage: usage{in: out.Usage.InputTokens, out: out.Usage.OutputTokens}}
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			resp.text = append(resp.text, b.Text)
		case "tool_use":
			input := b.Input
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			resp.calls = append(resp.calls, toolCall{id: b.ID, name: b.Name, input: input})
		}
	}
	return resp, nil
}

// anthropicMessages projects the neutral model onto the Messages shape:
// roles carry over, and every part becomes one content block — text,
// tool_use, or tool_result.
func anthropicMessages(messages []message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(messages))
	for _, m := range messages {
		am := anthropicMessage{Role: m.role}
		for _, p := range m.parts {
			switch {
			case p.call != nil:
				input := p.call.input
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				am.Content = append(am.Content, anthropicPart{
					Type:  "tool_use",
					ID:    p.call.id,
					Name:  p.call.name,
					Input: input,
				})
			case p.result != nil:
				am.Content = append(am.Content, anthropicPart{
					Type:      "tool_result",
					ToolUseID: p.result.id,
					Content:   p.result.output,
					IsError:   p.result.isError,
				})
			default:
				am.Content = append(am.Content, anthropicPart{Type: "text", Text: p.text})
			}
		}
		out = append(out, am)
	}
	return out
}

func anthropicTools(tools []tool) []anthropicTool {
	out := make([]anthropicTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, anthropicTool{Name: t.name, Description: t.description, InputSchema: t.schema})
	}
	return out
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools"`
	Thinking  *anthropicThinking `json:"thinking,omitempty"`
}

type anthropicThinking struct {
	Type         string `json:"type"` // "enabled"
	BudgetTokens int    `json:"budget_tokens"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content []anthropicPart `json:"content"`
}

// anthropicPart is one content block. Text, tool_use and tool_result
// share the wire, so the fields union all three; omitempty keeps each
// block to what its type needs.
type anthropicPart struct {
	Type      string          `json:"type"` // "text" | "tool_use" | "tool_result"
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type anthropicResponse struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}
