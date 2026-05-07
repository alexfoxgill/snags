# Snags TUI — Design Spec

**Date:** 2026-05-07  
**Status:** Approved

## Overview

`snags` is a terminal UI app for managing a queue of small, autonomous code changes ("snags") in a git repository. The user adds snag descriptions; Claude Code works through them one at a time in isolated git worktrees, merging successes back to main and reporting failures.

---

## Architecture

**Stack:** Go + Bubbletea (single binary)  
**Invocation:** `snags` run from a git repo root (uses cwd as project)  
**State file:** `.snags/state.yaml` in the project directory  
**Worktrees:** `.snags/worktrees/<id>/`  
**Both gitignored:** `.snags/` is added to the project's `.gitignore` on first run

### Single-process model

The Bubbletea program owns everything. Background work runs via `tea.Cmd` goroutines. When a snag completes, a `tea.Msg` is sent to the event loop which triggers state save and the next snag dispatch.

On launch:
- Load `.snags/state.yaml`
- Reset any `inflight` snags to `pending` (prior process is gone)
- Begin working the queue unless paused

On exit (ctrl+c or esc):
- Kill in-flight `claude` subprocess
- Reset its snag to `pending`
- Flush state to disk

### File layout

```
snags/           ← this repo
  main.go
  model.go       ← Bubbletea model, Update, View
  worker.go      ← subprocess launch, tea.Cmd wrappers
  state.go       ← YAML load/save, Snag struct
  keys.go        ← key bindings
```

---

## Data Model

```yaml
# .snags/state.yaml
snags:
  - id: "a1b2c3"
    description: "Fix the flaky timeout in auth_test.go"
    status: pending         # pending | inflight | complete | failed
    created_at: 2026-05-07T10:00:00Z
    branch: ""              # populated on success (snag/<id>)
    notes: ""               # model's notes/assumptions (success) or failure reason
```

**Status meanings:**
- `pending` — queued, not yet started
- `inflight` — currently being processed (reset to `pending` on next launch)
- `complete` — done; squash-committed to default branch; hidden from TUI queue
- `failed` — claude or merge failed; `notes` contains failure reason; shown in TUI with ✗

State is written to disk on every status transition.

---

## TUI Layout

```
┌─────────────────────────────────────────┐
│ snags                          [running] │  ← title + worker status
├─────────────────────────────────────────┤
│   ⟳ fix flaky timeout in auth_test.go   │  ← inflight (spinner)
│ ▶ add dark mode toggle to settings      │  ← selected (pending)
│   rename UserID to user_id everywhere   │  ← pending
│   remove deprecated /v1/ping endpoint   │  ← pending
│   ...                                   │  ← truncated (>10 items)
│ ✗ update go.mod to go 1.22              │  ← failed (red)
├─────────────────────────────────────────┤
│ > _                                     │  ← entry field
├─────────────────────────────────────────┤
│ claude: failed — go toolchain not found │  ← status bar (failed snag)
└─────────────────────────────────────────┘
```

**Queue display:** shows `pending`, `inflight`, and `failed` snags in order. `complete` snags are hidden. Maximum 10 rows visible; truncated with `...` if more. Selection scrolls with the list when truncated.

**Status bar:** shown at bottom. When a failed snag is highlighted, displays its `notes` (failure reason). Otherwise shows worker status hints.

**Worker status indicator** (top right):
- `[running]` — actively processing
- `[paused]` — worker paused by user
- `[idle]` — queue empty

### Key Bindings

| Key | Context | Action |
|-----|---------|--------|
| `↑` / `↓` | anywhere | Navigate list; `↓` from last item returns focus to entry field; `↑` from entry field moves into list |
| `backspace` | list focused, non-inflight row | Delete highlighted snag |
| `backspace` | list focused, inflight row | No-op |
| `enter` | entry field | Add snag text to bottom of queue; clear field |
| `esc` | entry field, non-empty | Clear entry field |
| `esc` | entry field, empty | Exit app |
| `ctrl+p` | anywhere | Toggle pause / resume worker |
| `ctrl+c` | anywhere | Exit app |

**Default focus:** entry field. Arrow up moves focus into the list.

---

## Worker & Claude Invocation

### Happy path

1. Pop top `pending` snag, mark `inflight`, save state
2. Detect the repo's default branch (`git symbolic-ref refs/remotes/origin/HEAD` or fallback to `main`/`master`). Create git worktree branching from it: `git worktree add .snags/worktrees/<id> -b snag/<id> <default-branch>`
3. Run `claude` headless in that worktree:

```sh
claude \
  --model claude-sonnet-4-6 \
  -p "<prompt>" \
  --output-format json \
  --json-schema '{"type":"object","properties":{"status":{"type":"string","enum":["success","failed"]},"notes":{"type":"string"}},"required":["status"]}' \
  --permission-mode auto \
  --settings '{"autoMode":{"environment":["$defaults"]}}' \
  --no-update-notification
```

Auto mode replaces `--dangerously-skip-permissions` and `--allowedTools` — the classifier permits routine coding operations by default and blocks destructive/exfiltrating actions. The `environment: ["$defaults"]` trusts the working repo and its configured remotes.

`--json-schema` forces the model's final response into a structured object. The result is read from `stdout | jq '.structured_output'`:
- `{"status":"success","notes":"..."}` → proceed to merge; notes included in commit body
- `{"status":"failed","notes":"..."}` → mark snag failed; notes stored as failure reason

4. If `status == "success"` and exit 0: in the project root (not the worktree), run:
   ```sh
   git merge --squash snag/<id>
   git commit -m "snag: <description>" -m "<notes>"   # notes omitted if empty
   ```
   This produces a single clean commit per snag. The model's notes appear in the commit body.

### Prompt template

```
You are working autonomously in a git worktree to complete a small code change (a "snag").
The project is already checked out. Do not ask for clarification — use your best judgement.

Snag: <description>

Complete the task fully. Your final response must be a JSON object with:
- "status": "success" if the task is complete, or "failed" if you could not complete it
- "notes": any assumptions you made, decisions you took, or (if failed) why you could not complete it
```

### Merge conflict resolution

If `git merge --squash` fails (conflicts in working tree):
1. Dispatch a second `claude` agent in the project root:
   ```
   A git merge --squash conflict occurred while merging branch snag/<id>.
   Resolve the conflicts in the working tree, then run: git commit -m "snag: <description>"
   ```
2. If that succeeds → mark `complete`
3. If that also fails → mark `failed`, leave worktree in place for manual inspection

### Failure paths

| Scenario | Outcome |
|----------|---------|
| `structured_output.status == "failed"` | Mark `failed`, `notes` = `structured_output.notes` |
| claude exits non-zero or JSON unparseable | Mark `failed`, `notes` = stderr |
| `git merge --squash` fails, resolver agent fails | Mark `failed`, `notes` = merge error; worktree preserved |
| Worktree creation fails | Mark `failed`, `notes` = stderr |

On success: worktree removed (`git worktree remove --force`), branch deleted.

### Pause behavior

Pause prevents the *next* snag from being dispatched. An in-flight claude process always runs to completion. Its result is still applied normally; the worker then stops and waits for resume.

---

## Error Handling & Edge Cases

- **Not a git repo:** checked on launch before TUI starts; exit with clear error message
- **`claude` not in PATH:** checked on launch; exit with clear error message
- **Deleting an inflight snag:** `backspace` is ignored on inflight rows
- **Concurrent launches:** no file locking; two instances in the same directory will corrupt state — documented as known limitation
- **Mid-run exit:** in-flight claude subprocess killed, snag reset to `pending`, state flushed to disk

---

## Out of Scope

- History view for completed snags
- Parallel snag processing
- Daemon mode (snags survive closing the TUI)
- File locking for concurrent instances
