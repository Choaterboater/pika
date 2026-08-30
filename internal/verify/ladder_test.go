package verify

import (
	"testing"

	"github.com/Choaterboater/pika/internal/profiles"
)

func TestFromProfilesOverridesDiscoverySentinels(t *testing.T) {
	cs := profiles.CheckSet{
		Format:    profiles.Check{ID: "format", Discovery: true},
		Lint:      profiles.Check{ID: "lint", Discovery: true},
		Typecheck: profiles.Check{ID: "typecheck", Discovery: true},
		Test:      profiles.Check{ID: "test", Discovery: true},
		Smoke:     profiles.Check{ID: "smoke", Discovery: true},
	}
	gates, err := FromProfiles(cs, map[string]string{
		"format": "gofmt -l .",
		"lint":   "golangci-lint run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 5 {
		t.Fatalf("got %d gates, want 5", len(gates))
	}
	// Spec §12.6 rung order.
	want := []string{"format", "lint", "typecheck", "test", "smoke"}
	for i, id := range want {
		if gates[i].ID != id {
			t.Fatalf("gate %d = %q, want %q", i, gates[i].ID, id)
		}
	}
	if got := gates[0].Cmd; len(got) != 3 || got[0] != "gofmt" || got[1] != "-l" || got[2] != "." {
		t.Fatalf("format cmd = %v, want [gofmt -l .]", got)
	}
	if got := gates[1].Cmd; len(got) != 2 || got[0] != "golangci-lint" || got[1] != "run" {
		t.Fatalf("lint cmd = %v, want [golangci-lint run]", got)
	}
	// Discovery sentinels with no contract command are skips, not failures.
	for _, g := range gates[2:] {
		if g.SkipReason == "" || g.Cmd != nil {
			t.Fatalf("gate %s = %+v, want skip with reason and no cmd", g.ID, g)
		}
	}
}

func TestFromProfilesKeepsProfileCommands(t *testing.T) {
	cs := profiles.CheckSet{
		Format:    profiles.Check{ID: "format", Cmd: []string{"make", "fmt"}},
		Lint:      profiles.Check{ID: "lint", Discovery: true},
		Typecheck: profiles.Check{ID: "typecheck", Discovery: true},
		Test:      profiles.Check{ID: "test", Discovery: true},
		Smoke:     profiles.Check{ID: "smoke", Discovery: true},
	}
	gates, err := FromProfiles(cs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := gates[0].Cmd; len(got) != 2 || got[0] != "make" || got[1] != "fmt" {
		t.Fatalf("format cmd = %v, want [make fmt]", got)
	}
	if gates[1].SkipReason == "" {
		t.Fatalf("lint gate = %+v, want discovery skip", gates[1])
	}
}

// TestFromProfilesOverridesProfileCommand asserts precedence over a real
// profile command (not just a discovery sentinel): a contract command
// replaces the resolved pack's command for the same slot.
func TestFromProfilesOverridesProfileCommand(t *testing.T) {
	cs := profiles.CheckSet{
		Format:    profiles.Check{ID: "format", Discovery: true},
		Lint:      profiles.Check{ID: "lint", Discovery: true},
		Typecheck: profiles.Check{ID: "typecheck", Discovery: true},
		Test:      profiles.Check{ID: "test", Cmd: []string{"go", "test", "./..."}},
		Smoke:     profiles.Check{ID: "smoke", Discovery: true},
	}
	gates, err := FromProfiles(cs, map[string]string{"test": "make test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := gates[3]; got.ID != "test" || len(got.Cmd) != 2 || got.Cmd[0] != "make" || got.Cmd[1] != "test" {
		t.Fatalf("test gate = %+v, want contract command [make test]", gates[3])
	}
}

func TestFromProfilesRejectsEmptyContractCommand(t *testing.T) {
	cs := profiles.CheckSet{Lint: profiles.Check{ID: "lint", Discovery: true}}
	if _, err := FromProfiles(cs, map[string]string{"lint": "   "}); err == nil {
		t.Fatal("expected error for empty contract command")
	}
}

func TestFromProfilesRejectsUndeclaredSlot(t *testing.T) {
	cs := profiles.CheckSet{Lint: profiles.Check{ID: "lint"}}
	if _, err := FromProfiles(cs, nil); err == nil {
		t.Fatal("expected error for slot with neither command nor discovery")
	}
}

// The slot's fail-on-output must reach the gate on every path the
// command can arrive by, above all the contract override: `pika init`
// writes the pack's own hint into contract.commands, so for a scaffolded
// repository the override IS the pack's command, and dropping the flag
// there would restore the format gate that cannot fail. Slots without
// the flag must not acquire it.
func TestFromProfilesCarriesFailOnOutputOntoEveryCommandPath(t *testing.T) {
	cs := profiles.CheckSet{
		Format:    profiles.Check{ID: "format", Discovery: true, Hint: []string{"gofmt", "-l", "."}, FailOnOutput: true},
		Lint:      profiles.Check{ID: "lint", Cmd: []string{"go", "vet", "./..."}},
		Typecheck: profiles.Check{ID: "typecheck", Cmd: []string{"swift", "format", "lint"}, FailOnOutput: true},
		Test:      profiles.Check{ID: "test", Discovery: true},
		Smoke:     profiles.Check{ID: "smoke", Discovery: true},
	}
	gates, err := FromProfiles(cs, map[string]string{"format": "gofmt -l ."})
	if err != nil {
		t.Fatal(err)
	}
	if !gates[0].FailOnOutput {
		t.Error("contract-overridden format gate lost the slot's fail-on-output")
	}
	if gates[1].FailOnOutput {
		t.Error("lint gate acquired fail-on-output its slot never declared")
	}
	if !gates[2].FailOnOutput {
		t.Error("pack-command typecheck gate lost the slot's fail-on-output")
	}
}
