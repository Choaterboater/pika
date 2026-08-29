package contract

import (
	"strings"
	"testing"
)

func TestValidMinimumContract(t *testing.T) {
	c, err := Load("testdata/valid-minimum.yaml")
	if err != nil {
		t.Fatalf("expected valid contract, got %v", err)
	}
	if c.Schema != 1 {
		t.Fatalf("schema = %d, want 1", c.Schema)
	}
	if len(c.Profiles) < 1 {
		t.Fatal("profiles must not be empty")
	}
	if c.Project.Name != "fixture" || c.Project.Topology != "single" {
		t.Fatalf("project = %+v, want name=fixture topology=single", c.Project)
	}
	if c.Commands["test"] != "go test ./..." {
		t.Fatalf("commands[test] = %q, want %q", c.Commands["test"], "go test ./...")
	}
	if c.GitHub.Merge != "squash" {
		t.Fatalf("github.merge = %q, want squash", c.GitHub.Merge)
	}
	if c.Evidence.Publish != "sanitized" {
		t.Fatalf("evidence.publish = %q, want sanitized", c.Evidence.Publish)
	}
}

func TestRejectDuplicateYAMLKey(t *testing.T) {
	_, err := Load("testdata/invalid-duplicate-key.yaml")
	if err == nil {
		t.Fatal("expected duplicate-key error, got nil")
	}
	// The second `schema` key is on line 2, column 1 of the fixture.
	if !strings.Contains(err.Error(), `[2:1] duplicate key "schema"`) {
		t.Fatalf("error should report [line:col] and name the duplicate key, got: %v", err)
	}
}
func TestRejectUnknownTopLevelKey(t *testing.T) {
	_, err := Load("testdata/invalid-unknown-key.yaml")
	if err == nil {
		t.Fatal("expected unknown-key error, got nil")
	}
	if !strings.Contains(err.Error(), "bogusKey") {
		t.Fatalf("error should name the unknown key, got: %v", err)
	}
}

func TestRejectInvalidEnum(t *testing.T) {
	_, err := Load("testdata/invalid-enum.yaml")
	if err == nil {
		t.Fatal("expected enum violation error, got nil")
	}
	if !strings.Contains(err.Error(), "topology") {
		t.Fatalf("error should name the offending field, got: %v", err)
	}
}

func TestAgentsTypedConfig(t *testing.T) {
	c, err := Load("testdata/valid-minimum.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Agents != nil {
		t.Fatalf("agents should be absent in minimal fixture, got %+v", c.Agents)
	}
}
