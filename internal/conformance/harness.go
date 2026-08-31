package conformance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The harness: the binary under test, the cache of pinned checkouts, and
// the two subprocess boundaries a row crosses (pika, and git).
//
// It follows internal/smoke rather than inventing a second way to run
// the product: the module root is resolved rather than assumed, the
// binary is built with CGO_ENABLED=0 the way it ships, and a non-zero
// exit is a result rather than an error.

// ErrNetwork marks a repository the corpus could not obtain.
//
// The distinction it draws is the one thing that makes a red corpus
// worth reading. A fetch happens before pika is invoked at all, so a
// fetch that fails is never evidence about pika — and a run that
// reported "could not reach github.com" as a conformance failure would
// be ignored within a week, which is exactly the situation the corpus
// most needs to be believed in.
var ErrNetwork = errors.New("the corpus could not fetch a pinned repository")

// harness owns everything one corpus run creates outside the cache.
type harness struct {
	// root is the pika module this run builds from.
	root string
	// dir holds the built binary and is removed when the run ends.
	dir string
	// pika is the binary under test. Every claim the corpus makes is
	// about what THIS file does when it is executed.
	pika string
	// cache holds one checkout per pinned commit, so a local re-run
	// pays the network once.
	cache string
}

// newHarness builds the binary under test into a fresh temp directory.
func newHarness() (*harness, error) {
	root, err := moduleRoot()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "pika-conformance-")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	h := &harness{
		root:  root,
		dir:   dir,
		pika:  filepath.Join(dir, exeName("pika")),
		cache: cacheRoot(),
	}
	if err := os.MkdirAll(h.cache, 0o755); err != nil {
		h.close()
		return nil, fmt.Errorf("cache dir %s: %w", h.cache, err)
	}
	build := exec.Command("go", "build", "-o", h.pika, "./cmd/pika")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		h.close()
		return nil, fmt.Errorf("build ./cmd/pika: %w\n%s", err, b)
	}
	return h, nil
}

// close removes the built binary. The cache is deliberately left: it is
// keyed by commit, so it is immutable, and re-cloning five repositories
// per run would make a local re-run cost the network every time.
func (h *harness) close() error {
	if err := os.RemoveAll(h.dir); err != nil {
		return fmt.Errorf("conformance left %s behind: %w", h.dir, err)
	}
	return nil
}

// cacheRoot is where pinned checkouts live: under the system temp
// directory unless the operator names somewhere else. Never $HOME, and
// never the pika checkout — the corpus writes nothing an operator did
// not ask for and nothing that outlives a reboot.
func cacheRoot() string {
	if dir := strings.TrimSpace(os.Getenv(CacheEnv)); dir != "" {
		return dir
	}
	return filepath.Join(os.TempDir(), "pika-conformance-cache")
}

// fetch returns the cached checkout of r, cloning it once if it is not
// there yet. Any failure to obtain it is wrapped in ErrNetwork.
//
// The clone is shallow and asks for the commit by name, so the corpus
// downloads one tree rather than a decade of history. The checkout is
// assembled beside the cache and renamed into place, so an interrupted
// run leaves no half-populated directory for the next run to trust.
func (h *harness) fetch(r Repo) (string, error) {
	dst := filepath.Join(h.cache, r.Name+"-"+r.SHA[:12])
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		return dst, nil
	}
	tmp, err := os.MkdirTemp(h.cache, "fetching-")
	if err != nil {
		return "", fmt.Errorf("%w: temp dir: %v", ErrNetwork, err)
	}
	defer os.RemoveAll(tmp)
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", r.URL},
		// core.autocrlf off: a Windows checkout that rewrote line
		// endings would hand the format gate CRLF in files upstream
		// wrote with LF, which is a fact about git, not about pika.
		{"config", "core.autocrlf", "false"},
		{"fetch", "--quiet", "--depth", "1", "origin", r.SHA},
		{"checkout", "--quiet", "FETCH_HEAD"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmp
		if b, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("%w: %s@%s: git %s: %v\n%s",
				ErrNetwork, r.URL, r.SHA[:12], strings.Join(args, " "), err, strings.TrimSpace(string(b)))
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		// Another run of the corpus won the race and put an identical
		// checkout there first; the commit is pinned, so either copy is
		// the same tree.
		if _, statErr := os.Stat(filepath.Join(dst, ".git")); statErr == nil {
			return dst, nil
		}
		return "", fmt.Errorf("%w: caching %s: %v", ErrNetwork, r.Name, err)
	}
	return dst, nil
}

// checkout copies a cached tree into dir, which is where pika runs.
//
// A copy rather than a run in place: pika writes a contract, a lock and
// a review bundle, and the toolchains it then spawns write `target/`,
// `.build/` and worse. The cache holds exactly what upstream published,
// so the next row — and the next run — starts from the same tree.
func checkout(src, dir string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode()&fs.ModeSymlink != 0:
			// Upstream trees carry symlinks; recreating them keeps the
			// copy faithful, and a link pika cannot follow is a finding
			// about the repository, not about the copy.
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case !info.Mode().IsRegular():
			return nil
		}
		return copyFile(p, target, info.Mode().Perm())
	})
}

// copyFile writes src to dst with the given permissions. The execute bit
// travels: a repository whose checks run through a committed shell
// script is one where dropping it changes the outcome.
func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// result is one invocation of the binary under test: what it was asked,
// what it exited, and what it said.
type result struct {
	argv     []string
	dir      string
	exit     int
	stdout   string
	stderr   string
	duration time.Duration
}

// String renders the invocation for a failure message.
func (r result) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "pika %s (in %s) exited %d", strings.Join(r.argv, " "), r.dir, r.exit)
	if s := strings.TrimRight(r.stdout, "\n"); s != "" {
		fmt.Fprintf(&b, "\n--- stdout ---\n%s", excerpt(s, 2000))
	}
	if s := strings.TrimRight(r.stderr, "\n"); s != "" {
		fmt.Fprintf(&b, "\n--- stderr ---\n%s", excerpt(s, 2000))
	}
	return b.String()
}

// run executes the built binary in dir. A non-zero exit is a result, not
// an error: three of the five rows are expected to end red.
func (h *harness) run(dir string, args ...string) (result, error) {
	var stdout, stderr strings.Builder
	cmd := exec.Command(h.pika, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	r := result{argv: args, dir: dir, stdout: stdout.String(), stderr: stderr.String(), duration: time.Since(start)}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		r.exit = exitErr.ExitCode()
	default:
		return r, fmt.Errorf("could not run pika %s: %w", strings.Join(args, " "), err)
	}
	return r, nil
}

// unwrap decodes a --json payload into the envelope every consumer reads
// before it knows the shape, and then into v.
func unwrap(r result, command string, v any) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil {
		return env, fmt.Errorf("`pika %s --json` did not print a cliout envelope: %w\n%s", command, err, r)
	}
	if env.Schema != 1 {
		return env, fmt.Errorf("`pika %s --json` printed envelope schema %d, want 1\n%s", command, env.Schema, r)
	}
	if env.Command != command {
		return env, fmt.Errorf("`pika %s --json` printed command %q, want %q\n%s", command, env.Command, command, r)
	}
	if v == nil || len(env.Result) == 0 {
		return env, nil
	}
	if err := json.Unmarshal(env.Result, v); err != nil {
		return env, fmt.Errorf("could not read the %s payload: %w\n%s", command, err, r)
	}
	return env, nil
}

// moduleRoot resolves the root of the module this test was started in,
// so the corpus builds the ./cmd/pika of the checkout under test
// whatever directory it was invoked from.
func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", errors.New("the conformance corpus must run inside the pika module; `go env GOMOD` reports none")
	}
	return filepath.Dir(gomod), nil
}

// exeName is what an executable is called on this platform.
func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
