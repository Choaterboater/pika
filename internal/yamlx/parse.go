// Package yamlx provides the strict YAML decoding shared by the contract,
// profiles, exceptions, and evidence-schema loaders. goccy/go-yaml has no
// decoder option that rejects duplicate keys or non-string map keys with
// usable [line:col] positions, and its Strict mode is all-or-nothing, so
// structural checks are done here on the AST and per-struct strictness on
// top of a lenient decode.
package yamlx

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// UnmarshalStrict decodes the single YAML document in data into out, rejecting:
//
//   - duplicate mapping keys at any depth (a quoted key and its bare spelling
//     count as the same key), reported at the duplicate's [line:col];
//   - non-string mapping keys, reported at the key's [line:col];
//   - multi-document input (anything but exactly one document);
//   - unknown fields in structs that opt in to strictness.
//
// The root struct is strict. A nested struct opts in when the field holding
// it carries a `yamlx:"strict"` tag; untagged structs decode leniently.
// Strictness applies through pointers, slices, arrays, and maps (a tagged
// field makes the struct elements of its map or slice strict too). Field
// names match goccy's rules: the yaml tag (falling back to the json tag) or
// the lowercased Go field name. Errors report [line:col] positions; callers
// add file context by wrapping.
func UnmarshalStrict(data []byte, out any) error {
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("yamlx: decode target must be a non-nil pointer")
	}
	file, err := parser.ParseBytes(data, 0, parser.AllowDuplicateMapKey())
	if err != nil {
		return err
	}
	if len(file.Docs) > 1 {
		return fmt.Errorf("yamlx: expected a single YAML document, got %d", len(file.Docs))
	}
	if len(file.Docs) == 1 {
		if err := checkDuplicateKeys(file.Docs[0]); err != nil {
			return err
		}
		if err := checkKeys(file.Docs[0], rv.Type().Elem(), true); err != nil {
			return err
		}
	}
	// Duplicate, non-string-key, and unknown-field rejection already happened
	// above; the decode itself stays lenient so untagged structs decode
	// without surprises.
	return yaml.UnmarshalWithOptions(data, out, yaml.AllowDuplicateMapKey())
}

// checkDuplicateKeys walks the YAML AST and reports the first mapping key
// that repeats at the same mapping level, at any depth, or the first key
// that is not a string. Quoted and bare spellings of the same name are the
// same key.
func checkDuplicateKeys(node ast.Node) error {
	switch n := node.(type) {
	case nil:
		return nil
	case *ast.DocumentNode:
		return checkDuplicateKeys(n.Body)
	case *ast.MappingNode:
		seen := make(map[string]struct{}, len(n.Values))
		for _, v := range n.Values {
			key := keyName(v.Key)
			if !isStringKey(v.Key) {
				return positionError(v.Key, "non-string map key %q", key)
			}
			if _, dup := seen[key]; dup {
				return positionError(v.Key, "duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := checkDuplicateKeys(v.Value); err != nil {
				return err
			}
		}
	case *ast.MappingValueNode:
		if err := checkDuplicateKeys(n.Value); err != nil {
			return err
		}
	case *ast.SequenceNode:
		for _, v := range n.Values {
			if err := checkDuplicateKeys(v); err != nil {
				return err
			}
		}
	case *ast.AnchorNode:
		return checkDuplicateKeys(n.Value)
	}
	return nil
}

// isStringKey reports whether key is acceptable as a mapping key: a string
// (bare or quoted) or the merge key. Null, boolean, integer, float, and
// sequence/mapping keys are rejected because the decoders target
// string-keyed Go types only.
func isStringKey(key ast.Node) bool {
	switch key.(type) {
	case *ast.StringNode, *ast.MergeKeyNode:
		return true
	}
	return false
}

// keyName canonicalizes a mapping key for comparison: string keys compare by
// their decoded value (so "name" and name collide), the merge key by its
// "<<" token.
func keyName(key ast.Node) string {
	if s, ok := key.(*ast.StringNode); ok {
		return s.Value
	}
	if tk := key.GetToken(); tk != nil {
		return tk.Value
	}
	return key.String()
}

// checkKeys walks the AST alongside the target type and rejects unknown
// fields in strict structs. Nodes whose subtree type is not statically known
// (any, aliases) are skipped; the structural checks above cover them.
//
// A bare mapping found where a []T (or [N]T) is declared is checked
// against T itself, not against the slice: some fields accept either
// one object or a list of them under the same key (a custom
// UnmarshalYAML makes the decode succeed either way), and strictness
// must see through the singular form the same as the plural one.
func checkKeys(node ast.Node, t reflect.Type, strict bool) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch n := node.(type) {
	case nil:
		return nil
	case *ast.DocumentNode:
		return checkKeys(n.Body, t, strict)
	case *ast.MappingNode:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			// A bare object where a []T field is declared: a caller-side
			// custom UnmarshalYAML may accept this as a one-item list (the
			// shape internal/checks uses for exceptions.yaml), and if it
			// does, strictness must validate the object against T, the
			// element type, or an unknown field inside it would never be
			// caught — the node shape not matching the slice kind is not
			// the same thing as the object being valid.
			return checkMappingKeys(n, t.Elem(), strict)
		}
		return checkMappingKeys(n, t, strict)
	case *ast.MappingValueNode:
		// A single-entry mapping (e.g. a nested "a: b: c" chain).
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			return checkMappingEntry(n, t.Elem(), strict)
		}
		return checkMappingEntry(n, t, strict)
	case *ast.SequenceNode:
		switch t.Kind() {
		case reflect.Slice, reflect.Array:
			for _, v := range n.Values {
				if err := checkKeys(v, t.Elem(), strict); err != nil {
					return err
				}
			}
		}
	case *ast.AnchorNode:
		return checkKeys(n.Value, t, strict)
	}
	return nil
}

func checkMappingKeys(node *ast.MappingNode, t reflect.Type, strict bool) error {
	switch t.Kind() {
	case reflect.Struct:
		fields := structFields(t)
		for _, v := range node.Values {
			if err := checkFieldEntry(v, fields, strict); err != nil {
				return err
			}
		}
	case reflect.Map:
		for _, v := range node.Values {
			if err := checkKeys(v.Value, t.Elem(), strict); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkMappingEntry(node *ast.MappingValueNode, t reflect.Type, strict bool) error {
	switch t.Kind() {
	case reflect.Struct:
		return checkFieldEntry(node, structFields(t), strict)
	case reflect.Map:
		return checkKeys(node.Value, t.Elem(), strict)
	}
	return nil
}

func checkFieldEntry(v *ast.MappingValueNode, fields map[string]fieldInfo, strict bool) error {
	if _, isMerge := v.Key.(*ast.MergeKeyNode); isMerge {
		// The merged mapping's shape is unknown statically; decode handles it.
		return nil
	}
	s, ok := v.Key.(*ast.StringNode)
	if !ok {
		// Non-string key: reported by the structural walk; nothing to match.
		return nil
	}
	f, known := fields[s.Value]
	if !known {
		if strict {
			return positionError(v.Key, `unknown field %q`, s.Value)
		}
		return nil
	}
	return checkKeys(v.Value, f.typ, f.strict)
}

func positionError(key ast.Node, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if tk := key.GetToken(); tk != nil && tk.Position != nil {
		return fmt.Errorf("[%d:%d] %s", tk.Position.Line, tk.Position.Column, msg)
	}
	return fmt.Errorf("%s", msg)
}

// fieldInfo describes one decodable struct field.
type fieldInfo struct {
	typ    reflect.Type
	strict bool // `yamlx:"strict"` opts the subtree into unknown-key rejection
	inline bool
}

// structFields mirrors goccy/go-yaml's struct field resolution: the yaml tag
// names the field, falling back to the json tag, then to the lowercased Go
// field name; unexported and "-"-tagged fields are skipped. Inline embedded
// structs contribute their own fields to the parent's key set.
func structFields(t reflect.Type) map[string]fieldInfo {
	fields := make(map[string]fieldInfo)
	collectFields(t, fields)
	return fields
}

func collectFields(t reflect.Type, fields map[string]fieldInfo) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("yaml")
		if tag == "" {
			tag = field.Tag.Get("json")
		}
		if field.PkgPath != "" && !field.Anonymous || tag == "-" {
			continue
		}
		options := strings.Split(tag, ",")
		name := strings.ToLower(field.Name)
		if options[0] != "" {
			name = options[0]
		}
		info := fieldInfo{typ: field.Type}
		switch field.Tag.Get("yamlx") {
		case "strict":
			info.strict = true
		}
		for _, opt := range options[1:] {
			if opt == "inline" {
				info.inline = true
			}
		}
		if field.Anonymous && (info.inline || tag == "") {
			// Embedded struct: its fields are addressable directly.
			if elem := info.typ; elem.Kind() == reflect.Struct ||
				(elem.Kind() == reflect.Pointer && elem.Elem().Kind() == reflect.Struct) {
				collectFields(elem, fields)
				continue
			}
		}
		fields[name] = info
	}
}
