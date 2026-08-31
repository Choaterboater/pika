package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// This file holds the second class of target the kernel keeps a region
// in: the agent instruction files that live in the operator's home
// directory rather than in any repository.
//
// They exist because the repository projections cannot reach the case
// that matters most for a new repository. A projection is generated from
// a contract; an agent standing in a directory that has no contract
// reads nothing at all, and the one thing it most needs to be told there
// is that `pika init` and `pika adopt` exist. So the global files carry
// a routing preface the projections do not, and then the same shipped
// skills the projections carry, from the same templates, so that
// renaming a command is one edit rather than three.
//
// Two rules separate this from the repository half, and both are load
// bearing:
//
//   - Nothing a repository contains can cause one of these files to be
//     written. Installing them is an explicit --global on a command
//     line. A contract that could reach the home directory would mean
//     that cloning a repository handed it a capability over the
//     operator's machine, which is not a trade a verification kernel
//     gets to make on somebody's behalf.
//   - No gate checks them. They are absent from a fresh checkout by
//     definition, so a gate that digested them would fail on every clone
//     of every repository. Their state is reported — by `pika skills
//     --global` and by `pika doctor` — and never enforced.

// globalDir is the embedded directory holding the templates that exist
// only for the global agent files.
const globalDir = templatesDir + "/global"

// globalSkillName is the directory name the user-level skill is
// installed under, and the name of the template that supplies both its
// frontmatter and the routing section it opens with.
//
// They are the same name deliberately. omp discovers a user-level skill
// at <root>/skills/<name>/SKILL.md and takes the directory as the skill
// name, so a template whose frontmatter announced a different one would
// install a skill that misreports itself.
const globalSkillName = "pika"

// GlobalTarget is one agent instruction file outside every repository.
type GlobalTarget struct {
	// Harness is the harness that reads the file, named from the same
	// enumeration a contract's projections draw from.
	Harness string
	// Rel is the file's slash path beneath the home directory. It is
	// relative rather than absolute so that a report says the same thing
	// on every machine.
	Rel string
	// Frontmatter records that this harness will not surface the file at
	// all unless it opens with the skill's `name` and `description`.
	//
	// That block cannot live inside the kernel-owned region: a region
	// begins with its marker, and frontmatter that is not the first
	// thing in the file is not frontmatter. So it is written above the
	// markers, from the same template the region renders, and only when
	// the file is being created — above the markers is the operator's
	// half, and the kernel does not take back a description somebody
	// reworded.
	Frontmatter bool
}

// GlobalTargets is the fixed set of files `--global` installs.
//
// It is a fixed list and not a contract setting on purpose. A
// configurable global target would be a path in a file that a repository
// could ship, which is exactly the write into somebody's home directory
// this design refuses to make possible.
func GlobalTargets() []GlobalTarget {
	return []GlobalTarget{
		{
			Harness:     "omp",
			Rel:         path.Join(".agents", "skills", globalSkillName, skillFile),
			Frontmatter: true,
		},
		{
			Harness: "codex",
			Rel:     path.Join(".codex", "AGENTS.md"),
		},
	}
}

// abs is the file this target names beneath home.
func (t GlobalTarget) abs(home string) string {
	return filepath.Join(home, filepath.FromSlash(t.Rel))
}

// preamble is the text the file must open with when it is being created.
func (t GlobalTarget) preamble() []byte {
	if !t.Frontmatter {
		return nil
	}
	for _, g := range GlobalTemplates() {
		if g.Name == globalSkillName {
			return frontmatterOf(g.Body)
		}
	}
	return nil
}

// GlobalStatus is one global agent file: which harness reads it, where
// it lives beneath the home directory, and whether its kernel-owned
// region still matches what this pika renders and what it records about
// its own bytes.
type GlobalStatus struct {
	Harness string   `json:"harness"`
	Path    string   `json:"path"`
	State   string   `json:"state"`
	Sources []Source `json:"sources"`
	Region  string   `json:"region"`
	Written bool     `json:"written,omitempty"`
	Detail  string   `json:"detail,omitempty"`
}

// GlobalReport is the answer `pika skills --global` gives in all three
// of its modes.
//
// OK is true only when every target is current. Absent counts against it
// here and nowhere else: an operator who asked `pika skills check
// --global` asked whether the global files are in place, and answering
// "fine" about files that do not exist would be answering a different
// question. `pika doctor` treats the same absence as informational,
// because there it is one row among many about a repository that has no
// business requiring them.
type GlobalReport struct {
	OK      bool           `json:"ok"`
	Home    string         `json:"home"`
	Targets []GlobalStatus `json:"targets"`
}

// ResolveHome answers where the global agent files live.
//
// override wins when it is set. It is what --home passes, and it is what
// keeps a test — and a sandbox — off the operator's real home directory;
// nothing else in this package can be pointed anywhere.
//
// Otherwise os.UserHomeDir decides. That is deliberately not $HOME: the
// same binary runs on Windows, where the answer is %USERPROFILE%, and a
// kernel that hard-coded one variable would write the file where nothing
// reads it on the other platform.
//
// A home that cannot be resolved is an error and never a fallback.
// Continuing with a relative path would put an instruction file nothing
// will ever load in whatever directory the operator happened to be
// standing in, and report success for it.
func ResolveHome(override string) (string, error) {
	if override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("skills: resolve %s: %w", override, err)
		}
		return abs, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("skills: the global agent files live under your home directory, and this machine reports none: %w", err)
	}
	if home == "" {
		return "", errors.New("skills: the global agent files live under your home directory, and this machine reports it as an empty path")
	}
	return home, nil
}

// GlobalTemplates returns the templates that exist only for the global
// agent files, in name order. They are what the routing preface is
// rendered from, and they are separate files rather than a run-time
// rewrite of the repository text because the two say different things:
// the repository skills speak to an agent inside a governed repository,
// and this one has to establish whether there is a contract at all
// before any of that applies.
func GlobalTemplates() []Skill {
	entries, err := fs.ReadDir(templatesFS, globalDir)
	if err != nil {
		// Same reasoning as Shipped: the templates are baked into the
		// binary, so a read failure means the go:embed directive and
		// globalDir disagree, which every test here trips over.
		panic("skills: read embedded global templates: " + err.Error())
	}
	out := make([]Skill, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		ref := path.Join(globalDir, e.Name())
		body, err := fs.ReadFile(templatesFS, ref)
		if err != nil {
			panic("skills: read embedded global template " + e.Name() + ": " + err.Error())
		}
		out = append(out, Skill{Name: strings.TrimSuffix(e.Name(), ".md"), Ref: ref, Body: body})
	}
	slices.SortFunc(out, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// globalBody is the region every global agent file carries. Both targets
// carry the same bytes, for the same reason every declared projection in
// a repository does: the text does not depend on who reads it.
//
// It is rendered from the templates embedded in this binary and from
// nothing else. A global file is installed where no repository exists,
// so there is no working tree to read canonical skills from and no
// resolved profile to contribute stack guidance. Citing either would be
// provenance for a source the file's own reader cannot check, and would
// make the file stale the moment the operator changed repositories.
//
// The routing preface comes first and the shipped skills follow it
// unchanged. That order is the whole difference between the two target
// classes.
func globalBody() body {
	parts := GlobalTemplates()
	parts = append(parts, Shipped()...)
	texts := make([]canonical, 0, len(parts))
	for _, p := range parts {
		texts = append(texts, canonical{
			kind:   SourceTemplate,
			name:   p.Name,
			rel:    p.Ref,
			body:   p.Body,
			digest: digestOf(p.Body),
		})
	}
	return newBody(texts, nil, globalOrigin)
}

// InspectGlobal reports the state of every global agent file beneath
// home without writing anything. It returns no error because there is
// no input for it to fail on: a file it cannot read is a state it
// reports, not a reason to stop.
func InspectGlobal(home string) *GlobalReport {
	b := globalBody()
	rep := &GlobalReport{OK: true, Home: home}
	for _, t := range GlobalTargets() {
		st := GlobalStatus{Harness: t.Harness, Path: t.Rel, Sources: b.sources(), Region: b.digest}
		st.State, st.Detail = inspectRegionFile(t.abs(home), b)
		if st.State != StateCurrent {
			rep.OK = false
		}
		rep.Targets = append(rep.Targets, st)
	}
	return rep
}

// InstallGlobal writes the kernel-owned region into every global agent
// file beneath home, creating each file when it is missing.
//
// Everything outside the markers is left exactly as it was. A codex
// AGENTS.md in a home directory is where an operator keeps notes about
// every tool they use, not only this one, and an install that rewrote
// the file rather than its own region would delete them.
func InstallGlobal(home string) (*GlobalReport, error) {
	b := globalBody()
	rep := &GlobalReport{OK: true, Home: home}
	for _, t := range GlobalTargets() {
		st := GlobalStatus{Harness: t.Harness, Path: t.Rel, Sources: b.sources(), Region: b.digest}
		var err error
		st.State, st.Detail, st.Written, err = writeRegionFile(t.abs(home), t.preamble(), b)
		if err != nil {
			return nil, err
		}
		if st.State != StateCurrent {
			rep.OK = false
		}
		rep.Targets = append(rep.Targets, st)
	}
	return rep, nil
}
