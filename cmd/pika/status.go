package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Choaterboater/pika/internal/evidence"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/workrec"
)

// statusUsage is the one-line synopsis printed beside a usage error. It
// is a human hint, not part of the machine payload: with --json the
// error envelope's code and message are the whole answer.
const statusUsage = "usage: pika status [<work-id>] [--json] [--root <dir>]"

// unsettledOutcome is what text mode calls a record carrying no terminal
// outcome.
//
// Such a record is either a run still in flight or a run whose terminal
// save never landed, and those two are bit-for-bit identical on disk:
// nothing status can read tells them apart. Printing "running" would be
// status asserting a fact it does not have, in the one command whose
// entire job is to report what the record says. The question mark is the
// honest answer, and it is the answer an operator can act on — both
// readings end in "go look at the branch".
//
// --json says the same thing by saying nothing: workrec.Record omits an
// empty outcome, so a consumer sees the field's absence rather than a
// verdict pika invented.
const unsettledOutcome = "in-flight?"

// runStatus implements `pika status [<work-id>] [--json] [--root <dir>]`:
// a read-only view of the durable run records under
// .project/state/work/. Bare, it lists every run newest first; given a
// work id, it reports that one run in full. It mutates nothing and runs
// no gate.
//
// Exit codes: 0 always on a readable repository — including one that has
// never run `pika improve`, which is a valid state and not a failure —
// and 2 on a usage error, an unknown work id, or a record too damaged to
// read.
func runStatus(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit the listing or the run as JSON on stdout")
	rootFlag := fs.String("root", "", rootFlagUsage)
	// stdlib flag stops at the first non-flag argument, so the optional
	// work id is consumed between two parses the way explain consumes
	// its rule id. Without this `pika status <id> --json` — how anyone
	// would actually type it — would leave --json unparsed.
	if err := fs.Parse(args); err != nil {
		return 2
	}
	workID := ""
	if rest := fs.Args(); len(rest) > 0 {
		workID = rest[0]
		if err := fs.Parse(rest[1:]); err != nil {
			return 2
		}
		if fs.NArg() != 0 {
			code := fail(*jsonOut, stdout, stderr, "status", codeUsage,
				fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
			if !*jsonOut {
				fmt.Fprintln(stderr, statusUsage)
			}
			return code
		}
	}

	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "status", codeConfig, err.Error())
	}
	if workID != "" {
		return statusRun(root, workID, *jsonOut, stdout, stderr)
	}
	return statusList(root, *jsonOut, stdout, stderr)
}

// statusList reports every run, newest first.
//
// Two of its exits are load-bearing and deliberately unalike.
//
// A repository with no runs at all exits 0 with an empty listing: never
// having run `pika improve` is a valid state, not a failure, the same
// way doctor reports an unadopted repository rather than refusing.
//
// A single corrupt record, by contrast, fails the whole listing.
// workrec.List reports damage instead of skipping past it — report,
// never repair — and status does not soften that: filtering the bad
// record out would print a listing that looked complete while hiding
// the corruption, which is the opposite of what the record's design
// chose. The error names the offending file, which is where the
// operator's next move is.
func statusList(root *repopath.Root, jsonOut bool, stdout, stderr io.Writer) int {
	runs, err := workrec.List(root)
	if err != nil {
		return fail(jsonOut, stdout, stderr, "status", codeConfig, err.Error())
	}
	// The listing drops each record's baseline and recheck reports:
	// twenty runs would otherwise be twenty full ladder reports. They
	// are what `pika status <work-id>` is for. Everything else stays the
	// record's own shape and field names, so a listing row and the
	// record.json it came from read the same.
	rows := make([]workrec.Record, 0, len(runs))
	for _, rec := range runs {
		rec.Baseline = nil
		rec.Recheck = nil
		rows = append(rows, rec)
	}
	if jsonOut {
		if !emitJSON(stdout, stderr, "status", true, map[string]any{"runs": rows}) {
			return 1
		}
		return 0
	}
	printRunList(stdout, root, rows)
	return 0
}

// statusRun reports one run in full, reports included.
func statusRun(root *repopath.Root, workID string, jsonOut bool, stdout, stderr io.Writer) int {
	if err := evidence.ValidateWorkID(workID); err != nil {
		// A malformed id is a wrong invocation, not a repository state
		// pika cannot work in, so it is a usage error rather than a
		// configuration one.
		code := fail(jsonOut, stdout, stderr, "status", codeUsage, err.Error())
		if !jsonOut {
			fmt.Fprintln(stderr, statusUsage)
		}
		return code
	}
	handle, err := workrec.Open(root, workID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fail(jsonOut, stdout, stderr, "status", codeConfig,
				fmt.Sprintf("no run %q in %s", workID, root.Dir()))
		}
		// The run directory exists and its record does not read. That
		// is damage, and workrec's error already names the file.
		return fail(jsonOut, stdout, stderr, "status", codeConfig, err.Error())
	}
	rec := handle.Record()
	if jsonOut {
		if !emitJSON(stdout, stderr, "status", true, map[string]any{"run": rec}) {
			return 1
		}
		return 0
	}
	printRunDetail(stdout, rec)
	return 0
}

// printRunList writes one row per run, newest first.
func printRunList(stdout io.Writer, root *repopath.Root, runs []workrec.Record) {
	if len(runs) == 0 {
		// Naming the root answers the question an empty listing
		// immediately raises: which repository did it look in.
		fmt.Fprintf(stdout, "status: no runs recorded in %s\n", root.Dir())
		return
	}
	fmt.Fprintf(stdout, "%-30s %-8s %-9s %-11s %s\n", "run", "kind", "phase", "outcome", "branch")
	for _, rec := range runs {
		fmt.Fprintf(stdout, "%-30s %-8s %-9s %-11s %s\n",
			rec.WorkID, orDash(rec.Kind), orDash(rec.Phase), displayOutcome(rec), orDash(rec.Branch))
	}
}

// printRunDetail writes one run: its labeled fields, then the phase
// history that is the run's actual chronology.
func printRunDetail(stdout io.Writer, rec workrec.Record) {
	printRunField(stdout, "run", rec.WorkID)
	printRunField(stdout, "kind", rec.Kind)
	printRunField(stdout, "goal", rec.Goal)
	printRunField(stdout, "phase", rec.Phase)
	printRunField(stdout, "outcome", displayOutcome(rec))
	printRunField(stdout, "reason", rec.Reason)
	printRunField(stdout, "branch", rec.Branch)
	printRunField(stdout, "base", rec.BaseCommit)
	printRunField(stdout, "commit", rec.Commit)
	printRunField(stdout, "role", rec.Role)
	printRunField(stdout, "runtime", rec.Runtime)
	if len(rec.Phases) == 0 {
		return
	}
	fmt.Fprintln(stdout, "\nphases")
	for _, p := range rec.Phases {
		fmt.Fprintf(stdout, "  %-9s %s", p.Phase, p.At.UTC().Format(time.RFC3339))
		if p.Note != "" {
			fmt.Fprintf(stdout, "  %s", p.Note)
		}
		fmt.Fprintln(stdout)
	}
}

// printRunField writes one labeled line, and writes nothing at all for a
// field the record does not carry: a run that never committed has no
// commit, and an empty value beside a label reads like a value that got
// lost on the way here.
func printRunField(stdout io.Writer, label, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(stdout, "%-8s %s\n", label, value)
}

// displayOutcome is the run's outcome as text mode states it. See
// unsettledOutcome for why a record with no outcome is reported as a
// question rather than an answer.
func displayOutcome(rec workrec.Record) string {
	if rec.Outcome != "" {
		return rec.Outcome
	}
	return unsettledOutcome
}

// orDash renders a column the record left empty. A blank cell in a table
// reads as a misaligned row rather than an absent value.
func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
