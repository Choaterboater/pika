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
	"github.com/Choaterboater/pika/internal/version"
)

// Gate1 runs verification-ladder rung 1 (spec §12.6): the contract
// schema-version ceiling, the exceptions record load, and the
// naming/ownership projection checks. It is the single implementation
// shared by the `pika check` command and the MCP run_checks tool so
// agents and humans always agree on gate 1.
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
	if err := checkLock(repoRoot, c); err != nil {
		return 1, err.Error(), nil
	}
	var findings []string
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

// checkLock verifies .project/profiles.lock against the contract's
// profile selection (spec §16, §5.3): the lock must exist, must pin
// every contract profile ref at the contract's version, and every
// pinned digest must match the embedded registry's current digest for
// that pack.
func checkLock(repoRoot string, c *contract.Contract) error {
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
			problems = append(problems, fmt.Sprintf("pack %s digest %s in profiles.lock does not match the embedded pack %s; regenerate the lock with `pika init --force`", name, pinned.Digest, ref))
		}
	}
	if len(problems) > 0 {
		return errors.New("profiles.lock: " + strings.Join(problems, "; "))
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
