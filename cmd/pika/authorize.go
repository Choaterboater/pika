package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/Choaterboater/pika/internal/authorize"
	"github.com/Choaterboater/pika/internal/envelope"
)

// authorizeResult is the --json shape. It reports what was asked for,
// what the envelope grants, whether it landed, and — when it did not —
// the delta the operator refused to accept blindly.
type authorizeResult struct {
	Root     string        `json:"root"`
	Scope    string        `json:"scope"`
	Path     string        `json:"path"`
	Written  bool          `json:"written"`
	Envelope *envelope.Env `json:"envelope"`
	Document string        `json:"document"`
	Changes  []string      `json:"changes,omitempty"`
	Error    string        `json:"error,omitempty"`
}

// runAuthorize implements `pika authorize`: it generates
// .project/state/envelope.yaml from a declared scope instead of making
// the operator hand-author the one file that stands between an agent and
// every mutating tool. `pika doctor` and `pika explain envelope_denied`
// both send people here, so this command is the answer to their advice.
//
// Exit codes: 0 the envelope was written and re-validated, 1 nothing was
// written (an existing envelope without --force) or the written document
// failed to load back, 2 usage or configuration error.
func runAuthorize(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("authorize", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", authorize.ScopeProject,
		"what to authorize: read (nothing mutating), project (.project, docs, review), or repo (the whole tree)")
	var network, credential, github stringList
	fs.Var(&network, "network", "host or host:port to authorize (repeatable; never granted implicitly)")
	fs.Var(&credential, "credential", "credential name to authorize (repeatable; never granted implicitly)")
	fs.Var(&github, "github", "GitHub scope to authorize (repeatable; never granted implicitly)")
	force := fs.Bool("force", false, "replace an existing envelope")
	jsonOut := fs.Bool("json", false, "emit the result as JSON on stdout")
	rootFlag := fs.String("root", "", rootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "pika authorize: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "pika authorize: %v\n", err)
		return 2
	}

	env, err := authorize.Build(authorize.Options{
		Root:       root,
		Scope:      *scope,
		Network:    network,
		Credential: credential,
		GitHub:     github,
	})
	if err != nil {
		fmt.Fprintf(stderr, "pika authorize: %v\n", err)
		return 2
	}
	doc, err := authorize.Render(env)
	if err != nil {
		fmt.Fprintf(stderr, "pika authorize: %v\n", err)
		return 1
	}

	res := authorizeResult{
		Root:     root.Dir(),
		Scope:    *scope,
		Path:     root.Envelope(),
		Envelope: env,
		Document: string(doc),
	}

	if !*jsonOut {
		fmt.Fprint(stdout, string(doc))
	}

	if !*force {
		if changes, refuse := existingEnvelope(root.Dir(), root.Envelope(), env); refuse {
			res.Changes = changes
			res.Error = "an envelope already exists; nothing was written"
			if *jsonOut {
				writeJSON(stdout, res)
			} else {
				fmt.Fprintf(stderr, "\npika authorize: %s already exists; nothing was written\n", res.Path)
				printChanges(changes, stderr)
				fmt.Fprintln(stderr, "re-run with --force to replace it")
			}
			return 1
		}
	}

	if err := os.MkdirAll(root.StateDir(), 0o755); err != nil {
		fmt.Fprintf(stderr, "pika authorize: %v\n", err)
		return 1
	}
	// 0600, not 0644: this file is a capability grant, and every other
	// user on the machine has no business reading what an agent running
	// as this operator is allowed to do. os.WriteFile only applies perm
	// when it *creates* the file, so replacing a hand-authored 0644
	// envelope would leave it world-readable; chmod the result instead
	// of assuming the write settled it.
	if err := os.WriteFile(root.Envelope(), doc, 0o600); err != nil {
		fmt.Fprintf(stderr, "pika authorize: %v\n", err)
		return 1
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(root.Envelope(), 0o600); err != nil {
			fmt.Fprintf(stderr, "pika authorize: %v\n", err)
			return 1
		}
	}
	// Re-read what actually landed and put it through the kernel's own
	// loader. A generated envelope that fails the validator every other
	// command runs would be worse than no envelope at all: the operator
	// would believe they were authorized.
	if _, err := envelope.Load(root.Dir(), root.Envelope()); err != nil {
		fmt.Fprintf(stderr, "pika authorize: wrote %s but it does not validate: %v\n", root.Envelope(), err)
		return 1
	}

	res.Written = true
	if *jsonOut {
		writeJSON(stdout, res)
		return 0
	}
	// Report the mode actually on disk rather than the one we asked for:
	// an unconditional "(mode 0600)" is precisely what kept the overwrite
	// case invisible.
	mode := ""
	if info, err := os.Stat(root.Envelope()); err == nil {
		mode = fmt.Sprintf(" (mode %04o)", info.Mode().Perm())
	}
	fmt.Fprintf(stdout, "\nwrote %s%s, verified by envelope.Load\n", root.Envelope(), mode)
	return 0
}

// existingEnvelope reports the delta against an envelope already on disk
// and whether the write must be refused. A missing file is not a refusal;
// an unreadable or invalid one is, because replacing a document nobody
// can read is exactly the moment to make the operator say --force.
func existingEnvelope(repoRoot, path string, next *envelope.Env) (changes []string, refuse bool) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		return []string{"! the existing envelope could not be read: " + err.Error()}, true
	}
	old, err := envelope.Validate(data)
	if err != nil {
		return []string{"! the existing envelope does not validate: " + err.Error()}, true
	}
	return authorize.Diff(old, next), true
}

func printChanges(changes []string, w io.Writer) {
	if len(changes) == 0 {
		fmt.Fprintln(w, "would change: nothing; the existing envelope already grants exactly this")
		return
	}
	fmt.Fprintln(w, "would change:")
	for _, c := range changes {
		fmt.Fprintln(w, "  "+strings.TrimRight(c, " "))
	}
}
