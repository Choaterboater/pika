# pika M3 — Trust and the Upgrade Path Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make what pika already does trustworthy — a `--force` that does not destroy the operator's work, an upgrade signal for repositories scaffolded from since-corrected templates, a private-state filter that survives quoted paths, and redaction where local state is written.

**Architecture:** No new commands and no new machinery. `initcmd` learns to read the contract back; `profiles` folds templates into its digest so `CheckLock` can signal staleness and `apply` can refresh kernel-owned files; three porcelain parsers move to `-z`; `workrec` and the MCP board redact at write time; `fs_read` gains the one enforcement call site whose operation class actually occurs.

**Tech Stack:** Go 1.26, stdlib only. Two direct dependencies, unchanged.

**Spec:** [docs/superpowers/specs/2026-08-30-pika-m3-trust-and-upgrade-design.md](../specs/2026-08-30-pika-m3-trust-and-upgrade-design.md)

## Global Constraints

- `go.mod` MUST still declare exactly two direct dependencies.
- `CGO_ENABLED=0 go build ./...` MUST succeed. macOS, Linux and Windows are supported targets.
- No code path may call a model or make a network request. pika imports no networking package today and MUST NOT gain one.
- Exit codes: `0` success, `1` failure, `2` usage or configuration error.
- Every command supports `--json` through the `internal/cliout` envelope; `cmd/pika/main_test.go`'s registry guard fails any `--json` command without a `jsonCases` entry — satisfy it by adding a case, never by weakening it.
- Standard library `testing` only.
- pika governs itself: `pika check --all` must pass and CI runs `pika check --ci`.
- Commit after every task. Conventional commits.
- Run only the tests named in each task. The full suite runs once, in Task 8.

---

### Task 1: `--force` reads the contract back

**Files:**
- Modify: `internal/initcmd/init.go`
- Test: `internal/initcmd/init_test.go`

**Interfaces:**
- Consumes: `contract.Load`, `checks.ProfileRefs`, `discover` (for `go.mod`).
- Produces: `InitOptions` gains `ResetDocs bool`; `--force` resolves profiles, name and module from the existing repository when the corresponding flag is absent.

`pika init --force` currently rebuilds the contract from the profiles named on the command line and never reads the existing one. A bare `--force` in a Go repository therefore produces a core-only contract with `commands: {}`. It also resets `.project/exceptions.yaml` to `{}`, destroying records that each carry a rationale, an owner and a review condition written by a human. And this command is the documented remedy for a rotated digest.

- [ ] **Step 1: Write the failing tests**

```go
func TestForceReadsProfilesBackFromTheContract(t *testing.T)
func TestForceReadsProjectNameBackFromTheContract(t *testing.T)
func TestExplicitFlagsWinOverReadBack(t *testing.T)
func TestForcePreservesRecordedExceptions(t *testing.T)
func TestForceRefusesWhenNoModuleCanBeRecovered(t *testing.T)
```

`TestForcePreservesRecordedExceptions` is the important one: seed `.project/exceptions.yaml` with a real record (path key, `rule-id`, `reason`, `owner`, `review-condition`), run `--force`, assert the record is still there byte-for-byte.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/initcmd/ -run 'TestForce|TestExplicitFlags'`
Expected: FAIL — profiles come from flags, exceptions are reset.

- [ ] **Step 3: Implement**

When `--force` is set and a contract exists, resolve each value as: explicit flag, else read-back, else refuse.

- profiles: `contract.Load` then `checks.ProfileRefs`.
- name: `contract.Load`'s `project.name`.
- module: the contract has NO module field — it can only come from `go.mod` via `discover`. When neither a flag nor a `go.mod` supplies one, REFUSE. Do not fall back to the directory name; that is what writes a stray `cmd/<dirname>/main.go` today.

Never reset `.project/exceptions.yaml`.

An unparseable existing contract refuses rather than silently falling back to flags — a corrupt contract is a fact to report, and quietly rebuilding from flags is how an operator loses a contract they could have fixed.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/initcmd/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/initcmd/
git commit -m "fix(init): --force regenerates from the repository, not from flags alone"
```

---

### Task 2: `--force` stops rewriting operator-owned files

**Files:**
- Modify: `internal/initcmd/init.go`, `cmd/pika/init.go`
- Test: `internal/initcmd/init_test.go`, `cmd/pika/init_test.go`
- Modify: `internal/initcmd/testdata/golden/**` if the emitted set changes

**Interfaces:**
- Produces: `--reset-docs` flag; `--force` regenerates only kernel-owned files.

- [ ] **Step 1: Write the failing tests**

```go
func TestForcePreservesOperatorOwnedFiles(t *testing.T)  // README, AGENTS.md, CONTRIBUTING edited then --force
func TestResetDocsRestoresTemplates(t *testing.T)
func TestForceStillRegeneratesKernelOwnedFiles(t *testing.T) // lock, PR template, CI workflow
```

The first must edit all three files with recognisable content and assert it survives.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/initcmd/ -run 'TestForcePreservesOperator|TestResetDocs'`
Expected: FAIL — `--force` rewrites all three today.

- [ ] **Step 3: Implement**

Split the managed-file set into kernel-owned (`.project/profiles.lock`, `.github/pull_request_template.md`, `.github/workflows/ci.yml`) and operator-owned (`README.md`, `AGENTS.md`, `CONTRIBUTING.md`, the language scaffold).

`--force` regenerates kernel-owned only. `--reset-docs` additionally restores operator-owned, so the old behavior stays reachable for someone who wants the templates back — nothing becomes impossible, only non-default.

First-time `init` (no existing contract) is unchanged: it writes everything, because there is nothing to preserve.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/initcmd/ ./cmd/pika/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/initcmd/ cmd/pika/
git commit -m "fix(init): --force leaves operator-owned files alone unless --reset-docs"
```

---

### Task 3: Templates inside the pack digest

**Files:**
- Modify: `internal/profiles/registry.go`
- Test: `internal/profiles/registry_test.go`
- Modify: `internal/initcmd/testdata/golden/**` (five `profiles.lock`), `.project/profiles.lock`

**Interfaces:**
- Produces: `PackDigest()` and `PackDigestFor()` hash pack YAML **and** the pack's templates, in a stable order.

`core@1.yaml` declares `templates: []`, so not even template filenames are hashed today; the templates' separate `go:embed` is entirely uncovered. A corrected template therefore rotates nothing, and an adopted repository has no way to learn its scaffolded CI is stale.

- [ ] **Step 1: Write the failing test**

```go
func TestEditingATemplateRotatesThePackDigest(t *testing.T)
func TestTemplateHashingIsOrderStable(t *testing.T)
```

The first cannot literally edit an embedded file at test time — hash the template FS through the same helper the digest uses and assert the helper's output changes when its input does, and that `PackDigestFor` incorporates it. State in the test comment why it is written that way.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/profiles/ -run TestEditingATemplate`
Expected: FAIL — templates are not hashed.

- [ ] **Step 3: Implement**

Walk the template FS in sorted path order, hashing each path and its bytes with a separator, exactly as `PackDigest` already does for pack refs. Sorted order matters: `fs.WalkDir` is deterministic but the guarantee must be explicit, or a future refactor to a map silently makes digests unstable.

- [ ] **Step 4: Regenerate the locks**

Five golden `profiles.lock` files plus this repository's own. Do NOT weaken a digest assertion.

- [ ] **Step 5: Run to verify they pass**

Run: `go test ./internal/profiles/ ./internal/initcmd/ ./internal/checks/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/profiles/ internal/initcmd/ .project/
git commit -m "feat(profiles): fold pack templates into the pack digest"
```

---

### Task 4: `apply` refreshes kernel-owned files

**Files:**
- Modify: `internal/apply/apply.go`
- Test: `internal/apply/apply_test.go`

`apply` renders a core file only when it is missing (`Lstat` skip in `buildPlan`/`promote`), so it never refreshes one the kernel has since corrected.

- [ ] **Step 1: Write the failing tests**

```go
func TestApplyRefreshesAStaleKernelOwnedFile(t *testing.T)
func TestApplyNeverTouchesAnOperatorOwnedFile(t *testing.T)
func TestApplyReportsEveryRefresh(t *testing.T)
```

The second is the guard: seed an edited `README.md` and assert `apply` leaves it exactly as found.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/apply/ -run TestApplyRefreshes`
Expected: FAIL — existing files are skipped.

- [ ] **Step 3: Implement**

For kernel-owned files only (PR template, CI workflow), compare on-disk content against the rendered template and rewrite when they differ. Operator-owned files keep create-if-missing exactly as today.

Every refresh appears in the apply report. A silent kernel rewrite is indistinguishable from an operator's own edit, which is how trust in the tool erodes.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/apply/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/apply/
git commit -m "feat(apply): refresh kernel-owned files and report every rewrite"
```

---

### Task 5: `-z` porcelain parsing across all three call sites

**Files:**
- Modify: `internal/improve/improve.go`, `internal/changed/changed.go`, `internal/improve/receipt.go`
- Test: their test files

Git C-quotes any path with non-ASCII, whitespace or control bytes, so the parser receives `".project/state/w\303\251ird.json"` with a leading `"`. `isPrivateState` is a plain `HasPrefix(path, ".project/state/")`, which is false for that literal — so `privateStateMoved` does not refuse and `changePaths` does not drop. **Both guards fail open.**

- [ ] **Step 1: Write the failing test**

```go
func TestPrivateStateWithANonASCIINameIsRefused(t *testing.T)
```

It MUST use a genuinely quoted path — create a file with a non-ASCII name so git actually quotes it. A test using a plain ASCII path proves nothing about this hole.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/improve/ -run TestPrivateStateWithANonASCII`
Expected: FAIL — the quoted path passes the prefix test and is committed.

- [ ] **Step 3: Implement**

Move all three parsers to `-z`. Two things about `-z` are not cosmetic:

1. Paths are emitted verbatim, never quoted — that is the fix.
2. **Rename encoding changes and the field order REVERSES.** v1 `-z` omits the `->` and emits `XY <to>\0<from>\0`, so the origin arrives as a separate trailing field AFTER the destination. That inverts the origin-first assumption baked into `statusEntries`' struct, `changePaths`' `{origin, path}` ordering, and the literal-porcelain unit tests. Update them together, and feed the tests real NUL-delimited fixtures rather than arrow-joined strings.

A malformed entry must REFUSE the run, not be skipped. A skipped entry is exactly how the current hole leaks.

All three call sites move. Leaving one behind preserves the hole in a different command.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/improve/ ./internal/changed/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/improve/ internal/changed/
git commit -m "fix: parse git porcelain with -z so quoted paths cannot evade the filter"
```

---

### Task 6: Redact local state at the point of writing

**Files:**
- Modify: `internal/workrec/record.go`, `internal/mcp/server.go` (board appends)
- Test: `internal/workrec/workrec_test.go`, `internal/mcp/server_test.go`

`record.json` is written unredacted and carries the operator's goal plus full baseline and recheck reports including every gate's `OutputTail`. The board is written unredacted from agent-supplied strings.

- [ ] **Step 1: Write the failing tests**

```go
func TestRecordRedactsTheGoal(t *testing.T)
func TestRecordRedactsGateOutput(t *testing.T)
func TestBoardAppendsAreRedacted(t *testing.T)
```

Seed credential-shaped text (`sk-`, `ghp_`, a PEM header) and assert it is absent from the bytes on disk.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/workrec/ ./internal/mcp/ -run 'TestRecordRedacts|TestBoardAppends'`
Expected: FAIL — no `redact` import exists in either.

- [ ] **Step 3: Implement**

Apply `redact.Apply` to the goal and to each gate's `OutputTail` before writing, the same treatment `evidence.Build` already gives every string it emits. Same for the board's appended strings.

This is defence in depth on purpose: Task 5 closes the filter hole that exists today, and this reduces what a future filter bug could leak. A guarantee resting on one correct prefix test is one bug from a disclosure, and that test was already wrong once.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/workrec/ ./internal/mcp/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/workrec/ internal/mcp/
git commit -m "fix: redact the run record and board at the point of writing"
```

---

### Task 7: Enforce `fs_read`; document what cannot occur

**Files:**
- Modify: `internal/mcp/server.go`
- Test: `internal/mcp/server_test.go`
- Modify: `docs/reference/m3-delta.md` (create)

- [ ] **Step 1: Write the failing test**

```go
func TestReadPathsAreAuthorized(t *testing.T)
func TestHumanCLIStillNeedsNoEnvelope(t *testing.T)
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/mcp/ -run TestReadPathsAreAuthorized`
Expected: FAIL — `fs_read` has no call site.

- [ ] **Step 3: Implement**

Add the `fs_read` check to the MCP paths that read on an agent's behalf. `allowsRead` already implements a repo-inside default and `contract.NormalizeRepoPath` already bounds targets, so this is a call site, not a mechanism.

Do NOT add checks for `network`, `credential`, `github` or `budget`. The binary contains no operation of any of those classes — no networking package is imported at all — so a check would be dead code guarding an operation that never happens, and it would make the envelope look more protective than it is.

- [ ] **Step 4: Write the delta**

Create `docs/reference/m3-delta.md` stating, per unenforced kind, that the operation class does not occur in the kernel, and naming where the real network boundary lives: the Codex child process, sandboxed through argv (`--sandbox workspace-write`, `network_access=false`), and generated CI's module fetches. Record the copy-leak as a known limit with the reasoning from spec §7.

- [ ] **Step 5: Run to verify they pass**

Run: `go test ./internal/mcp/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/mcp/ docs/
git commit -m "feat(mcp): authorize fs_read; document the kinds that cannot occur"
```

---

### Task 8: End-to-end and full verification

**Files:**
- Modify: `internal/e2e/`, `README.md`, `docs/guides/usage.md`

- [ ] **Step 1: E2E**

`--force` on a repository with a hand-written `README.md` and a recorded exception preserves both. A repo whose lock predates a template correction fails `check` with a message naming the pack, and `apply` refreshes it.

- [ ] **Step 2: Documentation**

Rewrite the upgrading section: `--force` is now safe, `--reset-docs` is the destructive opt-in. Re-read the guide against current behavior rather than appending.

- [ ] **Step 3: Full suite**

Run: `go test ./... -count=1`

- [ ] **Step 4: Build and dependency floor**

```bash
CGO_ENABLED=0 go build ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

- [ ] **Step 5: pika governs pika**

```bash
go build -o /tmp/pika-m3 ./cmd/pika
/tmp/pika-m3 check --all
/tmp/pika-m3 doctor
```

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "test: end-to-end coverage for the safe upgrade path"
```

## Self-Review

**Spec coverage.** §5.1→Tasks 1,2; §5.2→Tasks 3,4; §5.3→Task 5; §5.4→Task 6; §5.5→Task 7; §7→Task 7 Step 4; §8→distributed; §9→Task 8.

**Ordering.** Tasks 1 and 2 are sequential (both `initcmd`). Task 3 precedes Task 4 (apply needs the digest to know staleness). Task 5 is independent. Task 6 is independent. Task 7 is independent. Task 8 last.

**Known cost.** Task 3 rotates every lock a third time. That is why it is paired with Tasks 1-2: an operator told to run `--force` must first be able to run it without losing their work.
