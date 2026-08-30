# Agent guidance for pika

This repository is managed by pika. The contract at
`.project/contract.yaml` declares the active profiles, the verification
commands, and the repository conventions; `.project/profiles.lock` pins
the exact profile packs in use.

## Working in this repository

- Keep the contract, the lock, and the documentation spine in sync with
  every change.
- Record any deliberate deviation from a naming rule in
  `.project/exceptions.yaml`; each record needs a rule ID, a rationale,
  an owner, and a review condition.
- Never bypass the verification ladder: run `pika check` before
  declaring work done.

## Conventions

- Branches: `{type}/{slug}` with types feat, fix, docs, refactor, chore.
- Pull requests stay draft until checks pass.
- Merge strategy: squash.

## Dot-prefixed paths are exempt from pika's own rules

The naming walk (`internal/checks/naming.go`, `walkFiles`) skips every
dot-prefixed path segment — recorded in the M1.5 design as the dot-segment
exemption.
`.project/`, `.github/`, `.git/` and `.superpowers/` are therefore invisible
to `naming-kebab-case`, `naming-catch-all`, `file-size-review` and
`generated-owner`. Two consequences:

- Do not "fix" a name under `.project/` or `.github/` to satisfy a rule. The
  rule never looked at it.
- Do not record an exception for a dot-prefixed path either. It would be a
  dead record: nothing would ever have fired.

Everything outside a dot-prefixed segment — including `docs/`, `review/`,
`cmd/` and `internal/` — is walked normally.

## pika governs pika

This repository is adopted by its own kernel, so the workflow above is not
advice, it is enforced:

```sh
go build -o /tmp/pika ./cmd/pika
/tmp/pika doctor        # diagnose without running a gate
/tmp/pika check --all   # the full ladder
```

`.github/workflows/ci.yml` builds the binary from the commit under test —
never `go install ...@latest` — and runs `pika check --ci`, so a change that
breaks the verifier is caught by the verifier it breaks.

`naming-kebab-case` fires on 14 paths in this repository. Every one is a
decided, recorded exception in `.project/exceptions.yaml`: Xcode fixture
layout, init templates named for the file they emit, and profile-pack files
whose name is the pack identifier (`<profile>@<version>`). Before adding a
fifteenth, prefer renaming.

`file-size-review` warns on files over 500 lines. It is a **warning**: it
never fails a gate, and those warnings are deliberately left visible rather
than excepted away.

### Tests must be hermetic, and now it is enforced by consequence

pika's contract runs `go test ./... -count=1` as its `test` gate. A test that
resolves a repository root by discovery — no `--root`, no `t.Chdir` into a
fixture — resolves **this** checkout, and any command it then runs re-enters
the suite through the gate that started it. `TestImproveIsNotHijackedByAFlagValuedVersion`
did exactly that: it created a branch named `version` in the working
checkout and spawned the test gate recursively.

Every test that invokes a command MUST pin its root: `t.Chdir(t.TempDir())`
for in-process dispatch, or `cmd.Dir = <fixture>` / `--root <fixture>` for a
spawned binary.

## Milestone delta

`docs/reference/m1-5-delta.md` records what changed underneath users in
M1.5 — the rotated profile-pack digests, and which envelope classes actually
have an enforcement call site (`fs_write` and `exec`; the rest are
schema-only).
