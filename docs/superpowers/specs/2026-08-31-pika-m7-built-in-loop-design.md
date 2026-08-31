# pika M7 Design Specification — The Built-In Loop

**Status:** Approved
**Date:** 2026-08-31
**Product:** `pika`
**Builds on:** [2026-08-31-pika-m6-runtime-adapters-design.md](2026-08-31-pika-m6-runtime-adapters-design.md)
**Implements:** [2026-08-28-pika-design.md](2026-08-28-pika-design.md) §10 (the reversal), §9.1
**Supersedes:** §10's V1 clause — the sentence "`pika` does not implement a new LLM client or coding-agent loop in V1" is replaced, and §10 records the replacement

## 1 Purpose

pika stops pretending it cannot reason, because the seam M6 built is exactly where a loop belongs.

Design §10's V1 clause reads "`pika` does not implement a new LLM client or coding-agent loop in V1" (`2026-08-28-pika-design.md:450`). M6 built the adapter layer where a built-in loop lands and stated plainly, in its own header, that the reversal is M7's. M7 is that reversal: pika gains a built-in coding-agent loop as an eighth contract `harness` value — `pika` — and speaks to a provider over stdlib `net/http` for the first time.

The loop is one more runtime, not a new kind of thing. It is selected the same way (`agents.<name>.runtime: pika`), driven by the same `Runner` contract M6 built (`Run(ctx, root, promptPath, outputPath) error` + `Runtime() string`), and held to the same guarantees: the Git-state equality check, the read-only rules for explorer and reviewer, and the recheck ladder. None of those knows or cares which runtime produced the change. Every existing harness binary stays exactly as it is.

## 2 Goals

1. Reverse §10's V1 clause: pika implements a coding-agent loop as the `pika` runtime, and the design document records the replacement.
2. Speak to two provider families — Anthropic Messages and OpenAI-compatible Chat Completions — over stdlib `net/http`, with no SDK and no new dependency.
3. Keep the loop provider-agnostic: one shared turn loop, one neutral message model, and two thin wire clients that differ only in how a message, a tool call and a token count travel.
4. Give the loop a tool set that mirrors the most permissive M6 posture: read, write, and unrestricted shell, logged, redacted, and bounded by per-run limits.
5. Record what the run spent — model calls and tokens — in `record.json` per agent and in the bundle transcript, with no receipt schema change.
6. Introspect the loop through `pika doctor` like every other runtime, with no new command.
7. Keep the guarantees M1–M6 earned where they still apply: one run lease, the ladder as the only gate, exactly two direct dependencies, and a suite that stays provably LLM-free.

## 3 Non-goals

- **No intent router.** `pika do "<goal>"` stays deferred. It needs a design about what intent is, and M7 is the loop alone.
- **No envelope enforcement on the loop.** Runtimes are not envelope-checked and never have been; the envelope governs MCP agents. Adding one here would invent a policy for a harness M6 never checked.
- **No budget enforcement.** The envelope's `budget` kind is membership-only and `pika authorize` refuses to write it. Spend is recorded, not capped by policy; the runaway guards (turn and token caps) are constants, not configuration.
- **No receipt schema change.** Usage lives in `record.json` and the bundle transcript. The receipt is closed and pinned at schema 1; bumping it for a number a record already carries is churn for no attestation.
- **No `pika mcp` wiring.** M4 made serving the kernel to a spawned agent useless; the loop changes nothing about that.
- **No new commands.** The loop is contract-driven and introspected by `pika doctor`. The command surface stays at 18.

## 4 Current-state findings

Verified against `main` at `ffa4128`.

| Finding | Evidence |
|---|---|
| The schema's harness enum is closed at seven values | `internal/contract/contract.schema.json:99` (`omp, codex, claude, gemini, opencode, acp, custom`) |
| `HarnessEnum()` reads the enum back from the embedded schema | `internal/contract/schema.go:140-157` |
| An agent entry already carries `runtime`, `provider`, `model` and `effort` | `internal/contract/contract.schema.json:72-82` |
| Nothing consumes the contract's `provider` — `Agent` has no such field | `internal/adapters/adapters.go:301-309` |
| The adapter package doc states the V1 rule M7 reverses | `internal/adapters/adapters.go:6-8` ("pika never speaks to a model, opens a socket or implements an agent loop") |
| The adapter table has seven entries and two transports, both subprocess | `internal/adapters/adapters.go:111` (`builtins`), `:85-90` (`TransportProcess`, `TransportACP`) |
| The runner contract is two methods, agreed structurally across packages | `internal/adapters/process.go:15-21` |
| Every harness value is enforced to have an adapter | `internal/adapters/adapters_test.go:14` (`TestEveryHarnessInTheContractSchemaHasAnAdapter`) |
| `RunAgent` records role, agent and runtime — and nothing the run spent | `internal/workrec/record.go:45-49` |
| The lifecycle stamps one agent per role at three sites | `internal/improve/improve.go:710`, `:730`, `:805` |
| The receipt is pinned at schema 1 | `internal/evidence/receipt.go:36` (`const receiptSchemaVersion = 1`) |
| The envelope's `budget` kind exists and `pika authorize` never writes it | `internal/envelope/envelope.go:61`, `internal/authorize/authorize.go:284` |
| doctor stats a binary for every configured agent; `AgentFinding` has no provider | `internal/doctor/doctor.go:54-66`, `:587` |
| The command surface is 18 | `cmd/pika/main.go:86-187` (seventeen entries in the literal) plus `help` appended in `init` (`:194`) |
| Design §10's V1 clause is the sentence M7 replaces | `docs/superpowers/specs/2026-08-28-pika-design.md:450` |
| M6's spec names the reversal as M7's | `docs/superpowers/specs/2026-08-31-pika-m6-runtime-adapters-design.md` header (`**Does not supersede:**`) |
| The scheduled compat job drives the probe M6 deleted | `.github/workflows/codex-compat.yml:52-53` (`PIKA_CODEX_COMPAT`, `TestRealCodexCompatibility`) |
| There is no loop package and no HTTP client anywhere in the tree | named absence: `internal/` lists 31 packages, none `loop`; `net/http` appears nowhere under `internal/` or `cmd/` |
| `redact.Apply` exists — the loop's one persistence dependency | `internal/redact/redact.go:116` |
| `go.mod` declares exactly two direct dependencies | `go.mod` (`goccy/go-yaml`, `santhosh-tekuri/jsonschema/v6`) |

## 5 Design

### 5.1 The eighth runtime

The contract's `harness` enum gains one value, appended so the existing seven keep their order: `["omp", "codex", "claude", "gemini", "opencode", "acp", "custom", "pika"]`. No new contract fields: `runtime`, `provider`, `model` and `effort` already exist on `definitions.agent` and are everything the loop needs.

A contract with `runtime: pika` and no `provider` is schema-valid. The provider requirement is the adapter's, not the schema's, because a schema that knew about the loop would be the loop's design leaking into a document that does not know what a loop is.

`internal/adapters` registers the runtime the same declarative way as the other seven:

- `RuntimePika = "pika"` in the runtime-name constants, after `RuntimeCustom`.
- A third transport, `TransportLoop` — in-process, no subprocess at all.
- `Agent` gains `Provider string`, copied in `AgentFromContract`.
- The `builtins` table gains the eighth entry, after `custom`: no binary, no argv, no `--help`; `TransportLoop`, `OutputFile`, `Support{Model: true, Effort: true}`. The loop writes its own final message to `{output}` and takes model and effort as provider controls.
- `New` refuses the three subprocess-only controls before spawning anything, the same fail-closed rule M6 applies everywhere — a control the runtime cannot express is an error, never a silently dropped field:
  - `agent %q declares command on runtime pika; the loop has no binary`
  - `agent %q declares args on runtime pika; the loop has no argv`
  - `agent %q declares env on runtime pika; the loop reads the provider's canonical key var instead`
- `New` then hands off: `if ad.Transport == TransportLoop { return loop.NewRunner(a.Name, a.Provider, a.Model, a.Effort) }`.

The package doc's sentence about never speaking to a model and never implementing an agent loop is replaced with the new reality: the package imports `internal/loop` for the one runtime that is not a subprocess, and the V1 rule it cited is reversed by M7. `adapters` imports `internal/loop`; never the reverse. Nothing else about the dependency statement changes.

`Lookup("pika")` returns the entry; `ProbeFlags` returns nil (no `Args`) and `CheckCompatibility` returns nil (no `Help`), so the compat probe needs no edit for the loop.

The loop lives in a new package, `internal/loop`. It imports stdlib (`net/http`, `encoding/json`, `os/exec`, `context`, `time`, `errors`, `fmt`, `os`, `strings`, `path/filepath`) and `internal/redact` — and nothing else from the tree. It must never import `internal/adapters`, `internal/improve`, `internal/contract`, or `internal/envelope`. `internal/redact` is for the one artifact the loop persists itself (the transcript, §5.6); the prompt it reads and the final message it writes are redacted by `createHandoff` already.

```go
// Runner runs one loop: one prompt in, a final message out.
type Runner struct {
	name     string // contract key, for error messages
	provider provider
	model    string // resolved: the contract's model, else the provider's default
	effort   string // "" = omit the provider's reasoning control
	usage    usage  // accumulated across the run's calls
}

// NewRunner builds the loop for one resolved agent. Every refusal here is
// produced before a request is made.
func NewRunner(name, providerName, model, effort string) (*Runner, error)

func (r *Runner) Runtime() string { return "pika" }

// Usage reports the model calls and tokens the run spent.
func (r *Runner) Usage() (calls, tokensIn, tokensOut int)

func (r *Runner) Run(ctx context.Context, root, promptPath, outputPath string) error
```

Fail-closed errors from `NewRunner`, before any request:

- `agent %q declares runtime pika with no provider` — a loop with no provider is a loop that cannot pick a client.
- `agent %q declares provider %q; runtime pika speaks anthropic, openai and openrouter`
- `agent %q: provider %q needs %s in the environment` — the canonical key var is unset. The key is read from pika's own environment, never from the contract: a credential in a contract is a credential in every clone.

### 5.2 The provider table

One table row per vendor: which wire shape, where to send it, and which environment variables carry the key and a base-URL override. The base-URL override is the testing seam and it is the only one: a test points it at a local `httptest` server and the whole suite stays LLM-free.

| provider | client | baseURL | keyEnv | baseURLEnv | default model |
|---|---|---|---|---|---|
| `anthropic` | Anthropic Messages | `https://api.anthropic.com` | `ANTHROPIC_API_KEY` | `ANTHROPIC_BASE_URL` | `claude-sonnet-4-5` |
| `openai` | OpenAI-compatible | `https://api.openai.com/v1` | `OPENAI_API_KEY` | `OPENAI_BASE_URL` | `gpt-5-codex` |
| `openrouter` | OpenAI-compatible | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | `OPENROUTER_BASE_URL` | `anthropic/claude-sonnet-4-5` |

The key is read with `os.Getenv` at `NewRunner` time; the base-URL override at `Run` time, so a test's `t.Setenv` before `Run` takes effect. The contract's `model` wins when set; the default is a convenience, and a default a provider no longer serves produces the provider's own `404` naming the model — the operator sets `model` in the contract.

### 5.3 The neutral message model and two wire clients

One neutral model, Anthropic-shaped (parts, not role-per-call), because both APIs project onto it cleanly:

```go
type client interface {
	// complete exchanges the conversation so far for one response.
	complete(ctx context.Context, req request) (response, error)
}

type request struct {
	system   string
	messages []message
	tools    []tool
	model    string
	effort   string
}

type response struct {
	text  []string   // text parts, in order
	calls []toolCall // tool calls, in order
	usage usage
}

type message struct {
	role  string // "user" | "assistant"
	parts []part
}

// part is one content block: exactly one of the three fields is set.
type part struct {
	text   string
	call   *toolCall
	result *toolResult
}

type toolCall   struct{ id, name string; input json.RawMessage }
type toolResult struct{ id, output string; isError bool }
type tool       struct{ name, description string; schema any }
type usage      struct{ in, out int }
```

**`anthropicClient`** — POST `{baseURL}/v1/messages`. Headers: `x-api-key: <key>`, `anthropic-version: 2023-06-01`, `content-type: application/json`. Request: `{model, max_tokens, system, messages, tools, thinking?}`; the neutral model maps text → `{type:"text",text}`, call → `{type:"tool_use",id,name,input}` (input decoded from `json.RawMessage`), result → `{type:"tool_result",tool_use_id,content,is_error}`, with assistant and user keeping their roles; `tools` maps to `{name, description, input_schema}`. `max_tokens` is `32768` when thinking is enabled, else `16384` (thinking requires max_tokens to exceed its budget). `thinking` is present only when `effort != ""`, as `{type: "enabled", budget_tokens: N}` with low→`1024`, medium→`4096`, high→`16384`. Response: text blocks → `response.text`; `tool_use` blocks → `response.calls` (input re-encoded to `json.RawMessage`); `usage: {input_tokens, output_tokens}` maps straight.

**`openaiClient`** — POST `{baseURL}/chat/completions`. Headers: `Authorization: Bearer <key>`, `content-type: application/json`; no OpenRouter-specific headers (they are optional). Request: `{model, messages, tools, reasoning_effort?}`; system → a leading `{role:"system"}` message; an assistant message carrying calls maps to `{role:"assistant", content: <joined text or "" if none>, tool_calls: [{id, type:"function", function:{name, arguments}}]}` with `arguments` the raw `input` string; a tool result maps to a separate `{role:"tool", tool_call_id, content}` message, each result its own message following the assistant's; `tools` maps to `{type:"function", function:{name, description, parameters}}`. `reasoning_effort` is present only when `effort != ""`, verbatim `low`/`medium`/`high`. Response: `message.content` → `response.text` (one entry when non-empty); `message.tool_calls` → `response.calls` (arguments → `json.RawMessage`); `usage: {prompt_tokens, completion_tokens}` maps straight.

Both clients share one `*http.Client` (`&http.Client{Timeout: 0}` — the timeout is the request's, not the client's, because the turn loop owns it; §5.5), one `doJSON(ctx, method, url string, headers map[string]string, body any, out any) error` helper that encodes, sends, and decodes, and one retry policy (§5.5).

### 5.4 The tool set

Three tools, returned by `toolSet() []tool`, executed by `executeTool(ctx, root string, call toolCall) toolResult`. All three take repository-relative paths resolved against `root` through the same containment rule the contract uses for declared paths: reject absolute paths, drive letters, `~`, and any path that cleans to outside the root — and additionally reject anything under `.project/state/`, which is kernel-private; a model has no business reading or writing its own run record.

- `read_file` — params `{path: string}`. Returns the file's first 32 KiB, head-truncated with a marker `[truncated: first 32 KiB of a %d-byte file]` when longer, so the model reads top-down and is never shown a section it could mistake for the whole thing. Missing file, unreadable file, or refused path → an `isError: true` tool result naming the reason: the model self-corrects; a bad path is not a run failure.
- `write_file` — params `{path: string, content: string}`. `os.MkdirAll` the parent, write at `0644`. Refused path → `isError: true` naming the reason. This is the only tool that changes the tree, and the run's own checks — the Git-state equality check, and `requireNoNewChanges` for explorer and reviewer — are what hold it to its role, exactly as they hold a harness binary with edit tools.
- `run_command` — params `{command: string}`. Unrestricted: `sh -c <command>` on non-Windows, `cmd /c` on Windows, working dir `root`, `exec.CommandContext` with a 10-minute per-command timeout. Combined stdout+stderr is tail-truncated to the last 8 KiB with a `[truncated: last 8 KiB of a %d-byte output]` marker (the same bound `evidence` uses); the exit status is stated in the result (`exit N`, or `killed by timeout`). A non-zero exit is **not** an `isError` — it is a command that failed, which the model is supposed to see; `isError` is reserved for commands that could not be started.

Any tool name outside the three → `isError: true` tool result `unknown tool %q`.

### 5.5 The turn loop, limits and retries

`Run`:

1. Read the prompt file. First message: `{role: "user", parts: [{text: prompt}]}`.
2. Loop, `turn` from 1: before each request, refuse `pika loop: turn limit reached (%d)` when `turn > 40`, and `pika loop: token limit reached (%d)` when accumulated `usage.in + usage.out > 400_000`. Both are constants (`maxTurns = 40`, `maxRunTokens = 400_000`) and both are runaway guards, not policy: a model stuck in a tool loop or burning context is a defect to surface, not a budget to tune.
3. Call `provider.client.complete` with `system: systemPrompt`, the accumulated messages, the tool set, the resolved model, and `effort`.
4. Accumulate `usage.calls++`, `usage.in`, `usage.out`.
5. No tool calls → the run is done: the final message is the response's text parts joined with `\n`; write it to `outputPath` at `0600` (the loop writes `{output}` itself, like `codex`'s `OutputFile`), write the transcript (§5.6), return nil.
6. Tool calls → append the assistant's turn (`{role: "assistant", parts: <text parts then call parts>}`), execute each call with `executeTool`, append one `{role: "user", parts: [{result: …}]}` message **per result**, and loop.

`systemPrompt` is fixed kernel text describing the tools and the one rule that ends the loop:

> You are an agent in a verified Pika run, working in a repository with tools. Paths are repository-relative and must stay inside it; `.project/state/` is kernel-private and is refused. Use read_file to read a file, write_file to write one (the full new content), and run_command to run a shell command. Answer without a tool call to finish. Do not run git commit, git merge, git rebase, git push, or any GitHub command; Pika verifies and commits approved changes itself.

Each request carries its own timeout: `ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)` per `complete` call, cancelled on return. This is the gap M6 documented ("a harness that stops on a permission prompt blocks the run") becoming solvable: pika owns this loop, so it owns the timeout. There is deliberately no per-request retry of a *timed-out* call that may have produced tool effects on the far side — a timeout aborts the turn with a named error (`pika loop: request timed out after 5m`).

Retries apply to *responses*, not effects: on HTTP `429`, any `5xx`, or a transport error, retry up to `maxAttempts = 4` with backoff 1s, 2s, 4s, 8s, honouring a `Retry-After` header when present and under 60s. Never retry `4xx` — a `401`/`403`/`404`/`422` is a fact to surface verbatim: `pika loop: <provider> <status>: <redacted response body>`. The body is redacted (`redact.Apply`) and tail-truncated to 2 KiB before it goes into the error, because a provider error page is exactly where a leaked key or a path would otherwise travel to a terminal.

### 5.6 The transcript and usage

A new bundle artifact: `pika-transcript.json` at `0600` in the handoff bundle, written after the final message. It is the whole neutral `messages` slice plus the accumulated `usage`, marshalled indented, run through `redact.Apply` before writing — the loop's one direct use of `redact`, because file contents and command output that went to the provider raw must not land in a persisted file raw. The bundle for a loop run then holds the same files any run holds (`checks-before.json`, `prompt.md`, `pika-last-message.md`) plus the transcript; nothing about the bundle's naming or redaction of the existing files changes.

Usage also lands in `record.json`. `workrec.RunAgent` gains three fields, all `omitempty` so a pre-M7 record is byte-identical and every existing record fixture still parses:

```go
Calls     int `json:"calls,omitempty"`
TokensIn  int `json:"tokens_in,omitempty"`
TokensOut int `json:"tokens_out,omitempty"`
```

`redacted()` leaves them alone — kernel-generated counters, like `Role`/`Runtime`. No schema change anywhere: `record.json` has no JSON Schema, and the receipt is untouched.

`internal/improve` learns to ask, through one optional interface:

```go
// usageReporter is the optional interface a runner implements when it can
// say what the run spent. Subprocess runners cannot — the number would be
// a guess — so only the built-in loop reports, and the field stays absent
// for every other runtime.
type usageReporter interface{ Usage() (calls, tokensIn, tokensOut int) }
```

At each of the three `RunAgent` appends in the lifecycle, the fields are filled from `usageOf(cfg.Explorer.Runner)`, `usageOf(cfg.Builder.Runner)`, `usageOf(cfg.Reviewer.Runner)` respectively. Nothing else in the lifecycle changes: the loop is driven through the unchanged `Run(ctx, root, promptPath, rawResultPath)` call, and the role-read-only rule is unchanged — an explorer or reviewer on the loop whose model calls `write_file` is refused afterwards by `requireNoNewChanges`, exactly as a harness binary that wrote would be.

### 5.7 Doctor introspection

`AgentFinding` gains `Provider string \`json:"provider,omitempty"\``. In `checkAgents`, after the `Lookup` succeeds, when `ad.Transport == adapters.TransportLoop`: set `finding.Binary = "in-process"`, `finding.Found = true`, and skip the `exec.LookPath` — there is no binary to stat, and doctor's whole contract (probe nothing, mutate nothing) is kept by not inventing one. `finding.Provider` is set whenever the contract sets one, for every runtime. The compat probe (`PIKA_ADAPTER_COMPAT=1`) is skipped for the loop because `ad.Help` is nil — no special-casing needed.

`pika doctor`'s text output includes `provider: <name>` in the parts line before the model/effort controls when set. A loop row reads `builder    pika      in-process` with `provider: anthropic  model: mapped  effort: mapped  output: file  resume: no`.

## 6 Error handling

Every configuration refusal is produced before a request is made, and each names the agent, the fact, and the remedy:

| Error | Why it exists |
|---|---|
| `agent %q declares runtime pika with no provider` | A loop with no provider is a loop that cannot pick a client. |
| `agent %q declares provider %q; runtime pika speaks anthropic, openai and openrouter` | The provider table is closed; a typo is otherwise a silent misconfiguration. |
| `agent %q: provider %q needs %s in the environment` | The canonical key var is unset; the key is never read from the contract. |
| `agent %q declares command on runtime pika; the loop has no binary` | Fail-closed on a control the runtime cannot express. |
| `agent %q declares args on runtime pika; the loop has no argv` | Same. |
| `agent %q declares env on runtime pika; the loop reads the provider's canonical key var instead` | Same. |
| `pika loop: turn limit reached (%d)` | Runaway guard, not policy: a model stuck in a tool loop is a defect to surface. |
| `pika loop: token limit reached (%d)` | Same, for a model burning context. |
| `pika loop: request timed out after 5m` | A timed-out call may have produced tool effects on the far side, so it aborts the turn rather than retrying. |
| `pika loop: <provider> <status>: <redacted response body>` | A `4xx` is a fact to surface verbatim; the body is redacted and tail-truncated to 2 KiB. |

Tool failures are not run failures: a missing file, a refused path, an unknown tool (`unknown tool %q`) or a command that exits non-zero comes back as a tool result the model can read and self-correct from. `isError` is reserved for results the model could not have caused — a refused path, an unreadable file, a command that could not be started.

One hazard is documented rather than fixed, because fixing it is a larger change than this milestone: **unrestricted exec carries the risk of a runaway `run_command`.** The guard is the tool set itself — every command is logged, redacted, bounded by the 10-minute per-command timeout and the turn/token caps, and refused after the fact by the run's own tree checks if it writes where it should not. If that proves too loose in practice, the fallback is to allowlist the contract's check commands — a change to `executeTool` alone, not to the loop or the contract.

## 7 Testing

Unit, in `internal/loop` — a scripted fake provider per client, using `httptest.NewServer` and the base-URL env override; a `scripted` helper that asserts the request's shape (headers, model, system, message mapping, tool definitions) and replies with a canned body:

- `TestAnthropicRequestShape` / `TestOpenAIRequestShape` — headers, model, system placement, tool definitions, message mapping for text/call/result, `thinking`/`reasoning_effort` present only when effort is set, the `max_tokens` rule.
- `TestLoopRunsOneToolCallAndFinishes` — turn 1 a `read_file` call, turn 2 final text; the tool ran, the assistant turn and result were appended correctly, the final message lands at `outputPath`.
- `TestLoopAccumulatesUsage` — `Usage()` after a two-call run sums calls/in/out.
- `TestLoopRefusesTheTurnLimit` and `TestLoopRefusesTheTokenLimit` — both name the limit.
- `TestLoopRetriesOn429AndServerError` and `TestLoopDoesNotRetryOn4xx` — a scripted 429-then-200, and a 401 surfaced verbatim naming the status.
- `TestReadFileTruncatesAndRefusesPaths` (a >32 KiB file, an absolute path, a `.project/state/` path), `TestWriteFileRefusesPrivateState`, `TestRunCommandReportsExitAndTruncates` (a failing command, a >8 KiB output), `TestUnknownToolIsAnErrorResult`.
- `TestNewRunnerRefusesAnUnknownProvider`, `TestNewRunnerRefusesAMissingKey`, `TestNewRunnerRefusesNoProvider`.

Unit, elsewhere:

- `internal/contract`: the enum contains `"pika"`, and a contract with `agents: {builder: {runtime: pika, provider: anthropic}}` loads; `runtime: pika` with no provider is schema-valid.
- `internal/adapters`: `TestEveryHarnessInTheContractSchemaHasAnAdapter` passes once the adapter exists; `TestPikaRefusesCommandArgsAndEnv` (the three fail-closed refusals); `TestPikaRequiresAProvider`.
- `internal/workrec`: a `RunAgent` with usage fields round-trips, and a zero-usage agent still omits them.

End to end, through the real binary with a fake provider only — this is what keeps `pika check --ci` provably LLM-free: one scripted provider on `httptest` started by the test, `ANTHROPIC_BASE_URL` pointed at it, `ANTHROPIC_API_KEY` set to a dummy, contract `builder: {runtime: pika, provider: anthropic}`. The script: turn 1 calls `write_file` for the agent's edit, turn 2 returns final text.

- `TestWorkWithALoopBuilder` — `pika work "<goal>"` delivers; the edit is in the commit; the bundle holds `pika-transcript.json`; `record.json` shows runtime `pika` and non-zero usage on the builder; the receipt's `roles` names runtime `pika` with provider `anthropic`.
- `TestLoopBuilderRunsACommand` — the script's first call is `run_command: "go test ./..."`; the command ran (asserted via the transcript, which records the tool result), the run delivered, and the transcript is redacted.

Smoke gains one step, `loop`, after `roles`: the scripted provider on `httptest` inside the smoke program, a contract with `builder: {runtime: pika, provider: anthropic}`, `pika work "<goal>"` with the two env vars, asserting the deliver and that `record.json` names runtime `pika`.

## 8 Completion definition

M7 is complete when:

1. the contract's `harness` enum has eight values and every one resolves to an adapter, enforced by the test that reads the enum from the schema;
2. a contract naming `runtime: pika` with any of the three providers runs a real handoff against a scripted provider: the edit is committed, the final message lands in the bundle, and `pika-transcript.json` sits beside it;
3. `record.json` carries non-zero `calls`/`tokens_in`/`tokens_out` for a loop agent and stays byte-identical to its pre-M7 shape for every other runtime;
4. `pika doctor` reports the loop as binary `in-process` with its provider, in text and in `--json`, and stats no binary for it;
5. `go.mod` still declares exactly two direct dependencies, and `go test ./...` and `pika check --ci` are provably LLM-free — the only provider contact in any test is a local `httptest` server;
6. the loop's three fail-closed refusals (`command`, `args`, `env`) and the turn, token and timeout guards are asserted by test;
7. `.github/workflows/codex-compat.yml` is renamed `adapter-compat.yml` and runs `TestRealAdapterCompatibility` under `PIKA_ADAPTER_COMPAT=1`;
8. `docs/reference/m7-delta.md` exists, design §10 records the reversal, `docs/guides/usage.md` documents the runtime, and `README.md` carries the M7 status block;
9. `go test ./... -count=1` passes, `CGO_ENABLED=0 go build ./...` succeeds, `gofmt -l .` prints nothing, `go vet ./...` is clean, `go run ./internal/smoke` passes, `go run ./cmd/pika check --all` is green, and `go mod tidy && git diff --exit-code go.mod go.sum` is clean.
