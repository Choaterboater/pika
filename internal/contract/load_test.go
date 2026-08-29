package contract

import (
	"strings"
	"testing"
)

func TestNormalizeRepoPath(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "valid relative", in: "web/app", want: "web/app"},
		{name: "dot cleaned", in: "./web/./app", want: "web/app"},
		{name: "internal dotdot cleans away", in: "web/../app", want: "app"},
		{name: "trailing slash cleaned", in: "web/app/", want: "web/app"},
		{name: "backslash normalized", in: `web\app`, want: "web/app"},
		{name: "single segment", in: "cmd", want: "cmd"},
		{name: "repo root is legal", in: ".", want: "."},
		{name: "escape rejected", in: "../../etc", wantErr: "path escapes repository root: ../../etc"},
		{name: "parent rejected", in: "..", wantErr: "path escapes repository root: .."},
		{name: "absolute rejected", in: "/etc", wantErr: "path escapes repository root: /etc"},
		{name: "drive letter rejected", in: `C:\x`, wantErr: "path escapes repository root: C:\\x"},
		{name: "drive letter forward slash rejected", in: "C:/x", wantErr: "path escapes repository root: C:/x"},
		{name: "unc rejected", in: `\\server\share`, wantErr: "path escapes repository root: \\\\server\\share"},
		{name: "empty rejected", in: "", wantErr: "path is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeRepoPath(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("NormalizeRepoPath(%q) = %q, want error %q", tc.in, got, tc.wantErr)
				}
				if err.Error() != tc.wantErr {
					t.Fatalf("NormalizeRepoPath(%q) error = %q, want %q", tc.in, err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeRepoPath(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeRepoPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLoadRejectsPathEscape(t *testing.T) {
	_, err := Load("testdata/invalid-escape-root.yaml")
	if err == nil {
		t.Fatal("expected escape error, got nil")
	}
	want := "contract: packages.frontend.root: path escapes repository root: ../../etc"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestLoadRejectsAbsolutePath(t *testing.T) {
	_, err := Load("testdata/invalid-absolute-root.yaml")
	if err == nil {
		t.Fatal("expected absolute-path error, got nil")
	}
	if !strings.Contains(err.Error(), "packages.frontend.root") ||
		!strings.Contains(err.Error(), "/etc") {
		t.Fatalf("error should name field and value, got: %v", err)
	}
}

func TestLoadRejectsDriveLetterPath(t *testing.T) {
	_, err := Load("testdata/invalid-drive-root.yaml")
	if err == nil {
		t.Fatal("expected drive-letter error, got nil")
	}
	if !strings.Contains(err.Error(), "packages.frontend.root") ||
		!strings.Contains(err.Error(), "C:\\x") {
		t.Fatalf("error should name field and value, got: %v", err)
	}
}

func TestLoadRejectsEmptyRoot(t *testing.T) {
	_, err := Load("testdata/invalid-empty-root.yaml")
	if err == nil {
		t.Fatal("expected empty-path error, got nil")
	}
	if !strings.Contains(err.Error(), "packages.frontend.root") {
		t.Fatalf("error should name the offending field, got: %v", err)
	}
}

func TestLoadNormalizesBackslashRoot(t *testing.T) {
	c, err := Load("testdata/valid-normalized-root.yaml")
	if err != nil {
		t.Fatalf("expected valid contract, got %v", err)
	}
	if c.Packages["frontend"].Root != "web/app" {
		t.Fatalf("root = %q, want %q", c.Packages["frontend"].Root, "web/app")
	}
}
