package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// handleDetailsKey processes a key press while the details page is open.
func (m Model) handleDetailsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch {
	case key.Matches(msg, keys.Quit):
		return m.quit()
	case key.Matches(msg, keys.Escape), key.Matches(msg, keys.Enter):
		m.mode = viewList
		m.detailsID = ""
		m.detailsScroll = 0
		m.detailsEvents = nil
	case key.Matches(msg, keys.PauseResume):
		m.paused = !m.paused
		if !m.paused && !m.working {
			cmds = append(cmds, func() tea.Msg { return startWorkMsg{} })
		}
	case key.Matches(msg, keys.Up):
		if m.detailsScroll > 0 {
			m.detailsScroll--
		}
	case key.Matches(msg, keys.Down):
		if m.detailsScroll < m.detailsMaxScroll() {
			m.detailsScroll++
		}
	case key.Matches(msg, keys.PgUp):
		m.detailsScroll -= m.detailsPageSize()
		if m.detailsScroll < 0 {
			m.detailsScroll = 0
		}
	case key.Matches(msg, keys.PgDn):
		m.detailsScroll += m.detailsPageSize()
		if limit := m.detailsMaxScroll(); m.detailsScroll > limit {
			m.detailsScroll = limit
		}
	}
	return m, tea.Batch(cmds...)
}

func (m Model) detailsSnag() (Snag, bool) {
	for _, s := range m.state.Snags {
		if s.ID == m.detailsID {
			return s, true
		}
	}
	return Snag{}, false
}

// wrapLines wraps s to width and returns the individual lines, unstyled.
func wrapLines(s string, width int) []string {
	if width < 1 {
		width = 80
	}
	return strings.Split(lipgloss.NewStyle().Width(width).Render(s), "\n")
}

// styledWrap wraps s to width and styles each resulting line.
func styledWrap(style lipgloss.Style, s string, width int) []string {
	lines := wrapLines(s, width)
	for i := range lines {
		lines[i] = style.Render(lines[i])
	}
	return lines
}

// agentConfigLine formats an AgentConfig for the details metadata.
func agentConfigLine(ac AgentConfig) string {
	parts := []string{ac.Model}
	if ac.Effort != "" {
		parts = append(parts, "effort "+ac.Effort)
	}
	if ac.Timeout != 0 {
		parts = append(parts, "timeout "+time.Duration(ac.Timeout).String())
	}
	if len(ac.ExtraArgs) > 0 {
		parts = append(parts, "args "+strings.Join(ac.ExtraArgs, " "))
	}
	return strings.Join(parts, " · ")
}

func (m Model) detailsHeaderLines(s Snag) []string {
	var lines []string
	title := displayTitle(s)
	lines = append(lines, styledWrap(titleStyle, title, m.width)...)
	if s.Description != title {
		lines = append(lines, wrapLines(s.Description, m.width)...)
	}
	meta := []string{"status " + string(s.Status)}
	if s.Status == StatusInflight && !m.inflightStart.IsZero() {
		meta = append(meta, "elapsed "+time.Since(m.inflightStart).Round(time.Second).String())
	}
	if s.Status == StatusComplete || s.Status == StatusFailed {
		if d := snagDuration(s); d != "" {
			meta = append(meta, "duration "+d)
		}
	}
	if s.Source == SourceMarker {
		meta = append(meta, fmt.Sprintf("marker %s:%d", s.File, s.Line))
	}
	meta = append(meta, "agent "+agentConfigLine(m.cfg.Agents.Snag))
	meta = append(meta, "created "+s.CreatedAt.Format("2006-01-02 15:04:05"))
	if s.CommitHash != "" {
		meta = append(meta, "commit "+s.CommitHash)
	}
	if s.Branch != "" {
		meta = append(meta, "branch "+s.Branch)
	}
	for _, l := range meta {
		lines = append(lines, faintStyle.Render(l))
	}
	if s.Notes != "" {
		lines = append(lines, wrapLines(string(s.Notes), m.width)...)
	}
	lines = append(lines, strings.Repeat("─", m.width))
	return lines
}

// transcriptLines renders transcript events as styled, width-wrapped lines.
func transcriptLines(events []transcriptEvent, width int) []string {
	if len(events) == 0 {
		return []string{faintStyle.Render("no session log")}
	}
	var lines []string
	for _, ev := range events {
		switch ev.Type {
		case "run_start":
			ts := ev.Time
			if t, err := time.Parse(time.RFC3339, ev.Time); err == nil {
				ts = t.Format("15:04:05")
			}
			lines = append(lines, styledWrap(faintStyle, fmt.Sprintf("── %s · %s ──", ev.Label, ts), width)...)
		case "text":
			lines = append(lines, wrapLines(ev.Text, width)...)
		case "tool":
			detail := "» " + ev.Name
			if ev.Detail != "" {
				detail += "(" + ev.Detail + ")"
			}
			lines = append(lines, styledWrap(faintStyle, detail, width)...)
		case "result":
			if ev.Status == "success" {
				lines = append(lines, styledWrap(greenStyle, "✓ "+ev.Notes, width)...)
			} else {
				lines = append(lines, styledWrap(redStyle, "✗ "+ev.Notes, width)...)
			}
		}
	}
	return lines
}

// detailsRegionHeight is the number of transcript lines that fit between the
// fixed header and the status bar.
func (m Model) detailsRegionHeight(headerLen int) int {
	h := m.height - headerLen - 1
	if h < 3 {
		h = 3
	}
	return h
}

func (m Model) detailsPageSize() int {
	s, ok := m.detailsSnag()
	if !ok {
		return 1
	}
	return m.detailsRegionHeight(len(m.detailsHeaderLines(s)))
}

func (m Model) detailsMaxScroll() int {
	s, ok := m.detailsSnag()
	if !ok {
		return 0
	}
	region := m.detailsRegionHeight(len(m.detailsHeaderLines(s)))
	maxScroll := len(transcriptLines(m.detailsEvents, m.width)) - region
	if maxScroll < 0 {
		maxScroll = 0
	}
	return maxScroll
}

// detailsAtBottom reports whether the view is scrolled to the transcript tail.
func (m Model) detailsAtBottom() bool {
	return m.detailsScroll >= m.detailsMaxScroll()
}

// detailsClampScroll clamps the scroll position to the current limit; with
// followTail it snaps to the tail (used after refreshes and resizes so a view
// parked at the bottom stays there).
func (m *Model) detailsClampScroll(followTail bool) {
	if limit := m.detailsMaxScroll(); followTail || m.detailsScroll > limit {
		m.detailsScroll = limit
	}
}

// refreshDetails re-reads the viewed snag's transcript. If the user was at
// the bottom, follow the tail; otherwise keep position.
func (m *Model) refreshDetails() {
	atBottom := m.detailsAtBottom()
	m.detailsEvents = readTranscript(snagLogFile(m.projectRoot, m.detailsID))
	m.detailsClampScroll(atBottom)
}

func (m Model) detailsView() string {
	s, ok := m.detailsSnag()
	if !ok {
		return faintStyle.Render("no snag selected")
	}
	header := m.detailsHeaderLines(s)
	region := m.detailsRegionHeight(len(header))
	lines := transcriptLines(m.detailsEvents, m.width)

	maxScroll := len(lines) - region
	if maxScroll < 0 {
		maxScroll = 0
	}
	scroll := min(max(m.detailsScroll, 0), maxScroll)
	end := min(scroll+region, len(lines))

	out := make([]string, 0, m.height)
	out = append(out, header...)
	out = append(out, lines[scroll:end]...)
	for len(out) < m.height-1 {
		out = append(out, "")
	}
	out = append(out, faintStyle.Render("↑↓ scroll  pgup/pgdn page  esc back"))
	return strings.Join(out, "\n")
}
