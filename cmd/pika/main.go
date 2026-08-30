// Command pika is the root entrypoint for the pika CLI.
package main

import (
	"fmt"
	"os"

	"github.com/Choaterboater/pika/internal/version"
)

func main() {
	args := os.Args[1:]
	for _, arg := range args {
		if arg == "--version" || arg == "-version" || arg == "version" {
			fmt.Println(version.String())
			return
		}
	}
	if len(args) > 0 && args[0] == "init" {
		os.Exit(runInit(args[1:], os.Stdout, os.Stderr))
	}
	if len(args) > 0 && args[0] == "check" {
		os.Exit(runCheck(args[1:], os.Stdout, os.Stderr))
	}
	if len(args) > 0 && args[0] == "adopt" {
		os.Exit(runAdopt(args[1:], os.Stdout, os.Stderr))
	}
	if len(args) > 0 && args[0] == "apply" {
		os.Exit(runApply(args[1:], os.Stdout, os.Stderr))
	}
	if len(args) > 0 && args[0] == "mcp" {
		os.Exit(runMCP(args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	if len(args) > 0 && args[0] == "handoff" {
		os.Exit(runHandoff(args[1:], os.Stdout, os.Stderr))
	}
	if len(args) > 0 && args[0] == "improve" {
		os.Exit(runImprove(args[1:], os.Stdout, os.Stderr))
	}
	fmt.Fprintln(os.Stderr, "usage: pika [--version] | init [--profile <lang>] [--name <name>] [--module <path>] [--force] [--json] | check [--all|--changed|--ci] [--json] [--contract <path>] | adopt [--json] | apply [--json] | handoff [--agent <name>] [--json] | improve [--branch <name>] [--agent <name>] [--json] | mcp")
	os.Exit(2)
}
