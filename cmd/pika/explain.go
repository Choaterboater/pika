package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/explain"
	"github.com/Choaterboater/pika/internal/profiles"
)

// runExplain implements `pika explain <id> [--json] [--root <dir>]`: it
// answers "what is this id and what do I do about it" for a naming rule,
// a verification gate, or an MCP error code (design spec goal 10 — a rule
// nobody can act on is a rule that gets waived blindly).
//
// explain is a reading command with no side effects, so it must work in a
// repository that has not been adopted yet: without a loadable contract it
// explains the core pack, which is the selection every project starts from.
//
// Exit codes: 0 the id was explained, 2 usage error or unknown id.
func runExplain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit the entry as JSON on stdout")
	rootFlag := fs.String("root", "", rootFlagUsage)
	// stdlib flag stops at the first non-flag argument, and explain is
	// the first command taking a positional: parsing once would reject
	// `pika explain <id> --json`, the way anyone would actually type it.
	// Consume the id, then keep parsing — flags land on either side, and
	// a second positional is still the usage error it should be.
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "usage: pika explain <rule-id> [--json] [--root <dir>]")
		return 2
	}
	id := rest[0]
	if err := fs.Parse(rest[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "pika explain: unexpected argument %q\n", fs.Arg(0))
		fmt.Fprintln(stderr, "usage: pika explain <rule-id> [--json] [--root <dir>]")
		return 2
	}

	resolved, err := explainSelection(*rootFlag)
	if err != nil {
		fmt.Fprintln(stderr, "pika explain:", err)
		return 2
	}

	entry, err := explain.Lookup(id, resolved)
	if err != nil {
		fmt.Fprintln(stderr, "pika explain:", err)
		fmt.Fprintln(stderr, "known ids:", strings.Join(explain.KnownIDs(resolved), ", "))
		return 2
	}

	if *jsonOut {
		writeJSON(stdout, entry)
		return 0
	}
	printEntry(entry, stdout)
	return 0
}

// explainSelection resolves the profile packs whose rules explain is
// asked about: the adopted contract's selection when there is one, and
// core otherwise. A missing or unreadable contract is not an error here —
// gate ids and error codes are explainable everywhere, and core's naming
// rules are the ones an unadopted repository is about to inherit.
func explainSelection(rootFlag string) (*profiles.Resolved, error) {
	selected := []string{profiles.CoreRef}
	if root, err := resolveRoot(rootFlag); err == nil {
		if c, err := contract.Load(root.Contract()); err == nil && len(c.Profiles) > 0 {
			selected = c.Profiles
		}
	}
	return profiles.Resolve(selected)
}

// printEntry writes the human-readable explanation: the labeled fields
// first, then the exception record.
func printEntry(e *explain.Entry, stdout io.Writer) {
	fmt.Fprintf(stdout, "%s (%s)\n", e.ID, e.Kind)
	for _, f := range []struct{ label, value string }{
		{"owner", e.Owner},
		{"severity", e.Severity},
		{"matches", e.Matches},
		{"rationale", e.Rationale},
		{"remediation", e.Remediation},
	} {
		if f.value == "" {
			continue
		}
		fmt.Fprintf(stdout, "\n%s:\n  %s\n", f.label, f.value)
	}
	if e.Exception == "" {
		return
	}
	// The record is printed flush left, unlike the labeled fields above:
	// it is meant to be pasted into .project/exceptions.yaml, and a block
	// indented for display would paste as a YAML indentation error.
	fmt.Fprintf(stdout, "\nrecord an exception:\n\n%s\n", e.Exception)
}
