package verify

import (
	"fmt"
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
// A slot's FailOnOutput rides onto its gate whatever the command turned
// out to be, including a contract override. The flag is the pack's
// statement about the slot's success criterion for this stack — in Go, a
// format gate that has anything to say has found drift — and a contract
// that adopts the slot adopts that criterion. Dropping it on override
// would silently restore the gate that cannot fail, which is the whole
// defect: `pika init` writes the pack's own hint into contract.commands,
// so the override path is the ordinary path, not the exotic one.
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
			gates = append(gates, Gate{ID: id, Cmd: argv, FailOnOutput: slot.FailOnOutput})
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
