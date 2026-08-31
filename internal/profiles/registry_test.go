package profiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// wantCoreDigest recomputes the core pack's digest from first
// principles — the pack YAML, then each template name, a NUL, and the
// template's bytes — rather than calling packDigest. A test that asserts
// a digest by invoking the code that produced it asserts nothing.
func wantCoreDigest(t *testing.T) string {
	t.Helper()
	h := sha256.New()
	h.Write(corePackYAML)
	for _, name := range coreTemplateNames {
		b, err := fs.ReadFile(coreTemplates, name)
		if err != nil {
			t.Fatalf("core pack is missing template %s: %v", name, err)
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestCoreProfileResolve(t *testing.T) {
	r, err := Resolve([]string{"core@1"})
	if err != nil {
		t.Fatalf("Resolve([core@1]): %v", err)
	}
	if len(r.Layers) != 1 {
		t.Fatalf("got %d layers, want 1", len(r.Layers))
	}
	layer := r.Layers[0]
	if layer.Name != "core" || layer.Version != "1" || layer.Source != "embedded" {
		t.Errorf("layer = %s@%s (source %q), want core@1 (embedded)", layer.Name, layer.Version, layer.Source)
	}

	if got := layer.Pack.Layout.ContractPath; got != ".project/contract.yaml" {
		t.Errorf("contract path = %q, want .project/contract.yaml", got)
	}
	if got := layer.Pack.Layout.StateDir; got != ".project/state/" {
		t.Errorf("state dir = %q, want .project/state/", got)
	}
	for _, dir := range []string{
		"docs/architecture", "docs/decisions", "docs/guides", "docs/reference", "docs/work",
	} {
		if !slices.Contains(layer.Pack.Layout.DocsSpine, dir) {
			t.Errorf("docs spine missing %q (got %v)", dir, layer.Pack.Layout.DocsSpine)
		}
	}

	for _, f := range []string{
		"README.md", "AGENTS.md", "CONTRIBUTING.md", ".github/workflows/", ".github/pull_request_template.md",
	} {
		if !slices.Contains(layer.Pack.Files.Required, f) {
			t.Errorf("required files missing %q (got %v)", f, layer.Pack.Files.Required)
		}
	}

	// Every check slot must be a discovery sentinel: core adds no commands;
	// stack layers supply the real commands in a later task.
	slots := map[string]Check{
		"format":    r.Checks.Format,
		"lint":      r.Checks.Lint,
		"typecheck": r.Checks.Typecheck,
		"test":      r.Checks.Test,
		"smoke":     r.Checks.Smoke,
	}
	for slot, c := range slots {
		if c.ID != slot {
			t.Errorf("check slot %s carries ID %q", slot, c.ID)
		}
		if !c.Discovery {
			t.Errorf("check slot %s: want discovery sentinel, got cmd %v", slot, c.Cmd)
		}
		if len(c.Cmd) != 0 {
			t.Errorf("check slot %s: discovery sentinel must not carry a command, got %v", slot, c.Cmd)
		}
	}

	var kebab, catchAll, fileSize, generatedOwner *NamingRule
	for i := range r.NamingRules {
		switch r.NamingRules[i].RuleID {
		case "naming-kebab-case":
			kebab = &r.NamingRules[i]
		case "naming-catch-all":
			catchAll = &r.NamingRules[i]
		case "file-size-review":
			fileSize = &r.NamingRules[i]
		case "generated-owner":
			generatedOwner = &r.NamingRules[i]
		}
	}
	if kebab == nil {
		t.Fatalf("naming rules %v missing naming-kebab-case", r.NamingRules)
	}
	if catchAll == nil {
		t.Fatalf("naming rules %v missing naming-catch-all", r.NamingRules)
	}
	if fileSize == nil {
		t.Fatalf("naming rules %v missing file-size-review", r.NamingRules)
	}
	if generatedOwner == nil {
		t.Fatalf("naming rules %v missing generated-owner", r.NamingRules)
	}
	if kebab.Pattern == "" {
		t.Errorf("naming-kebab-case: empty pattern")
	}
	// The kebab rule must exempt the ecosystem-conventional stems:
	for _, stem := range []string{"README", "AGENTS", "CONTRIBUTING", "Makefile", "LICENSE", "Dockerfile", "Cargo", "Package", "Sources", "Tests", "__init__"} {
		if !slices.Contains(kebab.Exempt, stem) {
			t.Errorf("naming-kebab-case exempt-stems missing %q (got %v)", stem, kebab.Exempt)
		}
	}
	// Size thresholds and style drift are review signals, not hard
	// failures (spec §6.2).
	if kebab.Severity != "warning" {
		t.Errorf("naming-kebab-case severity = %q, want warning", kebab.Severity)
	}
	if catchAll.Severity != "error" {
		t.Errorf("naming-catch-all severity = %q, want error", catchAll.Severity)
	}
	for _, banned := range []string{"utils", "helpers", "common", "misc", "manager"} {
		if !slices.Contains(catchAll.Banned, banned) {
			t.Errorf("naming-catch-all banned = %v, missing %q", catchAll.Banned, banned)
		}
	}
	if fileSize.Severity != "warning" || fileSize.Scope != "file-lines" || fileSize.Pattern == "" {
		t.Errorf("file-size-review = %+v, want a warning file-lines rule with a threshold", fileSize)
	}
	if generatedOwner.Severity != "error" || generatedOwner.Scope != "generated-patterns" {
		t.Errorf("generated-owner = %+v, want an error generated-patterns rule", generatedOwner)
	}
	if len(generatedOwner.Pattern) != 0 && len(generatedOwner.Banned) != 0 {
		t.Errorf("generated-owner must stay patternless in M1 (no generated files): %+v", generatedOwner)
	}

	if len(r.DocTriggers) == 0 {
		t.Errorf("doc triggers: want at least one, got none")
	}

	if !layer.Pack.Conventions.PullRequests.DraftUntilChecksPass {
		t.Errorf("pull requests: want draft-until-checks-pass")
	}
	if got := layer.Pack.Conventions.PullRequests.MergeStrategy; got != "squash" {
		t.Errorf("pull request merge strategy = %q, want squash", got)
	}
	for _, bt := range []string{"feat", "fix", "docs", "refactor", "chore"} {
		if !slices.Contains(layer.Pack.Conventions.Branches.Types, bt) {
			t.Errorf("branch types %v missing %q", layer.Pack.Conventions.Branches.Types, bt)
		}
	}
}

func TestWriteLockDigest(t *testing.T) {
	first := filepath.Join(t.TempDir(), "profiles.lock")
	if err := WriteLock(first, []string{CoreRef}); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	raw, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Digest string              `json:"digest"`
		Packs  map[string]LockPack `json:"packs"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatalf("profiles.lock is not valid JSON: %v", err)
	}
	if lock.Packs["core"].Version != "1" {
		t.Errorf("core version = %q, want 1", lock.Packs["core"].Version)
	}
	if lock.Packs["core"].Source != "embedded" {
		t.Errorf("source = %q, want embedded", lock.Packs["core"].Source)
	}
	if got := lock.Packs["core"].Digest; got != wantCoreDigest(t) {
		t.Errorf("core digest = %q, want sha256 over the core pack YAML and its templates", got)
	}
	// The templates are genuinely inside the digest: the pack YAML alone
	// no longer produces it. Before M3 it did, which is why correcting
	// the CI template rotated nothing and left every adopted repository
	// with an `@latest` workflow and no way to find out.
	yamlOnly := sha256.Sum256(corePackYAML)
	if lock.Packs["core"].Digest == hex.EncodeToString(yamlOnly[:]) {
		t.Error("core digest is the pack YAML alone: the pack's templates are outside the digest")
	}
	if lock.Digest != PackDigest() {
		t.Errorf("digest = %q, want canonical PackDigest", lock.Digest)
	}
	if len(lock.Packs) != 1 {
		t.Errorf("single-pack lock has %d entries, want 1", len(lock.Packs))
	}

	second := filepath.Join(t.TempDir(), "profiles.lock")
	if err := WriteLock(second, []string{CoreRef}); err != nil {
		t.Fatalf("WriteLock (second): %v", err)
	}
	raw2, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw2) != string(raw) {
		t.Errorf("lock output not stable across calls:\nfirst:  %s\nsecond: %s", raw, raw2)
	}
}

// TestWriteLockTwoPacks asserts the two-pack lock: per-pack entries with
// each pack's own digest, in a byte-stable document.
func TestWriteLockTwoPacks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.lock")
	if err := WriteLock(path, []string{CoreRef, GoRef}); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Digest string              `json:"digest"`
		Packs  map[string]LockPack `json:"packs"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatalf("profiles.lock is not valid JSON: %v", err)
	}
	if lock.Packs["core"].Version != "1" || lock.Packs["go"].Version != "1" {
		t.Errorf("pack versions = %v, want core and go at version 1", lock.Packs)
	}
	sumGo := sha256.Sum256(goPackYAML)
	if got := lock.Packs["core"].Digest; got != wantCoreDigest(t) {
		t.Errorf("core digest = %q, want sha256 over the core pack YAML and its templates", got)
	}
	// The go pack ships no templates, so its digest is its YAML alone.
	if lock.Packs["go"].Digest != hex.EncodeToString(sumGo[:]) {
		t.Errorf("go digest = %q, want sha256 of go pack bytes", lock.Packs["go"].Digest)
	}
	if lock.Digest != PackDigest() {
		t.Errorf("digest = %q, want canonical PackDigest", lock.Digest)
	}

	second := filepath.Join(t.TempDir(), "profiles.lock")
	if err := WriteLock(second, []string{CoreRef, GoRef}); err != nil {
		t.Fatalf("WriteLock (second): %v", err)
	}
	raw2, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw2) != string(raw) {
		t.Errorf("two-pack lock output not stable across calls")
	}
}

// TestWriteLockRejectsUnknownRef asserts the lock only pins registered
// packs.
func TestWriteLockRejectsUnknownPack(t *testing.T) {
	if err := WriteLock(filepath.Join(t.TempDir(), "profiles.lock"), []string{CoreRef, "bogus@9"}); err == nil {
		t.Error("WriteLock with unknown pack: want error, got none")
	}
}

func TestResolveRejectsUnknownProfiles(t *testing.T) {
	for _, sel := range [][]string{
		{},
		{"core"},
		{"core@2"},
		{"lang-go@1"},
		{"core@1", "core@1"},
	} {
		r, err := Resolve(sel)
		if err == nil {
			t.Errorf("Resolve(%v): want error, got %+v", sel, r)
			continue
		}
		if !strings.Contains(err.Error(), "core@1") {
			t.Errorf("Resolve(%v) error %q should list supported pack core@1", sel, err)
		}
	}
}

// autofill is a promise about a hint, so a pack that sets it without one
// — or on a slot that already carries a real command — is stating
// something incoherent and must be rejected loudly rather than silently
// ignored: a swallowed autofill is a check gate that quietly stops being
// populated.
func TestAutofillRequiresAHintedSentinel(t *testing.T) {
	full := func(extra ...checkSpec) *Pack {
		p := &Pack{}
		for _, id := range []string{"format", "lint", "typecheck", "test", "smoke"} {
			p.Verification.Checks = append(p.Verification.Checks, checkSpec{ID: id, Discovery: true})
		}
		for _, e := range extra {
			for i := range p.Verification.Checks {
				if p.Verification.Checks[i].ID == e.ID {
					p.Verification.Checks[i] = e
				}
			}
		}
		return p
	}
	for name, tc := range map[string]struct {
		spec    checkSpec
		wantErr string
	}{
		"autofill without hint": {
			spec:    checkSpec{ID: "lint", Discovery: true, Autofill: true},
			wantErr: "autofill needs a hint",
		},
		"autofill on a real command": {
			spec:    checkSpec{ID: "lint", Cmd: []string{"go", "vet", "./..."}, Autofill: true},
			wantErr: "autofill belongs to a discovery sentinel",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := full(tc.spec).checkSet()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("checkSet() error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}

	// The valid shape still resolves, so the guard rejects incoherence
	// rather than autofill itself.
	cs, err := full(checkSpec{ID: "lint", Discovery: true, Autofill: true, Hint: []string{"go", "vet", "./..."}}).checkSet()
	if err != nil {
		t.Fatalf("hinted autofill sentinel rejected: %v", err)
	}
	if !cs.Lint.Autofill {
		t.Error("resolved lint slot lost its autofill flag")
	}
}

// fail-on-output is a measured claim about a concrete command — this
// tool reports by printing and still exits 0 — so a pack may only make
// it about a command it ships. On a bare sentinel there is nothing the
// claim was measured against, and swallowing the flag there would leave
// a pack author believing a gate can fail when it cannot. The valid
// shapes (sentinel with a hint, and a real command) must survive with
// the flag intact.
func TestFailOnOutputRequiresACommandOrHint(t *testing.T) {
	full := func(extra ...checkSpec) *Pack {
		p := &Pack{}
		for _, id := range []string{"format", "lint", "typecheck", "test", "smoke"} {
			p.Verification.Checks = append(p.Verification.Checks, checkSpec{ID: id, Discovery: true})
		}
		for _, e := range extra {
			for i := range p.Verification.Checks {
				if p.Verification.Checks[i].ID == e.ID {
					p.Verification.Checks[i] = e
				}
			}
		}
		return p
	}

	_, err := full(checkSpec{ID: "format", Discovery: true, FailOnOutput: true}).checkSet()
	if err == nil || !strings.Contains(err.Error(), "fail-on-output needs a cmd or hint") {
		t.Fatalf("checkSet() error = %v, want one rejecting fail-on-output on a bare sentinel", err)
	}

	cs, err := full(checkSpec{ID: "format", Discovery: true, FailOnOutput: true, Hint: []string{"gofmt", "-l", "."}}).checkSet()
	if err != nil {
		t.Fatalf("hinted fail-on-output sentinel rejected: %v", err)
	}
	if !cs.Format.FailOnOutput {
		t.Error("resolved format slot lost its fail-on-output flag")
	}

	cs, err = full(checkSpec{ID: "format", Cmd: []string{"gofmt", "-l", "."}, FailOnOutput: true}).checkSet()
	if err != nil {
		t.Fatalf("fail-on-output on a real command rejected: %v", err)
	}
	if !cs.Format.FailOnOutput {
		t.Error("resolved format slot lost its fail-on-output flag on the cmd path")
	}
}

// --- templates inside the pack digest ---
//
// A pack's templates are baked in by go:embed, so no test can edit one on
// disk and watch the digest move. This drives the real seam instead: the
// registry's core entry is pointed at an in-memory mirror of the shipped
// templates — which must reproduce the shipped digest exactly, or the
// mirror is not a mirror and the rest proves nothing — and then at the
// same mirror with one byte changed. Same helper, same call path through
// PackDigestFor and PackDigest, one edited input.

// useCoreTemplates points the registered core pack at fsys for the rest
// of the test and restores the shipped templates afterwards.
func useCoreTemplates(t *testing.T, fsys fs.FS) {
	t.Helper()
	original := embeddedPacks[CoreRef]
	t.Cleanup(func() { embeddedPacks[CoreRef] = original })
	entry := original
	entry.templates = fsys
	embeddedPacks[CoreRef] = entry
}

// mirrorCoreTemplates copies the shipped templates into an in-memory
// filesystem the test can edit.
func mirrorCoreTemplates(t *testing.T) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	for _, name := range coreTemplateNames {
		b, err := fs.ReadFile(coreTemplates, name)
		if err != nil {
			t.Fatalf("core pack is missing template %s: %v", name, err)
		}
		out[name] = &fstest.MapFile{Data: b}
	}
	return out
}

func TestEditingATemplateRotatesThePackDigest(t *testing.T) {
	before, ok := PackDigestFor(CoreRef)
	if !ok {
		t.Fatal("core pack is not registered")
	}
	registryBefore := PackDigest()

	mirror := mirrorCoreTemplates(t)
	useCoreTemplates(t, mirror)
	if got, _ := PackDigestFor(CoreRef); got != before {
		t.Fatalf("mirror digest = %q, want the shipped %q: the in-memory copy is not byte-identical, so an edit to it would prove nothing", got, before)
	}

	edited := mirrorCoreTemplates(t)
	edited["ci.yml.tmpl"] = &fstest.MapFile{Data: append(slices.Clone(mirror["ci.yml.tmpl"].Data), '\n')}
	useCoreTemplates(t, edited)

	if got, _ := PackDigestFor(CoreRef); got == before {
		t.Errorf("editing ci.yml.tmpl left the core pack digest at %q: templates are outside PackDigestFor", got)
	}
	if got := PackDigest(); got == registryBefore {
		t.Errorf("editing ci.yml.tmpl left the registry digest at %q: templates are outside PackDigest", got)
	}
}

// The registry digest is what makes two pika builds distinguishable to a
// repository, so a build carrying a different set of packs must not
// produce the digest of one that carries fewer. Editing a pack is
// covered above; this is the other way the set moves between
// milestones, and it is the case a stale binary is in — it hashes the
// registry it has, which is not the registry the lock was written from.
func TestADifferentPackSetProducesADifferentRegistryDigest(t *testing.T) {
	before := PackDigest()

	shipped := embeddedPacks
	t.Cleanup(func() { embeddedPacks = shipped })
	extended := make(map[string]packEntry, len(shipped)+1)
	maps.Copy(extended, shipped)
	extended["fixture@1"] = packEntry{name: "fixture", version: "1", data: []byte("id: fixture@1\n")}
	embeddedPacks = extended

	after := PackDigest()
	if after == before {
		t.Fatalf("registering a pack left the registry digest at %s: two builds with different packs would be indistinguishable to every lock", after)
	}

	// And the digest is a function of the set, not of having been
	// called twice: restoring the shipped registry restores the value.
	embeddedPacks = shipped
	if got := PackDigest(); got != before {
		t.Fatalf("registry digest = %s after restoring the shipped packs, want %s", got, before)
	}
}

// reverseDirFS is a filesystem whose ReadDir hands entries back in
// reverse order. fs.ReadDir sorts only for a filesystem that does not
// implement ReadDirFS, so this genuinely reaches fs.WalkDir out of
// order — which is the point: it is the enumeration a future refactor
// could introduce without meaning to, and the digest must not notice.
type reverseDirFS struct{ fs.FS }

func (r reverseDirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(r.FS, name)
	if err != nil {
		return nil, err
	}
	slices.Reverse(entries)
	return entries, nil
}

// The digest hashes template paths in explicitly sorted order, so a
// filesystem that enumerates them backwards must hash to the same bytes.
// A digest that moved here would rotate without its input changing, and
// every adopted repository would fail gate 1 with no diff to point at.
func TestTemplateHashingIsOrderStable(t *testing.T) {
	sorted := sha256.New()
	if err := hashTemplates(sorted, coreTemplates); err != nil {
		t.Fatalf("hash shipped templates: %v", err)
	}
	reversed := sha256.New()
	if err := hashTemplates(reversed, reverseDirFS{coreTemplates}); err != nil {
		t.Fatalf("hash reverse-enumerated templates: %v", err)
	}
	if !bytes.Equal(sorted.Sum(nil), reversed.Sum(nil)) {
		t.Error("template hashing depends on the filesystem's enumeration order")
	}

	// And the agreement is not vacuous: the same helper over different
	// bytes has to disagree, or the test above would pass on a helper
	// that hashed nothing at all.
	changed := sha256.New()
	if err := hashTemplates(changed, fstest.MapFS{"ci.yml.tmpl": &fstest.MapFile{Data: []byte("x")}}); err != nil {
		t.Fatalf("hash substitute templates: %v", err)
	}
	if bytes.Equal(sorted.Sum(nil), changed.Sum(nil)) {
		t.Error("template hashing produced the same digest for a different template set")
	}
}

// agent-guidance was parsed on every pack and surfaced on none, so no
// consumer could ever read it. It is composed onto Resolved in layer
// order, kept with the ref that supplied it, because a projection has to
// name the pack whose advice it carries.
func TestResolveSurfacesAgentGuidancePerPack(t *testing.T) {
	core, err := Resolve([]string{CoreRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(core.AgentGuidance) != 0 {
		t.Errorf("core@1 contributes guidance it does not declare: %+v", core.AgentGuidance)
	}

	withGo, err := Resolve([]string{CoreRef, GoRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(withGo.AgentGuidance) != 1 {
		t.Fatalf("AgentGuidance = %+v, want exactly go@1's", withGo.AgentGuidance)
	}
	set := withGo.AgentGuidance[0]
	if set.Ref != GoRef {
		t.Errorf("guidance ref = %q, want %q", set.Ref, GoRef)
	}
	if len(set.Lines) == 0 {
		t.Fatal("go@1 declares no agent guidance; the worked example is empty")
	}
	// The advice must be about the gates this pack actually declares,
	// or it is decoration rather than guidance.
	joined := strings.Join(set.Lines, "\n")
	for _, want := range []string{"gofmt -l .", "-o /dev/null", "go test ./..."} {
		if !strings.Contains(joined, want) {
			t.Errorf("go@1 guidance never mentions %q:\n%s", want, joined)
		}
	}
}

// A pack that declares no guidance must contribute no entry at all, not
// an empty one: a projection cites every guidance source it composed, and
// citing a pack that said nothing would report drift the day that pack
// changed for an unrelated reason.
func TestPacksWithoutGuidanceContributeNoEntry(t *testing.T) {
	for _, ref := range SupportedRefs() {
		selected := []string{CoreRef}
		if ref != CoreRef {
			selected = append(selected, ref)
		}
		resolved, err := Resolve(selected)
		if err != nil {
			t.Fatalf("resolve %s: %v", ref, err)
		}
		for _, set := range resolved.AgentGuidance {
			if len(set.Lines) == 0 {
				t.Errorf("%s contributed an empty guidance entry", set.Ref)
			}
		}
	}
}
