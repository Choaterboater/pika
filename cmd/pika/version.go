package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/version"
)

// versionResult is the `pika version` payload: this binary's
// compatibility surface, plus the corresponding number recorded in the
// repository's lock when there is one to read.
//
// The lock section exists because of what the version is for. Two pika
// builds that both printed "0.1.0" disagreed about whether a correct
// repository passed gate 1, and neither could be attributed. The
// registry digest attributes them; the lock's digest beside it answers
// the next question immediately — "did this binary write this lock, or
// did some other one" — which is the comparison a stale-binary
// diagnosis turns on.
type versionResult struct {
	version.Build
	Lock *lockIdentity `json:"lock,omitempty"`
}

// lockIdentity is the registry digest a repository's profiles.lock was
// written with. Matches is arithmetic, not a verdict: it says the two
// numbers are equal or not, and says nothing about which side is stale.
// `pika check` and `pika doctor` are the commands that judge.
type lockIdentity struct {
	Path           string `json:"path"`
	RegistryDigest string `json:"registry_digest"`
	Matches        bool   `json:"matches"`
}

// runVersion implements `pika version [--json] [--root <dir>]`.
//
// It reports the release, the embedded pack registry digest, and the
// contract schema ceiling — the three values that decide whether this
// binary and some repository agree. The release alone identified nothing:
// it sat at 0.1.0 across four milestones of pack changes, so `pika
// version` could not tell two mutually incompatible builds apart, which
// is exactly what an operator holding a failing lock needs it for.
//
// It never fails on repository state. A repository that cannot be read
// simply contributes no lock line: version answers a question about the
// binary, and a binary can be identified anywhere.
//
// Exit codes: 0 always, 2 on a usage error.
func runVersion(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit the build identity as JSON on stdout")
	rootFlag := fs.String("root", "", rootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return fail(*jsonOut, stdout, stderr, "version", codeUsage,
			fmt.Sprintf("unexpected argument %q (usage: pika version [--json] [--root <dir>])", fs.Arg(0)))
	}

	result := versionResult{Build: version.Current(), Lock: lockedRegistry(*rootFlag)}

	if *jsonOut {
		if !emitJSON(stdout, stderr, "version", true, result) {
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "pika %s\n", result.Version)
	fmt.Fprintf(stdout, "pack registry:   %s\n", result.RegistryDigest)
	fmt.Fprintf(stdout, "contract schema: %d (highest supported)\n", result.MaxContractSchema)
	if result.Lock != nil {
		state := "differs from this binary"
		if result.Lock.Matches {
			state = "matches this binary"
		}
		fmt.Fprintf(stdout, "%s: %s (%s)\n", result.Lock.Path, result.Lock.RegistryDigest, state)
	}
	return 0
}

// lockedRegistry reads the registry digest recorded in the repository's
// profiles.lock, or nil when there is no readable lock to report. Every
// failure is silent by design: standing outside a project is not an
// error for this command, and a lock that cannot be parsed is a finding
// `pika check` and `pika doctor` own — reporting it twice, in two
// implementations, is how they come to disagree.
func lockedRegistry(rootFlag string) *lockIdentity {
	root, err := resolveRoot(rootFlag)
	if err != nil {
		return nil
	}
	lock, err := profiles.ReadLock(root.Lock())
	if err != nil {
		return nil
	}
	return &lockIdentity{
		Path:           filepath.ToSlash(root.Lock()),
		RegistryDigest: lock.Digest,
		Matches:        lock.Digest == version.RegistryDigest(),
	}
}
