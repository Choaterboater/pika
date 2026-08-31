package discover

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func inventoryFromFixture(t *testing.T, name string) *Inventory {
	t.Helper()
	inv, err := Discover(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("Discover(%q): %v", name, err)
	}
	if inv == nil {
		t.Fatalf("Discover(%q) returned nil inventory", name)
	}
	return inv
}

func TestDetectGoModule(t *testing.T) {
	inv := inventoryFromFixture(t, "go-mod")
	if !slices.Contains(inv.DetectedLanguages, "go") {
		t.Fatalf("expected go in %v", inv.DetectedLanguages)
	}
	if len(inv.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(inv.Packages))
	}
	pkg := inv.Packages[0]
	if pkg.Root != "." {
		t.Errorf("expected Root \".\", got %q", pkg.Root)
	}
	if pkg.Name != "example.com/x" {
		t.Errorf("expected module path example.com/x, got %q", pkg.Name)
	}
	if pkg.Kind != "single" {
		t.Errorf("expected kind single, got %q", pkg.Kind)
	}
	if !slices.Contains(inv.DetectedKinds, "single") {
		t.Fatalf("expected single in kinds %v", inv.DetectedKinds)
	}
}

func TestDetectTypeScriptSingle(t *testing.T) {
	inv := inventoryFromFixture(t, "ts-single")
	if !slices.Contains(inv.DetectedLanguages, "typescript") {
		t.Fatalf("expected typescript in %v", inv.DetectedLanguages)
	}
	if len(inv.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(inv.Packages))
	}
	pkg := inv.Packages[0]
	if pkg.Root != "." || pkg.Name != "ts-single" {
		t.Errorf("unexpected package: %+v", pkg)
	}
	if got := inv.ExistingChecks["test"]; got != "npm run test" {
		t.Errorf("ExistingChecks[test] = %q, want %q", got, "npm run test")
	}
	if got := inv.ExistingChecks["lint"]; got != "npm run lint" {
		t.Errorf("ExistingChecks[lint] = %q, want %q", got, "npm run lint")
	}
}

func TestDetectPythonProject(t *testing.T) {
	inv := inventoryFromFixture(t, "py-single")
	if !slices.Contains(inv.DetectedLanguages, "python") {
		t.Fatalf("expected python in %v", inv.DetectedLanguages)
	}
	if len(inv.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(inv.Packages))
	}
	if inv.Packages[0].Name != "py-single" {
		t.Errorf("expected name py-single, got %q", inv.Packages[0].Name)
	}
}

func TestDetectSwiftXcodeProject(t *testing.T) {
	inv := inventoryFromFixture(t, "swift-xcode")
	if !slices.Contains(inv.DetectedLanguages, "swift") {
		t.Fatalf("expected swift in %v", inv.DetectedLanguages)
	}
	if !slices.Contains(inv.DetectedKinds, "xcode") {
		t.Fatalf("expected xcode in kinds %v", inv.DetectedKinds)
	}
	if len(inv.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(inv.Packages))
	}
}

func TestDetectRustCargo(t *testing.T) {
	inv := inventoryFromFixture(t, "rust-cargo")
	if !slices.Contains(inv.DetectedLanguages, "rust") {
		t.Fatalf("expected rust in %v", inv.DetectedLanguages)
	}
	if len(inv.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(inv.Packages))
	}
	if inv.Packages[0].Name != "rust-cargo" {
		t.Errorf("expected name rust-cargo, got %q", inv.Packages[0].Name)
	}
}

func TestMonorepoPnpmWorkspaceSplit(t *testing.T) {
	inv := inventoryFromFixture(t, "monorepo-pnpm")
	if !slices.Contains(inv.DetectedLanguages, "typescript") {
		t.Fatalf("expected typescript in %v", inv.DetectedLanguages)
	}
	if !slices.Contains(inv.DetectedKinds, "workspace") {
		t.Fatalf("expected workspace in kinds %v", inv.DetectedKinds)
	}
	if len(inv.Packages) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(inv.Packages))
	}
	roots := []string{}
	for _, p := range inv.Packages {
		roots = append(roots, p.Root)
	}
	slices.Sort(roots)
	want := []string{"packages/a", "packages/b"}
	if !slices.Equal(roots, want) {
		t.Fatalf("package roots = %v, want %v", roots, want)
	}
	if got := inv.ExistingChecks["test"]; got != "pnpm run test" {
		t.Errorf("ExistingChecks[test] = %q, want %q", got, "pnpm run test")
	}
}

// The same split, for a Cargo workspace: no corpus row and no prior
// test exercised it, the same shape that hid the Justfile/Taskfile
// name defect until a real repository was pointed at it. Confirmed
// working here so a future regression has something to trip.
func TestMonorepoCargoWorkspaceSplit(t *testing.T) {
	inv := inventoryFromFixture(t, "monorepo-cargo")
	if !slices.Contains(inv.DetectedLanguages, "rust") {
		t.Fatalf("expected rust in %v", inv.DetectedLanguages)
	}
	if !slices.Contains(inv.DetectedKinds, "workspace") {
		t.Fatalf("expected workspace in kinds %v", inv.DetectedKinds)
	}
	if len(inv.Packages) != 2 {
		t.Fatalf("expected 2 packages, got %d: %+v", len(inv.Packages), inv.Packages)
	}
	roots := []string{}
	names := map[string]string{}
	for _, p := range inv.Packages {
		roots = append(roots, p.Root)
		names[p.Root] = p.Name
		if p.Language != "rust" {
			t.Errorf("package %+v language = %q, want rust", p, p.Language)
		}
	}
	slices.Sort(roots)
	want := []string{"crates/a", "crates/b"}
	if !slices.Equal(roots, want) {
		t.Fatalf("package roots = %v, want %v", roots, want)
	}
	if names["crates/a"] != "pkg-a" || names["crates/b"] != "pkg-b" {
		t.Errorf("package names = %v, want crates/a=pkg-a, crates/b=pkg-b", names)
	}
}

func TestSingleProjectEmitsExactlyOnePackage(t *testing.T) {
	for _, name := range []string{"ts-single", "py-single", "swift-xcode", "rust-cargo", "go-mod"} {
		inv := inventoryFromFixture(t, name)
		if len(inv.Packages) != 1 {
			t.Errorf("%s: expected 1 package, got %d", name, len(inv.Packages))
			continue
		}
		if inv.Packages[0].Root != "." {
			t.Errorf("%s: expected Root \".\", got %q", name, inv.Packages[0].Root)
		}
	}
}

func TestGitAndWorkflows(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	wf := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ci.yml", "release.yaml", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(wf, name), []byte("on: push\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/tmp\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inv, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if !inv.HasGit {
		t.Error("expected HasGit true")
	}
	want := []string{"ci.yml", "release.yaml"}
	if !slices.Equal(inv.GitHubWorkflows, want) {
		t.Errorf("GitHubWorkflows = %v, want %v", inv.GitHubWorkflows, want)
	}
}

func TestSkipsIgnoredDirsAndDepthLimit(t *testing.T) {
	root := t.TempDir()
	// Markers that must be ignored: inside node_modules, beyond depth 3.
	paths := map[string]string{
		"node_modules/pkg/package.json": `{"name":"noise"}`,
		"a/b/c/go.mod":                  "module example.com/deep\n",
		"a/b/go.mod":                    "module example.com/shallow\n",
	}
	for rel, content := range paths {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(inv.DetectedLanguages, "typescript") {
		t.Error("node_modules package.json must be ignored")
	}
	if len(inv.Packages) != 1 || inv.Packages[0].Name != "example.com/shallow" {
		t.Fatalf("expected only shallow go module, got %+v", inv.Packages)
	}
}

func TestParseTOMLValues(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"myproj" # the name`, "myproj"},
		{`"weird#name"`, "weird#name"},
		{`"esc\"aped"`, `esc"aped`},
		{`"plain"`, "plain"},
		{`bare # comment`, "bare"},
		{`plain`, "plain"},
	}
	for _, tc := range cases {
		if got := parseTOMLValue(tc.in); got != tc.want {
			t.Errorf("parseTOMLValue(%q) = %v, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCargoTOMLQuotedNameWithComment(t *testing.T) {
	root := t.TempDir()
	cargo := "[package]\nname = \"myproj\" # the name\nversion = \"0.1.0\"\n[workspace]\nmembers = [\"crates/a\", \"crates/b\"] # all of them\n"
	if err := os.WriteFile(filepath.Join(root, "Cargo.toml"), []byte(cargo), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := readCargoTOML(filepath.Join(root, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Package.Name != "myproj" {
		t.Errorf("Package.Name = %q, want myproj", c.Package.Name)
	}
	want := []string{"crates/a", "crates/b"}
	if !slices.Equal(c.Workspace.Members, want) {
		t.Errorf("members = %v, want %v", c.Workspace.Members, want)
	}
}

// A Justfile recipe named "format" (not "fmt") must be discovered as
// `just format`, not `just fmt`. canonicalVerb accepts both spellings as
// meaning the same check, but the constructed command must invoke the
// recipe that actually exists — `just fmt` on a Justfile with no recipe
// named `fmt` fails with "justfile does not contain recipe `fmt`",
// which is a discovery defect wearing a format-gate failure, not a
// real finding about the repository's code.
func TestJustfileRecipeNamedFormatIsInvokedByItsRealName(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/x\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "justfile"), []byte("format:\n    gofmt -l .\n\ntest:\n    go test ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inv, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := inv.ExistingChecks["fmt"]; got != "just format" {
		t.Errorf("ExistingChecks[fmt] = %q, want %q: the recipe is named `format`, not `fmt`", got, "just format")
	}
}

// A Justfile recipe that already IS the canonical spelling still works:
// the fix must not stop matching a recipe literally named `fmt`.
func TestJustfileRecipeNamedFmtStillMatches(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/x\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "justfile"), []byte("fmt:\n    gofmt -l .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inv, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := inv.ExistingChecks["fmt"]; got != "just fmt" {
		t.Errorf("ExistingChecks[fmt] = %q, want %q", got, "just fmt")
	}
}

// The same defect, same fix, for Taskfile.yml: a task named "format"
// must be invoked as `task format`, not `task fmt` — `task fmt` on a
// Taskfile with no task named `fmt` fails with `Task "fmt" does not
// exist`.
func TestTaskfileTaskNamedFormatIsInvokedByItsRealName(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/x\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	taskfile := "version: '3'\ntasks:\n  format:\n    cmds:\n      - gofmt -l .\n  test:\n    cmds:\n      - go test ./...\n"
	if err := os.WriteFile(filepath.Join(root, "Taskfile.yml"), []byte(taskfile), 0o644); err != nil {
		t.Fatal(err)
	}
	inv, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := inv.ExistingChecks["fmt"]; got != "task format" {
		t.Errorf("ExistingChecks[fmt] = %q, want %q: the task is named `format`, not `fmt`", got, "task format")
	}
}

// A known, narrower limitation, not a defect this package fixes:
// goreleaser/goreleaser's own Taskfile.yml indents one task's `cmds:`
// key level with the task name instead of nested under it — tolerated
// by go-task's own parser, rejected by goccy/go-yaml (strict) with
// "sequence was used where mapping is expected". The whole file
// discovers nothing, including clean tasks nowhere near the bad
// indentation, but the result is an honest "no command discovered"
// rather than a false failure — pinned here so a change to this
// behavior is a decision, not a surprise.
func TestTaskfileWithInconsistentIndentationDiscoversNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/x\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	taskfile := "version: '3'\ntasks:\n  fmt:\n    cmds:\n      - gofmt -l .\n\n  fuzz:\n    desc: bad\n  cmds:\n    - echo hi\n"
	if err := os.WriteFile(filepath.Join(root, "Taskfile.yml"), []byte(taskfile), 0o644); err != nil {
		t.Fatal(err)
	}
	inv, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := inv.ExistingChecks["fmt"]; ok {
		t.Errorf("ExistingChecks[fmt] = %q, want absent: the file fails to parse, so nothing is discovered", got)
	}
}
