package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
)

func rootAt(t *testing.T) *repopath.Root {
	t.Helper()
	root, err := repopath.At(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func codexContract() *contract.Contract {
	return &contract.Contract{
		Schema:   1,
		Profiles: []string{profiles.CoreRef},
		Skills:   &contract.Skills{Projections: []contract.Projection{{Harness: "codex", Path: "AGENTS.md"}}},
	}
}

func resolve(t *testing.T, refs ...string) *profiles.Resolved {
	t.Helper()
	resolved, err := profiles.Resolve(refs)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// Every skill this binary ships must be installable and must carry the
// frontmatter a skill loader keys on; a template that lost it would
// install a file no harness recognises as a skill.
func TestShippedSkillsAreWellFormed(t *testing.T) {
	shipped := Shipped()
	if len(shipped) == 0 {
		t.Fatal("no skills are shipped")
	}
	for _, s := range shipped {
		if !strings.HasPrefix(string(s.Body), "---\nname: "+s.Name+"\n") {
			t.Errorf("skill %s does not open with frontmatter naming itself:\n%s", s.Name, firstLines(s.Body, 3))
		}
		if !strings.Contains(string(s.Body), "\ndescription: ") {
			t.Errorf("skill %s declares no description, so no loader can decide when to read it", s.Name)
		}
	}
}

func firstLines(b []byte, n int) string {
	lines := strings.SplitN(string(b), "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// A repository that declares no projection has nothing to verify: the
// canonical location is the only copy, which is the state spec §9.2
// prefers over a generated one.
func TestVerifyIsSilentWithoutDeclaredProjections(t *testing.T) {
	root := rootAt(t)
	c := &contract.Contract{Schema: 1, Profiles: []string{profiles.CoreRef}}
	if err := Verify(root.Dir(), c, resolve(t, profiles.CoreRef)); err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
}

func TestVerifyFailsWhenADeclaredProjectionWasNeverWritten(t *testing.T) {
	root := rootAt(t)
	err := Verify(root.Dir(), codexContract(), resolve(t, profiles.CoreRef))
	if err == nil {
		t.Fatal("Verify = nil on a projection that does not exist")
	}
	for _, want := range []string{"AGENTS.md", "codex", "pika skills install"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
}

func TestInstallThenVerifyIsClean(t *testing.T) {
	root := rootAt(t)
	c, resolved := codexContract(), resolve(t, profiles.CoreRef)
	st, err := Install(root, c, resolved, false)
	if err != nil {
		t.Fatal(err)
	}
	if !st.OK {
		t.Fatalf("install status not ok: %+v", st)
	}
	for _, s := range st.Skills {
		if !s.Written {
			t.Errorf("skill %s was not written into an empty repository", s.Name)
		}
	}
	if err := Verify(root.Dir(), c, resolved); err != nil {
		t.Fatalf("Verify after install = %v, want nil", err)
	}
}

// The digest in the header is what makes a stale copy distinguishable
// from a current one. Moving the source without regenerating must be
// reported as exactly that, naming the source that moved.
func TestDriftNamesTheSourceThatMoved(t *testing.T) {
	root := rootAt(t)
	c, resolved := codexContract(), resolve(t, profiles.CoreRef)
	if _, err := Install(root, c, resolved, false); err != nil {
		t.Fatal(err)
	}
	skill := root.Skill("project-work")
	body, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, append(body, []byte("\nOne more rule.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	err = Verify(root.Dir(), c, resolved)
	if err == nil {
		t.Fatal("Verify = nil after the source moved")
	}
	if !strings.Contains(err.Error(), ".agents/skills/project-work/SKILL.md") {
		t.Errorf("error does not name the source that moved: %v", err)
	}
	if !strings.Contains(err.Error(), "which is now sha256:") {
		t.Errorf("error does not report the digest that replaced the cited one: %v", err)
	}
	if _, err := Install(root, c, resolved, false); err != nil {
		t.Fatal(err)
	}
	if err := Verify(root.Dir(), c, resolved); err != nil {
		t.Fatalf("Verify after regenerating = %v, want nil", err)
	}
}

// A file whose markers do not pair up is refused rather than rewritten:
// the kernel owns the region, not the file, and here it cannot tell
// where the region ends.
func TestUnpairedMarkersAreRefusedRatherThanRewritten(t *testing.T) {
	root := rootAt(t)
	c, resolved := codexContract(), resolve(t, profiles.CoreRef)
	target := filepath.Join(root.Dir(), "AGENTS.md")
	const damaged = "# Mine\n\n<!-- pika:skills:begin -->\nhalf a region\n"
	if err := os.WriteFile(target, []byte(damaged), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Install(root, c, resolved, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.OK {
		t.Fatal("install reported ok on a file it could not parse")
	}
	if got := readFile(t, target); got != damaged {
		t.Errorf("a file with unpaired markers was rewritten:\n%s", got)
	}
	if st.Projections[0].State != StateUnreadable {
		t.Errorf("state = %s, want %s", st.Projections[0].State, StateUnreadable)
	}
}

func TestTwoRegionsAreRefused(t *testing.T) {
	root := rootAt(t)
	c, resolved := codexContract(), resolve(t, profiles.CoreRef)
	target := filepath.Join(root.Dir(), "AGENTS.md")
	doubled := beginMarker + "\na\n" + endMarker + "\n" + beginMarker + "\nb\n" + endMarker + "\n"
	if err := os.WriteFile(target, []byte(doubled), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Install(root, c, resolved, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.OK || st.Projections[0].State != StateUnreadable {
		t.Fatalf("two regions were not refused: %+v", st.Projections[0])
	}
	if got := readFile(t, target); got != doubled {
		t.Errorf("a file with two regions was rewritten:\n%s", got)
	}
}

// Frontmatter states when a skill loader should read a skill. A
// projection is always read, so carrying it across would state a
// condition nothing evaluates — and would put a YAML fence in the middle
// of a Markdown document.
func TestRenderStripsFrontmatterAndDemotesHeadings(t *testing.T) {
	b := newBody([]canonical{{
		name:   "demo",
		rel:    ".agents/skills/demo/SKILL.md",
		body:   []byte("---\nname: demo\ndescription: d\n---\n\n# Title\n\n## Sub\n\n###### Deep\n"),
		digest: digestOf([]byte("x")),
	}}, nil, repoOrigin)
	got := string(b.region)
	if strings.Contains(got, "description: d") {
		t.Errorf("frontmatter survived into the projection:\n%s", got)
	}
	for _, want := range []string{"\n## Title\n", "\n### Sub\n", "\n###### Deep\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "#######") {
		t.Errorf("a level-six heading was demoted past what Markdown allows:\n%s", got)
	}
}

// Guidance lines are folded across source lines in pack YAML for
// readability. A bullet that carried those newlines through would end
// the list item at the first one.
func TestGuidanceIsRenderedAsOneBulletPerLine(t *testing.T) {
	b := newBody(nil, []profiles.GuidanceSet{{Ref: "go@1", Lines: []string{"first line\nsecond line", "another"}}}, repoOrigin)
	got := string(b.region)
	if !strings.Contains(got, "- first line second line\n- another\n") {
		t.Errorf("guidance was not folded onto one bullet each:\n%s", got)
	}
	if !strings.Contains(got, "<!-- pika:source guidance go@1 sha256:") {
		t.Errorf("guidance is not cited as a source:\n%s", got)
	}
}

// A pack edit that does not touch guidance cannot change what a
// projection says, so it must not be reported as having drifted one.
// profiles.lock is where whole-pack drift is already caught.
func TestGuidanceDigestCoversGuidanceAlone(t *testing.T) {
	same := newBody(nil, []profiles.GuidanceSet{{Ref: "go@1", Lines: []string{"a", "b"}}}, repoOrigin).sources()
	other := newBody(nil, []profiles.GuidanceSet{{Ref: "go@1", Lines: []string{"a", "c"}}}, repoOrigin).sources()
	if len(same) != 1 || len(other) != 1 {
		t.Fatalf("sources = %v / %v", same, other)
	}
	if same[0].Digest == other[0].Digest {
		t.Error("changing a guidance line did not change its digest")
	}
	if same[0].Digest != newBody(nil, []profiles.GuidanceSet{{Ref: "go@1", Lines: []string{"a", "b"}}}, repoOrigin).sources()[0].Digest {
		t.Error("the same guidance hashed to two different digests")
	}
}

// go@1 is the worked example: its guidance must reach a Go repository's
// projection and no other stack's.
func TestGoGuidanceReachesOnlyAGoProjection(t *testing.T) {
	goRoot, tsRoot := rootAt(t), rootAt(t)
	goContract := codexContract()
	goContract.Profiles = []string{profiles.CoreRef, profiles.GoRef}
	if _, err := Install(goRoot, goContract, resolve(t, profiles.CoreRef, profiles.GoRef), false); err != nil {
		t.Fatal(err)
	}
	tsContract := codexContract()
	tsContract.Profiles = []string{profiles.CoreRef, profiles.TypeScriptRef}
	if _, err := Install(tsRoot, tsContract, resolve(t, profiles.CoreRef, profiles.TypeScriptRef), false); err != nil {
		t.Fatal(err)
	}
	goDoc := readFile(t, filepath.Join(goRoot.Dir(), "AGENTS.md"))
	tsDoc := readFile(t, filepath.Join(tsRoot.Dir(), "AGENTS.md"))
	if !strings.Contains(goDoc, "gofmt -l .") {
		t.Errorf("go@1 guidance is missing from a Go repository's projection:\n%s", goDoc)
	}
	if strings.Contains(tsDoc, "gofmt -l .") {
		t.Errorf("go@1 guidance leaked into a TypeScript repository's projection:\n%s", tsDoc)
	}
	if !strings.Contains(goDoc, "## Stack guidance") || strings.Contains(tsDoc, "## Stack guidance") {
		t.Error("the stack guidance section does not follow the pack that supplies it")
	}
}

// An operator's own skill directory is projected alongside the shipped
// ones: a projection that ignored it would be a partial copy of the
// canonical location, which is worse than no copy.
func TestOperatorSkillsAreProjectedAndReported(t *testing.T) {
	root := rootAt(t)
	c, resolved := codexContract(), resolve(t, profiles.CoreRef)
	mine := root.Skill("house-style")
	if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("---\nname: house-style\n---\n\n# House style\n\nCommit messages are imperative.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Install(root, c, resolved, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range st.Skills {
		if s.Name == "house-style" {
			found = true
		}
	}
	if !found {
		t.Errorf("the operator's own skill is missing from the report: %+v", st.Skills)
	}
	doc := readFile(t, filepath.Join(root.Dir(), "AGENTS.md"))
	if !strings.Contains(doc, "Commit messages are imperative.") {
		t.Errorf("the operator's own skill was not projected:\n%s", doc)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A marker is a whole line, not a substring. Documentation above a
// region names the marker inside an inline code span — this repository's
// own AGENTS.md does — and a substring search counted that as a second
// region and refused to regenerate the file. A marker nobody may write
// down is a marker nobody can explain.
func TestMarkersNamedInProseAreNotRegionDelimiters(t *testing.T) {
	root := rootAt(t)
	c, resolved := codexContract(), resolve(t, profiles.CoreRef)
	target := filepath.Join(root.Dir(), "AGENTS.md")
	preamble := "# Mine\n\nThe block below is delimited by `" + beginMarker + "` and `" + endMarker + "`.\n"
	if err := os.WriteFile(target, []byte(preamble), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Install(root, c, resolved, false)
	if err != nil {
		t.Fatal(err)
	}
	if !st.OK {
		t.Fatalf("prose naming the markers blocked the install: %+v", st.Projections)
	}
	doc := readFile(t, target)
	if !strings.HasPrefix(doc, preamble) {
		t.Errorf("the prose was not preserved:\n%s", doc)
	}
	if err := Verify(root.Dir(), c, resolved); err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
}

// A marker indented or trailed by other text is not a delimiter either,
// so a fenced example of the region inside documentation cannot be
// mistaken for the region.
func TestIndentedMarkerIsNotARegionDelimiter(t *testing.T) {
	doc := []byte("    " + beginMarker + "\nbody\n    " + endMarker + "\n")
	start, _, err := regionBounds(doc)
	if err != nil {
		t.Fatalf("regionBounds = %v, want no error", err)
	}
	if start >= 0 {
		t.Errorf("an indented marker was read as a region delimiter at %d", start)
	}
}
