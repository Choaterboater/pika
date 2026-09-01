package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// applyReport mirrors the JSON apply report (apply.Report).
type applyReport struct {
	Applied []struct {
		Op   string `json:"op"`
		Path string `json:"path"`
	} `json:"applied"`
	Skipped []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"skipped"`
	Rollback bool `json:"rollback"`
	Gate1    struct {
		Pass     bool     `json:"pass"`
		Output   string   `json:"output"`
		Warnings []string `json:"warnings"`
	} `json:"gate1"`
}

// TestE2EAdoptApply closes the adoption loop end to end through the
// real binary: `pika adopt` previews on the go fixture, `pika apply`
// promotes the drafts transactionally, `check --all` is green (honest
// skips allowed where the fixture has no check commands), and a second
// apply is refused.
func TestE2EAdoptApply(t *testing.T) {
	dir := t.TempDir()
	copyFixtureTree(t, "go-mod", dir)

	// Adopt: drafts plus the visible review bundle, nothing applied.
	runCLI(t, dir, 0, "adopt", "--json")
	for _, p := range []string{".project/contract.yaml.draft", ".project/profiles.lock.draft", "review/adoption-review.md"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Fatalf("adopt did not write %s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".project", "contract.yaml")); err == nil {
		t.Fatal("adopt wrote the contract; it must stay preview-only")
	}
	proposed, err := os.ReadFile(filepath.Join(dir, "review", "adoption-review.md"))
	if err != nil || !strings.Contains(string(proposed), "Status: **PROPOSED**") {
		t.Fatalf("review bundle after adopt: %v\n%s", err, proposed)
	}

	// Apply: transactional promotion, JSON report, gate 1 pass.
	out := runCLI(t, dir, 0, "apply", "--json")
	env := unwrap(t, out, "apply")
	if !env.OK {
		t.Errorf("apply --json reported ok = false on the happy path:\n%s", out)
	}
	var rep applyReport
	if err := json.Unmarshal(env.Result, &rep); err != nil {
		t.Fatalf("apply --json result is not the apply report: %v\n%s", err, out)
	}
	if rep.Rollback {
		t.Error("apply reported a rollback on the happy path")
	}
	if !rep.Gate1.Pass {
		t.Errorf("gate 1 failed on the applied contract: %s", rep.Gate1.Output)
	}
	var sawContract bool
	for _, a := range rep.Applied {
		// AGENTS.md alone appears twice: once as a plain "create" (the
		// whole file did not exist), and once as a "write" for the
		// declared codex projection's region, which is kernel-owned and
		// spliced in regardless of the file's own create-if-missing
		// state (spec 5.2). The two drafts are "delete": consumed by
		// promotion, so apply removes them rather than leaving stale
		// byte-identical copies behind.
		isDraftDelete := a.Op == "delete" && (a.Path == ".project/contract.yaml.draft" || a.Path == ".project/profiles.lock.draft")
		if a.Op != "create" && !(a.Path == "AGENTS.md" && a.Op == "write") && !isDraftDelete {
			t.Errorf("op %q on %s, want create", a.Op, a.Path)
		}
		if a.Path == ".project/contract.yaml" {
			sawContract = true
		}
	}
	if !sawContract {
		t.Errorf("contract not applied: %v", rep.Applied)
	}
	for _, p := range []string{".project/contract.yaml", ".project/profiles.lock", ".project/exceptions.yaml",
		"AGENTS.md", "CONTRIBUTING.md", ".github/workflows/ci.yml", ".github/pull_request_template.md"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("apply did not write %s: %v", p, err)
		}
	}
	for _, p := range []string{".project/contract.yaml.draft", ".project/profiles.lock.draft"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); !os.IsNotExist(err) {
			t.Errorf("apply left %s behind, want it deleted (err=%v)", p, err)
		}
	}

	// The review bundle is rewritten as APPLIED with the outcome.
	review, err := os.ReadFile(filepath.Join(dir, "review", "adoption-review.md"))
	if err != nil || !strings.Contains(string(review), "Status: **APPLIED**") {
		t.Fatalf("review bundle after apply: %v\n%s", err, review)
	}

	// check --all is green on the adopted repository (gates with no
	// command in this fixture skip honestly).
	out = runCLI(t, dir, 0, "check", "--all", "--json")
	chk := parseCheckReport(t, out)
	if !chk.Pass {
		t.Errorf("check --all failed after apply:\n%s", out)
	}

	// A second apply is refused: the contract exists, nothing changes.
	// The refusal is still an envelope — ok:false with the reason — so a
	// consumer learns why without reading stderr prose.
	runCLI(t, dir, 1, "apply")
	refused := unwrap(t, runCLI(t, dir, 1, "apply", "--json"), "apply")
	if refused.OK {
		t.Error("refused apply reported ok = true")
	}
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(refused.Result, &failure); err != nil || failure.Error == "" {
		t.Errorf("refused apply did not report why (%v): %s", err, refused.Result)
	}

	// The refusal leaves the review bundle and the applied state alone.
	review2, err := os.ReadFile(filepath.Join(dir, "review", "adoption-review.md"))
	if err != nil || string(review2) != string(review) {
		t.Errorf("refused apply rewrote the review bundle: %v", err)
	}
}

// TestE2EApplyHumanOutput pins the human-readable summary mirroring
// adopt's style: applied list, skips, and the gate-1 verdict.
func TestE2EApplyHumanOutput(t *testing.T) {
	dir := t.TempDir()
	copyFixtureTree(t, "go-mod", dir)
	runCLI(t, dir, 0, "adopt")

	out := runCLI(t, dir, 0, "apply")
	for _, want := range []string{
		"applied (transaction committed)",
		"create .project/contract.yaml",
		"create AGENTS.md",
		"gate 1: pass",
		"review bundle rewritten: review/adoption-review.md",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q\n---\n%s", want, out)
		}
	}

	// Usage errors exit 2.
	runCLI(t, dir, 2, "apply", "junk")
}
