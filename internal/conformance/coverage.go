package conformance

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Choaterboater/pika/internal/profiles"
)

// Which pack commands have actually EXECUTED against foreign code.
//
// Detecting a pack is not running it. Every V1 pack had a corpus row
// before this file existed, and TestEveryLanguagePackIsExercisedByRealCode
// was green — while cobra's Makefile overrode all four Go commands and
// requests died at the format rung, so go@1's and python@1's own hints,
// the commands `pika init` writes into every repository it scaffolds,
// had never once been spawned on code pika did not write.
//
// That gap was visible only because somebody wrote a careful paragraph
// about it, and paragraphs rot. Here it is as data: the packs declare
// the commands, the manifest records the command each rung spawned, and
// the difference between the two sets is the gap. A command that stops
// being exercised fails by name; so does a command listed here as
// unexercised that some row has quietly started running, because a
// coverage report that only moves in one direction is how the last one
// went stale.

// PackCommand is one command a profile pack declares for one ladder
// slot: either an explicit `cmd` the resolver always uses, or a
// discovery `hint` that fills the slot when a repository supplies none.
type PackCommand struct {
	// Pack is the pack reference, e.g. "go@1".
	Pack string

	// Slot is the ladder rung the command fills.
	Slot string

	// Cmd is the declared argv joined with single spaces, which is the
	// spelling `check --json` reports and the manifest records.
	Cmd string

	// Explicit distinguishes a pack `cmd` from a discovery `hint`. An
	// explicit command runs whenever the slot is not overridden; a hint
	// runs only when apply autofills it or a repository's own discovery
	// happens to produce the same string.
	Explicit bool

	// Autofill is the pack's promise that this hint is a whole,
	// self-contained command apply may write into a contract unattended.
	// A hint without it can never be spawned by the adopt → apply →
	// check flow at all, which is a different kind of uncovered from a
	// command no repository in the corpus happens to reach.
	Autofill bool
}

// String renders the command with the pack and slot that declare it.
func (p PackCommand) String() string {
	return fmt.Sprintf("%s %s: %s", p.Pack, p.Slot, p.Cmd)
}

// PackCommands returns every command the registered packs declare, in
// registry order and then ladder order.
//
// The packs are the source of truth, walked through profiles.Resolve
// rather than re-read from YAML: a pack added to the registry tomorrow
// appears here without anyone remembering to list it, which is the whole
// difference between a coverage report and a second list to keep in
// sync. Slots that declare neither a command nor a hint — core's five
// sentinels, swift@1's lint, every pack's smoke — declare nothing to
// run and are absent rather than listed as uncovered.
func PackCommands() ([]PackCommand, error) {
	var out []PackCommand
	for _, ref := range profiles.SupportedRefs() {
		selection := []string{profiles.CoreRef}
		if ref != profiles.CoreRef {
			selection = append(selection, ref)
		}
		resolved, err := profiles.Resolve(selection)
		if err != nil {
			return nil, fmt.Errorf("conformance: resolve %s: %w", ref, err)
		}
		cs := resolved.Checks
		for _, slot := range []struct {
			id    string
			check profiles.Check
		}{
			{"format", cs.Format},
			{"lint", cs.Lint},
			{"typecheck", cs.Typecheck},
			{"test", cs.Test},
			{"smoke", cs.Smoke},
		} {
			switch {
			case len(slot.check.Cmd) > 0:
				out = append(out, PackCommand{
					Pack:     ref,
					Slot:     slot.id,
					Cmd:      strings.Join(slot.check.Cmd, " "),
					Explicit: true,
				})
			case len(slot.check.Hint) > 0:
				out = append(out, PackCommand{
					Pack:     ref,
					Slot:     slot.id,
					Cmd:      strings.Join(slot.check.Hint, " "),
					Autofill: slot.check.Autofill,
				})
			}
		}
	}
	return out, nil
}

// Coverage is one declared pack command and the corpus rows recorded to
// have spawned it.
type Coverage struct {
	PackCommand

	// Rows are the manifest rows whose ladder runs this command, in
	// manifest order. Empty means no repository pika did not write has
	// ever run it.
	Rows []string
}

// Exercised reports whether any row runs the command.
func (c Coverage) Exercised() bool { return len(c.Rows) > 0 }

// CoverageOf pairs every declared pack command with the rows that spawn
// it.
//
// A rung counts only when it produced a verdict from a process: pass or
// fail. A skip never spawned anything — not the discovery skip, and not
// the toolchain-not-installed skip, which resolves a command and records
// it precisely because exec never forked it.
func CoverageOf(corpus []Repo) ([]Coverage, error) {
	declared, err := PackCommands()
	if err != nil {
		return nil, err
	}
	out := make([]Coverage, 0, len(declared))
	for _, pc := range declared {
		cov := Coverage{PackCommand: pc}
		for _, r := range corpus {
			if !slices.Contains(r.Profiles, pc.Pack) {
				continue
			}
			for _, g := range r.Gates {
				if g.ID != pc.Slot || g.Cmd != pc.Cmd {
					continue
				}
				if g.Status == StatusPass || g.Status == StatusFail {
					cov.Rows = append(cov.Rows, r.Name)
				}
			}
		}
		out = append(out, cov)
	}
	return out, nil
}

// CoverageTable renders the coverage as fixed-width lines, one per
// declared command, so `go test -v` prints the answer to "which pack
// commands have run on foreign code" without anybody grepping for it.
func CoverageTable(cov []Coverage) string {
	var b strings.Builder
	slotWide, cmdWide := 0, 0
	for _, c := range cov {
		if n := len(c.Pack) + len(c.Slot) + 1; n > slotWide {
			slotWide = n
		}
		if n := len(c.Cmd); n > cmdWide {
			cmdWide = n
		}
	}
	ran, total := 0, len(cov)
	for _, c := range cov {
		// cmd, fill, hint: what pika would have to do to reach the
		// command at all. An explicit cmd always fills its slot; a fill
		// is a hint apply may adopt unattended; a bare hint is advice
		// doctor renders and check can only run if a repository asks
		// for the identical string on its own.
		kind := "hint"
		switch {
		case c.Explicit:
			kind = "cmd "
		case c.Autofill:
			kind = "fill"
		}
		who := "NOT RUN ON FOREIGN CODE"
		if c.Exercised() {
			ran++
			who = "ran in " + strings.Join(c.Rows, ", ")
		}
		fmt.Fprintf(&b, "  %-*s  %s  %-*s  %s\n", slotWide, c.Pack+" "+c.Slot, kind, cmdWide, c.Cmd, who)
	}
	fmt.Fprintf(&b, "  %d of %d declared pack commands have executed against a repository pika did not write", ran, total)
	return b.String()
}

// Gap is one declared pack command no corpus row runs, and the reason
// nobody has closed it.
type Gap struct {
	// Pack, Slot and Cmd identify the declared command exactly. All
	// three are compared, so editing a pack's hint turns its entry
	// stale instead of letting it keep vouching for a command that is
	// no longer declared.
	Pack string
	Slot string
	Cmd  string

	// Why is what stands between this command and a corpus row.
	Why string
}

// Unexercised is the honest remainder: every command a pack declares
// that no repository in the corpus has run, with the reason.
//
// It is a manifest, not a tolerance. An entry here is a claim that gets
// tested in both directions — a declared command missing from both the
// corpus and this list fails by name, and an entry for a command some
// row has started running fails too and has to be deleted. That is what
// keeps it from silently becoming the list of things nobody checks.
var Unexercised = []Gap{
	{
		Pack: "typescript@1", Slot: "format", Cmd: "npm run format",
		Why: "typescript@1 marks no hint autofillable — every one delegates to " +
			"a package.json script or a registry download a scaffold does not " +
			"provide — so `pika apply` never writes this into a contract. The " +
			"only route to it is a repository whose own package.json declares " +
			"a `format` script, because discovery spells that as the identical " +
			"string; got, the corpus's TypeScript row, declares none.",
	},
	{
		Pack: "typescript@1", Slot: "lint", Cmd: "npm run lint",
		Why: "Not autofillable, for the same reason as the format hint. Reachable " +
			"only through a repository that declares a `lint` script of its own; " +
			"got's package.json runs its linter from inside the test script.",
	},
	{
		Pack: "swift@1", Slot: "format", Cmd: "swift format lint --recursive Sources Tests",
		Why: "Not autofillable: swift-format ships inside the toolchain only from " +
			"Swift 6.0 on, while the pack targets swift-tools-version 5.10, so a " +
			"PATH probe for `swift` cannot tell a machine that can run this from " +
			"one that cannot. No Swift repository's own discovery produces it " +
			"either, so apple/swift-argument-parser's format rung skips.",
	},
}

// GapFor returns the recorded reason a command is unexercised.
func GapFor(pc PackCommand) (Gap, bool) {
	for _, g := range Unexercised {
		if g.Pack == pc.Pack && g.Slot == pc.Slot && g.Cmd == pc.Cmd {
			return g, true
		}
	}
	return Gap{}, false
}
