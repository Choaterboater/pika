// Package discover walks a repository and classifies its stack: which
// languages, package layouts (single vs workspace), kinds, and which check
// commands (test/lint/fmt/build/typecheck) already exist.
package discover

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
)

// Package is one discovered unit of build, rooted at Root (relative to the
// repository root, "/"-separated).
type Package struct {
	Root     string
	Name     string
	Language string
	Kind     string // "single", "workspace", "spm", "xcode"
}

// Inventory is the full classification of a repository.
type Inventory struct {
	Packages          []Package
	DetectedLanguages []string
	DetectedKinds     []string
	ExistingChecks    map[string]string
	HasGit            bool
	GitHubWorkflows   []string
}

// maxDepth is the maximum marker-search depth below the repo root: a marker
// file at "packages/a/package.json" is depth 3.
const maxDepth = 3

var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".venv":        true,
	"target":       true,
	".build":       true,
	"DerivedData":  true,
}

// markers maps a file or directory name to a marker category.
var markers = map[string]string{
	"package.json":        "packagejson",
	"pyproject.toml":      "pyproject",
	"requirements.txt":    "pyreq",
	"setup.py":            "pyreq",
	"Package.swift":       "packageswift",
	"Cargo.toml":          "cargo",
	"go.mod":              "gomod",
	"Makefile":            "makefile",
	"Justfile":            "justfile",
	"justfile":            "justfile",
	"Taskfile.yml":        "taskfile",
	"pnpm-workspace.yaml": "pnpmworkspace",
	"pnpm-lock.yaml":      "lock.pnpm",
	"package-lock.json":   "lock.npm",
	"bun.lockb":           "lock.bun",
	"yarn.lock":           "lock.yarn",
}

// walkHits records marker paths found during the bounded walk, plus flags for
// which lockfiles exist at the root.
type walkHits struct {
	markerPaths map[string][]string // category -> relative paths
	rootLocks   map[string]bool     // lock categories present at depth 1
}

// Discover walks repoRoot (bounded to maxDepth) and returns the repository
// classification. A nonexistent or non-directory root is an error; individual
// unreadable files are skipped.
func Discover(repoRoot string) (*Inventory, error) {
	root := repoRoot
	if root == "" {
		root = "."
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("discover: %s is not a directory", root)
	}

	hits, err := walk(root)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}

	inv := &Inventory{
		ExistingChecks: map[string]string{},
	}

	inv.HasGit = dirOrFileExists(filepath.Join(root, ".git"))
	inv.GitHubWorkflows = listWorkflows(filepath.Join(root, filepath.FromSlash(".github/workflows")))
	inv.ExistingChecks = existingChecks(root, hits)

	inv.Packages = classify(root, hits)
	langs := map[string]bool{}
	kinds := map[string]bool{}
	for _, p := range inv.Packages {
		langs[p.Language] = true
		kinds[p.Kind] = true
	}
	inv.DetectedLanguages = sortedKeys(langs)
	inv.DetectedKinds = sortedKeys(kinds)
	return inv, nil
}

// walk performs the bounded filesystem walk collecting markers.
func walk(root string) (*walkHits, error) {
	hits := &walkHits{
		markerPaths: map[string][]string{},
		rootLocks:   map[string]bool{},
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil // best effort: skip unreadable entries
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		depth := strings.Count(filepath.ToSlash(rel), "/") + 1
		if d.IsDir() {
			if strings.HasSuffix(d.Name(), ".xcodeproj") {
				hits.markerPaths["xcodeproj"] = append(hits.markerPaths["xcodeproj"], rel)
				return fs.SkipDir // marker, not something to descend into
			}
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			if depth >= maxDepth {
				return fs.SkipDir // files inside would exceed maxDepth
			}
			return nil
		}
		if depth > maxDepth {
			return fs.SkipDir
		}
		cat, ok := markers[d.Name()]
		if !ok {
			return nil
		}
		hits.markerPaths[cat] = append(hits.markerPaths[cat], rel)
		if depth == 1 && strings.HasPrefix(cat, "lock.") {
			hits.rootLocks[cat] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// classify decides between a workspace split and a single project, and emits
// one Package per project (or per member).
func classify(root string, hits *walkHits) []Package {
	if pkgs := workspacePackages(root, hits); len(pkgs) > 0 {
		return pkgs
	}
	return singlePackages(root, hits)
}

// workspacePackages emits one Package per declared workspace member. It
// returns nil when no ecosystem config declares members (or none resolve).
func workspacePackages(root string, hits *walkHits) []Package {
	var pkgs []Package
	for _, rel := range hits.markerPaths["packagejson"] {
		if rel != "package.json" { // only the root config splits a TS workspace
			continue
		}
		pj, err := readPackageJSON(filepath.Join(root, rel))
		if err != nil || len(pj.WorkspaceGlobs) == 0 {
			continue
		}
		for _, m := range expandMembers(root, pj.WorkspaceGlobs, "package.json") {
			name := ""
			if mpj, err := readPackageJSON(filepath.Join(root, filepath.FromSlash(m))); err == nil {
				name = mpj.Name
			}
			pkgs = append(pkgs, Package{Root: m, Name: name, Language: "typescript", Kind: "workspace"})
		}
		break
	}
	if len(pkgs) > 0 {
		slices.SortFunc(pkgs, func(a, b Package) int { return strings.Compare(a.Root, b.Root) })
		return pkgs
	}

	for _, rel := range hits.markerPaths["pnpmworkspace"] {
		if rel != "pnpm-workspace.yaml" {
			continue
		}
		globs, err := readPnpmWorkspace(filepath.Join(root, rel))
		if err != nil || len(globs) == 0 {
			continue
		}
		for _, m := range expandMembers(root, globs, "package.json") {
			name := ""
			if mpj, err := readPackageJSON(filepath.Join(root, filepath.FromSlash(m))); err == nil {
				name = mpj.Name
			}
			pkgs = append(pkgs, Package{Root: m, Name: name, Language: "typescript", Kind: "workspace"})
		}
		break
	}
	if len(pkgs) > 0 {
		slices.SortFunc(pkgs, func(a, b Package) int { return strings.Compare(a.Root, b.Root) })
		return pkgs
	}

	for _, rel := range hits.markerPaths["cargo"] {
		if rel != "Cargo.toml" {
			continue
		}
		cargo, err := readCargoTOML(filepath.Join(root, rel))
		if err != nil || len(cargo.Workspace.Members) == 0 {
			continue
		}
		for _, m := range expandMembers(root, cargo.Workspace.Members, "Cargo.toml") {
			name := ""
			if mc, err := readCargoTOML(filepath.Join(root, filepath.FromSlash(m), "Cargo.toml")); err == nil {
				name = mc.Package.Name
			}
			pkgs = append(pkgs, Package{Root: m, Name: name, Language: "rust", Kind: "workspace"})
		}
		break
	}
	if len(pkgs) > 0 {
		slices.SortFunc(pkgs, func(a, b Package) int { return strings.Compare(a.Root, b.Root) })
	}
	return pkgs
}

// expandMembers resolves workspace glob patterns to member roots (relative,
// "/"-separated) that contain the given manifest. A literal "." member keeps
// the root itself in the split.
func expandMembers(root string, globs []string, manifest string) []string {
	seen := map[string]bool{}
	var members []string
	for _, g := range globs {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if g == "." {
			if !seen["."] {
				seen["."] = true
				members = append(members, ".")
			}
			continue
		}
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(g)))
		if err != nil {
			continue
		}
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err != nil || !fi.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(m, manifest)); err != nil {
				continue
			}
			rel, err := filepath.Rel(root, m)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if !seen[rel] {
				seen[rel] = true
				members = append(members, rel)
			}
		}
	}
	slices.Sort(members)
	return members
}

// singlePackages emits one Package per language detected at the root. Order
// follows the detection table so the primary stack comes first.
func singlePackages(root string, hits *walkHits) []Package {
	var pkgs []Package
	add := func(language, kind, name string) {
		pkgs = append(pkgs, Package{Root: ".", Name: name, Language: language, Kind: kind})
	}

	first := func(cat, prefer string) string {
		if prefer != "" && slices.Contains(hits.markerPaths[cat], prefer) {
			return prefer
		}
		if len(hits.markerPaths[cat]) > 0 {
			return hits.markerPaths[cat][0]
		}
		return ""
	}
	if rel := first("gomod", "go.mod"); rel != "" {
		name := ""
		if mod, err := readGoMod(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			name = mod.Module
		}
		add("go", "single", name)
	}
	if rel := first("cargo", "Cargo.toml"); rel != "" {
		name := ""
		if cargo, err := readCargoTOML(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			name = cargo.Package.Name
		}
		add("rust", "single", name)
	}
	if len(hits.markerPaths["packageswift"]) > 0 {
		add("swift", "spm", "")
	}
	if len(hits.markerPaths["xcodeproj"]) > 0 {
		name := strings.TrimSuffix(filepath.Base(hits.markerPaths["xcodeproj"][0]), ".xcodeproj")
		add("swift", "xcode", name)
	}
	if rel := first("pyproject", "pyproject.toml"); rel != "" {
		name := ""
		if pp, err := readPyproject(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			name = pp.Project.Name
		}
		add("python", "single", name)
	} else if len(hits.markerPaths["pyreq"]) > 0 {
		add("python", "single", "")
	}
	if rel := first("packagejson", "package.json"); rel != "" {
		name := ""
		if pj, err := readPackageJSON(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			name = pj.Name
		}
		add("typescript", "single", name)
	}
	return pkgs
}

// existingChecks maps canonical verbs (test, lint, fmt, build, typecheck) to
// the first matching command found, preferring Makefile > Justfile > Taskfile
// > package.json scripts. Only root-level configs are considered.
func existingChecks(root string, hits *walkHits) map[string]string {
	checks := map[string]string{}
	record := func(verbs []string, command string) {
		for _, v := range verbs {
			if _, ok := checks[v]; !ok {
				checks[v] = command
			}
		}
	}
	for _, rel := range hits.markerPaths["makefile"] {
		if filepath.Dir(rel) != "." {
			continue
		}
		for verb, target := range makefileTargets(filepath.Join(root, rel)) {
			record([]string{verb}, "make "+target)
		}
	}
	for _, rel := range hits.markerPaths["justfile"] {
		if filepath.Dir(rel) != "." {
			continue
		}
		for verb := range justfileRecipes(filepath.Join(root, rel)) {
			record([]string{verb}, "just "+verb)
		}
	}
	for _, rel := range hits.markerPaths["taskfile"] {
		if filepath.Dir(rel) != "." {
			continue
		}
		for verb := range taskfileTasks(filepath.Join(root, rel)) {
			record([]string{verb}, "task "+verb)
		}
	}
	pm := "npm"
	switch {
	case hits.rootLocks["lock.pnpm"]:
		pm = "pnpm"
	case hits.rootLocks["lock.yarn"]:
		pm = "yarn"
	case hits.rootLocks["lock.bun"]:
		pm = "bun"
	}
	if slices.Contains(hits.markerPaths["packagejson"], "package.json") {
		if pj, err := readPackageJSON(filepath.Join(root, "package.json")); err == nil {
			names := sortedKeys2(pj.Scripts)
			for _, script := range names {
				if verb, ok := canonicalVerb(script); ok {
					record([]string{verb}, pm+" run "+script)
				}
			}
		}
	}
	return checks
}

// canonicalVerb maps a discovered target/script name to one of the canonical
// check verbs, or reports that it is not a check.
func canonicalVerb(name string) (string, bool) {
	switch name {
	case "test":
		return "test", true
	case "lint":
		return "lint", true
	case "fmt", "format":
		return "fmt", true
	case "build", "compile":
		return "build", true
	case "typecheck", "type-check", "tsc", "types":
		return "typecheck", true
	}
	return "", false
}

var (
	makefileTargetRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.-]*):(?:\s|$)`)
	makefilePhonyRe  = regexp.MustCompile(`^\.PHONY\s*:\s*(.+)$`)
)

// makefileTargets returns canonical check verbs found in a Makefile, mapped to
// their target name. Targets come from explicit rules and .PHONY listings.
func makefileTargets(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	verbs := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if m := makefilePhonyRe.FindStringSubmatch(line); m != nil {
			for _, name := range strings.Fields(m[1]) {
				if verb, ok := canonicalVerb(name); ok {
					if _, dup := verbs[verb]; !dup {
						verbs[verb] = name
					}
				}
			}
			continue
		}
		if strings.HasPrefix(line, "\t") {
			continue
		}
		if m := makefileTargetRe.FindStringSubmatch(line); m != nil {
			if verb, ok := canonicalVerb(m[1]); ok {
				if _, dup := verbs[verb]; !dup {
					verbs[verb] = m[1]
				}
			}
		}
	}
	return verbs
}

var justfileRecipeRe = regexp.MustCompile(`^@?([A-Za-z_][A-Za-z0-9_-]*)(?:\s+[^\s:#@]+)*\s*:`)

// justfileRecipes returns the canonical check verbs that appear as recipes.
func justfileRecipes(path string) map[string]bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	verbs := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		if m := justfileRecipeRe.FindStringSubmatch(line); m != nil {
			if verb, ok := canonicalVerb(m[1]); ok {
				verbs[verb] = true
			}
		}
	}
	return verbs
}

// taskfileTasks returns the canonical check verbs that appear as tasks.
func taskfileTasks(path string) map[string]bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var tf struct {
		Tasks map[string]yaml.MapSlice `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(data, &tf); err != nil {
		return nil
	}
	verbs := map[string]bool{}
	for name := range tf.Tasks {
		if verb, ok := canonicalVerb(name); ok {
			verbs[verb] = true
		}
	}
	return verbs
}

// --- typed config readers (all best-effort; parse errors surface as err) ---

type packageJSON struct {
	Name           string            `json:"name"`
	Scripts        map[string]string `json:"scripts"`
	WorkspaceGlobs []string          `json:"-"`
	RawWorkspaces  json.RawMessage   `json:"workspaces"`
}

func readPackageJSON(path string) (*packageJSON, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pj packageJSON
	if err := json.Unmarshal(data, &pj); err != nil {
		return nil, err
	}
	if len(pj.RawWorkspaces) > 0 {
		var globs []string
		if err := json.Unmarshal(pj.RawWorkspaces, &globs); err == nil {
			pj.WorkspaceGlobs = globs
		} else {
			var obj struct {
				Packages []string `json:"packages"`
			}
			if err := json.Unmarshal(pj.RawWorkspaces, &obj); err == nil {
				pj.WorkspaceGlobs = obj.Packages
			}
		}
	}
	return &pj, nil
}

func readPnpmWorkspace(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pw struct {
		Packages []string `yaml:"packages"`
	}
	if err := yaml.Unmarshal(data, &pw); err != nil {
		return nil, err
	}
	return pw.Packages, nil
}

// --- minimal TOML reading ---
//
// goccy/go-yaml is strict YAML and rejects TOML's "[table]" headers, and the
// brief forbids new dependencies, so discovery parses the few TOML fields it
// needs with the small line-based reader below. It understands [table]
// headers, comments, and string or string-array values (single- or
// multi-line); it is not a general TOML parser.

type cargoTOML struct {
	Package struct {
		Name string
	}
	Workspace struct {
		Members []string
	}
}

type pyproject struct {
	Project struct {
		Name string
	}
	BuildSystem struct {
		Requires []string
	}
}

// tomlTables maps "[table]" name -> its string and []string values.
type tomlTables map[string]map[string]any

func readCargoTOML(path string) (*cargoTOML, error) {
	tables, err := parseTOMLTables(path)
	if err != nil {
		return nil, err
	}
	var c cargoTOML
	if pkg := tables["package"]; pkg != nil {
		c.Package.Name, _ = pkg["name"].(string)
	}
	if ws := tables["workspace"]; ws != nil {
		c.Workspace.Members, _ = ws["members"].([]string)
	}
	return &c, nil
}

func readPyproject(path string) (*pyproject, error) {
	tables, err := parseTOMLTables(path)
	if err != nil {
		return nil, err
	}
	var p pyproject
	if proj := tables["project"]; proj != nil {
		p.Project.Name, _ = proj["name"].(string)
	}
	if bs := tables["build-system"]; bs != nil {
		p.BuildSystem.Requires, _ = bs["requires"].([]string)
	}
	return &p, nil
}

func parseTOMLTables(path string) (tomlTables, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	tables := tomlTables{}
	current := ""
	var key, pending string
	flush := func() {
		if key == "" {
			return
		}
		if tables[current] == nil {
			tables[current] = map[string]any{}
		}
		tables[current][key] = parseTOMLValue(strings.TrimSpace(pending))
		key, pending = "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if pending != "" { // continuation of a multi-line array
			pending += " " + strings.TrimSpace(line)
			if strings.Count(pending, "[") <= strings.Count(pending, "]") {
				flush()
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			flush()
			current = strings.Trim(trimmed, "[]")
			continue
		}
		if eq := strings.Index(trimmed, "="); eq > 0 {
			flush()
			key = strings.TrimSpace(trimmed[:eq])
			pending = strings.TrimSpace(trimmed[eq+1:])
			if strings.HasPrefix(pending, "[") && strings.Count(pending, "[") > strings.Count(pending, "]") {
				continue // array continues on following lines
			}
			flush()
		}
	}
	flush()
	return tables, nil
}

// parseTOMLValue converts a trimmed TOML value to a string or []string;
// anything else is returned verbatim as a string.
func parseTOMLValue(v string) any {
	if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		body := strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
		var out []string
		for _, item := range strings.Split(body, ",") {
			item = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(item), `"`), `"`)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	}
	if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
		return strings.TrimSuffix(strings.TrimPrefix(v, `"`), `"`)
	}
	// Strip trailing comments from bare values.
	if idx := strings.Index(v, "#"); idx >= 0 {
		v = strings.TrimSpace(v[:idx])
	}
	return v
}

type goMod struct {
	Module string
	Go     string
}

var (
	goModuleRe = regexp.MustCompile(`(?m)^module\s+(\S+)`)
	goDirRe    = regexp.MustCompile(`(?m)^go\s+([0-9.]+)`)
)

func readGoMod(path string) (*goMod, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var gm goMod
	if m := goModuleRe.FindStringSubmatch(string(data)); m != nil {
		gm.Module = m[1]
	}
	if m := goDirRe.FindStringSubmatch(string(data)); m != nil {
		gm.Go = m[1]
	}
	return &gm, nil
}

func dirOrFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func listWorkflows(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func sortedKeys2(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
