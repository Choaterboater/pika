// Command pika is the root entrypoint for the pika CLI.
package main

import (
	"fmt"
	"io"
	"os"

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
