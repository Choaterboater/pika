package evidence

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Choaterboater/pika/internal/fsutil"
)

// tempPrefix names evidence temp files; Write removes them on failure,
// and tests use the prefix to assert no leftovers.
const tempPrefix = ".evidence-"

// sync hooks delegate to fsutil; tests override them to observe the
// durability contract.
var (
	syncDir          = fsutil.SyncDir
	syncCreatedChain = fsutil.SyncCreatedChain
	existingAncestor = fsutil.ExistingAncestor
)

// Write durably writes the receipt to path as indented JSON, creating
// parent directories as needed. The write is atomic and crash-durable:
// the payload goes to a temp file in the target's directory, is fsynced,
// and renamed over the target. Directory durability is handled in two
// steps: the chain of directories MkdirAll created is fsynced up to the
// nearest pre-existing ancestor (so the directories themselves survive a
// crash), then the target directory is fsynced again so the rename entry
// survives. On Windows the directory fsyncs are best-effort (see
// internal/fsutil/sync_windows.go).
func Write(path string, r *Receipt) error {
	bs, err := encode(r)
	if err != nil {
		return fmt.Errorf("evidence: encode receipt: %w", err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("evidence: resolve %q: %w", path, err)
	}
	dir := filepath.Dir(abs)
	anchor := existingAncestor(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("evidence: create %s: %w", dir, err)
	}
	if err := syncCreatedChain(dir, anchor); err != nil {
		return fmt.Errorf("evidence: fsync created directory chain: %w", err)
	}

	tmp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return fmt.Errorf("evidence: create temp file: %w", err)
	}
	name := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(name)
	}
	if _, err := tmp.Write(bs); err != nil {
		cleanup()
		return fmt.Errorf("evidence: write temp file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		cleanup()
		return fmt.Errorf("evidence: chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("evidence: fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("evidence: close temp file: %w", err)
	}
	if err := os.Rename(name, abs); err != nil {
		os.Remove(name)
		return fmt.Errorf("evidence: rename receipt into place: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("evidence: fsync receipt directory: %w", err)
	}
	return nil
}
