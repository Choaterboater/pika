package changed

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/repopath"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestNoGitDegradesLoudly(t *testing.T) {
	root, err := repopath.At(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := Files(root)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if !set.Degraded {
		t.Fatal("Degraded = false outside a git repository")
	}
	if set.Reason == "" {
		t.Error("degradation carries no reason")
	}
}

func TestWorkingTreeChangesAreDetected(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, filepath.Join(dir, "a.txt"), "one\n")
	gitCommitAll(t, dir, "init")
	writeFile(t, filepath.Join(dir, "b.txt"), "two\n")

	root, _ := repopath.At(dir)
	set, err := Files(root)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if set.Degraded {
		t.Fatalf("unexpected degradation: %s", set.Reason)
	}
	found := false
	for _, p := range set.Paths {
		if p == "b.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Paths = %v, want it to include b.txt", set.Paths)
	}
}

func TestSelectsPackageByPrefix(t *testing.T) {
	set := &Set{Paths: []string{"apps/api/main.go"}}
	if !set.SelectsPackage("apps/api") {
		t.Error("SelectsPackage(apps/api) = false")
	}
	if set.SelectsPackage("apps/web") {
		t.Error("SelectsPackage(apps/web) = true, want false")
	}
	// Prefix matching must be path-segment aware, not string-prefix.
	if set.SelectsPackage("apps/ap") {
		t.Error("SelectsPackage(apps/ap) matched a partial segment")
	}
}

func TestEmptySetIsNotDegraded(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, filepath.Join(dir, "a.txt"), "one\n")
	gitCommitAll(t, dir, "init")

	root, _ := repopath.At(dir)
	set, err := Files(root)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if set.Degraded {
		t.Fatalf("clean tree reported as degraded: %s", set.Reason)
	}
	if !set.Empty() {
		t.Fatalf("Paths = %v, want empty", set.Paths)
	}
}

// A degraded set must widen, never narrow: it selects every package and is
// never reported as empty, so no caller can mistake "unknown" for "nothing
// changed" and skip the gates.
func TestDegradedSetSelectsEverything(t *testing.T) {
	set := degraded("git is not on PATH")
	if !set.SelectsPackage("apps/api") {
		t.Error("degraded set did not select apps/api")
	}
	if !set.SelectsPackage(".") {
		t.Error("degraded set did not select the repository root package")
	}
	if set.Empty() {
		t.Error("Empty() = true for a degraded set; callers would skip gates")
	}
}

// The repository root as a package root is selected by any change at all,
// and by nothing when the tree is clean.
func TestSelectsRootPackage(t *testing.T) {
	set := &Set{Paths: []string{"main.go"}}
	for _, root := range []string{".", "", "./"} {
		if !set.SelectsPackage(root) {
			t.Errorf("SelectsPackage(%q) = false, want true", root)
		}
	}
	clean := &Set{}
	if clean.SelectsPackage(".") {
		t.Error("clean tree selected the root package")
	}
}

// Windows-style package roots come out of contracts edited on Windows;
// they must match the slash-separated paths git reports.
func TestSelectsPackageNormalizesSeparators(t *testing.T) {
	set := &Set{Paths: []string{"apps/api/main.go"}}
	if !set.SelectsPackage(`apps\api`) {
		t.Error(`SelectsPackage("apps\\api") = false`)
	}
	if !set.SelectsPackage("apps/api/") {
		t.Error(`SelectsPackage("apps/api/") = false`)
	}
}

// A file that IS the package root (a single-file package) selects it.
func TestSelectsPackageOnExactPath(t *testing.T) {
	set := &Set{Paths: []string{"tools/gen.go"}}
	if !set.SelectsPackage("tools/gen.go") {
		t.Error("exact path did not select its own package root")
	}
}

// Committed work on a branch counts, not just the dirty working tree:
// otherwise `--changed` would miss everything already committed since the
// fork point.
func TestCommittedChangesSinceMergeBaseAreDetected(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, filepath.Join(dir, "apps", "api", "main.go"), "package main\n")
	gitCommitAll(t, dir, "init")

	// A local "origin/main" makes the fork point resolvable without a
	// network remote.
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	writeFile(t, filepath.Join(dir, "apps", "web", "app.js"), "//\n")
	gitCommitAll(t, dir, "web")

	root, _ := repopath.At(dir)
	set, err := Files(root)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if set.Degraded {
		t.Fatalf("unexpected degradation: %s", set.Reason)
	}
	if !set.SelectsPackage("apps/web") {
		t.Fatalf("Paths = %v, want the committed apps/web change", set.Paths)
	}
	if set.SelectsPackage("apps/api") {
		t.Errorf("Paths = %v, want apps/api untouched since the merge base", set.Paths)
	}
}

// A ref that resolves but shares no history with HEAD is the genuinely
// uncertain case. It must degrade loudly rather than report a change set
// computed from the working tree alone.
func TestUnrelatedHistoryDegrades(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, filepath.Join(dir, "a.txt"), "one\n")
	gitCommitAll(t, dir, "init")

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	original := trimNL(run("rev-parse", "--abbrev-ref", "HEAD"))
	// An orphan branch has no common ancestor with HEAD.
	run("checkout", "-q", "--orphan", "detached")
	writeFile(t, filepath.Join(dir, "b.txt"), "two\n")
	gitCommitAll(t, dir, "orphan")
	orphan := trimNL(run("rev-parse", "HEAD"))
	run("checkout", "-q", "-f", original)
	run("update-ref", "refs/remotes/origin/main", orphan)

	root, _ := repopath.At(dir)
	set, err := Files(root)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if !set.Degraded {
		t.Fatalf("unrelated history did not degrade; Paths = %v", set.Paths)
	}
	if set.Reason == "" {
		t.Error("degradation carries no reason")
	}
}

// A shallow clone has a truncated history, so any merge base it reports is
// an artifact of the depth cut rather than the real fork point. That is
// exactly the case where a narrowed run would silently skip gates, so it
// degrades instead.
func TestShallowCloneDegrades(t *testing.T) {
	src := t.TempDir()
	gitInit(t, src)
	writeFile(t, filepath.Join(src, "a.txt"), "one\n")
	gitCommitAll(t, src, "one")
	writeFile(t, filepath.Join(src, "a.txt"), "two\n")
	gitCommitAll(t, src, "two")

	dst := filepath.Join(t.TempDir(), "shallow")
	clone := exec.Command("git", "clone", "-q", "--depth", "1", "file://"+filepath.ToSlash(src), dst)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Skipf("shallow clone unavailable here: %v\n%s", err, out)
	}

	root, err := repopath.At(dst)
	if err != nil {
		t.Fatal(err)
	}
	set, err := Files(root)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if !set.Degraded {
		t.Fatalf("shallow clone did not degrade; Paths = %v", set.Paths)
	}
	if !strings.Contains(set.Reason, "shallow") {
		t.Errorf("Reason = %q, want it to name the shallow clone", set.Reason)
	}
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
