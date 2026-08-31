package adapters

import (
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/contract"
)

// The schema promises seven runtimes and the table must supply seven
// adapters. This reads the enum from the schema rather than restating it,
// so adding a value to the contract without adding an adapter fails here
// instead of failing for an operator at handoff time.
func TestEveryHarnessInTheContractSchemaHasAnAdapter(t *testing.T) {
	harnesses, err := contract.HarnessEnum()
	if err != nil {
		t.Fatalf("HarnessEnum: %v", err)
	}
	if len(harnesses) == 0 {
		t.Fatal("the contract schema declares no harnesses")
	}
	for _, h := range harnesses {
		ad, ok := Lookup(h)
		if !ok {
			t.Errorf("no adapter implements runtime %q, which the contract schema accepts", h)
			continue
		}
		if ad.Runtime != h {
			t.Errorf("Lookup(%q).Runtime = %q", h, ad.Runtime)
		}
		if h != RuntimeCustom && ad.Binary == "" {
			t.Errorf("adapter %q declares no binary", h)
		}
	}
}

// The golden argv of every adapter, with and without the optional
// controls. These are the exact bytes a harness receives, so a change here
// is a change in what pika asks another program to do.
func TestArgsPerAdapter(t *testing.T) {
	const (
		root   = "/repo"
		prompt = "/bundle/prompt.md"
		output = "/bundle/last-message.raw"
	)
	cases := []struct {
		runtime string
		bare    []string
		full    []string
	}{
		{
			runtime: RuntimeCodex,
			bare: []string{"exec", "-c", "sandbox_workspace_write.network_access=false",
				"--approve-for-me", "--cd", root, "--output-last-message", output, "-"},
			full: []string{"exec", "-c", "sandbox_workspace_write.network_access=false",
				"--model", "gpt-5-codex", "-c", `model_reasoning_effort="high"`,
				"--approve-for-me", "--cd", root, "--output-last-message", output, "-"},
		},
		{
			runtime: RuntimeClaude,
			bare:    []string{"-p", "--output-format", "text", "--permission-mode", "acceptEdits"},
			full: []string{"-p", "--output-format", "text", "--permission-mode", "acceptEdits",
				"--model", "gpt-5-codex", "--effort", "high"},
		},
		{
			runtime: RuntimeOMP,
			bare: []string{"-p", "--cwd", root, "--mode", "text", "--approval-mode", "write",
				"--no-session", "@" + prompt},
			full: []string{"-p", "--cwd", root, "--mode", "text", "--approval-mode", "write",
				"--no-session", "--model", "gpt-5-codex", "--thinking", "high", "@" + prompt},
		},
		{
			runtime: RuntimeGemini,
			bare:    []string{"-p", "--output-format", "text", "--approval-mode", "auto_edit", "--skip-trust"},
			full: []string{"-p", "--output-format", "text", "--approval-mode", "auto_edit", "--skip-trust",
				"--model", "gpt-5-codex"},
		},
		{
			runtime: RuntimeOpenCode,
			bare:    []string{"run", "--format", "default", "--auto", "--dir", root, "--file", prompt},
			full: []string{"run", "--format", "default", "--auto", "--dir", root,
				"--model", "gpt-5-codex", "--file", prompt},
		},
		{
			runtime: RuntimeACP,
			bare:    []string{"acp"},
			full:    []string{"acp"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.runtime, func(t *testing.T) {
			ad, ok := Lookup(tc.runtime)
			if !ok {
				t.Fatalf("no adapter for %q", tc.runtime)
			}
			bare, err := Agent{Name: "builder", Runtime: tc.runtime}.argv(ad, Spawn{
				Root: root, PromptPath: prompt, OutputPath: output,
			})
			if err != nil {
				t.Fatalf("argv without controls: %v", err)
			}
			assertArgv(t, bare, tc.bare)
			full, err := Agent{Name: "builder", Runtime: tc.runtime, Model: "gpt-5-codex", Effort: "high"}.argv(ad, Spawn{
				Root: root, PromptPath: prompt, OutputPath: output, Model: "gpt-5-codex", Effort: "high",
			})
			if err != nil {
				t.Fatalf("argv with controls: %v", err)
			}
			assertArgv(t, full, tc.full)
			if tc.runtime == RuntimeGemini || tc.runtime == RuntimeOpenCode {
				if strings.Contains(strings.Join(full, " "), "high") {
					t.Errorf("runtime %q has no effort control but its argv carries one: %v", tc.runtime, full)
				}
			}
		})
	}
}

func assertArgv(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q (whole argv: %v)", i, got[i], want[i], got)
		}
	}
}

// No adapter may ask a harness to stop asking permission. The whole point
// of the permission column in the table is that each adapter took the
// least dangerous auto-approval its runtime offers; a bypass flag would
// make the handoff unbounded and the sandbox decorative.
func TestEveryAdapterArgvCarriesItsOwnPermissionPosture(t *testing.T) {
	bypass := []string{
		"--dangerously-skip-permissions",
		"--allow-dangerously-skip-permissions",
		"bypassPermissions",
		"yolo",
		"--approval-mode yolo",
		"--sandbox danger-full-access",
	}
	for _, ad := range All() {
		if ad.Args == nil {
			continue
		}
		argv := ad.Args(probeSpawn)
		joined := strings.Join(argv, "\n")
		for _, flag := range bypass {
			if strings.Contains(joined, flag) {
				t.Errorf("adapter %q emits %q, which bypasses permission: %v", ad.Runtime, flag, argv)
			}
		}
	}
}

// codex rejects --sandbox beside --approve-for-me, and the pair made every
// handoff exit 2 on argument parsing before an agent read a byte of the
// prompt. The bug was fixed by removing the flag, so the test is that it
// stays removed and that the posture it replaced is still there.
func TestCodexNeverSendsSandboxAlongsideApproveForMe(t *testing.T) {
	ad, _ := Lookup(RuntimeCodex)
	argv := ad.Args(probeSpawn)
	var sandbox, approve bool
	for _, a := range argv {
		if a == "--sandbox" {
			sandbox = true
		}
		if a == "--approve-for-me" {
			approve = true
		}
	}
	if sandbox {
		t.Errorf("codex argv carries --sandbox alongside --approve-for-me: %v", argv)
	}
	if !approve {
		t.Errorf("codex argv lost --approve-for-me: %v", argv)
	}
}

// Flags is how the compatibility probe knows what to check, so it has to
// be the flags a real argv produces and not a second list. The sentinel
// and every value are excluded because neither is a flag.
func TestFlagsDropsTheStdinSentinelAndEveryValue(t *testing.T) {
	ad, _ := Lookup(RuntimeCodex)
	got := ad.ProbeFlags()
	want := []string{"-c", "--model", "--approve-for-me", "--cd", "--output-last-message"}
	if len(got) != len(want) {
		t.Fatalf("Flags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Flags[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
	for _, f := range got {
		if strings.Contains(f, "{") || f == "-" {
			t.Errorf("Flags carries a value or the stdin sentinel: %q", f)
		}
	}
}

// A control the runtime cannot express is an error, never a silently
// dropped field: an operator who set effort and got none would have no way
// to discover it.
func TestEffortIsRefusedWhereTheRuntimeHasNoEffortControl(t *testing.T) {
	for _, runtime := range []string{RuntimeGemini, RuntimeOpenCode, RuntimeACP} {
		_, err := New(Agent{Name: "builder", Runtime: runtime, Effort: "high"})
		if err == nil {
			t.Fatalf("runtime %q accepted an effort it cannot express", runtime)
		}
		if !strings.Contains(err.Error(), `has no effort control`) {
			t.Errorf("runtime %q error = %q, want it to name the missing control", runtime, err)
		}
	}
	// And model, for the one runtime that maps neither.
	if _, err := New(Agent{Name: "builder", Runtime: RuntimeACP, Model: "glm"}); err == nil {
		t.Fatal("runtime acp accepted a model it cannot express")
	}
}

// custom has no binary of its own: the contract has to name one.
func TestCustomAdapterRequiresACommand(t *testing.T) {
	_, err := New(Agent{Name: "builder", Runtime: RuntimeCustom})
	if err == nil {
		t.Fatal("a custom agent with no command was accepted")
	}
	if !strings.Contains(err.Error(), "declares runtime custom with no command") {
		t.Errorf("error = %q, want it to name the missing command", err)
	}
	// With a command it resolves, so the refusal is specifically about
	// the missing command.
	if _, err := New(Agent{Name: "builder", Runtime: RuntimeCustom, Command: "/bin/echo"}); err != nil {
		t.Errorf("a custom agent with a command was refused: %v", err)
	}
}

// A runtime outside the table is refused naming both the agent and the
// runtime, because the schema and the table are two lists that have to
// agree and this is where their disagreement surfaces.
func TestAnUnimplementedRuntimeIsRefused(t *testing.T) {
	_, err := New(Agent{Name: "builder", Runtime: "socketpuppet"})
	if err == nil {
		t.Fatal("an unimplemented runtime was accepted")
	}
	if !strings.Contains(err.Error(), "no adapter implements it") {
		t.Errorf("error = %q, want it to name the missing adapter", err)
	}
}

// A custom template expresses exactly the controls it references, so
// {model} in a template is a model control and the absence of {effort} is
// not an effort control.
func TestCustomSupportComesFromTheTemplate(t *testing.T) {
	agent := Agent{
		Name: "builder", Runtime: RuntimeCustom, Command: "/bin/echo",
		Args: []string{"--model", "{model}", "--root", "{root}"},
	}
	ad, _ := Lookup(RuntimeCustom)
	if got := agent.Support(ad); got.Model != true || got.Effort != false {
		t.Fatalf("Support = %+v, want model only", got)
	}
	if _, err := New(Agent{Name: "builder", Runtime: RuntimeCustom, Command: "/bin/echo",
		Args: agent.Args, Effort: "high"}); err == nil {
		t.Error("a custom template with no {effort} accepted an effort")
	}
	if _, err := New(Agent{Name: "builder", Runtime: RuntimeCustom, Command: "/bin/echo",
		Args: agent.Args, Model: "glm"}); err != nil {
		t.Errorf("a custom template with {model} refused a model: %v", err)
	}
}

// A template that names {output} writes the message itself; one that does
// not is read from stdout.
func TestCustomOutputComesFromTheTemplate(t *testing.T) {
	ad, _ := Lookup(RuntimeCustom)
	file := Agent{Name: "b", Runtime: RuntimeCustom, Args: []string{"--out", "{output}", "--prompt", "{prompt}"}}
	stdout := Agent{Name: "b", Runtime: RuntimeCustom, Args: []string{"--prompt", "{prompt}"}}
	if got := file.Output(ad); got != OutputFile {
		t.Errorf("Output = %q, want file", got)
	}
	if got := stdout.Output(ad); got != OutputStdout {
		t.Errorf("Output = %q, want stdout", got)
	}
}

func TestAgentFromContractResolvesTheContractKey(t *testing.T) {
	c := &contract.Contract{Agents: map[string]contract.AgentConfig{
		"reviewer": {Runtime: RuntimeClaude, Model: "opus", Command: "claude-dev",
			Args: []string{"-p"}, Env: []string{"ANTHROPIC_API_KEY"}},
	}}
	got, err := AgentFromContract(c, "/repo/.project/contract.yaml", "reviewer")
	if err != nil {
		t.Fatalf("AgentFromContract: %v", err)
	}
	if got.Name != "reviewer" || got.Runtime != RuntimeClaude || got.Model != "opus" {
		t.Errorf("agent = %+v, want the reviewer entry", got)
	}
	if got.Command != "claude-dev" || len(got.Args) != 1 || len(got.Env) != 1 {
		t.Errorf("agent = %+v, want command, args and env carried over", got)
	}
}

// The refusal names the contract file, because an agent that is not
// configured is missing from a document and an operator needs to know
// which one to edit.
func TestAgentFromContractNamesTheContractItIsMissingFrom(t *testing.T) {
	c := &contract.Contract{}
	_, err := AgentFromContract(c, "/repo/.project/contract.yaml", "explorer")
	if err == nil {
		t.Fatal("an unconfigured agent was accepted")
	}
	if !strings.Contains(err.Error(), `agent "explorer" is not configured in /repo/.project/contract.yaml`) {
		t.Errorf("error = %q, want it to name the contract path", err)
	}
}
