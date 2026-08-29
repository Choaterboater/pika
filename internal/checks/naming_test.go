package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/profiles"
)

// coreRules mirrors the resolved core@1 naming rules the check command
// passes to Naming.
func coreRules() []profiles.NamingRule {
	return []profiles.NamingRule{
		{
			RuleID:   "naming-kebab-case",
			Severity: "warning",
			Scope:    "path-segments",
			Pattern:  `^[a-z0-9][a-z0-9._-]*$`,
			Exempt:   []string{"README", "AGENTS", "CONTRIBUTING", "Makefile", "LICENSE", "Dockerfile", "Cargo", "Package", "Sources", "Tests", "__init__"},
		},
		{
			RuleID:   "naming-catch-all",
			Severity: "error",
			Scope:    "path-segments",
			Banned:   []string{"utils", "helpers", "common", "misc", "manager"},
		},
		{
			RuleID:   "file-size-review",
			Severity: "warning",
			Scope:    "file-lines",
			Pattern:  "500",
		},
		{
			RuleID:   "generated-owner",
			Severity: "error",
			Scope:    "generated-patterns",
		},
	}
}

// writeTree lays out files (with content) under dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// findViolation returns the violation for ruleID, failing when absent.
func findViolation(t *testing.T, vs []Violation, ruleID string) Violation {
	t.Helper()
	for _, v := range vs {
		if v.RuleID == ruleID {
			return v
		}
	}
	t.Fatalf("no %s violation in %+v", ruleID, vs)
	return Violation{}
}

// catchAllFixture is the acceptance fixture: a catch-all named source file.
func catchAllFixture(t *testing.T) (string, map[string]Exception) {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"src/utils/helpers.ts": "// helpers\n",
	})
	return dir, map[string]Exception{}
}

func TestNamingCatchAllWithoutException(t *testing.T) {
	dir, exceptions := catchAllFixture(t)
	vs := Naming(dir, coreRules(), exceptions)
	v := findViolation(t, vs, "naming-catch-all")
	if v.Severity != "error" {
		t.Errorf("severity = %q, want error", v.Severity)
	}
	if v.Path != "src/utils/helpers.ts" {
		t.Errorf("path = %q, want src/utils/helpers.ts", v.Path)
	}
}

func TestNamingCatchAllWithException(t *testing.T) {
	dir, _ := catchAllFixture(t)
	writeTree(t, dir, map[string]string{
		".project/exceptions.yaml": "src/utils/helpers.ts:\n  rule-id: naming-catch-all\n  reason: legacy module pending split\n  owner: alice\n  review-condition: revisit at the 2026-10 architecture sync\n",
	})
	exceptions, err := LoadExceptions(dir)
	if err != nil {
		t.Fatalf("LoadExceptions: %v", err)
	}
	if vs := Naming(dir, coreRules(), exceptions); len(vs) != 0 {
		t.Fatalf("excepted fixture produced violations: %+v", vs)
	}
}

func TestNamingExceptionCoversDirectoryScope(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"src/utils/helpers.ts": "// helpers\n",
		"src/utils/load.ts":    "// loader\n",
	})
	// A directory-scoped exception suppresses everything beneath it.
	exceptions := map[string]Exception{
		"src/utils": {
			RuleID:          "naming-catch-all",
			Path:            "src/utils",
			Reason:          "legacy module pending split",
			Owner:           "alice",
			ReviewCondition: "revisit at the 2026-10 architecture sync",
		},
	}
	if vs := Naming(dir, coreRules(), exceptions); len(vs) != 0 {
		t.Fatalf("directory exception did not suppress files beneath it: %+v", vs)
	}
}

func TestNamingExceptionDoesNotLeakAcrossRules(t *testing.T) {
	dir, _ := catchAllFixture(t)
	// The exception targets a different rule: the catch-all finding stands.
	exceptions := map[string]Exception{
		"src/utils/helpers.ts": {
			RuleID:          "naming-kebab-case",
			Path:            "src/utils/helpers.ts",
			Reason:          "wrong rule",
			Owner:           "alice",
			ReviewCondition: "never",
		},
	}
	v := findViolation(t, Naming(dir, coreRules(), exceptions), "naming-catch-all")
	if v.Path != "src/utils/helpers.ts" {
		t.Errorf("path = %q, want src/utils/helpers.ts", v.Path)
	}
}

func TestNamingKebabCaseWarning(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"src/MyComponent/readMe.md": "x\n",
	})
	v := findViolation(t, Naming(dir, coreRules(), nil), "naming-kebab-case")
	if v.Severity != "warning" {
		t.Errorf("severity = %q, want warning", v.Severity)
	}
	if v.Path != "src/MyComponent/readMe.md" {
		t.Errorf("path = %q, want src/MyComponent/readMe.md", v.Path)
	}
	if !strings.Contains(v.Message, "MyComponent") {
		t.Errorf("message %q should name the offending segment", v.Message)
	}
}

func TestNamingKebabCaseIgnoresFileExtension(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"src/lib/data.ts":             "x\n", // stem data is clean kebab
		"cmd/pika/main.go":      "x\n", // go.mod-style dotted stems stay clean
		"docs/superpowers/specs/x.md": "x\n",
	})
	if vs := Naming(dir, coreRules(), nil); len(vs) != 0 {
		t.Fatalf("kebab-case fired on extension or dotted segments: %+v", vs)
	}
}

func TestNamingKebabCaseExemptStems(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"README.md":       "x\n",
		"AGENTS.md":       "x\n",
		"CONTRIBUTING.md": "x\n",
		"Makefile":        "x\n",
		"LICENSE":         "x\n",
		"Dockerfile":      "x\n",
		"README":          "x\n", // extensionless conventional stem
	})
	if vs := Naming(dir, coreRules(), nil); len(vs) != 0 {
		t.Fatalf("conventional stems must not trip the kebab rule: %+v", vs)
	}
}

func TestNamingKebabCaseExemptIsStemExact(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"src/Readme.md":     "x\n", // wrong case is not the exempt stem
		"src/README.bak.md": "x\n", // stem README.bak is not exempt
	})
	vs := Naming(dir, coreRules(), nil)
	if len(vs) != 2 {
		t.Fatalf("violations = %+v, want 2 for non-exempt spellings", vs)
	}
}

func TestNamingKebabCaseToleratesSnakeCaseAndDunderInit(t *testing.T) {
	// Python's package layout is snake_case by language mandate (PEP 8);
	// the kebab rule's pattern admits lowercase snake and dotted stems,
	// and __init__ is a conventional exempt stem.
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"src/python_single/__init__.py": "x\n",
		"tests/test_init.py":            "x\n",
	})
	if vs := Naming(dir, coreRules(), nil); len(vs) != 0 {
		t.Fatalf("python-conventional paths must not trip the kebab rule: %+v", vs)
	}
}

func TestNamingKebabCaseDefaultPatternWhenUnset(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"src/BadName.go": "x\n",
	})
	rules := []profiles.NamingRule{
		{RuleID: "naming-kebab-case", Severity: "warning", Scope: "path-segments"},
	}
	v := findViolation(t, Naming(dir, rules, nil), "naming-kebab-case")
	if v.Path != "src/BadName.go" {
		t.Errorf("path = %q, want src/BadName.go", v.Path)
	}
	// The brief's default pattern tolerates underscores and dots: a file
	// named snake_case passes the default rule but not the pack override.
	dir2 := t.TempDir()
	writeTree(t, dir2, map[string]string{"src/snake_case.go": "x\n"})
	if vs := Naming(dir2, rules, nil); len(vs) != 0 {
		t.Fatalf("default pattern should tolerate snake_case: %+v", vs)
	}
}

func TestNamingFileSizeReview(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("line\n", 501) // 501 lines
	exact := strings.Repeat("line\n", 500)
	writeTree(t, dir, map[string]string{
		"src/big.go":   big,
		"src/exact.go": exact,
	})
	v := findViolation(t, Naming(dir, coreRules(), nil), "file-size-review")
	if v.Severity != "warning" {
		t.Errorf("severity = %q, want warning", v.Severity)
	}
	if v.Path != "src/big.go" {
		t.Errorf("path = %q, want src/big.go", v.Path)
	}
	for _, x := range Naming(dir, coreRules(), nil) {
		if x.RuleID == "file-size-review" && x.Path == "src/exact.go" {
			t.Errorf("file with exactly 500 lines flagged: %+v", x)
		}
	}
}

func TestNamingGeneratedOwner(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"gen/api.pb.go":      "package api\n", // matches the synthetic glob, no header
		"gen/api2.pb.go":     "// Code generated by protoc-gen-go. DO NOT EDIT.\npackage api\n",
		"src/handwritten.go": "package src\n", // outside the glob
	})
	rules := []profiles.NamingRule{
		{RuleID: "generated-owner", Severity: "error", Scope: "generated-patterns", Pattern: "gen/*.pb.go"},
	}
	v := findViolation(t, Naming(dir, rules, nil), "generated-owner")
	if v.Severity != "error" {
		t.Errorf("severity = %q, want error", v.Severity)
	}
	if v.Path != "gen/api.pb.go" {
		t.Errorf("path = %q, want gen/api.pb.go", v.Path)
	}
	for _, x := range Naming(dir, rules, nil) {
		if x.RuleID == "generated-owner" && x.Path != "gen/api.pb.go" {
			t.Errorf("unexpected generated-owner finding for %q: %+v", x.Path, x)
		}
	}
}

func TestNamingGeneratedOwnerNoMatchDefault(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"gen/api.pb.go": "package api\n", // would violate if the rule matched
	})
	// The core pack ships generated-owner with no patterns (the registry
	// test pins that): the rule mechanism is live but matches nothing.
	rules := []profiles.NamingRule{
		{RuleID: "generated-owner", Severity: "error", Scope: "generated-patterns"},
	}
	if vs := Naming(dir, rules, nil); len(vs) != 0 {
		t.Fatalf("patternless generated-owner rule must match nothing: %+v", vs)
	}
}

func TestNamingSkipsRootAndDotPaths(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"main.go":                   "package main\n",
		".project/contract.yaml":    "schema: 1\n",
		".project/exceptions.yaml":  "Bad_Name.txt:\n  rule-id: naming-kebab-case\n  reason: x\n  owner: o\n  review-condition: c\n",
		".git/config":               "[core]\n",
		".github/workflows/ci.yaml": "on: push\n",
		"src/utils/helpers.ts":      "// helpers\n",
	})
	// The root itself is a legal package root and is never a finding; the
	// only finding comes from src/utils/helpers.ts.
	var catchAll int
	for _, v := range Naming(dir, coreRules(), nil) {
		switch {
		case v.RuleID == "naming-catch-all" && v.Path == "src/utils/helpers.ts":
			catchAll++
		case v.RuleID == "naming-kebab-case":
			t.Errorf("dot-prefixed paths must be skipped, got %+v", v)
		}
	}
	if catchAll != 1 {
		t.Errorf("catch-all findings = %d, want 1", catchAll)
	}
}

func TestLoadExceptions(t *testing.T) {
	t.Run("missing file is empty", func(t *testing.T) {
		m, err := LoadExceptions(t.TempDir())
		if err != nil {
			t.Fatalf("LoadExceptions: %v", err)
		}
		if len(m) != 0 {
			t.Fatalf("exceptions = %v, want empty", m)
		}
	})
	t.Run("empty file is empty", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{".project/exceptions.yaml": ""})
		m, err := LoadExceptions(dir)
		if err != nil {
			t.Fatalf("LoadExceptions: %v", err)
		}
		if len(m) != 0 {
			t.Fatalf("exceptions = %v, want empty", m)
		}
	})
	t.Run("valid record", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{".project/exceptions.yaml": "src/utils/helpers.ts:\n  rule-id: naming-catch-all\n  reason: legacy module pending split\n  owner: alice\n  review-condition: revisit at the 2026-10 architecture sync\n"})
		m, err := LoadExceptions(dir)
		if err != nil {
			t.Fatalf("LoadExceptions: %v", err)
		}
		ex, ok := m["src/utils/helpers.ts"]
		if !ok {
			t.Fatalf("exceptions = %v, missing src/utils/helpers.ts", m)
		}
		if ex.RuleID != "naming-catch-all" || ex.Reason == "" || ex.Owner != "alice" || ex.ReviewCondition == "" {
			t.Fatalf("exception = %+v", ex)
		}
		if ex.Path != "src/utils/helpers.ts" {
			t.Fatalf("exception path = %q, want src/utils/helpers.ts", ex.Path)
		}
	})
	t.Run("missing required field is a load error", func(t *testing.T) {
		for _, drop := range []string{"rule-id", "reason", "owner", "review-condition"} {
			dir := t.TempDir()
			lines := map[string]string{
				"rule-id":          "  rule-id: naming-catch-all\n",
				"reason":           "  reason: r\n",
				"owner":            "  owner: o\n",
				"review-condition": "  review-condition: c\n",
			}
			var b strings.Builder
			b.WriteString("src/utils/helpers.ts:\n")
			for _, f := range []string{"rule-id", "reason", "owner", "review-condition"} {
				if f != drop {
					b.WriteString(lines[f])
				}
			}
			body := b.String()
			writeTree(t, dir, map[string]string{".project/exceptions.yaml": body})
			if _, err := LoadExceptions(dir); err == nil || !strings.Contains(err.Error(), drop) {
				t.Fatalf("dropping %s: err = %v, want a load error naming %s", drop, err, drop)
			}
		}
	})
	t.Run("unknown field rejected", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{".project/exceptions.yaml": "src/utils/helpers.ts:\n  rule-id: naming-catch-all\n  reason: r\n  owner: o\n  review-condition: c\n  surprise: 1\n"})
		if _, err := LoadExceptions(dir); err == nil {
			t.Fatal("unknown field must be rejected")
		}
	})
	t.Run("duplicate path key rejected", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{".project/exceptions.yaml": "src/utils/helpers.ts:\n  rule-id: naming-catch-all\n  reason: r\n  owner: o\n  review-condition: c\nsrc/utils/helpers.ts:\n  rule-id: naming-catch-all\n  reason: r2\n  owner: o2\n  review-condition: c2\n"})
		if _, err := LoadExceptions(dir); err == nil {
			t.Fatal("duplicate key must be rejected")
		}
	})
	t.Run("path field must agree with key", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{".project/exceptions.yaml": "src/utils/helpers.ts:\n  rule-id: naming-catch-all\n  path: src/other.ts\n  reason: r\n  owner: o\n  review-condition: c\n"})
		if _, err := LoadExceptions(dir); err == nil || !strings.Contains(err.Error(), "disagree") {
			t.Fatalf("err = %v, want a disagreement error", err)
		}
	})
	t.Run("escaping key rejected", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{".project/exceptions.yaml": "../../etc/passwd:\n  rule-id: naming-catch-all\n  reason: r\n  owner: o\n  review-condition: c\n"})
		if _, err := LoadExceptions(dir); err == nil {
			t.Fatal("path escape must be rejected")
		}
	})
	t.Run("unreadable file is an error", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{".project/exceptions.yaml": "src/x.ts:\n  rule-id: r\n  reason: r\n  owner: o\n  review-condition: c\n"})
		p := filepath.Join(dir, ".project", "exceptions.yaml")
		if err := os.Chmod(p, 0o000); err != nil {
			t.Skip("cannot chmod on this platform")
		}
		defer os.Chmod(p, 0o644) //nolint:errcheck
		if _, err := LoadExceptions(dir); err == nil {
			t.Fatal("unreadable exceptions file must be an error")
		}
	})
}
