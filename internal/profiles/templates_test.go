package profiles

import (
	"strings"
	"testing"
)

// coreTemplateNames lists every template the core pack must serve: the
// docs spine files and repository files init renders from the pack
// (spec §6.2, §7.1).
var coreTemplateNames = []string{
	"README.md.tmpl",
	"AGENTS.md.tmpl",
	"CONTRIBUTING.md.tmpl",
	"ci.yml.tmpl",
	"pull_request_template.md.tmpl",
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
