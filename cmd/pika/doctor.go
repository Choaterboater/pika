package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Choaterboater/pika/internal/doctor"
	"github.com/Choaterboater/pika/internal/skills"
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
		return fail(*jsonOut, stdout, stderr, "doctor", codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "doctor", codeConfig, err.Error())
	}

	// doctor reports what is wrong; it never stops because it could not
	// find something. A machine that reports no home directory is one
	// where the global agent files cannot exist, which is a row in the
	// report and not a reason to refuse the other twelve.
	home, err := skills.ResolveHome("")
	if err != nil {
		home = ""
	}

	rep := doctor.Run(root, home)
	if *jsonOut {
		if !emitJSON(stdout, stderr, "doctor", rep.OK, rep) {
			return 1
		}
	} else {
		printDoctorReport(rep, stdout)
	}
	if !rep.OK {
		return 1
	}
	return 0
}

// printDoctorReport writes the human-readable report: the root and how it
// was resolved, one line per finding with its remediation indented
// beneath it, and then the agents the contract configures.
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
	printAgents(rep, stdout)
}

// printAgents writes the one block that cannot be a finding: several facts
// about one agent belong on one line, and a table whose rows are
// severities has no shape for that.
func printAgents(rep *doctor.Report, stdout io.Writer) {
	if len(rep.Agents) == 0 {
		return
	}
	fmt.Fprintf(stdout, "\nAgents\n\n")
	for _, a := range rep.Agents {
		fmt.Fprintf(stdout, "%-10s %-9s %s\n", a.Name, a.Runtime, a.Binary)
		var parts []string
		if a.Model != "" {
			parts = append(parts, "model: "+a.Model)
		}
		if a.Effort != "" {
			parts = append(parts, "effort: "+a.Effort)
		}
		if a.Output != "" {
			parts = append(parts, "output: "+a.Output)
		}
		parts = append(parts, "resume: "+yesNo(a.Resume))
		if len(a.CompatChecks) > 0 {
			parts = append(parts, "missing flags: "+strings.Join(a.CompatChecks, ", "))
		}
		fmt.Fprintf(stdout, "%-10s %-9s %s\n", "", "", strings.Join(parts, "  "))
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
