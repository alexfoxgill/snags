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
