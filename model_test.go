package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestModel(snags []Snag) Model {
	return New("/tmp/testproj", "main", State{Snags: snags}, DefaultConfig(), false)
}

func update(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func updateWithCmd(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

// collectMsgs executes a (possibly batched) command tree and returns every
// message it produces. Only safe for commands whose side effects are cheap.
func collectMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectMsgs(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func keyMsg(k tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: k}
}

func runeMsg(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
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

func TestSnagDoneMsgSuccessClearsBranch(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusInflight, Description: "the task", Branch: "snag/abc"}}
	m := newTestModel(snags)
	m.working = true
	m = update(m, snagDoneMsg{snagID: "abc", success: true, commitHash: "deadbeef"})

	if m.state.Snags[0].Branch != "" {
		t.Errorf("expected branch cleared on success (it was deleted), got %q", m.state.Snags[0].Branch)
	}
	if m.state.Snags[0].CommitHash != "deadbeef" {
		t.Errorf("expected commit hash recorded, got %q", m.state.Snags[0].CommitHash)
	}
}

func TestSnagDoneMsgMergeFailedSetsBranch(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusInflight, Description: "the task"}}
	m := newTestModel(snags)
	m.working = true
	m = update(m, snagDoneMsg{snagID: "abc", success: false, mergeFailed: true, notes: "merge conflict — branch snag/abc preserved"})

	if m.state.Snags[0].Status != StatusFailed {
		t.Errorf("expected failed, got %q", m.state.Snags[0].Status)
	}
	if m.state.Snags[0].Branch != "snag/abc" {
		t.Errorf("expected preserved branch recorded, got %q", m.state.Snags[0].Branch)
	}
}

func TestSnagDoneMsgMergeFailedMarkerAutoMerges(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusInflight, Description: "the task", Source: SourceMarker, File: "main.go"}}
	m := newTestModel(snags)
	m.working = true
	m = update(m, snagDoneMsg{snagID: "abc", success: false, mergeFailed: true, notes: "merge conflict — branch snag/abc preserved"})

	if m.state.Snags[0].Status != StatusFailed {
		t.Errorf("expected failed, got %q", m.state.Snags[0].Status)
	}
	if m.state.Snags[0].Branch != "snag/abc" {
		t.Errorf("expected preserved branch recorded, got %q", m.state.Snags[0].Branch)
	}
	if m.mergingID != "abc" {
		t.Errorf("expected marker snag to auto-start agentic merge, mergingID=%q", m.mergingID)
	}
	if m.cancelMerge == nil {
		t.Error("expected cancelMerge to be set for auto merge")
	}
}

func TestSnagDoneMsgMergeFailedTypedDoesNotAutoMerge(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusInflight, Description: "the task", Source: SourceInput}}
	m := newTestModel(snags)
	m.working = true
	m = update(m, snagDoneMsg{snagID: "abc", success: false, mergeFailed: true, notes: "merge conflict"})

	if m.mergingID != "" {
		t.Errorf("expected typed snag not to auto-merge, mergingID=%q", m.mergingID)
	}
}

func TestSnagDoneMsgPlainFailureClearsBranch(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusInflight, Description: "the task", Branch: "snag/abc"}}
	m := newTestModel(snags)
	m.working = true
	m = update(m, snagDoneMsg{snagID: "abc", success: false, notes: "agent failed"})

	if m.state.Snags[0].Branch != "" {
		t.Errorf("expected branch cleared on non-merge failure, got %q", m.state.Snags[0].Branch)
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

func TestRetryClearsBranch(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusFailed, Description: "the task", Branch: "snag/abc", Notes: "merge conflict"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	if m.state.Snags[0].Branch != "" {
		t.Errorf("expected branch cleared on retry (rerun deletes it), got %q", m.state.Snags[0].Branch)
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

// --- Marker scan ---

func TestScanDoneEnqueuesMarkers(t *testing.T) {
	m := newTestModel(nil)
	m = update(m, scanDoneMsg{markers: []Marker{
		{File: "a.go", Line: 3, Text: "fix x", Context: "func a() {}"},
	}})

	if len(m.state.Snags) != 1 {
		t.Fatalf("expected 1 snag, got %d", len(m.state.Snags))
	}
	s := m.state.Snags[0]
	if s.Status != StatusPending || s.Source != SourceMarker {
		t.Errorf("expected pending marker snag, got status=%q source=%q", s.Status, s.Source)
	}
	if s.Description != "fix x" || s.File != "a.go" || s.Line != 3 || s.Context != "func a() {}" {
		t.Errorf("marker fields not copied: %+v", s)
	}
	if s.ID == "" || s.CreatedAt.IsZero() {
		t.Error("expected ID and CreatedAt to be set")
	}
	if m.notice != "1 marker(s) queued" {
		t.Errorf("wrong notice: %q", m.notice)
	}
}

func TestScanDoneDedupesAgainstLiveMarkerSnags(t *testing.T) {
	snags := []Snag{
		{ID: "p", Status: StatusPending, Source: SourceMarker, File: "a.go", Description: "fix a"},
		{ID: "i", Status: StatusInflight, Source: SourceMarker, File: "b.go", Description: "fix b"},
		{ID: "f", Status: StatusFailed, Source: SourceMarker, File: "c.go", Description: "fix c"},
		{ID: "c", Status: StatusComplete, Source: SourceMarker, File: "d.go", Description: "fix d"},
	}
	m := newTestModel(snags)
	m = update(m, scanDoneMsg{markers: []Marker{
		{File: "a.go", Line: 1, Text: "fix a"},
		{File: "b.go", Line: 1, Text: "fix b"},
		{File: "c.go", Line: 1, Text: "fix c"},
		{File: "d.go", Line: 1, Text: "fix d"}, // complete doesn't block re-pickup
	}})

	if len(m.state.Snags) != 5 {
		t.Fatalf("expected 5 snags (4 existing + 1 re-picked), got %d", len(m.state.Snags))
	}
	added := m.state.Snags[4]
	if added.Description != "fix d" || added.Status != StatusPending {
		t.Errorf("expected new pending snag for fix d, got %+v", added)
	}
	if m.notice != "1 marker(s) queued, 3 duplicate(s) skipped" {
		t.Errorf("wrong notice: %q", m.notice)
	}
}

func TestScanDoneDedupesWithinBatch(t *testing.T) {
	m := newTestModel(nil)
	m = update(m, scanDoneMsg{markers: []Marker{
		{File: "a.go", Line: 1, Text: "fix x"},
		{File: "a.go", Line: 9, Text: "fix x"},
		{File: "b.go", Line: 2, Text: "fix x"}, // same text, other file: not a dup
	}})

	if len(m.state.Snags) != 2 {
		t.Fatalf("expected 2 snags, got %d", len(m.state.Snags))
	}
	if m.notice != "2 marker(s) queued, 1 duplicate(s) skipped" {
		t.Errorf("wrong notice: %q", m.notice)
	}
}

func TestScanDoneKicksWorkWhenIdle(t *testing.T) {
	// Nonexistent project root: executing the spawned commands (saveCmd,
	// summaryCmd) fails fast without side effects.
	root := filepath.Join(t.TempDir(), "missing")
	m := New(root, "main", State{}, DefaultConfig(), false)
	m, cmd := updateWithCmd(m, scanDoneMsg{markers: []Marker{{File: "a.go", Line: 1, Text: "fix x"}}})

	if m.working {
		t.Fatal("scanDoneMsg itself should not flip working")
	}
	var kicked bool
	for _, msg := range collectMsgs(cmd) {
		if _, ok := msg.(startWorkMsg); ok {
			kicked = true
		}
	}
	if !kicked {
		t.Error("expected startWorkMsg to be fired when idle")
	}
}

func TestScanDoneNoMarkersSetsNotice(t *testing.T) {
	m := newTestModel(nil)
	m = update(m, scanDoneMsg{})
	if m.notice != "no markers found" {
		t.Errorf("expected 'no markers found', got %q", m.notice)
	}
	if m.statusBarStr() != "no markers found" {
		t.Errorf("status bar should show the notice, got %q", m.statusBarStr())
	}
}

func TestScanDoneErrorSetsNotice(t *testing.T) {
	m := newTestModel(nil)
	m = update(m, scanDoneMsg{err: errors.New("git grep: boom")})
	if m.notice != "scan failed: git grep: boom" {
		t.Errorf("wrong notice: %q", m.notice)
	}
	if m.scanning {
		t.Error("expected scanning cleared")
	}
}

func TestNoticeClearedOnKeyPress(t *testing.T) {
	m := newTestModel(nil)
	m.notice = "no markers found"
	m = update(m, keyMsg(tea.KeyUp))
	if m.notice != "" {
		t.Errorf("expected notice cleared on key press, got %q", m.notice)
	}
}

func TestScanKeyStartsScan(t *testing.T) {
	m := newTestModel(nil)
	m, cmd := updateWithCmd(m, keyMsg(tea.KeyCtrlS))
	if !m.scanning {
		t.Error("expected scanning=true after ctrl+s")
	}
	if cmd == nil {
		t.Error("expected a scan command")
	}
}

func TestScanKeyIgnoredWhileScanning(t *testing.T) {
	m := newTestModel(nil)
	m.scanning = true
	m, cmd := updateWithCmd(m, keyMsg(tea.KeyCtrlS))
	if cmd != nil {
		t.Error("expected no command while a scan is in flight")
	}
	if !m.scanning {
		t.Error("scanning flag should be unchanged")
	}
}

// --- Summaries ---

func TestSummaryDoneSetsSummary(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusPending, Description: "// long marker text", Source: SourceMarker}}
	m := newTestModel(snags)
	m = update(m, summaryDoneMsg{snagID: "abc", summary: "Fix the widget"})
	if m.state.Snags[0].Summary != "Fix the widget" {
		t.Errorf("expected summary set, got %q", m.state.Snags[0].Summary)
	}
}

func TestSummaryDoneErrorIsSilent(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusPending, Description: "task"}}
	m := newTestModel(snags)
	m = update(m, summaryDoneMsg{snagID: "abc", err: errors.New("boom")})
	if m.state.Snags[0].Summary != "" {
		t.Errorf("expected no summary on error, got %q", m.state.Snags[0].Summary)
	}
	if m.notice != "" {
		t.Errorf("summary errors should be silent, got notice %q", m.notice)
	}
}

func TestDisplayTitleFallsBackToDescription(t *testing.T) {
	if got := displayTitle(Snag{Description: "desc", Summary: "sum"}); got != "sum" {
		t.Errorf("expected summary, got %q", got)
	}
	if got := displayTitle(Snag{Description: "desc"}); got != "desc" {
		t.Errorf("expected description fallback, got %q", got)
	}
}

func TestSelectedNotesShowsMarkerOrigin(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusPending, Source: SourceMarker, File: "pkg/a.go", Line: 12, Description: "fix"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	if got := m.selectedNotes(); got != "from pkg/a.go:12" {
		t.Errorf("expected marker origin, got %q", got)
	}
}

// --- Agentic merge ---

func TestMergeKeyStartsMerge(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusFailed, Branch: "snag/abc", Description: "task"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m, cmd := updateWithCmd(m, runeMsg('m'))
	if m.mergingID != "abc" {
		t.Errorf("expected mergingID=abc, got %q", m.mergingID)
	}
	if cmd == nil {
		t.Error("expected a merge command")
	}
}

func TestMergeKeyIgnoredWhileWorking(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusFailed, Branch: "snag/abc", Description: "task"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m.working = true
	m = update(m, runeMsg('m'))
	if m.mergingID != "" {
		t.Errorf("merge should be ignored while working, got mergingID=%q", m.mergingID)
	}
	if m.notice != "waiting for current snag to finish" {
		t.Errorf("expected a notice explaining the deferral, got %q", m.notice)
	}
}

func TestMergeKeyIgnoredWhileAnotherMergeRuns(t *testing.T) {
	snags := []Snag{
		{ID: "abc", Status: StatusFailed, Branch: "snag/abc", Description: "task"},
		{ID: "def", Status: StatusFailed, Branch: "snag/def", Description: "other"},
	}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 1
	m.mergingID = "abc"
	m = update(m, runeMsg('m'))
	if m.mergingID != "abc" {
		t.Errorf("second merge should be ignored, got mergingID=%q", m.mergingID)
	}
}

func TestMergeKeyIgnoredWithoutBranch(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusFailed, Description: "task"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, runeMsg('m'))
	if m.mergingID != "" {
		t.Errorf("merge requires a preserved branch, got mergingID=%q", m.mergingID)
	}
}

func TestMergeKeyInInputForwardsToInput(t *testing.T) {
	m := newTestModel(nil)
	m = update(m, runeMsg('m'))
	if m.input.Value() != "m" {
		t.Errorf("expected 'm' typed into input, got %q", m.input.Value())
	}
}

func TestMergeDoneSuccess(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusFailed, Branch: "snag/abc", Description: "task", Notes: "merge conflict — branch snag/abc preserved"}}
	m := newTestModel(snags)
	m.mergingID = "abc"
	m.cancelMerge = func() {}
	m = update(m, mergeDoneMsg{snagID: "abc", success: true, commitHash: "deadbeef"})

	s := m.state.Snags[0]
	if s.Status != StatusComplete {
		t.Errorf("expected complete, got %q", s.Status)
	}
	if s.CommitHash != "deadbeef" {
		t.Errorf("expected commit hash, got %q", s.CommitHash)
	}
	if s.Branch != "" {
		t.Errorf("expected branch cleared, got %q", s.Branch)
	}
	if s.Notes != "merged by agent" {
		t.Errorf("expected stale failure notes replaced with 'merged by agent', got %q", s.Notes)
	}
	if m.mergingID != "" {
		t.Error("expected mergingID cleared")
	}
	if m.cancelMerge != nil {
		t.Error("expected cancelMerge cleared")
	}
	if !m.sessionCompletedIDs["abc"] {
		t.Error("expected snag marked session-completed (stays visible)")
	}
}

func TestMergeDoneSuccessFillsEmptyNotes(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusFailed, Branch: "snag/abc", Description: "task"}}
	m := newTestModel(snags)
	m.mergingID = "abc"
	m = update(m, mergeDoneMsg{snagID: "abc", success: true, commitHash: "deadbeef"})
	if m.state.Snags[0].Notes != "merged by agent" {
		t.Errorf("expected 'merged by agent', got %q", m.state.Snags[0].Notes)
	}
}

func TestMergeKeySetsCancelMerge(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusFailed, Branch: "snag/abc", Description: "task"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m, _ = updateWithCmd(m, runeMsg('m')) // do not run the cmd: it would invoke claude
	if m.cancelMerge == nil {
		t.Error("expected a cancel func stored when a merge starts")
	}
}

func TestQuitCancelsMergeAgent(t *testing.T) {
	m := newTestModel(nil)
	var cancelled bool
	m.cancelMerge = func() { cancelled = true }
	m.quit()
	if !cancelled {
		t.Error("quit should cancel an in-flight merge agent")
	}
}

// --- Revert cancellation (same shape as the merge agent) ---

func TestRevertConfirmSetsCancelRevert(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusComplete, CommitHash: "deadbeef", Description: "task"}}
	m := newTestModel(snags)
	m.showHistory = true
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyBackspace)) // arm the revert confirmation
	if !m.confirmingRevert {
		t.Fatal("expected revert confirmation armed")
	}
	m, _ = updateWithCmd(m, keyMsg(tea.KeyEnter)) // do not run the cmd: it would invoke git revert
	if m.cancelRevert == nil {
		t.Error("expected a cancel func stored when a revert starts")
	}
}

func TestQuitCancelsRevertAgent(t *testing.T) {
	m := newTestModel(nil)
	var cancelled bool
	m.cancelRevert = func() { cancelled = true }
	m.quit()
	if !cancelled {
		t.Error("quit should cancel an in-flight revert resolver")
	}
}

func TestRevertDoneClearsCancelRevert(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusComplete, CommitHash: "deadbeef", Description: "task"}}
	m := newTestModel(snags)
	m.cancelRevert = func() {}
	m = update(m, revertDoneMsg{snagID: "abc", success: true})
	if m.cancelRevert != nil {
		t.Error("expected cancelRevert cleared on revertDoneMsg")
	}
	if m.state.Snags[0].Status != StatusReverted {
		t.Errorf("expected reverted, got %q", m.state.Snags[0].Status)
	}
}

func TestMergeDoneFailure(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusFailed, Branch: "snag/abc", Description: "task", Notes: "merge conflict"}}
	m := newTestModel(snags)
	m.mergingID = "abc"
	m = update(m, mergeDoneMsg{snagID: "abc", success: false, errMsg: "agent gave up"})

	s := m.state.Snags[0]
	if s.Status != StatusFailed {
		t.Errorf("expected still failed, got %q", s.Status)
	}
	if s.Notes != "agent gave up" {
		t.Errorf("expected notes=errMsg, got %q", s.Notes)
	}
	if s.Branch != "snag/abc" {
		t.Errorf("expected branch kept, got %q", s.Branch)
	}
	if m.mergingID != "" {
		t.Error("expected mergingID cleared")
	}
}

func TestStartNextSnagBlockedWhileMerging(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusPending, Description: "task"}}
	m := newTestModel(snags)
	m.mergingID = "other"
	m = update(m, startWorkMsg{})
	if m.working {
		t.Error("expected no work start while a merge runs")
	}
	if m.state.Snags[0].Status != StatusPending {
		t.Errorf("snag should stay pending, got %q", m.state.Snags[0].Status)
	}
}

func TestRetryIgnoredForMergingSnag(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusFailed, Branch: "snag/abc", Description: "task"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m.mergingID = "abc"
	m = update(m, runeMsg('r'))
	if m.state.Snags[0].Status != StatusFailed || m.state.Snags[0].Branch != "snag/abc" {
		t.Errorf("retry should be ignored while merging: %+v", m.state.Snags[0])
	}
}

func TestStatusBarShowsMergeForFailedWithBranch(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusFailed, Branch: "snag/abc", Description: "task"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	if got := m.statusBarStr(); got != "enter details  m agentic merge  r retry" {
		t.Errorf("wrong status bar: %q", got)
	}
	m.state.Snags[0].Branch = ""
	if got := m.statusBarStr(); got != "enter details  r retry" {
		t.Errorf("wrong status bar without branch: %q", got)
	}
}

func TestDeleteFailedSnagWithBranchCleansUpBranch(t *testing.T) {
	dir := initMergeTestRepo(t)
	gitRun(t, dir, "branch", "snag/abc")
	m := New(dir, "master", State{Snags: []Snag{{ID: "abc", Status: StatusFailed, Branch: "snag/abc", Description: "task"}}}, DefaultConfig(), false)
	m.focus = focusList
	m.cursor = 0
	m, cmd := updateWithCmd(m, keyMsg(tea.KeyBackspace))

	if len(m.visibleSnags()) != 0 {
		t.Fatal("expected snag removed")
	}
	collectMsgs(cmd) // runs the best-effort branch deletion
	if branchExists(dir, "snag/abc") {
		t.Error("expected preserved branch deleted along with the snag")
	}
}

func TestDeleteIgnoredForMergingSnag(t *testing.T) {
	snags := []Snag{{ID: "abc", Status: StatusFailed, Branch: "snag/abc", Description: "task"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m.mergingID = "abc"
	m = update(m, keyMsg(tea.KeyBackspace))
	if len(m.visibleSnags()) != 1 {
		t.Error("snag being merged should not be deletable")
	}
}

// --- Details page ---

func TestEnterOpensDetailsForSelectedSnag(t *testing.T) {
	snags := []Snag{
		{ID: "a", Status: StatusPending, Description: "first"},
		{ID: "b", Status: StatusFailed, Description: "second"},
	}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 1
	m = update(m, keyMsg(tea.KeyEnter))
	if m.mode != viewDetails {
		t.Fatal("expected details mode after enter")
	}
	if m.detailsID != "b" {
		t.Errorf("expected detailsID=b, got %q", m.detailsID)
	}
}

func TestEscClosesDetails(t *testing.T) {
	snags := []Snag{{ID: "a", Status: StatusPending, Description: "first"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyEnter))
	m = update(m, keyMsg(tea.KeyEsc))
	if m.mode != viewList {
		t.Error("expected esc to close details")
	}
	if m.detailsID != "" {
		t.Errorf("expected detailsID cleared, got %q", m.detailsID)
	}
	if m.focus != focusList {
		t.Error("expected to return to list focus")
	}
}

func TestEnterClosesDetails(t *testing.T) {
	snags := []Snag{{ID: "a", Status: StatusPending, Description: "first"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyEnter))
	m = update(m, keyMsg(tea.KeyEnter))
	if m.mode != viewList {
		t.Error("expected enter to close details")
	}
}

func makeTextEvents(n int) []transcriptEvent {
	events := make([]transcriptEvent, n)
	for i := range events {
		events[i] = transcriptEvent{Type: "text", Text: fmt.Sprintf("line %d", i)}
	}
	return events
}

func TestDetailsScrollClamps(t *testing.T) {
	snags := []Snag{{ID: "a", Status: StatusFailed, Description: "task"}}
	m := newTestModel(snags)
	m.height = 10
	m.width = 40
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyEnter))
	m.detailsEvents = makeTextEvents(20)

	maxScroll := m.detailsMaxScroll()
	if maxScroll <= 0 {
		t.Fatalf("expected scrollable transcript, maxScroll=%d", maxScroll)
	}
	for i := 0; i < 100; i++ {
		m = update(m, keyMsg(tea.KeyDown))
	}
	if m.detailsScroll != maxScroll {
		t.Errorf("expected scroll clamped at %d, got %d", maxScroll, m.detailsScroll)
	}
	for i := 0; i < 100; i++ {
		m = update(m, keyMsg(tea.KeyUp))
	}
	if m.detailsScroll != 0 {
		t.Errorf("expected scroll clamped at 0, got %d", m.detailsScroll)
	}
	m = update(m, keyMsg(tea.KeyPgDown))
	if m.detailsScroll != min(m.detailsPageSize(), maxScroll) {
		t.Errorf("expected one page down, got %d", m.detailsScroll)
	}
	m = update(m, keyMsg(tea.KeyPgUp))
	if m.detailsScroll != 0 {
		t.Errorf("expected back to top after pgup, got %d", m.detailsScroll)
	}
}

func TestDetailsViewRendersStatusBar(t *testing.T) {
	snags := []Snag{{ID: "a", Status: StatusFailed, Description: "task", Notes: "broke"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyEnter))
	view := m.View()
	if !strings.Contains(view, "↑↓ scroll  pgup/pgdn page  esc back") {
		t.Error("details view should show its status bar")
	}
	if !strings.Contains(view, "no session log") {
		t.Error("details view should show 'no session log' when transcript is empty")
	}
}

func TestQueueRunsWhileDetailsOpen(t *testing.T) {
	snags := []Snag{
		{ID: "a", Status: StatusInflight, Description: "running"},
		{ID: "b", Status: StatusPending, Description: "queued"},
	}
	m := newTestModel(snags)
	m.working = true
	m.focus = focusList
	m.cursor = 1
	m = update(m, keyMsg(tea.KeyEnter)) // open details on b
	m = update(m, snagDoneMsg{snagID: "a", success: true, notes: "done"})

	if m.state.Snags[0].Status != StatusComplete {
		t.Errorf("snagDoneMsg should still apply in details mode, got %q", m.state.Snags[0].Status)
	}
	if m.mode != viewDetails {
		t.Error("details should stay open")
	}
}

func TestDetailsCtrlPTogglesPause(t *testing.T) {
	snags := []Snag{{ID: "a", Status: StatusPending, Description: "first"}}
	m := newTestModel(snags)
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyEnter))
	m = update(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.paused {
		t.Error("ctrl+p should pause from details mode")
	}
	if m.mode != viewDetails {
		t.Error("details should stay open across ctrl+p")
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.paused {
		t.Error("second ctrl+p should resume")
	}
}

func TestResizeFollowsTailInDetails(t *testing.T) {
	snags := []Snag{{ID: "a", Status: StatusFailed, Description: "task"}}
	m := newTestModel(snags)
	m.width = 40
	m.height = 30
	m.focus = focusList
	m.cursor = 0
	m = update(m, keyMsg(tea.KeyEnter))
	m.detailsEvents = makeTextEvents(50)
	m.detailsScroll = m.detailsMaxScroll() // parked at the tail

	// Shrinking raises maxScroll; a tail-parked view must follow it.
	m = update(m, tea.WindowSizeMsg{Width: 40, Height: 12})
	if m.detailsScroll != m.detailsMaxScroll() {
		t.Errorf("expected to stay at tail after shrink, got %d want %d", m.detailsScroll, m.detailsMaxScroll())
	}

	// Not at the tail: resize keeps position.
	m.detailsScroll = 1
	m = update(m, tea.WindowSizeMsg{Width: 40, Height: 10})
	if m.detailsScroll != 1 {
		t.Errorf("expected scroll position kept when not at tail, got %d", m.detailsScroll)
	}
}

func TestDetailsHeaderShowsElapsedForInflight(t *testing.T) {
	snags := []Snag{{ID: "a", Status: StatusInflight, Description: "task"}}
	m := newTestModel(snags)
	m.inflightStart = time.Now().Add(-90 * time.Second)
	header := strings.Join(m.detailsHeaderLines(m.state.Snags[0]), "\n")
	if !strings.Contains(header, "elapsed 1m30s") {
		t.Errorf("expected elapsed line for inflight snag, got:\n%s", header)
	}
}

func TestDetailsHeaderShowsDurationForFinished(t *testing.T) {
	snags := []Snag{{ID: "a", Status: StatusComplete, Description: "task", Duration: "42s"}}
	m := newTestModel(snags)
	header := strings.Join(m.detailsHeaderLines(m.state.Snags[0]), "\n")
	if !strings.Contains(header, "duration 42s") {
		t.Errorf("expected duration line for complete snag, got:\n%s", header)
	}
	m.state.Snags[0].Duration = ""
	header = strings.Join(m.detailsHeaderLines(m.state.Snags[0]), "\n")
	if strings.Contains(header, "duration") {
		t.Errorf("expected no duration line without data, got:\n%s", header)
	}
}
