package main

import (
	"context"
	"fmt"
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
	titleStyle = lipgloss.NewStyle().Bold(true)
	faintStyle = lipgloss.NewStyle().Faint(true)
	redStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	boldStyle  = lipgloss.NewStyle().Bold(true)
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
}

func waitForSnagEvent(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

func New(projectRoot, defaultBranch string, state State) Model {
	ti := textinput.New()
	ti.Placeholder = "describe a snag..."
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return Model{
		state:         state,
		focus:         focusInput,
		input:         ti,
		spinner:       sp,
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

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		func() tea.Msg { return startWorkMsg{} },
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
		for i := range m.state.Snags {
			if m.state.Snags[i].ID == msg.snagID {
				if msg.success {
					m.state.Snags[i].Status = StatusComplete
					m.state.Snags[i].Branch = "snag/" + msg.snagID
				} else {
					m.state.Snags[i].Status = StatusFailed
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

		case key.Matches(msg, keys.Delete):
			if m.focus == focusList {
				visible := m.visibleSnags()
				if m.cursor < len(visible) && visible[m.cursor].Status != StatusInflight {
					id := visible[m.cursor].ID
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

		case key.Matches(msg, keys.Enter):
			if m.focus == focusInput && m.input.Value() != "" {
				snag := Snag{
					ID:          generateID(),
					Description: m.input.Value(),
					Status:      StatusPending,
					CreatedAt:   time.Now(),
				}
				m.state.Snags = append(m.state.Snags, snag)
				m.input.SetValue("")
				cmds = append(cmds, saveCmd(m.projectRoot, m.state))
				if !m.working && !m.paused {
					cmds = append(cmds, func() tea.Msg { return startWorkMsg{} })
				}
			}

		case key.Matches(msg, keys.Escape):
			if m.focus == focusInput {
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

	return m, tea.Batch(cmds...)
}

func (m Model) startNextSnag() (Model, tea.Cmd) {
	if m.paused || m.working {
		return m, nil
	}
	for i := range m.state.Snags {
		if m.state.Snags[i].Status == StatusPending {
			m.state.Snags[i].Status = StatusInflight
			m.working = true
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
	title := titleStyle.Render("snags")
	pad := m.width - len("snags") - len(status) - 2
	if pad < 1 {
		pad = 1
	}
	sb.WriteString(title + strings.Repeat(" ", pad) + faintStyle.Render(status) + "\n")
	sb.WriteString(strings.Repeat("─", m.width) + "\n")

	// Snag list
	visible := m.visibleSnags()
	end := m.viewOffset + maxVisible
	if end > len(visible) {
		end = len(visible)
	}
	for i, snag := range visible[m.viewOffset:end] {
		selected := m.focus == focusList && (m.viewOffset+i) == m.cursor
		sb.WriteString(m.renderRow(snag, selected) + "\n")
	}
	if end < len(visible) {
		sb.WriteString("  ...\n")
	}

	sb.WriteString(strings.Repeat("─", m.width) + "\n")
	sb.WriteString("> " + m.input.View() + "\n")
	sb.WriteString(strings.Repeat("─", m.width) + "\n")
	sb.WriteString(faintStyle.Render(m.statusBarStr()) + "\n")

	return sb.String()
}

func (m Model) renderRow(s Snag, selected bool) string {
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

	line := fmt.Sprintf("%s %s %s", sel, indicator, s.Description)

	switch {
	case s.Status == StatusFailed && selected:
		line = redStyle.Render(boldStyle.Render(line))
	case s.Status == StatusFailed:
		line = redStyle.Render(line)
	case selected:
		line = boldStyle.Render(line)
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
		}
	}
	if m.working && m.currentActivity != "" {
		return "claude: " + m.currentActivity
	}
	return "↑↓ navigate  backspace delete  ctrl+p pause/resume  esc clear/quit"
}
