package improve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/evidence"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/workrec"
)

// ErrReceiptExists refuses to replace a receipt that is already on disk.
//
// evidence.Write renames over an existing target without complaint, and
// work ids are 32 random bits that workrec.Create already refuses to
// reuse — but the receipt is a separate artifact on a separate path, and
// an invariant enforced somewhere else is not a guarantee here. The door
// is closed at this end too, and with a single O_EXCL create rather than
// a stat-then-write, for the same reason workrec.Create claims its run
// directory with a single mkdir.
var ErrReceiptExists = errors.New("improve: evidence receipt already exists")

// receiptOwnership classes every file the run committed. Inside a run
// the kernel observed exactly one writer — the agent it spawned in the
// handoff, whose edits it then re-verified — so that is what the receipt
// says.
const receiptOwnership = "agent"

// noSurfaceScenario states plainly that no real-surface scenario ran.
// `pika improve` verifies through the deterministic ladder only; rung 5
// is never part of check (spec §16). Claiming a scenario ran would be
// the exact kind of unobserved assertion this receipt exists to replace.
const noSurfaceScenario = "none: `pika improve` verifies through the deterministic ladder only"

// inProcessGate names a gate that executed inside pika rather than as a
// child process — gate 1, the contract check, has no argv. Naming it
// this way keeps the receipt from carrying a command line that was never
// executed.
const inProcessGate = "pika: in-process gate "

// issueReceipt writes the run's evidence receipt to
// .project/evidence/<work-id>.json.
//
// It is called once per run, after the terminal outcome has been
// recorded, so the receipt attests the run's final state — including a
// blocked one. A receipt that only ever describes successes attests the
// wrong half of what pika does.
//
// Every failure here is returned. In particular evidence.Build is
// fail-closed (an unredactable pack key errors rather than emitting), and
// that error surfaces: there is no path in this function that writes a
// partial, unvalidated or unredacted receipt.
func issueReceipt(ctx context.Context, root *repopath.Root, rec workrec.Record) error {
	if !attemptedWork(rec) {
		return nil
	}
	receipt, err := buildReceipt(ctx, root, rec)
	if err != nil {
		return err
	}
	path := filepath.Join(root.EvidenceDir(), rec.WorkID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("improve: create %s: %w", filepath.Dir(path), err)
	}
	// Claim the path before writing it. O_EXCL makes the refusal a
	// property of the filesystem rather than of a check that raced.
	claim, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrReceiptExists, path)
		}
		return fmt.Errorf("improve: claim %s: %w", path, err)
	}
	if err := claim.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("improve: claim %s: %w", path, err)
	}
	if err := evidence.Write(path, receipt); err != nil {
		// The claim is an empty file this call created; a failed write
		// must not leave it behind pretending to be a receipt.
		os.Remove(path)
		return err
	}
	return nil
}

// attemptedWork reports whether the run got as far as giving an agent
// work to do. The branch is the marker: the lifecycle creates it
// immediately before the handoff and records it before the agent runs,
// so every run that reached an agent has one — delivered or blocked —
// and every run refused before it did anything does not.
//
// The one exit this deliberately excludes is a repair run whose baseline
// ladder was already green. Nothing was attempted: no agent, no commit,
// no changed file, nothing to attest but a check report `pika check`
// already prints. `.project/evidence` is committed content, so issuing a
// receipt there would leave the working tree of a healthy repository
// dirty after every no-op run.
func attemptedWork(rec workrec.Record) bool { return rec.Branch != "" }

// buildReceipt assembles the receipt from what the run observed and
// nothing else: the contract and lock as they are on disk, the reports
// the ladder produced, and the commit the run actually created — read
// back out of Git rather than taken from the caller's memory.
func buildReceipt(ctx context.Context, root *repopath.Root, rec workrec.Record) (*evidence.Receipt, error) {
	c, err := loadContract(root)
	if err != nil {
		return nil, err
	}
	lock, err := loadProfileLock(root)
	if err != nil {
		return nil, err
	}
	tree, changed, err := delivered(ctx, root.Dir(), rec.Commit)
	if err != nil {
		return nil, err
	}
	return evidence.Build(evidence.ReceiptInput{
		WorkID:          rec.WorkID,
		ContractVersion: contractVersion(c),
		ProfileLock:     lock,
		Commit:          rec.Commit,
		Tree:            tree,
		Roles:           runRoles(rec, c),
		ChangedFiles:    changed,
		// Both ladder runs, in the order they happened: the baseline
		// the agent was given, then the recheck that decided whether
		// its work could be committed. The same gate legitimately
		// appears twice with different exits — that is the run.
		Commands:         append(gateCommands(rec.Baseline), gateCommands(rec.Recheck)...),
		SurfaceScenario:  evidence.SurfaceScenarioInput{Description: noSurfaceScenario},
		BaselineFailures: baselineFailures(rec.Baseline),
		Regressions:      regressions(rec.Baseline, rec.Recheck),
		Completion:       completion(rec),
	})
}

// loadContract reads the repository's contract. A repository with no
// contract is a real state — the receipt then names no contract version
// rather than inventing one — but a contract that exists and does not
// load is damage, and damage is reported, never worked around.
func loadContract(root *repopath.Root) (*contract.Contract, error) {
	c, err := contract.Load(root.Contract())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// contractVersion is the contract's declared schema version, which is
// the only version a contract carries.
func contractVersion(c *contract.Contract) string {
	if c == nil {
		return ""
	}
	return strconv.Itoa(c.Schema)
}

// loadProfileLock reads .project/profiles.lock as it stands: the
// registry digest the run verified against and every pinned pack. A
// missing lock leaves the block empty; an unparsable one is an error.
func loadProfileLock(root *repopath.Root) (evidence.ProfileLockInput, error) {
	lock, err := profiles.ReadLock(root.Lock())
	if errors.Is(err, fs.ErrNotExist) {
		return evidence.ProfileLockInput{}, nil
	}
	if err != nil {
		return evidence.ProfileLockInput{}, err
	}
	out := evidence.ProfileLockInput{
		Digest: lock.Digest,
		Packs:  make(map[string]evidence.PackInput, len(lock.Packs)),
	}
	for name, pack := range lock.Packs {
		out.Packs[name] = evidence.PackInput{
			Version: pack.Version,
			Source:  pack.Source,
			Digest:  pack.Digest,
		}
	}
	return out, nil
}

// delivered reads the tree and the changed files out of the commit the
// run produced. They are read back from Git rather than carried over
// from the run's own bookkeeping: the receipt states what the repository
// contains, not what the run believed it committed.
func delivered(ctx context.Context, root, commit string) (string, []evidence.ChangedFileInput, error) {
	if commit == "" {
		return "", nil, nil
	}
	tree, err := gitValue(ctx, root, "rev-parse", commit+"^{tree}")
	if err != nil {
		return "", nil, err
	}
	names, err := gitValue(ctx, root, "diff-tree", "--no-commit-id", "--name-only", "-r", "--root", commit)
	if err != nil {
		return "", nil, err
	}
	var changed []evidence.ChangedFileInput
	for _, name := range strings.Split(names, "\n") {
		if name = strings.TrimSpace(name); name != "" {
			changed = append(changed, evidence.ChangedFileInput{Path: name, Ownership: receiptOwnership})
		}
	}
	return tree, changed, nil
}

// runRoles reports the agent the run spawned. Role and runtime are what
// the lifecycle recorded — what actually ran — while provider and model
// come from the contract entry that configured it. Substituted is
// computed rather than asserted: it is true exactly when the runtime the
// run spawned is not the one the contract named. `pika improve` refuses
// that mismatch up front, so in practice it is false; hard-coding it
// false would make the receipt say so even if it stopped being true.
func runRoles(rec workrec.Record, c *contract.Contract) []evidence.RoleInput {
	if rec.Role == "" && rec.Runtime == "" {
		return nil
	}
	role := evidence.RoleInput{Role: rec.Role, Runtime: rec.Runtime}
	if c != nil {
		if agent, ok := c.Agents[rec.Role]; ok {
			role.Provider = agent.Provider
			role.Model = agent.Model
			role.Substituted = agent.Runtime != rec.Runtime
		}
	}
	return []evidence.RoleInput{role}
}

// gateCommands turns one ladder run into the commands it executed.
// Skipped gates are left out: a gate that never ran has no exit status,
// no duration and no output, and recording one as exit 0 would report a
// command that was never executed.
func gateCommands(report *verify.Report) []evidence.CommandInput {
	if report == nil {
		return nil
	}
	cmds := make([]evidence.CommandInput, 0, len(report.Gates))
	for _, gate := range report.Gates {
		if gate.Status != verify.StatusPass && gate.Status != verify.StatusFail {
			continue
		}
		cmds = append(cmds, evidence.CommandInput{
			Cmd:        gateCmd(gate),
			Exit:       gate.Exit,
			DurationMs: gate.DurationMs,
			Output:     gate.OutputTail,
		})
	}
	return cmds
}

// gateCmd names what the gate ran: its argv, verbatim, or the gate
// itself when it ran in-process and there is no argv to name.
func gateCmd(gate verify.GateResult) string {
	if len(gate.Cmd) > 0 {
		return strings.Join(gate.Cmd, " ")
	}
	return inProcessGate + gate.ID
}

// baselineFailures is what was already broken when the run started —
// the failures the agent was handed, not ones it caused.
func baselineFailures(baseline *verify.Report) []string {
	if baseline == nil {
		return nil
	}
	var out []string
	for _, gate := range failedGates(baseline) {
		out = append(out, describeFailure(gate))
	}
	return out
}

// regressions is what the recheck failed on that the baseline did not:
// the failures that appeared while the agent worked. A gate that was
// already failing before the handoff is a baseline failure however the
// recheck ends, and calling it a regression would blame the agent for
// the state it was asked to repair.
func regressions(baseline, recheck *verify.Report) []string {
	if recheck == nil {
		return nil
	}
	before := make(map[string]struct{})
	if baseline != nil {
		for _, gate := range failedGates(baseline) {
			before[gate.ID] = struct{}{}
		}
	}
	var out []string
	for _, gate := range failedGates(recheck) {
		if _, existed := before[gate.ID]; existed {
			continue
		}
		out = append(out, describeFailure(gate))
	}
	return out
}

// describeFailure identifies a failed gate compactly; its output is
// already carried in full by the command entry for the same gate.
func describeFailure(gate verify.GateResult) string {
	if gate.Reason != "" {
		return fmt.Sprintf("%s: exit %d: %s", gate.ID, gate.Exit, gate.Reason)
	}
	return fmt.Sprintf("%s: exit %d", gate.ID, gate.Exit)
}

// completion restates the record's own verdict. A run that did not
// complete must say why, and the reason is the one the record already
// holds — the error verbatim, as an operator would read it. Blocker is
// left empty: it would only repeat the reason, and the schema forbids it
// on a complete run.
func completion(rec workrec.Record) evidence.CompletionInput {
	if rec.Outcome == workrec.OutcomeComplete {
		return evidence.CompletionInput{Complete: true}
	}
	reason := rec.Reason
	if reason == "" {
		reason = fmt.Sprintf("run ended at phase %q with outcome %q and no recorded reason", rec.Phase, rec.Outcome)
	}
	return evidence.CompletionInput{Reason: reason}
}
