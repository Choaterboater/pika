package e2e

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// copyFixtureTree clones a read-only discover fixture into dst.
func copyFixtureTree(t *testing.T, fixture, dst string) {
	t.Helper()
	root := filepath.Join("..", "discover", "testdata", fixture)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture %s: %v", fixture, err)
	}
}

// treePaths returns the slash-separated relative paths of every file
// under root.
func treePaths(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// TestE2EAdoptPreview runs `pika adopt` on a real fixture
// repository: the JSON report inventories the go stack against core@1,
// and the only writes are the two .draft proposal files — adopt is
// read-only otherwise (spec §13).
func TestE2EAdoptPreview(t *testing.T) {
	dir := t.TempDir()
	copyFixtureTree(t, "go-mod", dir)
	before := treePaths(t, dir)

	out := runCLI(t, dir, 0, "adopt", "--json")
	env := unwrap(t, out, "adopt")
	if !env.OK {
		t.Errorf("adopt --json reported ok = false on a clean preview:\n%s", out)
	}
	var rep struct {
		DetectedProfiles []string `json:"detectedProfiles"`
		Exceptions       []any    `json:"exceptions"`
		Conflicts        []any    `json:"conflicts"`
	}
	if err := json.Unmarshal(env.Result, &rep); err != nil {
		t.Fatalf("adopt --json result is not the adoption report: %v\n%s", err, out)
	}
	if !slices.Equal(rep.DetectedProfiles, []string{"core@1", "go@1"}) {
		t.Errorf("detectedProfiles = %v, want [core@1 go@1]", rep.DetectedProfiles)
	}

	after := treePaths(t, dir)
	var added []string
	for _, p := range after {
		if !slices.Contains(before, p) {
			added = append(added, p)
		}
	}
	slices.Sort(added)
	want := []string{".project/contract.yaml.draft", ".project/profiles.lock.draft", "review/adoption-review.md"}
	if !slices.Equal(added, want) {
		t.Errorf("adopt wrote %v, want exactly %v", added, want)
	}

	// Human-readable mode: a deterministic summary, no JSON.
	human := runCLI(t, dir, 0, "adopt")
	for _, want := range []string{"core@1, go@1", "proposed exceptions", "drafts written"} {
		if !strings.Contains(human, want) {
			t.Errorf("human report missing %q:\n%s", want, human)
		}
	}

	// Usage errors exit 2.
	runCLI(t, dir, 2, "adopt", "junk")
}

// TestE2EPreviewPlanExecGrantLoop walks the whole remediation path on an
// unadopted repository with a check command of its own, against the real
// binary: `pika authorize --scope project` before any contract exists,
// preview_plan denied because the preview would spawn `make test`, the
// exact invocation the denial names, and preview_plan then succeeding.
// Each step used to be a dead end — authorize exited 2 with no contract,
// preview_plan spawned the command with no exec grant at all, and there
// was no flag with which to grant it — so the loop is the assertion.
func TestE2EPreviewPlanExecGrantLoop(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not in PATH: the discovered baseline command really is spawned here")
	}
	dir := t.TempDir()
	copyFixtureTree(t, "go-mod", dir)
	// A root Makefile with a test target is what discovery turns into
	// ExistingChecks{"test": "make test"} — the legacy-repo shape adopt
	// exists for.
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test:\n\t@echo baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Step 1: authorize before adoption. It must succeed (fs_write does
	// not depend on a contract) and say what it could not derive.
	out := runCLI(t, dir, 0, "authorize", "--scope", "project", "--json")
	env := unwrap(t, out, "authorize")
	if !env.OK {
		t.Fatalf("authorize before adoption reported ok = false:\n%s", out)
	}
	var res struct {
		Warnings []string `json:"warnings"`
		Envelope struct {
			Allow struct {
				FSWrite []string `json:"fs_write"`
				Exec    []string `json:"exec"`
			} `json:"allow"`
		} `json:"envelope"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatalf("authorize --json result: %v\n%s", err, out)
	}
	if len(res.Envelope.Allow.FSWrite) == 0 {
		t.Fatalf("authorize granted no writes before adoption:\n%s", out)
	}
	if len(res.Envelope.Allow.Exec) != 0 {
		t.Fatalf("authorize invented exec grants with no contract: %v", res.Envelope.Allow.Exec)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "exec") {
		t.Fatalf("warnings = %v, want one explaining the missing exec grants", res.Warnings)
	}

	// Step 2: preview_plan is denied, and the denial names the exact
	// invocation that grants what it needs.
	s := startMCP(t, dir)
	s.request("initialize", map[string]any{"protocolVersion": "2024-11-05"})
	if _, errObj := s.call("preview_plan", map[string]any{}); errObj == nil {
		t.Fatal("preview_plan spawned the repository's own command with no exec grant")
	} else {
		if code := toolErrorCode(t, errObj); code != "envelope_denied" {
			t.Fatalf("preview_plan error code = %q, want envelope_denied", code)
		}
		msg, _ := errObj["message"].(string)
		if !strings.Contains(msg, `pika authorize --exec "make test"`) {
			t.Fatalf("denial message = %q, want the exact invocation that grants it", msg)
		}
	}
	for _, draft := range []string{".project/contract.yaml.draft", ".project/profiles.lock.draft"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(draft))); !os.IsNotExist(err) {
			t.Fatalf("a denied preview_plan wrote %s (stat err %v)", draft, err)
		}
	}
	s.stdin.Close()
	if err := s.cmd.Wait(); err != nil {
		t.Fatalf("mcp server did not exit cleanly on EOF: %v\nstderr: %s", err, s.stderr.String())
	}

	// Step 3: follow the remediation literally.
	runCLI(t, dir, 0, "authorize", "--scope", "project", "--exec", "make test", "--force")

	// Step 4: the same call now runs, and the baseline really ran the
	// granted command.
	s2 := startMCP(t, dir)
	s2.request("initialize", map[string]any{"protocolVersion": "2024-11-05"})
	result, errObj := s2.call("preview_plan", map[string]any{})
	if errObj != nil {
		t.Fatalf("preview_plan after the exec grant: %v", errObj)
	}
	data, _ := result["data"].(map[string]any)
	baseline, ok := data["baselineChecks"].([]any)
	if !ok || len(baseline) != 1 {
		t.Fatalf("baselineChecks = %v, want the one discovered command", data["baselineChecks"])
	}
	entry := baseline[0].(map[string]any)
	if entry["command"] != "make test" || entry["status"] != "pass" {
		t.Fatalf("baseline = %v, want make test to have run and passed", entry)
	}
	for _, draft := range []string{".project/contract.yaml.draft", ".project/profiles.lock.draft"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(draft))); err != nil {
			t.Fatalf("preview_plan did not write %s: %v", draft, err)
		}
	}
	s2.stdin.Close()
	if err := s2.cmd.Wait(); err != nil {
		t.Fatalf("mcp server did not exit cleanly on EOF: %v\nstderr: %s", err, s2.stderr.String())
	}
}
