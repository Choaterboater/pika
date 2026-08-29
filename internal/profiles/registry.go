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
	"slices"
	"strings"

	"github.com/Choaterboater/projectctl/internal/yamlx"
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

type Pack struct {
	Profile       string             `yaml:"profile"`
	Version       string             `yaml:"version"`
	Provenance    Provenance         `yaml:"provenance" yamlx:"strict"`
	Detection     Detection          `yaml:"detection" yamlx:"strict"`
	Layout        Layout             `yaml:"layout" yamlx:"strict"`
	Files         Files              `yaml:"files" yamlx:"strict"`
	Templates     []ScaffoldTemplate `yaml:"templates" yamlx:"strict"`
	Naming        Naming             `yaml:"naming" yamlx:"strict"`
	Verification  Verification       `yaml:"verification" yamlx:"strict"`
	DocTriggers   []DocTrigger       `yaml:"doc-triggers" yamlx:"strict"`
	AgentGuidance []string           `yaml:"agent-guidance"`
	Migration     Migration          `yaml:"migration" yamlx:"strict"`
	Compatibility Compatibility      `yaml:"compatibility" yamlx:"strict"`
	Conventions   Conventions        `yaml:"conventions" yamlx:"strict"`
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
	// Expectations records the stack-owned layout (spec §6.1) a
	// language pack requires; core leaves it empty.
	Expectations []string `yaml:"expectations"`
}

// Files declares files the profile requires to exist.
type Files struct {
	Required []string `yaml:"required"`
}

// ScaffoldTemplate names a scaffold template shipped by the pack.
type ScaffoldTemplate struct {
	ID   string `yaml:"id"`
	Path string `yaml:"path"`
}

// Naming groups the naming rules a profile enforces.
type Naming struct {
	Rules []namingSpec `yaml:"rules" yamlx:"strict"`
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
	Checks []checkSpec `yaml:"checks" yamlx:"strict"`
}

// checkSpec is the pack-side form of a check slot. A discovery sentinel
// may carry a hint: the suggested command for when the repository's own
// discovery finds nothing.
type checkSpec struct {
	ID        string   `yaml:"id"`
	Cmd       []string `yaml:"cmd"`
	Discovery bool     `yaml:"discovery"`
	Hint      []string `yaml:"hint"`
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
	Branches     BranchConventions      `yaml:"branches" yamlx:"strict"`
	PullRequests PullRequestConventions `yaml:"pull-requests" yamlx:"strict"`
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
// stack layer supplies the real command. A discovery sentinel may carry a
// hint: the suggested command for when repository discovery finds none.
type Check struct {
	ID        string
	Cmd       []string
	Discovery bool
	Hint      []string
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
// Resolved. M1 resolves [core@1] or [core@1, <language>@1]: core exactly
// once, first, followed by at most one language pack. Each later layer
// refines the previous ones; a language pack's check slots replace the
// core sentinels it declares.
func Resolve(selected []string) (*Resolved, error) {
	if err := validateSelection(selected); err != nil {
		return nil, err
	}

	resolved := &Resolved{Checks: CheckSet{}}
	for i, ref := range selected {
		entry, ok := embeddedPacks[ref]
		if !ok {
			return nil, fmt.Errorf("profiles: pack %s not registered (supported packs: %s)", ref, strings.Join(supportedRefs(), ", "))
		}

		var pack Pack
		if err := yamlx.UnmarshalStrict(entry.data, &pack); err != nil {
			return nil, fmt.Errorf("profiles: parse %s: %w", ref, err)
		}

		checks, err := pack.checkSet()
		if err != nil {
			return nil, fmt.Errorf("profiles: pack %s: %w", ref, err)
		}
		if i == 0 {
			resolved.Checks = checks
			resolved.NamingRules = namingRules(pack.Naming.Rules)
		} else {
			resolved.Checks = mergeChecks(resolved.Checks, checks)
			resolved.NamingRules = append(resolved.NamingRules, namingRules(pack.Naming.Rules)...)
		}
		resolved.DocTriggers = append(resolved.DocTriggers, pack.DocTriggers...)
		resolved.Layers = append(resolved.Layers, Layer{
			Name:    entry.name,
			Version: entry.version,
			Source:  lockSource,
			Pack:    pack,
		})
	}
	return resolved, nil
}

// validateSelection enforces the M1 composition rules: exactly one core
// pack in first position, then at most one language pack.
func validateSelection(selected []string) error {
	if len(selected) == 0 || selected[0] != CoreRef {
		return fmt.Errorf("profiles: unsupported selection %v; supported packs: %s", selected, strings.Join(supportedRefs(), ", "))
	}
	if len(selected) > 2 {
		return fmt.Errorf("profiles: selection %v composes at most [%s, <language>@1]", selected, CoreRef)
	}
	if len(selected) == 2 {
		ref := selected[1]
		if ref == CoreRef {
			return fmt.Errorf("profiles: %s may appear exactly once", CoreRef)
		}
		if _, ok := embeddedPacks[ref]; !ok {
			return fmt.Errorf("profiles: unsupported selection %v; supported packs: %s", selected, strings.Join(supportedRefs(), ", "))
		}
		if !languagePacks[ref] {
			return fmt.Errorf("profiles: pack %s is not a language pack; M1 composes only core and language layers", ref)
		}
	}
	return nil
}

// mergeChecks overlays the language layer's check slots onto the core
// layer's: each slot the language pack declares replaces the inherited
// value, so real commands and hinted sentinels win over bare core
// sentinels.
func mergeChecks(base, lang CheckSet) CheckSet {
	out := base
	baseSlots := map[string]*Check{
		"format":    &out.Format,
		"lint":      &out.Lint,
		"typecheck": &out.Typecheck,
		"test":      &out.Test,
		"smoke":     &out.Smoke,
	}
	langSlots := map[string]Check{
		"format":    lang.Format,
		"lint":      lang.Lint,
		"typecheck": lang.Typecheck,
		"test":      lang.Test,
		"smoke":     lang.Smoke,
	}
	for id, c := range langSlots {
		if c.ID == id {
			*baseSlots[id] = c
		}
	}
	return out
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
			if len(spec.Cmd) != 0 {
				return cs, fmt.Errorf("check %q: discovery takes no cmd", spec.ID)
			}
			slot.Discovery = true
			slot.Hint = spec.Hint
			continue
		}
		if len(spec.Cmd) == 0 {
			return cs, fmt.Errorf("check %q: need cmd or discovery", spec.ID)
		}
		if len(spec.Hint) != 0 {
			return cs, fmt.Errorf("check %q: hint belongs to a discovery sentinel", spec.ID)
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

// packDigest returns the hex sha256 of one pack's raw bytes.
func packDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PackDigest returns the canonical integrity digest over ALL embedded
// packs: every pack reference in sorted order, each followed by its raw
// bytes, hashed together. Adding or editing any embedded pack changes
// the digest.
func PackDigest() string {
	refs := make([]string, 0, len(embeddedPacks))
	for ref := range embeddedPacks {
		refs = append(refs, ref)
	}
	slices.Sort(refs)
	h := sha256.New()
	for _, ref := range refs {
		h.Write([]byte(ref))
		h.Write([]byte{0})
		h.Write(embeddedPacks[ref].data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// lockPack is one profiles.lock entry: the pinned version, the pack
// source, and the sha256 digest of the embedded pack bytes.
type lockPack struct {
	Version string `json:"version"`
	Source  string `json:"source"`
	Digest  string `json:"digest"`
}

// lockFile is the profiles.lock document: the canonical digest over all
// embedded packs plus one entry per selected pack. encoding/json sorts
// map keys, so the same selection marshals to identical bytes.
type lockFile struct {
	Digest string              `json:"digest"`
	Packs  map[string]lockPack `json:"packs"`
}

// WriteLock writes the profiles.lock JSON to path for the selected pack
// references: the canonical registry digest plus one entry per selected
// pack carrying its version, source, and the sha256 digest of the
// embedded pack bytes. Task 12's init calls this for
// .project/profiles.lock; adopt calls it for the draft lock with the
// selection pinned in its draft contract, so lock and contract agree.
func WriteLock(path string, selected []string) error {
	lock := lockFile{Digest: PackDigest(), Packs: make(map[string]lockPack, len(selected))}
	for _, ref := range selected {
		entry, ok := embeddedPacks[ref]
		if !ok {
			return fmt.Errorf("profiles: pack %s not registered (supported packs: %s)", ref, strings.Join(supportedRefs(), ", "))
		}
		var pack Pack
		if err := yamlx.UnmarshalStrict(entry.data, &pack); err != nil {
			return fmt.Errorf("profiles: parse %s: %w", ref, err)
		}
		if _, err := pack.checkSet(); err != nil {
			return fmt.Errorf("profiles: pack %s: %w", ref, err)
		}
		lock.Packs[entry.name] = lockPack{
			Version: entry.version,
			Source:  lockSource,
			Digest:  packDigest(entry.data),
		}
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
