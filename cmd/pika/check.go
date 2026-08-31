package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/Choaterboater/pika/internal/changed"
	"github.com/Choaterboater/pika/internal/checks"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/verify"
)

// runCheck implements `pika check [--all|--changed|--ci] [--json]`.
// The verification ladder (spec §12.6): gate 1 runs the contract/profile
// validation — the schema-version ceiling, the exceptions record, and the
// naming and ownership projection checks (Task 8); gates 2-4 are the
// ordered CheckSet entries from profiles.Resolve with contract commands
// overriding discovery sentinels; gate 5 (agent review) is never part of
// check.
//
// Exit codes: 0 all gates pass or skip, 1 any gate failed, 2 usage or
// configuration error.
func runCheck(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "run every gate")
	changedFlag := fs.Bool("changed", false, "run gates for packages touched since the merge base")
	ci := fs.Bool("ci", false, "CI mode: implies --all; no interactive prompts")
	jsonOut := fs.Bool("json", false, "emit the JSON report on stdout")
	contractPath := fs.String("contract", "", "path to the contract file (default <root>/.project/contract.yaml)")
	rootFlag := fs.String("root", "", rootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return fail(*jsonOut, stdout, stderr, "check", codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	scopes := 0
	for _, b := range []*bool{all, changedFlag, ci} {
		if *b {
			scopes++
		}
	}
	if scopes > 1 {
		return fail(*jsonOut, stdout, stderr, "check", codeUsage,
			"--all, --changed, and --ci are mutually exclusive")
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

	if *jsonOut {
		if !emitJSON(stdout, stderr, "check", rep.Pass, rep) {
			return 1
		}
	} else {
		printReport(rep, stdout)
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
func printReport(rep *verify.Report, stdout io.Writer) {
	for _, w := range rep.Warnings {
		fmt.Fprintf(stdout, "warning: %s\n", w)
	}
	for _, g := range rep.Gates {
		switch g.Status {
		case verify.StatusFail:
			fmt.Fprintf(stdout, "FAIL %-10s %s\n%s", g.ID, g.Reason, g.OutputTail)
		case verify.StatusSkip:
			fmt.Fprintf(stdout, "SKIP %-10s %s\n", g.ID, g.Reason)
		default:
			fmt.Fprintf(stdout, "PASS %-10s %dms\n", g.ID, g.DurationMs)
		}
	}
	fmt.Fprintf(stdout, "check: %d passed, %d failed, %d skipped\n",
		rep.Summary.Pass, rep.Summary.Fail, rep.Summary.Skip)
}
