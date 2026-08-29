// Package envelope implements the capability envelope: the authorization
// record that states, per change class, what a task is allowed to do
// (spec section 12.4). Policy is deny-by-default everywhere — an absent
// class denies, an empty list denies, and only explicit entries allow.
// The envelope file itself is runtime state at .project/state/
// envelope.yaml (gitignored). The schema describing its shape is
// embedded here, at internal/envelope/envelope.schema.json — the one
// canonical in-repo copy.
package envelope

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Choaterboater/projectctl/internal/contract"
	"github.com/Choaterboater/projectctl/internal/yamlx"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed envelope.schema.json
var schemaJSON []byte

var (
	compileSchemaOnce sync.Once
	compiledSchema    *jsonschema.Schema
	compileSchemaErr  error
)

func compileSchema() (*jsonschema.Schema, error) {
	compileSchemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
		if err != nil {
			compileSchemaErr = err
			return
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("urn:projectctl:envelope.schema.json", doc); err != nil {
			compileSchemaErr = err
			return
		}
		compiledSchema, compileSchemaErr = compiler.Compile("urn:projectctl:envelope.schema.json")
	})
	return compiledSchema, compileSchemaErr
}

// Operation kinds recognized by Envelope.Allows.
const (
	KindFSRead     = "fs_read"
	KindFSWrite    = "fs_write"
	KindExec       = "exec"
	KindNetwork    = "network"
	KindCredential = "credential"
	KindGitHub     = "github"
	KindBudget     = "budget"
)

// Operation is a single authorization question: Kind names the change
// class and Target the specific object (a repo-relative path for fs_read /
// fs_write, an argv line for exec, a host or host:port for network, a
// credential name, a GitHub scope string, or a budget provider).
type Operation struct {
	Kind   string
	Target string
}

// Env is the parsed, schema-validated envelope policy. It is bound to a
// repository root by NewEnvelope before authorization decisions are made.
type Env struct {
	Schema           int    `yaml:"schema"            json:"schema"`
	Allow            Allow  `yaml:"allow"             json:"allow,omitempty" yamlx:"strict"`
	RollbackBoundary string `yaml:"rollback_boundary" json:"rollback_boundary,omitempty"`
}

// Allow lists the explicitly granted capabilities per change class. Every
// class is deny-by-default: nothing here means nothing allowed. Budget
// maps provider names to a numeric USD ceiling; the ceiling is static
// policy — M1 compares spend against it elsewhere, Allows only gates on
// the provider being declared.
type Allow struct {
	FSWrite    []string           `yaml:"fs_write"   json:"fs_write,omitempty"`
	Exec       []string           `yaml:"exec"       json:"exec,omitempty"`
	Network    []string           `yaml:"network"    json:"network,omitempty"`
	Credential []string           `yaml:"credential" json:"credential,omitempty"`
	GitHub     []string           `yaml:"github"     json:"github,omitempty"`
	Budget     map[string]float64 `yaml:"budget"     json:"budget,omitempty"`
}

// Envelope binds an Env to a repository root so Allows stays pure.
type Envelope struct {
	Env      *Env
	repoRoot string
}

// Validate strictly parses (yamlx) and JSON-Schema-validates an envelope
// document, then normalizes declared fs_write paths to repo-relative form.
func Validate(data []byte) (*Env, error) {
	var raw any
	if err := yamlx.UnmarshalStrict(data, &raw); err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	if err := checkRawShape(raw); err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	var env Env
	if err := yamlx.UnmarshalStrict(data, &env); err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	bs, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("envelope: encode for validation: %w", err)
	}
	var instance any
	if err := json.Unmarshal(bs, &instance); err != nil {
		return nil, fmt.Errorf("envelope: decode for validation: %w", err)
	}
	s, err := compileSchema()
	if err != nil {
		return nil, fmt.Errorf("envelope: embedded schema invalid: %w", err)
	}
	if err := s.Validate(instance); err != nil {
		return nil, fmt.Errorf("envelope: schema validation failed: %w", err)
	}
	// A bare "*" exec entry would match every command (the prefix
	// wildcard consumes nothing) — the one silent allow-all in a
	// fail-closed module. Operators must list the commands they mean.
	for i, e := range env.Allow.Exec {
		if e == "*" {
			return nil, fmt.Errorf("envelope: allow.exec[%d]: a bare \"*\" entry grants every command; list the specific commands instead", i)
		}
	}
	for i, p := range env.Allow.FSWrite {
		norm, err := contract.NormalizeRepoPath(p)
		if err != nil {
			return nil, fmt.Errorf("envelope: allow.fs_write[%d]: %w", i, err)
		}
		env.Allow.FSWrite[i] = norm
	}
	return &env, nil
}

// checkRawShape guards against the YAML decoder coercing non-string
// scalars into the typed Env fields (goccy decodes `1` into a string
// field as "1"): allow lists must be lists of strings, budget ceilings
// numbers, schema an integer, rollback_boundary a string.
func checkRawShape(raw any) error {
	root, ok := raw.(map[string]any)
	if !ok {
		return errors.New("document must be a mapping")
	}
	if v, ok := root["schema"]; ok && !isInt(v) {
		return errors.New("schema must be an integer")
	}
	if v, ok := root["rollback_boundary"]; ok {
		if _, isStr := v.(string); !isStr {
			return errors.New("rollback_boundary must be a string")
		}
	}
	allow, ok := root["allow"]
	if !ok {
		return nil
	}
	am, ok := allow.(map[string]any)
	if !ok {
		return errors.New("allow must be a mapping")
	}
	for _, key := range []string{"fs_write", "exec", "network", "credential", "github"} {
		v, present := am[key]
		if !present {
			continue
		}
		list, isList := v.([]any)
		if !isList {
			return fmt.Errorf("allow.%s must be a list", key)
		}
		for _, item := range list {
			if _, isStr := item.(string); !isStr {
				return fmt.Errorf("allow.%s entries must be strings", key)
			}
		}
	}
	if v, present := am["budget"]; present {
		bm, isMap := v.(map[string]any)
		if !isMap {
			return errors.New("allow.budget must be a mapping")
		}
		for provider, n := range bm {
			if !isNumber(n) {
				return fmt.Errorf("allow.budget.%s must be a number", provider)
			}
		}
	}
	return nil
}

// isInt reports whether v is a YAML integer (goccy decodes integers in
// any-typed positions as uint64, with int/int64 fallbacks by width).
func isInt(v any) bool {
	switch v.(type) {
	case uint64, int, int64:
		return true
	}
	return false
}

// isNumber reports whether v is a YAML integer or float.
func isNumber(v any) bool {
	return isInt(v) || isFloat(v)
}

func isFloat(v any) bool {
	_, ok := v.(float64)
	return ok
}

// NewEnvelope binds a validated Env to repoRoot so Allows can apply the
// fs_read repository-inside default without side effects.
func NewEnvelope(env *Env, repoRoot string) *Envelope {
	return &Envelope{Env: env, repoRoot: filepath.Clean(repoRoot)}
}

// Load reads and validates the envelope file at path, expected at
// .project/state/envelope.yaml under a repository root, which is derived
// by stripping that layout from path.
func Load(path string) (*Envelope, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("envelope: read %s: %w", path, err)
	}
	env, err := Validate(src)
	if err != nil {
		return nil, fmt.Errorf("envelope: %s: %w", path, err)
	}
	return NewEnvelope(env, filepath.Dir(filepath.Dir(filepath.Dir(path)))), nil
}

// Allows reports whether op is authorized. Every class is deny-by-default,
// unknown kinds deny, and the zero Envelope denies everything. fs_read is
// the one liberal default: reads inside the repository root are always
// allowed; reads outside it are denied — the M1 envelope declares no
// out-of-repo read scope.
func (e *Envelope) Allows(op Operation) bool {
	if e == nil || e.Env == nil {
		return false
	}
	switch op.Kind {
	case KindFSRead:
		return e.allowsRead(op.Target)
	case KindFSWrite:
		return e.matchesPath(op.Target)
	case KindExec:
		return matchesExec(e.Env.Allow.Exec, op.Target)
	case KindNetwork:
		return matchesNetwork(e.Env.Allow.Network, op.Target)
	case KindCredential:
		return containsExact(e.Env.Allow.Credential, op.Target)
	case KindGitHub:
		return containsExact(e.Env.Allow.GitHub, op.Target)
	case KindBudget:
		_, ok := e.Env.Allow.Budget[op.Target]
		return ok
	default:
		return false
	}
}

// allowsRead reports whether target lies inside the repository root.
// Relative targets are repo-relative by definition and must not escape;
// absolute targets must resolve below repoRoot.
func (e *Envelope) allowsRead(target string) bool {
	if target == "" {
		return false
	}
	if filepath.IsAbs(target) {
		rel, err := filepath.Rel(e.repoRoot, filepath.Clean(target))
		if err != nil {
			return false
		}
		return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
	}
	_, err := contract.NormalizeRepoPath(target)
	return err == nil
}

// matchesPath matches target against the fs_write entries: an entry allows
// itself and everything beneath it (directory-prefix semantics), compared
// on normalized repo-relative paths so entry ".project/state" permits
// ".project/state/x.json" but not ".project/staterun/x".
func (e *Envelope) matchesPath(target string) bool {
	norm, err := contract.NormalizeRepoPath(target)
	if err != nil {
		return false
	}
	for _, entry := range e.Env.Allow.FSWrite {
		if norm == entry || strings.HasPrefix(norm, entry+"/") {
			return true
		}
	}
	return false
}

// matchesExec matches an argv line against exec entries. Entry and target
// are split on whitespace. An entry matches when every entry element
// equals the corresponding target element and the counts are equal
// (exact), or when the entry's last element is a bare "*", in which case
// the remaining entry elements must form an element-wise prefix of the
// target ("git *" allows "git push origin main").
func matchesExec(entries []string, target string) bool {
	tv := strings.Fields(target)
	if len(tv) == 0 {
		return false
	}
	for _, entry := range entries {
		ev := strings.Fields(entry)
		if len(ev) == 0 {
			continue
		}
		glob := ev[len(ev)-1] == "*"
		if glob {
			ev = ev[:len(ev)-1]
		}
		if len(ev) > len(tv) || (!glob && len(ev) != len(tv)) {
			continue
		}
		match := true
		for i, ee := range ev {
			if ee != tv[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// matchesNetwork matches a host or host:port target against network
// entries. An exact entry matches itself; a portless entry matches its
// host on any port; a port-pinned entry matches only that exact host:port;
// an "*.suffix" entry matches subdomains of suffix on any port.
func matchesNetwork(entries []string, target string) bool {
	if target == "" {
		return false
	}
	host, port, hasPort := splitHostPort(target)
	if host == "" {
		return false
	}
	for _, entry := range entries {
		if entry == target {
			return true
		}
		ehost, eport, ehasPort := splitHostPort(entry)
		if strings.HasPrefix(ehost, "*.") {
			if !strings.HasSuffix(host, ehost[1:]) {
				continue
			}
			if ehasPort && (!hasPort || eport != port) {
				continue
			}
			return true
		}
		if ehost == host && (!ehasPort || (hasPort && eport == port)) {
			return true
		}
	}
	return false
}

// splitHostPort splits "host[:port]" on the last colon; the suffix counts
// as a port only when it is entirely digits (leaving IPv6 literals and
// odd spellings to exact-match entries).
func splitHostPort(s string) (host, port string, hasPort bool) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return s, "", false
	}
	p := s[i+1:]
	for _, c := range p {
		if c < '0' || c > '9' {
			return s, "", false
		}
	}
	return s[:i], p, true
}

func containsExact(list []string, target string) bool {
	if target == "" {
		return false
	}
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}
