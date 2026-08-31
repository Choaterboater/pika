package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Choaterboater/pika/internal/evidence"
	"github.com/Choaterboater/pika/internal/improve"
	"github.com/Choaterboater/pika/internal/verify"
)

// resumeUsage is the one-line synopsis printed beside a usage error. It
// is a human hint, not part of the machine payload: with --json the error
// envelope's code and message are the whole answer.
const resumeUsage = "usage: pika resume <work-id> [--agent <name>] [--json] [--root <dir>]"

// runResume implements `pika resume <work-id> [--agent <name>] [--json]
// [--root <dir>]`: it rejoins the run the id names and carries it to a
// terminal outcome, or refuses with the specific reason it cannot.
//
// There is deliberately no --branch. The run's own record names the
// branch its work is on, and a flag that is silently ignored whenever the
// record has one is a flag that will eventually be believed. A run
// interrupted before it ever branched resumes onto the same default
// `pika improve` would have used.
//
// Exit codes: 0 when the run reaches a complete outcome, 1 when the
// resumed run itself fails, and 2 for a usage error or for a repository
// state resume refuses to rejoin. The three refusals are exit 2 rather
// than 1 because nothing was attempted: they are states of the
// repository, the same species of answer as an unknown work id, not work
// that ran and failed.
func runResume(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agent := fs.String("agent", "builder", "contract agent name")
	jsonOut := fs.Bool("json", false, "emit the resumed run as JSON on stdout")
	rootFlag := fs.String("root", "", rootFlagUsage)
	// stdlib flag stops at the first non-flag argument, so the work id is
	// consumed between two parses the way status consumes its optional
	// one. Without this `pika resume <id> --json` — how anyone would
	// actually type it — would leave --json unparsed.
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return usageError(*jsonOut, stdout, stderr, "a work id is required; `pika status` lists the runs this repository has")
	}
	workID := rest[0]
	if err := fs.Parse(rest[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return usageError(*jsonOut, stdout, stderr, fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	if err := evidence.ValidateWorkID(workID); err != nil {
		// A malformed id is a wrong invocation, not a repository state
		// pika cannot work in, so it is a usage error rather than a
		// configuration one.
		return usageError(*jsonOut, stdout, stderr, err.Error())
	}

	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "resume", codeConfig, err.Error())
	}
	cfg, err := configuredRoles(root, *agent)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "resume", codeConfig, err.Error())
	}
	cfg.Branch = defaultImproveBranch
	cfg.Check = func() (*verify.Report, error) { return currentCheckReport(root) }
	result, err := improve.Resume(context.Background(), root.Dir(), workID, cfg)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fail(*jsonOut, stdout, stderr, "resume", codeConfig,
			fmt.Sprintf("no run %q in %s", workID, root.Dir()))
	case errors.Is(err, improve.ErrRunFinished),
		errors.Is(err, improve.ErrBranchGone),
		errors.Is(err, improve.ErrTreeDiverged):
		return fail(*jsonOut, stdout, stderr, "resume", codeConfig, err.Error())
	}

	if *jsonOut {
		// The result is the payload on both paths: a resumed run that
		// stopped still has to say which branch it stopped on and where
		// its handoff bundle is.
		if err != nil {
			if !emitFailure(stdout, stderr, "resume", err, result) {
				fmt.Fprintln(stderr, "pika resume:", err)
			}
			return 1
		}
		if !emitJSON(stdout, stderr, "resume", true, result) {
			return 1
		}
		return 0
	}
	printResumeResult(stdout, result, err)
	if err != nil {
		fmt.Fprintln(stderr, "pika resume:", err)
		return 1
	}
	return 0
}

// usageError reports a wrong invocation of resume and adds the synopsis
// for a human. With --json the envelope is the whole answer, so the
// synopsis is not printed and stderr stays empty.
func usageError(jsonOut bool, stdout, stderr io.Writer, message string) int {
	code := fail(jsonOut, stdout, stderr, "resume", codeUsage, message)
	if !jsonOut {
		fmt.Fprintln(stderr, resumeUsage)
	}
	return code
}

// printResumeResult writes the human-readable outcome of one resumed run.
// Every branch names the run, the way improve's and handoff's do: the
// work id is the operator's handle on what just happened.
func printResumeResult(stdout io.Writer, result improve.Result, err error) {
	switch {
	case err != nil:
		// Same contract as improve's stopped report, for the same
		// reason: the branch is the one Git was actually on, and the
		// bundle line is omitted rather than printed empty.
		fmt.Fprintf(stdout, "resume: run %s stopped on branch %s; no commit created\n",
			result.WorkID, stoppedBranch(result))
		if result.Handoff.Dir != "" {
			fmt.Fprintf(stdout, "handoff: %s\n", result.Handoff.Dir)
		}
	case result.Commit != "" && len(result.ChangedFiles) == 0:
		// A resume that commits always knows what it committed, so a
		// commit with no changed files is the reconciled deliver: Git
		// already held the work, and resume recorded the outcome without
		// re-running anything.
		fmt.Fprintf(stdout, "resume: run %s was already delivered as %s on %s; the record now says so and nothing was re-run\n",
			result.WorkID, result.Commit, result.Branch)
	case result.Commit != "":
		fmt.Fprintf(stdout, "resume: verified fixes committed on %s; run %s\ncommit: %s\nchanged: %v\n",
			result.Branch, result.WorkID, result.Commit, result.ChangedFiles)
	default:
		fmt.Fprintf(stdout, "resume: run %s completed with nothing left to commit\n", result.WorkID)
	}
}
