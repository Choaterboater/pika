package main

import (
	"bytes"
	"os"
	"path/filepath"
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
