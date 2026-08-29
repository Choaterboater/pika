package evidence

import (
	"fmt"
	"os"
	"path/filepath"
)

// tempPrefix names evidence temp files; Write removes them on failure,
// and tests use the prefix to assert no leftovers.
const tempPrefix = ".evidence-"

// Write durably writes the receipt to path as indented JSON, creating
// parent directories as needed. The write is atomic: the payload goes to
// a temp file in the target's directory, is fsynced, renamed over the
// target, and the directory is fsynced so the rename itself survives a
// crash. On Windows the directory fsync is best-effort (see
// sync_windows.go).
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("evidence: create %s: %w", dir, err)
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
