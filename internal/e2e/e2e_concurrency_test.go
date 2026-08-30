package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The hazard M4 exists to close, driven through the real binary: one
// user, two terminals, one repository. Both runs would edit one working
// tree and move one HEAD, and neither would know the other was there.
//
// Everything here reuses the work lifecycle's fixtures next door — the
// scaffolded repository, the fake agent on PATH, the real ladder — so
// what is being excluded is a genuine `pika work`, not a stand-in.

// refusalMessage returns why a run was refused. `work` and `resume`
// report a refusal as a not-ok envelope whose reason travels inside the
// result, beside whatever a stopped run can still say about where it
// stopped, so the message is read from there rather than from the
// envelope's own error field.
func refusalMessage(t *testing.T, out, command string) string {
	t.Helper()
	env := unwrap(t, out, command)
	if env.OK {
		t.Fatalf("%s reported ok; want a refusal:\n%s", command, out)
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(env.Result, &payload); err != nil {
		t.Fatalf("parse the %s refusal: %v\n%s", command, err, out)
	}
	if payload.Error == "" {
		t.Fatalf("%s refused without saying why:\n%s", command, out)
	}
	return payload.Error
}

// runLeaseFile is the whole-repository run lease, spelled out rather
// than asked for. improve.RunLease is the one definition the product
// uses precisely so nothing drifts; a test that asked it the same
// question would follow a move instead of catching it.
func runLeaseFile(dir string) string {
	return filepath.Join(dir, ".project", "state", "run.lock")
}

// TestE2ETwoConcurrentRunsAreExcludedAndTheFirstCompletes is the
// milestone's claim at its narrowest: while one `pika work` is inside
// the repository, nothing else may be.
//
// The first run is held mid-flight by an agent that signals and then
// blocks, so the second invocation meets a real live holder rather than
// a fixture. Three things have to be true at that moment. The second
// run must refuse and name the holder — an "already running" that says
// nothing leaves the operator with no next move. `pika recover` must
// refuse too, because a recovery that cleared the lease a live run is
// inside would be the corruption it exists to prevent, dressed as a
// remedy. And the first run must then finish normally: an exclusion
// that damaged the run holding it would be worse than none.
func TestE2ETwoConcurrentRunsAreExcludedAndTheFirstCompletes(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	dir := workRepo(t)
	side := t.TempDir()
	// The first run is held before its agent edits anything, so the
	// second one meets a live lease rather than the dirty working tree
	// an agent's half-finished edit would leave. Both are refusals, and
	// only one of them is this milestone's.
	spawned := filepath.Join(side, "agent-spawned")
	proceed := filepath.Join(side, "agent-proceed")

	first, log := startCLI(t, dir, codexEnv(
		"FAKE_CODEX_FILE="+agentEditPath,
		"FAKE_CODEX_CONTENT="+agentEditContent,
		"FAKE_CODEX_SPAWNED="+spawned,
		"FAKE_CODEX_WAIT="+proceed,
	), "work", workGoal)
	waitForFile(t, spawned, "the first run's agent to reach the repository", log)

	// The second terminal. It must not spawn an agent, must not touch
	// the tree, and must say who is in there.
	refusal := refusalMessage(t,
		runCLIEnv(t, dir, codexEnv(), 1, "work", "a second goal in the same repository", "--json"),
		"work")
	for _, want := range []string{"another run holds this repository", "pid ", "in progress"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("the refusal = %q, want it to contain %q", refusal, want)
		}
	}
	holder := statusRuns(t, dir)
	if len(holder) != 1 {
		t.Fatalf("the refused run left %d runs, want only the one holding the repository:\n%+v", len(holder), holder)
	}
	if !strings.Contains(refusal, holder[0].WorkID) {
		t.Errorf("the refusal = %q, want it to name run %s so `pika status` can be pointed at it", refusal, holder[0].WorkID)
	}
	t.Logf("second concurrent run refused with: %s", refusal)

	// Recovery is not a way around it either. A lease whose holder is
	// running is never cleared, whatever the operator asked for.
	denied := unwrap(t, runCLIEnv(t, dir, codexEnv(), 2, "recover", "--apply", "--json"), "recover")
	if denied.OK || denied.Error == nil {
		t.Fatal("recover --apply cleared the lease a live run is inside")
	}
	if !strings.Contains(denied.Error.Message, holder[0].WorkID) {
		t.Errorf("recover --apply refusal = %q, want it to name the holder", denied.Error.Message)
	}
	if _, err := os.Stat(runLeaseFile(dir)); err != nil {
		t.Fatalf("recover cleared a live run's lease: %v", err)
	}

	// And the run that held it all along finishes normally.
	if err := os.WriteFile(proceed, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err != nil {
		t.Fatalf("the first run did not complete: %v\n%s", err, log())
	}
	// Its own record and Git are the assertion rather than the process
	// output: the run's stdout and stderr share one inherited file here,
	// so what the operator would see is checked where it is unambiguous.
	runs := statusRuns(t, dir)
	if len(runs) != 1 {
		t.Fatalf("status reports %d runs, want the one that actually ran:\n%+v", len(runs), runs)
	}
	done := runs[0]
	if done.WorkID != holder[0].WorkID {
		t.Fatalf("status reports run %s, want the one that held the repository, %s", done.WorkID, holder[0].WorkID)
	}
	if done.Outcome != "complete" {
		t.Fatalf("the first run's outcome = %q, want complete; the exclusion cost the run that held it:\n%s", done.Outcome, log())
	}
	if done.Commit == "" {
		t.Fatal("the first run reached no commit")
	}
	if head := git(t, dir, "rev-parse", improveBranch); head != done.Commit {
		t.Errorf("branch %s is at %s, but the run recorded commit %s", improveBranch, head, done.Commit)
	}
	// The lease is given back on the way out. A run that finished
	// holding its exclusion would wedge the repository as thoroughly as
	// one that crashed.
	if _, err := os.Stat(runLeaseFile(dir)); !os.IsNotExist(err) {
		t.Errorf("the finished run left its lease behind (%v); the next run is locked out", err)
	}
}

// TestE2ERecoverClearsALeaseWhoseRunWasKilled is the other half: the
// exclusion holds, so a process killed inside it leaves the repository
// locked out, and there has to be a supported way back.
//
// This is the dead end `pika recover` was built for one layer down. In
// M2 a killed `pika apply` could only be cleared by deleting a file by
// hand; shipping a run lease without extending recover to it would
// reintroduce exactly that, one layer up.
func TestE2ERecoverClearsALeaseWhoseRunWasKilled(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	if runtime.GOOS == "windows" {
		// processAlive reports every positive pid as alive on Windows,
		// so no lease is ever provably stale there and the operator
		// removes the file by hand. A documented gap, not a failure.
		t.Skip("a killed run's lease cannot be proved stale on Windows")
	}
	dir := workRepo(t)
	side := t.TempDir()
	started := filepath.Join(side, "agent-started")
	release := filepath.Join(side, "agent-release")

	cmd, log := startCLI(t, dir, codexEnv(
		"FAKE_CODEX_FILE="+agentEditPath,
		"FAKE_CODEX_CONTENT="+agentEditContent,
		"FAKE_CODEX_STARTED="+started,
		"FAKE_CODEX_HANG="+release,
	), "work", workGoal)
	waitForFile(t, started, "the agent to reach the repository", log)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("interrupt pika: %v", err)
	}
	_ = cmd.Wait()
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// The report first, and it changes nothing. An operator locked out
	// of their own repository has to be able to see what recovery would
	// do before authorizing it.
	report := runCLI(t, dir, 0, "recover")
	for _, want := range []string{"run.lock", "stale", "whole repository"} {
		if !strings.Contains(report, want) {
			t.Errorf("the recover report = %q, want it to name %q", report, want)
		}
	}
	if _, err := os.Stat(runLeaseFile(dir)); err != nil {
		t.Fatalf("the report cleared the lease: %v", err)
	}

	applied := runCLI(t, dir, 0, "recover", "--apply")
	if !strings.Contains(applied, "cleared the run lease") {
		t.Fatalf("recover --apply did not clear the crashed run's lease:\n%s", applied)
	}
	if _, err := os.Stat(runLeaseFile(dir)); !os.IsNotExist(err) {
		t.Fatalf("the lease survived recovery (%v); the repository is still locked out", err)
	}
	// Cleared, and the repository takes a run again — which is the only
	// thing the operator actually wanted.
	if runs := statusRuns(t, dir); len(runs) != 1 {
		t.Fatalf("recover left %d runs, want the interrupted one untouched:\n%+v", len(runs), runs)
	}
}
