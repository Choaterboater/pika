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

func TestCheckSchemaSupported(t *testing.T) {
	if err := Check(MaxContractSchema); err != nil {
		t.Fatalf("Check(%d) = %v, want nil", MaxContractSchema, err)
	}
	if err := Check(1); err != nil {
		t.Fatalf("Check(1) = %v, want nil", err)
	}
}

func TestCheckSchemaUnsupported(t *testing.T) {
	err := Check(MaxContractSchema + 1)
	if err == nil {
		t.Fatal("Check above MaxContractSchema = nil, want error")
	}
	if !strings.Contains(err.Error(), "supported") {
		t.Fatalf("error %q should name the supported ceiling", err)
	}
}
