package improve

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// EnabledEnv gates CheckCodexCompatibility the same way
// internal/conformance gates its corpus: an environment variable rather
// than a build tag, so the check stays compiled and vetted on every
// ordinary run and only the real subprocess call moves behind the
// switch. codex is not on every developer's PATH, and calling it is not
// appropriate for `go test ./...`.
const EnabledEnv = "PIKA_CODEX_COMPAT"

// codexHelpFlagPattern matches one declared option line from `codex exec
// --help`, clap's own format: an optional short alias ("-c, ") followed
// by the long flag ("--config"). Long-only options indent four spaces
// deeper than short+long ones; the pattern does not depend on the exact
// column, only on the flag syntax itself.
var codexHelpFlagPattern = regexp.MustCompile(`(?m)^\s+(?:-([A-Za-z]), )?(--[A-Za-z][\w-]*)`)

// declaredCodexFlags parses every flag spelling (both "-c" and
// "--config") that a `codex exec --help` transcript declares.
func declaredCodexFlags(help string) map[string]bool {
	declared := make(map[string]bool)
	for _, m := range codexHelpFlagPattern.FindAllStringSubmatch(help, -1) {
		if m[1] != "" {
			declared["-"+m[1]] = true
		}
		declared[m[2]] = true
	}
	return declared
}

// sentCodexFlags returns the flag tokens (not their values, and not the
// "-" stdin sentinel) that CodexRunner.args constructs. It reads them
// from a real args() call rather than a second, hand-maintained list, so
// a flag added to args tomorrow is covered here without anyone
// remembering to update a parallel copy.
func sentCodexFlags(binary string) []string {
	args := (CodexRunner{Binary: binary, Model: "x", Effort: "y"}).args("/repo", "/tmp/result.md")
	var flags []string
	for _, a := range args {
		if a == "-" || !strings.HasPrefix(a, "-") {
			continue
		}
		flags = append(flags, a)
	}
	return flags
}

// CheckCodexCompatibility runs `<binary> exec --help` and reports every
// flag CodexRunner constructs that this codex's own --help no longer
// declares — a rename or a removal that would otherwise surface only as
// `codex exec` exiting 2 while parsing its own arguments, after every
// later check gate has already been skipped (13cbf73, a1fedfd).
//
// It calls no model and spends no tokens: --help is a static usage dump
// clap prints without touching the network, and codex's own conflict
// validation (e.g. --sandbox beside --approve-for-me) is deliberately
// not exercised here — --help short-circuits clap before conflicts are
// checked, and the only way to observe a conflict for real is a live
// invocation whose argument parsing succeeds and then keeps running,
// which is exactly the model call and tokens this check exists to
// avoid. That gap is real and stays a gap; this closes the narrower one
// a static transcript can close safely.
func CheckCodexCompatibility(ctx context.Context, binary string) ([]string, error) {
	if binary == "" {
		binary = "codex"
	}
	out, err := exec.CommandContext(ctx, binary, "exec", "--help").Output()
	if err != nil {
		return nil, fmt.Errorf("%s exec --help: %w", binary, err)
	}
	declared := declaredCodexFlags(string(out))
	var missing []string
	for _, flag := range sentCodexFlags(binary) {
		if !declared[flag] {
			missing = append(missing, flag)
		}
	}
	return missing, nil
}
