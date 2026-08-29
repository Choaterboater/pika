package redact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bech32 charset after the "1" separator for nsec/npub keys
// (excludes 1, b, i, o).
const bech32 = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

func TestApplyFamilies(t *testing.T) {
	nostr := "nsec1" + strings.Repeat(bech32, 2)[:58]
	github := "ghp_" + "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef1234" // 36 after prefix
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"api-key", "export OPENAI_KEY=sk-abcdefghijklmnopqrstuvwx",
			"export OPENAI_KEY=<redacted:api-key>"},
		{"oauth", "claude key sk-ant-api03-abcdef1234567890abcdef12",
			"claude key <redacted:oauth>"},
		{"github-token", "push with " + github,
			"push with <redacted:github-token>"},
		{"github-token-gho", "run as gho_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef1234",
			"run as <redacted:github-token>"},
		{"slack-token", "slack bot xoxb-FAKE-FAKE-FAKE-FAKE-FAKE-FAKE-FAKE",
			"slack bot <redacted:slack-token>"},
		{"aws-key", "aws access key AKIAIOSFODNN7EXAMPLE in env",
			"aws access key <redacted:aws-key> in env"},
		{"nostr-key", "my key is " + nostr + " keep it secret",
			"my key is <redacted:nostr-key> keep it secret"},
		{"bearer", "curl -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9'",
			"curl -H 'Authorization: <redacted:bearer>'"},
		{"pem-block", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----",
			"<redacted:pem>"},
		{"pem-header-alone", "oops -----BEGIN OPENSSH PRIVATE KEY-----",
			"oops <redacted:pem-header>"},
		{"user-path", "config at /Users/alice/project",
			"config at <redacted:user-path>project"},
		{"user-path-home", "/home/bob/.config/x",
			"<redacted:user-path>.config/x"},
		{"machine-path", "temp in /var/folders/zz/abcdef",
			"temp in <redacted:machine-path>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Apply(tt.in); got != tt.want {
				t.Errorf("Apply(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestApplyNonMatches(t *testing.T) {
	tests := []string{
		// Code with the word "token" in identifiers.
		"const tokenValue = token.GetValue(); // refresh the token",
		"func setToken(t token) { tokenStore.Set(t) }",
		// Short / truncated credentials below each pattern's minimum.
		"sk-short12345",
		"xoxb-123",
		"AKIA12345",
		"nsec1abc",
		"ghp_short",
		"Bearer x",
		"bearer 123456789012345", // 15 chars, one below the 16 minimum
		// URLs with long paths must not be redacted as user paths.
		"https://example.com/home/johndoe/very/long/path/segments/here",
		"https://docs.example.org/Users/guide/install",
		// YAML keys named token with plain values.
		"api_token: abcdefghijklmnop\nsecret_token: Bearer\ngithub_token: ghp",
		// Ordinary prose.
		"The Users guide and Home page describe home directories.",
	}
	for _, in := range tests {
		if got := Apply(in); got != in {
			t.Errorf("Apply(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestApplyOverlapLongestMatchWins(t *testing.T) {
	// sk-ant-... is both an api-key (sk-...) and oauth (sk-ant-...) prefix
	// match; the longer oauth match must win.
	in := "key sk-ant-api03-abcdef1234567890abcdef12"
	want := "key <redacted:oauth>"
	if got := Apply(in); got != want {
		t.Errorf("Apply(%q) = %q, want %q", in, got, want)
	}
	// A full PEM block must win over its own header line.
	in = "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----\ntail"
	want = "<redacted:pem>\ntail"
	if got := Apply(in); got != want {
		t.Errorf("PEM block Apply = %q, want %q", got, want)
	}
}

func TestFileClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clean.txt")
	if err := os.WriteFile(path, []byte("just some plain text\nnothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, findings, err := File(path)
	if err != nil {
		t.Fatal(err)
	}
	if !clean || len(findings) != 0 {
		t.Errorf("File(clean) = %v, %v; want clean, no findings", clean, findings)
	}
}

func TestFileFindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cred.txt")
	content := "line one\nkey = AKIAIOSFODNN7EXAMPLE\nline three\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, findings, err := File(path)
	if err != nil {
		t.Fatal(err)
	}
	if clean {
		t.Error("File(cred) reported clean, want false")
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %v", len(findings), findings)
	}
	f := findings[0]
	if f.Kind != "aws-key" {
		t.Errorf("Kind = %q, want aws-key", f.Kind)
	}
	if f.Line != 2 {
		t.Errorf("Line = %d, want 2", f.Line)
	}
	line2 := "key = AKIAIOSFODNN7EXAMPLE"
	wantCol := strings.Index(line2, "AKIA") + 1 // 1-based byte column within the line
	if f.Col != wantCol {
		t.Errorf("Col = %d, want %d", f.Col, wantCol)
	}
}

func TestFileFindingsCappedAt100(t *testing.T) {
	path := filepath.Join(t.TempDir(), "many.txt")
	var b strings.Builder
	for range 120 {
		b.WriteString("sk-abcdefghijklmnopqrstuvwx\n") // one api-key per line
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, findings, err := File(path)
	if err != nil {
		t.Fatal(err)
	}
	if clean {
		t.Error("clean = true, want false for file with findings")
	}
	if len(findings) != 100 {
		t.Errorf("len(findings) = %d, want capped at 100", len(findings))
	}
	if findings[0].Line != 1 || findings[99].Line != 100 {
		t.Errorf("findings not in line order: first Line=%d, 100th Line=%d",
			findings[0].Line, findings[99].Line)
	}
}

func TestFileMissing(t *testing.T) {
	if _, _, err := File(filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Error("File(missing) err = nil, want error")
	}
}
