package profiles

import (
	"embed"
	"fmt"
	"path"
)

// coreTemplatesFS embeds the core pack's scaffold templates. They live
// in the pack — under packs/core@1/templates — because the docs spine
// and repository files they render belong to the core profile, not to
// any consumer.
//
//go:embed packs/core@1/templates
var coreTemplatesFS embed.FS

// coreTemplatesDir is the embedded directory the core pack's templates
// ship in.
const coreTemplatesDir = "packs/core@1/templates"

// Template returns the raw text of one core-pack scaffold template by
// its file name, e.g. "README.md.tmpl". A template missing from the
// pack is a hard error carrying the template name: init must fail, not
// scaffold a hole.
func Template(name string) (string, error) {
	b, err := coreTemplatesFS.ReadFile(path.Join(coreTemplatesDir, name))
	if err != nil {
		return "", fmt.Errorf("profiles: core pack is missing template %s: %w", name, err)
	}
	return string(b), nil
}
