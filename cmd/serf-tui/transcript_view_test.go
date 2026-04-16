package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/server"
)

func TestBuildTranscriptPickerItems_MergesObservedAndActiveSubagents(t *testing.T) {
	m := newModel("localhost:0", t.TempDir(), nil)
	m.sessionID = "root-123"
	m.observedSubagents["sub-complete"] = subagentUI{status: "completed", turnsUsed: 3}

	items := m.buildTranscriptPickerItems(server.StatusInfo{
		SessionID: "root-123",
		Detailed: &server.DetailedStatus{
			Subagents: []server.SubagentStatusInfo{
				{ID: "sub-running", Status: "running", TurnsUsed: 1},
			},
		},
	})

	if len(items) != 3 {
		t.Fatalf("expected 3 transcript targets, got %d", len(items))
	}
	if items[0].id != "root-123" || items[0].display != "main session (live)" {
		t.Fatalf("unexpected main-session item: %+v", items[0])
	}

	displays := []string{items[1].display, items[2].display}
	joined := strings.Join(displays, "\n")
	if !strings.Contains(joined, "subagent sub-comp") {
		t.Errorf("picker should include completed observed subagent, got %q", joined)
	}
	if !strings.Contains(joined, "subagent sub-runn") {
		t.Errorf("picker should include active status subagent, got %q", joined)
	}
}

func TestTranscriptView_RefreshesSubagentTranscript(t *testing.T) {
	initTheme()

	stateDir := t.TempDir()
	writer, err := agent.NewTranscriptWriter(transcriptFilePath(stateDir, "sub-123"), agent.TranscriptHeader{
		SessionID: "sub-123",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "gpt-5",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer writer.Close()

	if err := writer.Append(agent.NewTurn(agent.TurnUserInput, llm.User("first"))); err != nil {
		t.Fatalf("Append user turn: %v", err)
	}

	m := newModel("localhost:0", stateDir, []chatMessage{{Kind: msgUser, Text: "main"}})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	cmd := m.enterTranscriptView("sub-123", "subagent sub-123")
	if cmd == nil {
		t.Fatal("enterTranscriptView should start transcript polling for subagents")
	}
	if m.transcriptView == nil {
		t.Fatal("transcript view should be active")
	}
	if len(m.visibleMessages()) != 1 || m.visibleMessages()[0].Text != "first" {
		t.Fatalf("unexpected initial transcript messages: %+v", m.visibleMessages())
	}

	if err := writer.Append(agent.NewTurn(agent.TurnAssistant, llm.Assistant("second"))); err != nil {
		t.Fatalf("Append assistant turn: %v", err)
	}

	updated, cmd = m.Update(transcriptRefreshMsg{})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("transcriptRefreshMsg should reschedule polling while transcript view is active")
	}

	msgs := m.visibleMessages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 transcript messages after refresh, got %d", len(msgs))
	}
	if msgs[1].Text != "second" {
		t.Fatalf("assistant transcript message = %q, want %q", msgs[1].Text, "second")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(model)
	if m.transcriptView != nil {
		t.Fatal("transcript view should close on escape")
	}
	if m.scrollMode {
		t.Fatal("scroll mode should be disabled after leaving transcript view")
	}
	if len(m.visibleMessages()) != 1 || m.visibleMessages()[0].Text != "main" {
		t.Fatalf("expected to return to main transcript, got %+v", m.visibleMessages())
	}
}
