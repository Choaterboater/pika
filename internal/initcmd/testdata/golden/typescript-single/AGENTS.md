# Agent guidance for typescript-single

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
