// Package txn implements transactional file application. A plan of
// create/write/delete/move operations is applied under a crash-safe
// recovery journal at .project/state/recovery/<txid>.jsonl: each op is
// backed up and its journal entry appended and fsynced before the
// mutation runs, so a crash at any point is either fully applied or
// fully absent per op. Commit retires the journal; Rollback (or package
// level Recover after a crash) undoes the entries in reverse order to
// the exact pre-state.
package txn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/lease"
)

// Kind is one journalable file operation.
type Kind string

const (
	OpCreate Kind = "create"
	OpWrite  Kind = "write"
	OpDelete Kind = "delete"
	OpMove   Kind = "move"
)

// Op is one planned file operation, on repository-relative "/"-separated
// paths. Content carries the new bytes for create/write; Dest is the
// repository-relative target for move.
type Op struct {
	Kind    Kind
	Path    string
	Content []byte
	Dest    string
}

// Plan is an ordered sequence of operations applied as one transaction.
type Plan []Op

// Diff mirrors the preview diff shape produced by adoption (adopt.Diff):
// Path is repository-relative and Before/After carry full file contents,
// with Before empty for new files. The type is declared here rather than
// imported so the dependency direction stays adopt -> txn; the identical
// layout keeps an adopt-side conversion a plain assignment.
type Diff struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// PlanFromDiffs converts preview diffs into a plan. A diff with an empty
// Before becomes a create, an empty After a delete, and anything else a
// write. Moves have no diff representation and are added to plans
// directly.
func PlanFromDiffs(diffs []Diff) (Plan, error) {
	plan := make(Plan, 0, len(diffs))
	for _, d := range diffs {
		p, err := contract.NormalizeRepoPath(d.Path)
		if err != nil {
			return nil, fmt.Errorf("txn: diff path: %w", err)
		}
		switch {
		case d.Before == "" && d.After == "":
			return nil, fmt.Errorf("txn: diff %s: empty before and after", p)
		case d.Before == "":
			plan = append(plan, Op{Kind: OpCreate, Path: p, Content: []byte(d.After)})
		case d.After == "":
			plan = append(plan, Op{Kind: OpDelete, Path: p})
		default:
			plan = append(plan, Op{Kind: OpWrite, Path: p, Content: []byte(d.After)})
		}
	}
	return plan, nil
}

// validate checks an operation's shape before any filesystem or journal
// work.
func (op Op) validate() error {
	switch op.Kind {
	case OpCreate, OpWrite, OpDelete:
		if op.Path == "" {
			return errors.New("path is required")
		}
		if op.Dest != "" {
			return fmt.Errorf("dest is not valid for %s", op.Kind)
		}
	case OpMove:
		if op.Path == "" || op.Dest == "" {
			return errors.New("move requires path and dest")
		}
	default:
		return fmt.Errorf("unknown op kind %q", op.Kind)
	}
	return nil
}

// normalize routes every operation path through the repository path
// contract, refusing escapes from the root.
func normalize(p string) (string, error) {
	n, err := contract.NormalizeRepoPath(p)
	if err != nil {
		return "", fmt.Errorf("txn: path %q rejected: %w", p, err)
	}
	return n, nil
}

// Tx is one active transaction. It owns the recovery lock and the
// journal; exactly one transaction may be open per repository root.
type Tx struct {
	root        string
	id          string
	recDir      string
	journalPath string
	backupsDir  string
	lock        *lease.Handle
	journal     *os.File
	seq         int
	finished    bool

	// OnOpComplete, when non-nil, runs after each operation is journaled
	// and applied, before the next one starts. Test hook for interrupt
	// injection.
	OnOpComplete func(seq int, op Op)
}

// Begin opens a transaction on root ("" means the current directory). It
// creates the recovery directory, takes the O_EXCL recovery lock — a
// second Begin while a transaction is active fails with ErrLeaseRequired
// — and opens the transaction's journal.
func Begin(root string) (*Tx, error) {
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("txn: resolve root %q: %w", root, err)
	}
	recDir := filepath.Join(abs, filepath.FromSlash(recoveryRelPath))
	// Remember which ancestor existed before creation: the chain of
	// links to fsync is exactly what MkdirAll is about to create.
	anchor := existingAncestor(recDir)
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		return nil, fmt.Errorf("txn: create recovery dir: %w", err)
	}
	// Persist every directory link created by this MkdirAll: recDir's
	// link lives in its parent, that parent's in its own parent, and so
	// on up to the anchor. Missing one lets a crash lose the whole
	// recovery tree the first time it is created — the fsynced journal,
	// lock, and backups along with it.
	if err := syncCreatedChain(recDir, anchor); err != nil {
		return nil, fmt.Errorf("txn: sync recovery chain: %w", err)
	}
	txid, err := newTxID()
	if err != nil {
		return nil, fmt.Errorf("txn: generate tx id: %w", err)
	}
	lock, err := acquireLock(recDir, txid)
	if err != nil {
		return nil, err
	}

	journalPath := filepath.Join(recDir, txid+".jsonl")
	jf, err := os.OpenFile(journalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		lock.Release()
		return nil, fmt.Errorf("txn: open journal: %w", err)
	}
	// Persist the journal's directory entry: a crash before the first
	// entry is fsynced must not lose the file itself.
	if err := syncDir(recDir); err != nil {
		jf.Close()
		lock.Release()
		return nil, fmt.Errorf("txn: sync recovery dir: %w", err)
	}
	return &Tx{
		root:        abs,
		id:          txid,
		recDir:      recDir,
		journalPath: journalPath,
		backupsDir:  filepath.Join(recDir, txid),
		lock:        lock,
		journal:     jf,
	}, nil
}

// Apply executes the plan in order. Each op is validated and routed
// through the repository path contract; the first failure stops the plan
// with the transaction still open, so the caller can Rollback (or Commit
// the applied prefix). An op's backup and journal entry are durable
// before its mutation runs, so an interrupted Apply is always recoverable
// to the pre-state by Rollback or Recover.
func (tx *Tx) Apply(plan Plan) error {
	if err := tx.checkOpen(); err != nil {
		return err
	}
	for _, op := range plan {
		if err := tx.applyOp(op); err != nil {
			return fmt.Errorf("txn: apply %s %s: %w", op.Kind, op.Path, err)
		}
	}
	return nil
}

func (tx *Tx) applyOp(op Op) error {
	if err := op.validate(); err != nil {
		return err
	}
	path, err := normalize(op.Path)
	if err != nil {
		return err
	}
	dest := ""
	if op.Kind == OpMove {
		if dest, err = normalize(op.Dest); err != nil {
			return err
		}
		if path == dest {
			return errors.New("move source and dest are the same path")
		}
	}
	if err := tx.checkPreconditions(op, path, dest); err != nil {
		return err
	}

	seq := tx.seq + 1
	// 1. Preserve the original bytes for rollback/recovery.
	ref := ""
	if op.Kind == OpWrite || op.Kind == OpDelete || op.Kind == OpMove {
		if ref, err = tx.backup(seq, path); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}
	// 2. Journal the intent, fsynced, before any mutation.
	if err := tx.appendEntry(Entry{Seq: seq, Op: op.Kind, Path: path, Dest: dest, BackupRef: ref}); err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	tx.seq = seq
	// 3. Mutate.
	if err := tx.mutate(op, path, dest); err != nil {
		return err
	}
	// 4. Completed.
	if tx.OnOpComplete != nil {
		tx.OnOpComplete(seq, op)
	}
	return nil
}

// checkPreconditions enforces each op's starting state: creates must not
// clobber, writes and deletes must target an existing regular file, and
// moves must not overwrite the destination.
func (tx *Tx) checkPreconditions(op Op, path, dest string) error {
	switch op.Kind {
	case OpCreate:
		if _, err := os.Lstat(tx.abs(path)); err == nil {
			return fmt.Errorf("target already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	case OpWrite, OpDelete:
		fi, err := os.Lstat(tx.abs(path))
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("target does not exist")
		}
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return fmt.Errorf("target is a directory")
		}
	case OpMove:
		fi, err := os.Lstat(tx.abs(path))
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("source does not exist")
		}
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return fmt.Errorf("source is a directory")
		}
		if _, err := os.Lstat(tx.abs(dest)); err == nil {
			return fmt.Errorf("dest already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// mutate performs the op's filesystem effect. File content always lands
// via atomicWrite (temp file + fsync + rename); moves fsync the
// destination directory after the rename.
func (tx *Tx) mutate(op Op, path, dest string) error {
	switch op.Kind {
	case OpCreate, OpWrite:
		return tx.atomicWrite(path, op.Content)
	case OpDelete:
		return os.Remove(tx.abs(path))
	case OpMove:
		dst := tx.abs(dest)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.Rename(tx.abs(path), dst); err != nil {
			return err
		}
		return syncDir(filepath.Dir(dst))
	}
	return fmt.Errorf("unknown op kind %q", op.Kind)
}

// Commit makes the applied plan permanent. Every byte is already on disk
// and durable — payloads are fsynced before their rename and journal
// entries before their mutation — so committing only retires the
// journal, the per-op backups, and the recovery lock.
func (tx *Tx) Commit() error {
	if err := tx.checkOpen(); err != nil {
		return err
	}
	return tx.finish()
}

// Rollback undoes every journaled operation in reverse order, restoring
// backups and removing created files until the pre-state is exact. On an
// undo failure the transaction stays open with its journal intact, so
// the caller can retry or hand off to Recover.
func (tx *Tx) Rollback() error {
	if err := tx.checkOpen(); err != nil {
		return err
	}
	if err := tx.undoJournal(); err != nil {
		return err
	}
	return tx.finish()
}

// undoJournal classifies and undoes the journal, newest entry first.
func (tx *Tx) undoJournal() error {
	entries, err := readEntries(tx.journalPath)
	if err != nil {
		return fmt.Errorf("txn: tx %s: %w", tx.id, err)
	}
	_, _, uerr := tx.undoEntries(entries)
	return uerr
}

// checkOpen refuses work on a committed or rolled-back transaction.
func (tx *Tx) checkOpen() error {
	if tx.finished {
		return fmt.Errorf("txn: tx %s: %w", tx.id, ErrClosed)
	}
	return nil
}

// finish retires the transaction's on-disk state. The transaction is
// marked finished first: a failed cleanup never leaves it reusable.
func (tx *Tx) finish() error {
	tx.finished = true
	return errors.Join(
		tx.journal.Close(),
		os.Remove(tx.journalPath),
		os.RemoveAll(tx.backupsDir),
		tx.releaseLease(),
	)
}

// releaseLease drops the recovery lock this transaction took. Only a Tx
// from Begin holds one: the Tx values recovery builds to classify a
// journal never took the lock and never finish.
func (tx *Tx) releaseLease() error {
	if tx.lock == nil {
		return nil
	}
	return tx.lock.Release()
}

func (tx *Tx) abs(rel string) string {
	return filepath.Join(tx.root, filepath.FromSlash(rel))
}
