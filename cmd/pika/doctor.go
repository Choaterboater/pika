package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/Choaterboater/pika/internal/doctor"
)

// runDoctor implements `pika doctor`: a read-only diagnosis of the
// contract, lock, exceptions record, capability envelope, verification
// gates, and toolchain. It mutates nothing and executes no gate, so it is
// safe to run in any state — including a repository that was never
// adopted, which is a reportable finding rather than a fatal error.
//
// Exit codes: 0 when nothing is error-severity, 1 when something is, 2 on
// a usage or root-resolution error.
func runDoctor(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit the report as JSON on stdout")
	rootFlag := fs.String("root", "", rootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "pika doctor: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "pika doctor: %v\n", err)
		return 2
	}

	rep := doctor.Run(root)
	if *jsonOut {
		writeJSON(stdout, rep)
	} else {
		printDoctorReport(rep, stdout)
	}
	if !rep.OK {
		return 1
	}
	return 0
}

// printDoctorReport writes the human-readable report: the root and how it
// was resolved, then one line per finding with its remediation indented
// beneath it.
func printDoctorReport(rep *doctor.Report, stdout io.Writer) {
	fmt.Fprintf(stdout, "root  %s (%s)\n\n", rep.Root, rep.Origin)
	for _, f := range rep.Findings {
		// The root finding is the header above; repeating it as a row
		// would say the same thing twice.
		if f.ID == "root" {
			continue
		}
		fmt.Fprintf(stdout, "%-5s %-14s %s\n", f.Severity, f.ID, f.Detail)
		if f.Remediation != "" {
			fmt.Fprintf(stdout, "%-5s %-14s → %s\n", "", "", f.Remediation)
		}
	}
}
