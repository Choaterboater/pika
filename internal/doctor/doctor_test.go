package doctor

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repolease"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/skills"
)

// writeProject lays down the smallest repository gate 1 accepts at dir:
// a contract selecting the given profile refs, the matching profile lock
// written by profiles.WriteLock (the only writer that produces digests
// this binary's embedded registry agrees with), and an empty exceptions
// record.
func writeProject(t *testing.T, dir string, refs ...string) {
	t.Helper()
	project := filepath.Join(dir, ".project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	selection := "["
	for i, ref := range refs {
		if i > 0 {
			selection += ", "
		}
		selection += ref
	}
	selection += "]"
	doc := `schema: 1
project:
  name: fixture
  topology: single
profiles: ` + selection + `
github:
  merge: squash
evidence:
  publish: sanitized
commands:
  test: "true"
`
	if err := os.WriteFile(filepath.Join(project, "contract.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := profiles.WriteLock(filepath.Join(project, "profiles.lock"), refs); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "exceptions.yaml"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeHealthyProject(t *testing.T, dir string) {
	t.Helper()
	writeProject(t, dir, "core@1")
}

func writeHealthyTypeScriptProject(t *testing.T, dir string) {
	t.Helper()
	writeProject(t, dir, "core@1", "typescript@1")
}

// runDoctor is Run against a home directory that is not the developer's.
//
// Every test in this file is about a repository, and none of them may
// touch the operator's real home: doctor's global-skills row is answered
// from an empty temporary directory instead, so the report reads the
// same on a machine that has a global install and one that does not.
func runDoctor(t *testing.T, root *repopath.Root) *Report {
	t.Helper()
	return Run(root, t.TempDir())
}

func findingByID(t *testing.T, rep *Report, id string) Finding {
	t.Helper()
	for _, f := range rep.Findings {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no finding %q in %+v", id, rep.Findings)
	return Finding{}
}

func TestUnadoptedRepositoryIsReportedNotFailed(t *testing.T) {
	root, err := repopath.At(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rep := runDoctor(t, root)

	f := findingByID(t, rep, "contract")
	if f.Severity != SeverityError {
		t.Errorf("contract severity = %q, want %q", f.Severity, SeverityError)
	}
	if f.Remediation == "" {
		t.Error("contract finding has no remediation")
	}
	if rep.OK {
		t.Error("OK = true for an unadopted repository")
	}
	// doctor itself must not panic or bail: it reports every category
	// even when the contract is missing.
	for _, id := range []string{"root", "contract", "lock", "envelope", "git"} {
		findingByID(t, rep, id)
	}
}

func TestHealthyProjectReportsOK(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}

	rep := runDoctor(t, root)
	for _, f := range rep.Findings {
		if f.Severity == SeverityError {
			t.Errorf("unexpected error finding %q: %s", f.ID, f.Detail)
		}
	}
	if !rep.OK {
		t.Error("OK = false for a healthy project")
	}
	if findingByID(t, rep, "root").Detail == "" {
		t.Error("root finding does not report how the root was resolved")
	}
}

func TestDriftedLockIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	lock := filepath.Join(dir, ".project", "profiles.lock")
	if err := os.WriteFile(lock, []byte(`{"digest":"deadbeef","packs":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	root, _ := repopath.At(dir)

	if got := findingByID(t, runDoctor(t, root), "lock").Severity; got != SeverityError {
		t.Fatalf("lock severity = %q, want %q", got, SeverityError)
	}
}

func TestMissingEnvelopeIsAWarningNotAnError(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	root, _ := repopath.At(dir)

	f := findingByID(t, runDoctor(t, root), "envelope")
	if f.Severity != SeverityWarn {
		t.Fatalf("envelope severity = %q, want %q", f.Severity, SeverityWarn)
	}
	if f.Remediation == "" {
		t.Error("envelope finding must point at pika authorize")
	}
}

// Check.Hint is resolved today and read by nobody. doctor is its first
// consumer: an undiscovered slot must surface the pack's suggestion.
func TestUndiscoveredGateSurfacesPackHint(t *testing.T) {
	dir := t.TempDir()
	writeHealthyTypeScriptProject(t, dir)
	root, _ := repopath.At(dir)

	f := findingByID(t, runDoctor(t, root), "gate.lint")
	if f.Severity != SeverityWarn {
		t.Errorf("gate.lint severity = %q, want %q", f.Severity, SeverityWarn)
	}
	// The pack's lint hint is [npm, run, lint]; asserting the rendered
	// hint content is the only way this test can detect the hints map
	// being disconnected, since the fallback remediation is also
	// non-empty.
	if !strings.Contains(f.Remediation, "npm run lint") {
		t.Fatalf("gate.lint remediation = %q, want it to surface the pack hint %q", f.Remediation, "npm run lint")
	}
}

// A never-checked lock is not a second failure: one missing contract must
// yield one error. The lock finding still exists so every category is
// reported, but at warn severity.
func TestNeverCheckedLockIsAWarning(t *testing.T) {
	root, err := repopath.At(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := findingByID(t, runDoctor(t, root), "lock").Severity; got != SeverityWarn {
		t.Errorf("lock severity with no contract = %q, want %q", got, SeverityWarn)
	}

	// Symmetry: an unresolvable profile must also leave a lock finding,
	// with the same never-checked shape.
	dir := t.TempDir()
	writeProject(t, dir, "core@1")
	contractPath := filepath.Join(dir, ".project", "contract.yaml")
	doc, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	doc = bytes.Replace(doc, []byte("profiles: [core@1]"), []byte("profiles: [core@1, nosuchpack@1]"), 1)
	if err := os.WriteFile(contractPath, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	root2, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}
	rep := runDoctor(t, root2)
	if got := findingByID(t, rep, "profiles").Severity; got != SeverityError {
		t.Errorf("profiles severity = %q, want %q", got, SeverityError)
	}
	f := findingByID(t, rep, "lock")
	if f.Severity != SeverityWarn {
		t.Errorf("lock severity with unresolvable profile = %q, want %q", f.Severity, SeverityWarn)
	}
	if f.Detail != "not checked: profiles did not resolve" {
		t.Errorf("lock detail = %q", f.Detail)
	}
}

// doctor is a diagnostic: it must report what a gate WOULD run without
// running it. A contract command that would fail loudly if executed must
// still produce an ok finding, because only its presence on PATH is
// probed.
func TestDoctorNeverExecutesAGate(t *testing.T) {
	dir := t.TempDir()
	writeProject(t, dir, "core@1")
	marker := filepath.Join(dir, "ran")
	contractPath := filepath.Join(dir, ".project", "contract.yaml")
	doc, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	// touch <marker> exits 0 and leaves a file behind: if doctor ever
	// spawns the gate, the marker proves it.
	doc = append(doc, []byte("  smoke: \"touch "+marker+"\"\n")...)
	if err := os.WriteFile(contractPath, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	root, _ := repopath.At(dir)

	if got := findingByID(t, runDoctor(t, root), "gate.smoke").Severity; got != SeverityOK {
		t.Fatalf("gate.smoke severity = %q, want %q", got, SeverityOK)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("doctor executed the smoke gate")
	}
}

// doctor must not report a state the kernel does not enforce. A resolved
// gate whose binary is missing is a hard failure in `pika check` — verify
// builds the gate regardless of PATH and scores the spawn failure
// StatusFail — so doctor must call it an error and exit non-zero too.
// Anything softer sends an operator into CI believing the repository is
// merely imperfect.
func TestMissingGateBinaryIsAnError(t *testing.T) {
	const absent = "pika-no-such-binary-4f21c8"
	// Non-vacuous by construction: if this name ever resolves, the test
	// would be asserting nothing.
	if path, err := exec.LookPath(absent); err == nil {
		t.Fatalf("%s unexpectedly exists at %s", absent, path)
	}
	dir := t.TempDir()
	writeProject(t, dir, "core@1")
	contractPath := filepath.Join(dir, ".project", "contract.yaml")
	doc, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	doc = append(doc, []byte("  smoke: \""+absent+" --run\"\n")...)
	if err := os.WriteFile(contractPath, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	root, _ := repopath.At(dir)

	rep := runDoctor(t, root)
	f := findingByID(t, rep, "gate.smoke")
	if f.Severity != SeverityError {
		t.Errorf("gate.smoke severity = %q, want %q", f.Severity, SeverityError)
	}
	if !strings.Contains(f.Detail, absent) {
		t.Errorf("gate.smoke detail = %q, want it to name the missing binary", f.Detail)
	}
	if rep.OK {
		t.Error("a gate that cannot run must flip Report.OK false, matching `pika check` exit 1")
	}
}

// writeRecoveryLock stands a recovery lock up at dir held by pid.
func writeRecoveryLock(t *testing.T, dir string, pid int) {
	t.Helper()
	rec := filepath.Join(dir, ".project", "state", "recovery")
	if err := os.MkdirAll(rec, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"txId":"0000000000000001-c0ffee01","pid":` + strconv.Itoa(pid) + `,"startedAt":"2026-08-30T12:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(rec, "lock"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A stale recovery lock wedges every future transaction on the
// repository — `pika apply` fails with scope-lease-required until it is
// gone — and the lock is deliberately never stolen, so nothing clears it
// on its own. That is a hard failure, not a review signal, and doctor is
// where an operator goes to find out why a command will not run. The
// remediation has to name the command that fixes it, or the diagnosis
// leaves them exactly where they were.
func TestDoctorReportsAStaleTransactionLock(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	writeRecoveryLock(t, dir, 99999999)
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}

	rep := runDoctor(t, root)
	f := findingByID(t, rep, "recovery")
	if f.Severity != SeverityError {
		t.Errorf("recovery severity = %q, want %q: the repository cannot transact", f.Severity, SeverityError)
	}
	if !strings.Contains(f.Detail, "99999999") {
		t.Errorf("recovery detail = %q, want it to name the holder", f.Detail)
	}
	if !strings.Contains(f.Remediation, "pika recover") {
		t.Errorf("recovery remediation = %q, want it to name \"pika recover\"", f.Remediation)
	}
	if rep.OK {
		t.Error("OK = true with a stale lock: the next `pika apply` fails")
	}
}

// A transaction that is actually running is not damage. Reporting it as
// an error would make doctor fail during a normal `pika apply` and teach
// an operator that its verdict means nothing.
func TestDoctorReportsALiveTransactionWithoutFailing(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	writeRecoveryLock(t, dir, os.Getpid())
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}

	rep := runDoctor(t, root)
	f := findingByID(t, rep, "recovery")
	if f.Severity != SeverityWarn {
		t.Errorf("recovery severity = %q, want %q: a running transaction is not damage", f.Severity, SeverityWarn)
	}
	if rep.OK != true {
		t.Error("OK = false while a transaction is merely in progress")
	}
}

// The common case has to be quiet, and it has to be present: a finding
// that only appears when something is wrong is one an operator cannot
// tell apart from a check that was never run.
func TestDoctorReportsNoInterruptedTransaction(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}

	if f := findingByID(t, runDoctor(t, root), "recovery"); f.Severity != SeverityOK {
		t.Errorf("recovery = %+v, want an ok finding on a repository with nothing pending", f)
	}
}

// writeEnvelope lays down a capability envelope at dir granting exactly
// the given exec argv lines and nothing else.
func writeEnvelope(t *testing.T, dir string, execGrants ...string) {
	t.Helper()
	state := filepath.Join(dir, ".project", "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	quoted := make([]string, 0, len(execGrants))
	for _, g := range execGrants {
		quoted = append(quoted, strconv.Quote(g))
	}
	doc := "schema: 1\nallow:\n  exec: [" + strings.Join(quoted, ", ") + "]\n"
	if err := os.WriteFile(filepath.Join(state, "envelope.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

// addCommand appends one commands entry to the fixture contract.
func addCommand(t *testing.T, dir, slot, cmd string) {
	t.Helper()
	path := filepath.Join(dir, ".project", "contract.yaml")
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc = append(doc, []byte("  "+slot+": \""+cmd+"\"\n")...)
	if err := os.WriteFile(path, doc, 0o644); err != nil {
		t.Fatal(err)
	}
}

// findingIDs lists the ids of every finding whose id carries prefix.
func findingIDs(rep *Report, prefix string) []string {
	var out []string
	for _, f := range rep.Findings {
		if strings.HasPrefix(f.ID, prefix) {
			out = append(out, f.ID)
		}
	}
	return out
}

// TestDoctorWarnsWhenTheEnvelopeWouldDenyAGate covers the cross-check
// between the two halves of doctor that never spoke: the envelope it
// loads and the gate argv it resolves. Before this, the first notice an
// agent got that its envelope did not cover a gate was an
// envelope_denied from MCP run_checks, mid-task, with nothing in
// `pika doctor` predicting it.
//
// The fixture also pins the matching rule. `true --all` is denied by a
// grant of "true" because envelope.matchesExec compares whole argv lines
// element-wise; a cross-check written against g.Cmd[0] would call it
// covered and reproduce exactly the surprise this finding exists to
// remove.
func TestDoctorWarnsWhenTheEnvelopeWouldDenyAGate(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir) // the contract already declares commands.test: "true"
	addCommand(t, dir, "smoke", "true --all")
	writeEnvelope(t, dir, "true")
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}

	rep := runDoctor(t, root)

	// The envelope exists and parses, so the envelope finding itself
	// stays ok: the denial is reported per gate, not by condemning the
	// file.
	if got := findingByID(t, rep, "envelope").Severity; got != SeverityOK {
		t.Errorf("envelope severity = %q, want %q for a present, valid envelope", got, SeverityOK)
	}
	// `true` is granted exactly, so the test gate must not be reported.
	if ids := findingIDs(rep, "envelope.gate.test"); len(ids) != 0 {
		t.Errorf("gate `true` is granted exactly but was reported denied: %v", ids)
	}
	f := findingByID(t, rep, "envelope.gate.smoke")
	if f.Severity != SeverityWarn {
		t.Errorf("envelope.gate.smoke severity = %q, want %q", f.Severity, SeverityWarn)
	}
	if !strings.Contains(f.Detail, "true --all") {
		t.Errorf("detail = %q, want it to name the whole denied argv line", f.Detail)
	}
	if !strings.Contains(f.Detail, "envelope_denied") {
		t.Errorf("detail = %q, want it to name the run_checks outcome it predicts", f.Detail)
	}
	if !strings.Contains(f.Remediation, "allow.exec") {
		t.Errorf("remediation = %q, want it to name allow.exec", f.Remediation)
	}
	// A warning must not fail the report: `pika check` consults no
	// envelope, so nothing a human runs on this repository is broken.
	if !rep.OK {
		t.Error("an envelope that does not cover a gate must not flip Report.OK false")
	}

	// Widening the grant to the whole line clears the finding, which is
	// what makes the remediation actionable rather than decorative.
	writeEnvelope(t, dir, "true", "true --all")
	if ids := findingIDs(runDoctor(t, root), "envelope.gate."); len(ids) != 0 {
		t.Errorf("grants covering every gate still reported denials: %v", ids)
	}
}

// An ABSENT envelope stays exactly the warning it has always been, and
// produces no per-gate denials. A human running `pika check` never needs
// an envelope; one denial per gate for every repository that has not run
// `pika authorize` would bury the finding that means something and
// change the verdict of a command nobody asked to change.
func TestAbsentEnvelopeStaysAWarningWithNoGateDenials(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	addCommand(t, dir, "smoke", "true --all")
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}

	rep := runDoctor(t, root)
	if got := findingByID(t, rep, "envelope").Severity; got != SeverityWarn {
		t.Errorf("absent envelope severity = %q, want %q", got, SeverityWarn)
	}
	if ids := findingIDs(rep, "envelope.gate."); len(ids) != 0 {
		t.Errorf("absent envelope produced per-gate denials %v; only a present envelope is cross-examined", ids)
	}
	if !rep.OK {
		t.Error("a repository with no envelope must still report OK")
	}
}

// deadPID is a pid no process on this machine has. It is above the
// default pid_max on every supported platform, so a liveness check
// answers no rather than accidentally naming somebody's shell.
const deadPID = 99999999

// writeLeaseFile stands a holder lock up at path. A stale or
// foreign-host holder cannot be produced by acquiring one — this process
// is alive and is on this host — so those two states are written
// directly, exactly as writeRecoveryLock does for the transaction lock.
// The states that CAN be produced honestly are, below.
func writeLeaseFile(t *testing.T, path, id string, pid int, host string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":` + strconv.Quote(id) + `,"pid":` + strconv.Itoa(pid) +
		`,"startedAt":"2026-08-30T12:00:00Z","host":` + strconv.Quote(host) + "}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func thisHost(t *testing.T) string {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func mustRoot(t *testing.T, dir string) *repopath.Root {
	t.Helper()
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// A repository whose run lease outlived its run cannot start a run at
// all: every `pika work`, `pika improve`, `pika resume` and `pika
// handoff` refuses until the lease is released. doctor reported that
// repository as clean, because its recovery finding covered the
// transaction lock and nothing else — the one command that would have
// explained the lockout was silent about it.
//
// Error, not warning, and for the same reason a stale transaction lock
// is an error: the state is provable here, it blocks every run, and it
// has a mechanical remedy. A doctor that disagreed with the command that
// refused would be worse than no doctor.
func TestDoctorReportsAStaleRunLease(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	root := mustRoot(t, dir)
	leaseDir, name := repolease.RunLock(root)
	writeLeaseFile(t, filepath.Join(leaseDir, name), "20260830-feature-c0ffee01", deadPID, thisHost(t))

	rep := runDoctor(t, root)
	f := findingByID(t, rep, "lease.run")
	if f.Severity != SeverityError {
		t.Errorf("lease.run severity = %q, want %q: no run can start in this repository", f.Severity, SeverityError)
	}
	for _, want := range []string{"20260830-feature-c0ffee01", strconv.Itoa(deadPID), "stale"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("lease.run detail = %q, want it to name %q", f.Detail, want)
		}
	}
	if !strings.Contains(f.Remediation, "pika recover") {
		t.Errorf("lease.run remediation = %q, want it to name \"pika recover\"", f.Remediation)
	}
	if rep.OK {
		t.Error("OK = true with a stale run lease: the next `pika work` refuses to start")
	}
}

// A run that is genuinely running is not a fault. Somebody's colleague,
// or somebody's own second terminal, is legitimately mid-run, and a
// doctor that exited 1 for it would teach an operator that its verdict
// means nothing.
//
// The lease here is the real one, taken through the entry point `pika
// work` uses, so what is being reported is a live holder rather than a
// fixture that resembles one.
func TestDoctorReportsAHeldRunLeaseWithoutFailing(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	root := mustRoot(t, dir)
	h, err := repolease.TakeRun(root, "20260830-feature-live0001")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Release() })

	rep := runDoctor(t, root)
	f := findingByID(t, rep, "lease.run")
	if f.Severity != SeverityWarn {
		t.Errorf("lease.run severity = %q, want %q: a running run is normal operation", f.Severity, SeverityWarn)
	}
	if !rep.OK {
		t.Error("OK = false while a run is merely in progress: doctor must not fail a healthy second terminal")
	}
	if !strings.Contains(f.Detail, "20260830-feature-live0001") {
		t.Errorf("lease.run detail = %q, want it to name the holder", f.Detail)
	}
	if strings.Contains(f.Detail, "stale") {
		t.Errorf("lease.run detail = %q, want a live holder never described as stale", f.Detail)
	}
	// `pika recover` refuses a live holder, correctly. Naming it here
	// would send an operator to a command that turns them away.
	if strings.Contains(f.Remediation, "pika recover --apply") {
		t.Errorf("lease.run remediation = %q, want it not to prescribe a recovery that will refuse", f.Remediation)
	}
}

// "Stale" is the word that makes an operator clear a lock. A pid
// recorded on another host proves nothing here — it can be long dead
// locally and very much alive where it was taken — so this state is
// reported as exactly what it is, and never as stale.
//
// Warn, not error: doctor cannot tell a colleague mid-run from a crash
// on that machine, and reporting the guess as an error would fail every
// shared checkout. The remediation sends the operator to the machine
// that can answer, not to a recovery that will refuse.
func TestDoctorNeverReportsAForeignHostLeaseAsStale(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	root := mustRoot(t, dir)
	leaseDir, name := repolease.RunLock(root)
	writeLeaseFile(t, filepath.Join(leaseDir, name), "20260830-feature-abroad01", deadPID, "build-01")

	rep := runDoctor(t, root)
	f := findingByID(t, rep, "lease.run")
	if f.Severity != SeverityWarn {
		t.Errorf("lease.run severity = %q, want %q: nothing here can be proved", f.Severity, SeverityWarn)
	}
	if strings.Contains(f.Detail, "stale") || strings.Contains(f.Remediation, "stale lease") {
		t.Errorf("lease.run = %+v, want a foreign holder never described as stale", f)
	}
	if !strings.Contains(f.Detail, "build-01") || !strings.Contains(f.Remediation, "build-01") {
		t.Errorf("lease.run = %+v, want it to name the machine that can answer", f)
	}
	if strings.Contains(f.Remediation, "pika recover --apply") {
		t.Errorf("lease.run remediation = %q, want it not to prescribe a sweep that refuses this state", f.Remediation)
	}
	if !rep.OK {
		t.Error("OK = false for a lease this machine cannot judge: every shared checkout would fail doctor")
	}
}

// A crashed MCP session leaves its scope leases behind, and they block
// more than the next acquire_scope: a run covers the whole repository,
// so a leftover scope lease stops `pika work` too. doctor said nothing
// about either.
func TestDoctorReportsAStaleScopeLease(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	root := mustRoot(t, dir)
	writeLeaseFile(t, filepath.Join(repolease.ScopeLocks(root), "docs%2Fguides.lock"),
		"scope:docs/guides#1", deadPID, thisHost(t))

	rep := runDoctor(t, root)
	f := findingByID(t, rep, "lease.scope.docs/guides")
	if f.Severity != SeverityError {
		t.Errorf("scope lease severity = %q, want %q", f.Severity, SeverityError)
	}
	if !strings.Contains(f.Detail, "docs/guides") {
		t.Errorf("scope lease detail = %q, want it to name the scope, not just the lock file", f.Detail)
	}
	if !strings.Contains(f.Remediation, "pika recover") {
		t.Errorf("scope lease remediation = %q, want it to name \"pika recover\"", f.Remediation)
	}
	if rep.OK {
		t.Error("OK = true with a stale scope lease: no run can start and that path cannot be leased")
	}
}

// A lease claimed by a process that died before it wrote its holder
// record names nobody. Every run refuses it and `pika recover` refuses
// it too — nothing about such a file can be proved — so the finding is
// an error that names the file rather than a command that will turn the
// operator away.
func TestDoctorReportsALeaseThatNamesNoHolder(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	root := mustRoot(t, dir)
	leaseDir, name := repolease.RunLock(root)
	path := filepath.Join(leaseDir, name)
	if err := os.MkdirAll(leaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := runDoctor(t, root)
	f := findingByID(t, rep, "lease.run")
	if f.Severity != SeverityError {
		t.Errorf("lease.run severity = %q, want %q", f.Severity, SeverityError)
	}
	if !strings.Contains(f.Remediation, path) {
		t.Errorf("lease.run remediation = %q, want it to name the file to remove", f.Remediation)
	}
	if rep.OK {
		t.Error("OK = true with a lock nothing can judge: every run refuses")
	}
}

// The common case has to be quiet and present. A finding that only
// appears when something is wrong is one an operator cannot tell apart
// from a check that was never run.
func TestDoctorReportsNoLeaseHeld(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	if f := findingByID(t, runDoctor(t, mustRoot(t, dir)), "leases"); f.Severity != SeverityOK {
		t.Errorf("leases = %+v, want an ok finding on a repository holding nothing", f)
	}
}

// doctor is the read-only "what is wrong here" command, and it is the
// only place a stale global agent file is ever mentioned. No gate may
// check those files — they are absent from a fresh checkout, so a gate
// that digested them would fail on every clone of every repository — so
// if doctor were silent about them, nothing would ever say a word.
func TestDoctorReportsGlobalAgentFilesAsInstalledAndCurrent(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	home := t.TempDir()
	if _, err := skills.InstallGlobal(home); err != nil {
		t.Fatal(err)
	}

	rep := Run(mustRoot(t, dir), home)
	f := findingByID(t, rep, "skills.global")
	if f.Severity != SeverityOK {
		t.Errorf("skills.global severity = %q, want %q: %s", f.Severity, SeverityOK, f.Detail)
	}
	if !strings.Contains(f.Detail, home) {
		t.Errorf("the finding does not say which home directory it looked in: %s", f.Detail)
	}
	if !rep.OK {
		t.Error("OK = false on a repository whose global files are current")
	}
}

// Most repositories will never have a global install, so absent is
// informational. Warning about a file nobody asked for is how an
// operator learns to ignore this command's warnings, and the warnings
// that matter are the lease ones two functions up.
func TestDoctorTreatsAnAbsentGlobalInstallAsInformational(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)

	rep := Run(mustRoot(t, dir), t.TempDir())
	f := findingByID(t, rep, "skills.global")
	if f.Severity != SeverityOK {
		t.Errorf("skills.global severity = %q, want %q: %s", f.Severity, SeverityOK, f.Detail)
	}
	if !strings.Contains(f.Detail, "pika skills install --global") {
		t.Errorf("the finding does not name what would install them: %s", f.Detail)
	}
	if !rep.OK {
		t.Error("OK = false on a repository that simply has no global install")
	}
}

// A hand-edited global file is a warning and never an error. It is a
// warning because an agent is reading instructions that no longer match
// this binary; it is not an error because the repository is not broken,
// and failing doctor over a file outside the repository would make the
// exit code answer a question nobody asked it.
//
// The remedy has to say that regenerating destroys the edit. `pika
// skills install --global` is the fix for a stale file and the
// destruction of a hand-edited one, and one sentence for both would tell
// somebody whose words are about to disappear to run the command that
// disappears them.
func TestDoctorWarnsWithoutFailingOnATamperedGlobalAgentFile(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	home := t.TempDir()
	if _, err := skills.InstallGlobal(home); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".codex", "AGENTS.md")
	doc, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(doc), "## Driving pika", "## Driving pika (edited)", 1)
	if edited == string(doc) {
		t.Fatal("fixture did not find the heading it meant to edit")
	}
	if err := os.WriteFile(target, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := Run(mustRoot(t, dir), home)
	f := findingByID(t, rep, "skills.global")
	if f.Severity != SeverityWarn {
		t.Fatalf("skills.global severity = %q, want %q: %s", f.Severity, SeverityWarn, f.Detail)
	}
	if !strings.Contains(f.Detail, "tampered") || !strings.Contains(f.Detail, ".codex/AGENTS.md") {
		t.Errorf("the finding does not say which file is in what state: %s", f.Detail)
	}
	if !strings.Contains(f.Remediation, "pika skills install --global") {
		t.Errorf("the remediation does not name the command that regenerates them: %s", f.Remediation)
	}
	if !strings.Contains(f.Remediation, "DISCARDS") {
		t.Errorf("the remediation hides that regenerating destroys the edit: %s", f.Remediation)
	}
	if !rep.OK {
		t.Error("OK = false: a file outside the repository must not fail this repository's diagnosis")
	}
}

// A machine that reports no home directory is one where these files
// cannot exist. That is a row saying the check did not happen, not a
// reason to refuse the other twelve findings.
func TestDoctorSaysSoWhenThereIsNoHomeToCheck(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)

	rep := Run(mustRoot(t, dir), "")
	f := findingByID(t, rep, "skills.global")
	if f.Severity != SeverityWarn {
		t.Errorf("skills.global severity = %q, want %q: %s", f.Severity, SeverityWarn, f.Detail)
	}
	if !strings.Contains(f.Detail, "not checked") {
		t.Errorf("the finding claims a result it did not obtain: %s", f.Detail)
	}
	if !rep.OK {
		t.Error("OK = false because a home directory could not be resolved")
	}
	// Everything else still ran.
	for _, id := range []string{"contract", "lock", "envelope", "leases", "git"} {
		findingByID(t, rep, id)
	}
}
