package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Sentinel errors from squashMerge so the caller can distinguish a real merge
// conflict (which warrants preserving the snag branch for a retry) from a
// no-op merge (where the snag branch had no effective changes vs the default
// branch).
var (
	errNothingToMerge = errors.New("nothing to merge")
	errMergeConflict  = errors.New("merge conflict")
)

type snagDoneMsg struct {
	snagID     string
	success    bool
	notes      string
	commitHash string
	// mergeFailed marks a merge-stage failure: the agent's work is intact on
	// branch snag/<id>, only merging it back failed.
	mergeFailed bool
}

type revertDoneMsg struct {
	snagID  string
	success bool
	errMsg  string
}

type mergeDoneMsg struct {
	snagID     string
	success    bool
	commitHash string
	errMsg     string
}

type snagProgressMsg struct {
	snagID   string
	kind     string // "tool", "text", or "" for pipeline status updates
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
			Text  string          `json:"text"`
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
	branchExists := func(name string) bool {
		return exec.Command("git", "-C", projectRoot, "rev-parse", "--verify", "refs/heads/"+name).Run() == nil
	}
	cmd := exec.Command("git", "-C", projectRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if out, err := cmd.Output(); err == nil {
		name := strings.TrimSpace(string(out))
		if parts := strings.SplitN(name, "/", 2); len(parts) == 2 {
			name = parts[1]
		}
		if branchExists(name) {
			return name
		}
	}
	for _, b := range []string{"main", "master"} {
		if branchExists(b) {
			return b
		}
	}
	// No conventional default branch (e.g. neo4j's "dev"): base snags on
	// whatever branch is checked out.
	if b, err := currentBranch(projectRoot); err == nil {
		return b
	}
	return "main"
}

// currentBranch returns the branch HEAD is on; it errors on detached HEAD.
func currentBranch(projectRoot string) (string, error) {
	out, err := exec.Command("git", "-C", projectRoot, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("could not determine current branch (detached HEAD?): %s", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// baseBranchFor picks the branch a snag's worktree is created from and merged
// into. Marker snags always base on the current branch: the marker lives in
// the working tree of whatever branch the user is on, which may not be the
// default branch. Input snags use the default branch.
func baseBranchFor(projectRoot string, snag Snag, defaultBranch string) (string, error) {
	if snag.Source != SourceMarker {
		return defaultBranch, nil
	}
	return currentBranch(projectRoot)
}

func worktreePath(projectRoot, snagID string) string {
	return filepath.Join(projectRoot, ".snags", "worktrees", snagID)
}

func createWorktree(projectRoot, snagID, baseBranch string) error {
	path := worktreePath(projectRoot, snagID)
	// Defensive cleanup in case a prior run crashed and left an orphan
	exec.Command("git", "-C", projectRoot, "worktree", "remove", "--force", path).Run()
	exec.Command("git", "-C", projectRoot, "branch", "-D", "snag/"+snagID).Run()

	cmd := exec.Command("git", "-C", projectRoot, "worktree", "add",
		path, "-b", "snag/"+snagID, baseBranch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("worktree add: %s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func removeWorktreeOnly(projectRoot, snagID string) {
	exec.Command("git", "-C", projectRoot, "worktree", "remove", "--force",
		worktreePath(projectRoot, snagID)).Run()
}

func deleteSnagBranch(projectRoot, snagID string) {
	exec.Command("git", "-C", projectRoot, "branch", "-D", "snag/"+snagID).Run()
}

func removeWorktree(projectRoot, snagID string) {
	removeWorktreeOnly(projectRoot, snagID)
	deleteSnagBranch(projectRoot, snagID)
}

func buildPrompt(description string) string {
	return fmt.Sprintf(`You are working autonomously in a git worktree to complete a small code change (a "snag").
The project is already checked out. Do not ask for clarification — use your best judgement.
If the request is ambiguous, pick the most plausible interpretation, do it, and explain the choice in notes.

Snag: %s

Complete the task fully. Your final response must be a JSON object with:
- "status": "success" if the task is complete, or "failed" if you could not complete it
- "notes": any assumptions you made, decisions you took, or (if failed) why you could not complete it`, description)
}

// buildMarkerPrompt is buildPrompt for snags discovered via inline comment
// markers: it points the agent at the marker's location and makes removing
// the marker comment part of the task.
func buildMarkerPrompt(description, file string, line int, context string) string {
	const fence = "```"
	return fmt.Sprintf(`You are working autonomously in a git worktree to complete a small code change (a "snag").
The project is already checked out. Do not ask for clarification — use your best judgement.
If the request is ambiguous, pick the most plausible interpretation, do it, and explain the choice in notes.

Snag: %s

This request came from an inline comment marker at %s:%d. Surrounding code at the time of discovery:

%s
%s
%s

If the marker comment containing this request still exists in the checkout, removing it is part of the task.

Complete the task fully. Your final response must be a JSON object with:
- "status": "success" if the task is complete, or "failed" if you could not complete it
- "notes": any assumptions you made, decisions you took, or (if failed) why you could not complete it`,
		description, file, line, fence, context, fence)
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

// isolationArgs are the hardening flags shared by every headless claude run:
// they skip user-level settings/hooks/plugins so the worker doesn't inherit
// the user's global Claude config (skills, SessionStart hooks, MCP servers,
// slash commands, etc.).
var isolationArgs = []string{
	"--setting-sources", "project,local",
	"--strict-mcp-config",
	"--mcp-config", `{"mcpServers":{}}`,
	"--disable-slash-commands",
	"--exclude-dynamic-system-prompt-sections",
}

// claudeArgs builds the claude argv (excluding the binary name) for a headless run.
func claudeArgs(cfg AgentConfig, prompt, schema string) []string {
	args := []string{"--model", cfg.Model}
	if cfg.Effort != "" {
		args = append(args, "--effort", cfg.Effort)
	}
	args = append(args,
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--json-schema", schema,
		"--permission-mode", "auto",
	)
	args = append(args, isolationArgs...)
	args = append(args,
		"--tools", "Read,Edit,Write,Bash,Grep,Glob,Agent",
		"--settings", `{"autoMode":{"environment":["$defaults"]}}`,
	)
	return append(args, cfg.ExtraArgs...)
}

// ctxNotes describes why a run produced no result, given a done context.
func ctxNotes(ctx context.Context, timeout Duration) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timed out after " + time.Duration(timeout).String()
	}
	return "cancelled"
}

// runClaudeHeadless runs claude headless in dir with the given prompt.
// onActivity is called with a kind ("tool" or "text") and description for each
// agent event (may be nil). Text, tool, and result events are teed to tl
// (may be nil); the caller writes the run_start event.
func runClaudeHeadless(ctx context.Context, dir, prompt string, cfg AgentConfig, tl *transcriptLogger, onActivity func(kind, activity string)) (success bool, notes string, err error) {
	start := time.Now()
	if debugLog != nil {
		debugLog.Printf("agent start dir=%s", dir)
	}
	defer func() {
		tl.result(success, notes)
		if debugLog != nil {
			debugLog.Printf("agent done dir=%s duration=%s success=%v notes=%q", dir, time.Since(start).Round(time.Millisecond), success, notes)
		}
	}()

	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.Timeout))
		defer cancel()
	}

	const schema = `{"type":"object","properties":{"status":{"type":"string","enum":["success","failed"]},"notes":{"type":"string"}},"required":["status"]}`
	cmd := exec.CommandContext(ctx, "claude", claudeArgs(cfg, prompt, schema)...)
	cmd.Dir = dir
	// Run claude in its own process group and kill the whole group on
	// timeout/cancel: killing just claude leaves its tool subprocesses alive,
	// and a child that inherited stdout would keep the scanner below blocked.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// If a grandchild escaped the process group (setsid) and holds stdout open
	// after the group kill, force-close the pipes so Wait can't block forever.
	cmd.WaitDelay = 10 * time.Second

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	stdout, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return false, pipeErr.Error(), nil
	}
	if startErr := cmd.Start(); startErr != nil {
		if ctx.Err() != nil {
			return false, ctxNotes(ctx, cfg.Timeout), nil
		}
		return false, startErr.Error(), nil
	}

	var (
		resultSuccess bool
		resultNotes   string
		foundResult   bool
	)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)
	for scanner.Scan() {
		var line streamLine
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		switch line.Type {
		case "assistant":
			for _, block := range line.Message.Content {
				var kind, activity string
				switch block.Type {
				case "tool_use":
					detail := extractToolDetail(block.Name, string(block.Input))
					tl.tool(block.Name, detail)
					kind = "tool"
					activity = block.Name
					if detail != "" {
						activity = block.Name + "(" + detail + ")"
					}
				case "text":
					if block.Text != "" {
						tl.text(block.Text)
						kind = "text"
						activity = block.Text
					}
				}
				if activity != "" && onActivity != nil {
					select {
					case <-ctx.Done():
					default:
						onActivity(kind, activity)
					}
				}
			}
		case "result":
			foundResult = true
			resultSuccess = line.StructuredOutput.Status == "success"
			resultNotes = line.StructuredOutput.Notes
		}
	}

	scanErr := scanner.Err()
	// Drain anything left on the pipe (e.g. after a scanner overflow stopped
	// the loop early) so the child never blocks writing stdout.
	io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if !foundResult {
		if ctx.Err() != nil {
			return false, ctxNotes(ctx, cfg.Timeout), nil
		}
		stderr := strings.TrimSpace(stderrBuf.String())
		if stderr == "" && scanErr != nil {
			stderr = "reading claude output: " + scanErr.Error()
		}
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

// commitWorktreeChanges stages and commits any uncommitted changes in the
// worktree at dir. The buildPrompt() prompt does not instruct the worker to
// commit, so changes often end up uncommitted; without this step `git merge
// --squash` sees no commits on the snag branch and the subsequent commit
// fails with "nothing to commit". Returns nil if the tree is already clean.
func commitWorktreeChanges(dir, description string) error {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git status: %s", err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil
	}
	cmd = exec.Command("git", "add", "-A")
	cmd.Dir = dir
	if cmdOut, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %s: %s", err, strings.TrimSpace(string(cmdOut)))
	}
	cmd = exec.Command("git", "commit", "-m", "snag: "+description)
	cmd.Dir = dir
	if cmdOut, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %s: %s", err, strings.TrimSpace(string(cmdOut)))
	}
	return nil
}

func hasUnmergedPaths(dir string) bool {
	cmd := exec.Command("git", "diff", "--name-only", "--diff-filter=U")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

func hasStagedChanges(dir string) bool {
	cmd := exec.Command("git", "diff", "--cached", "--quiet")
	cmd.Dir = dir
	// `git diff --cached --quiet` exits 0 if there are no staged changes,
	// non-zero if there are.
	return cmd.Run() != nil
}

// requireBaseBranch errors unless HEAD in projectRoot is on baseBranch.
// action names the operation for the error message ("running snags", "merging").
func requireBaseBranch(projectRoot, baseBranch, action string) error {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("could not determine current branch: %s", err)
	}
	current := strings.TrimSpace(string(out))
	if current != baseBranch {
		return fmt.Errorf("not on %s (currently on %s) — switch to %s before %s", baseBranch, current, baseBranch, action)
	}
	return nil
}

func squashMerge(projectRoot, snagID, description, notes, baseBranch string) error {
	if err := requireBaseBranch(projectRoot, baseBranch, "running snags"); err != nil {
		return err
	}

	// Refuse to merge over the user's staged work: the squash commit would
	// silently sweep it in, and backing a failed commit out with `git reset
	// --merge` would destroy it.
	if hasStagedChanges(projectRoot) {
		return errors.New("staged changes in working tree — commit or unstage them, then retry the merge")
	}

	cmd := exec.Command("git", "merge", "--squash", "snag/"+snagID)
	cmd.Dir = projectRoot
	mergeOut, mergeErr := cmd.CombinedOutput()
	if hasUnmergedPaths(projectRoot) {
		return errMergeConflict
	}
	if mergeErr != nil {
		return fmt.Errorf("merge: %s: %s", mergeErr, strings.TrimSpace(string(mergeOut)))
	}
	if !hasStagedChanges(projectRoot) {
		return errNothingToMerge
	}

	args := []string{"commit", "-m", "snag: " + description}
	if notes != "" {
		args = append(args, "-m", notes)
	}
	cmd = exec.Command("git", args...)
	cmd.Dir = projectRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		// Back out the staged squash: left in the index it makes every later
		// merge refuse until the user cleans up by hand.
		commitErr := fmt.Errorf("commit: %s: %s", err, strings.TrimSpace(string(out)))
		resetCmd := exec.Command("git", "reset", "--merge")
		resetCmd.Dir = projectRoot
		if resetOut, resetErr := resetCmd.CombinedOutput(); resetErr != nil {
			return fmt.Errorf("%w; git reset --merge failed (%s: %s) — staged squash left in index",
				commitErr, resetErr, strings.TrimSpace(string(resetOut)))
		}
		return commitErr
	}
	return nil
}

// snagCommitLanded returns the hash of the first commit in preHead..HEAD whose
// subject starts with "snag:", or "" if none landed. It distinguishes a merge
// agent's squash commit from unrelated commits the user may have made while
// the agent ran — recording HEAD instead would make a later revert undo the
// user's work.
func snagCommitLanded(projectRoot, preHead string) string {
	cmd := exec.Command("git", "log", preHead+"..HEAD", "--format=%H %s")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		hash, subject, ok := strings.Cut(line, " ")
		if ok && strings.HasPrefix(subject, "snag:") {
			return hash
		}
	}
	return ""
}

func headCommitHash(projectRoot string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// mergeStage runs everything after the snag branch is committed: marker
// deletion, the squash merge, and worktree/branch cleanup. Merge-stage
// failures remove the worktree but preserve branch snag/<id> so the user can
// retry the merge agentically. Each merge failure is also written to the
// snag's transcript via tl, so the log doesn't end on the agent's success
// while the snag shows failed.
func mergeStage(projectRoot, baseBranch string, snag Snag, notes string, cfg Config, tl *transcriptLogger) (msg snagDoneMsg) {
	defer func() {
		if msg.mergeFailed {
			tl.result(false, msg.notes)
		}
	}()

	if snag.Source == SourceMarker {
		if err := DeleteMarker(projectRoot, snag.File, snag.Description, cfg.Marker); err != nil {
			removeWorktreeOnly(projectRoot, snag.ID)
			return snagDoneMsg{snagID: snag.ID, success: false, mergeFailed: true,
				notes: fmt.Sprintf("marker removal failed: %s — branch snag/%s preserved", err, snag.ID)}
		}
	}

	mergeErr := squashMerge(projectRoot, snag.ID, snag.Description, notes, baseBranch)
	switch {
	case mergeErr == nil:
		// merged + committed cleanly
	case errors.Is(mergeErr, errNothingToMerge):
		// Claude reported success but produced no net change vs the
		// default branch. Mark the snag complete with a clear marker.
		removeWorktree(projectRoot, snag.ID)
		noteText := "no code changes"
		if notes != "" {
			noteText = "no code changes — " + notes
		}
		return snagDoneMsg{snagID: snag.ID, success: true, notes: noteText}
	case errors.Is(mergeErr, errMergeConflict):
		// Back out the half-done squash merge; the work survives on the branch.
		failNotes := fmt.Sprintf("merge conflict — branch snag/%s preserved", snag.ID)
		resetCmd := exec.Command("git", "reset", "--merge")
		resetCmd.Dir = projectRoot
		if out, err := resetCmd.CombinedOutput(); err != nil {
			failNotes += fmt.Sprintf("; git reset --merge failed (%s: %s) — working tree left mid-conflict",
				err, strings.TrimSpace(string(out)))
		}
		removeWorktreeOnly(projectRoot, snag.ID)
		return snagDoneMsg{snagID: snag.ID, success: false, mergeFailed: true, notes: failNotes}
	default:
		removeWorktreeOnly(projectRoot, snag.ID)
		return snagDoneMsg{snagID: snag.ID, success: false, mergeFailed: true, notes: mergeErr.Error()}
	}

	commitHash := headCommitHash(projectRoot)
	removeWorktree(projectRoot, snag.ID)
	return snagDoneMsg{snagID: snag.ID, success: true, notes: notes, commitHash: commitHash}
}

// RunSnag starts the snag pipeline in a goroutine and returns a channel of events.
// The channel carries snagProgressMsg (activity updates) and a final snagDoneMsg.
func RunSnag(ctx context.Context, projectRoot, defaultBranch string, snag Snag, cfg Config) chan tea.Msg {
	ch := make(chan tea.Msg, 64)
	go func() {
		baseBranch, err := baseBranchFor(projectRoot, snag, defaultBranch)
		if err != nil {
			ch <- snagDoneMsg{snagID: snag.ID, success: false, notes: err.Error()}
			return
		}
		if err := createWorktree(projectRoot, snag.ID, baseBranch); err != nil {
			ch <- snagDoneMsg{snagID: snag.ID, success: false, notes: err.Error()}
			return
		}

		ch <- snagProgressMsg{snagID: snag.ID, activity: "starting up"}

		onActivity := func(kind, activity string) {
			select {
			case ch <- snagProgressMsg{snagID: snag.ID, kind: kind, activity: activity}:
			default:
			}
		}

		tl := newTranscriptLogger(projectRoot, snag.ID)
		defer tl.Close()
		tl.runStart("agent")

		prompt := buildPrompt(snag.Description)
		if snag.Source == SourceMarker {
			prompt = buildMarkerPrompt(snag.Description, snag.File, snag.Line, string(snag.Context))
		}

		success, notes, err := runClaudeHeadless(ctx, worktreePath(projectRoot, snag.ID), prompt, cfg.Agents.Snag, tl, onActivity)
		if err != nil {
			removeWorktree(projectRoot, snag.ID)
			ch <- snagDoneMsg{snagID: snag.ID, success: false, notes: err.Error()}
			return
		}
		if !success {
			if ctx.Err() != nil {
				notes = "cancelled"
			}
			removeWorktree(projectRoot, snag.ID)
			ch <- snagDoneMsg{snagID: snag.ID, success: false, notes: notes}
			return
		}

		// Capture any uncommitted edits Claude left in the worktree so the
		// squash merge below has commits to operate on.
		if commitErr := commitWorktreeChanges(worktreePath(projectRoot, snag.ID), snag.Description); commitErr != nil {
			removeWorktree(projectRoot, snag.ID)
			ch <- snagDoneMsg{snagID: snag.ID, success: false, notes: commitErr.Error()}
			return
		}

		ch <- snagProgressMsg{snagID: snag.ID, activity: "merging"}
		ch <- mergeStage(projectRoot, baseBranch, snag, notes, cfg, tl)
	}()
	return ch
}

// agenticMergeCmd retries a failed merge: a headless claude run in the project
// root squash-merges the preserved branch snag/<id> and resolves any conflicts.
// Cancelling ctx kills the merge agent (e.g. on quit, so it cannot keep
// committing to the default branch after the app exits); the failure path then
// applies and the branch is preserved.
func agenticMergeCmd(ctx context.Context, projectRoot, defaultBranch string, cfg Config, snag Snag) tea.Cmd {
	return func() tea.Msg {
		// Marker snags merge into the current branch by definition; for input
		// snags the guard catches a user who switched branches since the snag
		// failed — the agent would merge in the wrong place and the preserved
		// branch would still be deleted.
		baseBranch, err := baseBranchFor(projectRoot, snag, defaultBranch)
		if err != nil {
			return mergeDoneMsg{snagID: snag.ID, success: false, errMsg: err.Error()}
		}
		if err := requireBaseBranch(projectRoot, baseBranch, "merging"); err != nil {
			return mergeDoneMsg{snagID: snag.ID, success: false, errMsg: err.Error()}
		}

		tl := newTranscriptLogger(projectRoot, snag.ID)
		defer tl.Close()
		tl.runStart("merge")

		prompt := fmt.Sprintf(
			"Branch snag/%s holds completed work for the task: %s\n\n"+
				"Perform `git merge --squash snag/%s` into %s, resolving any conflicts in favor of the task's intent, "+
				"then commit with: git commit -m %q\n\n"+
				"Do not commit unrelated local changes.",
			snag.ID, snag.Description, snag.ID, baseBranch, "snag: "+snag.Description)
		if snag.Source == SourceMarker {
			prompt += fmt.Sprintf(
				"\n\nIf an inline comment marker `%s: %s` remains at %s, remove it before committing.",
				cfg.Marker, snag.Description, snag.File)
		}

		preHead := headCommitHash(projectRoot)
		success, notes, err := runClaudeHeadless(ctx, projectRoot, prompt, cfg.Agents.Merge, tl, nil)

		// Trust git, not the agent's report: the merge is only done if a new
		// commit actually landed AND one of the new commits is a snag commit
		// (a user committing to the default branch mid-run also advances HEAD).
		// Branch snag/<id> is the sole copy of the work, so it is deleted only
		// on a verified merge. Record the snag commit's hash, not HEAD: a user
		// commit on top mid-run would otherwise be what a later revert undoes.
		newHead := headCommitHash(projectRoot)
		headAdvanced := newHead != "" && newHead != preHead
		snagHash := snagCommitLanded(projectRoot, preHead)
		if err == nil && success && headAdvanced && snagHash != "" {
			deleteSnagBranch(projectRoot, snag.ID)
			return mergeDoneMsg{snagID: snag.ID, success: true, commitHash: snagHash}
		}

		var errMsg string
		switch {
		case err != nil:
			errMsg = err.Error()
		case !success:
			errMsg = notes
		case headAdvanced:
			errMsg = fmt.Sprintf("merge agent reported success but no snag commit landed (HEAD moved by other commits) — branch snag/%s preserved", snag.ID)
		default:
			errMsg = fmt.Sprintf("merge agent reported success but no commit was created — branch snag/%s preserved", snag.ID)
		}
		// Don't leave the repo mid-conflict if the agent gave up or timed out
		// partway through resolving the merge.
		if hasUnmergedPaths(projectRoot) {
			resetCmd := exec.Command("git", "reset", "--merge")
			resetCmd.Dir = projectRoot
			if out, resetErr := resetCmd.CombinedOutput(); resetErr != nil {
				errMsg += fmt.Sprintf(" (git reset --merge failed: %s: %s)", resetErr, strings.TrimSpace(string(out)))
			} else {
				errMsg += " (ran git reset --merge to back out the conflicted merge)"
			}
		}
		return mergeDoneMsg{snagID: snag.ID, success: false, errMsg: errMsg}
	}
}

// runSummary asks claude for a one-line summary of a marker's request,
// suitable for display in the snag list.
func runSummary(ctx context.Context, projectRoot string, cfg AgentConfig, markerText, codeContext string) (string, error) {
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.Timeout))
		defer cancel()
	}

	prompt := fmt.Sprintf(
		"A code comment contains a task request. Produce a one-line imperative summary of the request, "+
			"at most 60 characters. Respond with plain text only: no quotes, no markdown, no explanation.\n\n"+
			"Request: %s\n\nSurrounding code:\n%s",
		markerText, codeContext)

	args := []string{"--model", cfg.Model}
	if cfg.Effort != "" {
		args = append(args, "--effort", cfg.Effort)
	}
	args = append(args,
		"-p", prompt,
		"--output-format", "text",
		"--tools", "",
	)
	args = append(args, isolationArgs...)
	args = append(args, cfg.ExtraArgs...)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = projectRoot
	// Same process-group kill as runClaudeHeadless: a timeout must take down
	// claude's subprocesses too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// Same pipe-hang hardening as runClaudeHeadless: a setsid-escaped
	// grandchild holding stdout must not block Wait after the group kill.
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
			return "", fmt.Errorf("summary: %s: %s", err, bytes.TrimSpace(ee.Stderr))
		}
		return "", fmt.Errorf("summary: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if l := strings.TrimSpace(line); l != "" {
			return l, nil
		}
	}
	return "", errors.New("summary: empty output")
}

// revertSnag reverts a completed snag's commit. A conflicted revert spawns a
// headless claude to resolve it; cancelling ctx kills that resolver (e.g. on
// quit, so it cannot keep committing to the default branch after the app
// exits) and the conflicted revert is aborted.
func revertSnag(ctx context.Context, projectRoot, snagID, description, commitHash string, cfg Config) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("git", "revert", "--no-edit", commitHash)
		cmd.Dir = projectRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			if hasUnmergedPaths(projectRoot) {
				tl := newTranscriptLogger(projectRoot, snagID)
				defer tl.Close()
				tl.runStart("revert")
				prompt := fmt.Sprintf(
					"A `git revert` conflict occurred while reverting commit %s (snag: %q). "+
						"Resolve the conflicts in the working tree, then run: git commit --no-edit",
					commitHash, description,
				)
				resolveSuccess, resolveNotes, resolveErr := runClaudeHeadless(ctx, projectRoot, prompt, cfg.Agents.Merge, tl, nil)
				if resolveErr != nil || !resolveSuccess {
					exec.Command("git", "-C", projectRoot, "revert", "--abort").Run() //nolint
					msg := "revert conflict unresolved"
					if resolveErr != nil {
						msg = resolveErr.Error()
					} else if resolveNotes != "" {
						msg = resolveNotes
					}
					return revertDoneMsg{snagID: snagID, success: false, errMsg: msg}
				}
				return revertDoneMsg{snagID: snagID, success: true}
			}
			return revertDoneMsg{snagID: snagID, success: false, errMsg: strings.TrimSpace(string(out))}
		}
		return revertDoneMsg{snagID: snagID, success: true}
	}
}
