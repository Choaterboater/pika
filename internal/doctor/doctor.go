// Package doctor diagnoses a repository's pika health without mutating
// anything and without executing any gate command. It answers the
// question "why did that not work" that previously required reading
// kernel source.
package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"

	"github.com/Choaterboater/pika/internal/checks"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/envelope"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/txn"
	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/version"
)

// Severity levels. Only SeverityError affects the exit code; a warning is
// a review signal, matching gate 1's severity model.
const (
	SeverityOK    = "ok"
	SeverityWarn  = "warn"
	SeverityError = "error"
)

// Finding is one diagnosed fact.
type Finding struct {
	ID          string `json:"id"`
	Severity    string `json:"severity"`
	Detail      string `json:"detail"`
	Remediation string `json:"remediation,omitempty"`
}

// Report is the doctor result.
type Report struct {
	Root     string    `json:"root"`
	Origin   string    `json:"origin"`
	Findings []Finding `json:"findings"`
	OK       bool      `json:"ok"`
}

func (r *Report) add(id, severity, detail, remediation string) {
	r.Findings = append(r.Findings, Finding{
		ID: id, Severity: severity, Detail: detail, Remediation: remediation,
	})
	if severity == SeverityError {
		r.OK = false
	}
}

// Run diagnoses the repository. It never returns an error: a broken
// repository is the thing being reported, not a reason to fail. A missing
// contract is an error-severity finding rather than an abort, so an
// unadopted repository still gets a report covering root, lock, envelope,
// and toolchain.
func Run(root *repopath.Root) *Report {
	rep := &Report{Root: root.Dir(), Origin: root.Origin(), OK: true}
	rep.add("root", SeverityOK,
		fmt.Sprintf("%s (resolved by %s)", root.Dir(), root.Origin()), "")

	c := checkContract(rep, root)
	resolved := checkProfiles(rep, root, c)
	checkExceptions(rep, root)
	checkEnvelope(rep, root)
	checkRecovery(rep, root)
	checkGates(rep, c, resolved)
	checkGit(rep)
	return rep
}

func checkContract(rep *Report, root *repopath.Root) *contract.Contract {
	c, err := contract.Load(root.Contract())
	if err != nil {
		// contract.Load wraps the read error, so the not-exist test must
		// unwrap: os.IsNotExist would report false here and mislabel an
		// unadopted repository as a malformed one.
		if errors.Is(err, fs.ErrNotExist) {
			rep.add("contract", SeverityError, "no contract at "+root.Contract(),
				"run \"pika init\" for a new project or \"pika adopt\" for an existing one")
			return nil
		}
		rep.add("contract", SeverityError, err.Error(),
			"fix the contract, then re-run \"pika doctor\"")
		return nil
	}
	if err := version.Check(c.Schema); err != nil {
		rep.add("contract", SeverityError, err.Error(),
			"upgrade the pika binary; this contract targets a newer schema")
		return c
	}
	rep.add("contract", SeverityOK,
		fmt.Sprintf("schema %d, profiles %v", c.Schema, c.Profiles), "")
	return c
}

func checkProfiles(rep *Report, root *repopath.Root, c *contract.Contract) *profiles.Resolved {
	if c == nil {
		// One root cause must produce one error. The contract finding
		// already carries the actionable remediation; this is a warning
		// recording that the lock was never examined.
		rep.add("lock", SeverityWarn, "not checked: no contract", "")
		return nil
	}
	resolved, err := profiles.Resolve(c.Profiles)
	if err != nil {
		rep.add("profiles", SeverityError, err.Error(),
			"correct the profiles list in the contract")
		rep.add("lock", SeverityWarn, "not checked: profiles did not resolve", "")
		return nil
	}
	if _, err := profiles.ReadLock(root.Lock()); err != nil {
		rep.add("lock", SeverityError, err.Error(),
			"re-run \"pika init --force\" or \"pika apply\" to regenerate the lock")
		return resolved
	}
	// checks.CheckLock is gate 1's implementation; reuse it so doctor and
	// check can never disagree about lock health.
	if err := checks.CheckLock(root.Dir(), c); err != nil {
		rep.add("lock", SeverityError, err.Error(),
			"regenerate the lock; the pinned digests no longer match the embedded packs")
		return resolved
	}
	rep.add("lock", SeverityOK, "pinned digests match the embedded registry", "")
	return resolved
}

func checkExceptions(rep *Report, root *repopath.Root) {
	if _, err := checks.LoadExceptions(root.Dir()); err != nil {
		rep.add("exceptions", SeverityError, err.Error(),
			"fix .project/exceptions.yaml; unverifiable records must not widen the rules")
		return
	}
	rep.add("exceptions", SeverityOK, "exceptions record loads", "")
}

func checkEnvelope(rep *Report, root *repopath.Root) {
	path := root.Envelope()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		// A warning, not an error: `pika check` never needs an envelope.
		// Only the mutating MCP tools do.
		rep.add("envelope", SeverityWarn, "no capability envelope at "+path,
			"run \"pika authorize --scope project\"; without it every mutating MCP tool is denied")
		return
	}
	env, err := envelope.Load(root.Dir(), path)
	if err != nil {
		rep.add("envelope", SeverityError, err.Error(),
			"fix or regenerate the envelope with \"pika authorize --force\"")
		return
	}
	rep.add("envelope", SeverityOK, "grants: "+grantedKinds(env), "")
}

// checkRecovery reports an unfinished transaction, which is the one
// repository state that stops `pika apply` working and clears itself
// under no circumstances.
//
// The lock is taken with O_EXCL and deliberately never stolen, so a
// crashed apply leaves it behind and every later transaction fails with
// scope-lease-required until it is removed. Before `pika recover` there
// was no command that would remove it and nothing that named the file,
// which made a diagnosis an operator could not act on — so the
// remediation here names the command, not the symptom.
//
// Severity follows what the state actually costs. A stale lock or an
// orphaned journal is an error: the repository cannot transact, and a
// tree left half-mutated by an interrupted apply is worse than that. A
// transaction that is genuinely running is only a warning — reporting a
// normal `pika apply` as damage would teach an operator that doctor's
// verdict means nothing.
func checkRecovery(rep *Report, root *repopath.Root) {
	pending, err := txn.Inspect(root.Dir())
	if err != nil {
		rep.add("recovery", SeverityError, err.Error(),
			"inspect .project/state/recovery; \"pika recover\" reports what it finds there")
		return
	}
	if pending.Clean() {
		rep.add("recovery", SeverityOK, "no interrupted transaction", "")
		return
	}
	const remediation = "run \"pika recover\" to see what would be rolled back, then \"pika recover --apply\""
	switch l := pending.Lock; {
	case l != nil && l.Alive:
		rep.add("recovery", SeverityWarn,
			fmt.Sprintf("transaction %s is in progress: lock held by running pid %d since %s", l.TxID, l.PID, l.StartedAt),
			"wait for it to finish; \"pika recover\" refuses to touch a live transaction")
	case l != nil && l.Unreadable != "":
		rep.add("recovery", SeverityError,
			fmt.Sprintf("the recovery lock at %s names no holder (%s); every transaction on this repository will fail until it is gone", l.Path, l.Unreadable),
			"confirm no transaction is running, then remove "+l.Path)
	case l != nil:
		rep.add("recovery", SeverityError,
			fmt.Sprintf("stale recovery lock: transaction %s was held by pid %d since %s and that process is gone; every transaction on this repository will fail until it is released", l.TxID, l.PID, l.StartedAt),
			remediation)
	default:
		rep.add("recovery", SeverityError,
			fmt.Sprintf("%d uncommitted transaction journal(s) under %s with no lock; an interrupted apply may have left this tree half-mutated", len(pending.Txs), pending.Dir),
			remediation)
	}
}

func grantedKinds(env *envelope.Envelope) string {
	out, err := json.Marshal(env.Env.Allow)
	if err != nil {
		return "unreadable"
	}
	return string(out)
}

// checkGates reports each slot's resolved command, or the pack's hint
// when the slot is an undiscovered sentinel — Check.Hint's first consumer
// outside init. It never executes a gate: spawning real toolchains from a
// diagnostic command would make doctor slow and side-effecting, so the
// binary is probed with exec.LookPath instead.
//
// The two negative outcomes carry the severity `pika check` would give
// the same repository, because a diagnostic that disagrees with the
// kernel it diagnoses is worse than no diagnostic. An undiscovered slot
// is a warning: verify records it as StatusSkip, which does not fail the
// ladder. A resolved command whose binary is absent is an error: verify
// builds a real gate for it either way, exec.CommandContext fails with
// something that is not an *exec.ExitError, and runGate's default branch
// scores it StatusFail — exit 1.
func checkGates(rep *Report, c *contract.Contract, resolved *profiles.Resolved) {
	if c == nil || resolved == nil {
		return
	}
	hints := map[string][]string{
		"format":    resolved.Checks.Format.Hint,
		"lint":      resolved.Checks.Lint.Hint,
		"typecheck": resolved.Checks.Typecheck.Hint,
		"test":      resolved.Checks.Test.Hint,
		"smoke":     resolved.Checks.Smoke.Hint,
	}
	gates, err := verify.FromProfiles(resolved.Checks, c.Commands)
	if err != nil {
		rep.add("gates", SeverityError, err.Error(),
			"correct the commands map in the contract")
		return
	}
	for _, g := range gates {
		id := "gate." + g.ID
		if g.SkipReason != "" {
			remediation := "no command discovered and the pack offers no hint"
			if h := hints[g.ID]; len(h) > 0 {
				remediation = fmt.Sprintf("set commands.%s in the contract, for example %q", g.ID, strings.Join(h, " "))
			}
			rep.add(id, SeverityWarn, g.SkipReason, remediation)
			continue
		}
		if _, err := exec.LookPath(g.Cmd[0]); err != nil {
			rep.add(id, SeverityError,
				fmt.Sprintf("%s: %s is not on PATH", strings.Join(g.Cmd, " "), g.Cmd[0]),
				"install the toolchain; \"pika check\" runs this gate here and fails when the binary is absent")
			continue
		}
		rep.add(id, SeverityOK, strings.Join(g.Cmd, " "), "")
	}
}

func checkGit(rep *Report) {
	if _, err := exec.LookPath("git"); err != nil {
		rep.add("git", SeverityWarn, "git is not on PATH",
			"\"pika check --changed\" will fall back to running every gate")
		return
	}
	rep.add("git", SeverityOK, "git is available", "")
}
