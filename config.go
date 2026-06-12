package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration for YAML parsing of Go duration strings.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	s := value.Value
	if s == "" || s == "0" {
		*d = 0
		return nil
	}
	td, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(td)
	return nil
}

type AgentConfig struct {
	Model     string   `yaml:"model"`
	Effort    string   `yaml:"effort"`
	Timeout   Duration `yaml:"timeout"`
	ExtraArgs []string `yaml:"extra_args"`
}

type Config struct {
	Marker string `yaml:"marker"`
	Agents struct {
		Snag    AgentConfig `yaml:"snag"`
		Summary AgentConfig `yaml:"summary"`
		Merge   AgentConfig `yaml:"merge"`
	} `yaml:"agents"`
}

func DefaultConfig() Config {
	var c Config
	c.Marker = "snag"
	c.Agents.Snag = AgentConfig{
		Model:     "fable",
		Effort:    "low",
		Timeout:   Duration(15 * time.Minute),
		ExtraArgs: []string{},
	}
	c.Agents.Summary = AgentConfig{
		Model:     "haiku",
		Effort:    "medium",
		Timeout:   Duration(2 * time.Minute),
		ExtraArgs: []string{},
	}
	c.Agents.Merge = AgentConfig{
		Model:     "sonnet",
		Effort:    "medium",
		Timeout:   Duration(2 * time.Minute),
		ExtraArgs: []string{},
	}
	return c
}

func LoadConfig(projectRoot string) (Config, error) {
	path := filepath.Join(projectRoot, ".snags", "config.yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return Config{}, err
	}

	cfg := DefaultConfig()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return cfg, nil
		}
		return Config{}, err
	}

	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateConfig(cfg Config) error {
	if cfg.Marker == "" {
		return fmt.Errorf("marker keyword must not be empty")
	}
	agents := []struct {
		name string
		ac   AgentConfig
	}{
		{"agents.snag", cfg.Agents.Snag},
		{"agents.summary", cfg.Agents.Summary},
		{"agents.merge", cfg.Agents.Merge},
	}
	// Empty effort is allowed: it means the --effort flag is omitted and the CLI default applies.
	valid := map[string]bool{"": true, "low": true, "medium": true, "high": true}
	for _, a := range agents {
		if !valid[a.ac.Effort] {
			return fmt.Errorf("invalid effort %q for %s: must be one of low, medium, high", a.ac.Effort, a.name)
		}
		if time.Duration(a.ac.Timeout) < 0 {
			return fmt.Errorf("negative timeout for %s", a.name)
		}
	}
	return nil
}
