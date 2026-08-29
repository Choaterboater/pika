package txn

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func seedFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wantFile(t *testing.T, root, rel, content string) {
	t.Helper()
	bs, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	if string(bs) != content {
		t.Errorf("%s = %q, want %q", rel, bs, content)
	}
}

func wantAbsent(t *testing.T, root, rel string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s = %v, want absent", rel, err)
	}
}

func journals(t *testing.T, root string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, ".project", "state", "recovery", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// TestApplyCommitHappyPath applies a mixed plan, verifies the files, and
// asserts commit retires the journal, the backups, and the lock.
func TestApplyCommitHappyPath(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "existing.txt", "old\n")

	tx, err := Begin(root)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		{Kind: OpCreate, Path: "a.txt", Content: []byte("A")},
		{Kind: OpWrite, Path: "existing.txt", Content: []byte("new\n")},
		{Kind: OpCreate, Path: "nested/dir/b.txt", Content: []byte("B")},
	}
	if err := tx.Apply(plan); err != nil {
		t.Fatal(err)
	}
	wantFile(t, root, "a.txt", "A")
	wantFile(t, root, "existing.txt", "new\n")
	wantFile(t, root, "nested/dir/b.txt", "B")

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := journals(t, root); len(got) != 0 {
		t.Errorf("journals after commit = %v, want none", got)
	}
	wantAbsent(t, root, ".project/state/recovery/lock")
	wantAbsent(t, root, ".project/state/recovery/"+tx.id)

	// The root is free for the next transaction.
	tx2, err := Begin(root)
	if err != nil {
		t.Fatalf("begin after commit: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestRollbackRestoresPreState applies write/create/delete and rolls back;
// every file must be byte-identical to the pre-state.
func TestRollbackRestoresPreState(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "a.txt", "one")
	seedFile(t, root, "b/c.txt", "two")

	tx, err := Begin(root)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		{Kind: OpWrite, Path: "a.txt", Content: []byte("ONE")},
		{Kind: OpCreate, Path: "d.txt", Content: []byte("D")},
		{Kind: OpDelete, Path: "b/c.txt"},
	}
	if err := tx.Apply(plan); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	wantFile(t, root, "a.txt", "one")
	wantFile(t, root, "b/c.txt", "two")
	wantAbsent(t, root, "d.txt")
	if got := journals(t, root); len(got) != 0 {
		t.Errorf("journals after rollback = %v, want none", got)
	}
	wantAbsent(t, root, ".project/state/recovery/lock")
}

// TestCommitThenRollbackErrors asserts a finished transaction refuses
// every further mutation.
func TestCommitThenRollbackErrors(t *testing.T) {
	root := t.TempDir()
	tx, err := Begin(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Apply(Plan{{Kind: OpCreate, Path: "a.txt", Content: []byte("A")}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); !errors.Is(err, ErrClosed) {
		t.Errorf("rollback after commit = %v, want ErrClosed", err)
	}
	if err := tx.Apply(Plan{{Kind: OpCreate, Path: "b.txt"}}); !errors.Is(err, ErrClosed) {
		t.Errorf("apply after commit = %v, want ErrClosed", err)
	}
	if err := tx.Commit(); !errors.Is(err, ErrClosed) {
		t.Errorf("second commit = %v, want ErrClosed", err)
	}
	wantFile(t, root, "a.txt", "A")
}

// TestInterruptAfterSecondOpRecoversPreState is the brief's step-1 test:
// a three-op plan interrupted after the second op must recover to the
// exact pre-state.
func TestInterruptAfterSecondOpRecoversPreState(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "a.txt", "one")
	seedFile(t, root, "b.txt", "two")

	tx, err := Begin(root)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		{Kind: OpWrite, Path: "a.txt", Content: []byte("ONE")},
		{Kind: OpWrite, Path: "b.txt", Content: []byte("TWO")},
		{Kind: OpCreate, Path: "c.txt", Content: []byte("C")},
	}
	tx.OnOpComplete = func(seq int, op Op) {
		if seq == 2 {
			panic("simulated crash")
		}
	}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("apply did not panic on injected crash")
			}
		}()
		_ = tx.Apply(plan)
	}()
	// Ops 1 and 2 were applied before the crash.
	wantFile(t, root, "a.txt", "ONE")
	wantFile(t, root, "b.txt", "TWO")
	wantAbsent(t, root, "c.txt")
	tx.journal.Close() // simulate process death without cleanup

	// A real crash leaves a lock whose holder is gone; rewrite it as a
	// dead pid, as the post-crash on-disk state would look.
	lock := filepath.Join(root, ".project", "state", "recovery", "lock")
	dead := `{"txId":"` + tx.id + `","pid":99999999,"startedAt":"2020-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(lock, []byte(dead), 0o644); err != nil {
		t.Fatal(err)
	}

	reports, err := Recover(root)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	rep := reports[0]
	if rep.TxID != tx.id {
		t.Errorf("report tx = %q, want %q", rep.TxID, tx.id)
	}
	if len(rep.Undone) != 2 {
		t.Errorf("undone = %d entries, want 2", len(rep.Undone))
	}
	// Pre-state restored byte-identical.
	wantFile(t, root, "a.txt", "one")
	wantFile(t, root, "b.txt", "two")
	wantAbsent(t, root, "c.txt")
	if got := journals(t, root); len(got) != 0 {
		t.Errorf("journals after recovery = %v, want none", got)
	}
	// The recovered transaction's stale lock is gone; a new tx can begin.
	if _, err := Begin(root); err != nil {
		t.Fatalf("begin after recovery: %v", err)
	}
}

// TestConcurrentBeginFailsWithLeaseError asserts the brief's step-3
// behavior: a second transaction on the same root fails with
// scope-lease-required, and exactly one of two racing begins wins.
func TestConcurrentBeginFailsWithLeaseError(t *testing.T) {
	root := t.TempDir()
	tx, err := Begin(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Begin(root)
	if !errors.Is(err, ErrLeaseRequired) {
		t.Errorf("second begin = %v, want ErrLeaseRequired", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	// Racing begins: exactly one may hold the lease.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	var mu sync.Mutex
	var winners []*Tx
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wtx, err := Begin(root)
			if err == nil {
				mu.Lock()
				winners = append(winners, wtx)
				mu.Unlock()
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrLeaseRequired) {
			t.Errorf("racing begin = %v, want nil or ErrLeaseRequired", err)
		}
	}
	if successes != 1 {
		t.Errorf("racing begins succeeded %d times, want 1", successes)
	}
	for _, wtx := range winners {
		if err := wtx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestStaleLockReported asserts a leftover lock is reported with holder
// diagnostics and never stolen automatically.
func TestStaleLockReported(t *testing.T) {
	root := t.TempDir()
	lock := filepath.Join(root, ".project", "state", "recovery", "lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `{"txId":"deadbeef","pid":99999999,"startedAt":"2020-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(lock, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Begin(root)
	if !errors.Is(err, ErrLeaseRequired) {
		t.Fatalf("begin under stale lock = %v, want ErrLeaseRequired", err)
	}
	for _, want := range []string{"99999999", "2020-01-01", "deadbeef"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("stale-lock error %q missing %q", err, want)
		}
	}
	// Still not stolen.
	if _, err := Begin(root); !errors.Is(err, ErrLeaseRequired) {
		t.Errorf("second begin under stale lock = %v, want ErrLeaseRequired", err)
	}
}

// TestPlanFromDiffs asserts the adopt-shaped preview diff mapping and
// path-safety rejection at plan construction.
func TestPlanFromDiffs(t *testing.T) {
	plan, err := PlanFromDiffs([]Diff{
		{Path: "new.txt", After: "x"},
		{Path: "mod.txt", Before: "a", After: "b"},
		{Path: "gone.txt", Before: "a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Plan{
		{Kind: OpCreate, Path: "new.txt", Content: []byte("x")},
		{Kind: OpWrite, Path: "mod.txt", Content: []byte("b")},
		{Kind: OpDelete, Path: "gone.txt"},
	}
	if len(plan) != len(want) {
		t.Fatalf("plan = %+v, want %+v", plan, want)
	}
	if _, err := PlanFromDiffs([]Diff{{Path: "noop.txt"}}); err == nil {
		t.Error("PlanFromDiffs accepted a diff with empty Before and After")
	}
	if _, err := PlanFromDiffs([]Diff{{Path: "../escape.txt", After: "x"}}); err == nil {
		t.Error("PlanFromDiffs accepted an escaping path")
	}
}

// TestApplyRejectsUnsafeOps asserts path escapes and precondition
// violations fail without touching disk outside the transaction.
func TestApplyRejectsUnsafeOps(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "existing.txt", "old")

	tx, err := Begin(root)
	if err != nil {
		t.Fatal(err)
	}
	bad := []Op{
		{Kind: OpCreate, Path: "../escape.txt", Content: []byte("x")},
		{Kind: OpCreate, Path: "/absolute.txt", Content: []byte("x")},
		{Kind: OpCreate, Path: "sub/../../outside.txt", Content: []byte("x")},
		{Kind: OpCreate, Path: "existing.txt", Content: []byte("x")}, // create over existing
		{Kind: OpWrite, Path: "missing.txt", Content: []byte("x")},   // write without existing
		{Kind: OpDelete, Path: "missing.txt"},
		{Kind: OpMove, Path: "existing.txt", Dest: "existing.txt"},
		{Kind: OpMove, Path: "existing.txt", Dest: "../escape.txt"},
		{Kind: Kind("rename"), Path: "existing.txt"},
	}
	for _, op := range bad {
		if err := tx.Apply(Plan{op}); err == nil {
			t.Errorf("apply %+v succeeded, want error", op)
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	wantFile(t, root, "existing.txt", "old")
	if _, err := os.Lstat(filepath.Join(filepath.Dir(root), "escape.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("escape target created outside root: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(filepath.Dir(root)), "outside.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("escape target created outside root: %v", err)
	}
}

// TestRecoverStopsOnMismatch asserts external interference between crash
// and recovery stops recovery with a report instead of guessing.
func TestRecoverStopsOnMismatch(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "a.txt", "one")
	seedFile(t, root, "b.txt", "two")

	tx, err := Begin(root)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		{Kind: OpWrite, Path: "a.txt", Content: []byte("ONE")},
		{Kind: OpDelete, Path: "b.txt"},
	}
	tx.OnOpComplete = func(seq int, op Op) {
		if seq == 2 {
			panic("simulated crash")
		}
	}
	func() {
		defer func() { _ = recover() }()
		_ = tx.Apply(plan)
	}()
	tx.journal.Close()

	// External interference: the deleted file reappears with different
	// content, so the delete entry's state matches neither its
	// precondition (content == backup) nor its postcondition (absent).
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, ".project", "state", "recovery", "lock")
	dead := `{"txId":"` + tx.id + `","pid":99999999,"startedAt":"2020-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(lock, []byte(dead), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Recover(root); err == nil {
		t.Fatal("recovery succeeded despite mismatch, want error")
	}
	// Recovery stopped at the mismatching entry: nothing was undone.
	wantFile(t, root, "a.txt", "ONE")
	wantFile(t, root, "b.txt", "tampered")
	if got := journals(t, root); len(got) != 1 {
		t.Errorf("journal kept after mismatch = %d, want 1", len(got))
	}

	// Repair the interference; recovery then completes to the pre-state.
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(root); err != nil {
		t.Fatalf("recovery after repair: %v", err)
	}
	wantFile(t, root, "a.txt", "one")
	wantFile(t, root, "b.txt", "two")
	if got := journals(t, root); len(got) != 0 {
		t.Errorf("journals after recovery = %v, want none", got)
	}
}

// TestRecoverSkipsIntentWithoutMutation exercises the crash window
// between journal fsync and mutation: the op never ran, recovery must
// leave the file untouched and still retire the journal.
func TestRecoverSkipsIntentWithoutMutation(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "a.txt", "one")

	tx, err := Begin(root)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := tx.backup(1, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.appendEntry(Entry{Seq: 1, Op: OpWrite, Path: "a.txt", BackupRef: ref}); err != nil {
		t.Fatal(err)
	}
	tx.journal.Close() // abandon: mutation never ran

	lock := filepath.Join(root, ".project", "state", "recovery", "lock")
	dead := `{"txId":"` + tx.id + `","pid":99999999,"startedAt":"2020-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(lock, []byte(dead), 0o644); err != nil {
		t.Fatal(err)
	}

	reports, err := Recover(root)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if len(reports) != 1 || len(reports[0].Undone) != 0 {
		t.Errorf("reports = %+v, want one report with no undone entries", reports)
	}
	wantFile(t, root, "a.txt", "one")
	if got := journals(t, root); len(got) != 0 {
		t.Errorf("journals after recovery = %v, want none", got)
	}
}

// TestMoveRollback asserts move undo restores the source and clears the
// destination.
func TestMoveRollback(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "src.txt", "S")

	tx, err := Begin(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Apply(Plan{{Kind: OpMove, Path: "src.txt", Dest: "dst/dir.txt"}}); err != nil {
		t.Fatal(err)
	}
	wantFile(t, root, "dst/dir.txt", "S")
	wantAbsent(t, root, "src.txt")
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	wantFile(t, root, "src.txt", "S")
	wantAbsent(t, root, "dst/dir.txt")
}

// TestRecoverSkipsLiveTransaction asserts recovery never touches the
// journal of a transaction whose lock holder is still running.
func TestRecoverSkipsLiveTransaction(t *testing.T) {
	root := t.TempDir()
	tx, err := Begin(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Apply(Plan{{Kind: OpCreate, Path: "live.txt", Content: []byte("L")}}); err != nil {
		t.Fatal(err)
	}
	reports, err := Recover(root)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if len(reports) != 1 || len(reports[0].Undone) != 0 || len(reports[0].Notes) == 0 {
		t.Errorf("reports = %+v, want one skip note and no undo", reports)
	}
	if got := journals(t, root); len(got) != 1 {
		t.Errorf("live journal touched: %d journals, want 1", len(got))
	}
	wantFile(t, root, "live.txt", "L")
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

// TestRecoverMoveUndoInterruptedMidUndo simulates a crash between the
// two steps of move undo (dest removed, src not yet restored): recovery
// must classify the state as ran, complete the undo, and restore the
// exact pre-state instead of halting on a false mismatch.
func TestRecoverMoveUndoInterruptedMidUndo(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "src.txt", "S")

	tx, err := Begin(root)
	if err != nil {
		t.Fatal(err)
	}
	tx.OnOpComplete = func(seq int, op Op) {
		if seq == 1 {
			panic("simulated crash")
		}
	}
	func() {
		defer func() { _ = recover() }()
		_ = tx.Apply(Plan{{Kind: OpMove, Path: "src.txt", Dest: "dst.txt"}})
	}()
	tx.journal.Close()

	// The op ran: src gone, dest present. Now simulate the mid-undo
	// crash: the dest-first undo step removed the dest, the src restore
	// never ran.
	if err := os.Remove(filepath.Join(root, "dst.txt")); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, ".project", "state", "recovery", "lock")
	dead := `{"txId":"` + tx.id + `","pid":99999999,"startedAt":"2020-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(lock, []byte(dead), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Recover(root); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	wantFile(t, root, "src.txt", "S")
	wantAbsent(t, root, "dst.txt")
	if got := journals(t, root); len(got) != 0 {
		t.Errorf("journals after recovery = %v, want none", got)
	}
}

// TestRecoverRejectsCorruptDest asserts a corrupted journal line whose
// move dest escapes the root is rejected before any undo runs.
func TestRecoverRejectsCorruptDest(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "src.txt", "S")
	if err := os.MkdirAll(filepath.Join(root, ".project", "state", "recovery"), 0o755); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(root, ".project", "state", "recovery", "evil-tx.jsonl")
	line := `{"seq":1,"op":"move","path":"src.txt","dest":"../../evil.txt"}` + "\n"
	if err := os.WriteFile(journal, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(root); err == nil {
		t.Fatal("recovery accepted a journal with an escaping dest")
	}
	wantFile(t, root, "src.txt", "S")
	if _, err := os.Lstat(filepath.Join(filepath.Dir(root), "evil.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("escape target touched: %v", err)
	}
}
