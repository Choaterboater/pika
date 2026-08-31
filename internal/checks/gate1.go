package checks

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/skills"
	"github.com/Choaterboater/pika/internal/version"
)

// Gate1 runs verification-ladder rung 1 (spec §12.6): the contract
// schema-version ceiling, the exceptions record load, the profile-lock
// and skills-projection digest checks, and the naming/ownership
// projection checks. It is the single implementation shared by the
// `pika check` command and the MCP run_checks tool so agents and humans
// always agree on gate 1.
//
// An error-severity violation — or an exceptions file that fails to load
// (unverifiable records must not silently widen the rules) — fails the
// gate: exit 1 with the findings joined as gate output, and any
// warnings accumulated before the failure are still returned. A
// warning-severity violation is a review signal returned as a warning
// without failing. Exit is 0 when nothing error-severity was found.
func Gate1(repoRoot string, c *contract.Contract, resolved *profiles.Resolved) (exit int, output string, warnings []string) {
	if err := version.Check(c.Schema); err != nil {
		return 1, err.Error(), nil
	}
	exceptions, err := LoadExceptions(repoRoot)
	if err != nil {
		return 1, err.Error(), nil
	}
	// Spec §16: CI validates the contract and profile locks; §5.3 pins
	// the profile versions in profiles.lock. An unpinned, stale, or
	// drifted lock is a gate failure, never a silent pass.
	if err := CheckLock(repoRoot, c); err != nil {
		return 1, err.Error(), nil
	}
	// Spec §9.2: a harness-native projection identifies its source and
	// digest, and CI rejects drift rather than maintaining parallel
	// handwritten copies. This is that rejection. It is beside the lock
	// check because it is the same kind of statement — a generated
	// artifact certifying bytes that have since moved — and because a
	// projection that lies to an agent is worse than one that lies to a
	// human: the agent cannot notice.
	if err := skills.Verify(repoRoot, c, resolved); err != nil {
		return 1, err.Error(), nil
	}
	var findings []string
	unreviewed := 0
	for _, list := range exceptions {
		for _, ex := range list {
			if ex.Owner == AutoRecordedOwner {
				unreviewed++
			}
		}
	}
	if unreviewed > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d exception(s) in %s are still owned by %q; nothing forces a human to accept them — `pika exceptions reassign --owner <name>` to reassign them, or record why the default stands",
			unreviewed, ExceptionsFile, AutoRecordedOwner))
	}
	for _, v := range Naming(repoRoot, resolved.NamingRules, exceptions) {
		line := fmt.Sprintf("%s: %s: %s", v.RuleID, v.Path, v.Message)
		if v.Severity == SeverityError {
			findings = append(findings, line)
			continue
		}
		warnings = append(warnings, line)
	}
	if len(findings) > 0 {
		return 1, strings.Join(findings, "\n"), warnings
	}
	return 0, "", warnings
}

// LockDisagreementCauses states what a digest disagreement between a
// repository's lock and this binary's embedded packs does and does not
// prove, and LockDisagreementRemedy is the one action that tells the two
// causes apart. They are exported because `pika doctor` reports the same
// finding and must say the same thing about it.
//
// The message they replace named one cause and prescribed its remedy:
// "regenerate the lock with `pika init --force`". That remedy is correct
// when the lock is the stale side and destructive when the binary is —
// it rewrites a correct lock to pin whatever older packs the running
// build happens to carry, and the repository is downgraded silently,
// green. The digests cannot distinguish the two cases: they are two
// numbers that differ, and nothing in either says which was written
// later. So the gate states the disagreement, prints both numbers, names
// both causes, and hands over the comparison that settles it. This is
// the discipline `pika recover` already applies to a lease it cannot
// judge and `doctor` applies to a holder it cannot verify: a check that
// cannot tell two causes apart must not prescribe the destructive one as
// if it were the only one.
//
// Both are whole sentences, capitalized: they follow the finding's own
// sentence in the gate's message, and they are the operator's paragraph,
// not an error prefix. LockDisagreementAction is the same instruction at
// the length a report column can carry — doctor prints the full prose in
// the finding's detail, so its remediation line summarizes rather than
// repeats. Keeping all three here is what stops the short form from
// drifting into advice the long form does not give.
const (
	LockDisagreementCauses = "The lock and this pika disagree about the pack bytes, and the digests alone cannot say which side is behind: either the lock is stale (the packs moved on after it was written) or this binary is stale (the lock is correct and this pika predates it)."
	LockDisagreementRemedy = "Compare `pika version` here against the pika that wrote the lock, or read the lock's provenance in version control, to establish which side is behind; only if the lock is the stale side, regenerate it with `pika init --force` — running that on a stale binary rewrites a correct lock to pin older packs and downgrades the repository silently."
	LockDisagreementAction = "run `pika version` here and on the pika that wrote the lock — the build whose registry digest matches the lock is the one that wrote it; regenerate with `pika init --force` only if the lock is the stale side"
)

// CheckLock verifies .project/profiles.lock against the contract's
// profile selection (spec §16, §5.3): the lock must exist, must pin
// every contract profile ref at the contract's version, every pinned
// digest must match the embedded registry's current digest for that
// pack, and the lock's top-level digest must match this binary's whole
// embedded pack registry. It is exported so `pika doctor` diagnoses lock
// health with gate 1's own implementation instead of a second one that
// could disagree.
func CheckLock(repoRoot string, c *contract.Contract) error {
	// The lock's location is owned by repopath, never spelled out here:
	// gate 1 must look under exactly the root its caller resolved.
	root, err := repopath.At(repoRoot)
	if err != nil {
		return fmt.Errorf("profiles.lock: %w", err)
	}
	lock, err := profiles.ReadLock(root.Lock())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s missing; run `pika init` (or `pika adopt`) to write the profile lock (spec §5.3)", filepath.ToSlash(root.Lock()))
		}
		return fmt.Errorf("profiles.lock: %w", err)
	}
	var problems []string
	// disagreement records that at least one problem is a digest this
	// binary and the lock state differently, which is the finding whose
	// cause the kernel cannot determine. The pack-version and
	// missing-pin problems above it are contract-versus-lock: both sides
	// are in the repository, so regenerating is unambiguously right
	// there and stays prescribed.
	disagreement := false
	for _, ref := range ProfileRefs(c) {
		name, wantVersion, ok := strings.Cut(ref, "@")
		if !ok || name == "" || wantVersion == "" {
			problems = append(problems, fmt.Sprintf("contract profile ref %q is not a pack reference (expected name@version)", ref))
			continue
		}
		pinned, ok := lock.Packs[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("pack %s (contract ref %s) is not pinned in profiles.lock; regenerate the lock with `pika init --force`", name, ref))
			continue
		}
		if pinned.Version != wantVersion {
			problems = append(problems, fmt.Sprintf("pack %s pinned at version %s in profiles.lock, contract requires %s; regenerate the lock with `pika init --force`", name, pinned.Version, wantVersion))
			continue
		}
		digest, ok := profiles.PackDigestFor(ref)
		if !ok {
			problems = append(problems, fmt.Sprintf("contract profile ref %q is not a registered pack", ref))
			continue
		}
		if pinned.Digest != digest {
			problems = append(problems, fmt.Sprintf("pack %s is pinned in profiles.lock at digest %s, and this pika's embedded pack %s is %s", name, pinned.Digest, ref, digest))
			disagreement = true
		}
	}
	// The top-level digest covers the entire embedded registry, not just
	// the selected packs, so it is the field that catches a lock written
	// by a pika built from different pack bytes — including a pika newer
	// than the one running now. profiles.WriteLock is the only writer,
	// so a difference means the two builds carry different packs (or the
	// lock was hand-edited); it never means the lock is the wrong side.
	if want := profiles.PackDigest(); lock.Digest != want {
		problems = append(problems, fmt.Sprintf("profiles.lock records registry digest %s, and this pika's embedded pack registry is %s", lock.Digest, want))
		disagreement = true
	}
	if len(problems) > 0 {
		msg := "profiles.lock: " + strings.Join(problems, "; ")
		if disagreement {
			msg += ". " + LockDisagreementCauses + " " + LockDisagreementRemedy
		}
		return errors.New(msg)
	}
	return nil
}

// ProfileRefs collects the contract's profile refs: the project-level
// selection plus every package's profiles, deduplicated in sorted
// order. It is the shared view of "which packs does this contract
// select" used by the lock check and by `pika apply`.
func ProfileRefs(c *contract.Contract) []string {
	var refs []string
	refs = append(refs, c.Profiles...)
	for _, p := range c.Packages {
		refs = append(refs, p.Profiles...)
	}
	slices.Sort(refs)
	return slices.Compact(refs)
}
