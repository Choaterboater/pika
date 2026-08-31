package loop

// The tool set: the bounds each tool keeps, and the containment rule
// every model-supplied path answers to. A refused path, a missing file
// or an unknown tool is an isError tool result the model self-corrects
// from, never a run failure.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestReadFileTruncatesAndRefusesPaths pins the 32 KiB head truncation
// with its marker, and the paths read_file must refuse: absolute,
// traversal above the root, and anything under kernel-private
// .project/state/.
func TestReadFileTruncatesAndRefusesPaths(t *testing.T) {
	root := t.TempDir()
	big := bytes.Repeat([]byte("0123456789"), 4000) // 40000 bytes
	if err := os.WriteFile(filepath.Join(root, "big.txt"), big, 0o644); err != nil {
		t.Fatal(err)
	}

	res := executeTool(context.Background(), root, toolCall{
		id: "t1", name: "read_file", input: json.RawMessage(`{"path":"big.txt"}`),
	})
	if res.isError {
		t.Fatalf("reading a real file is an error result: %q", res.output)
	}
	if !strings.HasPrefix(res.output, string(big[:100])) {
		t.Errorf("the truncated output does not start with the file's head")
	}
	if !strings.Contains(res.output, "[truncated: first 32 KiB of a 40000-byte file]") {
		t.Errorf("no truncation marker in the output:\n%.100s…", res.output)
	}
	// Head-truncated means the result is exactly the first 32 KiB, then
	// the marker.
	want := string(big[:maxFileBytes]) + "\n[truncated: first 32 KiB of a 40000-byte file]"
	if res.output != want {
		t.Errorf("the truncated result is not the first 32 KiB plus the marker: %d bytes, want %d", len(res.output), len(want))
	}

	for path, reason := range map[string]string{
		"/etc/hostname":                     "path escapes repository root",
		"../outside.txt":                    "path escapes repository root",
		".project/state/work/x/record.json": "kernel-private",
	} {
		res := executeTool(context.Background(), root, toolCall{
			id:    "t2",
			name:  "read_file",
			input: json.RawMessage(`{"path":` + strconvQuote(path) + `}`),
		})
		if !res.isError {
			t.Errorf("read_file(%q) is not an error result: %q", path, res.output)
			continue
		}
		if !strings.Contains(res.output, reason) {
			t.Errorf("read_file(%q) error %q does not name %q", path, res.output, reason)
		}
	}

	// A missing file is an error result naming the failure, not a run
	// failure.
	res = executeTool(context.Background(), root, toolCall{
		id: "t3", name: "read_file", input: json.RawMessage(`{"path":"missing.txt"}`),
	})
	if !res.isError {
		t.Errorf("reading a missing file is not an error result: %q", res.output)
	}
}

// TestWriteFileRefusesPrivateState: .project/state/ is kernel-private,
// and the refusal must leave nothing on disk. A legitimate write creates
// its parent directories and lands at the repository-relative path.
func TestWriteFileRefusesPrivateState(t *testing.T) {
	root := t.TempDir()
	res := executeTool(context.Background(), root, toolCall{
		id: "w1", name: "write_file",
		input: json.RawMessage(`{"path":".project/state/record.json","content":"{}"}`),
	})
	if !res.isError || !strings.Contains(res.output, "kernel-private") {
		t.Errorf("write_file into .project/state/ = %+v, want a refusal naming the kernel-private path", res)
	}
	if _, err := os.Stat(filepath.Join(root, ".project")); !os.IsNotExist(err) {
		t.Errorf("the refused write left .project behind: %v", err)
	}

	res = executeTool(context.Background(), root, toolCall{
		id: "w2", name: "write_file",
		input: json.RawMessage(`{"path":"dir/new.txt","content":"content"}`),
	})
	if res.isError {
		t.Fatalf("writing a legitimate file is an error result: %q", res.output)
	}
	got, err := os.ReadFile(filepath.Join(root, "dir", "new.txt"))
	if err != nil {
		t.Fatalf("the write did not land: %v", err)
	}
	if string(got) != "content" {
		t.Errorf("written content = %q, want %q", got, "content")
	}
	if !strings.Contains(res.output, "wrote dir/new.txt (7 bytes)") {
		t.Errorf("write_file result = %q, want the write acknowledged", res.output)
	}
}

// TestRunCommandReportsExitAndTruncates: a non-zero exit is a command
// that failed, which the model is supposed to see — so it is not an
// isError, and the result states the exit status. Output past 8 KiB is
// tail-truncated with a marker, because a command's verdict lives at the
// end.
func TestRunCommandReportsExitAndTruncates(t *testing.T) {
	root := t.TempDir()
	res := executeTool(context.Background(), root, toolCall{
		id: "r1", name: "run_command", input: json.RawMessage(`{"command":"echo something; exit 3"}`),
	})
	if res.isError {
		t.Errorf("a non-zero exit is an isError result: %q", res.output)
	}
	if !strings.Contains(res.output, "something") || !strings.Contains(res.output, "exit 3") {
		t.Errorf("failing command result = %q, want the output and the exit status", res.output)
	}

	res = executeTool(context.Background(), root, toolCall{
		id: "r2", name: "run_command", input: json.RawMessage(`{"command":"echo ok"}`),
	})
	if res.isError || strings.TrimSpace(res.output) != "ok" {
		t.Errorf("successful command result = %+v, want the output and no status", res)
	}

	if runtime.GOOS == "windows" {
		// The truncation subcase needs a portable way to emit 9000
		// bytes; sh pipelines are what the non-Windows shell runs.
		t.Skip("the 8 KiB truncation subcase needs a POSIX pipeline")
	}
	res = executeTool(context.Background(), root, toolCall{
		id: "r3", name: "run_command",
		input: json.RawMessage(`{"command":"head -c 9000 /dev/zero | tr '\\0' 'y'"}`),
	})
	if res.isError {
		t.Fatalf("the truncation pipeline failed: %q", res.output)
	}
	if !strings.HasPrefix(res.output, "[truncated: last 8 KiB of a 9000-byte output]\n") {
		t.Errorf("no tail-truncation marker:\n%.100s…", res.output)
	}
	tail := strings.TrimPrefix(res.output, "[truncated: last 8 KiB of a 9000-byte output]\n")
	if got := strings.Count(tail, "y"); got != 8192 {
		t.Errorf("the kept tail is %d bytes, want exactly 8 KiB", got)
	}
}

// TestUnknownToolIsAnErrorResult: a tool name outside the three is an
// isError result naming it, so the model self-corrects instead of the
// run failing.
func TestUnknownToolIsAnErrorResult(t *testing.T) {
	res := executeTool(context.Background(), t.TempDir(), toolCall{
		id: "u1", name: "delete_everything", input: json.RawMessage(`{}`),
	})
	if !res.isError || !strings.Contains(res.output, `unknown tool "delete_everything"`) {
		t.Errorf("unknown tool result = %+v, want an error result naming it", res)
	}
	if res.id != "u1" {
		t.Errorf("result id = %q, want the call's id so the provider can match it", res.id)
	}
}

// strconvQuote renders s as a JSON string literal.
func strconvQuote(s string) string {
	q, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(q)
}
