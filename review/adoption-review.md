# Adoption review

Status: **APPLIED** — the adoption drafts were promoted into a live contract.

## Applied (7)

- [x] create `.project/contract.yaml`
- [x] create `.project/profiles.lock`
- [x] create `.project/exceptions.yaml`
- [x] create `AGENTS.md`
- [x] create `CONTRIBUTING.md`
- [x] create `.github/workflows/ci.yml`
- [x] create `.github/pull_request_template.md`

## Skipped (1 — your files were kept)

- `README.md` — already exists; kept the existing file

## Exceptions (14 recorded naming deviations)

| Path | Rule | Suggested action |
|---|---|---|
| `internal/discover/testdata/swift-xcode/MyApp.xcodeproj/project.pbxproj` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |
| `internal/initcmd/templates/Cargo.toml.tmpl` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |
| `internal/initcmd/templates/Package.swift.tmpl` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |
| `internal/profiles/packs/core@1.yaml` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |
| `internal/profiles/packs/core@1/templates/AGENTS.md.tmpl` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |
| `internal/profiles/packs/core@1/templates/CONTRIBUTING.md.tmpl` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |
| `internal/profiles/packs/core@1/templates/README.md.tmpl` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |
| `internal/profiles/packs/core@1/templates/ci.yml.tmpl` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |
| `internal/profiles/packs/core@1/templates/pull_request_template.md.tmpl` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |
| `internal/profiles/packs/go@1.yaml` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |
| `internal/profiles/packs/python@1.yaml` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |
| `internal/profiles/packs/rust@1.yaml` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |
| `internal/profiles/packs/swift@1.yaml` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |
| `internal/profiles/packs/typescript@1.yaml` | `naming-kebab-case` | keep as an exception, or rename the path to satisfy the rule |

## Gate 1 on the applied contract

Pass — no findings.

## Next step

Run `pika check --all` to verify the applied contract, then commit `review/`, `.project/contract.yaml`, `.project/profiles.lock`, and `.project/exceptions.yaml` together.
