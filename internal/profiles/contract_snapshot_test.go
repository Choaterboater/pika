package profiles_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Choaterboater/projectctl/internal/adopt"
	"github.com/Choaterboater/projectctl/internal/contract"
	"github.com/Choaterboater/projectctl/internal/profiles"
)

// copyFixture clones a discover testdata fixture tree into dst so the
// read-only golden fixtures stay untouched.
func copyFixture(t *testing.T, fixture, dst string) {
	t.Helper()
	root := filepath.Join("..", "discover", "testdata", fixture)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dstPath, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture %s: %v", fixture, err)
	}
}

// TestSnapshotGoFixtureContract is the brief's snapshot step: the draft
// contract adopt generates for the go-mod fixture pins [core@1, go@1],
// loads through contract.Load, and its profile selection resolves into
// the expected two-layer composition. Adopt-side resolution of every
// language stays a later integration point; this pins the generated
// artifact for one representative stack.
func TestSnapshotGoFixtureContract(t *testing.T) {
	root := t.TempDir()
	copyFixture(t, "go-mod", root)

	rep, err := adopt.Preview(root)
	if err != nil {
		t.Fatalf("adopt.Preview: %v", err)
	}
	if !slices.Equal(rep.DetectedProfiles, []string{"core@1", "go@1"}) {
		t.Fatalf("detected profiles = %v, want [core@1 go@1]", rep.DetectedProfiles)
	}

	draftPath := filepath.Join(root, ".project", "contract.yaml.draft")
	draft, err := contract.Load(draftPath)
	if err != nil {
		t.Fatalf("contract.Load(draft): %v", err)
	}
	if !slices.Equal(draft.Profiles, []string{"core@1", "go@1"}) {
		t.Errorf("draft profiles = %v, want [core@1 go@1]", draft.Profiles)
	}

	r, err := profiles.Resolve(draft.Profiles)
	if err != nil {
		t.Fatalf("Resolve(%v): %v", draft.Profiles, err)
	}
	if len(r.Layers) != 2 || r.Layers[0].Name != "core" || r.Layers[1].Name != "go" {
		t.Fatalf("layers = %+v, want core then go", r.Layers)
	}
	if !slices.Equal(r.Checks.Test.Cmd, []string{"go", "test", "./..."}) {
		t.Errorf("resolved test cmd = %v, want [go test ./...]", r.Checks.Test.Cmd)
	}

	// The lock draft must pin the same selection the draft contract
	// declares, with per-pack digests.
	lockRaw, err := os.ReadFile(filepath.Join(root, ".project", "profiles.lock.draft"))
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Packs map[string]struct {
			Version string `json:"version"`
			Source  string `json:"source"`
			Digest  string `json:"digest"`
		} `json:"packs"`
	}
	if err := json.Unmarshal(lockRaw, &lock); err != nil {
		t.Fatalf("profiles.lock.draft is not valid JSON: %v", err)
	}
	if len(lock.Packs) != 2 {
		t.Errorf("lock packs = %v, want core and go entries", lock.Packs)
	}
	for name := range map[string]bool{"core": true, "go": true} {
		p, ok := lock.Packs[name]
		if !ok {
			t.Errorf("lock missing pack %q", name)
			continue
		}
		if p.Version != "1" || p.Source != "embedded" || len(p.Digest) != 64 {
			t.Errorf("lock entry %s = %+v, want version 1, embedded, 64-hex digest", name, p)
		}
	}

	// Snapshot: the draft contract body is stable and pins both packs.
	contractYAML, err := os.ReadFile(draftPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"profiles:", "- core@1", "- go@1", "schema: 1"} {
		if !bytes.Contains(contractYAML, []byte(want)) {
			t.Errorf("draft contract missing %q in:\n%s", want, contractYAML)
		}
	}
	if !strings.Contains(string(lockRaw), "\"go\"") {
		t.Errorf("lock draft %s does not pin go", lockRaw)
	}
}
