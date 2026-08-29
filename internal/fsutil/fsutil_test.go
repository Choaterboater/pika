package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExistingAncestor(t *testing.T) {
	root := t.TempDir()
	fresh := filepath.Join(root, "a", "b", "c")
	if got := ExistingAncestor(fresh); got != root {
		t.Errorf("ExistingAncestor(fresh) = %q, want root %q", got, root)
	}
	partial := filepath.Join(root, "a")
	if err := os.MkdirAll(partial, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ExistingAncestor(filepath.Join(partial, "x", "y")); got != partial {
		t.Errorf("ExistingAncestor(partial) = %q, want %q", got, partial)
	}
	if got := ExistingAncestor(root); got != root {
		t.Errorf("ExistingAncestor(existing) = %q, want %q", got, root)
	}
}

func TestSyncCreatedChain(t *testing.T) {
	root := t.TempDir()
	leaf := filepath.Join(root, "x", "y", "z")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	// Syncing a real created chain must succeed and stop at the anchor.
	if err := SyncCreatedChain(leaf, root); err != nil {
		t.Fatalf("SyncCreatedChain: %v", err)
	}
}
