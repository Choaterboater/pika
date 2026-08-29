package contract

import (
	"fmt"

	"github.com/goccy/go-yaml/ast"
)

// checkDuplicateKeys walks the YAML AST and reports the first mapping key
// that repeats at the same mapping level, at any depth. goccy/go-yaml has no
// DisallowDuplicateKey decoder option, so the check is done structurally
// (the parser is run with AllowDuplicateMapKey so this helper is the
// authority). Kept as a small unexported helper so a later task can move it
// to a shared yamlx package without touching the loader.
func checkDuplicateKeys(node ast.Node) error {
	switch n := node.(type) {
	case nil:
		return nil
	case *ast.DocumentNode:
		return checkDuplicateKeys(n.Body)
	case *ast.MappingNode:
		seen := make(map[string]struct{}, len(n.Values))
		for _, v := range n.Values {
			key := v.Key.GetToken().Value
			if _, dup := seen[key]; dup {
				return fmt.Errorf("duplicate key %q", key)
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
	}
	return nil
}
