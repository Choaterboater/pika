package verify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGateFailureStopsLadder(t *testing.T) {
	cs := CheckSet{
		{ID: "g1", Cmd: []string{"true"}},
		{ID: "g2", Cmd: []string{"false"}},
		{ID: "g3", Cmd: []string{"true"}},
	}
	rep, err := Run(context.Background(), cs, All)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Regressions) == 0 {
		t.Fatal("expected regression recorded for g2")
	}
	if rep.Regressions[0].Gate != "g2" {
		t.Fatalf("regression gate = %q, want g2", rep.Regressions[0].Gate)
	}
	if rep.Pass {
		t.Fatal("report.Pass = true, want false")
	}
	if rep.Summary.Fail != 1 || rep.Summary.Pass != 1 {
		t.Fatalf("summary = %+v, want pass=1 fail=1", rep.Summary)
	}
	// g3 depends on g2 and must not have run.
	g3 := rep.Gates[2]
	if g3.Status != StatusSkip {
		t.Fatalf("g3 status = %q, want skip", g3.Status)
	}
	if g3.Reason == "" {
		t.Fatal("g3 skip recorded without reason")
	}
	if g3.Exit != 0 || g3.DurationMs != 0 {
		t.Fatalf("g3 = %+v, want untouched zero exit and duration", g3)
	}
}

func TestAllPassReport(t *testing.T) {
	cs := CheckSet{
		{ID: "g1", Cmd: []string{"true"}},
		{ID: "g2", Func: func(context.Context) (int, string) { return 0, "" }},
	}
	rep, err := Run(context.Background(), cs, All)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pass {
		t.Fatal("report.Pass = false, want true")
	}
	for _, g := range rep.Gates {
		if g.Status != StatusPass || g.Exit != 0 {
			t.Fatalf("gate %s = %+v, want pass with exit 0", g.ID, g)
		}
	}
}

func TestSkippedGateDoesNotStopLadder(t *testing.T) {
	cs := CheckSet{
		{ID: "g1", SkipReason: "no command discovered for g1"},
		{ID: "g2", Cmd: []string{"true"}},
	}
	rep, err := Run(context.Background(), cs, All)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pass {
		t.Fatal("skip must not fail the ladder")
	}
	if rep.Gates[0].Status != StatusSkip || rep.Gates[0].Reason == "" {
		t.Fatalf("g1 = %+v, want skip with reason", rep.Gates[0])
	}
	if rep.Gates[1].Status != StatusPass {
		t.Fatalf("g2 status = %q, want pass (skip must not stop the ladder)", rep.Gates[1].Status)
	}
	if rep.Summary.Skip != 1 || rep.Summary.Pass != 1 {
		t.Fatalf("summary = %+v, want pass=1 skip=1", rep.Summary)
	}
}

func TestFuncGateFailureRecorded(t *testing.T) {
	cs := CheckSet{
		{ID: "g1", Func: func(context.Context) (int, string) { return 1, "schema 2 unsupported" }},
		{ID: "g2", Cmd: []string{"true"}},
	}
	rep, err := Run(context.Background(), cs, All)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pass {
		t.Fatal("report.Pass = true, want false")
	}
	if got := rep.Gates[0]; got.Status != StatusFail || got.Exit != 1 || got.OutputTail == "" {
		t.Fatalf("g1 = %+v, want fail with exit 1 and output", got)
	}
	if rep.Gates[1].Status != StatusSkip {
		t.Fatalf("g2 status = %q, want skip", rep.Gates[1].Status)
	}
}

func TestOutputTailKeepsLast8KB(t *testing.T) {
	const big = 20 * 1024
	cs := CheckSet{{
		ID: "noisy",
		Func: func(context.Context) (int, string) {
			return 1, strings.Repeat("x", big) + "END"
		},
	}}
	rep, err := Run(context.Background(), cs, All)
	if err != nil {
		t.Fatal(err)
	}
	tail := rep.Gates[0].OutputTail
	if len(tail) != outputTailBytes {
		t.Fatalf("output tail length = %d, want %d", len(tail), outputTailBytes)
	}
	if !strings.HasSuffix(tail, "END") {
		t.Fatal("output tail lost the most recent output")
	}
}

// The reserved-scope warning is gone: --changed is a real scope now, and
// a warning claiming otherwise would be a lie in every report.
func TestChangedScopeRunsGatesWithoutTheReservedWarning(t *testing.T) {
	cs := CheckSet{{ID: "g1", Cmd: []string{"true"}}}
	rep, err := Run(context.Background(), cs, Changed)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Gates[0].Status != StatusPass {
		t.Fatalf("changed scope must run the gates it is given; got %q", rep.Gates[0].Status)
	}
	for _, w := range rep.Warnings {
		if strings.Contains(w, "reserved") {
			t.Errorf("changed scope still warns %q", w)
		}
	}
}

// A gate skipped for scope must be distinguishable in the report from a
// gate with no discovered command and from a gate skipped by a cascade.
// Collapsing the three would make a narrowed run indistinguishable from a
// broken one.
func TestScopeSkipReasonIsDistinct(t *testing.T) {
	cs := CheckSet{
		{ID: "contract", Cmd: []string{"true"}},
		{ID: "lint", SkipReason: ScopeSkipReason},
		{ID: "test", SkipReason: "no command discovered for test"},
	}
	rep, err := Run(context.Background(), cs, Changed)
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Gates[1].Reason; got != ScopeSkipReason {
		t.Errorf("scope skip reason = %q, want %q", got, ScopeSkipReason)
	}
	if rep.Gates[1].Status != StatusSkip {
		t.Errorf("scope-skipped gate status = %q, want %q", rep.Gates[1].Status, StatusSkip)
	}
	if rep.Gates[1].Reason == rep.Gates[2].Reason {
		t.Error("scope skip reuses the discovery skip reason")
	}
	if strings.Contains(ScopeSkipReason, "no command discovered") ||
		strings.HasPrefix(ScopeSkipReason, "skipped: gate") {
		t.Errorf("ScopeSkipReason = %q collides with an existing reason", ScopeSkipReason)
	}
	// A scope skip must not stop the ladder.
	if rep.Summary.Skip != 2 || rep.Summary.Pass != 1 || !rep.Pass {
		t.Errorf("summary = %+v pass=%v, want 1 pass / 2 skips / pass", rep.Summary, rep.Pass)
	}
}

// `go build -o /dev/null ./...` is the go@1 typecheck gate; the -o keeps
// the gate from linking a binary into the repository. cmd/go recognizes
// the null sink by comparing against os.DevNull, which is "NUL" on
// Windows, so a literal "/dev/null" there is an ordinary path and cmd/go
// refuses to write multiple packages to it. The rewrite is exact-match
// only: nothing else in the argv may move.
func TestNullDeviceArgIsRewrittenForWindows(t *testing.T) {
	cmd := []string{"go", "build", "-o", "/dev/null", "./..."}
	got := substituteNullDevice(cmd, "NUL")
	want := []string{"go", "build", "-o", "NUL", "./..."}
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
	if cmd[3] != "/dev/null" {
		t.Errorf("the caller's argv was mutated: %v", cmd)
	}
}

func TestNullDeviceRewriteIsExactMatchOnly(t *testing.T) {
	// Nothing merely containing the null path is touched: this is not
	// general path translation.
	cmd := []string{"sh", "-c", "echo >/dev/null", "/dev/null/x", "dev/null"}
	got := substituteNullDevice(cmd, "NUL")
	for i := range cmd {
		if got[i] != cmd[i] {
			t.Errorf("argv[%d] = %q, want %q untouched", i, got[i], cmd[i])
		}
	}
}

// On Unix the rewrite is the identity and must not allocate a new slice.
func TestNullDeviceRewriteIsIdentityOnUnix(t *testing.T) {
	cmd := []string{"go", "build", "-o", "/dev/null", "./..."}
	got := substituteNullDevice(cmd, "/dev/null")
	if &got[0] != &cmd[0] {
		t.Error("identity rewrite copied the argv")
	}
}

// The report must show the argv that actually ran, so evidence names the
// real command rather than a pre-translation one.
func TestGateResultReportsTheExecutedArgv(t *testing.T) {
	cs := CheckSet{{ID: "g1", Cmd: []string{"true"}}}
	rep, err := Run(context.Background(), cs, All)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Gates[0].Cmd) != 1 || rep.Gates[0].Cmd[0] != "true" {
		t.Errorf("Cmd = %v, want [true]", rep.Gates[0].Cmd)
	}
}

func TestRunRejectsGateWithoutCmdOrFunc(t *testing.T) {
	cs := CheckSet{{ID: "broken"}}
	if _, err := Run(context.Background(), cs, All); err == nil {
		t.Fatal("expected error for gate with neither cmd nor func")
	}
}

func TestRunRejectsEmptyCmd(t *testing.T) {
	cs := CheckSet{{ID: "broken", Cmd: []string{}}}
	if _, err := Run(context.Background(), cs, All); err == nil {
		t.Fatal("expected error for gate with empty cmd")
	}
}

func TestNoEnvExpansionInGateArgs(t *testing.T) {
	// Gate argv is passed to exec verbatim: no shell, no environment
	// expansion (spec §16 deterministic checks).
	cs := CheckSet{{ID: "g1", Cmd: []string{"echo", "$HOME"}}}
	rep, err := Run(context.Background(), cs, All)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Gates[0].OutputTail, "$HOME") {
		t.Fatalf("output tail %q lost literal $HOME; env expansion leaked into argv", rep.Gates[0].OutputTail)
	}
}

func TestContextCancelFailsGate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cs := CheckSet{{ID: "g1", Cmd: []string{"sleep", "60"}}}
	rep, err := Run(ctx, cs, All)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Gates[0].Status != StatusFail {
		t.Fatalf("cancelled gate status = %q, want fail", rep.Gates[0].Status)
	}
}

// TestGateTimeoutBoundsExecution exercises a real per-gate deadline with a
// short injected timeout (F2): the sleeping gate is reaped on the deadline
// and its downstream gates are skipped, with Run returning in bounded wall
// time instead of waiting out the sleep.
func TestGateTimeoutBoundsExecution(t *testing.T) {
	cs := CheckSet{
		{ID: "g1", Cmd: []string{"sleep", "60"}},
		{ID: "g2", Cmd: []string{"true"}},
	}
	start := time.Now()
	rep, err := Run(context.Background(), cs, All, WithGateTimeout(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run took %s; timeout did not bound the gate", elapsed)
	}
	g1 := rep.Gates[0]
	if g1.Status != StatusFail || g1.Exit != -1 {
		t.Fatalf("g1 = %+v, want fail with exit -1", g1)
	}
	if !strings.Contains(g1.Reason, "timed out") {
		t.Fatalf("g1 reason %q should name the timeout", g1.Reason)
	}
	if rep.Gates[1].Status != StatusSkip || rep.Gates[1].Reason == "" {
		t.Fatalf("g2 = %+v, want skip with reason after timeout", rep.Gates[1])
	}
	if rep.Pass {
		t.Fatal("report.Pass = true after a timeout failure")
	}
}

// TestTimeoutReapsProcessTree asserts the process-group kill: the gate
// shell spawns a background sleep (a grandchild) that inherits the
// combined-output pipe, then waits on it. The group kill must reap both,
// so Run returns near the deadline instead of blocking on the pipe until
// the reap delay. Skipped on Windows, which has no portable group kill
// (documented in sysproc_windows.go).
func TestTimeoutReapsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable process-group kill on Windows")
	}
	cs := CheckSet{{ID: "g1", Cmd: []string{"sh", "-c", "sleep 60 & wait"}}}
	start := time.Now()
	rep, err := Run(context.Background(), cs, All,
		WithGateTimeout(100*time.Millisecond), WithReapDelay(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Run took %s; grandchild was not reaped with the process group", elapsed)
	}
	if rep.Gates[0].Status != StatusFail || !strings.Contains(rep.Gates[0].Reason, "timed out") {
		t.Fatalf("g1 = %+v, want timeout fail", rep.Gates[0])
	}
}

// WithDir is what keeps `pika check --root` (and a check run from a
// subdirectory) honest: without it a gate would verify whatever tree the
// caller happened to stand in and report that as the repository's state.
func TestWithDirRunsGatesInThePassedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	dir := t.TempDir()
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	cs := CheckSet{{ID: "pwd", Cmd: []string{"sh", "-c", "pwd -P"}}}
	rep, err := Run(context.Background(), cs, All, WithDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Gates[0].Status != StatusPass {
		t.Fatalf("gate = %+v, want pass", rep.Gates[0])
	}
	if got := strings.TrimSpace(rep.Gates[0].OutputTail); got != want {
		t.Fatalf("gate ran in %q, want %q", got, want)
	}
}

// Unset, the option must not change anything: gates keep inheriting the
// process working directory.
func TestWithoutDirGatesInheritTheProcessDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatal(err)
	}
	cs := CheckSet{{ID: "pwd", Cmd: []string{"sh", "-c", "pwd -P"}}}
	rep, err := Run(context.Background(), cs, All)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(rep.Gates[0].OutputTail); got != want {
		t.Fatalf("gate ran in %q, want the process directory %q", got, want)
	}
}

// Re-entrancy. `pika check`'s test gate runs the repository's own suite,
// and that suite can invoke pika. In M1.5 that loop re-entered every ~13
// seconds until the machine held ~20 orphaned drivers.
const (
	// reentryDepthEnv switches the test binary into gate mode and carries
	// how deep the chain already is. It is a test-only cap: without it a
	// regression here does not merely fail, it reproduces the incident on
	// the machine running the suite.
	reentryDepthEnv = "PIKA_TEST_LADDER_DEPTH"
	maxReentryDepth = 3
	reentryTimeout  = 10 * time.Second
)

// reentryHelperArgv is the gate command: this test binary, running only
// the helper below. os.Executable is used rather than os.Args[0] because
// the gate runs with its working directory set to the fixture.
func reentryHelperArgv(t *testing.T) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	return []string{exe, "-test.run=^TestReentryHelperLadder$", "-test.timeout=60s"}
}

// TestReentryHelperLadder is not an assertion of its own: it is the gate
// body TestNestedRunIsRefused spawns. Run as a gate, it starts a ladder
// whose only gate is itself — the exact shape of the incident. It takes
// no WithDir, so it targets its own working directory, which runGate set
// to the tree the enclosing ladder is verifying.
func TestReentryHelperLadder(t *testing.T) {
	depth, err := strconv.Atoi(os.Getenv(reentryDepthEnv))
	if err != nil {
		t.Skip("not invoked as a nested ladder gate")
	}
	fmt.Printf("ladder-depth=%d\n", depth)
	if depth >= maxReentryDepth {
		fmt.Println("ladder-recursed")
		return
	}
	t.Setenv(reentryDepthEnv, strconv.Itoa(depth+1))
	cs := CheckSet{{ID: "reenter", Cmd: reentryHelperArgv(t)}}
	rep, err := Run(context.Background(), cs, All, WithGateTimeout(reentryTimeout))
	if err != nil {
		fmt.Printf("ladder-refused: %v\n", err)
		return
	}
	// runGate captures the child's output instead of passing it through,
	// so echo it: without this the depth the chain actually reached is
	// invisible to the outer assertion.
	fmt.Printf("ladder-recursed:\n%s\n", rep.Gates[0].OutputTail)
}

// TestNestedRunIsRefused exercises the recursion rather than asserting on
// a string: the gate really re-invokes the ladder against the tree the
// outer run is verifying. Without the guard the helper recurses and
// "ladder-depth=2" appears in the gate output; the depth cap and the
// short gate timeout keep a regression fast instead of wedging the
// machine.
func TestNestedRunIsRefused(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(reentryDepthEnv, "1")
	cs := CheckSet{{ID: "reenter", Cmd: reentryHelperArgv(t)}}
	rep, err := Run(context.Background(), cs, All, WithDir(dir), WithGateTimeout(reentryTimeout))
	// The outer run targets a fixture, so it is never itself nested —
	// including when this suite runs as pika's own test gate.
	if err != nil {
		t.Fatalf("outer Run refused: %v", err)
	}
	out := rep.Gates[0].OutputTail
	if strings.Contains(out, "ladder-depth=2") {
		t.Fatalf("the ladder re-entered itself; the guard did not hold:\n%s", out)
	}
	if !strings.Contains(out, "ladder-refused") {
		t.Fatalf("the nested run was not refused:\n%s", out)
	}
	// The refusal must name the outer run, not just complain.
	if want := canonicalDir(dir); !strings.Contains(out, want) {
		t.Errorf("refusal does not name the enclosing ladder %q:\n%s", want, out)
	}
	if rep.Gates[0].Status != StatusPass {
		t.Fatalf("gate = %+v, want pass; a refusal is reported, not a hang", rep.Gates[0])
	}
}

// The refusal is an error out of Run, never a skip: a skipped gate is
// StatusSkip and Pass is Summary.Fail == 0, so skipping would hand back a
// green report for a ladder that never ran.
func TestNestedRunReturnsAnErrorNotAGreenReport(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(LadderEnvVar, dir)
	rep, err := Run(context.Background(), CheckSet{{ID: "g1", Cmd: []string{"true"}}}, All, WithDir(dir))
	if !errors.Is(err, ErrNestedRun) {
		t.Fatalf("err = %v, want ErrNestedRun", err)
	}
	if rep != nil {
		t.Fatalf("report = %+v, want none; a refused ladder reports nothing", rep)
	}
}

// A ladder verifying a different tree is not the loop: it terminates.
// Refusing it would forbid every hermetic test that runs check against a
// fixture, which is most of this repository's suite.
func TestLadderForADifferentTreeIsAllowed(t *testing.T) {
	t.Setenv(LadderEnvVar, t.TempDir())
	rep, err := Run(context.Background(), CheckSet{{ID: "g1", Cmd: []string{"true"}}}, All, WithDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Run refused a ladder for an unrelated tree: %v", err)
	}
	if !rep.Pass {
		t.Fatalf("report = %+v, want pass", rep)
	}
}

// The marker is the chain of trees under verification, and the gate must
// see exactly one assignment of it: duplicate keys resolve last-wins on
// some platforms and first-wins on others.
func TestGateEnvironmentCarriesTheMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	outer := t.TempDir()
	dir := t.TempDir()
	t.Setenv(LadderEnvVar, outer)
	cs := CheckSet{{ID: "env", Cmd: []string{"sh", "-c", "env | grep '^" + LadderEnvVar + "=' || true"}}}
	rep, err := Run(context.Background(), cs, All, WithDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	var assignments []string
	for _, line := range strings.Split(rep.Gates[0].OutputTail, "\n") {
		if strings.HasPrefix(line, LadderEnvVar+"=") {
			assignments = append(assignments, line)
		}
	}
	if len(assignments) != 1 {
		t.Fatalf("gate saw %d assignments of %s, want exactly 1:\n%s",
			len(assignments), LadderEnvVar, rep.Gates[0].OutputTail)
	}
	want := strings.Join([]string{canonicalDir(outer), canonicalDir(dir)}, string(os.PathListSeparator))
	if got := strings.TrimPrefix(assignments[0], LadderEnvVar+"="); got != want {
		t.Errorf("marker = %q, want the chain %q", got, want)
	}
}

// Setting cmd.Env owns the whole environment: everything the process
// inherited must still reach the gate, or every toolchain gate breaks in
// a way that looks like a toolchain problem.
func TestUnnestedRunIsUnaffected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	t.Setenv("PIKA_TEST_INHERITED", "kept")
	cs := CheckSet{
		{ID: "inherit", Cmd: []string{"sh", "-c", `printf %s "$PIKA_TEST_INHERITED:$PATH"`}},
		{ID: "fails", Cmd: []string{"false"}},
		{ID: "downstream", Cmd: []string{"true"}},
	}
	rep, err := Run(context.Background(), cs, All, WithDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	got := rep.Gates[0].OutputTail
	inherited, path, _ := strings.Cut(got, ":")
	if inherited != "kept" {
		t.Errorf("gate saw %q for an inherited variable, want %q", inherited, "kept")
	}
	if path == "" {
		t.Error("gate ran without PATH; the inherited environment was dropped")
	}
	// The ladder itself is unchanged: first failure stops it, and the
	// report is not green.
	if rep.Pass || rep.Summary.Pass != 1 || rep.Summary.Fail != 1 || rep.Summary.Skip != 1 {
		t.Fatalf("report = %+v, want pass=1 fail=1 skip=1 and Pass false", rep.Summary)
	}
}

// gateEnvironment is the only place the child environment is built, so
// its two obligations are asserted directly: nothing inherited is lost,
// and an inherited marker is replaced rather than duplicated.
func TestGateEnvironmentReplacesAnInheritedMarker(t *testing.T) {
	parent := []string{"PATH=/bin", LadderEnvVar + "=/old", "HOME=/home/x"}
	env := gateEnvironment(parent, "/a"+string(os.PathListSeparator)+"/b")
	want := []string{"PATH=/bin", "HOME=/home/x", LadderEnvVar + "=/a" + string(os.PathListSeparator) + "/b"}
	if len(env) != len(want) {
		t.Fatalf("env = %q, want %q", env, want)
	}
	for i := range want {
		if env[i] != want[i] {
			t.Fatalf("env = %q, want %q", env, want)
		}
	}
}

// A Func gate returns from runGate before any exec.Cmd exists, so it
// never carries the marker — and needs none: it runs inside this process,
// which cannot re-enter Run without going through the entry guard first.
// What must hold is that Run builds the child environment without
// touching its own: an in-process gate sees exactly the marker the
// process already had, whether that is empty or inherited from an
// enclosing ladder (as it is when this suite runs as pika's test gate).
func TestFuncGateNeedsNoMarker(t *testing.T) {
	before := os.Getenv(LadderEnvVar)
	seen := "<unset>"
	cs := CheckSet{{ID: "inproc", Func: func(context.Context) (int, string) {
		seen = os.Getenv(LadderEnvVar)
		return 0, "ok"
	}}}
	rep, err := Run(context.Background(), cs, All, WithDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pass {
		t.Fatalf("report = %+v, want pass", rep)
	}
	if seen != before {
		t.Errorf("in-process gate saw %s=%q, want the process's own %q; Run must not mutate its own environment",
			LadderEnvVar, seen, before)
	}
}

// The tests below pin the whole of fail-on-output: a checking tool that
// reports by printing while exiting 0 — `gofmt -l .` is the one that
// forced the flag — must fail the gate, silence must pass it, and a gate
// without the flag must go on ignoring output entirely. The last two
// matter as much as the first: a flag that failed every chatty gate, or
// that changed behaviour when unset, would be a new defect rather than a
// fix.

func TestFailOnOutputFailsAGateThatPrintsAndExitsZero(t *testing.T) {
	cs := CheckSet{
		{ID: "format", Cmd: []string{"echo", "internal/verify/verify.go"}, FailOnOutput: true},
		{ID: "lint", Cmd: []string{"true"}},
	}
	rep, err := Run(context.Background(), cs, All)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pass {
		t.Fatalf("report = %+v, want fail: the gate printed a misformatted file", rep)
	}
	got := rep.Gates[0]
	if got.Status != StatusFail {
		t.Fatalf("format status = %q, want fail", got.Status)
	}
	if got.Exit != 0 {
		t.Errorf("format exit = %d, want the real 0: the flag changes the verdict, not the recorded status", got.Exit)
	}
	if got.Reason == "" {
		t.Error("format failed without a reason; a gate that fails on output must say so")
	}
	if !strings.Contains(got.OutputTail, "verify.go") {
		t.Errorf("format output tail = %q, want the offending file", got.OutputTail)
	}
	if len(rep.Regressions) != 1 || rep.Regressions[0].Gate != "format" {
		t.Errorf("regressions = %+v, want one for format", rep.Regressions)
	}
	// A fail-on-output failure stops the ladder like any other.
	if rep.Gates[1].Status != StatusSkip {
		t.Errorf("lint status = %q, want skip after format failed", rep.Gates[1].Status)
	}
}

func TestFailOnOutputPassesASilentGate(t *testing.T) {
	cs := CheckSet{{ID: "format", Cmd: []string{"true"}, FailOnOutput: true}}
	rep, err := Run(context.Background(), cs, All)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pass {
		t.Fatalf("report = %+v, want pass: a silent gate found nothing", rep)
	}
	if rep.Gates[0].Status != StatusPass {
		t.Fatalf("format status = %q, want pass", rep.Gates[0].Status)
	}
}

func TestFailOnOutputTreatsWhitespaceAsSilence(t *testing.T) {
	// A tool that ends its (empty) report with a newline has said
	// nothing. Reading that as drift would fail every clean run.
	cs := CheckSet{{ID: "format", Cmd: []string{"echo", ""}, FailOnOutput: true}}
	rep, err := Run(context.Background(), cs, All)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pass {
		t.Fatalf("report = %+v, want pass: a lone newline is not a report", rep)
	}
}

func TestFailOnOutputUnsetIgnoresOutput(t *testing.T) {
	cs := CheckSet{{ID: "test", Cmd: []string{"echo", "ok 42 tests passed"}}}
	rep, err := Run(context.Background(), cs, All)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pass {
		t.Fatalf("report = %+v, want pass: without the flag, output is not a verdict", rep)
	}
	if rep.Gates[0].Status != StatusPass {
		t.Fatalf("test status = %q, want pass", rep.Gates[0].Status)
	}
	if !strings.Contains(rep.Gates[0].OutputTail, "42 tests passed") {
		t.Errorf("output tail = %q, want the gate's output still captured", rep.Gates[0].OutputTail)
	}
}

// A gate that both fails on its exit status and prints keeps the reason
// that names the exit status: that is the more specific diagnosis, and
// overwriting it would make a compiler error read as a formatting one.
func TestFailOnOutputKeepsTheExitStatusReason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh to print and exit nonzero in one command")
	}
	cs := CheckSet{{ID: "format", Cmd: []string{"sh", "-c", "echo boom; exit 3"}, FailOnOutput: true}}
	rep, err := Run(context.Background(), cs, All)
	if err != nil {
		t.Fatal(err)
	}
	got := rep.Gates[0]
	if got.Status != StatusFail || got.Exit != 3 {
		t.Fatalf("gate = %+v, want fail with exit 3", got)
	}
	if !strings.Contains(got.Reason, "status 3") {
		t.Errorf("reason = %q, want the exit-status diagnosis preserved", got.Reason)
	}
}
