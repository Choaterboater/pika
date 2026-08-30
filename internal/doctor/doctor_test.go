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
	"github.com/Choaterboater/pika/internal/repopath"
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
	rep := Run(root)

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

	rep := Run(root)
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

	if got := findingByID(t, Run(root), "lock").Severity; got != SeverityError {
		t.Fatalf("lock severity = %q, want %q", got, SeverityError)
	}
}

func TestMissingEnvelopeIsAWarningNotAnError(t *testing.T) {
	dir := t.TempDir()
	writeHealthyProject(t, dir)
	root, _ := repopath.At(dir)

	f := findingByID(t, Run(root), "envelope")
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

	f := findingByID(t, Run(root), "gate.lint")
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
	if got := findingByID(t, Run(root), "lock").Severity; got != SeverityWarn {
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
	rep := Run(root2)
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

	if got := findingByID(t, Run(root), "gate.smoke").Severity; got != SeverityOK {
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

	rep := Run(root)
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

	rep := Run(root)
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

	rep := Run(root)
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

	if f := findingByID(t, Run(root), "recovery"); f.Severity != SeverityOK {
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

	rep := Run(root)

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
	if ids := findingIDs(Run(root), "envelope.gate."); len(ids) != 0 {
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

	rep := Run(root)
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
