package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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

func TestSaveStateLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".snags"), 0755); err != nil {
		t.Fatal(err)
	}
	state := State{Snags: []Snag{{ID: "abc", Description: "x", Status: StatusPending}}}
	for i := 0; i < 3; i++ {
		if err := SaveState(dir, state); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, ".snags"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "state.yaml" {
			t.Errorf("unexpected file left in .snags/: %s", e.Name())
		}
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

func TestEnsureSnagDirCreatesLogs(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureSnagDir(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".snags", "logs")); os.IsNotExist(err) {
		t.Error("expected .snags/logs/ directory to exist")
	}
}

func TestLoadStateBackwardCompat(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".snags"), 0755); err != nil {
		t.Fatal(err)
	}

	// Write old-format state with only the original four fields.
	oldYAML := "snags:\n- id: abc\n  description: old snag\n  status: pending\n  created_at: 2024-01-01T00:00:00Z\n"
	if err := os.WriteFile(filepath.Join(dir, ".snags", "state.yaml"), []byte(oldYAML), 0644); err != nil {
		t.Fatal(err)
	}

	state, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if len(state.Snags) != 1 {
		t.Fatalf("expected 1 snag, got %d", len(state.Snags))
	}
	s := state.Snags[0]
	// New fields should be zero values.
	if !s.StartedAt.IsZero() {
		t.Errorf("StartedAt should be zero, got %v", s.StartedAt)
	}
	if !s.CompletedAt.IsZero() {
		t.Errorf("CompletedAt should be zero, got %v", s.CompletedAt)
	}
	if s.Duration != "" {
		t.Errorf("Duration should be empty, got %q", s.Duration)
	}
	if s.Notes != "" {
		t.Errorf("Notes should be empty, got %q", s.Notes)
	}
	if s.CommitHash != "" {
		t.Errorf("CommitHash should be empty, got %q", s.CommitHash)
	}
	if s.Source != "" {
		t.Errorf("Source should be empty, got %q", s.Source)
	}

	// SaveState and verify omitempty keeps output clean (no unexpected keys for zero fields).
	if err := SaveState(dir, state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".snags", "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	// Parse back as a raw map to check no spurious keys appear.
	var raw struct {
		Snags []map[string]interface{} `yaml:"snags"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("could not parse saved yaml: %v", err)
	}
	if len(raw.Snags) != 1 {
		t.Fatalf("expected 1 snag in saved yaml, got %d", len(raw.Snags))
	}
	unexpected := []string{"started_at", "completed_at", "duration", "notes", "commit_hash", "source", "file", "context", "summary", "branch"}
	for _, key := range unexpected {
		if _, ok := raw.Snags[0][key]; ok {
			t.Errorf("saved yaml should not contain %q for zero-value snag, but it does", key)
		}
	}
}
