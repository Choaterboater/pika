// Package verify implements the verification ladder engine behind
// `projectctl check` (spec §12.6). Gates run in the order given by the
// CheckSet; a failing gate stops every downstream gate, recorded as skips
// with reasons. Skipped gates (a discovery sentinel with no discovered
// command) do not stop the ladder. M1 check runs deterministic gates only:
// rung 5, independent agent review, is never part of check (spec §16).
package verify

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

const (
	// gateTimeout bounds each gate execution.
	gateTimeout = 10 * time.Minute

	// outputTailBytes keeps the last 8 KiB of combined gate output
	// (spec §14.1 bounded output summaries).
	outputTailBytes = 8 * 1024
)

// Gate statuses recorded in GateResult.
const (
	StatusPass = "pass"
	StatusFail = "fail"
	StatusSkip = "skip"
)

// Gate is one verification rung: an external command (argv, run via exec
// with no shell and no environment expansion) or an in-process Func.
type Gate struct {
	ID  string
	Cmd []string

	// Func runs an in-process gate (for example gate 1's contract and
	// projection validation, Task 8's surface). It returns the gate's
	// exit status and combined output; a nonzero exit fails the gate.
	// When Func is set, Cmd is informational only.
	Func func(ctx context.Context) (exit int, output string)

	// SkipReason, when non-empty, records the gate as skipped with this
	// reason without executing it. A skip does not stop the ladder.
	SkipReason string
}

// CheckSet is the ordered gate list for one Run.
type CheckSet []Gate

// Scope selects which gates a Run covers. M1 treats Changed as All with a
// warning (the change-diff machinery lands in a later task); CI implies All
// and forbids interactive prompts — check runs nothing interactive by
// construction (gates get no stdin).
type Scope int

const (
	All Scope = iota
	Changed
	CI
)

// GateResult is the JSON-visible outcome of one gate.
type GateResult struct {
	ID         string   `json:"id"`
	Cmd        []string `json:"cmd,omitempty"`
	Exit       int      `json:"exit"`
	DurationMs int64    `json:"durationMs"`
	OutputTail string   `json:"outputTail,omitempty"`
	Status     string   `json:"status"`
	Reason     string   `json:"reason,omitempty"`
}

// Failure ties one regression to its gate.
type Failure struct {
	Gate   string `json:"gate"`
	Detail string `json:"detail"`
}

// Summary counts gate outcomes.
type Summary struct {
	Pass int `json:"pass"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// Report is the JSON check report (spec §12.6, §14.1).
type Report struct {
	Gates       []GateResult `json:"gates"`
	Baseline    []Failure    `json:"baseline,omitempty"`
	Regressions []Failure    `json:"regressions,omitempty"`
	Summary     Summary      `json:"summary"`
	Warnings    []string     `json:"warnings,omitempty"`
	DurationMs  int64        `json:"durationMs"`
	Pass        bool         `json:"pass"`
}

// Run executes the ladder: gates in order, first failure skips every
// downstream gate. Run returns an error only for a malformed CheckSet;
// gate failures are data in the Report, not errors.
func Run(ctx context.Context, cs CheckSet, scope Scope) (*Report, error) {
	rep := &Report{Gates: make([]GateResult, 0, len(cs))}
	if scope == Changed {
		rep.Warnings = append(rep.Warnings,
			"--changed is reserved; M1 runs all gates")
	}
	if scope == CI {
		rep.Warnings = append(rep.Warnings,
			"--ci implies --all; no interactive prompts are possible in check")
	}

	start := time.Now()
	failed := ""
	for _, g := range cs {
		if failed != "" {
			rep.Gates = append(rep.Gates, GateResult{
				ID:     g.ID,
				Status: StatusSkip,
				Reason: fmt.Sprintf("skipped: gate %s failed", failed),
			})
			rep.Summary.Skip++
			continue
		}
		res, err := runGate(ctx, g)
		if err != nil {
			return nil, err
		}
		rep.Gates = append(rep.Gates, res)
		switch res.Status {
		case StatusFail:
			failed = g.ID
			rep.Regressions = append(rep.Regressions, Failure{
				Gate:   g.ID,
				Detail: failureDetail(res),
			})
			rep.Summary.Fail++
		case StatusSkip:
			rep.Summary.Skip++
		default:
			rep.Summary.Pass++
		}
	}
	rep.DurationMs = time.Since(start).Milliseconds()
	rep.Pass = rep.Summary.Fail == 0
	return rep, nil
}

// runGate executes one gate and returns its result.
func runGate(ctx context.Context, g Gate) (GateResult, error) {
	if g.SkipReason != "" {
		return GateResult{ID: g.ID, Status: StatusSkip, Reason: g.SkipReason}, nil
	}
	if g.Func != nil {
		start := time.Now()
		exit, output := g.Func(ctx)
		return GateResult{
			ID:         g.ID,
			Exit:       exit,
			DurationMs: time.Since(start).Milliseconds(),
			OutputTail: tail(output),
			Status:     status(exit),
		}, nil
	}
	if len(g.Cmd) == 0 {
		return GateResult{}, fmt.Errorf("verify: gate %q has neither cmd nor func", g.ID)
	}

	ctx, cancel := context.WithTimeout(ctx, gateTimeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, g.Cmd[0], g.Cmd[1:]...)
	// Stdin stays nil: gates read /dev/null and can never prompt.
	output, err := cmd.CombinedOutput()
	res := GateResult{
		ID:         g.ID,
		Cmd:        g.Cmd,
		DurationMs: time.Since(start).Milliseconds(),
		OutputTail: tail(string(output)),
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.Exit = 0
		res.Status = StatusPass
	case ctx.Err() != nil:
		res.Exit = -1
		res.Status = StatusFail
		res.Reason = fmt.Sprintf("gate timed out after %s", gateTimeout)
	default:
		res.Status = StatusFail
		if errors.As(err, &exitErr) {
			res.Exit = exitErr.ExitCode()
			res.Reason = fmt.Sprintf("gate exited with status %d", res.Exit)
		} else {
			res.Exit = -1
			res.Reason = err.Error()
		}
	}
	return res, nil
}

func status(exit int) string {
	if exit == 0 {
		return StatusPass
	}
	return StatusFail
}

// failureDetail builds the human-readable regression detail from a failed
// gate result.
func failureDetail(res GateResult) string {
	detail := res.Reason
	if detail == "" {
		detail = fmt.Sprintf("gate %s failed", res.ID)
	}
	// Evidence carries a bounded output summary with the exit status
	// (spec §14.1).
	if res.OutputTail != "" {
		detail += "\n" + res.OutputTail
	}
	return detail
}

// tail keeps the last outputTailBytes of combined output, trimming any
// partial UTF-8 rune left by the cut.
func tail(s string) string {
	if len(s) <= outputTailBytes {
		return s
	}
	s = s[len(s)-outputTailBytes:]
	for i := 0; i < 3 && len(s) > 0 && s[0]&0xC0 == 0x80; i++ {
		s = s[1:]
	}
	return s
}
