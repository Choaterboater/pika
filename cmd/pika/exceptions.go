package main

import (
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/Choaterboater/pika/internal/checks"
)

// The two things `pika exceptions` can be asked to do.
const exceptionsReassign = "reassign"

// exceptionsResult is the --json shape for both modes: the read-only
// report and the write. Reassigned/Records/Owner are set only by
// reassign; Total/AutoOwned only by the report.
type exceptionsResult struct {
	Root       string                    `json:"root"`
	Path       string                    `json:"path"`
	Total      int                       `json:"total,omitempty"`
	AutoOwned  int                       `json:"autoOwned,omitempty"`
	Owner      string                    `json:"owner,omitempty"`
	Reassigned int                       `json:"reassigned,omitempty"`
	Records    []checks.ReassignedRecord `json:"records,omitempty"`
}

// runExceptions implements `pika exceptions [reassign --owner <name>]
// [--json] [--root <dir>]`.
//
// The default is a report, for the same reason `pika skills`'s and
// `pika recover`'s are: an operator arriving here does not yet know
// how many recorded exceptions exist or how many of them nobody has
// actually accepted. `reassign` is the separate, explicit act of
// writing — gate 1 warns about AutoRecordedOwner but names no way to
// fix it in bulk; this is that way, one pass over every record still
// carrying the placeholder rather than a hand edit per record.
//
// Exit codes: 0 for a report and for a completed reassignment, 1 when
// reassign refuses (an empty owner, the placeholder itself, or a
// record that does not load), 2 on a usage error or a repository whose
// exceptions record cannot be resolved.
func runExceptions(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("exceptions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit the result as JSON on stdout")
	owner := fs.String("owner", "", "with reassign: the new owner for every exception still owned by \"pika adopt\"")
	rootFlag := fs.String("root", "", rootFlagUsage)
	// stdlib flag stops at the first non-flag argument, so the
	// subcommand is consumed and parsing resumed — the same shape
	// `pika skills` and `pika explain` use.
	if err := fs.Parse(args); err != nil {
		return 2
	}
	reassigning := false
	if rest := fs.Args(); len(rest) > 0 {
		switch rest[0] {
		case exceptionsReassign:
			reassigning = true
		default:
			return fail(*jsonOut, stdout, stderr, "exceptions", codeUsage,
				fmt.Sprintf("unknown subcommand %q; expected reassign", rest[0]))
		}
		if err := fs.Parse(rest[1:]); err != nil {
			return 2
		}
		if fs.NArg() != 0 {
			return fail(*jsonOut, stdout, stderr, "exceptions", codeUsage,
				fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
		}
	}
	// --owner authorizes a write, and nothing else does. Accepting it
	// silently on the read-only report would teach the habit of
	// passing it, which is exactly how the flag stops meaning anything.
	if *owner != "" && !reassigning {
		return fail(*jsonOut, stdout, stderr, "exceptions", codeUsage, "--owner applies only to `pika exceptions reassign`")
	}
	if reassigning && *owner == "" {
		return fail(*jsonOut, stdout, stderr, "exceptions", codeUsage, "reassign requires --owner")
	}

	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "exceptions", codeConfig, err.Error())
	}

	if reassigning {
		rep, err := checks.ReassignAutoRecordedOwners(root.Dir(), *owner)
		if err != nil {
			if *jsonOut {
				if !emitFailure(stdout, stderr, "exceptions", err, nil) {
					fmt.Fprintf(stderr, "pika exceptions: %v\n", err)
				}
			} else {
				fmt.Fprintf(stderr, "pika exceptions: %v\n", err)
			}
			return 1
		}
		res := exceptionsResult{Root: root.Dir(), Path: root.Exceptions(), Owner: rep.Owner, Reassigned: rep.Reassigned, Records: rep.Records}
		if *jsonOut {
			if !emitJSON(stdout, stderr, "exceptions", true, res) {
				return 1
			}
		} else {
			printReassignResult(stdout, rep)
		}
		return 0
	}

	exceptions, err := checks.LoadExceptions(root.Dir())
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "exceptions", codeConfig, err.Error())
	}
	total, autoOwned := 0, 0
	for _, list := range exceptions {
		for _, ex := range list {
			total++
			if ex.Owner == checks.AutoRecordedOwner {
				autoOwned++
			}
		}
	}
	res := exceptionsResult{Root: root.Dir(), Path: root.Exceptions(), Total: total, AutoOwned: autoOwned}
	if *jsonOut {
		if !emitJSON(stdout, stderr, "exceptions", true, res) {
			return 1
		}
	} else {
		printExceptionsReport(stdout, res)
	}
	return 0
}

// printExceptionsReport writes the human-readable default report.
func printExceptionsReport(stdout io.Writer, res exceptionsResult) {
	fmt.Fprintf(stdout, "root  %s\n\n", res.Root)
	fmt.Fprintf(stdout, "%s: %d exception(s) recorded\n", res.Path, res.Total)
	if res.AutoOwned == 0 {
		fmt.Fprintln(stdout, "  none still owned by \"pika adopt\"")
		return
	}
	fmt.Fprintf(stdout, "  %d still owned by \"pika adopt\"; nothing forces a human to accept them\n", res.AutoOwned)
	fmt.Fprintln(stdout, "  → `pika exceptions reassign --owner <name>` to reassign every one of them at once")
}

// printReassignResult writes the human-readable reassign report,
// naming every record that changed rather than only the count: a bulk
// edit an operator cannot see the shape of is one they have to trust
// blindly.
func printReassignResult(stdout io.Writer, rep checks.Reassignment) {
	if rep.Reassigned == 0 {
		fmt.Fprintln(stdout, "nothing was owned by \"pika adopt\"; nothing to reassign")
		return
	}
	fmt.Fprintf(stdout, "reassigned %d exception(s) to %q:\n", rep.Reassigned, rep.Owner)
	records := append([]checks.ReassignedRecord(nil), rep.Records...)
	sort.Slice(records, func(i, j int) bool {
		if records[i].Path != records[j].Path {
			return records[i].Path < records[j].Path
		}
		return records[i].RuleID < records[j].RuleID
	})
	for _, r := range records {
		fmt.Fprintf(stdout, "  %s (%s)\n", r.Path, r.RuleID)
	}
}
