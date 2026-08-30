package txn

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/lease"
)

const (
	recoveryRelPath = ".project/state/recovery"
	lockName        = "lock"
	tempPrefix      = ".txn-tmp-"
)

// ErrLeaseRequired is wrapped by every failure to open a second
// transaction on a root whose recovery lock is held (the M1
// "scope-lease-required" outcome).
var ErrLeaseRequired = errors.New("txn: scope lease required")

// ErrClosed is wrapped by work attempted on a committed or rolled-back
// transaction.
var ErrClosed = errors.New("txn: transaction is already finished")

// Entry is one journal line: {seq, op, path, backupRef?}. Dest is set
// for move ops only. BackupRef is the path of the preserved original
// relative to the recovery directory (<txid>/<seq>.bak), present for
// write, delete, and move.
type Entry struct {
	Seq       int    `json:"seq"`
	Op        Kind   `json:"op"`
	Path      string `json:"path"`
	Dest      string `json:"dest,omitempty"`
	BackupRef string `json:"backupRef,omitempty"`
}

// Report describes the outcome of recovering one transaction's journal.
type Report struct {
	TxID    string   `json:"txId"`
	Undone  []Entry  `json:"undone,omitempty"`  // ops that had run and were rolled back
	Skipped []Entry  `json:"skipped,omitempty"` // ops whose mutation never ran
	Notes   []string `json:"notes,omitempty"`
}

// newTxID is sortable by creation time and unique within it.
func newTxID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%016x-%s", time.Now().UnixNano(), hex.EncodeToString(b[:])), nil
}

// acquireLock takes the recovery lease. An existing lock is reported
// with its holder diagnostics, never stolen — even when the holder looks
// dead.
func acquireLock(recDir, txid string) (*lease.Handle, error) {
	h, err := lease.Acquire(recDir, lockName, lease.Info{ID: txid})
	var busy *lease.HeldError
	if errors.As(err, &busy) {
		return nil, leaseError(busy.Path, busy.Info, busy.State, busy.Err)
	}
	if err != nil {
		return nil, fmt.Errorf("txn: %w", err)
	}
	return h, nil
}

// leaseError reports a held lock with everything an operator needs to
// decide it is stale: path, holder pid, start time, and tx id. A holder
// on another machine is never called stale: this host cannot see that
// process, and an operator told "stale" clears the lock.
func leaseError(lockPath string, info *lease.Info, state lease.State, cause error) error {
	if info == nil {
		return fmt.Errorf("txn: %w: recovery lock present at %s but unreadable (%v); remove it if no transaction is active", ErrLeaseRequired, lockPath, cause)
	}
	wrapped := fmt.Errorf("txn: %w: recovery lock at %s held by pid %d since %s (tx %s)",
		ErrLeaseRequired, lockPath, info.PID, info.StartedAt.Format(time.RFC3339Nano), info.ID)
	switch state {
	case lease.StateStale:
		return fmt.Errorf("%w; holder process is not running, so the lock appears stale — remove the lock file to proceed", wrapped)
	case lease.StateUnverifiable:
		return fmt.Errorf("%w; this machine cannot prove the holder on host %s is gone — check there before removing the lock file", wrapped, info.Host)
	}
	return wrapped
}

// backup copies the current file into the transaction's backup directory
// and fsyncs the copy, returning its recovery-relative reference.
func (tx *Tx) backup(seq int, rel string) (string, error) {
	if err := os.MkdirAll(tx.backupsDir, 0o755); err != nil {
		return "", err
	}
	// Persist the backups dir's entry in the recovery directory before
	// writing anything into it.
	if err := syncDir(tx.recDir); err != nil {
		return "", err
	}
	bs, err := os.ReadFile(tx.abs(rel))
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%d.bak", seq)
	bakPath := filepath.Join(tx.backupsDir, name)
	if err := os.WriteFile(bakPath, bs, 0o644); err != nil {
		return "", err
	}
	if err := syncFile(bakPath); err != nil {
		return "", err
	}
	// Persist the backup file's directory entry.
	if err := syncDir(tx.backupsDir); err != nil {
		return "", err
	}
	return tx.id + "/" + name, nil
}

// appendEntry writes one JSONL line and fsyncs the journal before the
// caller mutates anything, making the intent durable.
func (tx *Tx) appendEntry(e Entry) error {
	bs, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := tx.journal.Write(append(bs, '\n')); err != nil {
		return err
	}
	return tx.journal.Sync()
}

func readEntries(journalPath string) ([]Entry, error) {
	bs, err := os.ReadFile(journalPath)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, line := range strings.Split(string(bs), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("corrupt journal entry: %w", err)
		}
		switch e.Op {
		case OpCreate, OpWrite, OpDelete, OpMove:
		default:
			return nil, fmt.Errorf("corrupt journal entry: unknown op %q", e.Op)
		}
		if _, err := contract.NormalizeRepoPath(e.Path); err != nil {
			return nil, fmt.Errorf("corrupt journal entry: path %q: %w", e.Path, err)
		}
		if e.Op == OpMove {
			if e.Dest == "" {
				return nil, fmt.Errorf("corrupt journal entry: move %q has no dest", e.Path)
			}
			if _, err := contract.NormalizeRepoPath(e.Dest); err != nil {
				return nil, fmt.Errorf("corrupt journal entry: dest %q: %w", e.Dest, err)
			}
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// undoEntries undoes entries newest-first. Before undoing, each entry's
// on-disk state is classified against its pre- and postconditions: the
// op either ran (postcondition holds — undo it) or its mutation never
// completed (precondition still holds — skip it). Anything else is
// external interference and stops recovery with a report rather than
// guessing. It returns which entries were undone and which skipped.
func (tx *Tx) undoEntries(entries []Entry) (undone, skipped []Entry, err error) {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		ran, cerr := tx.classify(e)
		if cerr != nil {
			return undone, skipped, fmt.Errorf("txn: tx %s seq %d (%s %s): %w", tx.id, e.Seq, e.Op, e.Path, cerr)
		}
		if !ran {
			skipped = append(skipped, e)
			continue
		}
		if err := tx.undo(e); err != nil {
			return undone, skipped, fmt.Errorf("txn: tx %s seq %d (%s %s): undo: %w", tx.id, e.Seq, e.Op, e.Path, err)
		}
		undone = append(undone, e)
	}
	return undone, skipped, nil
}

// classify reports whether the entry's mutation ran, or an error when
// the on-disk state matches neither its pre- nor its postcondition.
func (tx *Tx) classify(e Entry) (bool, error) {
	switch e.Op {
	case OpCreate:
		// Precondition: absent. Postcondition: present. Content is not
		// journaled, so presence alone decides.
		if _, err := os.Lstat(tx.abs(e.Path)); errors.Is(err, os.ErrNotExist) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		return true, nil
	case OpWrite:
		// Precondition: present with the backup's content.
		// Postcondition: present with new content, which is not
		// journaled — but restoring the backup is correct either way, and
		// a no-op when the file already holds the backup's bytes (the op
		// never ran, or wrote identical content).
		cur, err := readRegularIfExists(tx.abs(e.Path))
		if err != nil {
			return false, err
		}
		if cur == nil {
			return false, fmt.Errorf("file absent but journal holds no record of its removal")
		}
		pre, err := tx.backupBytes(e)
		if err != nil {
			return false, err
		}
		if bytes.Equal(cur, pre) {
			return false, nil
		}
		return true, nil
	case OpDelete:
		// Precondition: present with the backup's content.
		// Postcondition: absent.
		cur, err := readRegularIfExists(tx.abs(e.Path))
		if err != nil {
			return false, err
		}
		if cur == nil {
			return true, nil
		}
		pre, err := tx.backupBytes(e)
		if err != nil {
			return false, err
		}
		if bytes.Equal(cur, pre) {
			return false, nil
		}
		return false, fmt.Errorf("file content differs from backup %q: external modification", e.BackupRef)
	case OpMove:
		src, err := readRegularIfExists(tx.abs(e.Path))
		if err != nil {
			return false, err
		}
		_, derr := os.Lstat(tx.abs(e.Dest))
		if derr != nil && !errors.Is(derr, os.ErrNotExist) {
			return false, derr
		}
		destExists := derr == nil
		switch {
		case src != nil && destExists:
			// Undo removes the dest first, so source-present with
			// dest-present can only be external modification.
			return false, fmt.Errorf("source present=%v dest present=%v matches neither precondition nor postcondition: external modification", src != nil, destExists)
		case src != nil && !destExists:
			return false, nil
		default:
			// src absent: the move ran. dest-present is the normal
			// postcondition; dest-absent is a crash mid-undo after the
			// dest removal — undoing again (restore src) is idempotent.
			return true, nil
		}
	}
	return false, fmt.Errorf("unknown op kind %q", e.Op)
}

// undo applies one entry's inverse: created files are removed, modified
// or deleted files are restored from their backup, and moves are brought
// back.
func (tx *Tx) undo(e Entry) error {
	switch e.Op {
	case OpCreate:
		return os.Remove(tx.abs(e.Path))
	case OpWrite, OpDelete:
		return tx.restoreBackup(e, tx.abs(e.Path))
	case OpMove:
		// Dest first, restore second: a crash mid-undo leaves
		// src absent + dest absent, which classifies as ran on
		// re-recovery and completes with the src restore. Restoring src
		// first would instead leave src present + dest present, which
		// looks like interference and halts recovery.
		if err := os.Remove(tx.abs(e.Dest)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return tx.restoreBackup(e, tx.abs(e.Path))
	}
	return fmt.Errorf("unknown op kind %q", e.Op)
}

// backupBytes resolves an entry's backupRef to the preserved original
// bytes. The reference is generated by this package
// (<txid>/<seq>.bak), but it is read back from disk, so it is re-validated
// against escaping the recovery directory.
func (tx *Tx) backupBytes(e Entry) ([]byte, error) {
	rel, err := contract.NormalizeRepoPath(e.BackupRef)
	if err != nil {
		return nil, fmt.Errorf("bad backup ref %q: %w", e.BackupRef, err)
	}
	return os.ReadFile(filepath.Join(tx.recDir, filepath.FromSlash(rel)))
}

// restoreBackup puts the preserved original bytes back at abs via the
// same temp-file + fsync + rename path as forward writes.
func (tx *Tx) restoreBackup(e Entry, abs string) error {
	bs, err := tx.backupBytes(e)
	if err != nil {
		return fmt.Errorf("restore from backup %q: %w", e.BackupRef, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return atomicWriteAt(abs, bs)
}

// Recover rolls back every uncommitted journal under root, restoring the
// pre-state of each interrupted transaction, retiring the recovered
// journals, and clearing the stale locks of transactions it fully
// rolled back. A journal whose lock holder is still running is left
// untouched and reported in the notes. A mismatch between a journal
// entry and the on-disk state — external interference between crash and
// recovery — stops that journal's recovery with an error naming the
// entry.
func Recover(root string) ([]Report, error) {
	abs, recDir, err := recoveryDir(root)
	if err != nil {
		return nil, err
	}
	journals, err := filepath.Glob(filepath.Join(recDir, "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("txn: scan recovery dir: %w", err)
	}
	sort.Strings(journals)

	var reports []Report
	var errs []error
	for _, jp := range journals {
		rep, err := recoverOne(abs, recDir, jp)
		if rep != nil {
			reports = append(reports, *rep)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return reports, errors.Join(errs...)
}

func recoverOne(root, recDir, journalPath string) (*Report, error) {
	txid := strings.TrimSuffix(filepath.Base(journalPath), ".jsonl")
	tx := &Tx{
		root:        root,
		id:          txid,
		recDir:      recDir,
		journalPath: journalPath,
		backupsDir:  filepath.Join(recDir, txid),
	}
	entries, err := readEntries(journalPath)
	if err != nil {
		return nil, fmt.Errorf("txn: recover %s: %w", txid, err)
	}

	lockPath := lease.Path(recDir, lockName)
	rep := &Report{TxID: txid}
	if info, state, _ := lease.Inspect(recDir, lockName); info != nil && info.ID == txid && state != lease.StateStale {
		rep.Notes = append(rep.Notes, fmt.Sprintf("transaction %s appears active (lock held by running pid %d since %s); not recovered", txid, info.PID, info.StartedAt.Format(time.RFC3339Nano)))
		return rep, nil
	}

	undone, skipped, err := tx.undoEntries(entries)
	if err != nil {
		return rep, err
	}
	rep.Undone = undone
	rep.Skipped = skipped
	cleanTempFiles(tx, entries, rep)

	if err := os.Remove(journalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return rep, fmt.Errorf("txn: recover %s: remove journal: %w", txid, err)
	}
	if err := os.RemoveAll(tx.backupsDir); err != nil {
		return rep, fmt.Errorf("txn: recover %s: remove backups: %w", txid, err)
	}
	// The holder crashed; completing its recovery includes releasing the
	// lock it can no longer release itself.
	if info, _, _ := lease.Inspect(recDir, lockName); info != nil && info.ID == txid {
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			rep.Notes = append(rep.Notes, fmt.Sprintf("stale recovery lock remains at %s: %v; remove it to start new transactions", lockPath, err))
		}
	}
	return rep, nil
}

// cleanTempFiles removes atomic-write temporaries left behind by a crash
// mid-op in the directories the journal touched.
func cleanTempFiles(tx *Tx, entries []Entry, rep *Report) {
	dirs := map[string]bool{}
	for _, e := range entries {
		dirs[filepath.Dir(tx.abs(e.Path))] = true
		if e.Dest != "" {
			dirs[filepath.Dir(tx.abs(e.Dest))] = true
		}
	}
	for dir := range dirs {
		matches, err := filepath.Glob(filepath.Join(dir, tempPrefix+"*"))
		if err != nil {
			continue
		}
		for _, m := range matches {
			if err := os.Remove(m); err == nil {
				rep.Notes = append(rep.Notes, "removed leftover temp file "+m)
			}
		}
	}
}

// readRegularIfExists reads a file, returning nil for an absent path and
// an error for anything that is not a regular file.
func readRegularIfExists(abs string) ([]byte, error) {
	fi, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s is a directory", abs)
	}
	return os.ReadFile(abs)
}

// atomicWrite writes content durably: temp file in the target's
// directory, fsync, rename, then an fsync of the directory so the rename
// itself survives a crash.
func (tx *Tx) atomicWrite(rel string, content []byte) error {
	abs := tx.abs(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return atomicWriteAt(abs, content)
}

func atomicWriteAt(abs string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(abs), tempPrefix+"*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(name)
	}
	if _, err := tmp.Write(content); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, abs); err != nil {
		os.Remove(name)
		return err
	}
	return syncDir(filepath.Dir(abs))
}
