# pika M6 Design Specification — Runtime Adapters

**Status:** Approved
**Date:** 2026-08-31
**Product:** `pika`
**Builds on:** [2026-08-30-pika-m5-agent-skill-layer-design.md](2026-08-30-pika-m5-agent-skill-layer-design.md)
**Implements:** [2026-08-28-pika-design.md](2026-08-28-pika-design.md) §9.1, §10, §18
**Does not supersede:** §10's V1 clause ("`pika` does not implement a new LLM client or coding-agent loop in V1") — that reversal is M7's

## 1 Purpose

The contract schema promises seven agent runtimes and the binary delivers one: `cmd/pika/improve.go:27` declares `const codexRuntime = "codex"` and `configuredCodexRunner` refuses every other value with ``agent %q uses runtime %q; `pika improve` requires runtime codex``.

Six of the seven runtimes the schema accepts are rejected at the only boundary that could run them. The same single-runtime assumption is what makes design §9.1's role set unreachable: pika has no vocabulary for "spawn a second agent under a different runtime," so a run has exactly one agent, whatever the contract says.

M6 closes both gaps. It implements §10's adapter layer as written — every adapter delegates the agent loop to a harness binary — and it does so without reversing §10's V1 clause. pika still makes no model call, opens no socket, and takes no new dependency.

## 2 Goals

1. Make every runtime the contract schema accepts actually runnable.
2. Keep the adapter layer declarative: an adapter is a table entry, not a plugin, and the contract supplies overrides only as data (`command`, `args`, `env`).
3. Give the operator one place to see what a configured agent resolves to — `pika doctor` — without adding a twentieth command.
4. Make design §9.1's `explorer` and `reviewer` real for the first time, as optional contract-driven phases around the builder's handoff.
5. Preserve the guarantees M1–M5 earned: no model call, no network, no new dependency, one run lease, the ladder as the only gate.
6. Pay M5's documentation debt: `docs/reference/m5-delta.md` and a README status block.

## 3 Non-goals

- **No model calls, no network, no new dependency.** pika still never speaks to a provider; adapters spawn harness binaries. §10's V1 clause stands, and `go.mod` keeps exactly two direct dependencies.
- **No intent router.** `pika do "<goal>"` — one command that picks adopt/improve/work/skills from repository state — is deferred. It needs a runtime that can reason about intent, which is M7's built-in loop; a rule-based classifier written now would invent a heuristic the kernel cannot justify.
- **No `pika mcp` wiring into spawned agents.** M1.5 §3 imagined serving the kernel to the spawned harness; M4 made that mostly useless, because `acquire_scope` is refused for every path while a run lease is held. A spawned builder would be handed tools that refuse by design.
- **No new commands.** Adapter introspection lands in `pika doctor`, which already exists, already reports toolchain, and already supports `--json`. The command surface stays at 19.
- **No coordination board.** M4 §6.4's trigger (two concurrent writers) still has not fired; roles here run sequentially inside one run lease.
- **No skill or projection edits.** M6 changes how pika *drives* agents, not how an agent drives pika. Editing `internal/skills/templates/*` or `.agents/skills/*` would rotate every projection digest; nothing in M6 requires it. `pika skills install` stays a no-op this milestone.

## 4 Current-state findings

Verified against `main` at `31fe8ef`.

| Finding | Evidence |
|---|---|
| The schema accepts seven runtimes | `internal/contract/contract.schema.json:92-95` (`omp, codex, claude, gemini, opencode, acp, custom`) |
| The binary spawns one | `cmd/pika/improve.go:27` `const codexRuntime = "codex"` |
| Six of seven are refused at the boundary | `cmd/pika/improve.go:311-313` |
| The runner is codex-shaped, not runtime-shaped | `internal/improve/handoff.go:29-72` (`CodexRunner.Run`, `.args`) |
| The compatibility probe is codex-only and lives in the wrong package | `internal/improve/codexcompat.go`, `EnabledEnv = "PIKA_CODEX_COMPAT"` |
| The probe's CI job is named for codex | `.github/workflows/codex-compat.yml` |
| An agent entry can name runtime, provider, model and effort — nothing else | `internal/contract/contract.schema.json:72-82`, `internal/contract/schema.go:77-82` |
| §9.1 names five roles; the codebase has no second one | design `:395-405` versus absence (no `explorer` or `reviewer` identifier exists outside the skill text) |
| §5.3's own YAML example already shows two runtimes in one contract | design `:185-189` (`builder: runtime: codex`, `reviewer: runtime: omp`) |
| `Config` carries `Agent`, `Runtime` and `Runner` as three fields describing one agent | `internal/improve/improve.go:143-164` |
| The lifecycle has three entry stages and no room for two more | `internal/improve/improve.go:384-388` |
| The record has four phases and one singular role/runtime pair | `internal/workrec/record.go:14-19`, `:58-59` |
| `evidence.ReviewInput{Agent, Finding, Disposition}` exists and no caller populates it | `internal/evidence/receipt.go:107-112`; `internal/improve/receipt.go:127-144` sets `Roles` only |
| The receipt's `roles` array is populated with exactly one entry | `internal/improve/receipt.go:243-256` (`runRoles`) |
| §10's V1 clause is the reason none of this needs a provider | design `:450` |
| §18 requires adapters be "loaded from declarative configuration" | design `:730` |
| The e2e suite proves the whole run with one fake binary named `codex` | `internal/e2e/testdata/fakecodex/main.go` |

## 5 Design

### 5.1 The adapter table

`internal/adapters` — named for what it adapts rather than `internal/runtime`, which would shadow the stdlib package. It imports `internal/contract` and nothing else from the tree, and `internal/improve` imports it; never the reverse.

An adapter is one struct:

```go
type Adapter struct {
	Runtime   string
	Binary    string     // default executable, PATH-resolved
	Transport Transport  // TransportProcess | TransportACP
	Output    OutputMode // OutputStdout | OutputFile
	Support   Support    // {Model, Effort bool}
	Resume    bool
	Env       []string      // env var NAMES forwarded; nil = inherit pika's environment
	CwdFlag   []string      // e.g. {"--cwd"}; nil = set the child process Dir instead
	Help      []string      // argv that prints this runtime's usage, for the compat probe
	Args      func(Spawn) []string
}
```

`Spawn` carries everything one argv needs: `Root`, `PromptPath`, `OutputPath`, `Model`, `Effort`. Placeholders — `{root}`, `{prompt}`, `{output}`, `{model}`, `{effort}` — are substituted by the runner, never by the contract.

| runtime | binary | argv | cwd | prompt | output | model | effort |
|---|---|---|---|---|---|---|---|
| `codex` | `codex` | `exec -c sandbox_workspace_write.network_access=false [--model {model}] [-c model_reasoning_effort="{effort}"] --approve-for-me --cd {root} --output-last-message {output} -` | `--cd` | stdin (`-`) | file | `--model` | `-c model_reasoning_effort="{effort}"` |
| `claude` | `claude` | `-p --output-format text --permission-mode acceptEdits [--model {model}] [--effort {effort}]` | process `Dir` | stdin | stdout | `--model` | `--effort` |
| `omp` | `omp` | `-p --cwd {root} --mode text --approval-mode write --no-session [--model {model}] [--thinking {effort}] @{prompt}` | `--cwd` | `@{prompt}` | stdout | `--model` | `--thinking` |
| `gemini` | `gemini` | `-p --output-format text --approval-mode auto_edit --skip-trust [--model {model}]` | process `Dir` | stdin | stdout | `--model` | unmapped → error |
| `opencode` | `opencode` | `run --format default --auto --dir {root} [--model {model}] --file {prompt}` | `--dir` | `--file {prompt}` | stdout | `--model` | unmapped → error |
| `acp` | `omp` (overridable via `command`) | `acp` | `session/new` params `cwd` | `session/prompt` text block | ACP chunks | unmapped → error | unmapped → error |
| `custom` | `command` (required) | `args` template | `{prompt}` if present | `{prompt}` in args, else stdin | file iff `{output}` appears in args | `{model}` in args | `{effort}` in args |

The `codex` row is the argv already in the tree. It is recorded with its verified version (codex-cli 0.151.0) because an adapter is a claim about somebody else's CLI, and a claim with no version attached cannot be checked later. `gemini` and `opencode` are transcribed from their official documentation — neither is installed on the development machine — so their rows carry that provenance instead of a verified version, and the compatibility probe (§5.5) is the enforcement.

**Permission posture.** Each adapter takes the least dangerous auto-approval its runtime offers, and pika never sends a bypass flag:

- codex: `--approve-for-me` plus `sandbox_workspace_write.network_access=false`, and never `--sandbox` alongside it (codex 0.151.0 exits 2 — the reason the comment at `internal/improve/handoff.go:65-70` exists).
- claude: `--permission-mode acceptEdits`. Edits are auto-approved; Bash remains governed by the harness's own policy.
- omp: `--approval-mode write`, plus `--no-session` so a one-shot handoff leaves no session behind.
- gemini: `--approval-mode auto_edit` and `--skip-trust` (the trust prompt would otherwise block a non-interactive run).
- opencode: `--auto`, the only auto option its CLI documents.
- `custom`: whatever posture the operator's argv states. pika injects none.

**`Support` is fail-closed.** A contract that sets `effort` on a runtime with no effort control is an error naming all three facts, never a silently dropped field. Design §10 says a fallback must be inside the authorized envelope and recorded; an unexpressible control is not a fallback, it is a configuration mistake, and it is refused before a process is spawned.

**`Flags` is derived.** `Adapter.Flags` calls `Args` and keeps every token beginning with `-`, dropping the bare `-` stdin sentinel and every value. That is exactly how `sentCodexFlags` derives codex's flags today (`internal/improve/codexcompat.go:44`), and generalizing it means no adapter ever keeps a second hand-maintained flag list — the failure the spec names twice, in §5.2 and §16, in the projection context.

### 5.2 Prompt and output contract

Every adapter gets its prompt as a file, never as an argv element that could be misquoted. `Spawn.PromptPath` is the absolute path `createHandoff` already writes. How it reaches the agent is per-adapter: stdin for the runtimes that read stdin, a `@{prompt}` positional for omp, a `--file` flag for opencode, a text content block for ACP, and `{prompt}` inside an operator-supplied template for `custom`.

The final message comes back one of two ways, and the runner knows which:

- `OutputFile` — the harness writes it to `{output}` itself (codex's `--output-last-message`).
- `OutputStdout` — the child's stdout is the message, so the runner tees it: `cmd.Stdout = io.MultiWriter(os.Stdout, rawFile)`. Streaming to the operator is preserved, and the message is captured.

Either way the run ends with one raw path and one redacted path, and `redactResult` is unchanged.

`ProcessRunner` replaces `improve.CodexRunner` for every transport but ACP, and its steps are the ones `CodexRunner.Run` already takes: resolve the binary with `exec.LookPath`, build argv, choose `cmd.Dir` or a cwd flag, attach stdin unless the template consumed `{prompt}`, attach stdout/stderr, run, and wrap failure as `fmt.Errorf("%s handoff: %w", runtime, err)`.

`ACPRunner` speaks ACP v1 over the child's stdio with stdlib `encoding/json` and `bufio` — no SDK, and therefore no dependency. It calls `initialize` with `"protocolVersion": 1`, `session/new` with `{cwd: root, mcpServers: []}`, and `session/prompt` with the prompt file's contents as one text block; it concatenates `session/update` notifications whose `sessionUpdate` is `agent_message_chunk`, in arrival order. A major-version mismatch in the `initialize` reply closes the connection and errors naming both versions.

It answers `session/request_permission` by selecting the first option with `kind: "allow_once"`, falling back to `reject_once`, and never `allow_always` — a remembered grant outlives the run that authorized it, and pika has no mechanism to revoke one. Each decision is logged to stderr as `acp: <allow|reject> <tool title> (<kind>)`, because a permission decision the operator cannot see is a decision the operator did not make. When the context is done it replies `{"outcome":{"outcome":"cancelled"}}`.

**Environment.** `Env == nil` means the child inherits pika's environment exactly as today. A declared allowlist passes only those names, plus `PATH`, `HOME` and `TMPDIR` when present — exec essentials, not secrets. A declared name that is unset in pika's environment is an error, not an empty variable: §10 asks for "environment-variable references, never embedded secret values," and the schema enforces that by pattern (`^[A-Za-z_][A-Za-z0-9_]*$`), so a value fails the pattern and a name passes.

### 5.3 Roles and phases

Design §9.1 names five roles. M6 implements three of them, and only in the shape the binary can actually support:

```go
type Role struct {
	Name   string // "builder" | "explorer" | "reviewer"
	Agent  string // contract key the agent was resolved from
	Runner Runner
}

type Config struct {
	Root, Branch, Kind, Goal string
	Check CheckFunc
	Builder  Role   // required
	Explorer *Role  // nil = the explore phase is skipped
	Reviewer *Role  // nil = the review phase is skipped
}
```

`lead` and `researcher` stay out. `lead` is the operator — pika does not own intent, and §9.4 says so. `researcher` studies documentation and public GitHub patterns, which is the one thing no deterministic gate can check and the one thing a review of this design cannot justify building before M7.

Roles are bound by contract key, not by new flags. `builder` is required and `--agent` keeps selecting it; `explorer` and `reviewer` are resolved from the contract keys of those names, and "not configured" means skip. A default contract therefore still runs exactly one agent, and the phase sequence stays `baseline, handoff, recheck, deliver`.

Two stages join the three that exist:

```go
const (
	stageBaseline stage = iota
	stageExplore
	stageHandoff
	stageRecheck
	stageReview
)
```

- **Explore**, after the baseline stamp and before the builder: a handoff into `handoff/explore` with `buildExplorePrompt(goal, failed)`. It is research, not repair, so the run requires a clean working tree afterwards — `git status --porcelain` must be empty, on top of `createHandoff`'s existing HEAD/branch/refs equality check. Its redacted final message is appended to the builder's prompt as a `## Explorer findings` section, tail-truncated to the last 8 KiB (the bound `evidence.Build` already uses) with a marker line naming the truncation.
- **Review**, after the recheck passes and before the commit: a handoff into `handoff/review` with `buildReviewPrompt(...)`, stamped `PhaseReview`. It is **advisory**: it never sets the outcome and never gates the commit. pika's own rule is that the ladder is the evidence and prose is not a gate; a reviewer that could block a green ladder would be a second gate that is not deterministic, which is the thing M1 was built to avoid. It is recorded because a review that leaves no trace is a review nobody can audit.

`resumeStage` maps both new phases: `PhaseExplore` → `stageHandoff` (the explore product is a prompt section that a resumed run rebuilds from scratch, so it is not durable in the way a baseline report is), and `PhaseReview` → `stageRecheck`. Because both phases are skipped when unconfigured, the stamp sequence a default contract produces is bit-identical to today's.

`workrec.RunAgent{Role, Agent, Runtime}` records every agent a run actually spawned, in spawn order, and `Record.Agents` carries them. `Record.Role` and `Record.Runtime` stay — an in-flight M5 record has them and `pika resume` must still rejoin it.

### 5.4 Doctor introspection

`pika doctor` gains an **Agents** section after the gates and toolchain rows, emitted only when a contract exists and declares at least one agent. Per configured agent it reports: the contract key, the runtime, the resolved adapter, the binary path from `exec.LookPath` or `not on PATH`, model and effort mapping as `mapped` or `unmapped`, the output mode, `resume: yes|no`, and any compatibility findings when `PIKA_ADAPTER_COMPAT=1`. The `--json` result carries the same objects under `agents`.

Doctor never spawns a harness: `exec.LookPath` is a stat, and the one probe that runs a binary stays behind its environment variable.

### 5.5 Compatibility probe

`CheckCompatibility(ctx, Adapter)` generalizes `CheckCodexCompatibility`: it runs `Adapter.Help`, parses declared flags with the same clap-format regex, and reports every flag `Adapter.Flags` constructs that the binary no longer declares. It stays gated on an environment variable — `PIKA_ADAPTER_COMPAT`, replacing `PIKA_CODEX_COMPAT` with no alias — because calling a harness binary is not appropriate for `go test ./...`.

Two properties carry over and generalize:

- **It calls no model and spends no tokens.** `--help` is a static usage dump.
- **It reads flags from a real `Args` call**, so a flag added to any adapter tomorrow is covered without anyone remembering to update a parallel list.

The test that runs it against real binaries skips adapters whose binary is absent, so it is a no-op where nothing is installed and reports every installed one. `.github/workflows/codex-compat.yml` becomes `adapter-compat.yml`.

## 6 Error handling

Every one of these is produced before a process is spawned, and each names the agent, the fact, and the remedy:

| Error | Why it exists |
|---|---|
| `agent %q uses runtime %q; no adapter implements it` | The schema and the table disagree; M6 keeps the old wording's shape for the case that still holds. |
| `agent %q declares runtime custom with no command` | `custom` has no binary of its own. |
| `agent %q sets model %q; runtime %q has no model control` | Fail-closed on `Support`. |
| `agent %q sets effort %q; runtime %q has no effort control` | Same. |
| `agent %q declares env %q, which is not set in pika's environment` | A reference to nothing is a reference, not an empty string. |
| `agent %q builds an unknown placeholder %q` | Only five placeholders are substituted; a typo is otherwise a silent no-op. |
| `agent %q overrides args for runtime %q but drops {output}; the final message would never be written` | The bundle would be missing the file the receipt describes. |
| `agent %q: runtime %q needs %q on PATH` | Wrapping `exec.LookPath`, naming runtime and binary. |
| `agent %q is not configured in %s` | Unchanged; the operator's existing muscle memory for this message still works. |

Two hazards are documented rather than fixed, because fixing either is a larger change than this milestone:

- **A harness that stops on a permission prompt blocks the run.** pika has no per-handoff timeout today (`context.Background()` throughout the lifecycle) and cannot interrupt a loop it did not write. A new timeout is a behavioural change to every run, including the ones that work.
- **A child that shells back into pika still hits `ErrNestedRun`** via `PIKA_CHECK_LADDER` (`internal/verify/verify.go:43`). M6 inherits that unchanged; it is now likelier, because more runtimes means more shells, and the refusal names itself.

## 7 Testing

Unit, in `internal/adapters`:

- `TestEveryHarnessInTheContractSchemaHasAnAdapter` — `contract.HarnessEnum()` × `adapters.Lookup`, so adding a schema value without an adapter fails.
- `TestArgsPerAdapter` — golden argv per runtime, with and without model and effort.
- `TestEveryAdapterArgvCarriesItsOwnPermissionPosture` — no adapter emits a bypass flag; assert the absence of `--dangerously-skip-permissions`, `bypassPermissions`, `yolo`, `--approval-mode yolo`, `--allow-dangerously-skip-permissions`.
- `TestEffortIsRefusedWhereTheRuntimeHasNoEffortControl`, `TestCustomAdapterRequiresACommand`, `TestUnknownPlaceholderIsRefused`, `TestOutputFileRequiresTheOutputPlaceholder`, `TestEnvAllowlistPassesOnlyTheDeclaredNames`, `TestAnUnsetDeclaredEnvVarIsRefused`, `TestMissingBinaryIsRefusedNamingRuntimeAndBinary`.
- `TestProcessRunnerTeesStdoutAndRedactsTheMessage`.
- `TestACPClientDrivesAFakeAgent`, `TestACPSelectsAllowOnceAndNeverAllowAlways`.
- `TestCompatibilityProbeNamesARemovedFlag`, `TestCompatibilityProbeFindsNothingWhenTranscriptMatches`, `TestRealAdapterCompatibility` (env-gated).

End to end, through the real binary with fake agents only — this is what keeps `pika check --ci` provably LLM-free:

- `TestWorkWithAClaudeBuilder` — argv carries `-p`, `--output-format text`, `--permission-mode acceptEdits`; the receipt's `roles` names runtime `claude`.
- `TestTwoRuntimesInOneWorkRun` — builder `codex`, reviewer `omp`; two argv records, `review` in `phases`, two receipt roles, one review finding with the advisory disposition.
- `TestExplorerFindingsReachTheBuilderPrompt` — explorer `gemini`; the builder's `prompt.md` carries the explorer's marker and the bundle exists under `handoff/explore`.
- `TestMissingBinaryRefusesWithAnActionableMessage` — the message names the runtime and the binary.

Smoke gains one step, `roles`, after `improve-again`: a scaffolded repository whose contract names a `claude` builder and an `omp` reviewer, run against the fakes, asserting both runtimes in `record.json` and `review` in `phases`.

## 8 Completion definition

M6 is complete when:

1. every value in the contract's `harness` enum resolves to an adapter, enforced by a test that reads the enum from the schema;
2. a contract naming any runtime runs under it, and a contract naming `custom` runs the operator's argv;
3. `explorer` and `reviewer` are contract-driven phases that a default contract skips without changing the phase sequence it stamps;
4. the review is recorded in the receipt's `review` array with an advisory disposition and never gates the commit;
5. `pika doctor` reports every configured agent's resolution, in text and in `--json`;
6. no adapter emits a permission-bypass flag, asserted by test;
7. pika still makes no model call, opens no socket, and declares exactly two direct dependencies;
8. `docs/reference/m5-delta.md` exists and `README.md` carries status blocks for M5 and M6;
9. `go test ./... -count=1` passes, `CGO_ENABLED=0 go build ./...` succeeds, `gofmt -l .` prints nothing, `go vet ./...` is clean, `go run ./internal/smoke` passes, and `go run ./cmd/pika check --all` is green.
