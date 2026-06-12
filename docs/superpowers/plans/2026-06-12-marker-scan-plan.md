# Marker Scan — Design & Implementation Plan

Port inker's marker workflow into snags: scan the working tree for inline
`// snag:` comment markers and enqueue each as a snag. Plus: yaml config,
one-line summaries for marker snags, and a details page with session history.

## Design

### Marker scan and lifecycle

- **Trigger**: `ctrl+s` in the TUI (works from input and list focus).
- **Discovery**: `git grep -nIE --untracked` for
  `(//|#|--|/\*|<!--)\s*snag:\s*(.+)` (keyword configurable). Tracked +
  untracked files, gitignore respected. The request continues across
  immediately-following comment lines with the same comment leader; stops at a
  non-comment line, blank comment, or another marker.
- **Capture**: one snag per marker. `Description` = marker text, plus
  `Source: marker`, `File`, `Line`, and a ~15-line context snippet. The marker
  stays in the file while pending/inflight — it is the in-code status
  indicator. Re-scans dedupe by file + marker text against non-complete
  marker snags.
- **Execution**: unchanged pipeline (worktree from default branch, serial
  queue, squash-merge back). Marker-snag prompt adds file/line/context and an
  instruction to remove the marker comment if present in the checkout.
- **Completion**: after the worktree commit, before `git merge --squash`, the
  app deletes the marker line(s) from the working-tree file, matched by
  content. If the marker was the file's only uncommitted change this makes
  the file identical to HEAD, so the merge proceeds. Guard: if the marker is
  already in HEAD, skip working-tree deletion — the agent's branch removes it
  and the merge propagates that.
- **Failure**: marker stays in the file; snag shows failed; `r` retries.
  Known edge: other uncommitted edits in the marker's file make the merge
  refuse; the snag fails with git's message (same as typed snags today).
  Merge failures are different to agent failures; the branch is preserved and
  an option is presented to try an agentic merge.

### Config

`.snags/config.yaml`, loaded at startup, all fields optional. Defaults:

```yaml
agents:
  snag: # agent config for resolving snags
    model: fable
    effort: low
    timeout: 15m
    extra_args: []
  summary: # agent config for generating marker summaries
  	model: haiku
  	effort: medium
  	timeout: 2m
 	extra_args: []
  merge: # agent config for resolving merge conflicts
  	model: sonnet
  	effort: medium
  	timeout: 2m
  	extra_args: []
```

Malformed config is a startup error.

### Summaries

Typed snags keep their description. For marker snags, an async one-shot call
(`summary_model`, no tools, text output) turns marker text + context into a
one-line summary, stored in `Snag.Summary`. The list shows `Summary` when
set, else marker text. Failures fall back silently; the queue never blocks.

### Details page and session history

- `runClaudeHeadless` tees assistant text, tool calls, and the result as
  JSON lines to `.snags/logs/<id>.jsonl`, with a run separator per attempt.
- `enter` on a list row opens a full-screen page: description + summary,
  status, `file:line` for marker snags, created time, duration, commit hash,
  notes, then the scrollable transcript. `↑↓`/`pgup`/`pgdn` scroll, `esc`
  closes. Inflight snags refresh as progress events arrive.
- `Snag` gains `Source` (`input`|`marker`), `File`, `Line`, `Context`,
  `Summary` — all `omitempty`, existing state files load unchanged.

## Implementation order

1. **`config.go` (new)** — `Config` struct, `LoadConfig(projectRoot)` with
   defaults when absent, error on malformed yaml/duration. Threaded into
   `Model` and the worker.
2. **`state.go`** — new `Snag` fields; `EnsureSnagDir` creates `.snags/logs/`.
3. **`scanner.go` (new)** — `ScanMarkers(projectRoot, keyword)` (git grep +
   continuation parsing + context snippet), `DeleteMarker(projectRoot, file,
   markerText)` (content-matched removal, skipped when marker is in HEAD).
   Tests: comment styles, continuation, custom keyword, dedupe, deletion in
   temp git repos.
4. **`worker.go`** — config-driven invocation (model/effort/extra_args),
   transcript tee, marker prompt variant, timeout context, `DeleteMarker`
   between `commitWorktreeChanges` and `squashMerge`, `runSummary`.
5. **`model.go` + `keys.go`** — scan command + `scanDoneMsg` (enqueue,
   dedupe, kick queue, fire summary commands), `summaryDoneMsg`, summary
   display fallback, details view mode with scrolling and live refresh.
6. **`main.go`** — load config, pass through.
7. **Verify** — `go test ./...`, `go build`, live test in a scratch repo:
   drop a marker, scan, watch it resolve, confirm marker deletion and merge.
