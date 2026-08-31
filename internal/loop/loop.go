// Package loop is the built-in coding-agent loop: the eighth runtime, and
// the only one that does not spawn a process.
//
// It speaks to Anthropic and OpenAI-compatible providers over stdlib
// net/http, with no SDK and no new dependency. Everything a harness binary
// does by subprocess, this does in-process: read the prompt, work the
// repository with tools, and leave a final message where the run asked for
// it. The guarantees the run holds over it are identical to the ones it
// holds over a harness: the Git-state equality check, the read-only rules
// for explorer and reviewer, and the recheck ladder — none of them knows
// or cares which runtime produced the change.
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Choaterboater/pika/internal/redact"
)

// Runaway guards. Both are constants, not policy: a model stuck in a tool
// loop or burning context is a defect to surface, not a budget to tune.
const (
	maxTurns     = 40
	maxRunTokens = 400_000
)

// requestTimeout bounds one provider call. This is the gap M6 documented
// ("a harness that stops on a permission prompt blocks the run") becoming
// solvable: pika owns this loop, so it owns the timeout.
const requestTimeout = 5 * time.Minute

// systemPrompt is fixed kernel text describing the tools and the one rule
// that ends the loop.
const systemPrompt = "You are an agent in a verified Pika run, working in a repository with tools. Paths are repository-relative and must stay inside it; `.project/state/` is kernel-private and is refused. Use read_file to read a file, write_file to write one (the full new content), and run_command to run a shell command. Answer without a tool call to finish. Do not run git commit, git merge, git rebase, git push, or any GitHub command; Pika verifies and commits approved changes itself."

// Runner runs one loop: one prompt in, a final message out.
type Runner struct {
	name     string // contract key, for error messages
	provider provider
	model    string // resolved: the contract's model, else the provider's default
	effort   string // "" = omit the provider's reasoning control
	key      string // resolved at NewRunner from the provider's key env var
	calls    int    // complete calls made across the run
	usage    usage  // accumulated across the run's calls
}

// NewRunner builds the loop for one resolved agent. Every refusal here is
// produced before a request is made.
func NewRunner(name, providerName, model, effort string) (*Runner, error) {
	if providerName == "" {
		return nil, fmt.Errorf("agent %q declares runtime pika with no provider", name)
	}
	p, ok := providers[providerName]
	if !ok {
		return nil, fmt.Errorf("agent %q declares provider %q; runtime pika speaks anthropic, openai and openrouter", name, providerName)
	}
	// The key is read from pika's own environment, never from the
	// contract: a credential in a contract is a credential in every clone.
	key := os.Getenv(p.keyEnv)
	if key == "" {
		return nil, fmt.Errorf("agent %q: provider %q needs %s in the environment", name, providerName, p.keyEnv)
	}
	if model == "" {
		model = p.model
	}
	return &Runner{name: name, provider: p, model: model, effort: effort, key: key}, nil
}

// Runtime names the runtime this runner is, so a bundle — and the receipt
// that describes it — can name the agent that produced the change.
func (r *Runner) Runtime() string { return "pika" }

// Usage reports the model calls and tokens the run spent. It is the
// optional interface improve type-asserts for; subprocess runners cannot
// answer it.
func (r *Runner) Usage() (calls, tokensIn, tokensOut int) {
	return r.calls, r.usage.in, r.usage.out
}

// Run works one prompt to a final message: read the prompt, trade turns
// with the provider until a response arrives without a tool call, and
// leave the final message where the run asked for it. The loop writes
// {output} itself, like codex's OutputFile.
func (r *Runner) Run(ctx context.Context, root, promptPath, outputPath string) error {
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		return fmt.Errorf("pika loop: read prompt %s: %w", promptPath, err)
	}
	// The base-URL override is read at Run time, so a test's t.Setenv
	// before Run takes effect; it is the testing seam and the only one.
	baseURL := r.provider.baseURL
	if v := os.Getenv(r.provider.baseURLEnv); v != "" {
		baseURL = v
	}
	cl := bindClient(r.provider.client, r.key, baseURL)

	messages := []message{{role: "user", parts: []part{{text: string(prompt)}}}}
	tools := toolSet()
	for turn := 1; ; turn++ {
		if turn > maxTurns {
			return fmt.Errorf("pika loop: turn limit reached (%d)", maxTurns)
		}
		if r.usage.in+r.usage.out > maxRunTokens {
			return fmt.Errorf("pika loop: token limit reached (%d)", maxRunTokens)
		}
		// Each request carries its own timeout, cancelled on return. A
		// timed-out call is never retried: it may have produced tool
		// effects on the far side, so it aborts the turn with a named
		// error instead.
		reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		resp, err := cl.complete(reqCtx, request{
			system:   systemPrompt,
			messages: messages,
			tools:    tools,
			model:    r.model,
			effort:   r.effort,
		})
		if err != nil {
			timedOut := errors.Is(reqCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
			cancel()
			if timedOut {
				return errors.New("pika loop: request timed out after 5m")
			}
			return err
		}
		cancel()
		r.calls++
		r.usage.in += resp.usage.in
		r.usage.out += resp.usage.out

		if len(resp.calls) == 0 {
			final := strings.Join(resp.text, "\n")
			if err := os.WriteFile(outputPath, []byte(final), 0o600); err != nil {
				return fmt.Errorf("pika loop: write final message %s: %w", outputPath, err)
			}
			var parts []part
			for _, t := range resp.text {
				parts = append(parts, part{text: t})
			}
			messages = append(messages, message{role: "assistant", parts: parts})
			return r.writeTranscript(outputPath, messages)
		}

		// Append the assistant's turn — text parts, then call parts — then
		// execute each call and append one user message per result.
		var parts []part
		for _, t := range resp.text {
			parts = append(parts, part{text: t})
		}
		for i := range resp.calls {
			c := resp.calls[i]
			parts = append(parts, part{call: &c})
		}
		messages = append(messages, message{role: "assistant", parts: parts})
		for _, call := range resp.calls {
			result := executeTool(ctx, root, call)
			messages = append(messages, message{role: "user", parts: []part{{result: &result}}})
		}
	}
}

// transcriptFile is the on-disk shape of pika-transcript.json: the whole
// conversation plus what the run spent.
type transcriptFile struct {
	Messages []message       `json:"messages"`
	Usage    transcriptUsage `json:"usage"`
}

type transcriptUsage struct {
	Calls int `json:"calls"`
	In    int `json:"in"`
	Out   int `json:"out"`
}

// writeTranscript persists the run's transcript next to the final message
// in the handoff bundle. It is marshalled indented and run through
// redact.Apply before writing — the loop's one direct use of redact —
// because file contents and command output that went to the provider raw
// must not land in a persisted file raw.
func (r *Runner) writeTranscript(outputPath string, messages []message) error {
	payload := transcriptFile{
		Messages: messages,
		Usage:    transcriptUsage{Calls: r.calls, In: r.usage.in, Out: r.usage.out},
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("pika loop: marshal transcript: %w", err)
	}
	data = append([]byte(redact.Apply(string(data))), '\n')
	path := filepath.Join(filepath.Dir(outputPath), "pika-transcript.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("pika loop: write transcript %s: %w", path, err)
	}
	return nil
}

// MarshalJSON on the neutral model exists for exactly one consumer: the
// transcript. The wire clients marshal their own request types and never
// see these.

func (m message) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Role  string `json:"role"`
		Parts []part `json:"parts"`
	}{Role: m.role, Parts: m.parts})
}

func (p part) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Text   string      `json:"text,omitempty"`
		Call   *toolCall   `json:"call,omitempty"`
		Result *toolResult `json:"result,omitempty"`
	}{Text: p.text, Call: p.call, Result: p.result})
}

func (c toolCall) MarshalJSON() ([]byte, error) {
	input := c.input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	return json.Marshal(struct {
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}{ID: c.id, Name: c.name, Input: input})
}

func (r toolResult) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID      string `json:"id"`
		Output  string `json:"output"`
		IsError bool   `json:"is_error,omitempty"`
	}{ID: r.id, Output: r.output, IsError: r.isError})
}
