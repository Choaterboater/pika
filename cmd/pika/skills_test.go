package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/skills"
)

// skillsProject lays down the smallest repository `pika skills` accepts:
// a core@1 contract with the given skills block, the matching lock, and
// an empty exceptions record.
func skillsProject(t *testing.T, projections string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `schema: 1
project:
  name: fixture
  topology: single
profiles: [core@1]
github:
  merge: squash
evidence:
  publish: sanitized
` + projections
	if err := os.WriteFile(filepath.Join(dir, ".project", "contract.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := profiles.WriteLock(filepath.Join(dir, ".project", "profiles.lock"), []string{"core@1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".project", "exceptions.yaml"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const codexProjection = `skills:
  projections:
    - harness: codex
      path: AGENTS.md
`

// runSkillsIn dispatches one `pika skills` invocation against dir.
func runSkillsIn(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runSkills(append(args, "--root", dir), strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func TestSkillsInstallWritesCanonicalSkillsAndProjection(t *testing.T) {
	dir := skillsProject(t, codexProjection)
	code, out, errb := runSkillsIn(t, dir, "install")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout %s stderr %s", code, out, errb)
	}
	for _, s := range skills.Shipped() {
		got, err := os.ReadFile(filepath.Join(dir, ".agents", "skills", s.Name, "SKILL.md"))
		if err != nil {
			t.Fatalf("canonical skill %s: %v", s.Name, err)
		}
		if string(got) != string(s.Body) {
			t.Errorf("canonical skill %s does not match the shipped text", s.Name)
		}
	}
	doc, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	for _, want := range []string{
		"<!-- pika:skills:begin -->",
		"<!-- pika:skills:end -->",
		"<!-- pika:source skill .agents/skills/project-work/SKILL.md sha256:",
		"The ladder is the evidence",
	} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("projection is missing %q:\n%s", want, doc)
		}
	}
}

// The region is kernel-owned; the file is not. An operator's own words
// above the markers survive a regeneration, because taking over the
// whole of AGENTS.md would trade one ownership collision for another.
func TestSkillsInstallKeepsOperatorTextOutsideTheRegion(t *testing.T) {
	dir := skillsProject(t, codexProjection)
	const preamble = "# Agent guidance for fixture\n\nRun the deploy script by hand; it is not in the contract.\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(preamble), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errb := runSkillsIn(t, dir, "install"); code != 0 {
		t.Fatalf("exit = %d; stdout %s stderr %s", code, out, errb)
	}
	doc, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(doc), preamble) {
		t.Errorf("operator text was not preserved:\n%s", doc)
	}
	// A second install must be a no-op, not an accumulation of regions.
	if code, _, errb := runSkillsIn(t, dir, "install"); code != 0 {
		t.Fatalf("second install exit = %d; stderr %s", code, errb)
	}
	again, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(doc, again) {
		t.Errorf("a second install changed the file:\n%s", again)
	}
	if n := strings.Count(string(again), "<!-- pika:skills:begin -->"); n != 1 {
		t.Errorf("region count = %d, want 1", n)
	}
}

func TestSkillsCheckFailsOnATamperedProjection(t *testing.T) {
	dir := skillsProject(t, codexProjection)
	if code, _, errb := runSkillsIn(t, dir, "install"); code != 0 {
		t.Fatalf("install exit = %d; stderr %s", code, errb)
	}
	if code, out, _ := runSkillsIn(t, dir, "check"); code != 0 {
		t.Fatalf("check exit = %d on a fresh install, want 0:\n%s", code, out)
	}

	// Tamper with the source, not the copy: the copy still certifies a
	// digest the source no longer has, which is exactly the stale-copy
	// state a projection carries a digest to make visible.
	skill := filepath.Join(dir, ".agents", "skills", "project-work", "SKILL.md")
	body, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, append(body, []byte("\nAn extra rule.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errb := runSkillsIn(t, dir, "check")
	if code != 1 {
		t.Fatalf("check exit = %d on a drifted projection, want 1; stdout %s stderr %s", code, out, errb)
	}
	for _, want := range []string{"drifted", "AGENTS.md", ".agents/skills/project-work/SKILL.md", "pika skills install"} {
		if !strings.Contains(out, want) {
			t.Errorf("drift report does not name %q:\n%s", want, out)
		}
	}
	// Regenerating fixes it.
	if code, _, errb := runSkillsIn(t, dir, "install"); code != 0 {
		t.Fatalf("regenerate exit = %d; stderr %s", code, errb)
	}
	if code, out, _ := runSkillsIn(t, dir, "check"); code != 0 {
		t.Fatalf("check exit = %d after regenerating, want 0:\n%s", code, out)
	}
}

// Editing inside the markers is a different failure from a moved source
// and has to read differently, or the operator is told to regenerate
// when what they actually did was put a change in the wrong file.
func TestSkillsCheckFailsOnAHandEditedRegion(t *testing.T) {
	dir := skillsProject(t, codexProjection)
	if code, _, errb := runSkillsIn(t, dir, "install"); code != 0 {
		t.Fatalf("install exit = %d; stderr %s", code, errb)
	}
	target := filepath.Join(dir, "AGENTS.md")
	doc, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(doc), "## Driving pika", "## Driving pika (edited by hand)", 1)
	if edited == string(doc) {
		t.Fatal("fixture did not find the heading it meant to edit")
	}
	if err := os.WriteFile(target, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runSkillsIn(t, dir, "check")
	if code != 1 {
		t.Fatalf("check exit = %d, want 1:\n%s", code, out)
	}
	if !strings.Contains(out, "edited by hand inside the pika skills markers") {
		t.Errorf("hand edit is not distinguished from a moved source:\n%s", out)
	}
}

// The canonical skill is the operator's. `pika skills install` writes one
// that is missing and keeps one that was changed; only --force replaces
// it, and the report says so either way.
func TestSkillsInstallNeedsForceToReplaceAnEditedSkill(t *testing.T) {
	dir := skillsProject(t, "")
	if code, _, errb := runSkillsIn(t, dir, "install"); code != 0 {
		t.Fatalf("install exit = %d; stderr %s", code, errb)
	}
	skill := filepath.Join(dir, ".agents", "skills", "project-work", "SKILL.md")
	const mine = "---\nname: project-work\ndescription: mine\n---\n\n# Mine\n"
	if err := os.WriteFile(skill, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, out, errb := runSkillsIn(t, dir, "install"); code != 0 {
		t.Fatalf("install exit = %d; stdout %s stderr %s", code, out, errb)
	}
	got, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != mine {
		t.Fatalf("install overwrote an edited skill without --force:\n%s", got)
	}
	if _, out, _ := runSkillsIn(t, dir); !strings.Contains(out, "edited") {
		t.Errorf("report does not say the skill was edited:\n%s", out)
	}

	if code, out, errb := runSkillsIn(t, dir, "install", "--force"); code != 0 {
		t.Fatalf("forced install exit = %d; stdout %s stderr %s", code, out, errb)
	}
	got, err = os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == mine {
		t.Fatal("--force did not replace the edited skill")
	}
}

func TestSkillsForceIsRejectedOutsideInstall(t *testing.T) {
	dir := skillsProject(t, "")
	code, out, errb := runSkillsIn(t, dir, "check", "--force")
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stdout %s stderr %s", code, out, errb)
	}
	if !strings.Contains(errb, "--force applies only to") {
		t.Errorf("stderr does not explain the refusal: %s", errb)
	}
}

func TestSkillsUnknownSubcommandExits2(t *testing.T) {
	dir := skillsProject(t, "")
	code, out, errb := runSkillsIn(t, dir, "reinstall")
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stdout %s stderr %s", code, out, errb)
	}
	if !strings.Contains(errb, "unknown subcommand") {
		t.Errorf("stderr does not name the mistake: %s", errb)
	}
}

// An unknown harness is a schema violation, not a projection that is
// silently skipped: a file nothing reads looks exactly like a file
// something reads, and the operator would never learn which they had.
func TestSkillsUnknownHarnessIsAContractError(t *testing.T) {
	dir := skillsProject(t, `skills:
  projections:
    - harness: codx
      path: AGENTS.md
`)
	code, out, errb := runSkillsIn(t, dir, "install")
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stdout %s stderr %s", code, out, errb)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Error("a projection was written for a harness the schema rejects")
	}
}

// A Go repository's projection carries go@1's guidance; a repository on
// another stack must not, or one pack's advice would be handed to every
// stack as though it were universal.
func TestGuidanceIsStackSpecific(t *testing.T) {
	goDir := skillsProject(t, codexProjection)
	writeProfiles(t, goDir, "[core@1, go@1]", []string{"core@1", "go@1"})
	if code, _, errb := runSkillsIn(t, goDir, "install"); code != 0 {
		t.Fatalf("go install exit = %d; stderr %s", code, errb)
	}
	goDoc := read(t, filepath.Join(goDir, "AGENTS.md"))
	const goMarker = "gofmt -l ."
	if !strings.Contains(goDoc, goMarker) {
		t.Errorf("go projection does not carry go@1's guidance:\n%s", goDoc)
	}
	if !strings.Contains(goDoc, "<!-- pika:source guidance go@1 sha256:") {
		t.Errorf("go projection does not cite go@1 as a source:\n%s", goDoc)
	}

	tsDir := skillsProject(t, codexProjection)
	writeProfiles(t, tsDir, "[core@1, typescript@1]", []string{"core@1", "typescript@1"})
	if code, _, errb := runSkillsIn(t, tsDir, "install"); code != 0 {
		t.Fatalf("typescript install exit = %d; stderr %s", code, errb)
	}
	tsDoc := read(t, filepath.Join(tsDir, "AGENTS.md"))
	if strings.Contains(tsDoc, goMarker) {
		t.Errorf("typescript projection carries go@1's guidance:\n%s", tsDoc)
	}
}

// writeProfiles rewrites a fixture's profile selection and lock together,
// so gate 1 and the resolver see the same stack.
func writeProfiles(t *testing.T, dir, selection string, refs []string) {
	t.Helper()
	path := filepath.Join(dir, ".project", "contract.yaml")
	doc := read(t, path)
	doc = strings.Replace(doc, "profiles: [core@1]", "profiles: "+selection, 1)
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := profiles.WriteLock(filepath.Join(dir, ".project", "profiles.lock"), refs); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSkillsJSONReportCarriesSourcesAndStates(t *testing.T) {
	dir := skillsProject(t, codexProjection)
	if code, _, errb := runSkillsIn(t, dir, "install"); code != 0 {
		t.Fatalf("install exit = %d; stderr %s", code, errb)
	}
	var out bytes.Buffer
	if code := runSkills([]string{"--json", "--root", dir}, strings.NewReader(""), &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("exit = %d, want 0:\n%s", code, out.String())
	}
	var st skills.Status
	resultOf(t, out.Bytes(), "skills", &st)
	if len(st.Projections) != 1 {
		t.Fatalf("projections = %d, want 1", len(st.Projections))
	}
	p := st.Projections[0]
	if p.State != skills.StateCurrent || p.Harness != "codex" || p.Path != "AGENTS.md" {
		t.Errorf("projection = %+v", p)
	}
	if len(p.Sources) != len(skills.Shipped()) {
		t.Errorf("sources = %d, want %d", len(p.Sources), len(skills.Shipped()))
	}
	for _, s := range p.Sources {
		if !strings.HasPrefix(s.Digest, "sha256:") {
			t.Errorf("source %+v carries no digest", s)
		}
	}
	if len(st.Skills) != len(skills.Shipped()) {
		t.Fatalf("skills = %d, want %d", len(st.Skills), len(skills.Shipped()))
	}
	for _, s := range st.Skills {
		if s.State != skills.StateInstalled {
			t.Errorf("skill %s state = %s, want %s", s.Name, s.State, skills.StateInstalled)
		}
	}
	// The envelope must not smuggle prose past a parsing agent.
	if err := json.Unmarshal(out.Bytes(), &map[string]any{}); err != nil {
		t.Errorf("stdout is not one JSON document: %v", err)
	}
}

// A skill that names a command pika does not have is worse than a
// message that does: the operator reading a message can try something
// else, and the agent reading a skill cannot notice. Every command name
// in the shipped skills, in the repository's own installed skills, and
// in every pack's agent guidance — the three texts that compose a
// projection — is resolved through the same registry dispatch uses.
func TestEveryCommandNamedInASkillOrGuidanceIsRegistered(t *testing.T) {
	mentions := 0
	report := func(where, text string) {
		missing, n := unregisteredMentions(text)
		mentions += n
		for _, name := range missing {
			t.Errorf("%s names `pika %s`, which is not a registered command", where, name)
		}
	}
	for _, s := range skills.Shipped() {
		report("shipped skill "+s.Name, string(s.Body))
	}
	for _, ref := range profiles.SupportedRefs() {
		selected := []string{profiles.CoreRef}
		if ref != profiles.CoreRef {
			selected = append(selected, ref)
		}
		resolved, err := profiles.Resolve(selected)
		if err != nil {
			t.Fatalf("resolve %s: %v", ref, err)
		}
		for _, g := range resolved.AgentGuidance {
			report("agent-guidance in "+g.Ref, strings.Join(g.Lines, "\n"))
		}
	}
	found, err := scanInstalledSkills(filepath.Join("..", ".."), report)
	if err != nil {
		t.Fatalf("scan installed skills: %v", err)
	}
	if found == 0 {
		t.Fatal("no installed skill was scanned; the guard is no longer reading this repository's own .agents/skills")
	}
	if mentions == 0 {
		t.Fatal("no `pika <command>` mention found in any skill or guidance; the guard is no longer reading the text it claims to")
	}
}

// scanInstalledSkills feeds every SKILL.md under root/.agents/skills to
// report and returns how many it found. It takes the root as a parameter
// so the test below can point it at a planted tree and prove the rule
// fires, rather than trusting that it would.
func scanInstalledSkills(root string, report func(where, text string)) (int, error) {
	dir := filepath.Join(root, ".agents", "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	found := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "SKILL.md")
		body, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return found, err
		}
		found++
		report(filepath.ToSlash(path), string(body))
	}
	return found, nil
}

// The guard above passes when nothing is wrong, which proves nothing on
// its own. Planting a skill that sends an agent to a command pika has
// never had — the `upgrade` mistake that made this guard exist — proves
// the rule fires rather than merely being present.
//
// The bad mention is assembled at run time rather than written as one
// literal. This file is Go source, so the sibling guard over string
// literals reads it too, and a planted dead end spelled out in one piece
// would fail that guard instead of this one — the fixture would be
// indistinguishable from the defect it exists to catch.
func TestSkillNamingAnUnregisteredCommandIsCaught(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agents", "skills", "planted"), 0o755); err != nil {
		t.Fatal(err)
	}
	planted := "---\nname: planted\n---\n\nWhen the packs are stale, run `pika " + "upgrade` and then `pika check`.\n"
	if err := os.WriteFile(filepath.Join(root, ".agents", "skills", "planted", "SKILL.md"), []byte(planted), 0o644); err != nil {
		t.Fatal(err)
	}
	var caught []string
	found, err := scanInstalledSkills(root, func(_, text string) {
		missing, _ := unregisteredMentions(text)
		caught = append(caught, missing...)
	})
	if err != nil {
		t.Fatal(err)
	}
	if found != 1 {
		t.Fatalf("scanned %d skills, want 1", found)
	}
	if len(caught) != 1 || caught[0] != "upgrade" {
		t.Fatalf("caught = %v, want [upgrade]", caught)
	}
}
