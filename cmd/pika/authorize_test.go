package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// authorizeProject scaffolds the smallest repository `pika authorize`
// operates on and makes it the working directory.
func authorizeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if code, _, errb := dispatchArgs(t, "init", "--profile", "go", "--name", "demo", "--root", dir); code != 0 {
		t.Fatalf("init exit = %d: %s", code, errb)
	}
	t.Chdir(dir)
	return dir
}

// The promise `pika doctor` and `pika explain envelope_denied` both make
// is that running this command produces a usable envelope. Anything less
// than a file the kernel's own loader accepts breaks that promise.
func TestAuthorizeWritesAnEnvelopeDoctorAccepts(t *testing.T) {
	dir := authorizeProject(t)
	code, out, errb := dispatchArgs(t, "authorize", "--scope", "project")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb)
	}
	path := filepath.Join(dir, ".project", "state", "envelope.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("envelope not written: %v", err)
	}
	if !strings.Contains(out, "fs_write") {
		t.Errorf("stdout did not show the document for review:\n%s", out)
	}
	for _, want := range []string{"schema: 1", "- .project", "- docs", "- review", "rollback_boundary: repository"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("envelope missing %q:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "budget") {
		t.Errorf("envelope carries a budget nothing enforces:\n%s", data)
	}

	code, dout, _ := dispatchArgs(t, "doctor")
	if strings.Contains(dout, "warn  envelope") {
		t.Errorf("doctor still warns about the envelope after authorize (exit %d):\n%s", code, dout)
	}
	if !strings.Contains(dout, "ok    envelope") {
		t.Errorf("doctor does not report the envelope as ok:\n%s", dout)
	}
}

// A capability grant is not world-readable.
func TestAuthorizeWritesMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not meaningful on Windows")
	}
	dir := authorizeProject(t)
	if code, _, errb := dispatchArgs(t, "authorize"); code != 0 {
		t.Fatalf("exit = %d: %s", code, errb)
	}
	info, err := os.Stat(filepath.Join(dir, ".project", "state", "envelope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("envelope mode = %04o, want 0600", got)
	}
}

// The migration this command exists for: an operator hand-authored an
// envelope (which lands 0644) and runs --force to regenerate it. Go's
// os.WriteFile does not change the mode of a file that already exists,
// so the overwrite path must chmod explicitly — and the message must
// not claim a mode nobody confirmed.
func TestAuthorizeForceTightensModeOnOverwrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not meaningful on Windows")
	}
	dir := authorizeProject(t)
	path := filepath.Join(dir, ".project", "state", "envelope.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("schema: 1\nallow:\n  fs_write:\n    - .project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errb := dispatchArgs(t, "authorize", "--scope", "project", "--force")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("envelope mode after --force overwrite = %04o, want 0600", got)
	}
	if !strings.Contains(out, "(mode 0600)") {
		t.Errorf("stdout does not report the verified mode:\n%s", out)
	}
}

// Overwriting an authorization silently is how an operator loses grants
// they deliberately added. Refuse, show the delta, write nothing.
func TestAuthorizeRefusesToOverwriteWithoutForce(t *testing.T) {
	dir := authorizeProject(t)
	if code, _, errb := dispatchArgs(t, "authorize", "--scope", "project", "--network", "proxy.golang.org"); code != 0 {
		t.Fatalf("first authorize exit = %d: %s", code, errb)
	}
	path := filepath.Join(dir, ".project", "state", "envelope.yaml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	code, _, errb := dispatchArgs(t, "authorize", "--scope", "read")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errb, "already exists") || !strings.Contains(errb, "--force") {
		t.Errorf("stderr does not explain the refusal:\n%s", errb)
	}
	if !strings.Contains(errb, "- network") || !strings.Contains(errb, "proxy.golang.org") {
		t.Errorf("stderr does not report the grant that would be lost:\n%s", errb)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("refused authorize still modified the envelope")
	}

	if code, _, errb := dispatchArgs(t, "authorize", "--scope", "read", "--force"); code != 0 {
		t.Fatalf("--force exit = %d: %s", code, errb)
	}
	forced, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(forced), "fs_write") || strings.Contains(string(forced), "proxy.golang.org") {
		t.Errorf("--force did not replace the envelope with the read scope:\n%s", forced)
	}
}

func TestAuthorizeJSONReportsWhatLanded(t *testing.T) {
	dir := authorizeProject(t)
	code, out, errb := dispatchArgs(t, "authorize", "--scope", "project", "--github", "pull_request:write", "--json")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, errb)
	}
	var res struct {
		Root     string `json:"root"`
		Scope    string `json:"scope"`
		Path     string `json:"path"`
		Written  bool   `json:"written"`
		Document string `json:"document"`
		Envelope struct {
			Schema int `json:"schema"`
			Allow  struct {
				FSWrite []string `json:"fs_write"`
				Exec    []string `json:"exec"`
				GitHub  []string `json:"github"`
			} `json:"allow"`
		} `json:"envelope"`
	}
	env := resultOf(t, []byte(out), "authorize", &res)
	if !env.OK {
		t.Errorf("ok = false though authorize wrote the envelope:\n%s", out)
	}
	if !res.Written || res.Scope != "project" || res.Envelope.Schema != 1 {
		t.Errorf("result = %+v, want a written project envelope", res)
	}
	if res.Path != filepath.Join(dir, ".project", "state", "envelope.yaml") {
		t.Errorf("path = %q", res.Path)
	}
	if len(res.Envelope.Allow.GitHub) != 1 || res.Envelope.Allow.GitHub[0] != "pull_request:write" {
		t.Errorf("github = %v", res.Envelope.Allow.GitHub)
	}
	if len(res.Envelope.Allow.Exec) == 0 {
		t.Error("project scope authorized no gate command")
	}
	if res.Document == "" {
		t.Error("JSON result omits the document")
	}
}

func TestAuthorizeUnknownScopeExits2(t *testing.T) {
	authorizeProject(t)
	code, out, errb := dispatchArgs(t, "authorize", "--scope", "everything")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb, "everything") {
		t.Errorf("stderr does not name the bad scope: %q", errb)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
}

// The read scope authorizes no change at all, so it must work where a
// contract does not exist yet — that is exactly the state an operator is
// in when they first reach for it.
func TestAuthorizeReadScopeWorksWithoutAContract(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if code, _, errb := dispatchArgs(t, "authorize", "--scope", "read"); code != 0 {
		t.Fatalf("exit = %d: %s", code, errb)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".project", "state", "envelope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "fs_write") || strings.Contains(string(data), "exec") {
		t.Errorf("read scope granted something mutating:\n%s", data)
	}

	// The project scope in the same directory has no gates to derive
	// exec grants from, but its write grant does not depend on a
	// contract: preview_plan needs fs_write in exactly this state, so
	// the remediation `pika doctor` and `pika explain envelope_denied`
	// both print has to work here.
	code, _, errb := dispatchArgs(t, "authorize", "--scope", "project", "--force")
	if code != 0 {
		t.Fatalf("project scope without a contract exit = %d, want 0 (stderr %q)", code, errb)
	}
	if !strings.Contains(errb, "no contract") || !strings.Contains(errb, "exec") {
		t.Errorf("stderr = %q, want a warning that no exec grants were derived", errb)
	}
	data, err = os.ReadFile(filepath.Join(dir, ".project", "state", "envelope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fs_write") {
		t.Errorf("project scope granted no writes:\n%s", data)
	}
	if strings.Contains(string(data), "exec") {
		t.Errorf("project scope invented exec grants with no contract to derive them from:\n%s", data)
	}
}
