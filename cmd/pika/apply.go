package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/Choaterboater/pika/internal/adopt"
	"github.com/Choaterboater/pika/internal/apply"
)

// runApply implements `pika apply [--json] [--root <dir>]`: a thin CLI over the
// transactional apply engine. Run promotes the adoption drafts into a
// live contract inside a txn transaction — create-if-missing for every
// proposed file, full rollback on any mid-plan failure — and rewrites
// the visible review bundle as APPLIED.
//
// Exit codes: 0 applied, 1 failure, 2 usage error.
func runApply(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit the apply report as JSON on stdout")
	rootFlag := fs.String("root", "", rootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return fail(*jsonOut, stdout, stderr, "apply", codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "apply", codeConfig, err.Error())
	}
	rep, err := apply.Run(apply.RunOptions{Dir: root.Dir()})
	if err != nil {
		// The report goes out even here: Report.Rollback and the applied
		// ops are how a consumer tells a clean pre-state from mutations
		// that may remain, and that answer cannot live only in prose.
		if *jsonOut && emitFailure(stdout, stderr, "apply", err, rep) {
			return 1
		}
		fmt.Fprintln(stderr, err)
		// Report.Rollback is truthful: true only when the undo completed.
		// A false with applied ops means the failure came after the commit
		// (only the review-bundle rewrite fails there); a false with
		// TransactionBegan false means the failure never reached
		// txn.Begin at all — no mutation was ever attempted, so there is
		// nothing to distinguish it from a completed rollback; a false
		// with TransactionBegan true and no applied ops means the undo
		// itself was refused and mutations may remain.
		if rep.Rollback || !rep.TransactionBegan {
			fmt.Fprintln(stderr, "nothing was applied; the repository is at its pre-state")
		} else if len(rep.Applied) > 0 {
			fmt.Fprintln(stderr, "the contract WAS applied; only the review bundle rewrite failed")
		} else {
			fmt.Fprintln(stderr, "the pre-state could not be restored; the transaction's mutations may remain")
		}
		return 1
	}
	if *jsonOut {
		if !emitJSON(stdout, stderr, "apply", true, rep) {
			return 1
		}
		return 0
	}
	printApplyReport(rep, stdout)
	return 0
}

// printApplyReport writes the human-readable apply summary, mirroring
// adopt's style: what was created, what was skipped, and the gate-1
// verdict on the applied contract.
func printApplyReport(rep apply.Report, stdout io.Writer) {
	fmt.Fprintln(stdout, "applied (transaction committed)")
	for _, a := range rep.Applied {
		fmt.Fprintf(stdout, "  %s %s\n", a.Op, a.Path)
	}
	if len(rep.Skipped) == 0 {
		fmt.Fprintln(stdout, "skipped: none")
	} else {
		// Not every skip is a file of the operator's that was kept:
		// a kernel-owned file that already matches the rendered
		// template is skipped too, and calling it "yours" tells the
		// operator the kernel deferred to a file it in fact owns.
		// The header states only what is true of every line — the
		// file is on disk as apply found it — and each line gives
		// its own reason.
		fmt.Fprintln(stdout, "skipped (left on disk as apply found them):")
		for _, s := range rep.Skipped {
			fmt.Fprintf(stdout, "  %s: %s\n", s.Path, s.Reason)
		}
	}
	if rep.Gate1.Pass && len(rep.Gate1.Warnings) == 0 {
		fmt.Fprintln(stdout, "gate 1: pass")
	} else if rep.Gate1.Pass {
		fmt.Fprintln(stdout, "gate 1: pass (with warnings)")
		for _, w := range rep.Gate1.Warnings {
			fmt.Fprintf(stdout, "  %s\n", w)
		}
	} else {
		fmt.Fprintln(stdout, "gate 1: FAIL (the applied state is valid; resolve these findings)")
		if rep.Gate1.Output != "" {
			fmt.Fprintln(stdout, rep.Gate1.Output)
		}
		for _, w := range rep.Gate1.Warnings {
			fmt.Fprintf(stdout, "  %s\n", w)
		}
	}
	fmt.Fprintf(stdout, "review bundle rewritten: %s\n", adopt.ReviewPath)
}
