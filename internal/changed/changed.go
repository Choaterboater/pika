// Package changed resolves the set of repository-relative paths modified
// relative to the merge base, so `pika check --changed` can narrow the
// ladder. Degradation is always explicit: narrowing verification by
// accident is the one failure mode that lets a regression through, so
// every uncertain case falls back to running everything.
package changed

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/Choaterboater/pika/internal/repopath"
)

// Set is the resolved change set. Degraded means the set could not be
// computed and every gate must run.
type Set struct {
	Paths    []string `json:"paths"`
	Degraded bool     `json:"degraded"`
	Reason   string   `json:"reason,omitempty"`
}

// Empty reports whether nothing changed. A clean tree is not degraded: it
// is a real, trustworthy answer, and the caller may legitimately skip the
// package gates on it. A degraded set is never empty in this sense — the
// caller must run everything instead.
func (s *Set) Empty() bool { return !s.Degraded && len(s.Paths) == 0 }

// mergeBaseRefs are probed in order for something to diff against: the
// branch's own upstream first, then origin's default branch, then the
// conventional names.
var mergeBaseRefs = []string{"@{upstream}", "origin/HEAD", "origin/main", "origin/master"}

// Files computes the change set: everything differing from the merge base
// with the upstream default branch, plus staged, unstaged, and untracked
// changes. It returns an error only for a caller mistake; every
// environmental failure is reported as a degraded Set so the caller can
// warn and widen rather than silently narrow.
func Files(root *repopath.Root) (*Set, error) {
	if root == nil {
		return nil, errors.New("changed: nil repository root")
	}
	dir := root.Dir()
	if _, err := exec.LookPath("git"); err != nil {
		return degraded("git is not on PATH"), nil
	}
	if out, err := git(dir, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		return degraded("not inside a git work tree"), nil
	}
	if out, err := git(dir, "rev-parse", "--is-shallow-repository"); err == nil && strings.TrimSpace(out) == "true" {
		return degraded("shallow clone: no reliable merge base"), nil
	}

	seen := map[string]bool{}
	// Staged, unstaged, and untracked changes always count.
	for _, args := range [][]string{
		{"diff", "--name-only", "--no-renames"},
		{"diff", "--name-only", "--no-renames", "--cached"},
		{"ls-files", "--others", "--exclude-standard"},
	} {
		out, err := git(dir, args...)
		if err != nil {
			return degraded("git " + strings.Join(args, " ") + " failed"), nil
		}
		collect(seen, out)
	}
	// Committed changes since the merge base, when there is a branch to
	// fork from.
	ref, base, err := mergeBase(dir)
	if err != nil {
		return degraded(err.Error()), nil
	}
	if base != "" {
		out, err := git(dir, "diff", "--name-only", "--no-renames", base+"...HEAD")
		if err != nil {
			return degraded("git diff against the merge base with " + ref + " failed"), nil
		}
		collect(seen, out)
	}

	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return &Set{Paths: paths}, nil
}

// mergeBase finds the fork point against the first of mergeBaseRefs that
// resolves. A repository where none of them resolve — one branch, no
// remote — legitimately has nothing to fork from: that returns an empty
// base and no error, and the staged/working-tree diffs still apply. A ref
// that resolves but has no common ancestor with HEAD is the genuinely
// uncertain case (grafted or unrelated history) and degrades.
func mergeBase(dir string) (ref, base string, err error) {
	for _, candidate := range mergeBaseRefs {
		if _, e := git(dir, "rev-parse", "--verify", "--quiet", candidate); e != nil {
			continue
		}
		out, e := git(dir, "merge-base", "HEAD", candidate)
		if e != nil {
			return candidate, "", fmt.Errorf("no merge base with %s: unrelated or grafted history", candidate)
		}
		return candidate, strings.TrimSpace(out), nil
	}
	return "", "", nil
}

func collect(seen map[string]bool, out string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			seen[line] = true
		}
	}
}

func degraded(reason string) *Set {
	return &Set{Degraded: true, Reason: reason}
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}
