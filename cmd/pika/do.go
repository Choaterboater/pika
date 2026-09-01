package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// doUsage is the one-line synopsis printed beside a usage error, matching
// workUsage's role at cmd/pika/work.go:18.
const doUsage = `usage: pika do ["<goal>"] [--branch <name>] [--agent <name>] [--json] [--root <dir>]`

// runDo implements `pika do ["<goal>"] [--branch <name>] [--agent <name>]
// [--json] [--root <dir>]`: it dispatches to the correct existing
// command for the repository's current state instead of requiring the
// operator to already know which one applies (design spec
// docs/superpowers/specs/2026-09-01-pika-do-routing-design.md).
//
// Exit codes: 2 for a usage error; every other code is whichever of
// adopt/improve/work got dispatched to, returned verbatim.
func runDo(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("do", flag.ContinueOnError)
	fs.SetOutput(stderr)
	branch := fs.String("branch", defaultImproveBranch, "local branch for the verified commit")
	agent := fs.String("agent", "builder", "contract agent name")
	jsonOut := fs.Bool("json", false, "emit the dispatched command's result as JSON")
	rootFlag := fs.String("root", "", rootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// stdlib flag stops at the first non-flag argument, so the goal is
	// consumed between two parses the way work, explain, status and
	// resume all consume theirs (cmd/pika/work.go:47-61).
	rest := fs.Args()
	var goal string
	if len(rest) > 0 {
		goal = rest[0]
		if err := fs.Parse(rest[1:]); err != nil {
			return 2
		}
		if fs.NArg() != 0 {
			return doUsageError(*jsonOut, stdout, stderr,
				fmt.Sprintf("unexpected argument %q; the goal is one quoted string", fs.Arg(0)))
		}
		if strings.TrimSpace(goal) == "" {
			return doUsageError(*jsonOut, stdout, stderr, "the goal is empty; state the work in one quoted string")
		}
	}
	_, _ = branch, agent // wired in Task 4
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "do", codeConfig, err.Error())
	}
	_, contractErr := os.Stat(root.Contract())
	_, draftErr := os.Stat(root.ContractDraft())
	contractExists := contractErr == nil
	draftExists := draftErr == nil

	switch {
	case contractExists:
		// wired in Task 4
	case draftExists:
		// wired in Task 3
	default:
		fmt.Fprintln(stderr, "routing: no live contract or draft, dispatching to adopt")
		return dispatchTo("adopt", passthroughArgs(*jsonOut, *rootFlag), stdin, stdout, stderr)
	}
	return 0
}

// doUsageError reports a wrong invocation of do and adds the synopsis
// for a human. With --json the envelope is the whole answer, so the
// synopsis is not printed and stderr stays empty — matching
// workUsageError at cmd/pika/work.go:114-123.
func doUsageError(jsonOut bool, stdout, stderr io.Writer, message string) int {
	code := fail(jsonOut, stdout, stderr, "do", codeUsage, message)
	if !jsonOut {
		fmt.Fprintln(stderr, doUsage)
	}
	return code
}

// dispatchTo runs a registered command's own handler directly — the same
// call main.go's top-level dispatch makes (cmd/pika/main.go:216-231).
// do never re-implements adopt/improve/work's own logic; it only
// decides which one to call and with what argv.
func dispatchTo(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c, ok := lookup(name)
	if !ok {
		// Unreachable outside a typo in this file: name is always one of
		// the three literal command names below.
		fmt.Fprintf(stderr, "pika do: internal error: no such command %q\n", name)
		return 1
	}
	return c.run(args, stdin, stdout, stderr)
}

// passthroughArgs builds adopt's argv: adopt takes --json and --root
// only (cmd/pika/adopt.go:20-27), never --branch/--agent.
func passthroughArgs(jsonOut bool, rootVal string) []string {
	var out []string
	if jsonOut {
		out = append(out, "--json")
	}
	if rootVal != "" {
		out = append(out, "--root", rootVal)
	}
	return out
}
