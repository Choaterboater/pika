package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --reset-docs is the opt-in that restores the scaffold's own text over
// files the operator now owns. It only ever acts on an existing scaffold,
// and an existing scaffold is refused without --force, so on its own the
// flag can only be a mistyped intention. Accepting it silently would let
// an operator believe they had asked for something.
func TestResetDocsWithoutForceIsAUsageError(t *testing.T) {
	dir := t.TempDir()
	code, _, errb := dispatchArgs(t, "init", "--root", dir, "--profile", "go", "--reset-docs")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr %q)", code, errb)
	}
	if !strings.Contains(errb, "--reset-docs requires --force") {
		t.Errorf("stderr does not explain the refusal:\n%s", errb)
	}
	if _, err := os.Stat(filepath.Join(dir, ".project", "contract.yaml")); err == nil {
		t.Error("a usage error still scaffolded a contract")
	}
}

// The flags reach initcmd: --force alone leaves the operator's README
// alone, and --force --reset-docs puts the scaffolded one back.
func TestInitForceAndResetDocsReachTheScaffold(t *testing.T) {
	dir := t.TempDir()
	if code, _, errb := dispatchArgs(t, "init", "--root", dir, "--profile", "go"); code != 0 {
		t.Fatalf("init exit = %d, want 0 (stderr %q)", code, errb)
	}
	readme := filepath.Join(dir, "README.md")
	scaffolded, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	const mine = "# mine\n\nwritten by a human\n"
	if err := os.WriteFile(readme, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, _, errb := dispatchArgs(t, "init", "--root", dir, "--force"); code != 0 {
		t.Fatalf("init --force exit = %d, want 0 (stderr %q)", code, errb)
	}
	got, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != mine {
		t.Errorf("--force rewrote README.md: %q", got)
	}

	if code, _, errb := dispatchArgs(t, "init", "--root", dir, "--force", "--reset-docs"); code != 0 {
		t.Fatalf("init --force --reset-docs exit = %d, want 0 (stderr %q)", code, errb)
	}
	if got, err = os.ReadFile(readme); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(scaffolded) {
		t.Errorf("--reset-docs did not restore README.md:\n got: %q\nwant: %q", got, scaffolded)
	}
}
