package checks

import (
	"fmt"
	"strings"

	"github.com/Choaterboater/projectctl/internal/contract"
	"github.com/Choaterboater/projectctl/internal/profiles"
	"github.com/Choaterboater/projectctl/internal/version"
)

// Gate1 runs verification-ladder rung 1 (spec §12.6): the contract
// schema-version ceiling, the exceptions record load, and the
// naming/ownership projection checks. It is the single implementation
// shared by the `projectctl check` command and the MCP run_checks tool so
// agents and humans always agree on gate 1.
//
// An error-severity violation — or an exceptions file that fails to load
// (unverifiable records must not silently widen the rules) — fails the
// gate: exit 1 with the findings joined as gate output and no warnings.
// Warning-severity violations are review signals returned as warnings
// without failing. Exit is 0 when nothing error-severity was found.
func Gate1(repoRoot string, c *contract.Contract, resolved *profiles.Resolved) (exit int, output string, warnings []string) {
	if err := version.Check(c.Schema); err != nil {
		return 1, err.Error(), nil
	}
	exceptions, err := LoadExceptions(repoRoot)
	if err != nil {
		return 1, err.Error(), nil
	}
	var findings []string
	for _, v := range Naming(repoRoot, resolved.NamingRules, exceptions) {
		line := fmt.Sprintf("%s: %s: %s", v.RuleID, v.Path, v.Message)
		if v.Severity == SeverityError {
			findings = append(findings, line)
			continue
		}
		warnings = append(warnings, line)
	}
	if len(findings) > 0 {
		return 1, strings.Join(findings, "\n"), warnings
	}
	return 0, "", warnings
}
