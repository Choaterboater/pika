package profiles

import _ "embed"

// Language pack references, one per V1 language profile (spec §5.4).
const (
	TypeScriptRef = "typescript@1"
	PythonRef     = "python@1"
	SwiftRef      = "swift@1"
	RustRef       = "rust@1"
	GoRef         = "go@1"
)

//go:embed packs/typescript@1.yaml
var typescriptPackYAML []byte

//go:embed packs/python@1.yaml
var pythonPackYAML []byte

//go:embed packs/swift@1.yaml
var swiftPackYAML []byte

//go:embed packs/rust@1.yaml
var rustPackYAML []byte

//go:embed packs/go@1.yaml
var goPackYAML []byte

// languageRefs lists the language packs in spec §5.4 V1 order.
var languageRefs = []string{TypeScriptRef, PythonRef, SwiftRef, RustRef, GoRef}

// languagePacks marks the embedded packs that compose as language layers.
var languagePacks = map[string]bool{
	TypeScriptRef: true,
	PythonRef:     true,
	SwiftRef:      true,
	RustRef:       true,
	GoRef:         true,
}

func init() {
	embeddedPacks[TypeScriptRef] = packEntry{name: "typescript", version: "1", data: typescriptPackYAML}
	embeddedPacks[PythonRef] = packEntry{name: "python", version: "1", data: pythonPackYAML}
	embeddedPacks[SwiftRef] = packEntry{name: "swift", version: "1", data: swiftPackYAML}
	embeddedPacks[RustRef] = packEntry{name: "rust", version: "1", data: rustPackYAML}
	embeddedPacks[GoRef] = packEntry{name: "go", version: "1", data: goPackYAML}
}

// LanguagePack maps a discover.Inventory language id ("typescript",
// "python", "swift", "rust", "go") to its pack reference. It is the
// pairing between stack detection and profile selection: a detected
// language id selects exactly one language pack.
func LanguagePack(language string) (string, bool) {
	switch language {
	case "typescript":
		return TypeScriptRef, true
	case "python":
		return PythonRef, true
	case "swift":
		return SwiftRef, true
	case "rust":
		return RustRef, true
	case "go":
		return GoRef, true
	}
	return "", false
}

// supportedRefs lists every selectable pack reference, core first.
func supportedRefs() []string {
	return append([]string{CoreRef}, languageRefs...)
}
