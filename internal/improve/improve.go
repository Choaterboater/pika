package improve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Choaterboater/pika/internal/evidence"
	"github.com/Choaterboater/pika/internal/lease"
	"github.com/Choaterboater/pika/internal/repolease"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/workrec"
)

// ErrDirtyTree prevents Pika from mixing an automated repair with work the
// caller has not committed yet.
var ErrDirtyTree = errors.New("improve: working tree must be clean")

// ErrRunInProgress refuses a second mutating run in a repository that
// already has one.
//
// The two runs share one working tree and one HEAD, so the second does
// not have to do anything unusual to corrupt the first — it only has to
// start. On the default branch the pair collide non-deterministically at
// `git switch -c`; given distinct branches nothing collides and both
// proceed, committing each other's half-finished edits through the same
// HEAD without either being told. The silent case is the one this
// refusal exists for.
var ErrRunInProgress = errors.New("improve: another run holds this repository")

// ErrScopeLeaseHeld refuses a run because an MCP session holds a scope
// lease somewhere inside this repository.
//
// It is a separate sentinel from ErrRunInProgress because the holder is
// a separate thing. "Another run holds this repository" sends an
// operator looking for a run with `pika status`, they find none, and
// they conclude the refusal was lying — when what is actually in the
// tree is an agent harness driving `pika mcp`, writing files under a
// path it leased. The refusal has to name what is really there.
//
// A run is refused by it because a run commits through the whole working
// tree, including the path that lease covers: the run's branch switch
// moves files the session is editing, and the run's `git add` sweeps up
// whatever the session has written so far.
var ErrScopeLeaseHeld = errors.New("improve: a scope lease holds part of this repository")

// ErrNoChanges prevents a misleading empty commit after an agent run.
var ErrNoChanges = errors.New("improve: Codex made no changes to commit")

// ErrBranchHoldsWork refuses a fresh run whose branch is already in the
// repository and carries commits the run's own starting point does not.
//
// A run that stops leaves its branch behind: `git switch -c` is the
// first thing the lifecycle does with it and nothing on the failure path
// deletes it. Before this refusal existed the leftover was fatal in the
// most useless way available — the next `pika improve` in that
// repository died on Git's own `a branch named 'chore/pika-improve'
// already exists`, exit 128, naming no remedy — and every run after it
// died the same way until an operator guessed that the fix was to delete
// a branch by hand.
//
// Deleting it automatically is the other wrong answer. A run can stop
// after its commit has landed — the receipt fails to write, the terminal
// save fails, the process is killed between the commit and the stamp —
// and the branch is then the only place that work exists. Pika does not
// destroy work it cannot prove is worthless: `pika recover` clears only
// a holder it can show is dead, and evidence publication refuses to
// overwrite. So a branch carrying commits gets this refusal, and the
// refusal names the remedy rather than leaving the operator to find one.
var ErrBranchHoldsWork = errors.New("improve: the run branch already exists and holds work")

// ErrPrivateStateMoved refuses a run whose agent moved Pika's private
// state out of the subtree that protects it.
//
// changePaths filters by path, so it can only protect state that is still
// where Pika put it. `git mv .project/state/work/<id>/record.json
// leaked.json` defeats a path filter outright: Git reports the rename on
// one line naming both sides, the destination is an ordinary path the
// filter has no reason to reject, and the private content rides into the
// commit inside it. Both sides are read for that reason, and the answer
// is a refusal rather than a silently dropped path — dropping half a
// rename would leave the tree in a shape the operator never asked for,
// and what is under `.project/state` is not a file Pika can quietly
// decline to commit and call the matter closed.
var ErrPrivateStateMoved = errors.New("improve: the agent moved Pika's private state out of " + privateStateDir)

// The three worlds `pika resume` refuses, named separately. Each one
// leaves an operator with a different decision to make — a finished run
// wants `pika status`, a vanished branch wants a new run, a moved
// repository wants them to look at what moved — and a single "cannot
// resume" would tell them none of that.
var (
	// ErrRunFinished refuses a run that already recorded a terminal
	// outcome. Resume continues an interrupted run; a finished one has
	// nothing left to continue.
	ErrRunFinished = errors.New("improve: run already reached a terminal outcome")

	// ErrBranchGone refuses a run whose recorded branch is no longer in
	// the repository. That branch held everything the run had done and
	// not yet committed; without it there is nothing to rejoin, and
	// recreating it would produce an empty branch standing in for the
	// run's work.
	ErrBranchGone = errors.New("improve: the run's branch is no longer in this repository")

	// ErrTreeDiverged refuses a run whose base commit is no longer HEAD.
	// Every phase the record describes was observed against that commit;
	// rejoining on top of a different one would verify and commit
	// against a repository the run never saw.
	ErrTreeDiverged = errors.New("improve: the repository moved off the run's base commit")
)

// privateStateDir is everything Pika keeps local to the machine: the run
// record, the handoff bundle it contains, the envelope, the board.
//
// changePaths drops the whole subtree rather than naming the bundle
// directory. The bundle has already moved once — from
// .project/state/handoffs/<unixnano> into the run record — and a filter
// that names one directory silently stops protecting the thing it was
// written for the next time that thing moves. `.project/state` is also
// gitignored, so in a normally adopted repository Git never offers these
// paths to a commit at all; this filter is what holds when it is not, and
// when an agent force-adds them.
const privateStateDir = ".project/state"

// deliverMessage is the subject every commit a run delivers carries. The
// lifecycle writes it and the resume reconciliation reads it back, so
// naming it once is what keeps the two from drifting into a
// reconciliation that has quietly stopped recognising the commits this
// package actually makes.
const deliverMessage = "chore: improve verified findings"

// CheckFunc runs Pika's deterministic ladder and returns its full report.
// The command layer supplies the same in-process check engine used by
// `pika check`; tests provide real, controlled reports.
type CheckFunc func() (*verify.Report, error)

// Role binds a lifecycle role to the agent that plays it.
//
// Name is the role — builder, explorer or reviewer — and Agent is the
// contract key the agent was resolved from. They are separate because they
// legitimately differ: `--agent claude-builder` plays the builder role, and
// a record that recorded only the role could not say which contract entry
// ran.
type Role struct {
	Name   string
	Agent  string
	Runner Runner
}

// Config configures a single run of the lifecycle.
type Config struct {
	Root   string
	Branch string
	// Kind is workrec.KindRepair or workrec.KindFeature. The empty string
	// means repair, so an existing caller that only knows about repairs
	// keeps working unchanged.
	Kind string
	// Goal is the work statement handed to the agent. Feature work
	// requires one — that is the whole input for work the ladder cannot
	// describe. Repair work leaves it empty and takes its instructions
	// from the failed gates instead.
	Goal  string
	Check CheckFunc
	// Builder is the agent that does the work. It is the only required
	// role: a run with no builder has nothing to hand the tree to.
	Builder Role
	// Explorer is read-only research the builder is given before it
	// starts. Nil means the explore phase is skipped, which is what every
	// contract that names no explorer gets.
	Explorer *Role
	// Reviewer reads the verified result before the commit. Nil means the
	// review phase is skipped. A review is advisory: it is recorded and
	// never gates the commit, because the ladder is the evidence and prose
	// is not a gate.
	Reviewer *Role
}

// Result is the complete local outcome. Any error return may still include a
// work id, branch, handoff bundle, and baseline report so the caller can
// inspect the uncommitted state without Pika concealing it.
type Result struct {
	WorkID       string   `json:"workId,omitempty"`
	Branch       string   `json:"branch,omitempty"`
	Commit       string   `json:"commit,omitempty"`
	ChangedFiles []string `json:"changedFiles,omitempty"`
	Handoff      Handoff  `json:"handoff,omitempty"`
	// Review is the advisory review bundle, present only when a reviewer
	// ran. It is separate from Handoff because it is a different agent's
	// bundle about a different question: the builder's is the work, the
	// reviewer's is what it thinks of the work.
	Review       Handoff        `json:"review,omitempty"`
	ChecksBefore *verify.Report `json:"checksBefore,omitempty"`
	ChecksAfter  *verify.Report `json:"checksAfter,omitempty"`
	// StoppedOn is the branch the repository was actually on when the
	// run stopped, read from Git as the run is closed out rather than
	// inferred from how far it got.
	//
	// It is a separate field from Branch because it is a separate fact.
	// Branch is the branch the run set out to work in and stays empty
	// until the run reaches it, so a failure earlier than that had no
	// branch to report and the report said `stopped on branch -` — a
	// line whose one job is to say where the run stopped, saying
	// nothing. The two also legitimately disagree: an agent that
	// switched branches during its handoff makes the branch in effect
	// something other than the run's own, and that disagreement is the
	// most useful thing the report can say.
	StoppedOn string `json:"stoppedOn,omitempty"`
}

// Run executes the safe local lifecycle: verify, hand an agent only the
// work it is allowed to do, re-verify, and commit only what the ladder
// proved. It intentionally contains no network, push, PR, or merge
// operation: the successful commit remains on the local branch for the
// caller to review and publish separately.
//
// Every transition is saved to a durable run record under
// .project/state/work/<work-id>/ before the next one begins, and every
// exit path records a terminal outcome. An interrupted run therefore
// leaves a record that names its branch, its bundle and the last phase
// that completed, instead of an anonymous directory nothing can read.
//
// Repair and feature work share this one state machine, and differ in
// exactly one decision: a green ladder means repair work is already done,
// while it says nothing about whether a goal has been met, so feature
// work goes on to the agent.
func Run(ctx context.Context, cfg Config) (result Result, err error) {
	if strings.TrimSpace(cfg.Root) == "" {
		return Result{}, errors.New("improve: repository root is required")
	}
	if strings.TrimSpace(cfg.Branch) == "" {
		return Result{}, errors.New("improve: branch is required")
	}
	if cfg.Check == nil {
		return Result{}, errors.New("improve: check function is required")
	}
	kind := cfg.Kind
	if kind == "" {
		kind = workrec.KindRepair
	}
	switch kind {
	case workrec.KindRepair:
	case workrec.KindFeature:
		if strings.TrimSpace(cfg.Goal) == "" {
			return Result{}, errors.New("improve: feature work requires a goal")
		}
	default:
		return Result{}, fmt.Errorf("improve: unknown work kind %q", kind)
	}

	// Everything above this line, the dirty-tree refusal below it and
	// the lease after that happen before the run exists on disk. A run
	// refused before it has done anything must leave no record behind: a
	// directory of empty runs is noise that makes the real records
	// harder to trust.
	if dirty, err := runGit(ctx, cfg.Root, "status", "--porcelain"); err != nil {
		return Result{}, err
	} else if strings.TrimSpace(dirty) != "" {
		return Result{}, ErrDirtyTree
	}

	baseCommit, err := runGit(ctx, cfg.Root, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, err
	}
	workID, err := evidence.NewWorkID(time.Now().UTC(), kind)
	if err != nil {
		return Result{}, err
	}
	root, err := repopath.At(cfg.Root)
	if err != nil {
		return Result{}, err
	}
	// The exclusion. It is taken after the dirty-tree gate — a run
	// refused for uncommitted work never needed the repository — and
	// before the first thing that writes to it, which is the branch the
	// lifecycle creates. It carries the run id so a refusal can name a
	// run `pika status` can look up, and it is held until settle has
	// recorded a terminal outcome.
	held, err := TakeRunLease(root, workID)
	if err != nil {
		return Result{}, err
	}
	defer func() { err = errors.Join(err, held.Release()) }()

	handle, err := workrec.Create(root, workrec.Record{
		WorkID:     workID,
		Goal:       cfg.Goal,
		Kind:       kind,
		BaseCommit: strings.TrimSpace(baseCommit),
		Role:       cfg.Builder.Name,
		Runtime:    builderRuntime(cfg),
	})
	if err != nil {
		return Result{}, err
	}

	result, runErr := lifecycle(ctx, cfg, root, kind, handle, stageBaseline, "")
	result.WorkID = workID
	// A fresh run's receipt cannot already exist: the work id is new and
	// workrec.Create has already refused to reuse a run directory. One
	// that is there anyway is a collision, so settle reports it rather
	// than writing over it.
	return settle(ctx, root, handle, result, runErr, false)
}

// builderRuntime is the runtime the builder will run under, read from the
// runner rather than carried as a second field. It is empty when the
// builder has no runner yet, which is the case a run without one is about
// to refuse.
func builderRuntime(cfg Config) string {
	if cfg.Builder.Runner == nil {
		return ""
	}
	return cfg.Builder.Runner.Runtime()
}

// TakeRunLease claims the repository for one run, refusing rather than
// waiting or stealing when anything already holds ground inside it.
//
// Neither alternative is available. Waiting would make a run that has
// stopped for a reason indistinguishable from one that hung, and an
// operator staring at a silent terminal has no way to tell which they
// have. Stealing is the defect itself: the lease's whole content is the
// promise that one process is in the tree, and a second that takes it
// anyway has only added a lock file to the corruption.
//
// It is exported because `pika handoff` drives one phase of this
// lifecycle without going through Run: it writes a bundle into the
// repository and spawns an agent in the working tree, which is the
// hazard this lease exists to exclude, reached through a second door.
//
// The exclusion it asks for is the whole repository, so an MCP session's
// scope lease refuses it too — a run commits through the working tree
// every scope sits in. internal/repolease owns that judgement, because
// the two exclusions have to be decided by one rule or they are not one
// exclusion at all.
func TakeRunLease(root *repopath.Root, workID string) (*lease.Handle, error) {
	handle, err := repolease.TakeRun(root, workID)
	var busy *repolease.ConflictError
	if errors.As(err, &busy) {
		return nil, refuseHeld(busy.Held)
	}
	if err != nil {
		return nil, err
	}
	return handle, nil
}

// refuseHeld turns a lost race into a refusal an operator can act on.
// The states lease.Inspect distinguishes leave three different decisions
// to make, and a single "already running" would tell them none of it:
//
//   - Held is a live holder on this machine. Whether to wait for it or
//     stop it is that operator's call, and both are things they can go
//     and do with the pid in front of them.
//   - Stale is a lease that outlived whatever took it: the holder's
//     process is gone and its host is this one, so `pika recover` is
//     the remedy. It is still not taken automatically — recovering is a
//     decision, not a retry.
//   - Unverifiable is a holder this machine cannot judge. A pid that is
//     dead here can be very much alive on the host that recorded it, so
//     this is never called stale: that word is exactly what would
//     invite an operator to clear a lock a live writer still holds.
//
// Which sentinel the refusal wraps follows what is actually holding the
// ground, not which command was refused. An MCP session's scope lease is
// not "another run", and an operator sent looking for one with `pika
// status` would find nothing and conclude the message was lying.
func refuseHeld(h repolease.Held) error {
	held := ErrRunInProgress
	if h.Kind == repolease.KindScope {
		held = ErrScopeLeaseHeld
	}
	if h.Info == nil {
		return fmt.Errorf("%w: %s is claimed but names no readable holder (%v); `pika recover` clears a lease no process is behind",
			held, h.Path, h.Err)
	}
	who := h.Describe()
	switch {
	case h.State == lease.StateStale:
		return fmt.Errorf("%w: %s is no longer running and never released its lease; `pika recover` clears it",
			held, who)
	case h.State == lease.StateUnverifiable:
		return fmt.Errorf("%w: %s, and %s, so whether that process is still running cannot be decided here; run `pika recover` only once you know it stopped",
			held, who, unverifiableBecause(h.Err))
	case h.Kind == repolease.KindScope:
		return fmt.Errorf("%w: %s, and a run commits through the whole working tree, including the path that lease covers; wait for that session to release it or end",
			held, who)
	default:
		return fmt.Errorf("%w: %s is in progress; one repository runs one run at a time, because both would commit through the same working tree",
			held, who)
	}
}

// unverifiableBecause names why a holder could not be judged. Almost
// always it is a foreign host; the other way to get here is this machine
// failing to say what it is called, and an operator told "a different
// host" when the truth is "no host could be resolved" would go looking
// for a machine that does not exist.
func unverifiableBecause(err error) string {
	if err != nil {
		return fmt.Sprintf("this host could not be resolved (%v)", err)
	}
	return "that is not this host"
}

// stage names a point in the lifecycle a run can be entered at. Run
// enters at stageBaseline and walks all of it; Resume enters at whichever
// stage the record and Git together prove the interrupted run reached.
//
// Explore and review sit where they do because each is optional and each
// feeds the phase beside it: explore precedes the builder's handoff so its
// findings can be part of the builder's prompt, and review follows the
// recheck so it reads a result the ladder has already passed.
type stage int

const (
	stageBaseline stage = iota
	stageExplore
	stageHandoff
	stageRecheck
	stageReview
)

// resumedNote labels every phase stamp a resumed run writes. A resumed
// run legitimately stamps a phase twice — the recheck it repeats is the
// obvious one — and a history that cannot say which stamp came from which
// process is a history an operator has to guess at.
const resumedNote = "resumed"

// Resume rejoins an interrupted run and carries it to a terminal outcome.
//
// The hard part is that the record cannot say, by itself, whether a run
// is still in flight. A record carrying a phase and no outcome is what a
// process that died mid-lifecycle leaves behind — and it is also, bit for
// bit, what a process leaves behind when it finished the work and then
// failed to write its terminal outcome. Phase and Outcome together do not
// distinguish those two worlds, so Resume never decides from them alone.
//
// Git is the ground truth and the record only narrows the search. The
// decisive question is whether the run's work is already committed, and
// deliveredCommit puts it to Git rather than to the phase stamp — which
// is written after the commit has already moved the branch, and so is
// exactly the thing a crash can lose. Where Git proves the work landed,
// the only thing missing is the record's own catching up, so Resume
// writes that and stops. Re-running the lifecycle there would redo work
// the repository already contains.
//
// Everything else that is not a clean continuation is a refusal with a
// specific reason. Resuming into a changed world silently is worse than
// refusing to resume at all.
//
// The record is the authority on what work the run is: its kind, its goal
// and its branch come from the record, never from cfg. cfg supplies only
// the machinery to do the work — the ladder, the agent runner, and a
// branch name for a run interrupted before it ever had one. Taking the
// kind or the goal from the caller would let a resume quietly turn a
// repair into a feature, or hand the agent a goal the run never had.
func Resume(ctx context.Context, root, workID string, cfg Config) (result Result, err error) {
	if strings.TrimSpace(root) == "" {
		return Result{}, errors.New("improve: repository root is required")
	}
	if cfg.Check == nil {
		return Result{}, errors.New("improve: check function is required")
	}
	repo, err := repopath.At(root)
	if err != nil {
		return Result{}, err
	}
	handle, err := workrec.Open(repo, workID)
	if err != nil {
		return Result{}, err
	}
	rec := handle.Record()
	if rec.Outcome != "" {
		return Result{}, fmt.Errorf("%w: %s ended as %q; resume continues an interrupted run, never a finished one",
			ErrRunFinished, workID, rec.Outcome)
	}

	cfg.Root = root
	cfg.Goal = rec.Goal
	kind := rec.Kind
	if kind == "" {
		kind = workrec.KindRepair
	}
	if rec.Branch != "" {
		cfg.Branch = rec.Branch
	}
	if strings.TrimSpace(cfg.Branch) == "" {
		return Result{}, errors.New("improve: branch is required")
	}

	// A resumed run walks into the same working tree a fresh one does,
	// so it takes the same exclusion, and takes it before it asks Git
	// anything at all.
	//
	// The crashed run's own lease is not inherited. A lease this process
	// did not take is one it cannot prove nobody is behind — the record
	// that says "interrupted" is bit for bit what a run still working
	// leaves — and picking it up on the strength of a matching work id
	// is how a resume joins a run that never stopped. `pika recover` is
	// where that judgement belongs, with an operator behind it.
	held, err := TakeRunLease(repo, workID)
	if err != nil {
		return Result{}, err
	}
	defer func() { err = errors.Join(err, held.Release()) }()

	if rec.Branch != "" {
		branchHead, exists, err := branchCommit(ctx, root, rec.Branch)
		if err != nil {
			return Result{}, err
		}
		if !exists {
			return Result{}, fmt.Errorf("%w: %s recorded branch %q, which no longer exists; the work it carried is gone and resume will not recreate it",
				ErrBranchGone, workID, rec.Branch)
		}
		// The deliver reconciliation, and the reason Resume asks Git
		// anything at all. Git decides whether this run's work already
		// landed; the record only says where to look. This runs before
		// the base-commit guard below because a delivered run has by
		// definition moved past its base commit — refusing it there
		// would refuse the one case Git has settled.
		delivered, err := deliveredCommit(ctx, root, rec, branchHead)
		if err != nil {
			return Result{}, err
		}
		if delivered != "" {
			// The record catches up to what Git proved. A run
			// reconciled at the deliver phase already names this commit
			// and its history is complete; a run reconciled inside the
			// deliver window never wrote either, so the stamp it lost is
			// written now — marked resumed, like every stamp a resumed
			// run makes.
			if rec.Phase != workrec.PhaseDeliver {
				if err := savePhase(handle, workrec.PhaseDeliver, resumedNote, func(rec *workrec.Record) {
					rec.Commit = delivered
				}); err != nil {
					return Result{}, err
				}
			}
			return settle(ctx, repo, handle, recordResult(handle.Record()), nil, true)
		}
	}

	head, err := runGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, err
	}
	// A record with no base commit cannot answer this question at all,
	// and an unanswerable guard is a refusal: resume would otherwise
	// rejoin a repository it cannot show is where the run left it.
	if now := strings.TrimSpace(head); now != rec.BaseCommit {
		return Result{}, fmt.Errorf("%w: %s started at %s and HEAD is now %s; resume will not rejoin a repository that moved underneath it",
			ErrTreeDiverged, workID, orNoBaseCommit(rec.BaseCommit), now)
	}

	result, runErr := lifecycle(ctx, cfg, repo, kind, handle, resumeStage(rec, cfg), resumedNote)
	result.WorkID = workID
	// This run's receipt may already be on disk: a run whose terminal
	// save failed had already issued one. Under the run's own id that is
	// the write being idempotent, not a collision.
	return settle(ctx, repo, handle, result, runErr, true)
}

// resumeStage is the point in the lifecycle a resumed run re-enters at.
// It skips exactly the phases whose product is durable, and repeats every
// phase whose product is a claim about the working tree — which no record
// can prove is still true.
//
//   - The baseline ladder is skipped when the record holds its report.
//     Re-running it now would run it over the agent's edits and record
//     the result as the baseline those edits came after, which is a lie
//     in the one document the receipt quotes.
//   - Explore is never resumed: its product is a prompt section the
//     builder already read, and the builder's own handoff is what has to
//     be repeated to use it again. A run interrupted in explore rejoins
//     at the builder.
//   - The handoff is skipped once it completed. It spawns the agent, and
//     spawning a second one to redo work already sitting in the tree is
//     the exact cost resume exists to avoid.
//   - The recheck is never skipped. "Commit only what the ladder proved"
//     has to be proved by this process against this tree; the record
//     proves only what another process saw in a tree nothing has vouched
//     for since.
//   - Review runs again whenever the recheck does: it reads the result
//     this process's ladder produced, and an advisory finding about a tree
//     this process has not itself verified would be a finding about
//     nothing.
func resumeStage(rec workrec.Record, cfg Config) stage {
	switch rec.Phase {
	case workrec.PhaseHandoff, workrec.PhaseRecheck, workrec.PhaseDeliver, workrec.PhaseReview:
		return stageRecheck
	case workrec.PhaseExplore:
		return stageHandoff
	case workrec.PhaseBaseline:
		if rec.Baseline == nil {
			// The stamp says the ladder ran and the record has no report
			// to show for it. Git and the record are the only evidence
			// resume has, so where they disagree it takes the phase it
			// can prove: run the ladder.
			return stageBaseline
		}
		// Both optional phases are skipped when unconfigured, so a
		// default contract still stamps exactly the same four phases it
		// did before M6.
		if cfg.Explorer != nil {
			return stageExplore
		}
		return stageHandoff
	default:
		return stageBaseline
	}
}

// usageReporter is the optional interface a runner implements when it can
// say what the run spent. Subprocess runners cannot — the number would be
// a guess — so only the built-in loop reports, and the field stays absent
// for every other runtime.
type usageReporter interface {
	Usage() (calls, tokensIn, tokensOut int)
}

// usageOf returns the runner's reported usage, or zeroes when it does not
// report.
func usageOf(r Runner) (calls, tokensIn, tokensOut int) {
	if u, ok := r.(usageReporter); ok {
		return u.Usage()
	}
	return 0, 0, 0
}

// lifecycle is the run itself, from the baseline ladder to the verified
// commit. It never records the terminal outcome: every one of its exits
// is a terminal outcome, so settle owns that single write and no exit
// path can forget it.
//
// from is where the run enters, and note labels the phase stamps it
// writes. Run enters at stageBaseline with no note; Resume enters where
// the record and Git prove the interrupted run got to, inherits the
// baseline report the record already holds, and marks everything it
// stamps as resumed.
func lifecycle(ctx context.Context, cfg Config, repo *repopath.Root, kind string, handle *workrec.Handle, from stage, note string) (Result, error) {
	// What the record already proved. A resumed run must not re-derive
	// its baseline: by the time it rejoins, the agent's edits are in the
	// working tree, and a ladder run over them is not the baseline they
	// came after — it is the state they produced, filed under the name of
	// the state they replaced.
	rec := handle.Record()
	result := Result{Branch: rec.Branch, ChecksBefore: rec.Baseline}

	if from == stageBaseline {
		before, err := cfg.Check()
		if err != nil {
			return result, fmt.Errorf("improve: baseline checks: %w", err)
		}
		if before == nil {
			return result, errors.New("improve: baseline checks returned no report")
		}
		result.ChecksBefore = before
		if err := savePhase(handle, workrec.PhaseBaseline, note, func(rec *workrec.Record) {
			rec.Baseline = before
		}); err != nil {
			return result, err
		}
	}

	if from <= stageHandoff {
		if result.ChecksBefore == nil {
			return result, errors.New("improve: baseline checks returned no report")
		}
		// The one decision the two kinds do not share.
		if result.ChecksBefore.Pass && kind == workrec.KindRepair {
			return result, nil
		}

		if cfg.Builder.Runner == nil {
			return result, errors.New("improve: a builder runner is required")
		}
		// A fresh run claims its branch, which is also where a run that
		// stopped earlier left one behind; claimBranch decides whether
		// that leftover carries anything before it reuses it. A resumed
		// run reconciles instead: its branch may already be there
		// because the interrupted process created it and died before
		// the record could name it.
		if from == stageBaseline {
			if err := claimBranch(ctx, repo, cfg.Root, cfg.Branch, rec.BaseCommit); err != nil {
				return result, err
			}
		} else if err := enterBranch(ctx, cfg.Root, cfg.Branch); err != nil {
			return result, err
		}
		result.Branch = cfg.Branch
		// The branch is recorded before the agent runs, not after. It
		// exists from this instant, and a crash while the agent works is
		// precisely the case where the record has to be able to name it.
		if err := saveRecord(handle, func(rec *workrec.Record) {
			rec.Branch = cfg.Branch
		}); err != nil {
			return result, err
		}

		// The explore phase: read-only research handed to the builder as
		// a section of its own prompt. It runs before the builder and
		// never on a resumed run that had already reached the builder,
		// because its only product is that section.
		var findings string
		if cfg.Explorer != nil && from <= stageExplore {
			explore, err := createHandoff(ctx, cfg.Root,
				filepath.Join(handle.HandoffDir(), "explore"),
				buildExplorePrompt(cfg.Goal, failedGates(result.ChecksBefore)),
				result.ChecksBefore, cfg.Explorer.Runner)
			if err != nil {
				return result, err
			}
			if err := requireNoNewChanges(ctx, cfg.Root, cfg.Explorer.Name, nil); err != nil {
				return result, err
			}
			findings = readFindings(explore.ResultPath)
			if err := savePhase(handle, workrec.PhaseExplore, note, func(rec *workrec.Record) {
				calls, tokensIn, tokensOut := usageOf(cfg.Explorer.Runner)
				rec.Agents = append(rec.Agents, workrec.RunAgent{
					Role:      cfg.Explorer.Name,
					Agent:     cfg.Explorer.Agent,
					Runtime:   cfg.Explorer.Runner.Runtime(),
					Calls:     calls,
					TokensIn:  tokensIn,
					TokensOut: tokensOut,
				})
			}); err != nil {
				return result, err
			}
		}

		handoff, err := createHandoff(ctx, cfg.Root, handle.HandoffDir(),
			buildBuilderPrompt(cfg.Goal, failedGates(result.ChecksBefore), findings),
			result.ChecksBefore, cfg.Builder.Runner)
		result.Handoff = handoff
		if err != nil {
			return result, err
		}
		if err := savePhase(handle, workrec.PhaseHandoff, note, func(rec *workrec.Record) {
			rec.Role = cfg.Builder.Name
			rec.Runtime = cfg.Builder.Runner.Runtime()
			calls, tokensIn, tokensOut := usageOf(cfg.Builder.Runner)
			rec.Agents = append(rec.Agents, workrec.RunAgent{
				Role:      cfg.Builder.Name,
				Agent:     cfg.Builder.Agent,
				Runtime:   cfg.Builder.Runner.Runtime(),
				Calls:     calls,
				TokensIn:  tokensIn,
				TokensOut: tokensOut,
			})
		}); err != nil {
			return result, err
		}
	} else {
		// A run rejoined after its handoff has to be standing on its own
		// branch before it verifies or commits anything.
		if err := enterBranch(ctx, cfg.Root, cfg.Branch); err != nil {
			return result, err
		}
		result.Branch = cfg.Branch
	}

	state, err := currentGitState(ctx, cfg.Root)
	if err != nil {
		return result, err
	}
	if state.Branch != cfg.Branch {
		return result, fmt.Errorf("improve: expected branch %q after handoff, found %q", cfg.Branch, state.Branch)
	}
	if _, err := runGit(ctx, cfg.Root, "reset"); err != nil {
		return result, err
	}
	after, err := cfg.Check()
	if err != nil {
		return result, fmt.Errorf("improve: post-handoff checks: %w", err)
	}
	if after == nil {
		return result, errors.New("improve: post-handoff checks returned no report")
	}
	result.ChecksAfter = after
	if err := savePhase(handle, workrec.PhaseRecheck, note, func(rec *workrec.Record) {
		rec.Recheck = after
	}); err != nil {
		return result, err
	}
	if !after.Pass {
		return result, errors.New("improve: post-handoff checks failed; changes left uncommitted")
	}

	entries, err := readStatus(ctx, cfg.Root)
	if err != nil {
		return result, err
	}
	if moved := privateStateMoved(entries); moved != "" {
		return result, fmt.Errorf("%w: %s", ErrPrivateStateMoved, moved)
	}
	result.ChangedFiles = changePaths(entries)
	if len(result.ChangedFiles) == 0 {
		return result, ErrNoChanges
	}
	// The review phase: an independent read of the verified result,
	// after the ladder has passed and before the commit that would make
	// it permanent. It is advisory in the strongest sense available —
	// its finding is recorded in the receipt and it never changes the
	// outcome, because pika's own rule is that the ladder is the evidence
	// and prose is not a gate. A reviewer that could block a green ladder
	// would be a second gate that is not deterministic.
	if cfg.Reviewer != nil && from <= stageReview {
		review, err := createHandoff(ctx, cfg.Root,
			filepath.Join(handle.HandoffDir(), "review"),
			buildReviewPrompt(cfg.Goal, result.ChecksBefore, after, result.ChangedFiles, readFindings(result.Handoff.ResultPath)),
			after, cfg.Reviewer.Runner)
		result.Review = review
		if err != nil {
			return result, err
		}
		if err := requireNoNewChanges(ctx, cfg.Root, cfg.Reviewer.Name, result.ChangedFiles); err != nil {
			return result, err
		}
		if err := savePhase(handle, workrec.PhaseReview, note, func(rec *workrec.Record) {
			calls, tokensIn, tokensOut := usageOf(cfg.Reviewer.Runner)
			rec.Agents = append(rec.Agents, workrec.RunAgent{
				Role:      cfg.Reviewer.Name,
				Agent:     cfg.Reviewer.Agent,
				Runtime:   cfg.Reviewer.Runner.Runtime(),
				Calls:     calls,
				TokensIn:  tokensIn,
				TokensOut: tokensOut,
			})
		}); err != nil {
			return result, err
		}
	}
	state, err = currentGitState(ctx, cfg.Root)
	if err != nil {
		return result, err
	}
	if state.Branch != cfg.Branch {
		return result, fmt.Errorf("improve: expected branch %q before commit, found %q", cfg.Branch, state.Branch)
	}
	if err := stageChanges(ctx, cfg.Root, result.ChangedFiles); err != nil {
		return result, err
	}
	if _, err := runGit(ctx, cfg.Root, "commit", "-m", deliverMessage); err != nil {
		return result, err
	}
	commit, err := runGit(ctx, cfg.Root, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	result.Commit = strings.TrimSpace(commit)
	if err := savePhase(handle, workrec.PhaseDeliver, note, func(rec *workrec.Record) {
		rec.Commit = result.Commit
	}); err != nil {
		return result, err
	}
	return result, nil
}

// settle closes a run: it records the terminal outcome and issues the
// receipt from the finished record. Every run ends here, started by Run
// or rejoined by Resume, so neither can end without one.
//
// The receipt is issued last, from the finished record, so what it
// attests is the run's terminal state — a blocked run included. A failed
// receipt is joined to the run's own error for the same reason a failed
// terminal save is: the reason a run stopped is the more useful of the
// two facts, so it is never replaced.
//
// resuming allows the one difference between the two callers. A receipt
// already on disk under a resumed run's own id is that run's receipt — a
// run whose terminal save failed had already issued it — so writing it
// again is a no-op rather than a collision.
func settle(ctx context.Context, root *repopath.Root, handle *workrec.Handle, result Result, runErr error, resuming bool) (Result, error) {
	// Where the run stopped is read here, from the run's own error and
	// before anything else is written: settle is the one point every
	// exit passes through, and nothing between the stop and this line
	// moves a branch. A run that ended well has not stopped anywhere and
	// is not asked.
	if runErr != nil {
		result.StoppedOn = currentBranch(ctx, root.Dir())
	}
	runErr = finish(handle, runErr)
	if err := issueReceipt(ctx, root, handle.Record()); err != nil {
		if resuming && errors.Is(err, ErrReceiptExists) {
			return result, runErr
		}
		return result, errors.Join(runErr, err)
	}
	return result, runErr
}

// finish writes the run's terminal outcome. A run that ended in an error
// is blocked, carrying that error verbatim as its reason — an operator
// reading the record must see what Pika saw, not a paraphrase.
//
// A failure to save the outcome is reported rather than swallowed, and it
// is joined to the run's own error rather than replacing it: losing the
// reason a run stopped in order to report that the record could not be
// updated would trade the more useful fact for the less useful one.
func finish(handle *workrec.Handle, runErr error) error {
	err := saveRecord(handle, func(rec *workrec.Record) {
		if runErr != nil {
			rec.Outcome = workrec.OutcomeBlocked
			rec.Reason = runErr.Error()
			return
		}
		rec.Outcome = workrec.OutcomeComplete
	})
	if err != nil {
		return errors.Join(runErr, err)
	}
	return runErr
}

// savePhase stamps a completed phase and saves the run durably. Phase is
// the head of the run's history and Phases is the history itself, so both
// move together and only here. note is empty for a fresh run and marks
// the stamp for a resumed one.
func savePhase(handle *workrec.Handle, phase, note string, apply func(*workrec.Record)) error {
	return saveRecord(handle, func(rec *workrec.Record) {
		if apply != nil {
			apply(rec)
		}
		rec.Phase = phase
		rec.Phases = append(rec.Phases, workrec.PhaseStamp{Phase: phase, At: time.Now().UTC(), Note: note})
	})
}

// saveRecord runs the read-modify-save loop workrec is built for. The
// record is taken from the handle, which clones Phases, so appending to
// it cannot write through the handle's cache. Baseline and Recheck are
// shared pointers and are only ever replaced wholesale, never mutated.
func saveRecord(handle *workrec.Handle, apply func(*workrec.Record)) error {
	rec := handle.Record()
	apply(&rec)
	return handle.Save(rec)
}

// recordResult is the run as the record already describes it: what Resume
// returns when Git proves the work landed and nothing had to be redone.
// ChangedFiles is left empty on purpose — this process changed no files,
// and listing the delivered commit's contents here would report work as
// if this run had just done it. The commit is the answer, and the receipt
// and `pika status` carry the rest.
func recordResult(rec workrec.Record) Result {
	return Result{
		WorkID:       rec.WorkID,
		Branch:       rec.Branch,
		Commit:       rec.Commit,
		ChecksBefore: rec.Baseline,
		ChecksAfter:  rec.Recheck,
	}
}

// deliveredCommit asks Git whether this run's work is already in the
// repository, and answers with the commit that carries it. An empty
// commit means Git proves nothing, and the caller falls through to the
// base-commit guard — where an operator who really did move the tree
// must still land.
//
// The record cannot answer this, and not only at the deliver phase. The
// deliver stamp is written *after* `git commit` has already moved the
// branch, so a crash in between leaves a durable record still reading
// `recheck` while the run's work is permanently in the repository. That
// is the likeliest interruption there is: nothing switches away from the
// run's branch between the handoff and the deliver, so the operator who
// re-runs `pika resume` is standing on the run's own completed commit.
// Both `deliver` and `recheck` are therefore worlds where the work may
// already have landed, and only Git can say which one this is.
//
// Each phase offers a different proof:
//
//   - At `deliver` the record names the commit, so the branch holding
//     that commit is the whole proof.
//   - At `recheck` the record names nothing: `Commit` is written by the
//     very save that stamps the deliver phase, so inside this window it
//     is necessarily empty, and `branchHead == rec.Commit` cannot be the
//     test. What identifies the commit instead is its shape, which the
//     lifecycle fixes completely: a run commits exactly once, onto a
//     branch it created at its own base commit, under one fixed subject.
//     A branch head whose single parent is the record's base commit and
//     whose subject is that message is a commit this run's own lifecycle
//     produced — an operator's own commit is on neither that parent nor
//     that subject, and a branch still sitting at the base commit has no
//     commit to recognise at all.
func deliveredCommit(ctx context.Context, root string, rec workrec.Record, branchHead string) (string, error) {
	switch rec.Phase {
	case workrec.PhaseDeliver:
		if rec.Commit != "" && branchHead == rec.Commit {
			return rec.Commit, nil
		}
	case workrec.PhaseRecheck:
		if rec.BaseCommit == "" || branchHead == "" || branchHead == rec.BaseCommit {
			return "", nil
		}
		parents, subject, err := commitShape(ctx, root, branchHead)
		if err != nil {
			return "", err
		}
		if len(parents) == 1 && parents[0] == rec.BaseCommit && subject == deliverMessage {
			return branchHead, nil
		}
	}
	return "", nil
}

// commitShape reads the two facts that identify a run's own delivery: the
// commit's parents and its subject. Both come from one Git call so they
// describe the same object even if the repository moves underneath.
//
// The commit is passed after `--end-of-options` rather than after `--`.
// `git show` is a revision command: `--` there means "paths follow", and
// `git show --format=... -- <commit>` reads the commit as a pathspec,
// prints nothing and exits zero — a silent wrong answer where a refusal
// belongs. `--end-of-options` is the separator that says "operands
// follow" without saying they are paths, so an argument beginning with
// `-` is read as a revision. Unguarded it is read as an option, and
// `git show --output=<path>` writes a file and exits zero.
func commitShape(ctx context.Context, root, commit string) ([]string, string, error) {
	shown, err := runGit(ctx, root, "show", "--no-patch", "--format=%P%n%s", "--end-of-options", commit)
	if err != nil {
		return nil, "", err
	}
	parents, subject, _ := strings.Cut(strings.TrimRight(shown, "\n"), "\n")
	return strings.Fields(parents), subject, nil
}

// enterBranch puts a resumed run on its branch: it switches to the branch
// when it exists and creates it when it does not. Which of those is
// needed is a question only Git can answer — a run interrupted between
// creating its branch and recording it leaves a branch the record cannot
// name — so it is asked rather than assumed.
//
// Only the switch to an existing branch takes a separator, and it is the
// bare `--`: `git switch` has no pathspec, so `--` unambiguously ends the
// options. The creating form needs none and must not have one — `-c`
// takes the next argument as its value whatever it starts with, so
// `git switch -c -- <branch>` would name the branch `--` and then read
// <branch> as options.
func enterBranch(ctx context.Context, root, branch string) error {
	state, err := currentGitState(ctx, root)
	if err != nil {
		return err
	}
	if state.Branch == branch {
		return nil
	}
	if _, exists, err := branchCommit(ctx, root, branch); err != nil {
		return err
	} else if exists {
		_, err = runGit(ctx, root, "switch", "--", branch)
		return err
	}
	_, err = runGit(ctx, root, "switch", "-c", branch)
	return err
}

// branchCommit resolves one local branch to the commit it points at and
// reports whether the branch exists at all.
//
// The ref is listed by its full refs/heads/ name and matched exactly, so
// neither a tag nor a branch that merely shares a prefix can answer for
// it. A Git failure is returned as an error rather than as an absent
// branch: "the branch is gone" is a refusal an operator will act on, and
// it must never be what Pika says when the real problem is that it could
// not read the repository at all.
func branchCommit(ctx context.Context, root, branch string) (string, bool, error) {
	listed, err := runGit(ctx, root, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	if err != nil {
		return "", false, err
	}
	want := "refs/heads/" + branch
	for _, line := range strings.Split(listed, "\n") {
		name, object, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && name == want {
			return object, true, nil
		}
	}
	return "", false, nil
}

// claimBranch puts a fresh run on the branch it is going to work in, and
// decides what to do about one that is already there.
//
// `git switch -c` on its own refuses an existing branch, and that
// refusal is load-bearing: it is what stops a run committing into work
// it did not do. What it never did was say anything an operator could
// act on, and a repository whose last run failed has that branch in it
// permanently. See ErrBranchHoldsWork for why deleting it unasked is not
// the answer either.
//
// So the branch is read before it is touched, and Git answers the only
// question that decides anything: is anything on it at risk. A branch
// head already contained in the commit this run starts from carries
// nothing — every commit reachable from it is reachable from the base,
// so moving it loses nothing the repository does not still have. That is
// exactly the leftover of a run that committed nothing, and it is reused
// without ceremony. Anything else holds commits, and holding them is
// enough to refuse whether or not Pika made them: an operator's own
// `chore/pika-improve` is not Pika's to write into.
//
// The run record is what turns that refusal into something actionable.
// Git can say a branch holds commits; only workrec can say which run put
// them there, and `git branch -D chore/pika-improve` is a very different
// instruction to an operator who can look the run up first.
func claimBranch(ctx context.Context, repo *repopath.Root, root, branch, baseCommit string) error {
	head, exists, err := branchCommit(ctx, root, branch)
	if err != nil {
		return err
	}
	if !exists {
		_, err := runGit(ctx, root, "switch", "-c", branch)
		return err
	}
	spare, err := carriesNothing(ctx, root, head, baseCommit)
	if err != nil {
		return err
	}
	if !spare {
		return branchHoldsWork(repo, branch, head)
	}
	// `-C` rather than `-c`, and it is a reset rather than a create: the
	// leftover is level with or behind the base commit, and the run has
	// already recorded that base commit as where its work starts.
	// Switching to the branch where it stands would put the tree on an
	// older commit while the record claims the newer one — a lie in the
	// one document `pika resume` reads to decide what world it is
	// rejoining. It takes no separator, for the same reason `-c` takes
	// none: the flag consumes the next argument as its value whatever
	// that argument starts with.
	_, err = runGit(ctx, root, "switch", "-C", branch)
	return err
}

// carriesNothing reports whether a branch head holds no commit the base
// commit does not already have.
//
// `git merge-base --is-ancestor A B` is that question exactly, and it
// answers in an exit code: 0 for yes, 1 for no, anything else for a
// repository Git could not read. runGit collapses all three into one
// error, so this asks the process directly — reading "not an ancestor"
// as a Git failure would turn a reusable leftover branch into an
// unexplained refusal, which is the defect this whole path exists to
// close. Both operands are object names Git itself produced in this
// process, but `--end-of-options` is passed anyway so no value can ever
// be read as a flag.
//
// A run with no recorded base commit cannot answer the question at all,
// and an unanswerable safety question is a no. Nothing here may assume a
// branch is empty.
func carriesNothing(ctx context.Context, root, head, baseCommit string) (bool, error) {
	if strings.TrimSpace(head) == "" || strings.TrimSpace(baseCommit) == "" {
		return false, nil
	}
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", "--end-of-options", head, baseCommit)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("improve: git merge-base --is-ancestor %s %s: %w: %s",
		head, baseCommit, err, strings.TrimSpace(string(output)))
}

// branchHoldsWork is the refusal a leftover branch carrying commits
// earns: what is there, who put it there, and what to do about it.
//
// The remedy is stated because the absence of one is the defect. An
// operator who reads only that a branch exists has to already know that
// Pika's run branch is disposable, that deleting it is safe once the
// work is merged, and that `--branch` exists at all.
func branchHoldsWork(repo *repopath.Root, branch, head string) error {
	rec, found, err := runOnBranch(repo, branch)
	if err != nil {
		return err
	}
	// The common case is the branch sitting on exactly the commit a run
	// recorded, and saying the same hash twice in one sentence buries
	// the run id between two copies of it. Where they differ both are
	// named, without claiming which came first: this code knows the
	// branch holds work, not what rearranged it.
	held := fmt.Sprintf("is at %s, which no run record claims", head)
	if found && rec.Commit == head {
		held = fmt.Sprintf("is at %s, delivered there by run %s", head, rec.WorkID)
	} else if found {
		held = fmt.Sprintf("is at %s; the last run to deliver there, %s, committed %s", head, rec.WorkID, rec.Commit)
	}
	return fmt.Errorf("%w: %s %s; read it with `git log %s`, then delete it with `git branch -D %s` once the work is merged or unwanted, or send this run elsewhere with --branch",
		ErrBranchHoldsWork, branch, held, branch, branch)
}

// runOnBranch finds the run that delivered work on a branch: the most
// recent record that names it and carries a commit.
//
// workrec.List is newest-first, so the first match is the last run to
// deliver there — the commit the branch is most likely sitting on, and
// the run an operator asking `pika status` about it wants. A record
// naming the branch with no commit is not an answer: that is a run which
// reached the branch and stopped before committing anything, which is
// precisely the leftover this refusal is not about.
func runOnBranch(repo *repopath.Root, branch string) (workrec.Record, bool, error) {
	runs, err := workrec.List(repo)
	if err != nil {
		return workrec.Record{}, false, err
	}
	for _, rec := range runs {
		if rec.Branch == branch && rec.Commit != "" {
			return rec, true, nil
		}
	}
	return workrec.Record{}, false, nil
}

// currentBranch names the branch a repository is on, or "" when this
// process cannot say — a detached HEAD, or a repository it can no longer
// read at all.
//
// It is best effort by design. Its only use is making a report about a
// failure more accurate, so a Git call that fails here must never
// replace the reason the run actually stopped: the caller falls back to
// what it already knew rather than reporting Git's problem instead of
// the run's.
func currentBranch(ctx context.Context, root string) string {
	out, err := runGit(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// orNoBaseCommit names an absent base commit in a refusal message.
// "started at" with nothing after it reads like a value that got lost on
// the way here.
func orNoBaseCommit(commit string) string {
	if commit == "" {
		return "no recorded base commit"
	}
	return commit
}

// changePaths removes Pika's own local state from the set of files a run
// is allowed to commit. See privateStateDir for why it filters the whole
// subtree.
//
// A rename contributes both of its paths, destination first — the order
// `-z` reports them in. Adding the origin is what stages the rename's
// deletion, so a commit built from this list records the move rather
// than leaving the file behind at both paths; naming only the
// destination, as this once did, is also how a private origin escaped
// the filter entirely. privateStateMoved has already refused that case
// by the time this runs, so what reaches here is either wholly private
// (dropped) or wholly public (kept).
func changePaths(entries []statusEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		for _, path := range [...]string{entry.path, entry.origin} {
			if path == "" || isPrivateState(path) {
				continue
			}
			out = append(out, path)
		}
	}
	return out
}

// privateStateMoved names the first path an agent has moved across the
// `.project/state` boundary, or "" if none was. Pika refuses the run when
// it answers.
//
// Both sides of a rename are read. Out of the subtree is the leak this
// exists for: the destination is an ordinary path the filter has no
// reason to reject, so the private content would ride into the commit
// inside it. Into the subtree is the mirror of it — a repository file
// pushed where Pika's own gitignored state lives. Between two private
// paths is the agent rearranging a run record that is not its to touch.
// None of the three is work Pika can commit on an operator's behalf.
//
// A deletion is the same event seen later. Pika resets the index before
// it reads this status, which collapses a staged rename into a worktree
// deletion of the origin plus an untracked destination — so the origin
// arrives alone, still under `.project/state`, still gone. Nothing in a
// run deletes tracked private state, and Run refuses a dirty tree before
// it starts, so a private path reported as deleted here can only have
// been removed by the agent during the handoff.
func privateStateMoved(entries []statusEntry) string {
	for _, entry := range entries {
		if entry.origin != "" {
			if isPrivateState(entry.path) {
				return entry.path
			}
			if isPrivateState(entry.origin) {
				return entry.origin
			}
			continue
		}
		if isPrivateState(entry.path) && strings.Contains(entry.code, "D") {
			return entry.path
		}
	}
	return ""
}

func isPrivateState(path string) bool {
	return path == privateStateDir || strings.HasPrefix(path, privateStateDir+"/")
}

func runGit(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("improve: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// stageChanges stages exactly the paths the ladder proved, and nothing
// Git chose to read into them.
//
// `git add` takes its arguments as PATHSPECS, not as filenames. A path
// holding a glob metacharacter is therefore a pattern, and in a pathspec
// `*` matches `/` as well — so an agent that leaves behind a file named
// `.project/stat*` hands Pika's own commit step a pattern covering the
// whole of `.project/state`: the run record, the handoff bundle inside
// it, the envelope, the board. changePaths dropped every one of those
// paths a few lines above, and the command meant to enforce that filter
// puts them back. It is the same defect `-z` closed in the status
// parser, one line later: this code believes it is naming exact paths
// and Git is reading something more permissive.
//
// Three flags are needed and none of them is sufficient alone.
// `--literal-pathspecs` is the one that turns matching off, so `*`, `?`
// and `[` are the characters they are and a leading `:` is not pathspec
// magic. `--pathspec-from-file=-` with `--pathspec-file-nul` is how the
// list arrives verbatim: read from stdin the paths are NUL-delimited
// records rather than argv, so a name holding a newline or a quote is
// neither split nor unescaped, and a large change set cannot run into a
// bounded argv. Passing the paths literally on the command line would
// still be pattern matching, and passing them NUL-delimited without
// `--literal-pathspecs` still is.
func stageChanges(ctx context.Context, root string, paths []string) error {
	var stdin bytes.Buffer
	for _, path := range paths {
		stdin.WriteString(path)
		stdin.WriteByte(0)
	}
	cmd := exec.CommandContext(ctx, "git", "--literal-pathspecs", "add", "--pathspec-from-file=-", "--pathspec-file-nul")
	cmd.Dir = root
	cmd.Stdin = &stdin
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("improve: git add: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// readStatus reads the working tree exactly as the guards below need to
// see it: every untracked file named, every path verbatim.
//
// It is one function rather than a command and a parse at the call site
// because the flags and the parser are a single contract — `-z` is what
// makes the paths verbatim, and a parser fed output produced without it
// would silently go back to reading Git's quoting as though it were a
// path.
func readStatus(ctx context.Context, root string) ([]statusEntry, error) {
	out, err := gitPorcelain(ctx, root, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	return statusEntries(out)
}

// gitPorcelain runs Git for machine-readable output and returns stdout
// alone.
//
// runGit merges stderr into what it returns, which is right for output
// nothing parses and wrong for this: a warning Git wrote to stderr would
// arrive inside a NUL-delimited record, and a parser that refuses what
// it cannot understand would refuse a run that had nothing wrong with
// it. Diagnostics still reach the error, where they belong.
func gitPorcelain(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("improve: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(output), nil
}

// statusEntry is one record of `git status --porcelain -z`: its two
// status columns, the path the record names, and — for a rename or a
// copy — the path the content came from. Both sides matter, so neither
// is thrown away here.
//
// `-z` is what makes the paths trustworthy. Without it Git C-quotes any
// path holding a non-ASCII byte, whitespace or a control character
// (`core.quotePath` is on by default), so `.project/state/wéird.json`
// arrives as the literal `".project/state/w\303\251ird.json"` — a string
// no prefix test for `.project/state` matches, and every guard below it
// fails open on exactly the input shaped to defeat them. Under `-z` Git
// emits each path verbatim and quotes nothing.
type statusEntry struct {
	code   string
	origin string // the pre-rename path; empty unless the record named two
	path   string
}

// statusEntries parses `git status --porcelain -z` into its records.
//
// `-z` also changes how a rename is encoded, and the change is not
// cosmetic: the `->` is gone and the field order is reversed, so a
// rename arrives as `XY <destination>\0<origin>\0` and the origin is a
// separate trailing field read after the path it moved to.
//
// A record this cannot parse is an error, never a skip. Everything the
// caller does with these entries is a guard, and a guard that quietly
// discards the record it failed to understand opens on precisely the
// input that confused it.
func statusEntries(value string) ([]statusEntry, error) {
	fields := strings.Split(value, "\x00")
	// Every record is NUL-terminated rather than NUL-separated, so
	// well-formed output ends in an empty trailing field — as does the
	// empty output of a clean tree.
	if n := len(fields); n > 0 && fields[n-1] == "" {
		fields = fields[:n-1]
	}
	out := make([]statusEntry, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		// Two status columns, the one space `-z` keeps as their
		// separator, and a path of at least one byte.
		if len(field) < 4 || field[2] != ' ' {
			return nil, fmt.Errorf("improve: unparsable git status record %q", field)
		}
		entry := statusEntry{code: field[:2], path: field[3:]}
		if renameOrCopy(entry.code) {
			i++
			if i >= len(fields) || fields[i] == "" {
				return nil, fmt.Errorf("improve: git status record %q names no rename origin", field)
			}
			entry.origin = fields[i]
		}
		out = append(out, entry)
	}
	return out, nil
}

// renameOrCopy reports whether a status code is the one shape that makes
// the next field an origin rather than the next record. Either column
// can carry it, and R and C are the only two letters Git spends on it.
func renameOrCopy(code string) bool {
	return strings.ContainsAny(code, "RC")
}
