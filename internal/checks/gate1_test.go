package checks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/skills"
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

// projectionFixture writes a lock, installs the canonical skills, and
// generates the declared projection, so the repository starts in the
// state gate 1 is meant to certify.
func projectionFixture(t *testing.T) (string, *contract.Contract, *profiles.Resolved) {
	t.Helper()
	root := lockFixture(t, []string{"core@1"})
	c := &contract.Contract{
		Schema:   1,
		Profiles: []string{"core@1"},
		Skills:   &contract.Skills{Projections: []contract.Projection{{Harness: "codex", Path: "AGENTS.md"}}},
	}
	resolved, err := profiles.Resolve([]string{"core@1"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	bound, err := repopath.At(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := skills.Install(bound, c, resolved, false); err != nil {
		t.Fatalf("skills.Install: %v", err)
	}
	return root, c, resolved
}

// Spec §9.2: a projection identifies its source and digest, and CI
// rejects drift rather than maintaining parallel handwritten copies.
// Gate 1 is where that rejection happens, so a projection generated from
// a source that has since moved must fail it — naming the projection,
// the source, and the command that regenerates it. It must say `stale`
// and not `tampered`: nothing an operator wrote is at risk here, and
// regenerating is the whole remedy.
func TestGate1StaleProjectionFails(t *testing.T) {
	root, c, resolved := projectionFixture(t)
	if exit, output, _ := Gate1(root, c, resolved); exit != 0 {
		t.Fatalf("Gate1 exit = %d on a freshly generated projection, want 0 (output %q)", exit, output)
	}

	skill := filepath.Join(root, ".agents", "skills", "project-work", "SKILL.md")
	body, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, append(body, []byte("\nA rule added after the projection was written.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	exit, output, _ := Gate1(root, c, resolved)
	if exit != 1 {
		t.Fatalf("Gate1 exit = %d on a stale projection, want 1", exit)
	}
	for _, want := range []string{"stale", "AGENTS.md", ".agents/skills/project-work/SKILL.md", "pika skills install"} {
		if !strings.Contains(output, want) {
			t.Errorf("output %q must name %q", output, want)
		}
	}
	if strings.Contains(output, "tampered") {
		t.Errorf("a moved source was reported to gate 1 as a hand edit: %q", output)
	}

	// Regenerating is the whole remedy: nothing else has to be touched.
	bound, err := repopath.At(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := skills.Install(bound, c, resolved, false); err != nil {
		t.Fatalf("skills.Install: %v", err)
	}
	if exit, output, _ := Gate1(root, c, resolved); exit != 0 {
		t.Fatalf("Gate1 exit = %d after regenerating, want 0 (output %q)", exit, output)
	}
}

// The projection is the file the harness actually reads, so a hand edit
// inside the kernel-owned region feeds an agent instructions the kernel
// never issued. Gate 1 must catch that as its own failure and not as a
// stale copy: the remedies are opposites, and `pika skills install`
// would destroy the edit rather than adopt it.
func TestGate1TamperedProjectionFailsAsItsOwnState(t *testing.T) {
	root, c, resolved := projectionFixture(t)
	target := filepath.Join(root, "AGENTS.md")
	doc, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(doc), "<!-- pika:skills:end -->", "A line nobody generated.\n<!-- pika:skills:end -->", 1)
	if edited == string(doc) {
		t.Fatal("fixture did not find the end marker it meant to edit inside")
	}
	if err := os.WriteFile(target, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	exit, output, _ := Gate1(root, c, resolved)
	if exit != 1 {
		t.Fatalf("Gate1 exit = %d on a tampered projection, want 1 (output %q)", exit, output)
	}
	for _, want := range []string{"tampered", "AGENTS.md", "DISCARD"} {
		if !strings.Contains(output, want) {
			t.Errorf("output %q must name %q", output, want)
		}
	}
}

// A hand edit must stay visible when a source moved in the same working
// tree. Inferring "hand-edited" by elimination — the region differs and
// no source moved — reports exactly this case as the one where nothing
// is at risk, and sends the operator to the command that erases it.
func TestGate1TamperIsNotMaskedByAMovedSource(t *testing.T) {
	root, c, resolved := projectionFixture(t)
	target := filepath.Join(root, "AGENTS.md")
	doc, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(doc), "<!-- pika:skills:end -->", "A line nobody generated.\n<!-- pika:skills:end -->", 1)
	if err := os.WriteFile(target, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(root, ".agents", "skills", "project-work", "SKILL.md")
	body, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, append(body, []byte("\nA rule added at the same time.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	exit, output, _ := Gate1(root, c, resolved)
	if exit != 1 {
		t.Fatalf("Gate1 exit = %d, want 1 (output %q)", exit, output)
	}
	if !strings.Contains(output, "tampered") || strings.Contains(output, "stale") {
		t.Errorf("a moved source masked the hand edit; gate 1 said: %q", output)
	}
}

// A contract that declares no projection has no drift to have, and gate
// 1 must not invent one: the canonical location alone is the state spec
// §9.2 prefers.
func TestGate1IgnoresRepositoriesWithNoDeclaredProjection(t *testing.T) {
	root := lockFixture(t, []string{"core@1"})
	if exit, output := gate1Exit(root); exit != 0 {
		t.Fatalf("Gate1 exit = %d, want 0 (output %q)", exit, output)
	}
}
