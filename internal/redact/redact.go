// Package redaction replaces credential- and path-shaped substrings with
// stable <redacted:kind> placeholders so that state snapshots, evidence
// bundles, and task payloads never carry live secrets. The rule table is
// plain data so Task 15 (state capture) and evidence generation can reuse
// the same mapping.
//
// Pattern complexity: every pattern compiles under Go's regexp (RE2), which
// guarantees linear-time matching — catastrophic backtracking is impossible
// by construction. All quantifiers are greedy or bounded ranges applied to a
// single character class; there are no nested or alternating quantifiers
// that could produce exponential ambiguity. Minima ({20,}, {36,}, {16,},
// {50,}) double as false-positive guards: truncated or partial credentials
// below the pattern's minimum do not match.
package redact

import (
	"os"
	"regexp"
	"sort"
	"strings"
)

// MaxFindings bounds the findings reported by File so evidence generation
// stays O(1) in output size for pathological inputs.
const MaxFindings = 100

// Finding locates one redactable span: 1-based Line and 1-based byte Col of
// the redacted payload within its line.
type Finding struct {
	Kind string
	Line int
	Col  int
}

// rule is one entry of the data-driven mapping table: kind → pattern.
// payload names the submatch holding the redactable span; rules with a
// consumed prefix (path rules) keep that prefix out of the payload so the
// surrounding character (e.g. whitespace, '=') survives replacement. A
// payload of 0 means the whole match is the payload.
type rule struct {
	kind    string
	pattern *regexp.Regexp
	payload int
}

// rules maps every credential/path family to its placeholder kind. Order is
// the deterministic scan order; overlap is resolved by longest match at a
// given position, so sk-ant-… (oauth) beats sk-… (api-key) and a full PEM
// block (pem) beats its header line (pem-header) regardless of order.
var rules = []rule{
	// Longest sk- variant first: sk-ant-... must classify as oauth.
	{kind: "oauth", pattern: regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)},
	{kind: "api-key", pattern: regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)},
	{kind: "github-token", pattern: regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}`)},
	{kind: "slack-token", pattern: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`)},
	{kind: "aws-key", pattern: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{kind: "nostr-key", pattern: regexp.MustCompile(`\b(?:nsec|npub)1[02-9ac-hj-np-z]{50,}`)},
	{kind: "bearer", pattern: regexp.MustCompile(`[Bb]earer[ \t]+[A-Za-z0-9._~+/=-]{16,}`)},
	{kind: "pem", pattern: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)},
	{kind: "pem-header", pattern: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)},
	// Path rules: a leading boundary character is consumed (payload group 1)
	// so "/home" inside a URL host (…example.com/home/x) never matches.
	{kind: "user-path", pattern: regexp.MustCompile(`(?m)(?:^|[\s"'([=:])(/Users/[A-Za-z0-9._-]{1,64}/|/home/[A-Za-z0-9._-]{1,64}/)`), payload: 1},
	// macOS per-machine temp/root noise only; hostnames are out of M1 scope.
	{kind: "machine-path", pattern: regexp.MustCompile(`(?m)(?:^|[\s"'([=:])(/private/var/(?:folders|root)/[A-Za-z0-9._/-]{0,80}|/var/(?:folders|root)/[A-Za-z0-9._/-]{0,80})`), payload: 1},
}

// span is a selected match: match extent plus payload extent.
type span struct {
	start, end     int
	payloadS, pldE int
	kind           string
}

// scan finds every non-overlapping redactable span in s: longest match wins
// at a given position, and a selected span consumes its extent so nothing
// inside it is re-redacted (no nested placeholders).
func scan(s string) []span {
	type raw struct {
		span
		order int
	}
	var all []raw
	for ri, r := range rules {
		for _, m := range r.pattern.FindAllStringSubmatchIndex(s, -1) {
			ps, pe := m[2*r.payload], m[2*r.payload+1]
			all = append(all, raw{
				span:  span{start: m[0], end: m[1], payloadS: ps, pldE: pe, kind: r.kind},
				order: ri,
			})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.start != b.start {
			return a.start < b.start
		}
		if a.end != b.end {
			return a.end > b.end // longer match wins at the same position
		}
		return a.order < b.order
	})
	var out []span
	consumed := 0
	for _, m := range all {
		if m.start < consumed {
			continue // overlaps an already-redacted span
		}
		out = append(out, m.span)
		consumed = m.end
	}
	return out
}

// Apply replaces every credential/path match in s with <redacted:kind>.
func Apply(s string) string {
	spans := scan(s)
	if len(spans) == 0 {
		return s
	}
	var b strings.Builder
	pos := 0
	for _, sp := range spans {
		b.WriteString(s[pos:sp.payloadS])
		b.WriteString("<redacted:")
		b.WriteString(sp.kind)
		b.WriteString(">")
		pos = sp.pldE
	}
	b.WriteString(s[pos:])
	return b.String()
}

// File reads path and reports redactable spans WITHOUT rewriting the file —
// evidence generation decides replacement. Findings are capped at
// MaxFindings (100); clean is false whenever any finding exists. Multi-line
// PEM blocks are reported per line (a lone BEGIN line yields a pem-header
// finding), since findings are line-addressed.
func File(path string) (clean bool, findings []Finding, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, nil, err
	}
	line := 0
	for off := 0; off <= len(data); {
		idx := strings.IndexByte(string(data[off:]), '\n')
		var text string
		if idx < 0 {
			text = string(data[off:])
			off = len(data) + 1
		} else {
			text = string(data[off : off+idx])
			off += idx + 1
		}
		line++
		text = strings.TrimSuffix(text, "\r")
		for _, sp := range scan(text) {
			findings = append(findings, Finding{Kind: sp.kind, Line: line, Col: sp.payloadS + 1})
			if len(findings) >= MaxFindings {
				return false, findings, nil
			}
		}
		if idx < 0 {
			break
		}
	}
	return len(findings) == 0, findings, nil
}
