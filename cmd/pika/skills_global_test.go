package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The global agent files live in the operator's home directory, so every
// test here is handed a temporary one through --home. Nothing in this
// file may read, let alone write, the developer's own: a test suite that
// installs into somebody's home directory is a test suite that changes
// the machine it runs on.
func runSkillsGlobalIn(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runSkills(append(args, "--global", "--home", home), strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

// homeEnvVar names the variable os.UserHomeDir consults on this
// platform. pika never reads it — that is why it calls os.UserHomeDir —
// but a test that wants to see the no-home path has to unset what the
// standard library looks at.
func homeEnvVar() string {
	switch runtime.GOOS {
	case "windows":
		return "USERPROFILE"
	case "plan9":
		return "home"
	default:
		return "HOME"
	}
}

const (
	ompGlobalSkill  = ".agents/skills/pika/SKILL.md"
	codexGlobalFile = ".codex/AGENTS.md"
)

func TestSkillsInstallGlobalWritesBothGlobalTargets(t *testing.T) {
	home := t.TempDir()
	code, out, errb := runSkillsGlobalIn(t, home, "install")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout %s stderr %s", code, out, errb)
	}
	for _, rel := range []string{ompGlobalSkill, codexGlobalFile} {
		doc, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("%s was not written: %v", rel, err)
		}
		for _, want := range []string{
			"<!-- pika:skills:begin -->",
			"<!-- pika:skills:end -->",
			"<!-- pika:region sha256:",
			"<!-- pika:source template templates/global/pika.md sha256:",
			"pika adopt",
			"The ladder is the evidence",
		} {
			if !strings.Contains(string(doc), want) {
				t.Errorf("%s is missing %q:\n%s", rel, want, doc)
			}
		}
		if !strings.Contains(out, rel) {
			t.Errorf("the report does not name %s:\n%s", rel, out)
		}
	}
	// The omp loader reads frontmatter and nothing else to decide
	// whether the skill exists at all.
	skill, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(ompGlobalSkill)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(skill), "---\nname: pika\n") {
		t.Errorf("the omp skill does not open with its loader frontmatter:\n%s", head(string(skill), 6))
	}
	if code, out, _ := runSkillsGlobalIn(t, home, "check"); code != 0 {
		t.Fatalf("check exit = %d after a fresh install, want 0:\n%s", code, out)
	}
}

// A home-directory AGENTS.md is where an operator keeps notes about
// every tool they use. The kernel owns a region inside it and never the
// file, exactly as it does for a repository projection, and an install
// that could not tell the difference would delete somebody's notes the
// first time they ran it.
func TestForeignProseInTheGlobalCodexFileSurvivesAnInstall(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, filepath.FromSlash(codexGlobalFile))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	const above = "# My own guidance\n\nThe staging deploy is manual. Ask before touching terraform/.\n"
	const below = "\n## Notes about a different tool\n\nThe linter config lives in ~/.config, not the repo.\n"
	if err := os.WriteFile(target, []byte(above), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errb := runSkillsGlobalIn(t, home, "install"); code != 0 {
		t.Fatalf("exit = %d; stdout %s stderr %s", code, out, errb)
	}
	doc, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(doc), above) {
		t.Fatalf("prose above the region did not survive:\n%s", doc)
	}
	if err := os.WriteFile(target, append(doc, []byte(below)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errb := runSkillsGlobalIn(t, home, "install"); code != 0 {
		t.Fatalf("second install exit = %d; stdout %s stderr %s", code, out, errb)
	}
	doc, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(doc), above) {
		t.Errorf("prose above the region did not survive a regeneration:\n%s", doc)
	}
	if !strings.HasSuffix(string(doc), below) {
		t.Errorf("prose below the region did not survive a regeneration:\n%s", doc)
	}
	// Whole-line marker occurrences only (region.go's markerLines):
	// project-maintain's own prose legitimately quotes the marker
	// syntax inside a sentence, which a raw substring count would
	// mistake for a second region.
	if n := countMarkerLines([]byte(doc), "<!-- pika:skills:begin -->"); n != 1 {
		t.Errorf("region count = %d, want 1", n)
	}
}

// The digest that makes a repository projection kernel-owned in fact has
// to do the same work here. A global file is the one an agent reads when
// no repository is telling it anything, so an edit inside the markers
// that nothing detected would be the most durable way to give an agent
// wrong instructions.
func TestSkillsCheckGlobalReportsAHandEditAsTampered(t *testing.T) {
	home := t.TempDir()
	if code, out, errb := runSkillsGlobalIn(t, home, "install"); code != 0 {
		t.Fatalf("install exit = %d; stdout %s stderr %s", code, out, errb)
	}
	target := filepath.Join(home, filepath.FromSlash(codexGlobalFile))
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

	code, out, _ := runSkillsGlobalIn(t, home, "check")
	if code != 1 {
		t.Fatalf("check exit = %d on a hand-edited global file, want 1:\n%s", code, out)
	}
	for _, want := range []string{"tampered", codexGlobalFile, "edited by hand inside the pika skills markers", "DISCARD"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "stale") {
		t.Errorf("a hand edit was reported as an out-of-date copy:\n%s", out)
	}
	// The untouched target must not be dragged down with it: the two
	// files are verified independently, and a report that blamed both
	// would send an operator looking for an edit that is not there.
	if !strings.Contains(out, "current    "+ompGlobalSkill) {
		t.Errorf("the untouched global skill is no longer reported current:\n%s", out)
	}
}

// A global file written by an older pika is authentic and simply out of
// date: it still hashes to the digest it carries, so nothing anyone
// typed is at risk and regenerating is the whole remedy. Reporting it
// with the same word as a hand edit would send an operator to protect an
// edit that does not exist — or worse, teach them that "tampered" is
// noise.
func TestSkillsCheckGlobalReportsAnOlderInstallAsStale(t *testing.T) {
	home := t.TempDir()
	if code, out, errb := runSkillsGlobalIn(t, home, "install"); code != 0 {
		t.Fatalf("install exit = %d; stdout %s stderr %s", code, out, errb)
	}
	target := filepath.Join(home, filepath.FromSlash(codexGlobalFile))
	rewriteAsOlderInstall(t, target)

	code, out, _ := runSkillsGlobalIn(t, home, "check")
	if code != 1 {
		t.Fatalf("check exit = %d on an out-of-date global file, want 1:\n%s", code, out)
	}
	for _, want := range []string{"stale", codexGlobalFile, "templates/global/pika.md", "pika skills install --global"} {
		if !strings.Contains(out, want) {
			t.Errorf("the stale report is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tampered") {
		t.Errorf("an out-of-date copy was reported as a hand edit:\n%s", out)
	}
	if code, _, errb := runSkillsGlobalIn(t, home, "install"); code != 0 {
		t.Fatalf("regenerate exit = %d; stderr %s", code, errb)
	}
	if code, out, _ := runSkillsGlobalIn(t, home, "check"); code != 0 {
		t.Fatalf("check exit = %d after regenerating, want 0:\n%s", code, out)
	}
}

// rewriteAsOlderInstall turns a freshly written global file into what an
// older pika would have left: a region that still hashes to the digest
// it carries, citing a template digest this binary does not have.
//
// Recomputing the region digest rather than corrupting it is the whole
// point of the fixture. A file with a broken digest is a hand edit, and
// this test is about the other failure — so the fixture has to be
// authentic kernel output from a different version, not damaged output
// from this one.
func rewriteAsOlderInstall(t *testing.T, path string) {
	t.Helper()
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(doc), "\n")
	if len(lines) < 4 || lines[0] != "<!-- pika:skills:begin -->" {
		t.Fatalf("fixture expects a file that is exactly one region:\n%s", head(string(doc), 4))
	}
	if !strings.HasPrefix(lines[2], "<!-- pika:region sha256:") {
		t.Fatalf("the region digest is not on the third line:\n%s", head(string(doc), 4))
	}
	changed := false
	rest := append([]string{}, lines[3:]...)
	for i, line := range rest {
		const prefix = "<!-- pika:source template templates/global/pika.md sha256:"
		if strings.HasPrefix(line, prefix) {
			rest[i] = prefix + strings.Repeat("0", 64) + " -->"
			changed = true
			break
		}
	}
	if !changed {
		t.Fatalf("fixture found no provenance line to age:\n%s", doc)
	}
	core := strings.Join(append([]string{lines[0], lines[1]}, rest...), "\n")
	sum := sha256.Sum256([]byte(core))
	digest := "<!-- pika:region sha256:" + hex.EncodeToString(sum[:]) + " (covers this region excluding this line) -->"
	next := strings.Join(append([]string{lines[0], lines[1], digest}, rest...), "\n")
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A home directory that cannot be resolved is exit 2 with the reason
// named. The operator asked for the global files by name; reporting
// "nothing to do", or writing under a relative path, would report
// success for work that never happened.
func TestSkillsGlobalWithNoResolvableHomeExits2(t *testing.T) {
	t.Setenv(homeEnvVar(), "")
	var out, errb bytes.Buffer
	code := runSkills([]string{"install", "--global"}, strings.NewReader(""), &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stdout %s stderr %s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "home directory") {
		t.Errorf("the refusal does not name what could not be resolved: %s", errb.String())
	}
}

// A flag that means nothing in the mode it was passed in is refused
// rather than ignored. A flag silently dropped is how an operator
// concludes a command did something it did not.
func TestSkillsGlobalRefusesFlagsThatDoNotApplyToIt(t *testing.T) {
	home := t.TempDir()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"install", "--global", "--reset-docs", "--home", home}, "--reset-docs does not apply to --global"},
		{[]string{"install", "--global", "--root", home, "--home", home}, "--root does not apply to --global"},
		{[]string{"install", "--home", home}, "--home applies only with --global"},
	} {
		var out, errb bytes.Buffer
		code := runSkills(tc.args, strings.NewReader(""), &out, &errb)
		if code != 2 {
			t.Errorf("%v exit = %d, want 2; stdout %s stderr %s", tc.args, code, out.String(), errb.String())
		}
		if !strings.Contains(errb.String(), tc.want) {
			t.Errorf("%v did not explain itself (%q missing): %s", tc.args, tc.want, errb.String())
		}
	}
	// And nothing was written while refusing.
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("a refused invocation wrote into the home directory: %v (%v)", entries, err)
	}
}

// The report mode is read-only in every mode, including this one: an
// operator who does not yet know what state their home directory is in
// must be able to ask without changing it.
func TestSkillsGlobalReportNeitherWritesNorFails(t *testing.T) {
	home := t.TempDir()
	code, out, errb := runSkillsGlobalIn(t, home)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout %s stderr %s", code, out, errb)
	}
	for _, want := range []string{"home  " + home, "absent", ompGlobalSkill, codexGlobalFile, "no gate checks these files"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report is missing %q:\n%s", want, out)
		}
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("the report wrote into the home directory: %v (%v)", entries, err)
	}
	// check, by contrast, is the verdict: absent files are not in place.
	if code, _, _ := runSkillsGlobalIn(t, home, "check"); code != 1 {
		t.Errorf("check exit = %d with nothing installed, want 1", code)
	}
}

func TestSkillsGlobalJSONCarriesEveryTargetAndItsProvenance(t *testing.T) {
	home := t.TempDir()
	if code, out, errb := runSkillsGlobalIn(t, home, "install"); code != 0 {
		t.Fatalf("install exit = %d; stdout %s stderr %s", code, out, errb)
	}
	code, out, errb := runSkillsGlobalIn(t, home, "--json")
	if code != 0 {
		t.Fatalf("exit = %d; stdout %s stderr %s", code, out, errb)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Home    string `json:"home"`
			Targets []struct {
				Harness string `json:"harness"`
				Path    string `json:"path"`
				State   string `json:"state"`
				Region  string `json:"region"`
				Sources []struct {
					Kind   string `json:"kind"`
					Ref    string `json:"ref"`
					Digest string `json:"digest"`
				} `json:"sources"`
			} `json:"targets"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("stdout is not a cliout envelope: %v\n%s", err, out)
	}
	if !env.OK {
		t.Errorf("ok = false on a fresh install:\n%s", out)
	}
	if env.Result.Home != home {
		t.Errorf("home = %q, want %q", env.Result.Home, home)
	}
	if len(env.Result.Targets) != 2 {
		t.Fatalf("targets = %d, want 2:\n%s", len(env.Result.Targets), out)
	}
	for _, target := range env.Result.Targets {
		if target.State != "current" {
			t.Errorf("%s state = %q, want current", target.Path, target.State)
		}
		if !strings.HasPrefix(target.Region, "sha256:") {
			t.Errorf("%s carries no region digest a consumer can compare", target.Path)
		}
		if len(target.Sources) == 0 {
			t.Errorf("%s cites no sources", target.Path)
		}
		for _, s := range target.Sources {
			if s.Kind != "template" {
				t.Errorf("%s cites a %s source; a global file is generated from this binary and nothing else", target.Path, s.Kind)
			}
			if !strings.HasPrefix(s.Ref, "templates/") || !strings.HasPrefix(s.Digest, "sha256:") {
				t.Errorf("%s cites %+v, which does not identify a template and its digest", target.Path, s)
			}
		}
	}
}

func head(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
