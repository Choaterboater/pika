package main

import (
	"os"

	"github.com/Choaterboater/pika/internal/repopath"
)

// rootFlagUsage is the one help string every command shows for --root, so
// the seven registrations cannot drift from each other.
const rootFlagUsage = "repository root (default: discovered from the working directory)"

// resolveRoot binds the repository root for one command invocation. An
// explicit --root bypasses discovery; otherwise the root is discovered by
// walking up from the working directory. Every command resolves exactly
// once, before doing any work, and threads the result as a parameter —
// the root is never a package global.
func resolveRoot(explicit string) (*repopath.Root, error) {
	if explicit != "" {
		return repopath.At(explicit)
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return repopath.Find(wd)
}
