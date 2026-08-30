package initcmd

import (
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/profiles"
)

// TestMain pins PATH resolution for the whole package. The contract's
// commands block is populated from pack hints whose tool is present on
// PATH, and the golden trees embed .project/contract.yaml byte for
// byte. Without a fixed lookPath the golden bytes would depend on what
// the machine running the tests happens to have installed. The stub
// reports every tool present, so the goldens record the full
// hint-populated contract.
func TestMain(m *testing.M) {
	lookPath = func(string) (string, error) { return "/usr/bin/stub", nil }
	os.Exit(m.Run())
}

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
			if _, err := Run(InitOptions{Dir: dir, Profiles: []string{lang}}); err != nil {
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

			// The scaffold must gitignore the state directory (spec
			// §14.2): state carries unredacted runtime records.
			gi, ok := generated[".gitignore"]
			if !ok {
				t.Fatal("scaffold is missing .gitignore")
			}
			if !strings.Contains(string(gi), ".project/state/") {
				t.Errorf(".gitignore = %q, want it to ignore .project/state/", gi)
			}
		})
	}
}

func TestCoreOnlyScaffold(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bare")
	if _, err := Run(InitOptions{Dir: dir}); err != nil {
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
	if _, err := Run(opts); err != nil {
		t.Fatalf("first init: %v", err)
	}
	_, err := Run(opts)
	if err == nil {
		t.Fatal("second init without --force: got nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("refusal error %q does not mention --force", err)
	}
}

func TestForceRewritesAndPreservesUserFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-single")
	if _, err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}}); err != nil {
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
	if _, err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}, Force: true}); err != nil {
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

// The manifest is init's answer to "what did you just create". It is
// returned as data — the JSON encoding lives in the command layer — and
// it must be sorted, complete, and true: every entry exists on disk and
// every created file is listed.
func TestManifestIsSortedAndComplete(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-single")
	manifest, err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !slices.IsSorted(manifest.Files) {
		t.Errorf("manifest not sorted: %v", manifest.Files)
	}
	for _, want := range []string{".project/contract.yaml", ".project/profiles.lock", "AGENTS.md"} {
		if !slices.Contains(manifest.Files, want) {
			t.Errorf("manifest missing %s: %v", want, manifest.Files)
		}
	}
	// The commands block init populated travels with the manifest: it is
	// how a caller learns which gates will actually run.
	if len(manifest.Commands) == 0 {
		t.Error("manifest reports no contract commands")
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
	if _, err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}, Name: "custom-name"}); err != nil {
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
	if _, err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}}); err != nil {
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
	if _, err := Run(InitOptions{Dir: dir2, Profiles: []string{"go"}, Module: "example.com/foo/bar"}); err != nil {
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
	_, err := Run(InitOptions{Dir: dir, Profiles: []string{"cobol"}})
	if err == nil {
		t.Fatal("unknown profile accepted")
	}
}

// TestCISetupCoversContractCommands pins the rule that every tool a
// generated contract command needs is installed by a CI setup step
// before the check invocation. Each row lists the substrings the
// language's workflow must carry; a new contract command (or a runner
// that lacks the tool) must add its row — the python gap (pytest not
// preinstalled on GitHub runners) regressed silently once already.
func TestCISetupCoversContractCommands(t *testing.T) {
	required := map[string][]string{
		// contract commands `gofmt -l -w .`, `go vet ./...`,
		// `go build -o /dev/null ./...`, `go test ./...`: setup-go
		// installs the whole toolchain.
		"go": []string{"actions/setup-go@v5"},
		// no typescript hint is autofillable, so the contract names no
		// command; node 24 setup keeps the hinted commands runnable once
		// the user adds the scripts and installs dependencies.
		"typescript": []string{"actions/setup-node@v4", "node-version: \"24\""},
		// contract commands `python -m pytest`, `ruff format .`,
		// `ruff check .`, `mypy .`: runners ship a bare interpreter, so
		// all three packages are installed explicitly.
		"python": []string{"actions/setup-python@v5", "python -m pip install pytest ruff mypy"},
		// contract commands `cargo build` / `cargo test` /
		// `cargo fmt -- --check` / `cargo clippy -- -D warnings`: cargo
		// ships on runners and rustup's stable profile carries rustfmt
		// and clippy; the workflow still pins stable.
		"rust": []string{"rustup default stable"},
		// contract commands `swift build` / `swift test`: swift ships on
		// runners. The format hint does not autofill, so no
		// swift-format-capable toolchain is required.
		"swift": []string{},
	}
	for _, lang := range languages {
		t.Run(lang, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), lang+"-single")
			if _, err := Run(InitOptions{Dir: dir, Profiles: []string{lang}}); err != nil {
				t.Fatalf("init %s: %v", lang, err)
			}
			ci, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "ci.yml"))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range required[lang] {
				if !strings.Contains(string(ci), want) {
					t.Errorf("generated ci.yml for %s lacks required setup %q; every tool a contract command needs must be installed before `pika check --ci`", lang, want)
				}
			}
		})
	}
}

// prTemplateSections lists the sections a pull request must state per
// spec §15.2. The scaffolded PR template carries one heading per
// section.
var prTemplateSections = []string{
	"## Why",
	"## Observable behavior",
	"## Implementation boundary",
	"## Verification evidence",
	"## Documentation and diagram impact",
	"## State migration and rollback",
	"## Remaining limitations",
}

func TestPullRequestTemplateCarriesSpecSections(t *testing.T) {
	out, err := renderCore("pull_request_template.md.tmpl", tmplData{Name: "demo"})
	if err != nil {
		t.Fatalf("render PR template: %v", err)
	}
	for _, section := range prTemplateSections {
		if !strings.Contains(string(out), section) {
			t.Errorf("scaffolded PR template lacks spec §15.2 section %q", section)
		}
	}
}

func TestRenderCoreMissingTemplateFails(t *testing.T) {
	const name = "nonexistent.md.tmpl"
	_, err := renderCore(name, tmplData{Name: "demo"})
	if err == nil {
		t.Fatalf("renderCore(%q) succeeded, want hard error", name)
	}
	if !strings.Contains(err.Error(), name) {
		t.Errorf("error %q does not name the missing template %q", err.Error(), name)
	}
}

// TestDigitLeadingNamesProduceValidStackIdentifiers scaffolds directories
// whose derived project name starts with a digit — legal under the
// contract name pattern — and asserts the language-level identifiers stay
// valid: cargo package names, Python module names, and Swift identifiers
// cannot begin with a digit. The contract keeps the raw name.
func TestDigitLeadingNamesProduceValidStackIdentifiers(t *testing.T) {
	for _, tc := range []struct {
		lang     string
		want     string // the guarded identifier that must appear in the scaffold
		file     string // scaffold-relative file carrying it
		fragment string
	}{
		{lang: "rust", want: "p1-check", file: "Cargo.toml", fragment: `name = "p1-check"`},
		{lang: "swift", want: "P1CheckTests", file: "Tests/1-check-tests/1-check-tests.swift", fragment: "final class P1CheckTests"},
		{lang: "python", want: "p1_check", file: "tests/test_init.py", fragment: "import p1_check"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "1-check")
			if _, err := Run(InitOptions{Dir: dir, Profiles: []string{tc.lang}}); err != nil {
				t.Fatalf("init %s: %v", tc.lang, err)
			}
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(tc.file)))
			if err != nil {
				t.Fatalf("read %s: %v", tc.file, err)
			}
			if !strings.Contains(string(data), tc.fragment) {
				t.Errorf("%s does not contain %q (want guarded identifier %q)", tc.file, tc.fragment, tc.want)
			}
			// The contract name itself keeps the raw, schema-legal form.
			c, err := contract.Load(filepath.Join(dir, ".project", "contract.yaml"))
			if err != nil {
				t.Fatalf("contract.Load: %v", err)
			}
			if c.Project.Name != "1-check" {
				t.Errorf("contract project name = %q, want 1-check", c.Project.Name)
			}
		})
	}
}

// A fresh TypeScript repo used to pass `pika check` with all five gates
// skipped: typescript@1 declares every slot discovery-only, so the report
// was green while nothing was verified. Populating from a bare PATH probe
// swapped that for a worse failure — every npm hint was adopted and the
// scaffold then failed its own checks — so a hint is adopted only when
// its pack marks it autofillable.
func TestCommandsPopulatedFromAutofillableHints(t *testing.T) {
	resolved, err := profiles.Resolve([]string{profiles.CoreRef, "go@1"})
	if err != nil {
		t.Fatal(err)
	}
	present := func(string) (string, error) { return "/usr/bin/stub", nil }

	got := commandsFromChecks(resolved.Checks, present)
	want := map[string]string{
		"format":    "gofmt -l -w .",
		"lint":      "go vet ./...",
		"typecheck": "go build -o /dev/null ./...",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
}

// Every typescript@1 hint runs through npm or npx, which resolve on any
// machine with node installed and then delegate to a package.json script
// or a registry download the scaffold does not provide. The PATH probe
// says yes to all four; autofill says no to all four, and the slots stay
// honest discovery skips.
func TestDelegatingHintsAreNotAdoptedEvenWhenTheirToolIsPresent(t *testing.T) {
	resolved, err := profiles.Resolve([]string{profiles.CoreRef, "typescript@1"})
	if err != nil {
		t.Fatal(err)
	}
	present := func(string) (string, error) { return "/usr/bin/stub", nil }

	if got := commandsFromChecks(resolved.Checks, present); len(got) != 0 {
		t.Fatalf("commands = %v; npm/npx hints delegate to scripts a fresh scaffold does not define and must never be adopted", got)
	}
	// The hints themselves survive: doctor renders them as remediation.
	if got := strings.Join(resolved.Checks.Lint.Hint, " "); got != "npm run lint" {
		t.Errorf("lint hint = %q, want it kept for doctor's remediation", got)
	}
}

func TestCommandsOmittedWhenToolAbsent(t *testing.T) {
	resolved, err := profiles.Resolve([]string{profiles.CoreRef, "go@1"})
	if err != nil {
		t.Fatal(err)
	}
	absent := func(string) (string, error) { return "", errors.New("not found") }

	if got := commandsFromChecks(resolved.Checks, absent); len(got) != 0 {
		t.Fatalf("commands = %v, want empty when no tool is on PATH", got)
	}
}

// A slot with a real cmd (not a hint) is the pack's own command and must
// not be duplicated into the contract.
func TestExplicitPackCommandsAreNotCopied(t *testing.T) {
	resolved, err := profiles.Resolve([]string{profiles.CoreRef, "go@1"})
	if err != nil {
		t.Fatal(err)
	}
	present := func(string) (string, error) { return "/usr/bin/stub", nil }

	if got := commandsFromChecks(resolved.Checks, present)["test"]; got != "" {
		t.Errorf("commands[test] = %q; go@1 already declares cmd, the contract must not duplicate it", got)
	}
}
