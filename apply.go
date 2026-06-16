package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
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
