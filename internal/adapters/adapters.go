// Package adapters is the boundary between pika and the agent harnesses it
// can drive.
//
// An adapter is a table entry, not a plugin: it names a binary, builds one
// argv, and declares how the runtime hands back its final message.
// Every adapter but one delegates the loop to a harness binary. The
// exception is the pika runtime itself: the built-in loop M7 added,
// which runs in-process and is the reason this package imports
// internal/loop alongside the standard library and the contract. The V1
// rule in design §10 — that pika never implements an agent loop — is
// reversed by M7; adapters remains the boundary for harness binaries.
//
// Two rules hold everywhere here and are the point of the package:
//
//   - A control the runtime cannot express is an error, never a silently
//     dropped field. A contract that sets effort on a runtime with no
//     effort control is refused before a process is spawned.
//   - A permission posture is the least dangerous auto-approval the
//     runtime offers. No adapter emits a bypass flag.
package adapters

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/loop"
)

// Runtime names. They mirror the contract's harness enum, and
// TestEveryHarnessInTheContractSchemaHasAnAdapter is what keeps the two
// from drifting.
const (
	RuntimeCodex    = "codex"
	RuntimeClaude   = "claude"
	RuntimeOMP      = "omp"
	RuntimeGemini   = "gemini"
	RuntimeOpenCode = "opencode"
	RuntimeACP      = "acp"
	RuntimeCustom   = "custom"
	RuntimePika     = "pika"
)

// Placeholders a template may reference. They are the complete set: a
// spelling outside it is refused rather than passed through, because a
// template that silently fails to substitute produces an argv that looks
// deliberate and is wrong.
const (
	placeholderRoot   = "{root}"
	placeholderPrompt = "{prompt}"
	placeholderOutput = "{output}"
	placeholderModel  = "{model}"
	placeholderEffort = "{effort}"
)

// placeholderPattern matches one braced token inside an argv element. It
// matches the placeholder inside a larger token too, which is how
// `-c model_reasoning_effort="{effort}"` and `@{prompt}` work: the
// substitution is textual and per-element, never per-token.
var placeholderPattern = regexp.MustCompile(`\{[a-z]+\}`)

// Spawn is everything an adapter needs to build one argv.
type Spawn struct {
	Root       string // repository root, absolute
	PromptPath string // absolute path of the redacted prompt file
	OutputPath string // absolute path the final message must land at
	Model      string // contract model, "" when unset
	Effort     string // contract effort (low|medium|high), "" when unset
}

// OutputMode is how a runtime hands back its final message.
type OutputMode string

const (
	// OutputStdout means the child's stdout is the message, so the runner
	// tees it: the operator still watches the stream and the run still
	// captures what was said.
	OutputStdout OutputMode = "stdout"
	// OutputFile means the harness writes the message to {output} itself.
	OutputFile OutputMode = "file"
)

// Transport is how pika talks to the runtime.
type Transport int

const (
	// TransportProcess is one shot: argv in, message out.
	TransportProcess Transport = iota
	// TransportACP is JSON-RPC 2.0 (ACP v1) over the child's stdio.
	TransportACP
	// TransportLoop is the built-in loop: in-process, no subprocess at all.
	TransportLoop
)

// Support reports which optional contract controls an adapter can express.
// A control it cannot express is an error, never a silently dropped field.
type Support struct{ Model, Effort bool }

// Adapter is one runtime.
type Adapter struct {
	Runtime   string
	Binary    string // default executable, PATH-resolved
	Transport Transport
	Output    OutputMode
	Support   Support
	Resume    bool
	Env       []string // env var NAMES forwarded; nil = inherit pika's environment
	CwdFlag   []string // e.g. {"--cwd"}; nil = set the child process Dir instead
	Help      []string // argv that prints this runtime's usage, for the compat probe
	Args      func(Spawn) []string
}

// builtins is the adapter table, in the order the contract's harness enum
// lists them.
var builtins = []Adapter{
	{
		// Verified against codex-cli 0.151.0. This argv has been in the
		// tree since M2, and `--approve-for-me` is the reason no
		// `--sandbox` may appear beside it: codex 0.151.0 exits 2 on the
		// pair, and every handoff died on argument parsing before an
		// agent read a byte of the prompt. The -c above is what disables
		// the network instead.
		Runtime: RuntimeCodex,
		Binary:  "codex",
		Output:  OutputFile,
		Support: Support{Model: true, Effort: true},
		CwdFlag: []string{"--cd"},
		Help:    []string{"exec", "--help"},
		Args: func(s Spawn) []string {
			args := []string{"exec", "-c", "sandbox_workspace_write.network_access=false"}
			if s.Model != "" {
				args = append(args, "--model", placeholderModel)
			}
			if s.Effort != "" {
				args = append(args, "-c", "model_reasoning_effort=\""+placeholderEffort+"\"")
			}
			return append(args, "--approve-for-me", "--cd", placeholderRoot, "--output-last-message", placeholderOutput, "-")
		},
	},
	{
		// Claude Code 2.1.220, `claude --help`: -p prints the response
		// and exits, which is the non-interactive form, and
		// acceptEdits auto-approves edits while leaving Bash governed by
		// the harness's own policy.
		Runtime: RuntimeClaude,
		Binary:  "claude",
		Output:  OutputStdout,
		Support: Support{Model: true, Effort: true},
		Help:    []string{"--help"},
		Args: func(s Spawn) []string {
			args := []string{"-p", "--output-format", "text", "--permission-mode", "acceptEdits"}
			if s.Model != "" {
				args = append(args, "--model", placeholderModel)
			}
			if s.Effort != "" {
				args = append(args, "--effort", placeholderEffort)
			}
			return args
		},
	},
	{
		// omp 18.0.11, `omp --help`. --no-session keeps a one-shot
		// handoff from leaving a session behind, and `@{prompt}` is how
		// omp takes a prompt file rather than stdin.
		Runtime: RuntimeOMP,
		Binary:  "omp",
		Output:  OutputStdout,
		Support: Support{Model: true, Effort: true},
		CwdFlag: []string{"--cwd"},
		Help:    []string{"--help"},
		Args: func(s Spawn) []string {
			args := []string{"-p", "--cwd", placeholderRoot, "--mode", "text", "--approval-mode", "write", "--no-session"}
			if s.Model != "" {
				args = append(args, "--model", placeholderModel)
			}
			if s.Effort != "" {
				args = append(args, "--thinking", placeholderEffort)
			}
			return append(args, "@"+placeholderPrompt)
		},
	},
	{
		// gemini-cli's documented CLI reference; not installed on the
		// machine this was written on, so the compatibility probe — not
		// a local run — is what enforces these flags. --skip-trust is
		// required because the trust prompt otherwise blocks a
		// non-interactive run.
		Runtime: RuntimeGemini,
		Binary:  "gemini",
		Output:  OutputStdout,
		Support: Support{Model: true},
		Help:    []string{"--help"},
		Args: func(s Spawn) []string {
			args := []string{"-p", "--output-format", "text", "--approval-mode", "auto_edit", "--skip-trust"}
			if s.Model != "" {
				args = append(args, "--model", placeholderModel)
			}
			return args
		},
	},
	{
		// opencode.ai/docs/cli; not installed on the machine this was
		// written on. --auto is the only auto-approval its CLI
		// documents: it auto-approves permissions not explicitly denied
		// and offers no narrower form.
		Runtime: RuntimeOpenCode,
		Binary:  "opencode",
		Output:  OutputStdout,
		Support: Support{Model: true},
		CwdFlag: []string{"--dir"},
		Help:    []string{"--help"},
		Args: func(s Spawn) []string {
			args := []string{"run", "--format", "default", "--auto", "--dir", placeholderRoot}
			if s.Model != "" {
				args = append(args, "--model", placeholderModel)
			}
			return append(args, "--file", placeholderPrompt)
		},
	},
	{
		// ACP v1 over the child's stdio. The binary is omp's `acp`
		// subcommand by default and overridable through
		// agent.command, because ACP is a protocol and not a vendor.
		Runtime:   RuntimeACP,
		Binary:    "omp",
		Transport: TransportACP,
		Output:    OutputStdout,
		Resume:    true,
		Help:      []string{"acp", "--help"},
		Args:      func(Spawn) []string { return []string{"acp"} },
	},
	{
		// custom carries whatever posture the operator's argv states;
		// pika injects none, because it cannot know what the command
		// is. Support is derived from the template rather than
		// declared: a template that references {model} has a model
		// control and one that does not does not.
		Runtime: RuntimeCustom,
		// A custom agent with no args template is read from stdout,
		// which is the only channel pika can assume of a command it did
		// not write. Agent.Output overrides this from the template when
		// the template names {output}.
		Output: OutputStdout,
	},
	{
		// The built-in loop. It is the only runtime with no binary, no
		// argv and no --help: it runs in-process, writes its own final
		// message to {output}, and takes model and effort as provider
		// controls. provider is required and selects the client.
		Runtime:   RuntimePika,
		Transport: TransportLoop,
		Output:    OutputFile,
		Support:   Support{Model: true, Effort: true},
	},
}

// Lookup returns the adapter for a runtime name.
func Lookup(runtime string) (Adapter, bool) {
	for _, a := range builtins {
		if a.Runtime == runtime {
			return a, true
		}
	}
	return Adapter{}, false
}

// All returns every adapter, in table order.
func All() []Adapter { return append([]Adapter(nil), builtins...) }

// probeSpawn is the Spawn Flags calls Args with: every optional control
// populated, so the flags a runtime could send are the flags a
// compatibility probe checks. Probing with an empty model and effort would
// silently stop checking --model the day an adapter stopped emitting it
// unconditionally.
var probeSpawn = Spawn{
	Root:       placeholderRoot,
	PromptPath: placeholderPrompt,
	OutputPath: placeholderOutput,
	Model:      placeholderModel,
	Effort:     placeholderEffort,
}

// Flags returns the flag tokens this adapter constructs, deduplicated and
// in argv order. Values are excluded and the bare "-" stdin sentinel is
// dropped.
//
// It is derived from a real Args call rather than a second, hand-maintained
// list, so a flag added to an adapter tomorrow is covered by the
// compatibility probe without anyone remembering to update a parallel
// copy. That is the same property the projection digest exists to enforce
// in the skill layer, for the same reason.
func (a Adapter) Flags(s Spawn) []string {
	if a.Args == nil {
		return nil
	}
	var flags []string
	seen := make(map[string]bool)
	for _, arg := range a.Args(s) {
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			continue
		}
		if seen[arg] {
			continue
		}
		seen[arg] = true
		flags = append(flags, arg)
	}
	return flags
}

// ProbeFlags is the flag list the compatibility probe checks.
func (a Adapter) ProbeFlags() []string { return a.Flags(probeSpawn) }

// Agent is one contract agent entry plus what it resolved to.
type Agent struct {
	Name    string // contract key, e.g. "builder"
	Runtime string
	Command string   // agent.command override; "" = the adapter's binary
	Args    []string // agent.args override template; nil = the adapter's Args
	Env     []string // agent.env allowlist; nil = inherit
	Model   string
	Effort  string
	// contract provider, "" when unset
	Provider string
}

// NotConfiguredError reports a contract that declares no agent by this
// name.
//
// It is a type and not a string because two callers need to tell it apart
// from other failures: a missing builder is fatal, while a missing
// explorer or reviewer means the phase is skipped. It carries the
// contract path so the message names the file an operator has to edit.
type NotConfiguredError struct {
	Name         string
	ContractPath string
}

func (e *NotConfiguredError) Error() string {
	return fmt.Sprintf("agent %q is not configured in %s", e.Name, e.ContractPath)
}

// AgentFromContract resolves one contract agent.
//
// contractPath is a parameter only so the refusal can name the file an
// operator has to edit: an agent that is not configured is missing from a
// document, and "agent %q is not configured" without saying which document
// sends an operator looking.
func AgentFromContract(c *contract.Contract, contractPath, name string) (Agent, error) {
	if c == nil {
		return Agent{}, fmt.Errorf("agent %q is not configured: no contract", name)
	}
	cfg, ok := c.Agents[name]
	if !ok {
		return Agent{}, &NotConfiguredError{Name: name, ContractPath: contractPath}
	}
	return Agent{
		Name:     name,
		Runtime:  cfg.Runtime,
		Command:  cfg.Command,
		Args:     cfg.Args,
		Env:      cfg.Env,
		Model:    cfg.Model,
		Effort:   cfg.Effort,
		Provider: cfg.Provider,
	}, nil
}

// Binary is the executable this agent resolves to: the contract override
// when there is one, else the adapter's own.
func (a Agent) Binary(ad Adapter) string {
	if strings.TrimSpace(a.Command) != "" {
		return strings.TrimSpace(a.Command)
	}
	return ad.Binary
}

// Support reports the controls this agent's argv can express. Every
// runtime but custom declares its own; custom expresses exactly the
// controls its template references.
func (a Agent) Support(ad Adapter) Support {
	if ad.Runtime != RuntimeCustom {
		return ad.Support
	}
	return Support{
		Model:  templateUses(a.Args, placeholderModel),
		Effort: templateUses(a.Args, placeholderEffort),
	}
}

// Output reports how this agent hands back its final message. A custom
// template that names {output} writes it itself; one that does not is read
// from stdout.
func (a Agent) Output(ad Adapter) OutputMode {
	if a.Args == nil {
		return ad.Output
	}
	if templateUses(a.Args, placeholderOutput) {
		return OutputFile
	}
	return OutputStdout
}

// EnvAllowlist is the allowlist this agent forwards: the contract's when it
// declares one, else the adapter's. Nil means the child inherits pika's
// environment exactly.
func (a Agent) EnvAllowlist(ad Adapter) []string {
	if len(a.Env) > 0 {
		return a.Env
	}
	return ad.Env
}

// New builds the runner for a resolved agent, or an error naming the one
// thing that stops it from running.
func New(a Agent) (Runner, error) {
	ad, ok := Lookup(a.Runtime)
	if !ok {
		return nil, fmt.Errorf("agent %q uses runtime %q; no adapter implements it", a.Name, a.Runtime)
	}
	if ad.Runtime == RuntimeCustom && strings.TrimSpace(a.Command) == "" {
		return nil, fmt.Errorf("agent %q declares runtime custom with no command", a.Name)
	}
	support := a.Support(ad)
	if a.Model != "" && !support.Model {
		return nil, fmt.Errorf("agent %q sets model %q; runtime %q has no model control", a.Name, a.Model, a.Runtime)
	}
	if a.Effort != "" && !support.Effort {
		return nil, fmt.Errorf("agent %q sets effort %q; runtime %q has no effort control", a.Name, a.Effort, a.Runtime)
	}
	if err := declaredEnv(a); err != nil {
		return nil, err
	}
	if ad.Transport == TransportLoop {
		if a.Command != "" {
			return nil, fmt.Errorf("agent %q declares command on runtime pika; the loop has no binary", a.Name)
		}
		if len(a.Args) > 0 {
			return nil, fmt.Errorf("agent %q declares args on runtime pika; the loop has no argv", a.Name)
		}
		if len(a.Env) > 0 {
			return nil, fmt.Errorf("agent %q declares env on runtime pika; the loop reads the provider's canonical key var instead", a.Name)
		}
		return loop.NewRunner(a.Name, a.Provider, a.Model, a.Effort)
	}
	if ad.Transport == TransportACP {
		return &ACPRunner{agent: a, adapter: ad}, nil
	}
	return &ProcessRunner{agent: a, adapter: ad}, nil
}

// declaredEnv refuses an env allowlist naming a variable pika's own
// environment does not set. A reference to nothing is a reference, not an
// empty string: passing NAME= through would hand the child a variable that
// exists and is empty, which is a different thing from the variable the
// operator meant and is the kind of difference a harness reports as a
// confusing authentication failure rather than a missing name.
func declaredEnv(a Agent) error {
	for _, name := range a.Env {
		if _, ok := os.LookupEnv(name); !ok {
			return fmt.Errorf("agent %q declares env %q, which is not set in pika's environment", a.Name, name)
		}
	}
	return nil
}

// expand substitutes the placeholder set into one argv element. A braced
// token outside that set is refused: a template that silently failed to
// substitute would produce an argv that looks deliberate and is wrong, and
// the failure would surface as a harness behaving strangely rather than as
// a typo.
func (a Agent) expand(element string, s Spawn) (string, error) {
	var unknown string
	out := placeholderPattern.ReplaceAllStringFunc(element, func(match string) string {
		switch match {
		case placeholderRoot:
			return s.Root
		case placeholderPrompt:
			return s.PromptPath
		case placeholderOutput:
			return s.OutputPath
		case placeholderModel:
			return s.Model
		case placeholderEffort:
			return s.Effort
		default:
			if unknown == "" {
				unknown = match
			}
			return match
		}
	})
	if unknown != "" {
		return "", fmt.Errorf("agent %q builds an unknown placeholder %q", a.Name, unknown)
	}
	return out, nil
}

// argv builds this agent's complete argv.
func (a Agent) argv(ad Adapter, s Spawn) ([]string, error) {
	template := a.Args
	if template == nil && ad.Args != nil {
		template = ad.Args(s)
	}
	// A custom agent that declares a command and no template runs that
	// command with no arguments, taking its prompt on stdin. Refusing it
	// would make `command: /path/to/harness` unusable on its own.
	out := make([]string, 0, len(template))
	for _, element := range template {
		expanded, err := a.expand(element, s)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded)
	}
	return out, nil
}

// templateUses reports whether any element of a template references a
// placeholder.
func templateUses(template []string, placeholder string) bool {
	for _, element := range template {
		if strings.Contains(element, placeholder) {
			return true
		}
	}
	return false
}
