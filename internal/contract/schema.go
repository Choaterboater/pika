package contract

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/Choaterboater/pika/internal/yamlx"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed contract.schema.json
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
		if err := compiler.AddResource("urn:pika:contract.schema.json", doc); err != nil {
			compileSchemaErr = err
			return
		}
		compiledSchema, compileSchemaErr = compiler.Compile("urn:pika:contract.schema.json")
	})
	return compiledSchema, compileSchemaErr
}

// Contract is the typed representation of a pika contract file
// (spec section 5.3 YAML shape).
type Contract struct {
	Schema   int                    `yaml:"schema"     json:"schema"`
	Project  Project                `yaml:"project"    json:"project"`
	Profiles []string               `yaml:"profiles"   json:"profiles,omitempty"`
	Packages map[string]Package     `yaml:"packages"   json:"packages,omitempty"`
	Commands map[string]string      `yaml:"commands"   json:"commands,omitempty"`
	Agents   map[string]AgentConfig `yaml:"agents"     json:"agents,omitempty"`
	GitHub   GitHub                 `yaml:"github"     json:"github"`
	Evidence Evidence               `yaml:"evidence"   json:"evidence"`
	// The skills block is strict all the way down. A key nobody reads is
	// how an author comes to believe a contract asked for something it
	// cannot ask for — a global install, most of all, which no contract
	// may request at any spelling. Being told the key means nothing is
	// the only answer that leaves them informed.
	Skills     *Skills        `yaml:"skills,omitempty" json:"skills,omitempty" yamlx:"strict"`
	Exceptions []any          `yaml:"exceptions" json:"exceptions,omitempty"`
	Extensions map[string]any `yaml:"extensions" json:"extensions,omitempty"`
}

// Project identifies the project and its layout topology.
type Project struct {
	Name     string `yaml:"name"     json:"name"`
	Topology string `yaml:"topology" json:"topology"`
}

// Package declares a package and the profiles it belongs to.
type Package struct {
	Root     string   `yaml:"root"     json:"root"`
	Profiles []string `yaml:"profiles" json:"profiles"`
}

// AgentConfig configures a named agent entry.
type AgentConfig struct {
	Runtime  string `yaml:"runtime"  json:"runtime"`
	Provider string `yaml:"provider" json:"provider,omitempty"`
	Model    string `yaml:"model"    json:"model,omitempty"`
	Effort   string `yaml:"effort"   json:"effort,omitempty"`
}

// GitHub holds repository workflow settings.
type GitHub struct {
	Merge string `yaml:"merge" json:"merge"`
}

// Evidence holds evidence publication policy.
type Evidence struct {
	Publish string `yaml:"publish" json:"publish"`
}

// Skills declares where the canonical agent skills — the harness-neutral
// source under .agents/skills — are additionally projected for harnesses
// that cannot read that location (spec §9.2). It is a pointer so a
// contract that declares nothing writes nothing: an empty `skills: {}`
// block in every generated contract would be noise claiming a decision
// nobody made.
//
// It declares repository projections and nothing else. The agent files
// in the operator's home directory are not reachable from here at any
// spelling: they are installed by an explicit `pika skills install
// --global` on a command line, so that cloning a repository never grants
// it a capability over the machine that cloned it.
type Skills struct {
	Projections []Projection `yaml:"projections" json:"projections,omitempty" yamlx:"strict"`
}

// Projection is one harness-native copy of that guidance: which harness
// reads it, and the repository-relative file it reads.
//
// Harness is drawn from the same enumeration as agents.<name>.runtime,
// which is the point: the harness you configured as the builder is the
// harness you project to, and a typo is a schema violation rather than a
// file nothing will ever read. Path is the whole of the target: a
// harness whose requirement is "a file at this path" needs no kernel
// change, only a contract line.
type Projection struct {
	Harness string `yaml:"harness" json:"harness"`
	Path    string `yaml:"path"    json:"path"`
}

// Load reads, strictly parses, and JSON-Schema-validates the contract file
// at path. Every declared repository-relative path (packages.<name>.root
// and skills.projections[].path) is normalized to forward slashes and
// checked against path escapes; keys outside the schema are rejected
// unless nested under extensions.
func Load(path string) (*Contract, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("contract: read %s: %w", path, err)
	}

	var c Contract
	if err := yamlx.UnmarshalStrict(src, &c); err != nil {
		return nil, fmt.Errorf("contract: %s: %w", path, err)
	}

	if err := validate(&c); err != nil {
		return nil, fmt.Errorf("contract: %s: %w", path, err)
	}

	for name, pkg := range c.Packages {
		root, err := NormalizeRepoPath(pkg.Root)
		if err != nil {
			return nil, fmt.Errorf("contract: packages.%s.root: %w", name, err)
		}
		pkg.Root = root
		c.Packages[name] = pkg
	}
	if c.Skills != nil {
		for i, p := range c.Skills.Projections {
			path, err := NormalizeRepoPath(p.Path)
			if err != nil {
				return nil, fmt.Errorf("contract: skills.projections[%d].path: %w", i, err)
			}
			c.Skills.Projections[i].Path = path
		}
	}
	return &c, nil
}

// validate checks the typed contract against the embedded JSON Schema.
func validate(c *Contract) error {
	bs, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("contract: encode for validation: %w", err)
	}
	var instance any
	if err := json.Unmarshal(bs, &instance); err != nil {
		return fmt.Errorf("contract: decode for validation: %w", err)
	}
	s, err := compileSchema()
	if err != nil {
		return fmt.Errorf("contract: embedded schema invalid: %w", err)
	}
	if err := s.Validate(instance); err != nil {
		return fmt.Errorf("contract: schema validation failed: %w", err)
	}
	return nil
}

// NormalizeRepoPath normalizes a declared repository-relative path to
// forward-slash form and validates it stays inside the repository root.
// Backslash separators are accepted and converted (Windows callers may
// pass either form); absolute paths (leading /, drive letters, UNC),
// home-relative paths (a leading ~) and paths that traverse above the
// repository root after cleaning are rejected, as is the empty path.
//
// The `~` rule is the one that is not about traversal. Go expands
// nothing, so `~/.codex/AGENTS.md` would be taken as an ordinary
// relative path and produce a directory literally named `~` inside the
// repository — a contract that reads as though it writes to the
// operator's home directory and instead writes rubbish nobody looks at.
// Refusing it says what the contract cannot do rather than doing
// something else quietly: the home directory is reachable only through
// an explicit `pika skills install --global` on a command line, never
// through a file a repository ships.
func NormalizeRepoPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is empty")
	}
	norm := strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(norm, "/") {
		return "", fmt.Errorf("path escapes repository root: %s", p)
	}
	if len(norm) >= 2 && norm[1] == ':' && isDriveLetter(norm[0]) {
		return "", fmt.Errorf("path escapes repository root: %s", p)
	}
	if norm == "~" || strings.HasPrefix(norm, "~/") {
		return "", fmt.Errorf("path is relative to a home directory, which a contract cannot reach: %s", p)
	}
	cleaned := path.Clean(norm)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path escapes repository root: %s", p)
	}
	return cleaned, nil
}

func isDriveLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}
