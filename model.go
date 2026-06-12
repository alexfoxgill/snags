package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const maxVisible = 10

type focusArea int

const (
	focusList focusArea = iota
	focusInput
)

type startWorkMsg struct{}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	faintStyle    = lipgloss.NewStyle().Faint(true)
	redStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	greenStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	boldStyle     = lipgloss.NewStyle().Bold(true)
	inflightStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

type Model struct {
	state               State
	cfg                 Config
	cursor              int
	viewOffset          int
	focus               focusArea
	input               textarea.Model
	spinner             spinner.Model
	paused              bool
	working             bool
	projectRoot         string
	defaultBranch       string
	width               int
	cancelWork          context.CancelFunc
	streamCh            chan tea.Msg
	currentTool         string
	currentText         string
	inflightStart       time.Time
	sessionCompletedIDs map[string]bool
	lastTitle           string
	showHistory         bool
	confirmingRevert    bool
}

func waitForSnagEvent(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

func New(projectRoot, defaultBranch string, state State, cfg Config, startPaused bool) Model {
	ti := textarea.New()
	ti.Placeholder = "describe a snag..."
	ti.ShowLineNumbers = false
	ti.Prompt = ""
	ti.EndOfBufferCharacter = ' '
	ti.SetWidth(78)
	ti.SetHeight(1)
	ti.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ti.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ti.FocusedStyle.Base = lipgloss.NewStyle()
	ti.BlurredStyle.Base = lipgloss.NewStyle()
	ti.Focus() //nolint

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot

	m := Model{
		state:               state,
		cfg:                 cfg,
		focus:               focusInput,
		input:               ti,
		spinner:             sp,
		paused:              startPaused,
		projectRoot:         projectRoot,
		defaultBranch:       defaultBranch,
		width:               80,
		sessionCompletedIDs: make(map[string]bool),
	}
	m.lastTitle = m.windowTitle()
	return m
}

func (m Model) visibleSnags() []Snag {
	var out []Snag
	for _, s := range m.state.Snags {
		if s.Status == StatusReverted {
			continue
		}
		if s.Status != StatusComplete || m.sessionCompletedIDs[s.ID] || m.showHistory {
			out = append(out, s)
		}
	}
	return out
}

func (m Model) windowTitle() string {
	count := fmt.Sprintf(" (%d)", len(m.visibleSnags()))
	short := shortenPath(m.projectRoot)
	status := m.workerStatusStr()
	return "snags" + count + " " + short + " " + status
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		func() tea.Msg { return startWorkMsg{} },
		tea.SetWindowTitle(m.windowTitle()),
		tea.EnableBracketedPaste,
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.input.SetWidth(msg.Width - 2)
		m.input.SetHeight(m.computeInputHeight())

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)

	case startWorkMsg:
		var cmd tea.Cmd
		m, cmd = m.startNextSnag()
		cmds = append(cmds, saveCmd(m.projectRoot, m.state), cmd)

	case snagProgressMsg:
		switch msg.kind {
		case "tool":
			m.currentTool = msg.activity
		case "text":
			m.currentText = msg.activity
		default:
			// pipeline status updates ("starting up", "merging", etc.)
			m.currentTool = msg.activity
			m.currentText = ""
		}
		if m.streamCh != nil {
			cmds = append(cmds, waitForSnagEvent(m.streamCh))
		}

	case snagDoneMsg:
		m.working = false
		m.currentTool = ""
		m.currentText = ""
		m.streamCh = nil
		m.inflightStart = time.Time{}
		for i := range m.state.Snags {
			if m.state.Snags[i].ID == msg.snagID {
				if msg.success {
					m.state.Snags[i].Status = StatusComplete
					m.state.Snags[i].Branch = "snag/" + msg.snagID
					m.state.Snags[i].CommitHash = msg.commitHash
					m.sessionCompletedIDs[msg.snagID] = true
					if debugLog != nil {
						debugLog.Printf("state change snag=%s inflight → complete hash=%s", msg.snagID, msg.commitHash)
					}
				} else {
					m.state.Snags[i].Status = StatusFailed
					if debugLog != nil {
						debugLog.Printf("state change snag=%s inflight → failed notes=%q", msg.snagID, msg.notes)
					}
				}
				m.state.Snags[i].Notes = msg.notes
				m.state.Snags[i].CompletedAt = time.Now()
				if !m.state.Snags[i].StartedAt.IsZero() {
					m.state.Snags[i].Duration = m.state.Snags[i].CompletedAt.Sub(m.state.Snags[i].StartedAt).Round(time.Second).String()
				}
				break
			}
		}
		var workCmd tea.Cmd
		m, workCmd = m.startNextSnag()
		cmds = append(cmds, saveCmd(m.projectRoot, m.state), workCmd)

	case revertDoneMsg:
		for i := range m.state.Snags {
			if m.state.Snags[i].ID == msg.snagID {
				if msg.success {
					m.state.Snags[i].Status = StatusReverted
					if debugLog != nil {
						debugLog.Printf("state change snag=%s complete → reverted", msg.snagID)
					}
				} else {
					m.state.Snags[i].Notes = msg.errMsg
					if debugLog != nil {
						debugLog.Printf("revert failed snag=%s err=%q", msg.snagID, msg.errMsg)
					}
				}
				break
			}
		}
		visible := m.visibleSnags()
		if m.cursor >= len(visible) && len(visible) > 0 {
			m.cursor = len(visible) - 1
		}
		m.clampView()
		cmds = append(cmds, saveCmd(m.projectRoot, m.state))

	case tea.KeyMsg:
		if m.confirmingRevert {
			switch {
			case key.Matches(msg, keys.Enter):
				m.confirmingRevert = false
				visible := m.visibleSnags()
				if m.cursor < len(visible) {
					snag := visible[m.cursor]
					if snag.CommitHash != "" {
						cmds = append(cmds, revertSnag(m.projectRoot, snag.ID, snag.Description, snag.CommitHash, m.cfg))
					}
				}
			case key.Matches(msg, keys.Escape):
				m.confirmingRevert = false
			}
			return m, tea.Batch(cmds...)
		}

		forwardToInput := false

		switch {
		case key.Matches(msg, keys.Quit):
			return m.quit()

		case key.Matches(msg, keys.PauseResume):
			m.paused = !m.paused
			if !m.paused && !m.working {
				cmds = append(cmds, func() tea.Msg { return startWorkMsg{} })
			}

		case key.Matches(msg, keys.Up):
			visible := m.visibleSnags()
			if m.focus == focusInput && len(visible) > 0 {
				m.focus = focusList
				m.cursor = len(visible) - 1
				m.clampView()
				m.input.Blur()
			} else if m.focus == focusList && m.cursor > 0 {
				m.cursor--
				m.clampView()
			}

		case key.Matches(msg, keys.Down):
			if m.focus == focusList {
				visible := m.visibleSnags()
				if m.cursor < len(visible)-1 {
					m.cursor++
					m.clampView()
				} else {
					m.focus = focusInput
					cmds = append(cmds, m.input.Focus())
				}
			}

		case key.Matches(msg, keys.MoveUp):
			if m.focus == focusList && m.cursor > 0 {
				visible := m.visibleSnags()
				if m.cursor < len(visible) && visible[m.cursor].Status != StatusInflight {
					cur := visible[m.cursor]
					prev := visible[m.cursor-1]
					var curIdx, prevIdx int
					for i, s := range m.state.Snags {
						if s.ID == cur.ID {
							curIdx = i
						}
						if s.ID == prev.ID {
							prevIdx = i
						}
					}
					m.state.Snags[curIdx], m.state.Snags[prevIdx] = m.state.Snags[prevIdx], m.state.Snags[curIdx]
					m.cursor--
					m.clampView()
					cmds = append(cmds, saveCmd(m.projectRoot, m.state))
				}
			}

		case key.Matches(msg, keys.MoveDown):
			if m.focus == focusList {
				visible := m.visibleSnags()
				if m.cursor < len(visible)-1 && visible[m.cursor].Status != StatusInflight {
					cur := visible[m.cursor]
					next := visible[m.cursor+1]
					var curIdx, nextIdx int
					for i, s := range m.state.Snags {
						if s.ID == cur.ID {
							curIdx = i
						}
						if s.ID == next.ID {
							nextIdx = i
						}
					}
					m.state.Snags[curIdx], m.state.Snags[nextIdx] = m.state.Snags[nextIdx], m.state.Snags[curIdx]
					m.cursor++
					m.clampView()
					cmds = append(cmds, saveCmd(m.projectRoot, m.state))
				}
			}

		case key.Matches(msg, keys.Delete):
			if m.focus == focusList {
				visible := m.visibleSnags()
				if m.cursor < len(visible) {
					snag := visible[m.cursor]
					if snag.Status == StatusComplete {
						if snag.CommitHash != "" {
							m.confirmingRevert = true
						}
					} else if snag.Status != StatusInflight {
						id := snag.ID
						if debugLog != nil {
							debugLog.Printf("state change snag=%s deleted status=%s", id, snag.Status)
						}
						var snags []Snag
						for _, s := range m.state.Snags {
							if s.ID != id {
								snags = append(snags, s)
							}
						}
						m.state.Snags = snags
						visible2 := m.visibleSnags()
						if len(visible2) == 0 {
							m.focus = focusInput
							cmds = append(cmds, m.input.Focus())
							m.cursor = 0
						} else if m.cursor >= len(visible2) {
							m.cursor = len(visible2) - 1
						}
						m.clampView()
						cmds = append(cmds, saveCmd(m.projectRoot, m.state))
					}
				}
			} else {
				forwardToInput = true
			}

		case key.Matches(msg, keys.ToggleHistory):
			m.showHistory = !m.showHistory
			visible := m.visibleSnags()
			if m.cursor >= len(visible) && len(visible) > 0 {
				m.cursor = len(visible) - 1
			}
			m.clampView()

		case key.Matches(msg, keys.Retry):
			if m.focus == focusList {
				visible := m.visibleSnags()
				if m.cursor < len(visible) && visible[m.cursor].Status == StatusFailed {
					id := visible[m.cursor].ID
					for i := range m.state.Snags {
						if m.state.Snags[i].ID == id {
							m.state.Snags[i].Status = StatusPending
							m.state.Snags[i].Notes = ""
							if debugLog != nil {
								debugLog.Printf("state change snag=%s failed → pending (retry)", id)
							}
							break
						}
					}
					cmds = append(cmds, saveCmd(m.projectRoot, m.state))
					if !m.working && !m.paused {
						cmds = append(cmds, func() tea.Msg { return startWorkMsg{} })
					}
				}
			} else {
				forwardToInput = true
			}

		case key.Matches(msg, keys.Edit):
			if m.focus == focusList {
				visible := m.visibleSnags()
				if m.cursor < len(visible) && visible[m.cursor].Status == StatusPending {
					snag := visible[m.cursor]
					var snags []Snag
					for _, s := range m.state.Snags {
						if s.ID != snag.ID {
							snags = append(snags, s)
						}
					}
					m.state.Snags = snags
					m.input.SetValue(snag.Description)
					m.input.SetHeight(m.computeInputHeight())
					m.focus = focusInput
					cmds = append(cmds, m.input.Focus())
					visible2 := m.visibleSnags()
					if m.cursor >= len(visible2) && m.cursor > 0 {
						m.cursor = len(visible2) - 1
					}
					if len(visible2) == 0 {
						m.cursor = 0
					}
					m.clampView()
					cmds = append(cmds, saveCmd(m.projectRoot, m.state))
				}
			} else {
				forwardToInput = true
			}

		case key.Matches(msg, keys.Enter):
			if m.focus == focusInput && m.input.Value() != "" {
				snag := Snag{
					ID:          generateID(),
					Description: m.input.Value(),
					Status:      StatusPending,
					CreatedAt:   time.Now(),
				}
				m.state.Snags = append(m.state.Snags, snag)
				if debugLog != nil {
					debugLog.Printf("state change snag=%s created → pending desc=%q", snag.ID, snag.Description)
				}
				if visible := m.visibleSnags(); len(visible) > maxVisible {
					m.viewOffset = len(visible) - maxVisible
				}
				m.input.SetValue("")
				m.input.SetHeight(1)
				cmds = append(cmds, saveCmd(m.projectRoot, m.state))
				if !m.working && !m.paused {
					cmds = append(cmds, func() tea.Msg { return startWorkMsg{} })
				}
			}

		case key.Matches(msg, keys.Escape):
			if m.focus == focusList {
				m.focus = focusInput
				cmds = append(cmds, m.input.Focus())
			} else if m.focus == focusInput {
				if m.input.Value() != "" {
					m.input.SetValue("")
					m.input.SetHeight(1)
				} else {
					return m.quit()
				}
			}

		default:
			forwardToInput = true
		}

		if m.focus == focusInput && forwardToInput {
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			m.input.SetHeight(m.computeInputHeight())
			cmds = append(cmds, inputCmd)
		}
	}

	// Only emit a SetWindowTitle when the title actually changes. Emitting
	// unconditionally causes a feedback loop: bubbletea dispatches the
	// resulting setWindowTitleMsg back through Update, which produces yet
	// another SetWindowTitle, pegging the CPU and flooding the terminal with
	// OSC sequences.
	if title := m.windowTitle(); title != m.lastTitle {
		m.lastTitle = title
		cmds = append(cmds, tea.SetWindowTitle(title))
	}
	return m, tea.Batch(cmds...)
}

func (m Model) startNextSnag() (Model, tea.Cmd) {
	if m.paused || m.working {
		return m, nil
	}
	for i := range m.state.Snags {
		if m.state.Snags[i].Status == StatusPending {
			m.state.Snags[i].Status = StatusInflight
			if debugLog != nil {
				debugLog.Printf("state change snag=%s pending → inflight desc=%q", m.state.Snags[i].ID, m.state.Snags[i].Description)
			}
			m.working = true
			m.inflightStart = time.Now()
			m.state.Snags[i].StartedAt = m.inflightStart
			ctx, cancel := context.WithCancel(context.Background())
			m.cancelWork = cancel
			ch := RunSnag(ctx, m.projectRoot, m.defaultBranch, m.state.Snags[i], m.cfg)
			m.streamCh = ch
			return m, waitForSnagEvent(ch)
		}
	}
	return m, nil
}

func (m Model) quit() (tea.Model, tea.Cmd) {
	if m.cancelWork != nil {
		m.cancelWork()
	}
	for i := range m.state.Snags {
		if m.state.Snags[i].Status == StatusInflight {
			m.state.Snags[i].Status = StatusPending
			break
		}
	}
	SaveState(m.projectRoot, m.state)
	return m, tea.Quit
}

func saveCmd(projectRoot string, state State) tea.Cmd {
	snagsCopy := make([]Snag, len(state.Snags))
	copy(snagsCopy, state.Snags)
	stateCopy := State{Snags: snagsCopy}
	return func() tea.Msg {
		if err := SaveState(projectRoot, stateCopy); err != nil {
			// Can't update TUI from goroutine directly; best effort log
			_ = err
		}
		return nil
	}
}

func (m Model) computeInputHeight() int {
	w := m.width
	if w <= 0 {
		w = 80
	}
	// Subtract 4: 2 for "> " prefix, 2 for word-wrap boundary margin
	divisor := w - 4
	if divisor < 1 {
		divisor = 1
	}
	rows := 0
	for _, line := range strings.Split(m.input.Value(), "\n") {
		rows += len([]rune(line))/divisor + 1
	}
	if rows < 1 {
		rows = 1
	}
	if rows > 15 {
		rows = 15
	}
	return rows
}

func (m *Model) clampView() {
	if m.cursor < m.viewOffset {
		m.viewOffset = m.cursor
	}
	if m.cursor >= m.viewOffset+maxVisible {
		m.viewOffset = m.cursor - maxVisible + 1
	}
	// scroll up to fill empty space at the bottom (e.g. after a deletion)
	if m.viewOffset > 0 {
		visible := m.visibleSnags()
		if minOffset := len(visible) - maxVisible; minOffset < m.viewOffset {
			if minOffset < 0 {
				minOffset = 0
			}
			m.viewOffset = minOffset
		}
	}
}

func (m Model) View() string {
	var sb strings.Builder

	// Title bar
	status := m.workerStatusStr()
	count := fmt.Sprintf(" (%d)", len(m.visibleSnags()))
	title := titleStyle.Render("snags") + faintStyle.Render(count)
	short := shortenPath(m.projectRoot)
	pad := m.width - len("snags") - len(count) - 1 - len(short) - len(status) - 2
	if pad < 1 {
		pad = 1
	}
	sb.WriteString(title + " " + faintStyle.Render(short) + strings.Repeat(" ", pad) + faintStyle.Render(status) + "\n")
	sb.WriteString(strings.Repeat("─", m.width) + "\n")

	// Snag list
	visible := m.visibleSnags()
	if len(visible) == 0 {
		sb.WriteString(faintStyle.Render("  no snags") + "\n")
	} else {
		end := m.viewOffset + maxVisible
		if end > len(visible) {
			end = len(visible)
		}
		if m.viewOffset > 0 {
			sb.WriteString("  ...\n")
		}
		for i, snag := range visible[m.viewOffset:end] {
			selected := m.focus == focusList && (m.viewOffset+i) == m.cursor
			sb.WriteString(m.renderRow(snag, m.viewOffset+i+1, selected) + "\n")
		}
		if end < len(visible) {
			sb.WriteString("  ...\n")
		}
	}

	sb.WriteString(strings.Repeat("─", m.width) + "\n")
	sb.WriteString("> " + m.input.View() + "\n")
	sb.WriteString(strings.Repeat("─", m.width) + "\n")

	if notes := m.selectedNotes(); notes != "" {
		wrapped := lipgloss.NewStyle().Width(m.width).Render(notes)
		sb.WriteString(faintStyle.Render(wrapped) + "\n")
		sb.WriteString(strings.Repeat("─", m.width) + "\n")
	}

	sb.WriteString(faintStyle.Render(m.statusBarStr()) + "\n")

	return sb.String()
}

func (m Model) renderRow(s Snag, pos int, selected bool) string {
	var indicator string
	switch s.Status {
	case StatusInflight:
		indicator = m.spinner.View()
	case StatusFailed:
		indicator = "✗"
	case StatusComplete:
		indicator = "✓"
	case StatusReverted:
		indicator = "↩"
	default:
		indicator = " "
	}

	sel := " "
	if selected {
		sel = "▶"
	}

	line := fmt.Sprintf("%s %2d %s %s", sel, pos, indicator, s.Description)

	switch {
	case s.Status == StatusInflight && selected:
		line = inflightStyle.Render(boldStyle.Render(line))
	case s.Status == StatusInflight:
		line = inflightStyle.Render(line)
	case s.Status == StatusFailed && selected:
		line = redStyle.Render(boldStyle.Render(line))
	case s.Status == StatusFailed:
		line = redStyle.Render(line)
	case s.Status == StatusComplete && selected:
		line = greenStyle.Render(boldStyle.Render(line))
	case s.Status == StatusComplete:
		line = greenStyle.Render(faintStyle.Render(line))
	case s.Status == StatusReverted && selected:
		line = faintStyle.Render(boldStyle.Render(line))
	case s.Status == StatusReverted:
		line = faintStyle.Render(line)
	case selected:
		line = boldStyle.Render(line)
	}

	if s.Status == StatusInflight && !m.inflightStart.IsZero() {
		elapsed := time.Since(m.inflightStart).Round(time.Second).String()
		line += faintStyle.Render("  " + elapsed)
	}

	if (s.Status == StatusComplete || s.Status == StatusFailed) && s.Duration != "" {
		line += faintStyle.Render("  " + s.Duration)
	} else if (s.Status == StatusComplete || s.Status == StatusFailed) && !s.StartedAt.IsZero() && !s.CompletedAt.IsZero() {
		line += faintStyle.Render("  " + s.CompletedAt.Sub(s.StartedAt).Round(time.Second).String())
	}

	if s.Status == StatusInflight && m.working {
		if m.currentText != "" && m.currentTool != "" {
			line += "\n       " + faintStyle.Render(truncateInline(m.currentText, 60))
			line += "\n       " + faintStyle.Render(m.currentTool)
		} else if m.currentText != "" {
			line += "\n       " + faintStyle.Render(truncateInline(m.currentText, 60))
		} else if m.currentTool != "" {
			line += "\n       " + faintStyle.Render(m.currentTool)
		}
	}

	return line
}

func (m Model) selectedNotes() string {
	if m.focus != focusList {
		return ""
	}
	visible := m.visibleSnags()
	if m.cursor >= len(visible) {
		return ""
	}
	s := visible[m.cursor]
	if s.Status == StatusInflight && m.currentText != "" {
		return m.currentText
	}
	if (s.Status == StatusComplete || s.Status == StatusFailed) && s.Notes != "" {
		return s.Notes
	}
	return ""
}

func (m Model) workerStatusStr() string {
	switch {
	case m.paused:
		return "[paused]"
	case m.working:
		return "[running]"
	default:
		return "[idle]"
	}
}

func (m Model) statusBarStr() string {
	if m.confirmingRevert {
		visible := m.visibleSnags()
		if m.cursor < len(visible) {
			desc := truncateInline(visible[m.cursor].Description, 40)
			return fmt.Sprintf("Revert %q?  enter confirm  esc cancel", desc)
		}
	}
	if m.focus == focusList {
		visible := m.visibleSnags()
		if m.cursor < len(visible) {
			s := visible[m.cursor]
			if s.Status == StatusFailed {
				return "r retry"
			}
			if s.Status == StatusPending {
				return "e edit  backspace delete  ↑↓ navigate  alt+↑↓ reorder"
			}
			if s.Status == StatusInflight {
				return "↑↓ navigate"
			}
			if s.Status == StatusComplete {
				if s.CommitHash != "" {
					return "backspace revert  tab toggle history  ↑↓ navigate"
				}
				return "tab toggle history  ↑↓ navigate"
			}
		}
		return "tab toggle history  ↑↓ navigate"
	}
	return "↑↓ navigate  backspace delete  ctrl+p pause/resume  esc clear/quit"
}

func truncateInline(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}

func shortenPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}
