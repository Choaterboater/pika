// Package apply implements `pika apply`: the transactional step that
// promotes adoption drafts into a live project contract. It closes the
// adoption loop opened by `pika adopt`: the drafts under .project/ are
// durable state, and apply derives everything it does from them — never
// from session memory — promotes them, writes the exceptions record and
// the core files the repository is still missing (create-if-missing:
// every required core file the repository already has — README.md,
// AGENTS.md, CONTRIBUTING.md, the language scaffold, the GitHub PR
// template, the CI workflow — is kept exactly as found, never
// overwritten), and rewrites the visible review bundle as APPLIED.
//
// Every required core file is create-if-missing, with no exception for
// the two the kernel renders (the PR template, the CI workflow): apply
// runs exactly once, on a repository it has never touched before (it
// refuses outright the moment a contract is already committed), so an
// existing file at one of those paths was never written by this or any
// prior pika — it is the operator's own, whatever its provenance, and
// treating it as a stale kernel copy to correct silently destroyed a
// repository's real CI workflow the first time a real one was adopted.
// `pika init --force` is the one command that regenerates a kernel-
// owned file the kernel itself previously wrote (a genuine upgrade,
// against a repository it scaffolded), and is unaffected: it is a
// wholly separate write path.
//
// Every mutation runs inside a txn transaction: a failure at any point
// after Begin rolls the repository back to its exact pre-state — and
// the report claims that rollback only when the undo actually
// completed. When the undo itself is refused (a commit error finishes
// the transaction; an undo error leaves it open with its journal
// intact), the error says the mutations may remain instead of claiming
// a pre-state that no longer holds. A failing gate 1 after a
// successful commit is NOT a rollback: the applied state is valid, it
// just carries findings, which the report and the review bundle state
// honestly.
package apply

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Choaterboater/pika/internal/adopt"
	"github.com/Choaterboater/pika/internal/checks"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/initcmd"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/skills"
	"github.com/Choaterboater/pika/internal/txn"
	"github.com/Choaterboater/pika/internal/version"
	"github.com/goccy/go-yaml"
)

// Repository-relative locations apply reads and writes.
const (
	// contractRel is the durable contract location; its presence means
	// the repository is already adopted.
	contractRel = ".project/contract.yaml"

	// lockRel is the durable profiles lock.
	lockRel = ".project/profiles.lock"

	// exceptionsRel is the naming-exceptions record.
	exceptionsRel = checks.ExceptionsFile

	// contractDraftRel and lockDraftRel are the adoption drafts apply
	// promotes. Both are required; neither is touched by apply.
	contractDraftRel = ".project/contract.yaml.draft"
	lockDraftRel     = ".project/profiles.lock.draft"
)

// Applied is one operation apply committed.
type Applied struct {
	Op   string `json:"op"`
	Path string `json:"path"`
}

// Skipped is one proposed file apply left on disk as it found it:
// an operator-owned file the user already has, or a kernel-owned file
// that already matches the rendered template. Reason says which.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Gate1 is verification-ladder rung 1's outcome on the applied
// contract. A failing gate does not roll apply back: the state is
// valid, it just has findings.
type Gate1 struct {
	Pass     bool     `json:"pass"`
	Output   string   `json:"output,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Report is apply's outcome. It is JSON-stable: slices are ordered by
// the deterministic plan, and no field carries a timestamp or duration.
type Report struct {
	Applied  []Applied `json:"applied"`
	Skipped  []Skipped `json:"skipped"`
	Rollback bool      `json:"rollback"`
	Gate1    Gate1     `json:"gate1"`
	// TransactionBegan is true once txn.Begin has succeeded — the
	// point past which a failure risks leaving mutations on disk.
	// It stays false's zero value for every failure Run returns
	// before that point (missing or invalid drafts, an unresolvable
	// profile selection, a plan the kernel could not build): nothing
	// was ever written, so nothing needs undoing. It distinguishes
	// that harmless case from the one genuinely dangerous one — a
	// commit or apply failure whose own rollback then failed too —
	// which is otherwise the same zero-valued Report (Rollback false,
	// Applied empty) and was, before this field existed, reported
	// identically to "nothing was applied" as "the pre-state could
	// not be restored; the transaction's mutations may remain": a
	// false alarm on every pre-transaction failure, misdirecting an
	// operator toward `pika recover`, which then correctly answers
	// "nothing to recover" — a contradiction from the one subsystem
	// whose entire job is trustworthy transactional honesty.
	TransactionBegan bool `json:"transactionBegan"`
}

// RunOptions configures Run.
type RunOptions struct {
	// Dir is the repository root (default ".").
	Dir string

	// failAfter, when > 0, applies only the first failAfter operations
	// of the plan and then fails, exercising the documented
	// Begin → Apply → Rollback contract. Test hook; zero in production.
	failAfter int
	// failCommit, when true, treats the commit as failed after it
	// completes, exercising the commit-failure path: the transaction
	// is already finished, so the undo is refused and the applied
	// mutations remain on disk. Test hook; false in production.
	failCommit bool
}

// Run applies the adoption drafts in opts.Dir. It returns an error —
// with nothing written — when the repository is already adopted, when
// either required draft is missing, or when a draft fails contract
// validation (path safety, schema, and strict YAML all apply to drafts).
// A mid-plan failure rolls the repository back to its exact pre-state
// and reports Rollback. A failing gate 1 after a successful commit is
// reported in the result, not rolled back.
func Run(opts RunOptions) (Report, error) {
	root := opts.Dir
	if root == "" {
		root = "."
	}

	// Preconditions, all fail-closed before a single byte is written.
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(contractRel))); err == nil {
		return Report{}, fmt.Errorf("apply: %s already exists: repository already adopted; use `pika check` instead", contractRel)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Report{}, fmt.Errorf("apply: stat %s: %w", contractRel, err)
	}
	var missing []string
	for _, draft := range []string{contractDraftRel, lockDraftRel} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(draft))); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				missing = append(missing, draft)
				continue
			}
			return Report{}, fmt.Errorf("apply: stat %s: %w", draft, err)
		}
	}
	if len(missing) > 0 {
		return Report{}, fmt.Errorf(
			"apply: missing adoption draft(s) %s — both %s and %s are required; run `pika adopt` first",
			strings.Join(missing, ", "), contractDraftRel, lockDraftRel)
	}

	// The drafts are the durable state: validate and read them, then
	// derive the whole plan from them.
	c, err := contract.Load(filepath.Join(root, filepath.FromSlash(contractDraftRel)))
	if err != nil {
		return Report{}, fmt.Errorf("apply: %s is invalid: %w", contractDraftRel, err)
	}
	// Fail closed on a draft the running toolchain cannot verify: the
	// schema-version ceiling applies to drafts before anything is
	// promoted.
	if err := version.Check(c.Schema); err != nil {
		return Report{}, fmt.Errorf("apply: %s is invalid: %w", contractDraftRel, err)
	}
	contractYAML, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(contractDraftRel)))
	if err != nil {
		return Report{}, fmt.Errorf("apply: read %s: %w", contractDraftRel, err)
	}
	lockDraftAbs := filepath.Join(root, filepath.FromSlash(lockDraftRel))
	lockYAML, err := os.ReadFile(lockDraftAbs)
	if err != nil {
		return Report{}, fmt.Errorf("apply: read %s: %w", lockDraftRel, err)
	}
	if _, err := profiles.ReadLock(lockDraftAbs); err != nil {
		return Report{}, fmt.Errorf("apply: %s is invalid: %w", lockDraftRel, err)
	}
	// checks.ProfileRefs(c) is deliberately NOT used here: it unions
	// every package's own Profiles into the repository-level
	// selection, and a repository whose packages span more than one
	// language (an ordinary shape — a Go module and a TypeScript
	// package sharing the repository root) would union to more packs
	// than profiles.Resolve composes, failing every such apply with
	// no remedy. c.Profiles is the contract's own repository-level
	// selection — core@1 alone when adopt could not name one
	// principled language for that slot, exactly the fallback Preview
	// already used to baseline this same repository's commands — and
	// resolving that is what determines the repository-level Commands
	// autofill and the rendered core files below. It is never asked to
	// answer the per-package composition question, because nothing
	// downstream of it does either: skills.Install and Gate1 read
	// resolved.NamingRules/AgentGuidance at the repository level only,
	// the same level c.Profiles already describes. The lock check
	// (gate1.CheckLock) verifies every pack any package references,
	// independently, via its own ProfileRefs(c) call — unaffected by
	// this.
	resolved, err := profiles.Resolve(c.Profiles)
	if err != nil {
		return Report{}, fmt.Errorf("apply: resolve draft profiles: %w", err)
	}
	// Same policy as `pika init`: a slot the draft leaves empty and the
	// pack declares as a discovery sentinel gets the pack's hint when
	// that tool is on PATH. Without it a repository can be applied with
	// every gate skipped — a green ladder that verifies nothing. Slots
	// adoption already discovered win; the draft is the user's stated
	// intent.
	if added := fillMissingCommands(c, initcmd.CommandsFromChecks(resolved.Checks)); added {
		contractYAML, err = yaml.Marshal(c)
		if err != nil {
			return Report{}, fmt.Errorf("apply: encode promoted contract: %w", err)
		}
	}
	// The core files render exactly the way init renders them: one
	// implementation, so an applied file is byte-identical to a
	// scaffolded one.
	core, err := initcmd.CoreFiles(initcmd.LanguageName(c.Profiles), c.Project.Name)
	if err != nil {
		return Report{}, fmt.Errorf("apply: %w", err)
	}
	exceptionsYAMLBytes, exceptions, err := exceptionsYAML(c.Exceptions)
	if err != nil {
		return Report{}, fmt.Errorf("apply: %s: %w", exceptionsRel, err)
	}

	plan, skipped, err := buildPlan(root, resolved, core, contractYAML, lockYAML, exceptionsYAMLBytes)
	if err != nil {
		return Report{}, err
	}

	// Transaction: any error after Begin rolls back before returning.
	tx, err := txn.Begin(root)
	if err != nil {
		return Report{}, fmt.Errorf("apply: %w", err)
	}
	run := plan
	if opts.failAfter > 0 && opts.failAfter < len(run) {
		run = run[:opts.failAfter]
	}
	applyErr := tx.Apply(run)
	if applyErr == nil && opts.failAfter > 0 {
		applyErr = errors.New("apply: injected mid-plan failure")
	}
	if applyErr != nil {
		return rollback(tx, applyErr)
	}
	commitErr := tx.Commit()
	if commitErr == nil && opts.failCommit {
		// The commit completed (the transaction is finished, its
		// journal retired); treating it as failed exercises the
		// rollback-failure reporting path.
		commitErr = errors.New("commit: injected failure")
	}
	if commitErr != nil {
		return rollback(tx, fmt.Errorf("commit: %w", commitErr))
	}

	report := Report{Applied: make([]Applied, 0, len(plan)), TransactionBegan: true}
	for _, op := range plan {
		report.Applied = append(report.Applied, Applied{Op: string(op.Kind), Path: op.Path})
	}
	report.Skipped = skipped

	// Post-apply sanity: gate 1 on the applied, durable contract. A
	// failing gate is honest data, not a rollback.
	applied, err := contract.Load(filepath.Join(root, filepath.FromSlash(contractRel)))
	if err != nil {
		return report, fmt.Errorf("apply: applied contract failed to load: %w", err)
	}

	// The four canonical skills and their declared projections go
	// through skills.Install exactly as `pika init` uses it: create the
	// canonical skill only where it is missing (apply's create-if-missing
	// path, spec 5.1 — never reset, since apply has no --reset-docs of
	// its own), then regenerate the projection region a declared harness
	// reads. This runs before gate 1, which verifies the same projection
	// and would otherwise fail an apply that is supposed to produce it.
	skillsRoot, err := repopath.At(root)
	if err != nil {
		return report, fmt.Errorf("apply: %w", err)
	}
	skillsSt, err := skills.Install(skillsRoot, applied, resolved, false)
	if err != nil {
		return report, fmt.Errorf("apply: install canonical skills: %w", err)
	}
	for _, s := range skillsSt.Skills {
		if s.Written {
			report.Applied = append(report.Applied, Applied{Op: string(txn.OpCreate), Path: s.Path})
		}
	}
	for _, p := range skillsSt.Projections {
		if p.Written {
			report.Applied = append(report.Applied, Applied{Op: string(txn.OpWrite), Path: p.Path})
		}
	}

	exit, output, warnings := checks.Gate1(root, applied, resolved)
	report.Gate1 = Gate1{Pass: exit == 0, Output: output, Warnings: warnings}

	// Rewrite the visible review bundle as APPLIED. A refusal above
	// never reaches this, so a refused apply leaves the bundle alone.
	gateLines := make([]string, 0, 1+len(warnings))
	if output != "" {
		gateLines = append(gateLines, output)
	}
	appliedReview := make([]adopt.ReviewChange, 0, len(report.Applied))
	for _, a := range report.Applied {
		appliedReview = append(appliedReview, adopt.ReviewChange{Action: a.Op, Path: a.Path})
	}
	skippedReview := make([]adopt.ReviewSkip, 0, len(skipped))
	for _, s := range skipped {
		skippedReview = append(skippedReview, adopt.ReviewSkip{Path: s.Path, Reason: s.Reason})
	}
	if err := adopt.WriteReview(root, adopt.ReviewData{
		Status:     adopt.ReviewApplied,
		Exceptions: exceptions,
		Applied:    appliedReview,
		Skipped:    skippedReview,
		Gate1Pass:  report.Gate1.Pass,
		Gate1Lines: gateLines,
	}); err != nil {
		return report, fmt.Errorf("apply: the contract was committed but %s could not be rewritten: %w", adopt.ReviewPath, err)
	}
	return report, nil
}

// rollback undoes the transaction after a failure and reports honestly:
// Rollback is true only when the undo completed. A failed Rollback —
// the transaction already finished by a commit error, or an undo that
// could not restore a file — leaves mutations behind (after a commit
// failure, all of them), and the returned error says so instead of
// claiming a pre-state that no longer holds.
func rollback(tx *txn.Tx, cause error) (Report, error) {
	if rerr := tx.Rollback(); rerr != nil {
		return Report{TransactionBegan: true}, errors.Join(
			fmt.Errorf("apply: %w", cause),
			fmt.Errorf("apply: ROLLBACK FAILED — mutations may remain on disk; inspect .project/state/recovery (the journal is preserved for recovery): %w", rerr))
	}
	return Report{Rollback: true}, fmt.Errorf("apply: %w (rolled back to the pre-state)", cause)
}

// fillMissingCommands adds every hint-derived command the draft does not
// already declare, and reports whether it changed the contract. A slot
// the draft already sets is left alone: adoption discovered a real
// command for it, and that beats a suggestion.
func fillMissingCommands(c *contract.Contract, hints map[string]string) bool {
	added := false
	for _, id := range []string{"format", "lint", "typecheck", "test", "smoke"} {
		cmd, ok := hints[id]
		if !ok || strings.TrimSpace(c.Commands[id]) != "" {
			continue
		}
		if c.Commands == nil {
			c.Commands = map[string]string{}
		}
		c.Commands[id] = cmd
		added = true
	}
	return added
}

// buildPlan assembles the ordered operation plan: promote the drafts,
// write the exceptions record, then create every required core file
// the repository is still missing. An existing file at any of those
// paths — the GitHub PR template and the CI workflow included — is
// skipped with a note, never overwritten: apply runs exactly once, on
// a repository it refuses to touch again the moment a contract is
// already committed, so a file already there was never apply's own
// prior output, and there is no template it could safely be judged
// against. `pika init --force` is the command that regenerates a
// kernel-owned file the kernel itself wrote — a real prior scaffold,
// not a first contact with foreign state — through its own separate
// write path.
func buildPlan(root string, resolved *profiles.Resolved, core map[string][]byte, contractYAML, lockYAML, exceptionsYAMLBytes []byte) (txn.Plan, []Skipped, error) {
	var plan txn.Plan
	var skipped []Skipped
	seen := map[string]bool{}
	promote := func(rel string, content []byte) error {
		if seen[rel] {
			return nil
		}
		seen[rel] = true
		full := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Lstat(full); err == nil {
			skipped = append(skipped, Skipped{Path: rel, Reason: "already exists; kept the existing file"})
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("apply: stat %s: %w", rel, err)
		}
		plan = append(plan, txn.Op{Kind: txn.OpCreate, Path: rel, Content: content})
		return nil
	}
	for _, item := range []struct {
		rel     string
		content []byte
	}{
		{contractRel, contractYAML},
		{lockRel, lockYAML},
		{exceptionsRel, exceptionsYAMLBytes},
	} {
		if err := promote(item.rel, item.content); err != nil {
			return nil, nil, err
		}
	}

	for _, req := range resolved.Layers[0].Pack.Files.Required {
		target, err := coreTargetFor(req, core)
		if err != nil {
			return nil, nil, err
		}
		if err := promote(target, core[target]); err != nil {
			return nil, nil, err
		}
	}
	return plan, skipped, nil
}

// coreTargetFor maps a core-pack required-file entry to a rendered core
// file that satisfies it. Plain paths must render directly; directory
// requirements (trailing slash) are satisfied by the lexicographically
// first rendered file inside them.
func coreTargetFor(req string, rendered map[string][]byte) (string, error) {
	if !strings.HasSuffix(req, "/") {
		if _, ok := rendered[req]; ok {
			return req, nil
		}
		return "", fmt.Errorf("apply: the core profile requires %s but no core template renders it", req)
	}
	best := ""
	for target := range rendered {
		if strings.HasPrefix(target, req) && (best == "" || target < best) {
			best = target
		}
	}
	if best == "" {
		return "", fmt.Errorf("apply: the core profile requires %s but no core template renders a file inside it", req)
	}
	return best, nil
}

// exceptionsYAML renders the draft contract's recorded exceptions as
// the .project/exceptions.yaml record: one mapping entry per excepted
// path, keys sorted, every spec §5.3 field present. A draft without
// exceptions produces the valid empty record. It also returns the
// exceptions as typed records for the review bundle.
func exceptionsYAML(raw []any) ([]byte, []checks.Exception, error) {
	if len(raw) == 0 {
		return []byte("{}\n"), nil, nil
	}
	exceptions := make([]checks.Exception, 0, len(raw))
	for i, item := range raw {
		fields, err := stringFields(item)
		if err != nil {
			return nil, nil, fmt.Errorf("exception %d: %w", i, err)
		}
		path, err := contract.NormalizeRepoPath(fields["path"])
		if err != nil {
			return nil, nil, fmt.Errorf("exception %d: path %q: %w", i, fields["path"], err)
		}
		ex := checks.Exception{
			RuleID:          fields["rule-id"],
			Path:            path,
			Reason:          fields["reason"],
			Owner:           fields["owner"],
			ReviewCondition: fields["review-condition"],
		}
		var missing []string
		for field, val := range map[string]string{
			"rule-id":          ex.RuleID,
			"reason":           ex.Reason,
			"owner":            ex.Owner,
			"review-condition": ex.ReviewCondition,
		} {
			if strings.TrimSpace(val) == "" {
				missing = append(missing, field)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return nil, nil, fmt.Errorf("exception %d (%s): missing %s", i, path, strings.Join(missing, ", "))
		}
		exceptions = append(exceptions, ex)
	}
	// Determinism first: path, then rule, so two exceptions at the same
	// path always render in the same order regardless of the draft's
	// own order.
	sort.Slice(exceptions, func(i, j int) bool {
		if exceptions[i].Path != exceptions[j].Path {
			return exceptions[i].Path < exceptions[j].Path
		}
		return exceptions[i].RuleID < exceptions[j].RuleID
	})
	// A path violating two different naming rules at once — a banned
	// catch-all directory segment and a non-kebab-case filename segment
	// in the same path — needs one exception per rule; exceptions.yaml's
	// map is keyed by path alone, so that path's value becomes a list.
	// Only the same rule recorded twice for the same path is the actual
	// duplicate.
	for i := 1; i < len(exceptions); i++ {
		if exceptions[i].Path == exceptions[i-1].Path && exceptions[i].RuleID == exceptions[i-1].RuleID {
			return nil, nil, fmt.Errorf("exception %q rule %q is recorded twice", exceptions[i].Path, exceptions[i].RuleID)
		}
	}
	doc := make(yaml.MapSlice, 0, len(exceptions))
	for i := 0; i < len(exceptions); {
		j := i + 1
		for j < len(exceptions) && exceptions[j].Path == exceptions[i].Path {
			j++
		}
		if j-i == 1 {
			// The common case renders exactly as before: a bare object,
			// not a one-item list, so every exceptions.yaml written
			// before multi-rule paths existed keeps its exact shape.
			doc = append(doc, yaml.MapItem{Key: exceptions[i].Path, Value: exceptions[i]})
		} else {
			doc = append(doc, yaml.MapItem{Key: exceptions[i].Path, Value: exceptions[i:j]})
		}
		i = j
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, nil, fmt.Errorf("encode: %w", err)
	}
	return out, exceptions, nil
}

// stringFields flattens one untyped draft-exception entry into its
// string fields.
func stringFields(item any) (map[string]string, error) {
	switch m := item.(type) {
	case map[string]string:
		return m, nil
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, v := range m {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("field %q: want a string, got %T", k, v)
			}
			out[k] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("want a mapping of fields, got %T", item)
	}
}
