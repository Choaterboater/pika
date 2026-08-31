// Package conformance runs pika against real foreign repositories.
//
// pika governed only itself for four milestones, and its own gates found
// nothing. Ten minutes after it was pointed at three repositories it did
// not write, two defects appeared that self-governance could not have
// surfaced: a format gate that reported `FAIL format exit=0` for a
// command that had just succeeded, and an adoption that recorded
// kebab-case deviations but dropped catch-all ones, so any repository
// containing a file called `utils` adopted "successfully" and then
// failed gate 1 — which skips every later gate.
//
// Neither was findable by a golden fixture, because a fixture encodes
// what its author already imagined. This corpus exists so that the next
// assumption pika makes about other people's code is caught by pika
// rather than by a human noticing.
//
// # Every expectation is data
//
// A repository is a row in Corpus: the URL, an exact commit SHA, the
// profiles adopt must detect, and the verdict each rung of the ladder
// must reach. Adding a repository is a manifest edit and nothing else.
// An UNEXPECTED PASS fails the run exactly as an unexpected failure
// does: a corpus that only notices regressions in one direction rots
// silently into a corpus that notices nothing.
//
// # Off unless asked, and honest about why it is off
//
// The corpus is gated on the PIKA_CONFORMANCE environment variable
// rather than a build tag. A build tag would take these files out of
// `go build ./...`, out of `go vet`, and out of the ordinary test
// compile — so the corpus would rot uncompiled while continuing to look
// maintained, which is the failure mode it exists to prevent. An
// environment variable keeps every line compiled and vetted on every
// ordinary run and moves only the network work behind the switch.
package conformance

import (
	"fmt"
	"os/exec"
	"strings"
)

// EnabledEnv is the switch. Anything other than "1" and the corpus
// skips, naming itself in the skip so a reader of the report knows the
// coverage was not taken.
const EnabledEnv = "PIKA_CONFORMANCE"

// CacheEnv overrides where fetched repositories are cached. The default
// is under the system temp directory: the corpus never writes to $HOME
// and never writes into the pika checkout.
const CacheEnv = "PIKA_CONFORMANCE_CACHE"

// Statuses a gate result may carry, mirroring internal/verify.
const (
	StatusPass = "pass"
	StatusFail = "fail"
	StatusSkip = "skip"
)

// GateWant is the verdict the manifest expects from one rung.
type GateWant struct {
	// ID is the rung, in ladder order.
	ID string

	// Status is pass, fail or skip.
	Status string

	// Exit is the process status a failing rung must report. It is
	// required for a fail and must be non-zero — the manifest cannot
	// express `FAIL ... exit=0`, because that verdict is a
	// contradiction and the corpus must not be able to bless one.
	// Negative values are the sentinels internal/verify records when no
	// process status produced the verdict (-1 never finished, -2 judged
	// on its output).
	Exit int

	// Reason is a substring the skip reason must contain. It is
	// required for a skip: "this gate did not run" is only a report
	// when it says why, and the four reasons pika can give — no command
	// discovered, toolchain not installed, an earlier gate failed, no
	// changed files in scope — mean four different things.
	Reason string
}

// Repo is one foreign repository in the corpus, pinned to an exact
// commit and carrying the whole outcome pika must produce on it.
type Repo struct {
	// Name is the subtest name and the label in every report line.
	Name string

	// URL is the clone source.
	URL string

	// SHA is an exact commit. Never a branch and never a tag: both
	// move, and a corpus whose inputs move cannot tell a pika
	// regression from an upstream commit.
	SHA string

	// Ref records which release SHA names, for a human reading the
	// manifest. It is documentation; nothing resolves it.
	Ref string

	// Why is what this repository buys that the others do not.
	Why string

	// Drift names what, other than a change to pika, can move this
	// row's expected outcome. A red corpus is only actionable if the
	// maintainer knows which expectations are hostage to somebody
	// else's release cadence.
	Drift string

	// Profiles is the exact detectedProfiles list adopt must report.
	Profiles []string

	// Needs are the argv[0]s that must be on PATH for the recorded
	// outcome to be reachable. A machine missing one cannot exercise
	// this row, and the row skips by name rather than failing: a
	// toolchain this developer does not have installed is not a defect
	// in pika.
	Needs []string

	// Absent are the argv[0]s whose ABSENCE the recorded outcome
	// depends on. cobra's lint rung is expected to fail because
	// `make lint` invokes a golangci-lint that is not installed; on a
	// machine that has one, the recorded verdict is simply not the
	// verdict under test, and the row skips rather than lying.
	Absent []string

	// Gates is the ladder, in order, exactly as `check --json` must
	// report it after apply.
	Gates []GateWant
}

// Corpus is the manifest.
//
// Every row was verified by running the documented flow against the
// pinned commit. The three that found the original defects are here
// because they found them; the Rust and Swift rows are here because
// those two packs had never met code pika did not scaffold.
var Corpus = []Repo{
	{
		Name: "spf13-cobra",
		URL:  "https://github.com/spf13/cobra",
		SHA:  "40b5bc1437a564fc795d388b23835e84f54cd1d1",
		Ref:  "v1.9.1",
		Why: "A Go repository that drives every check through a Makefile. " +
			"adopt discovers `make fmt` and writes it into the format slot; " +
			"`make fmt` narrates its work and exits 0, which pika once read " +
			"as a failure because the go@1 pack's fail-on-output flag rode " +
			"onto whatever command filled that slot. The format rung passing " +
			"here is the whole assertion.",
		Drift: "cobra's Makefile, and GNU make's exit status for a failed recipe (2).",
		// go is needed because `make fmt` shells out to gofmt.
		Needs:    []string{"git", "make", "go"},
		Absent:   []string{"golangci-lint"},
		Profiles: []string{"core@1", "go@1"},
		Gates: []GateWant{
			{ID: "contract", Status: StatusPass},
			{ID: "format", Status: StatusPass},
			{ID: "lint", Status: StatusFail, Exit: 2},
			{ID: "typecheck", Status: StatusSkip, Reason: "skipped: gate lint failed"},
			{ID: "test", Status: StatusSkip, Reason: "skipped: gate lint failed"},
			{ID: "smoke", Status: StatusSkip, Reason: "skipped: gate lint failed"},
		},
	},
	{
		Name: "psf-requests",
		URL:  "https://github.com/psf/requests",
		SHA:  "b25c87d7cb8d6a18a37fa12442b5f883f9e41741",
		Ref:  "v2.32.5",
		Why: "A Python repository with two files named `utils`. Adoption " +
			"recorded its kebab-case deviations and dropped the " +
			"catch-all ones, so adopt exited 0 and wrote a contract that " +
			"failed its own gate 1 — and a failed gate 1 skips the entire " +
			"rest of the ladder. This row is why the corpus asserts gate 1 " +
			"green after apply rather than trusting adopt's exit code.",
		Drift: "ruff's formatter style: the format rung fails because requests is " +
			"black-formatted and the python@1 pack checks with ruff.",
		Needs:    []string{"git", "ruff"},
		Profiles: []string{"core@1", "python@1"},
		Gates: []GateWant{
			{ID: "contract", Status: StatusPass},
			{ID: "format", Status: StatusFail, Exit: 1},
			{ID: "lint", Status: StatusSkip, Reason: "skipped: gate format failed"},
			{ID: "typecheck", Status: StatusSkip, Reason: "skipped: gate format failed"},
			{ID: "test", Status: StatusSkip, Reason: "skipped: gate format failed"},
			{ID: "smoke", Status: StatusSkip, Reason: "skipped: gate format failed"},
		},
	},
	{
		Name: "sindresorhus-got",
		URL:  "https://github.com/sindresorhus/got",
		SHA:  "a359bd385129d2adbc765b52dfbbadac5f54a825",
		Ref:  "v14.4.7",
		Why: "A TypeScript repository with a whole `source/core/utils/` " +
			"directory, so it carries the same catch-all defect as requests " +
			"at a different scale. It is also the only row where three rungs " +
			"legitimately find no command at all, which is what proves a " +
			"named skip is distinguishable from a silent one.",
		Drift: "got's package.json test script, and npm's propagation of a " +
			"shell's 127 for a binary a shallow clone never installed.",
		Needs: []string{"git", "npm"},
		// The test rung is expected to fail at `xo: command not found`.
		// A machine with a global xo would get a different, slower and
		// entirely unrelated verdict.
		Absent:   []string{"xo"},
		Profiles: []string{"core@1", "typescript@1"},
		Gates: []GateWant{
			{ID: "contract", Status: StatusPass},
			{ID: "format", Status: StatusSkip, Reason: "no command discovered for format"},
			{ID: "lint", Status: StatusSkip, Reason: "no command discovered for lint"},
			{ID: "typecheck", Status: StatusSkip, Reason: "no command discovered for typecheck"},
			{ID: "test", Status: StatusFail, Exit: 127},
			{ID: "smoke", Status: StatusSkip, Reason: "skipped: gate test failed"},
		},
	},
	{
		Name: "dtolnay-anyhow",
		URL:  "https://github.com/dtolnay/anyhow",
		SHA:  "f2b963a759decf0828efb58a8fdd417fb12f71fb",
		Ref:  "1.0.99",
		Why: "The first real Rust code the rust@1 pack has ever met, and the " +
			"only row that goes green end to end: adopt records a catch-all " +
			"`tests/common/mod.rs`, gate 1 passes anyway, and the pack's own " +
			"cargo commands then run for real. A corpus where every row is " +
			"red proves only that pika can be red.",
		Drift: "clippy. `cargo clippy -- -D warnings` is judged by whichever " +
			"clippy the machine has, and a new lint in a future release turns " +
			"this row red without pika changing at all. Check the lint rung's " +
			"output before believing a regression.",
		Needs:    []string{"git", "cargo", "cargo-fmt", "cargo-clippy"},
		Profiles: []string{"core@1", "rust@1"},
		Gates: []GateWant{
			{ID: "contract", Status: StatusPass},
			{ID: "format", Status: StatusPass},
			{ID: "lint", Status: StatusPass},
			{ID: "typecheck", Status: StatusPass},
			{ID: "test", Status: StatusPass},
			{ID: "smoke", Status: StatusSkip, Reason: "no command discovered for smoke"},
		},
	},
	{
		Name: "apple-swift-argument-parser",
		URL:  "https://github.com/apple/swift-argument-parser",
		SHA:  "6a52f3251125d74daf04fcbd5e6f08a75d074382",
		Ref:  "1.8.2",
		Why: "The first real Swift code the swift@1 pack has ever met. Its " +
			"tree is almost entirely UpperCamelCase, so it is also the " +
			"largest naming-exception record in the corpus — several hundred " +
			"paths — and gate 1 has to pass carrying all of them.",
		Drift: "the Swift toolchain: `swift build` and `swift test` are judged by " +
			"whichever Xcode or swiftly the machine has.",
		Needs:    []string{"git", "swift"},
		Profiles: []string{"core@1", "swift@1"},
		Gates: []GateWant{
			{ID: "contract", Status: StatusPass},
			{ID: "format", Status: StatusSkip, Reason: "no command discovered for format"},
			{ID: "lint", Status: StatusSkip, Reason: "no command discovered for lint"},
			{ID: "typecheck", Status: StatusPass},
			{ID: "test", Status: StatusPass},
			{ID: "smoke", Status: StatusSkip, Reason: "no command discovered for smoke"},
		},
	},
}

// Missing reports why this machine cannot exercise r, or "" when it can.
//
// The check runs before anything is cloned, and its verdict is about the
// machine, never about pika. Both halves matter: a row whose toolchain
// is absent cannot reach its recorded outcome, and a row whose recorded
// outcome depends on a tool's absence cannot be trusted on a machine
// that has it.
func (r Repo) Missing() string {
	var why []string
	for _, tool := range r.Needs {
		if _, err := exec.LookPath(tool); err != nil {
			why = append(why, fmt.Sprintf("%s is not on PATH", tool))
		}
	}
	for _, tool := range r.Absent {
		if path, err := exec.LookPath(tool); err == nil {
			why = append(why, fmt.Sprintf("%s IS on PATH (%s); this row's recorded outcome depends on its absence", tool, path))
		}
	}
	return strings.Join(why, "; ")
}

// Pass reports whether the manifest expects the whole ladder to pass,
// which is what `pika check` derives its exit code from.
func (r Repo) Pass() bool {
	for _, g := range r.Gates {
		if g.Status == StatusFail {
			return false
		}
	}
	return true
}

// Grade compares the ladder pika reported against what the manifest
// expects and returns one line per disagreement.
//
// A pass where the manifest recorded a failure is a disagreement, and
// says so in the same words as the reverse. cobra's lint rung is
// expected to fail on an absent golangci-lint; the day it starts passing
// something changed — the Makefile, the runner image, or pika's reading
// of an exit status — and a corpus that shrugged at good news would have
// nothing left to say about bad news either.
func (r Repo) Grade(gates []Gate) []string {
	var found []string
	got := make(map[string]Gate, len(gates))
	order := make([]string, 0, len(gates))
	for _, g := range gates {
		got[g.ID] = g
		order = append(order, g.ID)
	}
	want := make([]string, 0, len(r.Gates))
	for _, w := range r.Gates {
		want = append(want, w.ID)
	}
	if strings.Join(order, " ") != strings.Join(want, " ") {
		found = append(found, fmt.Sprintf("the ladder ran [%s]; the manifest expects [%s]",
			strings.Join(order, " "), strings.Join(want, " ")))
	}
	for _, w := range r.Gates {
		g, ok := got[w.ID]
		if !ok {
			found = append(found, fmt.Sprintf("gate %s: the manifest expects %s, and the rung did not run at all", w.ID, w.Status))
			continue
		}
		if g.Status != w.Status {
			found = append(found, fmt.Sprintf("gate %s: wanted %s, got %s\n    %s", w.ID, w.Status, g.Status, g.Evidence()))
			continue
		}
		switch w.Status {
		case StatusFail:
			if g.Exit != w.Exit {
				found = append(found, fmt.Sprintf("gate %s: failed with exit %d, and the manifest records exit %d\n    %s", w.ID, g.Exit, w.Exit, g.Evidence()))
			}
		case StatusSkip:
			if !strings.Contains(g.Reason, w.Reason) {
				found = append(found, fmt.Sprintf("gate %s: skipped for %q, and the manifest records a reason containing %q", w.ID, g.Reason, w.Reason))
			}
		}
	}
	return found
}

// Coherent returns one line per gate result that contradicts itself,
// whatever the manifest expects of it.
//
// These are the claims no repository is allowed to break, because they
// are about the report rather than about the code being reported on. The
// first of them — a failed gate carrying exit 0 — is the line that
// started this corpus: `FAIL format exit=0` told an operator the command
// succeeded and the gate failed in the same breath.
func Coherent(gates []Gate) []string {
	var found []string
	for _, g := range gates {
		switch {
		case g.ID == "":
			found = append(found, "a gate reported no id, so nothing it said can be attributed")
		case g.Status == StatusFail && g.Exit == 0:
			found = append(found, fmt.Sprintf("gate %s failed and reported exit 0; a failed gate never carries the exit status of a command that succeeded\n    %s", g.ID, g.Evidence()))
		case g.Status == StatusFail && g.Reason == "":
			found = append(found, fmt.Sprintf("gate %s failed and gave no reason\n    %s", g.ID, g.Evidence()))
		case g.Status == StatusSkip && strings.TrimSpace(g.Reason) == "":
			found = append(found, fmt.Sprintf("gate %s skipped without naming a reason; an unnamed skip is indistinguishable from a pass\n    %s", g.ID, g.Evidence()))
		case g.Status == StatusPass && g.Exit != 0:
			found = append(found, fmt.Sprintf("gate %s passed and reported exit %d\n    %s", g.ID, g.Exit, g.Evidence()))
		case g.Status != StatusPass && g.Status != StatusFail && g.Status != StatusSkip:
			found = append(found, fmt.Sprintf("gate %s reported status %q, which is none of pass, fail, skip", g.ID, g.Status))
		}
	}
	return found
}

// Unrecorded returns the naming deviations adopt reported as conflicts
// but did not record as exceptions.
//
// This is the c73f368 defect stated as an invariant instead of as a list
// of paths: adopt walks the tree, finds names core@1 forbids, and must
// record every one of them in the contract it writes. A deviation
// reported to the human and withheld from the contract is an adoption
// that cannot pass its own gate 1, which is how psf/requests and
// sindresorhus/got were dead on arrival.
func Unrecorded(rep AdoptReport) []string {
	recorded := make(map[string]bool, len(rep.Exceptions))
	for _, e := range rep.Exceptions {
		recorded[e.RuleID+"\x00"+e.Path] = true
	}
	var found []string
	for _, c := range rep.Conflicts {
		if !recorded[c.RuleID+"\x00"+c.Path] {
			found = append(found, fmt.Sprintf("adopt reported %s on %s as a conflict and recorded no exception for it", c.RuleID, c.Path))
		}
	}
	return found
}
