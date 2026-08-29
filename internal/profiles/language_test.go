package profiles

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Choaterboater/projectctl/internal/discover"
	"github.com/Choaterboater/projectctl/internal/yamlx"
)

// langCase is one language's Resolve expectations, paired with the golden
// discover fixture that detects it.
type langCase struct {
	fixture  string // directory under internal/discover/testdata
	language string // discover.Inventory language id
	ref      string // pack reference
	// cmds are slots resolved to real commands; hints are slots left as
	// discovery sentinels carrying the suggested command for when the
	// discovered one is absent. Every slot not listed here is smoke.
	cmds  map[string][]string
	hints map[string][]string
	// layoutTerm must appear in the language pack's §6.1 layout
	// expectations.
	layoutTerm string
}

var langCases = map[string]langCase{
	"go": {
		fixture:  "go-mod",
		language: "go",
		ref:      "go@1",
		cmds:     map[string][]string{"test": {"go", "test", "./..."}},
		hints: map[string][]string{
			"format":    {"gofmt", "-l", "-w", "."},
			"lint":      {"go", "vet", "./..."},
			"typecheck": {"go", "build", "./..."},
		},
		layoutTerm: "cmd/",
	},
	"typescript": {
		fixture:  "ts-single",
		language: "typescript",
		ref:      "typescript@1",
		hints: map[string][]string{
			"format":    {"npm", "run", "format"},
			"lint":      {"npm", "run", "lint"},
			"typecheck": {"npx", "tsc", "--noEmit"},
			"test":      {"npm", "test"},
		},
		layoutTerm: "src/",
	},
	"python": {
		fixture:  "py-single",
		language: "python",
		ref:      "python@1",
		cmds:     map[string][]string{"test": {"python", "-m", "pytest"}},
		hints: map[string][]string{
			"format":    {"ruff", "format", "."},
			"lint":      {"ruff", "check", "."},
			"typecheck": {"mypy", "."},
		},
		layoutTerm: "tests/",
	},
	"swift": {
		fixture:  "swift-xcode",
		language: "swift",
		ref:      "swift@1",
		cmds: map[string][]string{
			"typecheck": {"swift", "build"},
			"test":      {"swift", "test"},
		},
		hints:      map[string][]string{"format": {"swift", "format"}},
		layoutTerm: "Swift Package Manager",
	},
	"rust": {
		fixture:  "rust-cargo",
		language: "rust",
		ref:      "rust@1",
		cmds: map[string][]string{
			"typecheck": {"cargo", "build"},
			"test":      {"cargo", "test"},
		},
		hints: map[string][]string{
			"format": {"cargo", "fmt", "--", "--check"},
			"lint":   {"cargo", "clippy", "--", "-D", "warnings"},
		},
		layoutTerm: "Cargo",
	},
}

func slotChecks(cs CheckSet) map[string]Check {
	return map[string]Check{
		"format":    cs.Format,
		"lint":      cs.Lint,
		"typecheck": cs.Typecheck,
		"test":      cs.Test,
		"smoke":     cs.Smoke,
	}
}

// TestLanguageProfileResolve pairs each golden discover fixture with its
// language pack: detection agrees with discover, Resolve composes
// [core@1, <lang>@1] into two layers in spec order, and the CheckSet
// carries the real commands and hinted sentinels the pack declares.
func TestLanguageProfileResolve(t *testing.T) {
	for language, tc := range langCases {
		t.Run(language, func(t *testing.T) {
			inv, err := discover.Discover(filepath.Join("..", "..", "internal", "discover", "testdata", tc.fixture))
			if err != nil {
				t.Fatalf("Discover(%s): %v", tc.fixture, err)
			}
			if !slices.Contains(inv.DetectedLanguages, tc.language) {
				t.Fatalf("fixture %s detected %v, want %q", tc.fixture, inv.DetectedLanguages, tc.language)
			}
			ref, ok := LanguagePack(tc.language)
			if !ok || ref != tc.ref {
				t.Fatalf("LanguagePack(%q) = %q, %v; want %q", tc.language, ref, ok, tc.ref)
			}

			r, err := Resolve([]string{CoreRef, ref})
			if err != nil {
				t.Fatalf("Resolve([%s, %s]): %v", CoreRef, ref, err)
			}
			if len(r.Layers) != 2 {
				t.Fatalf("got %d layers, want 2", len(r.Layers))
			}
			if r.Layers[0].Name != "core" || r.Layers[0].Version != "1" {
				t.Errorf("layer 0 = %s@%s, want core@1", r.Layers[0].Name, r.Layers[0].Version)
			}
			if r.Layers[1].Name != tc.language || r.Layers[1].Version != "1" {
				t.Errorf("layer 1 = %s@%s, want %s@1", r.Layers[1].Name, r.Layers[1].Version, tc.language)
			}

			// Detection pairs the pack with discover's language id.
			pack := r.Layers[1].Pack
			if !strings.Contains(pack.Detection.When, tc.language) {
				t.Errorf("detection.when = %q, want it to reference %q", pack.Detection.When, tc.language)
			}

			// §6.1 layout expectations are recorded in the pack.
			if len(pack.Layout.Expectations) == 0 {
				t.Errorf("layout expectations empty; want spec §6.1 layout for %s", tc.language)
			} else {
				joined := strings.Join(pack.Layout.Expectations, "\n")
				if !strings.Contains(joined, tc.layoutTerm) {
					t.Errorf("layout expectations %q missing %q", joined, tc.layoutTerm)
				}
			}

			// Language packs add no naming rules in M1, so composition
			// keeps exactly core's rules (the composition hook stays).
			core, err := Resolve([]string{CoreRef})
			if err != nil {
				t.Fatalf("Resolve([core@1]): %v", err)
			}
			if len(r.NamingRules) != len(core.NamingRules) {
				t.Errorf("naming rules = %d, want core's %d (language packs add none in M1)",
					len(r.NamingRules), len(core.NamingRules))
			}

			// CheckSet: real commands win their slot; discovery sentinels
			// carry hints and never a command.
			checks := slotChecks(r.Checks)
			for id, want := range tc.cmds {
				c := checks[id]
				if c.Discovery {
					t.Errorf("check %s = discovery, want command %v", id, want)
					continue
				}
				if !slices.Equal(c.Cmd, want) {
					t.Errorf("check %s cmd = %v, want %v", id, c.Cmd, want)
				}
				if len(c.Hint) != 0 {
					t.Errorf("check %s: real command must not carry a hint, got %v", id, c.Hint)
				}
			}
			for id, want := range tc.hints {
				c := checks[id]
				if !c.Discovery {
					t.Errorf("check %s = cmd %v, want discovery sentinel with hint %v", id, c.Cmd, want)
					continue
				}
				if len(c.Cmd) != 0 {
					t.Errorf("check %s: discovery sentinel must not carry a command, got %v", id, c.Cmd)
				}
				if !slices.Equal(c.Hint, want) {
					t.Errorf("check %s hint = %v, want %v", id, c.Hint, want)
				}
			}
			smoke := checks["smoke"]
			if !smoke.Discovery || len(smoke.Cmd) != 0 || len(smoke.Hint) != 0 {
				t.Errorf("smoke = %+v, want a plain discovery sentinel", smoke)
			}
		})
	}
}

// TestResolveRejectsInvalidComposition covers the two-layer selection
// rules: core exactly once, at most one language, unknown packs rejected.
func TestResolveRejectsInvalidComposition(t *testing.T) {
	for _, sel := range [][]string{
		{"core@1", "typescript@1", "go@1"},   // two language packs
		{"core@1", "core@1"},                 // core twice
		{"typescript@1"},                     // no core
		{"core@1", "typescript@2"},           // unknown language pack version
		{"core@1", "core@1", "typescript@1"}, // three layers not supported in M1
	} {
		r, err := Resolve(sel)
		if err == nil {
			t.Errorf("Resolve(%v): want error, got %+v", sel, r)
		}
	}
}

// TestPackParsingRejectsDuplicateKeys proves pack parsing routes through
// yamlx: a duplicate key anywhere in a pack is rejected, and unknown
// fields are rejected by the strict walk.
func TestPackParsingRejectsDuplicateKeys(t *testing.T) {
	dupNested := []byte("profile: go\nversion: \"1\"\nverification:\n  checks:\n    - id: test\n      cmd: [go, test, ./...]\n      cmd: [go, vet, ./...]\n")
	var p Pack
	if err := yamlx.UnmarshalStrict(dupNested, &p); err == nil {
		t.Error("duplicate nested key in pack: want rejection, got none")
	}

	dupTop := []byte("profile: go\nprofile: rust\n")
	var q Pack
	if err := yamlx.UnmarshalStrict(dupTop, &q); err == nil {
		t.Error("duplicate top-level key in pack: want rejection, got none")
	}

	unknown := []byte("profile: go\nversion: \"1\"\nbogus: true\n")
	var u Pack
	if err := yamlx.UnmarshalStrict(unknown, &u); err == nil {
		t.Error("unknown top-level field in pack: want rejection, got none")
	}

	unknownNested := []byte("profile: go\nversion: \"1\"\nverification:\n  checks:\n    - id: test\n      cmd: [go, test, ./...]\n      bogus: true\n")
	var n Pack
	if err := yamlx.UnmarshalStrict(unknownNested, &n); err == nil {
		t.Error("unknown nested field in pack: want rejection, got none")
	}
}

// TestResolveRejectsDuplicateCheckSlot asserts the pack-level check-id
// uniqueness rule surfaces through Resolve.
func TestResolveRejectsDuplicateCheckSlot(t *testing.T) {
	dupSlot := []byte("profile: go\nversion: \"1\"\nverification:\n  checks:\n    - id: test\n      cmd: [go, test, ./...]\n    - id: test\n      cmd: [go, test, ./...]\n    - id: format\n      discovery: true\n    - id: lint\n      discovery: true\n    - id: typecheck\n      discovery: true\n    - id: smoke\n      discovery: true\n")
	orig := embeddedPacks[GoRef]
	embeddedPacks[GoRef] = packEntry{name: "go", version: "1", data: dupSlot}
	t.Cleanup(func() { embeddedPacks[GoRef] = orig })

	if _, err := Resolve([]string{CoreRef, GoRef}); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("Resolve with duplicate check slot: want %q error, got %v", "declared more than once", err)
	}
}
