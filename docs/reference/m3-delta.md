# Milestone 3 delta — trust, containment, and the upgrade path

A record of what M3 changed, and — at greater length, because it is the part
that is easy to get wrong — what it deliberately did **not** change and why.
It sits alongside [M1.5](m1-5-delta.md) and [M2](m2-delta.md) and does not
edit them: those are the historical evidence this milestone was built from.
Where a claim in one of them is no longer true, it is corrected here and
pointed at from there, never rewritten in place.

The milestone has two halves. §§1–4 are about authorization: M2 shipped a
table of seven envelope classes with two of them enforced, and the obvious way
to close that table is to add five more checks. That would be the wrong move
for four of them, and those sections are the argument for why, with the
evidence attached. An authorization check for an operation that never happens
is dead code wearing a safety costume, and its real cost is that it makes the
envelope look more protective than it is.

§§5–7 are about the upgrade path: `pika init --force` was the one remedy every
upgrade note pointed at and the one command that could destroy an operator's
work, a corrected pack template was invisible to every repository that already
had the old one, and neither problem could be fixed without the other. §7
collects everything an existing repository actually notices.

---

## 1. `fs_read` is enforced

`envelope.Allows` has always answered `fs_read` correctly. What was missing was
a caller. Two MCP tools read the repository on an agent's behalf, and both now
ask before reading:

| Tool | Target authorized | Where |
|---|---|---|
| `inspect_repo` | `"."` — the walk covers the whole tree | `internal/mcp/server.go`, `toolInspectRepo` |
| `read_contract` | the path the caller named, before normalization | `internal/mcp/server.go`, `toolReadContract` |

Both go through `(*server).authorize`, the same choke point the seven
`fs_write` and two `exec` call sites use. No mechanism was added.

`read_contract` asks the envelope about the **raw** argument, not the
normalized one. That ordering is the whole point: `contract.NormalizeRepoPath`
already proves a path is repo-inside, so authorizing the normalized result
would hand `allowsRead` a target it could never refuse. The check would pass
review and enforce nothing.

Both bounds are kept, because they do not overlap completely. An absolute path
*inside* the repository satisfies `allowsRead` and is still rejected by
`NormalizeRepoPath` with `invalid_params` — the tool joins repo-relative paths
onto the server's root and nothing else.

### Read this part before you conclude anything about read scope

**This did not narrow which reads are permitted.** `envelope.Allow` has no
`fs_read` field at all; nothing in `pika authorize` can write one, and
`allowsRead` implements a repo-inside default. Any valid envelope therefore
permits any in-repo read. The only new failure modes are:

1. **no envelope on disk** — previously the read tools worked anyway; now they
   are denied, like every other tool; and
2. **a target outside the repository** — `../../.ssh/id_rsa`, `/etc/passwd` —
   which is now `envelope_denied` rather than `invalid_params`.

So this is a requirement to *declare* a policy, not a tightening of what that
policy allows. Do not describe it as scoping an agent's reads; it does not.

Why require the declaration at all, when it grants everything in-repo anyway:
an agent enumerating a repository before the operator has authorized anything
is a real capability, not a neutral act, and "reads are exempt when no policy
exists" is precisely the pattern that makes an envelope feel optional. The
remedy is one command:

```sh
pika authorize --scope read     # grants no fs_write and no exec at all
```

### What did not move: the human CLI still needs no envelope

`pika check` runs its gates in the operator's own shell and consults no
envelope. It did not before M3 and does not after. The operator authorized
themselves by typing the command; the envelope exists to bound what an agent
may do *on their behalf*. Both directions are pinned by tests —
`TestHumanCLIStillNeedsNoEnvelope` in `internal/mcp` and
`TestE2EHumanCheckNeedsNoEnvelopeButMCPDoes` in `internal/e2e` — because an
asymmetry nobody names is an asymmetry that rots into a bug.

---

## 2. The four kinds that get no check, and the evidence

`network`, `credential`, `github` and `budget` remain unenforced, and this
milestone asserts something stronger than "not yet": the binary performs no
operation of any of those classes, so there is nothing for a check to guard.
Each claim below was verified against this tree.

### `network` — pika opens no socket

The dependency closure of the shipped binary contains no package that can open
a network connection:

```
$ CGO_ENABLED=0 go list -deps ./cmd/pika | grep -E '^(net|net/http|net/rpc|crypto/tls)$'
(no output; 166 packages in the closure)

$ grep -rn --include=*.go -E '"(net|net/http|net/url|net/rpc|crypto/tls)"' internal cmd
(no matches, tests included)

$ grep -rn --include=*.go -E 'net\.Dial|http\.Client|http\.Get|http\.Post|http\.NewRequest|url\.Parse|tls\.' internal cmd
(no matches)
```

`go.mod` declares two direct dependencies — `github.com/goccy/go-yaml` and
`github.com/santhosh-tekuri/jsonschema/v6` — plus `golang.org/x/text`
indirect. None is a network client.

One precision, because a security claim that is 95% right is a liability: the
closure *does* contain `net/url` and `net/netip`. Neither opens anything. They
arrive through `jsonschema/v6` (JSON Schema `format` keywords validate `uri`
and `ipv4`/`ipv6` **strings**) and through `text/template` (the `urlquery`
escaper). They are parsers. `net`, `net/http`, `net/rpc` and `crypto/tls` are
genuinely absent, and no pika source file imports any `net/*` package
directly.

### `credential` — pika reads none and constructs none

No pika code path reads a credential, names one, or selects one to pass on.
The only environment reads in the non-test kernel are in `internal/verify`:

```
$ grep -rn --include=*.go -E 'os\.Getenv|os\.Environ|LookupEnv' internal cmd | grep -v _test.go
internal/verify/verify.go:218:  chain := ladderChain(os.Getenv(LadderEnvVar))
internal/verify/verify.go:226:  rc.gateEnv = gateEnvironment(os.Environ(), ...)
```

`verify.gateEnvironment` copies the parent environment and rewrites exactly one
variable, `PIKA_CHECK_LADDER` (the nested-ladder guard from
[M2 §4](m2-delta.md#4-a-nested-ladder-is-refused-structurally-not-by-convention)).

Be precise about what that means. Child processes — verification gates and the
Codex handoff — **inherit the operator's environment**, exactly as they would
from a shell prompt, and that environment may hold credentials. pika does not
read them, filter them, or choose them. A `credential` check would not change
this in either direction: pika has no credential to gate, and gating one it
never touches would not scrub an inherited environment. Scrubbing is a
different feature with different trade-offs, and it is not in this milestone.

### `github` — no GitHub operation exists

`contract.github.merge` is a scaffolded field and nothing acts on it. It is
written by `initcmd` and `adopt` (both hardcode `"squash"`), carried through
`contract.Schema`, and read by nothing:

```
$ grep -rn --include=*.go 'GitHub' internal cmd | grep -v _test.go
internal/contract/schema.go:80:    Merge string `yaml:"merge" json:"merge"`
internal/initcmd/init.go:426:     GitHub:     contract.GitHub{Merge: "squash"},
internal/adopt/adopt.go:300:      GitHub:     contract.GitHub{Merge: "squash"},
internal/discover/discover.go:104: inv.GitHubWorkflows = listWorkflows(...)   // lists files on disk
... (the remainder are envelope/authorize plumbing and doc comments)
```

`discover.listWorkflows` reads `.github/workflows` off the filesystem — a
directory listing, not an API call. The generated `AGENTS.md` handoff prompt
explicitly forbids the agent from running any GitHub command
(`internal/improve/handoff.go`). There is no API client to authorize.

### `budget` — nothing accounts spend, and `authorize` refuses to pretend

No code compares spend against a ceiling. `pika authorize` cannot emit a budget
key even by accident: its YAML projection type has no budget field
(`internal/authorize/authorize.go`, `renderAllow`), and when it prints the
delta against an existing envelope it labels any budget entry it finds
`(authorize never writes a budget)`. A ceiling nothing enforces is a lie in a
file whose entire job is to be true.

### The updated table

| Envelope class | Enforced? | Where, or why not |
|---|---|---|
| `fs_write` | **Yes** | `internal/mcp/server.go` — `preview_plan`, `acquire_scope`, `release_scope`, `publish_evidence`, `propose_decision`, `record_sources` |
| `exec` | **Yes** | `internal/mcp/server.go` — `run_checks` authorizes each gate's full argv; `preview_plan` authorizes every discovered command its baseline would run |
| `fs_read` | **Yes (new in M3)** | `internal/mcp/server.go` — `inspect_repo`, `read_contract`. Repo-inside default; see §1 for what this does *not* mean |
| `network` | No, and cannot be | the binary opens no socket |
| `credential` | No, and cannot be | pika reads no credential and constructs none |
| `github` | No, and cannot be | no GitHub operation exists; `contract.github.merge` is scaffolded and never acted upon |
| `budget` | No, and cannot be | nothing accounts spend; `authorize` never writes a budget key |

An envelope that grants `network`, `credential` or `github` still grants
nothing in practice. Do not read those entries as protection.

---

## 3. Where the network boundary actually is

pika does not reach the network. Two things it *arranges* do, and both are
worth knowing precisely, because "pika makes no network request" is true and
"nothing in a pika workflow touches the network" is false.

**1. The Codex child process.** `pika handoff` / `pika work` spawn the
operator's harness. pika constrains it through argv
(`internal/improve/handoff.go`):

```go
args := []string{"exec", "-c", "sandbox_workspace_write.network_access=false"}
// ...
return append(args, "--sandbox", "workspace-write", "--approve-for-me",
    "--cd", root, "--output-last-message", outputPath, "-")
```

Read that for what it is. It is a **request pika makes of the child**, honored
by the child's own sandbox. pika does not intercept syscalls, does not proxy,
and cannot verify compliance — if the harness ignores the flag, or is a
different binary under the same name, nothing here stops it. The kernel's
guarantee is that it asks for the sandboxed, network-disabled,
non-approval-bypassing mode every time and never for a danger mode. That is a
real property and it is not the same property as enforcement. An envelope
`network` check would not change it either: the request is not pika's to make
on the child's behalf.

**2. Generated CI.** The scaffolded workflow
(`internal/profiles/packs/core@1/templates/ci.yml.tmpl`) runs
`actions/checkout@v4` and:

```yaml
run: go install "github.com/Choaterboater/pika/cmd/pika@$PIKA_REF"
```

That is a module fetch by the Go toolchain on GitHub's runner. It happens in
your CI, not in pika, and no envelope on any developer machine is consulted for
it. `PIKA_REF` pins it to the release that scaffolded the repository
([M2 §5](m2-delta.md#5-runs-are-durable-and-there-are-four-commands-for-them));
note [M2 gap 1](m2-delta.md#gap-1--a-template-only-pack-change-is-invisible-to-every-adopted-repository)
if your repository predates that fix — and [§6](#6-the-template-blind-spot-is-closed-and-what-it-cost)
for why pika can finally tell you so.

---

## 4. Known limit: the copy-leak

`.project/state/` is local and gitignored — it holds the run's whole working
context (goals, prompts, gate output, the agent's own words) so a run can be
resumed and diagnosed on the machine that ran it.
`.project/evidence/` is committed. The filter that keeps the first out of the
second is path-based.

An agent can defeat it in one move: copy a file out of `.project/state` into a
public path and commit that. What the filter then sees is an untracked add of
an ordinary path — `docs/notes.md`, say — with no relationship to
`.project/state` anywhere in it. **A path-based filter cannot see this, and
this one does not.**

Content-based detection is the obvious answer and it is a bad one here. Gate
output legitimately quotes the same compiler diagnostics, test names and stack
traces that the run record contains, so "this public file contains text that
also appears in local state" fires constantly on correct behavior. The
false-positive surface is large enough that the check would be disabled within
a week, and a disabled check is worse than an absent one because it is still
listed.

What does help, and shipped this milestone: **local state is redacted at the
point of writing.** `internal/mcp` `appendBoard` and `internal/workrec` run
every string through `redact.Apply` before it lands on disk, the same treatment
`evidence.Build` gives everything it emits. So a `cp` out of `.project/state`
now copies placeholders where the credentials were. The path filter is still
one prefix test that has been wrong twice; redacting at write time means the
next filter bug leaks `<redacted:oauth>` instead of a key.

This does not close the leak — a copied file still exposes internal notes,
paths and reasoning. It bounds the worst outcome. The residual is recorded here
rather than solved, because the honest fix is content provenance tracking and
that is a milestone of its own.

---

## 5. `pika init --force` is safe to run

`--force` has always been the only way to refresh a `.project/profiles.lock`
an older pika wrote, so every upgrade note in this repository points at it. It
was also, until this milestone, the command most likely to destroy something.
It rewrote **every** file `init` manages and reset `.project/exceptions.yaml`
to `{}` — so the documented remedy for "your lock is stale" also deleted your
README, your entry point, and every exception a reviewer had accepted.

Two changes, and they are the same change seen from two sides.

**Ownership decides what a regeneration rewrites.** `initcmd`'s `genFile` now
carries a `kernel` flag — set on the contract, on the profiles lock, and on
the two entries of the `coreTemplateTargets` table the kernel owns — and only
those files are rewritten unconditionally:

| Kernel-owned — rewritten | Operator-owned — seeded once |
|---|---|
| `.project/contract.yaml` | `README.md`, `AGENTS.md`, `CONTRIBUTING.md` |
| `.project/profiles.lock` | `.gitignore`, the `docs/` spine placeholders |
| `.github/pull_request_template.md` | the language scaffold (`go.mod`, `cmd/<name>/main.go`, `Package.swift`, …) |
| `.github/workflows/ci.yml` | `.project/exceptions.yaml` |

The line is not taste. The contract, the lock, the PR template and the CI
workflow encode how the kernel wants to be run, so a copy left behind by an
older kernel is the kernel's own defect to correct. Everything else is a
starting point a repository is expected to outgrow.
`.project/exceptions.yaml` is stricter still: it is seeded when absent and
never rewritten, **not even by `--reset-docs`**, because each record is a
rationale, an owner and a review condition a human wrote and a reviewer
accepted. Regenerating documentation is not a reason to discard evidence, and
there is no flag that clears the record.

**Inputs are resolved from the repository, not from the command line.**
Profiles, project name and Go module path each resolve as: the explicit flag,
else the value the repository already declares (profiles and name from the
contract, the module from `go.mod`), else a refusal that names what could not
be recovered. Before this, a bare `pika init --force` in a `go@1` repository
wrote a core-only contract with `commands: {}` — every verification gate
silently gone — and a `go.mod` renamed after whatever directory the operator
happened to be standing in. The refusal matters as much as the read-back: a
Go scaffold whose module can be recovered from neither flag nor `go.mod` is
refused rather than renamed.

`--reset-docs` is the opt-in that restores the old behavior over the
operator-owned files. It requires `--force`; alone it exits 2, because by
itself it could only be a mistyped intention.

### One surprise, stated so it is not discovered

`go.mod` is operator-owned. So **`--module` is inert under a bare `--force`**
in a repository that already has one: the flag reaches the rendered scaffold,
and that `go.mod` is then not written because yours exists. It takes effect on
a fresh `init`, or together with `--reset-docs`. This is the correct
behavior — a scaffolding flag should not rename the module every import in the
repository refers to — but a flag that silently does nothing is a flag an
operator will eventually believe did something, so it is written down here and
in [the usage guide](../guides/usage.md#--force-regenerates-more-than-the-lock).

`TestE2EForceKeepsOperatorWorkAndResetDocsIsTheOptIn` in `internal/e2e` pins
all of this against the real binary: a repository carrying a hand-written
README and a recorded exception, a bare `--force`, and byte-for-byte
comparisons afterwards.

---

## 6. The template blind spot is closed, and what it cost

[M2 recorded this as gap 1](m2-delta.md#gap-1--a-template-only-pack-change-is-invisible-to-every-adopted-repository),
and the description there is still an accurate account of what was true then.
Both halves of it are now false, which is the point.

**Half one: detection.** `PackDigest()` and `PackDigestFor()` hashed pack YAML
only. `core@1.yaml` declares `templates: []`, so not even the template
filenames were covered, and the templates' separate `go:embed` FS was outside
the digest entirely. M2's correction to the scaffolded CI workflow therefore
rotated nothing, and gate 1 had no signal to give. A pack's templates are part
of what the pack is, so both digests now fold them in — every template path in
sorted order, each followed by a NUL and its bytes. A repository scaffolded
from a since-corrected template fails gate 1 with the pack named:

```
profiles.lock: pack core digest e824…2fdf in profiles.lock does not match the
embedded pack core@1; regenerate the lock with `pika init --force`
```

The sort is explicit and redundant today — `fs.WalkDir` walks lexically — on
purpose. A digest whose value depends on an enumeration order nothing states
can rotate without its input changing, and that failure mode is worse than
having no digest at all: every adopted repository fails gate 1 at once with no
diff to point at.

**Half two: the fix.** M2's gap 1 also observed that `pika apply` only ever
created core files a repository was missing — "existing files are skipped with
`already exists; kept the existing file`, never overwritten." That sentence is
no longer true. It is one of the two claims in the M2 delta that describe
present behavior rather than history; the other is its `--force` caveat,
corrected in §5. Both are forward-pointed there rather than rewritten.
Apply now compares the **two kernel-owned files**
against the rendered template and rewrites a stale one through the transaction,
so the refresh rolls back with everything else. Operator-owned files keep
create-if-missing exactly as before, and a kernel-owned file that already
matches is skipped with a reason that says so.

Every refresh is reported as a `write` in the apply report and in the human
summary. A silent kernel rewrite is indistinguishable from an operator's own
edit, and that is precisely how trust in a tool erodes.

### Which command is the remedy, in which state

These two do **not** compose into one story, and pretending they do would
leave an operator running the wrong command:

| Repository state | Stale kernel-owned file is refreshed by |
|---|---|
| unadopted, being adopted now (`adopt` → `apply`) | `pika apply` |
| already adopted (`.project/contract.yaml` exists) | `pika init --force` |

`apply` refuses on an already-adopted repository before it inspects anything
(*"repository already adopted; use `pika check` instead"*) — so its refresh is
reachable only during adoption. For every repository that already ran `init`
or `apply`, and that is every repository this upgrade note is addressed to,
the refresh command is `pika init --force`, which rewrites the same two files
from the same rendering code. Both directions are pinned in `internal/e2e` by
`TestE2EStaleScaffoldIsDetectableAndForceIsTheRemedy` (including the refusal)
and `TestE2EApplyRefreshesAStaleKernelFileAndReportsIt`.

**The cost is one more digest rotation, for everyone.** Every
`.project/profiles.lock` written by an earlier build now fails gate 1. That is
the third rotation in three milestones, and it is why this change shipped
together with §5: an operator told to run `--force` must first be able to run
it without losing their work.

---

## 7. Release notes: what an existing repository notices

Everything above, reduced to the changes that are visible from outside the
binary.

**1. Every `profiles.lock` fails gate 1 until regenerated.** `core@1` rotated
because its templates joined the digest; `go@1`, `python@1`, `rust@1`,
`swift@1` and `typescript@1` ship no templates and did not rotate on that
account. Remedy: `pika init --force`, which now needs no arguments and rewrites
nothing you wrote (§5).

**2. `read_contract` with an out-of-repo path answers `envelope_denied`, not
`invalid_params`.** This is an error-code change on a documented surface — the
code appears in `pika explain`, in the MCP error envelope, and in agent
harnesses that branch on it. The authorization now runs *before*
`contract.NormalizeRepoPath`, on the raw argument, because authorizing the
normalized result would hand `allowsRead` a target already proved repo-inside;
the check would pass review and enforce nothing. `NormalizeRepoPath` still
rejects an absolute path inside the repository with `invalid_params`, so both
codes remain reachable — only the ordering for escaping paths moved.

**3. The MCP read tools need an envelope.** `inspect_repo` and `read_contract`
are denied when `.project/state/envelope.yaml` is absent, where they previously
worked. Read this precisely, because it is easy to overstate and §1 says so at
length: `envelope.Allow` has **no `fs_read` field**, nothing in `pika
authorize` can write one, and `allowsRead` implements a repo-inside default.
Any valid envelope therefore permits any in-repo read. The new requirement is
to *declare* a policy, not a narrower policy. The remedy is
`pika authorize --scope read`, which grants no writes and no exec at all. The
human CLI is unaffected and still consults no envelope.

**4. `pika apply` may now rewrite two files it used to skip.** Only
`.github/pull_request_template.md` and `.github/workflows/ci.yml`, only when
they differ from the rendered template, only during adoption, and always
reported as a `write` (§6).

**5. Two filters that failed open now hold.** Every git path listing pika
parses — `diff --name-only`, `ls-files --others` and `status --porcelain` —
is read with `-z`, so a path git would otherwise quote (a double quote, a
backslash, a control character, or any non-ASCII byte under the default
`core.quotePath`) can no longer evade the
private-state filter that keeps `.project/state/` out of a commit. And
verified paths are staged with `--literal-pathspecs`, so a filename containing
`*`, `?` or `[` is staged as itself rather than as a glob that could drag
untouched files into the commit. Neither changes behavior for a repository
whose filenames are ordinary; both change it for exactly the repository that
was at risk.

**6. `record.json` and the MCP board are redacted at the point of writing.**
The handoff bundle already was. Nothing about this makes `.project/state/`
publishable — it is still gitignored, still unvalidated, and still full of
internal reasoning — but a copy that escapes now carries placeholders where
credentials were (§4). What is deliberately *not* redacted is the record's
identity: work id, kind, phase, branch, commits, role, runtime and outcome are
kernel-generated structural fields that `pika resume` needs to rejoin a run,
and rewriting one would break the resume while protecting nothing.
