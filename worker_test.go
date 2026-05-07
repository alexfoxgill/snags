package main

import (
	"encoding/json"
	"os/exec"
	"testing"
)

func TestParseClaudeOutputSuccess(t *testing.T) {
	raw := `{"structured_output":{"status":"success","notes":"renamed all usages via sed"}}`
	var out claudeOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.StructuredOutput.Status != "success" {
		t.Errorf("expected success, got %q", out.StructuredOutput.Status)
	}
	if out.StructuredOutput.Notes != "renamed all usages via sed" {
		t.Errorf("wrong notes: %q", out.StructuredOutput.Notes)
	}
}

func TestParseClaudeOutputFailed(t *testing.T) {
	raw := `{"structured_output":{"status":"failed","notes":"could not find the function anywhere"}}`
	var out claudeOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.StructuredOutput.Status != "failed" {
		t.Errorf("expected failed, got %q", out.StructuredOutput.Status)
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
