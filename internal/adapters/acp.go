package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/Choaterboater/pika/internal/version"
)

// ACP protocol constants. ACP is JSON-RPC 2.0 over the child's stdio,
// newline-delimited; the only dependency it needs is encoding/json, which
// is why pika can speak it without adding one.
const (
	acpProtocolVersion = 1
	acpClientName      = "pika"

	methodInitialize        = "initialize"
	methodSessionNew        = "session/new"
	methodSessionPrompt     = "session/prompt"
	methodSessionUpdate     = "session/update"
	methodRequestPermission = "session/request_permission"

	updateAgentMessageChunk = "agent_message_chunk"
	stopReasonEndTurn       = "end_turn"
)

// acpScannerBuffer is the largest single JSON-RPC message the runner will
// read. A permission request carrying a diff is comfortably under a
// megabyte; a message larger than this is refused rather than read in
// pieces that would never be assembled.
const acpScannerBuffer = 8 << 20

// ACPRunner drives an ACP v1 agent over the child's stdio.
type ACPRunner struct {
	agent   Agent
	adapter Adapter
}

// Runtime implements Runner.
func (r *ACPRunner) Runtime() string { return r.adapter.Runtime }

// Run speaks one ACP session: initialize, create the session, prompt it,
// and collect the agent's message.
func (r *ACPRunner) Run(ctx context.Context, root, promptPath, outputPath string) error {
	binary := r.agent.Binary(r.adapter)
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return fmt.Errorf("agent %q: runtime %q needs %q on PATH: %w",
			r.agent.Name, r.adapter.Runtime, binary, err)
	}
	spawn := Spawn{Root: root, PromptPath: promptPath, OutputPath: outputPath}
	args, err := r.agent.argv(r.adapter, spawn)
	if err != nil {
		return err
	}
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		return fmt.Errorf("open handoff prompt: %w", err)
	}

	cmd := exec.CommandContext(ctx, resolved, args...)
	cmd.Dir = root
	cmd.Env = r.childEnv()
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("acp: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s handoff: %w", r.adapter.Runtime, err)
	}
	conn := &acpConn{in: bufio.NewScanner(stdout), out: stdin, stderr: os.Stderr}
	conn.in.Buffer(make([]byte, 0, 64<<10), acpScannerBuffer)

	runErr := r.session(ctx, conn, root, string(prompt), outputPath)
	// Closing stdin is what tells the agent the conversation is over; an
	// agent waiting for a second prompt would otherwise hold the pipe
	// open and the Wait below would never return.
	closeErr := stdin.Close()
	waitErr := cmd.Wait()
	if runErr != nil {
		return runErr
	}
	if closeErr != nil && !errors.Is(closeErr, io.ErrClosedPipe) {
		return fmt.Errorf("acp: close agent stdin: %w", closeErr)
	}
	if waitErr != nil {
		return fmt.Errorf("%s handoff: %w", r.adapter.Runtime, waitErr)
	}
	return nil
}

// session drives one ACP session to its final message.
func (r *ACPRunner) session(ctx context.Context, conn *acpConn, root, prompt, outputPath string) error {
	init, err := conn.call(ctx, methodInitialize, map[string]any{
		"protocolVersion": acpProtocolVersion,
		"clientInfo":      map[string]any{"name": acpClientName, "version": version.String()},
	}, nil)
	if err != nil {
		return fmt.Errorf("acp: initialize: %w", err)
	}
	var initialized struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := json.Unmarshal(init, &initialized); err != nil {
		return fmt.Errorf("acp: initialize: unreadable result: %w", err)
	}
	// A different major version is a different protocol. Answering it as
	// though it were this one would send session/prompt to an agent that
	// reads it differently, which is a hang or a corrupt message rather
	// than an error anyone can act on.
	if initialized.ProtocolVersion != acpProtocolVersion {
		return fmt.Errorf("acp: agent speaks protocol version %d; pika speaks %d",
			initialized.ProtocolVersion, acpProtocolVersion)
	}

	created, err := conn.call(ctx, methodSessionNew, map[string]any{
		"cwd":        root,
		"mcpServers": []any{},
	}, nil)
	if err != nil {
		return fmt.Errorf("acp: session/new: %w", err)
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(created, &session); err != nil {
		return fmt.Errorf("acp: session/new: unreadable result: %w", err)
	}

	var message strings.Builder
	result, err := conn.call(ctx, methodSessionPrompt, map[string]any{
		"sessionId": session.SessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": prompt}},
	}, func(msg rpcMessage) {
		if msg.Method != methodSessionUpdate {
			return
		}
		// Chunks arrive in order and the message is their
		// concatenation; reordering them would produce text the agent
		// did not write.
		if text, ok := messageChunk(msg.Params); ok {
			message.WriteString(text)
		}
	})
	if err != nil {
		return fmt.Errorf("acp: session/prompt: %w", err)
	}
	var prompted struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(result, &prompted); err != nil {
		return fmt.Errorf("acp: session/prompt: unreadable result: %w", err)
	}
	if err := os.WriteFile(outputPath, []byte(message.String()), 0o600); err != nil {
		return fmt.Errorf("acp: write final message: %w", err)
	}
	if prompted.StopReason != stopReasonEndTurn {
		return fmt.Errorf("acp: agent stopped with reason %q", prompted.StopReason)
	}
	return nil
}

// messageChunk reads one session/update notification's text.
//
// ACP v1 nests the discriminated union under params.update. A flat body —
// sessionUpdate and content directly on params — is accepted too, because
// that is the shape several early agents shipped and refusing one nesting
// over the other would turn a readable stream into silence for a reason
// nobody can see. Only an agent_message_chunk contributes text; every
// other update kind is a progress event and is passed over.
func messageChunk(params []byte) (string, bool) {
	var body struct {
		SessionUpdate string `json:"sessionUpdate"`
		Content       struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Update *struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if err := json.Unmarshal(params, &body); err != nil {
		return "", false
	}
	if body.Update != nil {
		body.SessionUpdate, body.Content = body.Update.SessionUpdate, body.Update.Content
	}
	if body.SessionUpdate != updateAgentMessageChunk {
		return "", false
	}
	return body.Content.Text, true
}

// childEnv is the ACP transport's environment policy, and it is the same
// one the process transport uses.
func (r *ACPRunner) childEnv() []string {
	p := &ProcessRunner{agent: r.agent, adapter: r.adapter}
	return p.childEnv()
}

// acpConn is one JSON-RPC 2.0 conversation over a child's stdio.
type acpConn struct {
	in     *bufio.Scanner
	out    io.Writer
	stderr io.Writer
	nextID int
}

// rpcMessage is one JSON-RPC 2.0 envelope. ID is raw so an id the agent
// chose — number or string — is echoed back verbatim rather than coerced.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message) }

// call sends one request and waits for its response, handing every
// message that arrives first to onNotification.
//
// A request from the agent is answered inline rather than on another
// goroutine: the agent is blocked waiting for that answer, so reading it
// and replying on the one thread is both sufficient and free of the
// ordering questions a concurrent reader would raise.
func (c *acpConn) call(ctx context.Context, method string, params any, onNotification func(rpcMessage)) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID
	if err := c.send(rpcMessage{JSONRPC: "2.0", ID: rawID(id), Method: method, Params: mustJSON(params)}); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		msg, err := c.read()
		if err != nil {
			return nil, err
		}
		switch {
		case msg.Method == "":
			if string(msg.ID) != string(rawID(id)) {
				continue
			}
			if msg.Error != nil {
				return nil, msg.Error
			}
			return msg.Result, nil
		case msg.ID != nil:
			c.answer(ctx, msg)
		default:
			if onNotification != nil {
				onNotification(msg)
			}
		}
	}
}

// answer replies to one request the agent made. The only request pika
// answers is a permission question; anything else is refused with a
// method-not-found error rather than ignored, because an agent waiting on
// a reply that never comes is a hang and not an error.
func (c *acpConn) answer(ctx context.Context, msg rpcMessage) {
	if msg.Method != methodRequestPermission {
		_ = c.send(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + msg.Method}})
		return
	}
	_ = c.send(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustJSON(permissionOutcome(ctx, msg, c.stderr))})
}

// permissionOutcome selects one option from a permission request.
//
// It picks the first allow_once and otherwise rejects. It never picks
// allow_always: a remembered grant outlives the run that authorized it,
// and pika has no mechanism to revoke one — so a grant made for this
// handoff would silently cover every later one.
//
// Every decision is written to stderr. A permission decision the operator
// cannot see is a decision the operator did not make.
func permissionOutcome(ctx context.Context, msg rpcMessage, stderr io.Writer) map[string]any {
	var req struct {
		ToolCall struct {
			Title string `json:"title"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(msg.Params, &req); err != nil {
		req.ToolCall.Title = "unknown tool"
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(stderr, "acp: cancel %s (context done: %v)\n", title(req.ToolCall.Title), err)
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	}
	for _, opt := range req.Options {
		if opt.Kind == "allow_once" {
			fmt.Fprintf(stderr, "acp: allow %s (allow_once: %s)\n", title(req.ToolCall.Title), opt.Name)
			return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": opt.OptionID}}
		}
	}
	for _, opt := range req.Options {
		if opt.Kind == "reject_once" {
			fmt.Fprintf(stderr, "acp: reject %s (reject_once: %s)\n", title(req.ToolCall.Title), opt.Name)
			return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": opt.OptionID}}
		}
	}
	fmt.Fprintf(stderr, "acp: reject %s (no allow_once or reject_once option offered)\n", title(req.ToolCall.Title))
	return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
}

// title keeps a missing tool title from rendering as an empty sentence.
func title(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unnamed tool"
	}
	return strings.TrimSpace(s)
}

func (c *acpConn) send(msg rpcMessage) error {
	bs, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("acp: encode %s: %w", msg.Method, err)
	}
	if _, err := c.out.Write(append(bs, '\n')); err != nil {
		return fmt.Errorf("acp: write to agent: %w", err)
	}
	return nil
}

func (c *acpConn) read() (rpcMessage, error) {
	if !c.in.Scan() {
		if err := c.in.Err(); err != nil {
			return rpcMessage{}, fmt.Errorf("acp: read from agent: %w", err)
		}
		return rpcMessage{}, io.EOF
	}
	var msg rpcMessage
	if err := json.Unmarshal(c.in.Bytes(), &msg); err != nil {
		return rpcMessage{}, fmt.Errorf("acp: unreadable message from agent: %w", err)
	}
	return msg, nil
}

func rawID(n int) json.RawMessage {
	bs, err := json.Marshal(n)
	if err != nil {
		return json.RawMessage("0")
	}
	return bs
}

func mustJSON(v any) json.RawMessage {
	bs, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return bs
}
