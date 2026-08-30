package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Choaterboater/pika/internal/initcmd"
)

// stringList collects a repeatable string flag's values in the order
// they were given. init's --profile and authorize's --network,
// --credential, and --github all need exactly this, so it lives here
// once rather than being retyped per command.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// runInit implements `pika init [--profile <lang>] [--name <name>]
// [--module <path>] [--force] [--reset-docs] [--json] [--root <dir>]`.
// The scaffold target is --root, or the working directory: init creates
// a project where the caller is standing and never discovers an
// enclosing one.
//
// --force regenerates what the kernel owns (the contract, the profiles
// lock, the PR template, the CI workflow) and leaves the operator's
// README, AGENTS.md, CONTRIBUTING.md and language scaffold in place.
// --reset-docs additionally restores the scaffold's own text over them.
//
// Exit codes: 0 scaffold created, 1 scaffold refused (an existing
// contract), 2 usage error.
func runInit(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var profilesFlag stringList
	fs.Var(&profilesFlag, "profile", "language profile to scaffold (repeatable: go, typescript, python, swift, rust)")
	name := fs.String("name", "", "project name (default: directory name, kebab-cased)")
	module := fs.String("module", "", "go module path (default: derived from the project name)")
	force := fs.Bool("force", false, "regenerate the kernel-owned files (contract, profiles lock, PR template, CI workflow) even if a contract exists")
	resetDocs := fs.Bool("reset-docs", false, "with --force, also restore the scaffolded README, AGENTS.md, CONTRIBUTING.md and language scaffold over the repository's own")
	jsonOut := fs.Bool("json", false, "emit the created-file manifest as JSON on stdout")
	rootFlag := fs.String("root", "", targetRootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return fail(*jsonOut, stdout, stderr, "init", codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	if *resetDocs && !*force {
		// --reset-docs only ever modifies an existing scaffold, and an
		// existing scaffold is refused without --force. Alone it can
		// only be a mistyped intention, so say so rather than accept a
		// flag that does nothing.
		return fail(*jsonOut, stdout, stderr, "init", codeUsage,
			"--reset-docs requires --force")
	}
	root, err := targetRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "init", codeConfig, err.Error())
	}
	manifest, err := initcmd.Run(initcmd.InitOptions{
		Dir:       root.Dir(),
		Name:      *name,
		Module:    *module,
		Profiles:  profilesFlag,
		Force:     *force,
		ResetDocs: *resetDocs,
	})
	if err != nil {
		if *jsonOut && emitFailure(stdout, stderr, "init", err, nil) {
			return 1
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	// The scaffold is silent unless asked: init's human output is the
	// tree it just wrote. The manifest is encoded here, in the command
	// layer, rather than inside initcmd — the package returns data and
	// the CLI decides what a payload looks like.
	if *jsonOut && !emitJSON(stdout, stderr, "init", true, manifest) {
		return 1
	}
	return 0
}
