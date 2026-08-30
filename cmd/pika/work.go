package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Choaterboater/pika/internal/improve"
	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/workrec"
)

// workUsage is the one-line synopsis printed beside a usage error. It is
// a human hint, not part of the machine payload: with --json the error
// envelope's code and message are the whole answer.
const workUsage = "usage: pika work \"<goal>\" [--branch <name>] [--agent <name>] [--json] [--root <dir>]"

// runWork implements `pika work "<goal>" [--branch <name>] [--agent
// <name>] [--json] [--root <dir>]`: it runs a stated goal through the
// same durable lifecycle `pika improve` runs, as feature work.
//
// The two commands differ in exactly one thing, and it is the one the
// lifecycle already knows about: repair work is described entirely by
// the gates that failed, so a green ladder means it is already done,
// while a goal is work the ladder cannot describe and a green ladder
// says nothing about whether it has been met. `pika work` therefore
// always goes on to the agent, and its goal is mandatory.
//
// It shares --branch with improve rather than defaulting to a second
// branch name. `pika resume` puts a run interrupted before it ever
// branched onto improve's default, so a private default here would mean
// a resumed feature run silently landing somewhere its own command
// would never have chosen.
//
// Exit codes: 0 when the run reaches a verified commit, 1 when the run
// itself fails, 2 for a usage error or a repository state the run
// refuses to start in.
func runWork(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("work", flag.ContinueOnError)
	fs.SetOutput(stderr)
	branch := fs.String("branch", defaultImproveBranch, "local branch for the verified commit")
	agent := fs.String("agent", "builder", "contract agent name (must use the Codex runtime)")
	jsonOut := fs.Bool("json", false, "emit the work result as JSON on stdout")
	rootFlag := fs.String("root", "", rootFlagUsage)
	// stdlib flag stops at the first non-flag argument, so the goal is
	// consumed between two parses the way explain, status and resume
	// consume theirs. Without this `pika work "<goal>" --json` — how
	// anyone would actually type it — would leave --json unparsed.
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return workUsageError(*jsonOut, stdout, stderr, "a goal is required: `pika work \"<what you want done>\"`")
	}
	goal := rest[0]
	if err := fs.Parse(rest[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		// Almost always an unquoted goal. Taking the first word and
		// running with it would spawn an agent against a goal nobody
		// wrote, so the whole invocation is refused instead.
		return workUsageError(*jsonOut, stdout, stderr,
			fmt.Sprintf("unexpected argument %q; the goal is one quoted string", fs.Arg(0)))
	}
	if strings.TrimSpace(goal) == "" {
		// `pika work "$GOAL"` with GOAL unset is an empty positional,
		// not a missing one, so it has to be refused here as well as
		// above; improve.Run refuses it too, but only after resolving
		// the root, and a wrong invocation is not a repository state.
		return workUsageError(*jsonOut, stdout, stderr, "the goal is empty; state the work in one quoted string")
	}

	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "work", codeConfig, err.Error())
	}
	result, err := improve.Run(context.Background(), improve.Config{
		Root:    root.Dir(),
		Branch:  *branch,
		Kind:    workrec.KindFeature,
		Goal:    goal,
		Agent:   *agent,
		Runtime: codexRuntime,
		Check:   func() (*verify.Report, error) { return currentCheckReport(root) },
		Runner:  configuredRunner{root: root, agent: *agent},
	})
	if *jsonOut {
		// The result is the payload on both paths: a run that stopped
		// still has to say which branch it stopped on and where the
		// handoff bundle is.
		if err != nil {
			if !emitFailure(stdout, stderr, "work", err, result) {
				fmt.Fprintln(stderr, "pika work:", err)
			}
			return 1
		}
		if !emitJSON(stdout, stderr, "work", true, result) {
			return 1
		}
		return 0
	}
	printRunResult(stdout, "work", result, err)
	if err != nil {
		fmt.Fprintln(stderr, "pika work:", err)
		return 1
	}
	return 0
}

// workUsageError reports a wrong invocation of work and adds the
// synopsis for a human. With --json the envelope is the whole answer, so
// the synopsis is not printed and stderr stays empty.
func workUsageError(jsonOut bool, stdout, stderr io.Writer, message string) int {
	code := fail(jsonOut, stdout, stderr, "work", codeUsage, message)
	if !jsonOut {
		fmt.Fprintln(stderr, workUsage)
	}
	return code
}
