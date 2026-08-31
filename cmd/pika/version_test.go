package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/version"
)

// The defect this command exists for: two pika binaries carrying
// different packs both printed "0.1.0", so the operator holding a
// repository that one accepted and the other rejected had no way to tell
// them apart. The release alone identifies nothing; the pack registry
// digest is what a profiles.lock is verified against.
func TestVersionPrintsTheEmbeddedRegistryDigest(t *testing.T) {
	code, out, errb := dispatchArgs(t, "version")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, errb)
	}
	if !strings.Contains(out, version.Version) {
		t.Errorf("output %q does not carry the release %s", out, version.Version)
	}
	if !strings.Contains(out, profiles.PackDigest()) {
		t.Errorf("output %q does not carry the embedded pack registry digest %s: two builds with different packs would print the same thing", out, profiles.PackDigest())
	}
}

func TestVersionFlagSpellingsAgree(t *testing.T) {
	_, plain, _ := dispatchArgs(t, "version")
	for _, spelling := range []string{"--version", "-version"} {
		code, out, errb := dispatchArgs(t, spelling)
		if code != 0 {
			t.Fatalf("%s exit = %d, want 0; stderr: %s", spelling, code, errb)
		}
		if out != plain {
			t.Errorf("%s printed\n%s\nwant the same identity as `pika version`:\n%s", spelling, out, plain)
		}
	}
}

func TestVersionJSONCarriesTheSameFields(t *testing.T) {
	var out, errb bytes.Buffer
	code := runVersion([]string{"--json", "--root", t.TempDir()}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, errb.String())
	}
	var got versionResult
	resultOf(t, out.Bytes(), "version", &got)
	if got.Version != version.Version {
		t.Errorf("version = %q, want %q", got.Version, version.Version)
	}
	if got.RegistryDigest != profiles.PackDigest() {
		t.Errorf("registry_digest = %q, want %q", got.RegistryDigest, profiles.PackDigest())
	}
	if got.MaxContractSchema != version.MaxContractSchema {
		t.Errorf("max_contract_schema = %d, want %d", got.MaxContractSchema, version.MaxContractSchema)
	}
	if got.Lock != nil {
		t.Errorf("lock = %+v on a directory that is not a project, want none", got.Lock)
	}
}

// Attribution is the whole point: pointed at a repository, version says
// whether this binary is the one whose packs that lock was written from.
// That is the comparison the lock-mismatch message sends an operator to
// make, so it has to be answerable without running a gate.
func TestVersionReportsWhetherThisBinaryWroteTheLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".project", "profiles.lock")
	if err := profiles.WriteLock(lockPath, []string{"core@1"}); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}

	var out, errb bytes.Buffer
	if code := runVersion([]string{"--json", "--root", dir}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, errb.String())
	}
	var got versionResult
	resultOf(t, out.Bytes(), "version", &got)
	if got.Lock == nil {
		t.Fatal("no lock reported for a repository that has one")
	}
	if !got.Lock.Matches || got.Lock.RegistryDigest != profiles.PackDigest() {
		t.Fatalf("lock = %+v, want the registry digest %s and matches=true: this binary wrote that lock", got.Lock, profiles.PackDigest())
	}

	// Now the lock of a repository some other build wrote.
	foreign := strings.Repeat("a", 64)
	rewriteLockDigest(t, lockPath, foreign)
	out.Reset()
	errb.Reset()
	if code := runVersion([]string{"--json", "--root", dir}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, errb.String())
	}
	got = versionResult{}
	resultOf(t, out.Bytes(), "version", &got)
	if got.Lock == nil || got.Lock.RegistryDigest != foreign || got.Lock.Matches {
		t.Fatalf("lock = %+v, want digest %s and matches=false", got.Lock, foreign)
	}
}

// A repository state version cannot read is not version's failure: the
// command answers a question about the binary, and the binary can be
// identified from anywhere.
func TestVersionSurvivesAnUnreadableLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".project", "profiles.lock"), []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errb := dispatchArgs(t, "version", "--root", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, errb)
	}
	if !strings.Contains(out, profiles.PackDigest()) {
		t.Errorf("output %q lost the binary identity over a repository it could not read", out)
	}
}

// rewriteLockDigest replaces the lock's top-level registry digest,
// standing in for a lock written by a build carrying other packs.
func rewriteLockDigest(t *testing.T, path, digest string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m["digest"] = digest
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVersionRejectsAPositionalArgument(t *testing.T) {
	code, _, errb := dispatchArgs(t, "version", "0.5.0")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb, "0.5.0") {
		t.Errorf("stderr %q does not name the unexpected argument", errb)
	}
}
