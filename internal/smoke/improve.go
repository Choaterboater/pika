package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The repair lifecycle: the one part of pika that spawns an agent, and
// the part every defect closed on 2026-08-30 lived in.
//
// The agent boundary is a binary looked up on PATH, so this drives
// internal/e2e's fake agent — no model, no credential, no network.
// What that fake cannot do is stated where it matters, at the argv
// assertion below: it accepts whatever arguments it is given, so it can
// pin an argv pika must never spawn again, and it can never discover an
// argv the real `codex` would reject.

const (
	// improveBranch is the branch a repair run works in.
	improveBranch = "chore/pika-improve"
	// scaffoldedAgents is what `pika init` writes: which agent a project
	// runs is the project's choice, so a repository that is going to run
	// one has to name it first.
	scaffoldedAgents = "agents: {}\n"
	// builderAgent names it.
	builderAgent = "agents:\n  builder:\n    runtime: codex\n    provider: openai\n    model: gpt-5-codex\n"
)

// The formatting regression each run repairs, and the file it lands in.
// The repaired content is never written down here: it is the scaffold's
// own file, read before the defect overwrites it, so "the agent repaired
// it" means the bytes `pika init` wrote came back rather than that some
// string this program invented did.
const defectiveEntry = "// Command repo is the repo entrypoint.\npackage main\n\nimport \"fmt\"\n\nfunc  main( )  {\n\t\tfmt.Println( \"Hello from repo!\" )\n}\n"

// The second run's defect, in a second file, so the two runs cannot pass
// by repairing the same thing twice.
const (
	greetPath      = "cmd/" + scaffoldName + "/greet.go"
	defectiveGreet = "package main\n\nfunc  greet( ) string {\n\treturn  \"hello\"\n}\n"
	repairedGreet  = "package main\n\nfunc greet() string {\n\treturn \"hello\"\n}\n"
)

// repairRepo scaffolds a repository a repair run can actually happen in:
// the go@1 scaffold, a builder agent on the codex runtime, a formatting
// defect the ladder reports, and a Git repository holding all of it in
// one commit — a run refuses a dirty tree, so the defect has to be
// committed rather than merely present.
//
// It returns the repository, the branch it starts on, and the entry
// file's original content: the bytes the agent will be asked to restore.
func (h *harness) repairRepo(step string) (dir, branch, repaired string, err error) {
	dir, _, err = h.scaffold(step)
	if err != nil {
		return "", "", "", err
	}
	doc, err := readRepo(dir, ".project/contract.yaml")
	if err != nil {
		return "", "", "", err
	}
	if !strings.Contains(doc, scaffoldedAgents) {
		return "", "", "", fmt.Errorf("`pika init` no longer scaffolds %q, so this step cannot name a builder agent:\n%s", scaffoldedAgents, doc)
	}
	if err := writeRepo(dir, ".project/contract.yaml", strings.Replace(doc, scaffoldedAgents, builderAgent, 1)); err != nil {
		return "", "", "", err
	}
	if repaired, err = readRepo(dir, entryPath); err != nil {
		return "", "", "", err
	}
	if err := writeRepo(dir, entryPath, defectiveEntry); err != nil {
		return "", "", "", err
	}
	if branch, err = initGit(dir); err != nil {
		return "", "", "", err
	}
	return dir, branch, repaired, nil
}

// improve drives one `pika improve` through the fake agent, which writes
// content at the repository-relative path.
func (h *harness) improve(dir, path, content string, extra ...string) (result, error) {
	env := h.agentEnv(append([]string{
		"FAKE_AGENT_FILE=" + path,
		"FAKE_AGENT_CONTENT=" + content,
	}, extra...)...)
	return h.run(dir, env, "improve", "--json")
}

// improved decodes a run that was expected to deliver.
//
// A run that stopped is reported by the reason it gave rather than by
// its whole envelope: that payload carries the run's entire baseline
// ladder, and burying the one sentence that says why the run died under
// six gate objects is how a gate stops being read.
func improved(c *check, r result) (improveResult, bool) {
	var res improveResult
	env, ok := decodeEnvelope(c, r, "improve")
	if !ok {
		return res, false
	}
	if !env.OK {
		var f improveFailure
		if err := json.Unmarshal(env.Result, &f); err == nil && f.Error != "" {
			c.failf("`pika improve` stopped instead of delivering: %s\n(run %s, stopped on %q)", f.Error, f.Report.WorkID, f.Report.StoppedOn)
		} else {
			c.failf("`pika improve` reported not ok\n%s", r)
		}
		return res, false
	}
	return res, decodeResult(c, env, r, "improve", &res)
}

// refused decodes a run that was expected to stop, and returns the
// reason it gave.
func refused(c *check, r result) (string, bool) {
	env, ok := decodeEnvelope(c, r, "improve")
	if !ok {
		return "", false
	}
	if env.OK {
		c.failf("`pika improve` reported ok where it had to refuse\n%s", r)
		return "", false
	}
	var f improveFailure
	if !decodeResult(c, env, r, "improve failure", &f) {
		return "", false
	}
	return f.Error, true
}

// stepImprove is the step that spawns an agent, and the only one that
// crosses pika's single external boundary. A repository with a real
// finding in it is handed to the builder, and the run is required to
// deliver a commit on a recheck it verified itself — which is the whole
// four-milestone lifecycle, end to end, in one command.
func stepImprove(h *harness) error {
	c := &check{}
	dir, _, repaired, err := h.repairRepo("improve")
	if err != nil {
		return err
	}

	// The planted defect is asserted to be a real finding before the run
	// touches it. A run against a repository that was already green
	// would satisfy every assertion below without repairing anything.
	if rep, r, ok := runCheck(c, h, dir); ok {
		wantEqual(c, "`pika check --all` exit code with a planted formatting defect", r.exit, 1)
		g := wantGate(c, rep, "format", "fail")
		c.contains("the failing format gate", filepath.ToSlash(g.OutputTail), entryPath)
	}

	argvPath := filepath.Join(h.dir, "improve-argv.txt")
	promptPath := filepath.Join(h.dir, "improve-prompt.md")
	r, err := h.improve(dir, entryPath, repaired,
		"FAKE_AGENT_ARGV="+argvPath,
		"FAKE_AGENT_PROMPT="+promptPath)
	if err != nil {
		return err
	}
	wantEqual(c, "`pika improve` exit code on a repository with one repairable finding", r.exit, 0)
	res, ok := improved(c, r)
	if !ok {
		return c.err()
	}

	c.truef(res.Commit != "", "`pika improve` reported no commit, so nothing was delivered\n%s", r)
	wantEqual(c, "the branch `pika improve` worked in", res.Branch, improveBranch)
	changed := slices.Clone(res.ChangedFiles)
	for i := range changed {
		changed[i] = filepath.ToSlash(changed[i])
	}
	c.truef(slices.Equal(changed, []string{entryPath}),
		"`pika improve` reported changed files %v, want exactly [%s]", changed, entryPath)

	// The commit was made on a recheck the run verified, not because the
	// agent returned.
	if res.ChecksAfter == nil {
		c.failf("`pika improve` delivered a commit and reported no recheck at all\n%s", r)
	} else {
		c.truef(res.ChecksAfter.Pass, "`pika improve` committed on a recheck that was not green\n%s",
			quoteBlock("recheck", res.ChecksAfter.String()))
		wantGate(c, *res.ChecksAfter, "format", "pass")
	}

	// And the commit contains the repaired file. This is the assertion
	// the whole step exists for: not that a command exited 0, but that
	// the bytes an agent wrote are in the object database under the
	// commit pika says it made.
	if res.Commit != "" {
		head, err := git(dir, "rev-parse", improveBranch)
		if err != nil {
			return err
		}
		wantEqual(c, "the head of "+improveBranch+" against the commit `pika improve` reported", head, res.Commit)
		got, err := git(dir, "show", res.Commit+":"+entryPath)
		if err != nil {
			return err
		}
		wantEqual(c, "the content of "+entryPath+" in the delivered commit",
			strings.TrimRight(got, "\n"), strings.TrimRight(repaired, "\n"))
	}

	// The agent was really spawned, with a real argv, and was handed
	// something actionable.
	argvRaw, err := os.ReadFile(argvPath)
	argv := string(argvRaw)
	if err != nil {
		c.failf("the agent recorded no argv at %s, so nothing was spawned: %v\n%s", argvPath, err, r)
		return c.err()
	}
	c.contains("the argv pika spawned the agent with", argv, "exec", "--cd", dir, "--output-last-message")

	// `--sandbox` beside `--approve-for-me` is the pair codex 0.151.0
	// rejects while parsing its own arguments: approve-for-me already
	// runs under the workspace-write sandbox, and spelling it twice made
	// `codex exec` exit 2 before the agent read a byte of the prompt.
	//
	// This pins that pair and nothing more. The fake agent accepts any
	// arguments, so this assertion CANNOT discover a new incompatibility
	// with the real `codex` — only the real binary can reject its own
	// arguments, and a gate that runs on every check must make no model
	// call. What it can do is fail the day the pair comes back.
	lines := strings.Fields(argv)
	c.truef(!slices.Contains(lines, "--sandbox") || !slices.Contains(lines, "--approve-for-me"),
		"pika spawned `codex exec` with both --sandbox and --approve-for-me, which codex rejects while parsing its arguments\n%s",
		quoteBlock("argv", argv))

	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		c.failf("the agent recorded no prompt at %s, so it was handed nothing: %v", promptPath, err)
	} else {
		c.contains("the prompt pika handed the agent", filepath.ToSlash(string(prompt)), "format", entryPath)
	}

	// Finally, the operator's own view: the repository the run delivered
	// is green when they check it themselves.
	if rep, after, ok := runCheck(c, h, dir); ok {
		wantEqual(c, "`pika check --all` exit code in the repository a run delivered", after.exit, 0)
		c.truef(rep.Pass, "the repository a run delivered is not green\n%s", quoteBlock("check report", rep.String()))
	}
	return c.err()
}

// stepImproveAgain covers the branch a finished run leaves behind.
//
// Nothing on any failure path deleted it, so the next run in that
// repository died on Git's own `a branch named 'chore/pika-improve'
// already exists` — exit 128, no remedy named anywhere — and so did
// every run after it. Deleting it automatically is the other wrong
// answer: a run can stop after its commit has landed, and the branch is
// then the only place that work exists.
//
// So both worlds are walked here. A branch holding work nobody else has
// earns a refusal that names what is there and what to do; a branch
// whose work is already in the operator's history is reused without
// ceremony.
func stepImproveAgain(h *harness) error {
	c := &check{}
	dir, base, repaired, err := h.repairRepo("improve-again")
	if err != nil {
		return err
	}

	r, err := h.improve(dir, entryPath, repaired)
	if err != nil {
		return err
	}
	wantEqual(c, "the first `pika improve` exit code", r.exit, 0)
	first, ok := improved(c, r)
	if !ok {
		return c.err()
	}
	c.truef(first.Commit != "", "the first run delivered no commit, so there is no leftover branch to test\n%s", r)
	if first.Commit == "" {
		return c.err()
	}

	// The run leaves the operator on its branch. They go back to where
	// they were, and commit the receipt it issued — a run refuses a
	// dirty tree, and keeping the receipt is what they would do anyway.
	if _, err := git(dir, "switch", base); err != nil {
		return err
	}
	if err := commitAll(dir, "chore: keep the run receipt"); err != nil {
		return err
	}

	// World one: the branch is the only copy of the first run's work.
	// Reusing it would write into that work and deleting it would
	// destroy it, so the run refuses — and the refusal is the whole
	// remedy, because the operator used to get Git's exit 128 instead.
	blocked, err := h.improve(dir, entryPath, repaired)
	if err != nil {
		return err
	}
	wantEqual(c, "`pika improve` exit code against a branch holding unmerged work", blocked.exit, 1)
	if reason, ok := refused(c, blocked); ok {
		c.contains("the refusal over a branch holding unmerged work", reason,
			improveBranch, first.WorkID, first.Commit, "git branch -D "+improveBranch, "--branch")
		c.absent("the refusal over a branch holding unmerged work", reason,
			"a branch named", "exit status 128", "git switch -c")
	}
	// A refusal changes nothing.
	head, err := git(dir, "rev-parse", improveBranch)
	if err != nil {
		return err
	}
	wantEqual(c, "the head of "+improveBranch+" after a refused run", head, first.Commit)
	at, err := git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	wantEqual(c, "the branch the repository is on after a refused run", at, base)

	// World two: the operator merges the run's work the way every
	// forge's merge button does, and keeps the branch. Nothing on it is
	// at risk any more, so the next run takes it and gets on with the
	// job.
	if _, err := git(dir, "merge", "--no-ff", "-m", "chore: merge the run", improveBranch); err != nil {
		return err
	}
	if err := writeRepo(dir, greetPath, defectiveGreet); err != nil {
		return err
	}
	if err := commitAll(dir, "chore: plant a second defect"); err != nil {
		return err
	}
	restart, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}

	r, err = h.improve(dir, greetPath, repairedGreet)
	if err != nil {
		return err
	}
	wantEqual(c, "`pika improve` exit code against a merged leftover branch", r.exit, 0)
	second, ok := improved(c, r)
	if !ok {
		return c.err()
	}
	c.truef(second.Commit != "", "the run over a merged leftover branch delivered no commit\n%s", r)
	wantEqual(c, "the branch the second run worked in", second.Branch, improveBranch)
	c.truef(second.WorkID != first.WorkID, "both runs reported work id %s", second.WorkID)
	if second.Commit != "" {
		// The reused branch was brought onto where THIS run started
		// rather than left standing on the previous run's commit: the
		// record says the run began at restart, and the tree has to
		// agree with it.
		parent, err := git(dir, "rev-parse", second.Commit+"^")
		if err != nil {
			return err
		}
		wantEqual(c, "the parent of the second run's commit", parent, restart)
		got, err := git(dir, "show", second.Commit+":"+greetPath)
		if err != nil {
			return err
		}
		wantEqual(c, "the content of "+greetPath+" in the second run's commit",
			strings.TrimRight(got, "\n"), strings.TrimRight(repairedGreet, "\n"))
	}
	return c.err()
}
