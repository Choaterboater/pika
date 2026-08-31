package improve

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Choaterboater/pika/internal/evidence"
	"github.com/Choaterboater/pika/internal/lease"
	"github.com/Choaterboater/pika/internal/mcp"
	"github.com/Choaterboater/pika/internal/repolease"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/workrec"
)

func TestRunCommitsOnlyAfterVerifiedRecheck(t *testing.T) {
	root := fixtureRepository(t)
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail, OutputTail: "repair this"}}},
		{Pass: true, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusPass}}},
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: repairRunner{path: "fixed.txt", body: "verified fix\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ChecksBefore.Pass || !result.ChecksAfter.Pass {
		t.Fatalf("checks before=%+v after=%+v, want failing baseline and passing recheck", result.ChecksBefore, result.ChecksAfter)
	}
	if result.Branch != "chore/pika-improve" || result.Commit == "" {
		t.Fatalf("result = %+v, want branch and commit", result)
	}
	if got := gitOutput(t, root, "branch", "--show-current"); got != "chore/pika-improve" {
		t.Fatalf("branch = %q, want chore/pika-improve", got)
	}
	if got := gitOutput(t, root, "show", "--format=%s", "--no-patch", "HEAD"); got != "chore: improve verified findings" {
		t.Fatalf("commit subject = %q", got)
	}
	// The verified commit leaves exactly one thing behind: the receipt
	// the kernel issued for it. A receipt cannot be inside the commit it
	// attests, and `.project/evidence` is committed content rather than
	// ignored local state, so it is the run's one uncommitted artifact.
	want := "?? .project/evidence/" + result.WorkID + ".json"
	if got := gitOutput(t, root, "status", "--porcelain", "--untracked-files=all"); got != want {
		t.Fatalf("status = %q, want only the run's receipt %q", got, want)
	}
}

func TestRunGreenBaselineDoesNotRequireAgentOrCreateBranch(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			return &verify.Report{Pass: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Branch != "" || result.Commit != "" || result.Handoff.Dir != "" {
		t.Fatalf("result = %+v, want no branch, handoff, or commit", result)
	}
	if got := gitOutput(t, root, "branch", "--show-current"); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
}

func TestRunRefusesDirtyTreeBeforeChecks(t *testing.T) {
	root := fixtureRepository(t)
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			t.Fatal("checks must not run on a dirty tree")
			return nil, nil
		},
	})
	if !errors.Is(err, ErrDirtyTree) {
		t.Fatalf("error = %v, want ErrDirtyTree", err)
	}
}

func TestRunLeavesFailedRecheckUncommitted(t *testing.T) {
	root := fixtureRepository(t)
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}},
		{Pass: false, Gates: []verify.GateResult{{ID: "test", Status: verify.StatusFail}}},
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: repairRunner{path: "fixed.txt", body: "needs review\n"}},
	})
	if err == nil || !strings.Contains(err.Error(), "post-handoff checks failed") {
		t.Fatalf("error = %v, want failed recheck", err)
	}
	if result.Commit != "" || result.Branch != "chore/pika-improve" {
		t.Fatalf("result = %+v, want branch without commit", result)
	}
	if got := gitOutput(t, root, "status", "--porcelain"); !strings.Contains(got, "fixed.txt") {
		t.Fatalf("status = %q, want uncommitted agent edit", got)
	}
}

func TestRunRejectsAgentCreatedCommitBeforeRecheck(t *testing.T) {
	root := fixtureRepository(t)
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}},
		{Pass: true},
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: committingRunner{}},
	})
	if err == nil || !strings.Contains(err.Error(), "changed Git state") {
		t.Fatalf("error = %v, want agent commit refusal", err)
	}
	if result.ChecksAfter != nil {
		t.Fatalf("post-handoff checks ran after agent commit: %+v", result.ChecksAfter)
	}
}

func TestRunRejectsAgentBranchSwitchBeforeRecheck(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			return &verify.Report{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}}, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: switchingRunner{}},
	})
	if err == nil || !strings.Contains(err.Error(), "Git state") {
		t.Fatalf("error = %v, want branch-switch refusal", err)
	}
	if result.Branch != "chore/pika-improve" {
		t.Fatalf("result branch = %q", result.Branch)
	}
}

func TestRunRejectsAgentRewriteOfAnotherBranch(t *testing.T) {
	root := fixtureRepository(t)
	gitRun(t, root, "commit", "--allow-empty", "-qm", "second")
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			return &verify.Report{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}}, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: rewritingRunner{}},
	})
	if err == nil || !strings.Contains(err.Error(), "Git state") {
		t.Fatalf("error = %v, want ref-rewrite refusal", err)
	}
	if result.ChecksAfter != nil {
		t.Fatalf("post-handoff checks ran after ref rewrite: %+v", result.ChecksAfter)
	}
}

func TestRunRejectsPendingMergeState(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			return &verify.Report{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}}, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: pendingMergeRunner{}},
	})
	if err == nil || !strings.Contains(err.Error(), "pending Git operation") {
		t.Fatalf("error = %v, want pending merge refusal", err)
	}
	if result.ChecksAfter != nil {
		t.Fatalf("post-handoff checks ran with pending merge: %+v", result.ChecksAfter)
	}
}

// The bundle moved out of `.project/state/handoffs` and into the run
// record, so a filter naming the retired directory stopped covering it.
// The fixture here deliberately does NOT gitignore `.project/state`: once
// it is ignored, Git never offers the record or the bundle to a commit at
// all, and this test would pass no matter what changePaths filtered. An
// un-ignored state directory is the only world in which the filter is the
// thing standing between Pika's private state and the commit.
func TestRunDoesNotCommitAgentStagedPrivateState(t *testing.T) {
	root := fixtureRepositoryWithoutStateIgnore(t)
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}},
		{Pass: true},
	}
	runner := &stagingRunner{}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: runner},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Without this the test could keep passing against the retired path
	// while the real bundle went uncovered.
	wantPrefix := ".project/state/work/" + result.WorkID + "/handoff/"
	if !strings.HasPrefix(runner.staged, wantPrefix) {
		t.Fatalf("agent staged %q, want a path under %q: the filter is only proven at the bundle's real location", runner.staged, wantPrefix)
	}
	files := gitOutput(t, root, "show", "--format=", "--name-only", "HEAD")
	if strings.Contains(files, ".project/state") || !strings.Contains(files, "fixed.txt") {
		t.Fatalf("committed files = %q, want fixed.txt without private state", files)
	}
	for _, path := range result.ChangedFiles {
		if strings.HasPrefix(path, ".project/state") {
			t.Fatalf("changed files = %v, want nothing under .project/state", result.ChangedFiles)
		}
	}
}

// The force-add above is not the only way private state reaches a commit.
// `git status --porcelain --untracked-files=all` reports a staged rename
// on one line naming both sides — rename detection is on by default, no
// -M needed — so an agent that runs
//
//	git mv .project/state/work/seed/record.json leaked.json
//
// hands the filter a destination it has no reason to reject. Nothing
// before Pika's own `git add` catches it either: createHandoff compares
// HEAD, the branch and the refs, never the index.
//
// What that commits is not hypothetical. `.project/state` holds the
// handoff prompt, the pre-run check report, and — until CreateHandoff's
// deferred cleanup — the raw unredacted final message.
//
// The fixture tracks the private file, because Git only reports a rename
// for a path it is already tracking, and it does not ignore
// `.project/state`, for the reason the force-add test above gives.
func TestRunRefusesPrivateStateRenamedOutOfTheSubtree(t *testing.T) {
	const secret = "UNREDACTED-TRANSCRIPT-c0ffee"
	const private = ".project/state/work/seed/record.json"
	root := fixtureRepositoryWithTrackedPrivateState(t, private, secret+"\n")
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}},
		{Pass: true},
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: renamingRunner{from: private, to: "leaked.json"}},
	})
	if !errors.Is(err, ErrPrivateStateMoved) {
		t.Fatalf("error = %v, want ErrPrivateStateMoved", err)
	}
	if err != nil && !strings.Contains(err.Error(), private) {
		t.Errorf("refusal = %v, want it to name %s", err, private)
	}
	if result.Commit != "" {
		t.Errorf("result.Commit = %q, want no commit", result.Commit)
	}
	for _, path := range result.ChangedFiles {
		if path == "leaked.json" {
			t.Errorf("changed files = %v, want the smuggled path refused, not listed", result.ChangedFiles)
		}
	}

	// The guarantee is about what is in the repository, so read it from
	// the repository. Every committed path outside `.project/state` must
	// be free of the private content, whatever the run returned.
	head := gitOutput(t, root, "rev-parse", "--abbrev-ref", "HEAD")
	tracked := gitOutput(t, root, "ls-tree", "-r", "--name-only", "HEAD")
	for _, path := range strings.Split(tracked, "\n") {
		if path == "" || strings.HasPrefix(path, ".project/state/") {
			continue
		}
		if body := gitOutput(t, root, "show", "HEAD:"+path); strings.Contains(body, secret) {
			t.Fatalf("%s on branch %s committed Pika's private state", path, head)
		}
	}
	if strings.Contains(tracked, "leaked.json") {
		t.Fatalf("committed tree = %q, want the rename destination uncommitted", tracked)
	}
}

// The same rename, on a file Git has to quote.
//
// `core.quotePath` is on by default, so any path holding a non-ASCII
// byte, whitespace or a control character reaches the parser C-quoted:
// `.project/state/work/seed/wéird.json` arrives as the literal
// `".project/state/work/seed/w\303\251ird.json"`, leading ASCII
// double-quote included. `isPrivateState` is a prefix test against
// `.project/state`, which that literal does not satisfy, so
// privateStateMoved does not refuse and changePaths does not drop: both
// guards fail open on the one input that was shaped to defeat them.
//
// The path is not exotic. Any repository whose state directory holds an
// accented, spaced or otherwise non-ASCII filename has one, and a guard
// that protects only the paths an operator happened to name in ASCII is
// not a guard. The fixture proves Git really quotes before it asserts
// anything, so a pass here can never come from an ASCII path that was
// never the hole.
func TestPrivateStateWithANonASCIINameIsRefused(t *testing.T) {
	const secret = "UNREDACTED-TRANSCRIPT-c0ffee"
	const private = ".project/state/work/seed/wéird.json"
	root := fixtureRepositoryWithTrackedPrivateState(t, private, secret+"\n")
	if listed := gitOutput(t, root, "ls-files", "--", ".project"); !strings.HasPrefix(listed, `"`) {
		t.Fatalf("git lists %s as %s, unquoted: this fixture does not reproduce the quoting hole", private, listed)
	}

	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}},
		{Pass: true},
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: renamingRunner{from: private, to: "leaked.json"}},
	})
	if !errors.Is(err, ErrPrivateStateMoved) {
		t.Fatalf("error = %v, want ErrPrivateStateMoved\nstatus after the run:\n%s",
			err, gitOutput(t, root, "status", "--porcelain", "--untracked-files=all"))
	}
	if !strings.Contains(err.Error(), private) {
		t.Errorf("refusal = %v, want it to name %s verbatim, unquoted", err, private)
	}
	if result.Commit != "" {
		t.Errorf("result.Commit = %q, want no commit", result.Commit)
	}
	for _, path := range result.ChangedFiles {
		if strings.Contains(path, `\303`) || strings.HasPrefix(path, `"`) {
			t.Errorf("changed files = %v, want verbatim paths, not Git's quoting", result.ChangedFiles)
		}
	}

	head := gitOutput(t, root, "rev-parse", "--abbrev-ref", "HEAD")
	tracked := gitOutput(t, root, "ls-tree", "-r", "--name-only", "HEAD")
	for _, path := range strings.Split(tracked, "\n") {
		if path == "" || strings.HasPrefix(strings.Trim(path, `"`), ".project/state/") {
			continue
		}
		if body := gitOutput(t, root, "show", "HEAD:"+strings.Trim(path, `"`)); strings.Contains(body, secret) {
			t.Fatalf("%s on branch %s committed Pika's private state", path, head)
		}
	}
	if strings.Contains(tracked, "leaked.json") {
		t.Fatalf("committed tree = %q, want the rename destination uncommitted", tracked)
	}
}

// Refusing a quoted path is only half of it: the ordinary non-ASCII file
// an agent legitimately repairs has to reach the commit. A path read
// back quoted is not a path — `git add` matches nothing against the
// literal `"caf\303\251 fix.txt"` — so a filter that let it through
// would trade a silent leak for a run that dies on its own pathspec, and
// the receipt would attest a file the commit does not contain.
func TestRunCommitsANonASCIIPathVerbatim(t *testing.T) {
	const repaired = "café fix.txt"
	root := fixtureRepository(t)
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}},
		{Pass: true},
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: repairRunner{path: repaired, body: "verified fix\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.ChangedFiles, ",") != repaired {
		t.Fatalf("ChangedFiles = %q, want exactly [%q]", result.ChangedFiles, repaired)
	}
	// `git show` quotes on the way out too, so the assertion reads the
	// commit with quoting off rather than comparing against Git's escape
	// of the name this run is about.
	if got := gitOutput(t, root, "-c", "core.quotePath=false", "show", "--format=", "--name-only", "HEAD"); got != repaired {
		t.Fatalf("committed files = %q, want %q", got, repaired)
	}
	// The receipt reads its own file list back out of Git, so it is the
	// second reader of a quotable path and is asserted separately.
	receipt, _ := readReceipt(t, root, result.WorkID)
	if len(receipt.ChangedFiles) != 1 || receipt.ChangedFiles[0].Path != repaired {
		t.Fatalf("receipt changed files = %+v, want exactly %q", receipt.ChangedFiles, repaired)
	}
}

// A verbatim path still is not a literal one. `git add` reads what it is
// handed as PATHSPECS, so the last gate before `git commit` is pattern
// matching rather than naming, and in a pathspec `*` matches `/` as well
// as everything else. A file an agent leaves behind named
// `.project/stat*` is therefore a pattern covering the whole of
// `.project/state` — the run record, the handoff bundle inside it, the
// envelope, the board — and every one of those is a path changePaths had
// already dropped from this commit. The filter runs, and the command
// meant to enforce it puts them back.
//
// It is the defect `-z` closed in the status parser, one line later and
// in the other direction: there Git handed Pika a quoted string where it
// expected a path, here Pika hands Git a path where Git expects a
// pattern. It lands on the one commit whose entire promise is that it
// contains only what the ladder verified, built out of a working tree an
// agent has just been editing.
//
// Windows cannot hold this filename at all — `*` is not a legal
// character there — so on Windows the input is unreachable rather than
// unguarded.
func TestRunStagesGlobMetacharacterPathsLiterally(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows filename cannot contain `*`, so this pathspec cannot exist there")
	}
	// The fixture does not ignore `.project/state`, for the reason
	// TestRunDoesNotCommitAgentStagedPrivateState gives: once Git ignores
	// it, Git never offers those paths to a commit and this test would
	// pass however the pathspec were matched.
	const repaired = ".project/stat*"
	root := fixtureRepositoryWithoutStateIgnore(t)
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}},
		{Pass: true},
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: repairRunner{path: repaired, body: "verified fix\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Excluding private state proves nothing if there was none to
	// exclude: the record is what the over-matching pathspec reaches.
	record := filepath.Join(root, filepath.FromSlash(privateStateDir), "work", result.WorkID, "record.json")
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("run record: %v: the pathspec had nothing to over-match", err)
	}
	if strings.Join(result.ChangedFiles, ",") != repaired {
		t.Fatalf("ChangedFiles = %q, want exactly [%q]", result.ChangedFiles, repaired)
	}
	committed := gitOutput(t, root, "-c", "core.quotePath=false", "show", "--format=", "--name-only", "HEAD")
	if committed != repaired {
		t.Fatalf("committed files = %q, want exactly %q: the pathspec matched more than it named", committed, repaired)
	}
}

// The lifecycle above never sees the `R ` record itself: Pika resets the
// index before it reads status, which turns a staged rename into a
// worktree deletion plus an untracked destination. The rename record is
// still what Git reports for a staged rename anywhere else, and the two
// shapes are one event, so both are pinned here against literal `-z`
// porcelain — including the record the reproduction produced.
//
// `-z` inverts what a rename looks like. Where the human-readable format
// writes
//
//	R  .project/state/work/x/record.json -> tracked/leaked.json
//
// `-z` drops the arrow, reverses the two fields and NUL-terminates each
// one, so the same rename is
//
//	R  tracked/leaked.json\0.project/state/work/x/record.json\0
//
// with the origin arriving after the destination it moved to. The
// fixtures below are the real bytes, not arrow-joined strings, because
// an arrow-joined string is exactly the input this parser no longer
// accepts.
func TestPrivateStateMovedReadsBothSidesOfARename(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		want   string
	}{
		{"staged rename out of the subtree",
			"R  tracked/leaked.json\x00.project/state/work/x/record.json\x00",
			".project/state/work/x/record.json"},
		{"the same rename after the index reset",
			" D .project/state/work/x/record.json\x00?? tracked/leaked.json\x00",
			".project/state/work/x/record.json"},
		{"staged rename into the subtree",
			"R  .project/state/hidden.go\x00src/app.go\x00",
			".project/state/hidden.go"},
		{"a rename Pika has no business refusing",
			"R  src/new.go\x00src/old.go\x00?? fixed.txt\x00",
			""},
		{"private state merely present is dropped, not refused",
			"?? .project/state/work/x/record.json\x00 M README.md\x00",
			""},
		{"a path Git would have quoted arrives verbatim and is refused",
			" D .project/state/work/x/wéird.json\x00?? tracked/leaked.json\x00",
			".project/state/work/x/wéird.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := statusEntries(tc.status)
			if err != nil {
				t.Fatalf("statusEntries(%q): %v", tc.status, err)
			}
			if got := privateStateMoved(entries); got != tc.want {
				t.Fatalf("privateStateMoved(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// A rename Pika does allow has to commit as a rename. Staging only the
// destination leaves the origin behind, so the commit would carry the
// file's content at both paths. Both are staged in the order `-z`
// reports them — destination, then origin.
func TestChangePathsStagesBothSidesOfAnAllowedRename(t *testing.T) {
	entries, err := statusEntries("R  src/new.go\x00src/old.go\x00?? fixed.txt\x00?? .project/state/work/x/record.json\x00")
	if err != nil {
		t.Fatalf("statusEntries: %v", err)
	}
	got := changePaths(entries)
	want := []string{"src/new.go", "src/old.go", "fixed.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("changePaths = %v, want %v", got, want)
	}
}

// A record the parser cannot read refuses the run. Skipping it is how
// the quoting hole leaked in the first place: every caller of this
// parser is a guard, and a guard that drops what it did not understand
// opens on exactly the input that confused it.
func TestStatusEntriesRefuseAMalformedRecord(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
	}{
		{"truncated record", "?? \x00"},
		{"no separator after the status columns", "??x.project/state/work/x/record.json\x00"},
		{"a rename whose origin field never arrived", "R  tracked/leaked.json\x00"},
		{"a rename whose origin field is empty", "R  tracked/leaked.json\x00\x00"},
		{"a stray diagnostic between records", "?? fixed.txt\x00warning: something\x00"},
		{"an empty record", "\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := statusEntries(tc.status)
			if err == nil {
				t.Fatalf("statusEntries(%q) = %v, want a refusal", tc.status, entries)
			}
		})
	}
}

// A clean tree is not a malformed one: `git status -z` prints nothing at
// all for it, and the terminator of a real output's last record must not
// be read as a truncated field either.
func TestStatusEntriesAcceptACleanTree(t *testing.T) {
	entries, err := statusEntries("")
	if err != nil {
		t.Fatalf("statusEntries(\"\"): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("statusEntries(\"\") = %v, want no entries", entries)
	}
}

// A run that is interrupted is only recoverable if every transition
// reached the disk before the next one started. This asserts the whole
// history, not just its head: a record that jumped from baseline to
// deliver would be a record `pika resume` cannot trust.
func TestRunRecordsEveryPhaseTransition(t *testing.T) {
	root := fixtureRepository(t)
	baseCommit := gitOutput(t, root, "rev-parse", "HEAD")
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail, OutputTail: "repair this"}}},
		{Pass: true, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusPass}}},
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: repairRunner{path: "fixed.txt", body: "verified fix\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkID == "" {
		t.Fatal("result.WorkID is empty: a run the caller cannot name is the state M2 removes")
	}
	rec := runRecord(t, root, result.WorkID)
	want := []string{workrec.PhaseBaseline, workrec.PhaseHandoff, workrec.PhaseRecheck, workrec.PhaseDeliver}
	if got := phaseNames(rec); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("phases = %v, want %v", got, want)
	}
	if rec.Phase != workrec.PhaseDeliver || rec.Outcome != workrec.OutcomeComplete {
		t.Fatalf("phase = %q outcome = %q, want deliver and complete", rec.Phase, rec.Outcome)
	}
	if rec.Kind != workrec.KindRepair {
		t.Fatalf("kind = %q, want %q by default", rec.Kind, workrec.KindRepair)
	}
	if rec.Branch != "chore/pika-improve" || rec.Commit != result.Commit || rec.BaseCommit != baseCommit {
		t.Fatalf("record = %+v, want branch chore/pika-improve, commit %s, base commit %s", rec, result.Commit, baseCommit)
	}
	if rec.Baseline == nil || rec.Baseline.Pass {
		t.Fatalf("record baseline = %+v, want the failing baseline report", rec.Baseline)
	}
	if rec.Recheck == nil || !rec.Recheck.Pass {
		t.Fatalf("record recheck = %+v, want the passing recheck report", rec.Recheck)
	}
	for i := 1; i < len(rec.Phases); i++ {
		if rec.Phases[i].At.Before(rec.Phases[i-1].At) {
			t.Fatalf("phase %q stamped before %q: the history must be ordered", rec.Phases[i].Phase, rec.Phases[i-1].Phase)
		}
	}
}

// Repair work with nothing to repair is finished, and a finished run is
// recorded as such — with no branch, because there was nothing to put on
// one.
func TestGreenBaselineRecordsCompleteWithoutBranching(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			return &verify.Report{Pass: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := runRecord(t, root, result.WorkID)
	if got, want := phaseNames(rec), []string{workrec.PhaseBaseline}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("phases = %v, want %v", got, want)
	}
	if rec.Outcome != workrec.OutcomeComplete || rec.Reason != "" {
		t.Fatalf("outcome = %q reason = %q, want complete with no reason", rec.Outcome, rec.Reason)
	}
	if rec.Branch != "" || rec.Commit != "" {
		t.Fatalf("record = %+v, want no branch and no commit", rec)
	}
	if rec.Baseline == nil || !rec.Baseline.Pass {
		t.Fatalf("record baseline = %+v, want the green baseline report", rec.Baseline)
	}
	if got := gitOutput(t, root, "branch", "--show-current"); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
}

// The single place the two kinds diverge. A green ladder means repair
// work is done; it says nothing about whether a goal has been met, so
// feature work goes to the agent with the goal as its work statement and
// then through the same recheck and commit as any repair.
func TestFeatureKindProceedsToHandoffOnGreenBaseline(t *testing.T) {
	root := fixtureRepository(t)
	const goal = "add a CHANGELOG entry for the release"
	checks := []*verify.Report{{Pass: true}, {Pass: true}}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "feat/pika-work",
		Kind:   workrec.KindFeature,
		Goal:   goal,
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: repairRunner{path: "CHANGELOG.md", body: "# Changelog\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := runRecord(t, root, result.WorkID)
	want := []string{workrec.PhaseBaseline, workrec.PhaseHandoff, workrec.PhaseRecheck, workrec.PhaseDeliver}
	if got := phaseNames(rec); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("phases = %v, want %v: a green ladder must not end feature work", got, want)
	}
	if rec.Kind != workrec.KindFeature || rec.Goal != goal {
		t.Fatalf("record = %+v, want feature kind carrying the goal", rec)
	}
	if result.Branch != "feat/pika-work" || result.Commit == "" {
		t.Fatalf("result = %+v, want a commit on the feature branch", result)
	}
	prompt, err := os.ReadFile(result.Handoff.PromptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), goal) {
		t.Fatalf("prompt = %s, want the goal as the work statement", prompt)
	}
	if want := filepath.Join(root, ".project", "state", "work", result.WorkID, "handoff"); result.Handoff.Dir != want {
		t.Fatalf("bundle = %q, want the run record's own %q", result.Handoff.Dir, want)
	}
}

// A blocked run's record is the only place an operator learns why. The
// reason is the error verbatim, and the branch the agent's work was left
// on is recorded even though the handoff never completed.
func TestAgentFailureRecordsBlockedWithReason(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			return &verify.Report{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail}}}, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: failingMessageRunner{}},
	})
	if err == nil {
		t.Fatal("Run error = nil, want the agent failure")
	}
	rec := runRecord(t, root, result.WorkID)
	if rec.Outcome != workrec.OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", rec.Outcome)
	}
	if rec.Reason != err.Error() {
		t.Fatalf("reason = %q, want the returned error verbatim %q", rec.Reason, err.Error())
	}
	if !strings.Contains(rec.Reason, "Codex failed") {
		t.Fatalf("reason = %q, want the agent's own failure", rec.Reason)
	}
	if rec.Phase != workrec.PhaseBaseline {
		t.Fatalf("phase = %q, want baseline: the handoff phase never completed", rec.Phase)
	}
	if rec.Branch != "chore/pika-improve" {
		t.Fatalf("record branch = %q, want the branch the run left behind", rec.Branch)
	}
	if got := gitOutput(t, root, "branch", "--list", "chore/pika-improve"); got == "" {
		t.Fatal("the branch the record names does not exist")
	}
}

// A refusal that happens before the run does anything must leave nothing
// behind. A directory of empty runs would make every real record harder
// to trust.
func TestDirtyTreeRefusalWritesNoRecord(t *testing.T) {
	root := fixtureRepository(t)
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			t.Fatal("checks must not run on a dirty tree")
			return nil, nil
		},
	})
	if !errors.Is(err, ErrDirtyTree) {
		t.Fatalf("error = %v, want ErrDirtyTree", err)
	}
	assertNoRunRecorded(t, root, result)
}

// Feature work is defined by the goal it is given — the ladder cannot
// state it — so a feature run with no goal has nothing to ask an agent
// for. The refusal lands before the record exists, and leaves nothing
// behind for the same reason the dirty-tree one does.
func TestFeatureWorkWithoutAGoalWritesNoRecord(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Kind:   workrec.KindFeature,
		Check:  refusingCheck(t, "feature work with no goal must not reach the ladder"),
	})
	if err == nil || !strings.Contains(err.Error(), "feature work requires a goal") {
		t.Fatalf("error = %v, want the missing-goal refusal", err)
	}
	assertNoRunRecorded(t, root, result)
}

// Pika has two kinds of work and refuses anything else by name. Guessing
// a kind would pick which of the two state machines a caller's typo runs.
func TestUnknownWorkKindWritesNoRecord(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Kind:   "refactor",
		Check:  refusingCheck(t, "a kind pika does not have must not reach the ladder"),
	})
	if err == nil || !strings.Contains(err.Error(), `unknown work kind "refactor"`) {
		t.Fatalf("error = %v, want the unknown-kind refusal naming it", err)
	}
	assertNoRunRecorded(t, root, result)
}

// The milestone M4 exists for. Two runs in one repository share one
// working tree and one HEAD, so the second does not need to do anything
// unusual to corrupt the first — it only needs to start.
//
// Every case here holds the first run genuinely open: past the lease,
// on the branch it created, with its record on disk, parked inside the
// agent handoff until the test lets it out. A hand-placed lock file
// would prove the guard can read a file and nothing about the race it
// exists to lose.
func TestSecondConcurrentRunIsRefused(t *testing.T) {
	root := fixtureRepository(t)
	first := startBlockedRun(t, root, "chore/pika-improve")
	holder := soleRunID(t, root)

	result, err := Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-improve",
		Check:   refusingCheck(t, "a second concurrent run must not reach the ladder"),
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "a second concurrent run must not spawn an agent"}},
	})
	if !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("error = %v, want ErrRunInProgress", err)
	}
	if result.WorkID != "" {
		t.Fatalf("result = %+v, want nothing: the run was refused", result)
	}
	assertOnlyRunRecorded(t, root, holder)
	first.finish(t)
}

// The headline. Given distinct branches the two runs do not collide at
// `git switch -c`, so nothing stopped them: both walked into the same
// working tree and committed through the same HEAD, and neither was ever
// told. That silence is the defect — a refusal is the whole fix.
//
// The second run's branch must not exist afterwards. That is the proof
// the refusal landed before the working tree was touched rather than
// after: `git switch -c` is the lifecycle's first write, and a branch
// left behind would mean the guard fired too late to matter.
func TestSecondConcurrentRunWithADifferentBranchIsAlsoRefused(t *testing.T) {
	root := fixtureRepository(t)
	first := startBlockedRun(t, root, "chore/pika-improve")
	holder := soleRunID(t, root)

	result, err := Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-other",
		Check:   refusingCheck(t, "a second concurrent run must not reach the ladder"),
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "a second concurrent run must not spawn an agent"}},
	})
	if !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("error = %v, want ErrRunInProgress", err)
	}
	if result.WorkID != "" {
		t.Fatalf("result = %+v, want nothing: the run was refused", result)
	}
	if _, exists, err := branchCommit(context.Background(), root, "chore/pika-other"); err != nil {
		t.Fatal(err)
	} else if exists {
		t.Fatal("the refused run created chore/pika-other: it reached the working tree before it was stopped")
	}
	if head := gitOutput(t, root, "branch", "--show-current"); head != "chore/pika-improve" {
		t.Fatalf("HEAD = %q, want the holder's branch chore/pika-improve: the refused run moved HEAD", head)
	}
	assertOnlyRunRecorded(t, root, holder)
	first.finish(t)
}

// A refusal an operator cannot act on is a refusal that gets worked
// around. The message carries the four facts a person needs to decide
// what to do next: which run holds the repository, what process it is,
// which machine that process is on, and how long it has been there.
func TestRefusalNamesTheHolder(t *testing.T) {
	root := fixtureRepository(t)
	first := startBlockedRun(t, root, "chore/pika-improve")
	holder := soleRunID(t, root)

	dir, name := repolease.RunLock(repoRoot(t, root))
	info, state, err := lease.Inspect(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || state != lease.StateHeld {
		t.Fatalf("lease = %+v state = %v, want a live holder", info, state)
	}
	// The lease names the run, not some identity of its own: a refusal
	// quoting an id `pika status` cannot look up tells the operator
	// nothing they can follow.
	if info.ID != holder {
		t.Fatalf("lease holder = %q, want the run id %q", info.ID, holder)
	}

	_, err = Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-improve",
		Check:   refusingCheck(t, "a second concurrent run must not reach the ladder"),
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "a second concurrent run must not spawn an agent"}},
	})
	if !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("error = %v, want ErrRunInProgress", err)
	}
	for _, want := range []string{
		holder,
		fmt.Sprintf("%d", info.PID),
		info.Host,
		info.StartedAt.Format(time.RFC3339Nano),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to name %q", err, want)
		}
	}
	// "Stale" is what invites an operator to clear a lock a live writer
	// still holds, so a running holder must never be described as one.
	if strings.Contains(err.Error(), "stale") {
		t.Fatalf("error = %v, want a live holder never described as stale", err)
	}
	first.finish(t)
}

// The refusal is only worth having if the run it protects survives it.
// The first run finishes the lifecycle it was in the middle of, commits,
// records its terminal outcome, and hands the repository back — and the
// refused one leaves nothing behind at all.
func TestTheFirstRunIsUnaffectedByTheRefusal(t *testing.T) {
	root := fixtureRepository(t)
	first := startBlockedRun(t, root, "chore/pika-improve")
	holder := soleRunID(t, root)

	if _, err := Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-other",
		Check:   refusingCheck(t, "a second concurrent run must not reach the ladder"),
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "a second concurrent run must not spawn an agent"}},
	}); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("error = %v, want ErrRunInProgress", err)
	}

	result := first.finish(t)
	if result.WorkID != holder {
		t.Fatalf("result.WorkID = %q, want the holder %q", result.WorkID, holder)
	}
	if result.Commit == "" || result.ChecksAfter == nil || !result.ChecksAfter.Pass {
		t.Fatalf("result = %+v, want a verified commit", result)
	}
	rec := runRecord(t, root, holder)
	if rec.Outcome != workrec.OutcomeComplete || rec.Commit != result.Commit {
		t.Fatalf("record = %+v, want outcome complete on commit %s", rec, result.Commit)
	}
	assertOnlyRunRecorded(t, root, holder)
	// The repository is usable again. A lease held past its run is the
	// wedge `pika recover` exists to clear, and a run that reached a
	// terminal outcome must never need it.
	dir, name := repolease.RunLock(repoRoot(t, root))
	if info, state, err := lease.Inspect(dir, name); err != nil || state != lease.StateFree {
		t.Fatalf("lease = %+v state = %v err = %v, want free once the run settled", info, state, err)
	}
}

// blockedRun is a run genuinely in progress. Nothing about it is
// simulated: it took the real lease, created its real branch and wrote
// its real record, and it is parked where a run spends nearly all of its
// wall clock — waiting on the agent.
type blockedRun struct {
	runner *blockingRunner
	done   chan struct{}
	result Result
	err    error
}

func startBlockedRun(t *testing.T, root, branch string) *blockedRun {
	t.Helper()
	runner := &blockingRunner{entered: make(chan struct{}), release: make(chan struct{})}
	run := &blockedRun{runner: runner, done: make(chan struct{})}
	checks := []*verify.Report{failingBaseline(), passingLadder()}
	go func() {
		defer close(run.done)
		run.result, run.err = Run(context.Background(), Config{
			Root:   root,
			Branch: branch,
			Check: func() (*verify.Report, error) {
				report := checks[0]
				checks = checks[1:]
				return report, nil
			},
			Builder: Role{Name: "builder", Agent: "builder", Runner: runner},
		})
	}()
	select {
	case <-runner.entered:
	case <-run.done:
		t.Fatalf("the first run ended before it reached the agent: %+v, %v", run.result, run.err)
	case <-time.After(time.Minute):
		t.Fatal("the first run never reached the agent")
	}
	return run
}

// finish lets the held run out of the handoff and returns what it
// produced, so a case can assert the refusal cost it nothing.
func (r *blockedRun) finish(t *testing.T) Result {
	t.Helper()
	close(r.runner.release)
	select {
	case <-r.done:
	case <-time.After(time.Minute):
		t.Fatal("the first run never finished")
	}
	if r.err != nil {
		t.Fatalf("the first run failed: %v", r.err)
	}
	return r.result
}

// blockingRunner parks the lifecycle inside the handoff until the test
// releases it, and only then does the repair it was asked for. It waits
// before it writes so the tree it holds stays clean: a second run turned
// away by the dirty-tree gate would prove nothing about the lease.
type blockingRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (r *blockingRunner) Run(_ context.Context, root, _, outputPath string) error {
	close(r.entered)
	<-r.release
	if err := os.WriteFile(filepath.Join(root, "fixed.txt"), []byte("verified fix\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(outputPath, []byte("repaired\n"), 0o600)
}

// soleRunID names the one run the repository has recorded, read back
// from disk rather than taken from the run's own return value — the
// holder is still in flight and has returned nothing yet.
func soleRunID(t *testing.T, root string) string {
	t.Helper()
	runs, err := workrec.List(repoRoot(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("workrec.List = %+v, want exactly one run", runs)
	}
	return runs[0].WorkID
}

// assertOnlyRunRecorded is assertNoRunRecorded for a repository that
// legitimately holds one run already: the refused run must still have
// left nothing, and the holder's record must be the only one there.
func assertOnlyRunRecorded(t *testing.T, root, holder string) {
	t.Helper()
	runs, err := workrec.List(repoRoot(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].WorkID != holder {
		t.Fatalf("workrec.List = %+v, want only the holder %s: a refused run must write no record", runs, holder)
	}
}

// A run interrupted anywhere short of its terminal outcome is resumable
// from where it stopped, and a resume redoes only what the record cannot
// prove. The queued ladder reports are that assertion: a case that hands
// over one report and is asked for two has re-derived a baseline the
// record already held — over the agent's edits, which is not the baseline
// those edits came after.
func TestResumeContinuesFromEachInterruptiblePhase(t *testing.T) {
	const branch = "chore/pika-improve"
	for _, tc := range []struct {
		name      string
		phase     string
		agentRuns bool
	}{
		{name: "nothing recorded", phase: "", agentRuns: true},
		{name: "baseline", phase: workrec.PhaseBaseline, agentRuns: true},
		{name: "handoff", phase: workrec.PhaseHandoff, agentRuns: false},
		{name: "recheck", phase: workrec.PhaseRecheck, agentRuns: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fixtureRepository(t)
			rec := workrec.Record{Kind: workrec.KindRepair, Phase: tc.phase}
			if tc.phase != "" {
				rec.Baseline = failingBaseline()
			}
			if tc.phase == workrec.PhaseRecheck {
				rec.Recheck = passingLadder()
			}
			queued := []*verify.Report{passingLadder()}
			if tc.phase == "" {
				queued = []*verify.Report{failingBaseline(), passingLadder()}
			}
			var runner Runner = repairRunner{path: "fixed.txt", body: "verified fix\n"}
			if !tc.agentRuns {
				// The world the interrupted agent already left behind:
				// the run's branch, and its edits uncommitted on it.
				gitRun(t, root, "switch", "-c", branch)
				if err := os.WriteFile(filepath.Join(root, "fixed.txt"), []byte("verified fix\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				rec.Branch = branch
				runner = refusingRunner{t: t, why: "the record proves this run's agent already ran"}
			}
			workID := seedRun(t, root, rec)

			result, err := Resume(context.Background(), root, workID, Config{
				Branch: branch,
				Check: func() (*verify.Report, error) {
					if len(queued) == 0 {
						t.Fatal("the ladder ran more times than the resumed run had phases to redo")
					}
					report := queued[0]
					queued = queued[1:]
					return report, nil
				},
				Builder: Role{Name: "builder", Agent: "builder", Runner: runner},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(queued) != 0 {
				t.Fatalf("%d queued ladder runs never happened", len(queued))
			}
			if result.WorkID != workID || result.Branch != branch || result.Commit == "" {
				t.Fatalf("result = %+v, want run %s committed on %s", result, workID, branch)
			}
			if got := gitOutput(t, root, "branch", "--show-current"); got != branch {
				t.Fatalf("branch = %q, want %s", got, branch)
			}
			if got := gitOutput(t, root, "show", "--name-only", "--format=", "HEAD"); got != "fixed.txt" {
				t.Fatalf("commit contents = %q, want the agent's file", got)
			}
			saved := runRecord(t, root, workID)
			if saved.Outcome != workrec.OutcomeComplete || saved.Commit != result.Commit {
				t.Fatalf("record = %+v, want a complete run at %s", saved, result.Commit)
			}
			if last := saved.Phases[len(saved.Phases)-1]; last.Phase != workrec.PhaseDeliver || last.Note != "resumed" {
				t.Fatalf("last phase = %+v, want a deliver marked resumed", last)
			}
		})
	}
}

// The deliver phase is the one the record cannot settle by itself. A
// record stopping at deliver with no outcome is what a crash leaves — and
// it is also, bit for bit, what a run leaves when it committed and then
// failed to write its outcome. Git tells them apart: the branch holds the
// commit the record names, so the work landed, and the only thing left to
// redo is the write that failed. Re-running the lifecycle here would
// branch again and redo work the repository already contains.
func TestResumeRecordsTheOutcomeGitAlreadyProves(t *testing.T) {
	const branch = "chore/pika-improve"
	root := fixtureRepository(t)
	base := gitOutput(t, root, "rev-parse", "HEAD")
	commit := deliverOnBranch(t, root, branch)
	workID := seedRun(t, root, workrec.Record{
		Kind:       workrec.KindRepair,
		Phase:      workrec.PhaseDeliver,
		Branch:     branch,
		BaseCommit: base,
		Commit:     commit,
		Baseline:   failingBaseline(),
		Recheck:    passingLadder(),
	})

	result, err := Resume(context.Background(), root, workID, Config{
		Branch:  branch,
		Check:   refusingCheck(t, "Git already proves this run's work landed"),
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "Git already proves this run's work landed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkID != workID || result.Branch != branch || result.Commit != commit {
		t.Fatalf("result = %+v, want the delivered commit %s", result, commit)
	}
	if got := gitOutput(t, root, "rev-parse", branch); got != commit {
		t.Fatalf("%s = %s, want the recorded commit %s untouched", branch, got, commit)
	}
	if got := gitOutput(t, root, "rev-list", "--count", branch); got != "2" {
		t.Fatalf("%s holds %s commits, want 2: resume must not commit again", branch, got)
	}
	if got := gitOutput(t, root, "branch", "--format=%(refname:short)"); got != branch+"\nmain" {
		t.Fatalf("branches = %q, want no second branch", got)
	}
	saved := runRecord(t, root, workID)
	if saved.Outcome != workrec.OutcomeComplete {
		t.Fatalf("outcome = %q, want complete", saved.Outcome)
	}
	if got := strings.Join(phaseNames(saved), ","); got != "baseline,handoff,recheck,deliver" {
		t.Fatalf("phases = %q, want the record's own history, unchanged", got)
	}
}

// The world a failed terminal save actually leaves: the record carries no
// outcome, and the receipt is already on disk, because a run issues it
// after recording the outcome and keeps going when that write fails.
// Under the run's own work id a receipt that exists is this run's own, so
// resume writes the outcome and lets the receipt stand. Refusing there
// would make resume fail on precisely the case it exists to repair.
func TestResumeToleratesTheReceiptAnUnsettledRunAlreadyIssued(t *testing.T) {
	const branch = "chore/pika-improve"
	root := fixtureRepository(t)
	base := gitOutput(t, root, "rev-parse", "HEAD")
	commit := deliverOnBranch(t, root, branch)
	workID := seedRun(t, root, workrec.Record{
		Kind:       workrec.KindRepair,
		Phase:      workrec.PhaseDeliver,
		Branch:     branch,
		BaseCommit: base,
		Commit:     commit,
		Baseline:   failingBaseline(),
		Recheck:    passingLadder(),
	})
	cfg := Config{
		Branch:  branch,
		Check:   refusingCheck(t, "Git already proves this run's work landed"),
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "Git already proves this run's work landed"}},
	}
	if _, err := Resume(context.Background(), root, workID, cfg); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(root, ".project", "evidence", workID+".json")
	if _, err := os.Stat(receipt); err != nil {
		t.Fatalf("os.Stat(%q) = %v, want the receipt the resume issued", receipt, err)
	}

	// The outcome goes away again; the receipt stays. This is the record
	// a run whose terminal save failed leaves behind.
	handle, err := workrec.Open(repoRoot(t, root), workID)
	if err != nil {
		t.Fatal(err)
	}
	unsettled := handle.Record()
	unsettled.Outcome = ""
	if err := handle.Save(unsettled); err != nil {
		t.Fatal(err)
	}

	if _, err := Resume(context.Background(), root, workID, cfg); err != nil {
		t.Fatalf("Resume = %v, want the existing receipt tolerated as this run's own", err)
	}
	if saved := runRecord(t, root, workID); saved.Outcome != workrec.OutcomeComplete {
		t.Fatalf("outcome = %q, want complete", saved.Outcome)
	}
}

// The same record, and the opposite verdict from Git. The branch does not
// hold the commit the record names, so the deliver phase never durably
// completed and this is a genuine crash. The run is resumed; the agent is
// not, because its work is already in the tree. Only the phases that make
// a claim about that tree — the recheck and the commit — are redone.
func TestResumeTreatsADeliverGitDisprovesAsACrash(t *testing.T) {
	const branch = "chore/pika-improve"
	root := fixtureRepository(t)
	base := gitOutput(t, root, "rev-parse", "HEAD")
	lost := deliverOnBranch(t, root, branch)
	// The commit leaves the branch and its content returns to the working
	// tree: the record now names a commit the branch cannot show.
	gitRun(t, root, "reset", "--mixed", base)
	workID := seedRun(t, root, workrec.Record{
		Kind:       workrec.KindRepair,
		Phase:      workrec.PhaseDeliver,
		Branch:     branch,
		BaseCommit: base,
		Commit:     lost,
		Baseline:   failingBaseline(),
		Recheck:    passingLadder(),
	})

	ladder := 0
	result, err := Resume(context.Background(), root, workID, Config{
		Branch: branch,
		Check: func() (*verify.Report, error) {
			ladder++
			return passingLadder(), nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "the record proves this run's agent already ran"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ladder != 1 {
		t.Fatalf("the ladder ran %d times, want exactly one recheck", ladder)
	}
	// What the crash path has to prove is that the branch moved: the
	// reset left it on the base commit, and resume put a commit of its
	// own back on top. Whether that commit's hash differs from the
	// discarded one proves nothing either way — resume recreates an
	// identical tree on an identical parent under the same author and
	// message, so landing in the same wall-clock second yields a
	// byte-identical hash. That is Git addressing content correctly,
	// not the run failing to re-run, and an assertion that reads it as
	// failure is a race against the clock.
	if result.Commit == "" {
		t.Fatal("result.Commit is empty, want the commit resume made")
	}
	head := gitOutput(t, root, "rev-parse", branch)
	if head == base {
		t.Fatalf("%s is still on the base %s, want resume to have delivered again", branch, base)
	}
	if head != result.Commit {
		t.Fatalf("%s = %s, want the commit resume made, %s", branch, head, result.Commit)
	}
	if parent := gitOutput(t, root, "rev-parse", result.Commit+"^"); parent != base {
		t.Fatalf("result.Commit sits on %s, want it one commit ahead of the base %s", parent, base)
	}
	if got := gitOutput(t, root, "show", "--name-only", "--format=", branch); got != "fixed.txt" {
		t.Fatalf("commit contents = %q, want the agent's file", got)
	}
	saved := runRecord(t, root, workID)
	if saved.Outcome != workrec.OutcomeComplete || saved.Commit != result.Commit {
		t.Fatalf("record = %+v, want a complete run at %s", saved, result.Commit)
	}
	if got := strings.Join(phaseNames(saved), ","); got != "baseline,handoff,recheck,deliver,recheck,deliver" {
		t.Fatalf("phases = %q, want the redone recheck and deliver appended", got)
	}
}

// The window no phase stamp can cover. `git commit` moves the branch
// first and the deliver phase is saved second, so a crash in between
// leaves a durable record still reading `recheck` while the run's work is
// already permanently in the repository. It is also the likeliest
// interruption there is: nothing switches away from the run's branch
// between the handoff and the deliver, so the operator who sees the crash
// and immediately re-runs `pika resume` is standing on the run's own
// completed commit.
//
// The record cannot name that commit — `Commit` is written by the very
// save that stamps the deliver phase, so here it is empty, and
// `branch == Record.Commit` is not a test that can fire. Git can still
// identify it: one commit, sole parent the run's base, carrying the
// lifecycle's own subject. Going by the stamp instead sends the run into
// the base-commit guard, which reports the run's own verified work as a
// repository that moved underneath it.
//
// Git here is not staged: a real run makes the real commit, and only the
// record is rewound to the bytes on disk one instant before the deliver
// save.
func TestResumeReconcilesACommitTheDeliverStampNeverRecorded(t *testing.T) {
	const branch = "chore/pika-improve"
	root := fixtureRepository(t)
	base := gitOutput(t, root, "rev-parse", "HEAD")
	checks := []*verify.Report{failingBaseline(), passingLadder()}
	first, err := Run(context.Background(), Config{
		Root:   root,
		Branch: branch,
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: repairRunner{path: "fixed.txt", body: "verified fix\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOutput(t, root, "rev-parse", first.Commit+"^"); got != base {
		t.Fatalf("the run's commit sits on %s, want it one commit ahead of the base %s", got, base)
	}

	// Rewind the record to the instant before the deliver save and leave
	// Git exactly as the lifecycle left it. The deliver stamp, the
	// commit, the outcome and the receipt are all written after
	// `git commit` returned, so a crash in this window has none of them.
	handle, err := workrec.Open(repoRoot(t, root), first.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	crashed := handle.Record()
	crashed.Phase = workrec.PhaseRecheck
	crashed.Phases = phaseHistory(workrec.PhaseRecheck)
	crashed.Commit = ""
	crashed.Outcome = ""
	if err := handle.Save(crashed); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(root, ".project", "evidence", first.WorkID+".json")
	if err := os.Remove(receipt); err != nil {
		t.Fatal(err)
	}

	result, err := Resume(context.Background(), root, first.WorkID, Config{
		Branch:  branch,
		Check:   refusingCheck(t, "Git already proves this run's work landed"),
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "Git already proves this run's work landed"}},
	})
	if err != nil {
		t.Fatalf("Resume = %v, want the run's own commit recognised as delivered", err)
	}
	if result.WorkID != first.WorkID || result.Branch != branch || result.Commit != first.Commit {
		t.Fatalf("result = %+v, want the delivered commit %s", result, first.Commit)
	}
	if got := gitOutput(t, root, "rev-parse", branch); got != first.Commit {
		t.Fatalf("%s = %s, want the run's commit %s untouched", branch, got, first.Commit)
	}
	if got := gitOutput(t, root, "rev-list", "--count", branch); got != "2" {
		t.Fatalf("%s holds %s commits, want 2: resume must not commit again", branch, got)
	}
	if got := gitOutput(t, root, "branch", "--format=%(refname:short)"); got != branch+"\nmain" {
		t.Fatalf("branches = %q, want no second branch", got)
	}
	saved := runRecord(t, root, first.WorkID)
	if saved.Outcome != workrec.OutcomeComplete || saved.Commit != first.Commit {
		t.Fatalf("record = %+v, want a complete run at %s", saved, first.Commit)
	}
	if got := strings.Join(phaseNames(saved), ","); got != "baseline,handoff,recheck,deliver" {
		t.Fatalf("phases = %q, want the deliver stamp the crash lost written now", got)
	}
	if last := saved.Phases[len(saved.Phases)-1]; last.Note != "resumed" {
		t.Fatalf("last phase = %+v, want the recovered deliver marked resumed", last)
	}
	if _, err := os.Stat(receipt); err != nil {
		t.Fatalf("os.Stat(%q) = %v, want the receipt this resume issued", receipt, err)
	}
}

// The other half of that reconciliation: it recognises the run's own
// commit and nothing else. Here the branch has moved for a reason the run
// had nothing to do with — same phase, same branch, same parent, a
// different commit — so Git proves nothing and the base-commit guard is
// still the answer. Widening the guard instead of recognising the run's
// own work would have swallowed this case with it.
func TestResumeStillRefusesACommitTheRunDidNotMake(t *testing.T) {
	const branch = "chore/pika-improve"
	root := fixtureRepository(t)
	base := gitOutput(t, root, "rev-parse", "HEAD")
	gitRun(t, root, "switch", "-c", branch)
	if err := os.WriteFile(filepath.Join(root, "elsewhere.txt"), []byte("someone else's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "elsewhere.txt")
	gitRun(t, root, "commit", "-qm", "unrelated work")
	head := gitOutput(t, root, "rev-parse", "HEAD")
	workID := seedRun(t, root, workrec.Record{
		Kind:       workrec.KindRepair,
		Phase:      workrec.PhaseRecheck,
		Branch:     branch,
		BaseCommit: base,
		Baseline:   failingBaseline(),
		Recheck:    passingLadder(),
	})

	result, err := Resume(context.Background(), root, workID, Config{
		Branch:  branch,
		Check:   refusingCheck(t, "a moved repository must not be re-verified"),
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "a moved repository must not spawn an agent"}},
	})
	assertRefusal(t, err, ErrTreeDiverged)
	if !strings.Contains(err.Error(), base) || !strings.Contains(err.Error(), head) {
		t.Fatalf("error = %v, want both the run's base %s and the current HEAD %s", err, base, head)
	}
	if result.WorkID != "" {
		t.Fatalf("result = %+v, want nothing: the run was refused", result)
	}
	if saved := runRecord(t, root, workID); saved.Outcome != "" || saved.Commit != "" {
		t.Fatalf("record = %+v, want it untouched: nothing was delivered", saved)
	}
}

// A run that recorded a terminal outcome is finished. Resume says so
// rather than starting it over.
func TestResumeRefusesTerminalOutcome(t *testing.T) {
	root := fixtureRepository(t)
	workID := seedRun(t, root, workrec.Record{
		Kind:    workrec.KindRepair,
		Phase:   workrec.PhaseDeliver,
		Outcome: workrec.OutcomeComplete,
	})
	result, err := Resume(context.Background(), root, workID, Config{
		Branch:  "chore/pika-improve",
		Check:   refusingCheck(t, "a finished run must not be re-verified"),
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "a finished run must not spawn an agent"}},
	})
	assertRefusal(t, err, ErrRunFinished)
	if !strings.Contains(err.Error(), workID) || !strings.Contains(err.Error(), workrec.OutcomeComplete) {
		t.Fatalf("error = %v, want the run and the outcome it ended with", err)
	}
	if result.WorkID != "" {
		t.Fatalf("result = %+v, want nothing: the run was refused", result)
	}
}

// The recorded branch carried everything the run had done and not yet
// committed. Without it there is nothing to rejoin, and recreating it
// would produce an empty branch standing in for the run's work.
func TestResumeRefusesMissingBranch(t *testing.T) {
	const branch = "chore/pika-improve"
	root := fixtureRepository(t)
	workID := seedRun(t, root, workrec.Record{
		Kind:     workrec.KindRepair,
		Phase:    workrec.PhaseHandoff,
		Branch:   branch,
		Baseline: failingBaseline(),
	})
	result, err := Resume(context.Background(), root, workID, Config{
		Branch:  branch,
		Check:   refusingCheck(t, "a run whose branch is gone must not be re-verified"),
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "a run whose branch is gone must not spawn an agent"}},
	})
	assertRefusal(t, err, ErrBranchGone)
	if !strings.Contains(err.Error(), branch) {
		t.Fatalf("error = %v, want the branch it cannot find named", err)
	}
	if result.WorkID != "" {
		t.Fatalf("result = %+v, want nothing: the run was refused", result)
	}
}

// Every phase the record describes was observed against its base commit.
// Rejoining on top of a different one would verify and commit against a
// repository the run never saw.
func TestResumeRefusesDivergedTree(t *testing.T) {
	root := fixtureRepository(t)
	base := gitOutput(t, root, "rev-parse", "HEAD")
	workID := seedRun(t, root, workrec.Record{
		Kind:       workrec.KindRepair,
		Phase:      workrec.PhaseBaseline,
		BaseCommit: base,
		Baseline:   failingBaseline(),
	})
	// The repository moves on without the run.
	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("someone else's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "unrelated.txt")
	gitRun(t, root, "commit", "-qm", "unrelated work")
	head := gitOutput(t, root, "rev-parse", "HEAD")

	result, err := Resume(context.Background(), root, workID, Config{
		Branch:  "chore/pika-improve",
		Check:   refusingCheck(t, "a moved repository must not be re-verified"),
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "a moved repository must not spawn an agent"}},
	})
	assertRefusal(t, err, ErrTreeDiverged)
	if !strings.Contains(err.Error(), base) || !strings.Contains(err.Error(), head) {
		t.Fatalf("error = %v, want both the run's base %s and the current HEAD %s", err, base, head)
	}
	if result.WorkID != "" {
		t.Fatalf("result = %+v, want nothing: the run was refused", result)
	}
}

func repoRoot(t *testing.T, root string) *repopath.Root {
	t.Helper()
	resolved, err := repopath.At(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// runRecord reads back what the run durably wrote, never what it returned
// in memory: the record is the artifact under test.
func runRecord(t *testing.T, root, workID string) workrec.Record {
	t.Helper()
	handle, err := workrec.Open(repoRoot(t, root), workID)
	if err != nil {
		t.Fatal(err)
	}
	return handle.Record()
}

func phaseNames(rec workrec.Record) []string {
	names := make([]string, 0, len(rec.Phases))
	for _, stamp := range rec.Phases {
		names = append(names, stamp.Phase)
	}
	return names
}

// fixtureRepository builds an adopted repository: one that gitignores
// Pika's private state, as `pika init` leaves it.
func fixtureRepository(t *testing.T) string {
	t.Helper()
	return newFixture(t, ".project/state/\n")
}

// fixtureRepositoryWithoutStateIgnore builds one that does not, so the
// run record and the handoff bundle are offered to Git like any other
// untracked file.
func fixtureRepositoryWithoutStateIgnore(t *testing.T) string {
	t.Helper()
	return newFixture(t, "")
}

// fixtureRepositoryWithTrackedPrivateState goes one step further and
// commits a file inside `.project/state`. Git reports a rename only for a
// path it already tracks, so this is the world a `git mv` out of the
// subtree needs — and it is an ordinary one: any repository that does not
// ignore `.project/state` gets there the first time someone commits.
func fixtureRepositoryWithTrackedPrivateState(t *testing.T, path, body string) string {
	t.Helper()
	root := newFixture(t, "")
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "--", path)
	gitRun(t, root, "commit", "-qm", "track private state")
	return root
}

func newFixture(t *testing.T, gitignore string) string {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, "init", "-q", "-b", "main")
	gitRun(t, root, "config", "user.name", "Pika Test")
	gitRun(t, root, "config", "user.email", "pika@example.test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "README.md", ".gitignore")
	gitRun(t, root, "commit", "-qm", "initial")
	return root
}

func gitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}

type repairRunner struct {
	path string
	body string
}

type committingRunner struct{}

func (committingRunner) Run(_ context.Context, root, _, outputPath string) error {
	if err := os.WriteFile(filepath.Join(root, "agent.txt"), []byte("not allowed\n"), 0o644); err != nil {
		return err
	}
	cmd := exec.Command("git", "add", "agent.txt")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w: %s", err, output)
	}
	cmd = exec.Command("git", "commit", "-m", "agent commit")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, output)
	}
	return os.WriteFile(outputPath, []byte("committed\n"), 0o600)
}

type switchingRunner struct{}

func (switchingRunner) Run(_ context.Context, root, _, outputPath string) error {
	cmd := exec.Command("git", "switch", "main")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git switch: %w: %s", err, output)
	}
	return os.WriteFile(outputPath, []byte("switched\n"), 0o600)
}

// stagingRunner is the agent that force-adds Pika's own private state.
// It finds the bundle from the prompt it was handed rather than from a
// hard-coded path, so it stages wherever the run record actually put the
// bundle and cannot silently go on testing a location Pika retired.
type stagingRunner struct {
	staged string
}

func (r *stagingRunner) Run(_ context.Context, root, promptPath, outputPath string) error {
	if err := os.WriteFile(filepath.Join(root, "fixed.txt"), []byte("fixed\n"), 0o644); err != nil {
		return err
	}
	statePath := filepath.Join(filepath.Dir(promptPath), "private.txt")
	if err := os.WriteFile(statePath, []byte("private\n"), 0o600); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, statePath)
	if err != nil {
		return err
	}
	r.staged = filepath.ToSlash(rel)
	cmd := exec.Command("git", "add", "-f", "--", r.staged)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add private state: %w: %s", err, output)
	}
	return os.WriteFile(outputPath, []byte("staged private state\n"), 0o600)
}

// renamingRunner is the agent that smuggles private state past the path
// filter with a rename instead of a force-add. `git mv` is one command an
// agent has every ordinary reason to reach for, and the status line it
// produces names the private origin and an innocent destination together.
type renamingRunner struct {
	from string
	to   string
}

func (r renamingRunner) Run(_ context.Context, root, _, outputPath string) error {
	if err := os.WriteFile(filepath.Join(root, "fixed.txt"), []byte("fixed\n"), 0o644); err != nil {
		return err
	}
	cmd := exec.Command("git", "mv", "--", r.from, r.to)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git mv private state: %w: %s", err, output)
	}
	return os.WriteFile(outputPath, []byte("renamed private state\n"), 0o600)
}

type rewritingRunner struct{}

func (rewritingRunner) Run(_ context.Context, root, _, outputPath string) error {
	cmd := exec.Command("git", "branch", "-f", "main", "HEAD~1")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git branch -f: %w: %s", err, output)
	}
	return os.WriteFile(outputPath, []byte("rewrote main\n"), 0o600)
}

type pendingMergeRunner struct{}

func (pendingMergeRunner) Run(_ context.Context, root, _, outputPath string) error {
	cmd := exec.Command("git", "rev-parse", "--git-path", "MERGE_HEAD")
	cmd.Dir = root
	path, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git merge path: %w", err)
	}
	mergePath := strings.TrimSpace(string(path))
	if !filepath.IsAbs(mergePath) {
		mergePath = filepath.Join(root, mergePath)
	}
	if err := os.WriteFile(mergePath, []byte("pending\n"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(outputPath, []byte("merge pending\n"), 0o600)
}

func (r repairRunner) Run(_ context.Context, root, _, outputPath string) error {
	if err := os.WriteFile(filepath.Join(root, r.path), []byte(r.body), 0o644); err != nil {
		return err
	}
	return os.WriteFile(outputPath, []byte("repaired\n"), 0o600)
}

// Runtime is what every fake here reports. The lifecycle records it and
// the receipt prints it, so a fake that reported nothing would be testing
// a shape production cannot produce.
//
// The value is codex because that is the runtime these fixtures stood in
// for before M6, and the bundle filenames some of them assert are the
// ones a codex handoff writes.
func (repairRunner) Runtime() string       { return "codex" }
func (committingRunner) Runtime() string   { return "codex" }
func (switchingRunner) Runtime() string    { return "codex" }
func (*stagingRunner) Runtime() string     { return "codex" }
func (renamingRunner) Runtime() string     { return "codex" }
func (rewritingRunner) Runtime() string    { return "codex" }
func (pendingMergeRunner) Runtime() string { return "codex" }
func (r *blockingRunner) Runtime() string  { return "codex" }

// assertRefusal holds a refusal to naming exactly one of the three worlds
// resume can find itself in. Each leaves the operator with a different
// decision, so a refusal matching two of these sentinels would be the
// useless "cannot resume" wearing three names.
func assertRefusal(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	for _, other := range []error{ErrRunFinished, ErrBranchGone, ErrTreeDiverged} {
		if other != want && errors.Is(err, other) {
			t.Fatalf("error = %v, want only %v and not also %v", err, want, other)
		}
	}
}

// assertNoRunRecorded is the shape every refusal that lands before
// workrec.Create must leave: no work id to report, and no run directory
// at all.
func assertNoRunRecorded(t *testing.T, root string, result Result) {
	t.Helper()
	if result.WorkID != "" {
		t.Fatalf("result.WorkID = %q, want none: the run never started", result.WorkID)
	}
	work := filepath.Join(root, ".project", "state", "work")
	if _, err := os.Stat(work); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) = %v, want not-exist", work, err)
	}
	runs, err := workrec.List(repoRoot(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("workrec.List = %+v, want no runs", runs)
	}
}

// seedRun writes the record a crashed run leaves behind: phases stamped
// up to the one that completed, and no terminal outcome unless the case
// asks for one. It is built rather than produced by killing a real run
// because those two are the same bytes on disk — which is the whole
// reason Resume cannot decide from the record alone.
func seedRun(t *testing.T, root string, rec workrec.Record) string {
	t.Helper()
	if rec.WorkID == "" {
		id, err := evidence.NewWorkID(time.Now().UTC(), "repair")
		if err != nil {
			t.Fatal(err)
		}
		rec.WorkID = id
	}
	if rec.BaseCommit == "" {
		rec.BaseCommit = gitOutput(t, root, "rev-parse", "HEAD")
	}
	if rec.Phases == nil {
		rec.Phases = phaseHistory(rec.Phase)
	}
	if _, err := workrec.Create(repoRoot(t, root), rec); err != nil {
		t.Fatal(err)
	}
	return rec.WorkID
}

// phaseHistory is the stamp list a run that reached phase would have
// written on its way there.
func phaseHistory(phase string) []workrec.PhaseStamp {
	var stamps []workrec.PhaseStamp
	if phase == "" {
		return stamps
	}
	for _, p := range []string{workrec.PhaseBaseline, workrec.PhaseHandoff, workrec.PhaseRecheck, workrec.PhaseDeliver} {
		stamps = append(stamps, workrec.PhaseStamp{Phase: p, At: time.Now().UTC()})
		if p == phase {
			break
		}
	}
	return stamps
}

// deliverOnBranch is a run's delivered work as Git holds it: the branch,
// the agent's file, and the verified commit.
func deliverOnBranch(t *testing.T, root, branch string) string {
	t.Helper()
	gitRun(t, root, "switch", "-c", branch)
	if err := os.WriteFile(filepath.Join(root, "fixed.txt"), []byte("verified fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "fixed.txt")
	gitRun(t, root, "commit", "-qm", "chore: improve verified findings")
	return gitOutput(t, root, "rev-parse", "HEAD")
}

func failingBaseline() *verify.Report {
	return &verify.Report{Pass: false, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusFail, OutputTail: "repair this"}}}
}

func passingLadder() *verify.Report {
	return &verify.Report{Pass: true, Gates: []verify.GateResult{{ID: "lint", Status: verify.StatusPass}}}
}

// refusingCheck fails the test if the ladder runs at all.
func refusingCheck(t *testing.T, why string) CheckFunc {
	return func() (*verify.Report, error) {
		t.Helper()
		t.Fatalf("the ladder must not run: %s", why)
		return nil, nil
	}
}

// refusingRunner fails the test if an agent is spawned at all. It is how
// "this did not re-run the lifecycle" is proved: the agent is the
// expensive, non-idempotent half of a run, so a runner that fails the
// test the moment it is invoked is the evidence that matters.
type refusingRunner struct {
	t   *testing.T
	why string
}

func (r refusingRunner) Run(context.Context, string, string, string) error {
	r.t.Helper()
	r.t.Fatalf("the agent must not run: %s", r.why)
	return nil
}

// Runtime never runs: this runner's only job is to fail the test if it is
// invoked, and a name is the least it can report without doing so.
func (r refusingRunner) Runtime() string { return "codex" }

// A branch name is a value. Git reads a leading `-` as the start of an
// option unless it is told where the options stop, so a branch called
// `-weird` is either switched to or mistaken for a bundle of short
// flags. Such a branch is unusual but reachable — `git update-ref`
// creates one — and the day a branch name reaches Pika from anywhere but
// an operator's own flag, the difference between those two readings is
// the difference between a refusal and an argument Pika did not write.
func TestLeadingDashBranchIsSwitchedToAsAValue(t *testing.T) {
	root := newFixture(t, "")
	gitRun(t, root, "update-ref", "refs/heads/-weird", "HEAD")

	if err := enterBranch(context.Background(), root, "-weird"); err != nil {
		t.Fatalf("enterBranch on a branch named %q: %v", "-weird", err)
	}
	if head := gitOutput(t, root, "symbolic-ref", "--short", "HEAD"); head != "-weird" {
		t.Fatalf("HEAD = %q, want %q", head, "-weird")
	}
}

// The same reading applied to a commit is worse than a wrong branch:
// `git show --output=<path>` writes a file and exits zero, so an
// argument read as an option acts on the filesystem and reports success.
// The refusal is the point — the file must not appear.
func TestLeadingDashCommitIsNotReadAsAnOption(t *testing.T) {
	root := newFixture(t, "")
	written := filepath.Join(t.TempDir(), "written")

	if _, _, err := commitShape(context.Background(), root, "--output="+written); err == nil {
		t.Fatal("commitShape accepted an option-shaped commit, want a refusal")
	}
	if _, err := os.Stat(written); !os.IsNotExist(err) {
		t.Fatalf("git wrote %s: the commit argument was executed as an option", written)
	}
}

// mcpSession is a genuine `pika mcp` stdio session, driven over real OS
// pipes exactly as an MCP client drives it. Nothing here is a stand-in:
// the leases it takes are taken by the server's own acquire_scope, and
// it gives them back at EOF the way a disconnecting agent's session
// does.
type mcpSession struct {
	t    *testing.T
	inW  *os.File
	outR *os.File
	r    *bufio.Reader
	done chan error
}

func startMCPSession(t *testing.T, root string) *mcpSession {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	s := &mcpSession{t: t, inW: inW, outR: outR, r: bufio.NewReader(outR), done: make(chan error, 1)}
	go func() { s.done <- mcp.Serve(root, inR, outW, io.Discard) }()
	t.Cleanup(func() {
		s.end()
		outR.Close()
	})
	return s
}

// call sends one tools/call and returns the decoded response.
func (s *mcpSession) call(id int, name string, args map[string]any) map[string]any {
	s.t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.inW.Write(append(req, '\n')); err != nil {
		s.t.Fatalf("write request: %v", err)
	}
	line, err := s.r.ReadString('\n')
	if err != nil {
		s.t.Fatalf("read response: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		s.t.Fatalf("response is not JSON: %q: %v", line, err)
	}
	return resp
}

// acquire takes a scope lease and fails the test unless it is granted.
func (s *mcpSession) acquire(id int, path string) {
	s.t.Helper()
	resp := s.call(id, "acquire_scope", map[string]any{"path": path})
	res, ok := resp["result"].(map[string]any)
	if !ok || res["ok"] != true {
		s.t.Fatalf("acquire_scope %s = %v, want a granted lease", path, resp)
	}
}

// end closes stdin, which is the clean shutdown an MCP client performs
// and the point at which the server gives every lease back.
func (s *mcpSession) end() {
	s.t.Helper()
	if s.inW == nil {
		return
	}
	s.inW.Close()
	s.inW = nil
	select {
	case err := <-s.done:
		if err != nil {
			s.t.Errorf("mcp server exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		s.t.Error("mcp server did not exit after stdin EOF")
	}
}

// TestARunIsRefusedWhileAnMCPSessionHoldsAScope is the other half of the
// third door M4 left open.
//
// A run lease and a scope lease were two exclusions that never looked at
// each other, so `pika mcp` serving an agent harness in one terminal and
// `pika work` in another both held the same working tree. The run is the
// more dangerous of the two to let through: it switches branches under
// the session's edits and sweeps whatever the session has written so far
// into its own `git add`.
//
// The scope lease here is held by a real server session that really
// granted it, and the run is a real improve.Run reaching the real lease
// it always takes. The ladder and the agent are refusing stubs, which is
// how "the refusal landed before anything was touched" is proved.
func TestARunIsRefusedWhileAnMCPSessionHoldsAScope(t *testing.T) {
	root := fixtureRepository(t)
	writeMCPEnvelope(t, root, ".project", "src")
	session := startMCPSession(t, root)
	session.acquire(1, "src")

	_, err := Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-improve",
		Check:   refusingCheck(t, "a run must not reach the ladder while a scope lease is held"),
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "a run must not spawn an agent while a scope lease is held"}},
	})
	if !errors.Is(err, ErrScopeLeaseHeld) {
		t.Fatalf("error = %v, want ErrScopeLeaseHeld", err)
	}
	// The refusal must name what is really holding the tree. "Another
	// run" would send the operator to `pika status`, where they would
	// find nothing and conclude the message was lying.
	for _, want := range []string{"src", "scope lease"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "stale") {
		t.Errorf("error = %v, want a live session's lease never described as stale", err)
	}
	// Refused before the working tree was touched: `git switch -c` is
	// the lifecycle's first write, and a branch left behind would mean
	// the guard fired too late to matter.
	if _, exists, err := branchCommit(context.Background(), root, "chore/pika-improve"); err != nil {
		t.Fatal(err)
	} else if exists {
		t.Fatal("the refused run created its branch: it reached the working tree before it was stopped")
	}
	// And it left no run lease of its own behind, which would have
	// wedged the repository for everybody afterwards.
	dir, name := repolease.RunLock(repoRoot(t, root))
	if info, state, err := lease.Inspect(dir, name); err != nil || state != lease.StateFree {
		t.Fatalf("run lease = %+v state = %v err = %v, want free after a refusal", info, state, err)
	}

	// The session ends the way a disconnecting agent's does, giving back
	// what it took, and the repository runs again. The exclusion is a
	// refusal while the ground is taken, not a permanent denial.
	session.end()
	result, err := Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-improve",
		Check:   func() (*verify.Report, error) { return &verify.Report{Pass: true}, nil },
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "a green baseline needs no agent"}},
	})
	if err != nil {
		t.Fatalf("run after the session released its lease: %v", err)
	}
	if result.WorkID == "" {
		t.Fatalf("result = %+v, want a run that actually started", result)
	}
}

// writeMCPEnvelope grants an MCP session fs_write on the given paths.
// `.project` is always among them in practice: acquire_scope appends to
// the state board, and a session that cannot write there cannot take a
// lease at all.
func writeMCPEnvelope(t *testing.T, root string, paths ...string) {
	t.Helper()
	state := filepath.Join(root, ".project", "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "schema: 1\nallow:\n  fs_write: [" + strings.Join(paths, ", ") + "]\n"
	if err := os.WriteFile(filepath.Join(state, "envelope.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The defect, reproduced from the top. A run that fails leaves its
// branch behind — nothing on the failure path deletes it — and before
// this, every later run in that repository died on Git's own `a branch
// named 'chore/pika-improve' already exists`, exit 128, naming no
// remedy. The repository stayed poisoned until an operator happened to
// know that the fix was to delete a branch by hand.
//
// The leftover of a run that committed nothing carries nothing, so there
// is nothing to refuse over: the next run takes the branch and gets on
// with it.
func TestALeftoverBranchWithNoRecordedCommitsDoesNotBlockALaterRun(t *testing.T) {
	root := fixtureRepository(t)
	first, err := Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-improve",
		Check:   func() (*verify.Report, error) { return failingBaseline(), nil },
		Builder: Role{Name: "builder", Agent: "builder", Runner: failingMessageRunner{}},
	})
	if err == nil {
		t.Fatal("the first run must fail: its failure is what leaves the branch behind")
	}
	if rec := runRecord(t, root, first.WorkID); rec.Commit != "" {
		t.Fatalf("the first run committed %s; this case is the leftover of one that did not", rec.Commit)
	}
	if got := gitOutput(t, root, "branch", "--list", "chore/pika-improve"); got == "" {
		t.Fatal("the first run left no branch behind, so there is no leftover to test")
	}

	// What the operator does next: go back to where they were and
	// commit the receipt the failed run issued, which is committable
	// content rather than local state. It also moves HEAD, so the
	// leftover branch is now strictly behind the commit the second run
	// starts from — the ordinary shape of this, not a contrived one.
	gitRun(t, root, "switch", "--", "main")
	gitRun(t, root, "add", "--", ".project/evidence")
	gitRun(t, root, "commit", "-qm", "chore: keep the failed run's receipt")

	checks := []*verify.Report{failingBaseline(), passingLadder()}
	second, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: repairRunner{path: "fixed.txt", body: "verified fix\n"}},
	})
	if err != nil {
		t.Fatalf("the second run failed on a branch that carried nothing: %v", err)
	}
	if second.Commit == "" {
		t.Fatalf("result = %+v, want the second run's verified commit", second)
	}
	// The reused branch starts where THIS run started. Switching to the
	// leftover where it stood would have put the tree on the abandoned
	// run's commit while the record claimed the newer one.
	base := runRecord(t, root, second.WorkID).BaseCommit
	if parent := gitOutput(t, root, "rev-parse", second.Commit+"^"); parent != base {
		t.Fatalf("the delivered commit's parent is %s, want the second run's base commit %s", parent, base)
	}
}

// The other world, and the reason deleting a leftover unasked is not an
// option: a run can stop after its commit has landed, and the branch is
// then the only place that work exists. The refusal names the branch,
// the run that made it, what it holds, and what to do about it — the
// last of which the old failure named not at all.
func TestALeftoverBranchHoldingRecordedWorkIsRefusedByBranchRunAndRemedy(t *testing.T) {
	root := fixtureRepository(t)
	checks := []*verify.Report{failingBaseline(), passingLadder()}
	first, err := Run(context.Background(), Config{
		Root:   root,
		Branch: "chore/pika-improve",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Builder: Role{Name: "builder", Agent: "builder", Runner: repairRunner{path: "fixed.txt", body: "verified fix\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Commit == "" {
		t.Fatalf("result = %+v, want a delivered commit for the branch to hold", first)
	}
	// The operator goes back to their own branch without merging or
	// deleting anything, which is exactly the state a delivered run is
	// designed to leave: publishing is a human choice.
	gitRun(t, root, "switch", "--", "main")
	gitRun(t, root, "add", "--", ".project/evidence")
	gitRun(t, root, "commit", "-qm", "chore: keep the delivered run's receipt")

	second, err := Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-improve",
		Check:   func() (*verify.Report, error) { return failingBaseline(), nil },
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "the branch it would work on already holds committed work"}},
	})
	if !errors.Is(err, ErrBranchHoldsWork) {
		t.Fatalf("error = %v, want ErrBranchHoldsWork", err)
	}
	for _, want := range []string{
		"chore/pika-improve",
		first.WorkID,
		first.Commit,
		"git branch -D chore/pika-improve",
		"--branch",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err.Error(), want)
		}
	}
	// Nothing was destroyed and nothing was moved: a refusal that
	// rearranged the repository on its way out would be the defect
	// wearing a better message.
	if head := gitOutput(t, root, "rev-parse", "chore/pika-improve"); head != first.Commit {
		t.Fatalf("branch head = %s, want the first run's commit %s untouched", head, first.Commit)
	}
	if got := gitOutput(t, root, "branch", "--show-current"); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
	if second.StoppedOn != "main" {
		t.Fatalf("StoppedOn = %q, want main: the refused run never left it", second.StoppedOn)
	}
}

// Git is the ground truth about what is at risk, not the run record. A
// `chore/pika-improve` an operator made themselves holds work too, and
// no record will ever mention it — so the refusal has to be able to say
// that it found commits nobody here claims rather than assume that an
// unclaimed branch is Pika's to overwrite.
func TestALeftoverBranchNoRunRecordClaimsIsRefusedToo(t *testing.T) {
	root := fixtureRepository(t)
	gitRun(t, root, "switch", "-c", "chore/pika-improve")
	if err := os.WriteFile(filepath.Join(root, "mine.txt"), []byte("my own work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "mine.txt")
	gitRun(t, root, "commit", "-qm", "work of my own")
	head := gitOutput(t, root, "rev-parse", "HEAD")
	gitRun(t, root, "switch", "--", "main")

	_, err := Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-improve",
		Check:   func() (*verify.Report, error) { return failingBaseline(), nil },
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "the branch it would work on holds an operator's own commit"}},
	})
	if !errors.Is(err, ErrBranchHoldsWork) {
		t.Fatalf("error = %v, want ErrBranchHoldsWork", err)
	}
	for _, want := range []string{"chore/pika-improve", head, "which no run record claims"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err.Error(), want)
		}
	}
	if got := gitOutput(t, root, "rev-parse", "chore/pika-improve"); got != head {
		t.Fatalf("branch head = %s, want the operator's own commit %s untouched", got, head)
	}
}

// A run that stopped before it ever created a branch still stopped
// somewhere. Result.Branch is empty there by construction, and reporting
// only that is what printed `stopped on branch -` — a line whose whole
// job is to say where the run stopped, saying nothing.
func TestARunThatStoppedBeforeBranchingReportsTheBranchItWasOn(t *testing.T) {
	root := fixtureRepository(t)
	gitRun(t, root, "switch", "-c", "feature/mine")

	result, err := Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-improve",
		Check:   func() (*verify.Report, error) { return nil, errors.New("check: no contract") },
		Builder: Role{Name: "builder", Agent: "builder", Runner: refusingRunner{t: t, why: "the baseline ladder never produced a report"}},
	})
	if err == nil {
		t.Fatal("Run error = nil, want the baseline failure")
	}
	if result.Branch != "" {
		t.Fatalf("Branch = %q, want none: the run stopped before it branched", result.Branch)
	}
	if result.StoppedOn != "feature/mine" {
		t.Fatalf("StoppedOn = %q, want feature/mine: the branch the repository was actually on", result.StoppedOn)
	}
}

// And where the two branches disagree, the reported one is Git's. An
// agent that switches away mid-handoff leaves the run stopped somewhere
// other than its own branch, and that disagreement is the most useful
// thing the report can carry.
func TestARunTheAgentSwitchedAwayFromReportsTheBranchGitWasOn(t *testing.T) {
	root := fixtureRepository(t)
	result, err := Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-improve",
		Check:   func() (*verify.Report, error) { return failingBaseline(), nil },
		Builder: Role{Name: "builder", Agent: "builder", Runner: switchingRunner{}},
	})
	if err == nil {
		t.Fatal("Run error = nil, want the branch guard")
	}
	if result.Branch != "chore/pika-improve" {
		t.Fatalf("Branch = %q, want the branch the run created", result.Branch)
	}
	if result.StoppedOn != "main" {
		t.Fatalf("StoppedOn = %q, want main: the branch the agent left the repository on", result.StoppedOn)
	}
}
