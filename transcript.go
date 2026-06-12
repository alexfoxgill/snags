package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// transcriptEvent is one line of a snag's .snags/logs/<id>.jsonl transcript.
type transcriptEvent struct {
	Type   string `json:"type"`
	Label  string `json:"label,omitempty"`
	Time   string `json:"time,omitempty"`
	Text   string `json:"text,omitempty"`
	Name   string `json:"name,omitempty"`
	Detail string `json:"detail,omitempty"`
	Status string `json:"status,omitempty"`
	Notes  string `json:"notes,omitempty"`
}

// transcriptLogger appends events to a snag's transcript log. All methods are
// nil-receiver safe and best-effort: logging failures never break a run.
type transcriptLogger struct {
	f *os.File
}

func newTranscriptLogger(projectRoot, snagID string) *transcriptLogger {
	path := snagLogFile(projectRoot, snagID)
	os.MkdirAll(filepath.Dir(path), 0755)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil
	}
	return &transcriptLogger{f: f}
}

func (t *transcriptLogger) write(ev transcriptEvent) {
	if t == nil || t.f == nil {
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	t.f.Write(append(data, '\n'))
}

func (t *transcriptLogger) runStart(label string) {
	t.write(transcriptEvent{Type: "run_start", Label: label, Time: time.Now().Format(time.RFC3339)})
}

func (t *transcriptLogger) text(text string) {
	t.write(transcriptEvent{Type: "text", Text: text})
}

func (t *transcriptLogger) tool(name, detail string) {
	t.write(transcriptEvent{Type: "tool", Name: name, Detail: detail})
}

func (t *transcriptLogger) result(success bool, notes string) {
	status := "failed"
	if success {
		status = "success"
	}
	t.write(transcriptEvent{Type: "result", Status: status, Notes: notes})
}

func (t *transcriptLogger) Close() {
	if t != nil && t.f != nil {
		t.f.Close()
	}
}

// readTranscript loads a snag transcript. Readers may run while a worker is
// mid-append, so it skips unparseable lines (e.g. a partial trailing line)
// and returns nil for a missing file.
func readTranscript(path string) []transcriptEvent {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var events []transcriptEvent
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev transcriptEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		events = append(events, ev)
	}
	return events
}
