// Package lease is the one holder lock in the binary.
//
// A lease is a file created with O_CREATE|O_EXCL: the filesystem picks
// the winner, so two processes racing for the same name cannot both
// succeed, and the loser learns who won by reading the file. The holder
// record is written and fsynced — the file and its directory entry —
// before Acquire returns, and the file is removed again if any of that
// fails, so the only lock that outlives a crash is one that names a
// holder.
//
// A lease is never stolen automatically, on any path. Inspect reports
// what can be proved about the holder and nothing more; clearing a lease
// this process did not take is an operator's decision, made with that
// report in hand. The transaction lock this mechanism comes from has
// never corrupted a repository, and that refusal is why.
//
// StartedAt is recorded because a person deciding whether to clear a
// lock needs to know how long it has been there. Host is recorded
// because pid liveness only means something on the machine that wrote
// it: on a shared or network filesystem a pid that is dead here can be
// very much alive on the host that took the lease. That is why a
// foreign holder is unverifiable rather than stale — reporting it stale
// invites an operator to clear a lock a live process still holds, which
// is how two writers end up in one tree.
package lease

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Choaterboater/pika/internal/fsutil"
)

// State is what can be proved about a lease name from disk.
type State int

const (
	// StateFree: no lease file exists.
	StateFree State = iota
	// StateHeld: the holder was recorded on this host and its process
	// is running.
	StateHeld
	// StateStale: the holder was recorded on this host and its process
	// is gone. The lease is still not removed automatically; this is
	// the state in which an operator may remove it.
	StateStale
	// StateUnverifiable: the holder is on another host, so this machine
	// cannot say whether it is running. Never treated as stale.
	StateUnverifiable
)

func (s State) String() string {
	switch s {
	case StateFree:
		return "free"
	case StateHeld:
		return "held"
	case StateStale:
		return "stale"
	case StateUnverifiable:
		return "unverifiable"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// Info identifies one acquisition. ID is the acquisition's identity —
// unique per lease taken — and is what Release checks before removing
// anything.
type Info struct {
	ID        string
	PID       int
	StartedAt time.Time
	Host      string
}

// record is the on-disk form. TxID is read-only: locks written before
// this package existed spell the id "txId", and a repository wedged by
// one of them must still be diagnosable by a binary that has moved on.
type record struct {
	ID        string `json:"id,omitempty"`
	TxID      string `json:"txId,omitempty"`
	PID       int    `json:"pid"`
	StartedAt string `json:"startedAt"` // RFC3339Nano, UTC
	Host      string `json:"host,omitempty"`
}

// Handle is a lease this process took and may release.
type Handle struct {
	path string
	info Info
}

// Path is the file backing the lease named name in dir.
func Path(dir, name string) string { return filepath.Join(dir, name) }

// Path is the file backing this lease.
func (h *Handle) Path() string { return h.path }

// HeldError reports an Acquire that lost the race for a name. Info and
// State carry everything known about the holder, so the caller can
// explain the refusal without reading the file a second time. Info is
// nil when the lease exists but names no readable holder — a real crash
// state, since the file is claimed before the holder is written into it
// — and Err then says why it could not be read.
type HeldError struct {
	Path  string
	Info  *Info
	State State
	Err   error
}

func (e *HeldError) Error() string {
	if e.Info == nil {
		return fmt.Sprintf("lease: %s is claimed but names no readable holder: %v", e.Path, e.Err)
	}
	return fmt.Sprintf("lease: %s is %s: %s held by pid %d on %s since %s",
		e.Path, e.State, e.Info.ID, e.Info.PID, e.Info.Host, e.Info.StartedAt.Format(time.RFC3339Nano))
}

func (e *HeldError) Unwrap() error { return e.Err }

// Acquire claims name in dir for info, failing with a *HeldError when
// somebody already holds it. dir must exist.
//
// Empty fields of info describe this process: PID, StartedAt and Host
// are filled in when unset. A caller may set them itself — recording
// another host is how a test stands a lease up from a machine that is
// not this one.
func Acquire(dir, name string, info Info) (*Handle, error) {
	info, err := fill(info)
	if err != nil {
		return nil, err
	}
	path := Path(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil, held(path)
	}
	if err != nil {
		return nil, fmt.Errorf("lease: create %s: %w", path, err)
	}
	bs, err := json.Marshal(toRecord(info))
	if err == nil {
		_, err = f.Write(append(bs, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		// Persist the lease's directory entry; a crash right after
		// acquiring must not lose the lock it took.
		err = fsutil.SyncDir(dir)
	}
	if err != nil {
		// A lease nobody can read is a lease nobody can clear.
		os.Remove(path)
		return nil, fmt.Errorf("lease: write %s: %w", path, err)
	}
	return &Handle{path: path, info: info}, nil
}

// held builds the refusal for a name that was already claimed, reading
// the holder once so the caller does not have to.
func held(path string) error {
	info, err := read(path)
	if err != nil {
		return &HeldError{Path: path, State: StateUnverifiable, Err: err}
	}
	state, cerr := classify(info)
	return &HeldError{Path: path, Info: &info, State: state, Err: cerr}
}

// Release removes the lease this handle took. It refuses to remove a
// file it cannot prove is still that lease: one now naming a different
// holder belongs to somebody else, and removing it is exactly the theft
// this package does not do. An already-absent lease is the state the
// caller wanted, not an error.
func (h *Handle) Release() error {
	info, err := read(h.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lease: %s no longer names a readable holder (%w); not removed", h.path, err)
	}
	if info.ID != h.info.ID {
		return fmt.Errorf("lease: %s is now held by %s (pid %d on %s), not %s; not removed",
			h.path, info.ID, info.PID, info.Host, h.info.ID)
	}
	if err := os.Remove(h.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("lease: remove %s: %w", h.path, err)
	}
	return nil
}

// Clear removes a lease whose holder is provably gone, and refuses
// everything else. It is the one sweep in the binary: the rule for when
// a lease may be taken away from somebody who did not give it back
// lives here, so a second caller cannot arrive at a gentler version of
// it.
//
// It re-reads the file rather than trusting a state a caller inspected
// earlier. The decision to remove a lock has to be made from what is on
// disk at the moment of removal, not from what was there when a report
// was printed.
//
// Only StateStale is removed. StateHeld belongs to a process that is
// running. StateUnverifiable belongs to a holder on another host, where
// a pid that looks dead here proves nothing — sweeping it is exactly
// how two writers end up in one tree — and a lease naming no readable
// holder cannot be proved anything at all. Each of those comes back as
// a *HeldError carrying the state, so the caller can say which it met
// and what the operator should do about it.
//
// A free name returns false with no error: that is the state the caller
// wanted, not a failure.
func Clear(dir, name string) (bool, error) {
	path := Path(dir, name)
	info, state, err := Inspect(dir, name)
	switch state {
	case StateFree:
		return false, nil
	case StateStale:
	default:
		return false, &HeldError{Path: path, Info: info, State: state, Err: err}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("lease: remove %s: %w", path, err)
	}
	return true, nil
}

// Inspect reports the lease named name in dir without changing it. A
// free name returns a nil Info and no error. A lease that exists but
// names no readable holder returns a nil Info with the read error and
// StateUnverifiable: nothing about such a lock can be proved, least of
// all that it is stale.
func Inspect(dir, name string) (*Info, State, error) {
	path := Path(dir, name)
	info, err := read(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, StateFree, nil
	}
	if err != nil {
		return nil, StateUnverifiable, err
	}
	state, cerr := classify(info)
	return &info, state, cerr
}

// classify judges a holder record against this machine. A record with no
// host predates the field; every such lock on disk was written by this
// repository's own tooling, so it is judged locally rather than reported
// as forever unverifiable.
func classify(info Info) (State, error) {
	host, err := os.Hostname()
	if err != nil {
		return StateUnverifiable, fmt.Errorf("lease: resolve this host: %w", err)
	}
	if info.Host != "" && info.Host != host {
		return StateUnverifiable, nil
	}
	if processAlive(info.PID) {
		return StateHeld, nil
	}
	return StateStale, nil
}

// fill completes the fields that describe this process.
func fill(info Info) (Info, error) {
	if info.PID == 0 {
		info.PID = os.Getpid()
	}
	if info.StartedAt.IsZero() {
		info.StartedAt = time.Now().UTC()
	}
	if info.Host == "" {
		host, err := os.Hostname()
		if err != nil {
			// A lease that names no machine cannot be judged later, and
			// a lease that cannot be judged is one somebody eventually
			// clears by guessing.
			return info, fmt.Errorf("lease: resolve this host: %w", err)
		}
		info.Host = host
	}
	return info, nil
}

func toRecord(info Info) record {
	return record{
		ID:        info.ID,
		PID:       info.PID,
		StartedAt: info.StartedAt.UTC().Format(time.RFC3339Nano),
		Host:      info.Host,
	}
}

func read(path string) (Info, error) {
	var info Info
	bs, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	var rec record
	if err := json.Unmarshal(bytes.TrimSpace(bs), &rec); err != nil {
		return info, err
	}
	started, err := time.Parse(time.RFC3339Nano, rec.StartedAt)
	if err != nil {
		return info, fmt.Errorf("holder start time %q: %w", rec.StartedAt, err)
	}
	id := rec.ID
	if id == "" {
		id = rec.TxID
	}
	return Info{ID: id, PID: rec.PID, StartedAt: started, Host: rec.Host}, nil
}
