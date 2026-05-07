package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadStateMissingFile(t *testing.T) {
	dir := t.TempDir()
	state, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Snags) != 0 {
		t.Errorf("expected empty state, got %d snags", len(state.Snags))
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".snags"), 0755); err != nil {
		t.Fatal(err)
	}
	state := State{Snags: []Snag{
		{ID: "abc123", Description: "test snag", Status: StatusPending, CreatedAt: time.Now().UTC().Truncate(time.Second)},
	}}
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Snags) != 1 {
		t.Fatalf("expected 1 snag, got %d", len(loaded.Snags))
	}
	if loaded.Snags[0].Description != "test snag" {
		t.Errorf("description mismatch: %q", loaded.Snags[0].Description)
	}
	if loaded.Snags[0].Status != StatusPending {
		t.Errorf("status mismatch: %q", loaded.Snags[0].Status)
	}
}

func TestInflightResetOnLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".snags"), 0755); err != nil {
		t.Fatal(err)
	}
	state := State{Snags: []Snag{
		{ID: "abc", Description: "was running", Status: StatusInflight},
	}}
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Snags[0].Status != StatusPending {
		t.Errorf("expected inflight to be reset to pending, got %q", loaded.Snags[0].Status)
	}
}

func TestEnsureSnagDirCreatesDir(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureSnagDir(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".snags")); os.IsNotExist(err) {
		t.Error("expected .snags/ directory to exist")
	}
}

func TestEnsureSnagDirAddsGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureSnagDir(dir); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), ".snags/") {
		t.Error("expected .snags/ in .gitignore")
	}
}

func TestEnsureSnagDirIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureSnagDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSnagDir(dir); err != nil {
		t.Error("second call should not error")
	}
	content, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	count := strings.Count(string(content), ".snags/")
	if count != 1 {
		t.Errorf("expected .snags/ to appear once in .gitignore, got %d", count)
	}
}
