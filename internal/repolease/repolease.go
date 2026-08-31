// Package repolease is this repository's exclusion policy: where its
// holder locks live, what each one covers, and what any two of them mean
// to each other.
//
// internal/lease is the mechanism — one file, O_EXCL, never stolen, and
// it knows nothing about pika. This package is the policy built on it,
// and there is exactly one policy because there is exactly one hazard:
// two writers in one working tree.
//
// pika takes leases at two radii. A run lease is held by `pika work`,
// `pika improve`, `pika resume` and `pika handoff` for as long as a run
// is in the tree. A scope lease is held by an MCP session for one
// declared path it is writing. Until this package they were two
// independent exclusions that never looked at each other, so `pika work`
// in one terminal and `pika mcp` in another — serving an agent harness
// that writes files directly — proceeded simultaneously in one
// repository, each unaware of the other. That is the milestone's own
// hazard, reached through a third door.
//
// # The run lease is the scope lease on "."
//
// The relationship is not a hierarchy, and calling it one is what left
// the door open. A run lease is a claim over the whole repository; a
// scope lease is a claim over a subtree. They are the same kind of claim
// at different radii, and one rule decides every pair of them: a claim
// excludes everything inside it and everything containing it. RunScope
// is ".", which contains everything, so a run excludes every scope and
// every scope excludes a run — both directions, no exceptions, from the
// same rule that already made a lease on src conflict with one on
// src/pkg.
//
// They keep separate files because a refusal has to name the holder in
// the operator's own terms: "the run holding this repository" and "the
// scope lease on docs/guides" are different sentences with different
// next moves, and `pika recover` reports them as different things.
//
// # Why not subsumption
//
// The tempting alternative is to let a run lease subsume scopes: the run
// already holds the whole tree, so let its agent take scopes inside it
// freely. The mechanism cannot honestly implement that. Nothing links
// the process asking for a scope to the process holding the run — they
// are different commands, usually different processes, and a lease
// records a pid and a host, not a lineage. "The run holder may have this
// scope" would in practice read "anybody may have this scope while a run
// is in the tree", which is the hazard with a permission slip stapled to
// it. There is no re-entrancy anywhere in this design: acquire_scope
// already refuses the holding session a lease it is already holding.
//
// # Nothing waits, nothing is stolen
//
// Every acquisition claims its own file first and only then looks for
// conflicts. Claiming first means two acquisitions racing for
// overlapping ground each see the other's file and both refuse; looking
// first would let both pass the scan and both be granted. There is no
// retry loop, here or in any caller, so no deadlock is structurally
// possible: a refusal that could have been a grant is fixed by asking
// again, and two writers in one tree is fixed by nothing.
package repolease

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Choaterboater/pika/internal/lease"
	"github.com/Choaterboater/pika/internal/repopath"
)

// Kind names which of the two radii a lease was taken at.
type Kind string

const (
	// KindRun is the whole-repository lease one run holds.
	KindRun Kind = "run"
	// KindScope is the lease an MCP session holds over one path.
	KindScope Kind = "scope"
)

// RunScope is what a run lease covers, spelled as the repository-relative
// path it is: the root, which contains every scope anybody can name. The
// overlap rule needs no special case for the run lease because of this.
const RunScope = "."

// runLockName is the file a run holds for as long as it is in progress.
// It sits beside the run records rather than in `.git`, because what it
// excludes is a pika run and not a Git operation.
const runLockName = "run.lock"

// scopeLocksName is the directory scope leases live in, beneath the
// state directory.
const scopeLocksName = "locks"

// RunLock is the run lease's directory and name, in the form
// internal/lease takes.
func RunLock(root *repopath.Root) (dir, name string) {
	return root.StateDir(), runLockName
}

// ScopeLocks is the directory scope leases live in.
func ScopeLocks(root *repopath.Root) string {
	return filepath.Join(root.StateDir(), scopeLocksName)
}

// Held is one lease in this repository that is not free.
type Held struct {
	Kind Kind
	// Scope is the ground the lease covers: RunScope for a run lease,
	// the leased repository-relative path for a scope lease.
	Scope string
	Path  string
	State lease.State
	// Info is the holder record, or nil when the file is claimed and
	// names nobody readable. That is a real crash state rather than a
	// missing field: a lease is claimed before the holder is written
	// into it, and Err then says why it could not be read.
	Info *lease.Info
	// Err says why a holder could not be read, or why this machine
	// could not judge one.
	Err error
}

// What names the lease in the terms of whatever took it. A run lease
// excludes the whole repository and a scope lease one path inside it,
// and an operator handed only a file path has no way to tell which.
func (h Held) What() string {
	if h.Kind == KindScope {
		return "the scope lease on " + h.Scope
	}
	return "the run holding this repository"
}

// Who identifies the holder as far as it can be identified, with the
// four facts a person needs to decide anything: which acquisition, what
// process, which machine, and how long it has been there.
func (h Held) Who() string {
	if h.Info == nil {
		return fmt.Sprintf("no readable holder (%v)", h.Err)
	}
	return fmt.Sprintf("%s (pid %d on %s, started %s)", h.Info.ID, h.Info.PID, h.Info.Host,
		h.Info.StartedAt.UTC().Format(time.RFC3339Nano))
}

// Describe is the whole holder in one clause, which is what a refusal
// puts in front of an operator.
func (h Held) Describe() string {
	if h.Kind == KindScope {
		return fmt.Sprintf("the scope lease on %s, held by %s", h.Scope, h.Who())
	}
	return "run " + h.Who()
}

// ConflictError refuses an acquisition because another lease already
// covers the ground it asked for. Held carries everything known about
// that holder, so a caller can phrase the refusal in its own vocabulary
// — ErrRunInProgress for a run, scope_conflict for an MCP session —
// without reading the lease file a second time.
type ConflictError struct {
	// Want is the radius that was asked for, and Scope the ground.
	Want  Kind
	Scope string
	Held  Held
}

func (e *ConflictError) Error() string {
	subject := "this repository"
	if e.Want == KindScope {
		subject = e.Scope
	}
	return fmt.Sprintf("%s is already covered by %s, whose lease is %s: %s",
		subject, e.Held.What(), e.Held.State, e.Held.Who())
}

// ErrScopeNameTooLong refuses a path whose encoded lock name would not
// fit the shortest filename limit of the supported platforms. It is a
// bad argument rather than an internal failure, and callers that map
// error codes need to be able to tell the difference.
var ErrScopeNameTooLong = errors.New("repolease: scope path is too long to lease")

// TakeRun claims the whole repository for one run, identified by its
// work id so a refusal names a run `pika status` can look up.
//
// It refuses rather than waiting or stealing. Neither alternative is
// available: waiting would make a run that has stopped for a reason
// indistinguishable from one that hung, and an operator staring at a
// silent terminal has no way to tell which they have; stealing is the
// defect itself, since the lease's whole content is the promise that one
// process is in the tree.
func TakeRun(root *repopath.Root, workID string) (*lease.Handle, error) {
	dir, name := RunLock(root)
	return take(root, dir, name, KindRun, RunScope, workID)
}

// TakeScope claims one repository-relative path. The id it records names
// the scope, so a refusal quotes something a human recognizes, and
// carries a timestamp, so it is unique per lease taken — which is what
// lease.Release checks before it removes anything.
func TakeScope(root *repopath.Root, rel string) (*lease.Handle, error) {
	name, err := scopeLockName(rel)
	if err != nil {
		return nil, err
	}
	id := "scope:" + rel + "#" + strconv.FormatInt(time.Now().UnixNano(), 10)
	return take(root, ScopeLocks(root), name, KindScope, rel, id)
}

// take is the one acquisition in the binary: claim the file, then look
// at every other lease in the repository, and give the claim straight
// back if any of them covers the same ground. A refusing call must not
// leave a lease behind for ground nobody was granted.
//
// lease.Acquire does not create its directory, so this does. A
// repository that has never held a run or a scope has neither.
func take(root *repopath.Root, dir, name string, kind Kind, scope, id string) (*lease.Handle, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("repolease: create %s: %w", dir, err)
	}
	h, err := lease.Acquire(dir, name, lease.Info{ID: id})
	var busy *lease.HeldError
	if errors.As(err, &busy) {
		// The name itself was taken: same radius, same ground, and the
		// holder record came back with the refusal.
		return nil, &ConflictError{Want: kind, Scope: scope, Held: Held{
			Kind:  kind,
			Scope: scope,
			Path:  busy.Path,
			State: busy.State,
			Info:  busy.Info,
			Err:   busy.Err,
		}}
	}
	if err != nil {
		return nil, err
	}
	other, err := conflict(root, scope, h.Path())
	if err != nil {
		_ = h.Release()
		return nil, err
	}
	if other != nil {
		_ = h.Release()
		return nil, &ConflictError{Want: kind, Scope: scope, Held: *other}
	}
	return h, nil
}

// conflict reports the first lease overlapping scope, ignoring the
// caller's own lock file, or nil when there is none.
func conflict(root *repopath.Root, scope, self string) (*Held, error) {
	all, err := Scan(root)
	if err != nil {
		return nil, err
	}
	for i := range all {
		h := &all[i]
		if h.Path == self || !overlap(scope, h.Scope) {
			continue
		}
		return h, nil
	}
	return nil, nil
}

// Scan lists every lease in this repository that is not free: the run
// lease first, then the scope leases in directory order.
//
// A repository that has never run anything has neither location, and
// that is a clean repository rather than an error. A lease released
// between the directory read and the look at it reads free and is left
// out, which is the honest answer — it is not held now.
func Scan(root *repopath.Root) ([]Held, error) {
	var found []Held
	dir, name := RunLock(root)
	if h, ok := inspect(dir, name, KindRun, RunScope); ok {
		found = append(found, h)
	}

	scopeDir := ScopeLocks(root)
	entries, err := os.ReadDir(scopeDir)
	if errors.Is(err, fs.ErrNotExist) {
		return found, nil
	}
	if err != nil {
		return nil, fmt.Errorf("repolease: read %s: %w", scopeDir, err)
	}
	for _, e := range entries {
		scope, ok := ScopeFromLockName(e.Name())
		if !ok {
			continue
		}
		if h, ok := inspect(scopeDir, e.Name(), KindScope, scope); ok {
			found = append(found, h)
		}
	}
	return found, nil
}

// inspect describes one lease name, reporting false when nothing holds
// it. An error from lease.Inspect is not a failure to report: a lease
// that cannot be read is the most alarming thing here, and it is carried
// into the report as an unjudgeable holder rather than swallowing the
// finding it is.
func inspect(dir, name string, kind Kind, scope string) (Held, bool) {
	info, state, err := lease.Inspect(dir, name)
	if state == lease.StateFree {
		return Held{}, false
	}
	return Held{
		Kind:  kind,
		Scope: scope,
		Path:  lease.Path(dir, name),
		State: state,
		Info:  info,
		Err:   err,
	}, true
}

// overlap reports whether two claims can be held at the same time.
// Exclusive over a path means exclusive over everything under it: a
// lease on src conflicts with one on src/pkg in both directions, because
// an exclusion an agent could sidestep by naming a subdirectory would
// not be one.
func overlap(a, b string) bool {
	return a == b || covers(a, b) || covers(b, a)
}

// covers reports whether parent's subtree contains child. RunScope is
// the repository root, which contains everything — which is exactly how
// the run lease excludes every scope without a rule of its own.
func covers(parent, child string) bool {
	return parent == RunScope || strings.HasPrefix(child, parent+"/")
}

// scopeLockSuffix marks the lease files this package owns, so a stray
// file in the lock directory is not read as somebody's scope.
const scopeLockSuffix = ".lock"

// maxScopeLockName keeps the encoded name inside the shortest filename
// limit of the supported platforms.
const maxScopeLockName = 200

// scopeLockName encodes a repository-relative path as one lock file
// name. Every byte outside the unreserved set is percent-encoded, which
// makes the name legal on every supported platform — a repository path
// may legally contain characters Windows rejects in a filename — and
// keeps the encoding reversible, which the overlap scan needs to read
// the leased paths back out of a directory listing.
func scopeLockName(rel string) (string, error) {
	var b strings.Builder
	b.Grow(len(rel) + len(scopeLockSuffix))
	// Byte indices, not runes: this encodes bytes, and ranging a string
	// would decode UTF-8 and skip every continuation byte.
	for i := range len(rel) {
		switch c := rel[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	b.WriteString(scopeLockSuffix)
	if name := b.String(); len(name) <= maxScopeLockName {
		return name, nil
	}
	return "", fmt.Errorf("%w: %s exceeds %d bytes once encoded as a lock name; lease a shorter path",
		ErrScopeNameTooLong, rel, maxScopeLockName)
}

// ScopeFromLockName reads the repository-relative path a lock file name
// stands for, reporting false for any name this package did not write.
// The lock directory is an ordinary directory and a stray file in it
// names no scope.
func ScopeFromLockName(name string) (string, bool) {
	if !strings.HasSuffix(name, scopeLockSuffix) {
		return "", false
	}
	body := strings.TrimSuffix(name, scopeLockSuffix)
	var b strings.Builder
	b.Grow(len(body))
	// Classic form: the body advances i past a percent escape, which a
	// range loop's per-iteration variable would not carry.
	for i := 0; i < len(body); i++ {
		if body[i] != '%' {
			b.WriteByte(body[i])
			continue
		}
		if i+2 >= len(body) {
			return "", false
		}
		v, err := strconv.ParseUint(body[i+1:i+3], 16, 8)
		if err != nil {
			return "", false
		}
		b.WriteByte(byte(v))
		i += 2
	}
	return b.String(), true
}
