package txn

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// deadPID is a pid no process is running under. The other tests in this
// package already stand a stale lock up with it; naming it once keeps
// "the holder is gone" from being spelled three ways.
const deadPID = 99999999

// crashed leaves root in the state a killed transaction leaves behind:
// the plan half-applied, the journal and its backups on disk, and a lock
// whose holder is no longer running. The journal handle is closed
// because a dead process holds no open files — and because a file with
// a live handle cannot be removed on Windows, which would make the
// fixture behave unlike the crash it stands in for.
func crashed(t *testing.T, root string) *Tx {
	t.Helper()
	seedFile(t, root, "a.txt", "one")
	tx, err := Begin(root)
	if err != nil {
		t.Fatal(err)
	}
	// Op 1 completed: journaled, then mutated.
	if err := tx.Apply(Plan{{Kind: OpWrite, Path: "a.txt", Content: []byte("ONE")}}); err != nil {
		t.Fatal(err)
	}
	// Op 2 died in the crash window between the fsynced journal entry
	// and the mutation: the intent is durable, the file is not. That is
	// the state Inspect has to classify as "would be skipped" rather
	// than "would be rolled back", and OnOpComplete cannot produce it —
	// that hook fires after the mutation has already run.
	if err := tx.appendEntry(Entry{Seq: 2, Op: OpCreate, Path: "b.txt"}); err != nil {
		t.Fatal(err)
	}
	tx.journal.Close()
	lock := filepath.Join(root, ".project", "state", "recovery", "lock")
	dead := `{"txId":"` + tx.id + `","pid":` + strconv.Itoa(deadPID) + `,"startedAt":"2020-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(lock, []byte(dead), 0o644); err != nil {
		t.Fatal(err)
	}
	return tx
}

// Inspect is what turns "a transaction is wedged" into something an
// operator can read before deciding: it names the holder, says whether
// that process is still running, and classifies every journaled op the
// same way Rollback would — so the report is the plan, not a guess about
// it. It must change nothing, which is the whole reason `pika recover`
// defaults to it.
func TestInspectReportsWhatRecoveryWouldUndo(t *testing.T) {
	root := t.TempDir()
	tx := crashed(t, root)

	pending, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Clean() {
		t.Fatal("Clean() = true on a crashed transaction")
	}
	if pending.Lock == nil {
		t.Fatal("lock = nil, want the crashed holder reported")
	}
	if pending.Lock.TxID != tx.id || pending.Lock.PID != deadPID {
		t.Errorf("lock = %+v, want tx %q held by pid %d", pending.Lock, tx.id, deadPID)
	}
	if pending.Lock.Alive {
		t.Error("Alive = true for a holder that is not running")
	}
	if len(pending.Txs) != 1 {
		t.Fatalf("transactions = %+v, want the one crashed journal", pending.Txs)
	}
	ops := pending.Txs[0].Ops
	if len(ops) != 2 {
		t.Fatalf("ops = %+v, want both journaled entries", ops)
	}
	// Newest first: the order recovery undoes them in.
	if ops[0].Seq != 2 || ops[0].Undo || ops[0].Reason == "" {
		t.Errorf("ops[0] = %+v, want seq 2 skipped with a reason: its mutation never ran", ops[0])
	}
	if ops[1].Seq != 1 || !ops[1].Undo || ops[1].Path != "a.txt" {
		t.Errorf("ops[1] = %+v, want seq 1 on a.txt marked for undo", ops[1])
	}

	// Nothing moved. A report that mutates is not a report.
	wantFile(t, root, "a.txt", "ONE")
	if got := journals(t, root); len(got) != 1 {
		t.Errorf("journals after Inspect = %v, want the journal untouched", got)
	}
	if _, err := os.Lstat(filepath.Join(root, ".project", "state", "recovery", "lock")); err != nil {
		t.Errorf("lock after Inspect: %v, want it untouched", err)
	}
}

func TestInspectOnACleanRootIsClean(t *testing.T) {
	pending, err := Inspect(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !pending.Clean() {
		t.Fatalf("pending = %+v, want clean: nothing has ever transacted here", pending)
	}
}

// The lock is never stolen from a running process: two transactions
// applying one plan each to the same tree is precisely the corruption
// the lock exists to prevent, and an operator who has misdiagnosed a
// slow apply as a crash must be stopped rather than obeyed.
func TestReleaseStaleLockRefusesALiveHolder(t *testing.T) {
	root := t.TempDir()
	tx, err := Begin(root)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	released, err := ReleaseStaleLock(root)
	if released {
		t.Error("released = true: a running holder's lock was stolen")
	}
	if !errors.Is(err, ErrLeaseRequired) {
		t.Fatalf("err = %v, want ErrLeaseRequired", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Errorf("err = %q, want it to name the holding pid %d", err, os.Getpid())
	}
	if _, err := os.Lstat(filepath.Join(root, ".project", "state", "recovery", "lock")); err != nil {
		t.Errorf("lock after refusal: %v, want it still held", err)
	}
}

// The dead end this exists to remove: a crash can leave the lock with
// no journal beside it — the holder died between taking the lock and
// journaling anything, or after its journal was retired — and Recover
// walks journals, so it never sees that lock. Without this, every future
// transaction on the repository fails until someone deletes a file by
// hand that nothing in the product had told them about.
func TestReleaseStaleLockRemovesADeadHoldersLock(t *testing.T) {
	root := t.TempDir()
	recDir := filepath.Join(root, ".project", "state", "recovery")
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(recDir, lockName)
	stale := `{"txId":"deadbeef","pid":` + strconv.Itoa(deadPID) + `,"startedAt":"2020-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(lock, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	released, err := ReleaseStaleLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("released = false, want the dead holder's lock removed")
	}
	if _, err := Begin(root); err != nil {
		t.Fatalf("begin after release: %v, want the repository unwedged", err)
	}
}

// A lock with no readable holder cannot be proved stale, so it is
// reported rather than removed. acquireLock deletes the lock on any
// write failure, so this is only ever a crash inside the window between
// the O_EXCL create and the fsynced write — narrow, but a process could
// still be sitting in it, and guessing wrong here means two transactions
// in one tree.
func TestReleaseStaleLockRefusesAnUnreadableLock(t *testing.T) {
	root := t.TempDir()
	recDir := filepath.Join(root, ".project", "state", "recovery")
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(recDir, lockName)
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if released, err := ReleaseStaleLock(root); released || err == nil {
		t.Fatalf("released = %v err = %v, want a refusal naming the file", released, err)
	}
	pending, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Lock == nil || pending.Lock.Unreadable == "" {
		t.Fatalf("lock = %+v, want it reported as unreadable", pending.Lock)
	}
	if pending.Lock.Alive {
		t.Error("Alive = true for a lock naming no holder")
	}
}

// A lock already gone is the state the caller wanted, not an error.
func TestReleaseStaleLockOnNoLockIsANoOp(t *testing.T) {
	released, err := ReleaseStaleLock(t.TempDir())
	if released || err != nil {
		t.Fatalf("released = %v err = %v, want (false, nil)", released, err)
	}
}
