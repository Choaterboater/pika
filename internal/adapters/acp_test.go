package adapters

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeACP is a scripted ACP v1 peer: it answers initialize, creates a
// session, streams two message chunks, asks one permission question, and
// responds to the prompt. Everything it varies is an environment variable,
// so one script covers the happy path and each failure.
//
// It is a fixture and not a stub: the real ACPRunner builds the requests
// and parses these replies, so the protocol on both ends is the code under
// test. No model, no network, no SDK.
const fakeACP = `#!/bin/sh
VERSION="${FAKE_ACP_VERSION:-1}"
STOP="${FAKE_ACP_STOP:-end_turn}"
OPTIONS='[{"optionId":"once","name":"Allow once","kind":"allow_once"}]'
if [ -n "$FAKE_ACP_OPTIONS" ]; then
  OPTIONS="$FAKE_ACP_OPTIONS"
fi
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"jsonrpc":"2.0","id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":%s,"agentInfo":{"name":"fakeacp","version":"1.0.0"}}}\n' "$id" "$VERSION"
      ;;
    *'"method":"session/new"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-1"}}\n' "$id"
      ;;
    *'"method":"session/prompt"'*)
      printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"first "}}}}\n'
      printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call","content":{"type":"text","text":"ignored"}}}}\n'
      printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"second"}}}}\n'
      printf '{"jsonrpc":"2.0","id":"perm-1","method":"session/request_permission","params":{"sessionId":"sess-1","toolCall":{"toolCallId":"t1","title":"edit src/main.go","status":"pending"},"options":%s}}\n' "$OPTIONS"
      IFS= read -r reply
      if [ -n "$FAKE_ACP_PERM_OUT" ]; then
        printf '%s\n' "$reply" > "$FAKE_ACP_PERM_OUT"
      fi
      printf '{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"%s"}}\n' "$id" "$STOP"
      ;;
  esac
done
exit 0
`

// acpFixture is one ACP test's scaffolding. The adapter's default binary
// is omp's, so the fixture is installed under that name and driven through
// the `acp` subcommand argv the adapter builds.
type acpFixture struct {
	root       string
	promptPath string
	outputPath string
	permPath   string
}

func newACPFixture(t *testing.T) acpFixture {
	t.Helper()
	installScript(t, RuntimeOMP, fakeACP)
	f := newProcessFixture(t)
	permPath := filepath.Join(filepath.Dir(f.outputPath), "permission.json")
	t.Setenv("FAKE_ACP_PERM_OUT", permPath)
	return acpFixture{
		root:       f.root,
		promptPath: f.promptPath,
		outputPath: f.outputPath,
		permPath:   permPath,
	}
}

func (f acpFixture) run(t *testing.T) error {
	t.Helper()
	runner, err := New(Agent{Name: "builder", Runtime: RuntimeACP})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner.Run(context.Background(), f.root, f.promptPath, f.outputPath)
}

func (f acpFixture) message(t *testing.T) string {
	t.Helper()
	bs, err := os.ReadFile(f.outputPath)
	if err != nil {
		return ""
	}
	return string(bs)
}

func (f acpFixture) permissionReply(t *testing.T) string {
	t.Helper()
	bs, err := os.ReadFile(f.permPath)
	if err != nil {
		t.Fatalf("the agent received no permission reply: %v", err)
	}
	return string(bs)
}

// The whole protocol, end to end, against a scripted peer: initialize,
// session/new, session/prompt, two chunks concatenated in arrival order,
// one permission question answered, and the message written where the run
// asked for it.
func TestACPClientDrivesAFakeAgent(t *testing.T) {
	f := newACPFixture(t)
	if err := f.run(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := f.message(t); got != "first second" {
		t.Errorf("message = %q, want the two chunks concatenated in order", got)
	}
	if reply := f.permissionReply(t); !strings.Contains(reply, `"optionId":"once"`) {
		t.Errorf("permission reply = %s, want the allow_once option selected", reply)
	}
}

// allow_always is never selected. A remembered grant outlives the run that
// authorized it, and pika has no mechanism to revoke one, so a grant made
// for this handoff would silently cover every later one.
func TestACPSelectsAllowOnceAndNeverAllowAlways(t *testing.T) {
	f := newACPFixture(t)
	t.Setenv("FAKE_ACP_OPTIONS",
		`[{"optionId":"always","name":"Allow always","kind":"allow_always"},{"optionId":"once","name":"Allow once","kind":"allow_once"}]`)
	if err := f.run(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reply := f.permissionReply(t)
	if !strings.Contains(reply, `"optionId":"once"`) {
		t.Errorf("permission reply = %s, want allow_once", reply)
	}
	if strings.Contains(reply, `"optionId":"always"`) {
		t.Errorf("permission reply selected allow_always: %s", reply)
	}
	if !strings.Contains(reply, `"id":"perm-1"`) {
		t.Errorf("permission reply did not echo the agent's own id: %s", reply)
	}
}

// With no allow_once offered, the runner rejects rather than falling
// through to allow_always.
func TestACPRejectsWhenNoAllowOnceIsOffered(t *testing.T) {
	f := newACPFixture(t)
	t.Setenv("FAKE_ACP_OPTIONS",
		`[{"optionId":"always","name":"Allow always","kind":"allow_always"},{"optionId":"no","name":"Reject","kind":"reject_once"}]`)
	if err := f.run(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reply := f.permissionReply(t)
	if !strings.Contains(reply, `"optionId":"no"`) {
		t.Errorf("permission reply = %s, want reject_once", reply)
	}
	if strings.Contains(reply, "always") {
		t.Errorf("permission reply mentions allow_always: %s", reply)
	}
}

// A different major version is a different protocol. Answering it as though
// it were this one would send session/prompt to an agent that reads it
// differently, so the refusal names both sides.
func TestACPRefusesAProtocolVersionItDoesNotSpeak(t *testing.T) {
	f := newACPFixture(t)
	t.Setenv("FAKE_ACP_VERSION", "2")
	err := f.run(t)
	if err == nil {
		t.Fatal("a protocol version 2 agent was driven as version 1")
	}
	for _, want := range []string{"protocol version 2", "pika speaks 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %s", err, want)
		}
	}
}

// A stop reason other than end_turn means the agent did not finish the
// turn, and the message it produced is not the one the run asked for.
func TestACPNamesAStopReasonThatIsNotEndTurn(t *testing.T) {
	f := newACPFixture(t)
	t.Setenv("FAKE_ACP_STOP", "refusal")
	err := f.run(t)
	if err == nil {
		t.Fatal("an agent that stopped refusing was reported as finished")
	}
	if !strings.Contains(err.Error(), `agent stopped with reason "refusal"`) {
		t.Errorf("error = %q, want it to name the stop reason", err)
	}
	// The message it did produce is still on disk: a run that stopped
	// has to leave what it saw behind for whoever comes next.
	if got := f.message(t); got != "first second" {
		t.Errorf("message = %q, want the partial message preserved", got)
	}
}
