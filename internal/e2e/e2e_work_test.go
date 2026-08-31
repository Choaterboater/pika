package e2e

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Choaterboater/pika/internal/evidence"
)

// The durable work lifecycle end to end, through the real binary and the
// real agent boundary.
//
// `pika work` spawns its agent as a `codex` binary looked up on PATH, so
// these tests build a fake one (testdata/fakecodex) and put it at the
// front of the child's PATH. Everything else — the ladder, the run
// record, the branch, the commit, the receipt — is production code doing
// what it does in the field, and no model, credential or network is
// involved anywhere, which is what lets `pika check --ci` run this suite
// and still be provably LLM-free.
//
// Every run happens inside a temp repository. verify.Run's re-entrancy
// guard is scoped to the tree under verification, so a ladder run against
// a temp repo is fine even while pika's own suite runs under pika's own
// ladder; pointing one of these at the pika checkout would be exactly the
// runaway that guard refuses.

// agentEditPath and agentEditContent are the edit the fake agent makes.
// A markdown file keeps the repository's go@1 ladder green through the
// recheck — the run must be delivered on the strength of a passing
// ladder, not on a gate that quietly skipped.
const (
	agentEditPath    = "NOTES.md"
	agentEditContent = "# Notes\n\nWritten by the run's agent.\n"
)

// workGoal is the goal every feature run here is given. The fake agent
// records the prompt it was handed, so this string is also the assertion
// that what the operator typed reached the agent.
const workGoal = "record a NOTES.md the ladder can verify"

// improveBranch is the branch `pika work` and `pika resume` share. resume
// has no --branch of its own, so a resumed run landing anywhere else
// would be a silent redirection of the operator's work.
const improveBranch = "chore/pika-improve"

// gitAbsent reports why the work lifecycle cannot be exercised here. The
// lifecycle is built on Git — it branches, commits and reads HEAD — so
// without it there is nothing to test rather than something failing.
func gitAbsent() string {
	if _, err := exec.LookPath("git"); err != nil {
		return "git not in PATH"
	}
	return ""
}

// codexEnv puts the fake agent on PATH ahead of anything else and adds
// the scenario the test wants it to play.
func codexEnv(extra ...string) []string {
	env := []string{"PATH=" + fakeCodexDir + string(os.PathListSeparator) + os.Getenv("PATH")}
	return append(env, extra...)
}

// builderAgent is the contract entry a run resolves before it spawns
// anything. `pika init` scaffolds `agents: {}` on purpose — which agent a
// project runs is the project's choice — so a repository that is going to
// run one has to name it first.
const builderAgent = "agents:\n  builder:\n    runtime: codex\n    provider: openai\n    model: gpt-5-codex\n"

// workRepo scaffolds a repository a run can actually happen in: the go@1
// scaffold whose ladder is green, a `builder` agent on the codex runtime,
// and a Git repository holding all of it in one commit.
func workRepo(t *testing.T) string {
	t.Helper()
	dir := scaffoldRepo(t, "go")
	addBuilderAgent(t, dir)
	initGitRepo(t, dir)
	return dir
}

// addBuilderAgent names the agent in the scaffolded contract. The
// scaffolded form is asserted rather than assumed: if init stops emitting
// it, this fixture must be rewritten rather than silently produce a
// repository whose runs cannot start.
func addBuilderAgent(t *testing.T, dir string) {
	t.Helper()
	const scaffolded = "agents: {}\n"
	path := filepath.Join(dir, ".project", "contract.yaml")
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bs), scaffolded) {
		t.Fatalf("%s no longer scaffolds %q:\n%s", path, scaffolded, bs)
	}
	updated := strings.Replace(string(bs), scaffolded, builderAgent, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

// initGitRepo turns the scaffold into a repository with one commit. The
// identity is configured locally rather than inherited: a machine with no
// global Git identity is a machine where `git commit` fails, and that is
// a property of the test host, not of pika.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "e2e@pika.invalid"},
		{"config", "user.name", "pika end-to-end"},
		{"config", "commit.gpgsign", "false"},
		{"add", "-A"},
		{"commit", "-m", "chore: scaffold"},
	} {
		git(t, dir, args...)
	}
}

// git runs one Git command in dir and returns its trimmed output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitIgnored reports whether Git would refuse to track path. This is how
// the tests state the local-versus-committed split as a fact about the
// repository rather than as a claim about a directory name.
func gitIgnored(t *testing.T, dir, path string) bool {
	t.Helper()
	cmd := exec.Command("git", "check-ignore", "--quiet", "--", path)
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git check-ignore %s in %s: %v", path, dir, err)
	return false
}

// workResult mirrors the improve.Result payload `work` and `resume` nest
// under the envelope. It is declared here rather than imported so these
// tests assert the wire shape an outside consumer sees.
type workResult struct {
	WorkID       string   `json:"workId"`
	Branch       string   `json:"branch"`
	Commit       string   `json:"commit"`
	ChangedFiles []string `json:"changedFiles"`
	Handoff      struct {
		Dir string `json:"dir"`
	} `json:"handoff"`
}

// runRecord mirrors the durable run record `pika status` reports, in the
// record's own field names.
type runRecord struct {
	WorkID     string `json:"work_id"`
	Goal       string `json:"goal"`
	Kind       string `json:"kind"`
	Phase      string `json:"phase"`
	Branch     string `json:"branch"`
	BaseCommit string `json:"base_commit"`
	Commit     string `json:"commit"`
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason"`
	Phases     []struct {
		Phase string `json:"phase"`
		Note  string `json:"note"`
	} `json:"phases"`
}

// statusRuns unwraps `pika status --json` into the listing it reports.
func statusRuns(t *testing.T, dir string) []runRecord {
	t.Helper()
	env := unwrap(t, runCLI(t, dir, 0, "status", "--json"), "status")
	var payload struct {
		Runs []runRecord `json:"runs"`
	}
	if err := json.Unmarshal(env.Result, &payload); err != nil {
		t.Fatalf("parse status listing: %v", err)
	}
	return payload.Runs
}

// statusRun unwraps `pika status <work-id> --json` into the one run it
// reports.
func statusRun(t *testing.T, dir, workID string) runRecord {
	t.Helper()
	env := unwrap(t, runCLI(t, dir, 0, "status", workID, "--json"), "status")
	var payload struct {
		Run runRecord `json:"run"`
	}
	if err := json.Unmarshal(env.Result, &payload); err != nil {
		t.Fatalf("parse status run: %v", err)
	}
	return payload.Run
}

// readReceipt loads .project/evidence/<work-id>.json and validates it
// against the embedded schema. Validation is the point of the assertion:
// the kernel now issues this document itself, so nothing outside pika
// checked its shape before it landed in committed content.
func readReceipt(t *testing.T, dir, workID string) evidence.Receipt {
	t.Helper()
	path := filepath.Join(dir, ".project", "evidence", workID+".json")
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the run issued no receipt: %v", err)
	}
	var receipt evidence.Receipt
	if err := json.Unmarshal(bs, &receipt); err != nil {
		t.Fatalf("receipt %s is not JSON: %v\n%s", path, err, bs)
	}
	if err := evidence.Validate(&receipt); err != nil {
		t.Fatalf("receipt %s is not schema-valid: %v\n%s", path, err, bs)
	}
	return receipt
}

// TestE2EWorkDeliversAVerifiedCommitAndAReceipt runs the whole feature
// lifecycle through the real binary: `pika work` with a goal, a real
// agent process at the real boundary, a verified commit on the run's
// branch, a durable run record, and the receipt the kernel now issues
// itself.
//
// It also pins the split the record and the receipt live on either side
// of. `.project/state/work/` is local operational state and must be
// ignored; `.project/evidence/` is the public attestation and must be
// committable. Asking Git rather than reading a path prefix is the
// difference between testing the guarantee and testing a string.
func TestE2EWorkDeliversAVerifiedCommitAndAReceipt(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	dir := workRepo(t)
	base := git(t, dir, "rev-parse", "HEAD")
	side := t.TempDir()
	promptPath := filepath.Join(side, "prompt.md")
	argvPath := filepath.Join(side, "argv")

	out := runCLIEnv(t, dir, codexEnv(
		"FAKE_CODEX_FILE="+agentEditPath,
		"FAKE_CODEX_CONTENT="+agentEditContent,
		"FAKE_CODEX_PROMPT="+promptPath,
		"FAKE_CODEX_ARGV="+argvPath,
	), 0, "work", workGoal, "--json")

	env := unwrap(t, out, "work")
	if !env.OK {
		t.Fatalf("work reported not ok:\n%s", out)
	}
	var result workResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("parse work result: %v\n%s", err, out)
	}
	if result.Branch != improveBranch {
		t.Errorf("branch = %q, want %q", result.Branch, improveBranch)
	}
	if want := []string{agentEditPath}; !slices.Equal(result.ChangedFiles, want) {
		t.Errorf("changedFiles = %v, want %v", result.ChangedFiles, want)
	}
	if result.Commit == "" {
		t.Fatal("work reported no commit")
	}
	// The commit is the branch head, and it sits on top of where the run
	// started: the lifecycle commits once, onto a branch it created at
	// its own base commit.
	if head := git(t, dir, "rev-parse", improveBranch); head != result.Commit {
		t.Errorf("branch %s is at %s, but the run reported commit %s", improveBranch, head, result.Commit)
	}
	if parent := git(t, dir, "rev-parse", result.Commit+"^"); parent != base {
		t.Errorf("the delivered commit's parent is %s, want the run's base commit %s", parent, base)
	}

	// The goal the operator typed is what the agent was asked to do.
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("the agent recorded no prompt: %v", err)
	}
	if !strings.Contains(string(prompt), workGoal) {
		t.Errorf("the goal never reached the agent's prompt:\n%s", prompt)
	}
	// And it was asked under the sandbox the production runner promises.
	//
	// workspace-write is spelled once, not twice. codex rejects
	// `--sandbox` alongside `--approve-for-me` — approve-for-me already
	// runs the model's shell commands under the workspace-write sandbox
	// — so sending both made `codex exec` exit 2 while parsing its own
	// arguments, and every handoff died before the agent read a byte of
	// the prompt. The absence of `--sandbox` is asserted here for the
	// same reason its presence used to be: this is the boundary where a
	// wrong argv costs a whole run.
	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("the agent recorded no argv: %v", err)
	}
	for _, want := range []string{
		"sandbox_workspace_write.network_access=false",
		"--approve-for-me",
	} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("pika spawned the agent without %q:\n%s", want, argv)
		}
	}
	for _, line := range strings.Split(string(argv), "\n") {
		if strings.TrimSpace(line) == "--sandbox" {
			t.Errorf("pika spawned the agent with --sandbox, which codex refuses next to --approve-for-me:\n%s", argv)
		}
	}

	// The record is durable, complete, and local.
	rec := statusRun(t, dir, result.WorkID)
	if rec.Kind != "feature" {
		t.Errorf("kind = %q, want feature", rec.Kind)
	}
	if rec.Goal != workGoal {
		t.Errorf("goal = %q, want %q", rec.Goal, workGoal)
	}
	if rec.Outcome != "complete" {
		t.Errorf("outcome = %q, want complete", rec.Outcome)
	}
	if rec.Phase != "deliver" {
		t.Errorf("phase = %q, want deliver", rec.Phase)
	}
	if rec.Commit != result.Commit {
		t.Errorf("record commit = %q, want %q", rec.Commit, result.Commit)
	}
	recordPath := filepath.Join(".project", "state", "work", result.WorkID, "record.json")
	if _, err := os.Stat(filepath.Join(dir, recordPath)); err != nil {
		t.Fatalf("the run left no durable record: %v", err)
	}
	if !gitIgnored(t, dir, recordPath) {
		t.Errorf("%s is not gitignored; run records are local operational state", recordPath)
	}

	// The receipt is schema-valid, attests this run, and is committable.
	receipt := readReceipt(t, dir, result.WorkID)
	if receipt.WorkID != result.WorkID {
		t.Errorf("receipt work_id = %q, want %q", receipt.WorkID, result.WorkID)
	}
	if receipt.Commit != result.Commit {
		t.Errorf("receipt commit = %q, want %q", receipt.Commit, result.Commit)
	}
	if !receipt.Completion.Complete {
		t.Errorf("receipt completion = %+v, want complete", receipt.Completion)
	}
	if len(receipt.Roles) != 1 || receipt.Roles[0].Role != "builder" || receipt.Roles[0].Runtime != "codex" {
		t.Errorf("receipt roles = %+v, want the builder that actually ran", receipt.Roles)
	}
	if len(receipt.ChangedFiles) != 1 || receipt.ChangedFiles[0].Path != agentEditPath {
		t.Errorf("receipt changed_files = %+v, want %s", receipt.ChangedFiles, agentEditPath)
	}
	if len(receipt.Commands) == 0 {
		t.Error("receipt records no commands; the ladder it attests ran nothing")
	}
	receiptPath := filepath.Join(".project", "evidence", result.WorkID+".json")
	if gitIgnored(t, dir, receiptPath) {
		t.Errorf("%s is gitignored; evidence receipts are committed attestation", receiptPath)
	}

	// A finished run is not an interrupted one, and resume says so rather
	// than starting the work again.
	refusal := runCLIEnv(t, dir, codexEnv(), 2, "resume", result.WorkID, "--json")
	denied := unwrap(t, refusal, "resume")
	if denied.OK || denied.Error == nil {
		t.Fatalf("resume accepted a finished run:\n%s", refusal)
	}
	if !strings.Contains(denied.Error.Message, "terminal outcome") {
		t.Errorf("resume refusal = %q, want it to name the terminal outcome", denied.Error.Message)
	}
	if runs := statusRuns(t, dir); len(runs) != 1 {
		t.Errorf("the refused resume left %d runs, want 1", len(runs))
	}
}

// TestE2EInterruptedRunIsVisibleInStatusAndResumable is the milestone's
// whole claim, driven through the real CLI.
//
// The interruption is real: the agent signals that its edit is on disk
// and then blocks, and the test kills the `pika work` process while it
// waits. That leaves exactly what a crash leaves — a record with no
// terminal outcome, a branch holding uncommitted work — and no fixture
// pretending to be one.
//
// What has to be true afterwards is that an operator can find the run,
// clear what the crash left behind, and finish it: `pika status` shows
// it, `pika recover` releases the lease the killed process never gave
// back, and `pika resume` carries that same run to a verified commit
// and a receipt under its own work id. A resume that started a second
// run would leave the first one stranded forever, so the run count is
// asserted as carefully as the outcome.
//
// The recover step is not bookkeeping the test invented to get past a
// refusal. `pika resume` will not take a lease it did not take itself —
// a record saying "interrupted" is bit for bit what a run still working
// leaves behind — so clearing it is a decision, made by an operator,
// with one command. That is the real workflow after a crash, and this
// is what it costs.
func TestE2EInterruptedRunIsVisibleInStatusAndResumable(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	if runtime.GOOS == "windows" {
		// The crashed run's lease can only be cleared once its holder is
		// proved gone, and processAlive reports every positive pid as
		// alive on Windows. There the operator removes the file by hand,
		// which is a documented gap rather than a failure here.
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
	waitForFile(t, started, "the agent to put its edit in the repository", log)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("interrupt pika: %v", err)
	}
	// The kill is the interruption; Wait only reaps it.
	_ = cmd.Wait()
	// Release the agent pika left behind rather than let it sit out its
	// own timeout after the process that was waiting for it is gone.
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// Visible.
	runs := statusRuns(t, dir)
	if len(runs) != 1 {
		t.Fatalf("status reports %d runs after one interrupted run, want 1:\n%+v", len(runs), runs)
	}
	interrupted := runs[0]
	if interrupted.Outcome != "" {
		t.Fatalf("the interrupted run recorded outcome %q; a killed process settles nothing", interrupted.Outcome)
	}
	if interrupted.Branch != improveBranch {
		t.Fatalf("the interrupted run's record names branch %q, want %q; resume has nothing to rejoin without it",
			interrupted.Branch, improveBranch)
	}
	if interrupted.Goal != workGoal {
		t.Errorf("the interrupted run's record lost its goal: %q", interrupted.Goal)
	}
	// The human listing has to show it too — the operator arriving after a
	// crash runs `pika status`, not `pika status --json`.
	if listing := runCLI(t, dir, 0, "status"); !strings.Contains(listing, interrupted.WorkID) {
		t.Errorf("the human listing does not show the interrupted run:\n%s", listing)
	}

	// Refused first, so what recover is for is stated rather than
	// assumed: the killed run never gave its lease back, and resume
	// takes no lease it did not take itself.
	refused := runCLIEnv(t, dir, codexEnv(), 1, "resume", interrupted.WorkID, "--json")
	if why := refusalMessage(t, refused, "resume"); !strings.Contains(why, interrupted.WorkID) {
		t.Errorf("the refusal does not name the holder: %q", why)
	}

	// And cleared, by the one command that clears it. This is the whole
	// cost of a crash: a lease is never stolen, so an operator decides.
	cleared := runCLI(t, dir, 0, "recover", "--apply")
	if !strings.Contains(cleared, "cleared the run lease") {
		t.Fatalf("recover did not clear the interrupted run's lease:\n%s", cleared)
	}
	// Clearing the lease is not abandoning the run: the record recover
	// left alone is the one resume is about to rejoin.
	if runs := statusRuns(t, dir); len(runs) != 1 {
		t.Fatalf("recover left %d runs, want the interrupted one untouched:\n%+v", len(runs), runs)
	}

	// Resumable, under the same work id.
	out := runCLIEnv(t, dir, codexEnv(
		"FAKE_CODEX_FILE="+agentEditPath,
		"FAKE_CODEX_CONTENT="+agentEditContent,
	), 0, "resume", interrupted.WorkID, "--json")
	env := unwrap(t, out, "resume")
	if !env.OK {
		t.Fatalf("resume reported not ok:\n%s", out)
	}
	var result workResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("parse resume result: %v\n%s", err, out)
	}
	if result.WorkID != interrupted.WorkID {
		t.Fatalf("resume reported run %q, want the interrupted %q", result.WorkID, interrupted.WorkID)
	}
	if result.Branch != improveBranch {
		t.Errorf("the resumed run landed on %q, want the branch its record named, %q", result.Branch, improveBranch)
	}
	if result.Commit == "" {
		t.Fatal("the resumed run reached no commit")
	}
	if head := git(t, dir, "rev-parse", improveBranch); head != result.Commit {
		t.Errorf("branch %s is at %s, but resume reported commit %s", improveBranch, head, result.Commit)
	}

	// One run, finished, and its history says a second process finished it.
	if runs := statusRuns(t, dir); len(runs) != 1 {
		t.Fatalf("resume left %d runs, want the one it rejoined:\n%+v", len(runs), runs)
	}
	rec := statusRun(t, dir, interrupted.WorkID)
	if rec.Outcome != "complete" {
		t.Errorf("outcome = %q, want complete", rec.Outcome)
	}
	if rec.Commit != result.Commit {
		t.Errorf("record commit = %q, want %q", rec.Commit, result.Commit)
	}
	resumed := false
	for _, stamp := range rec.Phases {
		if stamp.Note == "resumed" {
			resumed = true
			break
		}
	}
	if !resumed {
		t.Errorf("no phase is marked resumed; the history cannot say a second process finished this run:\n%+v", rec.Phases)
	}

	// The receipt is issued under the interrupted run's own id.
	receipt := readReceipt(t, dir, interrupted.WorkID)
	if receipt.Commit != result.Commit {
		t.Errorf("receipt commit = %q, want %q", receipt.Commit, result.Commit)
	}
	if !receipt.Completion.Complete {
		t.Errorf("receipt completion = %+v, want complete", receipt.Completion)
	}
}

// startCLI starts the binary in dir without waiting for it, so a test can
// interrupt it. It returns the process and a reader for everything the
// process wrote.
//
// stdout and stderr are real files rather than in-memory buffers on
// purpose. exec copies an io.Writer through a pipe and a goroutine, and
// Wait blocks until every writer of that pipe has closed — including the
// agent pika spawned, which outlives the kill. An inherited file has no
// such wait, so Wait returns as soon as pika itself is gone.
func startCLI(t *testing.T, dir string, env []string, args ...string) (*exec.Cmd, func() string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "pika.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logFile.Close() })
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start pika %v: %v", args, err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd, func() string {
		bs, err := os.ReadFile(logPath)
		if err != nil {
			return "<no output: " + err.Error() + ">"
		}
		return string(bs)
	}
}

// waitForFile blocks until path exists. The timeout is generous because
// what it is waiting behind is a full ladder run on the test host, and a
// deadline tuned to a fast machine turns a slow one into a flake.
func waitForFile(t *testing.T, path, why string, log func() string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s:\n%s", why, log())
}

// A leftover run branch used to poison a repository permanently.
//
// Every run that reaches its handoff creates `chore/pika-improve`, and
// nothing on any path deletes it. Once one was there, the next run in
// that repository died on Git's own `a branch named 'chore/pika-improve'
// already exists` — exit 128, no remedy named anywhere — and so did
// every run after it, until an operator happened to know that the fix
// was to delete a branch by hand.
//
// The two tests below are the two worlds a leftover can be in, driven
// through the real binary: one where the branch carries nothing anybody
// can lose, and one where it is the only copy of a run's committed work.

// The branch is still there, and the work it carried is in the
// operator's own history: they merged it and did not delete the branch,
// which is what every forge's merge button leaves behind by default.
// Nothing on that branch is at risk, so the next run takes it and gets
// on with the job.
func TestE2EAMergedRunBranchDoesNotBlockTheNextRun(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	dir := workRepo(t)
	first := runWorkAgent(t, dir, 0, agentEditPath, agentEditContent)
	if first.Commit == "" {
		t.Fatalf("the first run made no commit: %+v", first)
	}

	// The operator merges the run's work, keeps the receipt it issued,
	// and carries on. The branch stays where it was.
	git(t, dir, "switch", "main")
	git(t, dir, "merge", "--ff-only", improveBranch)
	git(t, dir, "add", ".project/evidence")
	git(t, dir, "commit", "-m", "chore: keep the run receipt")
	if git(t, dir, "branch", "--list", improveBranch) == "" {
		t.Fatal("the merged branch is gone, so there is no leftover to test")
	}
	base := git(t, dir, "rev-parse", "HEAD")

	second := runWorkAgent(t, dir, 0, "SECOND.md", "# Second\n\nA later run's edit.\n")
	if second.Commit == "" {
		t.Fatalf("the second run made no commit: %+v", second)
	}
	if second.Branch != improveBranch {
		t.Errorf("branch = %q, want %q", second.Branch, improveBranch)
	}
	// The reused branch was brought onto where THIS run started rather
	// than left standing on the previous run's commit: the record says
	// the run began at base, and the tree has to agree with it.
	if parent := git(t, dir, "rev-parse", second.Commit+"^"); parent != base {
		t.Errorf("the delivered commit's parent is %s, want the second run's base commit %s", parent, base)
	}
	if second.WorkID == first.WorkID {
		t.Errorf("both runs reported work id %s", second.WorkID)
	}
}

// The branch holds a commit that exists nowhere else, because the
// operator has not published or merged it — which is the state a
// delivered run is designed to leave, since publishing is a human
// choice. Reusing the branch would write into that work and deleting it
// would destroy it, so the run refuses, and the refusal says what is
// there, which run put it there, and what the operator can do.
func TestE2EAnUnmergedRunBranchRefusesTheNextRunAndNamesTheRemedy(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	dir := workRepo(t)
	first := runWorkAgent(t, dir, 0, agentEditPath, agentEditContent)
	if first.Commit == "" {
		t.Fatalf("the first run made no commit: %+v", first)
	}
	git(t, dir, "switch", "main")
	git(t, dir, "add", ".project/evidence")
	git(t, dir, "commit", "-m", "chore: keep the run receipt")

	out := runCLIEnv(t, dir, codexEnv(
		"FAKE_CODEX_FILE=SECOND.md",
		"FAKE_CODEX_CONTENT=# Second\n",
	), 1, "work", workGoal, "--json")
	env := unwrap(t, out, "work")
	if env.OK {
		t.Fatalf("work reported ok on a branch holding unmerged work:\n%s", out)
	}
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(env.Result, &failure); err != nil {
		t.Fatalf("parse work failure: %v\n%s", err, out)
	}
	// The old failure was Git's, and it named nothing an operator could
	// act on. Every one of these is a thing they now do not have to
	// already know.
	for _, want := range []string{
		improveBranch,
		first.WorkID,
		first.Commit,
		"git branch -D " + improveBranch,
		"--branch",
	} {
		if !strings.Contains(failure.Error, want) {
			t.Errorf("error = %q, want it to name %q", failure.Error, want)
		}
	}
	// And what an operator used to get instead: Git's own `fatal: a
	// branch named 'chore/pika-improve' already exists`, arriving as a
	// wrapped exit-128 git error with no remedy anywhere in it.
	for _, gone := range []string{"a branch named", "exit status 128", "git switch -c"} {
		if strings.Contains(failure.Error, gone) {
			t.Errorf("error = %q still carries Git's bare refusal %q", failure.Error, gone)
		}
	}

	// A refusal changes nothing. The branch still holds the first run's
	// commit and the operator is still standing where they were.
	if head := git(t, dir, "rev-parse", improveBranch); head != first.Commit {
		t.Errorf("branch %s is at %s, want the first run's commit %s untouched", improveBranch, head, first.Commit)
	}
	if got := git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("HEAD is on %q, want main: a refused run must not move the tree", got)
	}

	// And the remedy the refusal named actually works: the operator
	// deletes the branch, and the repository runs again.
	git(t, dir, "branch", "-D", improveBranch)
	again := runWorkAgent(t, dir, 0, "SECOND.md", "# Second\n\nA later run's edit.\n")
	if again.Commit == "" {
		t.Fatalf("the run after the named remedy made no commit: %+v", again)
	}
}

// runWorkAgent drives one `pika work` through the fake agent and returns
// the result it reported.
func runWorkAgent(t *testing.T, dir string, wantExit int, path, content string) workResult {
	t.Helper()
	out := runCLIEnv(t, dir, codexEnv(
		"FAKE_CODEX_FILE="+path,
		"FAKE_CODEX_CONTENT="+content,
	), wantExit, "work", workGoal, "--json")
	env := unwrap(t, out, "work")
	if !env.OK {
		t.Fatalf("work reported not ok:\n%s", out)
	}
	var result workResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("parse work result: %v\n%s", err, out)
	}
	return result
}
