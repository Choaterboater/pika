// Package apply implements `pika apply`: the transactional step that
// promotes adoption drafts into a live project contract. It closes the
// adoption loop opened by `pika adopt`: the drafts under .project/ are
// durable state, and apply derives everything it does from them — never
// from session memory — promotes them, writes the exceptions record and
// the core files the repository is still missing, and rewrites the
// visible review bundle as APPLIED.
//
// Core files are split by ownership, the same split `pika init --force`
// honours. Operator-owned files (README.md, AGENTS.md, CONTRIBUTING.md
// and the language scaffold) are create-if-missing: a file the user
// already has is kept, always. The two kernel-owned files — the GitHub
// PR template and the CI workflow — encode how the kernel wants to be
// run, so apply compares them against the rendered template and
// refreshes a stale one. That refresh is the supported remedy for a
// repository scaffolded by an older kernel whose template has since
// been corrected; without it, a rotated pack digest fails gate 1 with
// no way to fix itself. Every refresh is reported as a `write`,
// because a silent kernel rewrite is indistinguishable from an
// operator's own edit.
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
	"bytes"
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
	refs := checks.ProfileRefs(c)
	resolved, err := profiles.Resolve(refs)
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
	core, err := initcmd.CoreFiles(initcmd.LanguageName(refs), c.Project.Name)
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

	report := Report{Applied: make([]Applied, 0, len(plan))}
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
		return Report{}, errors.Join(
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

// kernelOwnedCore is the set of rendered core files whose content the
// kernel alone determines. The GitHub PR template and the CI workflow
// encode how the kernel wants to be run, so a copy left behind by an
// older kernel is the kernel's defect to correct. Everything else the
// core pack renders — README.md, AGENTS.md, CONTRIBUTING.md — plus
// go.mod and the whole language scaffold belongs to the operator the
// moment it exists, and is only ever created when missing. The split
// mirrors the `kernel` column of initcmd's coreTemplateTargets, which
// is where `pika init --force` reads the same boundary; the state
// files are deliberately absent, because apply refuses outright on an
// already-adopted repository.
var kernelOwnedCore = map[string]bool{
	".github/pull_request_template.md": true,
	".github/workflows/ci.yml":         true,
}

// buildPlan assembles the ordered operation plan: promote the drafts,
// write the exceptions record, then reconcile every required core file.
// A missing file is created. An existing operator-owned file — the user
// may have written it themselves, or kept it since an earlier scaffold
// — is skipped with a note, never overwritten. An existing kernel-owned
// file is compared against the rendered template and rewritten when it
// differs, as a journalled write so the refresh rolls back with the
// rest of the transaction; when it already matches, it is skipped.
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
		info, err := os.Lstat(full)
		switch {
		case errors.Is(err, os.ErrNotExist):
			plan = append(plan, txn.Op{Kind: txn.OpCreate, Path: rel, Content: content})
			return nil
		case err != nil:
			return fmt.Errorf("apply: stat %s: %w", rel, err)
		case !kernelOwnedCore[rel]:
			skipped = append(skipped, Skipped{Path: rel, Reason: "already exists; kept the existing file"})
			return nil
		case !info.Mode().IsRegular():
			// A symlink or a directory at a kernel-owned path is not
			// something a content comparison can speak to, and
			// replacing it would destroy whatever it points at. Keep
			// it and say so.
			skipped = append(skipped, Skipped{Path: rel, Reason: "already exists and is not a regular file; kept it"})
			return nil
		}
		current, err := os.ReadFile(full)
		if err != nil {
			return fmt.Errorf("apply: read %s: %w", rel, err)
		}
		if bytes.Equal(current, content) {
			skipped = append(skipped, Skipped{Path: rel, Reason: "kernel-owned and already matches the rendered template"})
			return nil
		}
		plan = append(plan, txn.Op{Kind: txn.OpWrite, Path: rel, Content: content})
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
	sort.Slice(exceptions, func(i, j int) bool { return exceptions[i].Path < exceptions[j].Path })
	for i := 1; i < len(exceptions); i++ {
		if exceptions[i].Path == exceptions[i-1].Path {
			return nil, nil, fmt.Errorf("exception %q is recorded twice", exceptions[i].Path)
		}
	}
	doc := make(yaml.MapSlice, 0, len(exceptions))
	for _, ex := range exceptions {
		doc = append(doc, yaml.MapItem{Key: ex.Path, Value: ex})
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
