// Package repopath resolves the repository root and owns every path
// beneath .project. Before this package the root was the process working
// directory in every command and the .project path strings were
// duplicated across six packages.
package repopath

import (
	"fmt"
	"os"
	"path/filepath"
)

// Origin records which marker resolved the root, so `pika doctor` can
// explain the answer rather than assert it.
const (
	OriginContract = "contract"
	OriginDraft    = "draft"
	OriginGit      = "git"
	OriginCWD      = "cwd"
	OriginExplicit = "explicit"
)

// Root is a resolved repository root. It is immutable and safe to share.
type Root struct {
	dir    string
	origin string
}

// markers are probed in priority order at each level of the walk: an
// adopted repository beats a mid-adoption one, which beats a bare git
// checkout.
var markers = []struct {
	rel    string
	origin string
}{
	{filepath.Join(".project", "contract.yaml"), OriginContract},
	{filepath.Join(".project", "contract.yaml.draft"), OriginDraft},
	{".git", OriginGit},
}

// Find walks up from start and returns the nearest directory carrying a
// marker. When no ancestor carries one it returns start itself with
// OriginCWD: an unadopted directory is a valid, reportable state, not an
// error.
func Find(start string) (*Root, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return nil, fmt.Errorf("repopath: %w", err)
	}
	dir := abs
	for {
		for _, m := range markers {
			if _, err := os.Stat(filepath.Join(dir, m.rel)); err == nil {
				return &Root{dir: dir, origin: m.origin}, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return &Root{dir: abs, origin: OriginCWD}, nil
}

// At binds an explicit root, bypassing discovery. It is what --root uses.
func At(dir string) (*Root, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("repopath: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("repopath: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repopath: %s is not a directory", abs)
	}
	return &Root{dir: abs, origin: OriginExplicit}, nil
}

// Dir is the absolute repository root.
func (r *Root) Dir() string { return r.dir }

// Origin names the marker that resolved Dir.
func (r *Root) Origin() string { return r.origin }

// Join builds an absolute path beneath the root from slash-free parts.
func (r *Root) Join(parts ...string) string {
	return filepath.Join(append([]string{r.dir}, parts...)...)
}

// The durable spine (design spec §6). These replace the string literals
// previously duplicated in check, initcmd, apply, adopt, gate1, and
// envelope.
func (r *Root) Contract() string      { return r.Join(".project", "contract.yaml") }
func (r *Root) ContractDraft() string { return r.Contract() + ".draft" }
func (r *Root) Lock() string          { return r.Join(".project", "profiles.lock") }
func (r *Root) LockDraft() string     { return r.Lock() + ".draft" }
func (r *Root) Exceptions() string    { return r.Join(".project", "exceptions.yaml") }
func (r *Root) StateDir() string      { return r.Join(".project", "state") }
func (r *Root) Envelope() string      { return r.Join(".project", "state", "envelope.yaml") }
func (r *Root) Baseline() string      { return r.Join(".project", "state", "baseline.json") }
func (r *Root) Board() string         { return r.Join(".project", "state", "board.jsonl") }
func (r *Root) EvidenceDir() string   { return r.Join(".project", "evidence") }
func (r *Root) Review() string        { return r.Join("review", "adoption-review.md") }

// SkillsDir is the canonical, harness-neutral location agent skills live
// in: one directory per skill, each holding a SKILL.md. It is dot-
// prefixed for the same reason .project is — the naming walk skips
// dot segments, so guidance files named by an external convention
// (SKILL.md) do not each need a recorded naming exception.
func (r *Root) SkillsDir() string { return r.Join(".agents", "skills") }

// Skill is the canonical source path of one named skill.
func (r *Root) Skill(name string) string { return r.Join(".agents", "skills", name, "SKILL.md") }
