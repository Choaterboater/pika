package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

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

// runSkills implements `pika skills [install|check] [--force] [--global]
// [--home <dir>] [--json] [--root <dir>]`: it installs and verifies the
// agent instructions this repository ships, and — only when asked
// explicitly — the ones that live in the operator's home directory.
//
// The default is a report, for the same reason `pika recover`'s is: the
// operator arriving here does not yet know what state the repository is
// in. It names every canonical skill, every declared projection, and
// whether each projection still matches the sources it was generated
// from — all read-only. `install` is the separate, explicit act of
// writing, and `check` is the verdict on its own, for a human who wants
// the drift answer without the rest of the ladder.
//
// --global selects the other class of target rather than adding to this
// one, and it is the only way to reach it. The two classes share no
// state: a global file is installed where there may be no repository at
// all, so global mode reads no contract and resolves no root, and a
// repository can therefore say nothing about whether, when, or where one
// of those files is written. That is the point of the flag and not an
// implementation detail — a contract able to reach the home directory
// would mean cloning a repository granted it a capability over the
// operator's machine.
//
// The envelope's ok field answers "did the command do what it was asked",
// which differs by mode and is meant to. A report that reports drift
// reported successfully; a check that finds drift did not pass.
//
// Exit codes: 0 for a report and for a completed install, 1 when `check`
// finds drift or an install cannot finish, 2 on a usage error, a
// repository whose contract cannot be read, or a home directory that
// cannot be resolved.
func runSkills(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("skills", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit the report as JSON on stdout")
	force := fs.Bool("force", false, "overwrite a canonical skill the operator has edited")
	global := fs.Bool("global", false, "act on the agent files in the operator's home directory instead of this repository's projections")
	homeFlag := fs.String("home", "", "with --global, use this directory as the home directory instead of the operator's own")
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
	// Every flag that means nothing in the mode it was passed in is
	// refused rather than ignored. A flag silently dropped is how an
	// operator concludes a command did something it did not: --force
	// on a global install would suggest the home files have an
	// operator-owned half that a flag overrides, and --root would
	// suggest a repository chose where they went.
	if *global {
		if *force {
			return fail(*jsonOut, stdout, stderr, "skills", codeUsage,
				"--force does not apply to --global; the global agent files are kernel-owned between their markers and operator-owned outside them, and neither half needs overriding")
		}
		if *rootFlag != "" {
			return fail(*jsonOut, stdout, stderr, "skills", codeUsage,
				"--root does not apply to --global; the global agent files live outside every repository, and no repository decides anything about them")
		}
		return runSkillsGlobal(mode, *homeFlag, *jsonOut, stdout, stderr)
	}
	if *homeFlag != "" {
		return fail(*jsonOut, stdout, stderr, "skills", codeUsage,
			"--home applies only with --global")
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

// runSkillsGlobal is the --global half: the same three modes against the
// agent files in the operator's home directory.
//
// A home that cannot be resolved is exit 2 and not a skipped step. The
// operator asked for the global files by name; answering "nothing to do"
// about a directory the kernel could not find would report success for
// work it never attempted.
func runSkillsGlobal(mode, homeFlag string, jsonOut bool, stdout, stderr io.Writer) int {
	home, err := skills.ResolveHome(homeFlag)
	if err != nil {
		return fail(jsonOut, stdout, stderr, "skills", codeConfig, err.Error())
	}
	var rep *skills.GlobalReport
	if mode == skillsInstall {
		rep, err = skills.InstallGlobal(home)
		if err != nil {
			if !jsonOut || !emitFailure(stdout, stderr, "skills", err, nil) {
				fmt.Fprintf(stderr, "pika skills: %v\n", err)
			}
			return 1
		}
	} else {
		rep = skills.InspectGlobal(home)
	}

	ok := rep.OK
	if mode == skillsReport {
		ok = true
	}
	if jsonOut {
		if !emitJSON(stdout, stderr, "skills", ok, rep) {
			return 1
		}
	} else {
		printGlobalStatus(rep, mode, stdout)
	}
	if !ok {
		return 1
	}
	return 0
}

// printGlobalStatus writes the human-readable global report in the same
// shape as the repository one, so an operator reading both does not have
// to learn two layouts.
//
// It closes by saying that no gate consults these files. That is not
// reassurance, it is the fact an operator needs in order to read the
// report correctly: a stale global file will never turn a check red, so
// nothing will remind them again.
func printGlobalStatus(rep *skills.GlobalReport, mode string, stdout io.Writer) {
	fmt.Fprintf(stdout, "home  %s\n\n", rep.Home)
	fmt.Fprintln(stdout, "global agent files")
	for _, t := range rep.Targets {
		fmt.Fprintf(stdout, "  %-10s %-46s %s %s\n", t.State, t.Path, t.Harness, writtenMark(t.Written))
		if t.Detail != "" {
			fmt.Fprintf(stdout, "  %-10s → %s\n", "", t.Detail)
		}
	}
	for _, s := range rep.Targets[0].Sources {
		fmt.Fprintf(stdout, "  %-10s   %s %s %s\n", "", s.Kind, s.Ref, s.Digest)
	}
	if mode != skillsInstall {
		fmt.Fprintln(stdout, "\nno gate checks these files: they are absent from a fresh checkout, so digesting\n"+
			"  them would fail every clone. This report and `pika doctor` are where their\n"+
			"  state is said out loud.")
	}
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
		fmt.Fprintln(stdout, "\n"+skillsRemedy(st))
	}
}

// skillsRemedy is the closing line of a report that found something
// wrong. It is not one fixed line, because the two ways a projection
// fails have opposite consequences: regenerating a stale copy costs
// nothing, and regenerating a tampered one throws away whatever
// somebody typed inside the markers. Printing "run `pika skills
// install`" under a tampered projection would be the kernel
// recommending the destructive move without saying it is one.
func skillsRemedy(st *skills.Status) string {
	var tampered []string
	for _, p := range st.Projections {
		if p.State == skills.StateTampered {
			tampered = append(tampered, p.Path)
		}
	}
	if len(tampered) == 0 {
		return "run `pika skills install` to regenerate the projections above"
	}
	return "hand-edited kernel-owned region in " + strings.Join(tampered, ", ") + ":\n" +
		"  `pika skills install` will DISCARD those edits. Move the change into the\n" +
		"  canonical skill under .agents/skills/ first, then regenerate."
}

// writtenMark marks the rows this invocation actually wrote, so an
// install reports what it did rather than only what is now true.
func writtenMark(written bool) string {
	if written {
		return "(written)"
	}
	return ""
}
