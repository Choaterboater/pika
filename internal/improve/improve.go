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
	Goal   string
	Check  CheckFunc
	Runner Runner
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
	})
	if err != nil {
		return Result{}, err
	}

	result, runErr := lifecycle(ctx, cfg, kind, handle)
	result.WorkID = workID
	return result, finish(handle, runErr)
}

// lifecycle is the run itself, from the baseline ladder to the verified
// commit. It never records the terminal outcome: every one of its exits
// is a terminal outcome, so finish owns that single write and no exit
// path can forget it.
func lifecycle(ctx context.Context, cfg Config, kind string, handle *workrec.Handle) (Result, error) {
	before, err := cfg.Check()
	if err != nil {
		return Result{}, fmt.Errorf("improve: baseline checks: %w", err)
	}
	if before == nil {
		return Result{}, errors.New("improve: baseline checks returned no report")
	}
	result := Result{ChecksBefore: before}
	if err := savePhase(handle, workrec.PhaseBaseline, func(rec *workrec.Record) {
		rec.Baseline = before
	}); err != nil {
		return result, err
	}

	// The one decision the two kinds do not share.
	if before.Pass && kind == workrec.KindRepair {
		return result, nil
	}

	if cfg.Runner == nil {
		return result, errors.New("improve: agent runner is required")
	}
	if _, err := runGit(ctx, cfg.Root, "switch", "-c", cfg.Branch); err != nil {
		return result, err
	}
	result.Branch = cfg.Branch
	// The branch is recorded before the agent runs, not after. It exists
	// from this instant, and a crash while the agent works is precisely
	// the case where the record has to be able to name it.
	if err := saveRecord(handle, func(rec *workrec.Record) {
		rec.Branch = cfg.Branch
	}); err != nil {
		return result, err
	}

	handoff, err := createHandoff(ctx, cfg.Root, handle.HandoffDir(), cfg.Goal, before, cfg.Runner)
	result.Handoff = handoff
	if err != nil {
		return result, err
	}
	if err := savePhase(handle, workrec.PhaseHandoff, nil); err != nil {
		return result, err
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
	if err := savePhase(handle, workrec.PhaseRecheck, func(rec *workrec.Record) {
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
	if err := savePhase(handle, workrec.PhaseDeliver, func(rec *workrec.Record) {
		rec.Commit = result.Commit
	}); err != nil {
		return result, err
	}
	return result, nil
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
// move together and only here.
func savePhase(handle *workrec.Handle, phase string, apply func(*workrec.Record)) error {
	return saveRecord(handle, func(rec *workrec.Record) {
		if apply != nil {
			apply(rec)
		}
		rec.Phase = phase
		rec.Phases = append(rec.Phases, workrec.PhaseStamp{Phase: phase, At: time.Now().UTC()})
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
