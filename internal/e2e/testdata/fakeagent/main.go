// Command fakeagent stands in for every harness binary at the single
// external boundary a pika run crosses.
//
// The end-to-end tests build it once and install it on PATH under each
// runtime's own binary name, then let the real binary drive the real
// adapter argv into it. That keeps the lifecycle honest — every other
// participant is production code — while requiring no Codex install, no
// Claude install, no credentials, no model and no network, which is what
// makes `pika check --ci` provably LLM-free even though it runs these
// tests.
//
// One binary serves every runtime and every scenario because everything it
// does is read from the environment:
//
//	FAKE_AGENT_FILE     repository-relative file to write: the agent's edit
//	FAKE_AGENT_CONTENT  contents for that file
//	FAKE_AGENT_MESSAGE  the final agent message (defaults to a fixed line)
//	FAKE_AGENT_PROMPT   copy the prompt it was handed to this path
//	FAKE_AGENT_ARGV     record the argv pika spawned at this path, one per line
//	FAKE_AGENT_SPAWNED  create this file before the edit is made, so a
//	                    test can act while the working tree is still clean
//	FAKE_AGENT_WAIT     block until this file appears, before the edit
//	FAKE_AGENT_STARTED  create this file once the edit is on disk
//	FAKE_AGENT_FILE     repository-relative file to write: the agent's edit
//	FAKE_AGENT_ARGV     record the argv pika spawned at this path, one per line
//	FAKE_AGENT_ARGV_ADD append rather than overwrite, so one file holds
//	                    every argv a multi-role run spawned, in spawn order
//	FAKE_AGENT_EDIT_ON  write the file only when the argv contains this
//	                    substring — how a multi-role run gives one role an
//	                    edit and the other none
//
// The two gates bracket the one thing the agent does that the
// repository can see. SPAWNED/WAIT hold a run that has taken everything
// it takes and changed nothing — which is where a test can ask what a
// second run does about it — and STARTED/HANG hold one whose edit is
// already on disk.
//
// Every path but FAKE_AGENT_FILE is absolute and belongs to the test, not
// to the repository under test: a run's own tree must contain only what
// the agent was asked to change.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// hangTimeout bounds FAKE_AGENT_HANG. The test that uses it kills pika
// and then releases this process, so the timeout is only what keeps an
// orphan from outliving the suite when that test fails early.
const hangTimeout = 2 * time.Minute

// hangPoll is how often the release file is checked. The wait ends a
// process, never a measurement, so a coarse poll costs nothing.
const hangPoll = 20 * time.Millisecond

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		os.Exit(1)
	}
}

func run(argv []string) error {
	// The prompt arrives on stdin, or as a path in the argv, or not at
	// all depending on the runtime's adapter. Draining stdin is not
	// optional either way: pika writes the whole bundle into the pipe and
	// a reader that leaves it unread would deadlock the writer on a large
	// one.
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read prompt: %w", err)
	}
	if path := promptPath(argv); path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read prompt file: %w", err)
		}
		prompt = contents
	}
	root, err := rootDir(argv)
	if err != nil {
		return err
	}
	// A multi-role run spawns this process more than once, so one file
	// has to be able to hold every argv in the order they happened — the
	// order is the proof that the explorer ran before the builder.
	if err := record(os.Getenv("FAKE_AGENT_ARGV"), strings.Join(argv, "\n")+"\n"); err != nil {
		return err
	}
	if err := record(os.Getenv("FAKE_AGENT_PROMPT"), string(prompt)); err != nil {
		return err
	}
	// Before the edit: the run has taken its branch and its lease and
	// has changed nothing the repository can see. A test holding here
	// is asking what a second run does about a first one that is
	// genuinely in flight.
	if err := record(os.Getenv("FAKE_AGENT_SPAWNED"), "spawned\n"); err != nil {
		return err
	}
	hang(os.Getenv("FAKE_AGENT_WAIT"))
	// One fixture serves every role in a run, so a multi-role test needs
	// a way to give the edit to one role and not the other. The gate is
	// a substring of the argv, which is the only thing that tells this
	// process which adapter spawned it.
	gate := os.Getenv("FAKE_AGENT_EDIT_ON")
	if name := os.Getenv("FAKE_AGENT_FILE"); name != "" && (gate == "" || strings.Contains(strings.Join(argv, "\n"), gate)) {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(os.Getenv("FAKE_AGENT_CONTENT")), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	message := os.Getenv("FAKE_AGENT_MESSAGE")
	if message == "" {
		message = "fakeagent: the requested edit is in the working tree.\n"
	}
	// Where the message goes is the runtime's business: codex names a
	// file and writes it itself, every other adapter's message is the
	// process's stdout, which pika tees.
	if output := value(argv, "--output-last-message"); output != "" {
		if err := os.WriteFile(output, []byte(message), 0o644); err != nil {
			return fmt.Errorf("write final message: %w", err)
		}
	} else {
		if _, err := os.Stdout.WriteString(message); err != nil {
			return fmt.Errorf("write final message: %w", err)
		}
	}
	// The started marker is written last so a test that waits on it
	// knows the edit is already on disk. A marker written first would
	// let the test interrupt pika before the agent had done anything,
	// which is a different scenario than the one it asked for.
	if err := record(os.Getenv("FAKE_AGENT_STARTED"), "started\n"); err != nil {
		return err
	}
	hang(os.Getenv("FAKE_AGENT_HANG"))
	return nil
}

// rootDir is the repository the agent was told to work in. Adapters name
// it three different ways, and a runtime that names it none of them gets
// pika's own working directory — which is exactly what the kernel set for
// the runtimes that take no --cd equivalent.
func rootDir(argv []string) (string, error) {
	for _, flag := range []string{"--cd", "--cwd", "--dir"} {
		if root := value(argv, flag); root != "" {
			return root, nil
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	return wd, nil
}

// promptPath is the file an adapter named as the prompt: omp's trailing
// @file, or opencode's --file. An adapter that names neither put the
// prompt on stdin instead.
func promptPath(argv []string) string {
	if path := value(argv, "--file"); path != "" {
		return path
	}
	for _, arg := range argv {
		if strings.HasPrefix(arg, "@") {
			return strings.TrimPrefix(arg, "@")
		}
	}
	return ""
}

// value returns the argument following name, or "" when pika did not
// pass it.
func value(argv []string, name string) string {
	for i, arg := range argv {
		if arg == name && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// record writes content to path when the test asked for it. An empty
// path means the test does not care.
//
// It appends when FAKE_AGENT_ARGV_ADD is set, which is how one file holds
// every argv a multi-role run spawned instead of only the last.
func record(path, content string) error {
	if path == "" {
		return nil
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if os.Getenv("FAKE_AGENT_ARGV_ADD") != "" {
		flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// hang blocks until path exists or hangTimeout elapses. Reaching the
// timeout is not reported as a failure: by then the process that was
// waiting for this one is gone, and there is nobody left to tell.
func hang(path string) {
	if path == "" {
		return
	}
	deadline := time.Now().Add(hangTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(hangPoll)
	}
}
