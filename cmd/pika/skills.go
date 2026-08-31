package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/skills"
)

// The three things `pika skills` can be asked to do.
const (
	skillsReport  = "report"
	skillsInstall = "install"
	skillsCheck   = "check"
)

// runSkills implements `pika skills [install|check] [--force] [--json]
// [--root <dir>]`: it installs and verifies the agent instructions this
// repository ships.
//
// The default is a report, for the same reason `pika recover`'s is: the
// operator arriving here does not yet know what state the repository is
// in. It names every canonical skill, every declared projection, and
// whether each projection still matches the sources it was generated
// from — all read-only. `install` is the separate, explicit act of
// writing, and `check` is the verdict on its own, for a human who wants
// the drift answer without the rest of the ladder.
//
// The envelope's ok field answers "did the command do what it was asked",
// which differs by mode and is meant to. A report that reports drift
// reported successfully; a check that finds drift did not pass.
//
// Exit codes: 0 for a report and for a completed install, 1 when `check`
// finds drift or an install cannot finish, 2 on a usage error or a
// repository whose contract cannot be read.
func runSkills(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("skills", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit the report as JSON on stdout")
	force := fs.Bool("force", false, "overwrite a canonical skill the operator has edited")
	rootFlag := fs.String("root", "", rootFlagUsage)
	// stdlib flag stops at the first non-flag argument, so the
	// subcommand is consumed and parsing resumed — the same two-pass
	// shape `pika explain` uses — and `pika skills install --json`
	// works the way anyone would type it. The difference is that the
	// positional is optional here: no subcommand is the report.
	if err := fs.Parse(args); err != nil {
		return 2
	}
	mode := skillsReport
	if rest := fs.Args(); len(rest) > 0 {
		switch rest[0] {
		case skillsInstall, skillsCheck:
			mode = rest[0]
		default:
			return fail(*jsonOut, stdout, stderr, "skills", codeUsage,
				fmt.Sprintf("unknown subcommand %q; expected install or check", rest[0]))
		}
		if err := fs.Parse(rest[1:]); err != nil {
			return 2
		}
		if fs.NArg() != 0 {
			return fail(*jsonOut, stdout, stderr, "skills", codeUsage,
				fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
		}
	}
	// --force authorizes replacing an operator's own words, and nothing
	// else does. Accepting it silently on a read-only mode would teach
	// the habit of passing it, which is exactly how the flag stops
	// meaning anything.
	if *force && mode != skillsInstall {
		return fail(*jsonOut, stdout, stderr, "skills", codeUsage,
			"--force applies only to `pika skills install`")
	}

	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "skills", codeConfig, err.Error())
	}
	c, err := contract.Load(root.Contract())
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "skills", codeConfig, err.Error())
	}
	resolved, err := profiles.Resolve(c.Profiles)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "skills", codeConfig, err.Error())
	}

	var st *skills.Status
	if mode == skillsInstall {
		st, err = skills.Install(root, c, resolved, *force)
	} else {
		st, err = skills.Inspect(root, c, resolved)
	}
	if err != nil {
		if *jsonOut {
			if !emitFailure(stdout, stderr, "skills", err, nil) {
				fmt.Fprintf(stderr, "pika skills: %v\n", err)
			}
		} else {
			fmt.Fprintf(stderr, "pika skills: %v\n", err)
		}
		return 1
	}

	ok := st.OK
	if mode == skillsReport {
		ok = true
	}
	if *jsonOut {
		if !emitJSON(stdout, stderr, "skills", ok, st) {
			return 1
		}
	} else {
		printSkillsStatus(st, root, mode, stdout)
	}
	if !ok {
		return 1
	}
	return 0
}

// printSkillsStatus writes the human-readable report: the root, one line
// per canonical skill, and one line per declared projection with its
// remedy indented beneath it — the same shape `pika doctor` prints, so
// an operator reading both does not have to learn two layouts.
func printSkillsStatus(st *skills.Status, root *repopath.Root, mode string, stdout io.Writer) {
	fmt.Fprintf(stdout, "root  %s (%s)\n\n", st.Root, root.Origin())

	fmt.Fprintln(stdout, "canonical skills")
	for _, s := range st.Skills {
		fmt.Fprintf(stdout, "  %-10s %-46s %s\n", s.State, s.Path, writtenMark(s.Written))
		if s.Detail != "" {
			fmt.Fprintf(stdout, "  %-10s → %s\n", "", s.Detail)
		}
	}

	fmt.Fprintln(stdout, "\nprojections")
	if len(st.Projections) == 0 {
		fmt.Fprintln(stdout, "  none declared; .agents/skills is the only copy")
		fmt.Fprintln(stdout, "  → declare one under skills.projections in .project/contract.yaml")
		return
	}
	for _, p := range st.Projections {
		fmt.Fprintf(stdout, "  %-10s %-46s %s\n", p.State, p.Path, p.Harness)
		if p.Detail != "" {
			fmt.Fprintf(stdout, "  %-10s → %s\n", "", p.Detail)
		}
		for _, s := range p.Sources {
			fmt.Fprintf(stdout, "  %-10s   %s %s %s\n", "", s.Kind, s.Ref, s.Digest)
		}
	}
	if mode == skillsReport && !st.OK {
		fmt.Fprintln(stdout, "\nrun `pika skills install` to regenerate the drifted projections")
	}
}

// writtenMark marks the rows this invocation actually wrote, so an
// install reports what it did rather than only what is now true.
func writtenMark(written bool) string {
	if written {
		return "(written)"
	}
	return ""
}
