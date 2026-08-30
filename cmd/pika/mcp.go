package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/Choaterboater/pika/internal/mcp"
)

// runMCP implements `pika mcp [--root <dir>]`: the agent-facing MCP stdio
// server (spec §8.2). The protocol is line-delimited JSON-RPC 2.0 on
// stdin/stdout and is always JSON, so --root is the only flag;
// diagnostics go to stderr only. Requests are processed sequentially
// (single-flight); EOF on stdin shuts the server down cleanly.
func runMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rootFlag := fs.String("root", "", rootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "pika mcp: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		fmt.Fprintln(stderr, "pika mcp:", err)
		return 2
	}
	// The server's repoRoot is what every tool's path authorization is
	// measured against, so it is bound once here and never re-derived.
	if err := mcp.Serve(root.Dir(), stdin, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "pika mcp:", err)
		return 1
	}
	return 0
}
