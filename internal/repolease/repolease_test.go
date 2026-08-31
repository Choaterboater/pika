package repolease

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/lease"
	"github.com/Choaterboater/pika/internal/repopath"
)

func at(t *testing.T) *repopath.Root {
	t.Helper()
	root, err := repopath.At(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// The whole cross-domain design in one assertion: the run lease is the
// scope lease on ".", so the rule that already made src conflict with
// src/pkg is the rule that makes a run conflict with everything. If this
// stops holding, the two exclusions have quietly become independent
// again.
func TestRunScopeCoversEveryScope(t *testing.T) {
	for _, scope := range []string{".", "src", "src/pkg", "docs/guides/deep", ".project/state"} {
		if !overlap(RunScope, scope) {
			t.Errorf("overlap(%q, %q) = false, want the run lease to cover it", RunScope, scope)
		}
		if !overlap(scope, RunScope) {
			t.Errorf("overlap(%q, %q) = false, want the exclusion to run both ways", scope, RunScope)
		}
	}
}

func TestOverlapIsSubtreeContainmentInBothDirections(t *testing.T) {
	overlapping := [][2]string{
		{"src", "src"},
		{"src", "src/pkg"},
		{"src/pkg", "src"},
		{"docs", "docs/guides/usage.md"},
	}
	for _, pair := range overlapping {
		if !overlap(pair[0], pair[1]) {
			t.Errorf("overlap(%q, %q) = false, want true", pair[0], pair[1])
		}
	}
	// A shared prefix is not containment. "src" must not exclude
	// "srcgen": an exclusion that swallowed sibling directories would
	// refuse work it has no claim over.
	disjoint := [][2]string{
		{"src", "srcgen"},
		{"src", "docs"},
		{"src/pkg", "src/other"},
	}
	for _, pair := range disjoint {
		if overlap(pair[0], pair[1]) {
			t.Errorf("overlap(%q, %q) = true, want two unrelated paths to be leasable at once", pair[0], pair[1])
		}
	}
}

// The encoding has to be reversible, because the conflict scan reads the
// leased paths back out of a directory listing. A name that did not
// round-trip would be a lease nothing could name.
func TestScopeLockNameRoundTrips(t *testing.T) {
	for _, rel := range []string{".", "src", "docs/guides", "a b/c", "ünïcødé/文書", "x.y-z_1"} {
		name, err := scopeLockName(rel)
		if err != nil {
			t.Fatalf("scopeLockName(%q): %v", rel, err)
		}
		got, ok := ScopeFromLockName(name)
		if !ok || got != rel {
			t.Errorf("ScopeFromLockName(%q) = %q, %v; want %q", name, got, ok, rel)
		}
	}
}

// The lock directory is an ordinary directory. A stray file in it names
// no scope, and reading one as a lease would invent a holder.
func TestStrayFilesNameNoScope(t *testing.T) {
	for _, name := range []string{"README", "src", "notes.txt", "%ZZ.lock", "trailing%.lock"} {
		if scope, ok := ScopeFromLockName(name); ok {
			t.Errorf("ScopeFromLockName(%q) = %q, true; want it refused", name, scope)
		}
	}
}

func TestAnOverlongScopeIsRefusedAsABadArgument(t *testing.T) {
	_, err := TakeScope(at(t), strings.Repeat("a", maxScopeLockName+1))
	if !errors.Is(err, ErrScopeNameTooLong) {
		t.Fatalf("error = %v, want ErrScopeNameTooLong so the caller can answer invalid_params", err)
	}
}

// The two exclusions refuse each other, in both directions, and a
// refusal never leaves a claim behind. A lease file for ground nobody
// was granted would wedge the repository exactly as a crash does.
func TestTheTwoExclusionsRefuseEachOther(t *testing.T) {
	root := at(t)
	run, err := TakeRun(root, "20260830-feature-c0ffee01")
	if err != nil {
		t.Fatal(err)
	}

	_, err = TakeScope(root, "src")
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("TakeScope while a run holds the repository = %v, want a ConflictError", err)
	}
	if conflict.Held.Kind != KindRun || !strings.Contains(conflict.Error(), "20260830-feature-c0ffee01") {
		t.Errorf("conflict = %q, want it to name the run holding the repository", conflict)
	}
	if _, err := os.Stat(filepath.Join(ScopeLocks(root), "src.lock")); !os.IsNotExist(err) {
		t.Fatalf("the refused acquire left a lease behind (stat err %v)", err)
	}

	// The other direction, from a clean start.
	if err := run.Release(); err != nil {
		t.Fatal(err)
	}
	scope, err := TakeScope(root, "src")
	if err != nil {
		t.Fatal(err)
	}
	_, err = TakeRun(root, "20260830-feature-deadbeef")
	if !errors.As(err, &conflict) {
		t.Fatalf("TakeRun while a scope is leased = %v, want a ConflictError", err)
	}
	if conflict.Held.Kind != KindScope || conflict.Held.Scope != "src" {
		t.Errorf("conflict = %+v, want it to name the scope lease on src", conflict.Held)
	}
	dir, name := RunLock(root)
	if info, state, err := lease.Inspect(dir, name); err != nil || state != lease.StateFree {
		t.Fatalf("run lease = %+v state = %v err = %v, want the refused run to have given its claim back", info, state, err)
	}
	if err := scope.Release(); err != nil {
		t.Fatal(err)
	}
}

// Two scopes that cannot collide are both grantable. An exclusion that
// refused unrelated paths would be a global lock wearing a path
// argument.
func TestDisjointScopesAreBothGranted(t *testing.T) {
	root := at(t)
	src, err := TakeScope(root, "src")
	if err != nil {
		t.Fatal(err)
	}
	docs, err := TakeScope(root, "docs")
	if err != nil {
		t.Fatalf("TakeScope(docs) while src is leased: %v", err)
	}
	if err := src.Release(); err != nil {
		t.Fatal(err)
	}
	if err := docs.Release(); err != nil {
		t.Fatal(err)
	}
}

// Scan is what `pika recover` and `pika doctor` both read the repository
// through, so it has to list the run lease first, name every scope it
// finds, and ignore anything it did not write.
func TestScanListsEveryHolderAndIgnoresStrayFiles(t *testing.T) {
	root := at(t)
	if held, err := Scan(root); err != nil || len(held) != 0 {
		t.Fatalf("Scan of a repository that has run nothing = %v, %v; want no holders and no error", held, err)
	}

	run, err := TakeRun(root, "20260830-feature-c0ffee01")
	if err != nil {
		t.Fatal(err)
	}
	// Taken directly, because a run holds the repository and TakeScope
	// would rightly refuse: this stands the two of them up side by side
	// to prove Scan reports both.
	name, err := scopeLockName("docs/guides")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ScopeLocks(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Acquire(ScopeLocks(root), name, lease.Info{ID: "scope:docs/guides#1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ScopeLocks(root), "README"), []byte("not a lease\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	held, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 2 {
		t.Fatalf("Scan = %+v, want exactly the run lease and the scope lease", held)
	}
	if held[0].Kind != KindRun || held[0].Scope != RunScope {
		t.Errorf("Scan[0] = %+v, want the run lease first", held[0])
	}
	if held[1].Kind != KindScope || held[1].Scope != "docs/guides" {
		t.Errorf("Scan[1] = %+v, want the scope named, not its lock file", held[1])
	}
	for _, h := range held {
		if h.State != lease.StateHeld {
			t.Errorf("%s state = %v, want held: this process is the holder", h.Path, h.State)
		}
		if !strings.Contains(h.Who(), h.Info.Host) || !strings.Contains(h.Describe(), h.Info.ID) {
			t.Errorf("%s renders as %q / %q, want the holder named", h.Path, h.Who(), h.Describe())
		}
	}
	if err := run.Release(); err != nil {
		t.Fatal(err)
	}
}
