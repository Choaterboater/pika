package conformance

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// These tests cover the corpus's own machinery, and they run on every
// ordinary `go test ./...`: no network, no opt-in, no toolchain.
//
// What they defend is the property the whole task turns on — that this
// corpus can fail, and names the repository and the rung when it does. A
// grader that quietly accepted a wrong verdict, or a manifest that could
// express a self-contradictory expectation, would go green while pika
// was broken, which is worse than having no corpus at all. `smoke: go
// run ./cmd/pika version` survived four milestones on exactly that.

// sha40 matches an exact commit object name.
var sha40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// missingToolSkip is the skip verify records when a gate's own binary is
// not on PATH. Spelled out rather than imported, like every other reason
// the manifest matches on: these are the words an outside consumer
// reads off `check --json`, and a test that imported the constant would
// assert only that pika agrees with itself.
const missingToolSkip = "toolchain not installed"

// Every row must be addressable, pinned, and complete. A branch or a tag
// in SHA would make an upstream commit indistinguishable from a pika
// regression, which is the one thing the corpus may never confuse.
func TestEveryRowIsPinnedAndComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Corpus {
		if r.Name == "" {
			t.Fatalf("a corpus row has no name; nothing it reports could be attributed")
		}
		if seen[r.Name] {
			t.Errorf("two corpus rows are called %q", r.Name)
		}
		seen[r.Name] = true
		if !sha40.MatchString(r.SHA) {
			t.Errorf("%s: SHA = %q, which is not a 40-hex commit; a branch or tag moves and the corpus stops being deterministic", r.Name, r.SHA)
		}
		if !strings.HasPrefix(r.URL, "https://") {
			t.Errorf("%s: URL = %q, want an https clone source", r.Name, r.URL)
		}
		for _, field := range []struct{ name, value string }{
			{"Ref", r.Ref}, {"Why", r.Why}, {"Drift", r.Drift},
		} {
			if strings.TrimSpace(field.value) == "" {
				t.Errorf("%s: %s is empty; a row nobody can explain is a row nobody will maintain", r.Name, field.name)
			}
		}
		if len(r.Profiles) == 0 {
			t.Errorf("%s: no expected profiles, so adopt's detection is unasserted", r.Name)
		}
		if !slices.Contains(r.Needs, "git") {
			t.Errorf("%s: Needs must include git; every row is fetched with it", r.Name)
		}
		if len(r.Gates) == 0 {
			t.Errorf("%s: no expected gates, so the ladder is unasserted", r.Name)
		}
	}
}

// The manifest itself must not be able to express `FAIL ... exit=0`, or
// a skip whose reason is unnamed, or a rung that produced a verdict
// without naming the command that produced it. The corpus asserts all
// three of pika; a manifest that could record any of them would be able
// to bless the very contradiction the corpus exists to catch.
func TestTheManifestCannotRecordAnIncoherentExpectation(t *testing.T) {
	for _, r := range Corpus {
		gateSeen := map[string]bool{}
		for _, g := range r.Gates {
			if gateSeen[g.ID] {
				t.Errorf("%s: gate %s is expected twice", r.Name, g.ID)
			}
			gateSeen[g.ID] = true
			switch g.Status {
			case StatusPass:
				if g.Exit != 0 || g.Reason != "" {
					t.Errorf("%s: gate %s expects a pass but records exit=%d reason=%q", r.Name, g.ID, g.Exit, g.Reason)
				}
			case StatusFail:
				if g.Exit == 0 {
					t.Errorf("%s: gate %s expects a failure with exit 0, which is the contradiction this corpus exists to catch", r.Name, g.ID)
				}
			case StatusSkip:
				if strings.TrimSpace(g.Reason) == "" {
					t.Errorf("%s: gate %s expects a skip and names no reason; an unnamed skip is indistinguishable from a pass", r.Name, g.ID)
				}
				if g.Exit != 0 {
					t.Errorf("%s: gate %s expects a skip and records exit=%d", r.Name, g.ID, g.Exit)
				}
			default:
				t.Errorf("%s: gate %s expects status %q, which is none of pass, fail, skip", r.Name, g.ID, g.Status)
			}
			// A verdict without a command is the shape the whole
			// coverage question turns on: cobra's green format rung and
			// x/sync's green format rung look identical until the
			// manifest says one ran `make fmt` and the other `gofmt -l
			// .`. The contract gate is the only rung that legitimately
			// spawns nothing — it runs in process.
			ran := g.Status == StatusPass || g.Status == StatusFail
			switch {
			case ran && g.ID != "contract" && g.Cmd == "":
				t.Errorf("%s: gate %s expects a %s and names no command; a verdict nobody can attribute to a command is how a pack's hints go unrun while its row stays green", r.Name, g.ID, g.Status)
			case ran && g.ID == "contract" && g.Cmd != "":
				t.Errorf("%s: the contract gate runs in process and records the command %q", r.Name, g.Cmd)
			case g.Status == StatusSkip && g.Cmd != "" && !strings.Contains(g.Reason, missingToolSkip):
				t.Errorf("%s: gate %s expects a skip for %q and records the command %q; only a %q skip resolves a command it never spawned",
					r.Name, g.ID, g.Reason, g.Cmd, missingToolSkip)
			}
		}
	}
}

// The five V1 packs, and which of them the corpus reaches. A pack with
// no row is a pack whose only evidence is code pika wrote itself, and
// the test says so by name rather than leaving it to a reader to notice.
func TestEveryLanguagePackIsExercisedByRealCode(t *testing.T) {
	covered := map[string]bool{}
	for _, r := range Corpus {
		for _, p := range r.Profiles {
			covered[p] = true
		}
	}
	for _, pack := range []string{"core@1", "go@1", "python@1", "typescript@1", "rust@1", "swift@1"} {
		if !covered[pack] {
			t.Errorf("no corpus row detects %s, so that pack has never met code pika did not write", pack)
		}
	}
}

// ladder builds a report from compact "id:status:exit:reason" rows.
func ladder(rows ...string) []Gate {
	gates := make([]Gate, 0, len(rows))
	for _, row := range rows {
		f := strings.SplitN(row, ":", 4)
		g := Gate{ID: f[0], Status: f[1]}
		if len(f) > 2 && f[2] != "" {
			exit, err := strconv.Atoi(f[2])
			if err != nil {
				panic("ladder: " + row + ": " + err.Error())
			}
			g.Exit = exit
		}
		if len(f) > 3 {
			g.Reason = f[3]
		}
		gates = append(gates, g)
	}
	return gates
}

// ran sets the argv on one rung. The compact ladder spelling cannot
// carry it: a skip reason contains colons, so it has to be the last
// field.
func ran(gates []Gate, id string, argv ...string) []Gate {
	for i := range gates {
		if gates[i].ID == id {
			gates[i].Cmd = argv
		}
	}
	return gates
}

// The grader must reject a rung that failed where the manifest expects a
// pass — and reject a rung that passed where the manifest expects a
// failure, in the same breath. A corpus that only notices regressions in
// one direction quietly rots: the day cobra's absent golangci-lint
// appears on a runner image, its lint rung starts passing and every
// later rung starts running, and a grader that shrugged would report
// green while verifying something else entirely.
//
// It must also reject a rung that reached the recorded verdict by
// running something else. That is not a hypothetical: cobra's Makefile
// takes over all four Go slots, so its ladder can be exactly the
// recorded colour while go@1's own commands never execute.
func TestGradeCatchesBothDirections(t *testing.T) {
	r := Repo{
		Name: "row",
		Gates: []GateWant{
			{ID: "contract", Status: StatusPass},
			{ID: "lint", Status: StatusFail, Exit: 2, Cmd: "golangci-lint run"},
			{ID: "test", Status: StatusSkip, Reason: "skipped: gate lint failed"},
		},
	}
	clean := ran(ladder("contract:pass", "lint:fail:2:gate exited with status 2", "test:skip::skipped: gate lint failed"), "lint", "golangci-lint", "run")
	if found := r.Grade(clean); len(found) != 0 {
		t.Errorf("the recorded ladder graded as a disagreement:\n%s", strings.Join(found, "\n"))
	}
	for _, tc := range []struct {
		name  string
		gates []Gate
		want  string
	}{
		{"an expected pass that failed", ladder("contract:fail:1:boom", "lint:fail:2:x", "test:skip::skipped: gate lint failed"), "gate contract: wanted pass, got fail"},
		{"an expected failure that passed", ladder("contract:pass", "lint:pass", "test:skip::skipped: gate lint failed"), "gate lint: wanted fail, got pass"},
		{"a failure with a different exit", ladder("contract:pass", "lint:fail:1:x", "test:skip::skipped: gate lint failed"), "gate lint: failed with exit 1"},
		{"a skip for a different reason", ladder("contract:pass", "lint:fail:2:x", "test:skip::toolchain not installed: pytest is not on PATH"), "gate test: skipped for"},
		{"a rung that never ran", ladder("contract:pass", "lint:fail:2:x"), "the ladder ran [contract lint]"},
		{"a rung that reached the recorded verdict by running something else",
			ran(ladder("contract:pass", "lint:fail:2:x", "test:skip::skipped: gate lint failed"), "lint", "make", "lint"),
			`gate lint: ran "make lint"; the manifest records "golangci-lint run"`},
		{"a rung that ran nothing where the manifest records a command",
			ladder("contract:pass", "lint:fail:2:x", "test:skip::skipped: gate lint failed"),
			`gate lint: ran ""; the manifest records "golangci-lint run"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found := r.Grade(tc.gates)
			if len(found) == 0 {
				t.Fatalf("graded clean; want a disagreement naming %q", tc.want)
			}
			if !strings.Contains(strings.Join(found, "\n"), tc.want) {
				t.Errorf("no disagreement named %q:\n%s", tc.want, strings.Join(found, "\n"))
			}
		})
	}
}

// The self-contradictory results, whatever any manifest expects.
func TestCoherentCatchesAReportThatContradictsItself(t *testing.T) {
	for _, tc := range []struct {
		name  string
		gates []Gate
		want  string
	}{
		{"a failure with exit 0", ladder("format:fail:0:judged on output"), "reported exit 0"},
		{"a failure with no reason", ladder("format:fail:1:"), "gave no reason"},
		{"an unnamed skip", ladder("lint:skip::"), "without naming a reason"},
		{"a pass with a nonzero exit", ladder("test:pass:3:"), "passed and reported exit 3"},
		{"a status that is none of the three", ladder("test:flaky::"), `reported status "flaky"`},
		{"a rung with no id", []Gate{{Status: StatusPass}}, "reported no id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found := Coherent(tc.gates)
			if len(found) == 0 {
				t.Fatalf("accepted %s; want a contradiction naming %q", tc.name, tc.want)
			}
			if !strings.Contains(strings.Join(found, "\n"), tc.want) {
				t.Errorf("no contradiction named %q:\n%s", tc.want, strings.Join(found, "\n"))
			}
		})
	}
	// The verdicts pika legitimately produces must all survive: a
	// negative exit is a sentinel, not an incoherence, and the whole
	// point of the fix was that it comes with a reason.
	ok := ladder(
		"contract:pass",
		"format:fail:-2:gate command exited 0 and printed a report",
		"lint:fail:-1:gate timed out after 10m0s",
		"typecheck:skip::toolchain not installed: tsc is not on PATH",
		"test:skip::no command discovered for test",
		"smoke:skip::skipped: gate test failed",
	)
	if found := Coherent(ok); len(found) != 0 {
		t.Errorf("a coherent report was rejected:\n%s", strings.Join(found, "\n"))
	}
}

// Unrecorded is the c73f368 defect as an invariant. A conflict adopt
// reported and did not record is an adoption that cannot pass its own
// gate 1, and the finding has to name the rule and the path: "adoption
// is incomplete" costs a maintainer the whole investigation.
func TestUnrecordedNamesTheDeviationAdoptDidNotRecord(t *testing.T) {
	rep := AdoptReport{}
	rep.Conflicts = append(rep.Conflicts, struct {
		RuleID string `json:"ruleId"`
		Path   string `json:"path"`
	}{"naming-catch-all", "src/requests/utils.py"})
	found := Unrecorded(rep)
	if len(found) != 1 {
		t.Fatalf("Unrecorded returned %v, want one finding", found)
	}
	for _, want := range []string{"naming-catch-all", "src/requests/utils.py"} {
		if !strings.Contains(found[0], want) {
			t.Errorf("the finding does not name %q: %s", want, found[0])
		}
	}
	rep.Exceptions = append(rep.Exceptions, struct {
		RuleID string `json:"ruleId"`
		Path   string `json:"path"`
	}{"naming-catch-all", "src/requests/utils.py"})
	if found := Unrecorded(rep); len(found) != 0 {
		t.Errorf("a recorded deviation was reported as unrecorded: %v", found)
	}
}

// Pass is what the exit-code assertion is derived from, so it must read
// the ladder rather than the summary pika printed beside it.
func TestPassIsFalseWhenAnyRungIsExpectedToFail(t *testing.T) {
	green := Repo{Gates: []GateWant{{ID: "a", Status: StatusPass}, {ID: "b", Status: StatusSkip, Reason: "x"}}}
	if !green.Pass() {
		t.Error("a ladder of passes and skips reported Pass() = false")
	}
	red := Repo{Gates: []GateWant{{ID: "a", Status: StatusPass}, {ID: "b", Status: StatusFail, Exit: 1}}}
	if red.Pass() {
		t.Error("a ladder with a failing rung reported Pass() = true")
	}
}

// A row whose toolchain is absent, or whose recorded outcome depends on
// a tool this machine has, must say which and why. The check is what
// keeps "this machine cannot run it" from being reported as "pika is
// wrong" — the conflation that makes a corpus ignorable.
func TestMissingNamesTheToolAndWhichSideItIsOn(t *testing.T) {
	absent := Repo{Name: "row", Needs: []string{"git", "a-tool-no-machine-has-9c1f"}}
	why := absent.Missing()
	if !strings.Contains(why, "a-tool-no-machine-has-9c1f is not on PATH") {
		t.Errorf("Missing() = %q, want it to name the absent tool", why)
	}
	// go is on PATH here by construction: these tests run under it.
	present := Repo{Name: "row", Needs: []string{"git"}, Absent: []string{"go"}}
	why = present.Missing()
	if !strings.Contains(why, "go IS on PATH") || !strings.Contains(why, "depends on its absence") {
		t.Errorf("Missing() = %q, want it to say the row's outcome depends on go being absent", why)
	}
	if why := (Repo{Name: "row", Needs: []string{"git", "go"}}).Missing(); why != "" {
		t.Errorf("Missing() = %q on a machine with git and go, want \"\"", why)
	}
}
