package authorize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/envelope"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/verify"
)

// writeGoProject lays down the smallest repository authorize accepts: a
// contract selecting core@1 + go@1 with an empty commands map, plus the
// matching lock from profiles.WriteLock (the only writer producing
// digests this binary's embedded registry agrees with).
func writeGoProject(t *testing.T, dir string) {
	t.Helper()
	project := filepath.Join(dir, ".project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `schema: 1
project:
  name: fixture
  topology: single
profiles: [core@1, go@1]
github:
  merge: squash
evidence:
  publish: sanitized
commands: {}
`
	if err := os.WriteFile(filepath.Join(project, "contract.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := profiles.WriteLock(filepath.Join(project, "profiles.lock"), []string{"core@1", "go@1"}); err != nil {
		t.Fatal(err)
	}
}

func projectRoot(t *testing.T) *repopath.Root {
	t.Helper()
	dir := t.TempDir()
	writeGoProject(t, dir)
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadScopeGrantsNothingMutating(t *testing.T) {
	env, err := Build(Options{Root: projectRoot(t), Scope: ScopeRead})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(env.Allow.FSWrite) != 0 {
		t.Errorf("read scope granted fs_write: %v", env.Allow.FSWrite)
	}
	if len(env.Allow.Exec) != 0 {
		t.Errorf("read scope granted exec: %v", env.Allow.Exec)
	}
}

func TestProjectScopeGrantsProjectPathsOnly(t *testing.T) {
	env, err := Build(Options{Root: projectRoot(t), Scope: ScopeProject})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := map[string]bool{".project": true, "docs": true, "review": true}
	if len(env.Allow.FSWrite) != len(want) {
		t.Fatalf("fs_write = %v, want exactly %v", env.Allow.FSWrite, want)
	}
	for _, p := range env.Allow.FSWrite {
		if !want[p] {
			t.Errorf("unexpected fs_write grant %q", p)
		}
	}
}

// This pins what authorize EMITS for the repo scope, not what that
// emission currently authorizes. envelope.matchesPath
// (internal/envelope/envelope.go:303) matches only `norm == entry` or
// `strings.HasPrefix(norm, entry+"/")`, and contract.NormalizeRepoPath
// never returns a "./" prefix, so the entry "." presently matches the
// literal path "." and nothing beneath it. That is a known open defect
// in envelope enforcement, owned by the task that owns enforcement.
// Asserting here that the repo scope authorizes "main.go" would hand
// that task a green test over behavior that does not exist yet, so this
// test deliberately stops at the emitted grant.
func TestRepoScopeGrantsRepoRoot(t *testing.T) {
	env, err := Build(Options{Root: projectRoot(t), Scope: ScopeRepo})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(env.Allow.FSWrite) != 1 || env.Allow.FSWrite[0] != "." {
		t.Fatalf("fs_write = %v, want [\".\"]", env.Allow.FSWrite)
	}
}

// The round trip that decides whether this command is real: an envelope
// generated from a contract must authorize, through the kernel's own
// Envelope.Allows, every gate that same contract resolves to. Granting
// argv[0] alone ("go") passes a naive membership assertion and denies
// "go test ./..." at runtime — authorize and enforcement have to agree
// or the command is theater.
func TestGeneratedEnvelopeAuthorizesTheGatesItWasDerivedFrom(t *testing.T) {
	root := projectRoot(t)
	env, err := Build(Options{Root: root, Scope: ScopeProject})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	bound := envelope.NewEnvelope(env, root.Dir())

	c, err := contract.Load(root.Contract())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := profiles.Resolve(c.Profiles)
	if err != nil {
		t.Fatal(err)
	}
	gates, err := verify.FromProfiles(resolved.Checks, c.Commands)
	if err != nil {
		t.Fatal(err)
	}

	ran := 0
	for _, g := range gates {
		if len(g.Cmd) == 0 {
			continue
		}
		ran++
		target := strings.Join(g.Cmd, " ")
		if !bound.Allows(envelope.Operation{Kind: envelope.KindExec, Target: target}) {
			t.Errorf("generated envelope denies its own gate %q (exec = %v)", target, env.Allow.Exec)
		}
	}
	if ran == 0 {
		t.Fatal("fixture resolved no command gates, so the round trip proved nothing")
	}
	// The go@1 pack's test slot is the command gate the fixture has.
	if !bound.Allows(envelope.Operation{Kind: envelope.KindExec, Target: "go test ./..."}) {
		t.Errorf("exec = %v, want it to authorize the go@1 test gate", env.Allow.Exec)
	}
	// And it authorizes nothing beyond them.
	if bound.Allows(envelope.Operation{Kind: envelope.KindExec, Target: "rm -rf /"}) {
		t.Error("generated envelope authorized a command no gate runs")
	}
	for _, e := range env.Allow.Exec {
		if e == "*" {
			t.Fatal("authorize generated a bare * exec grant")
		}
	}
}

// The project scope's fs_write grants must likewise survive enforcement:
// .project is granted so that everything the kernel writes beneath it is
// authorized, and nothing outside is.
func TestProjectScopeAuthorizesKernelWrites(t *testing.T) {
	root := projectRoot(t)
	env, err := Build(Options{Root: root, Scope: ScopeProject})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	bound := envelope.NewEnvelope(env, root.Dir())
	for _, p := range []string{".project/state/board.jsonl", ".project/evidence/x.json", "review/adoption-review.md", "docs/guides/x.md"} {
		if !bound.Allows(envelope.Operation{Kind: envelope.KindFSWrite, Target: p}) {
			t.Errorf("project scope denies %q, which the kernel writes", p)
		}
	}
	for _, p := range []string{"main.go", "internal/x/y.go"} {
		if bound.Allows(envelope.Operation{Kind: envelope.KindFSWrite, Target: p}) {
			t.Errorf("project scope authorized source write %q", p)
		}
	}
}

func TestNetworkCredentialGitHubNeverImplicit(t *testing.T) {
	for _, scope := range []string{ScopeRead, ScopeProject, ScopeRepo} {
		env, err := Build(Options{Root: projectRoot(t), Scope: scope})
		if err != nil {
			t.Fatalf("Build(%s): %v", scope, err)
		}
		if len(env.Allow.Network) != 0 || len(env.Allow.Credential) != 0 || len(env.Allow.GitHub) != 0 {
			t.Errorf("scope %s implicitly granted network/credential/github", scope)
		}
		if len(env.Allow.Budget) != 0 {
			t.Errorf("scope %s wrote a budget ceiling that nothing enforces", scope)
		}
	}
}

// Render has no budget field at all, so no ceiling can reach the file
// even if an Env were built carrying one.
func TestRenderNeverWritesABudget(t *testing.T) {
	env, err := Build(Options{Root: projectRoot(t), Scope: ScopeProject})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	env.Allow.Budget = map[string]float64{"openai": 5}
	data, err := Render(env)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(data), "budget") {
		t.Fatalf("rendered envelope carries a budget nothing enforces:\n%s", data)
	}
}

func TestExplicitGrantsAreHonored(t *testing.T) {
	env, err := Build(Options{
		Root:    projectRoot(t),
		Scope:   ScopeProject,
		Network: []string{"proxy.golang.org"},
		GitHub:  []string{"pull_request:write"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(env.Allow.Network) != 1 || env.Allow.Network[0] != "proxy.golang.org" {
		t.Errorf("network = %v", env.Allow.Network)
	}
	if len(env.Allow.GitHub) != 1 {
		t.Errorf("github = %v", env.Allow.GitHub)
	}
}

// The generated document must survive the kernel's own strict validator.
func TestRenderedEnvelopeValidates(t *testing.T) {
	for _, scope := range []string{ScopeRead, ScopeProject, ScopeRepo} {
		env, err := Build(Options{
			Root:       projectRoot(t),
			Scope:      scope,
			Network:    []string{"proxy.golang.org"},
			Credential: []string{"gh-token"},
			GitHub:     []string{"pull_request:write"},
		})
		if err != nil {
			t.Fatalf("Build(%s): %v", scope, err)
		}
		data, err := Render(env)
		if err != nil {
			t.Fatalf("Render(%s): %v", scope, err)
		}
		got, err := envelope.Validate(data)
		if err != nil {
			t.Fatalf("generated %s envelope failed envelope.Validate: %v\n%s", scope, err, data)
		}
		if got.RollbackBoundary != "repository" {
			t.Errorf("rollback_boundary = %q, want %q", got.RollbackBoundary, "repository")
		}
		if len(got.Allow.Exec) != len(env.Allow.Exec) {
			t.Errorf("exec survived the round trip as %v, want %v", got.Allow.Exec, env.Allow.Exec)
		}
	}
}

func TestUnknownScopeIsAnError(t *testing.T) {
	if _, err := Build(Options{Root: projectRoot(t), Scope: "wide"}); err == nil {
		t.Fatal("Build(unknown scope) = nil error, want error")
	}
}

func TestProjectScopeRequiresAContract(t *testing.T) {
	root, err := repopath.At(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(Options{Root: root, Scope: ScopeProject}); err == nil {
		t.Fatal("Build without a contract = nil error, want error")
	}
	if _, err := Build(Options{Root: root, Scope: ScopeRead}); err != nil {
		t.Fatalf("read scope must work without a contract: %v", err)
	}
}

// A contract command overrides the pack slot, so the exec grant must
// follow the contract rather than the pack — otherwise authorize
// authorizes a command check never runs and denies the one it does.
func TestExecGrantFollowsContractCommands(t *testing.T) {
	dir := t.TempDir()
	writeGoProject(t, dir)
	doc := `schema: 1
project:
  name: fixture
  topology: single
profiles: [core@1, go@1]
github:
  merge: squash
evidence:
  publish: sanitized
commands:
  test: "gotestsum --format testname"
  lint: "golangci-lint run"
`
	if err := os.WriteFile(filepath.Join(dir, ".project", "contract.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := repopath.At(dir)
	if err != nil {
		t.Fatal(err)
	}
	env, err := Build(Options{Root: root, Scope: ScopeProject})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{"golangci-lint run", "gotestsum --format testname"}
	if len(env.Allow.Exec) != len(want) {
		t.Fatalf("exec = %v, want %v", env.Allow.Exec, want)
	}
	for i, w := range want {
		if env.Allow.Exec[i] != w {
			t.Errorf("exec[%d] = %q, want %q", i, env.Allow.Exec[i], w)
		}
	}
}

func TestDiffReportsWhatWouldChange(t *testing.T) {
	root := projectRoot(t)
	next, err := Build(Options{Root: root, Scope: ScopeProject})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := Diff(next, next); len(got) != 0 {
		t.Errorf("Diff(x, x) = %v, want no changes", got)
	}
	old := &envelope.Env{Schema: 1}
	old.Allow.FSWrite = []string{".project", "tmp"}
	old.Allow.Budget = map[string]float64{"openai": 5}
	got := strings.Join(Diff(old, next), "\n")
	for _, want := range []string{"+ fs_write   docs", "+ fs_write   review", "- fs_write   tmp", "+ exec       go test ./...", "- budget     openai"} {
		if !strings.Contains(got, want) {
			t.Errorf("Diff missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "fs_write   .project") {
		t.Errorf("Diff reported an unchanged grant:\n%s", got)
	}
}
