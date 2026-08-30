package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/verify"
)

// resolved compares paths through EvalSymlinks: macOS t.TempDir() hands
// back /var/... while repopath answers with filepath.Abs, which does not
// resolve the /var -> /private/var symlink.
func resolved(t *testing.T, path string) string {
	t.Helper()
	got, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return got
}

func TestResolveRootFindsAncestorContract(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".project", "contract.yaml"), []byte("schema: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	got, err := resolveRoot("")
	if err != nil {
		t.Fatalf("resolveRoot: %v", err)
	}
	if resolved(t, got.Dir()) != resolved(t, root) {
		t.Fatalf("Dir() = %q, want %q", got.Dir(), root)
	}
}

func TestResolveRootExplicitOverride(t *testing.T) {
	other := t.TempDir()
	t.Chdir(t.TempDir())

	got, err := resolveRoot(other)
	if err != nil {
		t.Fatalf("resolveRoot: %v", err)
	}
	if resolved(t, got.Dir()) != resolved(t, other) {
		t.Fatalf("Dir() = %q, want %q", got.Dir(), other)
	}
}

func TestResolveRootRejectsMissingDir(t *testing.T) {
	if _, err := resolveRoot(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("resolveRoot(missing) = nil error, want error")
	}
}

// Task 3 registered `[--root <dir>]` in every usage string while no
// command defined the flag, so `pika check --root /tmp` exited 2 with
// "flag provided but not defined". Every registered command must now
// accept the flag it advertises.
func TestEveryCommandAcceptsRootFlag(t *testing.T) {
	// A bare directory: no contract, no git. Each command must get past
	// flag parsing; what it then does about the missing contract is its
	// own business and is not asserted here.
	dir := t.TempDir()
	t.Chdir(t.TempDir())
	for _, c := range commands {
		if c.name == "help" {
			continue // help takes a command name, not a root
		}
		if !strings.Contains(c.usage, "--root <dir>") {
			t.Errorf("command %q does not advertise --root: %s", c.name, c.usage)
			continue
		}
		var out, errb bytes.Buffer
		// mcp reads stdin as JSON-RPC; an immediate EOF shuts it down.
		c.run([]string{"--root", dir}, strings.NewReader(""), &out, &errb)
		if strings.Contains(errb.String(), "flag provided but not defined") {
			t.Errorf("command %q rejects the --root it advertises: %s", c.name, errb.String())
		}
	}
}

// An explicit --root is what the command operates on, regardless of the
// working directory: init scaffolds there, not here.
func TestInitScaffoldsIntoExplicitRoot(t *testing.T) {
	target := t.TempDir()
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)

	var out, errb bytes.Buffer
	if code := runInit([]string{"--root", target, "--name", "rooted"}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("init exit = %d; stderr: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(target, ".project", "contract.yaml")); err != nil {
		t.Errorf("init did not scaffold into --root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, ".project")); !os.IsNotExist(err) {
		t.Errorf("init wrote into the working directory: %v", err)
	}
}

// init CREATES a project where the caller is standing; there is nothing
// to discover yet. Discovery would be actively wrong here: repopath.Find
// stops at the enclosing repository's .git, so the workflow
// docs/guides/usage.md §1 documents verbatim —
// `mkdir my-service && cd my-service && pika init --profile go` — would
// scaffold at the git root and still exit 0, leaving my-service empty.
func TestInitScaffoldsInTheWorkingDirectoryNotAnEnclosingRepository(t *testing.T) {
	gitRoot := t.TempDir()
	// The .git marker is exactly what repopath.Find keys on; creating it
	// directly keeps the test hermetic (no git binary required).
	if err := os.MkdirAll(filepath.Join(gitRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	service := filepath.Join(gitRoot, "my-service")
	if err := os.MkdirAll(service, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(service)

	var out, errb bytes.Buffer
	if code := runInit([]string{"--profile", "go", "--name", "my-service"}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("init exit = %d; stderr: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(service, ".project", "contract.yaml")); err != nil {
		t.Errorf("init did not scaffold into the working directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gitRoot, ".project", "contract.yaml")); !os.IsNotExist(err) {
		t.Errorf("init scaffolded into the enclosing repository root: %v", err)
	}
}

// The whole point of Task 1: check must work from a subdirectory.
func TestCheckRunsFromSubdirectory(t *testing.T) {
	root := t.TempDir()
	writeMinimalProject(t, root)
	nested := filepath.Join(root, "internal", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	var out, errb bytes.Buffer
	code := runCheck([]string{"--all", "--json"}, strings.NewReader(""), &out, &errb)
	if code == 2 {
		t.Fatalf("check from subdirectory returned usage error: %s", errb.String())
	}
	// The payload is the shared envelope, so the discriminators are part
	// of the assertion: a consumer standing in a subdirectory must be
	// able to tell which command answered before reading the report.
	var rep verify.Report
	env := resultOf(t, out.Bytes(), "check", &rep)
	if !env.OK {
		t.Errorf("ok = false for a passing check from a subdirectory:\n%s", out.String())
	}
	if len(rep.Gates) == 0 || rep.Gates[0].ID != "contract" {
		t.Fatalf("gate 1 did not run against the discovered root:\n%s", out.String())
	}
	if rep.Gates[0].Status != verify.StatusPass {
		t.Errorf("gate 1 = %s from a subdirectory, want pass: %s", rep.Gates[0].Status, rep.Gates[0].OutputTail)
	}
}

// A command gate must run in the repository root, not in the directory
// check was invoked from: otherwise `go build ./...` and friends would
// verify a subtree and report a false pass.
func TestCheckRunsGatesInTheRepositoryRoot(t *testing.T) {
	root := t.TempDir()
	writeMinimalProject(t, root)
	// Invoked through sh so the argv[0] lookup does not depend on the
	// process working directory; the script path resolves against the
	// child's directory, which is what this test is about.
	writeContract(t, root, "  test: sh ./where.sh\n")
	script := "#!/bin/sh\npwd > pwd.txt\n"
	if err := os.WriteFile(filepath.Join(root, "where.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "internal", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	var out, errb bytes.Buffer
	if code := runCheck([]string{"--all", "--json"}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("check exit = %d; stderr: %s\nstdout: %s", code, errb.String(), out.String())
	}
	got, err := os.ReadFile(filepath.Join(root, "pwd.txt"))
	if err != nil {
		t.Fatalf("gate did not run from the repository root: %v", err)
	}
	if resolved(t, strings.TrimSpace(string(got))) != resolved(t, root) {
		t.Errorf("gate ran in %q, want the repository root %q", strings.TrimSpace(string(got)), root)
	}
}
