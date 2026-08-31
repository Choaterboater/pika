package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// The steps that need no agent: a fresh repository, the generated
// projection, the lock, and the diagnosis. The repair lifecycle is next
// door in improve.go.

// runCheck runs the ladder in dir and decodes the report. The bool is
// false when the payload could not be read at all, at which point no
// assertion about a gate means anything.
func runCheck(c *check, h *harness, dir string) (report, result, bool) {
	var rep report
	r, err := h.run(dir, nil, "check", "--all", "--json")
	if err != nil {
		c.failf("%v", err)
		return rep, r, false
	}
	env, ok := decodeEnvelope(c, r, "check")
	if !ok {
		return rep, r, false
	}
	if !decodeResult(c, env, r, "check report", &rep) {
		return rep, r, false
	}
	// ok is the boolean the exit code is derived from and pass is the
	// ladder's own verdict; a payload where they disagree sends an agent
	// down the wrong branch, whichever one is right.
	wantEqual(c, "`pika check --json` envelope ok against the report's own pass", env.OK, rep.Pass)
	return rep, r, true
}

// wantGate asserts one rung's status.
//
// The failure carries the rung's own evidence and a one-line digest of
// the ladder around it, not the whole report: a step that asserts six
// rungs would otherwise paste the same report six times, and a message
// nobody reads to the end is a message that named nothing. The full
// report is reproduced once, by the assertion on the ladder's verdict.
func wantGate(c *check, rep report, id, status string) gate {
	g, ok := rep.gate(id)
	if !ok {
		c.failf("the ladder has no %q gate at all\n%s", id, quoteBlock("check report", rep.String()))
		return gate{}
	}
	if g.Status != status {
		c.failf("gate %s: expected status %q, got %q\n%s\n%s", id, status, g.Status, g.evidence(), rep.ladder())
	}
	return g
}

// stepInit is the claim a repository is created against: `pika init`
// writes a repository, and the ladder is green in it before anybody has
// touched anything. A scaffold that needs a fix before it passes its own
// gates is a scaffold nobody can start from.
func stepInit(h *harness) error {
	c := &check{}
	dir, init, err := h.scaffold("init")
	if err != nil {
		return err
	}
	// The scaffold is a set of files on disk, not a success message.
	for _, rel := range []string{
		".project/contract.yaml",
		".project/profiles.lock",
		".project/exceptions.yaml",
		"AGENTS.md",
		"README.md",
		"CONTRIBUTING.md",
		".github/workflows/ci.yml",
		entryPath,
	} {
		if _, err := readRepo(dir, rel); err != nil {
			c.failf("`pika init` exited 0 but did not write %s: %v\n%s", rel, err, init)
		}
	}

	rep, r, ok := runCheck(c, h, dir)
	if !ok {
		return c.err()
	}
	wantEqual(c, "`pika check --all` exit code in a freshly initialized repository", r.exit, 0)
	for _, id := range []string{"contract", "format", "lint", "typecheck", "test"} {
		wantGate(c, rep, id, "pass")
	}
	// The go@1 pack offers no smoke hint, so a scaffolded repository's
	// rung 5 skips with a recorded reason. That is the honest state and
	// it is asserted rather than tolerated: a skip that silently became
	// a pass is the failure mode this whole program exists to end.
	smoke := wantGate(c, rep, "smoke", "skip")
	c.contains("the skipped smoke gate's reason", smoke.Reason, "no command discovered")
	c.truef(rep.Pass, "the ladder is not green in a freshly initialized repository\n%s",
		quoteBlock("check report", rep.String()))
	wantEqual(c, "failed gates in a freshly initialized repository", rep.Summary.Fail, 0)
	return c.err()
}

const (
	beginMarker = "<!-- pika:skills:begin -->"
	endMarker   = "<!-- pika:skills:end -->"
)

// stepSkills covers the generated half of a repository: `pika init`
// already scaffolds a skills block declaring AGENTS.md as a codex
// projection and installs the four canonical skills, `pika skills
// install` is idempotent over that state, the ladder is green with the
// result, and an edit INSIDE the markers fails `pika check` naming the
// file.
//
// The tamper case is the one worth running the product for. The region
// asserts in its own header that it is kernel-owned; without a gate that
// recomputes its digest, that assertion is a claim the repository cannot
// back — and the remedy for the two ways a region goes wrong are
// opposites, so a check that could not tell them apart would tell an
// operator whose edit is about to be destroyed to run the command that
// destroys it.
func stepSkills(h *harness) error {
	c := &check{}
	dir, _, err := h.scaffold("skills")
	if err != nil {
		return err
	}

	install, err := h.run(dir, nil, "skills", "install")
	if err != nil {
		return err
	}
	wantEqual(c, "`pika skills install` exit code", install.exit, 0)
	agents, err := readRepo(dir, "AGENTS.md")
	if err != nil {
		return err
	}
	// The region is what the gate later recomputes, so its parts are
	// asserted before anything is done to them.
	c.contains("AGENTS.md after `pika skills install`", agents,
		beginMarker, endMarker, "<!-- pika:region sha256:", "<!-- pika:source skill ")

	if rep, r, ok := runCheck(c, h, dir); ok {
		wantEqual(c, "`pika check --all` exit code after `pika skills install`", r.exit, 0)
		c.truef(rep.Pass, "the ladder is not green with a freshly installed projection\n%s",
			quoteBlock("check report", rep.String()))
	}

	// Edit kernel-owned bytes. The insertion point is inside the
	// markers, and the markers are located rather than assumed: a tamper
	// that silently landed outside the region would make a broken gate
	// look like a working one.
	end := strings.Index(agents, endMarker)
	if end < 0 {
		return fmt.Errorf("AGENTS.md carries no %s, so there is no region to tamper with:\n%s", endMarker, excerpt(agents, outputExcerpt))
	}
	const handEdit = "An operator edited this line inside the kernel-owned region.\n"
	if err := writeRepo(dir, "AGENTS.md", agents[:end]+handEdit+agents[end:]); err != nil {
		return err
	}

	rep, r, ok := runCheck(c, h, dir)
	if !ok {
		return c.err()
	}
	wantEqual(c, "`pika check --all` exit code with a hand-edited kernel region", r.exit, 1)
	c.truef(!rep.Pass, "the ladder is green with a hand-edited kernel-owned region\n%s",
		quoteBlock("check report", rep.String()))
	g := wantGate(c, rep, "contract", "fail")
	// Naming the file is the whole point: the operator has to be sent to
	// the bytes that moved, and told that regenerating discards them.
	c.contains("the contract gate's report of a hand-edited region", g.OutputTail,
		"AGENTS.md", "tampered", "DISCARD", "pika skills install")
	return c.err()
}

// foreignDigest stands in for a lock written by a pika carrying
// different pack bytes. Its value is arbitrary and deliberately not a
// real digest of anything: the point is that two numbers differ, and
// that nothing in either says which side is behind.
const foreignDigest = "beefbeefbeefbeefbeefbeefbeefbeefbeefbeefbeefbeefbeefbeefbeefbeef"

// stepLock covers the digest disagreement and the remedy that used to
// corrupt a correct repository.
//
// A lock whose digests differ from the running binary's has two possible
// causes with opposite remedies — the lock is stale, or the BINARY is —
// and the digests cannot tell them apart. The message used to name one
// cause and prescribe `pika init --force`, which on a stale binary
// rewrites a correct lock to pin older packs and then reports green: a
// silent downgrade. So this asserts that both causes are named, that the
// destructive remedy appears once and only under the condition that
// scopes it, and — because a remedy an operator cannot carry out is not
// one — that `pika version` actually supplies the comparison it tells
// them to make.
func stepLock(h *harness) error {
	c := &check{}
	dir, _, err := h.scaffold("lock")
	if err != nil {
		return err
	}

	healthy, ok := readVersion(c, h, dir)
	if !ok {
		return c.err()
	}
	c.truef(healthy.Version != "", "`pika version` reports no release at all")
	c.truef(len(healthy.RegistryDigest) == 64,
		"`pika version` reports registry digest %q, want 64 hex characters identifying this build's packs", healthy.RegistryDigest)
	if healthy.Lock == nil {
		c.failf("`pika version --json` reports no lock section in a repository that has a lock at %s",
			filepath.Join(dir, ".project", "profiles.lock"))
		return c.err()
	}
	c.truef(healthy.Lock.Matches,
		"`pika version` says a freshly initialized repository's lock does not match the binary that wrote it: binary %s, lock %s",
		healthy.RegistryDigest, healthy.Lock.RegistryDigest)
	wantEqual(c, "the lock digest `pika version` reports against this binary's", healthy.Lock.RegistryDigest, healthy.RegistryDigest)

	if err := corruptLock(dir); err != nil {
		return err
	}

	rep, r, ok := runCheck(c, h, dir)
	if !ok {
		return c.err()
	}
	wantEqual(c, "`pika check --all` exit code against a lock from a different pack set", r.exit, 1)
	g := wantGate(c, rep, "contract", "fail")
	tail := g.OutputTail
	// Both numbers, so the finding can be attributed at all.
	c.contains("the lock-disagreement message", tail, foreignDigest, healthy.RegistryDigest)
	// Both causes, neither asserted.
	c.contains("the lock-disagreement message", tail, "the lock is stale", "this binary is stale")
	// And the destructive remedy exactly once, after the condition that
	// scopes it — an agent reading top-down must not reach it first.
	c.truef(strings.Count(tail, "pika init --force") == 1,
		"the lock-disagreement message prescribes `pika init --force` %d times; it must appear once, under its condition\n%s",
		strings.Count(tail, "pika init --force"), quoteBlock("contract gate output", tail))
	condition := strings.Index(tail, "only if the lock is the stale side")
	c.truef(condition >= 0 && condition < strings.Index(tail, "pika init --force"),
		"the lock-disagreement message prescribes `pika init --force` before conditioning it on the lock being the stale side\n%s",
		quoteBlock("contract gate output", tail))

	// The remedy tells the operator to compare `pika version` here
	// against the pika that wrote the lock. That comparison has to be
	// available, and it has to report the disagreement rather than
	// shrugging.
	stale, ok := readVersion(c, h, dir)
	if !ok {
		return c.err()
	}
	if stale.Lock == nil {
		c.failf("`pika version --json` drops the lock section when the lock disagrees, which is exactly when it is needed")
		return c.err()
	}
	c.truef(!stale.Lock.Matches, "`pika version` reports a lock pinned at %s as matching this binary's %s",
		stale.Lock.RegistryDigest, stale.RegistryDigest)
	wantEqual(c, "the lock digest `pika version` reports from a corrupted lock", stale.Lock.RegistryDigest, foreignDigest)
	wantEqual(c, "this binary's own registry digest, which a repository's lock must never change", stale.RegistryDigest, healthy.RegistryDigest)
	return c.err()
}

// readVersion runs `pika version --json` in dir and decodes it.
func readVersion(c *check, h *harness, dir string) (versionResult, bool) {
	var v versionResult
	r, err := h.run(dir, nil, "version", "--json")
	if err != nil {
		c.failf("%v", err)
		return v, false
	}
	wantEqual(c, "`pika version` exit code", r.exit, 0)
	env, ok := decodeEnvelope(c, r, "version")
	if !ok {
		return v, false
	}
	return v, decodeResult(c, env, r, "version", &v)
}

// corruptLock rewrites every digest in the repository's lock to a
// foreign value, which is what a lock written by a pika carrying
// different pack bytes looks like from here.
func corruptLock(dir string) error {
	const rel = ".project/profiles.lock"
	raw, err := readRepo(dir, rel)
	if err != nil {
		return err
	}
	var lock map[string]any
	if err := json.Unmarshal([]byte(raw), &lock); err != nil {
		return fmt.Errorf("%s is not the JSON document this step corrupts: %w\n%s", rel, err, raw)
	}
	lock["digest"] = foreignDigest
	packs, ok := lock["packs"].(map[string]any)
	if !ok || len(packs) == 0 {
		return fmt.Errorf("%s records no packs to corrupt:\n%s", rel, raw)
	}
	for _, entry := range packs {
		pack, ok := entry.(map[string]any)
		if !ok {
			return fmt.Errorf("%s records a pack entry that is not an object:\n%s", rel, raw)
		}
		pack["digest"] = foreignDigest
	}
	out, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	return writeRepo(dir, rel, string(out)+"\n")
}

// stepDoctor covers the read-only diagnosis. It executes no gate, so it
// is the one command an operator can run in any state — and a doctor
// that reports a problem in a repository that has none would send them
// looking for it.
//
// It runs against the harness's own home directory rather than the
// operator's: doctor reports on the agent files installed under ~, and a
// gate whose verdict depends on what the developer happens to have there
// is not a gate. It reads that directory and never writes to it.
func stepDoctor(h *harness) error {
	c := &check{}
	dir, _, err := h.scaffold("doctor")
	if err != nil {
		return err
	}

	r, err := h.run(dir, h.homeEnv(), "doctor", "--json")
	if err != nil {
		return err
	}
	wantEqual(c, "`pika doctor` exit code in a healthy repository", r.exit, 0)
	env, ok := decodeEnvelope(c, r, "doctor")
	if !ok {
		return c.err()
	}
	var rep doctorReport
	if !decodeResult(c, env, r, "doctor", &rep) {
		return c.err()
	}
	c.truef(rep.OK, "`pika doctor` reports a healthy repository as not ok\n%s", quoteBlock("doctor report", rep.String()))
	if bad := rep.errors(); len(bad) > 0 {
		c.failf("`pika doctor` reports %d error-severity finding(s) in a freshly initialized repository:\n%s",
			len(bad), strings.Join(bad, "\n"))
	}
	// The findings that decide whether this repository is usable at all.
	// A doctor that exits 0 because it looked at nothing would satisfy
	// every assertion above it.
	for _, id := range []string{"contract", "lock", "exceptions", "gate.format", "gate.test", "git"} {
		found := false
		for _, f := range rep.Findings {
			if f.ID == id {
				found = true
				c.truef(f.Severity == "ok", "`pika doctor` reports %s as %q in a healthy repository: %s", id, f.Severity, f.Detail)
			}
		}
		c.truef(found, "`pika doctor` never reports on %s\n%s", id, quoteBlock("doctor report", rep.String()))
	}

	// And the human path prints something, rather than being a JSON
	// command with a text mode nobody ever runs.
	text, err := h.run(dir, h.homeEnv(), "doctor")
	if err != nil {
		return err
	}
	wantEqual(c, "`pika doctor` exit code without --json", text.exit, 0)
	c.contains("`pika doctor` plain-text output", text.stdout, "root", dir, "contract", "lock")
	return c.err()
}
