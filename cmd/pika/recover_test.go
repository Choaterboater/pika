package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/txn"
)

// deadPID is a pid no process is running under; internal/txn's own tests
// stand their stale locks up with the same one.
const deadPID = 99999999

// crashedTxID is the transaction id every fixture here uses. Real ids
// are minted from the clock and four random bytes; a fixed one lets the
// assertions name what they are looking for.
const crashedTxID = "0000000000000001-c0ffee01"

func recoverOut(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runRecover(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func writeAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lockPath(dir string) string {
	return filepath.Join(dir, ".project", "state", "recovery", "lock")
}

// crashedTransaction puts dir in the state a `pika apply` killed
// mid-plan leaves behind: tracked.txt already rewritten, the journal
// entry and the backup its rollback restores from on disk, and the lock
// naming the holder that died.
//
// It lays the files down directly rather than driving txn.Begin, and
// that is the faithful reproduction rather than the shortcut. A process
// that died holds no open file handles; a live *Tx inside this test
// process would hold the journal open, which is not what recovery meets
// in the field and — on Windows, where an open file cannot be removed —
// would not behave like a crash at all.
func crashedTransaction(t *testing.T, dir string, pid int) {
	t.Helper()
	rec := filepath.Join(dir, ".project", "state", "recovery")
	writeAt(t, filepath.Join(dir, "tracked.txt"), "after\n")
	writeAt(t, filepath.Join(rec, crashedTxID, "1.bak"), "before\n")
	writeAt(t, filepath.Join(rec, crashedTxID+".jsonl"),
		`{"seq":1,"op":"write","path":"tracked.txt","backupRef":"`+crashedTxID+`/1.bak"}`+"\n")
	writeAt(t, filepath.Join(rec, "lock"),
		`{"txId":"`+crashedTxID+`","pid":`+strconv.Itoa(pid)+`,"startedAt":"2026-08-30T12:00:00Z"}`+"\n")
}

func wantContent(t *testing.T, path, want string) {
	t.Helper()
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(bs) != want {
		t.Fatalf("%s = %q, want %q", path, bs, want)
	}
}

func TestRecoverRejectsUnknownFlag(t *testing.T) {
	code, _, stderrOut := recoverOut(t, "--unknown")
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr: %s", code, stderrOut)
	}
	if !strings.Contains(stderrOut, "flag provided but not defined") {
		t.Fatalf("stderr = %q, want flag error", stderrOut)
	}
}

// The default is a report, and the report changes nothing. An operator
// whose repository is already wedged must be able to see what recovery
// would do before authorizing it — a recovery command that acts first
// and explains afterwards is one an operator learns not to run.
func TestRecoverReportsWithoutChangingAnything(t *testing.T) {
	dir := t.TempDir()
	crashedTransaction(t, dir, deadPID)

	code, out, stderrOut := recoverOut(t, "--root", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout: %s stderr: %s", code, out, stderrOut)
	}
	for _, want := range []string{crashedTxID, strconv.Itoa(deadPID), "tracked.txt", "--apply"} {
		if !strings.Contains(out, want) {
			t.Errorf("report = %q, want it to name %q", out, want)
		}
	}
	// Untouched: the file, the journal, and the lock.
	wantContent(t, filepath.Join(dir, "tracked.txt"), "after\n")
	if _, err := os.Lstat(filepath.Join(dir, ".project", "state", "recovery", crashedTxID+".jsonl")); err != nil {
		t.Errorf("journal after a report: %v, want it untouched", err)
	}
	if _, err := os.Lstat(lockPath(dir)); err != nil {
		t.Errorf("lock after a report: %v, want it untouched", err)
	}
}

// The command exists because txn.Recover had no production caller: a
// crashed `pika apply` left the tree half-mutated and the repository
// wedged, and nothing in the product would roll it back. --apply is that
// caller.
func TestRecoverRollsBackACrashedJournal(t *testing.T) {
	dir := t.TempDir()
	crashedTransaction(t, dir, deadPID)

	code, out, stderrOut := recoverOut(t, "--apply", "--root", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout: %s stderr: %s", code, out, stderrOut)
	}
	if !strings.Contains(out, crashedTxID) {
		t.Errorf("output = %q, want it to name the recovered transaction", out)
	}
	if !strings.Contains(out, "released the recovery lock") {
		t.Errorf("output = %q, want it to say the lock is gone: that is what unwedges the repository", out)
	}
	// The pre-state, byte for byte.
	wantContent(t, filepath.Join(dir, "tracked.txt"), "before\n")
	if _, err := os.Lstat(filepath.Join(dir, ".project", "state", "recovery", crashedTxID+".jsonl")); !os.IsNotExist(err) {
		t.Errorf("journal after recovery = %v, want it retired", err)
	}
	// And the repository is usable again, which is the point.
	tx, err := txn.Begin(dir)
	if err != nil {
		t.Fatalf("begin after recovery: %v, want the repository unwedged", err)
	}
	tx.Commit()
}

// Stealing a lock from a running process is how two transactions come to
// apply plans to one tree. A live holder is refused whatever the
// operator asked for, and the refusal names the process so they can go
// look at it rather than guess again.
func TestRecoverRefusesALiveLock(t *testing.T) {
	dir := t.TempDir()
	crashedTransaction(t, dir, os.Getpid())

	code, out, stderrOut := recoverOut(t, "--apply", "--root", dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (nothing was attempted); stdout: %s stderr: %s", code, out, stderrOut)
	}
	for _, want := range []string{strconv.Itoa(os.Getpid()), crashedTxID} {
		if !strings.Contains(stderrOut, want) {
			t.Errorf("refusal = %q, want it to name %q", stderrOut, want)
		}
	}
	// Nothing was rolled out from under the live transaction.
	wantContent(t, filepath.Join(dir, "tracked.txt"), "after\n")
	if _, err := os.Lstat(lockPath(dir)); err != nil {
		t.Errorf("lock after refusal: %v, want it still held", err)
	}
}

// The narrower dead end, and the one no journal walk can reach: a crash
// can leave the lock alone, with nothing to roll back beside it.
// txn.Recover walks journals, so it never sees that lock, and every
// future transaction on the repository fails until it is gone.
func TestRecoverReleasesADeadHoldersLock(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, lockPath(dir),
		`{"txId":"`+crashedTxID+`","pid":`+strconv.Itoa(deadPID)+`,"startedAt":"2026-08-30T12:00:00Z"}`+"\n")

	code, out, stderrOut := recoverOut(t, "--apply", "--root", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout: %s stderr: %s", code, out, stderrOut)
	}
	if !strings.Contains(out, "lock") {
		t.Errorf("output = %q, want it to say the lock was released", out)
	}
	if _, err := os.Lstat(lockPath(dir)); !os.IsNotExist(err) {
		t.Errorf("lock after --apply = %v, want it released", err)
	}
	tx, err := txn.Begin(dir)
	if err != nil {
		t.Fatalf("begin after release: %v, want the repository unwedged", err)
	}
	tx.Commit()
}

// A repository with nothing to recover is a normal state, not a
// failure — the same way `pika status` exits 0 on a repository that has
// never run anything.
func TestRecoverOnACleanRepositorySaysSo(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"--root", dir}, {"--apply", "--root", dir}} {
		code, out, stderrOut := recoverOut(t, args...)
		if code != 0 {
			t.Fatalf("%v: exit = %d, want 0; stdout: %s stderr: %s", args, code, out, stderrOut)
		}
		if !strings.Contains(out, "no interrupted transaction") {
			t.Errorf("%v: output = %q, want it to say there is nothing to recover", args, out)
		}
	}
}

// The lease fixtures below spell the on-disk locations literally rather
// than asking improve.RunLease or mcp.ScopeLocksDir where they are.
// That is deliberate: recover uses those accessors precisely so it
// cannot drift, and a test that used them too would follow the drift
// instead of catching it. These constants are the documented layout,
// and moving one is a decision that has to be made here as well.
func runLeasePath(dir string) string {
	return filepath.Join(dir, ".project", "state", "run.lock")
}

func scopeLeasePath(dir, lockName string) string {
	return filepath.Join(dir, ".project", "state", "locks", lockName)
}

// writeLease lays a holder record down the way a process that died
// leaves one: the file is on disk and nothing holds it open.
func writeLease(t *testing.T, path, id string, pid int, host string) {
	t.Helper()
	writeAt(t, path, `{"id":"`+id+`","pid":`+strconv.Itoa(pid)+
		`,"startedAt":"2026-08-30T12:00:00Z","host":"`+host+`"}`+"\n")
}

func thisHost(t *testing.T) string {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return host
}

// skipIfStalenessUnprovable steps aside where a dead holder cannot be
// recognised. processAlive reports every positive pid as alive on
// Windows, so no lease is ever stale there and the operator removes a
// dead holder's lock by hand. That is a documented gap, not a failure.
func skipIfStalenessUnprovable(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a dead holder cannot be proved dead on Windows")
	}
}

const crashedWorkID = "20260830-feature-c0ffee01"

// A killed `pika work` leaves a run lease behind, and `pika resume`
// will not take it: a lease is never stolen, on any path. Without this
// the repository is wedged with no supported way out — exactly the dead
// end recover was built for one layer down, reintroduced one layer up.
func TestRecoverClearsAStaleRunLease(t *testing.T) {
	skipIfStalenessUnprovable(t)
	dir := t.TempDir()
	writeLease(t, runLeasePath(dir), crashedWorkID, deadPID, thisHost(t))

	// The report first, because that is what the operator runs first,
	// and it must change nothing.
	code, out, stderrOut := recoverOut(t, "--root", dir)
	if code != 0 {
		t.Fatalf("report exit = %d, want 0; stdout: %s stderr: %s", code, out, stderrOut)
	}
	for _, want := range []string{"run.lock", crashedWorkID, "stale", "whole repository"} {
		if !strings.Contains(out, want) {
			t.Errorf("report = %q, want it to name %q", out, want)
		}
	}
	if _, err := os.Lstat(runLeasePath(dir)); err != nil {
		t.Fatalf("the report removed the lease: %v", err)
	}

	code, out, stderrOut = recoverOut(t, "--apply", "--root", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout: %s stderr: %s", code, out, stderrOut)
	}
	if !strings.Contains(out, "cleared the run lease") {
		t.Errorf("output = %q, want it to say the run lease is gone", out)
	}
	if _, err := os.Lstat(runLeasePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("the run lease is still there (%v); the repository is still wedged", err)
	}
}

// A lease whose holder is running is a run in the working tree, and
// clearing it invites a second one in beside it. The refusal names the
// holder so the operator can go and look at that process rather than
// guess again.
func TestRecoverRefusesAHeldRunLease(t *testing.T) {
	dir := t.TempDir()
	writeLease(t, runLeasePath(dir), crashedWorkID, os.Getpid(), thisHost(t))

	code, out, stderrOut := recoverOut(t, "--apply", "--root", dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (nothing was attempted); stdout: %s stderr: %s", code, out, stderrOut)
	}
	for _, want := range []string{crashedWorkID, strconv.Itoa(os.Getpid()), "still running"} {
		if !strings.Contains(stderrOut, want) {
			t.Errorf("refusal = %q, want it to name %q", stderrOut, want)
		}
	}
	if _, err := os.Lstat(runLeasePath(dir)); err != nil {
		t.Fatalf("the refusal removed the live run's lease anyway: %v", err)
	}
}

// The one that matters most, because getting it wrong is silent. A
// holder recorded on another host names a pid that means nothing here:
// it can be long dead locally and still writing on the machine that
// took the lease. Recover must never call that stale and must never
// sweep it, whatever the local pid table says.
func TestRecoverNeverSweepsAnUnverifiableLease(t *testing.T) {
	dir := t.TempDir()
	// A pid that is not running here, so a recover that judged this
	// lease locally would sweep it.
	writeLease(t, runLeasePath(dir), crashedWorkID, deadPID, "some-other-machine")

	code, out, stderrOut := recoverOut(t, "--root", dir)
	if code != 0 {
		t.Fatalf("report exit = %d, want 0; stdout: %s stderr: %s", code, out, stderrOut)
	}
	if !strings.Contains(out, "unverifiable") {
		t.Errorf("report = %q, want it to call the foreign holder unverifiable", out)
	}
	// The state itself, from the machine-readable payload rather than
	// from prose: the human report legitimately says the word "stale"
	// while explaining that this lease is not it.
	_, payload, _ := recoverOut(t, "--json", "--root", dir)
	if !strings.Contains(payload, `"state": "unverifiable"`) {
		t.Errorf("recover --json = %s, want the foreign holder reported unverifiable", payload)
	}
	if strings.Contains(payload, `"state": "stale"`) {
		t.Errorf("recover --json = %s, want it never to report a foreign holder stale: that word is what makes an operator clear a lock a live writer still holds", payload)
	}
	if !strings.Contains(out, "some-other-machine") {
		t.Errorf("report = %q, want it to name the host that has to be checked", out)
	}

	code, out, stderrOut = recoverOut(t, "--apply", "--root", dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stdout: %s stderr: %s", code, out, stderrOut)
	}
	if !strings.Contains(stderrOut, "some-other-machine") {
		t.Errorf("refusal = %q, want it to name the host the holder is on", stderrOut)
	}
	if _, err := os.Lstat(runLeasePath(dir)); err != nil {
		t.Fatalf("recover swept a lease it cannot prove dead: %v", err)
	}
}

// A killed MCP session leaves its scope leases behind too, and until
// now nothing in the product cleared them: every later acquire_scope on
// those paths was refused with scope_conflict, forever.
func TestRecoverClearsAStaleScopeLease(t *testing.T) {
	skipIfStalenessUnprovable(t)
	dir := t.TempDir()
	// The encoded lock name for the repository path "docs/guides": the
	// slash is percent-encoded, so the leases live in one flat
	// directory.
	writeLease(t, scopeLeasePath(dir, "docs%2Fguides.lock"), "scope:docs/guides", deadPID, thisHost(t))
	// A file the lock directory did not put there names no scope and
	// must be left exactly where it is.
	writeAt(t, scopeLeasePath(dir, "README"), "not a lease\n")

	code, out, stderrOut := recoverOut(t, "--root", dir)
	if code != 0 {
		t.Fatalf("report exit = %d, want 0; stdout: %s stderr: %s", code, out, stderrOut)
	}
	if !strings.Contains(out, "docs/guides") {
		t.Errorf("report = %q, want it to name the scope the lease covers, not just its lock file", out)
	}

	code, out, stderrOut = recoverOut(t, "--apply", "--root", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout: %s stderr: %s", code, out, stderrOut)
	}
	if !strings.Contains(out, "cleared the scope lease") {
		t.Errorf("output = %q, want it to say the scope lease is gone", out)
	}
	if _, err := os.Lstat(scopeLeasePath(dir, "docs%2Fguides.lock")); !os.IsNotExist(err) {
		t.Fatalf("the stale scope lease survived (%v); acquire_scope on that path is still refused", err)
	}
	if _, err := os.Lstat(scopeLeasePath(dir, "README")); err != nil {
		t.Errorf("recover removed a file that is not a lease: %v", err)
	}
}
