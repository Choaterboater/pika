package adapters

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A real --help transcript from codex 0.151.0, captured verbatim
// (`codex exec --help`). Parsing this pins the format this package reads
// against the shape a harness actually prints, not a hand-simplified
// stand-in.
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

// declaredFlags reads both spellings of a short+long option and the bare
// spelling of a long-only one, against the real transcript.
func TestDeclaredFlagsParsesShortAndLongForms(t *testing.T) {
	declared := declaredFlags(codexExecHelpTranscript)
	for _, want := range []string{"-c", "--config", "--enable", "-m", "--model", "--approve-for-me", "-C", "--cd", "-o", "--output-last-message"} {
		if !declared[want] {
			t.Errorf("declaredFlags missed %q in:\n%s", want, codexExecHelpTranscript)
		}
	}
}

// Every flag an adapter actually sends must be a real one: a check that
// always finds itself compatible with a fixture nobody updates proves
// nothing. This is the same assertion CheckCompatibility makes, against
// the transcript pinned above instead of a live subprocess.
func TestSentFlagsAreAllDeclaredInTheRealTranscript(t *testing.T) {
	ad, _ := Lookup(RuntimeCodex)
	declared := declaredFlags(codexExecHelpTranscript)
	for _, flag := range ad.ProbeFlags() {
		if !declared[flag] {
			t.Errorf("the codex adapter sends %q, which codex 0.151.0's own --help does not declare", flag)
		}
	}
}

// Flags must name flags, never the values or the "-" stdin sentinel that
// follows them — a check that flagged "/repo" as an unrecognized flag
// would be noise, not signal.
func TestProbeFlagsExcludesValuesAndTheStdinSentinel(t *testing.T) {
	ad, _ := Lookup(RuntimeCodex)
	flags := ad.ProbeFlags()
	for _, notAFlag := range []string{"-", "{root}", "{output}", "{model}", `model_reasoning_effort="{effort}"`, "exec", "sandbox_workspace_write.network_access=false"} {
		if contains(flags, notAFlag) {
			t.Errorf("ProbeFlags = %v, must not contain value or sentinel %q", flags, notAFlag)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// A renamed flag is exactly the drift this check exists to catch: codex
// 0.151.0 declares --approve-for-me, and a hypothetical build that renamed
// it to --auto-approve would still exit 0 on --help — the silence this
// probe exists to end. The fixture is a tiny stand-in binary whose --help
// omits one flag, so detection is proved without touching the network or a
// real install.
func TestCompatibilityProbeNamesARemovedFlag(t *testing.T) {
	ad, _ := Lookup(RuntimeCodex)
	ad.Binary = writeFakeHelp(t, strings.ReplaceAll(codexExecHelpTranscript, "--approve-for-me", "--auto-approve"))
	missing, err := CheckCompatibility(context.Background(), ad)
	if err != nil {
		t.Fatalf("CheckCompatibility: %v", err)
	}
	if len(missing) != 1 || missing[0] != "--approve-for-me" {
		t.Errorf("missing = %v, want exactly [--approve-for-me]", missing)
	}
}

// The healthy case: a stand-in whose --help is the real transcript
// verbatim reports nothing missing.
func TestCompatibilityProbeFindsNothingWhenTranscriptMatches(t *testing.T) {
	ad, _ := Lookup(RuntimeCodex)
	ad.Binary = writeFakeHelp(t, codexExecHelpTranscript)
	missing, err := CheckCompatibility(context.Background(), ad)
	if err != nil {
		t.Fatalf("CheckCompatibility: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none: the transcript declares every flag the adapter sends", missing)
	}
}

// An adapter with no Help argv cannot be probed, and reporting "no
// findings" for it is the honest answer rather than an error: the operator
// wrote the argv, so there is nothing for pika to check.
func TestCompatibilityProbeSkipsAnAdapterWithNoHelpArgv(t *testing.T) {
	ad, _ := Lookup(RuntimeCustom)
	missing, err := CheckCompatibility(context.Background(), ad)
	if err != nil {
		t.Fatalf("CheckCompatibility: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none for an adapter with no help argv", missing)
	}
}

// writeFakeHelp builds a shell script that prints help to stdout
// regardless of its arguments, exactly the shape `codex exec --help`
// takes, and returns its path.
func writeFakeHelp(t *testing.T, help string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no sh to script a fake harness on windows")
	}
	path := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\ncat <<'EOF'\n" + help + "\nEOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// Every installed harness, exercised end to end exactly the way a
// scheduled job would run them. Gated on PIKA_ADAPTER_COMPAT — an
// environment variable rather than a build tag, so this file stays
// compiled and vetted on every ordinary `go test ./...` and only the
// subprocess calls move behind the switch, the same shape
// internal/conformance already uses for a network-dependent check.
//
// An adapter whose binary is absent is skipped rather than failed: the
// probe is a canary for the machines that have the harness, and a report
// that says which ones did not run is more useful than a failure on a
// developer's laptop that never installed gemini.
func TestRealAdapterCompatibility(t *testing.T) {
	if os.Getenv(EnabledEnv) != "1" {
		t.Skipf("skipping: set %s=1 to run this against the installed harnesses", EnabledEnv)
	}
	for _, ad := range All() {
		if len(ad.Help) == 0 {
			continue
		}
		t.Run(ad.Runtime, func(t *testing.T) {
			if _, err := exec.LookPath(ad.Binary); err != nil {
				t.Skipf("skipping: %s is not on PATH", ad.Binary)
			}
			missing, err := CheckCompatibility(context.Background(), ad)
			if err != nil {
				t.Fatalf("CheckCompatibility: %v", err)
			}
			if len(missing) != 0 {
				t.Errorf("%s on this machine no longer declares %v, which the adapter sends on every handoff", ad.Runtime, missing)
			}
		})
	}
}
