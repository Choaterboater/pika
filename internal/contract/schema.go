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

	"github.com/Choaterboater/projectctl/internal/yamlx"
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
		if err := compiler.AddResource("urn:projectctl:contract.schema.json", doc); err != nil {
			compileSchemaErr = err
			return
		}
		compiledSchema, compileSchemaErr = compiler.Compile("urn:projectctl:contract.schema.json")
	})
	return compiledSchema, compileSchemaErr
}

// Contract is the typed representation of a projectctl contract file
// (spec section 5.3 YAML shape).
type Contract struct {
	Schema     int                    `yaml:"schema"     json:"schema"`
	Project    Project                `yaml:"project"    json:"project"`
	Profiles   []string               `yaml:"profiles"   json:"profiles,omitempty"`
	Packages   map[string]Package     `yaml:"packages"   json:"packages,omitempty"`
	Commands   map[string]string      `yaml:"commands"   json:"commands,omitempty"`
	Agents     map[string]AgentConfig `yaml:"agents"     json:"agents,omitempty"`
	GitHub     GitHub                 `yaml:"github"     json:"github"`
	Evidence   Evidence               `yaml:"evidence"   json:"evidence"`
	Exceptions []any                  `yaml:"exceptions" json:"exceptions,omitempty"`
	Extensions map[string]any         `yaml:"extensions" json:"extensions,omitempty"`
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

// Load reads, strictly parses, and JSON-Schema-validates the contract file
// at path. Every declared repository-relative path (currently
// packages.<name>.root) is normalized to forward slashes and checked
// against path escapes; keys outside the schema are rejected unless nested
// under extensions.
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
// pass either form); absolute paths (leading /, drive letters, UNC) and
// paths that traverse above the repository root after cleaning are
// rejected, as is the empty path.
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
	cleaned := path.Clean(norm)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path escapes repository root: %s", p)
	}
	return cleaned, nil
}

func isDriveLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}
