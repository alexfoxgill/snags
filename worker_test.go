package main

import (
	"encoding/json"
	"os/exec"
	"testing"
)

func TestParseStreamLineResultSuccess(t *testing.T) {
	raw := `{"type":"result","subtype":"success","structured_output":{"status":"success","notes":"renamed all usages via sed"}}`
	var line streamLine
	if err := json.Unmarshal([]byte(raw), &line); err != nil {
		t.Fatal(err)
	}
	if line.Type != "result" {
		t.Errorf("expected type=result, got %q", line.Type)
	}
	if line.StructuredOutput.Status != "success" {
		t.Errorf("expected success, got %q", line.StructuredOutput.Status)
	}
	if line.StructuredOutput.Notes != "renamed all usages via sed" {
		t.Errorf("wrong notes: %q", line.StructuredOutput.Notes)
	}
}

func TestParseStreamLineResultFailed(t *testing.T) {
	raw := `{"type":"result","subtype":"success","structured_output":{"status":"failed","notes":"could not find the function anywhere"}}`
	var line streamLine
	if err := json.Unmarshal([]byte(raw), &line); err != nil {
		t.Fatal(err)
	}
	if line.StructuredOutput.Status != "failed" {
		t.Errorf("expected failed, got %q", line.StructuredOutput.Status)
	}
}

func TestExtractToolDetailBash(t *testing.T) {
	detail := extractToolDetail("Bash", `{"command":"go test ./..."}`)
	if detail != "go test ./..." {
		t.Errorf("expected 'go test ./...', got %q", detail)
	}
}

func TestExtractToolDetailBashTruncated(t *testing.T) {
	long := "echo " + string(make([]byte, 60))
	detail := extractToolDetail("Bash", `{"command":"`+long+`"}`)
	if len(detail) > 50 {
		t.Errorf("expected truncation to 50 chars, got %d: %q", len(detail), detail)
	}
}

func TestExtractToolDetailFilePath(t *testing.T) {
	detail := extractToolDetail("Edit", `{"file_path":"worker.go","old_string":"foo","new_string":"bar"}`)
	if detail != "worker.go" {
		t.Errorf("expected 'worker.go', got %q", detail)
	}
}

func TestExtractToolDetailWebSearch(t *testing.T) {
	detail := extractToolDetail("WebSearch", `{"query":"bubbletea streaming"}`)
	if detail != "bubbletea streaming" {
		t.Errorf("expected 'bubbletea streaming', got %q", detail)
	}
}

func TestExtractToolDetailUnknownTool(t *testing.T) {
	detail := extractToolDetail("UnknownTool", `{"something":"value"}`)
	if detail != "" {
		t.Errorf("expected empty for unknown tool, got %q", detail)
	}
}

func TestExtractToolDetailInvalidJSON(t *testing.T) {
	detail := extractToolDetail("Bash", `not json`)
	if detail != "" {
		t.Errorf("expected empty for invalid JSON, got %q", detail)
	}
}

func TestDetectDefaultBranchMain(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Run()
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-m", "init")

	branch := detectDefaultBranch(dir)
	if branch != "main" {
		t.Errorf("expected main, got %q", branch)
	}
}

func TestDetectDefaultBranchMaster(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Run()
	}
	run("init", "-b", "master")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-m", "init")

	branch := detectDefaultBranch(dir)
	if branch != "master" {
		t.Errorf("expected master, got %q", branch)
	}
}
