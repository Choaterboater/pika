package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/version"
)

func dispatchArgs(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := dispatch(args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func TestBareInvocationPrintsCommandTable(t *testing.T) {
	code, out, _ := dispatchArgs(t)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, name := range []string{"init", "check", "adopt", "apply", "mcp", "help"} {
		if !strings.Contains(out, name) {
			t.Errorf("command table is missing %q:\n%s", name, out)
		}
	}
}

func TestHelpForOneCommand(t *testing.T) {
	code, out, _ := dispatchArgs(t, "help", "check")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "--changed") {
		t.Errorf("help check omitted its flags:\n%s", out)
	}
}

func TestHelpForUnknownCommandExits2(t *testing.T) {
	code, _, errb := dispatchArgs(t, "help", "bogus")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb, "bogus") {
		t.Errorf("stderr did not name the unknown command:\n%s", errb)
	}
}

func TestUnknownCommandExits2(t *testing.T) {
	code, _, errb := dispatchArgs(t, "frobnicate")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb, "frobnicate") {
		t.Errorf("stderr did not name the unknown command:\n%s", errb)
	}
}

func TestVersionOnlyAsFirstArgument(t *testing.T) {
	code, out, _ := dispatchArgs(t, "--version")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("--version printed nothing")
	}
}

// Regression: main.go:13-18 scanned every argument, so `pika check
// --version` printed the version instead of running check. A free-form
// string flag valued "version" hit the same trap.
func TestVersionIsNotHijackedFromFlagPosition(t *testing.T) {
	code, out, _ := dispatchArgs(t, "check", "--version")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (unknown flag for check)", code)
	}
	if strings.Contains(out, "pika ") {
		t.Errorf("version output leaked from a flag position:\n%s", out)
	}
}

// Regression for a live bug on main: `pika improve --branch version`
// printed the version and exited 0, silently doing nothing while
// reporting success. improve's --branch and --agent are the binary's
// first free-form string flags, which is what made the argv scan
// exploitable.
//
// The test runs in an empty temp directory, and that is not incidental.
// improve mutates whatever repository it resolves: without the chdir it
// resolved the pika checkout the test binary was compiled from, created
// a branch named "version" in it, and ran the contract's own test gate —
// which is `go test ./...`, so the suite re-entered itself once pika
// began governing pika. Standing in a directory that is not a project
// makes the outcome depend on dispatch alone: improve cannot succeed
// there, it names itself in its own output, and the version is never
// printed.
func TestImproveIsNotHijackedByAFlagValuedVersion(t *testing.T) {
	t.Chdir(t.TempDir())
	code, out, errb := dispatchArgs(t, "improve", "--branch", "version")
	if code == 0 {
		t.Fatalf("improve reported success in a directory with no project:\n%s", out)
	}
	if strings.TrimSpace(out) == version.String() {
		t.Fatalf("dispatch printed the version instead of running improve: %q", out)
	}
	if !strings.Contains(out+errb, "improve") {
		t.Fatalf("dispatch never reached improve; stdout %q stderr %q", out, errb)
	}
}

func TestEveryCommandHasSummaryAndUsage(t *testing.T) {
	for _, c := range commands {
		if strings.TrimSpace(c.summary) == "" {
			t.Errorf("command %q has no summary", c.name)
		}
		if strings.TrimSpace(c.usage) == "" {
			t.Errorf("command %q has no usage", c.name)
		}
	}
}
