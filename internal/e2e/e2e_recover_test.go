package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Recovery from a transaction that never finished, through the real
// binary. Unlike the work lifecycle next door, nothing here spawns an
// agent or runs a ladder: the input is a repository on disk in the state
// a killed `pika apply` leaves behind, and the assertions are what
// `pika recover` reports about it and what it refuses to do.

// deadPID is a pid no process runs under; internal/txn's own tests stand
// their stale locks up with the same one.
const deadPID = 99999999

// crashedTxID is the transaction id the fixture below uses. Real ids are
// minted from the clock and four random bytes; a fixed one lets the
// assertions name what they are looking for.
const crashedTxID = "0000000000000001-c0ffee01"

// crashedApply puts dir in the state a killed `pika apply` leaves behind:
// tracked.txt already rewritten, the journal entry and the backup its
// rollback restores from on disk, and the lock naming the holder that
// died.
//
// The files are laid down directly rather than by driving txn.Begin, and
// that is the faithful reproduction rather than the shortcut. A process
// that died holds no open file handles; a live transaction inside this
// test process would hold the journal open, which is not what recovery
// meets in the field.
func crashedApply(t *testing.T, dir string, pid int) {
	t.Helper()
	rec := filepath.Join(dir, ".project", "state", "recovery")
	writeFileAt(t, filepath.Join(dir, "tracked.txt"), "after\n")
	writeFileAt(t, filepath.Join(rec, crashedTxID, "1.bak"), "before\n")
	writeFileAt(t, filepath.Join(rec, crashedTxID+".jsonl"),
		`{"seq":1,"op":"write","path":"tracked.txt","backupRef":"`+crashedTxID+`/1.bak"}`+"\n")
	writeRecoveryLock(t, dir, pid)
}

// writeRecoveryLock replaces the lock with one naming pid. Whether that
// process is alive is the only thing standing between a report and a
// rollback.
func writeRecoveryLock(t *testing.T, dir string, pid int) {
	t.Helper()
	writeFileAt(t, filepath.Join(dir, ".project", "state", "recovery", "lock"),
		`{"txId":"`+crashedTxID+`","pid":`+strconv.Itoa(pid)+`,"startedAt":"2026-08-30T12:00:00Z"}`+"\n")
}

func writeFileAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wantFileContent(t *testing.T, path, want string) {
	t.Helper()
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(bs) != want {
		t.Fatalf("%s = %q, want %q", path, bs, want)
	}
}

// recoverPayload mirrors the `recover` result on the wire.
type recoverPayload struct {
	Pending struct {
		Dir  string `json:"dir"`
		Lock *struct {
			Path  string `json:"path"`
			TxID  string `json:"txId"`
			PID   int    `json:"pid"`
			Alive bool   `json:"alive"`
		} `json:"lock"`
		Transactions []struct {
			TxID string `json:"txId"`
			Ops  []struct {
				Seq  int    `json:"seq"`
				Op   string `json:"op"`
				Path string `json:"path"`
				Undo bool   `json:"undo"`
			} `json:"ops"`
		} `json:"transactions"`
	} `json:"pending"`
	Applied   bool `json:"applied"`
	Recovered []struct {
		TxID   string `json:"txId"`
		Undone []struct {
			Path string `json:"path"`
		} `json:"undone"`
	} `json:"recovered"`
	LockReleased bool `json:"lockReleased"`
}

// TestE2ERecoverReportsACrashedApplyAndRefusesALiveLock drives the only
// way out of a real dead end through the real binary.
//
// A killed `pika apply` leaves a half-mutated tree behind an O_EXCL lock
// that is deliberately never stolen, so every later transaction fails
// until someone intervenes. The three things `pika recover` has to get
// right are asserted in the order an operator meets them: the report that
// changes nothing, the refusal to touch a lock whose holder is still
// running, and the rollback once the holder is provably gone.
func TestE2ERecoverReportsACrashedApplyAndRefusesALiveLock(t *testing.T) {
	dir := scaffoldRepo(t, "go")
	tracked := filepath.Join(dir, "tracked.txt")
	crashedApply(t, dir, deadPID)

	// The report, which changes nothing. An operator whose repository is
	// wedged has to be able to see what recovery would do first.
	env := unwrap(t, runCLI(t, dir, 0, "recover", "--json"), "recover")
	if !env.OK {
		t.Fatal("recover reported not ok on a repository it can read")
	}
	var report recoverPayload
	if err := json.Unmarshal(env.Result, &report); err != nil {
		t.Fatalf("parse recover report: %v", err)
	}
	if report.Applied {
		t.Error("a bare `pika recover` reported that it applied something")
	}
	if report.Pending.Lock == nil {
		t.Fatal("the report names no lock; the operator has nothing to look at")
	}
	if report.Pending.Lock.PID != deadPID || report.Pending.Lock.TxID != crashedTxID {
		t.Errorf("lock = %+v, want the crashed holder", report.Pending.Lock)
	}
	if runtime.GOOS != "windows" && report.Pending.Lock.Alive {
		t.Error("the report calls a dead holder alive")
	}
	if len(report.Pending.Transactions) != 1 || report.Pending.Transactions[0].TxID != crashedTxID {
		t.Fatalf("transactions = %+v, want the one crashed journal", report.Pending.Transactions)
	}
	ops := report.Pending.Transactions[0].Ops
	if len(ops) != 1 || ops[0].Path != "tracked.txt" || !ops[0].Undo {
		t.Errorf("ops = %+v, want one write on tracked.txt that recovery would undo", ops)
	}
	wantFileContent(t, tracked, "after\n")

	// A live holder is refused whatever the operator asked for, and the
	// refusal names the process so they can go look at it.
	live := os.Getpid()
	writeRecoveryLock(t, dir, live)
	denied := unwrap(t, runCLI(t, dir, 2, "recover", "--apply", "--json"), "recover")
	if denied.OK || denied.Error == nil {
		t.Fatal("recover --apply stole a lock from a running process")
	}
	if !strings.Contains(denied.Error.Message, strconv.Itoa(live)) {
		t.Errorf("refusal = %q, want it to name pid %d", denied.Error.Message, live)
	}
	wantFileContent(t, tracked, "after\n")

	if runtime.GOOS == "windows" {
		// processAlive reports every positive pid as alive on Windows, so
		// recovery refuses every lock there and the operator removes the
		// file by hand. That is a documented gap, not a failure here.
		t.Skip("recovery cannot prove a holder dead on Windows")
	}

	// Authorized, with the holder provably gone: the tree goes back and
	// the repository can transact again.
	writeRecoveryLock(t, dir, deadPID)
	applied := unwrap(t, runCLI(t, dir, 0, "recover", "--apply", "--json"), "recover")
	if !applied.OK {
		t.Fatal("recover --apply reported not ok")
	}
	var result recoverPayload
	if err := json.Unmarshal(applied.Result, &result); err != nil {
		t.Fatalf("parse recover result: %v", err)
	}
	if !result.Applied || !result.LockReleased {
		t.Errorf("result = %+v, want an applied recovery that released the lock", result)
	}
	if len(result.Recovered) != 1 || len(result.Recovered[0].Undone) != 1 {
		t.Fatalf("recovered = %+v, want the one journaled write rolled back", result.Recovered)
	}
	wantFileContent(t, tracked, "before\n")
	lock := filepath.Join(dir, ".project", "state", "recovery", "lock")
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Errorf("the stale lock is still at %s (%v); the repository is still wedged", lock, err)
	}
}
