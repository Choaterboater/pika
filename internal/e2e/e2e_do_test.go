// internal/e2e/e2e_do_test.go
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EDoRoutesToAdopt closes the ungoverned path through the real
// binary: `pika do` on a fresh checkout writes the same adoption drafts
// `pika adopt` itself would.
func TestE2EDoRoutesToAdopt(t *testing.T) {
	dir := t.TempDir()
	copyFixtureTree(t, "go-mod", dir)

	runCLI(t, dir, 0, "do")

	for _, p := range []string{".project/contract.yaml.draft", ".project/profiles.lock.draft"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("do did not dispatch to adopt: %s missing: %v", p, err)
		}
	}
}

// TestE2EDoRoutesToWork closes the governed-with-a-goal path: adopt then
// apply first (matching TestE2EAdoptApply's own setup at
// internal/e2e/e2e_apply_test.go:34-54), then a Git commit — required
// because the work lifecycle it dispatches to refuses before it starts
// on a tree that is not a repository at all (internal/improve/improve.go
// runs `git status --porcelain` before anything else), the same
// initGitRepo step every other e2e fixture exercising work/improve takes
// (internal/e2e/e2e_work_test.go:84). A freshly adopted contract
// configures no builder agent, so the lifecycle still creates the
// branch and record (proving `work`, not `improve`, ran — a repair run
// would have stopped silently on the green-or-red baseline with no
// branch at all, per internal/improve/improve.go:667-687) and then
// stops exactly where the builder would be invoked, printed by
// printRunResult's exact "stopped on branch" wording
// (cmd/pika/improve.go:239) — the identical outcome
// TestWorkCarriesTheGoalIntoTheHandoffPrompt already gets from the
// equivalent in-process fixture (cmd/pika/work_test.go:126-128).
func TestE2EDoRoutesToWork(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	dir := t.TempDir()
	copyFixtureTree(t, "go-mod", dir)
	runCLI(t, dir, 0, "adopt")
	runCLI(t, dir, 0, "apply")
	initGitRepo(t, dir)

	out := runCLI(t, dir, 1, "do", "add a health check endpoint")
	if !strings.Contains(out, "stopped on branch") {
		t.Fatalf("do with a goal did not appear to run the work lifecycle:\n%s", out)
	}
}
