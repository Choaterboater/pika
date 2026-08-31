package conformance

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The payloads the corpus reads back off pika's stdout.
//
// They are declared here rather than imported from the packages that
// produce them, for the same reason internal/smoke declares its own: a
// corpus that unmarshalled into the producer's struct would assert that
// pika agrees with itself, which it always does. These are the shapes an
// outside consumer sees.

// Envelope is the cliout envelope every --json payload carries.
type Envelope struct {
	Schema  int             `json:"schema"`
	Command string          `json:"command"`
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Gate is one rung of the ladder as `check --json` reports it.
type Gate struct {
	ID         string   `json:"id"`
	Cmd        []string `json:"cmd"`
	Exit       int      `json:"exit"`
	Status     string   `json:"status"`
	Reason     string   `json:"reason"`
	OutputTail string   `json:"outputTail"`
	DurationMs int64    `json:"durationMs"`
}

// Report is the verification report nested under a check envelope.
type Report struct {
	Gates   []Gate `json:"gates"`
	Summary struct {
		Pass int `json:"pass"`
		Fail int `json:"fail"`
		Skip int `json:"skip"`
	} `json:"summary"`
	Pass bool `json:"pass"`
}

// Ladder renders every rung on one line, which is what a per-repository
// summary needs: a red `lint` is usually explained by what `format` did.
func (r Report) Ladder() string {
	parts := make([]string, 0, len(r.Gates))
	for _, g := range r.Gates {
		parts = append(parts, g.ID+"="+g.Status)
	}
	return strings.Join(parts, " ")
}

// Evidence is everything one gate said about itself. A disagreement over
// a gate is unreadable without it: "wanted pass, got fail" sends a
// maintainer to the repository, and the command plus its output tail is
// what says whether the repository or pika moved.
func (g Gate) Evidence() string {
	var b strings.Builder
	fmt.Fprintf(&b, "status=%s exit=%d", g.Status, g.Exit)
	if len(g.Cmd) > 0 {
		fmt.Fprintf(&b, " cmd=%q", strings.Join(g.Cmd, " "))
	}
	if g.Reason != "" {
		fmt.Fprintf(&b, "\n    reason: %s", g.Reason)
	}
	if tail := strings.TrimSpace(g.OutputTail); tail != "" {
		fmt.Fprintf(&b, "\n    output: %s", excerpt(tail, 600))
	}
	return b.String()
}

// AdoptReport is the part of the adoption report the corpus reads: what
// adopt detected, and the two naming lists whose disagreement was the
// defect that made psf/requests unadoptable.
type AdoptReport struct {
	DetectedProfiles []string `json:"detectedProfiles"`
	Conflicts        []struct {
		RuleID string `json:"ruleId"`
		Path   string `json:"path"`
	} `json:"conflicts"`
	Exceptions []struct {
		RuleID string `json:"ruleId"`
		Path   string `json:"path"`
	} `json:"exceptions"`
	BaselineChecks []struct {
		Verb    string `json:"verb"`
		Command string `json:"command"`
		Exit    int    `json:"exit"`
		Status  string `json:"status"`
	} `json:"baselineChecks"`
}

// excerpt caps text at n bytes and says how much it dropped. Output that
// trails off reads as a command that stopped mid-sentence.
func excerpt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:] + fmt.Sprintf("\n    (... %d earlier bytes elided)", len(s)-n)
}
