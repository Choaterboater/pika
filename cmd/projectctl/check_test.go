package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/projectctl/internal/verify"
)

// writeFixture lays down a minimal contract plus optional lint script in a
// fresh temp directory and changes into it for the duration of the test.
func writeFixture(t *testing.T, commands string, lintScript string) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	contract := `schema: 1
project:
  name: fixture
  topology: single
profiles: [core@1]
github:
  merge: squash
evidence:
  publish: sanitized
commands:
` + commands
	if err := os.WriteFile(filepath.Join(dir, ".project", "contract.yaml"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}
	if lintScript != "" {
		path := filepath.Join(dir, "lint.sh")
		if err := os.WriteFile(path, []byte(lintScript), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// golden JSON contract: {gates:[{id,cmd,exit,durationMs,outputTail,status}],
// summary:{pass,fail,skip},pass:boolean} plus baseline/regressions and
// warnings for reserved scopes.

func TestCheckPassingFixtureGoldenJSON(t *testing.T) {
	writeFixture(t, `  format: "true"
  lint: "true"
  typecheck: "true"
  test: "true"
  smoke: "true"
`, "")

	var stdout, stderr bytes.Buffer
	code := runCheck([]string{"--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr.String())
	}
	var rep verify.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("stdout not valid JSON report: %v\n%s", err, stdout.String())
	}
	if !rep.Pass || rep.Summary.Fail != 0 || rep.Summary.Pass != 6 {
		t.Fatalf("report = %+v, want pass with 6 passing gates (contract + 5)", rep)
	}
	if rep.Gates[0].ID != "contract" {
		t.Fatalf("first gate = %q, want contract (ladder rung 1)", rep.Gates[0].ID)
	}
}

func TestCheckFailingLintFixtureGoldenJSON(t *testing.T) {
	writeFixture(t, `  format: "true"
  lint: ./lint.sh
  typecheck: "true"
  test: "true"
  smoke: "true"
`, "#!/bin/sh\necho 'lint failure: unexpected semicolon'\nexit 1\n")

	var stdout, stderr bytes.Buffer
	code := runCheck([]string{"--json"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr: %s", code, stderr.String())
	}
	var rep verify.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("stdout not valid JSON report: %v\n%s", err, stdout.String())
	}
	if rep.Pass {
		t.Fatal("report.Pass = true, want false")
	}
	if len(rep.Regressions) != 1 || rep.Regressions[0].Gate != "lint" {
		t.Fatalf("regressions = %+v, want one for lint", rep.Regressions)
	}
	if !strings.Contains(rep.Regressions[0].Detail, "unexpected semicolon") {
		t.Fatalf("regression detail %q lost the lint output", rep.Regressions[0].Detail)
	}
	if rep.Gates[0].ID != "contract" || rep.Gates[0].Status != verify.StatusPass {
		t.Fatalf("contract gate = %+v, want pass", rep.Gates[0])
	}
	// Gates after lint depend on it: not run.
	for _, g := range rep.Gates {
		if g.ID == "typecheck" || g.ID == "test" || g.ID == "smoke" {
			if g.Status != verify.StatusSkip || g.Reason == "" {
				t.Fatalf("gate %s = %+v, want skip with reason (lint failed)", g.ID, g)
			}
		}
	}
	if rep.Summary.Fail != 1 || rep.Summary.Skip != 3 || rep.Summary.Pass != 2 {
		t.Fatalf("summary = %+v, want pass=2 fail=1 skip=3", rep.Summary)
	}
}

func TestCheckDiscoverySentinelsSkippedWithReason(t *testing.T) {
	// No commands block: every profile slot is a discovery sentinel with
	// no discovered command.
	writeFixture(t, `  format: "true"
`, "")
	var stdout, stderr bytes.Buffer
	code := runCheck([]string{"--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; skips are not failures; stderr: %s", code, stderr.String())
	}
	var rep verify.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Skip != 4 || rep.Summary.Pass != 2 {
		t.Fatalf("summary = %+v, want pass=2 skip=4", rep.Summary)
	}
	for _, g := range rep.Gates {
		if g.Status == verify.StatusSkip && !strings.Contains(g.Reason, "no command discovered") {
			t.Fatalf("skip reason %q missing discovery explanation", g.Reason)
		}
	}
}

func TestCheckUsageAndConfigErrorsExit2(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// Unknown flag.
	var stdout, stderr bytes.Buffer
	if code := runCheck([]string{"--bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown flag exit = %d, want 2", code)
	}
	// Missing contract.
	stdout.Reset()
	stderr.Reset()
	if code := runCheck(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("missing contract exit = %d, want 2; stderr: %s", code, stderr.String())
	}
	// Mutually exclusive scopes.
	stdout.Reset()
	stderr.Reset()
	if code := runCheck([]string{"--all", "--ci"}, &stdout, &stderr); code != 2 {
		t.Fatalf("conflicting scopes exit = %d, want 2", code)
	}
	// Extra positional argument.
	stdout.Reset()
	stderr.Reset()
	if code := runCheck([]string{"junk"}, &stdout, &stderr); code != 2 {
		t.Fatalf("extra arg exit = %d, want 2", code)
	}
}

func TestCheckChangedScopeWarnsInJSON(t *testing.T) {
	writeFixture(t, `  test: "true"
`, "")
	var stdout, stderr bytes.Buffer
	if code := runCheck([]string{"--json", "--changed"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr.String())
	}
	var rep verify.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) == 0 {
		t.Fatal("--changed must record a warning in the JSON report")
	}
}

func TestCheckCIScopeRunsAll(t *testing.T) {
	writeFixture(t, `  test: "true"
  smoke: "true"
`, "")
	var stdout, stderr bytes.Buffer
	if code := runCheck([]string{"--json", "--ci"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr.String())
	}
	var rep verify.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Pass != 3 || rep.Summary.Fail != 0 {
		t.Fatalf("summary = %+v, want contract+test+smoke passing", rep.Summary)
	}
}

func TestCheckContractSchemaTooNewFailsGate1(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	contract := `schema: 99
project:
  name: fixture
  topology: single
profiles: [core@1]
github:
  merge: squash
evidence:
  publish: sanitized
`
	if err := os.WriteFile(filepath.Join(dir, ".project", "contract.yaml"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runCheck([]string{"--json"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1; stderr: %s", code, stderr.String())
	}
	var rep verify.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Gates[0].ID != "contract" || rep.Gates[0].Status != verify.StatusFail {
		t.Fatalf("contract gate = %+v, want fail (schema ceiling)", rep.Gates[0])
	}
	if !strings.Contains(rep.Gates[0].OutputTail, "supported") {
		t.Fatalf("gate output %q should name the schema ceiling", rep.Gates[0].OutputTail)
	}
	// Everything downstream is skipped.
	if rep.Summary.Skip != 5 {
		t.Fatalf("summary = %+v, want 5 downstream skips", rep.Summary)
	}
}

// writeCheckFixture extends writeFixture with a file tree laid out in the
// fixture repository.
func writeCheckFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := writeFixture(t, `  format: "true"
  lint: "true"
  typecheck: "true"
  test: "true"
  smoke: "true"
`, "")
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const validException = `src/utils/helpers.ts:
  rule-id: naming-catch-all
  reason: legacy module pending split
  owner: alice
  review-condition: revisit at the 2026-10 architecture sync
`

func TestCheckNamingCatchAllFailsGate1(t *testing.T) {
	writeCheckFixture(t, map[string]string{"src/utils/helpers.ts": "// helpers\n"})
	var stdout, stderr bytes.Buffer
	if code := runCheck([]string{"--json"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1; stderr: %s", code, stderr.String())
	}
	var rep verify.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	g := rep.Gates[0]
	if g.ID != "contract" || g.Status != verify.StatusFail {
		t.Fatalf("gate 1 = %+v, want a failed contract gate", g)
	}
	if !strings.Contains(g.OutputTail, "naming-catch-all") || !strings.Contains(g.OutputTail, "src/utils/helpers.ts") {
		t.Fatalf("gate output %q should name the rule and path", g.OutputTail)
	}
	// Gate 1 is the top of the ladder: its failure stops gates 2-4.
	if rep.Summary.Skip != 5 {
		t.Fatalf("summary = %+v, want 5 downstream skips", rep.Summary)
	}
	if len(rep.Regressions) == 0 || rep.Regressions[0].Gate != "contract" {
		t.Fatalf("regressions = %+v, want a contract-gate regression", rep.Regressions)
	}
}

func TestCheckNamingExceptionCleansGate1(t *testing.T) {
	writeCheckFixture(t, map[string]string{
		"src/utils/helpers.ts":     "// helpers\n",
		".project/exceptions.yaml": validException,
	})
	var stdout, stderr bytes.Buffer
	if code := runCheck([]string{"--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0 with a valid exception; stderr: %s", code, stderr.String())
	}
	var rep verify.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Pass || rep.Gates[0].Status != verify.StatusPass {
		t.Fatalf("report = %+v, want gate 1 passing", rep)
	}
	if len(rep.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none for an excepted path", rep.Warnings)
	}
}

func TestCheckMalformedExceptionFailsGate1(t *testing.T) {
	writeCheckFixture(t, map[string]string{
		"src/utils/helpers.ts":     "// helpers\n",
		".project/exceptions.yaml": "src/utils/helpers.ts:\n  rule-id: naming-catch-all\n  reason: r\n  review-condition: c\n",
	})
	var stdout, stderr bytes.Buffer
	if code := runCheck([]string{"--json"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1 for a malformed exceptions file; stderr: %s", code, stderr.String())
	}
	var rep verify.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Gates[0].Status != verify.StatusFail || !strings.Contains(rep.Gates[0].OutputTail, "owner") {
		t.Fatalf("gate 1 = %+v, want a load error naming the missing owner", rep.Gates[0])
	}
	if rep.Summary.Skip != 5 {
		t.Fatalf("summary = %+v, want 5 downstream skips", rep.Summary)
	}
}

func TestCheckKebabWarningCarriesInJSON(t *testing.T) {
	writeCheckFixture(t, map[string]string{"src/BadName.go": "package src\n"})
	var stdout, stderr bytes.Buffer
	if code := runCheck([]string{"--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0; warnings never fail the ladder; stderr: %s", code, stderr.String())
	}
	var rep verify.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Gates[0].Status != verify.StatusPass {
		t.Fatalf("gate 1 = %+v, want pass", rep.Gates[0])
	}
	var namingWarning bool
	for _, w := range rep.Warnings {
		if strings.Contains(w, "naming-kebab-case") && strings.Contains(w, "src/BadName.go") {
			namingWarning = true
		}
	}
	if !namingWarning {
		t.Fatalf("warnings = %v, want a naming-kebab-case warning for src/BadName.go", rep.Warnings)
	}
}
