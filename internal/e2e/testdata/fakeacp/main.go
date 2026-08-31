// Command fakeacp is a scripted ACP v1 peer, for the end-to-end tests
// that drive a run whose builder speaks ACP instead of a CLI.
//
// It answers `initialize`, creates a session, streams two message chunks,
// asks one permission question, and responds to the prompt. It is a
// fixture and not a stub: the real ACPRunner builds the requests and
// parses these replies, so both ends of the protocol are the code under
// test. No model, no network, no SDK.
//
// It is installed through the contract's `command` field rather than
// under a harness's binary name, because ACP is a protocol and not a
// vendor: pika's default ACP binary is omp's, and a test that wanted this
// one to be found there would have to shadow it.
//
// The same FAKE_AGENT_* variables the process fixture reads drive it:
//
//	FAKE_AGENT_FILE     repository-relative file to write: the agent's edit
//	FAKE_AGENT_CONTENT  contents for that file
//	FAKE_AGENT_MESSAGE  the final agent message (defaults to a fixed line)
//	FAKE_AGENT_ARGV     record the argv pika spawned at this path, one per line
//	FAKE_AGENT_PROMPT   copy the prompt it was handed to this path
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fakeacp:", err)
		os.Exit(1)
	}
}

// rpc is one JSON-RPC 2.0 envelope. ID is raw so an id pika chose —
// number or string — is echoed back verbatim rather than coerced.
type rpc struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

func run() error {
	if err := record(os.Getenv("FAKE_AGENT_ARGV"), strings.Join(os.Args[1:], "\n")+"\n"); err != nil {
		return err
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 8<<20)
	out := json.NewEncoder(os.Stdout)
	var root, prompt string
	for in.Scan() {
		var msg rpc
		if err := json.Unmarshal(in.Bytes(), &msg); err != nil {
			return fmt.Errorf("unreadable request: %w", err)
		}
		switch msg.Method {
		case "initialize":
			if err := out.Encode(rpc{JSONRPC: "2.0", ID: msg.ID,
				Result: mustJSON(map[string]any{
					"protocolVersion": 1,
					"agentInfo":       map[string]any{"name": "fakeacp", "version": "1.0.0"},
				})}); err != nil {
				return err
			}
		case "session/new":
			var params struct {
				Cwd string `json:"cwd"`
			}
			if err := json.Unmarshal(msg.Params, &params); err != nil {
				return fmt.Errorf("session/new params: %w", err)
			}
			root = params.Cwd
			if err := out.Encode(rpc{JSONRPC: "2.0", ID: msg.ID,
				Result: mustJSON(map[string]any{"sessionId": "sess-fakeacp"})}); err != nil {
				return err
			}
		case "session/prompt":
			var params struct {
				Prompt []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"prompt"`
			}
			if err := json.Unmarshal(msg.Params, &params); err != nil {
				return fmt.Errorf("session/prompt params: %w", err)
			}
			for _, block := range params.Prompt {
				if block.Type == "text" {
					prompt += block.Text
				}
			}
			if err := record(os.Getenv("FAKE_AGENT_PROMPT"), prompt); err != nil {
				return err
			}
			// The edit, if the test asked for one.
			if name := os.Getenv("FAKE_AGENT_FILE"); name != "" && root != "" {
				path := filepath.Join(root, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
				}
				if err := os.WriteFile(path, []byte(os.Getenv("FAKE_AGENT_CONTENT")), 0o644); err != nil {
					return fmt.Errorf("write %s: %w", path, err)
				}
			}
			message := os.Getenv("FAKE_AGENT_MESSAGE")
			if message == "" {
				message = "fakeacp: the requested edit is in the working tree.\n"
			}
			// Two chunks, in order: the runner concatenates them and a
			// test that reads the message back is reading the join.
			for _, chunk := range []string{message, "[acp chunk two]"} {
				if err := out.Encode(rpc{JSONRPC: "2.0", Method: "session/update",
					Params: mustJSON(map[string]any{
						"sessionId": "sess-fakeacp",
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"content":       map[string]any{"type": "text", "text": chunk},
						},
					})}); err != nil {
					return err
				}
			}
			// One permission question, and the assertion this fixture
			// exists to make: pika must answer allow_once and never
			// allow_always.
			id := json.RawMessage(`"perm-fakeacp"`)
			if err := out.Encode(rpc{JSONRPC: "2.0", ID: id, Method: "session/request_permission",
				Params: mustJSON(map[string]any{
					"sessionId": "sess-fakeacp",
					"toolCall": map[string]any{
						"toolCallId": "call-1",
						"title":      "edit the repository",
						"status":     "pending",
					},
					"options": []any{
						map[string]any{"optionId": "always", "name": "Allow always", "kind": "allow_always"},
						map[string]any{"optionId": "once", "name": "Allow once", "kind": "allow_once"},
						map[string]any{"optionId": "no", "name": "Reject", "kind": "reject_once"},
					},
				})}); err != nil {
				return err
			}
			if !in.Scan() {
				if err := in.Err(); err != nil {
					return fmt.Errorf("no permission reply: %w", err)
				}
				return fmt.Errorf("no permission reply: pika closed the connection")
			}
			if err := checkPermissionReply(in.Bytes()); err != nil {
				return err
			}
			if err := out.Encode(rpc{JSONRPC: "2.0", ID: msg.ID,
				Result: mustJSON(map[string]any{"stopReason": "end_turn"})}); err != nil {
				return err
			}
		default:
			// An unknown method is answered rather than ignored: an
			// agent's client waiting on a reply that never comes is a
			// hang, and a hang in a test suite is a suite that never
			// finishes.
			if msg.ID != nil {
				_ = out.Encode(rpc{JSONRPC: "2.0", ID: msg.ID,
					Result: mustJSON(map[string]any{})})
			}
		}
	}
	if err := in.Err(); err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	return nil
}

// checkPermissionReply asserts the reply pika sent selected allow_once.
//
// allow_always is the wrong answer and this fixture says so out loud: a
// remembered grant outlives the run that authorized it, and pika has no
// mechanism to revoke one, so a grant made for this handoff would
// silently cover every later one.
func checkPermissionReply(line []byte) error {
	var reply struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &reply); err != nil {
		return fmt.Errorf("unreadable permission reply: %w", err)
	}
	if reply.Result.Outcome.Outcome != "selected" {
		return fmt.Errorf("permission reply = %s, want a selected outcome", line)
	}
	if reply.Result.Outcome.OptionID != "once" {
		return fmt.Errorf("permission reply selected optionId %q, want the allow_once option: %s",
			reply.Result.Outcome.OptionID, line)
	}
	if string(reply.ID) != `"perm-fakeacp"` {
		return fmt.Errorf("permission reply id = %s, want the id this agent asked under", reply.ID)
	}
	return nil
}

func mustJSON(v any) json.RawMessage {
	bs, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return bs
}

func record(path, content string) error {
	if path == "" {
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
