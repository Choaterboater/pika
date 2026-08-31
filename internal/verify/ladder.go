package verify

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Choaterboater/pika/internal/profiles"
)

// slotOrder realizes spec §12.6 rungs 2–4 in order: rung 2 is formatting,
// lint, compilation, and type checks; rung 3 is affected behavioral tests;
// rung 4 is real-surface smoke. Rung 1 (contract and generated projection
// checks) is Task 8's surface: the check command prepends it as the
// in-process "contract" gate. Rung 5 is agent review and never part of
// check.
var slotOrder = []string{"format", "lint", "typecheck", "test", "smoke"}

// FromProfiles converts a resolved profile CheckSet plus the contract's
// commands into the ordered gate list M1's check runs. A contract command
// overrides a discovery sentinel; a discovery sentinel with no discovered
// command becomes a skip with a recorded reason, not a failure.
//
// A slot's FailOnOutput reaches the gate only when the gate's argv is the
// argv the pack declared for that slot — its command, or the hint a
// discovery sentinel carries. The flag is not a property of the format
// slot; it is the pack saying how to read the output of ONE command it
// named. `gofmt -l .` lists misformatted files and exits 0, so its output
// is the finding. `make fmt`, `prettier --write`, `black .` and
// `cargo fmt` all print while succeeding, and judging them by the same
// rule failed gates whose commands had just succeeded — reported, with no
// possible reading, as `FAIL format exit=0`.
//
// The scaffolded path is unaffected, which is the point of comparing argv
// rather than dropping the flag on override: `pika init` and `pika apply`
// write the pack's own hint into contract.commands verbatim, so for a
// pika-scaffolded repository the override IS the pack's command and the
// flag rides along. It is `pika adopt`, which writes whatever command the
// foreign repository already had, that must not inherit a criterion
// written for a command it replaced.
func FromProfiles(cs profiles.CheckSet, commands map[string]string) (CheckSet, error) {
	slots := map[string]profiles.Check{
		"format":    cs.Format,
		"lint":      cs.Lint,
		"typecheck": cs.Typecheck,
		"test":      cs.Test,
		"smoke":     cs.Smoke,
	}
	gates := make(CheckSet, 0, len(slotOrder))
	for _, id := range slotOrder {
		slot := slots[id]
		if raw, ok := commands[id]; ok {
			argv, err := splitCommand(id, raw)
			if err != nil {
				return nil, err
			}
			gates = append(gates, Gate{
				ID:           id,
				Cmd:          argv,
				FailOnOutput: slot.FailOnOutput && slices.Equal(argv, declaredCommand(slot)),
			})
			continue
		}
		switch {
		case len(slot.Cmd) > 0:
			gates = append(gates, Gate{ID: id, Cmd: slot.Cmd, FailOnOutput: slot.FailOnOutput})
		case slot.Discovery:
			gates = append(gates, Gate{
				ID:         id,
				SkipReason: fmt.Sprintf("no command discovered for %s", id),
			})
		default:
			return nil, fmt.Errorf("verify: check %q has neither command nor discovery sentinel", id)
		}
	}
	return gates, nil
}

// declaredCommand is the argv the pack named for a slot: its command, or
// the hint a discovery sentinel offers in place of one. It is the only
// argv a pack's fail-on-output declaration can be about, because it is
// the only argv the pack ever saw. A sentinel with no hint declares
// nothing, and profiles.checkSet already refuses fail-on-output there.
func declaredCommand(slot profiles.Check) []string {
	if len(slot.Cmd) > 0 {
		return slot.Cmd
	}
	return slot.Hint
}

// splitCommand splits a contract command string into argv on whitespace.
// No shell is involved and no environment expansion is performed: the
// resulting argv goes to exec verbatim (spec §16 deterministic checks).
func splitCommand(id, raw string) ([]string, error) {
	argv := strings.Fields(raw)
	if len(argv) == 0 {
		return nil, fmt.Errorf("verify: contract command %q is empty", id)
	}
	return argv, nil
}
