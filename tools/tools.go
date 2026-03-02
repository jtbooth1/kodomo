package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"kodomo/agent"
)

// Register adds all built-in tools to the agent, rooted at workDir.
func Register(a *agent.Agent, workDir string) {
	a.AddTool(readTool(workDir))
	a.AddTool(writeTool(workDir))
	a.AddTool(editTool(workDir))
	a.AddTool(bashTool(workDir))
}

func resolvePath(workDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(workDir, path)
}

func readTool(workDir string) agent.ToolDef {
	return agent.ToolDef{
		Name:        "read",
		Description: "Read the contents of a text file. Defaults to the first 2000 lines. Use offset/limit for large files.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Path to the file (relative or absolute)"},
				"offset": map[string]any{"type": "integer", "description": "Line number to start from (1-indexed)"},
				"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to read"},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		Handler: func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}

			data, err := os.ReadFile(resolvePath(workDir, p.Path))
			if err != nil {
				return json.Marshal(map[string]string{"error": err.Error()})
			}

			lines := strings.Split(string(data), "\n")
			// Remove trailing empty line from Split
			if len(lines) > 0 && lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}

			offset := p.Offset
			if offset < 1 {
				offset = 1
			}
			limit := p.Limit
			if limit < 1 {
				limit = 2000
			}

			start := offset - 1
			if start > len(lines) {
				start = len(lines)
			}
			end := start + limit
			if end > len(lines) {
				end = len(lines)
			}

			var buf strings.Builder
			for i := start; i < end; i++ {
				fmt.Fprintf(&buf, "%d|%s\n", i+1, lines[i])
			}

			totalLines := len(lines)
			result := map[string]any{
				"content":    buf.String(),
				"totalLines": totalLines,
			}
			if end < totalLines {
				result["truncated"] = true
				result["hint"] = fmt.Sprintf("Showing lines %d-%d of %d. Use offset/limit to read more.", offset, end, totalLines)
			}
			return json.Marshal(result)
		},
	}
}

func writeTool(workDir string) agent.ToolDef {
	return agent.ToolDef{
		Name:        "write",
		Description: "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Path to the file (relative or absolute)"},
				"content": map[string]any{"type": "string", "description": "Content to write"},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		},
		Handler: func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}

			full := resolvePath(workDir, p.Path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return json.Marshal(map[string]string{"error": err.Error()})
			}
			if err := os.WriteFile(full, []byte(p.Content), 0o644); err != nil {
				return json.Marshal(map[string]string{"error": err.Error()})
			}

			lines := strings.Count(p.Content, "\n")
			if len(p.Content) > 0 && !strings.HasSuffix(p.Content, "\n") {
				lines++
			}
			return json.Marshal(map[string]any{
				"status": "ok",
				"path":   full,
				"lines":  lines,
			})
		},
	}
}

func editTool(workDir string) agent.ToolDef {
	return agent.ToolDef{
		Name:        "edit",
		Description: "Edit a file by replacing exact text. The oldText must match exactly (including whitespace).",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Path to the file (relative or absolute)"},
				"oldText": map[string]any{"type": "string", "description": "Exact text to find and replace"},
				"newText": map[string]any{"type": "string", "description": "New text to replace the old text with"},
			},
			"required":             []string{"path", "oldText", "newText"},
			"additionalProperties": false,
		},
		Handler: func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				Path    string `json:"path"`
				OldText string `json:"oldText"`
				NewText string `json:"newText"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}

			full := resolvePath(workDir, p.Path)
			data, err := os.ReadFile(full)
			if err != nil {
				return json.Marshal(map[string]string{"error": err.Error()})
			}

			content := string(data)
			count := strings.Count(content, p.OldText)
			if count == 0 {
				return json.Marshal(map[string]string{"error": "oldText not found in file"})
			}
			if count > 1 {
				return json.Marshal(map[string]string{"error": fmt.Sprintf("oldText found %d times, must be unique", count)})
			}

			updated := strings.Replace(content, p.OldText, p.NewText, 1)
			if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
				return json.Marshal(map[string]string{"error": err.Error()})
			}

			return json.Marshal(map[string]string{"status": "ok"})
		},
	}
}

func bashTool(workDir string) agent.ToolDef {
	return agent.ToolDef{
		Name:        "bash",
		Description: "Execute a bash command. Returns stdout and stderr.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "Bash command to execute"},
				"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds (optional)"},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
		Handler: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}

			if p.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Duration(p.Timeout)*time.Second)
				defer cancel()
			}

			cmd := exec.CommandContext(ctx, "bash", "-c", p.Command)
			cmd.Dir = workDir

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()

			exitCode := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					return json.Marshal(map[string]string{"error": err.Error()})
				}
			}

			result := map[string]any{
				"exitCode": exitCode,
			}

			out := truncateOutput(stdout.String(), 50000)
			errOut := truncateOutput(stderr.String(), 10000)
			if out != "" {
				result["stdout"] = out
			}
			if errOut != "" {
				result["stderr"] = errOut
			}

			return json.Marshal(result)
		},
	}
}

func truncateOutput(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Truncate at a line boundary
	scanner := bufio.NewScanner(strings.NewReader(s[:maxBytes]))
	var buf strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if buf.Len()+len(line)+1 > maxBytes {
			break
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	fmt.Fprintf(&buf, "\n... truncated (%d bytes total)", len(s))
	return buf.String()
}
