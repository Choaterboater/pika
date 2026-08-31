package conformance

import (
	"strings"
	"testing"
)

// These tests run on every ordinary `go test ./...`: no network, no
// opt-in, no toolchain. What they defend is the answer to one question —
// which commands the profile packs declare have ever been spawned
// against code pika did not write.
//
// The question had a wrong answer for a whole milestone and nothing
// noticed, because the only test that asked anything like it,
// TestEveryLanguagePackIsExercisedByRealCode, asks whether a pack was
// DETECTED. cobra is detected as go@1 and then runs `make fmt`, `make
// lint` and nothing else; requests is detected as python@1 and dies at
// the format rung. Both rows were green, both packs counted as covered,
// and seven of the packs' own commands had never executed anywhere.

// Every command a pack declares is either run by a corpus row or listed
// in Unexercised with a reason — and the two lists may not overlap.
//
// The overlap half is the half that rots if it is left out. A command
// listed as unexercised that some row has quietly started running is a
// report telling its reader to go find a repository for a gap that is
// already closed, which is how a coverage list stops being read.
func TestEveryPackCommandIsExercisedOrExplained(t *testing.T) {
	cov, err := CoverageOf(Corpus)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if len(cov) == 0 {
		t.Fatal("no pack declares a command, so this test asserts nothing; the registry walk is broken")
	}
	t.Logf("pack commands executed against foreign code:\n%s", CoverageTable(cov))

	for _, c := range cov {
		gap, listed := GapFor(c.PackCommand)
		switch {
		case c.Exercised() && listed:
			t.Errorf("%s is listed in Unexercised and %s runs it; delete the entry rather than leaving the report pointing at a gap that is already closed",
				c, strings.Join(c.Rows, ", "))
		case !c.Exercised() && !listed:
			t.Errorf("%s has never run against a repository pika did not write, and Unexercised does not say why. "+
				"Add a corpus row that reaches it, or record the reason it cannot be reached.", c)
		case !c.Exercised() && strings.TrimSpace(gap.Why) == "":
			t.Errorf("%s is listed in Unexercised with no reason; a gap nobody can explain is a gap nobody will close", c)
		}
	}

	// The reverse direction: an entry for a command no pack declares any
	// more. Editing a pack's hint must retire its excuse with it, or the
	// list starts vouching for commands that no longer exist.
	declared, err := PackCommands()
	if err != nil {
		t.Fatalf("pack commands: %v", err)
	}
	for _, g := range Unexercised {
		found := false
		for _, pc := range declared {
			if pc.Pack == g.Pack && pc.Slot == g.Slot && pc.Cmd == g.Cmd {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Unexercised records %s %s: %q, which no pack declares; the pack moved and its entry did not",
				g.Pack, g.Slot, g.Cmd)
		}
	}
}

// The two packs the corpus was extended for. Named individually because
// "coverage went down" is a fact nobody can act on: this says which
// command stopped running, in the same words the report uses.
func TestGoAndPythonPackCommandsAllRunOnForeignCode(t *testing.T) {
	cov, err := CoverageOf(Corpus)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	want := map[string][]string{
		"go@1": {
			"gofmt -l .",
			"go vet ./...",
			"go build -o /dev/null ./...",
			"go test ./...",
		},
		"python@1": {
			"ruff format --check .",
			"ruff check .",
			"mypy .",
			"pytest",
		},
	}
	for pack, cmds := range want {
		for _, cmd := range cmds {
			var got *Coverage
			for i := range cov {
				if cov[i].Pack == pack && cov[i].Cmd == cmd {
					got = &cov[i]
					break
				}
			}
			if got == nil {
				t.Errorf("%s no longer declares %q; the pack changed and this expectation did not", pack, cmd)
				continue
			}
			if !got.Exercised() {
				t.Errorf("%s %q is declared and no corpus row runs it. These are the commands `pika init` writes into "+
					"every %s repository it scaffolds; unexercised, their only evidence is code pika wrote itself.", pack, cmd, pack)
			}
		}
	}
}

// A skip never spawned anything, whatever it recorded. The
// toolchain-not-installed skip is the trap: verify resolves the argv,
// records it, and then exec never forks — so a coverage count that read
// the command instead of the verdict would report a command as exercised
// on precisely the machines that could not run it.
func TestCoverageCountsOnlyRungsThatSpawnedSomething(t *testing.T) {
	rows := []Repo{{
		Name:     "row",
		Profiles: []string{"core@1", "python@1"},
		Gates: []GateWant{
			{ID: "format", Status: StatusSkip, Reason: "toolchain not installed: ruff is not on PATH", Cmd: "ruff format --check ."},
			{ID: "lint", Status: StatusSkip, Reason: "no command discovered for lint"},
			{ID: "typecheck", Status: StatusFail, Exit: 1, Cmd: "mypy ."},
			{ID: "test", Status: StatusPass, Cmd: "pytest"},
		},
	}}
	cov, err := CoverageOf(rows)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	for _, tc := range []struct {
		cmd  string
		want bool
	}{
		{"ruff format --check .", false},
		{"ruff check .", false},
		{"mypy .", true},
		{"pytest", true},
	} {
		var got *Coverage
		for i := range cov {
			if cov[i].Pack == "python@1" && cov[i].Cmd == tc.cmd {
				got = &cov[i]
				break
			}
		}
		if got == nil {
			t.Fatalf("python@1 declares no %q", tc.cmd)
		}
		if got.Exercised() != tc.want {
			t.Errorf("%q exercised = %v, want %v (rows %v)", tc.cmd, got.Exercised(), tc.want, got.Rows)
		}
	}
}

// A row must not be credited with a command another pack's row ran. The
// slot matters too: the same string in the wrong rung is a different
// claim about what pika resolved.
func TestCoverageAttributesACommandToItsOwnPackAndSlot(t *testing.T) {
	rows := []Repo{{
		Name:     "impostor",
		Profiles: []string{"core@1", "go@1"},
		Gates: []GateWant{
			// The right command in the wrong rung.
			{ID: "lint", Status: StatusPass, Cmd: "gofmt -l ."},
		},
	}}
	cov, err := CoverageOf(rows)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	for _, c := range cov {
		if c.Exercised() {
			t.Errorf("%s was credited to %v, and no row ran it in its own slot", c, c.Rows)
		}
	}
}

// The registry walk itself. A slot that declares neither a command nor a
// hint has nothing to run and must be absent rather than counted as an
// uncovered command: core's five sentinels and every pack's smoke slot
// would otherwise be permanent entries in a report about gaps.
func TestPackCommandsListsOnlyWhatThePacksDeclare(t *testing.T) {
	declared, err := PackCommands()
	if err != nil {
		t.Fatalf("pack commands: %v", err)
	}
	byPack := map[string][]string{}
	for _, pc := range declared {
		byPack[pc.Pack] = append(byPack[pc.Pack], pc.Slot)
	}
	if got := byPack["core@1"]; len(got) != 0 {
		t.Errorf("core@1 declares commands for %v; every core slot is a bare discovery sentinel", got)
	}
	if got := strings.Join(byPack["go@1"], " "); got != "format lint typecheck test" {
		t.Errorf("go@1 declares commands for [%s], want [format lint typecheck test]", got)
	}
	if got := strings.Join(byPack["swift@1"], " "); got != "format lint typecheck test" {
		t.Errorf("swift@1 declares commands for [%s], want [format lint typecheck test]; its smoke slot carries no hint", got)
	}
	// Explicit and hint are different promises, and the report says
	// which: an explicit command runs wherever the slot is not
	// overridden, a hint runs only if apply autofills it or a repository
	// asks for the same string by itself.
	for _, tc := range []struct {
		pack, slot string
		explicit   bool
		autofill   bool
	}{
		{"go@1", "format", false, true},
		{"go@1", "test", true, false},
		{"typescript@1", "test", false, false},
		{"rust@1", "typecheck", true, false},
	} {
		var got *PackCommand
		for i := range declared {
			if declared[i].Pack == tc.pack && declared[i].Slot == tc.slot {
				got = &declared[i]
				break
			}
		}
		if got == nil {
			t.Errorf("%s declares nothing for %s", tc.pack, tc.slot)
			continue
		}
		if got.Explicit != tc.explicit || got.Autofill != tc.autofill {
			t.Errorf("%s: explicit=%v autofill=%v, want explicit=%v autofill=%v",
				got, got.Explicit, got.Autofill, tc.explicit, tc.autofill)
		}
	}
}
