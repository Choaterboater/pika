package profiles

import (
	"embed"
	"fmt"
	"hash"
	"io/fs"
	"slices"
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

// coreTemplates is coreTemplatesFS rooted at that directory, so a
// template is named by its file name ("README.md.tmpl") wherever the
// embed layout happens to put it. The pack digest hashes those names, so
// rooting the subtree keeps a directory move from rotating a digest
// while the templates themselves are unchanged.
var coreTemplates = subFS(coreTemplatesFS, coreTemplatesDir)

// subFS roots fsys at dir. fs.Sub on an embedded filesystem with a
// constant path cannot fail at runtime: a failure means the go:embed
// directive and the directory constant disagree, which is a build-time
// mistake every test in this package trips over immediately.
func subFS(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic("profiles: root pack templates at " + dir + ": " + err.Error())
	}
	return sub
}

// Template returns the raw text of one core-pack scaffold template by
// its file name, e.g. "README.md.tmpl". A template missing from the
// pack is a hard error carrying the template name: init must fail, not
// scaffold a hole.
func Template(name string) (string, error) {
	b, err := fs.ReadFile(coreTemplates, name)
	if err != nil {
		return "", fmt.Errorf("profiles: core pack is missing template %s: %w", name, err)
	}
	return string(b), nil
}

// hashTemplates folds one pack's scaffold templates into h: every file
// path in the templates subtree, in sorted order, each followed by a NUL
// separator and the file's bytes — the same shape PackDigest uses for
// pack references. A pack that ships no templates contributes nothing.
//
// The sort is explicit, and deliberately redundant today: fs.WalkDir
// already walks lexically. It is here because a digest whose value
// depends on an enumeration order nothing states is a digest that can
// rotate without its input changing — a map of templates, or an FS whose
// ReadDir does not sort, is all it would take. That failure mode is
// worse than having no digest at all: every adopted repository fails
// gate 1 at once, and no diff anywhere explains why.
func hashTemplates(h hash.Hash, fsys fs.FS) error {
	if fsys == nil {
		return nil
	}
	var names []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("profiles: walk pack templates: %w", err)
	}
	slices.Sort(names)
	for _, name := range names {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("profiles: read pack template %s: %w", name, err)
		}
		// hash.Hash.Write is documented never to return an error.
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(b)
	}
	return nil
}
