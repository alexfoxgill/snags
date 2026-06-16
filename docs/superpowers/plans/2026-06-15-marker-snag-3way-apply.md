# Marker-Snag 3-Way Apply Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land a completed marker snag into the live working tree with a per-file 3-way merge (ported from `~/git/inker`'s `applier`) instead of `git merge --squash`, so the common case no longer always falls through to the agentic merge.

**Architecture:** For marker snags, replace `squashMerge` with two steps that never require a clean working tree: (1) commit only the snag branch's touched paths onto the base branch via a throwaway index (`commit-tree`, never reading the live tree); (2) update each touched path in the live working tree with a per-file 3-way merge using `git merge-file`. Non-overlapping user edits / other pending markers merge silently; a true same-line overlap leaves standard `<<<<<<<` conflict markers. Typed snags keep using `squashMerge` unchanged.

**Tech Stack:** Go (package `main`, flat single-package repo), git CLI plumbing (`merge-base`, `ls-tree`, `cat-file`, `read-tree`/`update-index`/`write-tree`/`commit-tree`/`update-ref`, `merge-file`).

---

## Background (why the naive merge always fails)

Reproduced empirically: a **clean** tree merges fine with `git merge --squash`. The failure is that the live tree is almost never clean for a marker snag — the *other* pending `// snag:` markers (and any WIP) sit uncommitted, and `git merge --squash` aborts whenever the snag branch touches a file with any unrelated uncommitted change. `git apply --3way` does not help (refuses over a dirty index). Inker's per-file 3-way merge sidesteps this entirely: it commits via a temp index (ignoring the live tree) and merges each file in place.

## Conflict policy (decided)

Commit-first, like inker. The snag commit **always lands** once the agent succeeded and there are changes. If a per-file 3-way merge leaves conflict markers, the snag is still marked complete (commit landed), the conflicted file keeps its `<<<<<<<` markers in the live tree for manual resolution, the note names the conflicted files, and branch `snag/<id>` is **preserved** (worktree removed, branch kept) for inspection/recovery. Auto-agentic does **not** fire for this case. (Making `m`/agentic resolve already-committed conflict markers is a non-goal here — out of scope.)

## File structure

- **Create `apply.go`** — all ported git plumbing + the marker apply entry point. New unexported funcs (package `main`): `gitOut`, `gitOutRaw`, `gitOutEnv`, `tempIndexEnv`, `entryAt`, `blobAt`, `changedPaths`, `mergeBaseRev`, `commitTouchedPaths`, `mergeLive`, `mergeOnePath`, `writeResultFile`, `mergeFileInPlace`, `isBinaryBlob`, and `applyMarkerMergeStage`.
- **Create `apply_test.go`** — unit tests for the plumbing and the merge cases.
- **Modify `worker.go`** — add `conflict bool` to `snagDoneMsg`; in `mergeStage`, route `SourceMarker` snags to `applyMarkerMergeStage` and keep the existing `squashMerge` path for typed snags only.
- **Modify `model.go`** — on a successful `snagDoneMsg` with `conflict`, preserve `Branch` and do not clear it; never auto-trigger agentic for the conflict case.
- **Modify `worker_test.go` / `model_test.go`** — tests for the new routing and model handling.
- **Modify `CLAUDE.md` + `README.md`** — document the new marker merge mechanism.

---

## Task 1: Port git plumbing helpers into `apply.go`

**Files:**
- Create: `/Users/alex/snags/apply.go`
- Test: `/Users/alex/snags/apply_test.go`

These are ported (renamed to unexported, package `main`) from `~/git/inker/internal/gitx/gitx.go`. snags already uses `exec.Command("git", ...)` directly elsewhere; these helpers are local to the apply path.

- [ ] **Step 1: Write `apply.go` with the plumbing helpers**

```go
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitOut runs git with args in dir and returns trimmed stdout.
func gitOut(dir string, args ...string) (string, error) {
	out, err := gitOutRaw(dir, args...)
	return strings.TrimSpace(string(out)), err
}

// gitOutRaw is gitOut without trimming — for blob contents.
func gitOutRaw(dir string, args ...string) ([]byte, error) {
	return gitOutEnvRaw(dir, nil, args...)
}

// gitOutEnv is gitOut with extra environment variables (e.g. GIT_INDEX_FILE).
func gitOutEnv(dir string, env []string, args ...string) (string, error) {
	out, err := gitOutEnvRaw(dir, env, args...)
	return strings.TrimSpace(string(out)), err
}

func gitOutEnvRaw(dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %s: %w",
			strings.Join(args, " "), strings.TrimSpace(errb.String()), err)
	}
	return out.Bytes(), nil
}

// tempIndexEnv reserves a unique throwaway index file inside the git dir and
// returns the env entry pointing git at it plus a cleanup func.
func tempIndexEnv(dir string) (env []string, cleanup func(), err error) {
	gd, err := gitOut(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, nil, err
	}
	f, err := os.CreateTemp(gd, "snags-index-*")
	if err != nil {
		return nil, nil, err
	}
	path := f.Name()
	f.Close()
	os.Remove(path)
	return []string{"GIT_INDEX_FILE=" + path}, func() { os.Remove(path) }, nil
}

// entryAt returns the mode and blob hash of path in rev, ok=false if absent.
func entryAt(dir, rev, path string) (mode, hash string, ok bool, err error) {
	out, err := gitOut(dir, "ls-tree", "-r", rev, "--", path)
	if err != nil {
		return "", "", false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// <mode> SP <type> SP <hash> TAB <path>
		meta, name, found := strings.Cut(line, "\t")
		if !found {
			continue
		}
		if strings.HasPrefix(name, `"`) {
			return "", "", false, fmt.Errorf("path with special characters not supported: %s", name)
		}
		if name != path {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return "", "", false, fmt.Errorf("unexpected ls-tree line: %q", line)
		}
		return fields[0], fields[2], true, nil
	}
	return "", "", false, nil
}

// blobAt returns the contents of path at rev, ok=false if absent.
func blobAt(dir, rev, path string) (data []byte, ok bool, err error) {
	_, hash, ok, err := entryAt(dir, rev, path)
	if err != nil || !ok {
		return nil, ok, err
	}
	data, err = gitOutRaw(dir, "cat-file", "blob", hash)
	return data, true, err
}

// changedPaths lists paths that differ between two tree-ish revisions.
func changedPaths(dir, a, b string) ([]string, error) {
	out, err := gitOut(dir, "diff", "--name-only", "--no-renames", a, b)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// mergeBaseRev returns the common ancestor commit of a and b.
func mergeBaseRev(dir, a, b string) (string, error) {
	return gitOut(dir, "merge-base", a, b)
}
```

- [ ] **Step 2: Write `apply_test.go` with a repo helper and tests for the plumbing**

`worker_test.go` already defines `gitRun`, `initMergeTestRepo`, etc. in package `main`, so `apply_test.go` can reuse them directly. Add focused tests:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChangedPathsAndBlobAt(t *testing.T) {
	dir := initMergeTestRepo(t) // commits file.txt="original\n" on master
	gitRun(t, dir, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "feature change")

	base, err := mergeBaseRev(dir, "master", "feature")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := changedPaths(dir, base, "feature")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range paths {
		got[p] = true
	}
	if !got["file.txt"] || !got["new.txt"] || len(got) != 2 {
		t.Fatalf("changedPaths = %v, want file.txt + new.txt", paths)
	}

	data, ok, err := blobAt(dir, "feature", "file.txt")
	if err != nil || !ok || string(data) != "changed\n" {
		t.Fatalf("blobAt feature file.txt = %q ok=%v err=%v", data, ok, err)
	}
	if _, ok, _ := blobAt(dir, "master", "new.txt"); ok {
		t.Fatal("blobAt master new.txt: expected absent")
	}
}
```

- [ ] **Step 3: Run the tests**

Run: `cd /Users/alex/snags && go test ./... -run 'TestChangedPathsAndBlobAt' -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd /Users/alex/snags
git add apply.go apply_test.go
git commit -m "snag: add git plumbing helpers for marker 3-way apply

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: Port the commit-via-temp-index and per-file 3-way merge

**Files:**
- Modify: `/Users/alex/snags/apply.go`
- Test: `/Users/alex/snags/apply_test.go`

- [ ] **Step 1: Append `commitTouchedPaths` to `apply.go`**

Commits, onto the current branch, a tree equal to HEAD's tree with each touched path swapped to its state in `srcRev` (or removed if absent there). Never reads the live working tree or the real index. CAS-guards the branch update against the HEAD read at entry. `message` and `notes` become separate commit-message paragraphs (matching `squashMerge`'s `-m`/`-m`).

```go
// commitTouchedPaths commits the given paths (taken from srcRev) onto
// baseBranch as one commit parented at the current HEAD. It builds the tree in
// a throwaway index, so the live working tree and real index are untouched.
// Returns the new commit hash.
func commitTouchedPaths(dir string, paths []string, srcRev, baseBranch, message, notes string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("commitTouchedPaths: no paths to commit")
	}
	head, err := gitOut(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	env, cleanup, err := tempIndexEnv(dir)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if _, err := gitOutEnv(dir, env, "read-tree", head); err != nil {
		return "", err
	}
	for _, p := range paths {
		mode, hash, ok, err := entryAt(dir, srcRev, p)
		if err != nil {
			return "", err
		}
		if ok {
			_, err = gitOutEnv(dir, env, "update-index", "--add",
				"--cacheinfo", mode+","+hash+","+p)
		} else {
			_, err = gitOutEnv(dir, env, "update-index", "--force-remove", "--", p)
		}
		if err != nil {
			return "", err
		}
	}
	tree, err := gitOutEnv(dir, env, "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree, "-p", head, "-m", message}
	if notes != "" {
		args = append(args, "-m", notes)
	}
	commit, err := gitOut(dir, args...)
	if err != nil {
		return "", err
	}
	if _, err := gitOut(dir, "update-ref", "refs/heads/"+baseBranch, commit, head); err != nil {
		return "", fmt.Errorf("branch moved during apply: %w", err)
	}
	return commit, nil
}
```

- [ ] **Step 2: Append the per-file 3-way merge to `apply.go`**

```go
// mergeLive updates each touched path in the live working tree (at dir) to the
// agent's version, 3-way merging against edits present in the live tree but not
// in baseRev. Returns the paths left conflicted (markers written, or held back
// from deletion/restoration).
func mergeLive(dir, baseRev, srcRev string, touched []string) ([]string, error) {
	var conflicts []string
	for _, p := range touched {
		c, err := mergeOnePath(dir, baseRev, srcRev, p)
		if err != nil {
			return conflicts, fmt.Errorf("%s: %w", p, err)
		}
		if c {
			conflicts = append(conflicts, p)
		}
	}
	return conflicts, nil
}

func mergeOnePath(dir, baseRev, srcRev, path string) (conflict bool, err error) {
	base, baseOK, err := blobAt(dir, baseRev, path)
	if err != nil {
		return false, err
	}
	theirs, theirsOK, err := blobAt(dir, srcRev, path)
	if err != nil {
		return false, err
	}
	full := filepath.Join(dir, path)
	cur, curErr := os.ReadFile(full)
	curOK := curErr == nil
	if curErr != nil && !errors.Is(curErr, fs.ErrNotExist) {
		return false, curErr
	}

	switch {
	case !theirsOK: // agent deleted the file
		if !curOK {
			return false, nil // user deleted it too
		}
		if baseOK && bytes.Equal(cur, base) {
			return false, os.Remove(full) // untouched since base: delete
		}
		return true, nil // user edited since base: keep the user's file, flag it
	case !curOK:
		if baseOK {
			// Existed at base but the user deleted it since, while the agent
			// edited it. Restore the agent's version and flag.
			return true, writeResultFile(dir, srcRev, path, theirs)
		}
		return false, writeResultFile(dir, srcRev, path, theirs) // agent-added file
	case bytes.Equal(cur, theirs):
		return false, nil // already identical
	case bytes.Equal(cur, base):
		return false, writeResultFile(dir, srcRev, path, theirs) // clean overwrite
	case isBinaryBlob(base) || isBinaryBlob(theirs) || isBinaryBlob(cur):
		return true, nil // text merge would corrupt binary; keep the user's file, flag it
	case !baseOK:
		// Both sides created the file independently: merge against an empty base.
		return mergeFileInPlace(dir, full, nil, theirs)
	default:
		return mergeFileInPlace(dir, full, base, theirs)
	}
}

// isBinaryBlob mirrors git's heuristic: a NUL byte in the first 8000 bytes.
func isBinaryBlob(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(b[:n], 0) >= 0
}

// writeResultFile writes the agent's version of path into the live tree,
// applying the executable bit recorded in srcRev.
func writeResultFile(dir, srcRev, path string, data []byte) error {
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if m, _, ok, err := entryAt(dir, srcRev, path); err == nil && ok && m == "100755" {
		mode = 0o755
	}
	if err := os.WriteFile(full, data, mode); err != nil {
		return err
	}
	return os.Chmod(full, mode) // WriteFile mode only applies on create
}

// mergeFileInPlace runs `git merge-file` on the live file (in place). Exit
// status >0 means that many conflicts; <0 means error.
func mergeFileInPlace(dir, full string, base, theirs []byte) (bool, error) {
	tmp, err := os.MkdirTemp("", "snags-merge-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	basePath := filepath.Join(tmp, "base")
	theirsPath := filepath.Join(tmp, "agent")
	if err := os.WriteFile(basePath, base, 0o644); err != nil {
		return false, err
	}
	if err := os.WriteFile(theirsPath, theirs, 0o644); err != nil {
		return false, err
	}
	cmd := exec.Command("git", "merge-file",
		"-L", "yours", "-L", "base", "-L", "agent",
		full, basePath, theirsPath)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() > 0 {
			return true, nil // merged with conflicts left in the file
		}
		return false, fmt.Errorf("git merge-file: %v\n%s", err, out)
	}
	return false, nil
}
```

- [ ] **Step 3: Add tests for commit + merge to `apply_test.go`**

```go
func TestCommitTouchedPathsDoesNotTouchLiveTree(t *testing.T) {
	dir := initMergeTestRepo(t)
	// Snag branch off master changes file.txt.
	gitRun(t, dir, "branch", "snag/x", "master")
	gitRun(t, dir, "worktree", "add", "-q", filepath.Join(dir, "wt"), "snag/x")
	if err := os.WriteFile(filepath.Join(dir, "wt", "file.txt"), []byte("agent\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, filepath.Join(dir, "wt"), "commit", "-am", "snag: agent change")

	// Live tree has unrelated uncommitted WIP in file.txt.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("wip\n"), 0644); err != nil {
		t.Fatal(err)
	}

	base, _ := mergeBaseRev(dir, "master", "snag/x")
	touched, _ := changedPaths(dir, base, "snag/x")
	hash, err := commitTouchedPaths(dir, touched, "snag/x", "master", "snag: agent change", "did it")
	if err != nil {
		t.Fatal(err)
	}
	// HEAD now contains the agent version...
	data, _, _ := blobAt(dir, "HEAD", "file.txt")
	if string(data) != "agent\n" {
		t.Errorf("committed file.txt = %q, want agent\\n", data)
	}
	// ...but the live tree still has the user's WIP, uncommitted.
	live, _ := os.ReadFile(filepath.Join(dir, "file.txt"))
	if string(live) != "wip\n" {
		t.Errorf("live file.txt = %q, want wip\\n (commit must not touch live tree)", live)
	}
	if hash == "" {
		t.Error("expected a commit hash")
	}
}

func TestMergeLiveNonOverlapMergesClean(t *testing.T) {
	dir := initMergeTestRepo(t) // file.txt = "original\n"
	// Replace file.txt with multi-line content as the base.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"),
		[]byte("line A\nline B\nline C\nline D\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "commit", "-am", "multiline base")
	// Snag branch edits line D.
	gitRun(t, dir, "branch", "snag/y", "master")
	gitRun(t, dir, "worktree", "add", "-q", filepath.Join(dir, "wt"), "snag/y")
	if err := os.WriteFile(filepath.Join(dir, "wt", "file.txt"),
		[]byte("line A\nline B\nline C\nline D IMPROVED\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, filepath.Join(dir, "wt"), "commit", "-am", "snag: tweak D")
	// Live tree has unrelated WIP on line A.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"),
		[]byte("line A EDITED\nline B\nline C\nline D\n"), 0644); err != nil {
		t.Fatal(err)
	}

	base, _ := mergeBaseRev(dir, "master", "snag/y")
	conflicts, err := mergeLive(dir, base, "snag/y", []string{"file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", conflicts)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "file.txt"))
	want := "line A EDITED\nline B\nline C\nline D IMPROVED\n"
	if string(got) != want {
		t.Errorf("merged live = %q, want %q", got, want)
	}
}

func TestMergeLiveOverlapLeavesMarkers(t *testing.T) {
	dir := initMergeTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "file.txt"),
		[]byte("line A\nline B\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "commit", "-am", "base")
	gitRun(t, dir, "branch", "snag/z", "master")
	gitRun(t, dir, "worktree", "add", "-q", filepath.Join(dir, "wt"), "snag/z")
	if err := os.WriteFile(filepath.Join(dir, "wt", "file.txt"),
		[]byte("line A AGENT\nline B\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, filepath.Join(dir, "wt"), "commit", "-am", "snag: edit A")
	// Live WIP edits the SAME line A.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"),
		[]byte("line A WIP\nline B\n"), 0644); err != nil {
		t.Fatal(err)
	}

	base, _ := mergeBaseRev(dir, "master", "snag/z")
	conflicts, err := mergeLive(dir, base, "snag/z", []string{"file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0] != "file.txt" {
		t.Fatalf("expected conflict in file.txt, got %v", conflicts)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "file.txt"))
	if !strings.Contains(string(got), "<<<<<<<") || !strings.Contains(string(got), ">>>>>>>") {
		t.Errorf("expected conflict markers, got %q", got)
	}
}
```

Add `"strings"` to the `apply_test.go` import block.

- [ ] **Step 4: Run the tests**

Run: `cd /Users/alex/snags && go test ./... -run 'TestCommitTouchedPaths|TestMergeLive' -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
cd /Users/alex/snags
git add apply.go apply_test.go
git commit -m "snag: add commit-via-temp-index and per-file 3-way merge

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: Wire `applyMarkerMergeStage` into `mergeStage`

**Files:**
- Modify: `/Users/alex/snags/apply.go` (add `applyMarkerMergeStage`)
- Modify: `/Users/alex/snags/worker.go:29-37` (add `conflict` field) and `worker.go:527-574` (`mergeStage`)
- Test: `/Users/alex/snags/worker_test.go`

- [ ] **Step 1: Add the `conflict` field to `snagDoneMsg`**

In `worker.go`, the struct at lines 29-37 becomes:

```go
type snagDoneMsg struct {
	snagID     string
	success    bool
	notes      string
	commitHash string
	// mergeFailed marks a merge-stage failure: the agent's work is intact on
	// branch snag/<id>, only merging it back failed.
	mergeFailed bool
	// conflict marks a successful merge whose live-tree update left conflict
	// markers (or held-back files). The snag commit landed; branch snag/<id>
	// is preserved for manual resolution.
	conflict bool
}
```

- [ ] **Step 2: Add `applyMarkerMergeStage` to `apply.go`**

This is the marker-snag replacement for the `squashMerge` flow. Mirrors `mergeStage`'s return shapes (`removeWorktree` deletes the branch on full success; `removeWorktreeOnly` keeps it).

```go
// applyMarkerMergeStage lands a completed marker snag without git merge --squash:
// it commits the branch's touched paths onto baseBranch via a temp index, then
// 3-way merges each touched path into the live working tree. The marker comment
// is deleted from the live tree first so its file matches base (the agent's code
// change then applies as a clean overwrite rather than a phantom conflict).
func applyMarkerMergeStage(projectRoot, baseBranch string, snag Snag, notes string, cfg Config) snagDoneMsg {
	if err := requireBaseBranch(projectRoot, baseBranch, "merging"); err != nil {
		removeWorktreeOnly(projectRoot, snag.ID)
		return snagDoneMsg{snagID: snag.ID, success: false, mergeFailed: true,
			notes: fmt.Sprintf("%s — branch snag/%s preserved", err, snag.ID)}
	}
	branch := "snag/" + snag.ID
	base, err := mergeBaseRev(projectRoot, baseBranch, branch)
	if err != nil {
		removeWorktreeOnly(projectRoot, snag.ID)
		return snagDoneMsg{snagID: snag.ID, success: false, mergeFailed: true,
			notes: fmt.Sprintf("merge-base: %s — branch snag/%s preserved", err, snag.ID)}
	}
	touched, err := changedPaths(projectRoot, base, branch)
	if err != nil {
		removeWorktreeOnly(projectRoot, snag.ID)
		return snagDoneMsg{snagID: snag.ID, success: false, mergeFailed: true,
			notes: fmt.Sprintf("diff: %s — branch snag/%s preserved", err, snag.ID)}
	}
	if len(touched) == 0 {
		removeWorktree(projectRoot, snag.ID)
		noteText := "no code changes"
		if notes != "" {
			noteText = "no code changes — " + notes
		}
		return snagDoneMsg{snagID: snag.ID, success: true, notes: noteText}
	}

	// Delete the marker from the live tree first so the marker file matches
	// base and the agent's edit merges cleanly.
	if err := DeleteMarker(projectRoot, snag.File, snag.Description, cfg.Marker); err != nil {
		removeWorktreeOnly(projectRoot, snag.ID)
		return snagDoneMsg{snagID: snag.ID, success: false, mergeFailed: true,
			notes: fmt.Sprintf("marker removal failed: %s — branch snag/%s preserved", err, snag.ID)}
	}

	commitHash, err := commitTouchedPaths(projectRoot, touched, branch, baseBranch, "snag: "+snag.Description, notes)
	if err != nil {
		removeWorktreeOnly(projectRoot, snag.ID)
		return snagDoneMsg{snagID: snag.ID, success: false, mergeFailed: true,
			notes: fmt.Sprintf("commit: %s — branch snag/%s preserved", err, snag.ID)}
	}

	// The commit has landed; from here the snag is complete regardless of how
	// the live-tree update fares.
	conflicts, mergeErr := mergeLive(projectRoot, base, branch, touched)
	if mergeErr != nil {
		removeWorktreeOnly(projectRoot, snag.ID) // keep branch for recovery
		return snagDoneMsg{snagID: snag.ID, success: true, conflict: true, commitHash: commitHash,
			notes: fmt.Sprintf("merged as %s but live update incomplete: %s — resolve manually; branch %s preserved",
				shortHash(commitHash), mergeErr, branch)}
	}
	if len(conflicts) > 0 {
		removeWorktreeOnly(projectRoot, snag.ID) // keep branch for recovery
		return snagDoneMsg{snagID: snag.ID, success: true, conflict: true, commitHash: commitHash,
			notes: fmt.Sprintf("merged as %s with conflict markers in %s — resolve them; branch %s preserved",
				shortHash(commitHash), strings.Join(conflicts, ", "), branch)}
	}

	removeWorktree(projectRoot, snag.ID) // full success: delete branch
	return snagDoneMsg{snagID: snag.ID, success: true, commitHash: commitHash, notes: notes}
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}
```

- [ ] **Step 3: Route marker snags through it in `mergeStage`**

In `worker.go`, `mergeStage` (lines 527-574). Replace the body after the `defer` so marker snags use the new path and typed snags keep `squashMerge`. The marker-delete block (old lines 534-540) is removed — `applyMarkerMergeStage` owns marker deletion now.

```go
func mergeStage(projectRoot, baseBranch string, snag Snag, notes string, cfg Config, tl *transcriptLogger) (msg snagDoneMsg) {
	defer func() {
		if msg.mergeFailed {
			tl.result(false, msg.notes)
		}
	}()

	if snag.Source == SourceMarker {
		return applyMarkerMergeStage(projectRoot, baseBranch, snag, notes, cfg)
	}

	mergeErr := squashMerge(projectRoot, snag.ID, snag.Description, notes, baseBranch)
	switch {
	case mergeErr == nil:
		// merged + committed cleanly
	case errors.Is(mergeErr, errNothingToMerge):
		removeWorktree(projectRoot, snag.ID)
		noteText := "no code changes"
		if notes != "" {
			noteText = "no code changes — " + notes
		}
		return snagDoneMsg{snagID: snag.ID, success: true, notes: noteText}
	case errors.Is(mergeErr, errMergeConflict):
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
```

- [ ] **Step 4: Update/replace the marker merge tests in `worker_test.go`**

`TestMergeStageMarkerOnlyDirtyFileMerges` (worker_test.go:595) still applies and must still pass (own-line marker, clean overwrite). Add new tests proving the dirty-tree win and the conflict path. These use the existing `initMergeTestRepo` / `startSnagBranch` / `branchExists` / `workingTreeClean` helpers.

```go
// A marker snag merges even when the live tree is dirty with an UNRELATED
// uncommitted change in the same file (the case git merge --squash aborts on).
func TestMergeStageMarkerMergesOverDirtyTree(t *testing.T) {
	dir := initMergeTestRepo(t) // file.txt = "original\n"
	if err := os.WriteFile(filepath.Join(dir, "file.txt"),
		[]byte("top\nmiddle\nbottom\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "commit", "-am", "multiline")

	snag := Snag{ID: "mk1", Description: "tweak bottom", Source: SourceMarker, File: "file.txt", Line: 3}
	// Agent edits the bottom line on the snag branch.
	startSnagBranch(t, dir, snag.ID, "top\nmiddle\nbottom EDITED\n")

	// Live tree: unrelated WIP on the top line + a marker on its own line.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"),
		[]byte("top WIP\nmiddle\n// snag: tweak bottom\nbottom\n"), 0644); err != nil {
		t.Fatal(err)
	}

	msg := mergeStage(dir, "master", snag, "notes", DefaultConfig(), nil)
	if !msg.success || msg.mergeFailed || msg.conflict {
		t.Fatalf("expected clean success, got success=%v mergeFailed=%v conflict=%v notes=%q",
			msg.success, msg.mergeFailed, msg.conflict, msg.notes)
	}
	// Snag commit landed on master.
	if msg.commitHash == "" || msg.commitHash != headCommitHash(dir) {
		t.Errorf("commitHash %q != HEAD %q", msg.commitHash, headCommitHash(dir))
	}
	// Live tree: WIP preserved, marker gone, agent's edit applied.
	got, _ := os.ReadFile(filepath.Join(dir, "file.txt"))
	want := "top WIP\nmiddle\nbottom EDITED\n"
	if string(got) != want {
		t.Errorf("live tree = %q, want %q", got, want)
	}
	if branchExists(dir, "snag/mk1") {
		t.Error("branch should be deleted on full success")
	}
}

// A true same-line overlap lands the commit, leaves markers, preserves branch.
func TestMergeStageMarkerConflictLeavesMarkersAndKeepsBranch(t *testing.T) {
	dir := initMergeTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("shared\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "commit", "-am", "base")

	snag := Snag{ID: "mk2", Description: "change shared", Source: SourceMarker, File: "file.txt", Line: 1}
	startSnagBranch(t, dir, snag.ID, "shared AGENT\n")
	// Live WIP edits the SAME line (no separate marker line; the marker was the
	// shared line itself and is already consumed by the agent's edit).
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("shared WIP\n"), 0644); err != nil {
		t.Fatal(err)
	}

	msg := mergeStage(dir, "master", snag, "notes", DefaultConfig(), nil)
	if !msg.success || !msg.conflict || msg.mergeFailed {
		t.Fatalf("expected success+conflict, got success=%v conflict=%v mergeFailed=%v notes=%q",
			msg.success, msg.conflict, msg.mergeFailed, msg.notes)
	}
	if msg.commitHash == "" {
		t.Error("expected snag commit to have landed")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "file.txt"))
	if !strings.Contains(string(got), "<<<<<<<") {
		t.Errorf("expected conflict markers in live tree, got %q", got)
	}
	if !branchExists(dir, "snag/mk2") {
		t.Error("branch should be preserved on conflict")
	}
}
```

Ensure `worker_test.go` imports `os`, `path/filepath`, `strings` (it already uses `os`/`exec`/`strings`; add `path/filepath` if not present — it is, per existing tests).

- [ ] **Step 5: Run the tests**

Run: `cd /Users/alex/snags && go test ./... -run 'TestMergeStage' -v`
Expected: PASS, including the pre-existing `TestMergeStageMarkerOnlyDirtyFileMerges`, `TestMergeStageConflictPreservesBranch` (typed path), `TestMergeStageSuccess`, `TestMergeStageNothingToMerge`, and the two new tests.

- [ ] **Step 6: Commit**

```bash
cd /Users/alex/snags
git add apply.go worker.go worker_test.go
git commit -m "snag: route marker snags through 3-way apply instead of squash merge

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: Preserve branch on conflict-success in the model

**Files:**
- Modify: `/Users/alex/snags/model.go:209-244` (the `snagDoneMsg` success/branch handling)
- Test: `/Users/alex/snags/model_test.go`

- [ ] **Step 1: Preserve the branch when a successful merge left conflicts**

In `model.go`, the `if msg.success` block (lines 211-219) currently always clears `Branch`. Change it to keep the branch when `msg.conflict`:

```go
				if msg.success {
					m.state.Snags[i].Status = StatusComplete
					if msg.conflict {
						// Commit landed but the live-tree update left conflict
						// markers; keep the branch for inspection/recovery.
						m.state.Snags[i].Branch = "snag/" + msg.snagID
					} else {
						// The branch is deleted on a clean successful merge.
						m.state.Snags[i].Branch = ""
					}
					m.state.Snags[i].CommitHash = msg.commitHash
					m.sessionCompletedIDs[msg.snagID] = true
					if debugLog != nil {
						debugLog.Printf("state change snag=%s inflight → complete hash=%s conflict=%v", msg.snagID, msg.commitHash, msg.conflict)
					}
				} else {
```

The existing `else`/`mergeFailed` branch (lines 220-236) is unchanged: a genuine merge-stage failure (`mergeFailed`) still preserves the branch and still auto-triggers agentic for markers. A conflict-success does **not** set `mergeFailed`, so `failedMarker` stays nil and agentic does not auto-run.

- [ ] **Step 2: Add a model test**

```go
func TestSnagDoneMsgConflictKeepsBranchNoAutoMerge(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusInflight, Description: "the task", Source: SourceMarker, File: "main.go"}}
	m := newTestModel(snags)
	m.working = true
	m = update(m, snagDoneMsg{snagID: "abc", success: true, conflict: true, commitHash: "deadbeef",
		notes: "merged as deadbee with conflict markers in main.go"})

	if m.state.Snags[0].Status != StatusComplete {
		t.Errorf("expected complete, got %q", m.state.Snags[0].Status)
	}
	if m.state.Snags[0].Branch != "snag/abc" {
		t.Errorf("expected branch preserved on conflict, got %q", m.state.Snags[0].Branch)
	}
	if m.state.Snags[0].CommitHash != "deadbeef" {
		t.Errorf("expected commit recorded, got %q", m.state.Snags[0].CommitHash)
	}
	if m.mergingID != "" {
		t.Errorf("conflict-success must not auto-start agentic, mergingID=%q", m.mergingID)
	}
}
```

- [ ] **Step 3: Run the model tests**

Run: `cd /Users/alex/snags && go test ./... -run 'TestSnagDoneMsg' -v`
Expected: PASS, including the existing `TestSnagDoneMsgMergeFailedMarkerAutoMerges` and `...TypedDoesNotAutoMerge`.

- [ ] **Step 4: Commit**

```bash
cd /Users/alex/snags
git add model.go model_test.go
git commit -m "snag: preserve branch on conflict-success, skip auto-agentic

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: Update docs and run the full suite

**Files:**
- Modify: `/Users/alex/snags/CLAUDE.md`
- Modify: `/Users/alex/snags/README.md`

- [ ] **Step 1: Update `CLAUDE.md`**

In the `worker.go` description and the App Functionality bullets, replace the marker-merge description. The `worker.go` paragraph currently says marker snags squash-merge and run agentic on conflict. Update to:

> On success, marker snags land via a per-file 3-way apply (`applyMarkerMergeStage` in `apply.go`): commit the branch's touched paths onto the base branch through a throwaway index (never touching the live tree), then `git merge-file` each touched path into the live working tree. The marker is deleted from the live tree first. Non-overlapping user edits and other pending markers merge silently; a true same-line overlap lands the commit but leaves `<<<<<<<` markers in the file and preserves `snag/<id>` for manual resolution. Typed snags still squash-merge (`squashMerge`) and run the agentic merge on conflict.

Add a one-line entry for the new file in the file list:

> **`apply.go`** — marker-snag landing: commit touched paths via a temp index, then per-file 3-way merge into the live working tree (ported from the `inker` project's applier).

- [ ] **Step 2: Update `README.md`**

Adjust the merge step (around the existing "On success, the worktree is squash-merged back" / "Marker snags retry the merge agentically" lines):

> 3. On success, typed snags squash-merge to your default branch. Marker snags land with a 3-way apply into your current working tree, so unrelated in-progress edits and other pending markers are preserved.
> 5. Typed-snag merge conflicts preserve `snag/<id>`; press `m` to resolve them agentically. Marker-snag overlaps land the commit and leave `<<<<<<<` markers in the file for you to resolve.

- [ ] **Step 3: Run the full test suite and build**

Run: `cd /Users/alex/snags && go build ./... && go test ./...`
Expected: build succeeds; all tests PASS. (Note: `TestClaudeArgsDefault` was flagged as a pre-existing unrelated failure in commit 898ee5a's message — if it still fails, confirm it is unrelated to this change and leave it.)

- [ ] **Step 4: Commit**

```bash
cd /Users/alex/snags
git add CLAUDE.md README.md
git commit -m "snag: document marker 3-way apply merge

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-review notes

- **Spec coverage:** commit-first via temp index (Task 2), per-file 3-way merge (Task 2), marker-delete-first ordering (Task 3), conflict policy = land commit + markers + preserve branch + no auto-agentic (Tasks 3-4), typed snags unchanged (Task 3), docs (Task 5). Covered.
- **Type consistency:** `snagDoneMsg.conflict` added in Task 3 and consumed in Task 4. `applyMarkerMergeStage`, `commitTouchedPaths`, `mergeLive`, `mergeBaseRev`, `changedPaths`, `blobAt`, `entryAt` names consistent across tasks. `shortHash` defined in Task 3.
- **Pre-existing test:** `TestMergeStageMarkerOnlyDirtyFileMerges` must keep passing — the own-line-marker clean-overwrite case is handled by `mergeOnePath`'s `cur == base` branch after `DeleteMarker`.
- **Non-goal:** making `m`/agentic resolve already-committed conflict markers. The branch is preserved for inspection only.
