package checks

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// ReassignedRecord is one exception record ReassignAutoRecordedOwners
// changed the owner of.
type ReassignedRecord struct {
	Path   string `json:"path"`
	RuleID string `json:"ruleId"`
}

// Reassignment is what ReassignAutoRecordedOwners changed.
type Reassignment struct {
	Owner      string             `json:"owner"`
	Reassigned int                `json:"reassigned"`
	Records    []ReassignedRecord `json:"records,omitempty"`
}

// ReassignAutoRecordedOwners rewrites every exception record still owned
// by AutoRecordedOwner to owner, in place under repoRoot, and reports
// what it changed. A repository with none to reassign is a valid,
// unsurprising outcome — Reassignment.Reassigned is 0 — not an error.
//
// The rewrite edits bytes in place rather than re-marshaling the file
// through LoadExceptions and back: .project/exceptions.yaml is
// committed evidence, routinely carrying a header comment, hand-quoted
// multi-line reasons, and a redundant `path:` field repeating the
// mapping key — all of which a fresh yaml.Marshal would drop or
// reflow, turning a one-field ownership change into an unreviewable
// diff of a file nobody meant to touch. Instead every "owner:" value
// token that decodes to exactly AutoRecordedOwner is located by the
// line and column the parsed AST reports for it and replaced within
// that one line, leaving every other byte — comments, quoting, field
// order, the rest of the file — untouched.
//
// The file must already load through LoadExceptions before anything is
// written: an unverifiable record must not be rewritten any more than
// it may be silently accepted (gate 1 applies the same rule the other
// direction). After writing, the result is loaded again and asserted
// to carry zero remaining placeholder owners, so a caller never learns
// of success from anything but the kernel's own loader re-reading what
// actually landed.
func ReassignAutoRecordedOwners(repoRoot, owner string) (Reassignment, error) {
	if strings.TrimSpace(owner) == "" {
		return Reassignment{}, fmt.Errorf("owner must not be empty")
	}
	if owner == AutoRecordedOwner {
		return Reassignment{}, fmt.Errorf("owner %q is the placeholder itself; reassigning to it is not a reassignment", AutoRecordedOwner)
	}
	target := filepath.Join(repoRoot, filepath.FromSlash(ExceptionsFile))
	if _, err := LoadExceptions(repoRoot); err != nil {
		return Reassignment{}, fmt.Errorf("refusing to edit an exceptions record that does not load: %w", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return Reassignment{Owner: owner}, nil
		}
		return Reassignment{}, fmt.Errorf("%s: %w", ExceptionsFile, err)
	}

	file, err := parser.ParseBytes(data, 0)
	if err != nil {
		return Reassignment{}, fmt.Errorf("%s: %w", ExceptionsFile, err)
	}
	if len(file.Docs) != 1 || file.Docs[0].Body == nil {
		return Reassignment{Owner: owner}, nil
	}
	root, ok := file.Docs[0].Body.(*ast.MappingNode)
	if !ok {
		return Reassignment{Owner: owner}, nil
	}

	type edit struct {
		line, col int // 1-indexed, as goccy reports them
		record    ReassignedRecord
	}
	var edits []edit
	for _, pathEntry := range root.Values {
		for _, ex := range exceptionMappings(pathEntry.Value) {
			ownerEntry := mappingEntry(ex, "owner")
			if ownerEntry == nil {
				continue
			}
			ownerNode, ok := ownerEntry.Value.(*ast.StringNode)
			if !ok || ownerNode.Value != AutoRecordedOwner {
				continue
			}
			tok := ownerNode.GetToken()
			if tok == nil || tok.Position == nil {
				continue
			}
			ruleID := ""
			if r := mappingEntry(ex, "rule-id"); r != nil {
				if s, ok := r.Value.(*ast.StringNode); ok {
					ruleID = s.Value
				}
			}
			edits = append(edits, edit{
				line:   tok.Position.Line,
				col:    tok.Position.Column,
				record: ReassignedRecord{Path: keyString(pathEntry.Key), RuleID: ruleID},
			})
		}
	}
	if len(edits) == 0 {
		return Reassignment{Owner: owner}, nil
	}

	rendered, err := yaml.Marshal(owner)
	if err != nil {
		return Reassignment{}, fmt.Errorf("encode owner %q: %w", owner, err)
	}
	replacement := string(bytes.TrimSuffix(rendered, []byte("\n")))

	// A line-scoped replacement, not a whole-file byte splice: goccy's
	// token.Position.Offset does not point at a stable, documented
	// boundary relative to a scalar's own text (verified empirically —
	// it disagreed with both the leading and trailing whitespace an
	// Origin span would suggest), where Line and Column are exactly
	// the coordinates every "[line:col]" error message in this
	// codebase already trusts. Editing one line via its own column
	// also makes edits to different records independent of each
	// other's positions, so there is no earlier-offset-invalidated-by-
	// a-later-edit bookkeeping to get wrong.
	fileLines := strings.Split(string(data), "\n")
	for _, e := range edits {
		i := e.line - 1
		if i < 0 || i >= len(fileLines) {
			return Reassignment{}, fmt.Errorf("%s: owner token at line %d is out of range in a %d-line file", ExceptionsFile, e.line, len(fileLines))
		}
		col := e.col - 1
		line := fileLines[i]
		if col < 0 || col+len(AutoRecordedOwner) > len(line) || line[col:col+len(AutoRecordedOwner)] != AutoRecordedOwner {
			return Reassignment{}, fmt.Errorf("%s: line %d does not hold %q at column %d as parsed: %q", ExceptionsFile, e.line, AutoRecordedOwner, e.col, line)
		}
		fileLines[i] = line[:col] + replacement + line[col+len(AutoRecordedOwner):]
	}
	out := []byte(strings.Join(fileLines, "\n"))

	records := make([]ReassignedRecord, 0, len(edits))
	for _, e := range edits {
		records = append(records, e.record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Path != records[j].Path {
			return records[i].Path < records[j].Path
		}
		return records[i].RuleID < records[j].RuleID
	})

	if err := os.WriteFile(target, out, 0o644); err != nil {
		return Reassignment{}, fmt.Errorf("%s: %w", ExceptionsFile, err)
	}

	landed, err := LoadExceptions(repoRoot)
	if err != nil {
		return Reassignment{}, fmt.Errorf("%s: wrote an owner reassignment that no longer loads: %w", ExceptionsFile, err)
	}
	for _, list := range landed {
		for _, ex := range list {
			if ex.Owner == AutoRecordedOwner {
				return Reassignment{}, fmt.Errorf("%s: %s still records owner %q after reassignment", ExceptionsFile, ex.Path, AutoRecordedOwner)
			}
		}
	}

	return Reassignment{Owner: owner, Reassigned: len(records), Records: records}, nil
}

// exceptionMappings returns the mapping node for each exception a
// per-path value holds: the value itself when a path carries a single
// exception object, or one entry per element when it carries a list —
// exceptions.yaml's two legal shapes for a path's value (a path
// violating two rules at once needs one record per rule).
func exceptionMappings(value ast.Node) []*ast.MappingNode {
	switch v := value.(type) {
	case *ast.MappingNode:
		return []*ast.MappingNode{v}
	case *ast.MappingValueNode:
		// A single-entry mapping parses as a bare MappingValueNode
		// rather than a MappingNode wrapping one value.
		return []*ast.MappingNode{{BaseNode: v.BaseNode, Values: []*ast.MappingValueNode{v}}}
	case *ast.SequenceNode:
		var out []*ast.MappingNode
		for _, item := range v.Values {
			out = append(out, exceptionMappings(item)...)
		}
		return out
	}
	return nil
}

// mappingEntry returns the mapping-value node for key inside m, or nil.
func mappingEntry(m *ast.MappingNode, key string) *ast.MappingValueNode {
	for _, v := range m.Values {
		if keyString(v.Key) == key {
			return v
		}
	}
	return nil
}

// keyString decodes a mapping key node to the string it names.
func keyString(key ast.Node) string {
	if s, ok := key.(*ast.StringNode); ok {
		return s.Value
	}
	if tk := key.GetToken(); tk != nil {
		return tk.Value
	}
	return key.String()
}
