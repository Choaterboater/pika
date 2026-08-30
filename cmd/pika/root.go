package main

import (
	"os"

	"github.com/Choaterboater/pika/internal/repopath"
)

// rootFlagUsage is the help string the six commands that operate on an
// existing project share, so their registrations cannot drift.
const rootFlagUsage = "repository root (default: discovered from the working directory)"

// targetRootFlagUsage is init's. init creates rather than discovers, so
// --root names the directory to scaffold, not a root to look for.
const targetRootFlagUsage = "directory to scaffold (default: the working directory)"

// resolveRoot binds the repository root for one invocation of a command
// that operates on an existing project. An explicit --root bypasses
// discovery; otherwise the root is discovered by walking up from the
// working directory. Every command resolves exactly once, before doing
// any work, and threads the result as a parameter — the root is never a
// package global.
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

// targetRoot binds the directory a command creates a project in. It never
// discovers: discovery answers "which existing project am I operating
// on", and init has no project yet. Walking up would stop at an
// enclosing checkout's .git marker, so `mkdir my-service && cd my-service
// && pika init` would scaffold at the repository root and still report
// success — wrong place, exit 0.
func targetRoot(explicit string) (*repopath.Root, error) {
	dir := explicit
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		dir = wd
	}
	return repopath.At(dir)
}
