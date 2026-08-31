package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The payloads this gate reads back off pika's stdout.
//
// They are declared here rather than imported from the packages that
// produce them, on purpose. A smoke gate that unmarshalled into the
// producer's own struct would assert that pika agrees with itself, which
// it always does. These are the shapes an outside consumer sees, so a
// field renamed in a release note that nobody wrote fails here.

// envelope is the cliout envelope every --json payload carries.
type envelope struct {
	Schema  int             `json:"schema"`
	Command string          `json:"command"`
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// gate is one rung of the ladder as `check --json` reports it.
type gate struct {
	ID         string   `json:"id"`
	Cmd        []string `json:"cmd"`
	Exit       int      `json:"exit"`
	Status     string   `json:"status"`
	Reason     string   `json:"reason"`
	OutputTail string   `json:"outputTail"`
	DurationMs int64    `json:"durationMs"`
}

// evidence is everything a gate said about itself, for a failure
// message: the status alone never explains a red gate.
func (g gate) evidence() string {
	var b strings.Builder
	fmt.Fprintf(&b, "gate %s: status=%s exit=%d", g.ID, g.Status, g.Exit)
	if len(g.Cmd) > 0 {
		fmt.Fprintf(&b, " cmd=%q", strings.Join(g.Cmd, " "))
	}
	if g.Reason != "" {
		fmt.Fprintf(&b, "\nreason: %s", g.Reason)
	}
	if g.OutputTail != "" {
		fmt.Fprintf(&b, "\noutput: %s", excerpt(g.OutputTail, outputExcerpt))
	}
	return b.String()
}

// report is the verification report nested under a check envelope.
type report struct {
	Gates   []gate `json:"gates"`
	Summary struct {
		Pass int `json:"pass"`
		Fail int `json:"fail"`
		Skip int `json:"skip"`
	} `json:"summary"`
	Pass bool `json:"pass"`
}

// gate returns the named rung.
func (r report) gate(id string) (gate, bool) {
	for _, g := range r.Gates {
		if g.ID == id {
			return g, true
		}
	}
	return gate{}, false
}

// String renders the whole ladder one line per rung, which is what a
// failure message needs: a report that fails on `lint` is usually
// explained by what `format` did.
func (r report) String() string {
	var b strings.Builder
	for _, g := range r.Gates {
		fmt.Fprintf(&b, "%-4s %-10s exit=%d %s\n", strings.ToUpper(g.Status), g.ID, g.Exit, oneLine(g.Reason, g.OutputTail))
	}
	fmt.Fprintf(&b, "pass=%v (%d passed, %d failed, %d skipped)", r.Pass, r.Summary.Pass, r.Summary.Fail, r.Summary.Skip)
	return b.String()
}

// ladder renders the whole ladder on one line. It is the context a
// single wrong rung needs — a red `lint` is usually explained by what
// `format` did — without pasting the report beside every assertion that
// touched it.
func (r report) ladder() string {
	parts := make([]string, 0, len(r.Gates))
	for _, g := range r.Gates {
		parts = append(parts, g.ID+"="+g.Status)
	}
	return "ladder: " + strings.Join(parts, " ")
}

// improveResult is the payload `pika improve` nests under a successful
// envelope.
type improveResult struct {
	WorkID       string   `json:"workId"`
	Branch       string   `json:"branch"`
	Commit       string   `json:"commit"`
	ChangedFiles []string `json:"changedFiles"`
	Handoff      struct {
		Dir        string `json:"dir"`
		PromptPath string `json:"promptPath"`
	} `json:"handoff"`
	ChecksAfter *report `json:"checksAfter"`
}

// improveFailure is the payload a run that stopped nests under
// ok:false — the error, and the run's own report of how far it got.
type improveFailure struct {
	Error  string `json:"error"`
	Report struct {
		WorkID    string `json:"workId"`
		StoppedOn string `json:"stoppedOn"`
	} `json:"report"`
}

// versionResult is the payload `pika version --json` prints: this
// binary's identity, and the identity recorded in the repository's lock
// when there is one to read.
type versionResult struct {
	Version        string `json:"version"`
	RegistryDigest string `json:"registry_digest"`
	MaxSchema      int    `json:"max_contract_schema"`
	Lock           *struct {
		Path           string `json:"path"`
		RegistryDigest string `json:"registry_digest"`
		Matches        bool   `json:"matches"`
	} `json:"lock"`
}

// doctorReport is the diagnosis `pika doctor --json` prints.
type doctorReport struct {
	OK       bool `json:"ok"`
	Root     string
	Findings []struct {
		ID          string `json:"id"`
		Severity    string `json:"severity"`
		Detail      string `json:"detail"`
		Remediation string `json:"remediation"`
	} `json:"findings"`
}

// errors lists the error-severity findings, which are the ones that
// decide the exit code.
func (d doctorReport) errors() []string {
	var out []string
	for _, f := range d.Findings {
		if f.Severity == "error" {
			out = append(out, f.ID+": "+f.Detail)
		}
	}
	return out
}

// String renders the diagnosis one line per finding.
func (d doctorReport) String() string {
	var b strings.Builder
	for _, f := range d.Findings {
		fmt.Fprintf(&b, "%-5s %-14s %s\n", f.Severity, f.ID, oneLine(f.Detail))
	}
	fmt.Fprintf(&b, "ok=%v", d.OK)
	return b.String()
}

// oneLine folds the first non-empty of parts onto a single line, so a
// per-gate summary stays one row.
func oneLine(parts ...string) string {
	for _, p := range parts {
		if p == "" {
			continue
		}
		return excerpt(strings.ReplaceAll(strings.TrimSpace(p), "\n", " "), 160)
	}
	return ""
}
