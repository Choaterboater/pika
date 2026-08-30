package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/workrec"
)

// resumeEnvelopeRunID is the run TestEveryJSONCommandEmitsTheEnvelope
// resumes. jsonCases is a package-level table, so the id has to be a
// constant rather than one minted at run time; it is a real work id and
// seedFinishedRun writes a record under exactly it.
const resumeEnvelopeRunID = "20260830-finished-run-abcd1234"

// seedFinishedRun puts one already-terminal run in dir. Resuming it is a
// refusal, which is a real exercise of the whole command: it resolves the
// root, opens the record, and answers inside the envelope.
func seedFinishedRun(t *testing.T, dir string) {
	t.Helper()
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}
	seedRun(t, root, workrec.Record{
		WorkID:  resumeEnvelopeRunID,
		Kind:    workrec.KindRepair,
		Phase:   workrec.PhaseDeliver,
		Branch:  "chore/pika-improve",
		Outcome: workrec.OutcomeComplete,
	}, time.Now())
}

func resumeOut(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runResume(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestResumeRejectsUnknownFlag(t *testing.T) {
	code, _, stderrOut := resumeOut(t, "--unknown")
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr: %s", code, stderrOut)
	}
	if !strings.Contains(stderrOut, "flag provided but not defined") {
		t.Fatalf("stderr = %q, want flag error", stderrOut)
	}
}

// resume takes the run it is asked to continue. Defaulting to "the last
// one" would make the destructive half of pika guess which run an
// operator meant.
func TestResumeRequiresAWorkID(t *testing.T) {
	dir, _ := statusFixture(t)
	code, _, stderrOut := resumeOut(t, "--root", dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr: %s", code, stderrOut)
	}
	if !strings.Contains(stderrOut, "a work id is required") || !strings.Contains(stderrOut, resumeUsage) {
		t.Fatalf("stderr = %q, want the missing-id refusal and the synopsis", stderrOut)
	}
}

func TestResumeRejectsAMalformedWorkID(t *testing.T) {
	dir, _ := statusFixture(t)
	code, _, stderrOut := resumeOut(t, "yesterday's run", "--root", dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr: %s", code, stderrOut)
	}
	if !strings.Contains(stderrOut, "work_id") {
		t.Fatalf("stderr = %q, want the work-id shape explained", stderrOut)
	}
}

// An id nobody can look up is a dead end, and the operator's next move is
// to check what they typed — so the refusal repeats it back, the way
// `pika status` does.
func TestResumeUnknownWorkIDExitsTwoNamingIt(t *testing.T) {
	dir, _ := statusFixture(t)
	code, _, stderrOut := resumeOut(t, "20260830-missing-run-abcd1234", "--root", dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr: %s", code, stderrOut)
	}
	if !strings.Contains(stderrOut, "20260830-missing-run-abcd1234") || !strings.Contains(stderrOut, dir) {
		t.Fatalf("stderr = %q, want the id and the repository named", stderrOut)
	}
}

// A refusal is a state of the repository rather than work that ran and
// failed, so it exits 2 and carries its reason inside the envelope. An
// agent must be able to tell "this run is finished" from "the run I
// resumed failed" without parsing prose.
func TestResumeRefusalTravelsInTheEnvelope(t *testing.T) {
	dir, _ := statusFixture(t)
	seedFinishedRun(t, dir)
	code, out, stderrOut := resumeOut(t, resumeEnvelopeRunID, "--json", "--root", dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stdout: %s stderr: %s", code, out, stderrOut)
	}
	if stderrOut != "" {
		t.Fatalf("stderr = %q, want the envelope to be the whole answer", stderrOut)
	}
	env := envelopeOf(t, []byte(out), "resume")
	if env.OK {
		t.Errorf("ok = true on a refusal:\n%s", out)
	}
	if env.Error == nil || env.Error.Code != codeConfig {
		t.Fatalf("error = %+v, want code %q:\n%s", env.Error, codeConfig, out)
	}
	if !strings.Contains(env.Error.Message, "already reached a terminal outcome") {
		t.Errorf("message = %q, want the finished-run refusal", env.Error.Message)
	}
}
