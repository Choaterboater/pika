package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestImproveRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runImprove([]string{"--unknown"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr = %q, want flag error", stderr.String())
	}
}

func TestHandoffRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runHandoff([]string{"--unknown"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr = %q, want flag error", stderr.String())
	}
}
