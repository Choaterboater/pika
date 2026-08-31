# pika M7 — The Built-In Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reverse design §10's V1 clause: pika gains a built-in coding-agent loop as an eighth contract runtime — `pika` — speaking to Anthropic and OpenAI-compatible providers over stdlib `net/http`, with usage recorded in `record.json`, a transcript in the handoff bundle, and no change to any existing harness, the receipt, or the command surface.

**Architecture:** A new `internal/loop` package holds the loop: one provider table (anthropic, openai, openrouter), one neutral message model, two thin wire clients (Anthropic Messages, OpenAI-compatible Chat Completions), three tools (`read_file`, `write_file`, `run_command`), and one turn loop with turn/token guards, a 5-minute per-request timeout, and a response-only retry policy. `internal/adapters` registers the eighth runtime as a third transport with no binary, argv or `--help`. `internal/workrec` gains three `omitempty` usage fields and `internal/improve` fills them through an optional interface. `pika doctor` reports the loop as `in-process` with its provider. No new command, no new dependency.

**Tech Stack:** Go 1.26, stdlib only. Two direct dependencies, unchanged. First network client in the tree.

**Spec:** [docs/superpowers/specs/2026-08-31-pika-m7-built-in-loop-design.md](../specs/2026-08-31-pika-m7-built-in-loop-design.md)

## Global Constraints

- `go.mod` MUST still declare exactly two direct dependencies. `go mod tidy && git diff --exit-code go.mod go.sum` MUST stay clean.
- The loop's provider client MUST be fakeable through a base-URL environment override, with no code path that hard-codes a live call in a test.
- `go test ./...` and `pika check --ci` MUST stay provably LLM-free: the only provider contact in any test is a local `httptest` server.
- `CGO_ENABLED=0 go build ./...` MUST succeed. macOS, Linux and Windows are supported targets.
- `internal/loop` imports the standard library and `internal/redact` only. It MUST NOT import `internal/adapters`, `internal/improve`, `internal/contract`, or `internal/envelope`. `internal/adapters` imports `internal/loop`; never the reverse.
- API keys come from the provider's canonical environment variable, never from the contract.
- No receipt schema change: the receipt stays pinned at schema 1. Usage lives in `record.json` and the bundle transcript only.
- No new commands: the command surface stays at 18. `doctor --json` gains a `provider` field and its existing `jsonCases` entry must keep passing.
- Exit codes: `0` success, `1` failure, `2` usage or configuration error.
- Standard library `testing` only.
- pika governs itself: `pika check --all` must pass; CI runs `pika check --ci`.
- **Shared worktree:** commit with an explicit pathspec — `git commit -m "..." -- <your paths>`. Never `git add -A`, never `git stash`. Never commit `.project/state/` or `.superpowers/`.
- Run only the tests named in each task. The full suite runs once, in Task 11.

---

## Task 1: Contract — the eighth harness value

**Files:**
- Modify: `internal/contract/contract.schema.json`
- Test: `internal/contract/schema_test.go`

The `harness` enum is closed at seven values (`contract.schema.json:99`). The loop joins it as the eighth, and the schema learns nothing else: a schema that knew about the loop would be the loop's design leaking into a document that does not know what a loop is.

- [ ] **Step 1: Write the failing tests**

```go
func TestHarnessEnumIncludesPika(t *testing.T)                 // HarnessEnum() contains "pika" as the eighth value
func TestPikaRuntimeWithProviderLoads(t *testing.T)            // agents: {builder: {runtime: pika, provider: anthropic}}
func TestPikaRuntimeWithoutProviderIsSchemaValid(t *testing.T) // the provider requirement is the adapter's, not the schema's
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/contract/ -run 'TestHarnessEnumIncludesPika|TestPikaRuntime'`
Expected: FAIL — the enum rejects `pika`.

- [ ] **Step 3: Implement**

Append `"pika"` to the `definitions.harness` enum, keeping the existing seven in order: `["omp", "codex", "claude", "gemini", "opencode", "acp", "custom", "pika"]`. No new contract fields: `runtime`, `provider`, `model` and `effort` already exist on `definitions.agent`.

`contract.HarnessEnum()` reads the enum from the embedded schema, so it returns eight values with no edit.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/contract/`
Expected: PASS

Note: `TestEveryHarnessInTheContractSchemaHasAnAdapter` (`internal/adapters/adapters_test.go:14`) fails from here until Task 6 adds the adapter. That is the test doing its job; do not touch it.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(contract): add pika as the eighth harness value" -- internal/contract/
```

---

## Task 2: `internal/loop` — the runner and the provider table

**Files:**
- Create: `internal/loop/loop.go`, `internal/loop/provider.go`
- Test: `internal/loop/loop_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestNewRunnerRefusesNoProvider(t *testing.T)        // agent %q declares runtime pika with no provider
func TestNewRunnerRefusesAnUnknownProvider(t *testing.T) // names anthropic, openai and openrouter
func TestNewRunnerRefusesAMissingKey(t *testing.T)       // agent %q: provider %q needs %s in the environment
func TestProviderTableResolvesTheDefaultModel(t *testing.T) // contract model wins; default otherwise
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/loop/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

`internal/loop/loop.go`: the package doc from spec §5.1 (the built-in coding-agent loop: the eighth runtime, and the only one that does not spawn a process), and:

```go
type Runner struct {
	name     string // contract key, for error messages
	provider provider
	model    string // resolved: the contract's model, else the provider's default
	effort   string // "" = omit the provider's reasoning control
	usage    usage  // accumulated across the run's calls
}

func NewRunner(name, providerName, model, effort string) (*Runner, error)
func (r *Runner) Runtime() string { return "pika" }
func (r *Runner) Usage() (calls, tokensIn, tokensOut int)
```

`internal/loop/provider.go`: the `provider` struct and the table from spec §5.2, verbatim — `anthropic` (`https://api.anthropic.com`, `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `claude-sonnet-4-5`), `openai` (`https://api.openai.com/v1`, `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `gpt-5-codex`), `openrouter` (`https://openrouter.ai/api/v1`, `OPENROUTER_API_KEY`, `OPENROUTER_BASE_URL`, `anthropic/claude-sonnet-4-5`).

The key is read with `os.Getenv` at `NewRunner` time; the base-URL override at `Run` time, so a test's `t.Setenv` before `Run` takes effect. Every refusal in `NewRunner` is produced before a request is made.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/loop/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(loop): add the runner and the provider table" -- internal/loop/
```

---

## Task 3: `internal/loop` — the neutral message model and the two wire clients

**Files:**
- Create: `internal/loop/message.go`, `internal/loop/anthropic.go`, `internal/loop/openai.go`, `internal/loop/http.go`
- Test: `internal/loop/client_test.go`

- [ ] **Step 1: Write the failing tests**

A `scripted` helper: `httptest.NewServer`, assert the request's shape, reply with a canned body. The base-URL env override points the client at it.

```go
func TestAnthropicRequestShape(t *testing.T) // headers, model, system, message mapping, tools, thinking only with effort, max_tokens rule
func TestOpenAIRequestShape(t *testing.T)    // headers, system as leading message, tool_calls/tool mapping, reasoning_effort only with effort
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/loop/ -run 'RequestShape'`
Expected: FAIL — symbols undefined.

- [ ] **Step 3: Implement**

The neutral model from spec §5.3, verbatim: `client`, `request`, `response`, `message`, `part`, `toolCall`, `toolResult`, `tool`, `usage`.

`anthropicClient` — POST `{baseURL}/v1/messages`; headers `x-api-key`, `anthropic-version: 2023-06-01`, `content-type: application/json`; the exact request/response mapping of spec §5.3, including `max_tokens` `32768` with thinking / `16384` without, and `thinking: {type: "enabled", budget_tokens: N}` with low→`1024`, medium→`4096`, high→`16384` only when `effort != ""`.

`openaiClient` — POST `{baseURL}/chat/completions`; `Authorization: Bearer <key>`; the exact mapping of spec §5.3, including one `{role:"tool"}` message per result and `reasoning_effort` verbatim `low`/`medium`/`high` only when `effort != ""`.

`http.go` — one shared `&http.Client{Timeout: 0}` (the timeout is the request's; the turn loop owns it), one `doJSON(ctx, method, url string, headers map[string]string, body any, out any) error`, and the retry policy of spec §5.5: `429`, any `5xx`, or a transport error retried up to `maxAttempts = 4` with backoff 1s, 2s, 4s, 8s, honouring `Retry-After` when present and under 60s; `4xx` never retried and surfaced verbatim as `pika loop: <provider> <status>: <redacted response body>` with the body redacted (`redact.Apply`) and tail-truncated to 2 KiB.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/loop/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(loop): speak Anthropic Messages and OpenAI-compatible chat completions" -- internal/loop/
```

---

## Task 4: `internal/loop` — the tool set

**Files:**
- Create: `internal/loop/tools.go`
- Test: `internal/loop/tools_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestReadFileTruncatesAndRefusesPaths(t *testing.T)     // >32 KiB file, absolute path, .project/state/ path
func TestWriteFileRefusesPrivateState(t *testing.T)         // .project/state/ write refused
func TestRunCommandReportsExitAndTruncates(t *testing.T)    // failing command, >8 KiB output
func TestUnknownToolIsAnErrorResult(t *testing.T)           // unknown tool %q
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/loop/ -run 'TestReadFile|TestWriteFile|TestRunCommand|TestUnknownTool'`
Expected: FAIL — symbols undefined.

- [ ] **Step 3: Implement**

`toolSet() []tool` and `executeTool(ctx, root string, call toolCall) toolResult`, per spec §5.4.

The containment rule is the contract's for declared paths, plus `.project/state/`: reject absolute paths, drive letters, `~`, anything that cleans to outside the root, and anything under `.project/state/`.

- `read_file` — first 32 KiB, head-truncated with `[truncated: first 32 KiB of a %d-byte file]`. Missing/unreadable/refused → `isError: true` naming the reason.
- `write_file` — `os.MkdirAll` the parent, write at `0644`. Refused path → `isError: true` naming the reason.
- `run_command` — `sh -c` (non-Windows) / `cmd /c` (Windows), working dir `root`, `exec.CommandContext` with a 10-minute timeout. Combined stdout+stderr tail-truncated to the last 8 KiB with `[truncated: last 8 KiB of a %d-byte output]`; the result states `exit N` or `killed by timeout`. A non-zero exit is **not** an `isError`; `isError` is reserved for commands that could not be started.

Any other tool name → `isError: true` with `unknown tool %q`.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/loop/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(loop): add read_file, write_file and run_command" -- internal/loop/
```

---

## Task 5: `internal/loop` — the turn loop, limits and transcript

**Files:**
- Modify: `internal/loop/loop.go`
- Test: `internal/loop/loop_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestLoopRunsOneToolCallAndFinishes(t *testing.T)  // turn 1 a read_file call, turn 2 final text; message lands at outputPath
func TestLoopAccumulatesUsage(t *testing.T)            // Usage() after a two-call run sums calls/in/out
func TestLoopRefusesTheTurnLimit(t *testing.T)         // pika loop: turn limit reached (40)
func TestLoopRefusesTheTokenLimit(t *testing.T)        // pika loop: token limit reached (400000)
func TestLoopRetriesOn429AndServerError(t *testing.T)  // scripted 429-then-200
func TestLoopDoesNotRetryOn4xx(t *testing.T)           // 401 surfaced verbatim naming the status
func TestLoopWritesARedactedTranscript(t *testing.T)   // pika-transcript.json at 0600, redacted
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/loop/ -run 'TestLoop'`
Expected: FAIL — `Run` is unimplemented.

- [ ] **Step 3: Implement**

`Run` per spec §5.5: read the prompt; loop `turn` from 1 with the two guards (`maxTurns = 40`, `maxRunTokens = 400_000`, both constants, both runaway guards and not policy); call `complete` with `systemPrompt`, the accumulated messages, the tool set, the resolved model and `effort`; accumulate usage; on no tool calls write the joined text to `outputPath` at `0600`, write the transcript, return nil; on tool calls append the assistant turn, execute each call, append one user message per result, and loop.

`systemPrompt` is the fixed kernel text of spec §5.5, verbatim.

Each `complete` call gets `context.WithTimeout(ctx, 5*time.Minute)`, cancelled on return; a timeout aborts the turn as `pika loop: request timed out after 5m` and is never retried.

The transcript (spec §5.6): `pika-transcript.json` at `0600` beside the final message — the whole neutral `messages` slice plus the accumulated `usage`, marshalled indented, run through `redact.Apply` before writing.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/loop/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(loop): run the turn loop with guards, a request timeout and a transcript" -- internal/loop/
```

---

## Task 6: `internal/adapters` — register the eighth runtime

**Files:**
- Modify: `internal/adapters/adapters.go`
- Test: `internal/adapters/adapters_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestPikaRefusesCommandArgsAndEnv(t *testing.T) // the three fail-closed refusals
func TestPikaRequiresAProvider(t *testing.T)        // agent %q declares runtime pika with no provider
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/adapters/ -run 'TestPika'`
Expected: FAIL — `Lookup("pika")` finds nothing. (`TestEveryHarnessInTheContractSchemaHasAnAdapter` has been failing since Task 1; it goes green here.)

- [ ] **Step 3: Implement**

In `internal/adapters/adapters.go`:

- `RuntimePika = "pika"` in the runtime-name constants, after `RuntimeCustom`.
- A third transport after `TransportACP`: `// TransportLoop is the built-in loop: in-process, no subprocess at all.`
- `Agent` gains `Provider string // contract provider, "" when unset`, copied in `AgentFromContract`.
- The `builtins` table gains the eighth entry, after `custom`, verbatim from spec §5.1: no binary, no argv, no `--help`; `Transport: TransportLoop`, `Output: OutputFile`, `Support: Support{Model: true, Effort: true}`.
- `New` gains, before the `TransportACP` branch and after the `custom` check, the three refusals — `agent %q declares command on runtime pika; the loop has no binary`, `agent %q declares args on runtime pika; the loop has no argv`, `agent %q declares env on runtime pika; the loop reads the provider's canonical key var instead` — then `if ad.Transport == TransportLoop { return loop.NewRunner(a.Name, a.Provider, a.Model, a.Effort) }`.
- The package doc's sentence about never speaking to a model and never implementing an agent loop is replaced: the package imports `internal/loop` for the one runtime that is not a subprocess, and the V1 rule it cited is reversed by M7.

`ProbeFlags` returns nil for the loop (no `Args`) and `CheckCompatibility` returns nil (no `Help`); the compat probe needs no edit.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/adapters/`
Expected: PASS — including `TestEveryHarnessInTheContractSchemaHasAnAdapter`.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(adapters): register the built-in loop as the eighth runtime" -- internal/adapters/
```

---

## Task 7: Usage into `record.json`

**Files:**
- Modify: `internal/workrec/record.go`, `internal/improve/improve.go`
- Test: `internal/workrec/record_test.go`, `internal/improve/improve_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestRunAgentUsageRoundTrips(t *testing.T)     // fields encode and decode
func TestZeroUsageIsOmitted(t *testing.T)          // a zero-usage agent is byte-identical to pre-M7
func TestLoopUsageLandsOnTheBuilderRecord(t *testing.T) // a runner reporting usage stamps the three fields
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/workrec/ ./internal/improve/ -run 'Usage'`
Expected: FAIL — `RunAgent` has no usage fields.

- [ ] **Step 3: Implement**

`internal/workrec/record.go` — `RunAgent` gains, verbatim from spec §5.6:

```go
Calls     int `json:"calls,omitempty"`
TokensIn  int `json:"tokens_in,omitempty"`
TokensOut int `json:"tokens_out,omitempty"`
```

All three `omitempty`, so a pre-M7 record is byte-identical and every existing record fixture still parses. `redacted()` leaves them alone (kernel-generated counters, like `Role`/`Runtime`). No schema change anywhere — `record.json` has no JSON Schema; the receipt is untouched.

`internal/improve/improve.go` — one local interface and one helper, verbatim from spec §5.6 (`usageReporter`, `usageOf`). At each of the three `RunAgent` appends (`improve.go:710`, `:730`, `:805`), fill the three fields from `usageOf(cfg.Explorer.Runner)`, `usageOf(cfg.Builder.Runner)`, `usageOf(cfg.Reviewer.Runner)` respectively. Nothing else in the lifecycle changes.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/workrec/ ./internal/improve/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(improve): record loop usage in record.json" -- internal/workrec/ internal/improve/
```

---

## Task 8: Doctor — the in-process row and provider

**Files:**
- Modify: `internal/doctor/doctor.go`, `cmd/pika/doctor.go`
- Test: `internal/doctor/doctor_test.go`, `cmd/pika/doctor_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestDoctorReportsTheLoopAsInProcess(t *testing.T) // binary "in-process", found, no LookPath
func TestDoctorReportsProvider(t *testing.T)           // provider field set for every runtime that declares one
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/doctor/ -run 'TestDoctorReports'`
Expected: FAIL — `AgentFinding` has no `Provider`.

- [ ] **Step 3: Implement**

`AgentFinding` gains `Provider string \`json:"provider,omitempty"\``. In `checkAgents`, after the `Lookup` succeeds, when `ad.Transport == adapters.TransportLoop`: set `finding.Binary = "in-process"`, `finding.Found = true`, and skip the `exec.LookPath`. Set `finding.Provider = cfg.Provider` whenever the contract sets one, for every runtime. The compat probe stays skipped for the loop because `ad.Help` is nil — no special-casing.

`cmd/pika/doctor.go` `printAgents`: when `a.Provider != ""`, include `provider: <name>` in the parts line before the model/effort controls. A loop row reads `builder    pika      in-process` with `provider: anthropic  model: mapped  effort: mapped  output: file  resume: no`.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/doctor/ ./cmd/pika/`
Expected: PASS — including the existing `doctor --json` `jsonCases` entry.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(doctor): report the loop in-process with its provider" -- internal/doctor/ cmd/pika/
```

---

## Task 9: E2E and smoke against a scripted provider

**Files:**
- Create: `internal/e2e/e2e_loop_test.go` (with the scripted-provider fixture)
- Modify: `internal/smoke`

Real binary, fake provider only — this is what keeps `pika check --ci` provably LLM-free. One scripted provider on `httptest` started by the test, `ANTHROPIC_BASE_URL` pointed at it, `ANTHROPIC_API_KEY` set to a dummy; `FAKE_AGENT_*` unused (the loop is not the fixture). Contract `builder: {runtime: pika, provider: anthropic}`. The script: turn 1 calls `write_file` for the agent's edit, turn 2 returns final text.

- [ ] **Step 1: Write the failing tests**

```go
func TestWorkWithALoopBuilder(t *testing.T)   // delivers; edit in the commit; bundle holds pika-transcript.json; record.json shows runtime pika and non-zero usage; receipt roles name runtime pika with provider anthropic
func TestLoopBuilderRunsACommand(t *testing.T) // first call run_command "go test ./..."; transcript records the tool result; run delivered; transcript redacted
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/e2e/ -run 'Loop' -count=1`
Expected: FAIL before the fixture exists; after it exists, they pass against the Tasks 1–8 wiring.

- [ ] **Step 3: Implement the fixture and the tests**

The scripted provider: an `httptest.NewServer` whose handler asserts the Anthropic request shape and replies from a per-test script (tool call, then final text), exactly the `scripted` helper from Task 3 lifted to the wire.

- [ ] **Step 4: Add the smoke step**

One step `loop` after `roles`: spin the scripted provider on `httptest` inside the smoke program, write a contract with `builder: {runtime: pika, provider: anthropic}`, run `pika work "<goal>"` with the two env vars, assert the deliver and that `record.json` names runtime `pika`.

- [ ] **Step 5: Run to verify**

Run: `go test ./internal/e2e/ -run 'Loop' -count=1 && go run ./internal/smoke`
Expected: PASS, including the new `loop` step.

- [ ] **Step 6: Commit**

```bash
git commit -m "test(e2e): drive the loop through the real binary against a scripted provider" -- internal/e2e/ internal/smoke/
```

---

## Task 10: Documentation

**Files:**
- Create: `docs/reference/m7-delta.md`
- Modify: `docs/superpowers/specs/2026-08-28-pika-design.md` §10, `docs/guides/usage.md`, `README.md`
- Rename: `.github/workflows/codex-compat.yml` → `adapter-compat.yml`

- [ ] **Step 1: Write `docs/reference/m7-delta.md`**

The V1 clause reversed; the eighth runtime and how it is selected; the two-provider table; the tool set and the unrestricted-exec posture (operator's choice, stated); the turn/token guards and the 5-minute request timeout as the now-solvable version of M6's documented gap; usage in `record.json` and the transcript with no receipt schema change; "What an existing repository notices" (nothing: every other runtime is byte-identical, and a contract that names no `pika` agent spawns nothing new); "Known gaps" covering the six §3 non-goals plus `pika do` and budget.

- [ ] **Step 2: Reverse the V1 clause in the design**

`docs/superpowers/specs/2026-08-28-pika-design.md` §10: replace the sentence "`pika` does not implement a new LLM client or coding-agent loop in V1." with "`pika` implements a coding-agent loop as the `pika` runtime, speaking to a provider over stdlib `net/http` with no new dependency; the adapters remain the boundary for harness binaries."

- [ ] **Step 3: Update `docs/guides/usage.md`**

The runtime table in §10 gains a `pika` row (`in-process`, `output: file`, provider-controlled model/effort). A new subsection under the runtimes section documents the provider table, the canonical key/base-URL env vars (including the base-URL override as the way to test without a provider), the three tools and their bounds, the turn/token guards and request timeout, the transcript, and how usage lands in `record.json`. The doctor section's `agents` row gains `provider` and the `in-process` binary value.

- [ ] **Step 4: Update `README.md`**

An `**Milestone 7 (the built-in loop) complete.**` status block in the existing table style. Correct the M6 block's line about pika speaking to no provider (that was true at M6; the M6 block stays as history — the M7 block and m7-delta record the reversal).

- [ ] **Step 5: Fix the broken scheduled workflow**

Rename `.github/workflows/codex-compat.yml` to `adapter-compat.yml`; retitle the job `adapter-compat`; update the header comment to cite `internal/adapters` (it cites `internal/improve/handoff.go` and `CodexRunner`); run `go test ./internal/adapters/ -count=1 -v -run TestRealAdapterCompatibility` with `PIKA_ADAPTER_COMPAT: "1"`. Keep the `codex` install step (the test skips absent binaries) and keep it scheduled-only.

- [ ] **Step 6: Commit**

```bash
git commit -m "docs: M7 delta, the §10 reversal, usage guide and README; fix the compat workflow" -- docs/ README.md .github/
```

---

## Task 11: Gate

Every command below, run from the repository root on branch `feat/m7-built-in-loop`, all green before the squash merge.

- [ ] **Step 1: The whole suite**

Run: `go test ./... -count=1`
Expected: PASS — including `internal/loop` and the loop e2e tests, all against local `httptest` providers.

- [ ] **Step 2: The cross-compilation floor**

Run: `CGO_ENABLED=0 go build ./...`
Expected: success — the loop is stdlib `net/http` only, no cgo.

- [ ] **Step 3: Format and vet**

Run: `gofmt -l .` (must print nothing) and `go vet ./...`
Expected: clean.

- [ ] **Step 4: Smoke**

Run: `go run ./internal/smoke`
Expected: PASS, including the `loop` step.

- [ ] **Step 5: pika governing pika**

Run: `go run ./cmd/pika check --all`
Expected: green — no pack or template changed, so no digest rotation.

- [ ] **Step 6: Dependencies unchanged**

Run: `go mod tidy && git diff --exit-code go.mod go.sum`
Expected: clean — exactly two direct dependencies.

- [ ] **Step 7: New-behaviour proof, by hand against a scripted provider (never a live one)**

`cd $(mktemp -d) && git init -q .`, `go run ./cmd/pika init --profile go --name probe`, add `agents: {builder: {runtime: pika, provider: anthropic}}` to `.project/contract.yaml`:

- `go run ./cmd/pika doctor --json | jq '.result.agents'` → the builder reports runtime `pika`, binary `in-process`, `provider: anthropic`, `output: file`.
- With `ANTHROPIC_BASE_URL` pointed at a scripted provider and a dummy `ANTHROPIC_API_KEY`: `go run ./cmd/pika work "<goal>" --json` → exits 0, `.result.commit` non-empty, the bundle holds `pika-transcript.json`, and `record.json` shows the builder's `runtime == "pika"` with non-zero `calls`/`tokens_in`/`tokens_out`.
- `PIKA_ADAPTER_COMPAT=1 go test ./internal/adapters/ -count=1 -v -run TestRealAdapterCompatibility` → probes the installed binaries with `--help` only, skips the loop, reports no missing flags. No model call.

- [ ] **Step 8: Squash merge**

Squash merge `feat/m7-built-in-loop` to `main` with the closing message `feat(m7): … closing M7`.

---

**Spec coverage.** §5.1 lands in Tasks 1 and 6; §5.2 in Task 2; §5.3 in Task 3; §5.4 in Task 4; §5.5 in Task 5; §5.6 in Tasks 5 and 7; §5.7 in Task 8; §6 in Tasks 2, 5 and 6; §7 in Tasks 1–9; §8 is Task 11. Every §3 non-goal is a Global Constraint or a documented absence, not a task.

**Ordering.** The contract enum lands first so every later task compiles against the real schema, and the adapter-coverage test stays red between Tasks 1 and 6 — the test doing its job. The loop is built bottom-up (runner → clients → tools → turn loop) so each task's tests exercise only what exists. Wiring (adapters, improve, doctor) precedes the e2e fixture that drives it. Documentation precedes the gate so `pika check --all` and the hand-run proofs see the finished tree.

**Type consistency.** `adapters.Runner` and `improve.Runner` keep agreeing structurally, and `loop.Runner` satisfies both with `Runtime() string` and the unchanged `Run` signature. Usage crosses the boundary through the optional `usageReporter` interface, so subprocess runners stay zero-valued and `workrec.RunAgent`'s `omitempty` fields keep a pre-M7 record byte-identical. The neutral message model is the single shape the turn loop, the tools and the limits are written against; the two clients are the only places wire spelling exists.
