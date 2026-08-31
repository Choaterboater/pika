# Milestone 6 delta — runtime adapters

A record of what M6 changed, what it deliberately did not, and the gaps it
leaves. It sits alongside [M1.5](m1-5-delta.md), [M2](m2-delta.md),
[M3](m3-delta.md), [M4](m4-delta.md) and [M5](m5-delta.md) and edits none of
them.

M6 closes one gap and it is stated in a single sentence: **the contract schema
has accepted seven agent runtimes since M1, and the binary spawned one of
them.** Six of the seven were accepted by the schema and refused at the only
boundary that could run them:

```
pika work: agent "builder" uses runtime "claude"; `pika improve` requires runtime codex
```

Closing it also makes design §9.1's role set reachable for the first time.
One run can now spawn a builder, an optional explorer before it, and an
optional reviewer after the recheck — each under its own runtime.

---

## 1. `internal/adapters`, and what an adapter is

An adapter is a table entry, not a plugin. It names a binary, builds one argv,
and declares how the runtime hands back its final message:

| Runtime | Binary | Permission posture | Model | Effort |
| --- | --- | --- | --- | --- |
| `codex` | `codex` | `--approve-for-me`, network disabled | `--model` | `-c model_reasoning_effort=` |
| `claude` | `claude` | `--permission-mode acceptEdits` | `--model` | `--effort` |
| `omp` | `omp` | `--approval-mode write`, no session left behind | `--model` | `--thinking` |
| `gemini` | `gemini` | `--approval-mode auto_edit`, trust prompt skipped | `--model` | not supported |
| `opencode` | `opencode` | `--auto` | `--model` | not supported |
| `acp` | `omp` (`command` overrides) | the agent's own questions, answered `allow_once` | not supported | not supported |
| `custom` | `command` (required) | whatever your argv states | `{model}` in `args` | `{effort}` in `args` |

Each posture is the least dangerous auto-approval its runtime offers, and no
adapter emits a bypass flag. A test asserts the absence of
`--dangerously-skip-permissions`, `bypassPermissions`, `yolo` and their
spellings, so the day one comes back the suite fails.

The `codex` row is the argv that has been in the tree since M2, byte for byte,
and it is why the sandbox comment still stands: `--approve-for-me` already runs
shell commands under the workspace-write sandbox, and codex 0.151.0 **exits 2**
on `--sandbox` beside it. Every handoff died parsing its own arguments before
the agent read a byte of the prompt. The absence of `--sandbox` is pinned by a
test, exactly as its presence used to be.

`gemini` and `opencode` are transcribed from their published CLI reference —
neither was installed on the machine this was written on. That is stated
plainly rather than hidden, because an adapter is a claim about somebody
else's program. The compatibility probe is the enforcement: set
`PIKA_ADAPTER_COMPAT=1` and pika diffs each adapter's flags against the
installed binary's own `--help`, which is a static usage dump — no model call,
no tokens. A flag that has been renamed or removed is named.

## 2. A control the runtime cannot express is an error

This is the rule that matters most, because the alternative was silent:

```
pika work: agent "builder" sets effort "high"; runtime "gemini" has no effort control
```

A contract that sets `model` or `effort` on a runtime with no such control is
refused **before anything is spawned**. The same is true of every other
configuration mistake pika can see coming: a `custom` runtime with no
`command`, a template with a placeholder outside the five that exist, an
`env` name that is not set in pika's own environment, and an `args` override
that drops `{output}` from a runtime that writes its message to a file.

`env` holds variable **names**, never values, and the schema refuses a value by
pattern. A credential in a contract is a credential in every clone of the
repository.

## 3. Three roles, and two rules about the optional ones

The contract names roles by key. `builder` is required; `explorer` and
`reviewer` are optional.

```yaml
agents:
  builder:
    runtime: codex
  reviewer:
    runtime: omp
```

- **The explorer and the reviewer are read-only.** A run whose explorer or
  reviewer changed a file the run had not already accounted for is refused,
  naming the role and the path. An explorer that edited the tree would be
  doing the builder's work twice, from a prompt the builder never saw.
- **The review is advisory.** It is recorded in the run's receipt with the
  disposition `advisory: recorded, not a gate`, and it never gates the commit.
  pika's own rule is that the ladder is the evidence and prose is not a gate;
  a reviewer that could block a green ladder would be a second gate that is
  not deterministic, which is the thing M1 was built to avoid.

The explorer's message is handed to the builder as a `## Explorer findings`
section of the builder's own prompt, tail-truncated to the last 8 KiB with a
marker line saying it truncated — the same bound the receipt uses for captured
output, for the same reason: the builder needs the finding, not the
transcript.

## 4. What an existing repository notices

**Nothing breaks.** A contract that names only a `builder` runs exactly as it
did before M6: one agent, and the phase sequence `baseline, handoff, recheck,
deliver`. The two new phases are skipped when unconfigured, and the receipt's
`agents` array is omitted when a run spawned one.

Concretely:

- **The bundle's message file is named for the runtime that wrote it** —
  `<runtime>-last-message.md`. For `codex` that is byte-identical to the
  `codex-last-message.md` every milestone before M6 wrote, so the documented
  bundle layout still holds and muscle memory still works.
- **`--agent` still selects the builder**, and its flag help no longer claims
  the runtime must be Codex.
- **`pika doctor` gained an Agents block**, printed after the findings and
  carried in `--json` as `agents`. It spawns nothing.
- **`record.json` gained `agents`**, a list of every agent the run spawned in
  spawn order. `role` and `runtime` stay: a record written before M6 carries
  them and `pika resume` has to rejoin one without reading a field that did
  not exist when it was written.
- **The receipt's `review` array is populated for the first time.**
  `evidence.ReviewInput` has existed since M1 with no writer; a run with a
  reviewer now fills it, and a run without one leaves it empty rather than
  claiming a review happened and declined to say anything.
- **An agent's stream goes to stderr, not stdout.** pika's stdout is a
  machine-readable channel the moment `--json` is in play, and a harness
  streaming progress there interleaves with the envelope and corrupts it. A
  terminal shows either stream alike. This was found by an end-to-end test,
  not by design: `pika work --json` against a `claude` builder produced
  unparseable output until it was fixed.
- **No `.project/profiles.lock` was rotated.** M6 changed no pack YAML and no
  pack template.

## 5. What was deliberately not built

- **No model call, no network, no new dependency.** pika still never speaks to
  a provider; every adapter delegates the agent loop to a harness binary.
  Design §10's V1 clause — "`pika` does not implement a new LLM client or
  coding-agent loop in V1" — stands unmodified, and `go.mod` still declares
  exactly two direct dependencies. Reversing it is M7's, and the seam this
  milestone builds is where it lands: nothing in `Adapter` or `Runner` is
  specific to a subprocess.
- **No intent router.** `pika do "<goal>"` — one command that picks
  adopt/improve/work/skills from repository state — is deferred. It needs a
  runtime that can reason about intent; a rule-based classifier written now
  would invent a heuristic the kernel cannot justify.
- **No `pika mcp` wiring into spawned agents.** M1.5 §3 imagined serving the
  kernel to the spawned harness; M4 made that mostly useless, because
  `acquire_scope` is refused for every path while a run lease is held. A
  spawned builder would be handed tools that refuse by design.
- **No new commands.** Adapter introspection landed in `pika doctor`, which
  already existed and already reported toolchain. The command surface stays at
  19.
- **No coordination board.** M4 §6.4's trigger — two concurrent writers — has
  still not fired. Roles here run sequentially inside one run lease.
- **No skill or projection edits.** M6 changes how pika *drives* agents, not
  how an agent drives pika. Editing the skill templates would have rotated
  every projection digest for no gain.

## 6. Known gaps, deliberately left open

- **A harness that stops on a permission prompt blocks the run.** pika has no
  per-handoff timeout — `context.Background()` throughout the lifecycle — and
  cannot interrupt a loop it did not write. Adding a timeout is a behavioural
  change to every run, including the ones that work, so it is documented
  rather than fixed. The ACP transport is the exception: it answers permission
  questions, and it never answers `allow_always`, because a remembered grant
  outlives the run that authorized it and pika has no mechanism to revoke one.
- **The ACP transport has no resume.** `Adapter.Resume` is `true` for ACP only,
  and pika never uses it. Session resume is a future milestone's work.
- **`gemini` and `opencode` are unverified against a real binary.** Their
  flags come from documentation. If the probe ever disagrees, the fix is one
  table entry and a recorded version in the spec — and no test depends on a
  real install, so nothing here is blocked on one.
- **A child that shells back into pika still hits `ErrNestedRun`** via
  `PIKA_CHECK_LADDER`. M6 inherits that unchanged, and it is now likelier,
  because more runtimes means more shells. Documented rather than fixed.
- **`claude` is assumed to read its prompt on stdin.** Its `--help` documents
  `-p` as "print response and exit"; verifying by piping a real prompt would
  spend tokens. If a real Claude Code handoff produces no output, the fix is
  to pass the prompt text as a trailing positional and note the argv-size
  trade-off.
- **ACP's update shape is accepted in two spellings.** ACP v1 nests the
  discriminated union under `params.update`; a flat body is accepted too,
  because that is the shape several early agents shipped. Refusing one nesting
  over the other would turn a readable stream into silence for a reason
  nobody can see.
- **The doctor's "no adapter implements this runtime" row is unreachable
  through a contract that loaded.** The runtime came from the schema's enum
  and a test keeps that enum and the table the same length. It is still
  reported rather than printed as configured, and it is tested directly.
