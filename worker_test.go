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

func TestDetectDefaultBranchFallsBackToCurrentBranch(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Run()
	}
	run("init", "-b", "dev")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-m", "init")
	run("checkout", "-b", "feature-x")

	branch := detectDefaultBranch(dir)
	if branch != "feature-x" {
		t.Errorf("expected feature-x, got %q", branch)
	}
}

func TestDetectDefaultBranchIgnoresUnresolvableOriginHead(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Run()
	}
	run("init", "-b", "dev")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-m", "init")
	// origin/HEAD points at origin/main, but no local main exists.
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	branch := detectDefaultBranch(dir)
	if branch != "dev" {
		t.Errorf("expected dev, got %q", branch)
	}
}

func TestBaseBranchForMarkerUsesCurrentBranch(t *testing.T) {
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
	run("checkout", "-b", "feature-x")

	branch, err := baseBranchFor(dir, Snag{Source: SourceMarker}, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "feature-x" {
		t.Errorf("expected feature-x, got %q", branch)
	}
}

func TestBaseBranchForInputUsesDefaultBranch(t *testing.T) {
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
	run("checkout", "-b", "feature-x")

	branch, err := baseBranchFor(dir, Snag{Source: SourceInput}, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "main" {
		t.Errorf("expected main, got %q", branch)
	}
}

func TestBaseBranchForMarkerDetachedHead(t *testing.T) {
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
	run("checkout", "--detach")

	if _, err := baseBranchFor(dir, Snag{Source: SourceMarker}, "main"); err == nil {
		t.Error("expected error on detached HEAD")
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
		"--exclude-dynamic-system-prompt-sections",
		"--tools", "Read,Edit,Write,Bash,Grep,Glob,Agent",
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

	tl := newTranscriptLogger(dir, snag.ID)
	msg := mergeStage(dir, "master", snag, "agent notes", DefaultConfig(), tl)
	tl.Close()
	if msg.success || !msg.mergeFailed {
		t.Fatalf("expected merge failure, got success=%v mergeFailed=%v notes=%q", msg.success, msg.mergeFailed, msg.notes)
	}
	if !strings.Contains(msg.notes, "merge conflict") || !strings.Contains(msg.notes, "snag/abc123 preserved") {
		t.Errorf("unexpected notes: %q", msg.notes)
	}
	// The failure must land in the transcript so the log doesn't end on the
	// agent's success while the snag shows failed.
	events := readTranscript(snagLogFile(dir, snag.ID))
	if len(events) == 0 {
		t.Fatal("expected a transcript event for the merge failure")
	}
	last := events[len(events)-1]
	if last.Type != "result" || last.Status != "failed" || !strings.Contains(last.Notes, "merge conflict") {
		t.Errorf("expected failed result event in transcript, got %+v", last)
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

	msg := mergeStage(dir, "master", snag, "", DefaultConfig(), nil)
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

	msg := mergeStage(dir, "master", snag, "agent notes", DefaultConfig(), nil)
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

func TestMergeStageNothingToMerge(t *testing.T) {
	dir := initMergeTestRepo(t)
	snag := Snag{ID: "noop01", Description: "do nothing"}
	// Branch with no commits beyond master: nothing to squash-merge.
	if err := createWorktree(dir, snag.ID, "master"); err != nil {
		t.Fatal(err)
	}

	msg := mergeStage(dir, "master", snag, "agent notes", DefaultConfig(), nil)
	if !msg.success || msg.mergeFailed {
		t.Fatalf("expected success, got success=%v mergeFailed=%v notes=%q", msg.success, msg.mergeFailed, msg.notes)
	}
	if msg.notes != "no code changes — agent notes" {
		t.Errorf("unexpected notes: %q", msg.notes)
	}
	if msg.commitHash != "" {
		t.Errorf("expected empty commitHash, got %q", msg.commitHash)
	}
	if branchExists(dir, "snag/noop01") {
		t.Error("branch snag/noop01 not deleted")
	}
	if _, err := os.Stat(worktreePath(dir, snag.ID)); !os.IsNotExist(err) {
		t.Error("worktree still exists")
	}
}

func TestMergeStageCommitFailureLeavesIndexClean(t *testing.T) {
	dir := initMergeTestRepo(t)
	snag := Snag{ID: "hook01", Description: "change file"}
	startSnagBranch(t, dir, snag.ID, "snag version\n")

	// A failing pre-commit hook makes the squash succeed but the commit fail.
	hooks := t.TempDir()
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "config", "core.hooksPath", hooks)

	msg := mergeStage(dir, "master", snag, "", DefaultConfig(), nil)
	if msg.success || !msg.mergeFailed {
		t.Fatalf("expected merge failure, got success=%v mergeFailed=%v notes=%q", msg.success, msg.mergeFailed, msg.notes)
	}
	if !strings.Contains(msg.notes, "commit") {
		t.Errorf("unexpected notes: %q", msg.notes)
	}
	if !branchExists(dir, "snag/hook01") {
		t.Error("branch snag/hook01 was deleted")
	}
	if hasStagedChanges(dir) {
		t.Error("staged squash left in index after commit failure")
	}
	if !workingTreeClean(t, dir) {
		t.Error("working tree not clean after commit failure")
	}

	// With the index reset, a later snag's merge must go through.
	gitRun(t, dir, "config", "--unset", "core.hooksPath")
	snag2 := Snag{ID: "hook02", Description: "second change"}
	startSnagBranch(t, dir, snag2.ID, "second version\n")
	if msg2 := mergeStage(dir, "master", snag2, "", DefaultConfig(), nil); !msg2.success {
		t.Fatalf("follow-up merge blocked: %q", msg2.notes)
	}
}

func TestMergeStageStagedChangesBlockMerge(t *testing.T) {
	dir := initMergeTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("user original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "add other")

	snag := Snag{ID: "stg001", Description: "change file"}
	startSnagBranch(t, dir, snag.ID, "snag version\n")

	// The user has a staged edit to an unrelated file when the snag finishes.
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("user edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "other.txt")

	msg := mergeStage(dir, "master", snag, "", DefaultConfig(), nil)
	if msg.success || !msg.mergeFailed {
		t.Fatalf("expected merge failure, got success=%v mergeFailed=%v notes=%q", msg.success, msg.mergeFailed, msg.notes)
	}
	if !strings.Contains(msg.notes, "staged changes") {
		t.Errorf("unexpected notes: %q", msg.notes)
	}
	if !branchExists(dir, "snag/stg001") {
		t.Error("branch snag/stg001 was deleted")
	}
	// The staged edit must survive untouched: still staged, content intact.
	cmd := exec.Command("git", "diff", "--cached", "--name-only")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "other.txt" {
		t.Errorf("staged files changed: %q", out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "other.txt"))
	if err != nil || string(data) != "user edit\n" {
		t.Errorf("staged edit content lost: %q err=%v", data, err)
	}
	// And the squash must not have run: file.txt untouched on master.
	data, err = os.ReadFile(filepath.Join(dir, "file.txt"))
	if err != nil || string(data) != "original\n" {
		t.Errorf("squash ran despite staged changes: %q err=%v", data, err)
	}
}

// The marker-scan design centerpiece: a marker file dirty only by the marker
// line in the working tree, while the agent branch modifies that same file.
// DeleteMarker brings the file back to HEAD, so the squash merge proceeds.
func TestMergeStageMarkerOnlyDirtyFileMerges(t *testing.T) {
	dir := initMergeTestRepo(t)
	snag := Snag{ID: "mark01", Description: "change file", Source: SourceMarker, File: "file.txt", Line: 2}
	startSnagBranch(t, dir, snag.ID, "snag version\n")

	// The marker lives only in the working tree (it was never committed);
	// file.txt is dirty solely by the marker line.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("original\n// snag: change file\n"), 0644); err != nil {
		t.Fatal(err)
	}

	msg := mergeStage(dir, "master", snag, "notes", DefaultConfig(), nil)
	if !msg.success || msg.mergeFailed {
		t.Fatalf("expected success, got success=%v mergeFailed=%v notes=%q", msg.success, msg.mergeFailed, msg.notes)
	}
	if msg.commitHash == "" || msg.commitHash != headCommitHash(dir) {
		t.Errorf("expected commitHash %q, got %q", headCommitHash(dir), msg.commitHash)
	}
	data, err := os.ReadFile(filepath.Join(dir, "file.txt"))
	if err != nil || string(data) != "snag version\n" {
		t.Errorf("merged content wrong: %q err=%v", data, err)
	}
	if branchExists(dir, "snag/mark01") {
		t.Error("branch snag/mark01 not deleted on success")
	}
}

// writeStubClaude installs a fake `claude` on PATH whose body is the given
// shell script. It runs with cwd set by runClaudeHeadless (the project root).
func writeStubClaude(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const stubSuccessResult = `echo '{"type":"result","structured_output":{"status":"success","notes":"done"}}'`

func TestAgenticMergeCmdNoCommitIsFailure(t *testing.T) {
	dir := initMergeTestRepo(t)
	snag := Snag{ID: "agm001", Description: "change file"}
	startSnagBranch(t, dir, snag.ID, "snag version\n")
	removeWorktreeOnly(dir, snag.ID) // preserved branch, as after a merge failure

	// Agent claims success without committing anything.
	writeStubClaude(t, stubSuccessResult)
	msg, ok := agenticMergeCmd(context.Background(), dir, "master", DefaultConfig(), snag)().(mergeDoneMsg)
	if !ok {
		t.Fatal("expected mergeDoneMsg")
	}
	if msg.success {
		t.Fatal("expected failure when the agent claims success but HEAD did not advance")
	}
	if !strings.Contains(msg.errMsg, "no commit was created") {
		t.Errorf("unexpected errMsg: %q", msg.errMsg)
	}
	if msg.commitHash != "" {
		t.Errorf("expected empty commitHash, got %q", msg.commitHash)
	}
	if !branchExists(dir, "snag/agm001") {
		t.Error("branch snag/agm001 deleted despite no commit landing")
	}
}

func TestAgenticMergeCmdVerifiedCommitSucceeds(t *testing.T) {
	dir := initMergeTestRepo(t)
	snag := Snag{ID: "agm002", Description: "change file"}
	startSnagBranch(t, dir, snag.ID, "snag version\n")
	removeWorktreeOnly(dir, snag.ID)
	preHead := headCommitHash(dir)

	writeStubClaude(t,
		"git merge --squash snag/agm002 >/dev/null 2>&1\n"+
			"git commit -m 'snag: change file' >/dev/null 2>&1\n"+
			stubSuccessResult)
	msg := agenticMergeCmd(context.Background(), dir, "master", DefaultConfig(), snag)().(mergeDoneMsg)
	if !msg.success {
		t.Fatalf("expected success, got errMsg=%q", msg.errMsg)
	}
	if msg.commitHash == "" || msg.commitHash == preHead || msg.commitHash != headCommitHash(dir) {
		t.Errorf("expected new HEAD as commitHash, got %q (preHead %q)", msg.commitHash, preHead)
	}
	if branchExists(dir, "snag/agm002") {
		t.Error("branch snag/agm002 not deleted after verified merge")
	}
}

func TestAgenticMergeCmdForeignCommitIsFailure(t *testing.T) {
	dir := initMergeTestRepo(t)
	snag := Snag{ID: "agm004", Description: "change file"}
	startSnagBranch(t, dir, snag.ID, "snag version\n")
	removeWorktreeOnly(dir, snag.ID)

	// HEAD advances during the run — but by a user-style commit, not a snag
	// commit. The agent's success claim must not be trusted.
	writeStubClaude(t,
		"echo 'user work' > user.txt\n"+
			"git add user.txt >/dev/null 2>&1\n"+
			"git commit -m 'unrelated user commit' >/dev/null 2>&1\n"+
			stubSuccessResult)
	msg := agenticMergeCmd(context.Background(), dir, "master", DefaultConfig(), snag)().(mergeDoneMsg)
	if msg.success {
		t.Fatal("expected failure when HEAD advanced without a snag commit")
	}
	if !strings.Contains(msg.errMsg, "no snag commit landed") {
		t.Errorf("unexpected errMsg: %q", msg.errMsg)
	}
	if msg.commitHash != "" {
		t.Errorf("expected empty commitHash, got %q", msg.commitHash)
	}
	if !branchExists(dir, "snag/agm004") {
		t.Error("branch snag/agm004 deleted despite no snag commit landing")
	}
}

func TestAgenticMergeCmdWrongBranch(t *testing.T) {
	dir := initMergeTestRepo(t)
	snag := Snag{ID: "agm006", Description: "change file"}
	startSnagBranch(t, dir, snag.ID, "snag version\n")
	removeWorktreeOnly(dir, snag.ID)
	preHead := headCommitHash(dir)
	gitRun(t, dir, "checkout", "-b", "feature")

	// The stub would merge and commit if it ran; the guard must stop it first.
	writeStubClaude(t,
		"git merge --squash snag/agm006 >/dev/null 2>&1\n"+
			"git commit -m 'snag: change file' >/dev/null 2>&1\n"+
			stubSuccessResult)
	msg, ok := agenticMergeCmd(context.Background(), dir, "master", DefaultConfig(), snag)().(mergeDoneMsg)
	if !ok {
		t.Fatal("expected mergeDoneMsg")
	}
	if msg.success {
		t.Fatal("expected failure when not on the default branch")
	}
	if !strings.Contains(msg.errMsg, "not on master (currently on feature)") {
		t.Errorf("unexpected errMsg: %q", msg.errMsg)
	}
	if headCommitHash(dir) != preHead {
		t.Error("HEAD must not move when the guard trips")
	}
	if !branchExists(dir, "snag/agm006") {
		t.Error("branch snag/agm006 must be preserved")
	}
}

func TestAgenticMergeCmdRecordsSnagCommitHash(t *testing.T) {
	dir := initMergeTestRepo(t)
	snag := Snag{ID: "agm007", Description: "change file"}
	startSnagBranch(t, dir, snag.ID, "snag version\n")
	removeWorktreeOnly(dir, snag.ID)

	// The snag commit lands, then a user commit on top before the run ends:
	// HEAD is the user's commit, not the snag's. A revert of the recorded hash
	// must undo the snag, not the user's work.
	writeStubClaude(t,
		"git merge --squash snag/agm007 >/dev/null 2>&1\n"+
			"git commit -m 'snag: change file' >/dev/null 2>&1\n"+
			"echo 'user work' > user.txt\n"+
			"git add user.txt >/dev/null 2>&1\n"+
			"git commit -m 'user commit mid-run' >/dev/null 2>&1\n"+
			stubSuccessResult)
	msg := agenticMergeCmd(context.Background(), dir, "master", DefaultConfig(), snag)().(mergeDoneMsg)
	if !msg.success {
		t.Fatalf("expected success, got errMsg=%q", msg.errMsg)
	}
	cmd := exec.Command("git", "rev-parse", "HEAD^")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	snagHash := strings.TrimSpace(string(out))
	if msg.commitHash != snagHash {
		t.Errorf("expected snag commit hash %q, got %q", snagHash, msg.commitHash)
	}
	if msg.commitHash == headCommitHash(dir) {
		t.Error("recorded hash must not be the user's commit at HEAD")
	}
	if branchExists(dir, "snag/agm007") {
		t.Error("branch snag/agm007 not deleted after verified merge")
	}
}

func TestAgenticMergeCmdFailureResetsConflict(t *testing.T) {
	dir := initMergeTestRepo(t)
	snag := Snag{ID: "agm003", Description: "change file"}
	startSnagBranch(t, dir, snag.ID, "snag version\n")
	removeWorktreeOnly(dir, snag.ID)
	// Conflicting commit on master after the branch diverged.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("master version\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "conflicting change")

	// Agent starts the merge, hits the conflict, and gives up.
	writeStubClaude(t,
		"git merge --squash snag/agm003 >/dev/null 2>&1\n"+
			`echo '{"type":"result","structured_output":{"status":"failed","notes":"could not resolve"}}'`)
	msg := agenticMergeCmd(context.Background(), dir, "master", DefaultConfig(), snag)().(mergeDoneMsg)
	if msg.success {
		t.Fatal("expected failure")
	}
	if !strings.Contains(msg.errMsg, "could not resolve") || !strings.Contains(msg.errMsg, "reset --merge") {
		t.Errorf("unexpected errMsg: %q", msg.errMsg)
	}
	if hasUnmergedPaths(dir) {
		t.Error("unmerged paths left behind")
	}
	if !workingTreeClean(t, dir) {
		t.Error("working tree left mid-conflict")
	}
	if !branchExists(dir, "snag/agm003") {
		t.Error("branch snag/agm003 must be preserved on failure")
	}
}

func TestAgenticMergeCmdCancelled(t *testing.T) {
	dir := initMergeTestRepo(t)
	snag := Snag{ID: "agm005", Description: "change file"}
	startSnagBranch(t, dir, snag.ID, "snag version\n")
	removeWorktreeOnly(dir, snag.ID)
	preHead := headCommitHash(dir)

	// Quit cancels the context before/while the agent runs; the merge must
	// fail without touching the default branch, and the branch must survive.
	writeStubClaude(t, "sleep 5\n"+stubSuccessResult)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	msg, ok := agenticMergeCmd(ctx, dir, "master", DefaultConfig(), snag)().(mergeDoneMsg)
	if !ok {
		t.Fatal("expected mergeDoneMsg")
	}
	if msg.success {
		t.Fatal("expected failure on cancellation")
	}
	if msg.errMsg != "cancelled" {
		t.Errorf("expected errMsg 'cancelled', got %q", msg.errMsg)
	}
	if headCommitHash(dir) != preHead {
		t.Error("HEAD must not move on a cancelled merge")
	}
	if !branchExists(dir, "snag/agm005") {
		t.Error("branch snag/agm005 must be preserved on cancellation")
	}
}

func TestRunClaudeHeadlessLargeStreamLine(t *testing.T) {
	// A single ~1MB stream line — over the old 256KB scanner cap.
	writeStubClaude(t,
		`printf '{"type":"assistant","message":{"content":[{"type":"text","text":"'`+"\n"+
			`head -c 1048576 /dev/zero | tr '\0' 'x'`+"\n"+
			`printf '"}]}}\n'`+"\n"+
			stubSuccessResult)
	success, notes, err := runClaudeHeadless(context.Background(), t.TempDir(), "p", AgentConfig{Model: "m"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !success {
		t.Fatalf("expected success, notes=%q", notes)
	}
	if notes != "done" {
		t.Errorf("unexpected notes: %q", notes)
	}
}

func TestRunClaudeHeadlessScannerErrorSurfaced(t *testing.T) {
	// A single >4MB line overflows the scanner; with no result, the scanner
	// error must surface in notes instead of "no result from claude".
	writeStubClaude(t, `head -c 5242880 /dev/zero | tr '\0' 'x'; echo`)
	success, notes, err := runClaudeHeadless(context.Background(), t.TempDir(), "p", AgentConfig{Model: "m"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if success {
		t.Fatal("expected failure")
	}
	if !strings.Contains(notes, "token too long") {
		t.Errorf("expected scanner error in notes, got %q", notes)
	}
}

// A marker snag merges even when the live tree is dirty with an UNRELATED
// uncommitted change in the same file (the case git merge --squash aborts on).
func TestMergeStageMarkerMergesOverDirtyTree(t *testing.T) {
	dir := initMergeTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "file.txt"),
		[]byte("top\nmiddle\nbottom\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "commit", "-am", "multiline")

	snag := Snag{ID: "mk1", Description: "tweak bottom", Source: SourceMarker, File: "file.txt", Line: 3}
	startSnagBranch(t, dir, snag.ID, "top\nmiddle\nbottom EDITED\n")

	if err := os.WriteFile(filepath.Join(dir, "file.txt"),
		[]byte("top WIP\nmiddle\n// snag: tweak bottom\nbottom\n"), 0644); err != nil {
		t.Fatal(err)
	}

	msg := mergeStage(dir, "master", snag, "notes", DefaultConfig(), nil)
	if !msg.success || msg.mergeFailed || msg.conflict {
		t.Fatalf("expected clean success, got success=%v mergeFailed=%v conflict=%v notes=%q",
			msg.success, msg.mergeFailed, msg.conflict, msg.notes)
	}
	if msg.commitHash == "" || msg.commitHash != headCommitHash(dir) {
		t.Errorf("commitHash %q != HEAD %q", msg.commitHash, headCommitHash(dir))
	}
	got, _ := os.ReadFile(filepath.Join(dir, "file.txt"))
	want := "top WIP\nmiddle\nbottom EDITED\n"
	if string(got) != want {
		t.Errorf("live tree = %q, want %q", got, want)
	}
	if branchExists(dir, "snag/mk1") {
		t.Error("branch should be deleted on full success")
	}
}

// A true same-line overlap lands the commit, leaves markers, preserves branch.
func TestMergeStageMarkerConflictLeavesMarkersAndKeepsBranch(t *testing.T) {
	dir := initMergeTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("shared\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "commit", "-am", "base")

	snag := Snag{ID: "mk2", Description: "change shared", Source: SourceMarker, File: "file.txt", Line: 1}
	startSnagBranch(t, dir, snag.ID, "shared AGENT\n")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("shared WIP\n"), 0644); err != nil {
		t.Fatal(err)
	}

	msg := mergeStage(dir, "master", snag, "notes", DefaultConfig(), nil)
	if !msg.success || !msg.conflict || msg.mergeFailed {
		t.Fatalf("expected success+conflict, got success=%v conflict=%v mergeFailed=%v notes=%q",
			msg.success, msg.conflict, msg.mergeFailed, msg.notes)
	}
	if msg.commitHash == "" {
		t.Error("expected snag commit to have landed")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "file.txt"))
	if !strings.Contains(string(got), "<<<<<<<") {
		t.Errorf("expected conflict markers in live tree, got %q", got)
	}
	if !branchExists(dir, "snag/mk2") {
		t.Error("branch should be preserved on conflict")
	}
}
