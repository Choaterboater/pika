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
	"time"

	"github.com/Choaterboater/pika/internal/adapters"
	"github.com/Choaterboater/pika/internal/cliout"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/evidence"
	"github.com/Choaterboater/pika/internal/improve"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/workrec"
)

const defaultImproveBranch = "chore/pika-improve"

// runHandoff implements `pika handoff [--agent <name>] [--json]
// [--root <dir>]`. It is the explicit agent stage used by improve and can
// also be run independently when a caller wants to inspect the private
// bundle before acting on it.
func runHandoff(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agent := fs.String("agent", "builder", "contract agent name")
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
	runner, err := resolveRunner(root, *agent)
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
//
// It takes the same whole-repository run lease `pika work` does, and is
// refused in the same words. A handoff writes a bundle into the
// repository and spawns an agent inside the working tree, so a handoff
// running beside a run is two processes editing one tree — the exact
// hazard the lease exists to exclude. Not going through improve.Run is
// not a reason to hold nothing; it is only a second door into the same
// room.
func recordedHandoff(ctx context.Context, root *repopath.Root, agent string, report *verify.Report, runner improve.Runner) (handoff improve.Handoff, workID string, err error) {
	workID, err = evidence.NewWorkID(time.Now().UTC(), "handoff")
	if err != nil {
		return improve.Handoff{}, "", err
	}
	// Taken before anything is written, and carrying this run's id so a
	// refusal names a run `pika status` can look up. A handoff refused
	// because another run holds the repository has done nothing, so it
	// reports no work id either: there is no run to go and look at.
	leased, err := improve.TakeRunLease(root, workID)
	if err != nil {
		return improve.Handoff{}, "", err
	}
	defer func() { err = errors.Join(err, leased.Release()) }()
	// The baseline is already observed by the time this is reached: the
	// report handed in is what the ladder said, so the run is born with
	// its baseline phase already complete.
	handle, err := workrec.Create(root, workrec.Record{
		WorkID:   workID,
		Kind:     workrec.KindRepair,
		Phase:    workrec.PhaseBaseline,
		Baseline: report,
		Role:     "builder",
		Runtime:  runner.Runtime(),
		Agents: []workrec.RunAgent{{
			Role:    "builder",
			Agent:   agent,
			Runtime: runner.Runtime(),
		}},
		Phases: []workrec.PhaseStamp{{Phase: workrec.PhaseBaseline, At: time.Now().UTC()}},
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
	agent := fs.String("agent", "builder", "contract agent name")
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
	cfg, err := configuredRoles(root, *agent)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "improve", codeConfig, err.Error())
	}
	cfg.Root = root.Dir()
	cfg.Branch = *branch
	cfg.Check = func() (*verify.Report, error) { return currentCheckReport(root) }
	result, err := improve.Run(context.Background(), cfg)
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
	printRunResult(stdout, "improve", result, err)
	if err != nil {
		fmt.Fprintln(stderr, "pika improve:", err)
		return 1
	}
	return 0
}

// printRunResult writes the human-readable outcome of one lifecycle run.
// name is the command reporting it: `pika improve` and `pika work` drive
// the same state machine over the same Result and differ only in what
// they asked it to do, so one printer serves both rather than two that
// drift apart a branch at a time.
//
// Every branch names the run. The work id is the only handle an operator
// has on what just happened — it is the argument to `pika status` and
// the name of the receipt — so a text-mode run the caller cannot name
// reproduces the exact gap the durable record exists to close. `pika
// handoff` prints `run %s`; these say it the same way.
func printRunResult(stdout io.Writer, name string, result improve.Result, err error) {
	switch {
	case result.WorkID == "":
		// improve.Run refuses a dirty tree, an unknown work kind and
		// feature work with no goal before the record exists, and
		// returns a zero Result. There is no run to name and no branch
		// or bundle to report, so printing an empty run id would
		// invent the anonymous run the record abolished.
		fmt.Fprintf(stdout, "%s: refused before the run started; nothing was created\n", name)
	case err != nil:
		// A run that stopped is a run that stopped, whatever it had or
		// had not reached. This is read before the branch so a run that
		// died before branching is never mistaken for one that found
		// nothing to do.
		//
		// The bundle line is printed only when there is a bundle. A run
		// that stopped before its handoff has no path to give, and
		// `handoff: ` with nothing after it reads as a lost value
		// rather than an absent one — in the one report whose whole job
		// is to say truthfully where the run got to.
		fmt.Fprintf(stdout, "%s: stopped on branch %s; run %s; no commit created\n", name, stoppedBranch(result), result.WorkID)
		if result.Handoff.Dir != "" {
			fmt.Fprintf(stdout, "handoff: %s\n", result.Handoff.Dir)
		}
	case result.Branch == "":
		// Nothing was attempted. The branch is the marker: the
		// lifecycle creates it immediately before the handoff and
		// records it before the agent runs, so a run without one
		// spawned no agent and committed nothing.
		//
		// The predicate used to be ChecksBefore.Pass, which is the
		// deciding fact for repair work only — the lifecycle returns
		// early on a green baseline just when the kind is repair, so a
		// delivered feature run kept ChecksBefore.Pass == true and was
		// reported here, at exit 0, as having created no branch. Branch
		// reads off what the run did instead of what its baseline
		// predicted, and it is the same predicate
		// internal/improve/receipt.go names attemptedWork, so the two
		// agree by construction rather than by coincidence.
		//
		// With the error already handled above, the lifecycle's only
		// remaining exit without a branch is the green baseline a
		// repair run stops on, so that is what this says.
		fmt.Fprintf(stdout, "%s: baseline checks passed; run %s; no branch or handoff created\n", name, result.WorkID)
	default:
		fmt.Fprintf(stdout, "%s: verified work committed on %s; run %s\ncommit: %s\nchanged: %v\n", name, result.Branch, result.WorkID, result.Commit, result.ChangedFiles)
	}
}

// stoppedBranch names the branch a stopped run was actually on.
//
// improve.Result carries two branches and they are not the same claim.
// StoppedOn is read from Git as the run is closed out, so it is what the
// repository was really on at the end; Branch is the branch the run set
// out to work in, and stays empty until the run reaches it. Reporting
// Branch alone is how a run that stopped before it ever branched came to
// print `stopped on branch -`: the report's one job is to say where the
// run stopped, and it said nothing.
//
// The dash survives for the case where neither field knows, which is a
// repository this process could not read at all — a placeholder for a
// question with no answer, not a stand-in for an answer nobody looked
// for.
func stoppedBranch(result improve.Result) string {
	if result.StoppedOn != "" {
		return result.StoppedOn
	}
	return orDash(result.Branch)
}

// configuredRunner delays the agent's own validation until Pika has
// confirmed that a failed baseline actually needs a repair handoff.
//
// The delay is deliberate and it is why Runtime is separate from Run: a
// repository whose ladder is already green must not be failed by a
// misconfigured agent it was never going to spawn, and a record still has
// to name the runtime before anything is spawned.
type configuredRunner struct {
	root  *repopath.Root
	agent string
}

// Runtime reports the runtime the contract names for this agent. It
// resolves the contract and nothing heavier, so naming the runtime never
// costs a process.
func (r configuredRunner) Runtime() string {
	agent, err := contractAgent(r.root, r.agent)
	if err != nil {
		return ""
	}
	return agent.Runtime
}

func (r configuredRunner) Run(ctx context.Context, root, promptPath, outputPath string) error {
	runner, err := resolveRunner(r.root, r.agent)
	if err != nil {
		return err
	}
	return runner.Run(ctx, root, promptPath, outputPath)
}

// contractAgent resolves one contract agent without building a runner, so
// a caller can ask what an agent is without being committed to spawning
// it.
func contractAgent(root *repopath.Root, name string) (adapters.Agent, error) {
	c, err := contract.Load(root.Contract())
	if err != nil {
		return adapters.Agent{}, err
	}
	return adapters.AgentFromContract(c, root.Contract(), name)
}

// resolveRunner builds the runner for one contract agent.
func resolveRunner(root *repopath.Root, name string) (adapters.Runner, error) {
	agent, err := contractAgent(root, name)
	if err != nil {
		return nil, err
	}
	return adapters.New(agent)
}

// configuredRoles is the cast a run is given: the builder the --agent
// flag names, plus the explorer and reviewer the contract declares under
// those keys when it declares them at all.
//
// The builder is lazy on purpose — a repository whose ladder is already
// green must not be failed by an agent it was never going to spawn —
// while the two optional roles are resolved now, because "does this run
// have an explorer" is a question the lifecycle has to answer before it
// can plan the phase at all.
func configuredRoles(root *repopath.Root, agent string) (improve.Config, error) {
	cfg := improve.Config{
		Builder: improve.Role{
			Name:   "builder",
			Agent:  agent,
			Runner: configuredRunner{root: root, agent: agent},
		},
	}
	explorer, err := optionalRole(root, "explorer")
	if err != nil {
		return cfg, err
	}
	cfg.Explorer = explorer
	reviewer, err := optionalRole(root, "reviewer")
	if err != nil {
		return cfg, err
	}
	cfg.Reviewer = reviewer
	return cfg, nil
}

// optionalRole resolves a role the contract may not declare. "Not
// configured" is not an error: it means the phase is skipped, which is
// what every contract written before M6 gets.
//
// Any other failure is. A contract that names an explorer on a runtime
// with no adapter, or with a model the runtime cannot express, is broken
// in a way the operator has to fix — and discovering it at the end of a
// run, after the builder has already been paid for, is the worst time to
// discover it.
func optionalRole(root *repopath.Root, name string) (*improve.Role, error) {
	c, err := contract.Load(root.Contract())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// A repository with no contract is a state every command
			// here has always reported at the handoff, in the words the
			// contract loader chose. Refusing now would move that
			// refusal ahead of the ones this command reports first —
			// an unknown work id, a run that already finished — and
			// reordering those refusals is a behaviour change for no
			// gain.
			return nil, nil
		}
		return nil, err
	}
	agent, err := adapters.AgentFromContract(c, root.Contract(), name)
	if err != nil {
		var notConfigured *adapters.NotConfiguredError
		if errors.As(err, &notConfigured) {
			return nil, nil
		}
		return nil, err
	}
	runner, err := adapters.New(agent)
	if err != nil {
		return nil, err
	}
	return &improve.Role{Name: name, Agent: name, Runner: runner}, nil
}

// currentCheckReport runs the in-process ladder against root. The --root
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
