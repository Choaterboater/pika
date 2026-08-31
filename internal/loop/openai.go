package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// openaiClient speaks OpenAI-compatible Chat Completions: POST
// {baseURL}/chat/completions. openrouter rides the same wire shape; the
// optional OpenRouter-specific headers are deliberately not sent.
type openaiClient struct {
	name    string // provider name ("openai" | "openrouter"), for error messages
	key     string
	baseURL string
}

func (c openaiClient) complete(ctx context.Context, req request) (response, error) {
	body := openaiRequest{
		Model:    req.model,
		Messages: openaiMessages(req.system, req.messages),
		Tools:    openaiTools(req.tools),
	}
	if req.effort != "" {
		body.ReasoningEffort = req.effort
	}
	var out openaiResponse
	err := doJSON(ctx, "POST", c.baseURL+"/chat/completions", map[string]string{
		"Authorization": "Bearer " + c.key,
		"content-type":  "application/json",
	}, body, &out)
	if err != nil {
		return response{}, wrapProviderError(c.name, err)
	}
	if len(out.Choices) == 0 {
		return response{}, fmt.Errorf("pika loop: %s response carried no choices", c.name)
	}
	msg := out.Choices[0].Message
	resp := response{usage: usage{in: out.Usage.PromptTokens, out: out.Usage.CompletionTokens}}
	if msg.Content != "" {
		resp.text = []string{msg.Content}
	}
	for _, tc := range msg.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		resp.calls = append(resp.calls, toolCall{id: tc.ID, name: tc.Function.Name, input: args})
	}
	return resp, nil
}

// openaiMessages projects the neutral model onto the Chat Completions
// shape: the system prompt becomes a leading system message, an assistant
// turn carrying tool calls becomes one message whose content is its text
// parts joined (or "" when there are none), and every tool result becomes
// its own tool message following the assistant's.
func openaiMessages(system string, messages []message) []openaiMessage {
	out := []openaiMessage{{Role: "system", Content: system}}
	for _, m := range messages {
		var text []string
		var calls []openaiToolCall
		for _, p := range m.parts {
			switch {
			case p.result != nil:
				out = append(out, openaiMessage{Role: "tool", ToolCallID: p.result.id, Content: p.result.output})
			case p.call != nil:
				args := string(p.call.input)
				if args == "" {
					args = "{}"
				}
				calls = append(calls, openaiToolCall{
					ID:       p.call.id,
					Type:     "function",
					Function: openaiFunction{Name: p.call.name, Arguments: args},
				})
			default:
				text = append(text, p.text)
			}
		}
		if len(text) > 0 || len(calls) > 0 {
			out = append(out, openaiMessage{Role: m.role, Content: strings.Join(text, "\n"), ToolCalls: calls})
		}
	}
	return out
}

func openaiTools(tools []tool) []openaiTool {
	out := make([]openaiTool, 0, len(tools))
	for _, t := range tools {
		wt := openaiTool{Type: "function"}
		wt.Function.Name = t.name
		wt.Function.Description = t.description
		wt.Function.Parameters = t.schema
		out = append(out, wt)
	}
	return out
}

type openaiRequest struct {
	Model           string          `json:"model"`
	Messages        []openaiMessage `json:"messages"`
	Tools           []openaiTool    `json:"tools"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
}

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"` // "function"
	Function openaiFunction `json:"function"`
}

type openaiFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiTool struct {
	Type     string `json:"type"` // "function"
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}

type openaiResponse struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string         `json:"id"`
				Type     string         `json:"type"`
				Function openaiFunction `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
