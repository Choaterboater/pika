// Package initcmd implements `pika init`: the lean scaffold for a
// new pika-managed repository (spec §6).
//
// init creates the .project/ state (contract, profiles lock, exceptions
// record), the documentation spine, the core repository files (README,
// AGENTS, CONTRIBUTING, GitHub CI and PR template), and — only for the
// selected language — the stack-owned layout (spec §6.1). It never
// deletes user files, and under --force it does not rewrite them either.
// The scaffold is split by ownership: the contract, the profiles lock,
// the GitHub PR template and the CI workflow are the kernel's, and a
// regeneration restores them; the README, AGENTS.md, CONTRIBUTING.md,
// the language scaffold and the rest are the operator's the moment they
// exist, and are seeded once. --reset-docs puts the scaffold's own text
// back on request. What --force regenerates from is the repository
// rather than the command line — profiles, project name and Go module
// path are read back from the existing contract and go.mod whenever the
// matching flag is absent — and the exceptions record, whose entries
// carry rationales a human wrote and a reviewer accepted, is seeded once
// and never rewritten, not even by --reset-docs.
// The core pack's docs templates are served by internal/profiles from
// the pack itself; the language stack templates are embedded at build
// time under templates/.
package initcmd

import (
	"embed"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/Choaterboater/pika/internal/checks"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/discover"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/skills"
	"github.com/Choaterboater/pika/internal/version"

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
	// Name overrides the contract project name (--name). Under --force
	// it is read back from the existing contract's project.name.
	Name string
	// Module overrides the generated Go module path (--module). A fresh
	// scaffold derives it from the project name; under --force it is read
	// back from the repository's go.mod, and a Go scaffold whose module
	// can be recovered from neither is refused rather than renamed after
	// its directory.
	Module string
	// Profiles lists the language profiles to scaffold, as language ids
	// ("go") or pack references ("go@1"). Core is always included first;
	// composition rules (at most one language pack) are enforced by
	// profiles.Resolve. Under --force an empty list is read back from the
	// existing contract's selection.
	Profiles []string
	// Force regenerates the kernel-owned files — the contract, the
	// profiles lock, the GitHub PR template and the CI workflow — even
	// when a contract already exists. Operator-owned files (README,
	// AGENTS.md, CONTRIBUTING.md, the language scaffold) are written
	// only where they are missing, so a repository that has been lived
	// in keeps its own prose and its own entry point. User files
	// outside .project are never deleted.
	Force bool
	// ResetDocs restores the scaffold's own text over the operator-owned
	// files, which is what --force used to do unasked. It is reachable
	// on request and never by default. It does not reach the exceptions
	// record: an exception carries a rationale, an owner and a review
	// condition a human wrote and a reviewer accepted, and regenerating
	// docs is not a reason to discard evidence.
	ResetDocs bool
}

// Manifest is what init created: every file it wrote, sorted by path,
// plus every contract command slot it populated so the caller can see
// which gates will actually run. Run returns it; encoding it is the
// command layer's business, not this package's.
type Manifest struct {
	Files    []string          `json:"files"`
	Commands map[string]string `json:"commands"`
}

// genFile is one file init writes, with its slash-separated path relative
// to the scaffold root.
//
// kernel marks the files whose content the kernel alone determines — the
// contract, the profiles lock, the GitHub PR template and the CI workflow
// — and which a regeneration therefore rewrites unconditionally. Every
// other scaffolded file is the operator's the moment it exists: it is
// seeded once and, absent --reset-docs, never overwritten.
type genFile struct {
	path   string
	data   []byte
	kernel bool
}

// tmplData is the data every scaffold template renders with. PyName,
// RustName, and SwiftName are the language-level identifiers derived from
// the project name; each is guarded to start with a letter because cargo
// package names, Python module names, and Swift identifiers cannot begin
// with a digit even though the contract name pattern allows one.
// PikaRef is the kernel pin the scaffolded CI workflow installs.
type tmplData struct {
	Name      string
	Module    string
	RustName  string
	PyName    string
	SwiftName string
	PikaRef   string
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

// Run scaffolds a pika-managed repository into opts.Dir and returns the
// manifest of what it created. It fails without writing anything when a
// contract already exists and --force is not set.
//
// Under --force every input is resolved as explicit flag, else read-back
// from the repository, else refusal. --force is the documented remedy
// for a rotated profile digest, so it runs against repositories that
// already declare a selection, a name and a module; taking those from
// the command line alone turned a bare --force into a core-only contract
// with no gates and a go.mod renamed after its directory.
//
// The returned manifest lists the files Run actually wrote, so a
// regeneration that preserved an operator's README does not claim to
// have created it.
func Run(opts InitOptions) (*Manifest, error) {
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}

	contractPath := filepath.Join(dir, filepath.FromSlash(contractRel))
	existing, err := existingContract(contractPath, opts.Force)
	if err != nil {
		return nil, err
	}

	requested, wantName, module := opts.Profiles, opts.Name, opts.Module
	if existing != nil {
		if len(requested) == 0 {
			// existing.Profiles, not checks.ProfileRefs(existing): the
			// latter unions in every package's own Profiles, which can
			// name more packs than profiles.Resolve ever composes for
			// a repository whose packages span more than one
			// language. existing.Profiles is the contract's own
			// repository-level selection — the same field `pika
			// apply` itself now resolves for this exact purpose.
			requested = existing.Profiles
		}
		if wantName == "" {
			wantName = existing.Project.Name
		}
		if module == "" {
			module = goModulePath(dir)
		}
	}

	selection, err := selection(requested)
	if err != nil {
		return nil, err
	}
	resolved, err := profiles.Resolve(selection)
	if err != nil {
		return nil, fmt.Errorf("pika init: %w", err)
	}

	name := projectName(wantName, dir)
	lang := LanguageName(selection)
	if module == "" {
		// The contract records no module, so a regeneration that cannot
		// read one out of go.mod has nothing left but the directory name
		// — which renames the module nothing imports it by and scaffolds
		// a second cmd/<dirname>/main.go beside the real one. Refuse
		// instead. A fresh scaffold has no module to lose and keeps
		// deriving one from the project name.
		if existing != nil && lang == "go" {
			return nil, fmt.Errorf("pika init: --force cannot recover the go module path: the contract records none and %s has no go.mod; pass --module", dir)
		}
		module = name
	}

	commands := commandsFromChecks(resolved.Checks, lookPath)
	contractYAML, err := buildContract(name, selection, commands)
	if err != nil {
		return nil, err
	}
	files, err := buildFiles(lang, name, module, contractYAML, !present(dir, checks.ExceptionsFile))
	if err != nil {
		return nil, err
	}

	written := make([]genFile, 0, len(files)+1)
	for _, f := range files {
		// Operator-owned files are scaffolding, not managed state: the
		// moment one exists, the repository's copy is the authority.
		// --force regenerates what the kernel owns and writes the rest
		// only where it is missing, so a first-time init still lays down
		// the whole tree while a regeneration leaves an operator's prose
		// and entry point alone. --reset-docs asks for the scaffold's
		// text back, and is the only way to get it.
		if !f.kernel && !opts.ResetDocs && present(dir, f.path) {
			continue
		}
		if err := writeFile(dir, f.path, f.data); err != nil {
			return nil, err
		}
		written = append(written, f)
	}
	// The lock is written through profiles.WriteLock so lock and contract
	// pin the same packs and digests.
	if err := profiles.WriteLock(filepath.Join(dir, filepath.FromSlash(lockRel)), selection); err != nil {
		return nil, fmt.Errorf("pika init: %w", err)
	}
	written = append(written, genFile{path: lockRel, kernel: true})

	// The four canonical skills and the projections declared for them
	// go through skills.Install rather than the genFile loop above: a
	// skill is operator-owned exactly like README (create-if-missing,
	// --reset-docs restores it — resetDocs is that same flag, not a
	// second meaning for --force), but the projection it feeds is a
	// region spliced into a file that may already carry an operator's
	// own prose above it, which a plain create-if-missing write cannot
	// express. skills.Install is the one implementation of both halves.
	root, err := repopath.At(dir)
	if err != nil {
		return nil, fmt.Errorf("pika init: %w", err)
	}
	loaded, err := contract.Load(contractPath)
	if err != nil {
		return nil, fmt.Errorf("pika init: reload written contract: %w", err)
	}
	st, err := skills.Install(root, loaded, resolved, opts.ResetDocs)
	if err != nil {
		return nil, fmt.Errorf("pika init: %w", err)
	}
	for _, s := range st.Skills {
		if s.Written {
			written = append(written, genFile{path: s.Path})
		}
	}
	for _, p := range st.Projections {
		if p.Written {
			written = append(written, genFile{path: p.Path})
		}
	}

	return &Manifest{Files: filePaths(written), Commands: commands}, nil
}

// existingContract returns the contract already in place at path, or nil
// when there is none. Without --force an existing contract is init's
// idempotency refusal; with it, that contract is what the regeneration
// reads back from.
//
// A contract that exists but does not parse refuses either way. A corrupt
// contract is a fact to report: rebuilding it from whatever flags the
// invocation happened to carry is how an operator loses a contract they
// could have repaired.
func existingContract(path string, force bool) (*contract.Contract, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("pika init: %s: %w", contractRel, err)
	}
	if !force {
		return nil, fmt.Errorf("pika init: %s already exists; pass --force to regenerate", contractRel)
	}
	c, err := contract.Load(path)
	if err != nil {
		return nil, fmt.Errorf("pika init: %s exists but does not parse; repair or delete it rather than regenerating over it: %w", contractRel, err)
	}
	return c, nil
}

// goModulePath recovers the repository's root Go module path from its
// go.mod. The contract carries no module field, so this is the only place
// a regeneration can learn the module the repository already declares.
// discover owns go.mod parsing; asking it keeps one reader rather than
// two that can disagree. An empty result — no go.mod, or one with no
// module line — is a refusal at the call site, never a fallback.
func goModulePath(dir string) string {
	inv, err := discover.Discover(dir)
	if err != nil {
		return ""
	}
	for _, p := range inv.Packages {
		if p.Language == "go" && p.Root == "." && p.Name != "" {
			return p.Name
		}
	}
	return ""
}

// present reports whether the scaffold-relative path rel already exists
// under dir. It is what decides whether a file init would seed is already
// the repository's own: the exceptions record, whose entries are
// rationales, owners and review conditions a human wrote and a reviewer
// accepted, and every operator-owned scaffold file.
//
// A stat that fails for any reason other than absence counts as present.
// The safe answer is always to leave the file alone: an unreadable file
// is still somebody's.
func present(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	return !errors.Is(err, fs.ErrNotExist)
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
		Commands: commands,
		GitHub:   contract.GitHub{Merge: "squash"},
		Evidence: contract.Evidence{Publish: "sanitized"},
		// codex is the one harness whose file init already scaffolds by
		// default (AGENTS.md). A harness with no default file gets no
		// default projection; declaring one for a file init never
		// writes would fail gate 1 on every fresh scaffold.
		Skills:     &contract.Skills{Projections: []contract.Projection{{Harness: "codex", Path: "AGENTS.md"}}},
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
// half scaffold behind. seedExceptions asks for an empty exceptions
// record; it is false whenever the repository already has one, which
// --force must leave untouched.
func buildFiles(lang, name, module string, contractYAML []byte, seedExceptions bool) ([]genFile, error) {
	data := tmplData{
		Name:      name,
		Module:    module,
		RustName:  digitSafe(name, "p"),
		PyName:    digitSafe(strings.ReplaceAll(name, "-", "_"), "p"),
		SwiftName: digitSafe(camel(name), "P"),
		PikaRef:   pikaRef(),
		CISteps:   ciSteps(lang),
	}

	files := []genFile{
		{path: contractRel, data: contractYAML, kernel: true},
		{path: ".gitignore", data: []byte(gitignore(lang))},
	}
	if seedExceptions {
		files = append(files, genFile{path: checks.ExceptionsFile, data: []byte("{}\n")})
	}
	for _, d := range docsSpine {
		files = append(files, genFile{path: path.Join("docs", d, ".gitkeep")})
	}
	core, err := CoreFiles(lang, name)
	if err != nil {
		return nil, err
	}
	for _, target := range coreTemplateTargets {
		files = append(files, genFile{path: target.path, data: core[target.path], kernel: target.kernel})
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
//
// kernel splits the table by ownership. The PR template and the CI
// workflow encode how the kernel wants to be run, so a regeneration
// restores them; README, AGENTS.md and CONTRIBUTING.md are a starting
// point the repository is expected to outgrow, and overwriting them is
// how `pika init --force` used to destroy an operator's own words.
type coreTarget struct {
	tmpl   string
	path   string
	kernel bool
}

var coreTemplateTargets = []coreTarget{
	{tmpl: "README.md.tmpl", path: "README.md"},
	{tmpl: "AGENTS.md.tmpl", path: "AGENTS.md"},
	{tmpl: "CONTRIBUTING.md.tmpl", path: "CONTRIBUTING.md"},
	{tmpl: "pull_request_template.md.tmpl", path: ".github/pull_request_template.md", kernel: true},
	{tmpl: "ci.yml.tmpl", path: ".github/workflows/ci.yml", kernel: true},
}

// KernelOwnsCore reports whether the core-pack file at rel — a
// repository-relative slash path — is one the kernel alone determines,
// and therefore one a regeneration rewrites rather than keeps. It is the
// single reading of the boundary: `pika apply` asks this table the same
// question `pika init --force` does, instead of carrying its own copy of
// the answer. Two copies is how one command comes to refresh a file the
// other silently treats as the operator's, with nothing to say which is
// right. A path the core pack does not render is not kernel-owned.
func KernelOwnsCore(rel string) bool {
	for _, target := range coreTemplateTargets {
		if target.path == rel {
			return target.kernel
		}
	}
	return false
}

// CoreFiles renders the core pack's repository files — README, AGENTS,
// CONTRIBUTING, the GitHub PR template, and the CI workflow — for a
// project name and language id, keyed by repository-relative slash
// path. A template missing from the pack is a hard error, so callers
// fail before writing anything.
//
// The four canonical skills are not among them: they carry no template
// variables and their ownership split (operator-owned skill file,
// kernel-owned projection region spliced into an otherwise
// operator-owned host file) does not fit a plain create-if-missing
// genFile — skills.Install is the one implementation of that shape, and
// both Run and `pika apply` call it once the rest of the scaffold is on
// disk.
func CoreFiles(lang, name string) (map[string][]byte, error) {
	data := tmplData{Name: name, PikaRef: pikaRef(), CISteps: ciSteps(lang)}
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

// pikaRef is the git ref the scaffolded CI workflow pins its kernel to:
// the version of the binary doing the scaffolding, as a semver tag. The
// pin has to mean "the kernel that adopted this repository", and it can
// only mean that if it is derived from the version the binary reports
// rather than transcribed into the template, where it would go stale at
// the next release with nothing to catch it.
func pikaRef() string {
	if strings.HasPrefix(version.Version, "v") {
		return version.Version
	}
	return "v" + version.Version
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

// filePaths returns the manifest paths, sorted and deduplicated: a
// projection can regenerate the region inside a file the genFile loop
// already wrote whole (AGENTS.md, for instance), and the manifest
// reports that file once, not once per write that touched it.
func filePaths(files []genFile) []string {
	seen := make(map[string]bool, len(files))
	paths := make([]string, 0, len(files))
	for _, f := range files {
		if seen[f.path] {
			continue
		}
		seen[f.path] = true
		paths = append(paths, f.path)
	}
	slices.Sort(paths)
	return paths
}
