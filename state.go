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
	}
	return s, nil
}

func SaveState(projectRoot string, s State) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(stateFile(projectRoot), data, 0644)
}

func EnsureSnagDir(projectRoot string) error {
	if err := os.MkdirAll(snagDir(projectRoot), 0755); err != nil {
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
