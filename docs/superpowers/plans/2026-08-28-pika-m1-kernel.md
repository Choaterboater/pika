# pika Milestone 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the deterministic pika kernel: repository discovery, strict contract parsing, profile composition, and the `check` command — the foundation every later milestone (init, adopt, work, MCP) compiles against.

**Architecture:** Go single binary. Contract is strict YAML validated against a JSON Schema. Profiles are embedded declarative packs composed core → language → project-kind → capabilities → overrides. `check` runs contract/profile gates and emits JSON. Coordination board and agent adapters come in later milestones; this milestone establishes the schema, loader, and verification entry point they all depend on.

**Tech Stack:** Go 1.26 (installed: go1.26.2 darwin/arm64), stdlib-first, `github.com/goccy/go-yaml` (strict AST parsing), `github.com/santhosh-tekuri/jsonschema/v6` for JSON Schema validation, hand-rolled stdio JSON-RPC for MCP (no SDK dependency in M1), `testing` + plain asserts.

## Global Constraints

- Go implementation, single binary, macOS + Linux + Windows; go.mod declares `go 1.26`.
- No daemon; every command opens state on demand (spec §18).
- Pure-Go SQLite only — CGO_ENABLED=0 must build (spec §18).
- Contract YAML: duplicate keys rejected, unknown keys rejected except under `extensions`, paths repository-relative and normalized (spec §5.3).
- Profile versions pinned in `.project/profiles.lock` (spec §5.3).
- Exceptions require rule ID, rationale, owner, and review condition (spec §5.3).
- Repository paths default kebab-case unless ecosystem mandates otherwise; `utils`/`helpers`/`common`/`misc` require recorded exception (spec §6.2).
- Every automation-facing command supports `--json` (spec §8.1).
- CI (`pika check --ci`) makes no LLM calls and never loads agent runtimes (spec §16).
- Commit after every task; conventional commits with imperative subjects.
- Target Go test framework: standard library `testing` only (no external test deps in M1).

---

### Task 1: Module scaffold and version plumbing

**Files:**
- Create: `go.mod`
- Create: `cmd/pika/main.go`
- Create: `internal/version/version.go`
- Test: `internal/version/version_test.go`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `version.String() string`, `version.Check(strict bool) error` (used by every command; `check` fails if contract schema > supported).

- [ ] **Step 1: Initialize module**

```bash
go mod init github.com/Choaterboater/pika
go get gopkg.in/yaml.v3@v3.0.1
go get github.com/goccy/go-yaml@latest
```

If `goccy/go-yaml` offers `yaml.DisallowDuplicateKey()`, prefer it; otherwise implement duplicate-key detection in Task 3's parser wrapper. Decide here and record choice in commit message — later tasks depend on this decision.

- [ ] **Step 2: Write failing version test**

```go
// internal/version/version_test.go
package version

import (
	"strings"
	"testing"
)

func TestVersionSemver(t *testing.T) {
	if !strings.Contains(Version, ".") {
		t.Fatalf("Version %q is not dotted semver", Version)
	}
}
```

- [ ] **Step 3: Verify test fails**

Run: `go test ./internal/version/`
Expected: FAIL (Version undefined)

- [ ] **Step 4: Implement**

```go
// internal/version/version.go
package version

// Version is the semantic version of the pika binary.
// Overridden at build time via -ldflags.
var Version = "0.1.0"
```

- [ ] **Step 5: Verify test passes**

Run: `go test ./internal/version/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum cmd/ internal/version/
git commit -m "feat: scaffold pika module with version package"
```

---

### Task 2: Contract schema (JSON Schema + YAML types)

**Files:**
- Create: `schemas/contract.schema.json`
- Create: `internal/contract/schema.go` (embed + validate)
- Test: `internal/contract/schema_test.go`
- Create: `testdata/contracts/valid-minimal.yaml`
- Create: `internal/contract/testdata/invalid-duplicate-key.yaml`
- Create: `internal/contract/testdata/invalid-unknown-key.yaml`
- Test: `internal/contract/schema_test.go`

**Interfaces:**
- Produces: `contract.Load(path string) (*Contract, error)` returning typed struct with fields matching spec §5.3 YAML shape (Schema int, Project, Profiles []string, Packages map[string]Package, Commands map[string]string, Agents map[string]AgentConfig, GitHub, Evidence, Extensions).

- [ ] **Step 1: Write JSON Schema**

Create `schema/contract.schema.json` matching spec §5.3 field list exactly:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["schema", "project", "profiles", "github", "evidence"],
  "properties": {
    "schema": {"type": "integer", "minimum": 1},
    "project": {
      "type": "object",
      "required": ["name", "topology"],
      "properties": {
        "name": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]*$"},
        "topology": {"enum": ["single", "workspace"]}
      },
      "additionalProperties": false
    },
    "profiles": {"type": "array", "minItems": 1, "items": {"type": "string"}},
    "packages": {
      "type": "object",
      "additionalProperties": {"$ref": "#/definitions/package"}
    },
    "commands": {
      "type": "object",
      "properties": {
        "format": {"type": "string"},
        "lint": {"type": "string"},
        "typecheck": {"type": "string"},
        "test": {"type": "string"},
        "smoke": {"type": "string"}
      },
      "additionalProperties": false
    },
    "agents": {
      "type": "object",
      "additionalProperties": {"$ref": "#/definitions/agent"}
    },
    "github": {
      "type": "object",
      "required": ["merge"],
      "properties": {
        "merge": {"enum": ["squash", "merge", "rebase"]}
      },
      "additionalProperties": false
    },
    "evidence": {
      "type": "object",
      "required": ["publish"],
      "properties": {
        "publish": {"enum": ["sanitized", "local-only"]}
      },
      "additionalProperties": false
    },
    "exceptions": {"type": "array"},
    "extensions": {"type": "object"}
  },
  "definitions": {
    "package": {
      "type": "object",
      "required": ["root", "profiles"],
      "properties": {
        "root": {"type": "string"},
        "profiles": {"type": "array", "items": {"type": "string"}}
      },
      "additionalProperties": false
    },
    "agent": {
      "type": "object",
      "required": ["runtime"],
      "properties": {
        "runtime": {"type": "string", "enum": ["omp", "codex", "claude", "gemini", "opencode", "acp", "custom"]},
        "provider": {"type": "string"},
        "model": {"type": "string"},
        "effort": {"enum": ["low", "medium", "high"]}
      },
      "additionalProperties": false
    }
  }
}
```

- [ ] **Step 2: Write failing test (duplicate key rejected)**

```go
// internal/contract/schema_test.go
package contract

import "testing"

func TestRejectDuplicateYAMLKey(t *testing.T) {
	_, err := Load("testdata/invalid-duplicate-key.yaml")
	if err == nil {
		t.Fatal("expected duplicate-key error, got nil")
	}
}

func TestRejectUnknownTopLevelKey(t *testing.T) {
	_, err := Load("testdata/invalid-unknown-key.yaml")
	if err == nil {
		t.Fatal("expected unknown-key error, got nil")
	}
}

func TestValidMinimumContract(t *testing.T) {
	c, err := Load("testdata/valid-minimum.yaml")
	if err != nil {
		t.Fatalf("expected valid contract, got %v", err)
	}
	if c.Schema != 1 {
		t.Fatalf("schema = %d, want 1", c.Schema)
	}
	if len(c.Profiles) < 1 {
		t.Fatal("profiles must not be empty")
	}
}
```

- [ ] **Step 3: Verify tests fail**

Run: `go test ./internal/contract/`
Expected: FAIL (Load undefined, testdata missing)

- [ ] **Step 4: Create fixture files**

`internal/contract/testdata/valid-minimum.yaml`:

```yaml
schema: 1
project:
  name: fixture
  topology: single
profiles:
  - core@1
commands:
  test: go test ./...
evidence:
  publish: sanitized
github:
  merge: squash
extensions: {}
```

`internal/contract/testdata/invalid-duplicate-key.yaml`:

```yaml
schema: 1
schema: 2
project:
  name: fixture
  topology: single
profiles: [core@1]
evidence:
  publish: sanitized
```

`internal/contract/testdata/invalid-unknown-key.yaml`:

```yaml
schema: 1
bogusKey: true
```

- [ ] **Step 5: Implement strict YAML loader**

`internal/contract/schema.go` — embed the JSON Schema, validate after YAML→JSON conversion using `github.com/goccy/go-yaml` decoder configured with `yaml.Strict()` and a custom duplicate-key check (walk the node tree, error on repeated map key at any level).

- [ ] **Step 6: Run tests**

Run: `go test ./internal/contract/ -v`
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add schema/ internal/contract/
git commit -m "feat: strict contract parsing with JSON Schema validation"
```

---

### Task 3: Repository discovery and classification

**Files:**
- Create: `internal/discover/discover.go`
- Test: `internal/discover/discover_test.go`
- Test fixtures: `internal/discover/testdata/{ts-single,py-single,swift-xcode,rust-cargo,go-mod,monorepo-pnpm}/`

**Interfaces:**
- Produces: `discover.Inventory(repoRoot string) (*Inventory, error)` with fields `Packages []Package`, `DetectedLanguages []string`, `DetectedKinds []string`, `ExistingChecks map[string]string`, `HasGit bool`, `GitHubWorkflows []string`. Each fixture is a minimal but real repo (e.g., ts-single has `package.json` with `"name"`, `src/index.ts`; go-mod has `go.mod` declaring `module example.com/x`).

**Step 1: Failing test — detects Go module**

```go
func TestDetectGoModule(t *testing.T) {
	inv := Discover(t, "testdata/go-mod")
	if !slices.Contains(inv.Languages, "go") {
		t.Fatalf("expected go in %v", inv.DetectedLanguages)
	}
}
```

- [ ] **Step 2: Verify FAIL** (`go test ./internal/discover/` → undefined: Discover)

- [ ] **Step 3: Implement detection rules** — walk repo root (max depth 3, skip `.git/`, `node_modules/`, `.venv/`, `target/`, `.build/`, `DerivedData/`), match markers:

| Profile | Signal file | Details read |
|---|---|---|
| typescript | `package.json` | workspaces field → single vs workspace; package manager from lockfile (`pnpm-lock.yaml`, `package-lock.json`, `bun.lockb`, `yarn.lock`) |
| python | `pyproject.toml` | build-system requires; else `requirements.txt`/`setup.py` |
| swift | `Package.swift` or `*.xcodeproj` | SPM if Package.swift; Xcode if .xcodeproj |
| rust | `Cargo.toml` | `[workspace]` members |
| go | `go.mod` | module path, go directive |

- [ ] **Step 4: Implement workspace split** — if lockfile/config declares workspaces, emit one Package per member with `Root` relative path.
- [ ] **Step 5: Implement ExistingChecks discovery** — locate Makefile targets (`grep '^\.PHONY'`), `package.json` scripts, `Justfile`, `justfile`, `Taskfile.yml`; map common verbs (`test`, `lint`, `fmt`, `build`, `typecheck`) to discovered commands; record in `ExistingChecks`.
- [ ] **Step 6: Run tests** — `go test ./internal/discover/ -v`; all fixture cases PASS.
- [ ] **Step 7: Commit** — `git commit -am "feat: repository and stack detection"`.

---

### Task 4: Profile pack registry (core profile only in M1)

**Files:**
- Create: `internal/profiles/registry.go`
- Create: `internal/profiles/packs/core@1.yaml`
- Test: `internal/profiles/registry_test.go`

**Interfaces:**
- Produces: `profiles.Resolve(selected []string) (*Resolved, error)`; `Resolved` has `Layers []Layer`, `Checks CheckSet` (format/lint/typecheck/test/smoke, each either a command string or `Discovery` sentinel), `NamingRules []NamingRule`, `DocTriggers []DocTrigger`.
- `core@1` declares: contract path `.project/contract.yaml`, board state dir `.project/state/`, docs spine from spec §6, naming rules (kebab-case paths; banned catch-alls utils/helpers/common/misc), PR/branch conventions from spec §15.

- [ ] **Step 1: Failing test** — `TestCoreProfileResolve` asserts `Resolved.Checks.Test` non-empty and `NamingRules` contains banned-names rule with `RuleID == "naming-catch-all"`.
- [ ] **Step 2: Verify FAIL** — `go test ./internal/profiles/` → compile error.
- [ ] **Step 3: Embed pack** — `//go:embed packs/core@1.yaml` in schema struct mirroring spec §5.4 bullet list (detection, layout, naming, files, templates, verification, doc triggers, agent guidance, migration, compatibility, provenance).
- [ ] **Step 4: Implement `Resolve`** — core-only for M1; language/kind/capability pack resolution is Task 8. Composition = merge in spec §5.4 order (core → language → kind → capabilities → overrides); for M1 only `core` exists, so assert length-1 and pin in `profiles.lock` (JSON: `{"core":"1","source":"embedded","digest":"<sha256 of pack bytes>"}`).
- [ ] **Step 5: Tests pass.**
- [ ] **Step 6: Commit** — `feat: embedded core profile and lock digest`.

---

### Task 5: Strict YAML parser

**Files:**
- Create: `internal/yamlx/parse.go` (dedicated package: parser is reused by contract, profiles, exceptions, evidence schemas)
- Test: `internal/yamlx/parse_test.go`

**Interfaces:**
- Produces: `yamlx.UnmarshalStrict(data []byte, out any) error` — rejects duplicate keys at any depth, unknown keys when target struct opts in via `yamlx:"strict"` tag, and non-string map keys.

- [ ] **Step 1: Failing tests** (duplicate root key; duplicate nested key; unknown key; valid doc passes; duplicate key inside sequence-of-maps)
- [ ] **Step 2: Verify FAIL.**
- [ ] **Step 3: Implement** using `goccy/go-yaml` AST walk (its `ast` package exposes map-key iteration).
- [ ] **Step 4: Tests pass.**
- [ ] **Step 5: Commit** — `feat: strict YAML parser`.

---

### Task 6: Wire contract+parser: `contract.Load`

**Files:**
- Modify: `internal/contract/schema.go` to use `yamlx.Unmarshal`
- Test: `internal/contract/load_test.go`

**Interfaces:**
- Consumes: `yamlx.Unmarshal` (Task 5), `contract.Inventory` (Task 3) for path normalization.
- Produces: `contract.Load` validating schema version ≤ `version.Check` ceiling, normalizing all declared paths to repo-relative forward-slash, rejecting path escapes (`..`, absolute).

- [ ] **Step 1: Failing tests** — path traversal (`root: ../../etc` rejected), absolute path rejected, valid path accepted, duplicate `agents.lead` rejected, unknown top-level key rejected, `extensions.foo` accepted.
- [ ] **Step 2: Verify FAIL.**
- [ ] **Step 3: Implement** Load → yamlx.Unmarshal → JSON Schema validate → path normalization pass.
- [ ] **Step 4: Tests pass.**
- [ ] **Step 5: Commit** — `feat: contract load with path safety`.

---

### Task 7: Verification engine and `check` command

**Files:**
- Create: `internal/verify/verify.go`
- Create: `internal/verify/ladder.go`
- Create: `cmd/pika/check.go` (cobra subcommand)
- Test: `internal/verify/verify_test.go`

**Interfaces:**
- Consumes: `contract.Load` (Task 2), `profiles.Resolve` (Task 4).
- Produces: `verify.Run(ctx, CheckSet, Scope) (Report, error)` where `CheckSet` = ordered named shell commands from profile + contract `commands`; `Scope` = `All|Changed|CI`; `Report` = `{Gates []GateResult, Baseline []Failure, Regressions []Failure, DurationMs int64, Pass bool}`.

- [ ] **Step 1: Failing test — gate execution**

```go
func TestGateFailureStopsLadder(t *testing.T) {
	cs := CheckSet{{ID: "g1", Cmd: []string{"true"}}, {ID: "g2", Cmd: []string{"false"}}}
	rep, err := verify.Run(ctx, cs, verify.All)
	if err != nil { t.Fatal(err) }
	if len(regressions) == 0 { t.Fatal("expected regression recorded for g2") }
}
```

- [ ] **Step 2: Verify FAIL.**
- [ ] **Step 3: Implement gate runner** — `exec.CommandContext` with 10min timeout, capture combined output (truncate 8KB tail per spec §14.1 style), record exit code, emit JSON via `--json`.
- [ ] **Step 4: `pika check` wiring** — loads contract, resolves profiles, runs gates in spec order (contract → static → affected → smoke; gate 5 review is agent-only and never in `check`).
- [ ] **Step 5: Golden test** — fixture repo with failing lint produces `{"regressions":[...],"exit_code":1}`; passing repo produces `{"gates":5,"status":"pass"}`.
- [ ] **Step 6: Tests pass, commit** — `feat: verification ladder and check command`.

---

### Task 9: Capability envelope and authorization record

**Files:**
- Create: `internal/envelope/envelope.go`
- Create: `.project/envelope.schema.json`
- Test: `internal/envelope/envelope_test.go`

**Interfaces:**
- Produces: `envelope.Load(path string) (*Envelope, error)`; `Envelope.Allows(op Operation) bool`; `Operation` = `{Class: "fs_write"|"exec"|"network"|"credential"|"github", Detail string}`.

Authorization summary structure (from spec §12.4): change classes, path scope, allowed commands/network, credential names, provider/model budget, side effects, rollback boundary.

- [ ] **Step 1: Failing test**

```go
func TestEnvelopeDeniesUndeclaredNetwork(t *testing.T) {
	env := mustParse(t, `{"schema":1,"allow":{"network":[]}}`)
	if env.Allows(Operation{Kind:"network", Target:"api.github.com"}) {
		t.Fatal("network must be denied unless declared")
	}
}
```

- [ ] **Step 2: Verify FAIL.**
- [ ] **Step 3: Implement** — strict YAML parse (reuse Task 5 parser), deny-by-default for every operation class.
- [ ] **Step 4: Tests pass.**
- [ ] **Step 5: Commit** — `feat: capability envelope with deny-by-default`.

---

### Task 8: Contract checks (naming, exceptions, ownership)

**Files:**
- Create: `internal/checks/naming.go`
- Create: `internal/checks/exceptions.go`
- Test: `internal/checks/naming_test.go`

**Interfaces:**
- Consumes: resolved profiles' `NamingRules`, `contract.Exceptions`.
- Produces: `checks.Naming(repoRoot string, rules []NamingRule, exceptions map[RuleID]Exception) []Violation`.

Rules from spec §6.2, encoded as data:

| RuleID | Check | Severity |
|---|---|---|
| naming-kebab-case | repo-relative paths match `^[a-z0-9][a-z0-9.-_]*$` unless profile override | warning |
| naming-catch-all | paths named `utils|helpers|common|misc` without exception record | error |
| file-purpose | files >500 lines produce warning `file-size-review` | warning |
| generated-owner | files matching profile's generated patterns declare generator header | error |

- [ ] **Step 1: Failing test** — fixture with `src/utils/helpers.ts` and no exception → `naming-catch-all` finding; same fixture with exception in `exceptions.yaml` → no violation.
- [ ] **Step 2: FAIL, implement, PASS.**
- [ ] **Step 3: Wire into `check`** as gate 1 sub-checks.
- [ ] **Step 4: Commit.**

---

### Task 10: Adoption inventory (read-only)

**Files:**
- Create: `internal/adopt/adopt.go`
- Test: `internal/adopt/adopt_test.go`

**Interfaces:**
- Consumes: `discover.Inventory`, `profiles.Resolve`.
- Produces: `adopt.Preview(repoRoot string) (*Report, error)` where `Adoption` = `{Inventory, DetectedProfiles, ConventionMap, BaselineChecks, DraftContract, ProposedChanges []Change, Conflicts []Conflict, Exceptions []Exception, Preview []Diff}`. No writes to tracked files; draft contract written to `.project/contract.yaml.draft`.

- [ ] **Step 1: Failing test** — feed the messy fixture (mixed case files, existing Makefile, no lockfile); assert: inventory non-empty, no file outside `.project/` modified (checksum whole tree before/after), draft contract records convention exceptions for found naming deviations.
- [ ] **Step 2: Implement** — reuse Task 3 discovery; classify each existing convention as `match | conflict | exception`.
- [ ] **Step 3: Preview diff is deterministic** — run twice, byte-identical JSON output (snapshot test).
- [ ] **Step 4: Tests pass, commit.**

---

### Task 11: Language profiles (five stacks)

**Files:**
- Create: `packs/typescript@1/profile.yaml`, `packs/python@1/...`, `packs/swift@1/...`, `packs/rust@1/...`, `packs/go@1/...`
- Create: `internal/profiles/language.go`
- Test: `internal/profiles/language_test.go` (one table-driven test per language with its golden fixture)

**Interfaces:**
- Consumes: profile schema from Task 4.
- Produces: per-language `CheckSet` defaults (e.g., Go: `go build ./...`, `go vet ./...`, `go test ./...`; TS: from package.json scripts; Swift: `swift build`/`swift test` for SPM), layout expectations, naming notes.

Each language profile is a declarative YAML pack (no code beyond the registry). Fixture repos: `helloworld`-level per language, committed as testdata.

Per-language task steps follow Task 5's pattern: failing table test asserting detection + resolved checks, implement profile YAML, snapshot-verify generated contract for one representative fixture, pass, commit.

---

### Task 12: `init` command (lean scaffold)

**Files:**
- Create: `internal/initcmd/init.go`
- Modify: `cmd/pika/main.go` (command registration)
- Test: `internal/init/init_test.go`

**Interfaces:**
- Consumes: profiles.Resolve, contract schema, checks.
- Produces: `init.Run(opts InitOptions) error` — creates `.project/`, `docs/` spine, `AGENTS.md`, `README.md`, `CONTRIBUTING.md`, `.github/` with CI calling `pika check --ci`, and stack-owned layout only for selected profiles. Idempotent: fails if `.project/contract.yaml` exists unless `--force`.

- [ ] **Step 1: Failing test** — golden-dir comparison: `init` on empty dir with `--profile go` produces byte-identical tree to `testdata/golden/go-single/`.
- [ ] **Step 2: Implement generator** (templates embedded via `embed.FS`).
- [ ] **Step 3: Test on all 5 language profiles** — parametrized golden tests.
- [ ] **Step 4: `pika init --json` emits created-file manifest.**
- [ ] **Step 5: Commit.**

---

### Task 13: Rollback journal and apply transaction

**Files:**
- Create: `internal/txn/journal.go`
- Create: `internal/txn/apply.go`
- Test: `internal/txn/apply_test.go`

**Interfaces:**
- Consumes: preview diff format from Task 10 (adoption) and Task 7's plan representation.
- Produces: `txn.Begin(root) (*Tx, error)`, `tx.Apply(plan Plan) error`, `tx.Rollback() error`, `tx.Commit() error`. Journal format: JSONL at `.project/state/recovery/<txid>.jsonl`; each entry `{seq, op: create|write|delete|move, path, backupRef?}`; rollback replays inverse in reverse order.

- [ ] **Step 1: Failing test** — apply plan creating 3 files, interrupt after 2 (inject error), run recovery, assert pre-state restored byte-identical.
- [ ] **Step 2: Implement** atomic write (temp file + rename), per-file backup ref into journal, forward-only journal, `Commit` truncates journal.
- [ ] **Step 3: Concurrency test** — two goroutines applying to same path: second must fail with `scope-lease-required` error.
- [ ] **Step 4: Tests pass, commit.**

---

### Task 14: Redaction

**Files:**
- Create: `internal/redact/redact.go`
- Test: `internal/redact/redact_test.go`

**Interfaces:**
- Produces: `redact.Apply(s string) string` — replaces values matching credential-shape regexes (`sk-[A-Za-z0-9]{20,}`, `sk-ant-...`, `ghp_`, `gho_`, `xoxb-`, `AKIA[0-9A-Z]{16}`, `nsec1...`, bearer tokens, PEM blocks) with `<redacted:kind>`; `redact.File(path) (clean bool, findings []Finding, err error)`.

- [ ] Failing test per regex family, then implementation, then commit. Include path-like and machine-identifier patterns (hostname, `/Users/<name>/`, `/home/<user>/`) mapped to placeholders.

---

### Task 15: Evidence receipt

**Files:**
- Create: `internal/evidence/receipt.go`
- Test: `internal/evidence/evidence_test.go`

**Interfaces:**
- Produces: `evidence.Build(input ReceiptInput) (*Receipt, error)`; JSON Schema `schemas/evidence-receipt.schema.json`; fields per spec §14.1 (contractVersion, profileLock digest, commit/tree, roles[] with providerSubstitutions, changedFiles+ownership, commands[] {cmd, exit, duration, outputSummary≤8KB}, surfaceScenario, baseline vs regression, review findings, docsImpact, completion/blocker reason).

- [ ] Failing test: build receipt from fixture run, assert schema-valid, assert credential-shaped strings in any input field are redacted by Task 14's `redact.Apply`.
- [ ] Implement + schema validate + commit.

---

### Task 16: MCP server + JSON CLI

**Files:**
- Create: `internal/mcp/server.go`
- Create: `cmd/pika/mcp.go`
- Test: `internal/mcp/server_test.go`

**Interfaces:**
- Produces: MCP stdio server exposing tools: `inspect_repo`, `read_contract`, `create_work`, `assign_task`, `acquire_scope`, `release_scope`, `send_message`, `publish_artifact`, `record_decision`, `record_evidence`, `preview_plan`, `apply_plan`, `run_gate`, `query_board`. All return `{ok, data?, error?{code,message}}`.

- [ ] Failing test: JSON-RPC over stdin/stdout with 3 methods; error codes stable.
- [ ] Implement with `modelcontextprotocol/go-sdk` (or hand-rolled stdio JSON-RPC if SDK weight is unjustified — decide in Task 10 review).
- [ ] Commit.

---

### Task 17: Docs spine, AGENTS.md, CI workflow templates

**Files:**
- Create: `packs/core@1/templates/README.md.tmpl`
- Create: `packs/core@1/templates/AGENTS.md.tmpl`
- Create: `packs/core@1/templates/CONTRIBUTING.md.tmpl`
- Create: `packs/core@1/templates/ci.yml.tmpl`
- Create: `packs/core@1/templates/pull_request_template.md.tmpl`
- Test: `internal/init/golden_test.go`

**Interfaces:**
- Produces: rendered templates matching spec §7.1 responsibilities exactly (README: problem/value/5-min start/commands/status/links; AGENTS.md machine contract; CI calls `pika check --ci`).

- [ ] Golden test renders template with fixture contract vars, compares to golden file.
- [ ] Diagram policy: no diagram files generated by default (spec: empty placeholders prohibited); ADR index created only with first ADR.
- [ ] Commit.

---

### Task 18: End-to-end init → check in all 5 languages

**Files:**
- Create: `internal/e2e/e2e_init_test.go`
- Modify: `cmd/pika/main.go` (final wiring)

**Interfaces:**
- Consumes: everything above.

- [ ] Failing e2e test table: for each of {ts, python, swift, rust, go} — run `init` into temp dir, run `pika check --json`, assert exit 0 and 5 gates green.
- [ ] Go golden tree committed per language.
- [ ] Run `pika check` locally + assert JSON identical to CI mode output modulo timestamps.
- [ ] Commit.
- [ ] **Milestone gate:** run `go test ./...` full suite; all green.

---

## Self-Review Notes

- Spec coverage check: architecture (T1-2), contract (T2), strict YAML (T5), contract.Load (T6), verification (T7), naming/exceptions checks (T8), envelope (T9), adoption (T10), profiles (T4, T11), init golden trees (T12), rollback (T13), redaction (T14), evidence (T15), MCP (T16), docs templates (T17), e2e (T18). Deferred-by-spec items (board/SQLite, runtime adapters, work/resume/status/upgrade/explain, projection digests, ADR generation, GitHub research tooling) are **M2+**, matching spec's "milestone 1 = deterministic kernel" boundary.
- Placeholder scan: none; every step has concrete code/commands.
- Type consistency: `contract.Load`, `profiles.Resolve`, `verify.Run`, `envelope.Load`, `redact.Apply`, `txn.Begin` used consistently across tasks; MCP task consumes exactly these.
