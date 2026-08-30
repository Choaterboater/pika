package improve

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Choaterboater/pika/internal/evidence"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/workrec"
)

// ErrDirtyTree prevents Pika from mixing an automated repair with work the
// caller has not committed yet.
var ErrDirtyTree = errors.New("improve: working tree must be clean")

// ErrNoChanges prevents a misleading empty commit after an agent run.
var ErrNoChanges = errors.New("improve: Codex made no changes to commit")

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

// CheckFunc runs Pika's deterministic ladder and returns its full report.
// The command layer supplies the same in-process check engine used by
// `pika check`; tests provide real, controlled reports.
type CheckFunc func() (*verify.Report, error)

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
	// Agent names the contract agent this run spawns; it is recorded as
	// the run's role. Runtime is the runtime that agent runs under.
	// Runner is an interface and cannot name either, and a receipt that
	// leaves the role and runtime empty is a lie of omission in a
	// document whose whole purpose is to attest what actually ran.
	Agent   string
	Runtime string
	Runner  Runner
}

// Result is the complete local outcome. Any error return may still include a
// work id, branch, handoff bundle, and baseline report so the caller can
// inspect the uncommitted state without Pika concealing it.
type Result struct {
	WorkID       string         `json:"workId,omitempty"`
	Branch       string         `json:"branch,omitempty"`
	Commit       string         `json:"commit,omitempty"`
	ChangedFiles []string       `json:"changedFiles,omitempty"`
	Handoff      Handoff        `json:"handoff,omitempty"`
	ChecksBefore *verify.Report `json:"checksBefore,omitempty"`
	ChecksAfter  *verify.Report `json:"checksAfter,omitempty"`
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
func Run(ctx context.Context, cfg Config) (Result, error) {
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

	// Everything above this line, and the dirty-tree refusal below it,
	// happens before the run exists on disk. A run refused before it has
	// done anything must leave no record behind: a directory of empty
	// runs is noise that makes the real records harder to trust.
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
	handle, err := workrec.Create(root, workrec.Record{
		WorkID:     workID,
		Goal:       cfg.Goal,
		Kind:       kind,
		BaseCommit: strings.TrimSpace(baseCommit),
		Role:       cfg.Agent,
		Runtime:    cfg.Runtime,
	})
	if err != nil {
		return Result{}, err
	}

	result, runErr := lifecycle(ctx, cfg, kind, handle, stageBaseline, "")
	result.WorkID = workID
	// A fresh run's receipt cannot already exist: the work id is new and
	// workrec.Create has already refused to reuse a run directory. One
	// that is there anyway is a collision, so settle reports it rather
	// than writing over it.
	return settle(ctx, root, handle, result, runErr, false)
}

// stage names a point in the lifecycle a run can be entered at. Run
// enters at stageBaseline and walks all of it; Resume enters at whichever
// stage the record and Git together prove the interrupted run reached.
type stage int

const (
	stageBaseline stage = iota
	stageHandoff
	stageRecheck
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
// decisive case is the deliver phase: if the run's branch points at the
// commit the record names, Git has already proved the work landed and the
// only thing missing is the terminal write, so Resume writes it and
// stops. Re-running the lifecycle there would branch again and redo work
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
func Resume(ctx context.Context, root, workID string, cfg Config) (Result, error) {
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
		// anything at all. A recorded deliver phase whose branch holds
		// the recorded commit is proof the work landed: the only thing
		// that failed was the terminal write, so it is the only thing
		// redone. This runs before the base-commit guard below because a
		// delivered run has by definition moved past its base commit —
		// refusing it there would refuse the one case Git has settled.
		if rec.Phase == workrec.PhaseDeliver && rec.Commit != "" && branchHead == rec.Commit {
			return settle(ctx, repo, handle, recordResult(rec), nil, true)
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

	result, runErr := lifecycle(ctx, cfg, kind, handle, resumeStage(rec), resumedNote)
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
//   - The handoff is skipped once it completed. It spawns the agent, and
//     spawning a second one to redo work already sitting in the tree is
//     the exact cost resume exists to avoid.
//   - The recheck is never skipped. "Commit only what the ladder proved"
//     has to be proved by this process against this tree; the record
//     proves only what another process saw in a tree nothing has vouched
//     for since.
func resumeStage(rec workrec.Record) stage {
	switch rec.Phase {
	case workrec.PhaseHandoff, workrec.PhaseRecheck, workrec.PhaseDeliver:
		return stageRecheck
	case workrec.PhaseBaseline:
		if rec.Baseline == nil {
			// The stamp says the ladder ran and the record has no report
			// to show for it. Git and the record are the only evidence
			// resume has, so where they disagree it takes the phase it
			// can prove: run the ladder.
			return stageBaseline
		}
		return stageHandoff
	default:
		return stageBaseline
	}
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
func lifecycle(ctx context.Context, cfg Config, kind string, handle *workrec.Handle, from stage, note string) (Result, error) {
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

		if cfg.Runner == nil {
			return result, errors.New("improve: agent runner is required")
		}
		// A fresh run creates its branch, and `switch -c` failing on a
		// branch that already exists is the guarantee that a run never
		// writes into work it did not do. A resumed run reconciles
		// instead: its branch may already be there because the
		// interrupted process created it and died before the record
		// could name it.
		if from == stageBaseline {
			if _, err := runGit(ctx, cfg.Root, "switch", "-c", cfg.Branch); err != nil {
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

		handoff, err := createHandoff(ctx, cfg.Root, handle.HandoffDir(), cfg.Goal, result.ChecksBefore, cfg.Runner)
		result.Handoff = handoff
		if err != nil {
			return result, err
		}
		if err := savePhase(handle, workrec.PhaseHandoff, note, nil); err != nil {
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

	changed, err := runGit(ctx, cfg.Root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return result, err
	}
	result.ChangedFiles = changePaths(statusPaths(changed))
	if len(result.ChangedFiles) == 0 {
		return result, ErrNoChanges
	}
	state, err = currentGitState(ctx, cfg.Root)
	if err != nil {
		return result, err
	}
	if state.Branch != cfg.Branch {
		return result, fmt.Errorf("improve: expected branch %q before commit, found %q", cfg.Branch, state.Branch)
	}
	addArgs := append([]string{"add", "--"}, result.ChangedFiles...)
	if _, err := runGit(ctx, cfg.Root, addArgs...); err != nil {
		return result, err
	}
	if _, err := runGit(ctx, cfg.Root, "commit", "-m", "chore: improve verified findings"); err != nil {
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

// enterBranch puts a resumed run on its branch: it switches to the branch
// when it exists and creates it when it does not. Which of those is
// needed is a question only Git can answer — a run interrupted between
// creating its branch and recording it leaves a branch the record cannot
// name — so it is asked rather than assumed.
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
		_, err = runGit(ctx, root, "switch", branch)
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
func changePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == privateStateDir || strings.HasPrefix(path, privateStateDir+"/") {
			continue
		}
		out = append(out, path)
	}
	return out
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

func statusPaths(value string) []string {
	var out []string
	for _, line := range strings.Split(value, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if before, after, renamed := strings.Cut(path, " -> "); renamed {
			path = after
			_ = before
		}
		if path != "" {
			out = append(out, path)
		}
	}
	return out
}
