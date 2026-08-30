# Milestone 3 delta — envelope enforcement

A record of what M3 changed about authorization, and — at greater length,
because it is the part that is easy to get wrong — what it deliberately did
**not** change and why. It sits alongside [M1.5](m1-5-delta.md) and
[M2](m2-delta.md) and does not edit them: those are the historical evidence
this milestone was built from.

M2 shipped a table of seven envelope classes with two of them enforced. The
obvious way to close that table is to add five more checks. That would be the
wrong move for four of them, and this document is the argument for why, with
the evidence attached. An authorization check for an operation that never
happens is dead code wearing a safety costume, and its real cost is that it
makes the envelope look more protective than it is.

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
if your repository predates that fix.

---

## 4. Known limit: the copy-leak

`.project/state/` is local and gitignored — it holds unredacted operational
state so a run can be resumed and diagnosed on the machine that ran it.
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
