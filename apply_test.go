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
