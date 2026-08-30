package improve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/evidence"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/verify"
	"github.com/Choaterboater/pika/internal/workrec"
)

// A receipt an agent writes about itself is a claim. This one is issued
// by the component that ran the gates, so it has to survive the same
// schema every published receipt is held to.
func TestDeliveredRunEmitsSchemaValidReceipt(t *testing.T) {
	root := fixtureAdoptedRepository(t)
	result := deliveredRun(t, root)

	receipt, raw := readReceipt(t, root, result.WorkID)
	if err := evidence.Validate(receipt); err != nil {
		t.Fatalf("receipt does not validate against the embedded schema: %v", err)
	}
	if receipt.WorkID != result.WorkID {
		t.Fatalf("receipt work id = %q, want %q", receipt.WorkID, result.WorkID)
	}
	// The contract and the lock as they stand on disk, not as the run
	// remembers them.
	if receipt.ContractVersion != "1" {
		t.Fatalf("contract version = %q, want the contract's declared schema version 1", receipt.ContractVersion)
	}
	lock, err := profiles.ReadLock(repoRoot(t, root).Lock())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ProfileLock.Digest != lock.Digest {
		t.Fatalf("profile lock digest = %q, want %q from .project/profiles.lock", receipt.ProfileLock.Digest, lock.Digest)
	}
	if len(receipt.ProfileLock.Packs) != len(lock.Packs) {
		t.Fatalf("receipt pins %d packs, lock pins %d", len(receipt.ProfileLock.Packs), len(lock.Packs))
	}
	for name, pinned := range lock.Packs {
		got, ok := receipt.ProfileLock.Packs[name]
		if !ok {
			t.Fatalf("receipt does not pin pack %q", name)
		}
		if got.Version != pinned.Version || got.Source != pinned.Source || got.Digest != pinned.Digest {
			t.Fatalf("pack %s = %+v, want %+v", name, got, pinned)
		}
	}
	// The schema forbids a blocker on a complete run: it must be absent,
	// not merely empty.
	if _, present := raw["completion"].(map[string]any)["blocker"]; present {
		t.Fatalf("complete receipt carries a blocker: %v", raw["completion"])
	}
}

// The point of a kernel-issued receipt is that every field traces back to
// something the run observed. Everything asserted here is read out of the
// durable record or back out of Git — never out of hand-written input.
func TestReceiptMatchesWhatTheRunObserved(t *testing.T) {
	root := fixtureAdoptedRepository(t)
	result := deliveredRun(t, root)
	rec := runRecord(t, root, result.WorkID)
	receipt, _ := readReceipt(t, root, rec.WorkID)

	if rec.Commit == "" || receipt.Commit != rec.Commit {
		t.Fatalf("receipt commit = %q, record commit = %q", receipt.Commit, rec.Commit)
	}
	if want := gitOutput(t, root, "rev-parse", rec.Commit+"^{tree}"); receipt.Tree != want {
		t.Fatalf("receipt tree = %q, want %q", receipt.Tree, want)
	}

	// The changed files are the commit's own contents, not a list the
	// run carried in memory.
	var paths []string
	for _, cf := range receipt.ChangedFiles {
		if cf.Ownership != "agent" {
			t.Fatalf("changed file %q ownership = %q, want agent", cf.Path, cf.Ownership)
		}
		paths = append(paths, cf.Path)
	}
	want := strings.Split(gitOutput(t, root, "diff-tree", "--no-commit-id", "--name-only", "-r", rec.Commit), "\n")
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("changed files = %v, want the commit's files %v", paths, want)
	}

	// Every executed gate of both ladder runs, in the order they ran,
	// with the argv, exit, duration and output the report recorded. A
	// gate that was skipped never ran and must not appear.
	var wantCmds []evidence.Command
	for _, report := range []*verify.Report{rec.Baseline, rec.Recheck} {
		if report == nil {
			t.Fatal("record is missing a ladder report")
		}
		for _, gate := range report.Gates {
			if gate.Status == verify.StatusSkip {
				continue
			}
			wantCmds = append(wantCmds, evidence.Command{
				Cmd:           gateCmd(gate),
				Exit:          gate.Exit,
				DurationMs:    gate.DurationMs,
				OutputSummary: gate.OutputTail,
			})
		}
	}
	if len(receipt.Commands) != len(wantCmds) {
		t.Fatalf("receipt records %d commands, the record's reports executed %d: %+v", len(receipt.Commands), len(wantCmds), receipt.Commands)
	}
	for i, got := range receipt.Commands {
		if got.Cmd != wantCmds[i].Cmd || got.Exit != wantCmds[i].Exit ||
			got.DurationMs != wantCmds[i].DurationMs || got.OutputSummary != wantCmds[i].OutputSummary {
			t.Fatalf("command %d = %+v, want %+v from the record", i, got, wantCmds[i])
		}
	}
	if got := receipt.Commands[0].Cmd; got != "pika: in-process gate contract" {
		t.Fatalf("in-process gate cmd = %q, want it named rather than given a command line it never ran", got)
	}

	// What the ladder found before the agent worked, and nothing the
	// agent broke.
	if got, want := strings.Join(receipt.BaselineFailures, ","), "lint: exit 1"; got != want {
		t.Fatalf("baseline failures = %v, want %q from the recorded baseline", receipt.BaselineFailures, want)
	}
	if len(receipt.Regressions) != 0 {
		t.Fatalf("regressions = %v, want none: the recorded recheck passed", receipt.Regressions)
	}

	// The role and runtime actually spawned, with the provider and model
	// the contract configured for them.
	if len(receipt.Roles) != 1 {
		t.Fatalf("roles = %+v, want the one agent the run spawned", receipt.Roles)
	}
	role := receipt.Roles[0]
	if role.Role != rec.Role || role.Runtime != rec.Runtime {
		t.Fatalf("role = %+v, want role %q runtime %q from the record", role, rec.Role, rec.Runtime)
	}
	if role.Role == "" || role.Runtime == "" {
		t.Fatal("the record left role or runtime empty: an attestation must name what ran")
	}
	if role.Provider != "openai" || role.Model != "gpt-5-codex" || role.Substituted {
		t.Fatalf("role = %+v, want the contract's provider and model with no substitution", role)
	}

	if rec.Outcome != workrec.OutcomeComplete {
		t.Fatalf("record outcome = %q, want complete", rec.Outcome)
	}
	if !receipt.Completion.Complete || receipt.Completion.Reason != "" {
		t.Fatalf("completion = %+v, want complete with no reason", receipt.Completion)
	}
	if receipt.SurfaceScenario.Ran {
		t.Fatal("receipt claims a real-surface scenario ran; improve runs none")
	}
}

// A milestone about durable evidence that only attests successes attests
// the wrong half. The blocked run's reason is the one the record holds,
// verbatim.
func TestBlockedRunEmitsIncompleteReceiptWithReason(t *testing.T) {
	root := fixtureAdoptedRepository(t)
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{
			{ID: "lint", Cmd: []string{"golangci-lint", "run"}, Status: verify.StatusFail, Exit: 1, DurationMs: 11, OutputTail: "repair this"},
		}},
		{Pass: false, Gates: []verify.GateResult{
			{ID: "lint", Cmd: []string{"golangci-lint", "run"}, Status: verify.StatusPass, Exit: 0, DurationMs: 9},
			{ID: "test", Cmd: []string{"go", "test", "./..."}, Status: verify.StatusFail, Exit: 2, DurationMs: 31, OutputTail: "FAIL"},
		}},
	}
	result, err := Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-improve",
		Agent:   "builder",
		Runtime: "codex",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Runner: repairRunner{path: "fixed.txt", body: "not enough\n"},
	})
	if err == nil {
		t.Fatal("run reported success on a failed recheck")
	}
	rec := runRecord(t, root, result.WorkID)
	if rec.Outcome != workrec.OutcomeBlocked || rec.Reason == "" {
		t.Fatalf("record = %+v, want a blocked outcome with a reason", rec)
	}

	receipt, raw := readReceipt(t, root, result.WorkID)
	if err := evidence.Validate(receipt); err != nil {
		t.Fatalf("blocked receipt does not validate: %v", err)
	}
	if receipt.Completion.Complete {
		t.Fatalf("completion = %+v, want incomplete", receipt.Completion)
	}
	if receipt.Completion.Reason != rec.Reason {
		t.Fatalf("receipt reason = %q, want the record's reason %q verbatim", receipt.Completion.Reason, rec.Reason)
	}
	if _, present := raw["completion"].(map[string]any)["reason"]; !present {
		t.Fatal("incomplete receipt carries no reason")
	}
	if receipt.Commit != "" || len(receipt.ChangedFiles) != 0 {
		t.Fatalf("receipt = commit %q files %+v, want neither: the run committed nothing", receipt.Commit, receipt.ChangedFiles)
	}
	// The gate the agent broke is a regression; the one it was handed is
	// not, however the recheck ended.
	if got, want := strings.Join(receipt.Regressions, ","), "test: exit 2"; got != want {
		t.Fatalf("regressions = %v, want %q", receipt.Regressions, want)
	}
	if got, want := strings.Join(receipt.BaselineFailures, ","), "lint: exit 1"; got != want {
		t.Fatalf("baseline failures = %v, want %q", receipt.BaselineFailures, want)
	}
}

// evidence.Write renames over an existing target without complaint, so
// the refusal has to live here. A second receipt for one work id would
// replace evidence that has already been read, cited, or committed.
func TestReceiptRefusesToOverwriteAnExistingReceipt(t *testing.T) {
	root := fixtureAdoptedRepository(t)
	result := deliveredRun(t, root)
	path := filepath.Join(root, ".project", "evidence", result.WorkID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	err = issueReceipt(context.Background(), repoRoot(t, root), runRecord(t, root, result.WorkID))
	if !errors.Is(err, ErrReceiptExists) {
		t.Fatalf("error = %v, want ErrReceiptExists", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("the refused write still changed the receipt on disk")
	}
}

// A repair run that finds a green ladder attempts nothing: no agent, no
// commit, no changed file. `.project/evidence` is committed content, so
// issuing a receipt there would leave a healthy repository dirty after
// every no-op run.
func TestGreenBaselineIssuesNoReceipt(t *testing.T) {
	root := fixtureAdoptedRepository(t)
	_, err := Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-improve",
		Agent:   "builder",
		Runtime: "codex",
		Check:   func() (*verify.Report, error) { return &verify.Report{Pass: true}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".project", "evidence")); !os.IsNotExist(err) {
		t.Fatalf("stat .project/evidence = %v, want it never created", err)
	}
	if got := gitOutput(t, root, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Fatalf("status = %q, want a healthy repository left clean", got)
	}
}

// deliveredRun drives one repair run all the way to a verified commit.
// The reports are the run's own observations: one in-process gate, one
// failing command gate the agent is handed, and a gate skipped behind
// it, then a green recheck.
func deliveredRun(t *testing.T, root string) Result {
	t.Helper()
	checks := []*verify.Report{
		{Pass: false, Gates: []verify.GateResult{
			{ID: "contract", Status: verify.StatusPass, Exit: 0, DurationMs: 3},
			{ID: "lint", Cmd: []string{"golangci-lint", "run"}, Status: verify.StatusFail, Exit: 1, DurationMs: 12, OutputTail: "repair this"},
			{ID: "test", Status: verify.StatusSkip, Reason: "skipped: gate lint failed"},
		}},
		{Pass: true, Gates: []verify.GateResult{
			{ID: "contract", Status: verify.StatusPass, Exit: 0, DurationMs: 2},
			{ID: "lint", Cmd: []string{"golangci-lint", "run"}, Status: verify.StatusPass, Exit: 0, DurationMs: 9, OutputTail: "0 issues"},
			{ID: "test", Cmd: []string{"go", "test", "./..."}, Status: verify.StatusPass, Exit: 0, DurationMs: 40},
		}},
	}
	result, err := Run(context.Background(), Config{
		Root:    root,
		Branch:  "chore/pika-improve",
		Agent:   "builder",
		Runtime: "codex",
		Check: func() (*verify.Report, error) {
			report := checks[0]
			checks = checks[1:]
			return report, nil
		},
		Runner: repairRunner{path: "fixed.txt", body: "verified fix\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// readReceipt reads the receipt the run wrote, both as the typed record
// and as raw JSON — the schema's rules about which keys may be present
// are only observable in the raw document.
func readReceipt(t *testing.T, root, workID string) (*evidence.Receipt, map[string]any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".project", "evidence", workID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt evidence.Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return &receipt, raw
}

// fixtureAdoptedRepository is a repository pika has adopted: a committed
// contract that configures the agent the run spawns, and the matching
// profile lock. The receipt states both, so a fixture without them would
// prove nothing about where those fields come from.
func fixtureAdoptedRepository(t *testing.T) string {
	t.Helper()
	root := fixtureRepository(t)
	project := filepath.Join(root, ".project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `schema: 1
project:
  name: fixture
  topology: single
profiles: [core@1]
agents:
  builder:
    runtime: codex
    provider: openai
    model: gpt-5-codex
github:
  merge: squash
evidence:
  publish: sanitized
`
	if err := os.WriteFile(filepath.Join(project, "contract.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := profiles.WriteLock(filepath.Join(project, "profiles.lock"), []string{"core@1"}); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", ".project/contract.yaml", ".project/profiles.lock")
	gitRun(t, root, "commit", "-qm", "adopt")
	if got := gitOutput(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("fixture is dirty: %q", got)
	}
	return root
}
