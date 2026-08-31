package contract

import (
	"strings"
	"testing"
)

func TestNormalizeRepoPath(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "valid relative", in: "web/app", want: "web/app"},
		{name: "dot cleaned", in: "./web/./app", want: "web/app"},
		{name: "internal dotdot cleans away", in: "web/../app", want: "app"},
		{name: "trailing slash cleaned", in: "web/app/", want: "web/app"},
		{name: "backslash normalized", in: `web\app`, want: "web/app"},
		{name: "single segment", in: "cmd", want: "cmd"},
		{name: "repo root is legal", in: ".", want: "."},
		{name: "escape rejected", in: "../../etc", wantErr: "path escapes repository root: ../../etc"},
		{name: "parent rejected", in: "..", wantErr: "path escapes repository root: .."},
		{name: "absolute rejected", in: "/etc", wantErr: "path escapes repository root: /etc"},
		{name: "drive letter rejected", in: `C:\x`, wantErr: "path escapes repository root: C:\\x"},
		{name: "drive letter forward slash rejected", in: "C:/x", wantErr: "path escapes repository root: C:/x"},
		{name: "unc rejected", in: `\\server\share`, wantErr: "path escapes repository root: \\\\server\\share"},
		{name: "empty rejected", in: "", wantErr: "path is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeRepoPath(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("NormalizeRepoPath(%q) = %q, want error %q", tc.in, got, tc.wantErr)
				}
				if err.Error() != tc.wantErr {
					t.Fatalf("NormalizeRepoPath(%q) error = %q, want %q", tc.in, err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeRepoPath(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeRepoPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLoadRejectsPathEscape(t *testing.T) {
	_, err := Load("testdata/invalid-escape-root.yaml")
	if err == nil {
		t.Fatal("expected escape error, got nil")
	}
	want := "contract: packages.frontend.root: path escapes repository root: ../../etc"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestLoadRejectsAbsolutePath(t *testing.T) {
	_, err := Load("testdata/invalid-absolute-root.yaml")
	if err == nil {
		t.Fatal("expected absolute-path error, got nil")
	}
	if !strings.Contains(err.Error(), "packages.frontend.root") ||
		!strings.Contains(err.Error(), "/etc") {
		t.Fatalf("error should name field and value, got: %v", err)
	}
}

func TestLoadRejectsDriveLetterPath(t *testing.T) {
	_, err := Load("testdata/invalid-drive-root.yaml")
	if err == nil {
		t.Fatal("expected drive-letter error, got nil")
	}
	if !strings.Contains(err.Error(), "packages.frontend.root") ||
		!strings.Contains(err.Error(), "C:\\x") {
		t.Fatalf("error should name field and value, got: %v", err)
	}
}

func TestLoadRejectsEmptyRoot(t *testing.T) {
	_, err := Load("testdata/invalid-empty-root.yaml")
	if err == nil {
		t.Fatal("expected empty-path error, got nil")
	}
	if !strings.Contains(err.Error(), "packages.frontend.root") {
		t.Fatalf("error should name the offending field, got: %v", err)
	}
}

func TestLoadNormalizesBackslashRoot(t *testing.T) {
	c, err := Load("testdata/valid-normalized-root.yaml")
	if err != nil {
		t.Fatalf("expected valid contract, got %v", err)
	}
	if c.Packages["frontend"].Root != "web/app" {
		t.Fatalf("root = %q, want %q", c.Packages["frontend"].Root, "web/app")
	}
}

// A projection path is a repository path like any other, so it is
// normalized and escape-checked on the same terms as packages.<name>.root:
// a projection is a file the kernel writes, and one that could name a
// path outside the repository would be a write primitive the contract
// never meant to grant.
func TestLoadNormalizesProjectionPaths(t *testing.T) {
	c, err := Load("testdata/valid-skills-projections.yaml")
	if err != nil {
		t.Fatalf("expected valid contract, got %v", err)
	}
	if c.Skills == nil || len(c.Skills.Projections) != 2 {
		t.Fatalf("projections = %+v, want two", c.Skills)
	}
	if got := c.Skills.Projections[1].Path; got != "docs/CLAUDE.md" {
		t.Fatalf("path = %q, want %q", got, "docs/CLAUDE.md")
	}
}

func TestLoadRejectsProjectionPathEscape(t *testing.T) {
	_, err := Load("testdata/invalid-skills-escape.yaml")
	if err == nil {
		t.Fatal("expected escape error, got nil")
	}
	if !strings.Contains(err.Error(), "skills.projections[0].path") {
		t.Fatalf("error should name the offending field, got: %v", err)
	}
}

// A `~` path is not a traversal, which is why it needs its own rule. Go
// expands nothing, so `~/.codex/AGENTS.md` would be taken as an ordinary
// relative path and would create a directory literally named `~` inside
// the repository: a contract that reads as though it writes to the
// operator's home directory, writing rubbish instead. Refusing it is
// what makes the answer to "can a repository reach my home directory"
// a plain no rather than "no, but it looks like yes".
func TestLoadRejectsAHomeRelativeProjectionPath(t *testing.T) {
	_, err := Load("testdata/invalid-skills-home.yaml")
	if err == nil {
		t.Fatal("a contract naming ~/.codex/AGENTS.md was accepted")
	}
	if !strings.Contains(err.Error(), "skills.projections[0].path") {
		t.Fatalf("error should name the offending field, got: %v", err)
	}
	if !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("error should say why a home path is refused, got: %v", err)
	}
}

// Every shape of path that leaves the repository is refused by the one
// function every declared path goes through, so a new caller inherits
// the rule instead of re-deriving it.
func TestNormalizeRepoPathRefusesEveryPathOutsideTheRepository(t *testing.T) {
	for _, p := range []string{
		"/etc/passwd",
		"C:\\Users\\me\\.codex\\AGENTS.md",
		"~",
		"~/.agents/skills/pika/SKILL.md",
		"../AGENTS.md",
		"a/../../AGENTS.md",
	} {
		if got, err := NormalizeRepoPath(p); err == nil {
			t.Errorf("NormalizeRepoPath(%q) = %q, want an error", p, got)
		}
	}
	// The rule must not swallow ordinary paths on the way past.
	for _, p := range []string{"AGENTS.md", "docs/CLAUDE.md", "a/~b/AGENTS.md"} {
		if _, err := NormalizeRepoPath(p); err != nil {
			t.Errorf("NormalizeRepoPath(%q): %v", p, err)
		}
	}
}

// The skills block declares repository projections and nothing else. An
// unknown key there is refused rather than dropped, because the one an
// author is most likely to invent is a request for the agent files in
// the operator's home directory — and silence would leave them believing
// their contract asked for something that no contract may ask for at any
// spelling.
func TestLoadRejectsAnUnknownKeyInTheSkillsBlock(t *testing.T) {
	_, err := Load("testdata/invalid-skills-unknown-key.yaml")
	if err == nil {
		t.Fatal("a contract declaring skills.global was accepted")
	}
	if !strings.Contains(err.Error(), "global") {
		t.Fatalf("error should name the key it refused, got: %v", err)
	}
}

// A harness the kernel does not know is a schema violation, not a
// projection that is quietly skipped. A file nothing reads is
// indistinguishable from a file something reads, so a typo that produced
// one would never be discovered — which is exactly how a stale parallel
// copy comes to exist.
func TestLoadRejectsUnknownHarness(t *testing.T) {
	_, err := Load("testdata/invalid-skills-harness.yaml")
	if err == nil {
		t.Fatal("expected a schema error for an unknown harness, got nil")
	}
	if !strings.Contains(err.Error(), "schema validation failed") {
		t.Fatalf("error should come from schema validation, got: %v", err)
	}
}

// A contract that declares no skills block stays nil rather than
// becoming an empty one, so the generated YAML of every project that
// never asked for a projection is unchanged.
func TestLoadLeavesSkillsUndeclared(t *testing.T) {
	c, err := Load("testdata/valid-minimum.yaml")
	if err != nil {
		t.Fatalf("expected valid contract, got %v", err)
	}
	if c.Skills != nil {
		t.Fatalf("skills = %+v, want nil on a contract that declares none", c.Skills)
	}
}
