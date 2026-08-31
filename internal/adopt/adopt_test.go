package adopt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Choaterboater/pika/internal/checks"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/profiles"
)

// writeFile creates rel (slash-separated) under root with the given content.
func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// messyFixture builds the brief's messy adoption target: a Go module with
// mixed-case and catch-all names, an existing Makefile, and no lockfile.
func messyFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/messy\n\ngo 1.26\n")
	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, root, "Makefile", ".PHONY: test lint fmt build\n\n"+
		"test:\n\techo test-ok\n"+
		"lint:\n\techo lint-fails 1>&2\n\texit 1\n"+
		"fmt:\n\techo fmt-ok\n"+
		"build:\n\techo build-ok\n")
	writeFile(t, root, "MyNotes.md", "# notes\n")
	writeFile(t, root, "Common/tools.go", "package tools\n")
	writeFile(t, root, "utils/helpers.go", "package helpers\n")
	writeFile(t, root, "README.md", "# messy\n")
	return root
}

// treeDigest hashes every file under root (path + content) into one digest,
// skipping the adoption proposal files — the two .project drafts and the
// visible review bundle — when skipDrafts is set.
func treeDigest(t *testing.T, root string, skipDrafts bool) string {
	t.Helper()
	var parts []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
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
		rel = filepath.ToSlash(rel)
		if skipDrafts && (rel == ".project/contract.yaml.draft" ||
			rel == ".project/profiles.lock.draft" || rel == "review/adoption-review.md") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		parts = append(parts, rel+":"+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

func findBaseline(t *testing.T, checks []BaselineCheck, verb string) BaselineCheck {
	t.Helper()
	for _, bc := range checks {
		if bc.Verb == verb {
			return bc
		}
	}
	t.Fatalf("no baseline check for verb %q in %v", verb, checks)
	return BaselineCheck{}
}

func findConvention(t *testing.T, cm ConventionMap, name string) Convention {
	t.Helper()
	for _, c := range cm {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no convention %q in %v", name, cm)
	return Convention{}
}

func changePaths(changes []Change) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, c.Path)
	}
	return out
}

func TestPreviewMessyFixture(t *testing.T) {
	root := messyFixture(t)
	before := treeDigest(t, root, true)

	rep, err := Preview(root)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	// Inventory is non-empty and detected profiles list the stack pack.
	if len(rep.Inventory.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(rep.Inventory.Packages))
	}
	if !slices.Contains(rep.Inventory.DetectedLanguages, "go") {
		t.Errorf("expected go in %v", rep.Inventory.DetectedLanguages)
	}
	if len(rep.DetectedProfiles) < 1 || rep.DetectedProfiles[0] != "core@1" {
		t.Errorf("expected core@1 first in %v", rep.DetectedProfiles)
	}
	if !slices.Contains(rep.DetectedProfiles, "go@1") {
		t.Errorf("expected go@1 in %v", rep.DetectedProfiles)
	}

	// Nothing outside .project/*.draft changed.
	if after := treeDigest(t, root, true); before != after {
		t.Fatal("tree changed outside .project draft files")
	}
	for _, draft := range []string{"contract.yaml.draft", "profiles.lock.draft"} {
		if _, err := os.Stat(filepath.Join(root, ".project", draft)); err != nil {
			t.Errorf("expected .project/%s to be written: %v", draft, err)
		}
	}

	// The draft is a full, valid contract.
	draft, err := contract.Load(filepath.Join(root, ".project", "contract.yaml.draft"))
	if err != nil {
		t.Fatalf("draft contract is not valid: %v", err)
	}
	if draft.Schema != 1 {
		t.Errorf("draft schema = %d, want 1", draft.Schema)
	}
	if draft.Project.Name != "messy" {
		t.Errorf("draft project name = %q, want messy", draft.Project.Name)
	}
	if draft.Project.Topology != "single" {
		t.Errorf("draft topology = %q, want single", draft.Project.Topology)
	}
	if draft.Profiles == nil || !slices.Equal(draft.Profiles, []string{"core@1", "go@1"}) {
		t.Errorf("draft profiles = %v, want [core@1 go@1]", draft.Profiles)
	}
	pkg, ok := draft.Packages["messy"]
	if !ok {
		t.Fatalf("draft packages missing \"messy\": %v", draft.Packages)
	}
	if pkg.Root != "." || !slices.Equal(pkg.Profiles, []string{"core@1", "go@1"}) {
		t.Errorf("draft package messy = %+v", pkg)
	}
	for verb, slot := range map[string]string{"test": "test", "lint": "lint", "fmt": "format"} {
		want := "make " + verb
		if draft.Commands[slot] != want {
			t.Errorf("draft commands[%s] = %q, want %q", slot, draft.Commands[slot], want)
		}
	}
	if draft.GitHub.Merge != "squash" {
		t.Errorf("draft github.merge = %q, want squash", draft.GitHub.Merge)
	}
	if draft.Evidence.Publish != "sanitized" {
		t.Errorf("draft evidence.publish = %q, want sanitized", draft.Evidence.Publish)
	}

	// Every naming deviation present at adoption becomes a recorded
	// exception with all four spec §5.3 fields — including the banned
	// catch-all, which is what lets the drafted contract pass gate 1.
	byPath := map[string]checks.Exception{}
	for _, ex := range rep.Exceptions {
		for _, field := range []string{ex.RuleID, ex.Reason, ex.Owner, ex.ReviewCondition} {
			if strings.TrimSpace(field) == "" {
				t.Errorf("exception for %s has an empty required field: %+v", ex.Path, ex)
			}
		}
		byPath[ex.Path] = ex
	}
	for path, rule := range map[string]string{
		"MyNotes.md":       "naming-kebab-case",
		"Common/tools.go":  "naming-kebab-case",
		"utils/helpers.go": "naming-catch-all",
	} {
		ex, ok := byPath[path]
		if !ok {
			t.Errorf("expected a naming exception for %s, got %v", path, slices.Sorted(maps.Keys(byPath)))
			continue
		}
		if ex.RuleID != rule {
			t.Errorf("exception for %s has rule %q, want %q", path, ex.RuleID, rule)
		}
	}
	// The catch-all rationale must say why this one is recordable —
	// it predates adoption — so a reviewer can tell it from a new one.
	if ex := byPath["utils/helpers.go"]; !strings.Contains(ex.Reason, "pre-existing") ||
		!strings.Contains(ex.Reason, "still fails gate 1") {
		t.Errorf("catch-all exception reason does not justify itself as pre-existing: %q", ex.Reason)
	}
	if len(draft.Exceptions) == 0 {
		t.Error("draft contract records no exceptions")
	}

	// Recording never hides: the inherited banned name is still reported
	// as a conflict so a human sees it before approving `pika apply`.
	var conflictPaths []string
	for _, c := range rep.Conflicts {
		if c.RuleID != "naming-catch-all" {
			t.Errorf("unexpected conflict rule %q on %s", c.RuleID, c.Path)
		}
		conflictPaths = append(conflictPaths, c.Path)
	}
	if !slices.Contains(conflictPaths, "utils/helpers.go") {
		t.Errorf("expected a conflict for utils/helpers.go, got %v", conflictPaths)
	}

	// Proposed changes cover missing required files and the draft records.
	changes := changePaths(rep.ProposedChanges)
	for _, want := range []string{
		"AGENTS.md", "CONTRIBUTING.md", ".github/workflows/",
		".github/pull_request_template.md", ".project/contract.yaml", ".project/profiles.lock",
	} {
		if !slices.Contains(changes, want) {
			t.Errorf("expected proposed change %s, got %v", want, changes)
		}
	}

	// The convention map classifies against core@1 expectations.
	if c := findConvention(t, rep.ConventionMap, "naming/naming-kebab-case"); c.Status != StatusException {
		t.Errorf("kebab-case convention status = %q, want %q", c.Status, StatusException)
	}
	if c := findConvention(t, rep.ConventionMap, "naming/naming-catch-all"); c.Status != StatusException {
		t.Errorf("catch-all convention status = %q, want %q", c.Status, StatusException)
	} else if !strings.Contains(c.Detail, "pre-existing") {
		t.Errorf("catch-all convention detail does not say the names are inherited: %q", c.Detail)
	}
	if c := findConvention(t, rep.ConventionMap, "file/README.md"); c.Status != StatusMatch {
		t.Errorf("README.md convention status = %q, want %q", c.Status, StatusMatch)
	}
	if c := findConvention(t, rep.ConventionMap, "check/build"); c.Status != StatusMatch || c.Detail != "make build" {
		t.Errorf("check/build convention = %+v", c)
	}

	// Valid existing conventions are recorded in the draft extensions.
	convs, ok := rep.DraftContract.Extensions["conventions"].([]map[string]string)
	if !ok || len(convs) == 0 {
		t.Fatalf("draft extensions.conventions missing or wrong type: %v", rep.DraftContract.Extensions["conventions"])
	}
	var convNames []string
	for _, c := range convs {
		convNames = append(convNames, c["name"])
	}
	if !slices.Contains(convNames, "check/build") {
		t.Errorf("expected check/build recorded in extensions.conventions, got %v", convNames)
	}

	// Baseline: failing command recorded, adopt still succeeds (err == nil).
	if bc := findBaseline(t, rep.BaselineChecks, "test"); bc.Status != "pass" || bc.Exit != 0 || bc.Command != "make test" {
		t.Errorf("baseline test = %+v", bc)
	}
	if bc := findBaseline(t, rep.BaselineChecks, "lint"); bc.Status != "fail" || bc.Exit == 0 {
		t.Errorf("baseline lint = %+v, want fail with nonzero exit", bc)
	}
}

// TestAdoptedExceptionsDoNotCoverACatchAllAddedLater pins the boundary
// that makes auto-recording defensible: the records adopt writes waive the
// exact paths it inventoried and nothing else, so a catch-all name someone
// introduces after adoption is a new decision the rule still fires on.
func TestAdoptedExceptionsDoNotCoverACatchAllAddedLater(t *testing.T) {
	root := messyFixture(t)
	rep, err := Preview(root)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	recorded := map[string][]checks.Exception{}
	for _, ex := range rep.Exceptions {
		recorded[ex.Path] = append(recorded[ex.Path], ex)
	}
	resolved, err := profiles.Resolve([]string{profiles.CoreRef})
	if err != nil {
		t.Fatal(err)
	}

	// Everything adopt inventoried is covered: the drafted contract is
	// one a `pika check` in this repository can pass.
	if vs := checks.Naming(root, resolved.NamingRules, recorded); len(vs) > 0 {
		t.Fatalf("adopted repository still has naming findings: %+v", vs)
	}

	// A catch-all written after the inventory is not covered by any record.
	writeFile(t, root, "internal/utils/parse.go", "package utils\n")
	var found bool
	for _, v := range checks.Naming(root, resolved.NamingRules, recorded) {
		if v.RuleID == "naming-catch-all" && v.Path == "internal/utils/parse.go" {
			found = true
		}
	}
	if !found {
		t.Error("a catch-all added after adoption was silently covered by the adopted exceptions")
	}
}

func TestPreviewDeterministic(t *testing.T) {
	root := messyFixture(t)

	rep1, err := Preview(root)
	if err != nil {
		t.Fatalf("first Preview: %v", err)
	}
	b1, err := json.MarshalIndent(rep1, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	c1, err := os.ReadFile(filepath.Join(root, ".project", "contract.yaml.draft"))
	if err != nil {
		t.Fatal(err)
	}
	l1, err := os.ReadFile(filepath.Join(root, ".project", "profiles.lock.draft"))
	if err != nil {
		t.Fatal(err)
	}

	rep2, err := Preview(root)
	if err != nil {
		t.Fatalf("second Preview: %v", err)
	}
	b2, err := json.MarshalIndent(rep2, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := os.ReadFile(filepath.Join(root, ".project", "contract.yaml.draft"))
	if err != nil {
		t.Fatal(err)
	}
	l2, err := os.ReadFile(filepath.Join(root, ".project", "profiles.lock.draft"))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(b1, b2) {
		t.Error("preview JSON is not byte-identical across runs")
	}
	if !bytes.Equal(c1, c2) {
		t.Error("draft contract is not byte-identical across runs")
	}
	if !bytes.Equal(l1, l2) {
		t.Error("draft lock is not byte-identical across runs")
	}
}

func TestPreviewBaselineTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep(1) is not guaranteed on windows")
	}
	old := baselineTimeout
	baselineTimeout = 200 * time.Millisecond
	defer func() { baselineTimeout = old }()

	root := t.TempDir()
	writeFile(t, root, "Makefile", "test:\n\tsleep 5\n")

	rep, err := Preview(root)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	bc := findBaseline(t, rep.BaselineChecks, "test")
	if bc.Status != "timeout" || bc.Exit == 0 {
		t.Errorf("baseline test = %+v, want timeout with nonzero exit", bc)
	}
}

// A repository whose only marker is a real ecosystem with no profile
// pack (Maven's pom.xml) must warn rather than silently look like a
// deliberate core-only adoption: zero packages either way, but the
// two situations are not the same and only one of them is worth an
// operator's attention before they trust the draft.
func TestPreviewWarnsOnAnUnclassifiedEcosystemMarker(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "pom.xml", "<project></project>\n")

	rep, err := Preview(root)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(rep.Inventory.Packages) != 0 {
		t.Fatalf("expected 0 packages, got %+v", rep.Inventory.Packages)
	}
	if len(rep.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly one", rep.Warnings)
	}
	if !strings.Contains(rep.Warnings[0], "pom.xml") {
		t.Errorf("warning does not name the unclassified marker: %q", rep.Warnings[0])
	}
	bundle, err := os.ReadFile(filepath.Join(root, ReviewPath))
	if err != nil {
		t.Fatalf("read review bundle: %v", err)
	}
	if !strings.Contains(string(bundle), "pom.xml") {
		t.Errorf("review bundle does not mention the unclassified marker:\n%s", bundle)
	}
}

// A repository with nothing distinctive at all gets a different,
// generic warning — not silence, and not a false claim to have seen a
// specific ecosystem it did not.
func TestPreviewWarnsWhenNothingIsClassifiable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "README.md", "# nothing here\n")

	rep, err := Preview(root)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(rep.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly one", rep.Warnings)
	}
	if !strings.Contains(rep.Warnings[0], "could not classify") {
		t.Errorf("warning does not say classification failed outright: %q", rep.Warnings[0])
	}
}

// A repository adopt DOES classify gets no classification warning at
// all — the signal must track "found nothing", not merely exist on
// every adoption.
func TestPreviewNoWarningWhenClassified(t *testing.T) {
	root := messyFixture(t)
	rep, err := Preview(root)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none: this fixture is a real go module", rep.Warnings)
	}
}

// A Go module with no Makefile leaves lint/typecheck/test empty in
// discover.ExistingChecks, so `pika apply` autofills them from go@1's
// hints (`go vet ./...`, `go build -o /dev/null ./...`, `go test
// ./...`). Before this fix, Preview only baselined what discover
// found — nothing, here — so a repository whose real code fails `go
// vet` adopted with a silent, clean baseline and a contract whose
// first `pika check` after apply was red. Reproduces the shape found
// against real foreign repositories (a real `cargo clippy` failure on
// ripgrep, a real `ruff format` failure on psf/requests): the pack
// hint is measured only against a freshly scaffolded `pika init`
// project, not against arbitrary pre-existing code.
func TestPreviewBaselinesAutofilledPackHintsNotJustDiscoveredCommands(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/vetfail\n\ngo 1.26\n")
	// A real go vet finding: a Printf verb mismatched with its
	// argument's type. This compiles — vet analyzes compiled code —
	// and go vet ./... fails on it for real.
	writeFile(t, root, "main.go", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Printf(\"%d\\n\", \"not a number\")\n}\n")

	rep, err := Preview(root)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if _, ok := rep.Inventory.ExistingChecks["lint"]; ok {
		t.Fatal("fixture leaked a discovered lint command; the test needs an empty slot to prove autofill is baselined")
	}
	lint := findBaseline(t, rep.BaselineChecks, "lint")
	if lint.Status != "fail" {
		t.Fatalf("baseline lint = %+v, want a failing `go vet` — this is the gate `pika check` will run red after apply, and Preview never ran it", lint)
	}
	if lint.Command != "go vet ./..." {
		t.Errorf("baseline lint command = %q, want the go@1 autofill hint", lint.Command)
	}
}

// A Go module whose real code is clean gets no autofill baseline
// failure: the fix must not turn every ordinary adoption into a false
// warning.
func TestPreviewAutofillBaselinePassesOnCleanCode(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/vetclean\n\ngo 1.26\n")
	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")

	rep, err := Preview(root)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	lint := findBaseline(t, rep.BaselineChecks, "lint")
	if lint.Status != "pass" {
		t.Errorf("baseline lint = %+v, want pass on real clean code", lint)
	}
}

func TestPreviewCommittedContractRejected(t *testing.T) {
	root := messyFixture(t)
	writeFile(t, root, ".project/contract.yaml",
		"schema: 1\nproject:\n  name: messy\n  topology: single\nprofiles: [core@1]\ngithub:\n  merge: squash\nevidence:\n  publish: sanitized\n")
	before := treeDigest(t, root, false)

	rep, err := Preview(root)
	if err == nil {
		t.Fatal("expected an error for an already-adopted repository")
	}
	if rep != nil {
		t.Errorf("expected no report, got %+v", rep)
	}
	if !strings.Contains(err.Error(), "check") {
		t.Errorf("error should direct to check/upgrade, got: %v", err)
	}
	if after := treeDigest(t, root, false); before != after {
		t.Fatal("adopt wrote files when a committed contract exists")
	}
}

// A large adoption's naming convention must not turn into a wall of
// paths: the prose caps at conventionDetailSampleSize with a "+K more"
// tail, but nothing about the data itself narrows — every path still
// gets its own exception record and its own line in the review bundle.
func TestConventionDetailCapsPathsButExceptionsStayComplete(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/big\n\ngo 1.26\n")
	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	const total = 8 // more than conventionDetailSampleSize (5)
	var want []string
	for i := range total {
		rel := fmt.Sprintf("utils/file%d.go", i)
		writeFile(t, root, rel, "package utils\n")
		want = append(want, rel)
	}

	rep, err := Preview(root)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	c := findConvention(t, rep.ConventionMap, "naming/naming-catch-all")
	if c.Status != StatusException {
		t.Fatalf("catch-all status = %q, want %q", c.Status, StatusException)
	}
	if !strings.Contains(c.Detail, fmt.Sprintf("%d pre-existing path(s)", total)) {
		t.Errorf("detail does not lead with the true count %d: %q", total, c.Detail)
	}
	if !strings.Contains(c.Detail, fmt.Sprintf("+%d more", total-conventionDetailSampleSize)) {
		t.Errorf("detail does not name how many were left out: %q", c.Detail)
	}
	if !strings.Contains(c.Detail, checks.ExceptionsFile) {
		t.Errorf("detail does not point at where the full list lives: %q", c.Detail)
	}
	if strings.Count(c.Detail, "utils/file") != conventionDetailSampleSize {
		t.Errorf("detail names %d paths directly, want exactly %d: %q",
			strings.Count(c.Detail, "utils/file"), conventionDetailSampleSize, c.Detail)
	}

	// The record is complete regardless of what the prose shows.
	if len(rep.Exceptions) != total {
		t.Fatalf("Report.Exceptions = %d records, want %d", len(rep.Exceptions), total)
	}
	var gotPaths []string
	for _, ex := range rep.Exceptions {
		gotPaths = append(gotPaths, ex.Path)
	}
	slices.Sort(gotPaths)
	slices.Sort(want)
	if !slices.Equal(gotPaths, want) {
		t.Errorf("Report.Exceptions paths = %v, want %v", gotPaths, want)
	}

	// The review bundle's exceptions section is untouched by the cap:
	// every path still gets its own line there.
	bundle, err := os.ReadFile(filepath.Join(root, ReviewPath))
	if err != nil {
		t.Fatalf("read review bundle: %v", err)
	}
	for _, rel := range want {
		if !strings.Contains(string(bundle), rel) {
			t.Errorf("review bundle is missing %s despite the capped convention detail", rel)
		}
	}
}

func TestPreviewDraftOverwrite(t *testing.T) {
	root := messyFixture(t)
	if _, err := Preview(root); err != nil {
		t.Fatalf("first Preview: %v", err)
	}
	c1, err := os.ReadFile(filepath.Join(root, ".project", "contract.yaml.draft"))
	if err != nil {
		t.Fatal(err)
	}

	// Only .draft files present: adopt replaces them instead of erroring.
	rep2, err := Preview(root)
	if err != nil {
		t.Fatalf("second Preview with existing drafts: %v", err)
	}
	if len(rep2.Preview) != 2 {
		t.Errorf("expected 2 preview diffs, got %d", len(rep2.Preview))
	}
	c2, err := os.ReadFile(filepath.Join(root, ".project", "contract.yaml.draft"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c1, c2) {
		t.Error("re-adopt changed the draft contract content")
	}
}
