package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/adopt"
)

// applyFixture builds a messy Go repository and runs adopt so the
// drafts and the review bundle exist.
func applyFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	write := func(rel, content string) {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/happy\n\ngo 1.26\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	write("README.md", "# happy\n")
	if _, err := adopt.Preview(dir); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	return dir
}

// TestRunApplyPostCommitFailureMessage pins the stderr note for the
// post-commit failure class: when only the review-bundle rewrite fails
// (the bundle path replaced by a directory, so the post-commit
// os.WriteFile fails), the contract IS applied and the CLI must say so
// — not claim a failed pre-state restore.
func TestRunApplyPostCommitFailureMessage(t *testing.T) {
	applyFixture(t)
	if err := os.Remove(filepath.Join("review", "adoption-review.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join("review", "adoption-review.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runApply(nil, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("runApply exit = %d, want 1\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "the contract WAS applied") {
		t.Errorf("stderr missing the applied-state note:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "pre-state could not be restored") {
		t.Errorf("stderr mischaracterizes a post-commit failure as a restore failure:\n%s", stderr.String())
	}
	if _, err := os.Stat(".project/contract.yaml"); err != nil {
		t.Errorf("applied contract missing: %v", err)
	}
}

// TestRunApplyUsage pins the usage-error exit code.
func TestRunApplyUsage(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := runApply([]string{"junk"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("runApply junk exit = %d, want 2", code)
	}
}
