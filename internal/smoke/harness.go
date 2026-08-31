package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// The harness: the binaries under test, the temp world they run in, and
// the two subprocess boundaries every step crosses (pika, and git).
//
// Everything here is infrastructure. When something in this file fails,
// the harness could not set up what a step needed — which is a different
// finding from "the product is wrong", and is worded as one.

// harness owns everything one smoke run creates. Every path in it is
// under dir, and dir is removed when the run ends: the gate runs on
// every `pika check`, so a run that leaked a repository per invocation
// would fill a developer's disk in an afternoon.
type harness struct {
	// root is the pika module this run builds from.
	root string
	// dir holds the built binaries, the fake agent, the fake home, and
	// every temp repository.
	dir string
	// pika is the binary under test. Every assertion in this program is
	// about what THIS file does when it is executed, never about what
	// the package it was built from returns in-process.
	pika string
	// agentDir holds the fake agent binary, and goes on the front of
	// PATH for the steps that spawn an agent.
	agentDir string
	// home stands in for the operator's home directory. `pika doctor`
	// reports on the agent files installed there, and a gate whose
	// verdict depends on what the developer happens to have in ~ is not
	// a gate.
	home string
	// scratch numbers the repositories, so a step's directory name says
	// which step made it.
	repos int
}

// newHarness builds the binaries under test into a fresh temp directory.
func newHarness() (*harness, error) {
	root, err := moduleRoot()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "pika-smoke-")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	h := &harness{
		root:     root,
		dir:      dir,
		pika:     filepath.Join(dir, exeName("pika")),
		agentDir: filepath.Join(dir, "agent"),
		home:     filepath.Join(dir, "home"),
	}
	for _, d := range []string{h.agentDir, h.home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			h.close()
			return nil, fmt.Errorf("temp dir: %w", err)
		}
	}
	// The agent boundary is a binary looked up on PATH, so faking it is
	// building one. This is internal/e2e's fake, not a second one: two
	// stand-ins for one boundary would drift, and the argv this gate
	// records is only worth reading if it went to the same fake the
	// end-to-end suite drives.
	for _, b := range []struct{ out, pkg string }{
		{h.pika, "./cmd/pika"},
		{filepath.Join(h.agentDir, exeName("fakeagent")), "./internal/e2e/testdata/fakeagent"},
	} {
		if err := h.build(b.out, b.pkg); err != nil {
			h.close()
			return nil, err
		}
	}
	// One script, installed under every runtime's own binary name: pika
	// resolves the harness by runtime, so a step whose contract names
	// claude has to find something called `claude` on PATH. Custom names
	// no binary of its own and acp is a transport, so neither gets a
	// copy.
	source, err := os.ReadFile(filepath.Join(h.agentDir, exeName("fakeagent")))
	if err != nil {
		h.close()
		return nil, fmt.Errorf("read fake agent: %w", err)
	}
	for _, name := range []string{"codex", "claude", "omp", "gemini", "opencode"} {
		if err := os.WriteFile(filepath.Join(h.agentDir, exeName(name)), source, 0o755); err != nil {
			h.close()
			return nil, fmt.Errorf("install fake %s: %w", name, err)
		}
	}
	return h, nil
}

// build compiles one package of this module into the harness directory.
// CGO_ENABLED=0 matches how the binary ships and how CI builds it.
func (h *harness) build(out, pkg string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = h.root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build %s: %w\n%s", pkg, err, b)
	}
	return nil
}

// close removes everything the run created.
func (h *harness) close() error {
	if err := os.RemoveAll(h.dir); err != nil {
		return fmt.Errorf("smoke left %s behind: %w", h.dir, err)
	}
	return nil
}

// result is one invocation of the binary under test: what it was asked,
// what it exited, and what it said. It is a value rather than three
// returns so a failure message can reproduce the whole invocation.
type result struct {
	argv   []string
	dir    string
	exit   int
	stdout string
	stderr string
}

// String renders the invocation for a failure message.
func (r result) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "pika %s (in %s) exited %d", strings.Join(r.argv, " "), r.dir, r.exit)
	if s := strings.TrimRight(r.stdout, "\n"); s != "" {
		fmt.Fprintf(&b, "\n--- stdout ---\n%s", excerpt(s, outputExcerpt))
	}
	if s := strings.TrimRight(r.stderr, "\n"); s != "" {
		fmt.Fprintf(&b, "\n--- stderr ---\n%s", excerpt(s, outputExcerpt))
	}
	return b.String()
}

// run executes the built pika binary in dir with env appended to this
// process's own environment. A non-zero exit is a result, not an error:
// half the claims this gate makes are about what pika does when it
// refuses.
func (h *harness) run(dir string, env []string, args ...string) (result, error) {
	var stdout, stderr strings.Builder
	cmd := exec.Command(h.pika, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	r := result{argv: args, dir: dir, stdout: stdout.String(), stderr: stderr.String()}
	err := cmd.Run()
	r.stdout, r.stderr = stdout.String(), stderr.String()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		r.exit = exitErr.ExitCode()
	default:
		return r, fmt.Errorf("could not run pika %s: %w", strings.Join(args, " "), err)
	}
	return r, nil
}

// agentEnv puts the fake agent at the front of PATH and adds the
// scenario the step wants it to play. See
// internal/e2e/testdata/fakeagent for the variables.
func (h *harness) agentEnv(extra ...string) []string {
	env := []string{"PATH=" + h.agentDir + string(os.PathListSeparator) + os.Getenv("PATH")}
	return append(env, extra...)
}

// homeEnv points a command at the harness's fake home directory. Both
// spellings are set because the two platforms disagree about which one
// names it.
func (h *harness) homeEnv() []string {
	return []string{"HOME=" + h.home, "USERPROFILE=" + h.home}
}

// scaffoldName is the project name every temp repository is initialized
// with. It is pinned rather than taken from the directory: `pika init`
// puts the entrypoint at cmd/<project>/main.go, and a step that reads
// that file must not depend on what the temp directory was called.
const scaffoldName = "repo"

// entryPath is where the go@1 scaffold puts its entrypoint, given
// scaffoldName.
const entryPath = "cmd/" + scaffoldName + "/main.go"

// scaffold runs `pika init --profile go` into a fresh directory and
// returns its path. step names the directory, so a run that somehow
// leaves one behind says which step owned it.
func (h *harness) scaffold(step string) (string, result, error) {
	h.repos++
	dir := filepath.Join(h.dir, fmt.Sprintf("%02d-%s", h.repos, step))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", result{}, fmt.Errorf("temp repo: %w", err)
	}
	r, err := h.run(dir, nil, "init", "--name", scaffoldName, "--profile", "go")
	if err != nil {
		return "", r, err
	}
	if r.exit != 0 {
		return "", r, fmt.Errorf("`pika init` could not scaffold a repository:\n%s", r)
	}
	return dir, r, nil
}

// readRepo reads a repository-relative slash path.
func readRepo(dir, rel string) (string, error) {
	bs, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	return string(bs), nil
}

// writeRepo writes a repository-relative slash path.
func writeRepo(dir, rel, content string) error {
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// git runs one git command in dir and returns its trimmed output. A
// failure is the harness's, not the product's: these are the commands an
// operator runs around a pika run, and none of them is under test.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %w\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// initGit turns a scaffold into a repository with one commit.
//
// Every setting is configured locally rather than inherited. A machine
// with no git identity is one where `git commit` fails, a machine that
// signs every commit is one where it blocks on a passphrase, and a
// Windows checkout with autocrlf on is one where the format gate sees
// CRLF in files this program wrote with LF — none of which is a fact
// about pika. The default branch is never assumed: it is read back from
// the repository that was just created, because CI runners still default
// to `master` and a step that hardcoded `main` already broke this
// repository's CI once.
func initGit(dir string) (branch string, err error) {
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "smoke@pika.invalid"},
		{"config", "user.name", "pika smoke gate"},
		{"config", "commit.gpgsign", "false"},
		{"config", "core.autocrlf", "false"},
		{"add", "-A"},
		{"commit", "-m", "chore: scaffold"},
	} {
		if _, err := git(dir, args...); err != nil {
			return "", err
		}
	}
	return git(dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// commitAll stages and commits everything in the working tree.
func commitAll(dir, message string) error {
	if _, err := git(dir, "add", "-A"); err != nil {
		return err
	}
	_, err := git(dir, "commit", "-m", message)
	return err
}

// moduleRoot resolves the root of the module this program was started
// in. Resolving it rather than assuming the working directory means the
// gate builds the right `./cmd/pika` whatever directory the ladder
// happened to run it from.
func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", errors.New("smoke must run inside the pika module; `go env GOMOD` reports none")
	}
	return filepath.Dir(gomod), nil
}

// exeName is what an executable is called on this platform. PATH lookup
// on Windows will not find a file without the suffix.
func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// decodeEnvelope unwraps one --json payload and asserts the
// discriminators every consumer reads before it knows the shape.
func decodeEnvelope(c *check, r result, command string) (envelope, bool) {
	var env envelope
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil {
		c.failf("`pika %s --json` did not print a cliout envelope: %v\n%s", command, err, r)
		return env, false
	}
	wantEqual(c, "envelope schema of `pika "+command+" --json`", env.Schema, 1)
	wantEqual(c, "envelope command of `pika "+command+" --json`", env.Command, command)
	return env, true
}

// decodeResult unwraps an envelope's result into v.
func decodeResult(c *check, env envelope, r result, what string, v any) bool {
	if err := json.Unmarshal(env.Result, &v); err != nil {
		c.failf("could not read the %s payload: %v\n%s", what, err, r)
		return false
	}
	return true
}
