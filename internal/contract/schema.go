package contract

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
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
// at path. The returned Contract has typed fields matching the schema; keys
// outside it are rejected unless nested under extensions.
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
