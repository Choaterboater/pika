package e2e

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// copyFixtureTree clones a read-only discover fixture into dst.
func copyFixtureTree(t *testing.T, fixture, dst string) {
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
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture %s: %v", fixture, err)
	}
}

// treePaths returns the slash-separated relative paths of every file
// under root.
func treePaths(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// TestE2EAdoptPreview runs `pika adopt` on a real fixture
// repository: the JSON report inventories the go stack against core@1,
// and the only writes are the two .draft proposal files — adopt is
// read-only otherwise (spec §13).
func TestE2EAdoptPreview(t *testing.T) {
	dir := t.TempDir()
	copyFixtureTree(t, "go-mod", dir)
	before := treePaths(t, dir)

	out := runCLI(t, dir, 0, "adopt", "--json")
	var rep struct {
		DetectedProfiles []string `json:"detectedProfiles"`
		Exceptions       []any    `json:"exceptions"`
		Conflicts        []any    `json:"conflicts"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("adopt --json did not print a JSON report: %v\n%s", err, out)
	}
	if !slices.Equal(rep.DetectedProfiles, []string{"core@1", "go@1"}) {
		t.Errorf("detectedProfiles = %v, want [core@1 go@1]", rep.DetectedProfiles)
	}

	after := treePaths(t, dir)
	var added []string
	for _, p := range after {
		if !slices.Contains(before, p) {
			added = append(added, p)
		}
	}
	slices.Sort(added)
	want := []string{".project/contract.yaml.draft", ".project/profiles.lock.draft", "review/adoption-review.md"}
	if !slices.Equal(added, want) {
		t.Errorf("adopt wrote %v, want exactly %v", added, want)
	}

	// Human-readable mode: a deterministic summary, no JSON.
	human := runCLI(t, dir, 0, "adopt")
	for _, want := range []string{"core@1, go@1", "proposed exceptions", "drafts written"} {
		if !strings.Contains(human, want) {
			t.Errorf("human report missing %q:\n%s", want, human)
		}
	}

	// Usage errors exit 2.
	runCLI(t, dir, 2, "adopt", "junk")
}
