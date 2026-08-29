package initcmd

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Choaterboater/projectctl/internal/contract"
)

// languages lists the V1 language profiles in spec §5.4 order. Each gets a
// parametrized golden-dir test.
var languages = []string{"typescript", "python", "swift", "rust", "go"}

// treeFiles returns every regular file under root as slash-separated
// relative path to content bytes.
func treeFiles(root string) (map[string][]byte, error) {
	out := map[string][]byte{}
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
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	return out, err
}

func TestGoldenPerLanguage(t *testing.T) {
	for _, lang := range languages {
		t.Run(lang, func(t *testing.T) {
			// The scaffold target is named after the golden directory so
			// dir-name-derived content (project name, module path, package
			// name) matches the committed tree byte for byte.
			dir := filepath.Join(t.TempDir(), lang+"-single")
			if err := Run(InitOptions{Dir: dir, Profiles: []string{lang}}); err != nil {
				t.Fatalf("init %s: %v", lang, err)
			}
			generated, err := treeFiles(dir)
			if err != nil {
				t.Fatalf("walk generated tree: %v", err)
			}
			goldenRoot := filepath.Join("testdata", "golden", lang+"-single")
			golden, err := treeFiles(goldenRoot)
			if err != nil {
				t.Fatalf("walk golden tree: %v", err)
			}

			var diffs []string
			for _, path := range slices.Sorted(maps.Keys(generated)) {
				want, ok := golden[path]
				if !ok {
					diffs = append(diffs, path+": not in golden tree")
					continue
				}
				if !bytes.Equal(want, generated[path]) {
					diffs = append(diffs, path+": content differs")
				}
			}
			for _, path := range slices.Sorted(maps.Keys(golden)) {
				if _, ok := generated[path]; !ok {
					diffs = append(diffs, path+": missing from generated tree")
				}
			}
			if len(diffs) > 0 {
				t.Errorf("generated tree for %s is not byte-identical to %s:\n%s\nif this change is intentional, regenerate the golden directory by running `init` with profile %q into a directory named %q and replacing %s with its contents",
					lang, goldenRoot, strings.Join(diffs, "\n"), lang, lang+"-single", goldenRoot)
			}

			// The generated contract must load through the real loader.
			c, err := contract.Load(filepath.Join(dir, ".project", "contract.yaml"))
			if err != nil {
				t.Fatalf("generated contract does not load: %v", err)
			}
			if c.Schema != 1 {
				t.Errorf("contract schema = %d, want 1", c.Schema)
			}
			if c.Project.Topology != "single" {
				t.Errorf("contract topology = %q, want single", c.Project.Topology)
			}
			wantProfiles := []string{"core@1", lang + "@1"}
			if !reflect.DeepEqual(c.Profiles, wantProfiles) {
				t.Errorf("contract profiles = %v, want %v", c.Profiles, wantProfiles)
			}
			if c.GitHub.Merge != "squash" {
				t.Errorf("contract github.merge = %q, want squash", c.GitHub.Merge)
			}
			if c.Evidence.Publish != "sanitized" {
				t.Errorf("contract evidence.publish = %q, want sanitized", c.Evidence.Publish)
			}
		})
	}
}

func TestCoreOnlyScaffold(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bare")
	if err := Run(InitOptions{Dir: dir}); err != nil {
		t.Fatalf("init: %v", err)
	}
	c, err := contract.Load(filepath.Join(dir, ".project", "contract.yaml"))
	if err != nil {
		t.Fatalf("contract: %v", err)
	}
	want := []string{"core@1"}
	if !reflect.DeepEqual(c.Profiles, want) {
		t.Errorf("profiles = %v, want %v", c.Profiles, want)
	}
	for _, p := range []string{"go.mod", "package.json", "pyproject.toml", "Package.swift", "Cargo.toml"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			t.Errorf("core-only scaffold created stack file %s", p)
		}
	}
	for _, p := range []string{
		"AGENTS.md", "README.md", "CONTRIBUTING.md", ".github/pull_request_template.md",
		"docs/architecture/.gitkeep", "docs/decisions/.gitkeep", "docs/guides/.gitkeep",
		"docs/reference/.gitkeep", "docs/work/.gitkeep",
	} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("core-only scaffold missing %s", p)
		}
	}
}

func TestIdempotencyErrorsWithoutForce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-single")
	opts := InitOptions{Dir: dir, Profiles: []string{"go"}}
	if err := Run(opts); err != nil {
		t.Fatalf("first init: %v", err)
	}
	err := Run(opts)
	if err == nil {
		t.Fatal("second init without --force: got nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("refusal error %q does not mention --force", err)
	}
}

func TestForceRewritesAndPreservesUserFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-single")
	if err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}}); err != nil {
		t.Fatalf("first init: %v", err)
	}
	// User files live outside .project; force must never delete them.
	userFiles := map[string]string{
		"notes/ideas.md":          "# Ideas\n\nkeep me\n",
		"docs/guides/my-notes.md": "user content\n",
		"cmd/go-single/extra.go":  "package main\n",
	}
	for rel, content := range userFiles {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}, Force: true}); err != nil {
		t.Fatalf("force init: %v", err)
	}
	for rel, content := range userFiles {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("user file %s lost by --force: %v", rel, err)
		}
		if string(data) != content {
			t.Errorf("user file %s rewritten by --force: %q, want %q", rel, data, content)
		}
	}
	if _, err := contract.Load(filepath.Join(dir, ".project", "contract.yaml")); err != nil {
		t.Fatalf("contract after force does not load: %v", err)
	}
}

func TestJSONManifestIsSorted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-single")
	var buf bytes.Buffer
	if err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}, JSON: true, Out: &buf}); err != nil {
		t.Fatalf("init: %v", err)
	}
	var manifest struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(buf.Bytes(), &manifest); err != nil {
		t.Fatalf("manifest is not JSON: %v\n%s", err, buf.String())
	}
	if !slices.IsSorted(manifest.Files) {
		t.Errorf("manifest not sorted: %v", manifest.Files)
	}
	for _, want := range []string{".project/contract.yaml", ".project/profiles.lock", "AGENTS.md"} {
		if !slices.Contains(manifest.Files, want) {
			t.Errorf("manifest missing %s: %v", want, manifest.Files)
		}
	}
	// Every manifest entry exists; every created file is listed.
	for _, rel := range manifest.Files {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("manifest lists %s but it does not exist", rel)
		}
	}
	created, err := treeFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for rel := range created {
		if !slices.Contains(manifest.Files, rel) {
			t.Errorf("created file %s missing from manifest", rel)
		}
	}
}

func TestNameOverridesDirName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "untidy Dir.Name")
	if err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}, Name: "custom-name"}); err != nil {
		t.Fatalf("init: %v", err)
	}
	c, err := contract.Load(filepath.Join(dir, ".project", "contract.yaml"))
	if err != nil {
		t.Fatalf("contract: %v", err)
	}
	if c.Project.Name != "custom-name" {
		t.Errorf("project name = %q, want custom-name", c.Project.Name)
	}
	goMod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "module custom-name") {
		t.Errorf("go.mod module = %q, want module custom-name", goMod)
	}
}

func TestModuleNameDerivedAndOverridable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-single")
	if err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}}); err != nil {
		t.Fatalf("init: %v", err)
	}
	goMod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "module go-single") {
		t.Errorf("go.mod = %q, want module go-single derived from dir name", goMod)
	}

	dir2 := filepath.Join(t.TempDir(), "go-single")
	if err := Run(InitOptions{Dir: dir2, Profiles: []string{"go"}, Module: "example.com/foo/bar"}); err != nil {
		t.Fatalf("init with --module: %v", err)
	}
	goMod2, err := os.ReadFile(filepath.Join(dir2, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod2), "module example.com/foo/bar") {
		t.Errorf("go.mod = %q, want module example.com/foo/bar", goMod2)
	}
}

func TestUnknownProfileRejected(t *testing.T) {
	dir := t.TempDir()
	err := Run(InitOptions{Dir: dir, Profiles: []string{"cobol"}})
	if err == nil {
		t.Fatal("unknown profile accepted")
	}
}
