This project (`snags`) is a TUI application that maintains a queue of small coding tasks ("snags") and processes them autonomously.
Each snag is executed by spawning `claude` CLI in headless mode inside an isolated git worktree, then squash-merging the result back to its base branch: the default branch for typed snags, the current branch for marker snags (`baseBranchFor`).

## Notes on This Repo
- Committing master will automatically reinstall the app to the user.

## Architecture

The app is a single Go package (`main`) with eleven files:

**`main.go`** — entry point. Validates prerequisites (git repo, `claude` in PATH), loads state, then hands off to BubbleTea.

**`model.go`** — BubbleTea `Model`. Owns all TUI state: snag list, cursor/scroll position, text input, spinner, and the `cancelWork` context cancel func. On `startWorkMsg`, calls `startNextSnag()` which picks the first `pending` snag, flips it to `inflight`, creates a `RunSnag` goroutine, and listens on its channel via `waitForSnagEvent`. Progress arrives as `snagProgressMsg`; completion as `snagDoneMsg`. On done, the model saves state and immediately tries the next snag. Newly scanned marker snags trigger a `summaryCmd` (haiku agent) that produces a short display title.

**`worker.go`** — snag execution pipeline. `RunSnag` creates a git worktree at `.snags/worktrees/<id>` on a branch `snag/<id>` off the snag's base branch (`baseBranchFor`: current branch for marker snags, detected default branch otherwise), invokes `claude` with `--output-format stream-json --permission-mode auto`, parses the NDJSON stream to extract tool-call activity (for the status bar) and the final structured-output result (`{"status":"success"|"failed","notes":"..."}`). On success, marker snags land via a per-file 3-way apply (`applyMarkerMergeStage` in `apply.go`): it commits the branch's touched paths onto the base branch through a throwaway index (never touching the live tree), then `git merge-file`s each touched path into the live working tree, after deleting the marker. Non-overlapping user edits and other pending markers merge silently; a true same-line overlap lands the commit but leaves conflict markers in the file and preserves `snag/<id>` for manual resolution. Typed snags still squash-merge (`squashMerge`) and run the agentic merge on conflict: on conflict the worktree is removed but `snag/<id>` is preserved and the user triggers an agentic merge with `m`. `agenticMergeCmd` runs a headless Claude in the project root to perform the merge and resolve conflicts; it only deletes the branch once a snag commit is verified on HEAD.

**`state.go`** — persistence. `State` (containing `[]Snag`) is marshalled to `.snags/state.yaml`. `LoadState` resets any `inflight` snags to `pending` on startup (crash recovery). `EnsureSnagDir` also appends `.snags/` to `.gitignore`.

**`config.go`** — YAML config at `.snags/config.yaml`. `Config` holds the marker keyword (default `snag`) and per-agent settings (`AgentConfig`: model, effort, timeout, extra_args) for three agents: `snag`, `summary`, and `merge`. Missing file falls back to `DefaultConfig()`.

**`scanner.go`** — `ScanMarkers` uses `git grep` as a prefilter then parses files to find `<keyword>:` comment markers (supports `//`, `#`, `--`, `/*`, `<!--`; continuation lines for line-comment styles). `DeleteMarker` removes a matched marker from the working tree, no-op if already committed or absent.

**`transcript.go`** — JSONL session logs at `.snags/logs/<id>.jsonl`. Each run (agent, merge, revert) appends `run_start`, `tool`, `text`, and `result` events. `readTranscript` skips unparseable lines so partial trailing writes are safe.

**`details.go`** — details page (opened with Enter on a list item). Shows snag metadata, notes, and the scrollable transcript. Keys: `↑/↓` scroll, `pgup/pgdn` page, `Esc`/`Enter` back to list.

**`keys.go`** — BubbleTea key bindings. All keybindings are centralised here.

**`apply.go`** — marker-snag landing: commits the branch's touched paths via a throwaway index, then 3-way merges each into the live working tree with `git merge-file` (ported from the `inker` project's applier).

**`debug.go`** — `--debug` support: opens `.snags/debug.log` and exposes the package-level `debugLog` logger (nil when disabled).

## App Functionality

- App creates a gitignored `.snags/` directory in the repo root where it is run.
- `.snags/state.yaml` stores current state.
- `.snags/config.yaml` (optional) overrides the marker keyword and per-agent model/effort/timeout/extra_args.
- `.snags/logs/<id>.jsonl` stores JSONL session transcripts for each snag.
- Git worktrees live at `.snags/worktrees/<snagID>`. They are created fresh per run and removed after success or failure. Orphan cleanup runs defensively at worktree creation time.
- Claude is invoked with a JSON schema enforcing the `{status, notes}` output shape.
- `Ctrl+S` scans the working tree for `// snag: ...` style markers via `git grep`, deduplicated against existing snags. Each new marker becomes a pending snag; a summary agent produces a short display title. On successful merge the marker is deleted from the working tree before committing.
- Marker snags land via a per-file 3-way apply into the live working tree; non-overlapping edits merge silently, while a true same-line overlap leaves conflict markers in the file and preserves `snag/<id>` for manual resolution. Typed-snag merge failures preserve `snag/<id>`; the user triggers an agentic merge by pressing `m` on a failed snag with a preserved branch. `agenticMergeCmd` is a headless Claude in the project root that performs the squash merge and resolves any conflicts.
