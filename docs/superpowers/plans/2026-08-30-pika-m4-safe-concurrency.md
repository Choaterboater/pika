# pika M4 — Safe Concurrency, Honestly Described Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a second concurrent mutating run refuse instead of corrupt, and stop telling agents they hold a lease that does not exist.

**Architecture:** Lift `internal/txn`'s proven `O_EXCL` lock into a shared package so the run lease and the transaction lock cannot drift apart. The lifecycle takes that lease before touching the working tree; release verifies ownership; `acquire_scope`/`release_scope` are backed by it so their advertised semantics become true.

**Tech Stack:** Go 1.26, stdlib only. Two direct dependencies, unchanged — explicitly no SQLite (spec §3).

**Spec:** [docs/superpowers/specs/2026-08-30-pika-m4-safe-concurrency-design.md](../specs/2026-08-30-pika-m4-safe-concurrency-design.md)

## Global Constraints

- `go.mod` MUST still declare exactly two direct dependencies. **No SQLite driver** — spec §3 records why.
- `CGO_ENABLED=0 go build ./...` MUST succeed. macOS, Linux and Windows are supported targets.
- No code path may call a model or make a network request.
- Exit codes: `0` success, `1` failure, `2` usage or configuration error.
- Every command supports `--json` through `internal/cliout`; `cmd/pika/main_test.go`'s registry guard fails any `--json` command without a `jsonCases` entry, and a second guard fails any message naming an unregistered command. Satisfy both by adding real cases.
- Standard library `testing` only.
- pika governs itself: `pika check --all` must pass; CI runs `pika check --ci`.
- **Shared worktree:** commit with an explicit pathspec — `git commit -m "..." -- <your paths>`. Never `git add -A`, never `git stash`.
- Run only the tests named in each task. The full suite runs once, in Task 6.

---

### Task 1: Lift the lock into a shared package

**Files:**
- Create: `internal/lease/lease.go`, `internal/lease/lease_test.go`
- Modify: `internal/txn/journal.go` to consume it
- Test: `internal/txn/apply_test.go`

**Interfaces:**
- Produces: `lease.Acquire(dir, name string, info Info) (*Handle, error)`, `(*Handle).Release() error`,
  `lease.Inspect(dir, name string) (*Info, State, error)`, `Info{ID, PID int, StartedAt time.Time, Host string}`,
  and states `StateFree`, `StateHeld`, `StateStale`, `StateUnverifiable`.

`internal/txn`'s `O_EXCL` lock is the only real mutual exclusion in the binary and it works. M4 needs a second exclusion for the run lifecycle. Two implementations of the same primitive would drift — that is the ownership-drift bug class M3 already fixed twice.

`lockInfo` today stores `TxID`, `PID`, `StartedAt` and **no hostname**, so a lock cannot be validated on a shared or network filesystem: PID liveness on a different machine proves nothing.

- [ ] **Step 1: Write the failing tests**

```go
func TestAcquireExcludesASecondHolder(t *testing.T)
func TestReleaseByNonHolderRefuses(t *testing.T)
func TestDeadHolderOnThisHostIsStale(t *testing.T)
func TestForeignHostIsUnverifiableNotStale(t *testing.T)
func TestAcquireIsAtomicUnderConcurrency(t *testing.T)  // N goroutines, exactly one wins
```

`TestForeignHostIsUnverifiableNotStale` is the one that matters most: reporting a foreign lock as stale invites an operator to clear a lock a live process on another machine still holds.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/lease/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

Port the proven mechanism from `internal/txn/journal.go`: `os.OpenFile(O_CREATE|O_EXCL|O_WRONLY)`, write the holder JSON, fsync the file and the directory, and remove the lock if the write fails.

Add `Host` from `os.Hostname()`. State resolution: free when absent; held when the recorded host matches and the PID is alive; stale when the host matches and the PID is dead; **unverifiable when the host differs**.

Never steal automatically, on any path. That property is why the existing lock has never corrupted anything.

- [ ] **Step 4: Rewire `internal/txn` onto it**

`txn` keeps its exact current behavior — same lock path, same refusal, same `ErrLeaseRequired`. Its existing tests must pass UNCHANGED; if one needs editing, stop and report, because that means behavior moved.

- [ ] **Step 5: Run to verify they pass**

Run: `go test ./internal/lease/ ./internal/txn/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(lease): extract the O_EXCL holder lock into one shared implementation" -- internal/lease/ internal/txn/
```

---

### Task 2: Release requires ownership

**Files:**
- Modify: `internal/lease/lease.go`, `internal/txn/journal.go`
- Test: `internal/lease/lease_test.go`, `internal/txn/apply_test.go`

`releaseLock` is an unconditional `os.Remove` with no ownership check, so any process able to write the directory can release another's lock — silently converting a correct exclusion into none at all.

- [ ] **Step 1: Write the failing test**

```go
func TestReleaseVerifiesTheRecordedHolder(t *testing.T)
```

Acquire as one identity, attempt release as another, assert refusal AND that the lock file survives. Assert the file, not just the error.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/lease/ -run TestReleaseVerifies`
Expected: FAIL — release removes unconditionally.

- [ ] **Step 3: Implement**

Re-read the lock and compare the recorded id against the holder's before removing. A mismatch refuses and leaves the file intact.

Note `pika recover` legitimately removes a lock it does not hold. That path must stay open — it is the documented remedy for a crashed holder — so it uses an explicit force path with its own name, not the ordinary release.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/lease/ ./internal/txn/ ./cmd/pika/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "fix(lease): refuse a release from a process that does not hold the lock" -- internal/lease/ internal/txn/
```

---

### Task 3: The lifecycle takes the lease

**Files:**
- Modify: `internal/improve/improve.go`
- Test: `internal/improve/improve_test.go`

**This is the milestone.** Two concurrent `pika work` runs in one repository are unprotected today. With the default branch they collide non-deterministically at `git switch -c`. With distinct `--branch` flags they **proceed** and corrupt each other's commits through the shared working tree and shared HEAD — the second case is worse, because nothing stops it.

- [ ] **Step 1: Write the failing tests**

```go
func TestSecondConcurrentRunIsRefused(t *testing.T)
func TestSecondConcurrentRunWithADifferentBranchIsAlsoRefused(t *testing.T)
func TestRefusalNamesTheHolder(t *testing.T)
func TestTheFirstRunIsUnaffectedByTheRefusal(t *testing.T)
```

The second test is the important one — it is the case that silently corrupts today. Both must be REAL concurrency (the first run genuinely in progress, e.g. blocked in a fake runner), not a simulated lock file, or they prove nothing about the actual race.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/improve/ -run TestSecondConcurrent`
Expected: FAIL — the second run proceeds.

- [ ] **Step 3: Implement**

Acquire the lease after the dirty-tree gate and before the working tree is touched, holding it until the run reaches a terminal outcome. Record the run id in the lease so a refusal can name it.

A held lease REFUSES with the holder's run id, pid, start time and host. Never wait, never steal. A command that blocks silently is indistinguishable from one that hung.

A refusal must leave no run record — same property the dirty-tree refusal already has, and there is an existing test shape to follow.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/improve/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(improve): hold an exclusive run lease so a second run refuses instead of corrupting" -- internal/improve/
```

---

### Task 4: Make `acquire_scope` mean what it says

**Files:**
- Modify: `internal/mcp/server.go`
- Test: `internal/mcp/server_test.go`, `internal/e2e/e2e_init_test.go`

`acquire_scope` is described to agents as an **"exclusive write lease"**. It performs an envelope check, appends one board line, and returns `granted:true`. Two sequential acquires of the same path both succeed. `release_scope` never checks an acquire happened.

An agent reading `tools/list` is entitled to believe the description. This is the same class of untruth M3 spent a milestone removing.

- [ ] **Step 1: Write the failing tests**

```go
func TestSecondAcquireOfAConflictingScopeIsRefused(t *testing.T)
func TestReleaseWithoutAcquireIsRefused(t *testing.T)
func TestAcquireThenReleaseThenAcquireSucceeds(t *testing.T)
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/mcp/ -run 'TestSecondAcquire|TestReleaseWithout'`
Expected: FAIL — both currently succeed unconditionally.

- [ ] **Step 3: Implement**

Back both tools with the real lease. A conflicting acquire refuses with a distinct, documented code — not `granted:true`, and not a reuse of `envelope_denied`, which means something else. Add the code to the closed set and to BOTH duplicated assertion lists (`internal/mcp/server_test.go` and `internal/e2e/e2e_init_test.go`) in lockstep.

Keep the board append: it is the human-readable audit trail. It is no longer the mechanism.

Update the tool descriptions to state the real semantics, and add the new code to `pika explain`'s error-code table so an agent can look it up.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/mcp/ ./internal/explain/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(mcp): back acquire_scope with a real lease so its description is true" -- internal/mcp/ internal/explain/ internal/e2e/
```

---

### Task 5: Close the option-injection surface

**Files:**
- Modify: `internal/improve/improve.go`, `internal/improve/receipt.go`
- Test: their test files

Several git invocations pass a `<branch>` or `<commit>` argument with no `--` separator, so a value beginning with `-` is read as an option. No such value is agent-controlled today; this closes the door before someone walks through it.

- [ ] **Step 1: Write the failing test**

A branch name beginning with `-` is treated as a value, not an option.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/improve/ -run TestLeadingDash`
Expected: FAIL

- [ ] **Step 3: Implement**

Add `--` before the branch or commit argument at each site. Audit the whole package rather than fixing only the two known ones, and list every site you checked in your report.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/improve/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "fix(improve): separate git options from branch and commit values" -- internal/improve/
```

---

### Task 6: End-to-end, recovery, and full verification

**Files:**
- Modify: `internal/e2e/`, `cmd/pika/recover.go`, `README.md`, `docs/guides/usage.md`, `docs/reference/m4-delta.md` (create)

- [ ] **Step 1: `pika recover` covers the run lease**

It already handles the transaction lock. Extend it to the run lease, using the explicit force path from Task 2 rather than the ownership-checked release. A crashed run must not wedge the repository — that is exactly the failure `recover` exists for, and M2 added it because `txn.Recover` had no caller at all.

- [ ] **Step 2: E2E through the real binary**

Two concurrent `pika work` invocations: the second refuses naming the holder, the first completes normally. Then `pika recover` releases a lease whose holder was killed.

- [ ] **Step 3: Documentation**

Document the refusal and what it means, that leases are whole-repository and why, and that `recover` is the remedy for a crashed run. Write `docs/reference/m4-delta.md` recording what changed — including, plainly, that the SQLite board and multi-agent machinery were NOT built and the evidence for that decision, so the next person reads a reasoned choice rather than an omission.

- [ ] **Step 4: Full suite**

Run: `go test ./... -count=1`

- [ ] **Step 5: Build and dependency floor**

```bash
CGO_ENABLED=0 go build ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

- [ ] **Step 6: pika governs pika**

```bash
go build -o /tmp/pika-m4 ./cmd/pika
/tmp/pika-m4 check --all
/tmp/pika-m4 doctor
```

- [ ] **Step 7: Commit**

```bash
git commit -m "test: prove a second concurrent run refuses, and recover releases a crashed lease" -- internal/e2e/ cmd/pika/ README.md docs/
```

## Self-Review

**Spec coverage.** §5.1→Tasks 1,3; §5.2→Task 2; §5.3→Task 4; §5.4→Task 5; §6→distributed; §7→Tasks 3,4,6; §8→Task 6.

**Ordering.** Task 1 first — everything else consumes the shared lease. Task 2 extends it. Task 3 and Task 4 both consume it and are independent of each other. Task 5 is independent of all. Task 6 last.

**Type consistency.** `lease.Acquire/Release/Inspect`, `Info{ID, PID, StartedAt, Host}` and the four states are defined in Task 1 and consumed unchanged in Tasks 2, 3, 4 and 6.

**Known risk.** Task 1 rewires `internal/txn`, the only exclusion that has never failed. Its existing tests must pass unchanged; an edit to one means behavior moved and is a stop-and-report condition.
