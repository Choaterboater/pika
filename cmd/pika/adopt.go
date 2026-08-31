package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Choaterboater/pika/internal/adopt"
)

// runAdopt implements `pika adopt [--json] [--root <dir>]` (spec §8.1,
// §13): a thin CLI over the read-only adoption inventory. Preview walks
// the resolved repository root, classifies every discovered convention
// against core@1, runs the discovered check commands once each to record
// a baseline, and writes exactly the two .draft proposal files — no
// tracked file is touched.
//
// Exit codes: 0 preview produced, 1 failure, 2 usage error.
func runAdopt(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("adopt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit the adoption report as JSON on stdout")
	rootFlag := fs.String("root", "", rootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return fail(*jsonOut, stdout, stderr, "adopt", codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "adopt", codeConfig, err.Error())
	}
	rep, err := adopt.Preview(root.Dir())
	if err != nil {
		if *jsonOut && emitFailure(stdout, stderr, "adopt", err, nil) {
			return 1
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		if !emitJSON(stdout, stderr, "adopt", true, rep) {
			return 1
		}
		return 0
	}
	printAdoptReport(rep, stdout)
	return 0
}

// printAdoptReport writes the human-readable adoption summary: the
// detected stacks, the convention map, baseline check failures, the
// conflicts that need a human decision, the proposed exceptions, and
// the drafts adopt wrote.
func printAdoptReport(rep *adopt.Report, stdout io.Writer) {
	fmt.Fprintln(stdout, "adoption preview (read-only; drafts only, nothing applied)")
	fmt.Fprintf(stdout, "detected profiles: %s\n", orNone(strings.Join(rep.DetectedProfiles, ", ")))
	fmt.Fprintf(stdout, "packages: %d\n", len(rep.Inventory.Packages))
	fmt.Fprintln(stdout, "conventions:")
	for _, c := range rep.ConventionMap {
		fmt.Fprintf(stdout, "  %-9s %s: %s\n", c.Status, c.Name, c.Detail)
	}
	for _, b := range rep.BaselineChecks {
		if b.Status == "pass" {
			fmt.Fprintf(stdout, "baseline %s: pass (%s)\n", b.Verb, b.Command)
		} else {
			fmt.Fprintf(stdout, "baseline %s: %s (%s, exit %d)\n", b.Verb, b.Status, b.Command, b.Exit)
		}
	}
	// A red baseline is inherited repository state, not an adopt error, so
	// adoption still succeeds and still exits 0 — refusing here would make
	// every imperfect repository unadoptable, which is most of them. But
	// printing the failures and then "drafts written" reads as success:
	// cobra adopted with `make lint` already failing and nothing said so.
	// The operator is about to run apply and inherit exactly these.
	if failed := failedBaselines(rep.BaselineChecks); len(failed) > 0 {
		fmt.Fprintf(stdout, "baseline is not green: %s %s failing before adoption, and %s after apply\n",
			strings.Join(failed, ", "),
			plural(len(failed), "is", "are"),
			plural(len(failed), "that gate will fail", "those gates will fail"))
	}
	if len(rep.Conflicts) == 0 {
		fmt.Fprintln(stdout, "conflicts: none")
	} else {
		fmt.Fprintln(stdout, "conflicts (need a human decision):")
		for _, c := range rep.Conflicts {
			fmt.Fprintf(stdout, "  %s %s: %s\n", c.RuleID, c.Path, c.Detail)
		}
	}
	if len(rep.Exceptions) == 0 {
		fmt.Fprintln(stdout, "proposed exceptions: none")
	} else {
		fmt.Fprintln(stdout, "proposed exceptions:")
		for _, e := range rep.Exceptions {
			fmt.Fprintf(stdout, "  %s %s\n", e.RuleID, e.Path)
		}
	}
	fmt.Fprintln(stdout, "proposed changes:")
	for _, ch := range rep.ProposedChanges {
		fmt.Fprintf(stdout, "  %s %s: %s\n", ch.Action, ch.Path, ch.Detail)
	}
	fmt.Fprintln(stdout, "drafts written (apply is a later, transactional step):")
	for _, d := range rep.Preview {
		fmt.Fprintf(stdout, "  %s\n", d.Path)
	}
}

// failedBaselines names the discovered check verbs that did not pass, in
// report order. These are the gates apply will hand to the ladder.
func failedBaselines(checks []adopt.BaselineCheck) []string {
	var failed []string
	for _, b := range checks {
		if b.Status != "pass" {
			failed = append(failed, b.Verb)
		}
	}
	return failed
}

// plural picks between a singular and a plural phrasing.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// orNone renders an empty summary value as "(none)".
func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
