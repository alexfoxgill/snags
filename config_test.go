package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	snagDir := filepath.Join(dir, ".snags")
	if err := os.MkdirAll(snagDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snagDir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigAbsentFile(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	def := DefaultConfig()
	if cfg.Marker != def.Marker {
		t.Errorf("marker: got %q want %q", cfg.Marker, def.Marker)
	}
	if cfg.Agents.Snag.Model != def.Agents.Snag.Model {
		t.Errorf("snag model: got %q want %q", cfg.Agents.Snag.Model, def.Agents.Snag.Model)
	}
	if cfg.Agents.Snag.Effort != def.Agents.Snag.Effort {
		t.Errorf("snag effort: got %q want %q", cfg.Agents.Snag.Effort, def.Agents.Snag.Effort)
	}
	if cfg.Agents.Snag.Timeout != def.Agents.Snag.Timeout {
		t.Errorf("snag timeout: got %v want %v", cfg.Agents.Snag.Timeout, def.Agents.Snag.Timeout)
	}
}

func TestLoadConfigPartialOverrideKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "agents:\n  snag:\n    model: opus\n")

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agents.Snag.Model != "opus" {
		t.Errorf("snag model: got %q want opus", cfg.Agents.Snag.Model)
	}
	// defaults preserved
	if cfg.Agents.Snag.Effort != "low" {
		t.Errorf("snag effort should default to low, got %q", cfg.Agents.Snag.Effort)
	}
	if cfg.Agents.Snag.Timeout != Duration(15*time.Minute) {
		t.Errorf("snag timeout should default to 15m, got %v", cfg.Agents.Snag.Timeout)
	}
	if cfg.Agents.Summary.Model != "haiku" {
		t.Errorf("summary model should default to haiku, got %q", cfg.Agents.Summary.Model)
	}
	if cfg.Marker != "snag" {
		t.Errorf("marker should default to snag, got %q", cfg.Marker)
	}
}

func TestLoadConfigFullFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `marker: fixme
agents:
  snag:
    model: opus
    effort: high
    timeout: 30m
    extra_args: ["--verbose"]
  summary:
    model: sonnet
    effort: low
    timeout: 1m
    extra_args: []
  merge:
    model: fable
    effort: high
    timeout: 5m
    extra_args: ["--flag", "val"]
`)

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Marker != "fixme" {
		t.Errorf("marker: got %q want fixme", cfg.Marker)
	}
	if cfg.Agents.Snag.Model != "opus" {
		t.Errorf("snag model: got %q", cfg.Agents.Snag.Model)
	}
	if cfg.Agents.Snag.Effort != "high" {
		t.Errorf("snag effort: got %q", cfg.Agents.Snag.Effort)
	}
	if cfg.Agents.Snag.Timeout != Duration(30*time.Minute) {
		t.Errorf("snag timeout: got %v", cfg.Agents.Snag.Timeout)
	}
	if len(cfg.Agents.Snag.ExtraArgs) != 1 || cfg.Agents.Snag.ExtraArgs[0] != "--verbose" {
		t.Errorf("snag extra_args: got %v", cfg.Agents.Snag.ExtraArgs)
	}
	if cfg.Agents.Merge.Model != "fable" {
		t.Errorf("merge model: got %q", cfg.Agents.Merge.Model)
	}
	if len(cfg.Agents.Merge.ExtraArgs) != 2 {
		t.Errorf("merge extra_args: got %v", cfg.Agents.Merge.ExtraArgs)
	}
}

func TestLoadConfigMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "marker: [unclosed\n")
	_, err := LoadConfig(dir)
	if err == nil {
		t.Error("expected error for malformed yaml")
	}
}

func TestLoadConfigInvalidDuration(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "agents:\n  snag:\n    timeout: notaduration\n")
	_, err := LoadConfig(dir)
	if err == nil {
		t.Error("expected error for invalid duration")
	}
}

func TestLoadConfigUnknownField(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "unknown_key: value\n")
	_, err := LoadConfig(dir)
	if err == nil {
		t.Error("expected error for unknown field")
	}
}

func TestLoadConfigInvalidEffort(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "agents:\n  snag:\n    effort: extreme\n")
	_, err := LoadConfig(dir)
	if err == nil {
		t.Error("expected error for invalid effort value")
	}
}

func TestLoadConfigNegativeTimeout(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "agents:\n  snag:\n    timeout: -1m\n")
	_, err := LoadConfig(dir)
	if err == nil {
		t.Error("expected error for negative timeout")
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
