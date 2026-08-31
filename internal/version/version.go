// Package version exposes the identity of the pika binary: the release
// it claims, the profile pack registry compiled into it, and the
// contract schema versions it supports.
//
// Those three values are one surface, not three facts that happen to
// live together. A repository's .project/profiles.lock is written by one
// binary and verified by another, and the only thing that decides
// whether they agree is the pack registry digest — so a build that
// carries different packs is a different build, whatever it calls
// itself. Keeping the digest here, beside the version, is what lets
// `pika version` answer "which pika is this" instead of "which release
// was this branched from".
package version

import (
	"fmt"

	"github.com/Choaterboater/pika/internal/profiles"
)

// Version is the semantic version of the pika binary.
//
// It is a constant. It was a var carrying a comment that promised it was
// "overridden at build time via -ldflags", and nothing in this
// repository, its CI workflow, or any install path ever passed those
// flags: every build since M1 printed 0.1.0 through four milestones. A
// stamp is also absent under `go install`, `go run` and `go build`,
// which is how pika is actually built, so the mechanism would have been
// wrong exactly where it mattered. Changing this line is the release.
const Version = "0.5.2"

// MaxContractSchema is the highest contract schema version this binary
// supports. Bump it when the embedded contract schema gains support for a
// newer contract schema version.
const MaxContractSchema = 1

// String returns the semantic version string.
func String() string {
	return Version
}

// RegistryDigest returns the digest of the profile pack registry
// embedded in this binary — the exact value gate 1 compares a
// repository's profiles.lock against. It is read from the registry
// itself rather than restated here, so it cannot drift from what the
// gate compares.
func RegistryDigest() string {
	return profiles.PackDigest()
}

// Build is one pika binary's compatibility surface: the release it
// claims, the pack registry it carries, and the newest contract schema
// it can read. Two builds that disagree about any of the three will
// disagree about some repository, which is why `pika version` prints all
// three rather than the release alone.
type Build struct {
	Version           string `json:"version"`
	RegistryDigest    string `json:"registry_digest"`
	MaxContractSchema int    `json:"max_contract_schema"`
}

// Current returns this binary's compatibility surface.
func Current() Build {
	return Build{
		Version:           Version,
		RegistryDigest:    RegistryDigest(),
		MaxContractSchema: MaxContractSchema,
	}
}

// Check reports whether the binary supports the given contract schema
// version. It errors when the schema is newer than MaxContractSchema;
// M1 has no lenient mode, so unsupported schemas are always fatal. The
// strict parameter from the Task 1 stub was dropped for that reason
// (see task 7 report).
func Check(schema int) error {
	if schema > MaxContractSchema {
		return fmt.Errorf("version: contract schema version %d is newer than the highest supported version %d; upgrade pika", schema, MaxContractSchema)
	}
	return nil
}
