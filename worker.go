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

		if mergeErr := squashMerge(projectRoot, snag.ID, snag.Description, notes, defaultBranch); mergeErr != nil {
			if resolveErr := runConflictResolver(projectRoot, snag.ID, snag.Description); resolveErr != nil {
				return snagDoneMsg{snagID: snag.ID, success: false,
					notes: fmt.Sprintf("merge conflict unresolved: %s", resolveErr)}
			}
		}

		removeWorktree(projectRoot, snag.ID)
		return snagDoneMsg{snagID: snag.ID, success: true, notes: notes}
	}
}
