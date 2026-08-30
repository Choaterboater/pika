// Command pika is the root entrypoint for the pika CLI.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Choaterboater/pika/internal/cliout"
	"github.com/Choaterboater/pika/internal/version"
)

// runFunc is the one signature every command implements. stdin is passed
// to all commands so the table stays uniform; only mcp reads it.
type runFunc func(args []string, stdin io.Reader, stdout, stderr io.Writer) int

// command is one registered subcommand. summary and usage are rendered by
// `pika help`, so the help text cannot drift from the registered set.
type command struct {
	name    string
	summary string
	usage   string
	run     runFunc
}

// The error codes an exit-2 --json envelope carries. Two, not twenty:
// "usage" means the invocation was wrong, "config" means the repository
// state prevents the command from running. A consumer that needs more
// than that distinction reads the message.
const (
	codeUsage  = "usage"
	codeConfig = "config"
)

// emitJSON writes one command's --json payload through cliout and reports
// whether it landed. An encoding or writer failure is the command's own
// failure: a consumer that received no payload must never be told the
// command succeeded.
func emitJSON(stdout, stderr io.Writer, name string, ok bool, result any) bool {
	if err := cliout.Write(stdout, name, ok, result); err != nil {
		fmt.Fprintf(stderr, "pika %s: %v\n", name, err)
		return false
	}
	return true
}

// failureResult is the --json result of a command that ran and failed
// (exit 1). ok:false already says it failed; this says why, and carries
// the command's own report when it produced one — a failed apply still
// has to tell a consumer what was applied and whether it rolled back.
// Commands whose report is itself the verdict (check, doctor) report the
// bare report instead: their failure is a normal outcome, not a stop.
type failureResult struct {
	Error  string `json:"error"`
	Report any    `json:"report,omitempty"`
}

// emitFailure writes the ok:false envelope for a run that stopped, and
// reports whether it landed so the caller can fall back to its own
// plain-text diagnostic.
func emitFailure(stdout, stderr io.Writer, name string, err error, report any) bool {
	return emitJSON(stdout, stderr, name, false, failureResult{Error: err.Error(), Report: report})
}

// fail reports a usage or configuration error and returns its exit code,
// which is always 2. With --json the error envelope on stdout is the
// whole answer and stderr stays empty; without it the operator gets the
// plain line they have always gotten. Every command routes its exit-2
// paths through here so an agent never has to parse prose to learn that
// its invocation was wrong.
func fail(jsonOut bool, stdout, stderr io.Writer, name, code, message string) int {
	if jsonOut {
		if err := cliout.WriteError(stdout, name, code, message); err == nil {
			return 2
		}
		// The envelope did not land; the reason still has to reach
		// someone, so fall through to stderr.
	}
	fmt.Fprintf(stderr, "pika %s: %s\n", name, message)
	return 2
}

// commands is the registry. Adding a command here is the only step needed
// to make it dispatchable and documented.
var commands = []command{
	{
		name:    "init",
		summary: "create a contract and scaffold for a new repository",
		usage:   "pika init [--profile <lang>]... [--name <name>] [--module <path>] [--force] [--json] [--root <dir>]",
		run:     runInit,
	},
	{
		name:    "adopt",
		summary: "inventory an existing repository and draft a contract",
		usage:   "pika adopt [--json] [--root <dir>]",
		run:     runAdopt,
	},
	{
		name:    "apply",
		summary: "promote adoption drafts into a live contract transactionally",
		usage:   "pika apply [--json] [--root <dir>]",
		run:     runApply,
	},
	{
		name:    "check",
		summary: "run the verification ladder",
		usage:   "pika check [--all|--changed|--ci] [--json] [--contract <path>] [--root <dir>]",
		run:     runCheck,
	},
	{
		name:    "doctor",
		summary: "diagnose contract, lock, envelope, gates, and toolchain",
		usage:   "pika doctor [--json] [--root <dir>]",
		run:     runDoctor,
	},
	{
		name:    "explain",
		summary: "explain a naming rule, gate, or error code",
		usage:   "pika explain <rule-id> [--json] [--root <dir>]",
		run:     runExplain,
	},
	{
		name:    "authorize",
		summary: "generate the capability envelope agents need",
		usage:   "pika authorize [--scope read|project|repo] [--exec \"<argv>\"]... [--network <host>]... [--credential <name>]... [--github <scope>]... [--force] [--json] [--root <dir>]",
		run:     runAuthorize,
	},
	{
		name:    "mcp",
		summary: "serve the kernel to agents over stdio JSON-RPC",
		usage:   "pika mcp [--root <dir>]",
		run:     runMCP,
	},
	{
		name:    "handoff",
		summary: "hand failed check gates to the configured builder agent",
		usage:   "pika handoff [--agent <name>] [--json] [--root <dir>]",
		run:     runHandoff,
	},
	{
		name:    "improve",
		summary: "run check, repair failures via the builder agent, and commit on a verified recheck",
		usage:   "pika improve [--branch <name>] [--agent <name>] [--json] [--root <dir>]",
		run:     runImprove,
	},
}

// help is registered here rather than in the literal above because
// commands -> runHelp -> writeOverview -> commands is an initialization
// cycle the compiler rejects. Registering it in init keeps help last in
// the rendered table without the cycle.
func init() {
	commands = append(commands, command{
		name:    "help",
		summary: "describe pika or one command",
		usage:   "pika help [<command>]",
		run:     runHelp,
	})
}

func lookup(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// dispatch routes one invocation. --version is honored only in the first
// argument position: scanning every argument (the pre-M1.5 behavior) made
// `pika check --version` print the version, and would have broken any
// command taking a free-form string.
func dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeOverview(stdout)
		return 0
	}
	switch args[0] {
	case "--version", "-version", "version":
		fmt.Fprintln(stdout, version.String())
		return 0
	}
	c, ok := lookup(args[0])
	if !ok {
		fmt.Fprintf(stderr, "pika: unknown command %q\n\n", args[0])
		writeOverview(stderr)
		return 2
	}
	return c.run(args[1:], stdin, stdout, stderr)
}

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
