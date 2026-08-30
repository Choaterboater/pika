// Package authorize generates a capability envelope from a declared
// intent. Before it, an operator had to hand-author
// .project/state/envelope.yaml or every mutating MCP tool returned
// envelope_denied — the single largest barrier to handing pika to an
// agent.
//
// The generated document is deliberately narrow. A scope grants writes
// and nothing else; network, credential, and GitHub access are granted
// only by explicit lists; and budget is never written at all, because no
// code in this binary compares spend against a ceiling and a ceiling
// nothing enforces is a lie in a file whose entire job is to be true.
package authorize

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/envelope"
	"github.com/Choaterboater/pika/internal/profiles"
	"github.com/Choaterboater/pika/internal/repopath"
	"github.com/Choaterboater/pika/internal/verify"
)

// Scopes, narrowest first.
const (
	ScopeRead    = "read"
	ScopeProject = "project"
	ScopeRepo    = "repo"
)

// Scopes lists the accepted --scope values in the order they widen.
var Scopes = []string{ScopeRead, ScopeProject, ScopeRepo}

// projectPaths are the directories pika itself owns (design spec §6).
var projectPaths = []string{".project", "docs", "review"}

// Options declares the intent an envelope is generated from.
type Options struct {
	Root       *repopath.Root
	Scope      string
	Network    []string
	Credential []string
	GitHub     []string
}

// errNoContract marks the single gateCommands failure that is not a
// defect in the repository: nothing has been adopted yet, so there are no
// resolved gates to derive exec grants from.
var errNoContract = errors.New("no contract")

// Build produces the envelope for the declared scope, plus any warnings
// about what could not be derived. Nothing beyond the scope's own grants
// and the explicit lists is ever granted: budget is deliberately never
// written, because no code compares spend against it, and a ceiling that
// is never enforced is a lie.
//
// The write grant does not depend on the contract. Exec grants are
// derived from the gates a contract resolves to, but preview_plan — the
// one MCP tool that only ever runs *before* a contract exists — needs
// fs_write in exactly the state where no contract can be loaded. Failing
// the whole build there left the canonical "run pika authorize --scope
// project" remediation with no state in which it worked. A missing
// contract is therefore a warning and an empty Exec list; a contract that
// exists and does not load is still an error, because that is a real
// defect and silently granting writes over it would hide it.
func Build(opts Options) (*envelope.Env, []string, error) {
	if opts.Root == nil {
		return nil, nil, fmt.Errorf("authorize: no repository root")
	}
	env := &envelope.Env{
		Schema:           1,
		RollbackBoundary: "repository",
	}
	switch opts.Scope {
	case ScopeRead:
	case ScopeProject:
		env.Allow.FSWrite = append([]string(nil), projectPaths...)
	case ScopeRepo:
		// "." is the canonical repository-root path
		// contract.NormalizeRepoPath produces, and
		// envelope.matchesPath reads it as the whole repository
		// subtree: this grant authorizes writes anywhere in the repo.
		env.Allow.FSWrite = []string{"."}
	default:
		return nil, nil, fmt.Errorf("authorize: unknown scope %q (want %s, %s, or %s)",
			opts.Scope, ScopeRead, ScopeProject, ScopeRepo)
	}

	// The read scope authorizes no change at all, so it needs no
	// contract: it must work in a repository that was never adopted.
	var warnings []string
	if opts.Scope != ScopeRead {
		execs, err := gateCommands(opts.Root)
		switch {
		case err == nil:
			env.Allow.Exec = execs
		case errors.Is(err, errNoContract):
			warnings = append(warnings, fmt.Sprintf(
				"no contract at %s yet, so no gate commands were derived: this envelope grants no exec. "+
					"Re-run \"pika authorize --force\" after \"pika init\" or \"pika adopt\" to authorize the gates the contract declares.",
				opts.Root.Contract()))
		default:
			return nil, nil, err
		}
	}

	env.Allow.Network = dedupe(opts.Network)
	env.Allow.Credential = dedupe(opts.Credential)
	env.Allow.GitHub = dedupe(opts.GitHub)
	return env, warnings, nil
}

// gateCommands collects the exec grants for the gates the contract will
// actually run. Each grant is the gate's full argv joined by spaces —
// exactly the target the enforcement side asks about — because
// Envelope.Allows matches an exec entry element-wise against the whole
// argv line. Granting only argv[0] ("go") would authorize the bare
// command and deny every gate that has arguments, which is the precise
// failure mode this command exists to remove. It is also the tighter
// grant, which is the right default: "go" would additionally authorize
// "go build -o /anywhere", a command no gate runs.
//
// A contract that is simply absent yields errNoContract, which Build
// downgrades to a warning; every other failure is a real defect in a
// contract that does exist and is returned as an error.
func gateCommands(root *repopath.Root) ([]string, error) {
	c, err := contract.Load(root.Contract())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w at %s", errNoContract, root.Contract())
		}
		return nil, fmt.Errorf("authorize: %w", err)
	}
	resolved, err := profiles.Resolve(c.Profiles)
	if err != nil {
		return nil, fmt.Errorf("authorize: %w", err)
	}
	gates, err := verify.FromProfiles(resolved.Checks, c.Commands)
	if err != nil {
		return nil, fmt.Errorf("authorize: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, g := range gates {
		if len(g.Cmd) == 0 {
			continue
		}
		line := strings.Join(g.Cmd, " ")
		// A bare "*" element would make the entry match every
		// command; envelope.Validate rejects such a document, so
		// refusing to generate one keeps authorize from writing a
		// file its own kernel would reject.
		if containsWildcard(g.Cmd) || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	sort.Strings(out)
	return out, nil
}

func containsWildcard(argv []string) bool {
	for _, a := range argv {
		if a == "*" {
			return true
		}
	}
	return false
}

// renderDoc is the YAML projection of an Env. It exists because
// envelope.Env's own yaml tags carry no omitempty: marshaling it
// directly emits `fs_write: null` for every ungranted class, and
// envelope.Validate rejects a null where it requires a list — the
// generated file would fail the kernel's own validator. It also has no
// budget field, so no ceiling can be written by accident.
type renderDoc struct {
	Schema           int         `yaml:"schema"`
	Allow            renderAllow `yaml:"allow"`
	RollbackBoundary string      `yaml:"rollback_boundary"`
}

type renderAllow struct {
	FSWrite    []string `yaml:"fs_write,omitempty"`
	Exec       []string `yaml:"exec,omitempty"`
	Network    []string `yaml:"network,omitempty"`
	Credential []string `yaml:"credential,omitempty"`
	GitHub     []string `yaml:"github,omitempty"`
}

// Render serializes the envelope to the YAML document written to
// .project/state/envelope.yaml.
func Render(env *envelope.Env) ([]byte, error) {
	if env == nil {
		return nil, fmt.Errorf("authorize: nothing to render")
	}
	data, err := yaml.Marshal(renderDoc{
		Schema: env.Schema,
		Allow: renderAllow{
			FSWrite:    env.Allow.FSWrite,
			Exec:       env.Allow.Exec,
			Network:    env.Allow.Network,
			Credential: env.Allow.Credential,
			GitHub:     env.Allow.GitHub,
		},
		RollbackBoundary: env.RollbackBoundary,
	})
	if err != nil {
		return nil, fmt.Errorf("authorize: %w", err)
	}
	header := "# Generated by \"pika authorize\". Local-only: .project/state/ is gitignored.\n" +
		"# Deny-by-default: anything absent here is refused.\n"
	return append([]byte(header), data...), nil
}

// Diff reports, one line per difference, how replacing old with next
// would change what is authorized. It is what `pika authorize` prints
// when it refuses to overwrite an existing envelope: an operator is
// owed the delta before being asked for --force.
func Diff(old, next *envelope.Env) []string {
	var out []string
	classes := []struct {
		name      string
		old, next []string
	}{
		{"fs_write", allowOf(old).FSWrite, allowOf(next).FSWrite},
		{"exec", allowOf(old).Exec, allowOf(next).Exec},
		{"network", allowOf(old).Network, allowOf(next).Network},
		{"credential", allowOf(old).Credential, allowOf(next).Credential},
		{"github", allowOf(old).GitHub, allowOf(next).GitHub},
	}
	for _, c := range classes {
		for _, v := range missing(c.next, c.old) {
			out = append(out, fmt.Sprintf("+ %-10s %s", c.name, v))
		}
		for _, v := range missing(c.old, c.next) {
			out = append(out, fmt.Sprintf("- %-10s %s", c.name, v))
		}
	}
	for provider := range allowOf(old).Budget {
		out = append(out, fmt.Sprintf("- %-10s %s (authorize never writes a budget)", "budget", provider))
	}
	sort.Strings(out)
	return out
}

func allowOf(env *envelope.Env) envelope.Allow {
	if env == nil {
		return envelope.Allow{}
	}
	return env.Allow
}

// missing returns the entries of want that are absent from have.
func missing(want, have []string) []string {
	if len(want) == 0 {
		return nil
	}
	present := make(map[string]bool, len(have))
	for _, v := range have {
		present[v] = true
	}
	var out []string
	for _, v := range want {
		if !present[v] {
			out = append(out, v)
		}
	}
	return out
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
