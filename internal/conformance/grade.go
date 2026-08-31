package conformance

import (
	"fmt"
	"os/exec"
	"strings"
)

// The graders: what makes a row's outcome a verdict rather than a
// transcript.
//
// They are separate from the manifest on purpose. Corpus is data a
// maintainer edits to add a repository; these are the rules every row is
// held to, and the difference matters when reading a red run — a
// disagreement here is a claim about pika, and a disagreement in the
// manifest is a claim about a repository.

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
//
// The command is graded beside the verdict, and for the same reason one
// rung further down. A rung is green either because the command the
// manifest recorded succeeded or because some other command did, and a
// grader that could not tell those apart is how go@1's four hints spent
// a whole milestone being reported as covered without ever being run.
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
		if cmd := strings.Join(g.Cmd, " "); cmd != w.Cmd {
			found = append(found, fmt.Sprintf("gate %s: ran %q; the manifest records %q\n    %s", w.ID, cmd, w.Cmd, g.Evidence()))
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
