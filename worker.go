package main

import (
	"bufio"
	"bytes"
	"context"
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

type snagProgressMsg struct {
	snagID   string
	activity string
}

// streamLine is a parsed line from --output-format stream-json.
// Tool calls arrive as assistant messages with content items of type "tool_use".
type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
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
	path := worktreePath(projectRoot, snagID)
	// Defensive cleanup in case a prior run crashed and left an orphan
	exec.Command("git", "-C", projectRoot, "worktree", "remove", "--force", path).Run()
	exec.Command("git", "-C", projectRoot, "branch", "-D", "snag/"+snagID).Run()

	cmd := exec.Command("git", "-C", projectRoot, "worktree", "add",
		path, "-b", "snag/"+snagID, defaultBranch)
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

// extractToolDetail returns a short human-readable detail string for the given tool and its JSON input.
func extractToolDetail(toolName, inputJSON string) string {
	var m map[string]interface{}
	if json.Unmarshal([]byte(inputJSON), &m) != nil {
		return ""
	}
	var key string
	switch toolName {
	case "Bash":
		key = "command"
	case "Edit", "Write", "Read", "MultiEdit":
		key = "file_path"
	case "WebSearch":
		key = "query"
	case "WebFetch":
		key = "url"
	default:
		return ""
	}
	v, _ := m[key].(string)
	if len(v) > 50 {
		v = v[:47] + "..."
	}
	return v
}

// runClaudeHeadless runs claude headless in dir with the given prompt.
// onActivity is called with a short description each time a tool is invoked (may be nil).
func runClaudeHeadless(ctx context.Context, dir, prompt string, onActivity func(string)) (success bool, notes string, err error) {
	const schema = `{"type":"object","properties":{"status":{"type":"string","enum":["success","failed"]},"notes":{"type":"string"}},"required":["status"]}`
	cmd := exec.CommandContext(ctx, "claude",
		"--model", "claude-sonnet-4-6",
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--json-schema", schema,
		"--permission-mode", "auto",
		// Skip user-level settings/hooks/plugins so the worker doesn't inherit the user's
		// global Claude config (skills, SessionStart hooks, MCP servers, etc.).
		"--setting-sources", "project,local",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
		"--disable-slash-commands",
		"--tools", "Read,Edit,Write,Bash,Grep,Glob,Agent",
		"--exclude-dynamic-system-prompt-sections",
		"--settings", `{"autoMode":{"environment":["$defaults"]}}`,
	)
	cmd.Dir = dir

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	stdout, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return false, pipeErr.Error(), nil
	}
	if startErr := cmd.Start(); startErr != nil {
		if ctx.Err() != nil {
			return false, "cancelled", nil
		}
		return false, startErr.Error(), nil
	}

	var (
		resultSuccess bool
		resultNotes   string
		foundResult   bool
	)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		var line streamLine
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		switch line.Type {
		case "assistant":
			if onActivity == nil {
				break
			}
			for _, block := range line.Message.Content {
				if block.Type == "tool_use" {
					detail := extractToolDetail(block.Name, string(block.Input))
					activity := block.Name
					if detail != "" {
						activity = block.Name + "(" + detail + ")"
					}
					select {
					case <-ctx.Done():
					default:
						onActivity(activity)
					}
				}
			}
		case "result":
			foundResult = true
			resultSuccess = line.StructuredOutput.Status == "success"
			resultNotes = line.StructuredOutput.Notes
		}
	}

	waitErr := cmd.Wait()
	if !foundResult {
		if ctx.Err() != nil {
			return false, "cancelled", nil
		}
		stderr := strings.TrimSpace(stderrBuf.String())
		if stderr == "" && waitErr != nil {
			stderr = waitErr.Error()
		}
		if stderr == "" {
			stderr = "no result from claude"
		}
		return false, stderr, nil
	}

	return resultSuccess, resultNotes, nil
}

func squashMerge(projectRoot, snagID, description, notes, defaultBranch string) error {
	// Verify we're on the default branch before merging
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("could not determine current branch: %s", err)
	}
	currentBranch := strings.TrimSpace(string(out))
	if currentBranch != defaultBranch {
		return fmt.Errorf("not on %s (currently on %s) — switch to %s before running snags", defaultBranch, currentBranch, defaultBranch)
	}

	cmd = exec.Command("git", "merge", "--squash", "snag/"+snagID)
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

func runConflictResolver(ctx context.Context, projectRoot, snagID, description string) error {
	prompt := fmt.Sprintf(
		"A git merge --squash conflict occurred while merging branch snag/%s. "+
			"Resolve the conflicts in the working tree, then run: git commit -m \"snag: %s\"",
		snagID, description,
	)
	success, notes, err := runClaudeHeadless(ctx, projectRoot, prompt, nil)
	if err != nil {
		return err
	}
	if !success {
		return fmt.Errorf("%s", notes)
	}
	return nil
}

// RunSnag starts the snag pipeline in a goroutine and returns a channel of events.
// The channel carries snagProgressMsg (activity updates) and a final snagDoneMsg.
func RunSnag(ctx context.Context, projectRoot, defaultBranch string, snag Snag) chan tea.Msg {
	ch := make(chan tea.Msg, 64)
	go func() {
		if err := createWorktree(projectRoot, snag.ID, defaultBranch); err != nil {
			ch <- snagDoneMsg{snagID: snag.ID, success: false, notes: err.Error()}
			return
		}

		ch <- snagProgressMsg{snagID: snag.ID, activity: "starting up"}

		onActivity := func(activity string) {
			select {
			case ch <- snagProgressMsg{snagID: snag.ID, activity: activity}:
			default:
			}
		}

		success, notes, err := runClaudeHeadless(ctx, worktreePath(projectRoot, snag.ID), buildPrompt(snag.Description), onActivity)
		if err != nil {
			removeWorktree(projectRoot, snag.ID)
			ch <- snagDoneMsg{snagID: snag.ID, success: false, notes: err.Error()}
			return
		}
		if !success {
			if ctx.Err() != nil {
				removeWorktree(projectRoot, snag.ID)
				ch <- snagDoneMsg{snagID: snag.ID, success: false, notes: "cancelled"}
				return
			}
			removeWorktree(projectRoot, snag.ID)
			ch <- snagDoneMsg{snagID: snag.ID, success: false, notes: notes}
			return
		}

		ch <- snagProgressMsg{snagID: snag.ID, activity: "merging"}

		if mergeErr := squashMerge(projectRoot, snag.ID, snag.Description, notes, defaultBranch); mergeErr != nil {
			ch <- snagProgressMsg{snagID: snag.ID, activity: "resolving conflicts"}
			if resolveErr := runConflictResolver(ctx, projectRoot, snag.ID, snag.Description); resolveErr != nil {
				removeWorktree(projectRoot, snag.ID)
				ch <- snagDoneMsg{snagID: snag.ID, success: false,
					notes: fmt.Sprintf("merge conflict unresolved: %s", resolveErr)}
				return
			}
		}

		removeWorktree(projectRoot, snag.ID)
		ch <- snagDoneMsg{snagID: snag.ID, success: true, notes: notes}
	}()
	return ch
}
