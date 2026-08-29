package yamlx

import (
	"strings"
	"testing"
)

// target types exercising the strict-tag opt-in.

type strictInner struct {
	Name string `yaml:"name"`
}

type lenientInner struct {
	Name string `yaml:"name"`
}

type taggedRoot struct {
	Inner strictInner  `yamlx:"strict" yaml:"inner"`
	Loose lenientInner `yaml:"loose"`
}

type plainRoot struct {
	Inner strictInner `yamlx:"strict" yaml:"inner"`
}

func TestValidDocumentPasses(t *testing.T) {
	var out taggedRoot
	src := `
inner:
  name: a
loose:
  name: b
`
	if err := UnmarshalStrict([]byte(src), &out); err != nil {
		t.Fatalf("expected valid doc to decode, got %v", err)
	}
	if out.Inner.Name != "a" || out.Loose.Name != "b" {
		t.Fatalf("decoded = %+v", out)
	}
}

func TestRejectDuplicateRootKey(t *testing.T) {
	var out taggedRoot
	src := `
inner:
  name: a
inner:
  name: b
`
	err := UnmarshalStrict([]byte(src), &out)
	if err == nil {
		t.Fatal("expected duplicate root key error, got nil")
	}
	if !strings.Contains(err.Error(), `duplicate key "inner"`) {
		t.Fatalf("error should name the duplicate key, got: %v", err)
	}
	if !strings.Contains(err.Error(), "[4:1]") {
		t.Fatalf("error should report the second occurrence position, got: %v", err)
	}
}

func TestRejectDuplicateNestedKey(t *testing.T) {
	var out taggedRoot
	src := `
inner:
  name: a
  name: b
`
	err := UnmarshalStrict([]byte(src), &out)
	if err == nil {
		t.Fatal("expected duplicate nested key error, got nil")
	}
	if !strings.Contains(err.Error(), `duplicate key "name"`) {
		t.Fatalf("error should name the duplicate key, got: %v", err)
	}
	if !strings.Contains(err.Error(), "[4:3]") {
		t.Fatalf("error should report [line:col] of the duplicate, got: %v", err)
	}
}

func TestRejectDuplicateKeyInSequenceOfMaps(t *testing.T) {
	var out struct {
		Items []strictInner `yamlx:"strict" yaml:"items"`
	}
	src := `
items:
  - name: x
    name: y
  - name: z
`
	err := UnmarshalStrict([]byte(src), &out)
	if err == nil {
		t.Fatal("expected duplicate key error inside sequence element, got nil")
	}
	if !strings.Contains(err.Error(), `duplicate key "name"`) {
		t.Fatalf("error should name the duplicate key, got: %v", err)
	}
}

func TestRejectDuplicateQuotedVsBareKey(t *testing.T) {
	var out taggedRoot
	src := `
inner:
  name: a
  "name": b
`
	err := UnmarshalStrict([]byte(src), &out)
	if err == nil {
		t.Fatal("expected quoted/bare duplicate key error, got nil")
	}
	if !strings.Contains(err.Error(), `duplicate key "name"`) {
		t.Fatalf("error should treat quoted and bare as the same key, got: %v", err)
	}
}

func TestRejectUnknownKeyInStrictStruct(t *testing.T) {
	var out taggedRoot
	src := `
inner:
  name: a
  bogus: true
`
	err := UnmarshalStrict([]byte(src), &out)
	if err == nil {
		t.Fatal("expected unknown key error in strict struct, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error should name the unknown key, got: %v", err)
	}
	if !strings.Contains(err.Error(), "[4:3]") {
		t.Fatalf("error should report [line:col] of the unknown key, got: %v", err)
	}
}

func TestUnknownKeyInUntaggedStructDecodes(t *testing.T) {
	var out taggedRoot
	src := `
inner:
  name: a
loose:
  name: b
  extra: 1
`
	if err := UnmarshalStrict([]byte(src), &out); err != nil {
		t.Fatalf("lenient struct must accept unknown keys, got %v", err)
	}
	if out.Loose.Name != "b" {
		t.Fatalf("decoded = %+v", out)
	}
}

func TestUnknownKeyAtRootRejected(t *testing.T) {
	var out plainRoot
	src := `
inner:
  name: a
bogus: true
`
	err := UnmarshalStrict([]byte(src), &out)
	if err == nil {
		t.Fatal("expected unknown key error at root, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error should name the unknown key, got: %v", err)
	}
}

func TestRejectNonStringMapKey(t *testing.T) {
	var out struct {
		M map[string]string `yaml:"m"`
	}
	src := `
m:
  1: a
`
	err := UnmarshalStrict([]byte(src), &out)
	if err == nil {
		t.Fatal("expected non-string map key error, got nil")
	}
	if !strings.Contains(err.Error(), "[3:3]") {
		t.Fatalf("error should report [line:col] of the offending key, got: %v", err)
	}
}

func TestRejectMultiDocument(t *testing.T) {
	var out taggedRoot
	src := `
inner:
  name: a
---
inner:
  name: b
`
	err := UnmarshalStrict([]byte(src), &out)
	if err == nil {
		t.Fatal("expected multi-document error, got nil")
	}
	if !strings.Contains(err.Error(), "document") {
		t.Fatalf("error should mention documents, got: %v", err)
	}
}

func TestEmptyInputDecodesZeroValue(t *testing.T) {
	var out taggedRoot
	if err := UnmarshalStrict(nil, &out); err != nil {
		t.Fatalf("empty input must decode to zero value, got %v", err)
	}
	if out.Inner.Name != "" || out.Loose.Name != "" {
		t.Fatalf("decoded = %+v, want zero value", out)
	}
}
