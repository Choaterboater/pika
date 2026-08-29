// Command projectctl is the root entrypoint for the projectctl CLI.
package main

import (
	"fmt"
	"os"

	"github.com/Choaterboater/projectctl/internal/version"
)

func main() {
	args := os.Args[1:]
	for _, arg := range args {
		if arg == "--version" || arg == "-version" || arg == "version" {
			fmt.Println(version.String())
			return
		}
	}
	if len(args) > 0 && args[0] == "check" {
		os.Exit(runCheck(args[1:], os.Stdout, os.Stderr))
	}
	fmt.Fprintln(os.Stderr, "usage: projectctl [--version] | check [--all|--changed|--ci] [--json] [--contract <path>]")
	os.Exit(2)
}
