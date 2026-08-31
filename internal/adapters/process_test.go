package adapters

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeScript is the stand-in harness. Everything it does is driven by the
// environment so one script serves every scenario, and it is a fixture
// rather than a stub: the real argv, the real environment and the real
// stdin reach it, because the real ProcessRunner built them.
const fakeScript = `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_AGENT_ARGV"
env > "$FAKE_AGENT_ENV"
cat > "${FAKE_AGENT_STDIN:-/dev/null}"
if [ -n "$FAKE_AGENT_FILE" ]; then
	printf '%s' "${FAKE_AGENT_MESSAGE:-fake agent final message}" > "$FAKE_AGENT_FILE"
fi
printf '%s' "${FAKE_AGENT_STDOUT:-fake agent final message}"
`

// installFakeBinary writes fakeScript into a temporary directory under the
// runtime's own binary name and puts that directory first on PATH, so
// exec.LookPath resolves the adapter's default binary to the fixture.
//
// It skips on Windows, which has no /bin/sh to run the script — the same
// reason the compatibility probe's scripted fixtures skip there.
func installFakeBinary(t *testing.T, runtimeName string) string {
	return installScript(t, runtimeName, fakeScript)
}

// installScript writes body into a temporary directory under name and
// puts that directory first on PATH, so exec.LookPath resolves whatever
// binary pika asks for to the fixture. It is how one fixture directory
// serves both the ordinary harness and the scripted ACP peer.
func installScript(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no /bin/sh to script a fake harness on windows")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// processFixture is one ProcessRunner test's scaffolding: a repository
// root, a prompt file, and the path the final message must land at.
type processFixture struct {
	root       string
	promptPath string
	outputPath string
}

func newProcessFixture(t *testing.T) processFixture {
	t.Helper()
	dir := t.TempDir()
	f := processFixture{
		root:       filepath.Join(dir, "repo"),
		promptPath: filepath.Join(dir, "prompt.md"),
		outputPath: filepath.Join(dir, "last-message.raw"),
	}
	if err := os.MkdirAll(f.root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.promptPath, []byte("do the work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_AGENT_ARGV", filepath.Join(dir, "argv.txt"))
	t.Setenv("FAKE_AGENT_ENV", filepath.Join(dir, "env.txt"))
	t.Setenv("FAKE_AGENT_STDIN", filepath.Join(dir, "stdin.txt"))
	return f
}

func (f processFixture) argv(t *testing.T) []string {
	t.Helper()
	bs, err := os.ReadFile(os.Getenv("FAKE_AGENT_ARGV"))
	if err != nil {
		t.Fatalf("the fake harness recorded no argv: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(bs), "\n"), "\n")
}

func (f processFixture) stdin(t *testing.T) string {
	t.Helper()
	bs, err := os.ReadFile(os.Getenv("FAKE_AGENT_STDIN"))
	if err != nil {
		t.Fatalf("the fake harness recorded no stdin: %v", err)
	}
	return string(bs)
}

func (f processFixture) env(t *testing.T) map[string]string {
	t.Helper()
	bs, err := os.ReadFile(os.Getenv("FAKE_AGENT_ENV"))
	if err != nil {
		t.Fatalf("the fake harness recorded no environment: %v", err)
	}
	env := make(map[string]string)
	for _, line := range strings.Split(string(bs), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok {
			env[name] = value
		}
	}
	return env
}

func assertArgvFields(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q (whole argv: %v)", i, got[i], want[i], got)
		}
	}
}

// A runtime whose message is its stdout must still be captured: the
// operator watches the stream and the run keeps the bytes, from one read
// of one pipe rather than two.
func TestProcessRunnerTeesStdoutAndCapturesTheMessage(t *testing.T) {
	installFakeBinary(t, RuntimeClaude)
	f := newProcessFixture(t)
	runner, err := New(Agent{Name: "builder", Runtime: RuntimeClaude})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := runner.Run(context.Background(), f.root, f.promptPath, f.outputPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := os.ReadFile(f.outputPath)
	if err != nil {
		t.Fatalf("the run captured no message: %v", err)
	}
	if string(got) != "fake agent final message" {
		t.Errorf("captured message = %q, want the harness's stdout verbatim", got)
	}
}

// A runtime that reads its prompt on stdin gets it there; a runtime that
// takes it as an argv element does not, and must not be given both.
func TestProcessRunnerGivesThePromptOnStdinUnlessTheArgvConsumedIt(t *testing.T) {
	cases := []struct {
		runtime string
		onStdin bool
	}{
		{RuntimeClaude, true},
		{RuntimeCodex, true},
		{RuntimeOMP, false},
		{RuntimeOpenCode, false},
	}
	for _, tc := range cases {
		t.Run(tc.runtime, func(t *testing.T) {
			installFakeBinary(t, tc.runtime)
			f := newProcessFixture(t)
			// The codex adapter writes its own message to a file; point
			// the fixture at the capture path so the run has one.
			t.Setenv("FAKE_AGENT_FILE", f.outputPath)
			runner, err := New(Agent{Name: "builder", Runtime: tc.runtime})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := runner.Run(context.Background(), f.root, f.promptPath, f.outputPath); err != nil {
				t.Fatalf("Run: %v", err)
			}
			argv := f.argv(t)
			if tc.onStdin {
				if got := f.stdin(t); got != "do the work\n" {
					t.Errorf("%s was given stdin %q, want the prompt", tc.runtime, got)
				}
				if tc.runtime == RuntimeCodex && argv[len(argv)-1] != "-" {
					t.Errorf("%s argv lost its stdin sentinel: %v", tc.runtime, argv)
				}
				return
			}
			if got := f.stdin(t); got != "" {
				t.Errorf("%s consumed its prompt in argv but was also given stdin %q", tc.runtime, got)
			}
			joined := strings.Join(argv, " ")
			want := "@" + f.promptPath
			if tc.runtime == RuntimeOpenCode {
				want = "--file " + f.promptPath
			}
			if !strings.Contains(joined, want) {
				t.Errorf("%s argv does not carry %s: %v", tc.runtime, want, argv)
			}
		})
	}
}

// A missing harness is the one refusal an operator can act on directly, so
// it names the runtime that asked for the binary as well as the binary.
func TestMissingBinaryIsRefusedNamingRuntimeAndBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // an empty directory: nothing resolves
	f := newProcessFixture(t)
	runner, err := New(Agent{Name: "reviewer", Runtime: RuntimeGemini})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = runner.Run(context.Background(), f.root, f.promptPath, f.outputPath)
	if err == nil {
		t.Fatal("a run with no gemini on PATH succeeded")
	}
	for _, want := range []string{`agent "reviewer"`, `runtime "gemini"`, "gemini", "on PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %s", err, want)
		}
	}
}

// A declared allowlist is a containment boundary: what is not named does
// not cross, so a contract cannot widen a harness's access to every
// credential pika's own environment holds.
func TestEnvAllowlistPassesOnlyTheDeclaredNames(t *testing.T) {
	installFakeBinary(t, RuntimeClaude)
	f := newProcessFixture(t)
	t.Setenv("PIKA_TEST_DECLARED", "declared-value")
	t.Setenv("PIKA_TEST_UNDECLARED", "undeclared-value")
	runner, err := New(Agent{Name: "builder", Runtime: RuntimeClaude, Env: []string{
		"PIKA_TEST_DECLARED",
		// The fixture needs its own channels named, or it has nowhere to
		// record what it saw. They are test scaffolding, not secrets.
		"FAKE_AGENT_ARGV", "FAKE_AGENT_ENV", "FAKE_AGENT_STDIN",
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := runner.Run(context.Background(), f.root, f.promptPath, f.outputPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	env := f.env(t)
	if env["PIKA_TEST_DECLARED"] != "declared-value" {
		t.Errorf("declared variable did not cross: %q", env["PIKA_TEST_DECLARED"])
	}
	if _, ok := env["PIKA_TEST_UNDECLARED"]; ok {
		t.Error("an undeclared variable crossed the allowlist")
	}
	// The exec essentials are directories, not secrets, and a harness
	// without them fails in ways that look like pika's fault.
	for _, name := range execEssentials {
		if _, ok := env[name]; !ok {
			t.Errorf("the child received no %s", name)
		}
	}
}

// With no allowlist at all the child inherits pika's environment exactly,
// which is how every handoff before M6 behaved and the safest default for
// a runtime pika knows nothing about.
func TestNoAllowlistInheritsTheEnvironment(t *testing.T) {
	installFakeBinary(t, RuntimeClaude)
	f := newProcessFixture(t)
	t.Setenv("PIKA_TEST_INHERITED", "inherited-value")
	runner, err := New(Agent{Name: "builder", Runtime: RuntimeClaude})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := runner.Run(context.Background(), f.root, f.promptPath, f.outputPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := f.env(t)["PIKA_TEST_INHERITED"]; got != "inherited-value" {
		t.Errorf("inherited variable = %q, want inherited-value", got)
	}
}

// A reference to a variable pika does not have is a reference to nothing,
// not an empty string: passing NAME= through would hand the harness a
// variable that exists and is empty, which is a different thing from the
// one the operator meant.
func TestAnUnsetDeclaredEnvVarIsRefused(t *testing.T) {
	const name = "PIKA_DEFINITELY_NOT_SET"
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	_, err := New(Agent{Name: "builder", Runtime: RuntimeClaude, Env: []string{name}})
	if err == nil {
		t.Fatal("an unset declared variable was accepted")
	}
	if !strings.Contains(err.Error(), "is not set in pika's environment") {
		t.Errorf("error = %q, want it to name the unset variable", err)
	}
}

// A placeholder outside the set of five is a typo, and a template that
// silently failed to substitute it would produce an argv that looks
// deliberate and is wrong.
func TestUnknownPlaceholderIsRefused(t *testing.T) {
	installFakeBinary(t, RuntimeCustom)
	f := newProcessFixture(t)
	runner, err := New(Agent{
		Name: "builder", Runtime: RuntimeCustom, Command: "custom",
		Args: []string{"--prompt", "{promt}"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = runner.Run(context.Background(), f.root, f.promptPath, f.outputPath)
	if err == nil {
		t.Fatal("a template with an unknown placeholder ran")
	}
	if !strings.Contains(err.Error(), `builds an unknown placeholder "{promt}"`) {
		t.Errorf("error = %q, want it to name the placeholder", err)
	}
}

// codex writes its own message to {output}. An override that drops the
// placeholder leaves nothing to write it, and the run would end with an
// empty bundle blamed on the harness.
func TestOutputFileRequiresTheOutputPlaceholder(t *testing.T) {
	installFakeBinary(t, RuntimeCodex)
	f := newProcessFixture(t)
	runner, err := New(Agent{
		Name: "builder", Runtime: RuntimeCodex,
		Args: []string{"exec", "--cd", "{root}", "-"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = runner.Run(context.Background(), f.root, f.promptPath, f.outputPath)
	if err == nil {
		t.Fatal("an override that drops {output} ran anyway")
	}
	if !strings.Contains(err.Error(), "drops {output}") {
		t.Errorf("error = %q, want it to name the dropped placeholder", err)
	}
}

// A custom agent takes the operator's argv verbatim, placeholders
// included, and a template naming {output} means the harness writes the
// message itself.
func TestCustomAgentRunsTheDeclaredCommandAndArgv(t *testing.T) {
	f := newProcessFixture(t)
	dir := t.TempDir()
	command := filepath.Join(dir, "harness")
	if err := os.WriteFile(command, []byte(fakeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_AGENT_FILE", f.outputPath)
	runner, err := New(Agent{
		Name: "builder", Runtime: RuntimeCustom, Command: command,
		Args: []string{"--root", "{root}", "--prompt", "{prompt}", "--out", "{output}"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("no /bin/sh to script a fake harness on windows")
	}
	if err := runner.Run(context.Background(), f.root, f.promptPath, f.outputPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertArgvFields(t, f.argv(t), []string{
		"--root", f.root, "--prompt", f.promptPath, "--out", f.outputPath,
	})
	// The template names {output}, so the harness wrote the message and
	// the runner did not tee stdout over it.
	got, err := os.ReadFile(f.outputPath)
	if err != nil {
		t.Fatalf("the custom harness wrote no message: %v", err)
	}
	if string(got) != "fake agent final message" {
		t.Errorf("message = %q, want what the harness wrote", got)
	}
}

// A runtime that declares OutputFile gets the message the harness wrote,
// untouched.
func TestOutputFileLeavesTheHarnessMessageAlone(t *testing.T) {
	installFakeBinary(t, RuntimeCodex)
	f := newProcessFixture(t)
	t.Setenv("FAKE_AGENT_FILE", f.outputPath)
	t.Setenv("FAKE_AGENT_MESSAGE", "written by the harness")
	runner, err := New(Agent{Name: "builder", Runtime: RuntimeCodex})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := runner.Run(context.Background(), f.root, f.promptPath, f.outputPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := os.ReadFile(f.outputPath)
	if err != nil {
		t.Fatalf("no message: %v", err)
	}
	if string(got) != "written by the harness" {
		t.Errorf("message = %q, want what the harness wrote", got)
	}
}
