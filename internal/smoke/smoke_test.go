package main

import (
	"encoding/json"
	"errors"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests cover the gate's own machinery, not the lifecycle: the
// lifecycle is what `go run ./internal/smoke` does, and running it twice
// per `pika check` would buy nothing.
//
// What they defend is the property the whole task turns on — that this
// program can fail, and says which step failed when it does. A smoke
// gate whose runner swallowed an error, or whose planted defect was not
// a defect, would pass while the product was broken, which is worse than
// having no gate at all.

// fixed returns a step that always ends the same way.
func fixed(id string, err error, ran *bool) step {
	return step{id: id, proves: "the runner is exercised", run: func(*harness) error {
		*ran = true
		return err
	}}
}

// A failing step stops the run, and the error names it. This is the
// contract every deliberate-failure transcript depends on: an operator
// reading CI has to be told which lifecycle step broke, not that
// something did.
func TestRunStepsNamesTheFailingStepAndStops(t *testing.T) {
	var first, second, third bool
	steps := []step{
		fixed("early", nil, &first),
		fixed("broken", errors.New("the commit was empty"), &second),
		fixed("later", nil, &third),
	}
	var out strings.Builder
	err := runSteps(steps, nil, &out)
	if err == nil {
		t.Fatalf("runSteps reported success with a failing step:\n%s", out.String())
	}
	for _, want := range []string{`step "broken"`, "the runner is exercised", "the commit was empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if !first || !second {
		t.Errorf("steps before and including the failure did not run (%v, %v)", first, second)
	}
	if third {
		t.Error("a step after the failure ran; the lifecycle must stop where it broke")
	}
	if !strings.Contains(out.String(), "FAIL broken") {
		t.Errorf("progress output does not mark the failing step:\n%s", out.String())
	}
}

// Every step passing is reported as such, one line each, so a green run
// says what it verified rather than how many things it did.
func TestRunStepsReportsEveryStepItPassed(t *testing.T) {
	var a, b bool
	var out strings.Builder
	if err := runSteps([]step{fixed("one", nil, &a), fixed("two", nil, &b)}, nil, &out); err != nil {
		t.Fatalf("runSteps failed on steps that pass: %v", err)
	}
	for _, want := range []string{"PASS one", "PASS two", "the runner is exercised"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not carry %q:\n%s", want, out.String())
		}
	}
}

// A step whose prerequisite is missing skips with the reason, and never
// runs. A skip that silently reported a pass is the exact failure this
// program exists to stop shipping.
func TestRunStepsSkipsWithANamedReasonWithoutRunningTheStep(t *testing.T) {
	ran := false
	s := fixed("needs-git", errors.New("must not run"), &ran)
	s.absent = func() string { return "git is not in PATH" }
	var out strings.Builder
	if err := runSteps([]step{s}, nil, &out); err != nil {
		t.Fatalf("a skipped step failed the run: %v", err)
	}
	if ran {
		t.Error("a skipped step ran anyway")
	}
	if !strings.Contains(out.String(), "SKIP needs-git") || !strings.Contains(out.String(), "git is not in PATH") {
		t.Errorf("the skip does not name the step and its reason:\n%s", out.String())
	}
}

// A step reports every disagreement it found, not the first. "The commit
// is empty" and "the branch is wrong" are one diagnosis together and two
// dead ends apart.
func TestCheckReportsEveryDisagreement(t *testing.T) {
	c := &check{}
	if err := c.err(); err != nil {
		t.Fatalf("an empty check reported %v", err)
	}
	wantEqual(c, "the commit", "", "abc123")
	wantEqual(c, "the branch", "wip", "chore/pika-improve")
	err := c.err()
	if err == nil {
		t.Fatal("two failed assertions reported success")
	}
	for _, want := range []string{"the commit", "abc123", "the branch", "chore/pika-improve", "(1)", "(2)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not carry %q:\n%v", want, err)
		}
	}
	if wantEqual(c, "equal values", 7, 7); len(c.problems) != 2 {
		t.Errorf("an assertion that holds recorded a problem: %v", c.problems)
	}
}

// contains names every missing string once, and reproduces the text that
// was searched. A gate that reports "the message is wrong" without the
// message costs an operator the whole investigation.
func TestContainsNamesWhatIsMissingAndShowsTheText(t *testing.T) {
	c := &check{}
	c.contains("the refusal", "chore/pika-improve already exists", "git branch -D", "--branch", "chore/pika-improve")
	if len(c.problems) != 1 {
		t.Fatalf("expected the missing strings to be reported together, got %d problems: %v", len(c.problems), c.problems)
	}
	got := c.problems[0]
	for _, want := range []string{"the refusal", `"git branch -D"`, `"--branch"`, "chore/pika-improve already exists"} {
		if !strings.Contains(got, want) {
			t.Errorf("problem does not carry %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"chore/pika-improve"`) {
		t.Errorf("a string that was present was reported missing:\n%s", got)
	}
}

// absent is how a repaired message states what must never come back.
func TestAbsentCatchesTextThatMustBeGone(t *testing.T) {
	c := &check{}
	c.absent("the refusal", "fatal: a branch named 'x' already exists", "a branch named", "exit status 128")
	if len(c.problems) != 1 {
		t.Fatalf("expected one problem, got %v", c.problems)
	}
	if !strings.Contains(c.problems[0], `"a branch named"`) {
		t.Errorf("problem does not name the text that came back:\n%s", c.problems[0])
	}
	if strings.Contains(c.problems[0], `"exit status 128"`) {
		t.Errorf("a string that was absent was reported present:\n%s", c.problems[0])
	}
}

// A truncated excerpt says how much it dropped. Text that trails off
// reads as a product that stopped mid-sentence.
func TestExcerptAdmitsToTruncating(t *testing.T) {
	long := strings.Repeat("x", 100)
	got := excerpt(long, 10)
	if !strings.HasPrefix(got, strings.Repeat("x", 10)) {
		t.Errorf("excerpt did not keep the head: %q", got)
	}
	if !strings.Contains(got, "90 more bytes") {
		t.Errorf("excerpt does not say what it dropped: %q", got)
	}
	if excerpt(long, 100) != long {
		t.Error("excerpt truncated text that fits")
	}
}

// The step table is what the gate runs. A duplicate or empty id would
// make a failure unattributable, and a step with no claim would report a
// PASS line that says nothing.
func TestEveryStepIsNamedAndStatesWhatItProves(t *testing.T) {
	seen := map[string]bool{}
	for i, s := range steps {
		if s.id == "" {
			t.Errorf("step %d has no id", i)
		}
		if seen[s.id] {
			t.Errorf("two steps are called %q", s.id)
		}
		seen[s.id] = true
		if s.proves == "" {
			t.Errorf("step %q states no claim", s.id)
		}
		if s.run == nil {
			t.Errorf("step %q has nothing to run", s.id)
		}
	}
	if len(steps) == 0 {
		t.Fatal("the gate runs no steps at all")
	}
}

// The gate builds ./cmd/pika out of the module it is running in. If the
// root it resolves does not hold that package, the build fails with a
// toolchain message and no step ever runs.
func TestModuleRootHoldsTheBinaryUnderTest(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		filepath.Join("cmd", "pika", "main.go"),
		filepath.Join("internal", "e2e", "testdata", "fakecodex", "main.go"),
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("module root %s does not hold %s: %v", root, rel, err)
		}
	}
}

// The planted defects have to be defects. A "misformatted" file that
// gofmt already agrees with would make the improve steps hand the agent
// a repository with nothing wrong in it, and the run would deliver
// nothing while every assertion about the delivery still passed.
func TestThePlantedDefectsAreReallyMisformattedAndTheRepairIsNot(t *testing.T) {
	for name, src := range map[string]string{
		"defectiveEntry": defectiveEntry,
		"defectiveGreet": defectiveGreet,
	} {
		formatted, err := format.Source([]byte(src))
		if err != nil {
			t.Errorf("%s does not parse, so it would fail the typecheck gate instead of the format gate: %v", name, err)
			continue
		}
		if string(formatted) == src {
			t.Errorf("%s is already gofmt-clean, so the format gate would not report it:\n%s", name, src)
		}
	}
	formatted, err := format.Source([]byte(repairedGreet))
	if err != nil {
		t.Fatalf("repairedGreet does not parse: %v", err)
	}
	if string(formatted) != repairedGreet {
		t.Errorf("repairedGreet is not gofmt-clean, so the recheck would stay red:\nwant %q\ngot  %q", formatted, repairedGreet)
	}
}

// corruptLock has to move every digest in the document. One it missed
// would leave the lock step asserting a both-causes message against a
// repository whose lock still agreed with the binary.
func TestCorruptLockMovesEveryDigest(t *testing.T) {
	dir := t.TempDir()
	const original = `{
  "digest": "aaaa",
  "packs": {
    "core": {"version": "1", "source": "embedded", "digest": "bbbb"},
    "go": {"version": "1", "source": "embedded", "digest": "cccc"}
  }
}
`
	if err := writeRepo(dir, ".project/profiles.lock", original); err != nil {
		t.Fatal(err)
	}
	if err := corruptLock(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := readRepo(dir, ".project/profiles.lock")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Digest string `json:"digest"`
		Packs  map[string]struct {
			Version string `json:"version"`
			Digest  string `json:"digest"`
		} `json:"packs"`
	}
	if err := json.Unmarshal([]byte(raw), &lock); err != nil {
		t.Fatalf("corruptLock wrote something that is not JSON: %v\n%s", err, raw)
	}
	if lock.Digest != foreignDigest {
		t.Errorf("top-level digest = %q, want the foreign one", lock.Digest)
	}
	if len(lock.Packs) != 2 {
		t.Fatalf("corruptLock dropped pack entries: %+v", lock.Packs)
	}
	for name, p := range lock.Packs {
		if p.Digest != foreignDigest {
			t.Errorf("pack %s digest = %q, want the foreign one", name, p.Digest)
		}
		if p.Version != "1" {
			t.Errorf("pack %s lost its version: %q", name, p.Version)
		}
	}
}

// A lock this step cannot recognize is refused rather than quietly left
// alone: silently corrupting nothing would make the step assert a
// disagreement that does not exist.
func TestCorruptLockRefusesADocumentItDoesNotRecognize(t *testing.T) {
	dir := t.TempDir()
	if err := writeRepo(dir, ".project/profiles.lock", `{"digest":"aaaa"}`); err != nil {
		t.Fatal(err)
	}
	if err := corruptLock(dir); err == nil {
		t.Error("a lock recording no packs was accepted")
	}
}
