package checks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/profiles"
)

// lockRelPath is the location gate 1 must look for the lock under the
// root it is given. Production code derives it from repopath; the test
// spells it out independently so a silent change to that path table
// fails here rather than passing by construction.
const lockRelPath = ".project/profiles.lock"

func lockFixture(t *testing.T, refs []string) string {
	t.Helper()
	root := t.TempDir()
	if err := profiles.WriteLock(filepath.Join(root, filepath.FromSlash(lockRelPath)), refs); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	return root
}

// tamperLock edits the profiles.lock JSON at root through fn.
func tamperLock(t *testing.T, root string, fn func(m map[string]any)) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(lockRelPath))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// gate1Exit runs Gate1 on root and returns its exit code and output.
func gate1Exit(root string) (int, string) {
	c := &contract.Contract{Schema: 1, Profiles: []string{"core@1"}}
	resolved, err := profiles.Resolve([]string{"core@1"})
	if err != nil {
		return -1, err.Error()
	}
	exit, output, _ := Gate1(root, c, resolved)
	return exit, output
}

func TestGate1LockValidPasses(t *testing.T) {
	root := lockFixture(t, []string{"core@1"})
	if exit, output := gate1Exit(root); exit != 0 {
		t.Fatalf("Gate1 exit = %d, want 0 (output %q)", exit, output)
	}
}

func TestGate1MissingLockFails(t *testing.T) {
	root := lockFixture(t, []string{"core@1"})
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(lockRelPath))); err != nil {
		t.Fatal(err)
	}
	exit, output := gate1Exit(root)
	if exit != 1 {
		t.Fatalf("Gate1 exit = %d, want 1", exit)
	}
	if !strings.Contains(output, lockRelPath) || !strings.Contains(output, "missing") {
		t.Errorf("output %q must name the missing lock", output)
	}
}

func TestGate1WrongPinnedVersionFails(t *testing.T) {
	root := lockFixture(t, []string{"core@1"})
	tamperLock(t, root, func(m map[string]any) {
		packs := m["packs"].(map[string]any)
		core := packs["core"].(map[string]any)
		core["version"] = "2"
	})
	exit, output := gate1Exit(root)
	if exit != 1 {
		t.Fatalf("Gate1 exit = %d, want 1", exit)
	}
	if !strings.Contains(output, "core") || !strings.Contains(output, "version") {
		t.Errorf("output %q must name the pack and the version mismatch", output)
	}
}

func TestGate1PackAbsentFromLockFails(t *testing.T) {
	// The lock is written for core only, but the contract also selects
	// go@1: the contract references a pack the lock never pinned.
	root := t.TempDir()
	if err := profiles.WriteLock(filepath.Join(root, filepath.FromSlash(lockRelPath)), []string{"core@1"}); err != nil {
		t.Fatal(err)
	}
	c := &contract.Contract{Schema: 1, Profiles: []string{"core@1", "go@1"}}
	resolved, err := profiles.Resolve([]string{"core@1", "go@1"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	exit, output, _ := Gate1(root, c, resolved)
	if exit != 1 {
		t.Fatalf("Gate1 exit = %d, want 1", exit)
	}
	if !strings.Contains(output, "go") || !strings.Contains(output, "not pinned") {
		t.Errorf("output %q must name the unpinned pack", output)
	}
}

func TestGate1DigestMismatchFails(t *testing.T) {
	root := lockFixture(t, []string{"core@1"})
	tamperLock(t, root, func(m map[string]any) {
		packs := m["packs"].(map[string]any)
		core := packs["core"].(map[string]any)
		core["digest"] = "0000000000000000000000000000000000000000000000000000000000000000"
	})
	exit, output := gate1Exit(root)
	if exit != 1 {
		t.Fatalf("Gate1 exit = %d, want 1", exit)
	}
	if !strings.Contains(output, "digest") {
		t.Errorf("output %q must name the digest mismatch", output)
	}
}

// The lock's top-level digest pins the whole embedded pack registry. It
// is written by profiles.WriteLock, so a value that disagrees with this
// binary's registry means the lock came from elsewhere — a gate failure
// naming both digests, never a silent pass.
func TestGate1TopLevelDigestMismatchFails(t *testing.T) {
	root := lockFixture(t, []string{"core@1"})
	tamperLock(t, root, func(m map[string]any) {
		m["digest"] = "0000000000000000000000000000000000000000000000000000000000000000"
	})
	exit, output := gate1Exit(root)
	if exit != 1 {
		t.Fatalf("Gate1 exit = %d, want 1", exit)
	}
	if !strings.Contains(output, "0000000000000000000000000000000000000000000000000000000000000000") {
		t.Errorf("output %q must name the stored digest", output)
	}
	if !strings.Contains(output, profiles.PackDigest()) {
		t.Errorf("output %q must name the embedded registry digest", output)
	}
}
