package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestModel(snags []Snag) Model {
	return New("/tmp/testproj", "main", State{Snags: snags})
}

func update(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func keyMsg(k tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: k}
}

// --- Entry field ---

func TestAddSnag(t *testing.T) {
	m := newTestModel(nil)
	m.input.SetValue("fix the flaky test")
	m = update(m, keyMsg(tea.KeyEnter))

	visible := m.visibleSnags()
	if len(visible) != 1 {
		t.Fatalf("expected 1 snag, got %d", len(visible))
	}
	if visible[0].Description != "fix the flaky test" {
		t.Errorf("wrong description: %q", visible[0].Description)
	}
	if visible[0].Status != StatusPending {
		t.Errorf("expected pending, got %q", visible[0].Status)
	}
}

func TestEnterWithEmptyFieldDoesNothing(t *testing.T) {
	m := newTestModel(nil)
	m = update(m, keyMsg(tea.KeyEnter))
	if len(m.visibleSnags()) != 0 {
		t.Error("should not add empty snag")
	}
}

func TestEscClearsNonEmptyInput(t *testing.T) {
	m := newTestModel(nil)
	m.input.SetValue("some text")
	m = update(m, keyMsg(tea.KeyEsc))
	if m.input.Value() != "" {
		t.Errorf("expected empty input, got %q", m.input.Value())
	}
}

func TestEscOnEmptyInputQuitsApp(t *testing.T) {
	m := newTestModel(nil)
	_, cmd := m.Update(keyMsg(tea.KeyEsc))
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected QuitMsg, got %T", msg)
	}
}

// --- List navigation ---

func TestUpMovesToList(t *testing.T) {
	m := newTestModel([]Snag{{ID: "a", Status: StatusPending, Description: "snag"}})
	// default focus is input
	m = update(m, keyMsg(tea.KeyUp))
	if m.focus != focusList {
		t.Error("expected focus to move to list after pressing up")
	}
	if m.cursor != 0 {
		t.Errorf("expected cursor 0, got %d", m.cursor)
	}
}

func TestUpOnEmptyListStaysInInput(t *testing.T) {
	m := newTestModel(nil)
	m = update(m, keyMsg(tea.KeyUp))
	if m.focus != focusInput {
		t.Error("should stay in input when list is empty")
	}
}

func TestDownFromLastItemReturnsToInput(t *testing.T) {
	m := newTestModel([]Snag{{ID: "a", Status: StatusPending, Description: "snag"}})
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyDown))
	if m.focus != focusInput {
		t.Error("expected focus to return to input")
	}
}

func TestNavigateBetweenItems(t *testing.T) {
	snags := []Snag{
		{ID: "a", Status: StatusPending, Description: "first"},
		{ID: "b", Status: StatusPending, Description: "second"},
	}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyDown))
	if m.cursor != 1 {
		t.Errorf("expected cursor 1 after down, got %d", m.cursor)
	}
	m = update(m, keyMsg(tea.KeyUp))
	if m.cursor != 0 {
		t.Errorf("expected cursor 0 after up, got %d", m.cursor)
	}
}

// --- Delete ---

func TestDeletePendingSnag(t *testing.T) {
	snags := []Snag{
		{ID: "a", Status: StatusPending, Description: "first"},
		{ID: "b", Status: StatusPending, Description: "second"},
	}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyBackspace))

	visible := m.visibleSnags()
	if len(visible) != 1 {
		t.Fatalf("expected 1 snag after delete, got %d", len(visible))
	}
	if visible[0].ID != "b" {
		t.Errorf("expected snag b to remain")
	}
}

func TestDeleteInflightSnagNoOp(t *testing.T) {
	snags := []Snag{{ID: "a", Status: StatusInflight, Description: "running"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyBackspace))
	if len(m.visibleSnags()) != 1 {
		t.Error("inflight snag should not be deletable")
	}
}

func TestDeleteLastSnagReturnsFocusToInput(t *testing.T) {
	snags := []Snag{{ID: "a", Status: StatusPending, Description: "only one"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyBackspace))
	if m.focus != focusInput {
		t.Error("expected focus to return to input after deleting last snag")
	}
}

// --- Pause/resume ---

func TestPauseResume(t *testing.T) {
	m := newTestModel(nil)
	m = update(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.paused {
		t.Error("expected paused after ctrl+p")
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.paused {
		t.Error("expected unpaused after second ctrl+p")
	}
}

// --- Worker result ---

func TestSnagDoneMsgSuccess(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusInflight, Description: "the task"}}
	m := newTestModel(snags)
	m.working = true
	m = update(m, snagDoneMsg{snagID: "abc", success: true, notes: "done"})

	var found *Snag
	for i := range m.state.Snags {
		if m.state.Snags[i].ID == "abc" {
			found = &m.state.Snags[i]
		}
	}
	if found == nil {
		t.Fatal("snag not found in state")
	}
	if found.Status != StatusComplete {
		t.Errorf("expected complete, got %q", found.Status)
	}
	if found.Notes != "done" {
		t.Errorf("expected notes 'done', got %q", found.Notes)
	}
	if m.working {
		t.Error("expected working=false after done")
	}
}

func TestSnagDoneMsgFailure(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusInflight, Description: "the task"}}
	m := newTestModel(snags)
	m.working = true
	m = update(m, snagDoneMsg{snagID: "abc", success: false, notes: "could not find file"})

	var found *Snag
	for i := range m.state.Snags {
		if m.state.Snags[i].ID == "abc" {
			found = &m.state.Snags[i]
		}
	}
	if found.Status != StatusFailed {
		t.Errorf("expected failed, got %q", found.Status)
	}
	// Failed snags are visible
	visible := m.visibleSnags()
	if len(visible) != 1 {
		t.Error("failed snag should still be visible")
	}
}

// --- Retry ---

func TestRetryFailedSnag(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusFailed, Description: "the task", Notes: "could not find file"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	var found *Snag
	for i := range m.state.Snags {
		if m.state.Snags[i].ID == "abc" {
			found = &m.state.Snags[i]
		}
	}
	if found == nil {
		t.Fatal("snag not found")
	}
	if found.Status != StatusPending {
		t.Errorf("expected pending after retry, got %q", found.Status)
	}
	if found.Notes != "" {
		t.Errorf("expected notes cleared after retry, got %q", found.Notes)
	}
}

func TestRetryOnlyWorksForFailed(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusPending, Description: "the task"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	if m.state.Snags[0].Status != StatusPending {
		t.Errorf("pressing r on pending snag should not change status, got %q", m.state.Snags[0].Status)
	}
}

func TestRetryInInputForwardsToInput(t *testing.T) {
	m := newTestModel(nil)
	// focus is on input by default
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if m.input.Value() != "r" {
		t.Errorf("expected 'r' to be typed in input, got %q", m.input.Value())
	}
}

// --- Visible snags filter ---

func TestCompleteSnagsHidden(t *testing.T) {
	snags := []Snag{
		{ID: "a", Status: StatusComplete},
		{ID: "b", Status: StatusPending},
		{ID: "c", Status: StatusFailed},
	}
	m := newTestModel(snags)
	visible := m.visibleSnags()
	if len(visible) != 2 {
		t.Fatalf("expected 2 visible snags (pending+failed), got %d", len(visible))
	}
	for _, s := range visible {
		if s.Status == StatusComplete {
			t.Error("complete snag should not be visible")
		}
	}
}
