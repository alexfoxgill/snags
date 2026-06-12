package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	startPaused := flag.Bool("paused", false, "start without automatically processing pending snags")
	debug := flag.Bool("debug", false, "log debug events to .snags/debug.log")
	flag.Parse()

	projectRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not get working directory: %v\n", err)
		os.Exit(1)
	}

	if _, err := os.Stat(filepath.Join(projectRoot, ".git")); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: not a git repository (no .git found in %s)\n", projectRoot)
		os.Exit(1)
	}

	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Fprintf(os.Stderr, "error: 'claude' not found in PATH — install Claude Code to use snags\n")
		os.Exit(1)
	}

	if err := EnsureSnagDir(projectRoot); err != nil {
		fmt.Fprintf(os.Stderr, "error: could not initialise .snags/: %v\n", err)
		os.Exit(1)
	}

	if *debug {
		if err := initDebugLog(projectRoot); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not open debug log: %v\n", err)
		}
	}

	state, err := LoadState(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not load state: %v\n", err)
		os.Exit(1)
	}

	cfg, err := LoadConfig(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid config: %v\n", err)
		os.Exit(1)
	}

	defaultBranch := detectDefaultBranch(projectRoot)
	m := New(projectRoot, defaultBranch, state, cfg, *startPaused)

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
