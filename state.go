package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type SnagStatus string

const (
	StatusPending  SnagStatus = "pending"
	StatusInflight SnagStatus = "inflight"
	StatusComplete SnagStatus = "complete"
	StatusFailed   SnagStatus = "failed"
	StatusReverted SnagStatus = "reverted"
)

const (
	SourceInput  = "input"
	SourceMarker = "marker"
)

type Snag struct {
	ID          string     `yaml:"id"`
	Description string     `yaml:"description"`
	Status      SnagStatus `yaml:"status"`
	CreatedAt   time.Time  `yaml:"created_at"`
	StartedAt   time.Time  `yaml:"started_at,omitempty"`
	CompletedAt time.Time  `yaml:"completed_at,omitempty"`
	Duration    string     `yaml:"duration,omitempty"`
	Branch      string     `yaml:"branch,omitempty"`
	Notes       string     `yaml:"notes,omitempty"`
	CommitHash  string     `yaml:"commit_hash,omitempty"`
	Source      string     `yaml:"source,omitempty"`
	File        string     `yaml:"file,omitempty"`
	Line        int        `yaml:"line,omitempty"`
	Context     string     `yaml:"context,omitempty"`
	Summary     string     `yaml:"summary,omitempty"`
}

type State struct {
	Snags []Snag `yaml:"snags"`
}

func generateID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func snagDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".snags")
}

func stateFile(projectRoot string) string {
	return filepath.Join(snagDir(projectRoot), "state.yaml")
}

func LoadState(projectRoot string) (State, error) {
	data, err := os.ReadFile(stateFile(projectRoot))
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := yaml.Unmarshal(data, &s); err != nil {
		return State{}, err
	}
	for i := range s.Snags {
		if s.Snags[i].Status == StatusInflight {
			s.Snags[i].Status = StatusPending
		}
		// Old schema set Branch on success; the branch is deleted once a snag
		// completes, so clear it rather than render a dead branch in details.
		if s.Snags[i].Status == StatusComplete || s.Snags[i].Status == StatusReverted {
			s.Snags[i].Branch = ""
		}
	}
	return s, nil
}

// SaveState writes state.yaml atomically (temp file + rename). BubbleTea runs
// save commands on goroutines, so saves can race; rename ensures a reader or
// competing saver never sees a half-written file.
func SaveState(projectRoot string, s State) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(snagDir(projectRoot), "state-*.yaml.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename has succeeded
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), stateFile(projectRoot))
}

func snagLogFile(projectRoot, snagID string) string {
	return filepath.Join(snagDir(projectRoot), "logs", snagID+".jsonl")
}

func EnsureSnagDir(projectRoot string) error {
	if err := os.MkdirAll(filepath.Join(snagDir(projectRoot), "logs"), 0755); err != nil {
		return err
	}
	gitignorePath := filepath.Join(projectRoot, ".gitignore")
	content, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(content), ".snags/") {
		return nil
	}
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	prefix := ""
	if len(content) > 0 && content[len(content)-1] != '\n' {
		prefix = "\n"
	}
	_, err = fmt.Fprintf(f, "%s.snags/\n", prefix)
	return err
}
