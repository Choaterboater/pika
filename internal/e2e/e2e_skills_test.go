// TestE2ESkills closes spec §7's remaining item: a scaffolded
// repository's skills and projections are present, consistent, and pass
// `check`, exercised through the real binary rather than the internal
// packages in isolation.
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canonicalSkillNames is the fixed set `pika init` and `pika apply` both
// write, in the order spec §9.2 names them.
var canonicalSkillNames = []string{"project-work", "project-research", "project-review", "project-maintain"}

// TestE2EScaffoldedSkillsAndProjectionsPassCheck scaffolds a repository,
// asserts every canonical skill exists on disk, asserts the declared
// codex projection carries a live region in AGENTS.md, asserts `pika
// check --all` and `pika skills check` both agree the result is clean,
// and asserts a second `pika skills install` changes nothing — the
// scaffold already produced a current install, not a stale one waiting
// to be regenerated.
func TestE2EScaffoldedSkillsAndProjectionsPassCheck(t *testing.T) {
	dir := scaffoldRepo(t, "go") // go runs everywhere the suite runs

	for _, name := range canonicalSkillNames {
		p := filepath.Join(dir, ".agents", "skills", name, "SKILL.md")
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("canonical skill %s: %v", name, err)
		}
		if !strings.HasPrefix(string(body), "---\nname: "+name) {
			t.Errorf("%s does not open with its own frontmatter:\n%s", p, body)
		}
	}

	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("AGENTS.md: %v", err)
	}
	for _, want := range []string{
		"<!-- pika:skills:begin -->",
		"<!-- pika:region sha256:",
		"<!-- pika:source skill .agents/skills/project-work/SKILL.md sha256:",
		"<!-- pika:source skill .agents/skills/project-research/SKILL.md sha256:",
		"<!-- pika:source skill .agents/skills/project-review/SKILL.md sha256:",
		"<!-- pika:source skill .agents/skills/project-maintain/SKILL.md sha256:",
		"<!-- pika:skills:end -->",
	} {
		if !strings.Contains(string(agents), want) {
			t.Errorf("AGENTS.md missing %q:\n%s", want, agents)
		}
	}

	// `pika check --all`: the projection gate 1 verifies is exactly the
	// one the scaffold just wrote, so it must already be current.
	out := runCLI(t, dir, 0, "check", "--all", "--json")
	rep := parseCheckReport(t, out)
	if !rep.Pass {
		t.Fatalf("check --all did not pass on a freshly scaffolded repository:\n%s", out)
	}

	// `pika skills check`: the standalone surface must agree with gate
	// 1 rather than reporting a second opinion about the same files.
	runCLI(t, dir, 0, "skills", "check", "--json")

	// A second install changes nothing: the scaffold's own install was
	// already current, not a stale placeholder waiting on this step.
	installOut := runCLI(t, dir, 0, "skills", "install", "--json")
	env := unwrap(t, installOut, "skills")
	var st struct {
		Skills []struct {
			Written bool `json:"written"`
		} `json:"skills"`
		Projections []struct {
			Written bool `json:"written"`
		} `json:"projections"`
	}
	if err := json.Unmarshal(env.Result, &st); err != nil {
		t.Fatalf("parse skills install result: %v\n%s", err, installOut)
	}
	for i, s := range st.Skills {
		if s.Written {
			t.Errorf("skill %d reported written on a second install; the scaffold's own install was not current", i)
		}
	}
	for i, p := range st.Projections {
		if p.Written {
			t.Errorf("projection %d reported written on a second install; the scaffold's own install was not current", i)
		}
	}
}

// TestE2EAdoptedSkillsAndProjectionsPassCheck covers the other
// scaffolding path spec §9.2 names: an adopted (not freshly
// initialized) repository gets the same four skills and the same
// current AGENTS.md projection through `pika apply`, and `pika check
// --all` agrees.
func TestE2EAdoptedSkillsAndProjectionsPassCheck(t *testing.T) {
	dir := t.TempDir()
	copyFixtureTree(t, "go-mod", dir)
	runCLI(t, dir, 0, "adopt", "--json")
	runCLI(t, dir, 0, "apply", "--json")

	for _, name := range canonicalSkillNames {
		if _, err := os.Stat(filepath.Join(dir, ".agents", "skills", name, "SKILL.md")); err != nil {
			t.Errorf("apply did not write canonical skill %s: %v", name, err)
		}
	}
	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agents), "<!-- pika:skills:begin -->") {
		t.Errorf("AGENTS.md carries no projection region after apply:\n%s", agents)
	}

	out := runCLI(t, dir, 0, "check", "--all", "--json")
	if rep := parseCheckReport(t, out); !rep.Pass {
		t.Fatalf("check --all did not pass on an adopted repository:\n%s", out)
	}
}
