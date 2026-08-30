// Package verify implements the verification ladder engine behind
// `pika check` (spec §12.6). Gates run in the order given by the
// CheckSet; a failing gate stops every downstream gate, recorded as skips
// with reasons. Skipped gates (a discovery sentinel with no discovered
// command) do not stop the ladder. M1 check runs deterministic gates only:
// rung 5, independent agent review, is never part of check (spec §16).
package verify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// defaultGateTimeout bounds each gate execution unless overridden
	// with WithGateTimeout.
	defaultGateTimeout = 10 * time.Minute

	// defaultReapDelay bounds how long Wait may block on orphaned
	// output-pipe holders (grandchildren) after a timeout kill unless
	// overridden with WithReapDelay.
	defaultReapDelay = 5 * time.Second

	// outputTailBytes keeps the last 8 KiB of combined gate output
	// (spec §14.1 bounded output summaries).
	outputTailBytes = 8 * 1024

	// unixDevNull is the null-device path as it appears in contract and
	// pack commands, which are written Unix-first. See portableArgv.
	unixDevNull = "/dev/null"
)

// LadderEnvVar names the marker every spawned gate carries: the chain of
// trees the enclosing ladders are verifying, outermost first, joined with
// the platform's path-list separator. Run reads it on entry and refuses a
// run whose target tree is already in the chain. It is the only
// environment variable the kernel sets.
const LadderEnvVar = "PIKA_CHECK_LADDER"

// ErrNestedRun is returned by Run when the tree it was asked to verify is
// already under verification by an enclosing ladder. Callers report it;
// falling back to running the ladder anyway would restore the loop it
// exists to cut.
var ErrNestedRun = errors.New("verify: refusing to re-enter a running ladder")

// runConfig carries the tunables injected through Run options; tests use
// the deadlines to exercise real timeouts without waiting minutes.
type runConfig struct {
	gateTimeout time.Duration
	reapDelay   time.Duration
	dir         string
	// gateEnv is the fully built environment for every spawned gate:
	// this process's environment with the ladder marker rewritten.
	// Computed once per Run, not injected by an Option.
	gateEnv []string
}

// Option tunes a Run.
type Option func(*runConfig)

// WithGateTimeout overrides the per-gate execution deadline.
func WithGateTimeout(d time.Duration) Option {
	return func(rc *runConfig) { rc.gateTimeout = d }
}

// WithReapDelay overrides how long Wait may block on orphaned pipe
// holders after the timeout kill.
func WithReapDelay(d time.Duration) Option {
	return func(rc *runConfig) { rc.reapDelay = d }
}

// WithDir runs every command gate in dir instead of the process working
// directory. `pika check` passes the resolved repository root, so a check
// invoked from a subdirectory — or against an explicit --root — verifies
// the repository the contract describes rather than wherever the caller
// happened to stand. Unset, gates keep inheriting the process working
// directory. In-process gates (Gate.Func, e.g. gate 1) never spawn a
// command and take their root as an explicit argument, so they are
// unaffected.
func WithDir(dir string) Option {
	return func(rc *runConfig) { rc.dir = dir }
}

// Gate statuses recorded in GateResult.
const (
	StatusPass = "pass"
	StatusFail = "fail"
	StatusSkip = "skip"
)

// ScopeSkipReason records a gate skipped because the change set is empty:
// the tree is clean, so there is nothing for that gate to check. It is
// deliberately distinct from the discovery reason ("no command discovered
// for <id>") and the cascade reason ("skipped: gate <id> failed"): a
// reader of the report must be able to tell "this gate had nothing to
// check" from "this gate had no command" and from "an earlier gate
// failed". Narrowed verification is only trustworthy when it says so in
// its own words.
const ScopeSkipReason = "no changed files in scope"

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

// Scope selects which gates a Run covers. Changed narrows the ladder to
// the packages a caller resolved as touched — the caller decides that and
// hands Run the narrowed CheckSet, marking out-of-scope gates with
// ScopeSkipReason; Run itself executes whatever it is given. CI implies
// All and forbids interactive prompts — check runs nothing interactive by
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
// downstream gate. Gate failures are data in the Report, not errors; Run
// returns an error only for a malformed CheckSet or for a nested run
// (ErrNestedRun). Options tune the per-gate execution deadline and
// post-kill reap delay (used by tests to exercise real deadlines
// quickly).
func Run(ctx context.Context, cs CheckSet, scope Scope, opts ...Option) (*Report, error) {
	rc := runConfig{gateTimeout: defaultGateTimeout, reapDelay: defaultReapDelay}
	for _, opt := range opts {
		opt(&rc)
	}

	// The re-entrancy guard, before any gate runs. `pika check`'s test
	// gate runs the repository's own suite, and that suite can invoke
	// pika: in M1.5 the loop re-entered every ~13 seconds until the
	// machine held ~20 orphaned drivers.
	//
	// Refusing — rather than skipping — is deliberate. A skipped gate is
	// StatusSkip, Pass is Summary.Fail == 0, so a silent skip would
	// return a green report for a ladder that never ran: the exact
	// failure class this guard exists to prevent.
	//
	// The guard is scoped to the tree under verification, not to the
	// process. A ladder verifying a fixture in a temp directory is not
	// the loop — it terminates — and refusing it would forbid every
	// hermetic test that runs check against a fixture.
	target, err := targetDir(rc.dir)
	if err != nil {
		return nil, err
	}
	chain := ladderChain(os.Getenv(LadderEnvVar))
	for _, enclosing := range chain {
		if enclosing == target {
			return nil, fmt.Errorf(
				"%w: %s (enclosing ladders: %s); a gate re-entered the ladder that spawned it — pin the inner command to a different root",
				ErrNestedRun, target, strings.Join(chain, ", "))
		}
	}
	rc.gateEnv = gateEnvironment(os.Environ(),
		strings.Join(append(chain, target), string(os.PathListSeparator)))

	rep := &Report{Gates: make([]GateResult, 0, len(cs))}
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
		res, err := runGate(ctx, g, rc)
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
func runGate(ctx context.Context, g Gate, rc runConfig) (GateResult, error) {
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

	argv := portableArgv(g.Cmd)

	ctx, cancel := context.WithTimeout(ctx, rc.gateTimeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// An unset dir leaves cmd.Dir empty, which is exec's own "inherit the
	// process working directory" default.
	cmd.Dir = rc.dir
	// Setting cmd.Env replaces the inherited environment wholesale, so
	// gateEnv carries every inherited entry forward; see gateEnvironment.
	cmd.Env = rc.gateEnv
	// Stdin stays nil: gates read /dev/null and can never prompt.
	setGroup(cmd)
	// exec.CommandContext's default Cancel kills only the direct child;
	// kill the gate's whole process group instead so grandchildren die
	// with it.
	cmd.Cancel = func() error { return killGroup(cmd) }
	// A grandchild that escaped the group (or a platform without group
	// kill) can still hold the combined-output pipe forever; WaitDelay
	// makes Wait close the pipes and return after reapDelay regardless,
	// so check can never hang past the deadline.
	cmd.WaitDelay = rc.reapDelay
	output, err := cmd.CombinedOutput()
	res := GateResult{
		ID:         g.ID,
		Cmd:        argv,
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
		res.Reason = fmt.Sprintf("gate timed out after %s", rc.gateTimeout)
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

// targetDir resolves the tree a Run verifies. An unset dir means the
// process working directory, which is exec's own default for a gate.
func targetDir(dir string) (string, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("verify: resolving the working directory: %w", err)
		}
		dir = wd
	}
	return canonicalDir(dir), nil
}

// canonicalDir puts a directory in the one comparable form the ladder
// chain uses: absolute and, where the path exists, symlink-free. Symlink
// resolution is what makes the guard hold on macOS, where /tmp and
// /var/folders are symlinks: two spellings of one tree must not read as
// two different trees. A path that cannot be resolved keeps its absolute
// form — a gate against a nonexistent directory fails on its own terms.
func canonicalDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// ladderChain parses the marker into the trees the enclosing ladders are
// verifying. Entries are canonicalized on the way in so a hand-set marker
// compares the same as one this package wrote.
func ladderChain(raw string) []string {
	parts := filepath.SplitList(raw)
	chain := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		chain = append(chain, canonicalDir(p))
	}
	return chain
}

// gateEnvironment builds the environment for a spawned gate: everything
// this process inherited, with the ladder marker set to chain.
//
// cmd.Env replaces the whole environment rather than extending it, so the
// inherited entries are copied explicitly. A gate stripped of PATH, HOME
// or GOCACHE would fail as a toolchain error and read as a broken
// repository rather than as a broken kernel.
//
// An inherited marker is dropped rather than left beside the new one:
// duplicate keys in an environment block resolve last-wins on some
// platforms and first-wins on others, and a gate must not have to guess
// which chain it is in.
func gateEnvironment(parent []string, chain string) []string {
	prefix := LadderEnvVar + "="
	env := make([]string, 0, len(parent)+1)
	for _, kv := range parent {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		env = append(env, kv)
	}
	return append(env, prefix+chain)
}

// portableArgv rewrites an argument that is exactly "/dev/null" to
// os.DevNull, so a gate command written against Unix still names the null
// device on Windows ("NUL"). On Unix the two strings are equal and this is
// the identity, allocating nothing.
//
// It exists for one concrete gate. The go@1 pack's typecheck command is
// `go build -o /dev/null ./...`; the `-o /dev/null` keeps the gate from
// linking a binary into the repository it is verifying. cmd/go decides
// whether -o names the null sink with base.IsNull
// (GOROOT/src/cmd/go/internal/base/path.go), which compares the value
// against os.DevNull — "NUL" on Windows — so a literal "/dev/null" there
// is an ordinary output path, and cmd/go then rejects it:
// "go: cannot write multiple packages to non-directory /dev/null"
// (GOROOT/src/cmd/go/internal/work/build.go). The gate would hard-fail on
// every Windows checkout.
//
// Deliberately exact-match only. This is not general path translation:
// gate argv goes to exec verbatim (spec §16), and rewriting anything
// broader would make a gate command mean something different from what
// the contract says.
func portableArgv(cmd []string) []string { return substituteNullDevice(cmd, os.DevNull) }

// substituteNullDevice is portableArgv with the platform's null device
// injected, so the Windows rewrite is testable from any host.
func substituteNullDevice(cmd []string, devNull string) []string {
	if devNull == unixDevNull {
		return cmd
	}
	// Copy on first hit only: the overwhelmingly common gate has no
	// "/dev/null" argument at all and must not allocate.
	argv := cmd
	for i, arg := range cmd {
		if arg != unixDevNull {
			continue
		}
		if len(argv) == 0 || &argv[0] == &cmd[0] {
			argv = append([]string(nil), cmd...)
		}
		argv[i] = devNull
	}
	return argv
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
