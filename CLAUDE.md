This project (`snags`) is a TUI application that maintains a queue of small coding tasks ("snags") and processes them autonomously.
Each snag is executed by spawning `claude` CLI in headless mode inside an isolated git worktree, then squash-merging the result back to the default branch.

## Notes on This Repo
- Committing master will automatically reinstall the app to the user.

## Architecture

The app is a single Go package (`main`) with five files:

**`main.go`** — entry point. Validates prerequisites (git repo, `claude` in PATH), loads state, then hands off to BubbleTea.

**`model.go`** — BubbleTea `Model`. Owns all TUI state: snag list, cursor/scroll position, text input, spinner, and the `cancelWork` context cancel func. On `startWorkMsg`, calls `startNextSnag()` which picks the first `pending` snag, flips it to `inflight`, creates a `RunSnag` goroutine, and listens on its channel via `waitForSnagEvent`. Progress arrives as `snagProgressMsg`; completion as `snagDoneMsg`. On done, the model squash-merges the result, saves state, and immediately tries the next snag.

**`worker.go`** — snag execution pipeline. `RunSnag` creates a git worktree at `.snags/worktrees/<id>` on a branch `snag/<id>`, invokes `claude` with `--output-format stream-json --permission-mode auto`, parses the NDJSON stream to extract tool-call activity (for the status bar) and the final structured-output result (`{"status":"success"|"failed","notes":"..."}`), then squash-merges on success. On merge conflict, spawns a second headless Claude call to resolve it.

**`state.go`** — persistence. `State` (containing `[]Snag`) is marshalled to `.snags/state.yaml`. `LoadState` resets any `inflight` snags to `pending` on startup (crash recovery). `EnsureSnagDir` also appends `.snags/` to `.gitignore`.

**`keys.go`** — BubbleTea key bindings. All keybindings are centralised here.

## App Functionality

- App creates a gitignored `.snags/` directory in the repo root where it is run.
- `.snags/state.yaml` stores current state
- Git worktrees live at `.snags/worktrees/<snagID>`. They are created fresh per run and removed after success or failure. Orphan cleanup runs defensively at worktree creation time.
- Claude is invoked with a JSON schema enforcing the `{status, notes}` output shape.
