package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTranscriptLoggerWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	tl := newTranscriptLogger(dir, "abc")
	if tl == nil {
		t.Fatal("expected logger, got nil")
	}
	tl.runStart("agent")
	tl.text("thinking about it")
	tl.tool("Bash", "go test ./...")
	tl.result(true, "all done")
	tl.Close()

	// A second run appends to the same log.
	tl2 := newTranscriptLogger(dir, "abc")
	tl2.runStart("merge")
	tl2.Close()

	data, err := os.ReadFile(snagLogFile(dir, "abc"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d:\n%s", len(lines), data)
	}
	var evs []map[string]string
	for i, line := range lines {
		var ev map[string]string
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d not valid JSON: %v: %s", i, err, line)
		}
		evs = append(evs, ev)
	}
	if evs[0]["type"] != "run_start" || evs[0]["label"] != "agent" {
		t.Errorf("bad run_start event: %v", evs[0])
	}
	if _, err := time.Parse(time.RFC3339, evs[0]["time"]); err != nil {
		t.Errorf("run_start time not RFC3339: %v", err)
	}
	if evs[1]["type"] != "text" || evs[1]["text"] != "thinking about it" {
		t.Errorf("bad text event: %v", evs[1])
	}
	if evs[2]["type"] != "tool" || evs[2]["name"] != "Bash" || evs[2]["detail"] != "go test ./..." {
		t.Errorf("bad tool event: %v", evs[2])
	}
	if evs[3]["type"] != "result" || evs[3]["status"] != "success" || evs[3]["notes"] != "all done" {
		t.Errorf("bad result event: %v", evs[3])
	}
	if evs[4]["type"] != "run_start" || evs[4]["label"] != "merge" {
		t.Errorf("bad appended run_start event: %v", evs[4])
	}
}

func TestTranscriptLoggerNilSafe(t *testing.T) {
	var tl *transcriptLogger
	tl.runStart("agent")
	tl.text("x")
	tl.tool("Bash", "ls")
	tl.result(false, "nope")
	tl.Close()
}

func TestReadTranscriptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	tl := newTranscriptLogger(dir, "rt")
	tl.runStart("agent")
	tl.text("thinking")
	tl.tool("Bash", "go vet")
	tl.result(true, "ok")
	tl.Close()

	evs := readTranscript(snagLogFile(dir, "rt"))
	if len(evs) != 4 {
		t.Fatalf("expected 4 events, got %d: %v", len(evs), evs)
	}
	if evs[0].Type != "run_start" || evs[0].Label != "agent" {
		t.Errorf("bad run_start: %+v", evs[0])
	}
	if evs[1].Type != "text" || evs[1].Text != "thinking" {
		t.Errorf("bad text: %+v", evs[1])
	}
	if evs[2].Type != "tool" || evs[2].Name != "Bash" || evs[2].Detail != "go vet" {
		t.Errorf("bad tool: %+v", evs[2])
	}
	if evs[3].Type != "result" || evs[3].Status != "success" || evs[3].Notes != "ok" {
		t.Errorf("bad result: %+v", evs[3])
	}
}

func TestReadTranscriptMissingFile(t *testing.T) {
	if evs := readTranscript(filepath.Join(t.TempDir(), "nope.jsonl")); evs != nil {
		t.Errorf("expected nil for missing file, got %v", evs)
	}
}

func TestReadTranscriptSkipsTruncatedTrailingLine(t *testing.T) {
	dir := t.TempDir()
	tl := newTranscriptLogger(dir, "abc")
	tl.runStart("agent")
	tl.tool("Bash", "ls")
	tl.Close()

	// Simulate a worker mid-append: a partial trailing line with no newline.
	f, err := os.OpenFile(snagLogFile(dir, "abc"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"text","text":"unfini`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	evs := readTranscript(snagLogFile(dir, "abc"))
	if len(evs) != 2 {
		t.Fatalf("expected 2 events (truncated line skipped), got %d: %v", len(evs), evs)
	}
	if evs[0].Type != "run_start" || evs[0].Label != "agent" {
		t.Errorf("bad run_start: %+v", evs[0])
	}
	if evs[1].Type != "tool" || evs[1].Name != "Bash" || evs[1].Detail != "ls" {
		t.Errorf("bad tool: %+v", evs[1])
	}
}
