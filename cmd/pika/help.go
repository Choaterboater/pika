package main

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// writeOverview renders the command table. It is generated from
// `commands`, so it can never describe a command that does not exist or
// omit one that does.
func writeOverview(w io.Writer) {
	fmt.Fprintln(w, "pika — a provider-neutral project operating system kernel")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "usage: pika <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range commands {
		fmt.Fprintf(tw, "  %s\t%s\n", c.name, c.summary)
	}
	tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "run \"pika help <command>\" for a command's flags")
	fmt.Fprintln(w, "run \"pika --version\" for the version")
}

// runHelp implements `pika help [<command>]`.
func runHelp(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeOverview(stdout)
		return 0
	}
	if len(args) > 1 {
		fmt.Fprintf(stderr, "pika help: unexpected argument %q\n", args[1])
		return 2
	}
	c, ok := lookup(args[0])
	if !ok {
		fmt.Fprintf(stderr, "pika help: unknown command %q\n\n", args[0])
		writeOverview(stderr)
		return 2
	}
	fmt.Fprintf(stdout, "%s\n\n%s\n", c.summary, c.usage)
	return 0
}
