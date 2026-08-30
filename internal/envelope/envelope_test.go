package envelope

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustParse validates doc as an envelope and binds it to a fake repo root.
func mustParse(t *testing.T, doc string) *Envelope {
	t.Helper()
	env, err := Validate([]byte(doc))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return NewEnvelope(env, "/repo")
}

func TestEnvelopeDeniesUndeclaredNetwork(t *testing.T) {
	env := mustParse(t, `{"schema":1,"allow":{"network":[]}}`)
	if env.Allows(Operation{Kind: "network", Target: "api.github.com"}) {
		t.Fatal("network must be denied unless declared")
	}
}

func TestDenyByDefaultEverywhere(t *testing.T) {
	env := mustParse(t, "schema: 1")
	denied := []Operation{
		{Kind: "fs_write", Target: "main.go"},
		{Kind: "exec", Target: "go build ./..."},
		{Kind: "network", Target: "api.github.com"},
		{Kind: "credential", Target: "gh-token"},
		{Kind: "github", Target: "contents:write"},
		{Kind: "budget", Target: "openai"},
		{Kind: "unknown-kind", Target: "x"},
	}
	for _, op := range denied {
		if env.Allows(op) {
			t.Errorf("absent class %q must deny %q", op.Kind, op.Target)
		}
	}
	// fs_read is the liberal default: inside the repo root it is allowed.
	if !env.Allows(Operation{Kind: "fs_read", Target: "main.go"}) {
		t.Error("fs_read inside repoRoot must be allowed by default")
	}
	if env.Allows(Operation{Kind: "fs_read", Target: "/etc/passwd"}) {
		t.Error("fs_read outside repoRoot must be denied by default")
	}
	// Every class explicitly empty also denies.
	env = mustParse(t, `{"schema":1,"allow":{"fs_write":[],"exec":[],"network":[],"credential":[],"github":[],"budget":{}}}`)
	for _, op := range denied {
		if env.Allows(op) {
			t.Errorf("empty class %q must deny %q", op.Kind, op.Target)
		}
	}
}

func TestFSWritePrefixSemantics(t *testing.T) {
	env := mustParse(t, `{"schema":1,"allow":{"fs_write":[".project/state"]}}`)
	allowed := []string{
		".project/state/x.json",
		".project/state",
		".project/state/sub/dir/y.json",
		".project/state/./x.json",
	}
	for _, p := range allowed {
		if !env.Allows(Operation{Kind: "fs_write", Target: p}) {
			t.Errorf("fs_write %q must be allowed by prefix entry", p)
		}
	}
	denied := []string{
		"docs/x.md",
		".project/staterun/x", // prefix must respect path boundaries
		".project/state/../secrets",
		"../outside",
		"/repo/.project/state/../cmd/main.go",
		"",
	}
	for _, p := range denied {
		if env.Allows(Operation{Kind: "fs_write", Target: p}) {
			t.Errorf("fs_write %q must be denied", p)
		}
	}
	// Exact-file entries allow only themselves.
	env = mustParse(t, `{"schema":1,"allow":{"fs_write":["go.mod"]}}`)
	if !env.Allows(Operation{Kind: "fs_write", Target: "go.mod"}) {
		t.Error("exact fs_write entry must match itself")
	}
	if env.Allows(Operation{Kind: "fs_write", Target: "go.sum"}) {
		t.Error("exact fs_write entry must not match siblings")
	}
}

func TestFSReadInsideRepoAllowed(t *testing.T) {
	env := mustParse(t, `{"schema":1,"allow":{"fs_write":[".project/state"]}}`)
	allowed := []string{"main.go", "internal/envelope/envelope.go", "/repo/internal/envelope/envelope.go"}
	for _, p := range allowed {
		if !env.Allows(Operation{Kind: "fs_read", Target: p}) {
			t.Errorf("fs_read %q inside repoRoot must be allowed", p)
		}
	}
	denied := []string{"/etc/passwd", "/repo-evil/main.go", "../secrets", ""}
	for _, p := range denied {
		if env.Allows(Operation{Kind: "fs_read", Target: p}) {
			t.Errorf("fs_read %q outside repoRoot must be denied", p)
		}
	}
}

func TestExecMatching(t *testing.T) {
	env := mustParse(t, `{"schema":1,"allow":{"exec":["go test","git *"]}}`)
	allowed := []string{
		"go test",              // exact element match
		"git push origin main", // trailing * glob on last element
		"git status",
		"git", // * absorbs zero additional elements
	}
	for _, c := range allowed {
		if !env.Allows(Operation{Kind: "exec", Target: c}) {
			t.Errorf("exec %q must be allowed", c)
		}
	}
	denied := []string{
		"go test ./...", // exact entry must not match extra elements
		"gitl push",     // first element must match exactly
		"go build",      // undeclared subcommand
		"sudo git push", // argv0 must match
		"",
	}
	for _, c := range denied {
		if env.Allows(Operation{Kind: "exec", Target: c}) {
			t.Errorf("exec %q must be denied", c)
		}
	}
}

func TestNetworkMatching(t *testing.T) {
	env := mustParse(t, `{"schema":1,"allow":{"network":["api.github.com","registry.npmjs.org:443","*.example.com"]}}`)
	allowed := []string{
		"api.github.com",
		"api.github.com:443", // portless entry matches any port
		"registry.npmjs.org:443",
		"api.example.com",
		"api.example.com:8080",
	}
	for _, h := range allowed {
		if !env.Allows(Operation{Kind: "network", Target: h}) {
			t.Errorf("network %q must be allowed", h)
		}
	}
	denied := []string{
		"evil.github.com",
		"registry.npmjs.org:8443", // port-pinned entry must not match other ports
		"example.com",             // wildcard covers subdomains only
		"notexample.com",
		"",
	}
	for _, h := range denied {
		if env.Allows(Operation{Kind: "network", Target: h}) {
			t.Errorf("network %q must be denied", h)
		}
	}
}

func TestCredentialAndGitHubExactMatch(t *testing.T) {
	env := mustParse(t, `{"schema":1,"allow":{"credential":["gh-token"],"github":["contents:write","pull_requests:read"]}}`)
	if !env.Allows(Operation{Kind: "credential", Target: "gh-token"}) {
		t.Error("declared credential must be allowed")
	}
	if env.Allows(Operation{Kind: "credential", Target: "gh-tok"}) {
		t.Error("credential names must match exactly, never by value prefix")
	}
	if !env.Allows(Operation{Kind: "github", Target: "contents:write"}) {
		t.Error("declared scope must be allowed")
	}
	if env.Allows(Operation{Kind: "github", Target: "contents:write:extra"}) {
		t.Error("scope must match exactly")
	}
}

func TestBudgetDeclaredProviderAllowed(t *testing.T) {
	env := mustParse(t, `{"schema":1,"allow":{"budget":{"openai":5.0,"anthropic":0}}}`)
	if !env.Allows(Operation{Kind: "budget", Target: "openai"}) {
		t.Error("declared budget provider must be allowed")
	}
	if !env.Allows(Operation{Kind: "budget", Target: "anthropic"}) {
		t.Error("zero ceiling is still a declared provider")
	}
	if env.Allows(Operation{Kind: "budget", Target: "google"}) {
		t.Error("undeclared budget provider must be denied")
	}
	if got := env.Env.Allow.Budget["openai"]; got != 5.0 {
		t.Errorf("declared max = %v, want 5.0", got)
	}
}

func TestValidateRejectsMalformedEnvelopes(t *testing.T) {
	cases := map[string]string{
		"unknown top-level key":  "schema: 1\noops: true\n",
		"unknown allow key":      "schema: 1\nallow:\n  fs_writ: [x]\n",
		"duplicate key":          "schema: 1\nschema: 2\n",
		"missing schema":         "allow:\n  network: []\n",
		"wrong schema version":   "schema: 2\n",
		"non-string entry":       "schema: 1\nallow:\n  network: [1]\n",
		"escaping fs_write path": "schema: 1\nallow:\n  fs_write: [../outside]\n",
		"absolute fs_write path": "schema: 1\nallow:\n  fs_write: [/etc]\n",
		"empty fs_write entry":   "schema: 1\nallow:\n  fs_write: [\"\"]\n",
		"negative budget":        "schema: 1\nallow:\n  budget:\n    openai: -1\n",
	}
	for name, doc := range cases {
		if _, err := Validate([]byte(doc)); err == nil {
			t.Errorf("%s: Validate must fail", name)
		}
	}
}

func TestValidateAcceptsFullEnvelope(t *testing.T) {
	doc := `
schema: 1
allow:
  fs_write:
    - .project/state
  exec:
    - go test *
    - git commit
  network:
    - api.github.com
  credential:
    - gh-token
  github:
    - contents:write
  budget:
    openai: 5.0
rollback_boundary: git reset --hard HEAD
`
	env, err := Validate([]byte(doc))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if env.Schema != 1 {
		t.Errorf("Schema = %d, want 1", env.Schema)
	}
	if env.RollbackBoundary != "git reset --hard HEAD" {
		t.Errorf("RollbackBoundary = %q", env.RollbackBoundary)
	}
	if len(env.Allow.FSWrite) != 1 || env.Allow.FSWrite[0] != ".project/state" {
		t.Errorf("FSWrite = %v", env.Allow.FSWrite)
	}
}

func TestLoad(t *testing.T) {
	// Load binds the root the caller passes, never one inferred from the
	// envelope's own location.
	root := t.TempDir()
	dir := filepath.Join(root, ".project", "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "envelope.yaml")
	doc := "schema: 1\nallow:\n  fs_write: [.project/state]\n"
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := Load(root, path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if env.repoRoot != filepath.Clean(root) {
		t.Errorf("repoRoot = %q, want %q", env.repoRoot, filepath.Clean(root))
	}
	if !env.Allows(Operation{Kind: "fs_write", Target: ".project/state/evidence.json"}) {
		t.Error("loaded envelope must enforce policy")
	}
	if env.Allows(Operation{Kind: "fs_write", Target: "docs/x.md"}) {
		t.Error("loaded envelope must deny undeclared writes")
	}
	if _, err := Load(root, filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("Load of missing file must fail")
	}
	// A relocated envelope keeps authorizing the root it was loaded
	// against; the old directory arithmetic would have re-rooted it at
	// the temp directory's grandparent.
	moved := filepath.Join(root, "envelope.yaml")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	relocated, err := Load(root, moved)
	if err != nil {
		t.Fatalf("Load relocated: %v", err)
	}
	if relocated.repoRoot != filepath.Clean(root) {
		t.Errorf("relocated repoRoot = %q, want %q", relocated.repoRoot, filepath.Clean(root))
	}
}

func TestEmbeddedSchemaIsCanonical(t *testing.T) {
	// The schema embedded at internal/envelope/envelope.schema.json is
	// the one canonical in-repo envelope schema; it is closed.
	if !strings.Contains(string(schemaJSON), "additionalProperties") {
		t.Fatal("schema must be closed")
	}
	if !strings.Contains(string(schemaJSON), `"exec"`) {
		t.Fatal("schema must constrain the exec allow list")
	}
}

func TestValidateRejectsBareStarExec(t *testing.T) {
	// A bare "*" exec entry grants every command — the one silent
	// allow-all in a fail-closed module. Validate must refuse it and
	// name the offending entry.
	const doc = `{"schema":1,"allow":{"exec":["*"]}}`
	_, err := Validate([]byte(doc))
	if err == nil {
		t.Fatal("Validate accepted a bare \"*\" exec entry")
	}
	if !strings.Contains(err.Error(), `"*"`) {
		t.Errorf("error %q must name the bare \"*\" entry", err)
	}
}
