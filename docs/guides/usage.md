# Using pika

Step-by-step usage for every pika command.

Every command takes `--root <dir>`. Without it, pika discovers the repository root by walking up from your working directory looking for `.project/contract.yaml`, then `.project/contract.yaml.draft`, then `.git` — so you can run `pika check` from any subdirectory and get the repository, not the folder.

`pika init` is the one deliberate exception: **it never discovers.** It scaffolds where you stand. Running `init` inside a subdirectory of an existing repository creates a new project there rather than silently re-scaffolding the enclosing repository.

Run `pika help` for the command list and `pika help <command>` for one command's flags; both are generated from the dispatch table, so they cannot drift from the commands that actually exist.

---

## 1. Start a brand-new project

```sh
mkdir my-service && cd my-service
pika init --profile go --name my-service
```

| Flag | Purpose |
|---|---|
| `--profile` | Language stack: `go`, `typescript`, `python`, `swift`, `rust` (repeatable for multi-stack) |
| `--name` | Project name (default: directory name, kebab-cased) |
| `--module` | Go module path (default: derived from name). Inert under a bare `--force` — see below |
| `--force` | Regenerate the kernel-owned files in an already-initialized repository; leaves your own alone |
| `--reset-docs` | With `--force` only: also restore the scaffolded `README.md`, `AGENTS.md`, `CONTRIBUTING.md`, `.gitignore` and language scaffold over the repository's own |
| `--json` | Emit the created-file manifest as JSON |

What you get: `.project/contract.yaml` (the project contract), `.project/profiles.lock`, `.project/exceptions.yaml`, a `docs/` spine, `AGENTS.md`, `README.md`, `CONTRIBUTING.md`, a GitHub Actions workflow, a `.gitignore` protecting `.project/state/`, and a language-owned source scaffold.

The scaffolded workflow pins the kernel that judges the repository to the pika
release that scaffolded it (`PIKA_REF`), rather than installing `@latest`. A
verifier that can change with no commit in the repository is a verifier that
turns a green pull request red on merge with nothing in the diff to explain
it. Bump `PIKA_REF` deliberately, in its own commit.

Then verify:

```sh
pika check --all
```

### `--force` regenerates more than the lock

`--force` is the only way to refresh a `.project/profiles.lock` that an older
pika wrote, so it is the remedy every upgrade note points at. It regenerates
more than the lock — but only what the *kernel* owns, and **it is safe to run
in a repository you have been living in.** That is a change: until M3 it
rewrote every file `init` manages and reset `.project/exceptions.yaml` to
`{}`, which made the one documented upgrade remedy something you had to brace
for.

| Rewritten by `--force` (kernel-owned) | Left exactly as it is (yours) |
|---|---|
| `.project/contract.yaml` | `README.md`, `AGENTS.md`, `CONTRIBUTING.md` |
| `.project/profiles.lock` | `.gitignore` and the `docs/` spine placeholders |
| `.github/pull_request_template.md` | the language scaffold — `go.mod`, `cmd/<name>/main.go`, `Package.swift`, … |
| `.github/workflows/ci.yml` | `.project/exceptions.yaml` — never rewritten, not even by `--reset-docs` |
| | every file `init` did not create, `.project/state/`, `.project/evidence/` |

The split is ownership, not convenience. The contract, the lock, the PR
template and the CI workflow encode how the kernel wants to be run, so a copy
left behind by an older kernel is the kernel's own defect to correct.
Everything else is a starting point your repository is expected to outgrow,
and it is yours the moment it exists.

**Inputs are read back from the repository, not from the command line.** Each
of profiles, project name and Go module path resolves the same way: the
explicit flag when you pass one, else the value the repository already
declares — profiles and name from the contract, the module from `go.mod` —
else a refusal naming what it could not recover. So a bare `pika init --force`
in a `go@1` repository stays a `go@1` repository with its gates intact, and
there is nothing you have to remember to retype:

```sh
pika init --force        # this is the whole upgrade command
```

One consequence is surprising unless it is stated: **`go.mod` is
operator-owned, so `--module` does nothing under a bare `--force`** in a
repository that already has one. The flag reaches the rendered scaffold, and
that `go.mod` is then left unwritten because yours already exists. `--module`
takes effect on a fresh `init`, or alongside `--reset-docs`. Renaming a Go
module means editing `go.mod` and the imports; it is not a scaffolding
operation.

### `--reset-docs` is the destructive opt-in

`--reset-docs` asks for the scaffold's own text back over your files — what
`--force` used to do unasked:

```sh
pika init --force --reset-docs   # overwrites README.md, AGENTS.md, CONTRIBUTING.md,
                                 # .gitignore, the docs spine placeholders and
                                 # the language scaffold
```

It requires `--force` (alone it exits 2, because by itself it could only be a
mistyped intention). It does **not** reach `.project/exceptions.yaml`: an
exception carries a rationale, an owner and a review condition a human wrote
and a reviewer accepted, and regenerating documentation is not a reason to
discard evidence. No flag clears the exceptions record; delete the entries you
no longer want.

Either way, regenerate on a clean tree and read the diff:

```sh
git status --porcelain   # clean: the diff below is then only the command's work
pika init --force
git diff                 # the kernel-owned files, and nothing else
pika check --all         # gate 1 goes green again
```

---

## 2. Adopt an existing project

```sh
cd /path/to/existing-repo
pika adopt
```

This is **read-only analysis**. It inventories the repository, detects the stack, compares your conventions against the contract, and writes:

| Output | Location |
|---|---|
| Adoption review (human-readable markdown) | `review/adoption-review.md` — **open this first** |
| Draft contract | `.project/contract.yaml.draft` |
| Draft profile lock | `.project/profiles.lock.draft` |

The review file lists detected profiles, conventions (match / conflict / exception), proposed changes as a checklist, and proposed naming exceptions with a suggested action per path.

**Before applying, review two things:**

1. `review/adoption-review.md` — decide per proposed exception whether to keep it or rename the file instead (edit `.project/contract.yaml.draft` to change the decision).
2. The draft contract itself, if you want to adjust verification commands or agent mappings.

---

## 3. Apply the adoption

```sh
pika apply
```

This promotes the drafts transactionally:

- contract and lock become live
- `exceptions.yaml` is written
- missing core files (AGENTS.md, CONTRIBUTING.md, PR template, CI workflow) are created from templates
- **your own files are never overwritten** — `README.md`, `AGENTS.md`,
  `CONTRIBUTING.md`, `go.mod` and the language scaffold are create-if-missing,
  and a file you already have is reported as `already exists; kept the
  existing file`
- the two **kernel-owned** files — `.github/pull_request_template.md` and
  `.github/workflows/ci.yml` — are compared against the current template and
  refreshed when they differ, because a copy left behind by an older kernel is
  the kernel's own defect to correct. Each refresh is reported as a `write`;
  one that already matches is skipped

That last point is the same ownership split `pika init --force` honours
([§1](#--force-regenerates-more-than-the-lock)), and it is the reason
`pika apply` can hand an unadopted repository a current CI workflow instead of
silently inheriting a stale one. A refresh is never silent: a kernel rewrite
nobody reported is indistinguishable from an edit you made yourself.

If anything fails mid-apply, the transaction rolls back — the refresh with it — and the report says so honestly, including the failure case where a rollback itself could not complete (it points you at `.project/state/recovery/`).

After a successful apply, the review file is rewritten with status **APPLIED** and the gate-1 result.

Refusals (safe, no mutation):

| Message | Meaning |
|---|---|
| `already adopted` | A committed contract exists — use `pika check` |
| missing drafts | Nothing to apply — run `pika adopt` first |
| invalid draft | The draft fails validation — fix or re-run `pika adopt` |

`already adopted` is a hard boundary, and it decides which command refreshes a
stale kernel-owned file. `apply` refuses before it inspects anything, so its
refresh is reachable **only during adoption** — a repository that already has
`.project/contract.yaml` cannot get there. In an adopted repository the
refresh command is `pika init --force`, which rewrites the same two files
([§1](#--force-regenerates-more-than-the-lock)).

---

## 4. Check a project

```sh
pika check --all          # every gate
pika check --changed      # narrow to what changed since the merge base
pika check --ci           # same engine CI runs; no LLM calls
pika check --json         # machine-readable report
```

| Flag | Purpose |
|---|---|
| `--all` | Run every gate (the default scope) |
| `--changed` | Resolve a change set from git and skip the package gates only when the tree is provably clean |
| `--ci` | CI mode: implies `--all`, no interactive prompts, no LLM calls |
| `--json` | Emit the report as JSON on stdout |
| `--contract <path>` | Use a contract other than `<root>/.project/contract.yaml` (a relative path resolves against the root) |
| `--root <dir>` | Repository root (default: discovered) |

`--all`, `--changed` and `--ci` are mutually exclusive.

The verification ladder: contract integrity → formatting/lint/compile → tests → smoke → (in agent runs) independent review.

Gate 1 also validates that your `profiles.lock` matches the contract and the embedded pack digests — a hand-edited lock or drifted contract fails here.

### Rung 5 is the real surface, and it has to be able to fail

`smoke` is the rung that runs the product. A `smoke:` command that prints a
version string fills the slot and can never fail, which makes it a gate in
name only — pika's own was `go run ./cmd/pika version` until 0.5.0, and every
defect closed on 2026-08-30 shipped behind it green. None of them was findable
by reading; all of them were found by running the product once.

So point the slot at something that exercises the shipped artifact the way a
user does, and make every assertion say what it expected and what it got.
pika's own is [`internal/smoke`](../../internal/smoke): it builds the binary
from the working tree and drives it as a subprocess through `init` → `check` →
`improve` → a second `improve` over the branch the first one left → `skills
install` and a hand-edited region → a corrupted `profiles.lock` → `doctor`,
inside temp repositories it removes when it ends. The agent boundary is a fake
`codex` binary on `PATH`, so there is no model call and no network, and `pika
check --ci` stays provably LLM-free with the gate in it.

### The gates report; they do not fix

A verification gate never edits the tree it is verifying. The format gate in
particular is a *checking* command — `gofmt -l .` for Go,
`ruff format --check .` for Python — and formatting drift comes back as a
failure you fix, not as files pika rewrote under you:

```
PASS contract   2ms
FAIL format     exit=0 drift.go

SKIP lint       skipped: gate format failed
```

`exit=0` in that line is not a typo. `gofmt -l` exits 0 whether or not it
found anything; the file it printed *is* the finding. Packs mark such slots
`fail-on-output`, and a gate carrying that flag fails when it prints, whatever
its exit status. Before M2 the gate was `gofmt -l -w .`, which rewrote your
files and then exited 0 — a gate that could not fail. See
[../reference/m2-delta.md](../reference/m2-delta.md#2-the-format-gate-can-now-fail--and-no-longer-rewrites-your-files).

### A gate may not re-enter the ladder that spawned it

If your `test` command runs a suite that itself invokes `pika check` on the
same repository, the ladder is refused rather than recursed:

```
pika check: verify: refusing to re-enter a running ladder: /path/to/repo
(enclosing ladders: /path/to/repo); a gate re-entered the ladder that
spawned it — pin the inner command to a different root
```

Exit 2, `code: "config"` under `--json`. It is refused rather than skipped on
purpose: a skipped gate still reports `pass`, so skipping would hand you a
green report for a ladder that never ran.

The guard is scoped to the **tree under verification**, carried down to gates
in `PIKA_CHECK_LADDER`, not to the process. A test that runs `pika check`
inside a fixture or temp repository is fine — it terminates. Only pointing the
inner run back at the tree the outer run is already verifying is the loop.

### What `--changed` actually narrows

The change set is a real git diff: everything differing from the merge base with the upstream default branch (`@{upstream}`, then `origin/HEAD`, `origin/main`, `origin/master`), plus staged, unstaged and untracked changes.

Gate 1 always runs — it validates the contract itself, and no change set can put that out of scope. Only the package gates (format, lint, typecheck, test, smoke) can be skipped, and only in one case: **the change set is known to be empty.**

Attribution is not a narrowing signal. The gates are repository-wide commands from `contract.commands`, and a changed file often belongs to no declared package while being able to break all of them — a root `go.mod`, a CI workflow, the contract itself. So "this file is outside every package root" never means "skip the gates".

**Degradation is loud, and it always widens.** When the change set cannot be trusted, pika prints a warning naming the reason and runs every gate anyway:

```
warning: --changed could not resolve a change set (shallow clone: no reliable merge base); running every gate
```

It degrades when:

| Situation | Why it cannot be trusted |
|---|---|
| `git` is not on `PATH`, or the directory is not a work tree | There is no diff to compute |
| The clone is shallow | Any merge base it reports is an artifact of the depth cut, not the real fork point |
| A ref resolves but shares no history with `HEAD` | Grafted or unrelated history: there is no common ancestor to diff against |
| A `git` invocation fails | Unknown is not the same as unchanged |

A repository with one branch and no remote is *not* a degradation: it legitimately has nothing to fork from, so the staged and working-tree diffs stand on their own.

---

## 5. Diagnose a repository without running anything

```sh
pika doctor
```

`doctor` answers "why is this repository not working?" without executing a single gate. It is safe to run anywhere, at any time, and it is the first thing to reach for when `check` behaves unexpectedly.

| Flag | Purpose |
|---|---|
| `--json` | Emit the report as JSON on stdout |
| `--root <dir>` | Repository root (default: discovered) |

What it inspects:

| Check | What it tells you |
|---|---|
| root | The resolved repository root, and how it was found (`contract`, `draft`, `git`) |
| contract | Schema version and selected profiles, or the parse error |
| lock | Whether `profiles.lock` pins the contract's profiles at digests matching this binary's embedded packs. Both green and red print the registry digest — red prints the lock's too, and names both reasons two digests can differ, because the kernel cannot tell a stale lock from a stale binary |
| exceptions | Whether `.project/exceptions.yaml` loads and every record is complete |
| envelope | The grants in `.project/state/envelope.yaml`, or a warning that agents will be denied |
| recovery | Whether a transaction never finished — who holds the lock, and whether that process is still running. It points at [`pika recover`](#15-unwedge-a-crashed-run-or-transaction-pika-recover) rather than acting |
| leases / `lease.*` | Whether a run lease or a scope lease is held, and what can be proved about each holder. A **stale** lease (holder gone, same host) is an error and names `pika recover`: no run can start until it is released. A **held** lease is a warning — somebody's second terminal is legitimately mid-run — and names no recovery, because `pika recover` refuses a live holder. A holder on **another host** is a warning reported as exactly that, never as stale, and sends you to the machine that can answer |
| `gate.*` | Per gate: the command that will run, or the pack's suggested hint when no command is configured — plus a warning when an envelope exists and does not authorize that gate's whole argv line, which is otherwise not discovered until an agent hits `envelope_denied` mid-task |
| git | Whether git is available |

Worked example, on this repository:

```
$ pika doctor
root  /home/you/pika (contract)

ok    contract       schema 1, profiles [core@1 go@1]
ok    lock           pinned digests match this pika's embedded registry f34a39847227902b0b36332796fddacdb4fdb07d03d5c8a8bcaed8c454f59e9e
ok    exceptions     exceptions record loads
warn  envelope       no capability envelope at /home/you/pika/.project/state/envelope.yaml
                     → run "pika authorize --scope project"; without it every MCP tool is denied, reads included
ok    recovery       no interrupted transaction
ok    leases         no run or scope lease is held
ok    gate.format    gofmt -l .
ok    gate.lint      go vet ./...
ok    gate.typecheck go build -o /dev/null ./...
ok    gate.test      go test ./... -count=1
ok    gate.smoke     go run ./internal/smoke
ok    git            git is available
```

Exit code is `0` unless something is error-severity. A missing envelope is a **warning**, not an error: the human CLI does not need one.

---

## 6. Understand a rule, a gate, or an error code

```sh
pika explain naming-kebab-case
pika explain file-size-review
pika explain test
pika explain envelope_denied
```

| Flag | Purpose |
|---|---|
| `--json` | Emit the entry as JSON on stdout |
| `--root <dir>` | Repository root (default: discovered) |

`explain` covers three id families, resolved from the contract's own profiles so it explains *your* rules, not a generic list:

| Family | Ids |
|---|---|
| Naming rules | `naming-kebab-case`, `naming-catch-all`, `file-size-review`, `generated-owner` |
| Verification gates | `contract`, `format`, `lint`, `typecheck`, `test`, `smoke` |
| MCP error codes | `envelope_denied`, `contract_invalid`, `already_adopted`, `scope_conflict`, `invalid_params`, `unavailable`, `internal` |

Run `pika explain` with an unknown id to print the ids this repository actually knows.

For a naming rule it prints the owning pack, the severity, what the rule matches, the rationale, the remediation, and a copy-pasteable exception record that parses as-is:

```
$ pika explain naming-kebab-case
naming-kebab-case (naming-rule)

owner:
  core@1

severity:
  warning

matches:
  scope path-segments; pattern ^[a-z0-9][a-z0-9._-]*$; exempt README, AGENTS, ...

rationale:
  Mixed-case path segments are the classic cross-platform trap: ...

remediation:
  Rename the offending segment to lowercase with dash, dot, or underscore separators ...

record an exception:

# .project/exceptions.yaml
<repo-relative path>:
  rule-id: naming-kebab-case
  reason: <why this path must keep its name>
  owner: <who accepts this>
  review-condition: <the condition that reopens this decision>
```

All four fields are mandatory. A record missing any of them fails gate 1 rather than silently widening the rule. An exception on a directory covers everything beneath it.

---

## 7. Authorize an agent (capability envelope)

```sh
pika authorize --scope project
```

Before this command existed, the only way to let an agent write anything was to hand-author `.project/state/envelope.yaml` correctly — the single largest barrier to actually using pika with an agent. `authorize` generates it.

| Flag | Purpose |
|---|---|
| `--scope` | `read`, `project` (default), or `repo` — see the table below |
| `--exec` | Command to authorize, as a **whole argv line**: `--exec "make test"`, not `--exec make` (repeatable; never granted implicitly) |
| `--network` | Host or `host:port` to authorize (repeatable; never granted implicitly) |
| `--credential` | Credential name to authorize (repeatable; never granted implicitly) |
| `--github` | GitHub scope to authorize (repeatable; never granted implicitly) |
| `--force` | Replace an existing envelope (without it, pika prints the diff and refuses) |
| `--json` | Emit the result as JSON on stdout |
| `--root <dir>` | Repository root (default: discovered) |

### Scopes

| Scope | `fs_write` grants | `exec` grants |
|---|---|---|
| `read` | none — nothing mutating at all | none, unless you pass `--exec` |
| `project` | `.project`, `docs`, `review` — the directories pika owns | every gate command in your contract, plus any `--exec` |
| `repo` | `.` — the whole repository tree | every gate command in your contract, plus any `--exec` |

`read` works in a repository that was never adopted, because it authorizes no change and therefore needs no contract.

`project` and `repo` work there too: the write grant does not depend on a contract. Exec grants are derived from the gates a contract resolves to, so before `pika init` or `pika adopt` there are none to derive — `authorize` says so on stderr, writes the envelope with no `exec`, and exits 0. That is the state `preview_plan` runs in, and it is why the remediation `pika doctor` prints works before adoption. Re-run `pika authorize --force` afterwards to pick up the contract's gate commands. A contract that exists and does not parse is still an error: it is a defect to fix, not a grant to skip.

`exec` grants are **whole argv lines**, whether derived or explicit: `go build -o /dev/null ./...`, not `go`; `--exec "make test"`, not `--exec make`. That is what the enforcement side matches against — `Envelope.Allows` compares an entry element-wise against the whole line the call site asks about, so `make` alone authorizes a bare `make` and denies `make test`. It is also the tighter grant: authorizing bare `go` would additionally authorize `go build -o /anywhere`, a command no gate runs.

`--exec` is the only way to authorize a command no contract declares — a discovered check command `preview_plan` would run before adoption, for instance. pika deliberately never derives those grants from discovery: `preview_plan` exists to inspect repositories nobody has vetted, and letting an unvetted tree grant itself execution would make the envelope a rubber stamp rather than a grant. Deriving from a *committed* contract is safe for the opposite reason — the contract is operator-owned. When a denial happens, the message names the exact invocation, e.g. `run "pika authorize --exec \"make test\""`.

Generated for this repository:

```yaml
# Generated by "pika authorize". Local-only: .project/state/ is gitignored.
# Deny-by-default: anything absent here is refused.
schema: 1
allow:
  fs_write:
  - .project
  - docs
  - review
  exec:
  - go build -o /dev/null ./...
  - go run ./cmd/pika version
  - go test ./... -count=1
  - go vet ./...
  - gofmt -l .
rollback_boundary: repository
```

### The envelope is local-only

`.project/state/envelope.yaml` is **written at mode 0600, lives under the gitignored `.project/state/`, and is never committed.** It records what *you* have authorized on *this* machine; it is not a project-level policy others inherit. Re-running `authorize` on an existing envelope tightens the file back to 0600 and, unless `--force` is given, prints the delta and refuses rather than silently widening or narrowing what an agent may do.

`budget` is deliberately never written. No code in the binary compares spend against a ceiling, and a ceiling nothing enforces is a lie in a file whose entire job is to be true. `network`, `credential` and `github` are written when you ask for them but have no enforcement call site — and cannot get one, because pika performs no operation of any of those classes; see [../reference/m3-delta.md](../reference/m3-delta.md). `fs_read` **is** enforced as of M3, at the MCP read tools. Note what that does and does not mean: the envelope has no `fs_read` list, and any in-repo read is permitted by any valid envelope, so the requirement is to *have* an envelope, not a narrowing of which reads are allowed.

Only the **MCP surface** authorizes. `pika check` from your shell runs its gates directly and needs no envelope: the envelope exists to bound what an agent may do on your behalf, not to make you ask permission to run your own tests.

---

## 8. Get help, and identify the binary

```sh
pika              # same as pika help
pika help         # every command, one line each
pika help check   # one command's usage line and flags
```

The help text is generated from the dispatch table, so a command that exists is listed and a command that is listed exists. There is no second copy to fall out of date.

### `pika version` identifies the build, not just the release

```sh
pika version              # release, pack registry, contract schema
pika version --root .     # the same, for a repository elsewhere
pika version --json
```

```
$ cd /tmp && pika version
pika 0.5.0
pack registry:   f34a39847227902b0b36332796fddacdb4fdb07d03d5c8a8bcaed8c454f59e9e
contract schema: 1 (highest supported)
```

The release number alone identifies nothing useful: what decides whether a
binary accepts a repository is the **pack registry digest**, the same value
`profiles.lock` records and gate 1 compares against. Two builds carrying
different packs print different digests even when they claim the same release
— which is the whole point, because for four milestones they did not, and a
`0.1.0` that rejected a repository was indistinguishable from the `0.1.0` that
had written it.

Run inside a project — or pointed at one with `--root` — it adds the digest
that repository's lock was written with, and whether it is this binary's:

```
$ pika version --root /path/to/project
pika 0.5.0
pack registry:   f34a39847227902b0b36332796fddacdb4fdb07d03d5c8a8bcaed8c454f59e9e
contract schema: 1 (highest supported)
/path/to/project/.project/profiles.lock: e824aaaa…aaaa2fdf (differs from this binary)
```

That line is arithmetic, not a verdict: it says the two numbers are equal or
not, and nothing about which side is right. `pika check` and `pika doctor`
are the commands that judge. It is what you run on each candidate binary when
a lock is rejected — the build whose digest matches is the one that wrote the
lock. See
[Upgrading](#upgrading-a-profileslock-and-a-pika-that-disagree).

`pika --version` and `pika -version` print exactly the same report.

Which release a build claims is enforced against what it actually carries: a
change to any embedded pack, template, or the contract schema ceiling fails
`internal/version`'s surface test until the version moves with it. A release
that silently means two different products is the defect that test exists for.

---

## 9. Connect an AI agent (MCP)

```sh
cd /path/to/project
pika mcp
```

Serves the kernel over stdio JSON-RPC. Point any MCP-compatible agent harness at the command `pika mcp`. **Every tool requires a capability envelope at `.project/state/envelope.yaml`, reads included** — deny-by-default, so an agent cannot inventory, read, run or write anything you have not explicitly authorized. Through M2 the read tools — `inspect_repo` and `read_contract` — worked without one; **as of M3 they do not**, because enumerating your repository is a capability an agent is granted, not a neutral act it may perform unasked. Read that precisely: the requirement is to *have* an envelope, not a narrowing of which reads are allowed — there is no `fs_read` list to write, and any valid envelope permits any in-repo read. The remedy is one command, and the smallest envelope is enough to read:

```sh
pika authorize --scope read     # grants no writes and no exec at all
```

One error code moved with that change, which matters if you match on codes:
`read_contract` with a path outside the repository — `../../.ssh/id_rsa` — now
answers `envelope_denied` where it answered `invalid_params`. The
authorization runs before the path normalization that used to reject it, on
purpose: normalizing first would hand the check a target already proved
repo-inside, and it could then never deny anything.

### Scope leases and `scope_conflict`

The envelope says which paths an agent *may* write; a **scope lease** is what makes one of them exclusive. `acquire_scope` takes a lease on a repository-relative path, recorded at `.project/state/locks/<encoded-path>.lock`; `release_scope` gives it back. Both are refused with the stable code `scope_conflict`:

| Situation | Why |
|---|---|
| the path, or one inside or containing it, is already leased | exclusive over a path means exclusive over its whole subtree — a lease on `src` conflicts with one on `src/pkg` in both directions, because an exclusion an agent could sidestep by naming a subdirectory would not be one |
| a pika run is in progress | a run takes a lease on the **whole repository** and commits through the working tree every scope sits in, so while one is running every path is refused. The run lease is the scope lease on `.`, and `.` contains everything |
| the holding session asks for a lease it already holds | a lease it holds is not a second lease it may take |
| `release_scope` names a path this session does not hold | an agent told it released somebody else's lease would go on to write under it |

The exclusion runs both ways. While any scope lease is held, `pika work`, `pika improve`, `pika resume` and `pika handoff` refuse to start, and the refusal names the scope and the session holding it rather than claiming another run is in progress:

```
pika work: improve: a scope lease holds part of this repository: the scope lease on src,
held by scope:src#1756598894000000000 (pid 41234 on laptop-01, started
2026-08-30T23:08:14Z), and a run commits through the whole working tree, including the
path that lease covers; wait for that session to release it or end
```

Leases never expire and are never stolen: the holding session releases it, or the session ends and the server gives back everything it still holds. Nothing ever waits — every acquisition claims its own file first and then looks, so two racing acquisitions both refuse instead of deadlocking. `pika explain scope_conflict` prints the rationale and the remedy.

A killed MCP session cannot give anything back, so its leases stay on disk; every later `acquire_scope` on those paths is refused, and so is every run in the repository. [`pika doctor`](#5-diagnose-a-repository-without-running-anything) reports them and [`pika recover`](#15-unwedge-a-crashed-run-or-transaction-pika-recover) clears the ones whose holder is provably gone.

---

## 10. Hand a failed check to Codex

```sh
pika handoff
```

`handoff` runs `pika check --all --json`, selects only failed check gates, and invokes the configured `agents.builder` when its runtime is `codex`. It runs Codex locally with a writable-workspace sandbox, automatic review, and network access disabled; it never uses a danger or approval-bypass mode. Pika also refuses the handoff if the agent changes Git history.

Every handoff belongs to a run, and the private bundle lives inside that run's record at `.project/state/work/<work-id>/handoff/`:

| File | Purpose |
| --- | --- |
| `checks-before.json` | Redacted baseline Pika report |
| `prompt.md` | Failed gates and safe repair instructions |
| `codex-last-message.md` | Codex's final response |

Warnings are not repair tasks. Intentional vendor assets, public filenames, and generated outputs must instead be covered by the applicable record in `.project/exceptions.yaml`; covered findings are excluded before the handoff is built.

Choose another configured Codex agent with `pika handoff --agent <name>`. Its configured `model` and `effort` are forwarded to Codex. Use `--json` for automation.

---

## 11. Improve a repository without opening a PR

```sh
pika improve
```

`improve` requires a clean worktree. It runs a baseline check; if it is already green, it exits without creating a branch or calling Codex. For failures, it creates `chore/pika-improve`, performs the Codex handoff, reruns the same Pika checks, and commits only a non-empty, verified diff with the message `chore: improve verified findings`.

Use `--branch <name>` to choose another local branch and `--agent <name>` to select a configured Codex builder. On a failed handoff or recheck, Pika leaves the branch and agent edits uncommitted so they can be inspected. `improve` never pushes, opens a pull request, or merges anything.

Your contract needs a Codex builder, for example:

```yaml
agents:
  builder:
    runtime: codex
```

### The run branch a stopped run leaves behind

Nothing deletes `chore/pika-improve` when a run fails, so the next run finds it already there. What happens then depends on what the branch actually holds, which Pika reads from Git and from the durable run records rather than from the branch name:

| The branch | What the next run does |
|---|---|
| holds no commit your starting point does not already have — the leftover of a run that stopped before it committed, or one whose work you have since merged | takes it, moves it onto the commit this run starts from, and carries on |
| holds commits that are not in your history — a run that committed and stopped afterwards, or a `chore/pika-improve` of your own | refuses, and names the branch, the commit, the run that delivered it, and the remedy |

```
pika work: improve: the run branch already exists and holds work: chore/pika-improve is at
cb74d5529dd518f8183debb07c4568a473a96584, delivered there by run 20260831-feature-b49c2cd2;
read it with `git log chore/pika-improve`, then delete it with `git branch -D
chore/pika-improve` once the work is merged or unwanted, or send this run elsewhere with
--branch
```

Pika does not delete the branch for you, for the same reason [`pika recover`](#15-unwedge-a-crashed-run-or-transaction-pika-recover) clears only a holder it can prove is dead: a run can stop *after* its commit lands, and the branch is then the only place that work exists. `pika status <work-id>` shows what the named run did before you decide.

---

## 12. Hand a goal to the agent (`pika work`)

```sh
pika work "add a /healthz endpoint that returns 200"
```

`work` is the normal entry point. It runs the *same* lifecycle as `improve` — clean worktree, baseline check, branch, Codex handoff, recheck, one verified local commit — and differs in exactly one decision.

A failed gate describes its own repair, so `improve` stops when the baseline is already green: there is nothing left to fix. A goal is work the ladder cannot describe, so a green baseline says nothing about whether the goal has been met, and `work` goes on to the agent regardless. The goal is the entire input, and it reaches the agent verbatim as the `## Goal` section of `prompt.md`.

The goal is exactly one quoted string:

```sh
pika work "add a /healthz endpoint"           # correct
pika work add a /healthz endpoint             # exit 2: unquoted, four positionals
pika work "$GOAL"                             # exit 2 when GOAL is unset or blank
```

`--branch <name>`, `--agent <name>`, `--json` and `--root <dir>` behave exactly as `improve`'s do, and the default branch is the same `chore/pika-improve` — so a run interrupted before it branched resumes onto the branch `pika resume` would pick anyway.

### One repository runs one run at a time

`work`, `improve`, `resume` and `handoff` each take an exclusive **run lease** on the whole repository, at `.project/state/run.lock`, and hold it until the run records a terminal outcome. A second one started while the first is in flight refuses immediately, before it spawns an agent or touches anything:

```
$ pika work "a second goal"
pika work: improve: another run holds this repository: run 20260830-feature-b2792c9f
(pid 8900 on build-01, started 2026-08-30T23:08:14Z) is in progress; one repository runs
one run at a time, because both would commit through the same working tree
```

This is the hazard one user with two terminals can reach. Both runs write one working tree and move one HEAD: the second run's agent edits land in the first run's commit, and the second run's branch checkout moves the tree the first one is verifying. The refusal names the holding run so `pika status <work-id>` can be pointed straight at it.

The lease is never waited on and never stolen. Waiting would make a run that stopped for a reason indistinguishable from one that hung, and an operator staring at a silent terminal could not tell which they had. Stealing is the defect itself. If the holder is gone, [`pika recover`](#15-unwedge-a-crashed-run-or-transaction-pika-recover) is the remedy — and it is a decision, not a retry.

---

## 13. See what pika has run (`pika status`)

```sh
pika status                  # every run, newest first
pika status <work-id>        # one run in full
```

Every run of `work`, `improve` and `handoff` writes a durable record: its goal, kind, branch, base commit, the phases that completed and when, the terminal outcome, and — when it stopped — the reason verbatim. `status` reads those records and executes nothing.

A repository that has never run anything exits 0 with an empty listing; that is a valid state, not a failure. A record too damaged to read fails the whole listing rather than being quietly skipped: a listing that looked complete while hiding corruption is worse than no listing.

A run showing `in-flight?` has no terminal outcome. That is genuinely ambiguous on disk — a run still working and a run whose final write never landed leave identical records — so `status` says so instead of guessing. Either way the next move is the same: look at the branch, or hand it to `pika resume`.

### The record is local; the receipt is committed

| Path | What | Committed? |
|---|---|---|
| `.project/state/work/<work-id>/` | The run record and its handoff bundle: the goal, the prompt, the agent's final message, full ladder reports | **Never** (gitignored) |
| `.project/evidence/<work-id>.json` | The kernel-issued evidence receipt for a run that attempted work | Yes |

The split is deliberate. The record is operational state — it exists so a run can be resumed and diagnosed on the machine that ran it, and it holds the run's whole working context: goals, prompts, gate output, the agent's own words. The receipt is the public attestation: schema-validated, redacted, and issued by the kernel rather than written by the agent whose work it describes.

**Both are redacted, and that is not the difference between them.** Every string in the handoff bundle has always been run through `redact.Apply` before it is written, and as of M3 so is every free-text and captured-output field of `record.json` itself — the same treatment the receipt gets. What separates them is not scrubbing but standing: the receipt is schema-validated and meant to be published; the record is neither, and it carries internal notes, paths and reasoning that belong on one machine.

The gitignore is the first guard, not the only one. Before it commits anything, a run drops every path under `.project/state` from the change set, and it **refuses outright** if the agent moved private state out of that directory — `git mv .project/state/work/<id>/record.json notes.md` would otherwise carry a private transcript into the commit under a name the path filter has no reason to reject. The refusal names the path that moved, exits 1, and commits nothing. Redacting at write time is what bounds the damage when a filter like that is wrong: a leaked file then carries `<redacted:oauth>` where a credential was. See [../reference/m3-delta.md](../reference/m3-delta.md#4-known-limit-the-copy-leak).

Two details about the receipt that are easier to read here than to discover:

- **A blocked run gets one too.** The receipt attests the run's terminal
  state, whatever that state is; a document that only ever describes successes
  attests the wrong half of what pika does. The one run that gets no receipt
  is a repair run whose baseline ladder was already green — no agent, no
  commit, nothing attempted, and issuing one there would leave a healthy
  repository's working tree dirty after every no-op run.
- **It is written after the commit, so it is not in it.** The receipt lands as
  a new untracked file under `.project/evidence/`. Committing it is your
  move — the kernel does not amend the commit it just verified.

---

## 14. Continue an interrupted run (`pika resume`)

```sh
pika resume <work-id>
```

A run interrupted by a crash, a lost terminal or a `Ctrl-C` leaves a record naming its branch, its bundle and the last phase that completed. `resume` rejoins it and carries it to a terminal outcome, skipping only the phases whose product is durable: the baseline ladder it already recorded, and the handoff it already ran. The recheck is never skipped — "commit only what the ladder proved" has to be proved by this process against this tree.

There is deliberately **no `--branch` flag**. The run's own record names the branch its work is on, and a flag that is silently ignored whenever the record has one is a flag that will eventually be believed.

`resume` refuses rather than guessing, and each refusal is a different next move:

| Refusal | What it means |
|---|---|
| unknown work id | check what you typed; `pika status` lists the runs this repository has |
| the run already reached a terminal outcome | it is finished; `pika status <work-id>` says how |
| the run's branch is gone | the work was deleted or merged away; start a new run |
| the repository moved under the run | look at what moved before resuming anything |

All four exit 2, not 1: nothing was attempted. Exit 1 means the resumed run itself ran and failed.

If Git proves the work already landed — the branch points at the commit the record names — `resume` writes the missing terminal outcome and stops. It does not branch again or re-run an agent over work the repository already contains.

---

## 15. Unwedge a crashed run or transaction (`pika recover`)

### The situation this exists for

pika holds three kinds of exclusive lock, and **none of them is ever stolen automatically, on any path** — not even when the process that took it is provably gone:

| Lock | Taken by | Where |
|---|---|---|
| Transaction lock | `pika apply` | `.project/state/recovery/lock` |
| Run lease | `pika work`, `pika improve`, `pika resume`, `pika handoff` | `.project/state/run.lock` |
| Scope leases | an MCP session's `acquire_scope` | `.project/state/locks/` |

That refusal is why the mechanism has never corrupted a repository: stealing a lock from a process that turns out to still be running is how two writers come to share one working tree. The cost of it is that a process killed part-way — a lost SSH session, an OOM kill, a CI runner that vanished — leaves the repository locked out, forever, until somebody intervenes. `pika recover` is that intervention, and it is the only supported one.

After a killed `pika apply`, three things are true at once:

1. The tree may be half-mutated: some planned operations ran, the rest did not.
2. The journal and per-op backups needed to undo them are on disk.
3. The transaction lock is still held, so every later transaction fails with:

```
txn: scope lease required: recovery lock at .../recovery/lock held by pid 41234 since ...
```

After a killed `pika work`, the run record is intact and `pika status` shows the run — but its lease was never given back, so `pika resume` refuses to rejoin it:

```
improve: another run holds this repository: run 20260830-feature-9ca9ae9e (pid 41234 on
build-01, started 2026-08-30T12:00:00Z) is no longer running and never released its
lease; `pika recover` clears it
```

`pika resume` does not clear that lease itself, and this is deliberate. A record saying "interrupted" is bit for bit what a run that is still working leaves behind, so a resume that took the lease on the strength of a matching work id would be joining a run that never stopped. Clearing it is a decision, and it belongs to an operator with the report in hand.

### Look first

```sh
pika recover
```

The default changes nothing. It reports where recovery state lives, who holds every lock, **whether that holder can be proved gone**, and every journaled operation with what a rollback would do to it:

```
root      /home/you/my-service (contract)
recovery  /home/you/my-service/.project/state/recovery

lock      tx 17e4c1a09b3f0000-3f9c21ab held by pid 41234 since 2026-08-30T12:00:00Z
          the holder process is not running, so the lock is stale

transaction 17e4c1a09b3f0000-3f9c21ab
  undo  write  .project/contract.yaml
  undo  create .project/profiles.lock
  skip  delete review/adoption-review.md  (the mutation never ran)

nothing has been changed. Re-run with --apply to roll this back and clear what no process is behind.
```

`undo` and `skip` are not guesses. Each entry is classified against the file on disk and its preserved backup, by the same code the rollback itself uses — so the report is the plan, not a second opinion about it.

A repository whose runs were killed rather than whose transactions were reports its leases the same way, and says of each one what `--apply` would do with it:

```
root      /home/you/my-service (contract)
recovery  /home/you/my-service/.project/state/recovery

no interrupted transaction

leases
  run   /home/you/my-service/.project/state/run.lock
        covers the whole repository: one run at a time, because two would commit through one working tree
        stale, held by 20260830-feature-9ca9ae9e (pid 41234 on build-01, started 2026-08-30T12:00:00Z)
        --apply clears this: the holder process is gone and it was recorded on this host
  scope /home/you/my-service/.project/state/locks/src%2Fapi.lock
        covers src/api and everything under it
        stale, held by scope:src/api#1756555200000000000 (pid 41250 on build-01, started 2026-08-30T12:00:05Z)
        --apply clears this: the holder process is gone and it was recorded on this host

nothing has been changed. Re-run with --apply to roll this back and clear what no process is behind.
```

### Leases are one exclusion at two radii

The run lease excludes the **entire repository**, not a directory or a branch. That is not caution, it is the shape of the hazard: two runs share one working tree and one HEAD, so the second one's agent edits land in the first one's commit, and the second one's branch checkout moves the tree the first one is verifying. Neither would know the other was there. Path-scoped run leases would serve parallel writers, and pika does not have any — one repository runs one run at a time.

A scope lease is narrower but works the same way in its subtree: a lease on `src` conflicts with one on `src/pkg` in both directions, because an exclusion an agent could sidestep by naming a subdirectory would not be one.

The two are not separate systems. The run lease *is* the scope lease on `.`, so the subtree rule decides every pair between them: `.` contains every path, therefore a run refuses every `acquire_scope` and any held scope refuses every run. `pika recover` reports them separately because their remedies read differently to a person — "the run holding this repository" and "the scope lease on `docs/guides`" are different sentences — but nothing in the product judges them by different rules.

### Then act

```sh
pika recover --apply
```

This rolls every journaled operation back in reverse order, restores each file byte-for-byte from its backup, retires the journal and the backups, releases the transaction lock, and clears every lease whose holder is provably gone:

```
cleared the run lease at /home/you/my-service/.project/state/run.lock (held by 20260830-feature-9ca9ae9e, no longer running)

the repository is clear; new transactions and runs can begin
```

Clearing a run's lease does **not** discard the run. The record stays exactly where it was, `pika status` still lists it, and `pika resume <work-id>` picks it up from the phase it reached. Recover unlocks the door; resume finishes the work.

### What it refuses

| State | What `pika recover` does |
|---|---|
| the holder process is still running | **refuses**, exit 2, naming the run and the pid — wait for it, do not roll its work out from under it or clear the lease it is inside |
| the holder is on **another host** | **refuses**, exit 2, and never calls it stale. A pid recorded on another machine proves nothing here: it can be long dead locally and still writing where it was taken. Check that machine, then remove the file yourself |
| the lock names no holder at all | **refuses**, exit 2 — an empty lock cannot be proved stale, so it says so and names the file to remove once you are certain |
| a journal entry does not match the disk | stops that journal with an error naming the entry; something outside pika edited those files between the crash and now |
| nothing pending | exits 0 saying so; a repository with no interrupted transaction and no lease left behind is a normal state |

Refusals are all-or-nothing and nothing is attempted: a live run in the tree is a reason not to start rolling anything back at all.

### On Windows, `--apply` always refuses

Deciding whether a lock is stale means deciding whether its holder is still
running, and there is no portable standard-library way to ask that on Windows.
pika answers conservatively rather than wrongly: **every positive pid reads as
alive**, so `pika recover --apply` refuses every wedged repository there with
`the holder process is still running`.

The report is unaffected — the journal walk, the `undo`/`skip` classification,
the lease listing and the file listing are all platform-independent, so
`pika recover` still tells you exactly what happened and what a rollback would
touch. Only the liveness verdict, and therefore the authorization to act on it,
cannot be trusted. Once you have confirmed by other means that the holder is
gone, delete `.project/state/recovery/lock`, `.project/state/run.lock` or the
file under `.project/state/locks/` by hand and re-run. This is a known gap:
[../reference/m2-delta.md](../reference/m2-delta.md#gap-2--pika-recover---apply-cannot-prove-a-holder-dead-on-windows).

`pika doctor` reports an interrupted **transaction** as a `recovery` finding and every held or stale **lease** as a `lease.*` finding, and points here, so neither situation needs you to already know this command exists. The refusal you get from `pika work` or `pika resume` names `pika recover` directly as well.

---

## 16. Install the agent instructions (`pika skills`)

```sh
pika skills             # report: what is installed, what is projected, what is stale or tampered
pika skills install     # write the canonical skills, regenerate every declared projection
pika skills check       # exit 1 if any projection is stale, tampered or unreadable

pika skills --global            # the same three modes, against the agent files in
pika skills install --global    # your home directory instead of this repository's
pika skills check --global      # projections
```

How to drive pika is written once, in the canonical, harness-neutral location:

```
.agents/skills/project-work/SKILL.md     running, repairing and resuming work
.agents/skills/project-review/SKILL.md   what counts as evidence, and what a reviewer must not do
```

`pika skills install` writes those files when they are missing. It never
overwrites one you have edited without `--force`: they are yours.

### Projections, and why they carry a digest

A harness that will not read `.agents/skills/` gets a **projection** — the same
guidance rendered where it does read. Which harnesses get one is declared in
the contract, never compiled into the kernel:

```yaml
skills:
  projections:
  - harness: codex
    path: AGENTS.md
  - harness: claude
    path: CLAUDE.md
```

`harness` is drawn from the same set as `agents.<name>.runtime` (`omp`,
`codex`, `claude`, `gemini`, `opencode`, `acp`, `custom`). A name outside it is
a schema error, not a projection that is quietly skipped: a file nothing reads
looks exactly like a file something reads, and you would never find out which
you had. A harness whose requirement is only "a file at this path" needs a
contract line and no new code.

A projection is a **region**, not a whole file:

```markdown
<!-- pika:skills:begin -->
<!-- Generated by `pika skills install`. … -->
<!-- pika:region sha256:d8c6… (covers this region excluding this line) -->
<!-- pika:source skill .agents/skills/project-work/SKILL.md sha256:4018… -->
<!-- pika:source guidance go@1 sha256:ba11… -->

## Driving pika
…
<!-- pika:skills:end -->
```

Everything outside the markers is yours and survives regeneration, which is why
`AGENTS.md` can be a projection target at all — it is a file you write.
Everything inside is the kernel's: edit the source and regenerate, never the
copy. A marker only counts when it is a line on its own, so prose that quotes
one — this page does — is not mistaken for a region.

Two digests, because a projection can go wrong in two directions and the
remedies are opposites. **Gate 1 recomputes both.**

The `pika:source` lines make a **stale** copy distinguishable from a current
one: the source moved and the copy did not follow. Regenerating is the whole
fix and nothing you wrote is at risk.

```
skills projection: stale AGENTS.md (harness codex) cites skill
.agents/skills/project-work/SKILL.md at sha256:4018…, which is now
sha256:9c2f…; regenerate it with `pika skills install`
```

The `pika:region` line makes a **tampered** region distinguishable from an
authentic one: it is the sha256 of the region's own bytes, taken over
everything between the markers *except that line* — a digest recorded inside
what it covers cannot cover itself, so the line says which bytes it covers and
the scheme is a fixed point, regenerating an unchanged projection reproduces
the same file. Editing inside the markers changes the bytes, the recorded
digest does not follow, and the region fails on its own evidence without
consulting any source:

```
skills projection: tampered AGENTS.md (harness codex) was edited by hand
inside the pika skills markers: it records region digest sha256:d8c6… but its
bytes now hash to sha256:7b41…; that region is kernel-owned, and `pika skills
install` would DISCARD whatever is there rather than keep it — make the change
in the canonical skill under .agents/skills/ (or in the profile pack guidance)
and regenerate
```

That independence is the point. Checking the region against its own digest
*before* comparing it to its sources is what keeps a hand edit visible when a
source moved in the same working tree. Inferring "hand-edited" by elimination —
the region differs and no source moved — hides the edit exactly then, and tells
the one operator whose work is about to be destroyed that regenerating is free.

A region the kernel cannot check at all — the `pika:region` line deleted,
doubled, or reworded — is `tampered` too, reported as unverifiable rather than
as an edit the kernel did not observe. Markers that are missing, unpaired,
duplicated or reordered are `unreadable`: the file is refused, never rewritten
and never reported `current`. Nothing about a projection fails open.

`pika skills install` may overwrite a hand-edited region — that region is
kernel property — but it says so in its report when it did, naming what it
replaced.

### Stack guidance comes from the profile packs

The skills are the same in every repository; what differs is the stack. Each
profile pack may declare `agent-guidance`, and the projection composes it into
a **Stack guidance** section citing the pack it came from. `go@1` carries the
advice its own gates imply — why the typecheck hint is `go build -o /dev/null`,
why the format gate is judged on output rather than exit status — so a Go
repository's projection says something a TypeScript repository's does not,
without either forking the skill.

A pack's guidance is digested on its own, not with the whole pack: an unrelated
pack edit rotates `profiles.lock` (which is where whole-pack drift belongs) and
leaves every projection current.

### The operator-wide files (`--global`)

A projection is generated from a contract, so an agent standing in a directory
that has no contract reads nothing at all — and that is exactly the moment it
most needs to be told `pika init` and `pika adopt` exist. `--global` installs
the two files a harness reads regardless of which repository you are in:

```sh
pika skills --global            # report: installed, stale, tampered or absent
pika skills install --global    # write them
pika skills check --global      # exit 1 if any of them is not current
```

| File | Read by |
|---|---|
| `<home>/.agents/skills/pika/SKILL.md` | omp, as a user-level skill |
| `<home>/.codex/AGENTS.md` | Codex, as global instructions |

They carry the same markers and the same `pika:region` digest as a projection,
so a hand edit inside them is `tampered` on the same terms. Everything outside
the markers is yours and survives every regeneration — a home-directory
`AGENTS.md` is where you keep notes about every tool you use, not only this one.
The `pika:source` lines cite `template` rather than `skill`, because a global
file is installed where no repository exists and is therefore generated from the
templates inside the pika binary. Upgrading pika makes them `stale`, which is
the signal to re-run the install.

Two rules are worth stating plainly.

**No gate checks these files.** They are absent from a fresh checkout by
definition, so a gate that digested them would fail on every clone of every
repository. `pika skills --global` and `pika doctor` report their state; nothing
enforces it.

**No contract can cause one of them to be written.** `--global` on a command
line is the whole of the authorization. A projection path that is absolute,
`~`-prefixed, or climbs out of the repository with `..` is a contract error, not
a write, and the `skills` block has no key that asks for a global install at any
spelling. Otherwise cloning a repository would hand it a capability over the
machine that cloned it.

`--home <dir>` points the whole of `--global` at a directory other than your
home. It exists so a test or a sandbox never touches the real one.

---

## Typical loops

**New project, end to end:**

```sh
mkdir my-app && cd my-app
pika init --profile typescript
pika check --all
git init && git add -A && git commit -m "init: pika scaffold"
```

**Existing project, end to end:**

```sh
cd my-legacy-repo
pika adopt        # read-only; review/adoption-review.md tells you what it found
pika apply        # transactional; review/adoption-review.md becomes APPLIED
pika doctor       # what is configured, without running a gate
pika check --all  # baseline green (or honest skips for missing toolchains)
```

**Handing the repository to an agent:**

```sh
pika authorize --scope project   # writes .project/state/envelope.yaml (0600, local-only)
pika doctor                      # confirms the grants
pika mcp                         # serve the kernel to the agent
```

**Everyday:**

```sh
pika check --changed  # while iterating; degrades loudly to --all when git cannot answer
pika check --all      # before pushing
```

**Delegating a piece of work:**

```sh
pika work "add a /healthz endpoint that returns 200"
pika status                       # the run, its phases, and how it ended
pika resume <work-id>             # only if it was interrupted
git log -1 chore/pika-improve     # the verified commit, still local and unpublished
```

**Something is off:**

```sh
pika doctor                       # root, contract, lock, exceptions, envelope, recovery, leases, gates, global skills, git
pika explain <rule-or-gate-or-code>
pika recover                      # a transaction or a run that never finished; --apply to act
pika skills check                 # a projection whose source moved, or whose region was edited
pika skills --global              # the agent files in your home directory: installed, stale, tampered or absent
```

---

## Exit codes

All commands: `0` success, `1` failure, `2` usage/config error.

## JSON output

Every `--json` payload is the same envelope, whatever produced it:

```json
{
  "schema": 1,
  "command": "check",
  "ok": true,
  "result": { }
}
```

`result` is the command's own report, unchanged — `check` nests the verification report, `doctor` its findings, `init` the created-file manifest. `ok` is the boolean the exit code is derived from: `ok:false` means exit `1`.

A usage or configuration error (exit `2`) replaces `result` with `error` and prints nothing on stderr:

```json
{
  "schema": 1,
  "command": "check",
  "ok": false,
  "error": {"code": "usage", "message": "unexpected argument \"junk\""}
}
```

`code` is `usage` (the invocation was wrong) or `config` (the repository state prevents the command from running). If `--json` itself could not be parsed — an unknown flag, for instance — the error is plain text on stderr instead, because there is no payload to put it in.

`schema` is the envelope version and only changes on a breaking shape change; `pika mcp` is not covered by it, as it speaks JSON-RPC rather than `--json`.

## Where state lives

| Path | What | Committed? |
|---|---|---|
| `.project/contract.yaml` | Live project contract | Yes |
| `.project/profiles.lock` | Pinned profile digests | Yes |
| `.project/exceptions.yaml` | Recorded naming exceptions | Yes |
| `.project/evidence/<work-id>.json` | Kernel-issued evidence receipt for one run — schema-validated and redacted | Yes |
| `.project/state/` | Board, recovery journals, run records, agent transcripts (redacted at write time, never published) | **Never** (gitignored by init) |
| `.project/state/work/<work-id>/` | One run's durable record and its handoff bundle | **Never** |
| `.project/state/recovery/` | Transaction journals, per-op backups, and the recovery lock | **Never** |
| `.project/state/run.lock` | The whole-repository run lease, present only while a run is in flight — or after one was killed holding it | **Never** |
| `.project/state/locks/` | One file per MCP scope lease, named for the path it covers | **Never** |
| `.project/state/envelope.yaml` | Capability envelope — mode 0600, local-only, machine-specific | **Never** |
| `.agents/skills/<name>/SKILL.md` | Canonical agent instructions — operator-owned, the only copy anyone edits | Yes |
| projection targets (`AGENTS.md`, `CLAUDE.md`, …) | Generated regions inside operator-owned files; the region is kernel-owned and digested, the rest is not | Yes |
| `review/adoption-review.md` | Human-readable adoption review | Yes |

## Upgrading: a `profiles.lock` and a pika that disagree

M1.5, M2 and M3 each rotated the embedded pack digests. Any
`.project/profiles.lock` written on one side of those changes fails gate 1
against a binary from the other — the lock is doing its job; the packs really
did change. M2's edits were to `go@1` (`gofmt -l .` with the new
`fail-on-output` flag) and `python@1` (`ruff format --check .`, and `pytest`
in place of `python -m pytest`). M3 changed no pack YAML at all: it folded
each pack's **templates** into its digest, so `core@1` rotated because its
`ci.yml.tmpl` is now part of what the pack is.

The failure prints both numbers and refuses to guess which of them is behind:

```
profiles.lock: profiles.lock records registry digest e824…2fdf, and this
pika's embedded pack registry is f34a…9e9e. The lock and this pika disagree
about the pack bytes, and the digests alone cannot say which side is behind:
either the lock is stale (the packs moved on after it was written) or this
binary is stale (the lock is correct and this pika predates it). Compare
`pika version` here against the pika that wrote the lock, or read the lock's
provenance in version control, to establish which side is behind; only if the
lock is the stale side, regenerate it with `pika init --force` — running that
on a stale binary rewrites a correct lock to pin older packs and downgrades
the repository silently.
```

**Establish which side is behind first.** The two causes have opposite
remedies, and the destructive one is silent: `pika init --force` driven by an
old binary rewrites a correct lock to pin whatever packs that build carries,
and the gate then reports green on a downgraded repository.

```sh
pika version --root .    # does THIS binary's registry match the lock?
which -a pika            # any other pika on this machine? ask each one
git log -1 -- .project/profiles.lock   # who wrote the lock, and when
```

If the lock's digest matches some other pika you have, that build wrote it and
the one that fails is the older one: upgrade or rebuild pika (`go build
./cmd/pika`, or reinstall) and re-run `pika check --all`. Nothing in the
repository needs to change.

If the lock is the stale side — the packs really did move on, which is what a
milestone upgrade looks like — the remedy is one command, and since M3 it
needs no arguments and costs you nothing you wrote:

```sh
git status --porcelain   # clean: the diff below is then only the command's work
pika init --force        # profiles, name and module are read back from the repository
git diff                 # the contract, the lock, the PR template, the CI workflow
pika check --all         # gate 1 goes green again
```

`--force` rewrites only what the kernel owns and leaves your `README.md`,
`AGENTS.md`, `CONTRIBUTING.md`, `.gitignore`, language scaffold and
`.project/exceptions.yaml` exactly as they are.
[§1 has the full split](#--force-regenerates-more-than-the-lock), including
why `--module` is inert here. Until M3 this command was the destructive
operation the older text on this page warned about; `--reset-docs` is now the
only way to ask for that behavior.

There is deliberately no in-place lock repair: a lock you can hand-edit back
to green proves nothing.

### The template blind spot is closed

[M2 recorded a gap](../reference/m2-delta.md#gap-1--a-template-only-pack-change-is-invisible-to-every-adopted-repository):
a pack change touching only `core@1`'s **templates** rotated no digest, so
gate 1 could not tell an adopted repository that its CI workflow was out of
date, and `pika apply` would not replace a file that already existed. **M3
closed both halves.** Pack templates are inside the pack digest, so a
corrected template now fails gate 1 by name; and `pika apply` refreshes the
two kernel-owned files rather than skipping them. The cost is the digest
rotation above — one more, for everyone, which is why it is paired with a
`--force` you can run without bracing for it. See
[../reference/m3-delta.md](../reference/m3-delta.md#6-the-template-blind-spot-is-closed-and-what-it-cost).
