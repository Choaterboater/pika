package verify

import (
	"context"
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

func TestChangedScopeWarnsAndRunsAll(t *testing.T) {
	cs := CheckSet{{ID: "g1", Cmd: []string{"true"}}}
	rep, err := Run(context.Background(), cs, Changed)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Gates[0].Status != StatusPass {
		t.Fatalf("changed scope must run gates in M1; got %q", rep.Gates[0].Status)
	}
	if len(rep.Warnings) == 0 {
		t.Fatal("changed scope must record a warning in the report")
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
