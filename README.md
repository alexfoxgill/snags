# snags

A TUI for queuing and autonomously processing small coding tasks. Each snag is executed by Claude Code in an isolated git worktree and squash-merged back to your default branch.

## Requirements

- Go 1.21+
- `claude` CLI ([Claude Code](https://claude.ai/code)) in your PATH
- A git repository

## Install

```sh
go install .
```

## Usage

Run `snags` from inside any git repository:

```sh
snags
```

Type a task description and press **Enter** to add it to the queue. Snags are processed one at a time automatically.

### Flags

| Flag | Description |
|------|-------------|
| `--paused` | Start without automatically processing pending snags |
| `--debug` | Log debug events to `.snags/debug.log` |

### Keybindings

| Key | Action |
|-----|--------|
| `Enter` | Add snag |
| `↑ / ↓` | Navigate list |
| `Alt+↑ / Alt+↓` | Reorder snag |
| `e` | Edit selected snag |
| `r` | Retry failed snag |
| `Backspace` | Delete selected snag |
| `Ctrl+P` | Pause / resume processing |
| `Esc` | Clear input / quit |
| `Ctrl+C` | Quit |

## How it works

1. Snags are persisted to `.snags/state.yaml` in the repo root (gitignored).
2. When processing starts, Claude Code runs in a fresh worktree at `.snags/worktrees/<id>` on branch `snag/<id>`.
3. On success, the worktree is squash-merged back to your default branch and removed.
4. On failure, the snag is marked failed and can be retried with `r`.
5. Merge conflicts are resolved automatically by a second Claude call.
