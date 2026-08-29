// Package evidence builds sanitized, schema-validated work receipts
// (spec section 14.1). A receipt is the committed, credential-free record
// of one work run: contract and profile versions, commit identity, agent
// roles, changed files, bounded command output, verification outcomes,
// review dispositions, and the completion verdict.
//
// Build is a pure function: it redacts every string field through
// redact.Apply (Task 14), truncates command output to the last 8 KiB the
// way verify's gate capture does, and validates the result against the
// embedded JSON Schema before returning — fail-closed, so an invalid
// receipt is an error, never a silently-wrong record. Writing files is
// the caller's job; evidence.Write provides the atomic path (temp file,
// fsync, rename, directory fsync).
package evidence

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/Choaterboater/projectctl/internal/redact"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed evidence-receipt.schema.json
var schemaJSON []byte

// receiptSchemaVersion is the receipt format version stamped into every
// receipt; bump when the shape changes.
const receiptSchemaVersion = 1

// outputSummaryBytes bounds each command's output summary to the last
// 8 KiB, mirroring verify's gate capture (spec section 14.1 bounded
// output summaries).
const outputSummaryBytes = 8 * 1024

// workIDPattern is the spec section 14.1 work-id shape:
// YYYYMMDD-short-slug-4hex, e.g. 20260828-auth-timeout-7f3a.
var workIDPattern = regexp.MustCompile(`^[0-9]{8}-[a-z0-9]+(-[a-z0-9]+)*-[0-9a-f]{4}$`)

// slugPattern is the kebab-case shape NewWorkID accepts for the slug
// component: lowercase alnum words joined by single hyphens.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// maxSlugLen bounds the slug so work IDs stay readable paths.
const maxSlugLen = 64

// ProfileLockInput mirrors the receipt's profile_lock: the lock digest
// plus one entry per resolved pack.
type ProfileLockInput struct {
	Digest string
	Packs  map[string]PackInput
}

// PackInput is one resolved pack's identity.
type PackInput struct {
	Version string
	Source  string
	Digest  string
}

// RoleInput records one agent role; Substituted marks a provider
// substitution (a different provider served the role than the contract
// named). No credentials, ever — only provider and model names.
type RoleInput struct {
	Role        string
	Runtime     string
	Provider    string
	Model       string
	Substituted bool
}

// ChangedFileInput is one file the run touched and its ownership class.
type ChangedFileInput struct {
	Path      string
	Ownership string
}

// CommandInput is one executed command. Output is the raw combined
// output; Build redacts and tail-truncates it into OutputSummary.
type CommandInput struct {
	Cmd        string
	Exit       int
	DurationMs int64
	Output     string
}

// SurfaceScenarioInput records whether a real-surface scenario ran and
// what it exercised.
type SurfaceScenarioInput struct {
	Ran         bool
	Description string
}

// ReviewInput is one independent review finding and its disposition.
type ReviewInput struct {
	Agent       string
	Finding     string
	Disposition string
}

// CompletionInput is the run's completion verdict. An incomplete run
// requires Reason; Blocker names the blocking issue and is only valid
// when Complete is false (both rules are enforced by the embedded schema).
type CompletionInput struct {
	Complete bool
	Reason   string
	Blocker  string
}

// ReceiptInput is the raw, pre-sanitization evidence collected during a
// work run. Every string field may contain credentials or user paths;
// Build redacts all of them before the receipt is returned.
type ReceiptInput struct {
	WorkID           string
	ContractVersion  string
	ProfileLock      ProfileLockInput
	Commit           string
	Tree             string
	Roles            []RoleInput
	ChangedFiles     []ChangedFileInput
	Commands         []CommandInput
	SurfaceScenario  SurfaceScenarioInput
	BaselineFailures []string
	Regressions      []string
	Review           []ReviewInput
	DocsImpact       []string
	Completion       CompletionInput
}

// Truncation records what tail truncation did to a command's output.
type Truncation struct {
	Truncated     bool  `json:"truncated"`
	OriginalBytes int64 `json:"original_bytes"`
}

// Command is one executed command with its bounded output summary.
type Command struct {
	Cmd              string     `json:"cmd"`
	Exit             int        `json:"exit"`
	DurationMs       int64      `json:"duration_ms"`
	OutputSummary    string     `json:"output_summary"`
	OutputTruncation Truncation `json:"output_truncation"`
}

// Pack is one resolved pack's identity in the receipt.
type Pack struct {
	Version string `json:"version"`
	Source  string `json:"source"`
	Digest  string `json:"digest"`
}

// ProfileLock is the receipt's profile_lock block.
type ProfileLock struct {
	Digest string          `json:"digest"`
	Packs  map[string]Pack `json:"packs"`
}

// Role is one agent role without credentials.
type Role struct {
	Role        string `json:"role"`
	Runtime     string `json:"runtime"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Substituted bool   `json:"substituted"`
}

// ChangedFile is one touched file and its ownership class.
type ChangedFile struct {
	Path      string `json:"path"`
	Ownership string `json:"ownership"`
}

// SurfaceScenario records whether a real-surface scenario ran.
type SurfaceScenario struct {
	Ran         bool   `json:"ran"`
	Description string `json:"description"`
}

// ReviewFinding is one review finding and its disposition.
type ReviewFinding struct {
	Agent       string `json:"agent"`
	Finding     string `json:"finding"`
	Disposition string `json:"disposition"`
}

// Completion is the run's completion verdict.
type Completion struct {
	Complete bool   `json:"complete"`
	Reason   string `json:"reason"`
	Blocker  string `json:"blocker,omitempty"`
}

// Receipt is the sanitized, schema-validated work record (spec
// section 14.1). Safe to commit: Build redacted every string field.
type Receipt struct {
	Schema           int             `json:"schema"`
	WorkID           string          `json:"work_id"`
	ContractVersion  string          `json:"contract_version"`
	ProfileLock      ProfileLock     `json:"profile_lock"`
	Commit           string          `json:"commit"`
	Tree             string          `json:"tree"`
	Roles            []Role          `json:"roles"`
	ChangedFiles     []ChangedFile   `json:"changed_files"`
	Commands         []Command       `json:"commands"`
	SurfaceScenario  SurfaceScenario `json:"surface_scenario"`
	BaselineFailures []string        `json:"baseline_failures"`
	Regressions      []string        `json:"regressions"`
	Review           []ReviewFinding `json:"review"`
	DocsImpact       []string        `json:"docs_impact"`
	Completion       Completion      `json:"completion"`
}

// Build converts raw run evidence into a sanitized, schema-validated
// receipt. Build performs no I/O; it is a pure input→receipt function.
// Every string field passes through redact.Apply before the receipt is
// assembled, command output is tail-truncated to the last 8 KiB (the
// truncation record preserves the original byte count), and the final
// receipt is validated against the embedded JSON Schema — an invalid
// receipt is an error, never a silently-wrong record.
func Build(input ReceiptInput) (*Receipt, error) {
	if err := ValidateWorkID(input.WorkID); err != nil {
		return nil, fmt.Errorf("evidence: %w", err)
	}

	r := &Receipt{
		Schema:          receiptSchemaVersion,
		WorkID:          redact.Apply(input.WorkID),
		ContractVersion: redact.Apply(input.ContractVersion),
		ProfileLock: ProfileLock{
			Digest: redact.Apply(input.ProfileLock.Digest),
			Packs:  make(map[string]Pack, len(input.ProfileLock.Packs)),
		},
		Commit:       redact.Apply(input.Commit),
		Tree:         redact.Apply(input.Tree),
		Roles:        make([]Role, 0, len(input.Roles)),
		ChangedFiles: make([]ChangedFile, 0, len(input.ChangedFiles)),
		Commands:     make([]Command, 0, len(input.Commands)),
		SurfaceScenario: SurfaceScenario{
			Ran:         input.SurfaceScenario.Ran,
			Description: redact.Apply(input.SurfaceScenario.Description),
		},
		BaselineFailures: redactAll(input.BaselineFailures),
		Regressions:      redactAll(input.Regressions),
		Review:           make([]ReviewFinding, 0, len(input.Review)),
		DocsImpact:       redactAll(input.DocsImpact),
		Completion: Completion{
			Complete: input.Completion.Complete,
			Reason:   redact.Apply(input.Completion.Reason),
			Blocker:  redact.Apply(input.Completion.Blocker),
		},
	}
	// Pack keys are strings too and must obey the redact-everything
	// invariant. A key that changes under redaction is credential- or
	// path-shaped; rather than emit an unredactable key (or silently
	// merge several into one placeholder), Build fails closed naming
	// only the redacted form — never the raw key, which may be a live
	// secret.
	for id, p := range input.ProfileLock.Packs {
		key := redact.Apply(id)
		if key != id {
			return nil, fmt.Errorf("evidence: pack key redacts to %q; refusing credential-shaped pack key in receipt", key)
		}
		r.ProfileLock.Packs[key] = Pack{
			Version: redact.Apply(p.Version),
			Source:  redact.Apply(p.Source),
			Digest:  redact.Apply(p.Digest),
		}
	}
	for _, role := range input.Roles {
		r.Roles = append(r.Roles, Role{
			Role:        redact.Apply(role.Role),
			Runtime:     redact.Apply(role.Runtime),
			Provider:    redact.Apply(role.Provider),
			Model:       redact.Apply(role.Model),
			Substituted: role.Substituted,
		})
	}
	for _, cf := range input.ChangedFiles {
		r.ChangedFiles = append(r.ChangedFiles, ChangedFile{
			Path:      redact.Apply(cf.Path),
			Ownership: redact.Apply(cf.Ownership),
		})
	}
	for _, cmd := range input.Commands {
		summary, trunc := summarize(cmd.Output)
		r.Commands = append(r.Commands, Command{
			Cmd:              redact.Apply(cmd.Cmd),
			Exit:             cmd.Exit,
			DurationMs:       cmd.DurationMs,
			OutputSummary:    summary,
			OutputTruncation: trunc,
		})
	}
	for _, rf := range input.Review {
		r.Review = append(r.Review, ReviewFinding{
			Agent:       redact.Apply(rf.Agent),
			Finding:     redact.Apply(rf.Finding),
			Disposition: redact.Apply(rf.Disposition),
		})
	}

	if err := Validate(r); err != nil {
		return nil, fmt.Errorf("evidence: built receipt is invalid: %w", err)
	}
	return r, nil
}

// summarize redacts then tail-truncates one command's output to the last
// outputSummaryBytes, recording the original byte count. Redaction runs
// first so no credential fragment survives at the truncation boundary and
// placeholders stay intact.
func summarize(output string) (string, Truncation) {
	s := redact.Apply(output)
	orig := int64(len(s))
	if len(s) <= outputSummaryBytes {
		return s, Truncation{Truncated: false, OriginalBytes: orig}
	}
	return tail(s), Truncation{Truncated: true, OriginalBytes: orig}
}

// tail keeps the last outputSummaryBytes of s, trimming any split UTF-8
// continuation bytes at the new start so the result stays valid UTF-8.
func tail(s string) string {
	s = s[len(s)-outputSummaryBytes:]
	for i := 0; i < 3 && len(s) > 0 && s[0]&0xC0 == 0x80; i++ {
		s = s[1:]
	}
	return s
}

func redactAll(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, redact.Apply(s))
	}
	return out
}

// NewWorkID derives a collision-resistant work ID of the spec section
// 14.1 shape YYYYMMDD-slug-4hex. The 4-hex suffix is the first two bytes
// of SHA-256 over the slug and the Unix second, so the same slug and
// timestamp (within the same second) always yield the same ID while
// distinct runs diverge. The slug must be kebab-case lowercase
// alnum words, at most maxSlugLen bytes.
func NewWorkID(now time.Time, slug string) (string, error) {
	if !slugPattern.MatchString(slug) {
		return "", fmt.Errorf("evidence: slug %q must be kebab-case lowercase alnum words", slug)
	}
	if len(slug) > maxSlugLen {
		return "", fmt.Errorf("evidence: slug exceeds %d bytes", maxSlugLen)
	}
	h := sha256.New()
	h.Write([]byte(slug))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatInt(now.Unix(), 10)))
	suffix := hex.EncodeToString(h.Sum(nil)[:2])
	return now.Format("20060102") + "-" + slug + "-" + suffix, nil
}

// ValidateWorkID checks the spec section 14.1 work-id shape:
// YYYYMMDD-kebab-slug-4hex.
func ValidateWorkID(id string) error {
	if !workIDPattern.MatchString(id) {
		return fmt.Errorf("work_id %q must match YYYYMMDD-kebab-slug-4hex", id)
	}
	return nil
}

var (
	compileSchemaOnce sync.Once
	compiledSchema    *jsonschema.Schema
	compileSchemaErr  error
)

func compileSchema() (*jsonschema.Schema, error) {
	compileSchemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
		if err != nil {
			compileSchemaErr = err
			return
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("urn:projectctl:evidence-receipt.schema.json", doc); err != nil {
			compileSchemaErr = err
			return
		}
		compiledSchema, compileSchemaErr = compiler.Compile("urn:projectctl:evidence-receipt.schema.json")
	})
	return compiledSchema, compileSchemaErr
}

// encode marshals r as indented JSON without HTML escaping so
// <redacted:...> placeholders stay human-readable in committed evidence.
func encode(r *Receipt) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Validate checks a receipt against the embedded JSON Schema — the same
// schema Build enforces, available to callers that reconstruct receipts
// from disk.
func Validate(r *Receipt) error {
	bs, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode for validation: %w", err)
	}
	var instance any
	if err := json.Unmarshal(bs, &instance); err != nil {
		return fmt.Errorf("decode for validation: %w", err)
	}
	s, err := compileSchema()
	if err != nil {
		return fmt.Errorf("embedded schema invalid: %w", err)
	}
	if err := s.Validate(instance); err != nil {
		return fmt.Errorf("schema validation failed: %w", err)
	}
	return nil
}
