package version

import (
	"testing"

	"github.com/Choaterboater/pika/internal/profiles"
)

// The compatibility surface pinned as of the current release. Each value
// is the one a repository can observe from the outside: the digest a
// profiles.lock is verified against, and the newest contract schema a
// contract may declare.
//
// Updating these three lines together is the release step. They are
// literals rather than a computation because a pin that recomputes what
// it guards proves nothing.
const (
	pinnedVersion           = "0.5.0"
	pinnedRegistryDigest    = "f34a39847227902b0b36332796fddacdb4fdb07d03d5c8a8bcaed8c454f59e9e"
	pinnedMaxContractSchema = 1
)

// pika verifies repositories and had never verified itself: the version
// sat at 0.1.0 for 99 commits and four milestones while the pack
// registry rotated underneath it, so two binaries both calling
// themselves 0.1.0 disagreed about whether a correct repository passed
// gate 1, and nothing in the product could say which one was behind.
//
// This is the gate for that drift, and it is deliberately narrow. It
// does not ask for a version bump per commit — a rule nobody can obey is
// a rule that gets deleted. It fires on exactly the pair whose
// disagreement produced the failure: when the embedded pack registry
// digest moves, or the contract schema ceiling moves, the version must
// move with it, because from a repository's side that binary is no
// longer the same product. A commit that touches neither packs, nor
// templates, nor the schema never reaches these branches.
func TestCompatibilitySurfaceMovesWithTheVersion(t *testing.T) {
	digest := RegistryDigest()
	surfaceMoved := digest != pinnedRegistryDigest || MaxContractSchema != pinnedMaxContractSchema

	if surfaceMoved && Version == pinnedVersion {
		if digest != pinnedRegistryDigest {
			t.Errorf("the embedded pack registry digest moved from %s to %s while Version stayed at %s.\n"+
				"Every profiles.lock written by the previous build now disagrees with this one, so this binary is a different product to every adopted repository: bump Version, then update pinnedRegistryDigest and pinnedVersion in this file.",
				pinnedRegistryDigest, digest, Version)
		}
		if MaxContractSchema != pinnedMaxContractSchema {
			t.Errorf("MaxContractSchema moved from %d to %d while Version stayed at %s.\n"+
				"A contract written for the new ceiling is unreadable to the previous build: bump Version, then update pinnedMaxContractSchema and pinnedVersion in this file.",
				pinnedMaxContractSchema, MaxContractSchema, Version)
		}
		return
	}

	// The surface did not move, or the version already did. Either way
	// the pin must record what this build actually is, or the next pack
	// change is measured against a stale record and passes unbumped.
	if Version != pinnedVersion {
		t.Errorf("Version is %s but the pinned surface still records %s: update pinnedVersion (and pinnedRegistryDigest %s, pinnedMaxContractSchema %d) so the next pack or schema change is measured against this release.",
			Version, pinnedVersion, digest, MaxContractSchema)
	}
}

// The surface pika reports has to be the value gate 1 compares a
// repository's lock against, not a second copy of it that could be
// right about nothing.
func TestReportedRegistryDigestIsTheDigestTheLockIsCheckedAgainst(t *testing.T) {
	if got, want := Current().RegistryDigest, profiles.PackDigest(); got != want {
		t.Fatalf("Current().RegistryDigest = %s, want the embedded registry digest %s", got, want)
	}
	if Current().Version != Version || Current().MaxContractSchema != MaxContractSchema {
		t.Fatalf("Current() = %+v, want version %s and schema %d", Current(), Version, MaxContractSchema)
	}
}
