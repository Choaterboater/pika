// Package profiles resolves profile packs into a Resolved composition.
//
// M1 ships exactly one pack, core@1, embedded at build time. Language,
// kind, and capability packs are composed in a later task; Resolve already
// builds a Layers slice so those layers merge in spec order without
// restructuring.
package profiles

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

//go:embed packs/core@1.yaml
var corePackYAML []byte

const (
	// CoreRef is the only pack reference M1 resolves.
	CoreRef = "core@1"

	// lockSource marks packs shipped inside the binary.
	lockSource = "embedded"
)

// packEntry is one registered embedded pack.
type packEntry struct {
	name    string
	version string
	data    []byte
}

// embeddedPacks is the M1 registry. Later tasks register language, kind,
// and capability packs here.
var embeddedPacks = map[string]packEntry{
	CoreRef: {name: "core", version: "1", data: corePackYAML},
}

// Pack mirrors the spec §5.4 pack structure.
type Pack struct {
	Profile       string        `yaml:"profile"`
	Version       string        `yaml:"version"`
	Provenance    Provenance    `yaml:"provenance"`
	Detection     Detection     `yaml:"detection"`
	Layout        Layout        `yaml:"layout"`
	Files         Files         `yaml:"files"`
	Templates     []Template    `yaml:"templates"`
	Naming        Naming        `yaml:"naming"`
	Verification  Verification  `yaml:"verification"`
	DocTriggers   []DocTrigger  `yaml:"doc-triggers"`
	AgentGuidance []string      `yaml:"agent-guidance"`
	Migration     Migration     `yaml:"migration"`
	Compatibility Compatibility `yaml:"compatibility"`
	Conventions   Conventions   `yaml:"conventions"`
}

// Provenance records where the pack came from.
type Provenance struct {
	Source      string `yaml:"source"`
	GeneratedBy string `yaml:"generated-by"`
}

// Detection states when a profile applies. Core always applies.
type Detection struct {
	When string `yaml:"when"`
}

// Layout declares the fixed project layout a profile requires.
type Layout struct {
	ContractPath string   `yaml:"contract-path"`
	StateDir     string   `yaml:"state-dir"`
	DocsSpine    []string `yaml:"docs-spine"`
}

// Files declares files the profile requires to exist.
type Files struct {
	Required []string `yaml:"required"`
}

// Template names a scaffold template shipped by the pack.
type Template struct {
	ID   string `yaml:"id"`
	Path string `yaml:"path"`
}

// Naming groups the naming rules a profile enforces.
type Naming struct {
	Rules []namingSpec `yaml:"rules"`
}

// namingSpec is the pack-side form of a naming rule.
type namingSpec struct {
	RuleID   string   `yaml:"rule-id"`
	Severity string   `yaml:"severity"`
	Scope    string   `yaml:"scope"`
	Pattern  string   `yaml:"pattern"`
	Banned   []string `yaml:"banned"`
}

// Verification groups the checks a profile declares.
type Verification struct {
	Checks []checkSpec `yaml:"checks"`
}

// checkSpec is the pack-side form of a check slot.
type checkSpec struct {
	ID        string   `yaml:"id"`
	Cmd       []string `yaml:"cmd"`
	Discovery bool     `yaml:"discovery"`
}

// DocTrigger names documentation that must stay in sync with changes.
type DocTrigger struct {
	ID    string   `yaml:"id"`
	When  string   `yaml:"when"`
	Paths []string `yaml:"paths"`
}

// Migration steps a profile applies to existing projects.
type Migration struct {
	Steps []string `yaml:"steps"`
}

// Compatibility constraints of a pack.
type Compatibility struct {
	Requires []string `yaml:"requires"`
}

// Conventions carries the PR and branch conventions from spec §15.
type Conventions struct {
	Branches     BranchConventions      `yaml:"branches"`
	PullRequests PullRequestConventions `yaml:"pull-requests"`
}

// BranchConventions declares branch naming.
type BranchConventions struct {
	Types  []string `yaml:"types"`
	Format string   `yaml:"format"`
}

// PullRequestConventions declares PR lifecycle.
type PullRequestConventions struct {
	DraftUntilChecksPass bool   `yaml:"draft-until-checks-pass"`
	MergeStrategy        string `yaml:"merge-strategy"`
}

// Layer is one composed profile layer, in spec §5.4 order
// (core → language → kind → capabilities → overrides).
type Layer struct {
	Name    string
	Version string
	Source  string
	Pack    Pack
}

// Check is one verification slot: either an explicit command (command name
// plus args, never a shell string) or a discovery sentinel meaning the
// stack layer supplies the real command.
type Check struct {
	ID        string
	Cmd       []string
	Discovery bool
}

// CheckSet holds the five verification slots.
type CheckSet struct {
	Format    Check
	Lint      Check
	Typecheck Check
	Test      Check
	Smoke     Check
}

// NamingRule is a resolved naming rule: an ID and severity plus matcher
// data (a regex pattern and/or banned path segments).
type NamingRule struct {
	RuleID   string
	Severity string
	Scope    string
	Pattern  string
	Banned   []string
}

// Resolved is the composition result consumed by downstream tasks.
type Resolved struct {
	Layers      []Layer
	Checks      CheckSet
	NamingRules []NamingRule
	DocTriggers []DocTrigger
}

// Resolve composes the selected profile packs, in spec §5.4 order, into a
// Resolved. M1 resolves exactly [core@1]; any other selection is an error
// listing the supported packs.
func Resolve(selected []string) (*Resolved, error) {
	if len(selected) != 1 || selected[0] != CoreRef {
		return nil, fmt.Errorf("profiles: unsupported selection %v; M1 resolves exactly [%s]", selected, CoreRef)
	}
	entry, ok := embeddedPacks[CoreRef]
	if !ok {
		return nil, fmt.Errorf("profiles: pack %s not registered", CoreRef)
	}

	var pack Pack
	if err := yaml.UnmarshalWithOptions(entry.data, &pack, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("profiles: parse %s: %w", CoreRef, err)
	}

	checks, err := pack.checkSet()
	if err != nil {
		return nil, fmt.Errorf("profiles: pack %s: %w", CoreRef, err)
	}

	return &Resolved{
		Layers: []Layer{{
			Name:    entry.name,
			Version: entry.version,
			Source:  lockSource,
			Pack:    pack,
		}},
		Checks:      checks,
		NamingRules: namingRules(pack.Naming.Rules),
		DocTriggers: pack.DocTriggers,
	}, nil
}

// checkSet maps the pack's declared checks onto the fixed slots. Every
// slot must be declared exactly once, as either a command or a discovery
// sentinel.
func (p *Pack) checkSet() (CheckSet, error) {
	var cs CheckSet
	slots := map[string]*Check{
		"format":    &cs.Format,
		"lint":      &cs.Lint,
		"typecheck": &cs.Typecheck,
		"test":      &cs.Test,
		"smoke":     &cs.Smoke,
	}
	seen := map[string]bool{}
	for _, spec := range p.Verification.Checks {
		slot, ok := slots[spec.ID]
		if !ok {
			return cs, fmt.Errorf("unknown check id %q (want one of format, lint, typecheck, test, smoke)", spec.ID)
		}
		if seen[spec.ID] {
			return cs, fmt.Errorf("check id %q declared more than once", spec.ID)
		}
		seen[spec.ID] = true
		slot.ID = spec.ID
		if spec.Discovery {
			slot.Discovery = true
			continue
		}
		if len(spec.Cmd) == 0 {
			return cs, fmt.Errorf("check %q: need cmd or discovery", spec.ID)
		}
		slot.Cmd = spec.Cmd
	}
	for id := range slots {
		if !seen[id] {
			return cs, fmt.Errorf("check %q not declared", id)
		}
	}
	return cs, nil
}

// namingRules converts pack naming specs into resolved rules.
func namingRules(specs []namingSpec) []NamingRule {
	rules := make([]NamingRule, 0, len(specs))
	for _, s := range specs {
		rules = append(rules, NamingRule{
			RuleID:   s.RuleID,
			Severity: s.Severity,
			Scope:    s.Scope,
			Pattern:  s.Pattern,
			Banned:   s.Banned,
		})
	}
	return rules
}

// PackDigest returns the hex sha256 of the embedded core pack bytes.
func PackDigest() string {
	sum := sha256.Sum256(corePackYAML)
	return hex.EncodeToString(sum[:])
}

// WriteLock writes the profiles.lock JSON to path: each resolved pack name
// mapped to its version, plus the pack source and the sha256 digest of the
// embedded pack bytes. Task 12's init calls this for .project/profiles.lock.
func WriteLock(path string) error {
	r, err := Resolve([]string{CoreRef})
	if err != nil {
		return err
	}
	lock := map[string]string{
		"source": lockSource,
		"digest": PackDigest(),
	}
	for _, l := range r.Layers {
		lock[l.Name] = l.Version
	}
	out, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return fmt.Errorf("profiles: encode lock: %w", err)
	}
	out = append(out, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("profiles: create lock dir: %w", err)
	}
	return os.WriteFile(path, out, 0o644)
}
