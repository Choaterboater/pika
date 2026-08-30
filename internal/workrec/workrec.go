// Package workrec stores one durable record per `pika improve` run under
// .project/state/work/<work-id>/. Before this package a run lived only in
// process memory: an interruption left a branch, uncommitted agent edits
// and an orphan bundle directory that no code could name, let alone read
// back. The record is what makes a run resumable.
//
// Two properties are load-bearing, because `pika resume` reads this file
// to decide what world it is rejoining:
//
//   - Saves are atomic. A crash between phases leaves the last committed
//     phase fully readable; record.json is never observably partial.
//   - A corrupt record is reported, never repaired. Open names the file
//     and refuses. Resume refusing is safe; resume guessing a phase from
//     a damaged record is how an agent resumes into the wrong world.
//
// The record is deliberately a flat per-run JSON document rather than a
// database: it is local state (.project/state/ is gitignored), it is
// rewritten whole on every transition, and a human debugging a stuck run
// can read it with cat.
package workrec

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Choaterboater/pika/internal/evidence"
	"github.com/Choaterboater/pika/internal/fsutil"
	"github.com/Choaterboater/pika/internal/repopath"
)

const (
	// workDirName is the collection of run directories beneath
	// repopath.Root.StateDir().
	workDirName = "work"
	// recordFileName is the run document inside a run directory.
	recordFileName = "record.json"
	// handoffDirName is the subdirectory the lifecycle fills with its
	// agent handoff bundle. workrec creates it and owns the name so no
	// caller re-declares the string.
	handoffDirName = "handoff"
	// tempPrefix names save temp files. It is dotted and distinctive so a
	// leftover from a crashed save is skipped by List and invisible to
	// Open, which reads recordFileName and nothing else.
	tempPrefix = ".workrec-"
)

// Durability hooks. They delegate to fsutil and os so tests can observe
// the atomic-save contract at the instant it matters — the rename.
var (
	existingAncestor = fsutil.ExistingAncestor
	syncCreatedChain = fsutil.SyncCreatedChain
	syncDir          = fsutil.SyncDir
	renameFile       = os.Rename
)

// Handle is an open run directory plus the record last read or written
// through it.
type Handle struct {
	dir    string
	workID string
	rec    Record
}

// Dir is the run's directory, the parent of both record.json and the
// handoff bundle.
func (h *Handle) Dir() string { return h.dir }

// HandoffDir is the directory the lifecycle fills with its handoff
// bundle.
func (h *Handle) HandoffDir() string { return filepath.Join(h.dir, handoffDirName) }

// Record is the last record read or written through this handle. The
// Phases slice is cloned, so a caller running the usual read-modify-save
// loop — read the record, append a phase, Save it back — cannot write
// through the handle's cache and desync it from disk.
//
// Baseline and Recheck are still shared pointers: deep-copying a whole
// verify.Report on every read would cost more than callers need, since a
// phase transition produces a new report rather than editing one. Replace
// those fields wholesale; never mutate the pointed-to report in place.
func (h *Handle) Record() Record {
	out := h.rec
	out.Phases = append([]PhaseStamp(nil), h.rec.Phases...)
	return out
}

// workDir is the collection of run directories for a root.
func workDir(root *repopath.Root) string {
	return filepath.Join(root.StateDir(), workDirName)
}

// runDir resolves one run's directory. The id is validated first, so a
// caller can never steer the path out of .project/state/work.
func runDir(root *repopath.Root, workID string) (string, error) {
	if err := evidence.ValidateWorkID(workID); err != nil {
		return "", fmt.Errorf("workrec: %w", err)
	}
	return filepath.Join(workDir(root), workID), nil
}

// Create starts a new run at .project/state/work/<work-id>/, writes its
// first record and creates the handoff directory. It refuses an id that
// already exists and never overwrites one: the run directory is claimed
// with a single mkdir, so two processes drawing the same work id lose
// loudly rather than sharing a record. The suffix entropy that makes a
// collision unlikely in the first place is documented on
// evidence.NewWorkID and stated only there; this refusal is what turns
// whatever margin that is into a guarantee, so it must stay a single
// mkdir and never soften into a stat-then-create.
func Create(root *repopath.Root, rec Record) (*Handle, error) {
	dir, err := runDir(root, rec.WorkID)
	if err != nil {
		return nil, err
	}

	parent := workDir(root)
	anchor := existingAncestor(parent)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("workrec: create %s: %w", parent, err)
	}
	if err := syncCreatedChain(parent, anchor); err != nil {
		return nil, fmt.Errorf("workrec: fsync created directory chain: %w", err)
	}

	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("workrec: run %s already exists at %s", rec.WorkID, dir)
		}
		return nil, fmt.Errorf("workrec: create %s: %w", dir, err)
	}

	h := &Handle{dir: dir, workID: rec.WorkID}
	if err := os.Mkdir(h.HandoffDir(), 0o755); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("workrec: create %s: %w", h.HandoffDir(), err)
	}
	// Make the run and handoff directories themselves durable before the
	// first record lands in them.
	if err := syncCreatedChain(h.HandoffDir(), parent); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("workrec: fsync created directory chain: %w", err)
	}
	if err := h.Save(rec); err != nil {
		// The directory was ours and holds nothing yet; removing it keeps
		// a failed Create from leaving a run id claimed but unreadable.
		os.RemoveAll(dir)
		return nil, err
	}
	return h, nil
}

// Open reads an existing run. A record that does not parse, or whose
// work_id disagrees with its directory, is reported with the offending
// path and left exactly as found: Open never rewrites, truncates or
// repairs a record.
func Open(root *repopath.Root, workID string) (*Handle, error) {
	dir, err := runDir(root, workID)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("workrec: open run %s: %w", workID, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("workrec: %s is not a run directory", dir)
	}
	rec, err := readRecord(filepath.Join(dir, recordFileName), workID)
	if err != nil {
		return nil, err
	}
	return &Handle{dir: dir, workID: workID, rec: rec}, nil
}

// readRecord loads and validates one record.json. Every failure names
// the path, because the caller's next move is to look at the file.
func readRecord(path, workID string) (Record, error) {
	bs, err := os.ReadFile(path)
	if err != nil {
		return Record{}, fmt.Errorf("workrec: read %s: %w", path, err)
	}
	var rec Record
	if err := json.Unmarshal(bs, &rec); err != nil {
		return Record{}, fmt.Errorf("workrec: %s is not a readable record: %w", path, err)
	}
	if rec.WorkID != workID {
		return Record{}, fmt.Errorf("workrec: %s holds work id %q, want %q", path, rec.WorkID, workID)
	}
	return rec, nil
}

// Save durably replaces the run's record. The write is atomic and
// crash-durable by the same path internal/evidence/write.go uses, over
// the same internal/fsutil helpers: the payload goes to a temp file in
// the target's own directory, is chmodded and fsynced, then renamed over
// record.json, after which the directory is fsynced so the rename entry
// survives. A second durability implementation would be free to drift
// from the first, so this one stays a mirror of that one. On Windows the
// directory fsyncs are best-effort (see internal/fsutil/sync_windows.go).
//
// Because the switch is a single intra-directory rename, a crash leaves
// either the previous record or the new one — never a half-written file.
func (h *Handle) Save(rec Record) error {
	if rec.WorkID != h.workID {
		return fmt.Errorf("workrec: record work id %q does not match run %s", rec.WorkID, h.workID)
	}
	bs, err := encode(rec)
	if err != nil {
		return fmt.Errorf("workrec: encode record: %w", err)
	}
	path := filepath.Join(h.dir, recordFileName)

	tmp, err := os.CreateTemp(h.dir, tempPrefix+"*")
	if err != nil {
		return fmt.Errorf("workrec: create temp file: %w", err)
	}
	name := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(name)
	}
	if _, err := tmp.Write(bs); err != nil {
		cleanup()
		return fmt.Errorf("workrec: write temp file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		cleanup()
		return fmt.Errorf("workrec: chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("workrec: fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("workrec: close temp file: %w", err)
	}
	if err := renameFile(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("workrec: rename record into place: %w", err)
	}
	// The cache is adopted here, between the rename and the directory
	// fsync, because the rename is the instant the new content becomes
	// what a reader of record.json sees. evidence/write.go has no cache to
	// keep honest, so its ordering does not matter; here, updating after
	// the fsync would let a failed fsync return an error while Record()
	// still reported the previous phase — disagreeing with a file that has
	// already moved on. The fsync error is still returned: it says the
	// rename may not survive a crash, not that it did not happen.
	h.rec = rec
	if err := syncDir(h.dir); err != nil {
		return fmt.Errorf("workrec: fsync run directory: %w", err)
	}
	return nil
}

// List returns every run record, newest first. "Newest" is the last time
// a record was saved, not the work id's date prefix, which is only
// day-granular and cannot order runs started on the same day; equal
// timestamps break ties on work id descending so the order is total.
//
// Entries that are not run directories — plain files, including a temp
// file left by a crashed save — are skipped, as is a directory claimed by
// Create whose record never landed. A record that exists but does not
// parse is an error, not a silent omission: List reports damage the same
// way Open does.
func List(root *repopath.Root) ([]Record, error) {
	dir := workDir(root)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("workrec: read %s: %w", dir, err)
	}

	type entry struct {
		rec  Record
		when time.Time
	}
	found := make([]entry, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if evidence.ValidateWorkID(id) != nil {
			continue
		}
		path := filepath.Join(dir, id, recordFileName)
		fi, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("workrec: stat %s: %w", path, err)
		}
		rec, err := readRecord(path, id)
		if err != nil {
			return nil, err
		}
		found = append(found, entry{rec: rec, when: fi.ModTime()})
	}

	sort.Slice(found, func(i, j int) bool {
		if !found[i].when.Equal(found[j].when) {
			return found[i].when.After(found[j].when)
		}
		return found[i].rec.WorkID > found[j].rec.WorkID
	})

	out := make([]Record, len(found))
	for i, f := range found {
		out[i] = f.rec
	}
	return out, nil
}
