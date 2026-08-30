package profiles

import (
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// coreTemplateNames lists every template the core pack must serve: the
// docs spine files and repository files init renders from the pack
// (spec §6.2, §7.1).
//
// It is in the sorted order the pack digest hashes them in, and
// TestTemplateSetIsExactlyTheDeclaredList holds it to the shipped
// filesystem. Writing the set out rather than walking for it is what
// lets wantCoreDigest state the digest's whole input: a template added
// without touching this list fails here instead of being absorbed into a
// new digest nobody can account for.
var coreTemplateNames = []string{
	"AGENTS.md.tmpl",
	"CONTRIBUTING.md.tmpl",
	"README.md.tmpl",
	"ci.yml.tmpl",
	"pull_request_template.md.tmpl",
}

// The declared list and the shipped filesystem must be the same set, in
// the same order. Everything the digest tests assert is stated in terms
// of the list, so a drift between the two would quietly narrow them.
func TestTemplateSetIsExactlyTheDeclaredList(t *testing.T) {
	var got []string
	err := fs.WalkDir(coreTemplates, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			got = append(got, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk core templates: %v", err)
	}
	slices.Sort(got)
	if !reflect.DeepEqual(got, coreTemplateNames) {
		t.Errorf("core pack ships %v, but coreTemplateNames declares %v", got, coreTemplateNames)
	}
}

func TestTemplateServesCorePackTemplates(t *testing.T) {
	for _, name := range coreTemplateNames {
		t.Run(name, func(t *testing.T) {
			src, err := Template(name)
			if err != nil {
				t.Fatalf("Template(%q): %v", name, err)
			}
			if src == "" {
				t.Fatalf("Template(%q) returned empty source", name)
			}
		})
	}
}

func TestTemplateMissingIsHardError(t *testing.T) {
	const name = "nonexistent.md.tmpl"
	_, err := Template(name)
	if err == nil {
		t.Fatalf("Template(%q) succeeded, want error", name)
	}
	if !strings.Contains(err.Error(), name) {
		t.Errorf("error %q does not name the missing template %q", err.Error(), name)
	}
}
