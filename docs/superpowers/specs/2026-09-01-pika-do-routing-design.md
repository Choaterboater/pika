# pika Design Specification — `pika do "<goal>"`

**Status:** Approved
**Date:** 2026-09-01
**Product:** `pika`
**Builds on:** [2026-08-31-pika-m7-built-in-loop-design.md](2026-08-31-pika-m7-built-in-loop-design.md)
**Implements:** the deferred command both M6 and M7 named and declined to build
**Does not implement:** the reasoning-based router M6/M7 anticipated — see §3 for why a
narrower, fully deterministic command satisfies the same goal without it

## 1 Purpose

Every milestone from M6 onward has carried the same non-goal, worded almost identically
twice:

> "**No intent router.** `pika do "<goal>"` — one command that picks
> adopt/improve/work/skills from repository state — is deferred. It needs a runtime that
> can reason about intent; a rule-based classifier written now would invent a heuristic
> the kernel cannot justify." (`docs/reference/m6-delta.md:149-152`, restated at
> `docs/reference/m7-delta.md:180-183`)

An operator today has to already know which of three commands applies: `pika adopt` for an
ungoverned repository, `pika improve` for a red ladder, `pika work "<goal>"` for a stated
goal. That is one more decision than the operator should have to make correctly before
pika can help — and getting it wrong (e.g. running `work` with no goal, or `improve` when
nothing is adopted yet) costs a refusal and a re-read of which command means what.

This spec builds `pika do`, but not the router M6/M7 declined to build. §3 explains why
that command is legitimately smaller than what "intent router" implies, and §4 shows the
concrete finding that makes it safe.

## 2 Goals

1. One command, `pika do ["<goal>"]`, that dispatches to the correct existing command
   for the repository's current state, without asking the operator to already know which
   one that is.
2. Zero new risk of misclassification: the routing decision must be exactly as
   deterministic and auditable as the commands it dispatches to.
3. Zero new failure surface: `do` adds no new way to lose work, corrupt state, or bypass
   an existing safety net (clean-tree refusal, run lease, verified recheck, rollback).
4. Preserve every guarantee M1–M7 earned: no new model call, no new contract schema, no
   new dependency, the ladder as the only gate.

## 3 Non-goals

- **No intent inference from goal text.** The M6/M7 non-goal objects to a *rule-based
  classifier* guessing what an ambiguous goal means — literally, "a heuristic the kernel
  cannot justify." `do` never attempts this. It classifies nothing about the *content* of
  the goal; it only asks whether one was given at all, which is a parse, not a guess.
- **No model call.** Considered and explicitly rejected during design (see the feature/
  repair finding in §4): the one
  place a model call could add value — recording `Kind: repair` instead of `Kind: feature`
  when a stated goal actually just describes a red gate — is real but narrow, and adding
  new structured-output machinery pika does not otherwise have was judged not worth it for
  a label. `work`'s own lifecycle already folds a red baseline into the builder's prompt
  regardless of `Kind`, so nothing about the *run* is worse for recording the wrong label.
- **No `skills` routing.** The original phrasing named a fourth destination,
  `pika skills install`. Distinguishing "this goal is about updating skill instructions"
  from "this goal is feature work" needs exactly the kind of content classification §3
  above declines to do. `pika skills install` stays a direct, explicit command.
- **No confirmation prompt.** `do`'s own dispatch adds no interactive gate. The safety
  boundary is the dispatched command's own: `adopt` only ever writes drafts, never the
  live contract; `improve`/`work` refuse a dirty tree, require a verified recheck before
  committing, and roll back on any mid-plan failure. An extra prompt in front of
  mechanisms that already fail closed is ceremony, not safety.
- **No new commands beyond `do` itself.** The command surface moves from 19 to 20 exactly
  once (`cmd/pika/main.go`'s `commands` table gains one entry); `do` does not gain flags
  or subcommands beyond what the three targets it dispatches to already accept.

## 4 Current-state findings

Verified against `main` at `cf5453a`.

| Finding | Evidence |
|---|---|
| Command registry is a linear `[]command` of `{name, summary, usage, run}`; adding one entry is the entire integration point | `cmd/pika/main.go:11-22`, `82-187` |
| `dispatch` looks up `args[0]` and calls `c.run(args[1:], stdin, stdout, stderr)` — the exact signature every command, including a new `do`, already has | `cmd/pika/main.go:216-231` |
| `resolveRoot(explicit)` is the existing helper every command already threads: `--root` bypasses discovery via `repopath.At` (tagging origin `"explicit"` unconditionally); otherwise `repopath.Find` walks up and tags one of `contract`/`draft`/`git`/`cwd` | `cmd/pika/root.go:23-32`; `internal/repopath/repopath.go:45-79` |
| **`repopath.At` never checks for a contract or draft at all — it only validates the directory exists.** `Origin()` therefore cannot distinguish governed from ungoverned whenever `--root` is passed; branching on it would silently mis-route every `do --root <dir>` invocation | `internal/repopath/repopath.go:66-79` |
| `Root.Contract()` / `Root.ContractDraft()` centralize the two paths that actually decide governance state, independent of how the root itself was resolved | `internal/repopath/repopath.go:95-100` |
| `adopt.Preview`'s own governance check is a direct stat on the live contract path, not a read of `Origin()` — the same pattern §5.1 reuses instead of switching on origin | `internal/adopt/adopt.go:240-244` |
| `work` requires exactly one nonempty positional goal, refuses a second, and sets `Kind: feature` before calling `improve.Run` | `cmd/pika/work.go:47-90` |
| `improve` accepts no positional and leaves `Config.Kind` empty | `cmd/pika/improve.go:159-187` |
| Empty `Kind` defaults to repair; repair short-circuits (no branch, no handoff) exactly when the baseline is already green | `internal/improve/improve.go:232-253`, `667-687` |
| Feature work always continues past baseline regardless of its color — a red gate is folded into the builder's prompt alongside the goal, not discarded | `internal/improve/improve.go:667-700`; `internal/improve/handoff.go:344-375` |
| **This is the finding that makes deterministic dispatch safe**: routing "goal given → `work`" never needs to know whether the ladder is red or green first, because `work`'s own lifecycle already handles both correctly | (derived from the two rows above) |
| pika's own model-call machinery (`internal/loop`) has no structured/schema-constrained output — only a fixed three-tool agentic loop whose terminal response is unconstrained text | `internal/loop/loop.go:39-123`; `internal/loop/openai.go:101-106`; `internal/loop/anthropic.go:109-117` |
| Contract `agents:` keys are validated as an arbitrary string-keyed map; nothing about the schema would have blocked a `router` role, had one been needed | `internal/contract/contract.schema.json:32-35` |
| `os.Stat` is the only existence primitive `Root` needs for this; no method on `Root` itself reports "governed" today, matching `adopt.Preview`'s own inline stat rather than a shared boolean helper | `internal/repopath/repopath.go:82-116` (no such method); `internal/adopt/adopt.go:240` |

## 5 Design

### 5.1 Routing logic

The governance check is a direct stat on the two state-file paths, not a read of
`origin.Kind` — §4 above shows why: `repopath.At` (what `--root` uses) never inspects the
directory's contents, so `Origin()` alone cannot tell "governed" from "ungoverned"
whenever `--root` is passed. Two stats, matching `adopt.Preview`'s own check exactly,
work identically whether the root came from discovery or from `--root`:

```go
root, err := resolveRoot(*rootFlag)  // the same helper check/improve/work already use
if err != nil {
    // config error, exit 2
}
_, contractErr := os.Stat(root.Contract())
_, draftErr := os.Stat(root.ContractDraft())
contractExists := contractErr == nil
draftExists := draftErr == nil

switch {
case contractExists:
    if goal == "" {
        dispatch("improve", passthroughArgs)
    } else {
        dispatch("work", append([]string{goal}, passthroughArgs...))
    }
case draftExists:
    fmt.Fprintf(stdout, "a draft already exists at %s — review it and run"+
        " `pika apply`, or re-run `pika adopt` to regenerate it\n", root.ContractDraft())
    return 0
default:
    dispatch("adopt", passthroughArgs)
}
```

`contractExists` is checked first so a live contract always wins even in the
near-unreachable case both files are somehow present at once — the same precedence
`markers`' own ordering gives contract over draft in `repopath.Find`
(`internal/repopath/repopath.go:36-38`).

`dispatch(name, args)` is `lookup(name).run(args, stdin, stdout, stderr)` — the exact
function `main.go`'s own top-level `dispatch` already calls. `do` never re-implements
`adopt`/`improve`/`work`'s own logic; it only decides which one to call and with what
argv, then returns that call's own exit code unmodified.

### 5.2 Command surface

```
pika do ["<goal>"] [--branch <name>] [--agent <key>] [--json] [--root <dir>]
```

- Goal is an optional single positional — zero means "route toward `improve`", one means
  "route toward `work` with this goal", two or more is refused as likely-unquoted input,
  matching `work.go`'s existing parse (`cmd/pika/work.go:47-75`).
- `--branch`, `--agent`, `--root` are passed through verbatim to whichever of
  `adopt`/`improve`/`work` gets dispatched to; `do` does not reinterpret them.
- `--json`: the routing rationale (one line, e.g. `routing: no live contract, dispatching
  to adopt`) always goes to stderr, never into the JSON envelope. Stdout carries exactly
  the dispatched command's own envelope, unmodified — a caller parsing `do --json`'s
  output sees `"command": "improve"` (or `"work"`, or `"adopt"`), not `"command": "do"`,
  because that is what actually ran and what its own schema already describes.

### 5.3 Error handling

- Usage errors (bad flags, 2+ positionals) → exit 2, matching every other command.
- `resolveRoot` failure → exit 2, matching `check.go`'s existing config-error path.
- Neither file existing, or only the draft existing, is not an error: `adopt` dispatch
  and the draft-guidance print both exit 0 — nothing is wrong in either case, the
  repository is just at an earlier stage of the same lifecycle.
- Every other exit code is the dispatched command's own, verbatim.

## 6 Testing

- `cmd/pika/do_test.go`: one test per branch (neither file exists → `adopt` invoked with
  the right args; only the draft exists → guidance printed, nothing dispatched, exit 0;
  contract exists + no goal → `improve` invoked; contract exists + goal → `work`
  invoked with the goal as its positional), plus the usage-error cases (2+ positionals)
  and a case with `--root` pointed at a directory discovery would never have reached on
  its own, proving the stat-based check does not depend on how the root was resolved.
- A real end-to-end test added to `internal/e2e/`: the actual built binary, run against an
  unadopted fixture, ends up writing adoption drafts (proving the `adopt` dispatch is
  real, not mocked); run against a governed, green fixture with a stated goal, ends up
  creating a feature branch and a commit (proving the `work` dispatch threads the goal
  through correctly).

## 7 Definition of done

1. `cmd/pika/do.go` implements the routing in §5.1, registered in `main.go`'s `commands`
   table; the surface moves from 19 commands to 20.
2. `cmd/pika/do_test.go` covers every branch in §6.
3. `internal/e2e/` gains the two real-binary scenarios in §6.
4. `docs/guides/usage.md` documents `pika do` alongside the four commands it dispatches
   to.
5. No change to `internal/contract`, `internal/adapters`, or `internal/loop` — this
   command adds no new schema, no new adapter, no new model call.
6. `go build ./...`, `go vet ./...`, `gofmt -l .` clean; `go test ./... -count=1` green;
   `pika check --all` green with no new warnings beyond the pre-existing
   `file-size-review` set.
