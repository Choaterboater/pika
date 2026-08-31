package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/checks"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/profiles"
)

// The upgrade path, end to end through the real binary.
//
// M3's claim is that an operator told to run `pika init --force` can run
// it without losing their work, and that a repository whose scaffold has
// gone stale can find that out and fix it. Both claims are about a whole
// repository over time, not about a function, so they are asserted here
// against the built binary in temp repositories rather than in the unit
// tests of the packages that implement them.

// operatorREADME is prose no template produces. It stands for everything
// an operator writes into a scaffolded file after the scaffold: the
// version compared is byte-for-byte, because "mostly preserved" is not a
// property anybody can rely on.
const operatorREADME = `# ledger-service

Written by a human, not by pika.

It records the two invariants the on-call rota actually depends on, and
nothing in the scaffold knows they exist. A regeneration that rewrites
this file destroys knowledge the kernel never had.
`

// operatorException is one complete exceptions record: all four
// mandatory fields (spec §5.3), each carrying a decision a human made
// and a reviewer accepted. It covers docs/API_Reference.md, which the
// core pack's kebab-case rule warns about, so the record is load-bearing
// — losing it is visible in `check`'s warnings, not just on disk.
const operatorException = `docs/API_Reference.md:
  rule-id: naming-kebab-case
  reason: the published documentation URL is load-bearing and renaming the file breaks every external link to it
  owner: platform-team
  review-condition: revisit once the docs site can serve permanent redirects
`

// exceptionPath is the excepted path, created so the rule really fires
// on it. An exception for a path that does not exist proves nothing.
const exceptionPath = "docs/API_Reference.md"

// staleWorkflow stands in for a CI workflow an older kernel scaffolded:
// it installs the verifier with `@latest`, which is exactly the defect
// M2 corrected in the template and M3 made detectable. Any repository
// still carrying it is judged by a kernel that can change with no commit
// in the repository.
const staleWorkflow = `name: check
on: [push]
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: go install "github.com/Choaterboater/pika/cmd/pika@latest"
      - run: pika check --ci
`

// preTemplateDigestLock is a real `.project/profiles.lock` written by a
// pika built before pack templates joined the pack digest — the core@1
// pin and the registry digest are this repository's own, taken verbatim
// from the commit preceding that change. core@1 rotated (its templates
// are now hashed); go@1 did not, because it shipped none, and that
// asymmetry is the whole point of the fixture: exactly the pack whose
// templates changed is the pack that fails.
//
// go@1's pin is therefore read from the live registry rather than
// frozen with the rest. Freezing it made the fixture correct exactly
// until go@1's own bytes moved for an unrelated reason — which they did,
// the day the pack gained agent guidance — and then the fixture blamed a
// second pack and the claim under test could no longer be stated at all.
func preTemplateDigestLock(t *testing.T) string {
	t.Helper()
	goDigest, ok := profiles.PackDigestFor(profiles.GoRef)
	if !ok {
		t.Fatalf("%s is not a registered pack", profiles.GoRef)
	}
	return `{
  "digest": "e892fb2a12938e299c7ce695af2c298879aa8ea100b83d0472db68cd0f8d0bc6",
  "packs": {
    "core": {
      "version": "1",
      "source": "embedded",
      "digest": "e8240007f5f61ea872ad727a695441bdef3a343b7046ab5aa08528c6b6ff2fdf"
    },
    "go": {
      "version": "1",
      "source": "embedded",
      "digest": "` + goDigest + `"
    }
  }
}
`
}

// repoFile resolves a repository-relative slash path under dir.
func repoFile(dir, rel string) string {
	return filepath.Join(dir, filepath.FromSlash(rel))
}

// readRepoFile reads a repository-relative file as a string.
func readRepoFile(t *testing.T, dir, rel string) string {
	t.Helper()
	bs, err := os.ReadFile(repoFile(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(bs)
}

// gateReport is the slice of the check report this file asserts on: the
// gate's own output, which carries gate 1's explanation. It is declared
// separately from checkReport because the message is the assertion here
// — a stale lock that failed for an unrelated reason would pass a test
// that only looked at the status.
type gateReport struct {
	Gates []struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		OutputTail string `json:"outputTail"`
	} `json:"gates"`
	Warnings []string `json:"warnings"`
	Pass     bool     `json:"pass"`
}

// parseGateReport unwraps a `check --json` envelope into gateReport.
func parseGateReport(t *testing.T, out string) gateReport {
	t.Helper()
	env := unwrap(t, out, "check")
	var rep gateReport
	if err := json.Unmarshal(env.Result, &rep); err != nil {
		t.Fatalf("parse check result: %v\noutput: %s", err, out)
	}
	return rep
}

// gate returns the named gate's result.
func (r gateReport) gate(t *testing.T, id string) (status, outputTail string) {
	t.Helper()
	for _, g := range r.Gates {
		if g.ID == id {
			return g.Status, g.OutputTail
		}
	}
	t.Fatalf("no gate %q in report %+v", id, r.Gates)
	return "", ""
}

// kebabWarnings returns the report's kebab-case warnings. The recorded
// exception's whole job is to make this empty.
func (r gateReport) kebabWarnings() []string {
	var out []string
	for _, w := range r.Warnings {
		if strings.Contains(w, "naming-kebab-case") {
			out = append(out, w)
		}
	}
	return out
}

// wantExceptionIntact asserts the recorded exception is still on disk,
// complete, and readable by the loader gate 1 itself uses. All four
// mandatory fields are compared: a record that survived with an empty
// owner is a record that fails gate 1, which is indistinguishable from
// having been deleted.
func wantExceptionIntact(t *testing.T, dir, after string) {
	t.Helper()
	got, err := checks.LoadExceptions(dir)
	if err != nil {
		t.Fatalf("%s: exceptions record does not load after %s: %v", checks.ExceptionsFile, after, err)
	}
	list, ok := got[exceptionPath]
	if !ok || len(list) != 1 {
		t.Fatalf("%s after %s: the record for %s is gone or duplicated (records: %v)", checks.ExceptionsFile, after, exceptionPath, got)
	}
	ex := list[0]
	for _, field := range []struct{ name, got, want string }{
		{"rule-id", ex.RuleID, "naming-kebab-case"},
		{"reason", ex.Reason, "the published documentation URL is load-bearing and renaming the file breaks every external link to it"},
		{"owner", ex.Owner, "platform-team"},
		{"review-condition", ex.ReviewCondition, "revisit once the docs site can serve permanent redirects"},
	} {
		if field.got != field.want {
			t.Errorf("after %s, exception %s = %q, want %q", after, field.name, field.got, field.want)
		}
	}
}

// TestE2EForceKeepsOperatorWorkAndResetDocsIsTheOptIn is the upgrade
// path's central claim, asserted on a repository that has been lived in:
// an operator's own README, an accepted naming exception, and a CI
// workflow an older kernel wrote.
//
// A bare `pika init --force` must refresh what the kernel owns, resolve
// profiles, name and module by reading the repository back rather than
// from the (empty) command line, and leave everything else exactly as it
// found it. `--reset-docs` must then really restore the scaffold's text,
// because an escape hatch nobody can prove works is not an escape hatch
// — and must still not touch the exceptions record, whose entries are
// evidence rather than boilerplate.
func TestE2EForceKeepsOperatorWorkAndResetDocsIsTheOptIn(t *testing.T) {
	if reason := toolchainAbsent("go"); reason != "" {
		t.Skipf("toolchain absent: %s", reason) // the go gates really spawn
	}
	dir := scaffoldRepo(t, "go")

	scaffoldedREADME := readRepoFile(t, dir, "README.md")
	scaffoldedWorkflow := readRepoFile(t, dir, ".github/workflows/ci.yml")
	scaffoldedGoMod := readRepoFile(t, dir, "go.mod")
	if scaffoldedREADME == operatorREADME {
		t.Fatal("fixture is not a fixture: the scaffolded README already equals the operator's")
	}

	// The repository as an operator leaves it: their own README, a path
	// the kebab rule warns about, the accepted exception that covers it,
	// and an older kernel's workflow.
	writeFileAt(t, repoFile(dir, "README.md"), operatorREADME)
	writeFileAt(t, repoFile(dir, exceptionPath), "# API reference\n")
	writeFileAt(t, repoFile(dir, checks.ExceptionsFile), operatorException)
	writeFileAt(t, repoFile(dir, ".github/workflows/ci.yml"), staleWorkflow)

	// Baseline: green, and the exception is doing work — the excepted
	// path raises no warning.
	before := parseGateReport(t, runCLI(t, dir, 0, "check", "--all", "--json"))
	if !before.Pass {
		t.Fatalf("fixture does not start green: %+v", before)
	}
	if w := before.kebabWarnings(); len(w) != 0 {
		t.Fatalf("the recorded exception does not cover %s: %v", exceptionPath, w)
	}

	// The remedy every upgrade note points at, run the way an operator
	// under pressure runs it: no flags at all.
	runCLI(t, dir, 0, "init", "--force")

	// 1. The operator's prose is untouched, byte for byte.
	wantFileContent(t, repoFile(dir, "README.md"), operatorREADME)

	// 2. The exception survives with all four fields.
	wantExceptionIntact(t, dir, "pika init --force")

	// 3. What the kernel owns was refreshed: the older workflow is gone
	//    and the current template is back.
	wantFileContent(t, repoFile(dir, ".github/workflows/ci.yml"), scaffoldedWorkflow)
	if strings.Contains(readRepoFile(t, dir, ".github/workflows/ci.yml"), "pika@latest") {
		t.Error("the refreshed workflow still installs the kernel with @latest")
	}

	// 4. Profiles, name and module came from the repository, not from
	//    the bare command line. A --force that took them from the flags
	//    would have written a core-only contract with no gates and
	//    renamed the module after the temp directory.
	c, err := contract.Load(repoFile(dir, ".project/contract.yaml"))
	if err != nil {
		t.Fatalf("contract after --force: %v", err)
	}
	if refs := checks.ProfileRefs(c); !strings.Contains(strings.Join(refs, " "), "go@1") {
		t.Errorf("--force lost the language profile: contract selects %v", refs)
	}
	// go@1's format slot is a discovery sentinel the scaffold autofills,
	// so an empty commands block is the signature of a --force that
	// resolved core only.
	if c.Commands["format"] == "" {
		t.Errorf("--force emptied the contract's commands: %v", c.Commands)
	}
	wantFileContent(t, repoFile(dir, "go.mod"), scaffoldedGoMod)

	// 5. Gate 1 is green again, the language layer's own gates still run,
	//    and the exception still covers the path.
	after := parseGateReport(t, runCLI(t, dir, 0, "check", "--all", "--json"))
	if !after.Pass {
		t.Fatalf("check --all is not green after --force: %+v", after)
	}
	if status, output := after.gate(t, "test"); status != "pass" {
		t.Errorf("the test gate is %q after --force, so the go layer did not survive:\n%s", status, output)
	}
	if w := after.kebabWarnings(); len(w) != 0 {
		t.Errorf("--force dropped the exception that covered %s: %v", exceptionPath, w)
	}

	// go.mod is the operator's, so --module has nothing to rewrite under
	// a bare --force. Asserted rather than assumed, because a flag that
	// silently does nothing is a flag an operator will eventually
	// believe did something.
	runCLI(t, dir, 0, "init", "--force", "--module", "example.com/renamed")
	wantFileContent(t, repoFile(dir, "go.mod"), scaffoldedGoMod)

	// --reset-docs is the destructive opt-in, and it really is
	// destructive: the scaffold's own text comes back over the
	// operator's.
	runCLI(t, dir, 0, "init", "--force", "--reset-docs")
	wantFileContent(t, repoFile(dir, "README.md"), scaffoldedREADME)

	// Not even --reset-docs reaches the exceptions record: regenerating
	// documentation is not a reason to discard a reviewed decision.
	wantExceptionIntact(t, dir, "pika init --force --reset-docs")

	// --reset-docs alone is a mistyped intention, not a no-op.
	runCLI(t, dir, 2, "init", "--reset-docs")
}

// TestE2EStaleScaffoldIsDetectableAndForceIsTheRemedy walks the second
// claim on an already-adopted repository: a lock written before the pack
// templates joined the digest fails gate 1 with a message naming the
// pack, and `pika init --force` is what fixes it — refreshing both the
// lock and the kernel-owned workflow the rotation was about, without
// touching the operator's own files.
//
// `pika apply` is asserted here too, in the negative: on a repository
// that already has a committed contract it refuses, so it is not the
// remedy in this state. The two commands do not compose into one story
// for an adopted repository, and pinning the refusal is how that stays
// true rather than becoming a surprise.
func TestE2EStaleScaffoldIsDetectableAndForceIsTheRemedy(t *testing.T) {
	if reason := toolchainAbsent("go"); reason != "" {
		t.Skipf("toolchain absent: %s", reason) // the go gates really spawn
	}
	dir := scaffoldRepo(t, "go")
	currentWorkflow := readRepoFile(t, dir, ".github/workflows/ci.yml")

	// A repository scaffolded by the older kernel: its lock and its
	// workflow are both that kernel's.
	writeFileAt(t, repoFile(dir, "README.md"), operatorREADME)
	writeFileAt(t, repoFile(dir, ".project/profiles.lock"), preTemplateDigestLock(t))
	writeFileAt(t, repoFile(dir, ".github/workflows/ci.yml"), staleWorkflow)

	// Detected: gate 1 fails and says which pack, which is the only way
	// an operator can tell a template correction from a hand edit.
	rep := parseGateReport(t, runCLI(t, dir, 1, "check", "--all", "--json"))
	if rep.Pass {
		t.Fatalf("a stale lock passed check: %+v", rep)
	}
	status, output := rep.gate(t, "contract")
	if status != "fail" {
		t.Fatalf("gate contract status = %q, want fail:\n%s", status, output)
	}
	for _, want := range []string{"profiles.lock", "pack core", "core@1", "pika init --force"} {
		if !strings.Contains(output, want) {
			t.Errorf("gate 1 output does not mention %q:\n%s", want, output)
		}
	}
	// go@1 ships no templates, so its pin did not rotate. Naming it here
	// too would send the operator looking for a change that never
	// happened.
	if strings.Contains(output, "pack go ") {
		t.Errorf("gate 1 blamed go@1, whose digest did not rotate:\n%s", output)
	}

	// Not the remedy: apply refuses outright once a contract exists, so
	// its kernel-file refresh is unreachable from this state.
	refused := unwrap(t, runCLI(t, dir, 1, "apply", "--json"), "apply")
	if refused.OK {
		t.Error("apply on an adopted repository reported ok = true")
	}
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(refused.Result, &failure); err != nil {
		t.Fatalf("refused apply result: %v\n%s", err, refused.Result)
	}
	if !strings.Contains(failure.Error, "already adopted") {
		t.Errorf("apply refusal = %q, want the already-adopted reason", failure.Error)
	}
	wantFileContent(t, repoFile(dir, ".github/workflows/ci.yml"), staleWorkflow)

	// The remedy, and it fixes both halves of the staleness at once: the
	// pin the digest rotation invalidated, and the template file the
	// rotation was actually about.
	runCLI(t, dir, 0, "init", "--force")
	wantFileContent(t, repoFile(dir, ".github/workflows/ci.yml"), currentWorkflow)
	wantFileContent(t, repoFile(dir, "README.md"), operatorREADME)

	green := parseGateReport(t, runCLI(t, dir, 0, "check", "--all", "--json"))
	if !green.Pass {
		t.Fatalf("check --all is not green after the remedy: %+v", green)
	}
	if status, output := green.gate(t, "contract"); status != "pass" {
		t.Fatalf("gate contract = %q after --force:\n%s", status, output)
	}
}

// staleAdoptionFixture builds the one state in which `pika apply`'s
// kernel-file refresh is reachable: an unadopted repository that already
// carries an older kernel's CI workflow, plus a README of the operator's
// own. It is the repository somebody scaffolded, abandoned, and is now
// adopting properly.
func staleAdoptionFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	copyFixtureTree(t, "go-mod", dir)
	writeFileAt(t, repoFile(dir, ".github/workflows/ci.yml"), staleWorkflow)
	writeFileAt(t, repoFile(dir, "README.md"), operatorREADME)
	return dir
}

// TestE2EApplyRefreshesAStaleKernelFileAndReportsIt pins the other half
// of the ownership split, through adoption rather than regeneration:
// apply rewrites the two kernel-owned files when they are stale, reports
// each rewrite as a `write` rather than doing it silently, and still
// never touches an operator-owned file that already exists.
func TestE2EApplyRefreshesAStaleKernelFileAndReportsIt(t *testing.T) {
	dir := staleAdoptionFixture(t)
	runCLI(t, dir, 0, "adopt")

	out := runCLI(t, dir, 0, "apply", "--json")
	env := unwrap(t, out, "apply")
	if !env.OK {
		t.Fatalf("apply reported ok = false:\n%s", out)
	}
	var rep applyReport
	if err := json.Unmarshal(env.Result, &rep); err != nil {
		t.Fatalf("apply --json result is not the apply report: %v\n%s", err, out)
	}
	if rep.Rollback {
		t.Fatal("apply reported a rollback on the happy path")
	}

	var refreshed bool
	for _, a := range rep.Applied {
		if a.Path == ".github/workflows/ci.yml" {
			if a.Op != "write" {
				t.Errorf("stale workflow applied as %q, want write", a.Op)
			}
			refreshed = true
		}
		if a.Path == "README.md" {
			t.Errorf("apply %s the operator's README; it is create-if-missing", a.Op)
		}
	}
	if !refreshed {
		t.Fatalf("apply did not refresh the stale workflow: %v", rep.Applied)
	}

	var keptREADME bool
	for _, s := range rep.Skipped {
		if s.Path == "README.md" {
			keptREADME = true
			if !strings.Contains(s.Reason, "kept the existing file") {
				t.Errorf("README skip reason = %q", s.Reason)
			}
		}
	}
	if !keptREADME {
		t.Errorf("apply did not report keeping the operator's README: %v", rep.Skipped)
	}

	// On disk: the current template, and the operator's prose intact.
	workflow := readRepoFile(t, dir, ".github/workflows/ci.yml")
	if strings.Contains(workflow, "pika@latest") {
		t.Errorf("the workflow still installs the kernel with @latest:\n%s", workflow)
	}
	if !strings.Contains(workflow, "PIKA_REF") {
		t.Errorf("the refreshed workflow does not pin PIKA_REF:\n%s", workflow)
	}
	wantFileContent(t, repoFile(dir, "README.md"), operatorREADME)

	// A second apply on the same tree would refuse (it is adopted now),
	// so the human summary is asserted on its own fixture. An operator
	// who cannot see the rewrite in the output cannot tell a kernel
	// refresh from an edit they made themselves.
	human := runCLI(t, staleAdoptionFixtureAdopted(t), 0, "apply")
	for _, want := range []string{
		"write .github/workflows/ci.yml",
		"README.md: already exists; kept the existing file",
	} {
		if !strings.Contains(human, want) {
			t.Errorf("human output missing %q\n---\n%s", want, human)
		}
	}
}

// staleAdoptionFixtureAdopted is staleAdoptionFixture with `pika adopt`
// already run, ready for one apply.
func staleAdoptionFixtureAdopted(t *testing.T) string {
	t.Helper()
	dir := staleAdoptionFixture(t)
	runCLI(t, dir, 0, "adopt")
	return dir
}
