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

	var kebab, catchAll, fileSize, generatedOwner *NamingRule
	for i := range r.NamingRules {
		switch r.NamingRules[i].RuleID {
		case "naming-kebab-case":
			kebab = &r.NamingRules[i]
		case "naming-catch-all":
			catchAll = &r.NamingRules[i]
		case "file-size-review":
			fileSize = &r.NamingRules[i]
		case "generated-owner":
			generatedOwner = &r.NamingRules[i]
		}
	}
	if kebab == nil {
		t.Fatalf("naming rules %v missing naming-kebab-case", r.NamingRules)
	}
	if catchAll == nil {
		t.Fatalf("naming rules %v missing naming-catch-all", r.NamingRules)
	}
	if fileSize == nil {
		t.Fatalf("naming rules %v missing file-size-review", r.NamingRules)
	}
	if generatedOwner == nil {
		t.Fatalf("naming rules %v missing generated-owner", r.NamingRules)
	}
	if kebab.Pattern == "" {
		t.Errorf("naming-kebab-case: empty pattern")
	}
	// Size thresholds and style drift are review signals, not hard
	// failures (spec §6.2).
	if kebab.Severity != "warning" {
		t.Errorf("naming-kebab-case severity = %q, want warning", kebab.Severity)
	}
	if catchAll.Severity != "error" {
		t.Errorf("naming-catch-all severity = %q, want error", catchAll.Severity)
	}
	for _, banned := range []string{"utils", "helpers", "common", "misc", "manager"} {
		if !slices.Contains(catchAll.Banned, banned) {
			t.Errorf("naming-catch-all banned = %v, missing %q", catchAll.Banned, banned)
		}
	}
	if fileSize.Severity != "warning" || fileSize.Scope != "file-lines" || fileSize.Pattern == "" {
		t.Errorf("file-size-review = %+v, want a warning file-lines rule with a threshold", fileSize)
	}
	if generatedOwner.Severity != "error" || generatedOwner.Scope != "generated-patterns" {
		t.Errorf("generated-owner = %+v, want an error generated-patterns rule", generatedOwner)
	}
	if len(generatedOwner.Pattern) != 0 && len(generatedOwner.Banned) != 0 {
		t.Errorf("generated-owner must stay patternless in M1 (no generated files): %+v", generatedOwner)
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
	if err := WriteLock(first, []string{CoreRef}); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	raw, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Digest string              `json:"digest"`
		Packs  map[string]lockPack `json:"packs"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatalf("profiles.lock is not valid JSON: %v", err)
	}
	if lock.Packs["core"].Version != "1" {
		t.Errorf("core version = %q, want 1", lock.Packs["core"].Version)
	}
	if lock.Packs["core"].Source != "embedded" {
		t.Errorf("source = %q, want embedded", lock.Packs["core"].Source)
	}
	sum := sha256.Sum256(corePackYAML)
	if lock.Packs["core"].Digest != hex.EncodeToString(sum[:]) {
		t.Errorf("core digest = %q, want sha256 of embedded pack bytes", lock.Packs["core"].Digest)
	}
	if lock.Digest != PackDigest() {
		t.Errorf("digest = %q, want canonical PackDigest", lock.Digest)
	}
	if len(lock.Packs) != 1 {
		t.Errorf("single-pack lock has %d entries, want 1", len(lock.Packs))
	}

	second := filepath.Join(t.TempDir(), "profiles.lock")
	if err := WriteLock(second, []string{CoreRef}); err != nil {
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

// TestWriteLockTwoPacks asserts the two-pack lock: per-pack entries with
// each pack's own digest, in a byte-stable document.
func TestWriteLockTwoPacks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.lock")
	if err := WriteLock(path, []string{CoreRef, GoRef}); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Digest string              `json:"digest"`
		Packs  map[string]lockPack `json:"packs"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatalf("profiles.lock is not valid JSON: %v", err)
	}
	if lock.Packs["core"].Version != "1" || lock.Packs["go"].Version != "1" {
		t.Errorf("pack versions = %v, want core and go at version 1", lock.Packs)
	}
	sumCore := sha256.Sum256(corePackYAML)
	sumGo := sha256.Sum256(goPackYAML)
	if lock.Packs["core"].Digest != hex.EncodeToString(sumCore[:]) {
		t.Errorf("core digest = %q, want sha256 of core pack bytes", lock.Packs["core"].Digest)
	}
	if lock.Packs["go"].Digest != hex.EncodeToString(sumGo[:]) {
		t.Errorf("go digest = %q, want sha256 of go pack bytes", lock.Packs["go"].Digest)
	}
	if lock.Digest != PackDigest() {
		t.Errorf("digest = %q, want canonical PackDigest", lock.Digest)
	}

	second := filepath.Join(t.TempDir(), "profiles.lock")
	if err := WriteLock(second, []string{CoreRef, GoRef}); err != nil {
		t.Fatalf("WriteLock (second): %v", err)
	}
	raw2, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw2) != string(raw) {
		t.Errorf("two-pack lock output not stable across calls")
	}
}

// TestWriteLockRejectsUnknownRef asserts the lock only pins registered
// packs.
func TestWriteLockRejectsUnknownPack(t *testing.T) {
	if err := WriteLock(filepath.Join(t.TempDir(), "profiles.lock"), []string{CoreRef, "bogus@9"}); err == nil {
		t.Error("WriteLock with unknown pack: want error, got none")
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
