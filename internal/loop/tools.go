package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Tool bounds. read_file is head-truncated so the model reads top-down
// and is never shown a section it could mistake for the whole thing;
// run_command is tail-truncated — the same bound evidence uses — because
// a command's verdict lives at the end of its output.
const (
	maxFileBytes   = 32 * 1024
	maxOutputBytes = 8 * 1024
)

// commandTimeout bounds one run_command call.
const commandTimeout = 10 * time.Minute

// toolSet is the loop's whole tool set: read a file, write a file, run a
// command. write_file is the only one that changes the tree, and the
// run's own checks — the Git-state equality check, and requireNoNewChanges
// for explorer and reviewer — are what hold it to its role, exactly as
// they hold a harness binary with edit tools.
func toolSet() []tool {
	return []tool{
		{
			name:        "read_file",
			description: "Read a repository file. Returns the first 32 KiB; a longer file is head-truncated with a marker.",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Repository-relative path of the file to read.",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			name:        "write_file",
			description: "Write a repository file with the full new content, creating parent directories as needed.",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Repository-relative path of the file to write.",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "The full new content of the file.",
					},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			name:        "run_command",
			description: "Run a shell command in the repository root. Returns the last 8 KiB of combined stdout and stderr plus the exit status.",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The shell command to run.",
					},
				},
				"required": []string{"command"},
			},
		},
	}
}

// executeTool runs one tool call. A bad path, a missing file or an
// unknown tool is an isError result, not a run failure: the model sees
// the reason and self-corrects.
func executeTool(ctx context.Context, root string, call toolCall) toolResult {
	switch call.name {
	case "read_file":
		var in struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(call.input, &in); err != nil {
			return errorResult(call.id, fmt.Sprintf("read_file: invalid input: %v", err))
		}
		return readFileTool(root, call.id, in.Path)
	case "write_file":
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(call.input, &in); err != nil {
			return errorResult(call.id, fmt.Sprintf("write_file: invalid input: %v", err))
		}
		return writeFileTool(root, call.id, in.Path, in.Content)
	case "run_command":
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(call.input, &in); err != nil {
			return errorResult(call.id, fmt.Sprintf("run_command: invalid input: %v", err))
		}
		return runCommandTool(ctx, root, call.id, in.Command)
	default:
		return errorResult(call.id, fmt.Sprintf("unknown tool %q", call.name))
	}
}

func errorResult(id, reason string) toolResult {
	return toolResult{id: id, output: reason, isError: true}
}

// readFileTool returns the file's first 32 KiB, head-truncated with a
// marker when longer.
func readFileTool(root, id, p string) toolResult {
	full, err := resolvePath(root, p)
	if err != nil {
		return errorResult(id, "read_file: "+err.Error())
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return errorResult(id, fmt.Sprintf("read_file: %v", err))
	}
	if len(data) > maxFileBytes {
		return toolResult{
			id:     id,
			output: fmt.Sprintf("%s\n[truncated: first 32 KiB of a %d-byte file]", data[:maxFileBytes], len(data)),
		}
	}
	return toolResult{id: id, output: string(data)}
}

// writeFileTool writes the full new content at 0644, creating the parent
// directory as needed.
func writeFileTool(root, id, p, content string) toolResult {
	full, err := resolvePath(root, p)
	if err != nil {
		return errorResult(id, "write_file: "+err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return errorResult(id, fmt.Sprintf("write_file: %v", err))
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return errorResult(id, fmt.Sprintf("write_file: %v", err))
	}
	return toolResult{id: id, output: fmt.Sprintf("wrote %s (%d bytes)", p, len(content))}
}

// runCommandTool runs an unrestricted shell command in the repository
// root: sh -c on non-Windows, cmd /c on Windows, with a 10-minute
// per-command timeout. A non-zero exit is not an isError — it is a
// command that failed, which the model is supposed to see; isError is
// reserved for commands that could not be started.
func runCommandTool(ctx context.Context, root, id, command string) toolResult {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	result := string(out)
	if len(out) > maxOutputBytes {
		result = fmt.Sprintf("[truncated: last 8 KiB of a %d-byte output]\n%s", len(out), out[len(out)-maxOutputBytes:])
	}
	var status string
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		status = "killed by timeout"
	case err != nil:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return errorResult(id, fmt.Sprintf("run_command: %v", err))
		}
		status = fmt.Sprintf("exit %d", exitErr.ExitCode())
	}
	if status != "" {
		if result != "" {
			result = strings.TrimRight(result, "\n") + "\n" + status
		} else {
			result = status
		}
	}
	return toolResult{id: id, output: result}
}

// resolvePath validates a model-supplied repository-relative path against
// the same containment rule the contract applies to declared paths —
// backslashes converted, absolute paths (leading /, drive letters, UNC),
// home-relative paths (a leading ~) and traversal above the root rejected —
// and additionally refuses anything under .project/state/, which is
// kernel-private: a model has no business reading or writing its own run
// record. It returns the cleaned path joined to root.
func resolvePath(root, p string) (string, error) {
	if p == "" {
		return "", errors.New("path is empty")
	}
	norm := strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(norm, "/") {
		return "", fmt.Errorf("path escapes repository root: %s", p)
	}
	if len(norm) >= 2 && norm[1] == ':' && isDriveLetter(norm[0]) {
		return "", fmt.Errorf("path escapes repository root: %s", p)
	}
	if norm == "~" || strings.HasPrefix(norm, "~/") {
		return "", fmt.Errorf("path is relative to a home directory, which the loop cannot reach: %s", p)
	}
	cleaned := path.Clean(norm)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path escapes repository root: %s", p)
	}
	// Case-insensitive: on a case-insensitive filesystem (macOS default)
	// .PROJECT/STATE/x names the same kernel-private directory.
	if lower := strings.ToLower(cleaned); lower == ".project/state" || strings.HasPrefix(lower, ".project/state/") {
		return "", fmt.Errorf("path is inside .project/state/, which is kernel-private: %s", p)
	}
	return filepath.Join(root, filepath.FromSlash(cleaned)), nil
}

func isDriveLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}
