package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const maxVisible = 10

type focusArea int

const (
	focusList  focusArea = iota
	focusInput
)

type startWorkMsg struct{}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	faintStyle    = lipgloss.NewStyle().Faint(true)
	redStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	boldStyle     = lipgloss.NewStyle().Bold(true)
	inflightStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

type Model struct {
	state           State
	cursor          int
	viewOffset      int
	focus           focusArea
	input           textinput.Model
	spinner         spinner.Model
	paused          bool
	working         bool
	projectRoot     string
	defaultBranch   string
	width           int
	cancelWork      context.CancelFunc
	streamCh        chan tea.Msg
	currentActivity string
	inflightStart   time.Time
}

func waitForSnagEvent(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

func New(projectRoot, defaultBranch string, state State, startPaused bool) Model {
	ti := textinput.New()
	ti.Placeholder = "describe a snag..."
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot

	return Model{
		state:         state,
		focus:         focusInput,
		input:         ti,
		spinner:       sp,
		paused:        startPaused,
		projectRoot:   projectRoot,
		defaultBranch: defaultBranch,
		width:         80,
	}
}

func (m Model) visibleSnags() []Snag {
	var out []Snag
	for _, s := range m.state.Snags {
		if s.Status != StatusComplete {
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
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)

	case startWorkMsg:
		var cmd tea.Cmd
		m, cmd = m.startNextSnag()
		cmds = append(cmds, saveCmd(m.projectRoot, m.state), cmd)

	case snagProgressMsg:
		m.currentActivity = msg.activity
		if m.streamCh != nil {
			cmds = append(cmds, waitForSnagEvent(m.streamCh))
		}

	case snagDoneMsg:
		m.working = false
		m.currentActivity = ""
		m.streamCh = nil
		m.inflightStart = time.Time{}
		for i := range m.state.Snags {
			if m.state.Snags[i].ID == msg.snagID {
				if msg.success {
					m.state.Snags[i].Status = StatusComplete
					m.state.Snags[i].Branch = "snag/" + msg.snagID
					if debugLog != nil {
						debugLog.Printf("state change snag=%s inflight → complete", msg.snagID)
					}
				} else {
					m.state.Snags[i].Status = StatusFailed
					if debugLog != nil {
						debugLog.Printf("state change snag=%s inflight → failed notes=%q", msg.snagID, msg.notes)
					}
				}
				m.state.Snags[i].Notes = msg.notes
				break
			}
		}
		var workCmd tea.Cmd
		m, workCmd = m.startNextSnag()
		cmds = append(cmds, saveCmd(m.projectRoot, m.state), workCmd)

	case tea.KeyMsg:
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
					m.input.Focus()
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
				if m.cursor < len(visible) && visible[m.cursor].Status != StatusInflight {
					id := visible[m.cursor].ID
					if debugLog != nil {
						debugLog.Printf("state change snag=%s deleted status=%s", id, visible[m.cursor].Status)
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
						m.input.Focus()
						m.cursor = 0
					} else if m.cursor >= len(visible2) {
						m.cursor = len(visible2) - 1
					}
					m.clampView()
					cmds = append(cmds, saveCmd(m.projectRoot, m.state))
				}
			} else {
				forwardToInput = true
			}

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
					m.focus = focusInput
					m.input.Focus()
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
				m.input.SetValue("")
				cmds = append(cmds, saveCmd(m.projectRoot, m.state))
				if !m.working && !m.paused {
					cmds = append(cmds, func() tea.Msg { return startWorkMsg{} })
				}
			}

		case key.Matches(msg, keys.Escape):
			if m.focus == focusList {
				m.focus = focusInput
				m.input.Focus()
			} else if m.focus == focusInput {
				if m.input.Value() != "" {
					m.input.SetValue("")
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
			cmds = append(cmds, inputCmd)
		}
	}

	cmds = append(cmds, tea.SetWindowTitle(m.windowTitle()))
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
			ctx, cancel := context.WithCancel(context.Background())
			m.cancelWork = cancel
			ch := RunSnag(ctx, m.projectRoot, m.defaultBranch, m.state.Snags[i])
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

func (m *Model) clampView() {
	if m.cursor < m.viewOffset {
		m.viewOffset = m.cursor
	}
	if m.cursor >= m.viewOffset+maxVisible {
		m.viewOffset = m.cursor - maxVisible + 1
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
	sb.WriteString(m.input.View() + "\n")
	sb.WriteString(strings.Repeat("─", m.width) + "\n")
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
	case selected:
		line = boldStyle.Render(line)
	}

	if s.Status == StatusInflight && !m.inflightStart.IsZero() {
		elapsed := time.Since(m.inflightStart).Round(time.Second).String()
		line += faintStyle.Render("  " + elapsed)
	}

	return line
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
	if m.focus == focusList {
		visible := m.visibleSnags()
		if m.cursor < len(visible) {
			s := visible[m.cursor]
			if s.Status == StatusFailed {
				if s.Notes != "" {
					return "✗ " + s.Notes + "  r retry"
				}
				return "r retry"
			}
			if s.Status == StatusPending {
				return "e edit  backspace delete  ↑↓ navigate  alt+↑↓ reorder"
			}
			if s.Status == StatusInflight && m.working {
				elapsed := time.Since(m.inflightStart).Round(time.Second).String()
				if m.currentActivity != "" {
					return "agent: " + m.currentActivity + "  " + elapsed
				}
				return elapsed
			}
		}
	}
	return "↑↓ navigate  backspace delete  ctrl+p pause/resume  esc clear/quit"
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
