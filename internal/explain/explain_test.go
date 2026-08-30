package explain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/checks"
	"github.com/Choaterboater/pika/internal/profiles"
)

func resolveCore(t *testing.T) *profiles.Resolved {
	t.Helper()
	r, err := profiles.Resolve([]string{profiles.CoreRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return r
}

// Design spec goal 10: every rule is explainable. A rule that cannot
// explain itself must not ship.
func TestEveryResolvedNamingRuleIsExplainable(t *testing.T) {
	resolved := resolveCore(t)
	if len(resolved.NamingRules) == 0 {
		t.Fatal("core resolved no naming rules")
	}
	for _, r := range resolved.NamingRules {
		e, err := Lookup(r.RuleID, resolved)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", r.RuleID, err)
		}
		if strings.TrimSpace(e.Rationale) == "" {
			t.Errorf("rule %q has no rationale", r.RuleID)
		}
		if strings.TrimSpace(e.Remediation) == "" {
			t.Errorf("rule %q has no remediation", r.RuleID)
		}
		if strings.TrimSpace(e.Owner) == "" {
			t.Errorf("rule %q names no owning pack", r.RuleID)
		}
		if strings.TrimSpace(e.Exception) == "" {
			t.Errorf("rule %q shows no exception record", r.RuleID)
		}
	}
}

// The guard above is only worth its runtime if Lookup reports the pack's
// prose verbatim rather than synthesizing filler for a rule that declared
// none. A pack rule with an empty rationale must surface as empty.
func TestLookupReportsPackProseVerbatim(t *testing.T) {
	resolved := &profiles.Resolved{
		NamingRules: []profiles.NamingRule{{
			RuleID:      "silent-rule",
			Severity:    "error",
			Scope:       "path-segments",
			Rationale:   "",
			Remediation: "",
		}},
	}
	e, err := Lookup("silent-rule", resolved)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if e.Rationale != "" {
		t.Errorf("Rationale = %q, want the pack's empty value", e.Rationale)
	}
	if e.Remediation != "" {
		t.Errorf("Remediation = %q, want the pack's empty value", e.Remediation)
	}
	if e.Owner != "" {
		t.Errorf("Owner = %q, want empty for a rule no layer owns", e.Owner)
	}
}

func TestExplainNamingRuleDetail(t *testing.T) {
	e, err := Lookup("naming-catch-all", resolveCore(t))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if e.Kind != KindNamingRule {
		t.Errorf("Kind = %q, want %q", e.Kind, KindNamingRule)
	}
	if e.Severity != "error" {
		t.Errorf("Severity = %q, want %q", e.Severity, "error")
	}
	if !strings.Contains(e.Matches, "utils") {
		t.Errorf("Matches does not mention the banned segments: %q", e.Matches)
	}
	if !strings.Contains(e.Exception, "naming-catch-all") {
		t.Errorf("Exception record does not carry the rule id: %q", e.Exception)
	}
}

// The exception record is advice the operator pastes into
// .project/exceptions.yaml, so it has to survive the loader that gate 1
// actually runs. Prose that does not parse is worse than no prose: it
// sends someone into a gate failure they were told how to avoid.
func TestExceptionRecordParsesAsARealException(t *testing.T) {
	e, err := Lookup("naming-catch-all", resolveCore(t))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	filled := strings.NewReplacer(
		"<repo-relative path>", "internal/utils/retry.go",
		"<why this path must keep its name>", "vendored verbatim from upstream",
		"<who accepts this>", "platform-team",
		"<the condition that reopens this decision>", "when the vendored copy is dropped",
	).Replace(e.Exception)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(checks.ExceptionsFile)), []byte(filled+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := checks.LoadExceptions(root)
	if err != nil {
		t.Fatalf("the record explain prints does not load:\n%s\nerror: %v", filled, err)
	}
	got, ok := loaded["internal/utils/retry.go"]
	if !ok {
		t.Fatalf("loaded %v, want an entry for the excepted path", loaded)
	}
	if got.RuleID != "naming-catch-all" {
		t.Errorf("rule-id = %q, want naming-catch-all", got.RuleID)
	}
}

func TestExplainGateID(t *testing.T) {
	e, err := Lookup("typecheck", resolveCore(t))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if e.Kind != KindGate {
		t.Errorf("Kind = %q, want %q", e.Kind, KindGate)
	}
}

func TestExplainErrorCode(t *testing.T) {
	e, err := Lookup("envelope_denied", resolveCore(t))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if e.Kind != KindErrorCode {
		t.Errorf("Kind = %q, want %q", e.Kind, KindErrorCode)
	}
	if !strings.Contains(e.Remediation, "authorize") {
		t.Errorf("envelope_denied does not point at pika authorize: %q", e.Remediation)
	}
}

func TestUnknownIDListsKnownIDs(t *testing.T) {
	resolved := resolveCore(t)
	if _, err := Lookup("no-such-rule", resolved); err == nil {
		t.Fatal("Lookup(unknown) = nil error, want error")
	}
	ids := KnownIDs(resolved)
	if len(ids) == 0 {
		t.Fatal("KnownIDs returned nothing")
	}
	for _, want := range []string{"naming-catch-all", "typecheck", "envelope_denied"} {
		found := false
		for _, id := range ids {
			if id == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("KnownIDs omits %q", want)
		}
	}
}
