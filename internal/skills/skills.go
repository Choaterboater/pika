// Package skills installs and verifies the agent instructions a pika
// repository ships: the canonical skills under .agents/skills, and the
// harness-native projections generated from them.
//
// The two halves have different owners, and that split is the whole
// design. A canonical skill is OPERATOR-OWNED — the kernel writes one
// only when it is missing, and overwrites one only under --force, for
// the same reason `pika init` stopped overwriting README and AGENTS.md.
// A projection is KERNEL-OWNED — a generated copy of guidance that
// already exists somewhere else, regenerated freely and never
// hand-edited.
//
// A projection is therefore not a whole file. Codex reads AGENTS.md at
// the repository root, and AGENTS.md is a file the operator writes;
// taking it over would trade one ownership collision for another. What
// the kernel owns is a marked region inside the file. Everything outside
// the markers is the operator's and is never touched.
//
// Each region names the sources it was generated from and their digests
// (spec §9.2), so a stale copy is distinguishable from a current one:
// gate 1 recomputes them and fails on disagreement rather than letting
// parallel handwritten copies accumulate.
package skills

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
)

//go:embed templates
var templatesFS embed.FS

// templatesDir is the embedded directory the shipped skills live in.
const templatesDir = "templates"

// skillFile is the file name a skill's text lives in inside its
// directory. It is an external convention (the name every harness that
// reads .agents/skills looks for), not pika's choice, which is why the
// canonical location is dot-prefixed and therefore outside the naming
// walk.
const skillFile = "SKILL.md"

// The state of one canonical skill on disk.
const (
	// StateInstalled means the file is present and byte-identical to
	// the skill this kernel ships.
	StateInstalled = "installed"
	// StateMissing means no file is present; `pika skills install`
	// writes it.
	StateMissing = "missing"
	// StateEdited means the operator has changed it. That is allowed —
	// they own it — and it is reported rather than corrected, because
	// projections are generated from what is on disk, not from what the
	// kernel shipped.
	StateEdited = "edited"
)

// The state of one declared projection.
const (
	// StateCurrent means the file's managed region is byte-identical to
	// what its sources render to right now.
	StateCurrent = "current"
	// StateDrifted means it is not: a source moved, or the region was
	// hand-edited.
	StateDrifted = "drifted"
	// StateAbsent means the file carries no managed region at all.
	StateAbsent = "absent"
	// StateUnreadable means the file exists but could not be read or
	// its markers do not pair up. It is never silently regenerated:
	// a file the kernel cannot parse is one it must not overwrite.
	StateUnreadable = "unreadable"
)

// Skill is one skill this kernel ships: the directory name it installs
// under, and the SKILL.md text.
type Skill struct {
	Name string
	Body []byte
}

// Status is the answer `pika skills` gives in all three of its modes,
// and the shape gate 1 reads its verdict from.
type Status struct {
	// OK is false when any declared projection is not current. A
	// canonical skill the operator has edited is not a failure: they
	// own it.
	OK          bool               `json:"ok"`
	Root        string             `json:"root"`
	Skills      []SkillStatus      `json:"skills"`
	Projections []ProjectionStatus `json:"projections"`
}

// SkillStatus is one canonical skill: where it lives, whether it is
// there, and whether it still matches what the kernel ships.
type SkillStatus struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	State   string `json:"state"`
	Written bool   `json:"written,omitempty"`
	// Detail is set only when the state needs explaining — an edited
	// skill that `pika skills install` refused to overwrite, say.
	Detail string `json:"detail,omitempty"`
}

// ProjectionStatus is one declared projection: which harness reads it,
// the file it reads, and whether that file's managed region still
// matches its sources.
type ProjectionStatus struct {
	Harness string   `json:"harness"`
	Path    string   `json:"path"`
	State   string   `json:"state"`
	Sources []Source `json:"sources"`
	Written bool     `json:"written,omitempty"`
	Detail  string   `json:"detail,omitempty"`
}

// Shipped returns the canonical skills this binary carries, in name
// order. They are embedded rather than read from the repository so that
// `pika skills install` in a repository that has none still has
// something to install.
func Shipped() []Skill {
	entries, err := fs.ReadDir(templatesFS, templatesDir)
	if err != nil {
		// The templates are an embed.FS baked into the binary; a read
		// failure means the go:embed directive and templatesDir
		// disagree, which every test in this package trips over.
		panic("skills: read embedded templates: " + err.Error())
	}
	out := make([]Skill, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, err := fs.ReadFile(templatesFS, path.Join(templatesDir, e.Name()))
		if err != nil {
			panic("skills: read embedded skill " + e.Name() + ": " + err.Error())
		}
		out = append(out, Skill{Name: strings.TrimSuffix(e.Name(), ".md"), Body: body})
	}
	slices.SortFunc(out, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// canonical is one skill as it exists in the repository: the name, the
// repository-relative path a projection cites as its source, and the
// bytes on disk. Projections are rendered from these rather than from
// the embedded templates, so an operator's edit to a skill flows into
// every projection instead of being silently reverted.
type canonical struct {
	name   string
	rel    string
	body   []byte
	digest string
}

// Inspect answers, without writing anything: which canonical skills are
// installed, which declared projections exist, and whether each one is
// current.
func Inspect(root *repopath.Root, c *contract.Contract, resolved *profiles.Resolved) (*Status, error) {
	installed, err := loadCanonical(root)
	if err != nil {
		return nil, err
	}
	st := &Status{OK: true, Root: root.Dir()}
	st.Skills = skillStatuses(root, installed, nil)
	body := newBody(installed, guidanceOf(resolved))
	for _, p := range projections(c) {
		st.Projections = append(st.Projections, inspectProjection(root, p, body))
	}
	for _, p := range st.Projections {
		if p.State != StateCurrent {
			st.OK = false
		}
	}
	return st, nil
}

// Install writes every canonical skill the repository is missing, then
// regenerates every declared projection from the skills on disk.
//
// force is only ever about the canonical half: it authorizes overwriting
// a skill the operator has edited. Projections need no such flag —
// nothing the kernel regenerates there was ever the operator's.
func Install(root *repopath.Root, c *contract.Contract, resolved *profiles.Resolved, force bool) (*Status, error) {
	written := map[string]bool{}
	for _, s := range Shipped() {
		target := root.Skill(s.Name)
		current, err := os.ReadFile(target)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return nil, fmt.Errorf("skills: read %s: %w", relTo(root, target), err)
		case string(current) == string(s.Body):
			continue
		case !force:
			// Reported by skillStatuses as edited-and-kept; refusing
			// here rather than failing keeps `pika skills install`
			// useful in a repository whose skills were customized on
			// purpose.
			continue
		}
		if err := writeFile(target, s.Body); err != nil {
			return nil, err
		}
		written[s.Name] = true
	}

	installed, err := loadCanonical(root)
	if err != nil {
		return nil, err
	}
	st := &Status{OK: true, Root: root.Dir()}
	st.Skills = skillStatuses(root, installed, written)
	body := newBody(installed, guidanceOf(resolved))
	for _, p := range projections(c) {
		got, err := writeProjection(root, p, body)
		if err != nil {
			return nil, err
		}
		if got.State != StateCurrent {
			st.OK = false
		}
		st.Projections = append(st.Projections, got)
	}
	return st, nil
}

// Verify reports every declared projection that is not current, as one
// error naming each projection, the source that explains it, and the
// command that fixes it. It is what gate 1 calls, so `pika check` and
// `pika skills check` cannot disagree about what drift is.
func Verify(repoRoot string, c *contract.Contract, resolved *profiles.Resolved) error {
	if len(projections(c)) == 0 {
		return nil
	}
	root, err := repopath.At(repoRoot)
	if err != nil {
		return fmt.Errorf("skills: %w", err)
	}
	st, err := Inspect(root, c, resolved)
	if err != nil {
		return err
	}
	var problems []string
	for _, p := range st.Projections {
		if p.State == StateCurrent {
			continue
		}
		problems = append(problems, fmt.Sprintf("%s (harness %s) %s", p.Path, p.Harness, p.Detail))
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("skills projection: " + strings.Join(problems, "; "))
}

// projections returns the contract's declared projections, or nothing
// when the contract declares none. A repository that declares no
// projection has no drift to have: the canonical location is the only
// copy, which is the state spec §9.2 prefers.
func projections(c *contract.Contract) []contract.Projection {
	if c == nil || c.Skills == nil {
		return nil
	}
	return c.Skills.Projections
}

// guidanceOf returns the resolved profile guidance, tolerating a nil
// resolution so a caller that could not resolve profiles still gets the
// skills half of the answer rather than nothing.
func guidanceOf(resolved *profiles.Resolved) []profiles.GuidanceSet {
	if resolved == nil {
		return nil
	}
	return resolved.AgentGuidance
}

// skillStatuses describes each shipped skill against what is on disk,
// then reports any additional skill the operator has added — those are
// projected too, so leaving them out of the report would hide half of
// what a projection is made from.
func skillStatuses(root *repopath.Root, installed []canonical, written map[string]bool) []SkillStatus {
	onDisk := map[string]canonical{}
	for _, s := range installed {
		onDisk[s.name] = s
	}
	var out []SkillStatus
	shipped := map[string]bool{}
	for _, s := range Shipped() {
		shipped[s.Name] = true
		got, ok := onDisk[s.Name]
		st := SkillStatus{Name: s.Name, Path: relTo(root, root.Skill(s.Name)), Written: written[s.Name]}
		switch {
		case !ok:
			st.State = StateMissing
			st.Detail = "run `pika skills install` to write it"
		case string(got.body) == string(s.Body):
			st.State = StateInstalled
		default:
			st.State = StateEdited
			st.Detail = "differs from the skill this pika ships; projections are generated from this file, and `pika skills install --force` would replace it"
		}
		out = append(out, st)
	}
	for _, s := range installed {
		if shipped[s.name] {
			continue
		}
		out = append(out, SkillStatus{
			Name:   s.name,
			Path:   s.rel,
			State:  StateEdited,
			Detail: "not shipped by this pika; it is the operator's own skill and is projected alongside the rest",
		})
	}
	slices.SortFunc(out, func(a, b SkillStatus) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// loadCanonical reads every skill installed under .agents/skills, in
// name order. A repository with no skills directory yet has no skills,
// which is a reportable state and not an error.
func loadCanonical(root *repopath.Root) ([]canonical, error) {
	dir := root.SkillsDir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("skills: read %s: %w", relTo(root, dir), err)
	}
	var out []canonical
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		target := filepath.Join(dir, e.Name(), skillFile)
		body, err := os.ReadFile(target)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("skills: read %s: %w", relTo(root, target), err)
		}
		out = append(out, canonical{
			name:   e.Name(),
			rel:    relTo(root, target),
			body:   body,
			digest: digestOf(body),
		})
	}
	slices.SortFunc(out, func(a, b canonical) int { return strings.Compare(a.name, b.name) })
	return out, nil
}

// digestOf is the one digest form projections cite: sha256 over the
// exact bytes, prefixed so the algorithm is on the page rather than
// inferred from the length.
func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// relTo renders an absolute path under root as the repository-relative
// slash path every report and every projection header uses. A path
// outside the root is reported as given: a wrong path is more useful
// visible than smoothed over.
func relTo(root *repopath.Root, abs string) string {
	rel, err := filepath.Rel(root.Dir(), abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// writeFile writes one file, creating parent directories.
func writeFile(target string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("skills: create %s: %w", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return fmt.Errorf("skills: write %s: %w", target, err)
	}
	return nil
}
