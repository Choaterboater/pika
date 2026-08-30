package verify

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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
