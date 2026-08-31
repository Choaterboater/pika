package adapters

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// Runner is the external-agent boundary. It mirrors improve.Runner
// structurally: adapters never imports improve, and improve never reaches
// into an adapter, so the two packages agree on a shape rather than on a
// type.
type Runner interface {
	Run(ctx context.Context, root, promptPath, outputPath string) error
	// Runtime is the runtime this runner spawns, so a bundle — and the
	// receipt that describes it — can name the agent that produced it
	// without being told separately.
	Runtime() string
}

// execEssentials are the environment variables a child with a declared
// allowlist still receives. Without PATH nothing resolves a binary, and
// without HOME and TMPDIR a harness that writes its own cache or temp
// files fails in a way that looks like a pika bug. They are names of
// directories, not secrets.
var execEssentials = []string{"PATH", "HOME", "TMPDIR"}

// ProcessRunner runs a one-shot harness: argv in, final message out.
type ProcessRunner struct {
	agent   Agent
	adapter Adapter
}

// Runtime implements Runner.
func (r *ProcessRunner) Runtime() string { return r.adapter.Runtime }

// Run resolves the binary, builds the argv, and runs the harness to
// completion. The final message lands at outputPath either because the
// harness wrote it there (OutputFile) or because this runner teed the
// child's stdout into it (OutputStdout).
func (r *ProcessRunner) Run(ctx context.Context, root, promptPath, outputPath string) error {
	spawn := Spawn{
		Root:       root,
		PromptPath: promptPath,
		OutputPath: outputPath,
		Model:      r.agent.Model,
		Effort:     r.agent.Effort,
	}
	// Configuration is checked before the binary is looked up: a
	// template that names no {output}, or a placeholder that does not
	// exist, is wrong whether or not the harness is installed, and an
	// operator fixing a contract should not have to install a runtime
	// first to be told they got it wrong.
	template := r.template()
	// A runtime that writes its own message to {output} has to be told
	// where, whether the argv is the adapter's or an operator's override.
	// Without the placeholder nothing is written and the run ends with an
	// empty bundle and an error that blames the harness.
	if r.adapter.Output == OutputFile && !templateUses(template, placeholderOutput) {
		return fmt.Errorf("agent %q overrides args for runtime %q but drops %s; the final message would never be written",
			r.agent.Name, r.adapter.Runtime, placeholderOutput)
	}
	args, err := r.agent.argv(r.adapter, spawn)
	if err != nil {
		return err
	}
	binary := r.agent.Binary(r.adapter)
	resolved, err := exec.LookPath(binary)
	if err != nil {
		// Naming the runtime as well as the binary: "codex is not on
		// PATH" tells an operator what to install, and only the runtime
		// says which adapter asked for it.
		return fmt.Errorf("agent %q: runtime %q needs %q on PATH: %w",
			r.agent.Name, r.adapter.Runtime, binary, err)
	}

	cmd := exec.CommandContext(ctx, resolved, args...)
	if r.adapter.CwdFlag == nil {
		cmd.Dir = root
	}
	cmd.Env = r.childEnv()
	cmd.Stderr = os.Stderr

	// stdin carries the prompt unless the argv already consumed it.
	// Prompt and argv are the two ways a runtime can be given its
	// instructions, and no runtime takes both.
	if !templateUses(template, placeholderPrompt) {
		prompt, err := os.Open(promptPath)
		if err != nil {
			return fmt.Errorf("open handoff prompt: %w", err)
		}
		defer prompt.Close()
		cmd.Stdin = prompt
	}

	var capture *os.File
	if r.agent.Output(r.adapter) == OutputStdout {
		// The stream the operator watches and the capture the run keeps
		// are the same bytes, not two reads of one pipe.
		capture, err = os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("create final message capture: %w", err)
		}
		defer capture.Close()
		// stderr, not stdout. pika's own stdout is a machine-readable
		// channel the moment --json is in play, and a harness streaming
		// its progress there interleaves with the envelope and corrupts
		// it — a JSON parse error at the far end of a run that actually
		// succeeded. A terminal shows either stream alike, so the
		// operator loses nothing and the envelope stays parseable.
		cmd.Stdout = io.MultiWriter(os.Stderr, capture)
	} else {
		cmd.Stdout = os.Stderr
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s handoff: %w", r.adapter.Runtime, err)
	}
	return nil
}

// template is the argv template this runner expands: a custom agent's own
// when it declares one, else whatever the adapter's builder produces for a
// fully-populated spawn.
func (r *ProcessRunner) template() []string {
	if r.agent.Args != nil {
		return r.agent.Args
	}
	if r.adapter.Args == nil {
		return nil
	}
	return r.adapter.Args(probeSpawn)
}

// childEnv builds the child's environment. A declared allowlist passes
// only those names plus the exec essentials; no allowlist at all means the
// child inherits pika's environment exactly, which is how every handoff
// before M6 behaved and the safest default for a runtime pika knows
// nothing about.
func (r *ProcessRunner) childEnv() []string {
	names := r.agent.EnvAllowlist(r.adapter)
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(names)+len(execEssentials))
	env := make([]string, 0, len(names)+len(execEssentials))
	for _, name := range append(append([]string(nil), names...), execEssentials...) {
		if seen[name] {
			continue
		}
		seen[name] = true
		value, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		env = append(env, name+"="+value)
	}
	return env
}
