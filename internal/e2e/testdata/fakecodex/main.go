// Command fakecodex stands in for the `codex` binary at the single
// external boundary a pika run crosses.
//
// The end-to-end tests build it, put it on PATH under the name pika
// actually spawns, and let the real binary drive the real
// improve.CodexRunner argv into it. That keeps the lifecycle honest —
// every other participant is production code — while requiring no Codex
// install, no credentials, no model and no network, which is what makes
// `pika check --ci` provably LLM-free even though it runs these tests.
//
// One binary serves every scenario because everything it does is read
// from the environment:
//
//	FAKE_CODEX_FILE     repository-relative file to write: the agent's edit
//	FAKE_CODEX_CONTENT  contents for that file
//	FAKE_CODEX_MESSAGE  the final agent message (defaults to a fixed line)
//	FAKE_CODEX_PROMPT   copy the prompt read from stdin to this path
//	FAKE_CODEX_ARGV     record the argv pika spawned at this path, one per line
//	FAKE_CODEX_SPAWNED  create this file before the edit is made, so a
//	                    test can act while the working tree is still clean
//	FAKE_CODEX_WAIT     block until this file appears, before the edit
//	FAKE_CODEX_STARTED  create this file once the edit is on disk
//	FAKE_CODEX_HANG     block until this file appears, so the test can
//	                    interrupt pika at a known point in the lifecycle
//
// The two gates bracket the one thing the agent does that the
// repository can see. SPAWNED/WAIT hold a run that has taken everything
// it takes and changed nothing — which is where a test can ask what a
// second run does about it — and STARTED/HANG hold one whose edit is
// already on disk.
//
// Every path but FAKE_CODEX_FILE is absolute and belongs to the test, not
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

// hangTimeout bounds FAKE_CODEX_HANG. The test that uses it kills pika
// and then releases this process, so the timeout is only what keeps an
// orphan from outliving the suite when that test fails early.
const hangTimeout = 2 * time.Minute

// hangPoll is how often the release file is checked. The wait ends a
// process, never a measurement, so a coarse poll costs nothing.
const hangPoll = 20 * time.Millisecond

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fakecodex:", err)
		os.Exit(1)
	}
}

func run(argv []string) error {
	// The prompt arrives on stdin, and draining it is not optional:
	// pika writes the whole bundle into the pipe and a reader that
	// leaves it unread would deadlock the writer on a large one.
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read prompt: %w", err)
	}
	root := value(argv, "--cd")
	output := value(argv, "--output-last-message")
	if root == "" || output == "" {
		return fmt.Errorf("spawned without --cd and --output-last-message: %v", argv)
	}
	if err := record(os.Getenv("FAKE_CODEX_ARGV"), strings.Join(argv, "\n")+"\n"); err != nil {
		return err
	}
	if err := record(os.Getenv("FAKE_CODEX_PROMPT"), string(prompt)); err != nil {
		return err
	}
	// Before the edit: the run has taken its branch and its lease and
	// has changed nothing the repository can see. A test holding here
	// is asking what a second run does about a first one that is
	// genuinely in flight.
	if err := record(os.Getenv("FAKE_CODEX_SPAWNED"), "spawned\n"); err != nil {
		return err
	}
	hang(os.Getenv("FAKE_CODEX_WAIT"))
	if name := os.Getenv("FAKE_CODEX_FILE"); name != "" {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(os.Getenv("FAKE_CODEX_CONTENT")), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	message := os.Getenv("FAKE_CODEX_MESSAGE")
	if message == "" {
		message = "fakecodex: the requested edit is in the working tree.\n"
	}
	if err := os.WriteFile(output, []byte(message), 0o644); err != nil {
		return fmt.Errorf("write final message: %w", err)
	}
	// The started marker is written last so a test that waits on it
	// knows the edit is already on disk. A marker written first would
	// let the test interrupt pika before the agent had done anything,
	// which is a different scenario than the one it asked for.
	if err := record(os.Getenv("FAKE_CODEX_STARTED"), "started\n"); err != nil {
		return err
	}
	hang(os.Getenv("FAKE_CODEX_HANG"))
	return nil
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
func record(path, content string) error {
	if path == "" {
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
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
