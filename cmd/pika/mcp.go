package main

import (
	"fmt"
	"io"

	"github.com/Choaterboater/pika/internal/mcp"
)

// runMCP implements `pika mcp`: the agent-facing MCP stdio server
// (spec §8.2). The protocol is line-delimited JSON-RPC 2.0 on stdin/stdout
// and is always JSON, so there are no flags; diagnostics go to stderr only.
// Requests are processed sequentially (single-flight); EOF on stdin shuts
// the server down cleanly.
func runMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprintln(stderr, "usage: pika mcp")
			return 2
		}
		fmt.Fprintf(stderr, "pika mcp: unexpected argument %q\n", arg)
		return 2
	}
	// M1's repository root is the process working directory, as in check.
	if err := mcp.Serve(".", stdin, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "pika mcp:", err)
		return 1
	}
	return 0
}
