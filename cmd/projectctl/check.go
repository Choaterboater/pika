package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/Choaterboater/projectctl/internal/checks"
	"github.com/Choaterboater/projectctl/internal/contract"
	"github.com/Choaterboater/projectctl/internal/profiles"
	"github.com/Choaterboater/projectctl/internal/verify"
)

// defaultContractPath is the core profile's contract location relative to
// the repository root.
const defaultContractPath = ".project/contract.yaml"

// runCheck implements `projectctl check [--all|--changed|--ci] [--json]`.
// The verification ladder (spec §12.6): gate 1 runs the contract/profile
// validation — the schema-version ceiling, the exceptions record, and the
// naming and ownership projection checks (Task 8); gates 2-4 are the
// ordered CheckSet entries from profiles.Resolve with contract commands
// overriding discovery sentinels; gate 5 (agent review) is never part of
// check.
//
// Exit codes: 0 all gates pass or skip, 1 any gate failed, 2 usage or
// configuration error.
func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "run every gate")
	changed := fs.Bool("changed", false, "changed-scope verification (reserved; M1 runs all gates)")
	ci := fs.Bool("ci", false, "CI mode: implies --all; no interactive prompts")
	jsonOut := fs.Bool("json", false, "emit the JSON report on stdout")
	contractPath := fs.String("contract", "", "path to the contract file (default .project/contract.yaml)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "projectctl check: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	scopes := 0
	for _, b := range []*bool{all, changed, ci} {
		if *b {
			scopes++
		}
	}
	if scopes > 1 {
		fmt.Fprintln(stderr, "projectctl check: --all, --changed, and --ci are mutually exclusive")
		return 2
	}
	scope := verify.All
	switch {
	case *ci:
		scope = verify.CI
	case *changed:
		scope = verify.Changed
	}

	// M1's repo root is the process working directory; the contract and
	// exceptions records are resolved beneath it (spec §5.2).
	repoRoot := "."

	path := *contractPath
	if path == "" {
		path = defaultContractPath
	}

	// Configuration errors surface before the ladder runs: without a
	// loadable contract and resolvable profiles there is nothing to
	// verify.
	c, err := contract.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "projectctl check:", err)
		return 2
	}
	resolved, err := profiles.Resolve(c.Profiles)
	if err != nil {
		fmt.Fprintln(stderr, "projectctl check:", err)
		return 2
	}

	// Gate 1 (rung 1, spec §12.6): contract schema ceiling, exceptions
	// record, and naming/ownership projection checks. Warnings raised by
	// the projection checks are review signals carried on the report.
	var gate1Warnings []string
	gates := verify.CheckSet{{
		ID: "contract",
		Func: func(context.Context) (int, string) {
			exit, output, warnings := checks.Gate1(repoRoot, c, resolved)
			gate1Warnings = warnings
			return exit, output
		},
	}}
	ordered, err := verify.FromProfiles(resolved.Checks, c.Commands)
	if err != nil {
		fmt.Fprintln(stderr, "projectctl check:", err)
		return 2
	}
	gates = append(gates, ordered...)

	rep, err := verify.Run(context.Background(), gates, scope)
	if err != nil {
		fmt.Fprintln(stderr, "projectctl check:", err)
		return 2
	}
	rep.Warnings = append(rep.Warnings, gate1Warnings...)

	if *jsonOut {
		data, err := json.Marshal(rep)
		if err != nil {
			fmt.Fprintln(stderr, "projectctl check:", err)
			return 2
		}
		fmt.Fprintln(stdout, string(data))
	} else {
		printReport(rep, stdout)
	}
	if !rep.Pass {
		return 1
	}
	return 0
}

// printReport writes the human-readable check report.
func printReport(rep *verify.Report, stdout io.Writer) {
	for _, w := range rep.Warnings {
		fmt.Fprintf(stdout, "warning: %s\n", w)
	}
	for _, g := range rep.Gates {
		switch g.Status {
		case verify.StatusFail:
			fmt.Fprintf(stdout, "FAIL %-10s exit=%d %s\n", g.ID, g.Exit, g.OutputTail)
		case verify.StatusSkip:
			fmt.Fprintf(stdout, "SKIP %-10s %s\n", g.ID, g.Reason)
		default:
			fmt.Fprintf(stdout, "PASS %-10s %dms\n", g.ID, g.DurationMs)
		}
	}
	fmt.Fprintf(stdout, "check: %d passed, %d failed, %d skipped\n",
		rep.Summary.Pass, rep.Summary.Fail, rep.Summary.Skip)
}
