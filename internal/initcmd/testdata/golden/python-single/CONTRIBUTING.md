# Contributing to python-single

## Before you open a pull request

1. Run `projectctl check` and make sure every gate passes.
2. Update the documentation spine (`docs/`) when behavior or
   architecture changes.
3. Record deliberate naming-rule deviations in
   `.project/exceptions.yaml` instead of working around them.

## Conventions

- Branch names follow `{type}/{slug}` with types feat, fix, docs,
  refactor, chore.
- Pull requests stay in draft until checks pass and are squash-merged.
