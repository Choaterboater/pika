package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Choaterboater/pika/internal/adopt"
)

// runAdopt implements `pika adopt [--json]` (spec §8.1, §13): a
// thin CLI over the read-only adoption inventory. Preview walks the
// current repository (M1's repo root is the process working directory),
// classifies every discovered convention against core@1, runs the
// discovered check commands once each to record a baseline, and writes
// exactly the two .draft proposal files — no tracked file is touched.
//
// Exit codes: 0 preview produced, 1 failure, 2 usage error.
func runAdopt(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("adopt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit the adoption report as JSON on stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "pika adopt: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	rep, err := adopt.Preview(".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintln(stderr, err)
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

// orNone renders an empty summary value as "(none)".
func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
