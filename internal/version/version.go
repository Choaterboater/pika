// Package version exposes the semantic version of the projectctl binary
// and the contract schema versions it supports.
package version

import "fmt"

// Version is the semantic version of the projectctl binary.
// Overridden at build time via -ldflags.
var Version = "0.1.0"

// MaxContractSchema is the highest contract schema version this binary
// supports. Bump it when the embedded contract schema gains support for a
// newer contract schema version.
const MaxContractSchema = 1

// String returns the semantic version string.
func String() string {
	return Version
}

// Check reports whether the binary supports the given contract schema
// version. It errors when the schema is newer than MaxContractSchema;
// M1 has no lenient mode, so unsupported schemas are always fatal. The
// strict parameter from the Task 1 stub was dropped for that reason
// (see task 7 report).
func Check(schema int) error {
	if schema > MaxContractSchema {
		return fmt.Errorf("version: contract schema version %d is newer than the highest supported version %d; upgrade projectctl", schema, MaxContractSchema)
	}
	return nil
}
