package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/txn"
)

// recoverResult is the --json payload. Pending is the state recovery
// found, on both paths: after --apply it is what was rolled back, which
// is the same document that would have been printed by the report the
// operator ran first.
type recoverResult struct {
	Pending      *txn.Pending `json:"pending"`
	Applied      bool         `json:"applied"`
	Recovered    []txn.Report `json:"recovered,omitempty"`
	LockReleased bool         `json:"lockReleased"`
}

// runRecover implements `pika recover [--apply] [--json] [--root <dir>]`:
// it reports, and on request undoes, a transaction that never finished.
//
// This is the only way out of a real dead end. `pika apply` runs under a
// crash-safe journal and an O_EXCL lock that is deliberately never
// stolen — not even when the process holding it is gone — so a killed
// apply leaves the tree half-mutated and every future transaction
// failing with scope-lease-required, forever, until someone deletes a
// file by hand. internal/txn has been able to roll that back since M1;
// nothing called it, and nothing told an operator it existed.
//
// The default is a report because the operator arriving here does not
// yet know what happened. It names the holder, says whether that process
// is still running, and lists every journaled operation with what
// recovery would do to it — all read-only. --apply is the separate,
// explicit act of authorizing it.
//
// A live holder is refused on both paths. Rolling a running
// transaction's work out from under it is how two transactions come to
// apply plans to one tree, which is the exact corruption the lock exists
// to prevent, and an operator who has mistaken a slow apply for a dead
// one must be stopped rather than obeyed.
//
// Exit codes: 0 for a report and for a completed recovery, 1 when
// recovery itself failed, 2 for a usage error or for a repository state
// recover refuses to touch.
func runRecover(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("recover", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apply := fs.Bool("apply", false, "roll the interrupted transaction back and release its stale lock")
	jsonOut := fs.Bool("json", false, "emit the recovery state as JSON on stdout")
	rootFlag := fs.String("root", "", rootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return fail(*jsonOut, stdout, stderr, "recover", codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "recover", codeConfig, err.Error())
	}
	pending, err := txn.Inspect(root.Dir())
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "recover", codeConfig, err.Error())
	}

	if !*apply {
		result := recoverResult{Pending: pending}
		if *jsonOut {
			if !emitJSON(stdout, stderr, "recover", true, result) {
				return 1
			}
			return 0
		}
		printPending(stdout, root, pending)
		return 0
	}

	if held := heldReason(pending); held != "" {
		// Nothing was attempted: this is a state of the repository, the
		// same species of answer as an unknown work id, not work that
		// ran and failed.
		return fail(*jsonOut, stdout, stderr, "recover", codeConfig, held)
	}

	result := recoverResult{Pending: pending, Applied: true}
	reports, rerr := txn.Recover(root.Dir())
	result.Recovered = reports
	if rerr == nil {
		// Only once every journal is retired. A lock released over a
		// recovery that failed halfway would let the next transaction
		// start on top of a tree still holding another one's edits.
		//
		// This runs even when Recover found nothing: a crash between
		// taking the lock and journaling anything, or between retiring
		// the journal and releasing the lock, leaves the lock alone —
		// and Recover walks journals, so it never sees it.
		released, lerr := txn.ReleaseStaleLock(root.Dir())
		// Either this call removed the lock or txn.Recover already did
		// when it retired the journal the lock named. Both mean the
		// same thing to whoever is reading — the repository can
		// transact again — so this reports the end state rather than
		// which of the two produced it.
		result.LockReleased = released || pending.Lock != nil
		rerr = lerr
	}
	if rerr != nil {
		if *jsonOut {
			if !emitFailure(stdout, stderr, "recover", rerr, result) {
				fmt.Fprintln(stderr, "pika recover:", rerr)
			}
			return 1
		}
		printRecovered(stdout, root, result)
		fmt.Fprintln(stderr, "pika recover:", rerr)
		return 1
	}
	if *jsonOut {
		if !emitJSON(stdout, stderr, "recover", true, result) {
			return 1
		}
		return 0
	}
	printRecovered(stdout, root, result)
	return 0
}

// heldReason reports why the lock must not be touched, or "" when it
// may be. Both cases are refusals with different next moves: a live
// holder is a transaction to wait for, an unreadable one is a file to
// look at.
func heldReason(pending *txn.Pending) string {
	l := pending.Lock
	switch {
	case l == nil:
		return ""
	case l.Alive:
		return fmt.Sprintf("transaction %s is still running: the recovery lock at %s is held by pid %d since %s. Wait for it to finish rather than rolling its work out from under it",
			l.TxID, l.Path, l.PID, l.StartedAt)
	case l.Unreadable != "":
		return fmt.Sprintf("the recovery lock at %s names no holder (%s), so it cannot be proved stale. Remove the file once you have confirmed no transaction is running",
			l.Path, l.Unreadable)
	}
	return ""
}

// printPending writes the read-only report: where recovery state lives,
// who holds the lock, and what each journaled operation would get.
func printPending(stdout io.Writer, root *repopath.Root, pending *txn.Pending) {
	fmt.Fprintf(stdout, "root      %s (%s)\n", root.Dir(), root.Origin())
	fmt.Fprintf(stdout, "recovery  %s\n\n", pending.Dir)
	if pending.Clean() {
		fmt.Fprintln(stdout, "no interrupted transaction; nothing to recover")
		return
	}
	printLock(stdout, pending.Lock)
	for _, tx := range pending.Txs {
		fmt.Fprintf(stdout, "\ntransaction %s\n", tx.TxID)
		if tx.Error != "" {
			fmt.Fprintf(stdout, "  unreadable: %s\n", tx.Error)
			continue
		}
		if len(tx.Ops) == 0 {
			fmt.Fprintln(stdout, "  no journaled operations; nothing was applied")
			continue
		}
		for _, op := range tx.Ops {
			verb := "skip"
			if op.Undo {
				verb = "undo"
			}
			fmt.Fprintf(stdout, "  %s  %-6s %s%s\n", verb, op.Op, opTarget(op), parenthesized(op.Reason))
		}
	}
	fmt.Fprintln(stdout, "\nnothing has been changed. Re-run with --apply to roll this back and release the lock.")
}

// printRecovered writes what --apply actually did.
func printRecovered(stdout io.Writer, root *repopath.Root, result recoverResult) {
	fmt.Fprintf(stdout, "root      %s (%s)\n", root.Dir(), root.Origin())
	fmt.Fprintf(stdout, "recovery  %s\n\n", result.Pending.Dir)
	if result.Pending.Clean() {
		fmt.Fprintln(stdout, "no interrupted transaction; nothing to recover")
		return
	}
	for _, rep := range result.Recovered {
		fmt.Fprintf(stdout, "transaction %s: %d rolled back, %d never applied\n",
			rep.TxID, len(rep.Undone), len(rep.Skipped))
		for _, note := range rep.Notes {
			fmt.Fprintf(stdout, "  note: %s\n", note)
		}
	}
	switch {
	case result.LockReleased && result.Pending.Lock != nil:
		fmt.Fprintf(stdout, "released the recovery lock at %s\n", result.Pending.Lock.Path)
	case result.LockReleased:
		fmt.Fprintln(stdout, "released the recovery lock")
	}
	fmt.Fprintln(stdout, "\nthe repository is back to its pre-transaction state; new transactions can begin")
}

func printLock(stdout io.Writer, l *txn.Lock) {
	if l == nil {
		fmt.Fprintln(stdout, "lock      none; the journals below were left without one")
		return
	}
	if l.Unreadable != "" {
		fmt.Fprintf(stdout, "lock      %s: names no holder (%s)\n", l.Path, l.Unreadable)
		return
	}
	state := "the holder process is not running, so the lock is stale"
	if l.Alive {
		state = "the holder process is still running"
	}
	fmt.Fprintf(stdout, "lock      tx %s held by pid %d since %s\n          %s\n", l.TxID, l.PID, l.StartedAt, state)
}

// opTarget names what the operation acts on: a move has two ends and
// naming only one of them would hide half of what recovery is about to
// do.
func opTarget(op txn.PendingOp) string {
	if op.Dest != "" {
		return op.Path + " -> " + op.Dest
	}
	return op.Path
}

func parenthesized(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return "  (" + reason + ")"
}
