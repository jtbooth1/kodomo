package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"kodomo/agent"
	"kodomo/workflow"
)

func setup(t *testing.T) (*agent.Agent, string) {
	t.Helper()
	e, err := workflow.Open(":memory:")
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	a, err := agent.New(e, agent.Config{Model: "test"})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	dir := t.TempDir()
	Register(a, dir)
	return a, dir
}

func callTool(t *testing.T, a *agent.Agent, name string, params any) map[string]any {
	t.Helper()
	p, _ := json.Marshal(params)
	tool := a.Tool(name)
	if tool == nil {
		t.Fatalf("tool %q not found", name)
	}
	out, err := tool.Handler(context.Background(), p)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var result map[string]any
	json.Unmarshal(out, &result)
	return result
}

func TestReadWrite(t *testing.T) {
	a, dir := setup(t)

	// Write a file
	result := callTool(t, a, "write", map[string]any{
		"path":    "hello.txt",
		"content": "line1\nline2\nline3\n",
	})
	if result["status"] != "ok" {
		t.Fatalf("write failed: %v", result)
	}

	// Verify it's on disk
	data, _ := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if string(data) != "line1\nline2\nline3\n" {
		t.Fatalf("unexpected file content: %q", data)
	}

	// Read it back
	result = callTool(t, a, "read", map[string]any{"path": "hello.txt"})
	content := result["content"].(string)
	if content != "1|line1\n2|line2\n3|line3\n" {
		t.Fatalf("unexpected read content: %q", content)
	}
	if result["totalLines"].(float64) != 3 {
		t.Fatalf("expected 3 total lines, got %v", result["totalLines"])
	}
}

func TestReadOffsetLimit(t *testing.T) {
	a, _ := setup(t)

	callTool(t, a, "write", map[string]any{
		"path":    "big.txt",
		"content": "a\nb\nc\nd\ne\n",
	})

	result := callTool(t, a, "read", map[string]any{
		"path":   "big.txt",
		"offset": 2,
		"limit":  2,
	})
	content := result["content"].(string)
	if content != "2|b\n3|c\n" {
		t.Fatalf("unexpected content: %q", content)
	}
	if result["truncated"] != true {
		t.Fatal("expected truncated=true")
	}
}

func TestReadMissingFile(t *testing.T) {
	a, _ := setup(t)
	result := callTool(t, a, "read", map[string]any{"path": "nope.txt"})
	if _, ok := result["error"]; !ok {
		t.Fatal("expected error for missing file")
	}
}

func TestWriteCreatesParentDirs(t *testing.T) {
	a, dir := setup(t)

	result := callTool(t, a, "write", map[string]any{
		"path":    "deep/nested/file.txt",
		"content": "ok",
	})
	if result["status"] != "ok" {
		t.Fatalf("write failed: %v", result)
	}

	data, err := os.ReadFile(filepath.Join(dir, "deep/nested/file.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("unexpected content: %q", data)
	}
}

func TestEdit(t *testing.T) {
	a, dir := setup(t)

	callTool(t, a, "write", map[string]any{
		"path":    "edit.txt",
		"content": "hello world\nfoo bar\n",
	})

	result := callTool(t, a, "edit", map[string]any{
		"path":    "edit.txt",
		"oldText": "foo bar",
		"newText": "baz qux",
	})
	if result["status"] != "ok" {
		t.Fatalf("edit failed: %v", result)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "edit.txt"))
	if string(data) != "hello world\nbaz qux\n" {
		t.Fatalf("unexpected content: %q", data)
	}
}

func TestEditNotFound(t *testing.T) {
	a, _ := setup(t)
	callTool(t, a, "write", map[string]any{"path": "e.txt", "content": "abc"})
	result := callTool(t, a, "edit", map[string]any{"path": "e.txt", "oldText": "xyz", "newText": "123"})
	if _, ok := result["error"]; !ok {
		t.Fatal("expected error for not-found text")
	}
}

func TestEditAmbiguous(t *testing.T) {
	a, _ := setup(t)
	callTool(t, a, "write", map[string]any{"path": "e.txt", "content": "aa aa"})
	result := callTool(t, a, "edit", map[string]any{"path": "e.txt", "oldText": "aa", "newText": "bb"})
	if _, ok := result["error"]; !ok {
		t.Fatal("expected error for ambiguous match")
	}
}

func TestBash(t *testing.T) {
	a, _ := setup(t)

	result := callTool(t, a, "bash", map[string]any{"command": "echo hello"})
	if result["exitCode"].(float64) != 0 {
		t.Fatalf("expected exit 0, got %v", result["exitCode"])
	}
	if result["stdout"] != "hello\n" {
		t.Fatalf("unexpected stdout: %q", result["stdout"])
	}
}

func TestBashNonZeroExit(t *testing.T) {
	a, _ := setup(t)
	result := callTool(t, a, "bash", map[string]any{"command": "exit 42"})
	if result["exitCode"].(float64) != 42 {
		t.Fatalf("expected exit 42, got %v", result["exitCode"])
	}
}

func TestBashTimeout(t *testing.T) {
	a, _ := setup(t)
	result := callTool(t, a, "bash", map[string]any{"command": "sleep 60", "timeout": 1})
	if _, ok := result["error"]; !ok {
		if result["exitCode"].(float64) == 0 {
			t.Fatal("expected non-zero exit or error for timed-out command")
		}
	}
}

func TestBashWorkDir(t *testing.T) {
	a, dir := setup(t)
	result := callTool(t, a, "bash", map[string]any{"command": "pwd"})
	stdout := result["stdout"].(string)
	// Resolve symlinks on both sides for macOS /private/var vs /var
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	resolvedPwd, _ := filepath.EvalSymlinks(stdout[:len(stdout)-1]) // trim newline
	if resolvedPwd != resolvedDir {
		t.Fatalf("expected cwd %q, got %q", resolvedDir, resolvedPwd)
	}
}
