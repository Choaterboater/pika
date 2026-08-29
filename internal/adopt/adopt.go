// Package adopt implements the read-only adoption inventory behind
// `pika adopt` (spec §13): discovery-first, non-destructive. Preview
// walks the repository with the Task 3 discovery engine, classifies every
// discovered convention against core@1, runs the discovered check commands
// once each to record a baseline, and writes the adoption proposals: the
// two .draft files under .project/ plus a visible, plain-language review
// bundle at review/adoption-review.md. No tracked file is touched:
// applying the proposal is a later, transactional step.
//
// Design decisions recorded for downstream consumers (Task 12 `adopt`,
// Task 18 e2e):
//
//   - ConventionMap maps each discovered convention (naming rules in
//     path-segments scope, required-file presence, existing check commands)
//     to match | conflict | exception against core@1. file-lines and
//     generated-patterns rules are review signals reported by `check`, not
//     adoption conventions, so they stay out of the map.
//   - Valid existing conventions (status match) are recorded in the draft
//     contract under extensions.conventions as {name, detail} entries; the
//     extensions map is schema-legal free-form and round-trips through
//     contract.Load.
//   - Warning-severity naming deviations (kebab-case) become proposed
//     exceptions: returned in Report.Exceptions and recorded in the draft
//     contract's exceptions list. Error-severity deviations (banned
//     catch-alls) become Conflicts — they need a human decision, not a
//     recorded exception.
//   - Baseline checks are the discovered command strings executed
//     sequentially with a 30s deadline each; failures are recorded as
//     baseline data, never as adopt errors.
//   - Diff.Before is always empty: the two draft files are new from
//     adopt's point of view (re-running adopt replaces drafts), which is
//     what makes repeated runs byte-identical.
package adopt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Choaterboater/pika/internal/checks"
	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/discover"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/goccy/go-yaml"
)

// Repository-relative locations adopt reads and writes.
const (
	// committedContractPath is the durable contract location; its presence
	// means the repository is already adopted.
	committedContractPath = ".project/contract.yaml"

	// contractDraftPath and lockDraftPath are the machine-readable
	// proposals adopt writes; review.go adds the human-readable bundle.
	contractDraftPath = ".project/contract.yaml.draft"
	lockDraftPath     = ".project/profiles.lock.draft"
)

// Convention statuses for the convention map.
const (
	StatusMatch     = "match"     // agrees with core@1; recorded as a convention
	StatusConflict  = "conflict"  // disagrees with core@1; needs a decision
	StatusException = "exception" // deviates; adopt proposes a recorded exception
)

// exception boilerplate for proposed naming exceptions (spec §5.3: every
// exception needs rule ID, rationale, owner, and review condition).
const (
	exceptionOwner   = "pika adopt"
	exceptionReason  = "pre-existing repository layout; adopt records the convention instead of renaming files for style conformity"
	exceptionReview  = "re-review when the path is next modified or at the next convention audit"
	baselineDeadline = 30 * time.Second
)

// baselineTimeout is a package variable so tests can shrink the deadline.
var baselineTimeout = baselineDeadline

// slotByVerb maps discovery check verbs to contract command slots. Discovered
// verbs without a contract slot (build) are recorded as conventions only.
var slotByVerb = map[string]string{
	"fmt":       "format",
	"lint":      "lint",
	"test":      "test",
	"typecheck": "typecheck",
}

// Convention is one discovered convention classified against core@1.
type Convention struct {
	Name   string `json:"name"`   // "naming/<rule-id>", "file/<path>", "check/<verb>"
	Status string `json:"status"` // match | conflict | exception
	Detail string `json:"detail"`
}

// ConventionMap is the classified convention list, ordered by name.
type ConventionMap []Convention

// BaselineCheck is the outcome of one discovered check command.
type BaselineCheck struct {
	Verb    string `json:"verb"`
	Command string `json:"command"`
	Exit    int    `json:"exit"`   // -1 when the command could not run or timed out
	Status  string `json:"status"` // pass | fail | timeout
}

// Change is one addition or modification adopt proposes for a later,
// transactional apply step.
type Change struct {
	Path   string `json:"path"`   // repository-relative, "/"-separated
	Action string `json:"action"` // "create" in M1
	Detail string `json:"detail"`
}

// Conflict is one disagreement with core@1 that recording alone cannot
// resolve.
type Conflict struct {
	RuleID string `json:"ruleId"`
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

// Diff is one proposed file change as full before/after contents. Before is
// empty for new files.
type Diff struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Report is the adoption inventory Preview produces. It is JSON-stable:
// every slice is deterministically ordered and no field carries a timestamp
// or duration, so repeated runs on an unchanged repository marshal to
// byte-identical output.
type Report struct {
	Inventory        discover.Inventory `json:"inventory"`
	DetectedProfiles []string           `json:"detectedProfiles"`
	ConventionMap    ConventionMap      `json:"conventionMap"`
	BaselineChecks   []BaselineCheck    `json:"baselineChecks"`
	DraftContract    *contract.Contract `json:"draftContract"`
	ProposedChanges  []Change           `json:"proposedChanges"`
	Conflicts        []Conflict         `json:"conflicts"`
	Exceptions       []checks.Exception `json:"exceptions"`
	Preview          []Diff             `json:"preview"`
}

// Preview inventories repoRoot and produces the adoption report plus the two
// draft files. It returns an error when the repository is already adopted
// (a committed contract exists — direct the caller to check/upgrade) or when
// discovery, draft generation, or validation fails. Baseline command
// failures are data in the report, not errors.
func Preview(repoRoot string) (*Report, error) {
	if repoRoot == "" {
		repoRoot = "."
	}
	if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(committedContractPath))); err == nil {
		return nil, fmt.Errorf("adopt: %s already exists: repository already adopted; use `pika check` or `pika upgrade` instead", committedContractPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("adopt: stat %s: %w", committedContractPath, err)
	}

	inv, err := discover.Discover(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("adopt: %w", err)
	}
	resolved, err := profiles.Resolve([]string{profiles.CoreRef})
	if err != nil {
		return nil, fmt.Errorf("adopt: %w", err)
	}

	detected := detectedProfiles(inv.DetectedLanguages)
	draft := buildDraft(repoRoot, inv, detected)

	conventions, exceptions, conflicts, changes := classifyConventions(repoRoot, inv, resolved, draft)

	baseline, err := runBaseline(repoRoot, inv.ExistingChecks)
	if err != nil {
		return nil, fmt.Errorf("adopt: baseline: %w", err)
	}

	contractYAML, err := writeDrafts(repoRoot, draft)
	if err != nil {
		return nil, err
	}
	lockJSON, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(lockDraftPath)))
	if err != nil {
		return nil, fmt.Errorf("adopt: read %s: %w", lockDraftPath, err)
	}

	changes = append(changes,
		Change{Path: committedContractPath, Action: "create", Detail: "draft written to " + contractDraftPath},
		Change{Path: ".project/profiles.lock", Action: "create", Detail: "draft written to " + lockDraftPath})
	if len(exceptions) > 0 {
		changes = append(changes, Change{
			Path:   checks.ExceptionsFile,
			Action: "create",
			Detail: "proposed naming exceptions; they are also recorded in the draft contract",
		})
	}
	sortChanges(changes)

	rep := &Report{
		Inventory:        *inv,
		DetectedProfiles: detected,
		ConventionMap:    conventions,
		BaselineChecks:   baseline,
		DraftContract:    draft,
		ProposedChanges:  changes,
		Conflicts:        conflicts,
		Exceptions:       exceptions,
		Preview: []Diff{
			{Path: contractDraftPath, After: string(contractYAML)},
			{Path: lockDraftPath, After: string(lockJSON)},
		},
	}
	// The review bundle is the visible, plain-language copy of this
	// report: .project/ is a dot-folder, and the drafts are YAML.
	if err := WriteReview(repoRoot, ReviewData{Status: ReviewProposed, Report: rep}); err != nil {
		return nil, err
	}
	return rep, nil
}

// detectedProfiles maps discovered languages to stack pack references
// with core@1 always first. The language-to-pack mapping is profiles':
// the same pairing LanguagePack exposes to composition.
func detectedProfiles(languages []string) []string {
	out := []string{profiles.CoreRef}
	for _, lang := range languages { // discover returns languages sorted
		if ref, ok := profiles.LanguagePack(lang); ok && !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	return out
}

// buildDraft composes the full draft contract: schema 1, detected profiles,
// one packages entry per discovered package, commands mapped from the
// discovered checks where verbs match contract slots, and the M1 defaults
// for GitHub merge and evidence policy.
func buildDraft(repoRoot string, inv *discover.Inventory, detected []string) *contract.Contract {
	topology := "single"
	if len(inv.Packages) > 1 {
		topology = "workspace"
	}
	c := &contract.Contract{
		Schema:     1,
		Project:    contract.Project{Name: projectName(repoRoot, inv), Topology: topology},
		Profiles:   detected,
		Packages:   map[string]contract.Package{},
		Commands:   map[string]string{},
		GitHub:     contract.GitHub{Merge: "squash"},
		Evidence:   contract.Evidence{Publish: "sanitized"},
		Extensions: map[string]any{},
	}
	for _, p := range inv.Packages {
		key := p.Root
		if key == "." {
			key = c.Project.Name
		}
		profilesList := []string{profiles.CoreRef}
		if ref, ok := profiles.LanguagePack(p.Language); ok {
			profilesList = append(profilesList, ref)
		}
		c.Packages[key] = contract.Package{Root: p.Root, Profiles: profilesList}
	}
	for verb, slot := range slotByVerb {
		if cmd, ok := inv.ExistingChecks[verb]; ok {
			c.Commands[slot] = cmd
		}
	}
	return c
}

// projectName derives the contract project name: the discovered primary
// package's declared name when available (module path, Cargo or package.json
// name), else the sanitized directory base name. The result always matches
// the schema pattern ^[a-z0-9][a-z0-9-]*$.
func projectName(repoRoot string, inv *discover.Inventory) string {
	for _, p := range inv.Packages {
		if p.Root == "." && p.Name != "" {
			name := p.Name
			if i := strings.LastIndex(name, "/"); i >= 0 {
				name = name[i+1:]
			}
			if n := sanitizeName(name); n != "" {
				return n
			}
		}
	}
	if n := sanitizeName(filepath.Base(repoRoot)); n != "" {
		return n
	}
	return "project"
}

// sanitizeName lowercases s and replaces every run of non-[a-z0-9] with a
// single dash, trimming leading and trailing dashes.
func sanitizeName(s string) string {
	var b strings.Builder
	dash := true // suppress a leading dash
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// classifyConventions evaluates the discovered conventions against core@1,
// records the valid ones in the draft contract's extensions, fills the draft
// exceptions, and returns the map plus the proposed exceptions, conflicts,
// and file additions. All returned slices are deterministically ordered.
func classifyConventions(repoRoot string, inv *discover.Inventory, resolved *profiles.Resolved, draft *contract.Contract) (ConventionMap, []checks.Exception, []Conflict, []Change) {
	var cm ConventionMap
	var exceptions []checks.Exception
	var conflicts []Conflict
	var changes []Change

	// Naming rules: only path-segments scope maps to adoption conventions.
	// file-lines and generated-patterns rules are review signals that
	// `check` reports; they are not conventions to record or except.
	violations := checks.Naming(repoRoot, resolved.NamingRules, nil)
	for _, rule := range resolved.NamingRules {
		if rule.Scope != "path-segments" {
			continue
		}
		var vs []checks.Violation
		for _, v := range violations {
			if v.RuleID == rule.RuleID {
				vs = append(vs, v)
			}
		}
		slices.SortFunc(vs, func(a, b checks.Violation) int { return strings.Compare(a.Path, b.Path) })

		name := "naming/" + rule.RuleID
		if len(vs) == 0 {
			cm = append(cm, Convention{Name: name, Status: StatusMatch, Detail: "all repository paths conform"})
			continue
		}
		paths := make([]string, 0, len(vs))
		for _, v := range vs {
			paths = append(paths, v.Path)
		}
		list := strings.Join(paths, ", ")
		if rule.Severity == checks.SeverityError {
			// Banned catch-alls need a narrower name or a human decision;
			// adopt never quietly excepts them.
			cm = append(cm, Convention{Name: name, Status: StatusConflict,
				Detail: fmt.Sprintf("%d path(s) need narrower names: %s", len(vs), list)})
			for _, v := range vs {
				conflicts = append(conflicts, Conflict{RuleID: v.RuleID, Path: v.Path, Detail: v.Message})
			}
			continue
		}
		cm = append(cm, Convention{Name: name, Status: StatusException,
			Detail: fmt.Sprintf("%d path(s) deviate; exceptions proposed: %s", len(vs), list)})
		for _, v := range vs {
			exceptions = append(exceptions, checks.Exception{
				RuleID:          v.RuleID,
				Path:            v.Path,
				Reason:          exceptionReason,
				Owner:           exceptionOwner,
				ReviewCondition: exceptionReview,
			})
		}
	}

	// Required files from the core pack: presence is a convention; absence
	// is a gap adopt proposes to fill.
	for _, req := range resolved.Layers[0].Pack.Files.Required {
		name := "file/" + req
		if requiredExists(repoRoot, req) {
			cm = append(cm, Convention{Name: name, Status: StatusMatch, Detail: "present"})
			continue
		}
		cm = append(cm, Convention{Name: name, Status: StatusConflict,
			Detail: "required by core@1 but missing; adopt proposes creating it"})
		changes = append(changes, Change{Path: req, Action: "create", Detail: "required by core@1"})
	}

	// Existing check commands are valid conventions; the ones with contract
	// slots are already mapped into the draft commands.
	for _, verb := range sortedVerbs(inv.ExistingChecks) {
		cm = append(cm, Convention{Name: "check/" + verb, Status: StatusMatch, Detail: inv.ExistingChecks[verb]})
	}

	// Record the valid existing conventions in the draft extensions.
	var recorded []map[string]string
	for _, c := range cm {
		if c.Status == StatusMatch {
			recorded = append(recorded, map[string]string{"name": c.Name, "detail": c.Detail})
		}
	}
	draft.Extensions["conventions"] = recorded

	// Proposed exceptions go into the draft contract as well, so the draft
	// is complete and loadable.
	for _, ex := range exceptions {
		draft.Exceptions = append(draft.Exceptions, map[string]string{
			"rule-id":          ex.RuleID,
			"path":             ex.Path,
			"reason":           ex.Reason,
			"owner":            ex.Owner,
			"review-condition": ex.ReviewCondition,
		})
	}

	slices.SortFunc(cm, func(a, b Convention) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(exceptions, func(a, b checks.Exception) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(conflicts, func(a, b Conflict) int { return strings.Compare(a.Path, b.Path) })
	sortChanges(changes)
	return cm, exceptions, conflicts, changes
}

// requiredExists stats a required-file entry; a trailing slash marks a
// directory requirement.
func requiredExists(repoRoot, req string) bool {
	full := filepath.Join(repoRoot, filepath.FromSlash(req))
	info, err := os.Stat(full)
	if err != nil {
		return false
	}
	return strings.HasSuffix(req, "/") == info.IsDir() || !strings.HasSuffix(req, "/")
}

// runBaseline executes each discovered check command once, sequentially,
// each with a 30s deadline, and records the outcome. A command that cannot
// start is a baseline failure (exit -1), not an adopt error.
func runBaseline(repoRoot string, existing map[string]string) ([]BaselineCheck, error) {
	out := make([]BaselineCheck, 0, len(existing))
	for _, verb := range sortedVerbs(existing) {
		command := existing[verb]
		bc := BaselineCheck{Verb: verb, Command: command, Exit: -1, Status: "fail"}
		argv := strings.Fields(command)
		if len(argv) == 0 {
			out = append(out, bc)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), baselineTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = repoRoot
		setGroup(cmd)
		cmd.Cancel = func() error { return killGroup(cmd) }
		cmd.WaitDelay = 2 * time.Second
		err := cmd.Run()
		switch {
		case err == nil:
			bc.Status = "pass"
			bc.Exit = 0
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			bc.Status = "timeout"
		default:
			bc.Status = "fail"
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				bc.Exit = ee.ExitCode()
			}
		}
		out = append(out, bc)
	}
	return out, nil
}

// writeDrafts marshals and writes the two draft files and validates that the
// draft contract loads and passes the schema. It returns the draft YAML
// bytes. Drafts are overwritten on every run (draft-overwrites-draft).
func writeDrafts(repoRoot string, draft *contract.Contract) ([]byte, error) {
	contractYAML, err := yaml.Marshal(draft)
	if err != nil {
		return nil, fmt.Errorf("adopt: encode draft contract: %w", err)
	}
	projDir := filepath.Join(repoRoot, ".project")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		return nil, fmt.Errorf("adopt: create .project: %w", err)
	}
	contractDraft := filepath.Join(projDir, "contract.yaml.draft")
	if err := os.WriteFile(contractDraft, contractYAML, 0o644); err != nil {
		return nil, fmt.Errorf("adopt: write %s: %w", contractDraftPath, err)
	}
	// The draft must be a complete, valid contract before anyone acts on it.
	if _, err := contract.Load(contractDraft); err != nil {
		return nil, fmt.Errorf("adopt: generated draft contract is invalid: %w", err)
	}
	if err := profiles.WriteLock(filepath.Join(projDir, "profiles.lock.draft"), draft.Profiles); err != nil {
		return nil, fmt.Errorf("adopt: write %s: %w", lockDraftPath, err)
	}
	return contractYAML, nil
}

func sortChanges(changes []Change) {
	slices.SortFunc(changes, func(a, b Change) int { return strings.Compare(a.Path, b.Path) })
}

func sortedVerbs(existing map[string]string) []string {
	verbs := make([]string, 0, len(existing))
	for verb := range existing {
		verbs = append(verbs, verb)
	}
	slices.Sort(verbs)
	return verbs
}
