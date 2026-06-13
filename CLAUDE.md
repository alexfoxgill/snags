This project (`snags`) is a TUI application that maintains a queue of small coding tasks ("snags") and processes them autonomously.
Each snag is executed by spawning `claude` CLI in headless mode inside an isolated git worktree, then squash-merging the result back to its base branch: the default branch for typed snags, the current branch for marker snags (`baseBranchFor`).

## Notes on This Repo
- Committing master will automatically reinstall the app to the user.

## Architecture

The app is a single Go package (`main`) with ten files:

**`main.go`** — entry point. Validates prerequisites (git repo, `claude` in PATH), loads state, then hands off to BubbleTea.

**`model.go`** — BubbleTea `Model`. Owns all TUI state: snag list, cursor/scroll position, text input, spinner, and the `cancelWork` context cancel func. On `startWorkMsg`, calls `startNextSnag()` which picks the first `pending` snag, flips it to `inflight`, creates a `RunSnag` goroutine, and listens on its channel via `waitForSnagEvent`. Progress arrives as `snagProgressMsg`; completion as `snagDoneMsg`. On done, the model saves state and immediately tries the next snag. Newly scanned marker snags trigger a `summaryCmd` (haiku agent) that produces a short display title.

**`worker.go`** — snag execution pipeline. `RunSnag` creates a git worktree at `.snags/worktrees/<id>` on a branch `snag/<id>` off the snag's base branch (`baseBranchFor`: current branch for marker snags, detected default branch otherwise), invokes `claude` with `--output-format stream-json --permission-mode auto`, parses the NDJSON stream to extract tool-call activity (for the status bar) and the final structured-output result (`{"status":"success"|"failed","notes":"..."}`). On success, calls `mergeStage`: deletes the inline marker from the working tree (marker snags only), then squash-merges the branch. On merge conflict the worktree is removed but `snag/<id>` is preserved; the user triggers an agentic merge with `m`. `agenticMergeCmd` runs a headless Claude in the project root to perform the merge and resolve conflicts; it only deletes the branch once a snag commit is verified on HEAD.

**`state.go`** — persistence. `State` (containing `[]Snag`) is marshalled to `.snags/state.yaml`. `LoadState` resets any `inflight` snags to `pending` on startup (crash recovery). `EnsureSnagDir` also appends `.snags/` to `.gitignore`.

**`config.go`** — YAML config at `.snags/config.yaml`. `Config` holds the marker keyword (default `snag`) and per-agent settings (`AgentConfig`: model, effort, timeout, extra_args) for three agents: `snag`, `summary`, and `merge`. Missing file falls back to `DefaultConfig()`.

**`scanner.go`** — `ScanMarkers` uses `git grep` as a prefilter then parses files to find `<keyword>:` comment markers (supports `//`, `#`, `--`, `/*`, `<!--`; continuation lines for line-comment styles). `DeleteMarker` removes a matched marker from the working tree, no-op if already committed or absent.

**`transcript.go`** — JSONL session logs at `.snags/logs/<id>.jsonl`. Each run (agent, merge, revert) appends `run_start`, `tool`, `text`, and `result` events. `readTranscript` skips unparseable lines so partial trailing writes are safe.

**`details.go`** — details page (opened with Enter on a list item). Shows snag metadata, notes, and the scrollable transcript. Keys: `↑/↓` scroll, `pgup/pgdn` page, `Esc`/`Enter` back to list.

**`keys.go`** — BubbleTea key bindings. All keybindings are centralised here.

**`debug.go`** — `--debug` support: opens `.snags/debug.log` and exposes the package-level `debugLog` logger (nil when disabled).

## App Functionality

- App creates a gitignored `.snags/` directory in the repo root where it is run.
- `.snags/state.yaml` stores current state.
- `.snags/config.yaml` (optional) overrides the marker keyword and per-agent model/effort/timeout/extra_args.
- `.snags/logs/<id>.jsonl` stores JSONL session transcripts for each snag.
- Git worktrees live at `.snags/worktrees/<snagID>`. They are created fresh per run and removed after success or failure. Orphan cleanup runs defensively at worktree creation time.
- Claude is invoked with a JSON schema enforcing the `{status, notes}` output shape.
- `Ctrl+S` scans the working tree for `// snag: ...` style markers via `git grep`, deduplicated against existing snags. Each new marker becomes a pending snag; a summary agent produces a short display title. On successful merge the marker is deleted from the working tree before committing.
- Merge failures preserve `snag/<id>`. Marker snags automatically run `agenticMergeCmd` on merge failure; for typed snags the user triggers it by pressing `m` on a failed snag with a preserved branch. `agenticMergeCmd` is a headless Claude in the project root that performs the squash merge and resolves any conflicts.
