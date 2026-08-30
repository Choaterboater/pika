package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/adopt"
	"github.com/Choaterboater/pika/internal/cliout"
	"github.com/Choaterboater/pika/internal/version"
)

func dispatchArgs(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := dispatch(args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

// envelopeOf unwraps one --json payload and asserts the discriminators
// every command shares. Tests read results through it rather than
// unmarshalling stdout directly, so a payload that lost its envelope
// fails loudly instead of quietly parsing into the report shape.
func envelopeOf(t *testing.T, out []byte, name string) cliout.Envelope {
	t.Helper()
	var env cliout.Envelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("stdout is not a cliout envelope (%v):\n%s", err, out)
	}
	if env.Schema != cliout.Schema {
		t.Errorf("schema = %d, want %d:\n%s", env.Schema, cliout.Schema, out)
	}
	if env.Command != name {
		t.Errorf("command = %q, want %q:\n%s", env.Command, name, out)
	}
	return env
}

// resultOf unwraps the envelope and decodes its result into v.
func resultOf(t *testing.T, out []byte, name string, v any) cliout.Envelope {
	t.Helper()
	env := envelopeOf(t, out, name)
	if len(env.Result) == 0 {
		t.Fatalf("%s envelope carries no result:\n%s", name, out)
	}
	if err := json.Unmarshal(env.Result, v); err != nil {
		t.Fatalf("%s result is not the expected shape (%v):\n%s", name, err, env.Result)
	}
	return env
}

func TestBareInvocationPrintsCommandTable(t *testing.T) {
	code, out, _ := dispatchArgs(t)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, name := range []string{"init", "check", "adopt", "apply", "mcp", "help"} {
		if !strings.Contains(out, name) {
			t.Errorf("command table is missing %q:\n%s", name, out)
		}
	}
}

func TestHelpForOneCommand(t *testing.T) {
	code, out, _ := dispatchArgs(t, "help", "check")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "--changed") {
		t.Errorf("help check omitted its flags:\n%s", out)
	}
}

func TestHelpForUnknownCommandExits2(t *testing.T) {
	code, _, errb := dispatchArgs(t, "help", "bogus")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb, "bogus") {
		t.Errorf("stderr did not name the unknown command:\n%s", errb)
	}
}

func TestUnknownCommandExits2(t *testing.T) {
	code, _, errb := dispatchArgs(t, "frobnicate")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb, "frobnicate") {
		t.Errorf("stderr did not name the unknown command:\n%s", errb)
	}
}

func TestVersionOnlyAsFirstArgument(t *testing.T) {
	code, out, _ := dispatchArgs(t, "--version")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("--version printed nothing")
	}
}

// Regression: main.go:13-18 scanned every argument, so `pika check
// --version` printed the version instead of running check. A free-form
// string flag valued "version" hit the same trap.
func TestVersionIsNotHijackedFromFlagPosition(t *testing.T) {
	code, out, _ := dispatchArgs(t, "check", "--version")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (unknown flag for check)", code)
	}
	if strings.Contains(out, "pika ") {
		t.Errorf("version output leaked from a flag position:\n%s", out)
	}
}

// Regression for a live bug on main: `pika improve --branch version`
// printed the version and exited 0, silently doing nothing while
// reporting success. improve's --branch and --agent are the binary's
// first free-form string flags, which is what made the argv scan
// exploitable.
//
// The test runs in an empty temp directory, and that is not incidental.
// improve mutates whatever repository it resolves: without the chdir it
// resolved the pika checkout the test binary was compiled from, created
// a branch named "version" in it, and ran the contract's own test gate —
// which is `go test ./...`, so the suite re-entered itself once pika
// began governing pika. Standing in a directory that is not a project
// makes the outcome depend on dispatch alone: improve cannot succeed
// there, it names itself in its own output, and the version is never
// printed.
func TestImproveIsNotHijackedByAFlagValuedVersion(t *testing.T) {
	t.Chdir(t.TempDir())
	code, out, errb := dispatchArgs(t, "improve", "--branch", "version")
	if code == 0 {
		t.Fatalf("improve reported success in a directory with no project:\n%s", out)
	}
	if strings.TrimSpace(out) == version.String() {
		t.Fatalf("dispatch printed the version instead of running improve: %q", out)
	}
	if !strings.Contains(out+errb, "improve") {
		t.Fatalf("dispatch never reached improve; stdout %q stderr %q", out, errb)
	}
}

func TestEveryCommandHasSummaryAndUsage(t *testing.T) {
	for _, c := range commands {
		if strings.TrimSpace(c.summary) == "" {
			t.Errorf("command %q has no summary", c.name)
		}
		if strings.TrimSpace(c.usage) == "" {
			t.Errorf("command %q has no usage", c.name)
		}
	}
}

// jsonCase exercises one command that advertises --json: the arguments
// it needs beyond `--json --root <dir>`, and the fixture that puts a
// repository in a state where the command has something to say.
type jsonCase struct {
	args  []string
	setup func(t *testing.T, dir string)
}

// jsonCases is keyed by command name. Every command whose usage
// advertises --json must have an entry: the test below fails for one
// that does not, so a command added later cannot silently opt out of the
// shared envelope by simply not being tested.
var jsonCases = map[string]jsonCase{
	"init": {},
	"adopt": {setup: func(t *testing.T, dir string) {
		writeUnadoptedRepo(t, dir)
	}},
	"apply": {setup: func(t *testing.T, dir string) {
		writeUnadoptedRepo(t, dir)
		if _, err := adopt.Preview(dir); err != nil {
			t.Fatalf("adopt fixture: %v", err)
		}
	}},
	"check":     {args: []string{"--all"}, setup: writeMinimalProject},
	"status":    {},
	"doctor":    {setup: writeMinimalProject},
	"explain":   {args: []string{"typecheck"}},
	"authorize": {args: []string{"--scope", "read"}},
	// handoff and improve run the ladder first: in a project whose gates
	// pass there is nothing to hand off, and improve stops before any
	// agent or Git mutation. Neither reaches a runtime.
	"handoff": {setup: writeMinimalProject},
	"improve": {setup: writeMinimalProject},
	// work always goes on to the agent — a green ladder says nothing
	// about whether a goal has been met — so the exercise that reaches
	// no runtime is the lifecycle's own refusal. A temp directory is a
	// project but not a git checkout, so the run stops on the very
	// first thing it does and no record, branch or bundle is created.
	"work": {args: []string{"add a health endpoint"}, setup: writeMinimalProject},
	// resume is handed a run that already finished: the refusal is a
	// real exercise of the command — root, record, envelope — and the
	// only one that reaches no agent and mutates no repository.
	"resume": {args: []string{resumeEnvelopeRunID}, setup: seedFinishedRun},
}

// writeUnadoptedRepo lays down the smallest repository `adopt` accepts:
// a Go module that has never been adopted.
func writeUnadoptedRepo(t *testing.T, dir string) {
	t.Helper()
	for rel, body := range map[string]string{
		"go.mod":    "module example.com/fixture\n\ngo 1.26\n",
		"main.go":   "package main\n\nfunc main() {}\n",
		"README.md": "# fixture\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The whole point of internal/cliout: an agent must be able to take any
// --json payload from any command and read schema, command, and ok
// without knowing which command produced it. This walks the registry
// rather than a hand-written list, so the surface cannot drift as
// commands are added.
func TestEveryJSONCommandEmitsTheEnvelope(t *testing.T) {
	for _, c := range commands {
		if !strings.Contains(c.usage, "--json") {
			continue
		}
		tc, ok := jsonCases[c.name]
		if !ok {
			t.Errorf("command %q advertises --json but has no jsonCases entry; add one so its envelope is proven", c.name)
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.setup != nil {
				tc.setup(t, dir)
			}
			args := append(append([]string{}, tc.args...), "--json", "--root", dir)
			var out, errb bytes.Buffer
			code := c.run(args, strings.NewReader(""), &out, &errb)

			// Unmarshalling the whole buffer is part of the assertion:
			// a command that also printed prose on stdout would fail
			// here, which is exactly what a parsing agent would hit.
			env := envelopeOf(t, out.Bytes(), c.name)
			switch code {
			case 0:
				if !env.OK {
					t.Errorf("exit 0 but ok = false:\n%s", out.String())
				}
				if env.Error != nil {
					t.Errorf("exit 0 but error = %+v:\n%s", env.Error, out.String())
				}
			case 1:
				if env.OK {
					t.Errorf("exit 1 but ok = true:\n%s", out.String())
				}
			case 2:
				if env.Error == nil || env.Error.Code == "" {
					t.Errorf("exit 2 without an error code:\n%s\nstderr: %s", out.String(), errb.String())
				}
			default:
				t.Fatalf("exit = %d, want 0, 1, or 2; stderr: %s", code, errb.String())
			}
		})
	}
}

// Exit 2 is the failure an agent is least able to recover from by
// guessing, so with --json the reason belongs in the payload rather than
// in prose on a stream the caller may never read. Without --json the
// operator's plain line is unchanged.
func TestExitTwoWithJSONEmitsTheErrorEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantCode string
	}{
		{name: "usage", args: []string{"check", "--json", "junk"}, wantCode: codeUsage},
		{name: "config", args: []string{"check", "--json"}, wantCode: codeConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var out, errb bytes.Buffer
			code := dispatch(append(tc.args, "--root", dir), strings.NewReader(""), &out, &errb)
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stdout: %s stderr: %s", code, out.String(), errb.String())
			}
			env := envelopeOf(t, out.Bytes(), "check")
			if env.OK {
				t.Error("ok = true on a usage or configuration error")
			}
			if env.Error == nil || env.Error.Code != tc.wantCode {
				t.Fatalf("error = %+v, want code %q", env.Error, tc.wantCode)
			}
			if env.Error.Message == "" {
				t.Error("error carries no message")
			}
			if errb.Len() != 0 {
				t.Errorf("stderr = %q, want nothing: with --json the envelope is the answer", errb.String())
			}
		})
	}
}
