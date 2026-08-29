// Package fsutil provides the crash-durability filesystem helpers shared
// by txn and evidence: file- and directory-level fsyncs with per-platform
// tolerance, and syncing the directory chain that MkdirAll just created.
//
// Durability contract: on unix, SyncFile and SyncDir issue real fsyncs;
// on Windows (see sync_windows.go) content fsyncs on write-mode handles
// are real, while read-mode sync attempts are tolerated as best-effort
// and never fail the operation — Task 13's documented I4 resolution.
package fsutil

import (
	"os"
	"path/filepath"
)

// ExistingAncestor returns the nearest existing ancestor of dir,
// including dir itself when it already exists; it never returns empty.
func ExistingAncestor(dir string) string {
	for {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// SyncCreatedChain fsyncs every directory from dir up to and including
// anchor, making the links that bind a newly created directory tree into
// the filesystem durable. Record anchor with ExistingAncestor before the
// MkdirAll that created dir.
func SyncCreatedChain(dir, anchor string) error {
	for {
		if err := SyncDir(dir); err != nil {
			return err
		}
		if dir == anchor {
			return nil
		}
		dir = filepath.Dir(dir)
	}
}
