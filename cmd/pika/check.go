package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Choaterboater/pika/internal/changed"
	"github.com/Choaterboater/pika/internal/checks"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/verify"
)

// runCheck implements `pika check [--all|--changed|--ci|--fast] [--json]`.
// The verification ladder (spec §12.6): gate 1 runs the contract/profile
// validation — the schema-version ceiling, the exceptions record, and the
// naming and ownership projection checks (Task 8); gates 2-4 are the
// ordered CheckSet entries from profiles.Resolve with contract commands
// overriding discovery sentinels; gate 5 (agent review) is never part of
// check.
//
// Exit codes: 0 all gates pass or skip, 1 any gate failed, 2 usage or
// configuration error. --record-baseline never changes this: it snapshots
// which gates are failing right now for a later run to compare against, and
// the human-readable report then marks a failure that matches the recorded
// snapshot as a known baseline rather than new — but every failure, known or
// not, still fails the run and its exit code. A baseline is a label an
// operator can act on, never a way to make a red ladder read green.
//
// --fast runs only format, lint, and typecheck; test and smoke skip with
// FastSkipReason rather than running. It exists for quick local iteration
// on the gates that catch most mistakes in seconds rather than minutes, and
// it is mutually exclusive with --all, --changed, and --ci: it is never the
// right flag for CI, which must verify behavior, not only shape.
func runCheck(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "run every gate")
	changedFlag := fs.Bool("changed", false, "resolve a change set from git; skip the package gates only when the tree is provably clean")
	ci := fs.Bool("ci", false, "CI mode: implies --all; no interactive prompts")
	fast := fs.Bool("fast", false, "run only format, lint, and typecheck; skip test and smoke for quick local iteration. Never use in CI: it does not verify behavior")
	jsonOut := fs.Bool("json", false, "emit the JSON report on stdout")
	contractPath := fs.String("contract", "", "path to the contract file (default <root>/.project/contract.yaml)")
	recordBaseline := fs.Bool("record-baseline", false, "replace the recorded baseline with this run's failing gates; does not affect this run's own pass/fail result")
	rootFlag := fs.String("root", "", rootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return fail(*jsonOut, stdout, stderr, "check", codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	scopes := 0
	for _, b := range []*bool{all, changedFlag, ci, fast} {
		if *b {
			scopes++
		}
	}
	if scopes > 1 {
		return fail(*jsonOut, stdout, stderr, "check", codeUsage,
			"--all, --changed, --ci, and --fast are mutually exclusive")
	}
	scope := verify.All
	switch {
	case *ci:
		scope = verify.CI
	case *changedFlag:
		scope = verify.Changed
	}

	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "check", codeConfig, err.Error())
	}

	// The contract, the exceptions record, and the gate subprocesses are
	// all bound to the resolved root (spec §5.2), so check reports on one
	// repository no matter which directory it was invoked from. A
	// relative --contract resolves against that root, not against the
	// caller's working directory.
	path := root.Contract()
	if *contractPath != "" {
		path = *contractPath
		if !filepath.IsAbs(path) {
			path = root.Join(filepath.FromSlash(path))
		}
	}

	// Configuration errors surface before the ladder runs: without a
	// loadable contract and resolvable profiles there is nothing to
	// verify.
	c, err := contract.Load(path)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "check", codeConfig, err.Error())
	}
	resolved, err := profiles.Resolve(c.Profiles)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "check", codeConfig, err.Error())
	}

	// Gate 1 (rung 1, spec §12.6): contract schema ceiling, exceptions
	// record, and naming/ownership projection checks. Warnings raised by
	// the projection checks are review signals carried on the report.
	var gate1Warnings []string
	gates := verify.CheckSet{{
		ID: "contract",
		Func: func(context.Context) (int, string) {
			exit, output, warnings := checks.Gate1(root.Dir(), c, resolved)
			gate1Warnings = warnings
			return exit, output
		},
	}}
	ordered, err := verify.FromProfiles(resolved.Checks, c.Commands)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "check", codeConfig, err.Error())
	}

	// --fast narrows by gate identity, not by file: it always covers the
	// whole repository's format, lint, and typecheck, and never runs test
	// or smoke regardless of what changed. That is a different axis from
	// --changed's file-driven narrowing, so it does not touch scope or
	// reuse ScopeSkipReason — a reader must be able to tell "you asked to
	// skip this" from "nothing changed here".
	if *fast {
		for i := range ordered {
			if (ordered[i].ID == "test" || ordered[i].ID == "smoke") && ordered[i].SkipReason == "" {
				ordered[i].SkipReason = verify.FastSkipReason
			}
		}
	}

	// Gate 1 always runs: it validates the contract itself, which no
	// change set can put out of scope. Only the package gates narrow.
	var scopeWarnings []string
	if scope == verify.Changed {
		set, err := changed.Files(root)
		if err != nil {
			return fail(*jsonOut, stdout, stderr, "check", codeConfig, err.Error())
		}
		if set.Degraded {
			scopeWarnings = append(scopeWarnings,
				"--changed could not resolve a change set ("+set.Reason+"); running every gate")
		} else if !scopeSelectsGates(set) {
			for i := range ordered {
				// A gate already skipped for a missing command keeps
				// that reason: "no command discovered" and "nothing
				// changed here" are different facts about the run.
				if ordered[i].SkipReason == "" {
					ordered[i].SkipReason = verify.ScopeSkipReason
				}
			}
		}
	}
	gates = append(gates, ordered...)

	rep, err := verify.Run(context.Background(), gates, scope, verify.WithDir(root.Dir()))
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "check", codeConfig, err.Error())
	}
	rep.Warnings = append(rep.Warnings, scopeWarnings...)
	rep.Warnings = append(rep.Warnings, gate1Warnings...)

	baseline, err := verify.LoadRecordedBaseline(root.Baseline())
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "check", codeConfig, err.Error())
	}
	// The report below reads against baseline as it stood BEFORE this
	// write, not after: a run that records a baseline is telling a
	// future run what it will already know, not claiming this run's own
	// failures were already known. Reporting against the fresh write
	// here would make the one run that introduces a baseline the one
	// run that cannot show anything as new.
	if *recordBaseline {
		var failedIDs []string
		for _, g := range rep.Gates {
			if g.Status == verify.StatusFail {
				failedIDs = append(failedIDs, g.ID)
			}
		}
		if err := os.MkdirAll(root.StateDir(), 0o755); err != nil {
			return fail(*jsonOut, stdout, stderr, "check", codeConfig, err.Error())
		}
		if err := verify.WriteRecordedBaseline(root.Baseline(), failedIDs); err != nil {
			return fail(*jsonOut, stdout, stderr, "check", codeConfig, err.Error())
		}
	}

	if *jsonOut {
		if !emitJSON(stdout, stderr, "check", rep.Pass, rep) {
			return 1
		}
	} else {
		printReport(rep, stdout, baseline)
	}
	if !rep.Pass {
		return 1
	}
	return 0
}

// scopeSelectsGates reports whether the change set puts the package gates
// (rungs 2-4) in scope. The rule in one sentence: only a change set that
// is known to be empty narrows the ladder; every other change set runs
// every gate.
//
// The gates are repository-wide commands, and a changed file cannot
// always be attributed to a declared package — a root-level go.mod, a CI
// workflow, or the contract itself belongs to no package while being able
// to break all of them. Attribution is therefore not a narrowing signal:
// treating "outside every package root" as "out of scope" would silently
// verify less exactly when the blast radius is widest. A degraded set is
// unknown and so also runs everything.
func scopeSelectsGates(set *changed.Set) bool {
	if set.Degraded {
		return true
	}
	return !set.Empty()
}

// printReport writes the human-readable check report.
//
// A failed gate leads with its Reason rather than with `exit=%d`. The
// reason names the exit status whenever an exit status is what decided
// the gate ("gate exited with status 1"), and says what decided it when
// nothing did — a timeout, or a gate judged on the report it printed.
// The old line could render `FAIL format exit=0`, which told the operator
// the command succeeded and the gate failed in the same six characters.
// The JSON report still carries the exit code as a field, where it is
// labelled and cannot be read as the verdict.
//
// A failed gate whose ID is in baseline is marked "(known baseline)":
// an operator already recorded it, so this run's job is only to say
// whether it is still the same set of failures, not to hide that it
// fails. baseline may be nil, meaning none has ever been recorded —
// every failure then reads as new, which is the correct default.
func printReport(rep *verify.Report, stdout io.Writer, baseline *verify.RecordedBaseline) {
	for _, w := range rep.Warnings {
		fmt.Fprintf(stdout, "warning: %s\n", w)
	}
	for _, g := range rep.Gates {
		switch g.Status {
		case verify.StatusFail:
			known := ""
			if baseline.Known(g.ID) {
				known = " (known baseline)"
			}
			fmt.Fprintf(stdout, "FAIL %-10s %s%s\n%s", g.ID, g.Reason, known, g.OutputTail)
		case verify.StatusSkip:
			fmt.Fprintf(stdout, "SKIP %-10s %s\n", g.ID, g.Reason)
		default:
			fmt.Fprintf(stdout, "PASS %-10s %dms\n", g.ID, g.DurationMs)
		}
	}
	fmt.Fprintf(stdout, "check: %d passed, %d failed, %d skipped\n",
		rep.Summary.Pass, rep.Summary.Fail, rep.Summary.Skip)
}
