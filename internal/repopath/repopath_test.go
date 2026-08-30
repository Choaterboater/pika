package repopath

import (
	"os"
	"path/filepath"
	"testing"
)

func mkdirAll(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	return p
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestFindWalksUpToContract(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".project", "contract.yaml"), "schema: 1\n")
	nested := mkdirAll(t, root, "internal", "deep", "deeper")

	got, err := Find(nested)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Dir() != root {
		t.Fatalf("Dir() = %q, want %q", got.Dir(), root)
	}
	if got.Origin() != OriginContract {
		t.Fatalf("Origin() = %q, want %q", got.Origin(), OriginContract)
	}
}

func TestFindPrefersContractOverDraftOverGit(t *testing.T) {
	tests := []struct {
		name       string
		seed       []string
		wantOrigin string
	}{
		{"contract", []string{".project/contract.yaml"}, OriginContract},
		{"draft", []string{".project/contract.yaml.draft"}, OriginDraft},
		{"git", []string{".git/HEAD"}, OriginGit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, rel := range tc.seed {
				writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), "x\n")
			}
			nested := mkdirAll(t, root, "a", "b")
			got, err := Find(nested)
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			if got.Dir() != root {
				t.Fatalf("Dir() = %q, want %q", got.Dir(), root)
			}
			if got.Origin() != tc.wantOrigin {
				t.Fatalf("Origin() = %q, want %q", got.Origin(), tc.wantOrigin)
			}
		})
	}
}

func TestFindContractBeatsGitAtSameLevel(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref\n")
	writeFile(t, filepath.Join(root, ".project", "contract.yaml"), "schema: 1\n")

	got, err := Find(root)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Origin() != OriginContract {
		t.Fatalf("Origin() = %q, want %q", got.Origin(), OriginContract)
	}
}

func TestFindNearestWins(t *testing.T) {
	outer := t.TempDir()
	writeFile(t, filepath.Join(outer, ".project", "contract.yaml"), "schema: 1\n")
	inner := mkdirAll(t, outer, "sub")
	writeFile(t, filepath.Join(inner, ".project", "contract.yaml"), "schema: 1\n")

	got, err := Find(mkdirAll(t, inner, "x"))
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Dir() != inner {
		t.Fatalf("Dir() = %q, want nearest %q", got.Dir(), inner)
	}
}

func TestFindFallsBackToStartDir(t *testing.T) {
	root := t.TempDir()
	nested := mkdirAll(t, root, "no", "markers")

	got, err := Find(nested)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Dir() != nested {
		t.Fatalf("Dir() = %q, want %q", got.Dir(), nested)
	}
	if got.Origin() != OriginCWD {
		t.Fatalf("Origin() = %q, want %q", got.Origin(), OriginCWD)
	}
}

func TestAtRejectsNonDirectory(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "file.txt")
	writeFile(t, f, "x\n")

	if _, err := At(f); err == nil {
		t.Fatal("At(file) = nil error, want error")
	}
	if _, err := At(filepath.Join(root, "missing")); err == nil {
		t.Fatal("At(missing) = nil error, want error")
	}
}

func TestPathAccessors(t *testing.T) {
	root := t.TempDir()
	r, err := At(root)
	if err != nil {
		t.Fatalf("At: %v", err)
	}
	cases := map[string]string{
		r.Contract():      filepath.Join(root, ".project", "contract.yaml"),
		r.ContractDraft(): filepath.Join(root, ".project", "contract.yaml.draft"),
		r.Lock():          filepath.Join(root, ".project", "profiles.lock"),
		r.LockDraft():     filepath.Join(root, ".project", "profiles.lock.draft"),
		r.Exceptions():    filepath.Join(root, ".project", "exceptions.yaml"),
		r.StateDir():      filepath.Join(root, ".project", "state"),
		r.Envelope():      filepath.Join(root, ".project", "state", "envelope.yaml"),
		r.Board():         filepath.Join(root, ".project", "state", "board.jsonl"),
		r.EvidenceDir():   filepath.Join(root, ".project", "evidence"),
		r.Review():        filepath.Join(root, "review", "adoption-review.md"),
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}
