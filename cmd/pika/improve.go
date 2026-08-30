package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Choaterboater/pika/internal/cliout"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/evidence"
	"github.com/Choaterboater/pika/internal/improve"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/workrec"
)

const defaultImproveBranch = "chore/pika-improve"

// codexRuntime is the only agent runtime pika spawns: configuredCodexRunner
// refuses a contract agent configured with any other, so this is the
// runtime both `pika handoff` and `pika improve` record for their runs.
const codexRuntime = "codex"

// runHandoff implements `pika handoff [--agent <name>] [--json]
// [--root <dir>]`. It is the explicit agent stage used by improve and can
// also be run independently when a caller wants to inspect the private
// bundle before acting on it.
func runHandoff(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agent := fs.String("agent", "builder", "contract agent name (must use the Codex runtime)")
	jsonOut := fs.Bool("json", false, "emit the handoff result as JSON")
	rootFlag := fs.String("root", "", rootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return fail(*jsonOut, stdout, stderr, "handoff", codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "handoff", codeConfig, err.Error())
	}
	report, err := currentCheckReport(root)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "handoff", codeConfig, err.Error())
	}
	if !hasFailedGate(report) {
		if *jsonOut {
			if !emitJSON(stdout, stderr, "handoff", true,
				map[string]any{"handoff": nil, "checks": report, "message": "no actionable failed check gates"}) {
				return 1
			}
		} else {
			fmt.Fprintln(stdout, "handoff: no actionable failed check gates")
		}
		return 0
	}
	runner, err := configuredCodexRunner(root, *agent)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "handoff", codeConfig, err.Error())
	}
	handoff, workID, err := recordedHandoff(context.Background(), root, *agent, report, runner)
	if err != nil {
		if *jsonOut && emitFailure(stdout, stderr, "handoff", err, map[string]any{"workId": workID}) {
			return 1
		}
		fmt.Fprintln(stderr, "pika handoff:", err)
		return 1
	}
	if *jsonOut {
		if !emitJSON(stdout, stderr, "handoff", true, map[string]any{"workId": workID, "handoff": handoff, "checks": report}) {
			return 1
		}
	} else {
		fmt.Fprintf(stdout, "handoff: Codex completed; run %s; bundle: %s\n", workID, handoff.Dir)
	}
	return 0
}

// recordedHandoff runs a standalone `pika handoff` as what it is: a short
// run. A handoff is one phase of the same lifecycle `pika improve` runs,
// so it gets the same durable record — created before the bundle is
// written, so the bundle has an identity, and closed with a terminal
// outcome on both exits. Without this the command minted a bundle at
// .project/state/handoffs/<unixnano>, which is exactly the anonymous
// directory the run record exists to abolish, reached through a second
// door.
//
// The record is created only once there is work to hand over: the
// no-failed-gate exit above returns before this is called, because a run
// refused before it does anything must leave nothing behind.
func recordedHandoff(ctx context.Context, root *repopath.Root, agent string, report *verify.Report, runner improve.Runner) (improve.Handoff, string, error) {
	workID, err := evidence.NewWorkID(time.Now().UTC(), "handoff")
	if err != nil {
		return improve.Handoff{}, "", err
	}
	// The baseline is already observed by the time this is reached: the
	// report handed in is what the ladder said, so the run is born with
	// its baseline phase already complete.
	handle, err := workrec.Create(root, workrec.Record{
		WorkID:   workID,
		Kind:     workrec.KindRepair,
		Phase:    workrec.PhaseBaseline,
		Baseline: report,
		Role:     agent,
		Runtime:  codexRuntime,
		Phases:   []workrec.PhaseStamp{{Phase: workrec.PhaseBaseline, At: time.Now().UTC()}},
	})
	if err != nil {
		return improve.Handoff{}, workID, err
	}
	handoff, runErr := improve.CreateHandoff(ctx, root.Dir(), handle.HandoffDir(), report, runner)
	rec := handle.Record()
	if runErr != nil {
		rec.Outcome = workrec.OutcomeBlocked
		rec.Reason = runErr.Error()
	} else {
		rec.Phase = workrec.PhaseHandoff
		rec.Phases = append(rec.Phases, workrec.PhaseStamp{Phase: workrec.PhaseHandoff, At: time.Now().UTC()})
		// `pika handoff` sets out to produce a bundle, not a commit. It
		// produced one, so the run is complete rather than abandoned.
		rec.Outcome = workrec.OutcomeComplete
	}
	if err := handle.Save(rec); err != nil {
		return handoff, workID, errors.Join(runErr, err)
	}
	return handoff, workID, runErr
}

// runImprove implements `pika improve [--branch <name>] [--agent <name>]
// [--json] [--root <dir>]`. The only Git mutation it performs is a local
// branch and verified local commit. Publishing remains a human choice.
func runImprove(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("improve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	branch := fs.String("branch", defaultImproveBranch, "local branch for verified fixes")
	agent := fs.String("agent", "builder", "contract agent name (must use the Codex runtime)")
	jsonOut := fs.Bool("json", false, "emit the improve result as JSON")
	rootFlag := fs.String("root", "", rootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return fail(*jsonOut, stdout, stderr, "improve", codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "improve", codeConfig, err.Error())
	}
	result, err := improve.Run(context.Background(), improve.Config{
		Root:    root.Dir(),
		Branch:  *branch,
		Agent:   *agent,
		Runtime: codexRuntime,
		Check:   func() (*verify.Report, error) { return currentCheckReport(root) },
		Runner:  configuredRunner{root: root, agent: *agent},
	})
	if *jsonOut {
		// The result is the payload on both paths: a run that stopped
		// still has to say which branch it stopped on and where the
		// handoff bundle is.
		if err != nil {
			if !emitFailure(stdout, stderr, "improve", err, result) {
				fmt.Fprintln(stderr, "pika improve:", err)
			}
			return 1
		}
		if !emitJSON(stdout, stderr, "improve", true, result) {
			return 1
		}
		return 0
	}
	if result.ChecksBefore != nil && result.ChecksBefore.Pass {
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
	root  *repopath.Root
	agent string
}

func (r configuredRunner) Run(ctx context.Context, root, promptPath, outputPath string) error {
	runner, err := configuredCodexRunner(r.root, r.agent)
	if err != nil {
		return err
	}
	return runner.Run(ctx, root, promptPath, outputPath)
}

func configuredCodexRunner(root *repopath.Root, agent string) (improve.Runner, error) {
	c, err := contract.Load(root.Contract())
	if err != nil {
		return nil, err
	}
	configured, ok := c.Agents[agent]
	if !ok {
		return nil, fmt.Errorf("agent %q is not configured in %s", agent, root.Contract())
	}
	if configured.Runtime != codexRuntime {
		return nil, fmt.Errorf("agent %q uses runtime %q; `pika improve` requires runtime codex", agent, configured.Runtime)
	}
	return improve.CodexRunner{Model: configured.Model, Effort: configured.Effort}, nil
}

// currentCheckReport runs the in-process ladder against root. The --root
// is passed explicitly so handoff and improve verify the same repository
// they are about to mutate, whatever the working directory is.
func currentCheckReport(root *repopath.Root) (*verify.Report, error) {
	var stdout, stderr bytes.Buffer
	code := runCheck([]string{"--all", "--json", "--root", root.Dir()}, nil, &stdout, &stderr)
	var env cliout.Envelope
	if code == 2 {
		// check reports its own usage and configuration errors inside
		// the envelope, so the reason travels with the payload; stderr
		// is only the fallback for an envelope that never landed.
		if err := json.Unmarshal(stdout.Bytes(), &env); err == nil && env.Error != nil {
			return nil, fmt.Errorf("check: %s", env.Error.Message)
		}
		return nil, errors.New(stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		return nil, fmt.Errorf("parse check report: %w", err)
	}
	var report verify.Report
	if err := json.Unmarshal(env.Result, &report); err != nil {
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
