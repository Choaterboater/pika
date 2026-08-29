package version

import (
	"strings"
	"testing"
)

func TestVersionSemver(t *testing.T) {
	if !strings.Contains(Version, ".") {
		t.Fatalf("Version %q is not dotted semver", Version)
	}
}

func TestString(t *testing.T) {
	if String() != Version {
		t.Fatalf("String() = %q, want %q", String(), Version)
	}
}

func TestCheckStub(t *testing.T) {
	if err := Check(false); err != nil {
		t.Fatalf("Check(false) = %v, want nil", err)
	}
}
