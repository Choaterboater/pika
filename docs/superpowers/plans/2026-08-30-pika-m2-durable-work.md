# pika M2 — Durable Work and Kernel-Issued Evidence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give pika's automation memory — durable, resumable, queryable work runs — and make the kernel, not the agent, the thing that issues evidence.

**Architecture:** A new `internal/workrec` writes one atomically-updated record per run under `.project/state/work/<work-id>/`. `internal/improve`'s existing loop becomes the `repair` kind of a shared lifecycle that also serves `feature` work, with every current refusal preserved. At delivery the kernel builds an `evidence.Receipt` from what it observed. Four M1.5 hardening deferrals land alongside, because long restartable agent-driven runs make them load-bearing.

**Tech Stack:** Go 1.26, stdlib only. `github.com/goccy/go-yaml` and `github.com/santhosh-tekuri/jsonschema/v6` remain the only direct dependencies — no SQLite (see spec §5.1).

**Spec:** [docs/superpowers/specs/2026-08-30-pika-m2-durable-work-design.md](../specs/2026-08-30-pika-m2-durable-work-design.md)

## Global Constraints

- `go.mod` MUST still declare exactly two direct dependencies at the end of this milestone.
- `CGO_ENABLED=0 go build ./...` MUST succeed. macOS, Linux and Windows are supported targets.
- No code path may call a model. The kernel spawns the operator's harness; it never speaks to a provider.
- `pika check --ci` MUST stay provably LLM-free and MUST pass on this repository in GitHub Actions.
- Every command supports `--json` through the `internal/cliout` envelope `{schema, command, ok, result}`.
- Exit codes: `0` success, `1` failure, `2` usage or configuration error.
- Deny-by-default: authorization precedes any filesystem or process effect on the MCP surface.
- Register new commands in the `commands` LITERAL in `cmd/pika/main.go`, never in `init()`.
- Commit after every task. Conventional commits, imperative subjects.
- Run only the tests named in each task. The full suite runs once, in Task 13.
- `.project/state/` is gitignored and stays that way: run records are local, receipts are committed.

---

### Task 1: Collision-resistant work IDs

**Files:**
- Modify: `internal/evidence/receipt.go` (`NewWorkID`)
- Test: `internal/evidence/evidence_test.go`

**Interfaces:**
- Produces: `evidence.NewWorkID(now time.Time, slug string) (string, error)` — same signature, non-deterministic suffix.

`NewWorkID` currently derives its 4-hex suffix from `sha256(slug ‖ 0x00 ‖ unix-seconds)`, so two runs with the same slug in the same second produce the SAME id and overwrite one evidence file. Determinism was never required; it was an artifact.

- [ ] **Step 1: Write the failing test**

```go
func TestWorkIDsDoNotCollideWithinOneSecond(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	const n = 512
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id, err := NewWorkID(now, "auth-timeout")
		if err != nil {
			t.Fatalf("NewWorkID: %v", err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("collision after %d ids: %q repeated for one slug in one second", i, id)
		}
		seen[id] = struct{}{}
		if err := ValidateWorkID(id); err != nil {
			t.Fatalf("ValidateWorkID(%q): %v", id, err)
		}
	}
}
```

With 16 bits of suffix, 512 draws will collide by the birthday bound. Use a wider suffix or assert a low duplicate rate — decide in Step 3 and make the test match the implementation you choose. Do NOT weaken the test to accept the current behavior, which produces 512 identical ids.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/evidence/ -run TestWorkIDsDoNotCollide`
Expected: FAIL on the first duplicate — every id is identical today.

- [ ] **Step 3: Implement**

Replace the digest suffix with `crypto/rand`. Keep the `YYYYMMDD-slug-4hex` shape so `ValidateWorkID` and every existing receipt stay valid. If you widen the suffix, `workIDPattern` and the receipt JSON Schema's `work_id` pattern must change together — check both before choosing. A `crypto/rand` read error must return an error, never silently fall back to a weaker source.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/evidence/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/evidence/
git commit -m "fix(evidence): draw work id suffixes from crypto/rand"
```

---

### Task 2: `internal/workrec` — the durable run record

**Files:**
- Create: `internal/workrec/workrec.go`, `internal/workrec/record.go`
- Test: `internal/workrec/workrec_test.go`

**Interfaces:**
- Consumes: `repopath.Root`, `fsutil` durability helpers, `verify.Report`.
- Produces:
  - `workrec.Create(root *repopath.Root, rec Record) (*Handle, error)` — refuses an existing id
  - `workrec.Open(root *repopath.Root, workID string) (*Handle, error)`
  - `workrec.List(root *repopath.Root) ([]Record, error)` — newest first
  - `(*Handle).Save(rec Record) error` — atomic
  - `(*Handle).Dir() string` — the run's directory, where the handoff bundle lives
  - `Record{WorkID, Goal, Kind, Phase, Branch, BaseCommit, Commit string; Baseline, Recheck *verify.Report; Role, Runtime string; Outcome, Reason string; Phases []PhaseStamp}`
  - Phase constants `PhaseBaseline`, `PhaseHandoff`, `PhaseRecheck`, `PhaseDeliver`; outcomes `OutcomeComplete`, `OutcomeBlocked`, `OutcomeAbandoned`; kinds `KindRepair`, `KindFeature`.

Tasks 3-8 consume these.

- [ ] **Step 1: Write the failing test**

Cover, at minimum:

```go
func TestCreateRefusesExistingID(t *testing.T)
func TestSaveIsAtomicAcrossPhases(t *testing.T)   // write phase A, save phase B, reopen: B is readable and complete
func TestTruncatedRecordIsReportedNotRepaired(t *testing.T) // truncate record.json; Open returns an error naming the file
func TestListIsNewestFirst(t *testing.T)
func TestDirIsUnderStateAndNamedByWorkID(t *testing.T)
```

For atomicity, write a record, then verify no partially-written `record.json` is ever observable: assert the file either parses fully as the previous phase or fully as the new one. Simulate by checking that `Save` writes through a temp file in the same directory and renames — assert no `*.tmp` remains after a successful save, and that a leftover temp file from a crashed save does not confuse `Open`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/workrec/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

`.project/state/work/<work-id>/record.json`, plus a `handoff/` subdirectory the lifecycle uses for its bundle.

Durability: write to a temp file in the target directory, `chmod`, fsync the file, rename over the target, fsync the parent directory chain. `internal/evidence/write.go` already implements exactly this — reuse `internal/fsutil`'s helpers rather than writing a second durability path, and say so in a comment.

`Open` on a malformed record returns an error naming the path. It never rewrites, truncates, or "fixes" the file: a corrupt record is a fact to report, and `resume` refusing is safer than `resume` guessing.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/workrec/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/workrec/
git commit -m "feat: durable per-run work records under .project/state/work"
```

---

### Task 3: Move the handoff bundle into the run record

**Files:**
- Modify: `internal/improve/handoff.go`
- Test: `internal/improve/handoff_test.go`

**Interfaces:**
- Consumes: `(*workrec.Handle).Dir()` (Task 2).
- Produces: `improve.CreateHandoff` takes an explicit bundle directory instead of minting `.project/state/handoffs/<unixnano>/`.

Today the bundle directory is named by `time.Now().UnixNano()` and has no identity: nothing links it to a run, and no code reads it back.

- [ ] **Step 1: Write the failing test**

Assert `CreateHandoff` writes `checks-before.json`, `prompt.md` and `codex-last-message.md` into the directory it is GIVEN, that it creates no directory under `.project/state/handoffs`, and that the raw last-message file is removed after redaction. Keep the existing assertions that the prompt contains failed-gate output and excludes warnings.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/improve/ -run TestCreateHandoff`
Expected: FAIL — the signature still mints its own directory.

- [ ] **Step 3: Implement**

Thread the directory in. Preserve every existing behavior: mode `0700` on the bundle dir and `0600` on its files, redaction before write, `defer os.Remove` of the raw message, the git-state equality refusal, and `ErrNoActionableFindings`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/improve/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/improve/
git commit -m "refactor(improve): write the handoff bundle into the run's record directory"
```

---

### Task 4: The shared lifecycle

**Files:**
- Modify: `internal/improve/improve.go`
- Test: `internal/improve/improve_test.go`

**Interfaces:**
- Consumes: `workrec` (Task 2), the relocated `CreateHandoff` (Task 3).
- Produces: `improve.Run(ctx, cfg Config) (Result, error)` gains `cfg.Kind` (`workrec.KindRepair` default) and `cfg.Goal`; it creates a run record, saves after every phase transition, and records a terminal outcome on every exit path.

- [ ] **Step 1: Write the failing test**

```go
func TestRunRecordsEveryPhaseTransition(t *testing.T)
func TestGreenBaselineRecordsCompleteWithoutBranching(t *testing.T)   // repair kind
func TestFeatureKindProceedsToHandoffOnGreenBaseline(t *testing.T)
func TestAgentFailureRecordsBlockedWithReason(t *testing.T)
func TestDirtyTreeRefusalWritesNoRecord(t *testing.T)
```

The last one matters: a refusal that happens before any work must not litter `.project/state/work` with empty runs.

Preserve and re-assert every existing safety test: dirty-tree refusal, branch identity re-verified before commit, the git-state equality check after the agent returns, the pending-git-operation refusal, and `changePaths` never committing anything under `.project/state`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/improve/ -run 'TestRun|TestGreen|TestFeature|TestAgent|TestDirty'`
Expected: FAIL — no record is written.

- [ ] **Step 3: Implement**

Create the record after the dirty-tree gate passes and before the branch is created. Save on entry to each phase and on every terminal path. On a blocked outcome, record the reason verbatim from the error.

`KindFeature` differs from `KindRepair` in exactly one place: a green baseline proceeds to handoff with `cfg.Goal` as the prompt instead of short-circuiting to complete. Do not otherwise fork the state machine — two nearly-identical loops is the outcome this task exists to avoid.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/improve/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/improve/
git commit -m "feat(improve): record every lifecycle phase durably"
```

---

### Task 5: Kernel-issued evidence receipts

**Files:**
- Modify: `internal/improve/improve.go` (deliver phase)
- Create: `internal/improve/receipt.go`
- Test: `internal/improve/receipt_test.go`

**Interfaces:**
- Consumes: `evidence.Build`, `evidence.Write`, `workrec.Record`, `contract`, `profiles`.
- Produces: `improve.buildReceipt(root *repopath.Root, rec workrec.Record) (*evidence.Receipt, error)`, written to `.project/evidence/<work-id>.json` at delivery.

This is the first receipt producer in the binary. Until now the only path to a receipt was an agent asserting its own success via MCP `publish_evidence`.

- [ ] **Step 1: Write the failing test**

```go
func TestDeliveredRunEmitsSchemaValidReceipt(t *testing.T)
func TestReceiptMatchesWhatTheRunObserved(t *testing.T)  // commands, exits, commit, completion all read from the record
func TestBlockedRunEmitsIncompleteReceiptWithReason(t *testing.T)
```

Assert against the run record, not against hand-written input — a receipt built from fabricated input proves nothing about the kernel observing the work.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/improve/ -run TestReceipt`
Expected: FAIL — `buildReceipt` undefined.

- [ ] **Step 3: Implement**

Populate `ReceiptInput` from observed state: contract schema and profile-lock digests as loaded, the commit and tree produced, the role/runtime actually spawned, the committed changed files, each gate's argv/exit/duration/output from the reports, baseline failures and regressions, and completion.

`evidence.Build` is fail-closed: an unredactable pack key errors rather than emitting. Do not defeat that. The schema requires `reason` when incomplete and forbids `blocker` when complete — a blocked run must supply the reason it recorded.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/improve/ ./internal/evidence/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/improve/
git commit -m "feat: the kernel issues the evidence receipt for a delivered run"
```

---

### Task 6: `pika status`

**Files:**
- Create: `cmd/pika/status.go`, `cmd/pika/status_test.go`
- Modify: `cmd/pika/main.go`

**Interfaces:**
- Consumes: `workrec.List`, `workrec.Open`, `cliout.Write`, `resolveRoot`.
- Produces: `runStatus(args []string, stdin io.Reader, stdout, stderr io.Writer) int`.

- [ ] **Step 1: Write the failing test**

Bare `status` lists runs newest-first; `status <work-id>` prints that run's phases, outcome, branch and commit; an unknown id exits 2 naming it; `--json` unmarshals into `cliout.Envelope` with `command: "status"`. No runs at all is exit 0 with an empty list, not an error — a repository with no history is a valid state.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/pika/ -run TestStatus`
Expected: FAIL — `runStatus` undefined.

- [ ] **Step 3: Implement**

Follow `cmd/pika/doctor.go` exactly for flag handling, `--root`, and `cliout` usage. Register in the `commands` literal after `check`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/pika/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/pika/
git commit -m "feat: pika status lists and inspects work runs"
```

---

### Task 7: `pika resume`

**Files:**
- Create: `cmd/pika/resume.go`, `cmd/pika/resume_test.go`
- Modify: `internal/improve/improve.go` (resume entry point), `cmd/pika/main.go`
- Test: `internal/improve/improve_test.go`

**Interfaces:**
- Consumes: `workrec.Open`, `improve.Run`.
- Produces: `improve.Resume(ctx, root, workID string, cfg Config) (Result, error)`; `runResume` in package `main`.

- [ ] **Step 1: Write the failing test**

```go
func TestResumeContinuesFromEachInterruptiblePhase(t *testing.T)
func TestResumeRefusesTerminalOutcome(t *testing.T)
func TestResumeRefusesMissingBranch(t *testing.T)
func TestResumeRefusesDivergedTree(t *testing.T)  // HEAD no longer at the recorded base commit
```

Each refusal must carry a DISTINCT message. "Cannot resume" tells an operator nothing about which of three worlds they are in.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/improve/ -run TestResume`
Expected: FAIL — `Resume` undefined.

- [ ] **Step 3: Implement**

Read the record, validate the world against it, and re-enter the lifecycle at the phase after the last committed one. Resuming into a changed world silently is worse than refusing — every guard is a refusal with a specific reason, never a best-effort continue.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/improve/ ./cmd/pika/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/improve/ cmd/pika/
git commit -m "feat: pika resume continues an interrupted run or refuses with a reason"
```

---

### Task 8: `pika work`

**Files:**
- Create: `cmd/pika/work.go`, `cmd/pika/work_test.go`
- Modify: `cmd/pika/main.go`

**Interfaces:**
- Consumes: `improve.Run` with `Kind: workrec.KindFeature`.
- Produces: `runWork(args []string, stdin io.Reader, stdout, stderr io.Writer) int`.

`pika work "<goal>"` is the spec's normal entry point (design spec §4.2). It takes exactly one positional goal.

- [ ] **Step 1: Write the failing test**

Zero or two-plus positionals exit 2. An empty or whitespace-only goal exits 2. `--branch`, `--agent`, `--json`, `--root` behave as `improve`'s do. The goal reaches the handoff prompt.

Note `explain` already solves accepting flags on either side of a positional — copy that approach rather than inventing a second one.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/pika/ -run TestWork`
Expected: FAIL — `runWork` undefined.

- [ ] **Step 3: Implement**

Register in the `commands` literal. Update `README.md`'s command table and add a `docs/guides/usage.md` section in the existing style.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/pika/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/pika/ README.md docs/
git commit -m "feat: pika work runs a goal through the durable lifecycle"
```

---

### Task 9: `pika recover`

**Files:**
- Create: `cmd/pika/recover.go`, `cmd/pika/recover_test.go`
- Modify: `internal/txn/` (export what the command needs), `internal/doctor/doctor.go`, `cmd/pika/main.go`
- Test: `internal/txn/apply_test.go`, `internal/doctor/doctor_test.go`

**Interfaces:**
- Consumes: `txn.Recover` and the lock's holder metadata.
- Produces: `runRecover`; a `doctor` finding for a stale transaction lock.

`txn.Recover` has NO production caller. The `O_EXCL` lock at `.project/state/recovery/lock` is never stolen — not even when the holder PID is dead — so a crashed `pika apply` wedges every future transaction until an operator deletes the lock by hand, and nothing in the product tells them that.

- [ ] **Step 1: Write the failing test**

```go
func TestRecoverRollsBackACrashedJournal(t *testing.T)
func TestRecoverRefusesALiveLock(t *testing.T)        // holder pid alive -> refuse, name the holder
func TestRecoverReleasesADeadHoldersLock(t *testing.T)
func TestDoctorReportsAStaleTransactionLock(t *testing.T)
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/pika/ -run TestRecover; go test ./internal/doctor/ -run TestDoctorReportsAStale`
Expected: FAIL — command and finding do not exist.

- [ ] **Step 3: Implement**

Default is a report: journal state, holder, and what would be rolled back. `--apply` performs recovery and releases the lock. A live holder is always refused — stealing a lock from a running process is how two transactions corrupt one tree.

`doctor` gains a finding pointing at `pika recover`. Reuse the existing process-liveness helpers in `internal/txn` rather than adding a second implementation.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/pika/ ./internal/txn/ ./internal/doctor/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/pika/ internal/txn/ internal/doctor/
git commit -m "feat: pika recover unwedges a crashed transaction"
```

---

### Task 10: Re-entrancy guard

**Files:**
- Modify: `internal/verify/verify.go`
- Test: `internal/verify/verify_test.go`

**Interfaces:**
- Produces: gates spawn with a nesting marker in their environment; `verify.Run` refuses when already nested.

`pika check`'s test gate can invoke pika, whose suite invokes pika. This fired during M1.5 and saturated a machine with ~20 orphaned drivers. There is no guard today: no `PIKA_*` variable is read or set anywhere, and gates inherit the parent environment untouched.

- [ ] **Step 1: Write the failing test**

```go
func TestNestedRunIsRefused(t *testing.T)
func TestGateEnvironmentCarriesTheMarker(t *testing.T)
func TestUnnestedRunIsUnaffected(t *testing.T)
```

The refusal test must be one that would hang or recurse without the guard — a gate that re-invokes the ladder. Bound it with a short `WithGateTimeout` so a regression fails fast instead of wedging CI.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/verify/ -run 'TestNested|TestGateEnvironment'`
Expected: FAIL — no marker exists.

- [ ] **Step 3: Implement**

Set the marker on each spawned gate's environment. This is the first `cmd.Env` manipulation in the codebase: gates currently inherit the parent environment implicitly, so you must construct the child environment explicitly and preserve everything else — dropping the inherited environment would break every toolchain gate.

On entry, `verify.Run` refuses with an error naming the outer run rather than recursing. Refuse; do not silently skip — a skipped gate reporting pass is the failure mode this whole milestone guards against.

Keep the existing `t.Chdir`-based regression test in `cmd/pika` as defence in depth, and update `AGENTS.md` to say the rule is now enforced rather than conventional.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/verify/ ./cmd/pika/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/verify/ AGENTS.md
git commit -m "feat(verify): refuse a nested ladder instead of recursing"
```

---

### Task 11: A format gate that can fail

**Files:**
- Modify: `internal/verify/verify.go`, `internal/profiles/registry.go`, `internal/profiles/packs/go@1.yaml`, `internal/profiles/packs/python@1.yaml`
- Modify: `.github/workflows/ci.yml`, `internal/initcmd/testdata/golden/**`
- Test: `internal/verify/verify_test.go`, `internal/profiles/registry_test.go`

**Interfaces:**
- Produces: a `fail-on-output` flag on a pack check, honored by `runGate`.

`runGate` decides pass/fail purely on exit status, so `gofmt -l -w .` — which rewrites files and exits 0 — is structurally incapable of failing while mutating the artifact under verification. `ruff format .` has the same shape. `rust@1` (`cargo fmt -- --check`) and `swift@1` (`swift format lint`) already do it correctly, proving the defect is pack-local.

Contract commands are exec'd with no shell (`strings.Fields`), so `test -z "$(gofmt -l .)"` cannot be expressed.

- [ ] **Step 1: Write the failing test**

```go
func TestFailOnOutputFailsAGateThatPrintsAndExitsZero(t *testing.T)
func TestFailOnOutputPassesASilentGate(t *testing.T)
func TestFailOnOutputUnsetIgnoresOutput(t *testing.T)
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/verify/ -run TestFailOnOutput`
Expected: FAIL — the flag does not exist.

- [ ] **Step 3: Implement**

Add the flag through the pack schema, `profiles.Check`, and `verify.Gate`, and honor it in `runGate` after the exit-status branch. Validate it the way `autofill` is validated — a flag on a slot that cannot use it is a pack error, not silent.

Change `go@1`'s format hint to `gofmt -l .` with the flag set. `python@1`'s becomes `ruff format --check .`, which exits nonzero natively and needs no flag.

Both packs rotate `PackDigest()`; regenerate the golden `profiles.lock` fixtures. Do not weaken a digest assertion.

Then remove pika's own `git diff --exit-code` CI step: it exists only to compensate for a format gate that could not fail, and leaving it would hide a regression in the real fix.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/verify/ ./internal/profiles/ ./internal/initcmd/`
Expected: PASS

- [ ] **Step 5: Verify against the real repository**

```bash
go build -o /tmp/pika-fmt ./cmd/pika
/tmp/pika-fmt check --all
```
Expected: pass, with the format gate genuinely running. Then introduce a deliberate formatting error in a scratch file, re-run, confirm the gate FAILS, and revert.

- [ ] **Step 6: Commit**

```bash
git add internal/verify/ internal/profiles/ internal/initcmd/ .github/
git commit -m "feat(verify): fail a gate whose output means failure"
```

---

### Task 12: Pack, template and doctor corrections

**Files:**
- Modify: `internal/profiles/packs/core@1/templates/ci.yml.tmpl`, `internal/profiles/packs/python@1.yaml`
- Modify: `internal/doctor/doctor.go`
- Test: `internal/initcmd/init_test.go`, `internal/doctor/doctor_test.go`
- Modify: `internal/initcmd/testdata/golden/**`

- [ ] **Step 1: Write the failing test**

```go
func TestScaffoldedCIBuildsPikaFromTheCommitUnderTest(t *testing.T)
func TestScaffoldedCIHasNoPathsFilter(t *testing.T)
func TestPythonTestCommandRunsOnDebian(t *testing.T)
func TestDoctorWarnsWhenTheEnvelopeWouldDenyAGate(t *testing.T)
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/initcmd/ -run TestScaffolded; go test ./internal/doctor/ -run TestDoctorWarns`
Expected: FAIL

- [ ] **Step 3: Implement**

Converge `ci.yml.tmpl` with the workflow pika hand-fixed for itself: drop the `paths:` filter, add `fetch-depth: 0` and a `permissions:` block, and build the kernel from the commit under test instead of `go install …@latest`. An adopting repo currently receives CI that verifies a published release rather than its own change.

`python@1`'s `test` is an unconditional `cmd`, not a hint, so PATH probing at init time cannot avoid it — change the command itself so it runs where only `python3` exists.

`doctor` already loads the envelope and resolves gate argv in two functions that never talk. Cross-check them: when a resolved gate command is not authorized by the present envelope's `exec` grants, emit a finding. This is the one diagnostic that would predict an `envelope_denied` from `run_checks` before an agent hits it. Absent envelope stays a `warn` as today — a human needs no envelope.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/initcmd/ ./internal/doctor/ ./internal/profiles/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/profiles/ internal/initcmd/ internal/doctor/
git commit -m "fix: correct the scaffolded CI template, python test command, and doctor's envelope cross-check"
```

---

### Task 13: End-to-end and full verification

**Files:**
- Modify: `internal/e2e/`
- Modify: `README.md`, `docs/guides/usage.md`, `docs/reference/m1-5-delta.md` (add an M2 delta or supersede)

- [ ] **Step 1: E2E coverage through the real binary**

Add coverage for `work`, `status`, `resume` and `recover`. Use the faked runner boundary — E2E must not require a Codex binary. Assert that an interrupted run is visible in `status` and resumable, and that a delivered run leaves a schema-valid receipt under `.project/evidence/`.

- [ ] **Step 2: Documentation**

Command table and usage sections for `work`, `status`, `resume`, `recover`. Document that run records are local-only under `.project/state/work/` while receipts are committed, and record the M2 delta: the kernel now issues receipts, nested ladders are refused structurally, and the format gate can fail.

- [ ] **Step 3: Full suite**

Run: `go test ./... -count=1`
Expected: PASS.

- [ ] **Step 4: CGO-free build and dependency floor**

```bash
CGO_ENABLED=0 go build ./...
go mod tidy && git diff --exit-code go.mod go.sum
```
Expected: success and no diff. If `go.mod` gained a direct dependency, remove it — the constraint is two.

- [ ] **Step 5: pika governs pika**

```bash
go build -o /tmp/pika-m2 ./cmd/pika
/tmp/pika-m2 check --all
/tmp/pika-m2 doctor
```
Expected: `check` passes with the format gate really running; `doctor` clean.

- [ ] **Step 6: Smoke the lifecycle end to end**

In a scratch repository, run `work` with a faked agent through to delivery, then `status` it, then confirm the receipt validates. Paste the receipt.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "test: end-to-end coverage for the durable work lifecycle"
```

---

## Self-Review

**Spec coverage.** §5.1→Task 2; §5.2→Task 1; §5.3→Tasks 3,4; §5.4→Tasks 6,7; §5.5→Task 5; §5.6→Task 9; §6.1→Task 10; §6.2→Task 11; §6.3→Task 12; §6.4→Task 12; §8 testing→distributed; §9 completion→Task 13.

**Type consistency.** `workrec.Record`, `Handle.Save`, `Handle.Dir` are defined in Task 2 and used unchanged in 3-8. `improve.Run`'s `Config` gains `Kind` and `Goal` in Task 4 and is consumed by Tasks 7 and 8. `evidence.NewWorkID` keeps its signature through Task 1. `fail-on-output` is added to the pack schema, `profiles.Check` and `verify.Gate` together in Task 11.

**Ordering.** Tasks 1 and 2 are independent and come first. Task 3 needs 2. Task 4 needs 2 and 3. Task 5 needs 4. Tasks 6, 7, 8 need 4. Tasks 9, 10, 11, 12 are independent of the lifecycle and of each other, except that 11 and 12 both touch `python@1` and the goldens — sequence them, do not run them concurrently. Task 13 is last.

**Known cost, accepted.** Task 11 and Task 12 each rotate `PackDigest()`. M1.5 deferred the template fix specifically to avoid a second rotation in one milestone; paying both here, together, is the reason they are adjacent. Golden `profiles.lock` fixtures regenerate once per rotating task.
