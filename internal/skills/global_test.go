package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/repopath"
)

// homeEnv names the variable os.UserHomeDir consults on this platform.
// The production code never reads it — that is the point of using
// os.UserHomeDir — but a test that wants to see what happens when the
// machine reports no home has to unset the thing the standard library
// looks at.
func homeEnv() string {
	switch runtime.GOOS {
	case "windows":
		return "USERPROFILE"
	case "plan9":
		return "home"
	default:
		return "HOME"
	}
}

// omp discovers a user-level skill at <root>/skills/<name>/SKILL.md and
// reads its frontmatter to decide when to load it. A file with no `name`
// and no `description` is not surfaced at all, so the install that wrote
// it would have produced a file no agent ever reads — a silent failure
// with a green exit code.
func TestGlobalSkillFileOpensWithTheFrontmatterItsLoaderNeeds(t *testing.T) {
	home := t.TempDir()
	if _, err := InstallGlobal(home); err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(filepath.Join(home, ".agents", "skills", "pika", "SKILL.md"))
	if err != nil {
		t.Fatalf("the omp user-level skill was not written: %v", err)
	}
	if !bytes.HasPrefix(doc, []byte("---\n")) {
		t.Fatalf("the file does not open with frontmatter:\n%s", firstLines(doc, 6))
	}
	head, _, ok := bytes.Cut(doc[len("---\n"):], []byte("\n---\n"))
	if !ok {
		t.Fatalf("the frontmatter block is not closed:\n%s", firstLines(doc, 6))
	}
	for _, want := range []string{"name: pika", "description: "} {
		if !bytes.Contains(head, []byte(want)) {
			t.Errorf("frontmatter is missing %q:\n%s", want, head)
		}
	}
	// The frontmatter is outside the region and the region follows it,
	// which is the only order that works: a marker cannot come first
	// without displacing the frontmatter, and displaced frontmatter is
	// not frontmatter.
	if bytes.Index(doc, []byte(beginMarker)) < len(head) {
		t.Errorf("the kernel-owned region starts before the frontmatter ends:\n%s", firstLines(doc, 8))
	}
}

func TestInstallGlobalWritesBothGlobalTargets(t *testing.T) {
	home := t.TempDir()
	rep, err := InstallGlobal(home)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("install reported not-ok: %+v", rep.Targets)
	}
	if len(rep.Targets) != 2 {
		t.Fatalf("targets = %d, want 2: %+v", len(rep.Targets), rep.Targets)
	}
	var regions [][]byte
	for _, target := range rep.Targets {
		if !target.Written {
			t.Errorf("%s was not reported as written", target.Path)
		}
		if target.State != StateCurrent {
			t.Errorf("%s state = %q, want %q", target.Path, target.State, StateCurrent)
		}
		doc, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(target.Path)))
		if err != nil {
			t.Fatalf("%s: %v", target.Path, err)
		}
		region, ok, err := extractRegion(doc)
		if err != nil || !ok {
			t.Fatalf("%s carries no readable region (%v):\n%s", target.Path, err, doc)
		}
		regions = append(regions, region)
	}
	if !bytes.Equal(regions[0], regions[1]) {
		t.Error("the two global targets carry different regions; they are rendered from the same templates and must say the same thing")
	}
	// Every target class is checked by the same mechanism, so the
	// markers, the self-digest and the provenance header all have to be
	// there — a global file without them would be a file the kernel can
	// write and never verify.
	text := string(regions[0])
	for _, want := range []string{
		beginMarker,
		endMarker,
		"<!-- pika:region sha256:",
		"<!-- pika:source template templates/global/pika.md sha256:",
		"<!-- pika:source template templates/project-work.md sha256:",
		"<!-- pika:source template templates/project-review.md sha256:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("region is missing %q:\n%s", want, text)
		}
	}
	if err := verifyRegion(regions[0]); err != nil {
		t.Errorf("a freshly written region does not hash to its own digest: %v", err)
	}
	if _, err := InstallGlobal(home); err != nil {
		t.Fatal(err)
	}
	again := InspectGlobal(home)
	for _, target := range again.Targets {
		if target.State != StateCurrent {
			t.Errorf("%s state = %q after a second install, want %q (%s)", target.Path, target.State, StateCurrent, target.Detail)
		}
	}
}

// A global skill is read where there may be no contract at all, so the
// one thing the repository skills cannot say is the first thing it has
// to: how to get from an ungoverned directory to a governed one, and
// that a governed repository's own skills outrank this copy.
func TestGlobalRegionRoutesAnUngovernedRepositoryAndDefersToAGovernedOne(t *testing.T) {
	text := string(globalBody().region)
	for _, want := range []string{
		".project/contract.yaml",
		"pika init",
		"pika adopt",
		"pika apply",
		".agents/skills/",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the global region never mentions %q:\n%s", want, text)
		}
	}
	// The routing preface has to come before the text that assumes a
	// contract, or an agent acts on the assumption before it reads the
	// check on it.
	routing := strings.Index(text, "pika adopt")
	governed := strings.Index(text, "## Driving pika")
	if routing < 0 || governed < 0 || routing > governed {
		t.Errorf("the ungoverned-repository routing does not come first (routing at %d, governed section at %d)", routing, governed)
	}
}

// The region is kernel-owned; the file is not. A home-directory
// AGENTS.md is where an operator keeps notes about every tool they use,
// and an install that took over the file rather than its own region
// would delete all of them.
func TestForeignProseInTheGlobalCodexFileSurvivesAnInstall(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".codex", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	const foreign = "# My notes\n\nAlways run the deploy script by hand.\nNever push to main on a Friday.\n"
	const trailing = "\n## Unrelated tool\n\nRemember the staging password is in 1Password.\n"
	if err := os.WriteFile(target, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallGlobal(home); err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(doc), foreign) {
		t.Fatalf("prose above the region did not survive:\n%s", doc)
	}

	// Prose below the region is the operator's too, and it is the half a
	// naive append would eat.
	if err := os.WriteFile(target, append(doc, []byte(trailing)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallGlobal(home); err != nil {
		t.Fatal(err)
	}
	doc, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(doc), foreign) || !strings.HasSuffix(string(doc), trailing) {
		t.Fatalf("operator prose around the region did not survive a regeneration:\n%s", doc)
	}
	if n := strings.Count(string(doc), beginMarker); n != 1 {
		t.Errorf("region count = %d, want 1: a regeneration accumulated regions", n)
	}
}

// The same digest that catches a hand edit in a repository projection
// has to catch one here, or the file a harness actually reads is the one
// nothing verifies.
func TestAHandEditInsideTheGlobalMarkersIsReportedTampered(t *testing.T) {
	home := t.TempDir()
	if _, err := InstallGlobal(home); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".codex", "AGENTS.md")
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
	rep := InspectGlobal(home)
	if rep.OK {
		t.Error("a hand-edited global file was reported ok")
	}
	got := targetNamed(t, rep, ".codex/AGENTS.md")
	if got.State != StateTampered {
		t.Fatalf("state = %q, want %q (%s)", got.State, StateTampered, got.Detail)
	}
	for _, want := range []string{"edited by hand inside the pika skills markers", "DISCARD", "pika skills install --global"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail is missing %q:\n%s", want, got.Detail)
		}
	}
	// The remedy for a repository projection is to edit the canonical
	// skill. There is no canonical skill behind a global file, so
	// sending an operator to one would be advice they cannot follow.
	if strings.Contains(got.Detail, ".agents/skills/") {
		t.Errorf("a tampered global file was told to edit a repository skill directory:\n%s", got.Detail)
	}
}

// A global file written by an older pika is authentic — it still hashes
// to the digest it carries — and simply out of date. Its remedy is the
// opposite of a hand edit's, so the two must never share a label.
func TestAGlobalFileFromAnOlderPikaIsReportedStale(t *testing.T) {
	home := t.TempDir()
	if _, err := InstallGlobal(home); err != nil {
		t.Fatal(err)
	}
	// Render a region from a template set this binary does not have,
	// which is exactly what an older pika left behind.
	older := newBody([]canonical{{
		kind:   SourceTemplate,
		name:   "pika",
		rel:    "templates/global/pika.md",
		body:   []byte("---\nname: pika\n---\n\n# Older text\n"),
		digest: digestOf([]byte("older")),
	}}, nil, globalOrigin)
	target := filepath.Join(home, ".codex", "AGENTS.md")
	if err := os.WriteFile(target, older.region, 0o644); err != nil {
		t.Fatal(err)
	}

	rep := InspectGlobal(home)
	got := targetNamed(t, rep, ".codex/AGENTS.md")
	if got.State != StateStale {
		t.Fatalf("state = %q, want %q (%s)", got.State, StateStale, got.Detail)
	}
	for _, want := range []string{"templates/global/pika.md", "pika skills install --global"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("stale detail does not name %q:\n%s", want, got.Detail)
		}
	}
	if strings.Contains(got.Detail, "edited by hand") {
		t.Errorf("an out-of-date file was reported as a hand edit:\n%s", got.Detail)
	}
	// Regenerating is the whole remedy, and it works.
	if _, err := InstallGlobal(home); err != nil {
		t.Fatal(err)
	}
	if got := targetNamed(t, InspectGlobal(home), ".codex/AGENTS.md"); got.State != StateCurrent {
		t.Fatalf("state = %q after regenerating, want %q (%s)", got.State, StateCurrent, got.Detail)
	}
}

func TestAnUninstalledGlobalFileIsReportedAbsentAndNotWritten(t *testing.T) {
	home := t.TempDir()
	rep := InspectGlobal(home)
	if rep.OK {
		t.Error("a home directory with no global files was reported ok by check")
	}
	for _, target := range rep.Targets {
		if target.State != StateAbsent {
			t.Errorf("%s state = %q, want %q", target.Path, target.State, StateAbsent)
		}
		if !strings.Contains(target.Detail, "pika skills install --global") {
			t.Errorf("%s does not name the command that would write it: %s", target.Path, target.Detail)
		}
		if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(target.Path))); err == nil {
			t.Errorf("inspecting wrote %s; a report must not install anything", target.Path)
		}
	}
}

// A home directory that cannot be resolved is an error and never a
// silent skip. Falling back to a relative path would write an
// instruction file into whatever directory the operator happened to be
// standing in and report success for it.
func TestResolveHomeFailsRatherThanFallingBackWhenThereIsNoHome(t *testing.T) {
	t.Setenv(homeEnv(), "")
	home, err := ResolveHome("")
	if err == nil {
		t.Fatalf("ResolveHome returned %q with no home directory, want an error", home)
	}
	if !strings.Contains(err.Error(), "home directory") {
		t.Errorf("the error does not say what could not be resolved: %v", err)
	}
	// The override is what a test and a sandbox use, and it must still
	// work when the machine reports nothing.
	dir := t.TempDir()
	got, err := ResolveHome(dir)
	if err != nil {
		t.Fatalf("ResolveHome(%q): %v", dir, err)
	}
	if got != dir {
		t.Errorf("ResolveHome(%q) = %q", dir, got)
	}
}

// THE SECURITY BOUNDARY: contract content cannot cause a write outside
// the repository, and therefore cannot reach the operator's home
// directory.
//
// contract.Load already refuses an absolute path, a `~` path and a `..`
// escape when it parses the file, and there is a test next to it that
// says so. This is the same rule enforced again at the only place that
// opens a file for writing, against a Contract assembled in memory — the
// shape a parser check cannot see. A boundary held in one place is one
// that the next caller to build a Contract without Load walks straight
// through, and the cost of being wrong here is a repository writing into
// somebody's home directory because they cloned it.
func TestContractContentCannotCauseAWriteOutsideTheRepository(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	root, err := repopath.At(repo)
	if err != nil {
		t.Fatal(err)
	}
	escape, err := filepath.Rel(repo, filepath.Join(outside, ".codex", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(escape, "..") {
		t.Fatalf("fixture path %q does not leave the repository", escape)
	}
	c := &contract.Contract{
		Skills: &contract.Skills{Projections: []contract.Projection{
			{Harness: "codex", Path: filepath.ToSlash(escape)},
		}},
	}

	_, err = Install(root, c, nil, false)
	if err == nil {
		t.Fatal("Install accepted a projection path outside the repository root")
	}
	if !strings.Contains(err.Error(), "outside the repository root") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, ".codex", "AGENTS.md")); statErr == nil {
		t.Fatal("a contract wrote a file outside the repository it governs")
	}

	// Reading is refused on the same terms. A contract that could name
	// any path on the machine would turn `pika check` into a way of
	// asking whether a file exists outside the repository.
	st, err := Inspect(root, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Projections) != 1 || st.Projections[0].State != StateUnreadable {
		t.Fatalf("inspect = %+v, want one unreadable projection", st.Projections)
	}
}

// targetNamed returns the one status for path, so a failure names the
// target rather than printing the whole report.
func targetNamed(t *testing.T, rep *GlobalReport, path string) GlobalStatus {
	t.Helper()
	for _, target := range rep.Targets {
		if target.Path == path {
			return target
		}
	}
	t.Fatalf("no target %q in %+v", path, rep.Targets)
	return GlobalStatus{}
}
