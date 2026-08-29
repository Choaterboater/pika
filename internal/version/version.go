// Package version exposes the semantic version of the projectctl binary.
package version

// Version is the semantic version of the projectctl binary.
// Overridden at build time via -ldflags.
var Version = "0.1.0"

// String returns the semantic version string.
func String() string {
	return Version
}

// Check reports whether the binary supports the features it is being asked
// to use. It is a stub in Task 1; later tasks wire contract-schema support
// checks into it (Check fails if a contract schema is newer than supported).
func Check(strict bool) error {
	return nil
}
