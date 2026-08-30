package main

import (
	"strings"
	"testing"
)

// explain is a reading command: it must answer in a directory that has no
// contract, because the operator most likely to need it is the one who has
// not adopted pika yet.
func TestExplainWorksWithoutAContract(t *testing.T) {
	t.Chdir(t.TempDir())
	code, out, errb := dispatchArgs(t, "explain", "naming-catch-all")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb)
	}
	for _, want := range []string{"naming-catch-all", "core@1", "utils", "exceptions.yaml"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
}

func TestExplainJSONCarriesRationaleAndRemediation(t *testing.T) {
	t.Chdir(t.TempDir())
	code, out, errb := dispatchArgs(t, "explain", "naming-catch-all", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb)
	}
	var entry struct {
		ID          string `json:"id"`
		Kind        string `json:"kind"`
		Rationale   string `json:"rationale"`
		Remediation string `json:"remediation"`
	}
	env := resultOf(t, []byte(out), "explain", &entry)
	if !env.OK {
		t.Errorf("ok = false for an explained id:\n%s", out)
	}
	if entry.ID != "naming-catch-all" || entry.Kind != "naming-rule" {
		t.Errorf("entry = %+v, want the naming-catch-all rule", entry)
	}
	if entry.Rationale == "" || entry.Remediation == "" {
		t.Errorf("entry = %+v, want rationale and remediation", entry)
	}
}

func TestExplainUnknownIDExits2AndListsKnownIDs(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, errb := dispatchArgs(t, "explain", "no-such-rule")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb, "no-such-rule") {
		t.Errorf("stderr does not name the unknown id: %q", errb)
	}
	for _, want := range []string{"naming-catch-all", "typecheck", "envelope_denied"} {
		if !strings.Contains(errb, want) {
			t.Errorf("stderr does not list known id %q: %s", want, errb)
		}
	}
}

// Zero and two-plus positionals are usage errors: explain answers about
// exactly one id, and silently explaining the first of several would hide
// the operator's mistake.
func TestExplainRequiresExactlyOneID(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"explain"},
		{"explain", "typecheck", "test"},
	} {
		code, out, _ := dispatchArgs(t, args...)
		if code != 2 {
			t.Errorf("%v exit = %d, want 2", args, code)
		}
		if out != "" {
			t.Errorf("%v wrote %q to stdout, want nothing", args, out)
		}
	}
}
