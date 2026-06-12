# snags

A TUI for queuing and autonomously processing small coding tasks. Each snag is executed by Claude Code in an isolated git worktree and squash-merged back to your default branch.

## Requirements

- Go 1.26.2+
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
| `Enter` | Add snag (input) / open details (list) |
| `↑ / ↓` | Navigate list |
| `Alt+↑ / Alt+↓` | Reorder snag |
| `e` | Edit selected snag |
| `r` | Retry failed snag |
| `m` | Agentic merge (failed snag with preserved branch) |
| `Ctrl+S` | Scan for inline markers |
| `Tab` | Show / hide completed snags |
| `Backspace` | Delete snag (or revert a completed one) |
| `Ctrl+P` | Pause / resume processing |
| `Esc` | Clear input / quit |
| `Ctrl+C` | Quit |

## How it works

1. Snags are persisted to `.snags/state.yaml` in the repo root (gitignored).
2. When processing starts, Claude Code runs in a fresh worktree at `.snags/worktrees/<id>` on branch `snag/<id>`.
3. On success, the worktree is squash-merged back to your default branch and removed.
4. On failure, the snag is marked failed and can be retried with `r`.
5. Merge conflicts preserve branch `snag/<id>`. Press `m` to run an agentic merge that resolves them.

## Inline markers

Add a comment anywhere in your code and press `Ctrl+S` to scan:

```js
// snag: rename this function to processPayment
```

Supported comment styles: `//`, `#`, `--`, `/* */`, `<!-- -->`. Continuation lines work for line-comment styles:

```python
# snag: refactor this loop to use list
# comprehension and handle the empty case
```

On success the marker comment is deleted before the squash commit. Markers already committed to HEAD are skipped — the agent's branch deletion propagates via the merge.

## Config

Create `.snags/config.yaml` to override defaults:

```yaml
marker: snag  # keyword matched in comment markers

agents:
  snag:
    model: fable
    effort: low
    timeout: 15m
    extra_args: []
  summary:
    model: haiku
    effort: medium
    timeout: 2m
    extra_args: []
  merge:
    model: sonnet
    effort: medium
    timeout: 10m
    extra_args: []
```

`effort` must be `low`, `medium`, or `high` (or omitted to use the CLI default).

## Details page

Press `Enter` on any list item to open the details page: snag metadata, agent notes, and a scrollable session transcript showing every tool call and agent message. `↑/↓` scrolls line by line, `PgUp/PgDn` pages. `Esc` or `Enter` returns to the list. Transcripts are stored as JSONL at `.snags/logs/<id>.jsonl`.
