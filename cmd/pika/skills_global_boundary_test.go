package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE SECURITY BOUNDARY: no contract content can cause a write to the
// operator's home directory.
//
// Installing the global agent files is an explicit --global on a command
// line and nothing else. If a committed file could ask for it — by
// naming a global target, by an absolute path, by a `~` path, or by
// climbing out of the repository with `..` — then cloning a repository
// would hand it a capability over the machine that cloned it, and every
// checkout of every repository would be exercising it.
//
// Each shape below must be refused at exit 2 with the offending field
// named, and the home directory must be untouched afterwards.
func TestNoContractContentCanCauseAWriteToTheOperatorHomeDirectory(t *testing.T) {
	home := t.TempDir()
	// Two real global targets under a home the contract will try to
	// reach. If any refusal below leaks, one of these appears.
	ompTarget := filepath.Join(home, filepath.FromSlash(ompGlobalSkill))
	codexTarget := filepath.Join(home, filepath.FromSlash(codexGlobalFile))

	for _, tc := range []struct {
		name string
		path string
	}{
		{"an absolute path to a global target", filepath.ToSlash(codexTarget)},
		{"a home-relative path to the omp global skill", "~/" + ompGlobalSkill},
		{"a home-relative path to the codex global file", "~/" + codexGlobalFile},
		{"a path that climbs out of the repository", "../../../../../../../../.codex/AGENTS.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := skillsProject(t, "skills:\n  projections:\n    - harness: codex\n      path: "+tc.path+"\n")
			code, out, errb := runSkillsIn(t, dir, "install")
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stdout %s stderr %s", code, out, errb)
			}
			if !strings.Contains(errb, "skills.projections[0].path") {
				t.Errorf("the refusal does not name the offending field: %s", errb)
			}
		})
	}

	// The harness name is not a lever either: `omp` is a legitimate
	// projection harness, and naming it changes only which harness the
	// file is for, never where the file goes.
	dir := skillsProject(t, "skills:\n  projections:\n    - harness: omp\n      path: AGENTS.md\n")
	if code, out, errb := runSkillsIn(t, dir, "install"); code != 0 {
		t.Fatalf("an omp projection inside the repository was refused: exit %d, stdout %s stderr %s", code, out, errb)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Fatalf("the in-repository omp projection was not written: %v", err)
	}

	// A contract cannot even name the capability: `skills` is a closed
	// object, so there is no key an author could set to ask for a global
	// install and no key a future reader has to remember to ignore.
	closed := skillsProject(t, "skills:\n  global: true\n")
	if code, out, errb := runSkillsIn(t, closed, "install"); code != 2 {
		t.Fatalf("a contract asking for a global install exited %d, want 2; stdout %s stderr %s", code, out, errb)
	}

	for _, target := range []string{ompTarget, codexTarget} {
		if _, err := os.Stat(target); err == nil {
			t.Fatalf("contract content produced a write to %s", target)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the home directory is no longer empty after every refusal: %v", entries)
	}
}

// Gate 1 must never consult the global files. They are absent from a
// fresh checkout by definition, so a gate that digested them would fail
// on every clone of every repository — and a repository has no business
// having an opinion about the operator's home directory in the first
// place. Their state is reported and never enforced.
func TestTheVerificationLadderIgnoresTheGlobalAgentFiles(t *testing.T) {
	home := t.TempDir()
	dir := skillsProject(t, codexProjection)
	if code, out, errb := runSkillsIn(t, dir, "install"); code != 0 {
		t.Fatalf("repository install exit = %d; stdout %s stderr %s", code, out, errb)
	}
	if code, out, errb := runSkillsGlobalIn(t, home, "install"); code != 0 {
		t.Fatalf("global install exit = %d; stdout %s stderr %s", code, out, errb)
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

	// The global report says so.
	if code, _, _ := runSkillsGlobalIn(t, home, "check"); code != 1 {
		t.Fatalf("global check exit = %d on a tampered file, want 1", code)
	}
	// The repository ladder does not, and must not.
	var out, errb bytes.Buffer
	if code := runCheck([]string{"--root", dir}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("pika check exit = %d with a tampered global file, want 0; stdout %s stderr %s", code, out.String(), errb.String())
	}
	if strings.Contains(out.String(), ".codex") {
		t.Errorf("the verification ladder mentioned a global file:\n%s", out.String())
	}
}
