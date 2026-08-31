package conformance

import (
	"errors"
	"os"
	"slices"
	"testing"
	"time"
)

// TestCorpus runs the documented adoption flow — adopt, review, apply,
// check — against every pinned repository in the manifest, and grades
// the result against what the manifest records.
//
// It is off unless PIKA_CONFORMANCE=1. It clones real repositories and
// spawns real toolchains, so it must never run inside `go test ./...` or
// `pika check`; a developer's ordinary run cannot be made to depend on
// github.com being up.
func TestCorpus(t *testing.T) {
	if os.Getenv(EnabledEnv) != "1" {
		t.Skipf("the conformance corpus did not run: it clones %d real repositories over the network and spawns their toolchains. "+
			"Enable it with %s=1 (for example `%s=1 go test ./internal/conformance/ -count=1 -v -timeout 30m`); "+
			"CI runs it as the scheduled `conformance` workflow.", len(Corpus), EnabledEnv, EnabledEnv)
	}
	h, err := newHarness()
	if err != nil {
		t.Fatalf("the corpus could not build the binary under test: %v", err)
	}
	t.Cleanup(func() {
		if err := h.close(); err != nil {
			t.Errorf("%v", err)
		}
	})
	t.Logf("binary under test: %s (cache: %s)", h.pika, h.cache)
	start := time.Now()
	for _, repo := range Corpus {
		t.Run(repo.Name, func(t *testing.T) { conform(t, h, repo) })
	}
	t.Logf("corpus: %d repositories in %s", len(Corpus), time.Since(start).Round(time.Millisecond))
}

// conform runs one row and grades it.
//
// Deliberately not a t.Helper: this IS the body of the subtest, and
// marking it one would collapse every finding onto the `t.Run` line
// above, which is the one place a maintainer reading a red corpus does
// not need to look.
func conform(t *testing.T, h *harness, r Repo) {
	if why := r.Missing(); why != "" {
		t.Skipf("not exercised on this machine: %s. %s is pinned at %s (%s).", why, r.URL, r.SHA[:12], r.Ref)
	}
	src, err := h.fetch(r)
	if errors.Is(err, ErrNetwork) {
		// Never a failure. Nothing about pika has been observed yet.
		t.Skipf("could not reach the network, so pika was never run: %v", err)
	}
	if err != nil {
		t.Fatalf("%v", err)
	}
	dir := t.TempDir()
	if err := checkout(src, dir); err != nil {
		t.Fatalf("the corpus could not copy the cached checkout of %s: %v", r.Name, err)
	}
	start := time.Now()

	// 1. adopt: inventory the repository and draft a contract.
	adopted, err := h.run(dir, "adopt", "--json")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if adopted.exit != 0 {
		t.Fatalf("`pika adopt` exited %d on %s@%s\n%s", adopted.exit, r.URL, r.SHA[:12], adopted)
	}
	var rep AdoptReport
	env, err := unwrap(adopted, "adopt", &rep)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !env.OK {
		t.Errorf("`pika adopt` reported ok=false on %s\n%s", r.Name, adopted)
	}
	if !slices.Equal(rep.DetectedProfiles, r.Profiles) {
		t.Errorf("adopt detected %v on %s; the manifest records %v.\n%s",
			rep.DetectedProfiles, r.Name, r.Profiles, r.Why)
	}
	// Every naming deviation adopt reports to the human must also be in
	// the contract it writes, or the contract cannot pass its own gate 1.
	for _, missing := range Unrecorded(rep) {
		t.Errorf("%s: %s", r.Name, missing)
	}

	// 2. apply: promote the drafts into a live contract.
	applied, err := h.run(dir, "apply", "--json")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if applied.exit != 0 {
		t.Fatalf("`pika apply` exited %d on %s\n%s", applied.exit, r.Name, applied)
	}
	if env, err := unwrap(applied, "apply", nil); err != nil {
		t.Fatalf("%v", err)
	} else if !env.OK {
		t.Fatalf("`pika apply` reported ok=false on %s\n%s", r.Name, applied)
	}

	// 3. check: run the ladder the applied contract declares.
	checked, err := h.run(dir, "check", "--all", "--json")
	if err != nil {
		t.Fatalf("%v", err)
	}
	var report Report
	if _, err := unwrap(checked, "check", &report); err != nil {
		t.Fatalf("%v", err)
	}

	// Gate 1 first, and on its own. An adoption that produces a
	// repository which cannot pass its own contract check is the defect
	// this corpus was built around: adopt exits 0, apply exits 0, and
	// the first rung then fails on a name adopt already saw and did not
	// record — which skips every later rung, so the whole ladder goes
	// dark behind one line.
	if g, ok := gateOf(report, "contract"); !ok {
		t.Errorf("%s: `check --all` reported no contract gate at all\nladder: %s", r.Name, report.Ladder())
	} else if g.Status != StatusPass {
		t.Errorf("%s: gate 1 (contract) did not pass after `pika apply`, so the adoption produced a repository that cannot pass its own check\n    %s\nladder: %s",
			r.Name, g.Evidence(), report.Ladder())
	}

	// Claims no repository is allowed to break, whatever the manifest
	// expects of it.
	for _, contradiction := range Coherent(report.Gates) {
		t.Errorf("%s: %s", r.Name, contradiction)
	}

	// The recorded outcome, in both directions: an unexpected pass is a
	// disagreement in the same words as an unexpected failure.
	for _, d := range r.Grade(report.Gates) {
		t.Errorf("%s: %s", r.Name, d)
	}
	if report.Pass != r.Pass() {
		t.Errorf("%s: the ladder reports pass=%v; the manifest expects pass=%v\nladder: %s", r.Name, report.Pass, r.Pass(), report.Ladder())
	}
	// The exit code is what a CI job reads, and it must agree with the
	// report printed beside it.
	wantExit := 0
	if !r.Pass() {
		wantExit = 1
	}
	if checked.exit != wantExit {
		t.Errorf("%s: `pika check --all` exited %d; the manifest's ladder wants %d\nladder: %s", r.Name, checked.exit, wantExit, report.Ladder())
	}
	// Once, at the end, and only when something disagreed: what other
	// than a change to pika could have moved this row. It is the first
	// question a maintainer reading a red corpus has to answer, and
	// repeating it beside every cascaded skip would bury the answer in
	// the noise it is meant to cut through.
	if t.Failed() && r.Drift != "" {
		t.Logf("%s: before treating that as a pika regression, note what else can move this row: %s", r.Name, r.Drift)
	}

	t.Logf("%s %s@%s  profiles=%v  ladder: %s  (%s)",
		verdict(t), r.URL, r.SHA[:12], rep.DetectedProfiles, report.Ladder(), time.Since(start).Round(time.Millisecond))
}

// gateOf returns the named rung.
func gateOf(r Report, id string) (Gate, bool) {
	for _, g := range r.Gates {
		if g.ID == id {
			return g, true
		}
	}
	return Gate{}, false
}

// verdict is the one-word prefix of a row's summary line, so a scrolled
// log reads top to bottom without cross-referencing the failures.
func verdict(t *testing.T) string {
	if t.Failed() {
		return "WRONG"
	}
	return "OK   "
}
