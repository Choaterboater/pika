package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/adopt"
)

// report builds the minimum Report printAdoptReport needs. Every other
// field is nil-safe and renders as an empty section.
func reportWithBaseline(checks ...adopt.BaselineCheck) *adopt.Report {
	return &adopt.Report{BaselineChecks: checks}
}

// The defect this covers: cobra adopted with `make lint` already failing,
// adopt printed that one line among many, then printed "drafts written"
// and exited 0. An operator reads that as success and runs apply, and the
// ladder goes red on a repository adopt had already measured as red. The
// failures were on screen; nothing said what they meant.
func TestAdoptSaysPlainlyWhenTheBaselineIsNotGreen(t *testing.T) {
	var out bytes.Buffer
	printAdoptReport(reportWithBaseline(
		adopt.BaselineCheck{Verb: "fmt", Command: "make fmt", Status: "pass"},
		adopt.BaselineCheck{Verb: "lint", Command: "make lint", Exit: 2, Status: "fail"},
	), &out)

	got := out.String()
	if !strings.Contains(got, "baseline is not green") {
		t.Fatalf("the report never says the baseline is red:\n%s", got)
	}
	// Naming the verb is the point: "something failed" sends the operator
	// back to scroll, and the whole defect was a failure that scrolled past.
	if !strings.Contains(got, "lint") {
		t.Errorf("the summary does not name the failing gate:\n%s", got)
	}
	if strings.Contains(got, "fmt is failing") {
		t.Errorf("the summary names a gate that passed:\n%s", got)
	}
}

// A timeout is not a pass. It is reported as its own status and must
// count as not-green, or a repository whose tests hang adopts silently.
func TestATimedOutBaselineIsNotGreenEither(t *testing.T) {
	var out bytes.Buffer
	printAdoptReport(reportWithBaseline(
		adopt.BaselineCheck{Verb: "test", Command: "make test", Exit: -1, Status: "timeout"},
	), &out)

	if got := out.String(); !strings.Contains(got, "baseline is not green") {
		t.Fatalf("a timed-out baseline reported as green:\n%s", got)
	}
}

// The line must not appear when there is nothing to warn about, or it
// becomes noise every adoption prints and every operator learns to skip.
func TestAGreenBaselineSaysNothingExtra(t *testing.T) {
	var out bytes.Buffer
	printAdoptReport(reportWithBaseline(
		adopt.BaselineCheck{Verb: "fmt", Command: "make fmt", Status: "pass"},
		adopt.BaselineCheck{Verb: "test", Command: "make test", Status: "pass"},
	), &out)

	if got := out.String(); strings.Contains(got, "not green") {
		t.Fatalf("a fully green baseline still warned:\n%s", got)
	}
}

// A repository with no discovered checks has no baseline to be red.
func TestNoDiscoveredChecksIsNotReportedAsARedBaseline(t *testing.T) {
	var out bytes.Buffer
	printAdoptReport(reportWithBaseline(), &out)

	if got := out.String(); strings.Contains(got, "not green") {
		t.Fatalf("an empty baseline warned:\n%s", got)
	}
}

func TestTheBaselineSummaryReadsCorrectlyForOneAndForMany(t *testing.T) {
	var one bytes.Buffer
	printAdoptReport(reportWithBaseline(
		adopt.BaselineCheck{Verb: "lint", Command: "make lint", Exit: 2, Status: "fail"},
	), &one)
	if got := one.String(); !strings.Contains(got, "lint is failing") || !strings.Contains(got, "that gate will fail") {
		t.Errorf("singular phrasing is wrong:\n%s", got)
	}

	var many bytes.Buffer
	printAdoptReport(reportWithBaseline(
		adopt.BaselineCheck{Verb: "lint", Command: "make lint", Exit: 2, Status: "fail"},
		adopt.BaselineCheck{Verb: "test", Command: "make test", Exit: 1, Status: "fail"},
	), &many)
	if got := many.String(); !strings.Contains(got, "lint, test are failing") || !strings.Contains(got, "those gates will fail") {
		t.Errorf("plural phrasing is wrong:\n%s", got)
	}
}
