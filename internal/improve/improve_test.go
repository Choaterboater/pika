package improve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Choaterboater/pika/internal/evidence"
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
		Runner: repairRunner{path: "fixed.txt", body: "verified fix\n"},
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
		Runner: repairRunner{path: "fixed.txt", body: "needs review\n"},
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
		Runner: committingRunner{},
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
		Runner: switchingRunner{},
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
		Runner: rewritingRunner{},
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
		Runner: pendingMergeRunner{},
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
		Runner: runner,
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
		Runner: renamingRunner{from: private, to: "leaked.json"},
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
		Runner: renamingRunner{from: private, to: "leaked.json"},
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
		Runner: repairRunner{path: repaired, body: "verified fix\n"},
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
		Runner: repairRunner{path: repaired, body: "verified fix\n"},
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
		Runner: repairRunner{path: "fixed.txt", body: "verified fix\n"},
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
		Runner: repairRunner{path: "CHANGELOG.md", body: "# Changelog\n"},
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
		Runner: failingMessageRunner{},
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
				Runner: runner,
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
		Branch: branch,
		Check:  refusingCheck(t, "Git already proves this run's work landed"),
		Runner: refusingRunner{t: t, why: "Git already proves this run's work landed"},
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
		Branch: branch,
		Check:  refusingCheck(t, "Git already proves this run's work landed"),
		Runner: refusingRunner{t: t, why: "Git already proves this run's work landed"},
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
		Runner: refusingRunner{t: t, why: "the record proves this run's agent already ran"},
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
		Runner: repairRunner{path: "fixed.txt", body: "verified fix\n"},
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
		Branch: branch,
		Check:  refusingCheck(t, "Git already proves this run's work landed"),
		Runner: refusingRunner{t: t, why: "Git already proves this run's work landed"},
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
		Branch: branch,
		Check:  refusingCheck(t, "a moved repository must not be re-verified"),
		Runner: refusingRunner{t: t, why: "a moved repository must not spawn an agent"},
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
		Branch: "chore/pika-improve",
		Check:  refusingCheck(t, "a finished run must not be re-verified"),
		Runner: refusingRunner{t: t, why: "a finished run must not spawn an agent"},
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
		Branch: branch,
		Check:  refusingCheck(t, "a run whose branch is gone must not be re-verified"),
		Runner: refusingRunner{t: t, why: "a run whose branch is gone must not spawn an agent"},
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
		Branch: "chore/pika-improve",
		Check:  refusingCheck(t, "a moved repository must not be re-verified"),
		Runner: refusingRunner{t: t, why: "a moved repository must not spawn an agent"},
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
