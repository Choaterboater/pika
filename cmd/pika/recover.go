package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Choaterboater/pika/internal/improve"
	"github.com/Choaterboater/pika/internal/lease"
	"github.com/Choaterboater/pika/internal/mcp"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/txn"
)

// recoverResult is the --json payload. Pending is the state recovery
// found, on both paths: after --apply it is what was rolled back, which
// is the same document that would have been printed by the report the
// operator ran first. Leases is the same story for the holder locks a
// transaction journal knows nothing about.
type recoverResult struct {
	Pending      *txn.Pending  `json:"pending"`
	Leases       []leaseReport `json:"leases,omitempty"`
	Applied      bool          `json:"applied"`
	Recovered    []txn.Report  `json:"recovered,omitempty"`
	LockReleased bool          `json:"lockReleased"`
}

// leaseReport is one holder lock recover found outside the transaction
// journal: the whole-repository run lease a `pika work` holds, or a
// scope lease an MCP session took. Both are reported in the terms an
// operator needs to decide anything — which lock, what it covers, who
// holds it, and whether this machine can prove that holder is gone.
type leaseReport struct {
	// Kind is leaseKindRun or leaseKindScope.
	Kind string `json:"kind"`
	// Scope is the repository-relative path a scope lease covers. A run
	// lease covers the whole repository and leaves this empty rather
	// than claiming a path it does not mean.
	Scope     string `json:"scope,omitempty"`
	Path      string `json:"path"`
	State     string `json:"state"`
	Holder    string `json:"holder,omitempty"`
	PID       int    `json:"pid,omitempty"`
	Host      string `json:"host,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	// Detail says why a lease could not be read at all. It is only ever
	// set alongside an unverifiable state.
	Detail  string `json:"detail,omitempty"`
	Cleared bool   `json:"cleared"`
}

const (
	leaseKindRun   = "run"
	leaseKindScope = "scope"
)

// runRecover implements `pika recover [--apply] [--json] [--root <dir>]`:
// it reports, and on request undoes, the state a process that never
// finished left behind.
//
// This is the only way out of a real dead end, and there are now three
// of them. `pika apply` runs under a crash-safe journal and an O_EXCL
// lock; `pika work`, `pika resume` and `pika handoff` run under a
// whole-repository run lease; an MCP session holds a scope lease for
// every path it is writing. None of those is ever stolen automatically,
// not even when the process holding it is provably gone — that refusal
// is why the mechanism has never corrupted a repository. The cost of it
// is that a killed process wedges the repository until somebody
// intervenes, and this command is that intervention.
//
// The default is a report because the operator arriving here does not
// yet know what happened. It names every holder, says whether that
// process can be proved gone, and lists every journaled operation with
// what recovery would do to it — all read-only. --apply is the
// separate, explicit act of authorizing it.
//
// A holder that is still running is refused on both paths. Rolling a
// running transaction's work out from under it, or clearing the lease a
// live run is inside, is how two writers come to share one tree — the
// exact corruption these locks exist to prevent — and an operator who
// has mistaken a slow run for a dead one must be stopped rather than
// obeyed.
//
// A holder this machine cannot judge is refused too, and is never
// called stale. A lease recorded on another host names a pid that means
// nothing here: it can be long dead locally and very much alive where
// it was taken. Sweeping it on the strength of a local liveness check
// is precisely how two writers end up in one tree, so recover says so
// and stops, leaving the decision with the person who can go and look
// at that machine.
//
// Exit codes: 0 for a report and for a completed recovery, 1 when
// recovery itself failed, 2 for a usage error or for a repository state
// recover refuses to touch.
func runRecover(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("recover", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apply := fs.Bool("apply", false, "roll the interrupted transaction back and clear the leases no process is behind")
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
	leases, err := collectLeases(root)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, "recover", codeConfig, err.Error())
	}

	if !*apply {
		result := recoverResult{Pending: pending, Leases: leases}
		if *jsonOut {
			if !emitJSON(stdout, stderr, "recover", true, result) {
				return 1
			}
			return 0
		}
		printPending(stdout, root, result)
		return 0
	}

	// Nothing is attempted when anything here is refused: this is a
	// state of the repository, the same species of answer as an unknown
	// work id, not work that ran and failed. The leases are judged
	// before the journal is touched, because a live run in the tree is a
	// reason not to start rolling anything back at all.
	if held := heldReason(pending); held != "" {
		return fail(*jsonOut, stdout, stderr, "recover", codeConfig, held)
	}
	if held := leaseRefusal(leases); held != "" {
		return fail(*jsonOut, stdout, stderr, "recover", codeConfig, held)
	}

	result := recoverResult{Pending: pending, Leases: leases, Applied: true}
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
	if rerr == nil {
		// Last, and for the same reason the transaction lock goes last:
		// a lease cleared over a rollback that failed halfway would
		// invite the next run into a tree still holding another one's
		// edits.
		rerr = clearStaleLeases(result.Leases)
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

// collectLeases reports every holder lock in the repository that is not
// free, run lease first and then the scope leases in directory order.
//
// Neither location is spelled here. improve.RunLease names the run
// lease and mcp.ScopeLocksDir names the scope directory, because a
// second spelling of either would be free to drift from the one the
// product actually writes — and it would drift silently, since a
// recover looking in the wrong place cheerfully reports a repository
// that is already clean.
//
// A repository that has never run anything has neither directory, and
// that is a clean repository rather than an error.
func collectLeases(root *repopath.Root) ([]leaseReport, error) {
	var found []leaseReport
	runDir, runName := improve.RunLease(root)
	if rep, ok, err := inspectLease(runDir, runName, leaseKindRun, ""); err != nil {
		return nil, err
	} else if ok {
		found = append(found, rep)
	}

	scopeDir := mcp.ScopeLocksDir(root.Dir())
	entries, err := os.ReadDir(scopeDir)
	if errors.Is(err, os.ErrNotExist) {
		return found, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", scopeDir, err)
	}
	for _, e := range entries {
		scope, ok := mcp.ScopeFromLockName(e.Name())
		if !ok {
			continue
		}
		rep, ok, err := inspectLease(scopeDir, e.Name(), leaseKindScope, scope)
		if err != nil {
			return nil, err
		}
		if ok {
			found = append(found, rep)
		}
	}
	return found, nil
}

// inspectLease describes one lease name, reporting false when nothing
// holds it. An error from Inspect is not a failure to report: a lease
// that cannot be read is the most alarming thing recover can find, and
// it is carried into the report as an unverifiable holder rather than
// aborting the command that was called to explain it.
func inspectLease(dir, name, kind, scope string) (leaseReport, bool, error) {
	info, state, err := lease.Inspect(dir, name)
	if state == lease.StateFree {
		return leaseReport{}, false, nil
	}
	rep := leaseReport{
		Kind:  kind,
		Scope: scope,
		Path:  lease.Path(dir, name),
		State: state.String(),
	}
	if err != nil {
		rep.Detail = err.Error()
	}
	if info != nil {
		rep.Holder = info.ID
		rep.PID = info.PID
		rep.Host = info.Host
		rep.StartedAt = info.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	return rep, true, nil
}

// leaseRefusal reports why the leases must not be swept, or "" when
// every one of them may be. The two refusals are different problems
// with different next moves, and a single "lease held" would tell an
// operator neither.
func leaseRefusal(leases []leaseReport) string {
	for _, l := range leases {
		switch l.State {
		case lease.StateHeld.String():
			return fmt.Sprintf("%s is still running: %s is held by %s. Wait for it to finish rather than clearing the lease it is inside",
				l.what(), l.Path, l.who())
		case lease.StateUnverifiable.String():
			return fmt.Sprintf("%s cannot be judged from this machine: %s is held by %s, and a pid recorded on another host proves nothing here. Confirm on that machine that the process stopped, then remove the file",
				l.what(), l.Path, l.who())
		}
	}
	return ""
}

// clearStaleLeases removes the leases whose holders are provably gone
// and marks them cleared. lease.Clear re-reads each file and refuses
// anything but a stale holder, so a lease taken between the report and
// this call is refused here rather than swept on the strength of a
// state that has since gone out of date.
func clearStaleLeases(leases []leaseReport) error {
	for i := range leases {
		l := &leases[i]
		if l.State != lease.StateStale.String() {
			continue
		}
		dir, name := splitLeasePath(l.Path)
		cleared, err := lease.Clear(dir, name)
		if err != nil {
			return err
		}
		l.Cleared = cleared
	}
	return nil
}

// splitLeasePath takes a lease path back apart into the directory and
// name lease.Clear wants. The report carries the joined path because
// that is the thing an operator would go and look at.
func splitLeasePath(path string) (dir, name string) {
	i := strings.LastIndexAny(path, `/\`)
	if i < 0 {
		return ".", path
	}
	return path[:i], path[i+1:]
}

// what names the lease in the terms of whatever took it: a run lease
// excludes the whole repository, a scope lease one path inside it.
func (l leaseReport) what() string {
	if l.Kind == leaseKindScope {
		return fmt.Sprintf("the scope lease on %s", l.Scope)
	}
	return "the run holding this repository"
}

// who identifies the holder as far as it can be identified. A lease
// naming no readable holder is a real crash state — the file is claimed
// before the holder is written into it — and saying so is more use than
// printing a zero pid.
func (l leaseReport) who() string {
	if l.Holder == "" {
		return fmt.Sprintf("no readable holder (%s)", l.Detail)
	}
	return fmt.Sprintf("%s (pid %d on %s, started %s)", l.Holder, l.PID, l.Host, l.StartedAt)
}

// printPending writes the read-only report: where recovery state lives,
// who holds each lock, and what each journaled operation would get.
func printPending(stdout io.Writer, root *repopath.Root, result recoverResult) {
	pending := result.Pending
	fmt.Fprintf(stdout, "root      %s (%s)\n", root.Dir(), root.Origin())
	fmt.Fprintf(stdout, "recovery  %s\n\n", pending.Dir)
	if pending.Clean() && len(result.Leases) == 0 {
		fmt.Fprintln(stdout, "no interrupted transaction and no lease left behind; nothing to recover")
		return
	}
	if pending.Clean() {
		fmt.Fprintln(stdout, "no interrupted transaction")
	} else {
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
	}
	printLeases(stdout, result.Leases)
	fmt.Fprintln(stdout, "\nnothing has been changed. Re-run with --apply to roll this back and clear what no process is behind.")
}

// printLeases lists every lease that is not free and what --apply would
// do with it. Saying so for each one is the point: an operator has to
// see that the foreign-host lease is being left alone, or they will
// read a silent report as a clean repository.
func printLeases(stdout io.Writer, leases []leaseReport) {
	if len(leases) == 0 {
		return
	}
	fmt.Fprintln(stdout, "\nleases")
	for _, l := range leases {
		fmt.Fprintf(stdout, "  %-5s %s\n", l.Kind, l.Path)
		fmt.Fprintf(stdout, "        covers %s\n", l.covers())
		fmt.Fprintf(stdout, "        %s, held by %s\n", l.State, l.who())
		fmt.Fprintf(stdout, "        %s\n", l.verdict())
	}
}

// covers says what the lease excludes, which for a run lease is the
// whole repository and is worth saying out loud: an operator looking at
// one lock file has no other way to learn that it stops every run in
// the tree rather than the directory it sits in.
func (l leaseReport) covers() string {
	if l.Kind == leaseKindScope {
		return l.Scope + " and everything under it"
	}
	return "the whole repository: one run at a time, because two would commit through one working tree"
}

// verdict is what --apply would do with this lease and why.
func (l leaseReport) verdict() string {
	switch l.State {
	case lease.StateStale.String():
		return "--apply clears this: the holder process is gone and it was recorded on this host"
	case lease.StateHeld.String():
		return "--apply refuses this: the holder process is running"
	default:
		return "--apply refuses this and never calls it stale: it cannot be judged from this machine, so confirm the holder stopped and remove the file yourself"
	}
}

// printRecovered writes what --apply actually did.
func printRecovered(stdout io.Writer, root *repopath.Root, result recoverResult) {
	fmt.Fprintf(stdout, "root      %s (%s)\n", root.Dir(), root.Origin())
	fmt.Fprintf(stdout, "recovery  %s\n\n", result.Pending.Dir)
	if result.Pending.Clean() && len(result.Leases) == 0 {
		fmt.Fprintln(stdout, "no interrupted transaction and no lease left behind; nothing to recover")
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
	for _, l := range result.Leases {
		if l.Cleared {
			fmt.Fprintf(stdout, "cleared the %s lease at %s (held by %s, no longer running)\n", l.Kind, l.Path, l.Holder)
		}
	}
	fmt.Fprintln(stdout, "\nthe repository is clear; new transactions and runs can begin")
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
