package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestParseStreamLineToolUse(t *testing.T) {
	raw := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"hello.txt","content":"hello world"}}]}}`
	var line streamLine
	if err := json.Unmarshal([]byte(raw), &line); err != nil {
		t.Fatal(err)
	}
	if line.Type != "assistant" {
		t.Errorf("expected type=assistant, got %q", line.Type)
	}
	if len(line.Message.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(line.Message.Content))
	}
	block := line.Message.Content[0]
	if block.Type != "tool_use" {
		t.Errorf("expected tool_use, got %q", block.Type)
	}
	if block.Name != "Write" {
		t.Errorf("expected Write, got %q", block.Name)
	}
	detail := extractToolDetail(block.Name, string(block.Input))
	if detail != "hello.txt" {
		t.Errorf("expected 'hello.txt', got %q", detail)
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

func TestClaudeArgsDefault(t *testing.T) {
	cfg := DefaultConfig().Agents.Snag // model=fable effort=low
	args := claudeArgs(cfg, "do the thing", "{}")
	expected := []string{
		"--model", "fable",
		"--effort", "low",
		"-p", "do the thing",
		"--output-format", "stream-json",
		"--verbose",
		"--json-schema", "{}",
		"--permission-mode", "auto",
		"--setting-sources", "project,local",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
		"--disable-slash-commands",
		"--tools", "Read,Edit,Write,Bash,Grep,Glob,Agent",
		"--exclude-dynamic-system-prompt-sections",
		"--settings", `{"autoMode":{"environment":["$defaults"]}}`,
	}
	if !reflect.DeepEqual(args, expected) {
		t.Errorf("args mismatch\ngot:  %q\nwant: %q", args, expected)
	}
}

func TestClaudeArgsNoEffort(t *testing.T) {
	args := claudeArgs(AgentConfig{Model: "sonnet"}, "p", "{}")
	if args[0] != "--model" || args[1] != "sonnet" {
		t.Errorf("expected --model sonnet first, got %q", args[:2])
	}
	for _, a := range args {
		if a == "--effort" {
			t.Error("--effort present despite empty Effort")
		}
	}
}

func TestClaudeArgsExtraArgsLast(t *testing.T) {
	cfg := AgentConfig{Model: "sonnet", Effort: "high", ExtraArgs: []string{"--foo", "bar"}}
	args := claudeArgs(cfg, "p", "{}")
	if args[len(args)-2] != "--foo" || args[len(args)-1] != "bar" {
		t.Errorf("extra args not appended last: %q", args[len(args)-2:])
	}
}

func TestTranscriptLoggerWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	tl := newTranscriptLogger(dir, "abc")
	if tl == nil {
		t.Fatal("expected logger, got nil")
	}
	tl.runStart("agent")
	tl.text("thinking about it")
	tl.tool("Bash", "go test ./...")
	tl.result(true, "all done")
	tl.Close()

	// A second run appends to the same log.
	tl2 := newTranscriptLogger(dir, "abc")
	tl2.runStart("merge")
	tl2.Close()

	data, err := os.ReadFile(snagLogFile(dir, "abc"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d:\n%s", len(lines), data)
	}
	var evs []map[string]string
	for i, line := range lines {
		var ev map[string]string
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d not valid JSON: %v: %s", i, err, line)
		}
		evs = append(evs, ev)
	}
	if evs[0]["type"] != "run_start" || evs[0]["label"] != "agent" {
		t.Errorf("bad run_start event: %v", evs[0])
	}
	if _, err := time.Parse(time.RFC3339, evs[0]["time"]); err != nil {
		t.Errorf("run_start time not RFC3339: %v", err)
	}
	if evs[1]["type"] != "text" || evs[1]["text"] != "thinking about it" {
		t.Errorf("bad text event: %v", evs[1])
	}
	if evs[2]["type"] != "tool" || evs[2]["name"] != "Bash" || evs[2]["detail"] != "go test ./..." {
		t.Errorf("bad tool event: %v", evs[2])
	}
	if evs[3]["type"] != "result" || evs[3]["status"] != "success" || evs[3]["notes"] != "all done" {
		t.Errorf("bad result event: %v", evs[3])
	}
	if evs[4]["type"] != "run_start" || evs[4]["label"] != "merge" {
		t.Errorf("bad appended run_start event: %v", evs[4])
	}
}

func TestTranscriptLoggerNilSafe(t *testing.T) {
	var tl *transcriptLogger
	tl.runStart("agent")
	tl.text("x")
	tl.tool("Bash", "ls")
	tl.result(false, "nope")
	tl.Close()
}

func TestBuildMarkerPrompt(t *testing.T) {
	prompt := buildMarkerPrompt("fix the off-by-one", "pkg/foo.go", 42, "for i := 0; i <= n; i++ {")
	for _, want := range []string{
		"pkg/foo.go:42",
		"```\nfor i := 0; i <= n; i++ {\n```",
		"removing it is part of the task",
		"fix the off-by-one",
		`"status"`,
		`"notes"`,
		"JSON object",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestCtxNotesTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if got := ctxNotes(ctx, Duration(15*time.Minute)); got != "timed out after 15m0s" {
		t.Errorf("expected 'timed out after 15m0s', got %q", got)
	}
}

func TestCtxNotesCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := ctxNotes(ctx, Duration(time.Minute)); got != "cancelled" {
		t.Errorf("expected 'cancelled', got %q", got)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %s", args, err, out)
	}
}

func initMergeTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "master")
	gitRun(t, dir, "config", "user.email", "test@test.com")
	gitRun(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// The real app gitignores .snags/ via EnsureSnagDir.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".snags/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "base")
	return dir
}

// startSnagBranch creates the worktree, applies an edit on the snag branch,
// and commits it — the state mergeStage expects on entry.
func startSnagBranch(t *testing.T, dir, snagID, content string) string {
	t.Helper()
	if err := createWorktree(dir, snagID, "master"); err != nil {
		t.Fatal(err)
	}
	wt := worktreePath(dir, snagID)
	if err := os.WriteFile(filepath.Join(wt, "file.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := commitWorktreeChanges(wt, "test snag"); err != nil {
		t.Fatal(err)
	}
	return wt
}

func branchExists(dir, branch string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--verify", branch).Run() == nil
}

func workingTreeClean(t *testing.T, dir string) bool {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out)) == ""
}

func TestMergeStageConflictPreservesBranch(t *testing.T) {
	dir := initMergeTestRepo(t)
	snag := Snag{ID: "abc123", Description: "change file"}
	wt := startSnagBranch(t, dir, snag.ID, "snag version\n")

	// Conflicting commit on master after the snag branch diverged.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("master version\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "conflicting change")

	msg := mergeStage(dir, "master", snag, "agent notes", DefaultConfig())
	if msg.success || !msg.mergeFailed {
		t.Fatalf("expected merge failure, got success=%v mergeFailed=%v notes=%q", msg.success, msg.mergeFailed, msg.notes)
	}
	if !strings.Contains(msg.notes, "merge conflict") || !strings.Contains(msg.notes, "snag/abc123 preserved") {
		t.Errorf("unexpected notes: %q", msg.notes)
	}
	if !workingTreeClean(t, dir) {
		t.Error("working tree not clean after git reset --merge")
	}
	if !branchExists(dir, "snag/abc123") {
		t.Error("branch snag/abc123 was deleted")
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("worktree still exists")
	}
}

func TestMergeStageMarkerDeleteErrorPreservesBranch(t *testing.T) {
	dir := initMergeTestRepo(t)
	// snag.File pointing at a directory makes DeleteMarker's ReadFile fail
	// with a non-IsNotExist error.
	if err := os.MkdirAll(filepath.Join(dir, "adir"), 0755); err != nil {
		t.Fatal(err)
	}
	snag := Snag{ID: "def456", Description: "fix thing", Source: SourceMarker, File: "adir", Line: 1}
	wt := startSnagBranch(t, dir, snag.ID, "snag version\n")

	msg := mergeStage(dir, "master", snag, "", DefaultConfig())
	if msg.success || !msg.mergeFailed {
		t.Fatalf("expected merge failure, got success=%v mergeFailed=%v notes=%q", msg.success, msg.mergeFailed, msg.notes)
	}
	if !strings.Contains(msg.notes, "marker removal failed") {
		t.Errorf("unexpected notes: %q", msg.notes)
	}
	if !branchExists(dir, "snag/def456") {
		t.Error("branch snag/def456 was deleted")
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("worktree still exists")
	}
}

func TestMergeStageSuccess(t *testing.T) {
	dir := initMergeTestRepo(t)
	snag := Snag{ID: "fed789", Description: "change file"}
	wt := startSnagBranch(t, dir, snag.ID, "snag version\n")

	msg := mergeStage(dir, "master", snag, "agent notes", DefaultConfig())
	if !msg.success || msg.mergeFailed {
		t.Fatalf("expected success, got success=%v mergeFailed=%v notes=%q", msg.success, msg.mergeFailed, msg.notes)
	}
	if msg.commitHash == "" || msg.commitHash != headCommitHash(dir) {
		t.Errorf("expected commitHash %q, got %q", headCommitHash(dir), msg.commitHash)
	}
	if msg.notes != "agent notes" {
		t.Errorf("unexpected notes: %q", msg.notes)
	}
	if branchExists(dir, "snag/fed789") {
		t.Error("branch snag/fed789 not deleted on success")
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("worktree still exists")
	}
	data, err := os.ReadFile(filepath.Join(dir, "file.txt"))
	if err != nil || string(data) != "snag version\n" {
		t.Errorf("merged content wrong: %q err=%v", data, err)
	}
}
