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
// must reach along with the command that reached it. Adding a
// repository is a manifest edit and nothing else. An UNEXPECTED PASS
// fails the run exactly as an unexpected failure does: a corpus that
// only notices regressions in one direction rots silently into a corpus
// that notices nothing.
//
// # Detecting a pack is not running it
//
// Every V1 pack had a row before coverage.go existed, and the test that
// said so was green — while cobra's Makefile overrode all four Go
// commands and requests died at its first rung, so go@1's and
// python@1's own hints had never been spawned on foreign code at all.
// Each rung therefore records the command it ran, and CoverageOf
// subtracts what the rows ran from what the packs declare. The
// remainder is Unexercised, with a reason per entry, graded in both
// directions.
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

	// Cmd is the whole command line the rung must spawn, argv joined
	// with single spaces, exactly as `check --json` reports it.
	//
	// It is what turns a ladder of verdicts into a record of what ran.
	// cobra's Makefile overrides every Go command pika would otherwise
	// use, so its green format rung says nothing about `gofmt -l .`;
	// without this field the manifest could not tell the two apart, and
	// the fact that go@1's own hints had never executed on foreign code
	// was visible only in a paragraph somebody wrote by hand. The
	// coverage table in coverage.go is derived from these strings.
	//
	// Empty when the rung spawns nothing: the in-process contract gate,
	// a rung that found no command, a rung skipped behind an earlier
	// failure. A toolchain-not-installed skip is the one skip that does
	// record a command — verify resolved it and exec never forked it.
	Cmd string
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
//
// The two rows added last are here because DETECTING a pack is not
// RUNNING it. cobra overrides every Go command with its Makefile and
// requests stops dead at the format rung, so go@1's and python@1's own
// command hints — the ones `pika init` writes into every repository it
// scaffolds — had never executed against foreign code at all. Which
// commands have actually run is now derived from the Cmd each row
// records, and coverage.go states the answer as data.
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
			{ID: "format", Status: StatusPass, Cmd: "make fmt"},
			{ID: "lint", Status: StatusFail, Exit: 2, Cmd: "make lint"},
			{ID: "typecheck", Status: StatusSkip, Reason: "skipped: gate lint failed"},
			{ID: "test", Status: StatusSkip, Reason: "skipped: gate lint failed"},
			{ID: "smoke", Status: StatusSkip, Reason: "skipped: gate lint failed"},
		},
	},
	{
		Name: "golang-x-sync",
		URL:  "https://github.com/golang/sync",
		SHA:  "5071ed6a9f1617117556b66384f765c934de3698",
		Ref:  "v0.21.0",
		Why: "The Go row cobra cannot be. cobra drives every check through a " +
			"Makefile, so discovery fills all four Go slots and go@1's own " +
			"hints — the commands `pika init` writes into every Go " +
			"repository it scaffolds — had never once run against code pika " +
			"did not write. golang.org/x/sync carries no Makefile, Justfile, " +
			"Taskfile or package.json, so discovery finds nothing, apply " +
			"autofills the pack's hints, and `gofmt -l .`, `go vet ./...`, " +
			"`go build -o /dev/null ./...` and `go test ./...` each spawn " +
			"for real. Its go.mod requires nothing, so the ladder needs no " +
			"network once the tree is cached.",
		Drift: "gofmt's formatting rules and go vet's analyzer set, which both " +
			"travel with the Go release; and go.mod's `go 1.25.0` line, " +
			"which makes every rung fail outright on an older toolchain " +
			"instead of skipping — Needs can only ask whether `go` is on " +
			"PATH, not which one. Read `go version` before believing a " +
			"regression.",
		Needs:    []string{"git", "go"},
		Profiles: []string{"core@1", "go@1"},
		Gates: []GateWant{
			{ID: "contract", Status: StatusPass},
			{ID: "format", Status: StatusPass, Cmd: "gofmt -l ."},
			{ID: "lint", Status: StatusPass, Cmd: "go vet ./..."},
			{ID: "typecheck", Status: StatusPass, Cmd: "go build -o /dev/null ./..."},
			{ID: "test", Status: StatusPass, Cmd: "go test ./..."},
			{ID: "smoke", Status: StatusSkip, Reason: "no command discovered for smoke"},
		},
	},
	{
		Name: "dhoinka-whichport",
		URL:  "https://github.com/dhoinka/whichport",
		SHA:  "12ab8b03f273fa1d64c2b19918c0e4714e668217",
		Ref:  "main",
		Why: "The third Go automation file, and the only row where one " +
			"actually runs on the ladder it changes: cobra's Makefile stops " +
			"discovery dead at the format rung and skips the rest behind its " +
			"failure, x-sync carries no automation file so go@1's own hints " +
			"fill every slot untouched, and neither says anything about " +
			"whether pika reads a Taskfile at all. whichport's Taskfile " +
			"names `fmt` and `test` tasks that match the verbs discovery " +
			"looks for, so `task fmt` and `task test` fill the format and " +
			"test slots and both spawn and pass; its `vet` and `build` tasks " +
			"do not match a verb pika discovers (lint wants `lint`, not " +
			"`vet`; typecheck wants `typecheck` or `build`, and Task's own " +
			"`build` task builds a binary rather than type-checking, so " +
			"go@1's `go build -o /dev/null ./...` hint fills lint and " +
			"typecheck instead), which is the fixture that proves discovery " +
			"matches by verb name and does not simply adopt every task a " +
			"Taskfile names.",
		Drift: "gofmt's formatting rules and go vet's analyzer set, which " +
			"both travel with the Go release; go.mod's declared Go version, " +
			"which fails typecheck outright on an older toolchain instead " +
			"of skipping; and the `task` binary itself, which is a separate " +
			"install from `go` and not guaranteed present alongside it.",
		Needs:    []string{"git", "go", "task"},
		Profiles: []string{"core@1", "go@1"},
		Gates: []GateWant{
			{ID: "contract", Status: StatusPass},
			{ID: "format", Status: StatusPass, Cmd: "task fmt"},
			{ID: "lint", Status: StatusPass, Cmd: "go vet ./..."},
			{ID: "typecheck", Status: StatusPass, Cmd: "go build -o /dev/null ./..."},
			{ID: "test", Status: StatusPass, Cmd: "task test"},
			{ID: "smoke", Status: StatusSkip, Reason: "no command discovered for smoke"},
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
			{ID: "format", Status: StatusFail, Exit: 1, Cmd: "ruff format --check ."},
			{ID: "lint", Status: StatusSkip, Reason: "skipped: gate format failed"},
			{ID: "typecheck", Status: StatusSkip, Reason: "skipped: gate format failed"},
			{ID: "test", Status: StatusSkip, Reason: "skipped: gate format failed"},
			{ID: "smoke", Status: StatusSkip, Reason: "skipped: gate format failed"},
		},
	},
	{
		Name: "pytest-dev-iniconfig",
		URL:  "https://github.com/pytest-dev/iniconfig",
		SHA:  "7faed13ae50bad7c5da3f5782f254a8a7736bb84",
		Ref:  "v2.3.0",
		Why: "The Python row requests cannot be. requests stops at `ruff " +
			"format --check .` failing, and a failed rung skips the rest of " +
			"the ladder, so python@1's other three commands had never run " +
			"on foreign code. iniconfig is the only repository of the 42 " +
			"surveyed where all four execute and pass: pure-stdlib, " +
			"ruff-formatted, ruff-clean, clean under `mypy .` with the " +
			"repository's own `strict = true`, and a suite that needs " +
			"nothing installed but pytest.\n" +
			"One honest caveat, because a rung nobody understands is a rung " +
			"nobody can trust: iniconfig uses a src/ layout, so the bare " +
			"`pytest` python@1 declares collects the checkout's test files " +
			"and imports the iniconfig in site-packages — the copy pytest " +
			"itself depends on — rather than the tree under test. The rung " +
			"asserts that pika resolves and spawns python@1's test command " +
			"and reads its status. The three rungs before it read the " +
			"checkout's own source off disk and are unaffected.",
		Drift: "ruff's formatter style and lint rule set, and mypy's, both " +
			"judged by whichever version the machine has; and pytest's " +
			"dependency on iniconfig — the day pytest drops it, the test " +
			"rung turns red at collection with an ImportError, which is a " +
			"fact about the packaging ecosystem and not about pika.",
		Needs:    []string{"git", "ruff", "mypy", "pytest"},
		Profiles: []string{"core@1", "python@1"},
		Gates: []GateWant{
			{ID: "contract", Status: StatusPass},
			{ID: "format", Status: StatusPass, Cmd: "ruff format --check ."},
			{ID: "lint", Status: StatusPass, Cmd: "ruff check ."},
			{ID: "typecheck", Status: StatusPass, Cmd: "mypy ."},
			{ID: "test", Status: StatusPass, Cmd: "pytest"},
			{ID: "smoke", Status: StatusSkip, Reason: "no command discovered for smoke"},
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
			{ID: "test", Status: StatusFail, Exit: 127, Cmd: "npm run test"},
			{ID: "smoke", Status: StatusSkip, Reason: "skipped: gate test failed"},
		},
	},
	{
		Name: "microsoft-typescript-babel-starter",
		URL:  "https://github.com/Microsoft/TypeScript-Babel-Starter",
		SHA:  "dd37b20ba24b6ee3b844dd179e04d7ed4dea5891",
		Ref:  "master (archived; no release tags)",
		Why: "The only row where typescript@1's typecheck slot actually spawns. " +
			"Its package.json declares `type-check` and nothing named `lint` or " +
			"`format`, so both of those stay honest discovery skips instead of " +
			"failures that would short-circuit the ladder before typecheck's " +
			"turn — the same shape got's own row proved for test.",
		Drift: "npm's propagation of a shell's 127 for `tsc`, which a shallow " +
			"clone never installs.",
		Needs: []string{"git", "npm"},
		// The typecheck rung is expected to fail at `tsc: command not
		// found`. A machine with a global tsc would get a different,
		// slower and entirely unrelated verdict.
		Absent:   []string{"tsc"},
		Profiles: []string{"core@1", "typescript@1"},
		Gates: []GateWant{
			{ID: "contract", Status: StatusPass},
			{ID: "format", Status: StatusSkip, Reason: "no command discovered for format"},
			{ID: "lint", Status: StatusSkip, Reason: "no command discovered for lint"},
			{ID: "typecheck", Status: StatusFail, Exit: 127, Cmd: "npm run type-check"},
			{ID: "test", Status: StatusSkip, Reason: "skipped: gate typecheck failed"},
			{ID: "smoke", Status: StatusSkip, Reason: "skipped: gate typecheck failed"},
		},
	},
	{
		Name: "particle-iot-particle-api-js",
		URL:  "https://github.com/particle-iot/particle-api-js",
		SHA:  "a449b595be92624830cb98033a3b6e723d4d6522",
		Ref:  "v12.0.3",
		Why: "The only row where typescript@1's lint slot actually spawns. Its " +
			"package.json declares `lint` and nothing named `format`, so format " +
			"stays an honest discovery skip instead of a failure that would " +
			"short-circuit the ladder before lint's turn.",
		Drift: "npm's propagation of a shell's 127 for `eslint`, which a shallow " +
			"clone never installs.",
		Needs: []string{"git", "npm"},
		// The lint rung is expected to fail at `eslint: command not
		// found`. A machine with a global eslint would get a different,
		// slower and entirely unrelated verdict.
		Absent:   []string{"eslint"},
		Profiles: []string{"core@1", "typescript@1"},
		Gates: []GateWant{
			{ID: "contract", Status: StatusPass},
			{ID: "format", Status: StatusSkip, Reason: "no command discovered for format"},
			{ID: "lint", Status: StatusFail, Exit: 127, Cmd: "npm run lint"},
			{ID: "typecheck", Status: StatusSkip, Reason: "skipped: gate lint failed"},
			{ID: "test", Status: StatusSkip, Reason: "skipped: gate lint failed"},
			{ID: "smoke", Status: StatusSkip, Reason: "skipped: gate lint failed"},
		},
	},
	{
		Name: "simonbengtsson-jspdf-autotable",
		URL:  "https://github.com/simonbengtsson/jsPDF-AutoTable",
		SHA:  "76f71916d816ebebe5492c49e5e57622ef672159",
		Ref:  "v5.0.8",
		Why: "The only row where typescript@1's format slot actually spawns. " +
			"Its package.json declares `format`, and format runs first in the " +
			"ladder, so it fails before lint, typecheck or test get a turn — " +
			"proving format is reachable at the cost of proving nothing about " +
			"the three rungs behind it.",
		Drift: "npm's propagation of a shell's 127 for `prettier`, which a " +
			"shallow clone never installs.",
		Needs: []string{"git", "npm"},
		// The format rung is expected to fail at `prettier: command not
		// found`. A machine with a global prettier would get a different,
		// slower and entirely unrelated verdict.
		Absent:   []string{"prettier"},
		Profiles: []string{"core@1", "typescript@1"},
		Gates: []GateWant{
			{ID: "contract", Status: StatusPass},
			{ID: "format", Status: StatusFail, Exit: 127, Cmd: "npm run format"},
			{ID: "lint", Status: StatusSkip, Reason: "skipped: gate format failed"},
			{ID: "typecheck", Status: StatusSkip, Reason: "skipped: gate format failed"},
			{ID: "test", Status: StatusSkip, Reason: "skipped: gate format failed"},
			{ID: "smoke", Status: StatusSkip, Reason: "skipped: gate format failed"},
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
			{ID: "format", Status: StatusPass, Cmd: "cargo fmt -- --check"},
			{ID: "lint", Status: StatusPass, Cmd: "cargo clippy -- -D warnings"},
			{ID: "typecheck", Status: StatusPass, Cmd: "cargo build"},
			{ID: "test", Status: StatusPass, Cmd: "cargo test"},
			{ID: "smoke", Status: StatusSkip, Reason: "no command discovered for smoke"},
		},
	},
	{
		Name: "dioxuslabs-taffy",
		URL:  "https://github.com/DioxusLabs/taffy",
		SHA:  "b3b387132be1dda0e9d08d5044692236532c166d",
		Ref:  "main HEAD (v0.14.0 is an older tag, a different commit)",
		Why: "The first row to exercise Justfile discovery at all: every " +
			"other row's format/lint either autofills or finds nothing. " +
			"taffy's justfile declares a `fmt` recipe and nothing named " +
			"`lint`, so the format slot is discovered (`just fmt`) while lint " +
			"still autofills (`cargo clippy -- -D warnings`) in the same " +
			"repository — proving discovery overrides autofill only where it " +
			"actually finds something.",
		Drift: "clippy, for the same reason as dtolnay-anyhow. Also: `just fmt` " +
			"runs `cargo fmt --all`, which rewrites files rather than checking " +
			"them — taffy's own choice of command, not pika's; a discovered " +
			"command is run as declared, mutating or not.",
		Needs:    []string{"git", "cargo", "cargo-fmt", "cargo-clippy", "just"},
		Profiles: []string{"core@1", "rust@1"},
		Gates: []GateWant{
			{ID: "contract", Status: StatusPass},
			{ID: "format", Status: StatusPass, Cmd: "just fmt"},
			{ID: "lint", Status: StatusPass, Cmd: "cargo clippy -- -D warnings"},
			{ID: "typecheck", Status: StatusPass, Cmd: "cargo build"},
			{ID: "test", Status: StatusPass, Cmd: "cargo test"},
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
			{ID: "typecheck", Status: StatusPass, Cmd: "swift build"},
			{ID: "test", Status: StatusPass, Cmd: "swift test"},
			{ID: "smoke", Status: StatusSkip, Reason: "no command discovered for smoke"},
		},
	},
}
