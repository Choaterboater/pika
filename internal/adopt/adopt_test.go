package adopt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Choaterboater/projectctl/internal/contract"
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
// skipping the .project draft files when skipDrafts is set.
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
		if skipDrafts && (rel == ".project/contract.yaml.draft" || rel == ".project/profiles.lock.draft") {
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

	// Naming deviations become recorded exceptions.
	var excPaths []string
	for _, ex := range rep.Exceptions {
		if ex.RuleID != "naming-kebab-case" {
			t.Errorf("unexpected exception rule %q on %s", ex.RuleID, ex.Path)
		}
		for _, field := range []string{ex.Reason, ex.Owner, ex.ReviewCondition} {
			if strings.TrimSpace(field) == "" {
				t.Errorf("exception for %s has an empty required field", ex.Path)
			}
		}
		excPaths = append(excPaths, ex.Path)
	}
	for _, want := range []string{"MyNotes.md", "Common/tools.go"} {
		if !slices.Contains(excPaths, want) {
			t.Errorf("expected a naming exception for %s, got %v", want, excPaths)
		}
	}
	if len(draft.Exceptions) == 0 {
		t.Error("draft contract records no exceptions")
	}

	// Banned catch-alls are conflicts, never exceptions.
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
	for _, ex := range rep.Exceptions {
		if slices.Contains(strings.Split(ex.Path, "/"), "utils") {
			t.Errorf("banned segment excepted instead of conflicted: %s", ex.Path)
		}
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
	if c := findConvention(t, rep.ConventionMap, "naming/naming-catch-all"); c.Status != StatusConflict {
		t.Errorf("catch-all convention status = %q, want %q", c.Status, StatusConflict)
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
