package cliout

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWriteWrapsResult(t *testing.T) {
	var buf bytes.Buffer
	type report struct {
		Gates int `json:"gates"`
	}
	if err := Write(&buf, "check", true, report{Gates: 6}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Schema != 1 {
		t.Errorf("Schema = %d, want 1", env.Schema)
	}
	if env.Command != "check" {
		t.Errorf("Command = %q, want %q", env.Command, "check")
	}
	if !env.OK {
		t.Error("OK = false, want true")
	}
	if env.Error != nil {
		t.Errorf("Error = %+v, want nil", env.Error)
	}
	var got report
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got.Gates != 6 {
		t.Errorf("Result.Gates = %d, want 6", got.Gates)
	}
}

func TestWriteIsIndentedAndNewlineTerminated(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, "doctor", true, map[string]int{"a": 1}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Error("output is not newline-terminated")
	}
	if !strings.Contains(out, "\n  \"command\": \"doctor\"") {
		t.Errorf("output is not 2-space indented:\n%s", out)
	}
}

func TestWriteErrorOmitsResult(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteError(&buf, "authorize", "usage", "unknown scope \"wide\""); err != nil {
		t.Fatalf("WriteError: %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.OK {
		t.Error("OK = true, want false")
	}
	if env.Error == nil || env.Error.Code != "usage" {
		t.Fatalf("Error = %+v, want code \"usage\"", env.Error)
	}
	if len(env.Result) != 0 {
		t.Errorf("Result = %s, want empty", env.Result)
	}
}

func TestWriteNilResultOmitsField(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, "help", true, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if strings.Contains(buf.String(), "\"result\"") {
		t.Errorf("nil result emitted a result field:\n%s", buf.String())
	}
}
