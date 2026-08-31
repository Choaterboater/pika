# pika M6 — Runtime Adapters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make all seven runtimes the contract schema accepts actually runnable, and let one run spawn a `builder`, an optional `explorer` before it, and an optional `reviewer` after the recheck — each under its own runtime.

**Architecture:** A new `internal/adapters` package holds a declarative adapter table (binary, argv builder, transport, output mode, supported controls, permission posture, env allowlist). `internal/improve` stops knowing about codex: `CodexRunner` is deleted, `Runner` gains `Runtime()`, and the lifecycle resolves up to three roles from contract keys. The compatibility probe moves to the new package and generalizes over every adapter. `pika doctor` gains an Agents section. No new command, no new dependency, no model call.

**Tech Stack:** Go 1.26, stdlib only. Two direct dependencies, unchanged.

**Spec:** [docs/superpowers/specs/2026-08-31-pika-m6-runtime-adapters-design.md](../specs/2026-08-31-pika-m6-runtime-adapters-design.md)

## Global Constraints

- `go.mod` MUST still declare exactly two direct dependencies. `go mod tidy && git diff --exit-code go.mod go.sum` MUST stay clean.
- `CGO_ENABLED=0 go build ./...` MUST succeed. macOS, Linux and Windows are supported targets.
- **No code path may call a model or make a network request.** Every test that touches a harness uses a fake binary on `PATH`. `--help` probes are the only real-binary contact, and they are env-gated.
- No adapter may emit a permission-bypass flag. Asserted by `TestEveryAdapterArgvCarriesItsOwnPermissionPosture`.
- `internal/adapters` MUST NOT import `internal/improve`. `internal/improve` imports `internal/adapters`. Nothing else in the tree may import the runner internals.
- Exit codes: `0` success, `1` failure, `2` usage or configuration error.
- Every command supports `--json` through `internal/cliout`; `cmd/pika/main_test.go`'s registry guard fails any `--json` command without a `jsonCases` entry. M6 adds no command, so no new case is required — but `doctor --json` gains an `agents` field and its existing case must keep passing.
- Standard library `testing` only.
- pika governs itself: `pika check --all` must pass; CI runs `pika check --ci`.
- **Shared worktree:** commit with an explicit pathspec — `git commit -m "..." -- <your paths>`. Never `git add -A`, never `git stash`.
- Run only the tests named in each task. The full suite runs once, in Task 10.

---

### Task 1: Contract — an agent can name a command

**Files:**
- Modify: `internal/contract/contract.schema.json`, `internal/contract/schema.go`
- Test: `internal/contract/schema_test.go`

The schema's `agent` definition accepts `runtime`, `provider`, `model` and `effort` and nothing else. `custom` cannot express which executable to run, so it cannot run at all.

- [ ] **Step 1: Write the failing tests**

```go
func TestAgentAcceptsCommandArgsAndEnv(t *testing.T)
func TestCustomAgentWithoutACommandIsRefused(t *testing.T)
func TestAgentEnvRejectsAValueThatIsNotAName(t *testing.T)  // "TOKEN=abc" fails the pattern
func TestHarnessEnumMatchesTheSchema(t *testing.T)          // HarnessEnum() reads the embedded schema
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/contract/ -run 'TestAgentAccepts|TestCustomAgent|TestAgentEnv|TestHarnessEnum'`
Expected: FAIL — `additionalProperties: false` rejects `command`, and `HarnessEnum` is undefined.

- [ ] **Step 3: Implement**

Add to `definitions.agent`: `command` (string, minLength 1), `args` (array of string), `env` (array of string matching `^[A-Za-z_][A-Za-z0-9_]*$`), and the `if runtime == custom then required: [command]` conditional.

Add the three fields to `AgentConfig` with `omitempty` on all three.

Add `HarnessEnum() ([]string, error)`: unmarshal `schemaJSON`, walk to `definitions.harness.enum`, return it. It reads the schema rather than restating it, so `TestEveryHarnessInTheContractSchemaHasAnAdapter` (Task 3) cannot pass against a stale copy of the list.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/contract/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(contract): let an agent declare command, args and env" -- internal/contract/
```

---

### Task 2: The adapter table

**Files:**
- Create: `internal/adapters/adapters.go`
- Test: `internal/adapters/adapters_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestEveryHarnessInTheContractSchemaHasAnAdapter(t *testing.T)
func TestArgsPerAdapter(t *testing.T)                              // golden argv, with and without model/effort
func TestEveryAdapterArgvCarriesItsOwnPermissionPosture(t *testing.T)
func TestFlagsDropsTheStdinSentinelAndEveryValue(t *testing.T)
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/adapters/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

Declare `Spawn`, `OutputMode` (`OutputStdout`, `OutputFile`), `Transport` (`TransportProcess`, `TransportACP`), `Support{Model, Effort bool}`, `Adapter`, `Lookup(runtime string) (Adapter, bool)` and `(Adapter).Flags(Spawn) []string`.

Populate the table from spec §5.1 — codex, claude, omp, gemini, opencode, acp, custom. Every `Args` function takes model and effort from `Spawn` and emits the control only when the value is non-empty, so `--model` never appears with no value.

`Flags` calls `Args` and keeps tokens beginning with `-`, dropping the bare `-` sentinel and every value.

Add `Agent` (name, runtime, command, args, env, model, effort), `AgentFromContract(c *contract.Contract, name string) (Agent, error)` and `New(a Agent) (Runner, error)`.

Every fail-closed error in spec §6 is produced here, before any process is spawned.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/adapters/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(adapters): add the runtime adapter table" -- internal/adapters/ internal/contract/
```

---

### Task 3: `ProcessRunner`

**Files:**
- Create: `internal/adapters/process.go`, `internal/adapters/process_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestProcessRunnerTeesStdoutAndRedactsTheMessage(t *testing.T)
func TestMissingBinaryIsRefusedNamingRuntimeAndBinary(t *testing.T)
func TestEnvAllowlistPassesOnlyTheDeclaredNames(t *testing.T)   // fake binary dumps its environment
func TestAnUnsetDeclaredEnvVarIsRefused(t *testing.T)
func TestUnknownPlaceholderIsRefused(t *testing.T)
func TestOutputFileRequiresTheOutputPlaceholder(t *testing.T)
func TestCustomAdapterRequiresACommand(t *testing.T)
func TestEffortIsRefusedWhereTheRuntimeHasNoEffortControl(t *testing.T)
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/adapters/ -run 'TestProcessRunner|TestMissingBinary|TestEnvAllowlist|TestAnUnset|TestUnknownPlaceholder|TestOutputFile|TestCustomAdapter|TestEffortIsRefused'`
Expected: FAIL — symbols undefined.

- [ ] **Step 3: Implement**

Resolve binary (`Command` override else `Adapter.Binary`), `exec.LookPath` it. Build argv from the override template or `Adapter.Args(s)`, substituting exactly five placeholders and refusing any other. `cmd.Dir = root` when `CwdFlag` is nil. `cmd.Stdin` is the open prompt file unless the template consumed `{prompt}`. `cmd.Stdout = io.MultiWriter(os.Stdout, rawFile)`; `cmd.Stderr = os.Stderr`. Run, wrapping failure as `fmt.Errorf("%s handoff: %w", runtime, err)`.

Environment: `Env == nil` inherits; an allowlist passes those names plus `PATH`, `HOME`, `TMPDIR` when present, and errors on a declared name that is unset.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/adapters/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(adapters): run a one-shot harness and capture its final message" -- internal/adapters/
```

---

### Task 4: `ACPRunner`

**Files:**
- Create: `internal/adapters/acp.go`, `internal/adapters/acp_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestACPClientDrivesAFakeAgent(t *testing.T)
func TestACPSelectsAllowOnceAndNeverAllowAlways(t *testing.T)
func TestACPRefusesAProtocolVersionItDoesNotSpeak(t *testing.T)
func TestACPNamesAStopReasonThatIsNotEndTurn(t *testing.T)
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/adapters/ -run 'TestACP'`
Expected: FAIL — symbols undefined.

- [ ] **Step 3: Implement**

Stdlib `encoding/json` + `bufio` over the child's stdin/stdout. `initialize` with `"protocolVersion": 1` and `clientInfo{name: "pika", version: version.String()}`; a different major version closes the connection and errors naming both. `session/new` with `{cwd: root, mcpServers: []}`. `session/prompt` with one text content block carrying the prompt file's contents. Concatenate `session/update` notifications whose `sessionUpdate` is `agent_message_chunk`, in arrival order.

Answer `session/request_permission` with the first `allow_once` option, else `reject_once`, never `allow_always`; log each to stderr as `acp: <allow|reject> <tool title> (<kind>)`. Reply `{"outcome":{"outcome":"cancelled"}}` when ctx is done.

On the `session/prompt` response: write the accumulated text raw; error as `acp: agent stopped with reason %q` when `stopReason != "end_turn"`. Then close stdin and wait for exit.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/adapters/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(adapters): speak ACP v1 over stdio with stdlib json" -- internal/adapters/
```

---

### Task 5: Migrate `CodexRunner` out of `internal/improve`

**Files:**
- Delete: `internal/improve/codexcompat.go`, `internal/improve/codexcompat_test.go`
- Create: `internal/adapters/compat.go`, `internal/adapters/compat_test.go`
- Modify: `internal/improve/handoff.go`, `internal/improve/handoff_test.go`
- Modify: `cmd/pika/improve.go`, `cmd/pika/work.go`, `cmd/pika/resume.go`
- Rename: `.github/workflows/codex-compat.yml` → `adapter-compat.yml`

This is a clean cutover, not an alias. `improve.CodexRunner` disappears and nothing keeps a deprecated spelling of it.

- [ ] **Step 1: Move the runner**

`Runner` gains `Runtime() string` — the bundle a handoff writes names the agent that produced it. `createHandoff` derives the two message paths from the runner: `filepath.Join(bundleDir, runtime+"-last-message.raw")` and `…+"-last-message.md"`. For codex that is byte-identical to today's `codex-last-message.{raw,md}`, so `handoff_test.go` and `docs/guides/usage.md` need no rename. `createHandoff` also `os.MkdirAll(bundleDir, 0o755)` so role subdirectories work.

- [ ] **Step 2: Move the probe**

`CheckCompatibility(ctx, Adapter) ([]string, error)` replaces `CheckCodexCompatibility(ctx, binary string)`. Same clap-format regex, same "flags come from a real `Args` call" property, gated on `PIKA_ADAPTER_COMPAT` (no alias for `PIKA_CODEX_COMPAT`).

- [ ] **Step 3: Move the tests**

`TestCodexRunnerArgsUseConfiguredModelAndEffort` and `TestCodexRunnerNeverSendsSandboxAlongsideApproveForMe` become table-driven tests over every adapter's `Args`, keeping both assertions.

`TestRealCodexCompatibility` becomes `TestRealAdapterCompatibility`: iterate every adapter, skip those whose binary is absent, run `--help` only.

- [ ] **Step 4: Rewire the command layer**

Delete `const codexRuntime`. Replace `configuredCodexRunner` with a resolver returning `(adapters.Runner, error)`. Keep `configuredRunner` lazy so a bad agent is still only diagnosed when a handoff is actually needed. Update the `--agent` flag help in `runHandoff`, `runImprove`, `runWork` and `runResume` from "must use the Codex runtime" to "contract agent name". Drop `Runtime: codexRuntime` from the four `improve.Config` literals.

- [ ] **Step 5: Rename the workflow**

Update its header comment to cite `internal/adapters`, and the test invocation to `go test ./internal/adapters/ -count=1 -v -run TestRealAdapterCompatibility` with `PIKA_ADAPTER_COMPAT: "1"`.

- [ ] **Step 6: Run to verify**

Run: `go test ./internal/adapters/ ./internal/improve/ ./cmd/pika/ ./internal/e2e/`
Expected: PASS — including the existing argv and bundle-path assertions unchanged.

- [ ] **Step 7: Commit**

```bash
git commit -m "refactor(adapters): move the agent boundary out of improve" -- internal/adapters/ internal/improve/ cmd/pika/ .github/
```

---

### Task 6: Roles — one run, up to three agents

**Files:**
- Modify: `internal/improve/improve.go`, `internal/improve/handoff.go`, `internal/improve/receipt.go`
- Modify: `internal/workrec/record.go`
- Modify: `cmd/pika/improve.go`, `cmd/pika/work.go`, `cmd/pika/resume.go`
- Test: `internal/improve/improve_test.go`, `internal/improve/receipt_test.go`, `cmd/pika/status_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestRunWithoutABuilderIsRefused(t *testing.T)              // "improve: a builder runner is required"
func TestExplorerFindingsReachTheBuilderPrompt(t *testing.T)
func TestAnExplorerThatChangesTheTreeIsRefused(t *testing.T)
func TestReviewIsRecordedAndNeverGatesTheCommit(t *testing.T)
func TestADefaultContractStampsTheSameFourPhases(t *testing.T) // baseline, handoff, recheck, deliver
func TestReceiptNamesEveryAgentTheRunSpawned(t *testing.T)
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/improve/ -run 'TestRunWithoutABuilder|TestExplorer|TestAnExplorer|TestReviewIs|TestADefaultContract|TestReceiptNames'`
Expected: FAIL — `Config` has no `Builder` field.

- [ ] **Step 3: Implement the type change**

Add `Role{Name, Agent, Runner}` and `Config{…, Builder Role, Explorer *Role, Reviewer *Role}`. **Remove** `Config.Agent`, `Config.Runtime` and `Config.Runner` — no aliases: `Runtime` was always a duplicate of what the runner knows, and `Agent` is now `Builder.Name`/`Builder.Agent`.

Migrate every `improve.Config{Runner: …}` literal in `internal/improve/improve_test.go` (including the resume table and the fixtures) and in `cmd/pika/{work,improve,resume}.go`.

- [ ] **Step 4: Implement the phases**

Add `stageExplore` and `stageReview` to the `stage` enum in that order; add `workrec.PhaseExplore = "explore"` and `PhaseReview = "review"`; add `RunAgent{Role, Agent, Runtime}` and `Record.Agents`.

Extend `resumeStage`: `PhaseExplore` → `stageHandoff`; `PhaseReview` → `stageRecheck`; `PhaseBaseline` with a report → `stageExplore` when an explorer is configured, else `stageHandoff`.

In the lifecycle: after the baseline stamp, when `cfg.Explorer != nil` and `from <= stageExplore`, `createHandoff` into `filepath.Join(handle.HandoffDir(), "explore")` with `buildExplorePrompt`, stamp `PhaseExplore`, append the explorer to `rec.Agents`, and require a clean working tree afterwards. After `PhaseRecheck` passes, when `cfg.Reviewer != nil` and `from <= stageReview`, `createHandoff` into `filepath.Join(handle.HandoffDir(), "review")` with `buildReviewPrompt`, stamp `PhaseReview`, append the reviewer.

Stamp `rec.Runtime` and `rec.Role` from the builder at the build handoff — both stay, so a pre-M6 record still rejoins.

- [ ] **Step 5: Implement the prompt builders**

`buildExplorePrompt(goal string, failed []verify.GateResult) string` and `buildReviewPrompt(goal string, before, after *verify.Report, changed []string, builderMessage string) string`. Both reuse `buildPrompt`'s fixed rules preamble verbatim and omit empty sections rather than emitting them blank.

The builder's prompt gains a `## Explorer findings` section when an explore bundle exists: the explorer's redacted final message, tail-truncated to the last 8 KiB with a marker line naming the truncation.

- [ ] **Step 6: Implement the receipt**

`runRoles` emits one `evidence.RoleInput` per `rec.Agents` entry when the slice is non-empty, falling back to the singular `rec.Role`/`rec.Runtime` for resumed pre-M6 records. `buildReceipt` populates `Review: []evidence.ReviewInput{{Agent: <reviewer agent name>, Finding: <redacted review message>, Disposition: reviewAdvisoryDisposition}}` where the constant is `"advisory: recorded, not a gate"`. No schema change — `ReviewInput` already has exactly those three fields.

- [ ] **Step 7: Wire the command layer**

`--agent` (default `builder`) selects the builder. `explorer` and `reviewer` are resolved from the contract keys of those names: "not configured" is nil (skip), any other resolution error is fatal. No new flags.

- [ ] **Step 8: Run to verify they pass**

Run: `go test ./internal/improve/ ./internal/workrec/ ./cmd/pika/`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git commit -m "feat(improve): run an explorer before the builder and a reviewer after the recheck" -- internal/improve/ internal/workrec/ cmd/pika/
```

---

### Task 7: Doctor introspection

**Files:**
- Modify: `internal/doctor/doctor.go`, `cmd/pika/doctor.go`
- Test: `internal/doctor/doctor_test.go`, `cmd/pika/doctor_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestDoctorReportsEveryConfiguredAgent(t *testing.T)
func TestDoctorReportsAnAbsentBinaryAsNotOnPath(t *testing.T)
func TestDoctorReportsAnUnmappedControl(t *testing.T)   // effort on gemini
func TestDoctorSpawnsNoHarness(t *testing.T)
func TestDoctorJSONCarriesAgents(t *testing.T)
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/doctor/ ./cmd/pika/ -run 'TestDoctor'`
Expected: FAIL — no `agents` field exists.

- [ ] **Step 3: Implement**

`checkAgents(rep, root, c)`, called after `checkGates` and before `checkGit`. Emitted only when a contract exists and declares at least one agent. Per agent, sorted by name: contract key, runtime, resolved adapter, binary path or `not on PATH`, model/effort mapping as `mapped`/`unmapped`, output mode, `resume: yes|no`, and compat findings when `PIKA_ADAPTER_COMPAT=1`.

Add `Agents []AgentFinding` to `Report` with `json:"agents,omitempty"`. An absent binary is a warning, not an error: doctor's exit code answers "is this repository workable," and a repository whose reviewer is uninstalled is workable — `pika work` will refuse the moment it is asked to run one.

Compat findings attach only when the env var is set, so doctor spawns nothing on an ordinary invocation.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/doctor/ ./cmd/pika/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(doctor): report what every configured agent resolves to" -- internal/doctor/ cmd/pika/
```

---

### Task 8: Fixtures

**Files:**
- Rename: `internal/e2e/testdata/fakecodex/` → `internal/e2e/testdata/fakeagent/`
- Create: `internal/e2e/testdata/fakeacp/main.go`
- Modify: `internal/e2e/e2e_work_test.go`, `internal/e2e/e2e_concurrency_test.go`

- [ ] **Step 1: Rename the fake agent**

Env vars `FAKE_CODEX_*` → `FAKE_AGENT_*`; behaviour unchanged. Update the package doc, which currently says it stands in for `codex`. Add `installFakeAgent(t, runtime string) string` — builds the fixture and puts it first on `PATH` under that runtime's binary name — and route `codexEnv()` and the argv assertion through it.

- [ ] **Step 2: Write the scripted ACP agent**

Answers `initialize` (protocolVersion 1), `session/new` (`sessionId`), streams two `agent_message_chunk` notifications, sends one `session/request_permission` and asserts the reply selects `allow_once`, then responds `{"stopReason":"end_turn"}`. Content driven by the same `FAKE_AGENT_*` env vars, so it is a fixture and not a second protocol.

- [ ] **Step 3: Run to verify**

Run: `go test ./internal/e2e/`
Expected: PASS — every existing scenario unchanged.

- [ ] **Step 4: Commit**

```bash
git commit -m "test: one installable fake agent per runtime, and a scripted ACP peer" -- internal/e2e/
```

---

### Task 9: End-to-end and smoke

**Files:**
- Create: `internal/e2e/e2e_runtimes_test.go`
- Modify: `internal/smoke/main.go`, `internal/smoke/steps.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestWorkWithAClaudeBuilder(t *testing.T)
func TestTwoRuntimesInOneWorkRun(t *testing.T)
func TestExplorerFindingsReachTheBuilderPrompt(t *testing.T)
func TestMissingBinaryRefusesWithAnActionableMessage(t *testing.T)
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/e2e/ -run 'TestWorkWithAClaude|TestTwoRuntimes|TestExplorerFindings|TestMissingBinary'`
Expected: FAIL — a `claude` runtime is refused before any process spawns.

- [ ] **Step 3: Implement**

Contracts written into the scaffolded repository per scenario, with `installFakeAgent` supplying each runtime's binary. Assert argv content, phase sequences, receipt `roles` entries and the advisory review disposition.

- [ ] **Step 4: Add the smoke step**

One step, `roles`, after `improve-again`: scaffold, write a contract with a `claude` builder and an `omp` reviewer, run `pika work "<goal>"` against the fakes, assert `record.json` lists both runtimes and `phases` includes `review`.

- [ ] **Step 5: Run to verify they pass**

Run: `go test ./internal/e2e/ -count=1` then `go run ./internal/smoke`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git commit -m "test: prove one run under two runtimes, end to end and in the smoke ladder" -- internal/e2e/ internal/smoke/
```

---

### Task 10: Documentation and the gate

**Files:**
- Modify: `docs/guides/usage.md`, `README.md`
- Create: `docs/reference/m5-delta.md`, `docs/reference/m6-delta.md`

- [ ] **Step 1: `docs/guides/usage.md`**

Replace the "must use the Codex runtime" framing around `pika work`/`improve`/`resume`/`handoff` with the runtime table from spec §5.1, the `command`/`args`/`env` fields for `custom` and `acp`, and the role convention (`builder` required, `explorer`/`reviewer` optional). The bundle file table needs no rename — the codex filenames are unchanged.

- [ ] **Step 2: `docs/reference/m5-delta.md`**

What M5 changed underneath users: canonical skills under `.agents/skills/`, generated projections, the digest gate and its two failure modes with opposite remedies, `pika skills` and `--global`, operator-owned skills versus kernel-owned projections. Include a "What an existing repository notices" section and a "Known gaps, deliberately left open" section.

- [ ] **Step 3: `docs/reference/m6-delta.md`**

The seven-runtime gap closed, role phases, the new contract fields, the unchanged no-model-call/no-dependency guarantee, "What an existing repository notices" (nothing breaks: a contract with only `builder` behaves exactly as before), and known gaps covering the six non-goals plus the permission-prompt hazard.

- [ ] **Step 4: `README.md`**

Add `**Milestone 5 (agent skill layer) complete.**` and `**Milestone 6 (runtime adapters) complete.**` status blocks in the existing table style, and correct any line that still implies a single runtime.

- [ ] **Step 5: Full suite**

Run: `go test ./... -count=1`

- [ ] **Step 6: Build, format and vet**

```bash
CGO_ENABLED=0 go build ./...
gofmt -l .
go vet ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

- [ ] **Step 7: Smoke and self-governance**

```bash
go run ./internal/smoke
go run ./cmd/pika check --all
```

- [ ] **Step 8: New-behaviour proof, by hand**

```bash
cd $(mktemp -d) && git init -q .
go run /Users/stephenchoate/Documents/pika/cmd/pika init --profile go --name probe
# add agents: {builder: {runtime: claude}, reviewer: {runtime: omp}} to .project/contract.yaml
go run /Users/stephenchoate/Documents/pika/cmd/pika doctor --json | jq '.result.agents'
PIKA_ADAPTER_COMPAT=1 go test ./internal/adapters/ -count=1 -v -run TestRealAdapterCompatibility
```

Expected: the builder reports runtime `claude`, its resolved binary path and `output: stdout`; the reviewer reports `omp`; `gemini` reports `not on PATH`; the probe reports no missing flags against the three installed binaries and skips the rest.

- [ ] **Step 9: Commit**

```bash
git commit -m "docs: M5 and M6 deltas, the runtime table, and README status blocks" -- docs/ README.md
```

## Self-Review

**Spec coverage.** §5.1→Tasks 2,3,4; §5.2→Tasks 3,4; §5.3→Task 6; §5.4→Task 7; §5.5→Task 5; §6→Tasks 2,3; §7→Tasks 2,3,4,8,9; §8→Task 10. Non-goals are constraints on every task, enforced by the Global Constraints block.

**Ordering.** Task 1 first — the adapter table reads the enum and the agent fields it adds. Tasks 2, 3 and 4 build one package and are sequential within it (table, then process transport, then ACP). Task 5 is the migration and must land before Task 6, because roles are expressed in terms of `adapters.Runner`. Task 7 is independent of Task 6 and may run beside it. Tasks 8 and 9 consume the finished package. Task 10 last.

**Type consistency.** `Adapter`, `Spawn`, `Support`, `OutputMode` and `Transport` are defined in Task 2 and consumed unchanged in 3, 4, 5 and 7. `Agent` and `AgentFromContract` come from Task 2 and are the only thing `cmd/pika` calls in Task 5 and Task 6. `improve.Role` and `Config.Builder/Explorer/Reviewer` are defined in Task 6 and consumed unchanged in Tasks 7, 9 and 10.

**Known risk.** Task 5 deletes `improve.CodexRunner`, the only agent boundary four milestones of tests are built on. Its existing tests must pass after migration with only the two named renames; a third edit means behaviour moved and is a stop-and-report condition.

**Known contingency.** `gemini` and `opencode` flags come from documentation, not an installed binary. If `PIKA_ADAPTER_COMPAT=1` ever disagrees, the probe names the flag, the adapter is corrected, and the verified version is recorded in spec §5.1. No test depends on a real gemini or opencode binary.
