package adapters

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// EnabledEnv gates CheckCompatibility the same way internal/conformance
// gates its corpus: an environment variable rather than a build tag, so
// the check stays compiled and vetted on every ordinary `go test ./...`
// and only the real subprocess call moves behind the switch. A harness is
// not on every developer's PATH, and calling one is not appropriate for
// that run.
const EnabledEnv = "PIKA_ADAPTER_COMPAT"

// helpFlagPattern matches one declared option line from a `--help`
// transcript, clap's own format: an optional short alias ("-c, ") followed
// by the long flag ("--config"). Long-only options indent four spaces
// deeper than short+long ones; the pattern does not depend on the exact
// column, only on the flag syntax itself.
var helpFlagPattern = regexp.MustCompile(`(?m)^\s+(?:-([A-Za-z]), )?(--[A-Za-z][\w-]*)`)

// declaredFlags parses every flag spelling (both "-c" and "--config") a
// `--help` transcript declares.
func declaredFlags(help string) map[string]bool {
	declared := make(map[string]bool)
	for _, m := range helpFlagPattern.FindAllStringSubmatch(help, -1) {
		if m[1] != "" {
			declared["-"+m[1]] = true
		}
		declared[m[2]] = true
	}
	return declared
}

// CheckCompatibility runs this adapter's Help argv and reports every flag
// the adapter constructs that the binary's own --help no longer declares —
// a rename or a removal that would otherwise surface only as the harness
// exiting 2 while parsing its own arguments, after every later check gate
// has already been skipped.
//
// It calls no model and spends no tokens: --help is a static usage dump
// clap prints without touching the network, and a runtime's own conflict
// validation (codex's --sandbox beside --approve-for-me, for one) is
// deliberately not exercised here — --help short-circuits argument parsing
// before conflicts are checked, and the only way to observe a conflict for
// real is a live invocation that then keeps running, which is exactly the
// model call this exists to avoid. That gap is real and stays a gap; this
// closes the narrower one a static transcript can close safely.
//
// An adapter with no Help argv cannot be probed and reports nothing; a
// binary that is not installed is an error, naming itself.
func CheckCompatibility(ctx context.Context, ad Adapter) ([]string, error) {
	if len(ad.Help) == 0 {
		return nil, nil
	}
	binary := ad.Binary
	if binary == "" {
		return nil, fmt.Errorf("runtime %q has no binary to probe", ad.Runtime)
	}
	out, err := exec.CommandContext(ctx, binary, ad.Help...).Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", binary, strings.Join(ad.Help, " "), err)
	}
	declared := declaredFlags(string(out))
	var missing []string
	for _, flag := range ad.ProbeFlags() {
		if !declared[flag] {
			missing = append(missing, flag)
		}
	}
	return missing, nil
}
