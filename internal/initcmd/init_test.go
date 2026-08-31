package initcmd

import (
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/checks"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/version"
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

// operatorOwnedEdits are the managed files an operator is expected to
// rewrite once the scaffold has served its purpose: the docs spine's
// prose and the language scaffold's entry point. Each is seeded with
// content no template could produce, so a survivor is unambiguous.
var operatorOwnedEdits = map[string]string{
	"README.md":             "# go-single\n\nOPERATOR PROSE: this README was rewritten by a human.\n",
	"AGENTS.md":             "# Agents\n\nOPERATOR PROSE: house rules for agents.\n",
	"CONTRIBUTING.md":       "# Contributing\n\nOPERATOR PROSE: how we actually work.\n",
	"cmd/go-single/main.go": "package main\n\n// OPERATOR CODE: the real entry point.\nfunc main() {}\n",
}

// scaffoldGo inits a go scaffold into a fresh directory named
// "go-single" (dir-name-derived content matters) and returns its path.
func scaffoldGo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "go-single")
	if _, err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}}); err != nil {
		t.Fatalf("first init: %v", err)
	}
	return dir
}

// overwrite replaces the file at rel under dir with content.
func overwrite(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readRel reads the file at rel under dir, failing the test if it is gone.
func readRel(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// --force is the documented remedy for a rotated pack digest, so it runs
// against repositories that have been lived in. What it regenerates must
// therefore be what the kernel owns: a README, AGENTS.md, CONTRIBUTING.md
// or entry point the operator has written is theirs, and restoring the
// scaffold's placeholder text over it destroys work that no other copy of
// the file can be recovered from.
//
// AGENTS.md is the one file in operatorOwnedEdits that also carries a
// declared projection: the region below the operator's prose is
// kernel-owned and --force regenerates it unconditionally (spec 5.2),
// so AGENTS.md is checked by prefix rather than full equality; the
// other three files carry no projection and are checked byte for byte.
func TestForcePreservesOperatorOwnedFiles(t *testing.T) {
	dir := scaffoldGo(t)
	for rel, content := range operatorOwnedEdits {
		overwrite(t, dir, rel, content)
	}
	if _, err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}, Force: true}); err != nil {
		t.Fatalf("force init: %v", err)
	}
	for rel, want := range operatorOwnedEdits {
		got := readRel(t, dir, rel)
		if rel == "AGENTS.md" {
			if !strings.HasPrefix(got, want) {
				t.Errorf("--force rewrote operator prose in %s:\n got: %q\nwant prefix: %q", rel, got, want)
			}
			continue
		}
		if got != want {
			t.Errorf("--force rewrote operator-owned %s:\n got: %q\nwant: %q", rel, got, want)
		}
	}
}

// Nothing becomes impossible, only non-default: --reset-docs asks for the
// scaffold's own text back and gets it byte for byte.
func TestResetDocsRestoresTemplates(t *testing.T) {
	dir := scaffoldGo(t)
	pristine := map[string]string{}
	for rel, edited := range operatorOwnedEdits {
		pristine[rel] = readRel(t, dir, rel)
		overwrite(t, dir, rel, edited)
	}
	if _, err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}, Force: true, ResetDocs: true}); err != nil {
		t.Fatalf("force init with --reset-docs: %v", err)
	}
	for rel, want := range pristine {
		if got := readRel(t, dir, rel); got != want {
			t.Errorf("--reset-docs did not restore %s:\n got: %q\nwant: %q", rel, got, want)
		}
	}
	// An exception carries a rationale, an owner and a review condition a
	// human wrote and a reviewer accepted. Regenerating docs is not a
	// reason to discard evidence, so --reset-docs does not reach it.
	const recorded = "naming/kebab-case:\n  reason: vendored\n"
	overwrite(t, dir, checks.ExceptionsFile, recorded)
	if _, err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}, Force: true, ResetDocs: true}); err != nil {
		t.Fatalf("second force init with --reset-docs: %v", err)
	}
	if got := readRel(t, dir, checks.ExceptionsFile); got != recorded {
		t.Errorf("--reset-docs reset %s: got %q, want %q", checks.ExceptionsFile, got, recorded)
	}
}

// The other half of the split: --force still exists to repair what the
// kernel owns. A stale CI workflow, a stale PR template and a stale lock
// are exactly what an operator runs it for.
func TestForceStillRegeneratesKernelOwnedFiles(t *testing.T) {
	dir := scaffoldGo(t)
	kernelOwned := []string{
		".project/profiles.lock",
		".github/pull_request_template.md",
		".github/workflows/ci.yml",
	}
	pristine := map[string]string{}
	for _, rel := range kernelOwned {
		pristine[rel] = readRel(t, dir, rel)
		overwrite(t, dir, rel, "stale\n")
	}
	if _, err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}, Force: true}); err != nil {
		t.Fatalf("force init: %v", err)
	}
	for _, rel := range kernelOwned {
		if got := readRel(t, dir, rel); got != pristine[rel] {
			t.Errorf("--force did not regenerate kernel-owned %s: got %q", rel, got)
		}
	}
	// The contract is kernel-owned too, and it must still load.
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
		// contract commands `gofmt -l .`, `go vet ./...`,
		// `go build -o /dev/null ./...`, `go test ./...`: setup-go
		// installs the whole toolchain.
		"go": []string{"actions/setup-go@v5"},
		// no typescript hint is autofillable, so the contract names no
		// command; node 24 setup keeps the hinted commands runnable once
		// the user adds the scripts and installs dependencies.
		"typescript": []string{"actions/setup-node@v4", "node-version: \"24\""},
		// contract commands `python -m pytest`, `ruff format --check .`,
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
		"format":    "gofmt -l .",
		"lint":      "go vet ./...",
		"typecheck": "go build -o /dev/null ./...",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
}

// Every typescript@1 hint runs through npm, which resolves on any
// machine with node installed and then delegates to a package.json
// script the scaffold does not provide. The PATH probe says yes to all
// four; autofill says no to all four, and the slots stay honest
// discovery skips.
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

// renderScaffoldedCI renders the workflow `pika init` writes for one
// language, straight from the core pack template.
func renderScaffoldedCI(t *testing.T, lang string) string {
	t.Helper()
	core, err := CoreFiles(lang, lang+"-single")
	if err != nil {
		t.Fatalf("render core files for %s: %v", lang, err)
	}
	ci, ok := core[".github/workflows/ci.yml"]
	if !ok {
		t.Fatalf("core files for %s carry no CI workflow", lang)
	}
	return string(ci)
}

// TestScaffoldedCIHasNoPathsFilter pins the removal of the trigger path
// filter. A filter can only name the directories that existed when the
// scaffold was written, so every directory the repository grows
// afterwards is exempt from CI while CI still reports success — the
// worst available failure mode for a verification tool. The filter also
// never listed the adopting repository's own source tree beyond the one
// directory the stack template creates.
func TestScaffoldedCIHasNoPathsFilter(t *testing.T) {
	for _, lang := range languages {
		t.Run(lang, func(t *testing.T) {
			ci := renderScaffoldedCI(t, lang)
			if strings.Contains(ci, "paths:") {
				t.Errorf("scaffolded ci.yml for %s still filters trigger paths:\n%s", lang, ci)
			}
			// The two settings the filter's removal depends on: a full
			// clone so `pika check --changed` can resolve a merge base
			// rather than silently widening to every gate, and least
			// privilege for a job that only reads the tree.
			for _, want := range []string{"fetch-depth: 0", "permissions:", "contents: read"} {
				if !strings.Contains(ci, want) {
					t.Errorf("scaffolded ci.yml for %s lacks %q:\n%s", lang, want, ci)
				}
			}
		})
	}
}

// TestScaffoldedCIPinsTheKernelThatScaffoldedIt pins the replacement of
// `go install ...@latest`. Under @latest the kernel that judges a
// repository can change with no commit to that repository: a green pull
// request goes red on merge with nothing in the diff to explain it, and
// the operator has no way to see why. The pin must also be DERIVED from
// the version the binary reports rather than transcribed into the
// template, or it stops meaning "the kernel that adopted this repo" at
// the next release with nothing to catch the drift.
func TestScaffoldedCIPinsTheKernelThatScaffoldedIt(t *testing.T) {
	want := "v" + version.Version
	for _, lang := range languages {
		t.Run(lang, func(t *testing.T) {
			ci := renderScaffoldedCI(t, lang)
			// The defect is the install target, not the word: the
			// template's own comment names @latest to explain why it is
			// gone, so the assertion has to name the module path too.
			if strings.Contains(ci, "cmd/pika@latest") {
				t.Errorf("scaffolded ci.yml for %s still installs a floating kernel:\n%s", lang, ci)
			}
			if !strings.Contains(ci, "PIKA_REF: "+want) {
				t.Errorf("scaffolded ci.yml for %s does not pin PIKA_REF to %q (the version this binary reports):\n%s", lang, want, ci)
			}
			if !strings.Contains(ci, "github.com/Choaterboater/pika/cmd/pika@$PIKA_REF") {
				t.Errorf("scaffolded ci.yml for %s does not install the kernel at the pinned ref:\n%s", lang, ci)
			}
		})
	}
}

// stubExecutable writes a no-op executable named name into dir, so a
// fabricated PATH can stand in for a host that has that binary and no
// other.
func stubExecutable(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestPythonTestCommandRunsOnDebian pins python@1's test slot against the
// most common Linux host. The slot is an unconditional cmd, not a hint,
// so init's PATH probing never gets a chance to drop it: whatever it
// names is written into every scaffolded contract and is what the test
// gate runs. `python -m pytest` names an interpreter Debian and Ubuntu
// do not ship — they provide `python3` only — so it produced a repository
// that failed its own test gate on the host most likely to run it.
func TestPythonTestCommandRunsOnDebian(t *testing.T) {
	resolved, err := profiles.Resolve([]string{profiles.CoreRef, "python@1"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := resolved.Checks.Test.Cmd
	if len(cmd) == 0 {
		t.Fatal("python@1 declares no test command")
	}
	// Platform-independent half: the gate must not name a bare `python`
	// executable at all. This holds on Windows too, where the python.org
	// installer creates python.exe but no python3.exe — so `python3 -m
	// pytest` would only have moved the hole rather than closed it.
	if cmd[0] == "python" {
		t.Fatalf("python@1 test command %q requires a `python` executable", strings.Join(cmd, " "))
	}

	if runtime.GOOS == "windows" {
		t.Skip("the PATH fixture below is a POSIX shell script; the assertion above covers Windows")
	}
	// Executable half: a PATH that is exactly a Debian host with pytest
	// installed — python3 and pytest, and no `python`.
	binDir := t.TempDir()
	stubExecutable(t, binDir, "python3")
	stubExecutable(t, binDir, "pytest")
	t.Setenv("PATH", binDir)
	// Non-vacuous by construction: if `python` still resolved, the run
	// below would prove nothing.
	if path, err := exec.LookPath("python"); err == nil {
		t.Fatalf("fixture PATH still resolves python at %s", path)
	}
	out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("python@1 test command %q does not run where only python3 exists: %v\n%s",
			strings.Join(cmd, " "), err, out)
	}
}

// --- `--force` regenerates from the repository, not from flags alone ---
//
// `pika init --force` is the documented remedy for a rotated profile
// digest, so it runs against repositories that already carry a contract,
// a profile selection, a module path and a set of recorded exceptions.
// Rebuilding all of that from whatever happened to be on the command
// line turns the remedy into data loss: a bare --force used to produce a
// core-only contract with an empty commands block and an erased
// exceptions record. Every value is now resolved as explicit flag, else
// read-back from the repository, else refusal.

// seedRepo scaffolds a repository the way an operator's would already
// exist before they run the upgrade note's `pika init --force`.
func seedRepo(t *testing.T, lang string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "go-single")
	if _, err := Run(InitOptions{Dir: dir, Profiles: []string{lang}}); err != nil {
		t.Fatalf("seed init: %v", err)
	}
	return dir
}

func loadContract(t *testing.T, dir string) *contract.Contract {
	t.Helper()
	c, err := contract.Load(filepath.Join(dir, filepath.FromSlash(contractRel)))
	if err != nil {
		t.Fatalf("contract: %v", err)
	}
	return c
}

func TestForceReadsProfilesBackFromTheContract(t *testing.T) {
	dir := seedRepo(t, "go")
	before := loadContract(t, dir)

	// A bare --force: no --profile, exactly what the upgrade note tells
	// the operator to run.
	if _, err := Run(InitOptions{Dir: dir, Force: true}); err != nil {
		t.Fatalf("force init: %v", err)
	}

	after := loadContract(t, dir)
	if !slices.Equal(after.Profiles, before.Profiles) {
		t.Errorf("profiles = %v, want %v read back from the contract", after.Profiles, before.Profiles)
	}
	if !slices.Contains(after.Profiles, "go@1") {
		t.Errorf("profiles = %v, want the go pack retained", after.Profiles)
	}
	// The selection is what fills the commands block, so losing it
	// silently disarms every gate.
	if len(after.Commands) == 0 {
		t.Errorf("commands = %v, want the go pack's gates retained", after.Commands)
	}
	if !maps.Equal(after.Commands, before.Commands) {
		t.Errorf("commands = %v, want %v", after.Commands, before.Commands)
	}
}

// TestForcePreservesADiscoveredCommandAutofillCannotReproduce closes a
// real defect: a repository with no package.json at all (a bare Node
// project invoked directly, say `node --test test/*.test.mjs`, adopted
// through `pika adopt`, not scaffolded by `pika init`) has a real test
// command in its committed contract that commandsFromChecks can never
// reconstruct — its autofill only ever resolves a pack-declared hint
// (an npm script) against PATH, and there is no npm script here at
// all. `pika init --force`, run as the documented remedy for a stale
// pack digest, silently replaced `commands.test` with an empty map:
// the operator's real test gate was disarmed, and `pika check --all`
// went green having verified nothing, with nothing in the output to
// say so.
func TestForcePreservesADiscoveredCommandAutofillCannotReproduce(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	const discoveredTest = "node --test test/*.test.mjs"
	writeContract := "schema: 1\n" +
		"project:\n  name: moldable\n  topology: single\n" +
		"profiles: [core@1, typescript@1]\n" +
		"packages:\n  moldable:\n    root: .\n    profiles: [core@1, typescript@1]\n" +
		"commands:\n  test: \"" + discoveredTest + "\"\n" +
		"github:\n  merge: squash\n" +
		"evidence:\n  publish: sanitized\n"
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(contractRel)), []byte(writeContract), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(InitOptions{Dir: dir, Force: true}); err != nil {
		t.Fatalf("force init: %v", err)
	}

	after := loadContract(t, dir)
	if got := after.Commands["test"]; got != discoveredTest {
		t.Errorf("commands[test] = %q, want the discovered command %q preserved", got, discoveredTest)
	}
}

func TestForceReadsProjectNameBackFromTheContract(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "untidy Dir.Name")
	if _, err := Run(InitOptions{Dir: dir, Profiles: []string{"go"}, Name: "custom-name"}); err != nil {
		t.Fatalf("seed init: %v", err)
	}
	if _, err := Run(InitOptions{Dir: dir, Force: true}); err != nil {
		t.Fatalf("force init: %v", err)
	}
	if got := loadContract(t, dir).Project.Name; got != "custom-name" {
		t.Errorf("project name = %q, want custom-name read back from the contract", got)
	}
}

func TestExplicitFlagsWinOverReadBack(t *testing.T) {
	dir := seedRepo(t, "go")
	if _, err := Run(InitOptions{
		Dir:      dir,
		Force:    true,
		Name:     "renamed",
		Module:   "example.com/renamed",
		Profiles: []string{"rust"},
	}); err != nil {
		t.Fatalf("force init: %v", err)
	}

	c := loadContract(t, dir)
	if c.Project.Name != "renamed" {
		t.Errorf("project name = %q, want renamed from --name", c.Project.Name)
	}
	if !slices.Contains(c.Profiles, "rust@1") || slices.Contains(c.Profiles, "go@1") {
		t.Errorf("profiles = %v, want the rust pack from --profile and no go pack", c.Profiles)
	}
	// --module wins over the module recovered from the go.mod the seed
	// scaffold left behind. go.mod is part of the language scaffold and
	// therefore operator-owned — a repository's go.mod carries its whole
	// dependency graph, and regenerating it from a two-line template
	// would delete every require directive — so bare --force preserves
	// it and --reset-docs is what asks for the scaffolded one back.
	goMod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "module go-single") {
		t.Fatalf("fixture go.mod = %q, want the seed module still present", goMod)
	}
	if _, err := Run(InitOptions{Dir: dir, Force: true, Profiles: []string{"go"}, Module: "example.com/renamed"}); err != nil {
		t.Fatalf("force init with --module: %v", err)
	}
	if goMod, err = os.ReadFile(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "module go-single") {
		t.Errorf("--force rewrote the operator's go.mod: %q", goMod)
	}
	if _, err := Run(InitOptions{Dir: dir, Force: true, ResetDocs: true, Profiles: []string{"go"}, Module: "example.com/renamed"}); err != nil {
		t.Fatalf("force init with --module --reset-docs: %v", err)
	}
	goMod, err = os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "module example.com/renamed") {
		t.Errorf("go.mod = %q, want module example.com/renamed from --module", goMod)
	}
}

// Each exception carries a rule id, a rationale, an owner and a review
// condition that a human wrote and a reviewer accepted. Regenerating a
// managed file must never discard them: that is destroying evidence, not
// rewriting a generated artifact.
func TestForcePreservesRecordedExceptions(t *testing.T) {
	dir := seedRepo(t, "go")
	record := "vendor/legacy_Client.go:\n" +
		"  rule-id: naming-kebab-case\n" +
		"  reason: vendored upstream source; renaming it breaks the sync script\n" +
		"  owner: platform-team\n" +
		"  review-condition: drop when upstream ships a Go module\n"
	path := filepath.Join(dir, filepath.FromSlash(checks.ExceptionsFile))
	if err := os.WriteFile(path, []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(InitOptions{Dir: dir, Force: true}); err != nil {
		t.Fatalf("force init: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s lost by --force: %v", checks.ExceptionsFile, err)
	}
	if string(got) != record {
		t.Errorf("%s rewritten by --force:\ngot:\n%s\nwant:\n%s", checks.ExceptionsFile, got, record)
	}
	// Byte equality is the assertion; loading it proves the bytes are
	// still a usable record rather than an accidentally intact blob.
	ex, err := checks.LoadExceptions(dir)
	if err != nil {
		t.Fatalf("LoadExceptions after force: %v", err)
	}
	if _, ok := ex["vendor/legacy_Client.go"]; !ok {
		t.Errorf("recorded exception missing after force: %v", ex)
	}
}

// The contract has no module field, so a Go module path can only come
// from a flag or from go.mod. Falling back to the directory name is what
// used to rewrite go.mod under a name nothing imports and scaffold a
// stray cmd/<dirname>/main.go beside the real one.
func TestForceRefusesWhenNoModuleCanBeRecovered(t *testing.T) {
	dir := seedRepo(t, "go")
	if err := os.Remove(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatal(err)
	}
	_, err := Run(InitOptions{Dir: dir, Force: true})
	if err == nil {
		t.Fatal("force with no recoverable module: got nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "--module") {
		t.Errorf("refusal %q does not tell the operator to pass --module", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("refusal still wrote go.mod: %v", err)
	}
}

// A corrupt contract is a fact to report. Quietly rebuilding from flags
// is how an operator loses a contract they could have repaired.
func TestForceRefusesUnparseableContract(t *testing.T) {
	dir := seedRepo(t, "go")
	path := filepath.Join(dir, filepath.FromSlash(contractRel))
	if err := os.WriteFile(path, []byte("schema: 1\nproject:\n  name: [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(InitOptions{Dir: dir, Force: true, Profiles: []string{"go"}, Module: "example.com/x"}); err == nil {
		t.Fatal("force over an unparseable contract: got nil error, want refusal")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "[broken") {
		t.Errorf("refusal overwrote the contract it could not read: %q", got)
	}
}

// A contract with more than one package could only have come from
// `pika adopt` + `pika apply` — buildContract, the only contract
// `pika init` itself ever writes, always declares exactly one. Real
// case: a Tauri-shaped repository (package.json + src-tauri/Cargo.toml)
// adopted through `pika adopt`/`pika apply`, then `pika init --force`
// run against it (the documented remedy for a rotated pack digest)
// used to silently rebuild a single-package, single-language,
// commandless contract over it and scaffold a bare src/index.ts into
// a repository with no such layout at all.
//
// --force now takes the narrower path instead: refresh profiles.lock
// and the two kernel-owned core files against the current registry,
// leaving contract.yaml — every package's root, profiles, and every
// discovered command — completely untouched. This is the actual
// remedy a rotated pack digest needs, scoped to what a multi-package
// contract can have refreshed safely.
func TestForceRefreshesLockAndKernelFilesForAnAdoptedMultiPackageContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filepath.FromSlash(contractRel))
	contractYAML := "schema: 1\n" +
		"project:\n  name: greencli\n  topology: workspace\n" +
		"profiles: [core@1]\n" +
		"packages:\n" +
		"  greencli:\n    root: .\n    profiles: [core@1, typescript@1]\n" +
		"  src-tauri:\n    root: src-tauri\n    profiles: [core@1, rust@1]\n" +
		"commands:\n  test: \"cargo test && npm test\"\n" +
		"github:\n  merge: squash\n" +
		"evidence:\n  publish: sanitized\n"
	if err := os.WriteFile(path, []byte(contractYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	// A lock recording digests no current registry could ever produce
	// — the stale-pack-digest state --force exists to fix.
	staleLock := `{"digest":"stale","packs":{"core":{"version":"1","source":"embedded","digest":"stale"},"typescript":{"version":"1","source":"embedded","digest":"stale"},"rust":{"version":"1","source":"embedded","digest":"stale"}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(lockRel)), []byte(staleLock), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pre-existing kernel-owned files, standing in for whatever an
	// older kernel scaffolded.
	const staleCI = "# stale workflow\n"
	const stalePR = "## stale PR template\n"
	if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".github", "workflows", "ci.yml"), []byte(staleCI), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".github", "pull_request_template.md"), []byte(stalePR), 0o644); err != nil {
		t.Fatal(err)
	}

	manifest, err := Run(InitOptions{Dir: dir, Force: true})
	if err != nil {
		t.Fatalf("force over a multi-package contract: %v", err)
	}

	// The contract itself is byte-identical: not rebuilt at all.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != contractYAML {
		t.Errorf("contract.yaml was rewritten:\ngot  %q\nwant %q (byte-identical)", got, contractYAML)
	}

	// No single-package scaffold pollution.
	if _, err := os.Stat(filepath.Join(dir, "src", "index.ts")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a single-package layout was scaffolded into the repository: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an operator-owned scaffold file was created where none existed: %v", err)
	}

	// The lock now matches the current registry — the whole point.
	lock, err := profiles.ReadLock(filepath.Join(dir, filepath.FromSlash(lockRel)))
	if err != nil {
		t.Fatalf("refreshed lock is invalid: %v", err)
	}
	if lock.Digest != profiles.PackDigest() {
		t.Errorf("lock.Digest = %q, want the current registry digest %q", lock.Digest, profiles.PackDigest())
	}
	for _, pack := range []string{"core", "typescript", "rust"} {
		got, ok := lock.Packs[pack]
		if !ok {
			t.Errorf("refreshed lock is missing pack %q", pack)
			continue
		}
		if want, _ := profiles.PackDigestFor(pack + "@1"); got.Digest != want {
			t.Errorf("lock pack %q digest = %q, want current %q", pack, got.Digest, want)
		}
	}

	// The two kernel-owned files were refreshed, not left stale.
	ci, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ci) == staleCI {
		t.Error("ci.yml was not refreshed")
	}
	pr, err := os.ReadFile(filepath.Join(dir, ".github", "pull_request_template.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(pr) == stalePR {
		t.Error("pull_request_template.md was not refreshed")
	}

	// The real discovered command is reported unchanged.
	if got := manifest.Commands["test"]; got != "cargo test && npm test" {
		t.Errorf("manifest.Commands[test] = %q, want the existing command preserved", got)
	}
}
