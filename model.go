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

type viewMode int

const (
	viewList viewMode = iota
	viewDetails
)

type startWorkMsg struct{}

type scanDoneMsg struct {
	markers []Marker
	err     error
}

type summaryDoneMsg struct {
	snagID  string
	summary string
	err     error
}

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
	height              int
	cancelWork          context.CancelFunc
	cancelMerge         context.CancelFunc
	cancelRevert        context.CancelFunc
	streamCh            chan tea.Msg
	currentTool         string
	currentText         string
	inflightStart       time.Time
	sessionCompletedIDs map[string]bool
	lastTitle           string
	showHistory         bool
	confirmingRevert    bool
	notice              string
	scanning            bool
	mergingID           string
	mode                viewMode
	detailsID           string
	detailsScroll       int
	detailsEvents       []transcriptEvent
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
		height:              24,
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
		// Capture tail position before the resize changes the scroll limit.
		atBottom := m.mode == viewDetails && m.detailsAtBottom()
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(msg.Width - 2)
		m.input.SetHeight(m.computeInputHeight())
		if m.mode == viewDetails {
			m.detailsClampScroll(atBottom)
		}

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
		if m.mode == viewDetails && msg.snagID == m.detailsID {
			m.refreshDetails()
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
					// The branch is deleted on a successful merge.
					m.state.Snags[i].Branch = ""
					m.state.Snags[i].CommitHash = msg.commitHash
					m.sessionCompletedIDs[msg.snagID] = true
					if debugLog != nil {
						debugLog.Printf("state change snag=%s inflight → complete hash=%s", msg.snagID, msg.commitHash)
					}
				} else {
					m.state.Snags[i].Status = StatusFailed
					if msg.mergeFailed {
						// Merge-stage failure: the work survives on this branch.
						m.state.Snags[i].Branch = "snag/" + msg.snagID
					} else {
						m.state.Snags[i].Branch = ""
					}
					if debugLog != nil {
						debugLog.Printf("state change snag=%s inflight → failed notes=%q", msg.snagID, msg.notes)
					}
				}
				m.state.Snags[i].Notes = fragileString(msg.notes)
				m.state.Snags[i].CompletedAt = time.Now()
				if !m.state.Snags[i].StartedAt.IsZero() {
					m.state.Snags[i].Duration = m.state.Snags[i].CompletedAt.Sub(m.state.Snags[i].StartedAt).Round(time.Second).String()
				}
				break
			}
		}
		if m.mode == viewDetails && msg.snagID == m.detailsID {
			m.refreshDetails()
		}
		var workCmd tea.Cmd
		m, workCmd = m.startNextSnag()
		cmds = append(cmds, saveCmd(m.projectRoot, m.state), workCmd)

	case scanDoneMsg:
		m.scanning = false
		if msg.err != nil {
			m.notice = "scan failed: " + msg.err.Error()
			break
		}
		seen := make(map[string]bool)
		added, skipped := 0, 0
		for _, mk := range msg.markers {
			batchKey := mk.File + "\x00" + mk.Text
			if seen[batchKey] || m.markerSnagExists(mk.File, mk.Text) {
				skipped++
				continue
			}
			seen[batchKey] = true
			snag := Snag{
				ID:          generateID(),
				Description: mk.Text,
				Status:      StatusPending,
				CreatedAt:   time.Now(),
				Source:      SourceMarker,
				File:        mk.File,
				Line:        mk.Line,
				Context:     fragileString(mk.Context),
			}
			m.state.Snags = append(m.state.Snags, snag)
			added++
			if debugLog != nil {
				debugLog.Printf("state change snag=%s created → pending (marker %s:%d) desc=%q", snag.ID, snag.File, snag.Line, snag.Description)
			}
			cmds = append(cmds, summaryCmd(m.projectRoot, m.cfg.Agents.Summary, snag))
		}
		if len(msg.markers) == 0 {
			m.notice = "no markers found"
		} else {
			m.notice = fmt.Sprintf("%d marker(s) queued", added)
			if skipped > 0 {
				m.notice += fmt.Sprintf(", %d duplicate(s) skipped", skipped)
			}
		}
		if added > 0 {
			// Scroll the new snags into view, but never move the list under a
			// cursor the user is navigating with.
			if m.focus == focusInput {
				if visible := m.visibleSnags(); len(visible) > maxVisible {
					m.viewOffset = len(visible) - maxVisible
				}
			}
			cmds = append(cmds, saveCmd(m.projectRoot, m.state))
			if !m.working && !m.paused {
				cmds = append(cmds, func() tea.Msg { return startWorkMsg{} })
			}
		}

	case summaryDoneMsg:
		if msg.err == nil && msg.summary != "" {
			for i := range m.state.Snags {
				if m.state.Snags[i].ID == msg.snagID {
					m.state.Snags[i].Summary = msg.summary
					break
				}
			}
			cmds = append(cmds, saveCmd(m.projectRoot, m.state))
		}

	case mergeDoneMsg:
		m.mergingID = ""
		if m.cancelMerge != nil {
			m.cancelMerge()
			m.cancelMerge = nil
		}
		for i := range m.state.Snags {
			if m.state.Snags[i].ID == msg.snagID {
				if msg.success {
					m.state.Snags[i].Status = StatusComplete
					m.state.Snags[i].CommitHash = msg.commitHash
					m.state.Snags[i].Branch = ""
					// Unconditional: failed snags carry stale failure notes
					// ("merge conflict — branch preserved") that must not
					// survive on a now-complete snag.
					m.state.Snags[i].Notes = "merged by agent"
					m.sessionCompletedIDs[msg.snagID] = true
					if debugLog != nil {
						debugLog.Printf("state change snag=%s failed → complete (agentic merge) hash=%s", msg.snagID, msg.commitHash)
					}
				} else {
					// Stays failed; the worker preserved the branch.
					m.state.Snags[i].Notes = fragileString(msg.errMsg)
					if debugLog != nil {
						debugLog.Printf("agentic merge failed snag=%s err=%q", msg.snagID, msg.errMsg)
					}
				}
				break
			}
		}
		if m.mode == viewDetails && msg.snagID == m.detailsID {
			m.refreshDetails()
		}
		cmds = append(cmds, saveCmd(m.projectRoot, m.state), func() tea.Msg { return startWorkMsg{} })

	case revertDoneMsg:
		if m.cancelRevert != nil {
			m.cancelRevert()
			m.cancelRevert = nil
		}
		for i := range m.state.Snags {
			if m.state.Snags[i].ID == msg.snagID {
				if msg.success {
					m.state.Snags[i].Status = StatusReverted
					if debugLog != nil {
						debugLog.Printf("state change snag=%s complete → reverted", msg.snagID)
					}
				} else {
					m.state.Snags[i].Notes = fragileString(msg.errMsg)
					if debugLog != nil {
						debugLog.Printf("revert failed snag=%s err=%q", msg.snagID, msg.errMsg)
					}
				}
				break
			}
		}
		if m.mode == viewDetails && msg.snagID == m.detailsID {
			m.refreshDetails()
		}
		visible := m.visibleSnags()
		if m.cursor >= len(visible) && len(visible) > 0 {
			m.cursor = len(visible) - 1
		}
		m.clampView()
		cmds = append(cmds, saveCmd(m.projectRoot, m.state))

	case tea.KeyMsg:
		m.notice = ""

		if m.mode == viewDetails {
			return m.handleDetailsKey(msg)
		}

		if m.confirmingRevert {
			switch {
			case key.Matches(msg, keys.Enter):
				m.confirmingRevert = false
				visible := m.visibleSnags()
				if m.cursor < len(visible) {
					snag := visible[m.cursor]
					if snag.CommitHash != "" {
						// Cancellable so quit() can kill the conflict resolver
						// rather than orphaning it to commit after the app exits.
						ctx, cancel := context.WithCancel(context.Background())
						m.cancelRevert = cancel
						cmds = append(cmds, revertSnag(ctx, m.projectRoot, snag.ID, snag.Description, snag.CommitHash, m.cfg))
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
					} else if snag.Status != StatusInflight && snag.ID != m.mergingID {
						id := snag.ID
						if debugLog != nil {
							debugLog.Printf("state change snag=%s deleted status=%s", id, snag.Status)
						}
						if snag.Branch != "" {
							// Best-effort: don't leak the preserved branch.
							root := m.projectRoot
							cmds = append(cmds, func() tea.Msg {
								deleteSnagBranch(root, id)
								return nil
							})
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
				if m.cursor < len(visible) && visible[m.cursor].Status == StatusFailed && visible[m.cursor].ID != m.mergingID {
					id := visible[m.cursor].ID
					for i := range m.state.Snags {
						if m.state.Snags[i].ID == id {
							m.state.Snags[i].Status = StatusPending
							m.state.Snags[i].Notes = ""
							// A rerun recreates the worktree, whose defensive
							// cleanup deletes any preserved branch.
							m.state.Snags[i].Branch = ""
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

		case key.Matches(msg, keys.Merge):
			if m.focus == focusList {
				visible := m.visibleSnags()
				if m.cursor < len(visible) {
					snag := visible[m.cursor]
					if snag.Status == StatusFailed && snag.Branch != "" && m.mergingID == "" {
						if m.working {
							m.notice = "waiting for current snag to finish"
						} else {
							m.mergingID = snag.ID
							if debugLog != nil {
								debugLog.Printf("agentic merge started snag=%s branch=%s", snag.ID, snag.Branch)
							}
							// Cancellable so quit() can kill the merge agent rather
							// than orphaning it to commit after the app exits.
							ctx, cancel := context.WithCancel(context.Background())
							m.cancelMerge = cancel
							cmds = append(cmds, agenticMergeCmd(ctx, m.projectRoot, m.defaultBranch, m.cfg, snag))
						}
					}
				}
			} else {
				forwardToInput = true
			}

		case key.Matches(msg, keys.Scan):
			if !m.scanning {
				m.scanning = true
				cmds = append(cmds, scanCmd(m.projectRoot, m.cfg.Marker))
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
			} else if m.focus == focusList {
				visible := m.visibleSnags()
				if m.cursor < len(visible) {
					m.mode = viewDetails
					m.detailsID = visible[m.cursor].ID
					m.detailsEvents = readTranscript(snagLogFile(m.projectRoot, m.detailsID))
					m.detailsScroll = m.detailsMaxScroll() // open at the tail
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
	if m.paused || m.working || m.mergingID != "" {
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
	if m.cancelMerge != nil {
		m.cancelMerge()
	}
	if m.cancelRevert != nil {
		m.cancelRevert()
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

func scanCmd(projectRoot, keyword string) tea.Cmd {
	return func() tea.Msg {
		markers, err := ScanMarkers(projectRoot, keyword)
		return scanDoneMsg{markers: markers, err: err}
	}
}

func summaryCmd(projectRoot string, cfg AgentConfig, snag Snag) tea.Cmd {
	return func() tea.Msg {
		summary, err := runSummary(context.Background(), projectRoot, cfg, snag.Description, string(snag.Context))
		return summaryDoneMsg{snagID: snag.ID, summary: summary, err: err}
	}
}

// markerSnagExists reports whether a live (pending/inflight/failed) marker
// snag already tracks the marker at file with the given text. Complete and
// reverted snags don't block re-pickup.
func (m Model) markerSnagExists(file, text string) bool {
	for _, s := range m.state.Snags {
		if s.Source != SourceMarker || s.File != file || s.Description != text {
			continue
		}
		switch s.Status {
		case StatusPending, StatusInflight, StatusFailed:
			return true
		}
	}
	return false
}

// displayTitle is the snag's list/details title: the agent summary when one
// exists, otherwise the raw description.
func displayTitle(s Snag) string {
	if s.Summary != "" {
		return s.Summary
	}
	return s.Description
}

// snagDuration is the run duration of a finished snag: the persisted Duration
// when present, otherwise derived from the start/completion timestamps.
func snagDuration(s Snag) string {
	if s.Duration != "" {
		return s.Duration
	}
	if !s.StartedAt.IsZero() && !s.CompletedAt.IsZero() {
		return s.CompletedAt.Sub(s.StartedAt).Round(time.Second).String()
	}
	return ""
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
	if m.mode == viewDetails {
		return m.detailsView()
	}

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
	merging := s.ID != "" && s.ID == m.mergingID

	var indicator string
	switch {
	case merging, s.Status == StatusInflight:
		indicator = m.spinner.View()
	case s.Status == StatusFailed:
		indicator = "✗"
	case s.Status == StatusComplete:
		indicator = "✓"
	case s.Status == StatusReverted:
		indicator = "↩"
	default:
		indicator = " "
	}

	sel := " "
	if selected {
		sel = "▶"
	}

	line := fmt.Sprintf("%s %2d %s %s", sel, pos, indicator, displayTitle(s))

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

	if merging {
		line += faintStyle.Render("  merging")
	}

	if s.Status == StatusInflight && !m.inflightStart.IsZero() {
		elapsed := time.Since(m.inflightStart).Round(time.Second).String()
		line += faintStyle.Render("  " + elapsed)
	}

	if s.Status == StatusComplete || s.Status == StatusFailed {
		if d := snagDuration(s); d != "" {
			line += faintStyle.Render("  " + d)
		}
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
		return string(s.Notes)
	}
	if s.Status == StatusPending && s.Source == SourceMarker && s.Notes == "" {
		return fmt.Sprintf("from %s:%d", s.File, s.Line)
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
	if m.notice != "" {
		return m.notice
	}
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
				if s.Branch != "" {
					return "enter details  m agentic merge  r retry"
				}
				return "enter details  r retry"
			}
			if s.Status == StatusPending {
				return "enter details  e edit  backspace delete  ↑↓ navigate  alt+↑↓ reorder"
			}
			if s.Status == StatusInflight {
				return "enter details  ↑↓ navigate"
			}
			if s.Status == StatusComplete {
				if s.CommitHash != "" {
					return "enter details  backspace revert  tab toggle history  ↑↓ navigate"
				}
				return "enter details  tab toggle history  ↑↓ navigate"
			}
		}
		return "tab toggle history  ↑↓ navigate"
	}
	return "↑↓ navigate  ctrl+s scan markers  ctrl+p pause/resume  esc clear/quit"
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
