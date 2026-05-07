# Snags TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go TUI app (`snags`) that manages a queue of small autonomous code changes worked on by Claude Code in isolated git worktrees.

**Architecture:** Single Go binary using Bubbletea for the TUI. Background work runs via `tea.Cmd` goroutines that launch `claude` as a subprocess; results return to the event loop via `tea.Msg`. State is persisted to `.snags/state.yaml` in the target project directory.

**Tech Stack:** Go 1.22, `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/bubbles` (textinput, spinner, key), `github.com/charmbracelet/lipgloss`, `gopkg.in/yaml.v3`

---

## File Map

| File | Responsibility |
|------|---------------|
| `main.go` | Entry point: launch checks (git repo, claude in PATH), load state, run Bubbletea program |
| `state.go` | `Snag` struct, `State` struct, YAML load/save, `.snags/` dir setup |
| `state_test.go` | Tests for load/save roundtrip, inflight reset, EnsureSnagDir |
| `keys.go` | `keyMap` struct and binding definitions |
| `worker.go` | Git helpers (worktree, merge), claude subprocess invocation, `RunSnag` tea.Cmd |
| `worker_test.go` | Tests for JSON output parsing, `detectDefaultBranch` |
| `model.go` | Bubbletea `Model`, `Init`, `Update`, `View` — all TUI logic |
| `model_test.go` | Tests for Update logic: add snag, delete, navigate, pause, snagDoneMsg |

---

## Task 1: Project Bootstrap

**Files:**
- Create: `go.mod`
- Create: `go.sum` (generated)
- Create: `main.go`, `state.go`, `state_test.go`, `keys.go`, `worker.go`, `worker_test.go`, `model.go`, `model_test.go` (stubs)

- [ ] **Step 1: Initialise git and Go module**

```bash
cd /Users/alex/snags
git init
go mod init snags
```

- [ ] **Step 2: Add dependencies**

```bash
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/bubbles@latest
go get github.com/charmbracelet/lipgloss@latest
go get gopkg.in/yaml.v3@latest
```

- [ ] **Step 3: Create stub files so the package compiles**

Create `main.go`:
```go
package main

func main() {}
```

Create `state.go`:
```go
package main
```

Create `state_test.go`:
```go
package main
```

Create `keys.go`:
```go
package main
```

Create `worker.go`:
```go
package main
```

Create `worker_test.go`:
```go
package main
```

Create `model.go`:
```go
package main
```

Create `model_test.go`:
```go
package main
```

- [ ] **Step 4: Verify it compiles**

```bash
go build ./...
```

Expected: no output (success)

- [ ] **Step 5: Create .gitignore and commit**

```bash
echo "snags" > .gitignore
git add .
git commit -m "chore: bootstrap Go module with dependencies"
```

---

## Task 2: State Layer (TDD)

**Files:**
- Write: `state_test.go`
- Write: `state.go`

- [ ] **Step 1: Write failing tests**

Replace `state_test.go` with:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadStateMissingFile(t *testing.T) {
	dir := t.TempDir()
	state, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Snags) != 0 {
		t.Errorf("expected empty state, got %d snags", len(state.Snags))
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".snags"), 0755); err != nil {
		t.Fatal(err)
	}
	state := State{Snags: []Snag{
		{ID: "abc123", Description: "test snag", Status: StatusPending, CreatedAt: time.Now().UTC().Truncate(time.Second)},
	}}
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Snags) != 1 {
		t.Fatalf("expected 1 snag, got %d", len(loaded.Snags))
	}
	if loaded.Snags[0].Description != "test snag" {
		t.Errorf("description mismatch: %q", loaded.Snags[0].Description)
	}
	if loaded.Snags[0].Status != StatusPending {
		t.Errorf("status mismatch: %q", loaded.Snags[0].Status)
	}
}

func TestInflightResetOnLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".snags"), 0755); err != nil {
		t.Fatal(err)
	}
	state := State{Snags: []Snag{
		{ID: "abc", Description: "was running", Status: StatusInflight},
	}}
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Snags[0].Status != StatusPending {
		t.Errorf("expected inflight to be reset to pending, got %q", loaded.Snags[0].Status)
	}
}

func TestEnsureSnagDirCreatesDir(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureSnagDir(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".snags")); os.IsNotExist(err) {
		t.Error("expected .snags/ directory to exist")
	}
}

func TestEnsureSnagDirAddsGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureSnagDir(dir); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), ".snags/") {
		t.Error("expected .snags/ in .gitignore")
	}
}

func TestEnsureSnagDirIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureSnagDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSnagDir(dir); err != nil {
		t.Error("second call should not error")
	}
	content, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	count := strings.Count(string(content), ".snags/")
	if count != 1 {
		t.Errorf("expected .snags/ to appear once in .gitignore, got %d", count)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
go test ./... -run TestLoad
```

Expected: compile error (types/functions not defined yet)

- [ ] **Step 3: Implement state.go**

Replace `state.go` with:

```go
package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type SnagStatus string

const (
	StatusPending  SnagStatus = "pending"
	StatusInflight SnagStatus = "inflight"
	StatusComplete SnagStatus = "complete"
	StatusFailed   SnagStatus = "failed"
)

type Snag struct {
	ID          string     `yaml:"id"`
	Description string     `yaml:"description"`
	Status      SnagStatus `yaml:"status"`
	CreatedAt   time.Time  `yaml:"created_at"`
	Branch      string     `yaml:"branch,omitempty"`
	Notes       string     `yaml:"notes,omitempty"`
}

type State struct {
	Snags []Snag `yaml:"snags"`
}

func generateID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func snagDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".snags")
}

func stateFile(projectRoot string) string {
	return filepath.Join(snagDir(projectRoot), "state.yaml")
}

func LoadState(projectRoot string) (State, error) {
	data, err := os.ReadFile(stateFile(projectRoot))
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := yaml.Unmarshal(data, &s); err != nil {
		return State{}, err
	}
	for i := range s.Snags {
		if s.Snags[i].Status == StatusInflight {
			s.Snags[i].Status = StatusPending
		}
	}
	return s, nil
}

func SaveState(projectRoot string, s State) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(stateFile(projectRoot), data, 0644)
}

func EnsureSnagDir(projectRoot string) error {
	if err := os.MkdirAll(snagDir(projectRoot), 0755); err != nil {
		return err
	}
	gitignorePath := filepath.Join(projectRoot, ".gitignore")
	content, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(content), ".snags/") {
		return nil
	}
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	prefix := ""
	if len(content) > 0 && content[len(content)-1] != '\n' {
		prefix = "\n"
	}
	_, err = fmt.Fprintf(f, "%s.snags/\n", prefix)
	return err
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./... -v -run "TestLoad|TestSave|TestInflight|TestEnsure"
```

Expected: all 6 tests PASS

- [ ] **Step 5: Commit**

```bash
git add state.go state_test.go
git commit -m "feat: add state persistence layer with YAML and snag lifecycle"
```

---

## Task 3: Key Bindings

**Files:**
- Write: `keys.go`

- [ ] **Step 1: Write keys.go**

```go
package main

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Up          key.Binding
	Down        key.Binding
	Delete      key.Binding
	Enter       key.Binding
	Escape      key.Binding
	PauseResume key.Binding
	Quit        key.Binding
}

var keys = keyMap{
	Up: key.NewBinding(
		key.WithKeys("up"),
		key.WithHelp("↑", "up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down"),
		key.WithHelp("↓", "down"),
	),
	Delete: key.NewBinding(
		key.WithKeys("backspace"),
		key.WithHelp("backspace", "delete snag"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "add snag"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "clear / quit"),
	),
	PauseResume: key.NewBinding(
		key.WithKeys("ctrl+p"),
		key.WithHelp("ctrl+p", "pause/resume"),
	),
	Quit: key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "quit"),
	),
}
```

- [ ] **Step 2: Verify compiles**

```bash
go build ./...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add keys.go
git commit -m "feat: add key binding definitions"
```

---

## Task 4: Worker Layer (TDD)

**Files:**
- Write: `worker_test.go`
- Write: `worker.go`

- [ ] **Step 1: Write failing tests**

Replace `worker_test.go` with:

```go
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
```

- [ ] **Step 2: Run to confirm compile failure**

```bash
go test ./... -run TestParse 2>&1 | head -5
```

Expected: compile error — `claudeOutput` and `detectDefaultBranch` undefined

- [ ] **Step 3: Write worker.go**

```go
package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type snagDoneMsg struct {
	snagID  string
	success bool
	notes   string
}

type claudeOutput struct {
	StructuredOutput struct {
		Status string `json:"status"`
		Notes  string `json:"notes"`
	} `json:"structured_output"`
}

func detectDefaultBranch(projectRoot string) string {
	cmd := exec.Command("git", "-C", projectRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if out, err := cmd.Output(); err == nil {
		parts := strings.SplitN(strings.TrimSpace(string(out)), "/", 2)
		if len(parts) == 2 {
			return parts[1]
		}
		return strings.TrimSpace(string(out))
	}
	for _, b := range []string{"main", "master"} {
		if exec.Command("git", "-C", projectRoot, "rev-parse", "--verify", b).Run() == nil {
			return b
		}
	}
	return "main"
}

func worktreePath(projectRoot, snagID string) string {
	return filepath.Join(projectRoot, ".snags", "worktrees", snagID)
}

func createWorktree(projectRoot, snagID, defaultBranch string) error {
	cmd := exec.Command("git", "-C", projectRoot, "worktree", "add",
		worktreePath(projectRoot, snagID), "-b", "snag/"+snagID, defaultBranch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("worktree add: %s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func removeWorktree(projectRoot, snagID string) {
	exec.Command("git", "-C", projectRoot, "worktree", "remove", "--force",
		worktreePath(projectRoot, snagID)).Run()
	exec.Command("git", "-C", projectRoot, "branch", "-D", "snag/"+snagID).Run()
}

func buildPrompt(description string) string {
	return fmt.Sprintf(`You are working autonomously in a git worktree to complete a small code change (a "snag").
The project is already checked out. Do not ask for clarification — use your best judgement.

Snag: %s

Complete the task fully. Your final response must be a JSON object with:
- "status": "success" if the task is complete, or "failed" if you could not complete it
- "notes": any assumptions you made, decisions you took, or (if failed) why you could not complete it`, description)
}

func runClaudeHeadless(dir, prompt string) (success bool, notes string, err error) {
	const schema = `{"type":"object","properties":{"status":{"type":"string","enum":["success","failed"]},"notes":{"type":"string"}},"required":["status"]}`
	cmd := exec.Command("claude",
		"--model", "claude-sonnet-4-6",
		"-p", prompt,
		"--output-format", "json",
		"--json-schema", schema,
		"--permission-mode", "auto",
		"--settings", `{"autoMode":{"environment":["$defaults"]}}`,
		"--no-update-notification",
	)
	cmd.Dir = dir
	out, runErr := cmd.Output()
	if runErr != nil {
		stderr := ""
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(exitErr.Stderr))
		}
		if stderr == "" {
			stderr = runErr.Error()
		}
		return false, stderr, nil
	}
	var result claudeOutput
	if jsonErr := json.Unmarshal(out, &result); jsonErr != nil {
		return false, fmt.Sprintf("failed to parse claude output: %s", jsonErr), nil
	}
	return result.StructuredOutput.Status == "success", result.StructuredOutput.Notes, nil
}

func squashMerge(projectRoot, snagID, description, notes string) error {
	cmd := exec.Command("git", "merge", "--squash", "snag/"+snagID)
	cmd.Dir = projectRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	args := []string{"commit", "-m", "snag: " + description}
	if notes != "" {
		args = append(args, "-m", notes)
	}
	cmd = exec.Command("git", args...)
	cmd.Dir = projectRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("commit: %s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runConflictResolver(projectRoot, snagID, description string) error {
	prompt := fmt.Sprintf(
		"A git merge --squash conflict occurred while merging branch snag/%s. "+
			"Resolve the conflicts in the working tree, then run: git commit -m \"snag: %s\"",
		snagID, description,
	)
	success, notes, err := runClaudeHeadless(projectRoot, prompt)
	if err != nil {
		return err
	}
	if !success {
		return fmt.Errorf("%s", notes)
	}
	return nil
}

func RunSnag(projectRoot, defaultBranch string, snag Snag) tea.Cmd {
	return func() tea.Msg {
		if err := createWorktree(projectRoot, snag.ID, defaultBranch); err != nil {
			return snagDoneMsg{snagID: snag.ID, success: false, notes: err.Error()}
		}

		success, notes, err := runClaudeHeadless(worktreePath(projectRoot, snag.ID), buildPrompt(snag.Description))
		if err != nil {
			removeWorktree(projectRoot, snag.ID)
			return snagDoneMsg{snagID: snag.ID, success: false, notes: err.Error()}
		}
		if !success {
			removeWorktree(projectRoot, snag.ID)
			return snagDoneMsg{snagID: snag.ID, success: false, notes: notes}
		}

		if mergeErr := squashMerge(projectRoot, snag.ID, snag.Description, notes); mergeErr != nil {
			if resolveErr := runConflictResolver(projectRoot, snag.ID, snag.Description); resolveErr != nil {
				return snagDoneMsg{snagID: snag.ID, success: false,
					notes: fmt.Sprintf("merge conflict unresolved: %s", resolveErr)}
			}
		}

		removeWorktree(projectRoot, snag.ID)
		return snagDoneMsg{snagID: snag.ID, success: true, notes: notes}
	}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./... -v -run "TestParse|TestDetect"
```

Expected: all 4 tests PASS

- [ ] **Step 5: Commit**

```bash
git add worker.go worker_test.go
git commit -m "feat: add worker layer — git ops, claude invocation, RunSnag cmd"
```

---

## Task 5: Model (TDD)

**Files:**
- Write: `model_test.go`
- Write: `model.go`

- [ ] **Step 1: Write failing tests**

Replace `model_test.go` with:

```go
package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestModel(snags []Snag) Model {
	return New("/tmp/testproj", "main", State{Snags: snags})
}

func update(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func keyMsg(k tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: k}
}

// --- Entry field ---

func TestAddSnag(t *testing.T) {
	m := newTestModel(nil)
	m.input.SetValue("fix the flaky test")
	m = update(m, keyMsg(tea.KeyEnter))

	visible := m.visibleSnags()
	if len(visible) != 1 {
		t.Fatalf("expected 1 snag, got %d", len(visible))
	}
	if visible[0].Description != "fix the flaky test" {
		t.Errorf("wrong description: %q", visible[0].Description)
	}
	if visible[0].Status != StatusPending {
		t.Errorf("expected pending, got %q", visible[0].Status)
	}
}

func TestEnterWithEmptyFieldDoesNothing(t *testing.T) {
	m := newTestModel(nil)
	m = update(m, keyMsg(tea.KeyEnter))
	if len(m.visibleSnags()) != 0 {
		t.Error("should not add empty snag")
	}
}

func TestEscClearsNonEmptyInput(t *testing.T) {
	m := newTestModel(nil)
	m.input.SetValue("some text")
	m = update(m, keyMsg(tea.KeyEsc))
	if m.input.Value() != "" {
		t.Errorf("expected empty input, got %q", m.input.Value())
	}
}

func TestEscOnEmptyInputQuitsApp(t *testing.T) {
	m := newTestModel(nil)
	_, cmd := m.Update(keyMsg(tea.KeyEsc))
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected QuitMsg, got %T", msg)
	}
}

// --- List navigation ---

func TestUpMovesToList(t *testing.T) {
	m := newTestModel([]Snag{{ID: "a", Status: StatusPending, Description: "snag"}})
	// default focus is input
	m = update(m, keyMsg(tea.KeyUp))
	if m.focus != focusList {
		t.Error("expected focus to move to list after pressing up")
	}
	if m.cursor != 0 {
		t.Errorf("expected cursor 0, got %d", m.cursor)
	}
}

func TestUpOnEmptyListStaysInInput(t *testing.T) {
	m := newTestModel(nil)
	m = update(m, keyMsg(tea.KeyUp))
	if m.focus != focusInput {
		t.Error("should stay in input when list is empty")
	}
}

func TestDownFromLastItemReturnsToInput(t *testing.T) {
	m := newTestModel([]Snag{{ID: "a", Status: StatusPending, Description: "snag"}})
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyDown))
	if m.focus != focusInput {
		t.Error("expected focus to return to input")
	}
}

func TestNavigateBetweenItems(t *testing.T) {
	snags := []Snag{
		{ID: "a", Status: StatusPending, Description: "first"},
		{ID: "b", Status: StatusPending, Description: "second"},
	}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyDown))
	if m.cursor != 1 {
		t.Errorf("expected cursor 1 after down, got %d", m.cursor)
	}
	m = update(m, keyMsg(tea.KeyUp))
	if m.cursor != 0 {
		t.Errorf("expected cursor 0 after up, got %d", m.cursor)
	}
}

// --- Delete ---

func TestDeletePendingSnag(t *testing.T) {
	snags := []Snag{
		{ID: "a", Status: StatusPending, Description: "first"},
		{ID: "b", Status: StatusPending, Description: "second"},
	}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyBackspace))

	visible := m.visibleSnags()
	if len(visible) != 1 {
		t.Fatalf("expected 1 snag after delete, got %d", len(visible))
	}
	if visible[0].ID != "b" {
		t.Errorf("expected snag b to remain")
	}
}

func TestDeleteInflightSnagNoOp(t *testing.T) {
	snags := []Snag{{ID: "a", Status: StatusInflight, Description: "running"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyBackspace))
	if len(m.visibleSnags()) != 1 {
		t.Error("inflight snag should not be deletable")
	}
}

func TestDeleteLastSnagReturnsFocusToInput(t *testing.T) {
	snags := []Snag{{ID: "a", Status: StatusPending, Description: "only one"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyBackspace))
	if m.focus != focusInput {
		t.Error("expected focus to return to input after deleting last snag")
	}
}

// --- Pause/resume ---

func TestPauseResume(t *testing.T) {
	m := newTestModel(nil)
	m = update(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.paused {
		t.Error("expected paused after ctrl+p")
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.paused {
		t.Error("expected unpaused after second ctrl+p")
	}
}

// --- Worker result ---

func TestSnagDoneMsgSuccess(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusInflight, Description: "the task"}}
	m := newTestModel(snags)
	m.working = true
	m = update(m, snagDoneMsg{snagID: "abc", success: true, notes: "done"})

	var found *Snag
	for i := range m.state.Snags {
		if m.state.Snags[i].ID == "abc" {
			found = &m.state.Snags[i]
		}
	}
	if found == nil {
		t.Fatal("snag not found in state")
	}
	if found.Status != StatusComplete {
		t.Errorf("expected complete, got %q", found.Status)
	}
	if found.Notes != "done" {
		t.Errorf("expected notes 'done', got %q", found.Notes)
	}
	if m.working {
		t.Error("expected working=false after done")
	}
}

func TestSnagDoneMsgFailure(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusInflight, Description: "the task"}}
	m := newTestModel(snags)
	m.working = true
	m = update(m, snagDoneMsg{snagID: "abc", success: false, notes: "could not find file"})

	var found *Snag
	for i := range m.state.Snags {
		if m.state.Snags[i].ID == "abc" {
			found = &m.state.Snags[i]
		}
	}
	if found.Status != StatusFailed {
		t.Errorf("expected failed, got %q", found.Status)
	}
	// Failed snags are visible
	visible := m.visibleSnags()
	if len(visible) != 1 {
		t.Error("failed snag should still be visible")
	}
}

// --- Visible snags filter ---

func TestCompleteSnagsHidden(t *testing.T) {
	snags := []Snag{
		{ID: "a", Status: StatusComplete},
		{ID: "b", Status: StatusPending},
		{ID: "c", Status: StatusFailed},
	}
	m := newTestModel(snags)
	visible := m.visibleSnags()
	if len(visible) != 2 {
		t.Fatalf("expected 2 visible snags (pending+failed), got %d", len(visible))
	}
	for _, s := range visible {
		if s.Status == StatusComplete {
			t.Error("complete snag should not be visible")
		}
	}
}
```

- [ ] **Step 2: Run to confirm compile failure**

```bash
go test ./... -run TestAdd 2>&1 | head -5
```

Expected: compile error — `Model`, `New`, `focusList`, `focusInput`, etc. undefined

- [ ] **Step 3: Write model.go**

```go
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const maxVisible = 10

type focusArea int

const (
	focusList  focusArea = iota
	focusInput
)

type startWorkMsg struct{}

var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	faintStyle = lipgloss.NewStyle().Faint(true)
	redStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	boldStyle  = lipgloss.NewStyle().Bold(true)
)

type Model struct {
	state         State
	cursor        int
	viewOffset    int
	focus         focusArea
	input         textinput.Model
	spinner       spinner.Model
	paused        bool
	working       bool
	projectRoot   string
	defaultBranch string
	width         int
}

func New(projectRoot, defaultBranch string, state State) Model {
	ti := textinput.New()
	ti.Placeholder = "describe a snag..."
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return Model{
		state:         state,
		focus:         focusInput,
		input:         ti,
		spinner:       sp,
		projectRoot:   projectRoot,
		defaultBranch: defaultBranch,
		width:         80,
	}
}

func (m Model) visibleSnags() []Snag {
	var out []Snag
	for _, s := range m.state.Snags {
		if s.Status != StatusComplete {
			out = append(out, s)
		}
	}
	return out
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		func() tea.Msg { return startWorkMsg{} },
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)

	case startWorkMsg:
		var cmd tea.Cmd
		m, cmd = m.startNextSnag()
		cmds = append(cmds, cmd)

	case snagDoneMsg:
		m.working = false
		for i := range m.state.Snags {
			if m.state.Snags[i].ID == msg.snagID {
				if msg.success {
					m.state.Snags[i].Status = StatusComplete
					m.state.Snags[i].Branch = "snag/" + msg.snagID
				} else {
					m.state.Snags[i].Status = StatusFailed
				}
				m.state.Snags[i].Notes = msg.notes
				break
			}
		}
		var workCmd tea.Cmd
		m, workCmd = m.startNextSnag()
		cmds = append(cmds, saveCmd(m.projectRoot, m.state), workCmd)

	case tea.KeyMsg:
		forwardToInput := false

		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit

		case key.Matches(msg, keys.PauseResume):
			m.paused = !m.paused
			if !m.paused && !m.working {
				var workCmd tea.Cmd
				m, workCmd = m.startNextSnag()
				cmds = append(cmds, workCmd)
			}

		case key.Matches(msg, keys.Up):
			visible := m.visibleSnags()
			if m.focus == focusInput && len(visible) > 0 {
				m.focus = focusList
				m.cursor = len(visible) - 1
				m.clampView()
				m.input.Blur()
			} else if m.focus == focusList && m.cursor > 0 {
				m.cursor--
				m.clampView()
			}

		case key.Matches(msg, keys.Down):
			if m.focus == focusList {
				visible := m.visibleSnags()
				if m.cursor < len(visible)-1 {
					m.cursor++
					m.clampView()
				} else {
					m.focus = focusInput
					m.input.Focus()
				}
			}

		case key.Matches(msg, keys.Delete):
			if m.focus == focusList {
				visible := m.visibleSnags()
				if m.cursor < len(visible) && visible[m.cursor].Status != StatusInflight {
					id := visible[m.cursor].ID
					var snags []Snag
					for _, s := range m.state.Snags {
						if s.ID != id {
							snags = append(snags, s)
						}
					}
					m.state.Snags = snags
					visible2 := m.visibleSnags()
					if len(visible2) == 0 {
						m.focus = focusInput
						m.input.Focus()
						m.cursor = 0
					} else if m.cursor >= len(visible2) {
						m.cursor = len(visible2) - 1
					}
					m.clampView()
					cmds = append(cmds, saveCmd(m.projectRoot, m.state))
				}
			} else {
				forwardToInput = true
			}

		case key.Matches(msg, keys.Enter):
			if m.focus == focusInput && m.input.Value() != "" {
				snag := Snag{
					ID:          generateID(),
					Description: m.input.Value(),
					Status:      StatusPending,
					CreatedAt:   time.Now(),
				}
				m.state.Snags = append(m.state.Snags, snag)
				m.input.SetValue("")
				cmds = append(cmds, saveCmd(m.projectRoot, m.state))
				if !m.working && !m.paused {
					var workCmd tea.Cmd
					m, workCmd = m.startNextSnag()
					cmds = append(cmds, workCmd)
				}
			}

		case key.Matches(msg, keys.Escape):
			if m.focus == focusInput {
				if m.input.Value() != "" {
					m.input.SetValue("")
				} else {
					return m, tea.Quit
				}
			}

		default:
			forwardToInput = true
		}

		if m.focus == focusInput && forwardToInput {
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			cmds = append(cmds, inputCmd)
		}
	}

	return m, tea.Batch(cmds...)
}

func (m Model) startNextSnag() (Model, tea.Cmd) {
	if m.paused || m.working {
		return m, nil
	}
	for i := range m.state.Snags {
		if m.state.Snags[i].Status == StatusPending {
			m.state.Snags[i].Status = StatusInflight
			m.working = true
			return m, RunSnag(m.projectRoot, m.defaultBranch, m.state.Snags[i])
		}
	}
	return m, nil
}

func saveCmd(projectRoot string, state State) tea.Cmd {
	return func() tea.Msg {
		SaveState(projectRoot, state)
		return nil
	}
}

func (m *Model) clampView() {
	if m.cursor < m.viewOffset {
		m.viewOffset = m.cursor
	}
	if m.cursor >= m.viewOffset+maxVisible {
		m.viewOffset = m.cursor - maxVisible + 1
	}
}

func (m Model) View() string {
	var sb strings.Builder

	// Title bar
	status := m.workerStatusStr()
	title := titleStyle.Render("snags")
	pad := m.width - len("snags") - len(status) - 2
	if pad < 1 {
		pad = 1
	}
	sb.WriteString(title + strings.Repeat(" ", pad) + faintStyle.Render(status) + "\n")
	sb.WriteString(strings.Repeat("─", m.width) + "\n")

	// Snag list
	visible := m.visibleSnags()
	end := m.viewOffset + maxVisible
	if end > len(visible) {
		end = len(visible)
	}
	for i, snag := range visible[m.viewOffset:end] {
		selected := m.focus == focusList && (m.viewOffset+i) == m.cursor
		sb.WriteString(m.renderRow(snag, selected) + "\n")
	}
	if end < len(visible) {
		sb.WriteString("  ...\n")
	}

	sb.WriteString(strings.Repeat("─", m.width) + "\n")
	sb.WriteString("> " + m.input.View() + "\n")
	sb.WriteString(strings.Repeat("─", m.width) + "\n")
	sb.WriteString(faintStyle.Render(m.statusBarStr()) + "\n")

	return sb.String()
}

func (m Model) renderRow(s Snag, selected bool) string {
	var indicator string
	switch s.Status {
	case StatusInflight:
		indicator = m.spinner.View()
	case StatusFailed:
		indicator = "✗"
	default:
		indicator = " "
	}

	sel := " "
	if selected {
		sel = "▶"
	}

	line := fmt.Sprintf("%s %s %s", sel, indicator, s.Description)

	switch {
	case s.Status == StatusFailed && selected:
		line = redStyle.Render(boldStyle.Render(line))
	case s.Status == StatusFailed:
		line = redStyle.Render(line)
	case selected:
		line = boldStyle.Render(line)
	}

	return line
}

func (m Model) workerStatusStr() string {
	switch {
	case m.paused:
		return "[paused]"
	case m.working:
		return "[running]"
	default:
		return "[idle]"
	}
}

func (m Model) statusBarStr() string {
	if m.focus == focusList {
		visible := m.visibleSnags()
		if m.cursor < len(visible) {
			s := visible[m.cursor]
			if s.Status == StatusFailed && s.Notes != "" {
				return "✗ " + s.Notes
			}
		}
	}
	return "↑↓ navigate  backspace delete  ctrl+p pause/resume  esc clear/quit"
}
```

- [ ] **Step 4: Run all tests**

```bash
go test ./... -v
```

Expected: all tests PASS. If any fail, read the error carefully — common issues:
- `focusList`/`focusInput` undefined: check constants in model.go
- `snagDoneMsg` undefined: check worker.go
- Import cycle: should not occur — all files are `package main`

- [ ] **Step 5: Commit**

```bash
git add model.go model_test.go
git commit -m "feat: add Bubbletea model with full TUI logic"
```

---

## Task 6: Main, Build, and Smoke Test

**Files:**
- Write: `main.go`

- [ ] **Step 1: Write main.go**

```go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	projectRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not get working directory: %v\n", err)
		os.Exit(1)
	}

	if _, err := os.Stat(filepath.Join(projectRoot, ".git")); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: not a git repository (no .git found in %s)\n", projectRoot)
		os.Exit(1)
	}

	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Fprintf(os.Stderr, "error: 'claude' not found in PATH — install Claude Code to use snags\n")
		os.Exit(1)
	}

	if err := EnsureSnagDir(projectRoot); err != nil {
		fmt.Fprintf(os.Stderr, "error: could not initialise .snags/: %v\n", err)
		os.Exit(1)
	}

	state, err := LoadState(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not load state: %v\n", err)
		os.Exit(1)
	}

	defaultBranch := detectDefaultBranch(projectRoot)
	m := New(projectRoot, defaultBranch, state)

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Run all tests one final time**

```bash
go test ./... -v
```

Expected: all tests PASS

- [ ] **Step 3: Build the binary**

```bash
go build -o snags .
```

Expected: produces `./snags` binary with no errors

- [ ] **Step 4: Smoke test — launch in a git repo**

The snags binary must be run from a git repository. Test with the snags repo itself:

```bash
./snags
```

Expected:
- TUI opens showing title `snags` with `[idle]` status
- Entry field is focused with placeholder text
- Type a description and press Enter — snag appears in list
- Press `ctrl+c` to exit

- [ ] **Step 5: Smoke test — launch checks**

```bash
cd /tmp && ./path/to/snags
```

Expected: `error: not a git repository` message and exit

- [ ] **Step 6: Smoke test — non-empty queue persists**

```bash
cd /path/to/any-git-repo
/path/to/snags    # add a snag, press ctrl+c
/path/to/snags    # snag should still be in the list
```

Expected: snag survives across restarts

- [ ] **Step 7: Add binary to .gitignore and commit**

```bash
git add main.go
git commit -m "feat: add main entry point with launch checks"
```

- [ ] **Step 8: Install binary (optional)**

```bash
go install .
```

This installs `snags` to `$(go env GOPATH)/bin/snags`. Make sure that path is in your `$PATH`.

---

## Self-Review

**Spec coverage check:**

| Spec requirement | Task |
|-----------------|------|
| Queue renders pending/inflight/failed | Task 5 (View) |
| Truncate at 10 items with `...` | Task 5 (View — `maxVisible`) |
| Navigate with arrow keys | Task 5 (Update — Up/Down) |
| Scroll with selection | Task 5 (`clampView`) |
| Backspace deletes highlighted snag | Task 5 (Update — Delete) |
| Inflight cannot be deleted | Task 5 + test |
| Entry field selected by default | Task 5 (`New` — `ti.Focus()`) |
| Enter adds snag | Task 5 + test |
| Esc clears non-empty field | Task 5 + test |
| Esc on empty field exits | Task 5 + test |
| ctrl+c exits | Task 5 (Update — Quit) |
| Worker runs queue continuously | Task 4 (`RunSnag`) + Task 5 (`startNextSnag`) |
| ctrl+p pause/resume | Task 5 + test |
| State persists to `.snags/state.yaml` | Task 2 |
| Inflight reset on restart | Task 2 + test |
| Failed snags shown with ✗ | Task 5 (renderRow) |
| Failure reason in status bar | Task 5 (statusBarStr) |
| Git worktree per snag | Task 4 (`createWorktree`) |
| Claude invoked headless with auto mode | Task 4 (`runClaudeHeadless`) |
| JSON schema for structured output | Task 4 |
| Notes in commit body | Task 4 (`squashMerge`) |
| Squash commit per snag | Task 4 (`squashMerge`) |
| Conflict resolver agent | Task 4 (`runConflictResolver`) |
| Not-a-git-repo check | Task 6 |
| claude-not-in-PATH check | Task 6 |
| `.snags/` gitignored | Task 2 (`EnsureSnagDir`) |
