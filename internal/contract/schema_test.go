package contract

import (
	"strings"
	"testing"
)

func TestValidMinimumContract(t *testing.T) {
	c, err := Load("testdata/valid-minimum.yaml")
	if err != nil {
		t.Fatalf("expected valid contract, got %v", err)
	}
	if c.Schema != 1 {
		t.Fatalf("schema = %d, want 1", c.Schema)
	}
	if len(c.Profiles) < 1 {
		t.Fatal("profiles must not be empty")
	}
	if c.Project.Name != "fixture" || c.Project.Topology != "single" {
		t.Fatalf("project = %+v, want name=fixture topology=single", c.Project)
	}
	if c.Commands["test"] != "go test ./..." {
		t.Fatalf("commands[test] = %q, want %q", c.Commands["test"], "go test ./...")
	}
	if c.GitHub.Merge != "squash" {
		t.Fatalf("github.merge = %q, want squash", c.GitHub.Merge)
	}
	if c.Evidence.Publish != "sanitized" {
		t.Fatalf("evidence.publish = %q, want sanitized", c.Evidence.Publish)
	}
}

func TestRejectDuplicateYAMLKey(t *testing.T) {
	_, err := Load("testdata/invalid-duplicate-key.yaml")
	if err == nil {
		t.Fatal("expected duplicate-key error, got nil")
	}
	// The second `schema` key is on line 2, column 1 of the fixture.
	if !strings.Contains(err.Error(), `[2:1] duplicate key "schema"`) {
		t.Fatalf("error should report [line:col] and name the duplicate key, got: %v", err)
	}
}
func TestRejectUnknownTopLevelKey(t *testing.T) {
	_, err := Load("testdata/invalid-unknown-key.yaml")
	if err == nil {
		t.Fatal("expected unknown-key error, got nil")
	}
	if !strings.Contains(err.Error(), "bogusKey") {
		t.Fatalf("error should name the unknown key, got: %v", err)
	}
}

func TestRejectInvalidEnum(t *testing.T) {
	_, err := Load("testdata/invalid-enum.yaml")
	if err == nil {
		t.Fatal("expected enum violation error, got nil")
	}
	if !strings.Contains(err.Error(), "topology") {
		t.Fatalf("error should name the offending field, got: %v", err)
	}
}

func TestAgentsTypedConfig(t *testing.T) {
	c, err := Load("testdata/valid-minimum.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Agents != nil {
		t.Fatalf("agents should be absent in minimal fixture, got %+v", c.Agents)
	}
}

// An agent may name the executable to run, the argv template it takes and
// the environment variable names that cross into it. `custom` has no
// binary of its own, so without `command` it names a runtime the kernel
// cannot start — the same class of error as a projection for a harness
// nothing reads, and refused at the schema for the same reason.
func TestAgentAcceptsCommandArgsAndEnv(t *testing.T) {
	c, err := Load("testdata/valid-agent-custom.yaml")
	if err != nil {
		t.Fatalf("expected valid contract, got %v", err)
	}
	a, ok := c.Agents["builder"]
	if !ok {
		t.Fatalf("agents = %+v, want a builder", c.Agents)
	}
	if a.Runtime != "custom" || a.Command != "/usr/local/bin/harness" {
		t.Fatalf("builder = %+v, want runtime=custom with a command", a)
	}
	want := []string{"--root", "{root}", "--prompt", "{prompt}", "--out", "{output}"}
	if len(a.Args) != len(want) {
		t.Fatalf("args = %v, want %v", a.Args, want)
	}
	for i := range want {
		if a.Args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, a.Args[i], want[i])
		}
	}
	if len(a.Env) != 2 || a.Env[0] != "HARNESS_TOKEN" || a.Env[1] != "HARNESS_REGION" {
		t.Errorf("env = %v, want [HARNESS_TOKEN HARNESS_REGION]", a.Env)
	}
}

func TestCustomAgentWithoutACommandIsRefused(t *testing.T) {
	_, err := Load("testdata/invalid-agent-custom-no-command.yaml")
	if err == nil {
		t.Fatal("a custom agent with no command was accepted")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Fatalf("error should name the field it requires, got: %v", err)
	}
}

// The env list holds NAMES, never values. A pattern that accepted a value
// would make `.project/contract.yaml` a place to commit a credential, and
// a contract is committed in every clone.
func TestAgentEnvRejectsAValueThatIsNotAName(t *testing.T) {
	_, err := Load("testdata/invalid-agent-env-value.yaml")
	if err == nil {
		t.Fatal("an env entry carrying a value was accepted")
	}
	if !strings.Contains(err.Error(), "HARNESS_TOKEN=sk-live-abc123") {
		t.Fatalf("error should name the entry it refused, got: %v", err)
	}
}

// HarnessEnum reads the schema rather than restating it, so the
// adapters test that asserts one adapter per harness cannot pass against a
// second, hand-maintained list that has drifted.
func TestHarnessEnumMatchesTheSchema(t *testing.T) {
	got, err := HarnessEnum()
	if err != nil {
		t.Fatalf("HarnessEnum: %v", err)
	}
	want := []string{"omp", "codex", "claude", "gemini", "opencode", "acp", "custom", "pika"}
	if len(got) != len(want) {
		t.Fatalf("HarnessEnum() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("HarnessEnum()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// `pika` is the eighth harness value: the built-in loop. The schema
// accepts the runtime with no `provider` — the provider requirement is the
// adapter's, not the schema's, because a schema that knew about the loop
// would be the loop's design leaking into a document that does not know
// what a loop is.
func TestAgentAcceptsThePikaRuntime(t *testing.T) {
	c, err := Load("testdata/valid-agent-pika.yaml")
	if err != nil {
		t.Fatalf("expected valid contract, got %v", err)
	}
	a, ok := c.Agents["builder"]
	if !ok {
		t.Fatalf("agents = %+v, want a builder", c.Agents)
	}
	if a.Runtime != "pika" || a.Provider != "anthropic" {
		t.Fatalf("builder = %+v, want runtime=pika provider=anthropic", a)
	}
}
