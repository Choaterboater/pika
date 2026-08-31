# Milestone 7 delta — the built-in loop

A record of what M7 changed, what it deliberately did not, and the gaps it
leaves. It sits alongside [M1.5](m1-5-delta.md), [M2](m2-delta.md),
[M3](m3-delta.md), [M4](m4-delta.md), [M5](m5-delta.md) and
[M6](m6-delta.md) and edits none of them.

M7 reverses one sentence, and it is the sentence M6 said was M7's to
reverse: design §10's V1 clause — "`pika` does not implement a new LLM
client or coding-agent loop in V1" — is replaced. pika gains a built-in
coding-agent loop as an eighth contract `harness` value, `pika`, and
speaks to a provider over stdlib `net/http` for the first time: no SDK,
no new dependency, and `go.mod` still declares exactly two direct
dependencies. Every existing harness binary stays exactly as it is; the
loop is one more runtime, selected the same way, driven by the same
`Runner` contract M6 built.

---

## 1. The eighth runtime, and how it is selected

The loop is not a command. It is one more value of the closed runtime
set, selected exactly where the other seven are:

```yaml
agents:
  builder:
    runtime: pika
    provider: anthropic
```

It is the only runtime with no binary, no argv and no `--help`: it runs
in-process, writes its own final message to the path the run gives it
(`output: file`), and takes `model` and `effort` as provider controls.
`provider` is required, and the three refusals below are produced **before
a request is made** — a loop that cannot pick a client, or that has no
key, is a configuration mistake, not a run:

```
pika work: agent "builder" declares runtime pika with no provider
pika work: agent "builder" declares provider "gemini"; runtime pika speaks anthropic, openai and openrouter
pika work: agent "builder": provider "anthropic" needs ANTHROPIC_API_KEY in the environment
```

The key is read from pika's own environment, never from the contract — a
credential in a contract is a credential in every clone of the repository,
the same rule `env` has followed since M6. For the same fail-closed
reason, `command`, `args` and `env` on a `pika` runtime are errors rather
than silently dropped fields: they are subprocess-only controls, and a
subprocess-free runtime has no use for them.

The guarantees a run holds over the loop are identical to the ones it
holds over a harness binary: the Git-state equality check, the read-only
rule for explorer and reviewer (refused after the fact by
`requireNoNewChanges`, exactly as a harness that wrote would be), and the
recheck ladder. None of them knows or cares which runtime produced the
change.

## 2. The provider table

Two wire shapes, three providers, one loop. The turn loop, the tools and
the limits are provider-agnostic; only how a message, a tool call and a
token count travel differs.

| Provider | Wire shape | Endpoint | Key variable | Base-URL override | Default model |
| --- | --- | --- | --- | --- | --- |
| `anthropic` | Anthropic Messages | `https://api.anthropic.com` | `ANTHROPIC_API_KEY` | `ANTHROPIC_BASE_URL` | `claude-sonnet-4-5` |
| `openai` | OpenAI-compatible Chat Completions | `https://api.openai.com/v1` | `OPENAI_API_KEY` | `OPENAI_BASE_URL` | `gpt-5-codex` |
| `openrouter` | OpenAI-compatible Chat Completions | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | `OPENROUTER_BASE_URL` | `anthropic/claude-sonnet-4-5` |

The contract's `model` overrides the table's default; `effort` maps to
the provider's own reasoning control (`thinking` on Anthropic,
`reasoning_effort` on OpenAI-compatible) and is omitted when unset. The
base-URL override is the testing seam, and it is the only one: a test
points it at a local `httptest` server, which is what keeps the whole
suite — and `pika check --ci` — provably LLM-free. The only provider
contact in any test is a local server the test started.

Retries apply to responses, never to effects: `429`, any `5xx` and
transport errors retry up to four times after the initial attempt (1s,
2s, 4s, 8s, honouring a `Retry-After` under 60s). Every other `4xx` is a
fact to surface verbatim — `pika loop: anthropic 401: …` — with the body
redacted and tail-truncated to 2 KiB, because a provider error page is
exactly where a leaked key or a path would otherwise travel to a
terminal. A timed-out call is never repeated: it may already have
produced tool effects on the far side.

## 3. The tool set, and the unrestricted-exec posture

Three tools, and no sandbox:

| Tool | What it does | Bound |
| --- | --- | --- |
| `read_file` | Reads a repository-relative file | first 32 KiB, head-truncated with a marker |
| `write_file` | Writes the full new content at `0644`, creating parents | the run's own tree checks hold it to the role |
| `run_command` | Any shell command, in the repository root | combined output tail-truncated to the last 8 KiB; 10-minute per-command timeout |

`run_command` is **unrestricted** — `sh -c` on non-Windows, `cmd /c` on
Windows — and that is an operator's choice, stated plainly rather than
softened: it mirrors the most permissive posture any M6 adapter takes,
because a builder that cannot run the repository's own checks is a
builder that guesses. What holds the tool to its purpose is the run
itself: every command is logged into the transcript, redacted, bounded
by the timeout and the turn/token caps below, and a model that writes
where its role may not is refused by the same tree checks that refuse a
harness binary. A non-zero exit is not an error result — it is a command
that failed, which the model is supposed to see. The system prompt tells
the model never to commit, merge, rebase, push or call GitHub: pika
verifies and commits approved changes itself.

All paths are repository-relative and pass the same containment rule the
contract applies to declared paths — absolute paths, drive letters, `~`
and traversal above the root are refused — and anything under
`.project/state/` is refused outright: a model has no business reading or
writing its own run record. A refused path, a missing file or an unknown
tool is an error *result*, not a run failure: the model sees the reason
and self-corrects.

## 4. The guards, and the timeout M6 could not have

Two runaway guards, both constants rather than configuration, because a
model stuck in a tool loop or burning context is a defect to surface,
not a budget to tune:

- **40 turns.** `pika loop: turn limit reached (40)`.
- **400,000 tokens** across the run's calls. `pika loop: token limit
  reached (400000)`.

Each provider call carries its own **5-minute timeout**. This is the gap
M6 documented — "a harness that stops on a permission prompt blocks the
run," unfixable because pika cannot interrupt a loop it did not write —
becoming solvable for the one loop pika *did* write: pika owns this
loop, so it owns the timeout. A call that exceeds it aborts the run with
`pika loop: request timed out after 5m` and is never retried. The
subprocess runtimes keep M6's documented gap; nothing about them
changed.

## 5. Usage, the transcript, and an untouched receipt

What a run spent is recorded in two places, and the evidence receipt is
not one of them:

- **`record.json`.** Each `agents[]` entry gains `calls`, `tokens_in`
  and `tokens_out`, all `omitempty` — kernel-generated counters, left
  alone by redaction like `role` and `runtime`. Only the loop can report
  them; a subprocess runner would be guessing, so the fields stay absent
  for every other runtime, and a record written before M7 encodes
  byte-identical to before.
- **`pika-transcript.json`.** A new bundle artifact at `0600`, written
  beside the final message: the whole conversation plus the accumulated
  usage, marshalled indented and run through `redact.Apply` before
  writing — file contents and command output that went to the provider
  raw must not land in a persisted file raw.

The receipt is closed and pinned at schema 1. Bumping it for a number a
record already carries is churn for no attestation, so it was not
bumped.

## 6. What an existing repository notices

**Nothing.** Every other runtime is byte-identical, and a contract that
names no `pika` agent spawns nothing new, persists nothing new and sees
no new field: the usage counters are omitted when zero, the transcript
is written only by loop runs, and the receipt's schema is untouched.
`pika doctor` gains a `provider` field and reports the loop's binary as
`in-process` instead of a path — it still spawns nothing, and the compat
probe skips the loop by construction because there is no `--help` to
diff.

One repair an existing repository *does* notice, in CI: the scheduled
compat job M6 left behind still referenced `TestRealCodexCompatibility`
and `PIKA_CODEX_COMPAT`, both deleted by M6, so it failed on schedule.
It is renamed `.github/workflows/adapter-compat.yml`, runs
`TestRealAdapterCompatibility` under `PIKA_ADAPTER_COMPAT=1`, and stays
scheduled-only — a renamed flag in somebody else's release is still not
a reason to redden every pull request's merge gate.

## 7. Known gaps, deliberately left open

- **No intent router.** `pika do "<goal>"` stays deferred. It needs a
  design about what intent is, and a rule-based classifier written now
  would invent a heuristic the kernel cannot justify. M7 is the loop
  alone.
- **No envelope enforcement on the loop.** Runtimes are not
  envelope-checked and never have been; the capability envelope governs
  MCP-served agents, and a harness binary M6 spawns is not checked
  either. Adding one here would invent a policy for a harness M6 never
  checked.
- **No budget enforcement.** The envelope's `budget` kind is
  membership-only and `pika authorize` refuses to write it. Spend is
  recorded, not capped by policy; the turn and token caps are runaway
  guards, constants rather than budget. If budget attestation is ever
  required, it is a receipt schema-2 bump carrying the per-role usage
  the record already has — a future milestone's decision, not an
  accident of this one.
- **No receipt schema change.** Usage lives in `record.json` and the
  bundle transcript. The receipt is closed and pinned at schema 1.
- **No `pika mcp` wiring.** M4 made serving the kernel to a spawned
  agent useless — `acquire_scope` is refused for every path while a run
  lease is held — and the loop changes nothing about that.
- **No new commands.** The loop is contract-driven and introspected by
  `pika doctor`. The command surface stays at 19.
- **The two wire shapes are written from documentation, not from a live
  call.** Verifying against a real provider would spend a key and
  tokens. If a provider rejects a field in practice, its own error
  surfaces verbatim — status and redacted body — the client mapping is
  corrected, and the verified shape is recorded in the spec. No test
  depends on a live provider, so nothing here is blocked on one.
- **Default model names may stop being current.** A default the provider
  no longer serves produces a `404` naming the model verbatim; the
  operator sets `model` in the contract. The default is a convenience,
  documented as one.
