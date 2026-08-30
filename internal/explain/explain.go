// Package explain answers "what is this id and what do I do about it" for
// naming rules, verification gates, and MCP error codes — design spec
// goal 10, every rule explainable.
package explain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Choaterboater/pika/internal/profiles"
)

// Entry kinds.
const (
	KindNamingRule = "naming-rule"
	KindGate       = "gate"
	KindErrorCode  = "error-code"
)

// Entry is one explained id.
type Entry struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Owner       string `json:"owner,omitempty"`
	Severity    string `json:"severity,omitempty"`
	Matches     string `json:"matches,omitempty"`
	Rationale   string `json:"rationale"`
	Remediation string `json:"remediation"`
	Exception   string `json:"exception,omitempty"`
}

// gateEntries explains the verification ladder's rungs (design spec §12.6).
var gateEntries = map[string]Entry{
	"contract":  {Kind: KindGate, Rationale: "Rung 1: contract schema ceiling, exceptions record, profile lock, and the naming projection.", Remediation: "Run \"pika check\" and read the contract gate's output: it names the sub-check that failed."},
	"format":    {Kind: KindGate, Rationale: "Rung 2: the stack formatter.", Remediation: "Set commands.format in the contract, or install the pack's suggested formatter."},
	"lint":      {Kind: KindGate, Rationale: "Rung 2: the stack linter.", Remediation: "Set commands.lint in the contract."},
	"typecheck": {Kind: KindGate, Rationale: "Rung 2: compilation and type checking.", Remediation: "Set commands.typecheck in the contract."},
	"test":      {Kind: KindGate, Rationale: "Rung 3: affected behavioral tests.", Remediation: "Set commands.test in the contract."},
	"smoke":     {Kind: KindGate, Rationale: "Rung 4: a real-surface smoke scenario.", Remediation: "Set commands.smoke in the contract; a skipped smoke gate means no real surface was exercised."},
}

// errorEntries explains the MCP server's closed error-code set.
var errorEntries = map[string]Entry{
	"invalid_params":   {Kind: KindErrorCode, Rationale: "The tool arguments failed validation.", Remediation: "Correct the arguments against the tool's input schema from tools/list."},
	"envelope_denied":  {Kind: KindErrorCode, Rationale: "The capability envelope does not grant this operation. Every MCP tool is deny-by-default, reads included: without an envelope even inspect_repo and read_contract are refused.", Remediation: "Run \"pika authorize --scope project\" and retry (\"--scope read\" is enough for the read tools). It works before a contract exists, but derives no exec grants there: a denied exec names the command it needs, so grant it as a whole argv line, e.g. pika authorize --exec \"make test\"."},
	"contract_invalid": {Kind: KindErrorCode, Rationale: "The contract failed strict parsing or schema validation.", Remediation: "Run \"pika check\" and read the contract gate's output for the specific violation."},
	"already_adopted":  {Kind: KindErrorCode, Rationale: "A committed contract already exists, so adoption would overwrite a live project.", Remediation: "Use \"pika check\" instead; adoption is for unadopted repositories."},
	"unavailable":      {Kind: KindErrorCode, Rationale: "The tool is registered for discoverability but not implemented in this build.", Remediation: "No action available; the capability lands in a later milestone."},
	"internal":         {Kind: KindErrorCode, Rationale: "The kernel failed unexpectedly.", Remediation: "Re-run with the failing input; if it reproduces, this is a pika defect."},
}

// Lookup resolves one id across all three namespaces.
func Lookup(id string, resolved *profiles.Resolved) (*Entry, error) {
	for _, r := range resolved.NamingRules {
		if r.RuleID != id {
			continue
		}
		e := Entry{
			ID:          r.RuleID,
			Kind:        KindNamingRule,
			Owner:       ownerOf(resolved, r.RuleID),
			Severity:    r.Severity,
			Matches:     matchSummary(r),
			Rationale:   r.Rationale,
			Remediation: r.Remediation,
			Exception:   exceptionRecord(r.RuleID),
		}
		return &e, nil
	}
	if e, ok := gateEntries[id]; ok {
		e.ID = id
		return &e, nil
	}
	if e, ok := errorEntries[id]; ok {
		e.ID = id
		return &e, nil
	}
	return nil, fmt.Errorf("explain: unknown id %q", id)
}

// KnownIDs lists every explainable id, sorted, for the unknown-id message.
func KnownIDs(resolved *profiles.Resolved) []string {
	ids := make([]string, 0, len(resolved.NamingRules)+len(gateEntries)+len(errorEntries))
	for _, r := range resolved.NamingRules {
		ids = append(ids, r.RuleID)
	}
	for id := range gateEntries {
		ids = append(ids, id)
	}
	for id := range errorEntries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ownerOf names the pack layer that contributed a rule.
func ownerOf(resolved *profiles.Resolved, ruleID string) string {
	for _, layer := range resolved.Layers {
		for _, r := range layer.Pack.Naming.Rules {
			if r.RuleID == ruleID {
				return layer.Name + "@" + layer.Version
			}
		}
	}
	return ""
}

func matchSummary(r profiles.NamingRule) string {
	var parts []string
	if r.Scope != "" {
		parts = append(parts, "scope "+r.Scope)
	}
	if r.Pattern != "" {
		parts = append(parts, "pattern "+r.Pattern)
	}
	if len(r.Banned) > 0 {
		parts = append(parts, "banned "+strings.Join(r.Banned, ", "))
	}
	if len(r.Exempt) > 0 {
		parts = append(parts, "exempt "+strings.Join(r.Exempt, ", "))
	}
	return strings.Join(parts, "; ")
}

// exceptionRecord shows the exact waiver the operator would record. The
// shape is checks.LoadExceptions': the record is keyed by the excepted
// repository path, and rule-id, reason, owner, and review-condition are
// all mandatory (design spec §5.3) — a record missing any of the four is
// a gate-1 load error, not a silent waiver.
func exceptionRecord(ruleID string) string {
	return strings.Join([]string{
		"# .project/exceptions.yaml",
		"<repo-relative path>:",
		"  rule-id: " + ruleID,
		"  reason: <why this path must keep its name>",
		"  owner: <who accepts this>",
		"  review-condition: <the condition that reopens this decision>",
	}, "\n")
}
