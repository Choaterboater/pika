package improve

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// A real --help transcript from codex 0.151.0, captured verbatim
// (`codex exec --help`). Parsing this pins the format this package reads
// against the shape codex actually prints, not a hand-simplified stand-in.
const codexExecHelpTranscript = `Run Codex non-interactively

Usage: codex exec [OPTIONS] [PROMPT]
       codex exec [OPTIONS] <COMMAND> [ARGS]

Options:
  -c, --config <key=value>
          Override a configuration value that would otherwise be loaded from ~/.codex/config.toml.

      --enable <FEATURE>
          Enable a feature (repeatable). Equivalent to -c features.<name>=true

  -i, --image <FILE>...
          Optional image(s) to attach to the initial prompt

  -m, --model <MODEL>
          Model the agent should use

  -s, --sandbox <SANDBOX_MODE>
          Select the sandbox policy to use when executing model-generated shell commands

      --approve-for-me
          Route approval requests through automatic review using the workspace-write sandbox

  -C, --cd <DIR>
          Tell the agent to use the specified directory as its working root

  -o, --output-last-message <FILE>
          Specifies file where the last message from the agent should be written

  -h, --help
          Print help (see a summary with '-h')

  -V, --version
          Print version
`

// declaredCodexFlags reads both spellings of a short+long option and the
// bare spelling of a long-only one, against the real transcript.
func TestDeclaredCodexFlagsParsesShortAndLongForms(t *testing.T) {
	declared := declaredCodexFlags(codexExecHelpTranscript)
	for _, want := range []string{"-c", "--config", "--enable", "-m", "--model", "--approve-for-me", "-C", "--cd", "-o", "--output-last-message"} {
		if !declared[want] {
			t.Errorf("declaredCodexFlags missed %q in:\n%s", want, codexExecHelpTranscript)
		}
	}
}

// Every flag CodexRunner actually sends must be a real one: a check that
// always finds itself compatible with a fixture nobody updates proves
// nothing. This is the same assertion CheckCodexCompatibility makes,
// against the transcript pinned above instead of a live subprocess.
func TestSentCodexFlagsAreAllDeclaredInTheRealTranscript(t *testing.T) {
	declared := declaredCodexFlags(codexExecHelpTranscript)
	for _, flag := range sentCodexFlags("codex") {
		if !declared[flag] {
			t.Errorf("CodexRunner sends %q, which codex 0.151.0's own --help does not declare", flag)
		}
	}
}

// sentCodexFlags must name the flags, never the values or the "-" stdin
// sentinel that follows them — a check that flagged "/repo" as an
// unrecognized flag would be noise, not signal.
func TestSentCodexFlagsExcludesValuesAndTheStdinSentinel(t *testing.T) {
	flags := sentCodexFlags("codex")
	for _, notAFlag := range []string{"-", "/repo", "/tmp/result.md", "x", "y", `model_reasoning_effort="y"`} {
		if slices.Contains(flags, notAFlag) {
			t.Errorf("sentCodexFlags = %v, must not contain value or sentinel %q", flags, notAFlag)
		}
	}
}

// A renamed flag is exactly the drift this check exists to catch: codex
// 0.151.0 declares --approve-for-me, and a hypothetical build that
// renamed it to --auto-approve would still exit 0 on --help — the
// silence 13cbf73 and this issue are both about. writeFakeCodex builds a
// tiny stand-in binary whose --help omits one flag CodexRunner sends, so
// this proves detection without touching the network or a real codex
// install.
func TestCheckCodexCompatibilityNamesARenamedFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh to script a fake codex on windows")
	}
	binary := writeFakeCodexHelp(t, strings.ReplaceAll(codexExecHelpTranscript, "--approve-for-me", "--auto-approve"))

	missing, err := CheckCodexCompatibility(context.Background(), binary)
	if err != nil {
		t.Fatalf("CheckCodexCompatibility: %v", err)
	}
	if len(missing) != 1 || missing[0] != "--approve-for-me" {
		t.Errorf("missing = %v, want exactly [--approve-for-me]", missing)
	}
}

// The healthy case: a fake codex whose --help is the real transcript
// verbatim reports nothing missing.
func TestCheckCodexCompatibilityFindsNothingMissingWhenTranscriptMatches(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh to script a fake codex on windows")
	}
	binary := writeFakeCodexHelp(t, codexExecHelpTranscript)

	missing, err := CheckCodexCompatibility(context.Background(), binary)
	if err != nil {
		t.Fatalf("CheckCodexCompatibility: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none: the transcript declares every flag CodexRunner sends", missing)
	}
}

// writeFakeCodexHelp builds a shell script that prints help to stdout
// regardless of its arguments, exactly the shape `codex exec --help`
// takes, and returns its path.
func writeFakeCodexHelp(t *testing.T, help string) string {
	t.Helper()
	path := t.TempDir() + "/codex"
	script := "#!/bin/sh\ncat <<'EOF'\n" + help + "\nEOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The real, installed codex binary, exercised end to end exactly the way
// a scheduled job would run it. Gated on PIKA_CODEX_COMPAT — an
// environment variable rather than a build tag, so this file stays
// compiled and vetted on every ordinary `go test ./...` and only the
// subprocess call moves behind the switch, the same shape
// internal/conformance already uses for a network-dependent check.
func TestRealCodexCompatibility(t *testing.T) {
	if os.Getenv(EnabledEnv) != "1" {
		t.Skipf("skipping: set %s=1 to run this against an installed codex", EnabledEnv)
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("skipping: codex is not on PATH")
	}
	missing, err := CheckCodexCompatibility(context.Background(), "codex")
	if err != nil {
		t.Fatalf("CheckCodexCompatibility: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("codex on this machine no longer declares %v, which CodexRunner sends on every handoff", missing)
	}
}
