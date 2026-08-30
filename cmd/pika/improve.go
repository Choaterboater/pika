package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/improve"
	"github.com/Choaterboater/pika/internal/verify"
)

const defaultImproveBranch = "chore/pika-improve"

// runHandoff implements `pika handoff [--agent builder] [--json]`. It is the
// explicit agent stage used by improve and can also be run independently when
// a caller wants to inspect the private bundle before acting on it.
func runHandoff(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agent := fs.String("agent", "builder", "contract agent name (must use the Codex runtime)")
	jsonOut := fs.Bool("json", false, "emit the handoff result as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "pika handoff: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	report, err := currentCheckReport()
	if err != nil {
		fmt.Fprintln(stderr, "pika handoff:", err)
		return 2
	}
	if !hasFailedGate(report) {
		if *jsonOut {
			writeJSON(stdout, map[string]any{"handoff": nil, "checks": report, "message": "no actionable failed check gates"})
		} else {
			fmt.Fprintln(stdout, "handoff: no actionable failed check gates")
		}
		return 0
	}
	runner, err := configuredCodexRunner(*agent)
	if err != nil {
		fmt.Fprintln(stderr, "pika handoff:", err)
		return 2
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "pika handoff:", err)
		return 2
	}
	handoff, err := improve.CreateHandoff(context.Background(), root, report, runner)
	if err != nil {
		fmt.Fprintln(stderr, "pika handoff:", err)
		return 1
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"handoff": handoff, "checks": report})
	} else {
		fmt.Fprintf(stdout, "handoff: Codex completed; bundle: %s\n", handoff.Dir)
	}
	return 0
}

// runImprove implements `pika improve`. The only Git mutation it performs is
// a local branch and verified local commit. Publishing remains a human choice.
func runImprove(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("improve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	branch := fs.String("branch", defaultImproveBranch, "local branch for verified fixes")
	agent := fs.String("agent", "builder", "contract agent name (must use the Codex runtime)")
	jsonOut := fs.Bool("json", false, "emit the improve result as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "pika improve: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "pika improve:", err)
		return 2
	}
	result, err := improve.Run(context.Background(), improve.Config{
		Root:   root,
		Branch: *branch,
		Check:  currentCheckReport,
		Runner: configuredRunner{agent: *agent},
	})
	if *jsonOut {
		writeJSON(stdout, result)
	} else if result.ChecksBefore != nil && result.ChecksBefore.Pass {
		fmt.Fprintln(stdout, "improve: baseline checks passed; no branch or handoff created")
	} else if err == nil {
		fmt.Fprintf(stdout, "improve: verified fixes committed on %s\ncommit: %s\nchanged: %v\n", result.Branch, result.Commit, result.ChangedFiles)
	} else {
		fmt.Fprintf(stdout, "improve: stopped on branch %s; no commit created\nhandoff: %s\n", result.Branch, result.Handoff.Dir)
	}
	if err != nil {
		fmt.Fprintln(stderr, "pika improve:", err)
		return 1
	}
	return 0
}

// configuredRunner delays contract-agent validation until Pika has confirmed
// that a failed baseline actually needs a repair handoff.
type configuredRunner struct {
	agent string
}

func (r configuredRunner) Run(ctx context.Context, root, promptPath, outputPath string) error {
	runner, err := configuredCodexRunner(r.agent)
	if err != nil {
		return err
	}
	return runner.Run(ctx, root, promptPath, outputPath)
}

func configuredCodexRunner(agent string) (improve.Runner, error) {
	c, err := contract.Load(defaultContractPath)
	if err != nil {
		return nil, err
	}
	configured, ok := c.Agents[agent]
	if !ok {
		return nil, fmt.Errorf("agent %q is not configured in .project/contract.yaml", agent)
	}
	if configured.Runtime != "codex" {
		return nil, fmt.Errorf("agent %q uses runtime %q; `pika improve` requires runtime codex", agent, configured.Runtime)
	}
	return improve.CodexRunner{Model: configured.Model, Effort: configured.Effort}, nil
}

func currentCheckReport() (*verify.Report, error) {
	var stdout, stderr bytes.Buffer
	code := runCheck([]string{"--all", "--json"}, nil, &stdout, &stderr)
	if code == 2 {
		return nil, errors.New(stderr.String())
	}
	var report verify.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		return nil, fmt.Errorf("parse check report: %w", err)
	}
	return &report, nil
}

func hasFailedGate(report *verify.Report) bool {
	for _, gate := range report.Gates {
		if gate.Status == verify.StatusFail {
			return true
		}
	}
	return false
}

func writeJSON(out io.Writer, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintln(out, `{"error":"could not encode result"}`)
		return
	}
	fmt.Fprintln(out, string(data))
}
