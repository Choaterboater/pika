// Package initcmd implements `pika init`: the lean scaffold for a
// new pika-managed repository (spec §6).
//
// init creates the .project/ state (contract, profiles lock, exceptions
// record), the documentation spine, the core repository files (README,
// AGENTS, CONTRIBUTING, GitHub CI and PR template), and — only for the
// selected language — the stack-owned layout (spec §6.1). It never
// deletes user files: --force rewrites the managed files in place, and
// that is all. The core pack's docs templates are served by
// internal/profiles from the pack itself; the language stack templates
// are embedded at build time under templates/.
package initcmd

import (
	"embed"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/profiles"

	"github.com/goccy/go-yaml"
)

//go:embed templates
var templatesFS embed.FS

// InitOptions configures Run.
type InitOptions struct {
	// Dir is the scaffold target directory (created if missing; default
	// "."). The project name defaults to the directory base name,
	// kebab-cased.
	Dir string
	// Name overrides the contract project name (--name).
	Name string
	// Module overrides the generated Go module path (--module); it
	// defaults to the project name.
	Module string
	// Profiles lists the language profiles to scaffold, as language ids
	// ("go") or pack references ("go@1"). Core is always included first;
	// composition rules (at most one language pack) are enforced by
	// profiles.Resolve.
	Profiles []string
	// Force rewrites the contract, lock, and managed files even when a
	// contract already exists. User files outside .project are never
	// deleted.
	Force bool
	// JSON emits the created-file manifest (sorted paths) on Out.
	JSON bool
	// Out receives the JSON manifest (default os.Stdout).
	Out io.Writer
}

// genFile is one file init writes, with its slash-separated path relative
// to the scaffold root.
type genFile struct {
	path string
	data []byte
}

// tmplData is the data every scaffold template renders with. PyName,
// RustName, and SwiftName are the language-level identifiers derived from
// the project name; each is guarded to start with a letter because cargo
// package names, Python module names, and Swift identifiers cannot begin
// with a digit even though the contract name pattern allows one.
type tmplData struct {
	Name      string
	Module    string
	RustName  string
	PyName    string
	SwiftName string
	CIPaths   string
	CISteps   string
}

// docsSpine lists the core pack's documentation spine (spec §6.2). Spec §6
// prohibits empty placeholder directories, so each directory carries a
// .gitkeep to stay present in git.
var docsSpine = []string{"architecture", "decisions", "guides", "reference", "work"}

// contractRel is the contract file's location relative to the scaffold
// root; its existence is init's idempotency marker.
const contractRel = ".project/contract.yaml"

// lockRel is the profiles lock's location relative to the scaffold root.
const lockRel = ".project/profiles.lock"

// Run scaffolds a pika-managed repository into opts.Dir. It fails
// without writing anything when a contract already exists and --force is
// not set.
func Run(opts InitOptions) error {
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}

	selection, err := selection(opts.Profiles)
	if err != nil {
		return err
	}
	resolved, err := profiles.Resolve(selection)
	if err != nil {
		return fmt.Errorf("pika init: %w", err)
	}

	contractPath := filepath.Join(dir, filepath.FromSlash(contractRel))
	if !opts.Force {
		if _, err := os.Stat(contractPath); err == nil {
			return fmt.Errorf("pika init: %s already exists; pass --force to regenerate", contractRel)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("pika init: %s: %w", contractRel, err)
		}
	}

	name := projectName(opts.Name, dir)
	lang := LanguageName(selection)
	module := opts.Module
	if module == "" {
		module = name
	}

	commands := commandsFromChecks(resolved.Checks, lookPath)
	contractYAML, err := buildContract(name, selection, commands)
	if err != nil {
		return err
	}
	files, err := buildFiles(lang, name, module, contractYAML)
	if err != nil {
		return err
	}

	for _, f := range files {
		if err := writeFile(dir, f.path, f.data); err != nil {
			return err
		}
	}
	// The lock is written through profiles.WriteLock so lock and contract
	// pin the same packs and digests.
	if err := profiles.WriteLock(filepath.Join(dir, filepath.FromSlash(lockRel)), selection); err != nil {
		return fmt.Errorf("pika init: %w", err)
	}
	files = append(files, genFile{path: lockRel})

	if opts.JSON {
		if err := writeManifest(files, commands, opts.Out); err != nil {
			return err
		}
	}
	return nil
}

// selection maps the requested profiles to pack references, core first.
// Language ids ("go") map through profiles.LanguagePack; pack references
// ("go@1") pass through unchanged.
func selection(requested []string) ([]string, error) {
	sel := []string{profiles.CoreRef}
	for _, p := range requested {
		ref := p
		if !strings.Contains(p, "@") {
			mapped, ok := profiles.LanguagePack(p)
			if !ok {
				return nil, fmt.Errorf("pika init: unknown profile %q (supported languages: go, typescript, python, swift, rust)", p)
			}
			ref = mapped
		}
		if !slices.Contains(sel, ref) {
			sel = append(sel, ref)
		}
	}
	return sel, nil
}

// LanguageName returns the composed language layer's id ("go"), or ""
// for a core-only selection. Language ids ("go") and pack references
// ("go@1") both count; core itself never names the language layer.
func LanguageName(selection []string) string {
	for _, ref := range selection {
		if name, _, ok := strings.Cut(ref, "@"); ok && name != "core" {
			return name
		}
	}
	return ""
}

// projectName derives the contract project name: opts.Name when set, else
// the scaffold directory's base name. Both are kebab-cased to satisfy the
// contract schema pattern ^[a-z0-9][a-z0-9-]*$.
func projectName(name, dir string) string {
	if name == "" {
		name = filepath.Base(absDir(dir))
	}
	return kebab(name)
}

// absDir resolves dir against the working directory so its base name is
// meaningful for "." and relative paths.
func absDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// kebab lowercases s and replaces every run of non-[a-z0-9] with a single
// dash, trimming leading and trailing dashes. An empty result falls back
// to "project" so the name always matches the schema pattern.
func kebab(s string) string {
	var b strings.Builder
	dash := true // suppress leading and trailing dashes
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		default:
			if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	if out := strings.Trim(b.String(), "-"); out != "" {
		return out
	}
	return "project"
}

// camel converts a kebab-case name to a PascalCase Swift identifier.
func camel(s string) string {
	var b strings.Builder
	upper := true
	for _, r := range s {
		if r == '-' || r == '_' {
			upper = true
			continue
		}
		if upper {
			b.WriteRune(byteRuneToUpper(r))
			upper = false
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func byteRuneToUpper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 'a' + 'A'
	}
	return r
}

// digitSafe prefixes prefix when s starts with a digit: the contract
// project-name pattern ^[a-z0-9][a-z0-9-]*$ admits a leading digit, but
// the language-level identifiers derived from it (cargo package name,
// Python module, Swift type) do not.
func digitSafe(s, prefix string) string {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return prefix + s
	}
	return s
}

// buildContract composes the initial contract: schema 1, single topology,
// one root package carrying the selection, the commands resolved from the
// selected packs' check slots, and the M1 defaults for GitHub merge and
// evidence policy.
func buildContract(name string, selection []string, commands map[string]string) ([]byte, error) {
	c := &contract.Contract{
		Schema:   1,
		Project:  contract.Project{Name: name, Topology: "single"},
		Profiles: selection,
		Packages: map[string]contract.Package{
			name: {Root: ".", Profiles: slices.Clone(selection)},
		},
		Commands:   commands,
		GitHub:     contract.GitHub{Merge: "squash"},
		Evidence:   contract.Evidence{Publish: "sanitized"},
		Extensions: map[string]any{},
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("pika init: encode contract: %w", err)
	}
	return data, nil
}

// lookPath resolves a tool name against PATH. It is a package variable
// so tests can pin it: the contract's commands block depends on what is
// installed, and the golden trees compare contract.yaml byte for byte.
var lookPath = exec.LookPath

// commandsFromChecks fills contract.commands from pack hints for slots
// that are discovery sentinels whose pack marks the hint autofillable and
// whose suggested tool is actually present. Without this a fresh
// repository can pass `pika check` with every gate skipped — green while
// verifying nothing.
//
// Autofill is load-bearing, not belt-and-braces. A tool on PATH proves
// the binary exists, never that the command works: `npm` is installed on
// most developer machines but `npm run lint` needs a script the scaffold
// does not define, so populating it hands the user a repository that
// fails its own checks the moment it is created. Only a pack that has
// verified its hint against a fresh scaffold sets autofill; every other
// hint stays advice for doctor to render and a human to adopt.
//
// lookPath is injected so golden tests stay deterministic regardless of
// what the authoring machine has installed.
func commandsFromChecks(cs profiles.CheckSet, lookPath func(string) (string, error)) map[string]string {
	slots := []struct {
		id    string
		check profiles.Check
	}{
		{"format", cs.Format},
		{"lint", cs.Lint},
		{"typecheck", cs.Typecheck},
		{"test", cs.Test},
		{"smoke", cs.Smoke},
	}
	out := map[string]string{}
	for _, s := range slots {
		// An explicit pack command already runs; duplicating it into the
		// contract would just create a second place to keep in sync.
		if len(s.check.Cmd) > 0 || !s.check.Discovery {
			continue
		}
		if !s.check.Autofill || len(s.check.Hint) == 0 {
			continue
		}
		if _, err := lookPath(s.check.Hint[0]); err != nil {
			continue
		}
		out[s.id] = strings.Join(s.check.Hint, " ")
	}
	return out
}

// CommandsFromChecks is commandsFromChecks resolved against the real
// PATH. `pika apply` applies the same policy when it promotes a draft
// contract, so both authoring paths share one implementation.
func CommandsFromChecks(cs profiles.CheckSet) map[string]string {
	return commandsFromChecks(cs, lookPath)
}

// buildFiles assembles every file init writes except the profiles lock
// (which goes through profiles.WriteLock). The list is built completely
// before the first write so a template or encoding failure never leaves a
// half scaffold behind.
func buildFiles(lang, name, module string, contractYAML []byte) ([]genFile, error) {
	data := tmplData{
		Name:      name,
		Module:    module,
		RustName:  digitSafe(name, "p"),
		PyName:    digitSafe(strings.ReplaceAll(name, "-", "_"), "p"),
		SwiftName: digitSafe(camel(name), "P"),
		CIPaths:   ciPaths(lang),
		CISteps:   ciSteps(lang),
	}

	files := []genFile{
		{path: contractRel, data: contractYAML},
		{path: ".project/exceptions.yaml", data: []byte("{}\n")},
		{path: ".gitignore", data: []byte(gitignore(lang))},
	}
	for _, d := range docsSpine {
		files = append(files, genFile{path: path.Join("docs", d, ".gitkeep")})
	}
	core, err := CoreFiles(lang, name)
	if err != nil {
		return nil, err
	}
	for _, target := range coreTemplateTargets {
		files = append(files, genFile{path: target.path, data: core[target.path]})
	}
	files = append(files, stackFiles(lang, data)...)

	slices.SortFunc(files, func(a, b genFile) int { return strings.Compare(a.path, b.path) })
	return files, nil
}

// gitignore returns the .gitignore init scaffolds: .project/state/ always
// (spec §14.2 — envelopes, boards, and recovery journals hold unredacted
// runtime records and must never be committed) plus the standard ignores
// of the selected stack's build artifacts.
func gitignore(lang string) string {
	lines := []string{".project/state/"}
	switch lang {
	case "typescript":
		lines = append(lines, "node_modules/")
	case "python":
		lines = append(lines, "__pycache__/", ".venv/")
	case "rust":
		lines = append(lines, "target/")
	case "swift":
		lines = append(lines, ".build/", ".swiftpm/")
	}
	return strings.Join(lines, "\n") + "\n"
}

// stackFiles returns the stack-owned layout for the selected language
// (spec §6.1): minimal but genuinely runnable entry files. A core-only
// scaffold gets none.
func stackFiles(lang string, data tmplData) []genFile {
	switch lang {
	case "go":
		return []genFile{
			{path: "go.mod", data: render1("go.mod.tmpl", data)},
			{path: path.Join("cmd", data.Name, "main.go"), data: render1("go-main.go.tmpl", data)},
		}
	case "swift":
		// The module identifiers stay PascalCase (Swift imports them),
		// but the source tree they live in is kebab-case and Package.swift
		// points the targets at it, so the scaffold passes its own
		// kebab-case naming rule.
		return []genFile{
			{path: "Package.swift", data: render1("Package.swift.tmpl", data)},
			{path: path.Join("Sources", data.Name, "main.swift"), data: render1("swift-main.swift.tmpl", data)},
			{path: path.Join("Tests", data.Name+"-tests", data.Name+"-tests.swift"), data: render1("swift-test.swift.tmpl", data)},
		}
	case "typescript":
		return []genFile{
			{path: "package.json", data: render1("package.json.tmpl", data)},
			{path: "src/index.ts", data: render1("index.ts.tmpl", data)},
		}
	case "python":
		return []genFile{
			{path: "pyproject.toml", data: render1("pyproject.toml.tmpl", data)},
			{path: path.Join("src", data.PyName, "__init__.py"), data: render1("py-init.py.tmpl", data)},
			{path: path.Join("tests", "test_init.py"), data: render1("py-test.py.tmpl", data)},
		}
	case "rust":
		return []genFile{
			{path: "Cargo.toml", data: render1("Cargo.toml.tmpl", data)},
			{path: "src/main.rs", data: render1("rust-main.rs.tmpl", data)},
		}
	default:
		return nil
	}
}

// coreTarget is one core-pack scaffold template and the
// repository-relative path it renders to. One table serves both `pika
// init` (which renders everything) and `pika apply` (which renders only
// the files a repository is missing), so both commands produce
// byte-identical core files from one rendering implementation.
type coreTarget struct {
	tmpl string
	path string
}

var coreTemplateTargets = []coreTarget{
	{"README.md.tmpl", "README.md"},
	{"AGENTS.md.tmpl", "AGENTS.md"},
	{"CONTRIBUTING.md.tmpl", "CONTRIBUTING.md"},
	{"pull_request_template.md.tmpl", ".github/pull_request_template.md"},
	{"ci.yml.tmpl", ".github/workflows/ci.yml"},
}

// CoreFiles renders the core pack's repository files — README, AGENTS,
// CONTRIBUTING, the GitHub PR template, and the CI workflow — for a
// project name and language id, keyed by repository-relative slash
// path. A template missing from the pack is a hard error, so callers
// fail before writing anything.
func CoreFiles(lang, name string) (map[string][]byte, error) {
	data := tmplData{Name: name, CIPaths: ciPaths(lang), CISteps: ciSteps(lang)}
	out := make(map[string][]byte, len(coreTemplateTargets))
	for _, target := range coreTemplateTargets {
		rendered, err := renderCore(target.tmpl, data)
		if err != nil {
			return nil, err
		}
		out[target.path] = rendered
	}
	return out, nil
}

// renderCore renders one core-pack docs template fetched through
// profiles.Template. A template missing from the pack is a hard error
// carrying the name, so init fails before writing anything.
func renderCore(name string, data tmplData) ([]byte, error) {
	src, err := profiles.Template(name)
	if err != nil {
		return nil, err
	}
	t, err := template.New(name).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("pika init: parse template %s: %w", name, err)
	}
	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("pika init: render %s: %w", name, err)
	}
	return []byte(buf.String()), nil
}

// render executes one embedded stack template by file name.
func render(name string, data tmplData) ([]byte, error) {
	t, err := template.ParseFS(templatesFS, path.Join("templates", name))
	if err != nil {
		return nil, fmt.Errorf("pika init: parse template %s: %w", name, err)
	}
	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("pika init: render %s: %w", name, err)
	}
	return []byte(buf.String()), nil
}

// render1 is render for templates that cannot fail: their parse errors
// surface in tests, and their data carries no dynamic structure.
func render1(name string, data tmplData) []byte {
	out, err := render(name, data)
	if err != nil {
		panic(err) // impossible for embedded templates; guarded by tests
	}
	return out
}

// ciPaths renders the CI trigger path list: the core-managed paths plus
// the selected stack's source paths.
func ciPaths(lang string) string {
	paths := []string{
		"'.project/**'",
		"'docs/**'",
		"'AGENTS.md'",
		"'README.md'",
		"'CONTRIBUTING.md'",
		"'.github/workflows/ci.yml'",
	}
	switch lang {
	case "go":
		paths = append(paths, "'cmd/**'", "'go.mod'")
	case "typescript":
		paths = append(paths, "'src/**'", "'package.json'")
	case "python":
		paths = append(paths, "'src/**'", "'tests/**'", "'pyproject.toml'")
	case "swift":
		paths = append(paths, "'Sources/**'", "'Package.swift'")
	case "rust":
		paths = append(paths, "'src/**'", "'Cargo.toml'")
	}
	var lines []string
	for _, p := range paths {
		lines = append(lines, "      - "+p)
	}
	return strings.Join(lines, "\n")
}

// ciSteps renders the toolchain setup steps preceding the pika
// steps. Go is always set up because pika itself installs through
// `go install`; the language step makes the stack's check gates runnable.
// Rust and Swift toolchains are preinstalled on GitHub-hosted runners,
// and rustup's stable profile carries rustfmt and clippy, so the rust
// format and lint commands need no extra install.
func ciSteps(lang string) string {
	steps := "      - uses: actions/setup-go@v5\n" +
		"        with:\n" +
		"          go-version: \"1.26\"\n"
	switch lang {
	case "typescript":
		steps += "      - uses: actions/setup-node@v4\n" +
			"        with:\n" +
			"          node-version: \"24\"\n"
	case "python":
		// A runner ships a Python interpreter and nothing else: every
		// tool python@1 can put in the contract — pytest for the test
		// slot, ruff for format and lint, mypy for typecheck — has to be
		// installed here or the gate that names it fails in CI while
		// passing on the author's machine.
		steps += "      - uses: actions/setup-python@v5\n" +
			"        with:\n" +
			"          python-version: \"3.13\"\n" +
			"      - name: Install check tooling\n" +
			"        run: python -m pip install pytest ruff mypy\n"
	case "rust":
		steps += "      - name: Pin stable Rust\n" +
			"        run: rustup default stable\n"
	}
	return steps
}

// writeFile writes one file under root, creating parent directories.
func writeFile(root, rel string, data []byte) error {
	target := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("pika init: create %s: %w", rel, err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return fmt.Errorf("pika init: write %s: %w", rel, err)
	}
	return nil
}

// writeManifest emits the created-file manifest as pretty-printed JSON:
// every file init wrote, sorted by path, plus every contract command
// slot init populated so the caller can see which gates will actually
// run.
func writeManifest(files []genFile, commands map[string]string, out io.Writer) error {
	if out == nil {
		out = os.Stdout
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(struct {
		Files    []string          `json:"files"`
		Commands map[string]string `json:"commands"`
	}{Files: filePaths(files), Commands: commands}); err != nil {
		return fmt.Errorf("pika init: encode manifest: %w", err)
	}
	return nil
}

// filePaths returns the manifest paths, sorted.
func filePaths(files []genFile) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.path)
	}
	slices.Sort(paths)
	return paths
}
