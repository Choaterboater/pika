package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/contract"
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

// The slot's fail-on-output must reach the gate whenever the gate's argv
// is the argv the pack declared, on every path that argv can arrive by —
// above all the contract override: `pika init` writes the pack's own hint
// into contract.commands, so for a scaffolded repository the override IS
// the pack's command, and dropping the flag there would restore the
// format gate that cannot fail. Slots without the flag must not acquire
// it. TestContractCommandDoesNotInheritThePacksOutputRule pins the other
// half: an override naming a DIFFERENT command inherits nothing.
func TestFromProfilesCarriesFailOnOutputOntoThePacksOwnCommand(t *testing.T) {
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

// The cobra defect, at the layer that caused it. `pika adopt` reads the
// repository's own Makefile and writes `format: make fmt`. The go@1 pack
// declares fail-on-output for `gofmt -l .` and for nothing else, so the
// adopted command must arrive at the gate judged on its exit status —
// `make fmt` prints while succeeding, and reading its narration as drift
// produced `FAIL format exit=0` for a command that had just succeeded.
func TestContractCommandDoesNotInheritThePacksOutputRule(t *testing.T) {
	cs := profiles.CheckSet{
		Format:    profiles.Check{ID: "format", Discovery: true, Hint: []string{"gofmt", "-l", "."}, FailOnOutput: true},
		Lint:      profiles.Check{ID: "lint", Discovery: true},
		Typecheck: profiles.Check{ID: "typecheck", Discovery: true},
		Test:      profiles.Check{ID: "test", Discovery: true},
		Smoke:     profiles.Check{ID: "smoke", Discovery: true},
	}
	gates, err := FromProfiles(cs, map[string]string{"format": "make fmt"})
	if err != nil {
		t.Fatal(err)
	}
	if gates[0].FailOnOutput {
		t.Errorf("format gate = %+v: `make fmt` acquired a success criterion the pack wrote for `gofmt -l .`", gates[0])
	}
}

// End to end on this repository's own configuration: resolve the packs
// pika's contract declares, build the ladder from the commands pika's
// contract sets, and run the format gate against a tree holding a file
// gofmt would rewrite. It must fail. Everything above narrows when the
// pack's output rule applies; this is the assertion that pika did not
// narrow it away from itself.
func TestPikasOwnFormatGateStillFailsOnUnformattedGo(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skipf("gofmt is not installed: %v", err)
	}
	c, err := contract.Load(filepath.Join("..", "..", ".project", "contract.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := profiles.Resolve(c.Profiles)
	if err != nil {
		t.Fatal(err)
	}
	gates, err := FromProfiles(resolved.Checks, c.Commands)
	if err != nil {
		t.Fatal(err)
	}
	if !gates[0].FailOnOutput {
		t.Fatalf("format gate = %+v: pika's own format command lost the criterion that lets it fail", gates[0])
	}

	// gofmt -l names files it would rewrite and still exits 0, so the
	// listing is the whole finding.
	dir := t.TempDir()
	unformatted := "package drift\nfunc  Drift( ) {\n\t\tx:=1\n_ = x\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "drift.go"), []byte(unformatted), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), CheckSet{gates[0]}, All, WithDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	got := rep.Gates[0]
	if got.Status != StatusFail {
		t.Fatalf("format = %+v, want fail: drift.go is misformatted", got)
	}
	if !strings.Contains(got.OutputTail, "drift.go") {
		t.Errorf("format output tail = %q, want the misformatted file named", got.OutputTail)
	}
	if got.Exit == 0 {
		t.Errorf("format = %+v: a failed gate must not report exit 0", got)
	}
}
