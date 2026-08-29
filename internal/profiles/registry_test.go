package profiles

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCoreProfileResolve(t *testing.T) {
	r, err := Resolve([]string{"core@1"})
	if err != nil {
		t.Fatalf("Resolve([core@1]): %v", err)
	}
	if len(r.Layers) != 1 {
		t.Fatalf("got %d layers, want 1", len(r.Layers))
	}
	layer := r.Layers[0]
	if layer.Name != "core" || layer.Version != "1" || layer.Source != "embedded" {
		t.Errorf("layer = %s@%s (source %q), want core@1 (embedded)", layer.Name, layer.Version, layer.Source)
	}

	if got := layer.Pack.Layout.ContractPath; got != ".project/contract.yaml" {
		t.Errorf("contract path = %q, want .project/contract.yaml", got)
	}
	if got := layer.Pack.Layout.StateDir; got != ".project/state/" {
		t.Errorf("state dir = %q, want .project/state/", got)
	}
	for _, dir := range []string{
		"docs/architecture", "docs/decisions", "docs/guides", "docs/reference", "docs/work",
	} {
		if !slices.Contains(layer.Pack.Layout.DocsSpine, dir) {
			t.Errorf("docs spine missing %q (got %v)", dir, layer.Pack.Layout.DocsSpine)
		}
	}

	for _, f := range []string{
		"README.md", "AGENTS.md", "CONTRIBUTING.md", ".github/workflows/", ".github/pull_request_template.md",
	} {
		if !slices.Contains(layer.Pack.Files.Required, f) {
			t.Errorf("required files missing %q (got %v)", f, layer.Pack.Files.Required)
		}
	}

	// Every check slot must be a discovery sentinel: core adds no commands;
	// stack layers supply the real commands in a later task.
	slots := map[string]Check{
		"format":    r.Checks.Format,
		"lint":      r.Checks.Lint,
		"typecheck": r.Checks.Typecheck,
		"test":      r.Checks.Test,
		"smoke":     r.Checks.Smoke,
	}
	for slot, c := range slots {
		if c.ID != slot {
			t.Errorf("check slot %s carries ID %q", slot, c.ID)
		}
		if !c.Discovery {
			t.Errorf("check slot %s: want discovery sentinel, got cmd %v", slot, c.Cmd)
		}
		if len(c.Cmd) != 0 {
			t.Errorf("check slot %s: discovery sentinel must not carry a command, got %v", slot, c.Cmd)
		}
	}

	var kebab, catchAll *NamingRule
	for i := range r.NamingRules {
		switch r.NamingRules[i].RuleID {
		case "naming-kebab-case":
			kebab = &r.NamingRules[i]
		case "naming-catch-all":
			catchAll = &r.NamingRules[i]
		}
	}
	if kebab == nil {
		t.Fatalf("naming rules %v missing naming-kebab-case", r.NamingRules)
	}
	if catchAll == nil {
		t.Fatalf("naming rules %v missing naming-catch-all", r.NamingRules)
	}
	if kebab.Pattern == "" {
		t.Errorf("naming-kebab-case: empty pattern")
	}
	if catchAll.Severity != "error" {
		t.Errorf("naming-catch-all severity = %q, want error", catchAll.Severity)
	}
	for _, banned := range []string{"utils", "helpers", "common", "misc"} {
		if !slices.Contains(catchAll.Banned, banned) {
			t.Errorf("naming-catch-all banned = %v, missing %q", catchAll.Banned, banned)
		}
	}

	if len(r.DocTriggers) == 0 {
		t.Errorf("doc triggers: want at least one, got none")
	}

	if !layer.Pack.Conventions.PullRequests.DraftUntilChecksPass {
		t.Errorf("pull requests: want draft-until-checks-pass")
	}
	if got := layer.Pack.Conventions.PullRequests.MergeStrategy; got != "squash" {
		t.Errorf("pull request merge strategy = %q, want squash", got)
	}
	for _, bt := range []string{"feat", "fix", "docs", "refactor", "chore"} {
		if !slices.Contains(layer.Pack.Conventions.Branches.Types, bt) {
			t.Errorf("branch types %v missing %q", layer.Pack.Conventions.Branches.Types, bt)
		}
	}
}

func TestWriteLockDigest(t *testing.T) {
	first := filepath.Join(t.TempDir(), "profiles.lock")
	if err := WriteLock(first); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	raw, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Core   string `json:"core"`
		Source string `json:"source"`
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatalf("profiles.lock is not valid JSON: %v", err)
	}
	if lock.Core != "1" {
		t.Errorf("core version = %q, want 1", lock.Core)
	}
	if lock.Source != "embedded" {
		t.Errorf("source = %q, want embedded", lock.Source)
	}
	sum := sha256.Sum256(corePackYAML)
	if lock.Digest != hex.EncodeToString(sum[:]) {
		t.Errorf("digest = %q, want sha256 of embedded pack bytes", lock.Digest)
	}

	second := filepath.Join(t.TempDir(), "profiles.lock")
	if err := WriteLock(second); err != nil {
		t.Fatalf("WriteLock (second): %v", err)
	}
	raw2, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw2) != string(raw) {
		t.Errorf("lock output not stable across calls:\nfirst:  %s\nsecond: %s", raw, raw2)
	}
}

func TestResolveRejectsUnknownProfiles(t *testing.T) {
	for _, sel := range [][]string{
		{},
		{"core"},
		{"core@2"},
		{"lang-go@1"},
		{"core@1", "core@1"},
	} {
		r, err := Resolve(sel)
		if err == nil {
			t.Errorf("Resolve(%v): want error, got %+v", sel, r)
			continue
		}
		if !strings.Contains(err.Error(), "core@1") {
			t.Errorf("Resolve(%v) error %q should list supported pack core@1", sel, err)
		}
	}
}
