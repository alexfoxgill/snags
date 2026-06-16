package main

import (
	"os"
	"path/filepath"
	"strings"
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

func TestCommitTouchedPathsDoesNotTouchLiveTree(t *testing.T) {
	dir := initMergeTestRepo(t)
	gitRun(t, dir, "branch", "snag/x", "master")
	gitRun(t, dir, "worktree", "add", "-q", filepath.Join(dir, "wt"), "snag/x")
	if err := os.WriteFile(filepath.Join(dir, "wt", "file.txt"), []byte("agent\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, filepath.Join(dir, "wt"), "commit", "-am", "snag: agent change")

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("wip\n"), 0644); err != nil {
		t.Fatal(err)
	}

	base, _ := mergeBaseRev(dir, "master", "snag/x")
	touched, _ := changedPaths(dir, base, "snag/x")
	hash, err := commitTouchedPaths(dir, touched, "snag/x", "master", "snag: agent change", "did it")
	if err != nil {
		t.Fatal(err)
	}
	data, _, _ := blobAt(dir, "HEAD", "file.txt")
	if string(data) != "agent\n" {
		t.Errorf("committed file.txt = %q, want agent\\n", data)
	}
	live, _ := os.ReadFile(filepath.Join(dir, "file.txt"))
	if string(live) != "wip\n" {
		t.Errorf("live file.txt = %q, want wip\\n (commit must not touch live tree)", live)
	}
	if hash == "" {
		t.Error("expected a commit hash")
	}
}

func TestMergeLiveNonOverlapMergesClean(t *testing.T) {
	dir := initMergeTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "file.txt"),
		[]byte("line A\nline B\nline C\nline D\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "commit", "-am", "multiline base")
	gitRun(t, dir, "branch", "snag/y", "master")
	gitRun(t, dir, "worktree", "add", "-q", filepath.Join(dir, "wt"), "snag/y")
	if err := os.WriteFile(filepath.Join(dir, "wt", "file.txt"),
		[]byte("line A\nline B\nline C\nline D IMPROVED\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, filepath.Join(dir, "wt"), "commit", "-am", "snag: tweak D")
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
