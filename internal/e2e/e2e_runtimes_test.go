package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// phaseNamesOf is the run's history in the order it was stamped. A run
// with an explorer or a reviewer stamps a phase the pre-M6 lifecycle
// never had, and the order is what says which agent ran when.
func phaseNamesOf(rec runRecord) []string {
	names := make([]string, 0, len(rec.Phases))
	for _, stamp := range rec.Phases {
		names = append(names, stamp.Phase)
	}
	return names
}

// The runtime adapters end to end, through the real binary.
//
// The contract schema has accepted seven runtimes since M1 and the binary
// has spawned one of them, so these tests are what closes that gap at the
// boundary that matters: a real `pika work` run, a real adapter argv, and
// a real receipt.
//
// No model, credential or network is involved — the seven harness binaries
// on the child's PATH are all the same fixture — which is what lets `pika
// check --ci` run this file and still be provably LLM-free.

// agentsBlock is the contract's agents block, naming each role under the
// runtime it runs. It is one function rather than concatenated snippets
// because YAML refuses a second `agents:` key, and the failure is a
// duplicate-key error whose fix is to compose the block in one place.
func agentsBlock(roles ...[2]string) string {
	var b strings.Builder
	b.WriteString("agents:\n")
	for _, role := range roles {
		b.WriteString("  " + role[0] + ":\n    runtime: " + role[1] + "\n")
	}
	return b.String()
}

// contractWithAgents rewrites the scaffolded repository's contract to
// declare the given agents block. The scaffold asserts its own shape
// first: if `pika init` stops emitting `agents: {}`, this fixture has to
// be rewritten rather than silently produce a repository whose runs
// resolve a different cast than the test asked for.
func contractWithAgents(t *testing.T, dir, agents string) {
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
	updated := strings.Replace(string(bs), scaffolded, agents, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A builder on a runtime that is not codex. Before M6 this run was
// refused by name — “agent "builder" uses runtime "claude"; `pika
// improve` requires runtime codex“ — before any process was spawned.
func TestWorkWithAClaudeBuilder(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	dir := scaffoldRepo(t, "go")
	contractWithAgents(t, dir, agentsBlock([2]string{"builder", "claude"}))
	initGitRepo(t, dir)

	side := t.TempDir()
	argvPath := filepath.Join(side, "argv")
	promptPath := filepath.Join(side, "prompt.md")

	out := runCLIEnv(t, dir, agentEnv(
		"FAKE_AGENT_FILE="+agentEditPath,
		"FAKE_AGENT_CONTENT="+agentEditContent,
		"FAKE_AGENT_ARGV="+argvPath,
		"FAKE_AGENT_PROMPT="+promptPath,
	), 0, "work", workGoal, "--json")

	env := unwrap(t, out, "work")
	if !env.OK {
		t.Fatalf("work reported not ok:\n%s", out)
	}
	var result workResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("parse work result: %v\n%s", err, out)
	}
	if result.Commit == "" {
		t.Fatal("the run delivered no commit")
	}

	// The adapter's argv, not codex's: claude takes its prompt on stdin
	// and answers on stdout, and it is asked for the least dangerous
	// auto-approval its CLI offers.
	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("the agent recorded no argv: %v", err)
	}
	for _, want := range []string{"-p", "--output-format", "text", "--permission-mode", "acceptEdits"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("pika spawned claude without %q:\n%s", want, argv)
		}
	}
	if !strings.Contains(string(argv), "--approve-for-me") && strings.Contains(string(argv), "codex") {
		t.Errorf("pika sent codex's argv to a claude builder:\n%s", argv)
	}
	// The prompt still reached the agent, over stdin rather than as an
	// argv element.
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("the agent recorded no prompt: %v", err)
	}
	if !strings.Contains(string(prompt), workGoal) {
		t.Errorf("the goal never reached the agent:\n%s", prompt)
	}

	// The receipt names what ran.
	receipt := readReceipt(t, dir, result.WorkID)
	if len(receipt.Roles) != 1 || receipt.Roles[0].Runtime != "claude" {
		t.Errorf("receipt roles = %+v, want one claude builder", receipt.Roles)
	}
}

// Two runtimes in one run: a codex builder and an omp reviewer. The
// receipt has to name both, and the review has to be recorded as
// advisory rather than as a gate.
func TestTwoRuntimesInOneWorkRun(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	dir := scaffoldRepo(t, "go")
	contractWithAgents(t, dir, agentsBlock([2]string{"builder", "codex"}, [2]string{"reviewer", "omp"}))
	initGitRepo(t, dir)

	side := t.TempDir()
	argvPath := filepath.Join(side, "argv")
	out := runCLIEnv(t, dir, agentEnv(
		"FAKE_AGENT_FILE="+agentEditPath,
		"FAKE_AGENT_CONTENT="+agentEditContent,
		// One file, appended: it then holds both argvs in spawn order,
		// which is the proof that two adapters ran and not one twice.
		"FAKE_AGENT_ARGV="+argvPath,
		"FAKE_AGENT_ARGV_ADD=1",
		"FAKE_AGENT_PROMPT="+filepath.Join(side, "prompt.md"),
		"FAKE_AGENT_MESSAGE=REVIEW-MARKER: nothing blocking.\n",
	), 0, "work", workGoal, "--json")

	env := unwrap(t, out, "work")
	if !env.OK {
		t.Fatalf("work reported not ok:\n%s", out)
	}
	var result workResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("parse work result: %v\n%s", err, out)
	}
	// The review is advisory: the commit landed anyway.
	if result.Commit == "" {
		t.Fatal("the run delivered no commit: the review gated it")
	}

	// Two argvs, in spawn order: codex's, then omp's. A run that drove
	// one adapter twice would show the same argv twice.
	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("the agents recorded no argv: %v", err)
	}
	got := string(argv)
	codex := strings.Index(got, "--approve-for-me")
	omp := strings.Index(got, "--approval-mode")
	if codex < 0 || omp < 0 {
		t.Fatalf("the recorded argv does not name both adapters:\n%s", got)
	}
	if codex >= omp {
		t.Errorf("the argv order is not codex then omp:\n%s", got)
	}
	for _, want := range []string{"exec", "sandbox_workspace_write.network_access=false", "--cd", "--output-last-message"} {
		if !strings.Contains(got, want) {
			t.Errorf("the codex argv is missing %q:\n%s", want, got)
		}
	}
	for _, want := range []string{"-p", "--cwd", "--mode", "text", "--approval-mode", "write", "--no-session", "@"} {
		if !strings.Contains(got, want) {
			t.Errorf("the omp argv is missing %q:\n%s", want, got)
		}
	}

	rec := statusRun(t, dir, result.WorkID)
	if got := phaseNamesOf(rec); !slices.Contains(got, "review") {
		t.Errorf("record phases = %v, want review among them", got)
	}
	if rec.Runtime != "codex" {
		t.Errorf("record runtime = %q, want the builder's codex", rec.Runtime)
	}

	receipt := readReceipt(t, dir, result.WorkID)
	if len(receipt.Roles) != 2 {
		t.Fatalf("receipt roles = %+v, want two", receipt.Roles)
	}
	if receipt.Roles[0].Role != "builder" || receipt.Roles[0].Runtime != "codex" {
		t.Errorf("roles[0] = %+v, want the codex builder", receipt.Roles[0])
	}
	if receipt.Roles[1].Role != "reviewer" || receipt.Roles[1].Runtime != "omp" {
		t.Errorf("roles[1] = %+v, want the omp reviewer", receipt.Roles[1])
	}
	if len(receipt.Review) != 1 {
		t.Fatalf("review = %+v, want one finding", receipt.Review)
	}
	if receipt.Review[0].Disposition != "advisory: recorded, not a gate" {
		t.Errorf("disposition = %q, want advisory", receipt.Review[0].Disposition)
	}
	if !strings.Contains(receipt.Review[0].Finding, "REVIEW-MARKER") {
		t.Errorf("finding = %q, want the reviewer's own words", receipt.Review[0].Finding)
	}
}

// The explorer's message is research the builder is handed. If it does
// not reach the builder's prompt, the phase cost an agent run and bought
// nothing.
func TestExplorerFindingsReachTheBuilderPrompt(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	dir := scaffoldRepo(t, "go")
	contractWithAgents(t, dir, agentsBlock([2]string{"builder", "codex"}, [2]string{"explorer", "gemini"}))
	initGitRepo(t, dir)

	side := t.TempDir()
	argvPath := filepath.Join(side, "argv")
	out := runCLIEnv(t, dir, agentEnv(
		"FAKE_AGENT_FILE="+agentEditPath,
		"FAKE_AGENT_CONTENT="+agentEditContent,
		// The explorer is read-only, and the run refuses it if it is
		// not. One fixture plays both roles here, so the edit is gated
		// on the flag only the codex adapter sends.
		"FAKE_AGENT_EDIT_ON=--approve-for-me",
		"FAKE_AGENT_ARGV="+argvPath,
		"FAKE_AGENT_MESSAGE=EXPLORER-MARKER: the loader lives in internal/config\n",
	), 0, "work", workGoal, "--json")

	env := unwrap(t, out, "work")
	if !env.OK {
		t.Fatalf("work reported not ok:\n%s", out)
	}
	var result workResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("parse work result: %v\n%s", err, out)
	}

	// The builder's own prompt, read out of the bundle the run wrote.
	prompt, err := os.ReadFile(filepath.Join(result.Handoff.Dir, "prompt.md"))
	if err != nil {
		t.Fatalf("the run wrote no builder prompt: %v", err)
	}
	if !strings.Contains(string(prompt), "## Explorer findings") {
		t.Errorf("the builder's prompt has no explorer section:\n%s", prompt)
	}
	if !strings.Contains(string(prompt), "EXPLORER-MARKER") {
		t.Errorf("the explorer's message never reached the builder:\n%s", prompt)
	}
	// The explorer's bundle is under handoff/explore, beside the
	// builder's rather than mixed into it.
	explore := filepath.Join(result.Handoff.Dir, "explore")
	if _, err := os.Stat(filepath.Join(explore, "gemini-last-message.md")); err != nil {
		t.Errorf("the explorer left no bundle in %s: %v", explore, err)
	}
	rec := statusRun(t, dir, result.WorkID)
	if got := phaseNamesOf(rec); !slices.Contains(got, "explore") {
		t.Errorf("record phases = %v, want explore among them", got)
	}
}

// ACP, driven through the real binary against the scripted peer. The
// contract points the runtime at it with `command`, which is the whole
// reason that field exists: ACP is a protocol and not a vendor.
func TestWorkWithAnACPBuilder(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	dir := scaffoldRepo(t, "go")
	contractWithAgents(t, dir,
		"agents:\n  builder:\n    runtime: acp\n    command: "+fakeACPPath+"\n")
	initGitRepo(t, dir)

	side := t.TempDir()
	promptPath := filepath.Join(side, "prompt.md")
	out := runCLIEnv(t, dir, agentEnv(
		"FAKE_AGENT_FILE="+agentEditPath,
		"FAKE_AGENT_CONTENT="+agentEditContent,
		"FAKE_AGENT_PROMPT="+promptPath,
	), 0, "work", workGoal, "--json")

	env := unwrap(t, out, "work")
	if !env.OK {
		t.Fatalf("work reported not ok:\n%s", out)
	}
	var result workResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("parse work result: %v\n%s", err, out)
	}
	if result.Commit == "" {
		t.Fatal("the run delivered no commit")
	}
	// The prompt went out as a session/prompt text block, which is the
	// one way an ACP agent is handed its instructions.
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("the agent recorded no prompt: %v", err)
	}
	if !strings.Contains(string(prompt), workGoal) {
		t.Errorf("the goal never reached the agent:\n%s", prompt)
	}
	receipt := readReceipt(t, dir, result.WorkID)
	if len(receipt.Roles) != 1 || receipt.Roles[0].Runtime != "acp" {
		t.Errorf("receipt roles = %+v, want one acp builder", receipt.Roles)
	}
}

// A contract naming a runtime whose binary is not installed refuses
// before it spawns anything, and the refusal names both the runtime and
// the binary — the two facts an operator needs to fix it.
func TestMissingBinaryRefusesWithAnActionableMessage(t *testing.T) {
	if why := gitAbsent(); why != "" {
		t.Skip(why)
	}
	dir := scaffoldRepo(t, "go")
	contractWithAgents(t, dir, agentsBlock([2]string{"builder", "claude"}))
	initGitRepo(t, dir)

	// A PATH carrying git and nothing else: the run has to reach the
	// handoff before it can discover the harness is missing.
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	out := runCLIEnv(t, dir, []string{"PATH=" + filepath.Dir(gitPath)}, 1, "work", workGoal, "--json")
	message := refusalMessage(t, out, "work")
	for _, want := range []string{`agent "builder"`, `runtime "claude"`, "claude", "on PATH"} {
		if !strings.Contains(message, want) {
			t.Errorf("refusal = %q, want it to name %s", message, want)
		}
	}
}
