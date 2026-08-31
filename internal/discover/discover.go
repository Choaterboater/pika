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
	"sort"
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
	// UnclassifiedMarkers names the repository-relative paths of a
	// real ecosystem config file discover recognized but has no
	// language pack for (Maven's pom.xml, Gradle's build.gradle[.kts],
	// CMake's CMakeLists.txt) — sorted. A repository with one of these
	// and nothing else yields zero Packages the same as a repository
	// discover saw nothing distinctive in at all; this is what lets a
	// caller tell those two very different situations apart and say
	// so, rather than reporting both as an equally deliberate
	// core-only adoption.
	UnclassifiedMarkers []string
}

// maxDepth is the maximum marker-search depth below the repo root: a marker
// file at "packages/a/package.json" is depth 3.
const maxDepth = 3

// SkipDirs names directories no naming or discovery walk should descend
// into: vendored dependencies and build/IDE output that belongs to no
// author at this repository and cannot be renamed. internal/checks
// reuses this exact set for the same reason — a file inside
// node_modules or DerivedData is not the repository's own naming to
// judge, and it must be exactly the set discover already skips when
// classifying the repository, not a second list that could drift.
var SkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".venv":        true,
	"target":       true,
	".build":       true,
	"DerivedData":  true,
}

// markers maps a file or directory name to a marker category.
//
// maven/gradle/cmake are real ecosystem markers with no language pack
// behind them (see unclassifiedCategories below): recognizing the
// file is what lets Discover report that it saw a real project it
// cannot classify, rather than looking identical to a repository with
// nothing distinctive in it at all.
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
	"pom.xml":             "maven",
	"build.gradle":        "gradle",
	"build.gradle.kts":    "gradle",
	"CMakeLists.txt":      "cmake",
}

// unclassifiedCategories are marker categories with no consumer at
// all: no singlePackages/workspacePackages branch turns them into a
// Package, and no existingChecks branch turns them into a discovered
// command, unlike makefile/justfile/taskfile which at least feed
// ExistingChecks. Populating Inventory.UnclassifiedMarkers from
// exactly this set is deliberate and closed — adding a marker to the
// map above without also deciding whether it belongs here would leave
// it silently inert either way.
var unclassifiedCategories = map[string]bool{
	"maven":  true,
	"gradle": true,
	"cmake":  true,
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
	for cat := range unclassifiedCategories {
		inv.UnclassifiedMarkers = append(inv.UnclassifiedMarkers, hits.markerPaths[cat]...)
	}
	sort.Strings(inv.UnclassifiedMarkers)

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
			if SkipDirs[d.Name()] {
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
		for verb, name := range justfileRecipes(filepath.Join(root, rel)) {
			record([]string{verb}, "just "+name)
		}
	}
	for _, rel := range hits.markerPaths["taskfile"] {
		if filepath.Dir(rel) != "." {
			continue
		}
		for verb, name := range taskfileTasks(filepath.Join(root, rel)) {
			record([]string{verb}, "task "+name)
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

// justfileRecipes returns canonical check verbs found in a Justfile, mapped
// to their recipe name — mirroring makefileTargets, because "just fmt" only
// works when a recipe literally named `fmt` exists: canonicalVerb accepts
// "fmt" and "format" as the same check, but a Justfile whose recipe is
// named "format" has no recipe named "fmt" at all, and invoking the wrong
// one fails with "justfile does not contain recipe `fmt`" — a discovery
// defect wearing a format-gate failure.
func justfileRecipes(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	verbs := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		if m := justfileRecipeRe.FindStringSubmatch(line); m != nil {
			if verb, ok := canonicalVerb(m[1]); ok {
				if _, dup := verbs[verb]; !dup {
					verbs[verb] = m[1]
				}
			}
		}
	}
	return verbs
}

// taskfileTasks returns canonical check verbs found in a Taskfile.yml,
// mapped to their task name, for the same reason justfileRecipes maps to
// a recipe name: "task fmt" only works when a task literally named
// "fmt" exists, and a Taskfile whose task is named "format" has no task
// named "fmt" — invoking it fails with `Task "fmt" does not exist`.
//
// Task names are sorted before the first-match-wins scan. tf.Tasks is a
// Go map (YAML key order is not preserved across the outer object), so
// without a fixed order two tasks that both canonicalize to the same
// verb would resolve to whichever the map happened to iterate first —
// a result that could change between identical runs. Sorting trades
// "first in the file" (what makefileTargets and justfileRecipes give)
// for "first alphabetically", which is not the same guarantee but is at
// least the same answer every time.
//
// A Taskfile that fails to unmarshal discovers nothing at all, silently
// — a known, narrower limit than the name defect above, not a fix
// candidate here. goreleaser/goreleaser's own Taskfile.yml is a real
// example: its `fuzz:` task's `cmds:` key is indented level with
// `fuzz:` itself rather than nested under it, which go-task's own
// parser tolerates and goccy/go-yaml (strict) rejects with "sequence
// was used where mapping is expected" — losing every task in the
// file, including the clean `fmt`/`lint`/`test` ones nowhere near the
// bad indentation. Unlike the name defect, the result here is an
// honest "no command discovered" rather than a false failure, so it
// is recorded rather than fixed: matching go-task's own leniency would
// mean replicating its specific YAML tolerances rather than reading
// YAML, which is a different and larger undertaking than this reader
// is for.
func taskfileTasks(path string) map[string]string {
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
	names := make([]string, 0, len(tf.Tasks))
	for name := range tf.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	verbs := map[string]string{}
	for _, name := range names {
		if verb, ok := canonicalVerb(name); ok {
			if _, dup := verbs[verb]; !dup {
				verbs[verb] = name
			}
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
// anything else is returned verbatim as a string. Inline comments are
// stripped, but never from inside a quoted token.
func parseTOMLValue(v string) any {
	if items, ok := parseTOMLArray(v); ok {
		return items
	}
	if s, ok := parseQuotedTOMLString(v); ok {
		return s
	}
	// Bare value: strip trailing comments.
	if idx := strings.Index(v, "#"); idx >= 0 {
		v = strings.TrimSpace(v[:idx])
	}
	return v
}

// parseTOMLArray parses a leading "[" ... matching "]" segment into string
// items, ignoring anything after the closing bracket (typically an inline
// comment). It reports ok=false when v does not start with "[" or the
// closing bracket is missing. Quoted items are unquoted; bare items keep
// their comment-stripped text.
func parseTOMLArray(v string) ([]string, bool) {
	if !strings.HasPrefix(v, "[") {
		return nil, false
	}
	depth, inQuote, esc := 0, false, false
	end := -1
scan:
	for i := range len(v) {
		c := v[i]
		if esc {
			esc = false
			continue
		}
		switch {
		case inQuote:
			if c == '\\' {
				esc = true
			} else if c == '"' {
				inQuote = false
			}
		case c == '"':
			inQuote = true
		case c == '[':
			depth++
		case c == ']':
			depth--
			if depth == 0 {
				end = i
				break scan
			}
		}
	}
	if end < 0 {
		return nil, false
	}
	var items []string
	start := 1
	inQuote, esc = false, false
	for i := 1; i < end; i++ {
		c := v[i]
		if esc {
			esc = false
			continue
		}
		if inQuote {
			if c == '\\' {
				esc = true
			} else if c == '"' {
				inQuote = false
			}
			continue
		}
		switch c {
		case '"':
			inQuote = true
		case ',':
			items = append(items, v[start:i])
			start = i + 1
		}
	}
	items = append(items, v[start:end])
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if s, ok := parseQuotedTOMLString(item); ok {
			out = append(out, s)
			continue
		}
		if idx := strings.Index(item, "#"); idx >= 0 {
			item = strings.TrimSpace(item[:idx])
		}
		if item != "" {
			out = append(out, item)
		}
	}
	return out, true
}

// parseQuotedTOMLString parses a leading double-quoted TOML string and
// returns its unquoted content plus everything after the closing quote
// (typically an inline comment). It reports ok=false when v does not start
// with a quote or the closing quote is missing. `\"` and `\\` escapes are
// resolved; other escape sequences are kept verbatim.
func parseQuotedTOMLString(v string) (string, bool) {
	if !strings.HasPrefix(v, `"`) {
		return "", false
	}
	var b strings.Builder
	for i := 1; i < len(v); i++ {
		switch c := v[i]; c {
		case '\\':
			if i+1 >= len(v) {
				return "", false
			}
			i++
			switch v[i] {
			case '"', '\\':
				b.WriteByte(v[i])
			default:
				b.WriteByte('\\')
				b.WriteByte(v[i])
			}
		case '"':
			return b.String(), true
		default:
			b.WriteByte(c)
		}
	}
	return "", false // unterminated string
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
