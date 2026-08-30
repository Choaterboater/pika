package apply

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/adopt"
	"github.com/Choaterboater/pika/internal/checks"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/initcmd"
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

// adoptionFixture builds a messy Go repository with one warning-level
// naming deviation pair (MyNotes.md, Common/tools.go), an existing
// README (so apply exercises create-if-missing on it), and make checks
// discover can map to contract commands. It then runs adopt so the two
// drafts and the review bundle exist.
func adoptionFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/happy\n\ngo 1.26\n")
	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, root, "Makefile", ".PHONY: test lint fmt build\n\n"+
		"test:\n\techo test-ok\n"+
		"lint:\n\techo lint-ok\n"+
		"fmt:\n\techo fmt-ok\n"+
		"build:\n\techo build-ok\n")
	writeFile(t, root, "MyNotes.md", "# notes\n")
	writeFile(t, root, "Common/tools.go", "package tools\n")
	writeFile(t, root, "README.md", "# happy\n")
	if _, err := adopt.Preview(root); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	return root
}

// treeDigest hashes every file under root (path + content) into one
// digest: apply's rollback must restore the exact pre-state.
func treeDigest(t *testing.T, root string) string {
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
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		parts = append(parts, filepath.ToSlash(rel)+":"+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

func appliedPaths(rep Report) []string {
	out := make([]string, 0, len(rep.Applied))
	for _, a := range rep.Applied {
		out = append(out, a.Path)
	}
	return out
}

func skippedPaths(rep Report) []string {
	out := make([]string, 0, len(rep.Skipped))
	for _, s := range rep.Skipped {
		out = append(out, s.Path)
	}
	return out
}

func readBytes(t *testing.T, root, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return data
}

// TestApplyHappyPath runs the full loop on a messy fixture: the drafts
// promoted unchanged apart from the command slots apply fills from pack
// hints, exceptions record written from the draft contract's recorded
// exceptions, the four missing core files rendered exactly as init
// would, the user's README kept, the recovery journal retired, and gate
// 1 green.
func TestApplyHappyPath(t *testing.T) {
	root := adoptionFixture(t)
	lockYAML := readBytes(t, root, ".project/profiles.lock.draft")

	rep, err := Run(RunOptions{Dir: root})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rep.Rollback {
		t.Error("report.Rollback = true, want false")
	}

	// The lock promotes byte-for-byte. The contract promotes unchanged
	// except for command slots the draft left empty, which apply fills
	// from the packs' hints when the tool is present — without that a
	// repository can be applied with gates that silently skip. Every
	// command adoption discovered survives untouched.
	if got := readBytes(t, root, ".project/profiles.lock"); !bytes.Equal(got, lockYAML) {
		t.Error("committed lock is not the draft lock bytes")
	}
	appliedContract, err := contract.Load(filepath.Join(root, ".project", "contract.yaml"))
	if err != nil {
		t.Fatalf("applied contract is invalid: %v", err)
	}
	draftContract, err := contract.Load(filepath.Join(root, ".project", "contract.yaml.draft"))
	if err != nil {
		t.Fatalf("draft contract is invalid: %v", err)
	}
	for id, want := range draftContract.Commands {
		if got := appliedContract.Commands[id]; got != want {
			t.Errorf("applied commands[%s] = %q, want the draft's %q", id, got, want)
		}
	}
	// The fixture's Makefile gives adopt format, lint, and test; go@1
	// leaves typecheck a discovery sentinel whose autofillable hint is
	// `go build -o /dev/null ./...` (plain `go build ./...` would drop a
	// linked binary in the repository root every time the gate ran).
	if got := appliedContract.Commands["typecheck"]; got != "go build -o /dev/null ./..." {
		t.Errorf("applied commands[typecheck] = %q, want the go@1 hint %q", got, "go build -o /dev/null ./...")
	}
	// Nothing outside commands is rewritten at promotion time.
	appliedContract.Commands = draftContract.Commands
	if !reflect.DeepEqual(appliedContract, draftContract) {
		t.Errorf("promoted contract differs from the draft outside commands:\napplied = %+v\ndraft   = %+v", appliedContract, draftContract)
	}

	// Exceptions record written from the draft's recorded exceptions.
	exc, err := checks.LoadExceptions(root)
	if err != nil {
		t.Fatalf("exceptions record failed to load: %v", err)
	}
	for _, want := range []string{"MyNotes.md", "Common/tools.go"} {
		if _, ok := exc[want]; !ok {
			t.Errorf("exceptions record missing %s: %v", want, exc)
		}
	}
	for _, ex := range exc {
		if ex.Owner != "pika adopt" || ex.ReviewCondition == "" || ex.Reason == "" {
			t.Errorf("exception %s is missing spec fields: %+v", ex.Path, ex)
		}
	}

	// The plan: three state files + four core files; README skipped.
	// Plan order: the state files promote first (contract, lock,
	// exceptions), then the core files in the core pack's required
	// order.
	wantApplied := []string{
		".project/contract.yaml",
		".project/profiles.lock",
		".project/exceptions.yaml",
		"AGENTS.md",
		"CONTRIBUTING.md",
		".github/workflows/ci.yml",
		".github/pull_request_template.md",
	}
	if got := appliedPaths(rep); !slices.Equal(got, wantApplied) {
		t.Errorf("applied = %v, want %v", got, wantApplied)
	}
	if got := skippedPaths(rep); !slices.Equal(got, []string{"README.md"}) {
		t.Errorf("skipped = %v, want [README.md]", got)
	}

	// The four missing core files rendered exactly as init renders
	// them. CoreFiles renders five; the fixture's README was skipped
	// create-if-missing, so it never entered the plan.
	draft, err := contract.Load(filepath.Join(root, ".project", "contract.yaml.draft"))
	if err != nil {
		t.Fatal(err)
	}
	wantCore, err := initcmd.CoreFiles("go", draft.Project.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(wantCore) != 5 {
		t.Fatalf("core renders %d files, want 5", len(wantCore))
	}
	for _, rel := range wantApplied {
		want, ok := wantCore[rel]
		if !ok {
			continue // state files, not core renders
		}
		if got := readBytes(t, root, rel); !bytes.Equal(got, want) {
			t.Errorf("%s differs from the init-rendered core file", rel)
		}
	}

	// The recovery journal is retired: no journal files remain under
	// the recovery directory (txn keeps the directory itself).
	entries, err := os.ReadDir(filepath.Join(root, ".project", "state", "recovery"))
	if err == nil && len(entries) > 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("recovery journal not retired: %v", names)
	}

	// Gate 1 on the applied contract: the recorded exceptions suppress
	// the naming warnings, so the gate passes.
	if !rep.Gate1.Pass {
		t.Errorf("gate 1 failed: %s", rep.Gate1.Output)
	}

	// The visible review bundle says APPLIED.
	review := readBytes(t, root, adopt.ReviewPath)
	if !strings.Contains(string(review), "Status: **APPLIED**") {
		t.Errorf("review bundle not marked APPLIED:\n%s", review)
	}
}

// TestApplyRefusedWhenAlreadyAdopted pins the fail-closed precondition:
// a second apply is refused and changes nothing, the review bundle
// included.
func TestApplyRefusedWhenAlreadyAdopted(t *testing.T) {
	root := adoptionFixture(t)
	if _, err := Run(RunOptions{Dir: root}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	before := treeDigest(t, root)

	rep, err := Run(RunOptions{Dir: root})
	if err == nil {
		t.Fatal("second apply: want already-adopted error, got nil")
	}
	if !strings.Contains(err.Error(), "already adopted") {
		t.Errorf("error = %v, want already adopted", err)
	}
	if rep.Rollback {
		t.Error("refused run must not report a rollback")
	}
	if after := treeDigest(t, root); after != before {
		t.Error("refused run changed the tree")
	}
}

// TestApplyMissingDrafts pins the draft precondition: both drafts are
// required and the error names what is missing.
func TestApplyMissingDrafts(t *testing.T) {
	root := adoptionFixture(t)

	if err := os.Remove(filepath.Join(root, ".project", "contract.yaml.draft")); err != nil {
		t.Fatal(err)
	}
	_, err := Run(RunOptions{Dir: root})
	if err == nil || !strings.Contains(err.Error(), ".project/contract.yaml.draft") {
		t.Fatalf("error = %v, want missing contract draft", err)
	}

	if err := os.Remove(filepath.Join(root, ".project", "profiles.lock.draft")); err != nil {
		t.Fatal(err)
	}
	_, err = Run(RunOptions{Dir: root})
	if err == nil || !strings.Contains(err.Error(), ".project/profiles.lock.draft") {
		t.Fatalf("error = %v, want missing lock draft", err)
	}
	for _, draft := range []string{".project/contract.yaml", ".project/profiles.lock", ".project/exceptions.yaml"} {
		if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(draft))); statErr == nil {
			t.Errorf("refused run wrote %s", draft)
		}
	}
}

// TestApplyInvalidDraft pins draft validation: the schema (and strict
// parsing) apply to drafts too, and nothing is written.
func TestApplyInvalidDraft(t *testing.T) {
	root := adoptionFixture(t)

	// Out-of-range schema version.
	writeFile(t, root, ".project/contract.yaml.draft",
		"schema: 99\nproject:\n  name: happy\n  topology: single\nprofiles: [core@1]\ngithub:\n  merge: squash\nevidence:\n  publish: sanitized\n")
	if _, err := Run(RunOptions{Dir: root}); err == nil {
		t.Fatal("invalid draft: want error, got nil")
	}
	// Unknown top-level key (strict YAML).
	writeFile(t, root, ".project/contract.yaml.draft",
		"schema: 1\nprojekt:\n  name: happy\n")
	if _, err := Run(RunOptions{Dir: root}); err == nil {
		t.Fatal("strict draft: want error, got nil")
	}
	if _, err := os.Stat(filepath.Join(root, ".project", "contract.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("refused run wrote the contract: %v", err)
	}
}

// TestApplyCreateIfMissing pins create-if-missing: a file the user
// created between adopt and apply is preserved and reported as skipped.
func TestApplyCreateIfMissing(t *testing.T) {
	root := adoptionFixture(t)
	writeFile(t, root, "AGENTS.md", "# my own agents notes\n")

	rep, err := Run(RunOptions{Dir: root})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := readBytes(t, root, "AGENTS.md"); string(got) != "# my own agents notes\n" {
		t.Errorf("AGENTS.md = %q, want the user's content", got)
	}
	if !slices.Contains(skippedPaths(rep), "AGENTS.md") {
		t.Errorf("skipped = %v, want AGENTS.md reported as skipped", rep.Skipped)
	}
	if slices.Contains(appliedPaths(rep), "AGENTS.md") {
		t.Error("AGENTS.md reported as applied")
	}
}

// TestApplyRollbackOnMidPlanFailure injects a mid-plan failure and pins
// the documented Begin → Apply → Rollback contract: any error after
// Begin rolls the repository back to its byte-identical pre-state.
func TestApplyRollbackOnMidPlanFailure(t *testing.T) {
	root := adoptionFixture(t)
	before := treeDigest(t, root)

	rep, err := Run(RunOptions{Dir: root, failAfter: 2})
	if err == nil {
		t.Fatal("injected failure: want error, got nil")
	}
	if !rep.Rollback {
		t.Error("report.Rollback = false, want true")
	}
	if after := treeDigest(t, root); after != before {
		t.Error("rollback did not restore the pre-state")
	}
	// The failure left the repository adoptable again: a new apply with
	// the same drafts succeeds.
	if _, err := Run(RunOptions{Dir: root}); err != nil {
		t.Fatalf("apply after rollback: %v", err)
	}
}

// TestApplyGate1FailureIsHonest pins the post-apply sanity contract: a
// failing gate 1 is reported but does NOT roll back, because the applied
// state is valid — it just carries findings.
func TestApplyGate1FailureIsHonest(t *testing.T) {
	root := adoptionFixture(t)
	writeFile(t, root, "utils/helpers.go", "package helpers\n")
	if _, err := adopt.Preview(root); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	rep, err := Run(RunOptions{Dir: root})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rep.Gate1.Pass {
		t.Fatal("gate 1 passed on a banned catch-all, want failure")
	}
	if !strings.Contains(rep.Gate1.Output, "naming-catch-all") {
		t.Errorf("gate 1 output = %q, want the catch-all finding", rep.Gate1.Output)
	}
	if rep.Rollback {
		t.Error("gate-1 failure must not roll back")
	}
	if _, err := os.Stat(filepath.Join(root, ".project", "contract.yaml")); err != nil {
		t.Errorf("applied contract missing after gate-1 failure: %v", err)
	}
	review := string(readBytes(t, root, adopt.ReviewPath))
	if !strings.Contains(review, "FAIL") || !strings.Contains(review, "naming-catch-all") {
		t.Errorf("review bundle does not report the gate-1 failure honestly:\n%s", review)
	}
}

// TestApplyCommitFailureReportsRollbackFailure pins honest reporting
// when the undo is impossible: a commit failure finishes the
// transaction, so Rollback is refused and the applied mutations remain
// on disk. The report must NOT claim a rollback, and the error must
// point at the recovery state instead of claiming the pre-state.
func TestApplyCommitFailureReportsRollbackFailure(t *testing.T) {
	root := adoptionFixture(t)

	rep, err := Run(RunOptions{Dir: root, failCommit: true})
	if err == nil {
		t.Fatal("injected commit failure: want error, got nil")
	}
	if rep.Rollback {
		t.Error("report.Rollback = true although the undo was refused")
	}
	if !strings.Contains(err.Error(), "ROLLBACK FAILED") {
		t.Errorf("error = %v, want an explicit rollback-failure report", err)
	}
	if !strings.Contains(err.Error(), ".project/state/recovery") {
		t.Errorf("error = %v, want a pointer to the recovery state", err)
	}

	// The commit completed before the injected failure: the applied
	// state is durable, which is exactly why the old "rolled back to
	// the pre-state" claim was a lie.
	if _, err := os.Stat(filepath.Join(root, ".project", "contract.yaml")); err != nil {
		t.Errorf("applied contract missing after commit failure: %v", err)
	}
	if _, err := contract.Load(filepath.Join(root, ".project", "contract.yaml")); err != nil {
		t.Errorf("applied contract is invalid: %v", err)
	}
}

// The two kernel-owned core files, seeded with content the current pack
// does not render: the state an operator lands in when their repository
// was scaffolded by an older kernel whose template has since been
// corrected.
const (
	staleCI = "# stale workflow from an older kernel\n"
	stalePR = "## stale PR template from an older kernel\n"
)

func seedStaleKernelFiles(t *testing.T, root string) {
	t.Helper()
	writeFile(t, root, ".github/workflows/ci.yml", staleCI)
	writeFile(t, root, ".github/pull_request_template.md", stalePR)
}

// appliedOp returns the reported operation kind for rel, or "" when the
// report does not mention it.
func appliedOp(rep Report, rel string) string {
	for _, a := range rep.Applied {
		if a.Path == rel {
			return a.Op
		}
	}
	return ""
}

// treeSnapshot maps every repository-relative path under root to a
// digest of its content, so two snapshots name exactly which files a run
// changed rather than only that something changed.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
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
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// bookkeeping is the state apply maintains outside the operation plan:
// the visible review bundle it rewrites after the commit, and the txn
// journal and backups. Neither is a repository file the report is
// accounting for.
func bookkeeping(rel string) bool {
	return rel == adopt.ReviewPath || strings.HasPrefix(rel, ".project/state/")
}

// renderedCore renders the core files exactly as init would for the
// fixture's draft contract, which is what a refreshed kernel-owned file
// must equal byte-for-byte.
func renderedCore(t *testing.T, root string) map[string][]byte {
	t.Helper()
	draft, err := contract.Load(filepath.Join(root, ".project", "contract.yaml.draft"))
	if err != nil {
		t.Fatal(err)
	}
	core, err := initcmd.CoreFiles("go", draft.Project.Name)
	if err != nil {
		t.Fatal(err)
	}
	return core
}

// TestApplyRefreshesAStaleKernelOwnedFile pins the kernel's half of the
// ownership split: the PR template and the CI workflow encode how the
// kernel wants to be run, so a copy left behind by an older kernel is a
// defect the kernel corrects. Without this, a repository whose
// scaffolded CI predates a template fix is told its lock is stale and
// given no supported remedy.
func TestApplyRefreshesAStaleKernelOwnedFile(t *testing.T) {
	root := adoptionFixture(t)
	seedStaleKernelFiles(t, root)

	rep, err := Run(RunOptions{Dir: root})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	core := renderedCore(t, root)

	for _, rel := range []string{".github/workflows/ci.yml", ".github/pull_request_template.md"} {
		if got := readBytes(t, root, rel); !bytes.Equal(got, core[rel]) {
			t.Errorf("%s was not refreshed:\ngot  %q\nwant %q", rel, got, core[rel])
		}
		if op := appliedOp(rep, rel); op != "write" {
			t.Errorf("applied op for %s = %q, want %q", rel, op, "write")
		}
		if slices.Contains(skippedPaths(rep), rel) {
			t.Errorf("%s reported as skipped although it was rewritten", rel)
		}
	}

	// A refresh is a correction, not a reason to fail: the applied
	// state still passes gate 1.
	if !rep.Gate1.Pass {
		t.Errorf("gate 1 failed after a refresh: %s", rep.Gate1.Output)
	}
}

// TestApplyKernelOwnedFileAlreadyCurrentIsNotRewritten pins the other
// half: a kernel-owned file that already matches the rendered template
// is left alone and reported as such. A refresh the operator can see is
// the point; a write op for identical bytes is noise.
func TestApplyKernelOwnedFileAlreadyCurrentIsNotRewritten(t *testing.T) {
	root := adoptionFixture(t)
	// Seed both kernel-owned files with exactly what the pack renders.
	core, err := initcmd.CoreFiles("go", "happy")
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".github/workflows/ci.yml", ".github/pull_request_template.md"} {
		writeFile(t, root, rel, string(core[rel]))
	}

	rep, err := Run(RunOptions{Dir: root})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, rel := range []string{".github/workflows/ci.yml", ".github/pull_request_template.md"} {
		if slices.Contains(appliedPaths(rep), rel) {
			t.Errorf("%s reported as applied although it already matched the template", rel)
		}
		if !slices.Contains(skippedPaths(rep), rel) {
			t.Errorf("skipped = %v, want %s reported as already current", rep.Skipped, rel)
		}
	}
}

// TestApplyNeverTouchesAnOperatorOwnedFile is the guard on the refresh:
// README.md, AGENTS.md, CONTRIBUTING.md and go.mod are the operator's
// the moment they exist. go.mod in particular carries the repository's
// dependency graph, and the kernel must never rewrite it.
func TestApplyNeverTouchesAnOperatorOwnedFile(t *testing.T) {
	root := adoptionFixture(t)
	operator := map[string]string{
		"README.md":       "# happy\n\nMy own words, nothing like the scaffold.\n",
		"AGENTS.md":       "# my own agent notes\n",
		"CONTRIBUTING.md": "# how we actually contribute\n",
		"go.mod":          "module example.com/happy\n\ngo 1.26\n\nrequire example.com/dep v1.2.3\n",
	}
	for rel, content := range operator {
		writeFile(t, root, rel, content)
	}
	// Stale kernel-owned files too: the refresh must not become a
	// licence to rewrite the neighbours.
	seedStaleKernelFiles(t, root)

	rep, err := Run(RunOptions{Dir: root})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	for rel, want := range operator {
		if got := string(readBytes(t, root, rel)); got != want {
			t.Errorf("%s was rewritten:\ngot  %q\nwant %q", rel, got, want)
		}
		if slices.Contains(appliedPaths(rep), rel) {
			t.Errorf("%s reported as applied; operator-owned files are create-if-missing", rel)
		}
	}
	for _, rel := range []string{"README.md", "AGENTS.md", "CONTRIBUTING.md"} {
		if !slices.Contains(skippedPaths(rep), rel) {
			t.Errorf("skipped = %v, want %s kept and reported", rep.Skipped, rel)
		}
	}
}

// TestApplyReportsEveryRefresh pins the visibility contract by
// construction rather than by enumeration: every repository file apply
// changed, added or removed must appear in the report. A silent kernel
// rewrite is indistinguishable from an operator's own edit.
func TestApplyReportsEveryRefresh(t *testing.T) {
	root := adoptionFixture(t)
	seedStaleKernelFiles(t, root)
	before := treeSnapshot(t, root)

	rep, err := Run(RunOptions{Dir: root})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	after := treeSnapshot(t, root)

	reported := map[string]bool{}
	for _, a := range rep.Applied {
		reported[a.Path] = true
	}
	for rel, sum := range after {
		if bookkeeping(rel) || before[rel] == sum {
			continue
		}
		if !reported[rel] {
			t.Errorf("apply changed %s without reporting it: applied = %v", rel, rep.Applied)
		}
	}
	for rel := range before {
		if bookkeeping(rel) {
			continue
		}
		if _, ok := after[rel]; !ok && !reported[rel] {
			t.Errorf("apply removed %s without reporting it: applied = %v", rel, rep.Applied)
		}
	}
	// The refreshes are genuinely in there — the loops above would also
	// pass on a run that changed nothing.
	for _, rel := range []string{".github/workflows/ci.yml", ".github/pull_request_template.md"} {
		if !reported[rel] {
			t.Errorf("report is missing the refresh of %s: applied = %v", rel, rep.Applied)
		}
	}
}

// TestApplyRefreshRollsBackWithTheTransaction pins that a refresh goes
// through the transactional path rather than around it: an injected
// failure after the whole plan has been applied must restore the stale
// bytes the refresh overwrote. A rewrite that cannot roll back is a
// worse failure than a stale template.
func TestApplyRefreshRollsBackWithTheTransaction(t *testing.T) {
	root := adoptionFixture(t)
	seedStaleKernelFiles(t, root)
	before := treeDigest(t, root)

	// failAfter past the end of the plan applies every operation — both
	// kernel-owned refreshes included — and then fails, so the undo has
	// to restore what the writes overwrote, not merely remove creates.
	rep, err := Run(RunOptions{Dir: root, failAfter: 99})
	if err == nil {
		t.Fatal("injected failure: want error, got nil")
	}
	if !rep.Rollback {
		t.Errorf("report.Rollback = false, want true (err = %v)", err)
	}
	if got := string(readBytes(t, root, ".github/workflows/ci.yml")); got != staleCI {
		t.Errorf("ci.yml after rollback = %q, want the stale pre-state %q", got, staleCI)
	}
	if got := string(readBytes(t, root, ".github/pull_request_template.md")); got != stalePR {
		t.Errorf("PR template after rollback = %q, want the stale pre-state %q", got, stalePR)
	}
	if after := treeDigest(t, root); after != before {
		t.Error("rollback did not restore the byte-identical pre-state")
	}

	// The assertions above would also hold if the refresh had never
	// entered the plan, so prove it did: with the pre-state restored,
	// the same drafts apply cleanly and the writes happen for real.
	rep, err = Run(RunOptions{Dir: root})
	if err != nil {
		t.Fatalf("apply after rollback: %v", err)
	}
	for _, rel := range []string{".github/workflows/ci.yml", ".github/pull_request_template.md"} {
		if op := appliedOp(rep, rel); op != "write" {
			t.Errorf("applied op for %s = %q, want %q — the rolled-back run had nothing to undo", rel, op, "write")
		}
	}
}
