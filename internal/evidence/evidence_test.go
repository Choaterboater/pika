package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture returns a fully populated, well-formed ReceiptInput.
func fixture() ReceiptInput {
	return ReceiptInput{
		WorkID:          "20260828-auth-timeout-7f3a",
		ContractVersion: "1.0.0",
		ProfileLock: ProfileLockInput{
			Digest: "sha256:abcdef0123456789",
			Packs: map[string]PackInput{
				"go-gke": {Version: "1.2.0", Source: "builtin", Digest: "sha256:0011"},
			},
		},
		Commit: "34b828f",
		Tree:   "tree/9aa2",
		Roles: []RoleInput{
			{Role: "implementer", Runtime: "omp", Provider: "openrouter", Model: "glm", Substituted: false},
			{Role: "reviewer", Runtime: "omp", Provider: "anthropic", Model: "claude", Substituted: true},
		},
		ChangedFiles: []ChangedFileInput{
			{Path: "internal/evidence/receipt.go", Ownership: "kernel"},
		},
		Commands: []CommandInput{
			{Cmd: "go test ./...", Exit: 0, DurationMs: 1200, Output: "ok  \tgithub.com/Choaterboater/projectctl\t1.2s"},
		},
		SurfaceScenario:  SurfaceScenarioInput{Ran: true, Description: "ran projectctl check --all locally"},
		BaselineFailures: []string{},
		Regressions:      []string{},
		Review: []ReviewInput{
			{Agent: "reviewer", Finding: "error wrapped twice", Disposition: "fixed"},
		},
		DocsImpact: []string{},
		Completion: CompletionInput{Complete: true, Reason: "all gates green"},
	}
}

func TestBuildProducesSchemaValidReceipt(t *testing.T) {
	r, err := Build(fixture())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.Schema != 1 {
		t.Errorf("receipt schema = %d, want 1", r.Schema)
	}
	if r.WorkID != "20260828-auth-timeout-7f3a" {
		t.Errorf("work_id = %q", r.WorkID)
	}
	if len(r.Commands) != 1 || r.Commands[0].OutputSummary == "" {
		t.Fatalf("commands not carried through: %+v", r.Commands)
	}
	if r.Commands[0].OutputTruncation.Truncated {
		t.Errorf("short output marked truncated: %+v", r.Commands[0].OutputTruncation)
	}
	if r.ProfileLock.Packs["go-gke"].Version != "1.2.0" {
		t.Errorf("pack version = %q", r.ProfileLock.Packs["go-gke"].Version)
	}
	// Round-trip through the embedded schema exactly as callers would.
	if err := Validate(r); err != nil {
		t.Errorf("receipt fails its own schema: %v", err)
	}
}

func TestBuildRedactsCredentialShapedStrings(t *testing.T) {
	oauth := "sk-ant-api03-abcdefghij0123456789ABCDE"
	gh := "ghp_" + strings.Repeat("a1B2c3D4e5", 4) // 40 alnum chars
	in := fixture()
	in.Commands[0].Output = "export OPENAI_KEY=" + oauth + " push with " + gh
	in.Completion = CompletionInput{Complete: false, Reason: "failed at " + gh + " see /Users/alice/secrets/notes.txt"}
	in.ChangedFiles[0].Path = "/Users/alice/work/internal/evidence/receipt.go"

	r, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	bs, err := encode(r)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	s := string(bs)
	for _, secret := range []string{oauth, gh, "/Users/alice/"} {
		if strings.Contains(s, secret) {
			t.Errorf("receipt leaks %q", secret)
		}
	}
	for _, want := range []string{"<redacted:oauth>", "<redacted:github-token>", "<redacted:user-path>"} {
		if !strings.Contains(s, want) {
			t.Errorf("receipt missing %s; got %s", want, s)
		}
	}
}

func TestOutputSummaryTruncatesTo8KBTail(t *testing.T) {
	in := fixture()
	in.Commands[0].Output = strings.Repeat("a", 10*1024)
	in.Commands = append(in.Commands, CommandInput{Cmd: "short", Exit: 0, DurationMs: 1, Output: "ok"})

	r, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	big := r.Commands[0]
	if len(big.OutputSummary) != 8*1024 {
		t.Errorf("summary len = %d, want %d", len(big.OutputSummary), 8*1024)
	}
	if !big.OutputTruncation.Truncated || big.OutputTruncation.OriginalBytes != 10*1024 {
		t.Errorf("truncation record = %+v, want {true 10240}", big.OutputTruncation)
	}
	short := r.Commands[1]
	if len(short.OutputSummary) != 2 || short.OutputTruncation.Truncated || short.OutputTruncation.OriginalBytes != 2 {
		t.Errorf("short summary = %q %+v", short.OutputSummary, short.OutputTruncation)
	}
}

func TestCompletionRules(t *testing.T) {
	t.Run("incomplete requires reason", func(t *testing.T) {
		in := fixture()
		in.Completion = CompletionInput{Complete: false, Reason: ""}
		if _, err := Build(in); err == nil {
			t.Error("Build accepted complete=false with empty reason")
		}
	})
	t.Run("blocker only when incomplete", func(t *testing.T) {
		in := fixture()
		in.Completion = CompletionInput{Complete: true, Reason: "done", Blocker: "stale blocker"}
		if _, err := Build(in); err == nil {
			t.Error("Build accepted blocker with complete=true")
		}
	})
	t.Run("blocker with reason and incomplete is valid", func(t *testing.T) {
		in := fixture()
		in.Completion = CompletionInput{Complete: false, Reason: "gate 3 red", Blocker: "flaky network gate"}
		r, err := Build(in)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if err := Validate(r); err != nil {
			t.Errorf("receipt fails schema: %v", err)
		}
	})
}

func TestNewWorkID(t *testing.T) {
	now := time.Date(2026, 8, 28, 14, 3, 9, 0, time.UTC)
	id1, err := NewWorkID(now, "auth-timeout")
	if err != nil {
		t.Fatalf("NewWorkID: %v", err)
	}
	id2, _ := NewWorkID(now.Add(300*time.Millisecond), "auth-timeout") // same second
	if id1 != id2 {
		t.Errorf("not deterministic within a second: %q vs %q", id1, id2)
	}
	if got, want := id1[:8], "20260828"; got != want {
		t.Errorf("date prefix = %q, want %q", got, want)
	}

	// Different second yields a different hash suffix (collision resistance).
	id3, _ := NewWorkID(now.Add(2*time.Second), "auth-timeout")
	if id3 == id1 {
		t.Error("different seconds produced identical ids")
	}
	// The spec §14.1 example shape validates.
	if err := ValidateWorkID("20260828-auth-timeout-7f3a"); err != nil {
		t.Errorf("ValidateWorkID rejected spec example: %v", err)
	}
	for _, bad := range []string{
		"", "20260828", "20260828-auth-timeout", "20260828-auth-timeout-7f3",
		"20260828--auth-7f3a", "20260828-Auth-7f3a", "20260828-auth-7f3a-", "20260828-auth-timeout-7f3a-x",
	} {
		if err := ValidateWorkID(bad); err == nil {
			t.Errorf("ValidateWorkID accepted %q", bad)
		}
	}
	for _, bad := range []string{"", "-slug", "slug-", "Slugs", "a--b"} {
		if _, err := NewWorkID(now, bad); err == nil {
			t.Errorf("NewWorkID accepted slug %q", bad)
		}
	}
}

func TestBuildValidatesWorkIDShape(t *testing.T) {
	in := fixture()
	in.WorkID = "not-a-work-id"
	if _, err := Build(in); err == nil {
		t.Error("Build accepted malformed work_id")
	}
}

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "work", "evidence.json")
	r, err := Build(fixture())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := Write(path, r); err != nil {
		t.Fatalf("Write: %v", err)
	}
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(bs, &got); err != nil {
		t.Fatalf("written receipt is not JSON: %v", err)
	}
	if got["work_id"] != "20260828-auth-timeout-7f3a" {
		t.Errorf("written work_id = %v", got["work_id"])
	}
	// No temp leftovers in the target directory.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".evidence-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestBuildRejectsCredentialShapedPackKey(t *testing.T) {
	oauth := "sk-ant-api03-abcdefghij0123456789ABCDE"
	in := fixture()
	in.ProfileLock.Packs[oauth] = PackInput{Version: "1.0.0", Source: "builtin", Digest: "sha256:42"}
	_, err := Build(in)
	if err == nil {
		t.Fatal("Build accepted a credential-shaped pack key")
	}
	msg := err.Error()
	if !strings.Contains(msg, "<redacted:oauth>") {
		t.Errorf("error does not name the redacted key form: %v", err)
	}
	if strings.Contains(msg, oauth) {
		t.Errorf("error leaks the raw pack key: %v", err)
	}
}

func TestWriteFsyncsCreatedDirectoryChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docs", "work", "run", "evidence.json")

	var calls [][2]string
	origChain, origDir := syncCreatedChain, syncDir
	syncCreatedChain = func(d, anchor string) error {
		calls = append(calls, [2]string{d, anchor})
		return origChain(d, anchor)
	}
	syncDir = origDir
	defer func() { syncCreatedChain, syncDir = origChain, origDir }()

	r, err := Build(fixture())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := Write(path, r); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("syncCreatedChain called %d times, want 1", len(calls))
	}
	gotDir, gotAnchor := calls[0][0], calls[0][1]
	if gotDir != filepath.Dir(path) {
		t.Errorf("chain start = %q, want %q", gotDir, filepath.Dir(path))
	}
	// The anchor must be the nearest pre-existing ancestor (the temp
	// root), not a directory MkdirAll just created.
	if gotAnchor != dir {
		t.Errorf("anchor = %q, want pre-existing %q", gotAnchor, dir)
	}
}
