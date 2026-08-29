package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Choaterboater/projectctl/internal/initcmd"
)

// profileFlags collects repeated --profile values.
type profileFlags []string

func (p *profileFlags) String() string { return strings.Join(*p, ",") }

func (p *profileFlags) Set(v string) error {
	*p = append(*p, v)
	return nil
}

// runInit implements `projectctl init [--profile <lang>] [--name <name>]
// [--module <path>] [--force] [--json]`. M1's scaffold target is the
// process working directory.
//
// Exit codes: 0 scaffold created, 1 scaffold refused (an existing
// contract), 2 usage error.
func runInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var profilesFlag profileFlags
	fs.Var(&profilesFlag, "profile", "language profile to scaffold (repeatable: go, typescript, python, swift, rust)")
	name := fs.String("name", "", "project name (default: directory name, kebab-cased)")
	module := fs.String("module", "", "go module path (default: derived from the project name)")
	force := fs.Bool("force", false, "regenerate the contract and managed files even if a contract exists")
	jsonOut := fs.Bool("json", false, "emit the created-file manifest as JSON on stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "projectctl init: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if err := initcmd.Run(initcmd.InitOptions{
		Dir:      ".",
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
