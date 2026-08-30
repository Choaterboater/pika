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
// [--module <path>] [--force] [--json] [--root <dir>]`. The scaffold
// target is --root, or the working directory: init creates a project
// where the caller is standing and never discovers an enclosing one.
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
	force := fs.Bool("force", false, "regenerate the contract and managed files even if a contract exists")
	jsonOut := fs.Bool("json", false, "emit the created-file manifest as JSON on stdout")
	rootFlag := fs.String("root", "", targetRootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "pika init: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	root, err := targetRoot(*rootFlag)
	if err != nil {
		fmt.Fprintln(stderr, "pika init:", err)
		return 2
	}
	if err := initcmd.Run(initcmd.InitOptions{
		Dir:      root.Dir(),
		Name:     *name,
		Module:   *module,
		Profiles: profilesFlag,
		Force:    *force,
		JSON:     *jsonOut,
		Out:      stdout,
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
