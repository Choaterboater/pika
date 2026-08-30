package txn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Choaterboater/pika/internal/lease"
)

// The operator-facing half of recovery: everything needed to look at a
// wedged repository and decide what to do about it, without changing
// anything by looking.
//
// Recover is the actor and it is deliberately all-or-nothing per
// journal, which is right for a machine and wrong for a person: an
// operator meeting a lock they did not expect needs to know who holds
// it, whether that process is still alive, and exactly which files
// rolling back would touch — before authorizing any of it. Inspect
// answers that from the same journal classification Rollback uses, so
// the report is the plan rather than a second opinion about it.

// Lock is the recovery lock as it stands on disk, with the one fact the
// file itself cannot carry: whether the process it names is still
// running.
//
// Unreadable is set when the lock exists but names no holder. That is a
// real crash state — acquireLock claims the path with O_EXCL before it
// writes the holder into it, and removes it again on any write failure,
// so a lock with no contents is a process that died inside that window.
// It is reported rather than inferred away: a lock naming no pid cannot
// be proved stale.
type Lock struct {
	Path       string `json:"path"`
	TxID       string `json:"txId,omitempty"`
	PID        int    `json:"pid,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	Alive      bool   `json:"alive"`
	Unreadable string `json:"unreadable,omitempty"`
}

// PendingOp is one journaled operation and what recovery would do with
// it. Undo means the mutation ran and would be rolled back; otherwise
// Reason says why it would be left alone — either its mutation never
// ran, or the on-disk state matches neither its pre- nor postcondition,
// which is external interference and would stop that journal's recovery.
type PendingOp struct {
	Seq    int    `json:"seq"`
	Op     Kind   `json:"op"`
	Path   string `json:"path"`
	Dest   string `json:"dest,omitempty"`
	Undo   bool   `json:"undo"`
	Reason string `json:"reason,omitempty"`
}

// PendingTx is one uncommitted journal. Error is set when the journal
// itself cannot be read, which is a state to report rather than a
// failure to inspect: the other journals still have answers.
type PendingTx struct {
	TxID  string      `json:"txId"`
	Path  string      `json:"path"`
	Ops   []PendingOp `json:"ops,omitempty"`
	Error string      `json:"error,omitempty"`
}

// Pending is one repository's recovery state.
type Pending struct {
	Dir  string      `json:"dir"`
	Lock *Lock       `json:"lock,omitempty"`
	Txs  []PendingTx `json:"transactions,omitempty"`
}

// Clean reports whether there is nothing to recover: no lock and no
// uncommitted journal.
func (p *Pending) Clean() bool { return p.Lock == nil && len(p.Txs) == 0 }

// Inspect reads the recovery state of root without changing it. It
// executes no undo, removes no journal and never touches the lock.
//
// A missing recovery directory is not an error — most repositories have
// never transacted — and neither is an unreadable lock or a corrupt
// journal. Those are the states worth reporting, so they are carried in
// the result instead of replacing it.
func Inspect(root string) (*Pending, error) {
	abs, recDir, err := recoveryDir(root)
	if err != nil {
		return nil, err
	}
	p := &Pending{Dir: recDir, Lock: inspectLock(recDir)}
	journalPaths, err := filepath.Glob(filepath.Join(recDir, "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("txn: scan recovery dir: %w", err)
	}
	sort.Strings(journalPaths)
	for _, jp := range journalPaths {
		p.Txs = append(p.Txs, inspectJournal(abs, recDir, jp))
	}
	return p, nil
}

// ReleaseStaleLock removes the recovery lock when its holder is proved
// gone, and reports whether it removed one. An absent lock is the state
// the caller wanted, not an error.
//
// It is the only thing in this package that removes a lock it did not
// take, and every refusal here is deliberate. A running holder is
// refused because stealing its lock is how two transactions come to
// apply plans to one tree. A lock naming no holder is refused because
// nothing about it can be proved: the window it comes from is narrow,
// but a process sitting in that window is indistinguishable from one
// that died in it, and the cost of guessing wrong is the same
// corruption. Both refusals name the file, so an operator who has
// established the truth some other way can still act on it.
func ReleaseStaleLock(root string) (bool, error) {
	_, recDir, err := recoveryDir(root)
	if err != nil {
		return false, err
	}
	path := lease.Path(recDir, lockName)
	info, state, err := lease.Inspect(recDir, lockName)
	switch {
	case state == lease.StateFree:
		return false, nil
	case info == nil:
		return false, fmt.Errorf("txn: %w: recovery lock at %s names no holder (%v), so it cannot be proved stale; remove the file once you have confirmed no transaction is running", ErrLeaseRequired, path, err)
	case state != lease.StateStale:
		return false, leaseError(path, info, state, err)
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("txn: release stale lock %s: %w", path, err)
	}
	return true, nil
}

// recoveryDir resolves root and returns it alongside its recovery
// directory. Both callers need both, and resolving twice is how the two
// come to disagree about which repository they are looking at.
func recoveryDir(root string) (string, string, error) {
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("txn: resolve root %q: %w", root, err)
	}
	return abs, filepath.Join(abs, filepath.FromSlash(recoveryRelPath)), nil
}

func inspectLock(recDir string) *Lock {
	path := lease.Path(recDir, lockName)
	info, state, err := lease.Inspect(recDir, lockName)
	if state == lease.StateFree {
		return nil
	}
	if info == nil {
		return &Lock{Path: path, Unreadable: err.Error()}
	}
	return &Lock{
		Path:      path,
		TxID:      info.ID,
		PID:       info.PID,
		StartedAt: info.StartedAt.Format(time.RFC3339Nano),
		// A holder this machine cannot see is assumed to be running:
		// only a holder proved gone is reported as not alive.
		Alive: state != lease.StateStale,
	}
}

// inspectJournal classifies one journal exactly as undoEntries would,
// newest entry first, so the listing reads in the order recovery would
// work through it. Classification only reads: it compares the file on
// disk against the entry's pre- and postconditions and its preserved
// backup.
func inspectJournal(root, recDir, journalPath string) PendingTx {
	txid := strings.TrimSuffix(filepath.Base(journalPath), ".jsonl")
	pt := PendingTx{TxID: txid, Path: journalPath}
	entries, err := readEntries(journalPath)
	if err != nil {
		pt.Error = err.Error()
		return pt
	}
	tx := &Tx{
		root:        root,
		id:          txid,
		recDir:      recDir,
		journalPath: journalPath,
		backupsDir:  filepath.Join(recDir, txid),
	}
	pt.Ops = make([]PendingOp, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		op := PendingOp{Seq: e.Seq, Op: e.Op, Path: e.Path, Dest: e.Dest}
		switch ran, cerr := tx.classify(e); {
		case cerr != nil:
			op.Reason = cerr.Error()
		case ran:
			op.Undo = true
		default:
			op.Reason = "the mutation never ran"
		}
		pt.Ops = append(pt.Ops, op)
	}
	return pt
}
