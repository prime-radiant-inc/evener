package main

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
)

func TestHubModelAgentsPickerReadsSelectedTranscriptThroughAppWire(t *testing.T) {
	var readRefs []string
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodSerfThreadTranscriptsList, func(_ context.Context, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
			if params.Ref != "local:01SEND" {
				t.Fatalf("transcript list ref=%q, want local:01SEND", params.Ref)
			}
			return appwire.ThreadTranscriptListResponse{Data: []appwire.ThreadTranscriptTarget{
				{Ref: "local:01SEND", Title: "main session (live)", Kind: "main", Status: appwire.ThreadStatusIdle},
				{Ref: "local:01SUB", Title: "subagent inspect", Kind: "subagent", Status: appwire.ThreadStatusEnded},
			}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			readRefs = append(readRefs, params.Ref)
			if params.Ref != "local:01SUB" || !params.IncludeTurns {
				t.Fatalf("thread/read params=%+v, want subagent full transcript", params)
			}
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:            "01SUB",
				SessionID:     "01SUB",
				Name:          "subagent inspect",
				ModelProvider: "gpt-5",
				Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusEnded},
				Source:        "local",
				Serf:          appwire.SerfThread{Ref: "local:01SUB", Kind: "subagent", ParentRef: "local:01SEND"},
				Turns: []appwire.Turn{{
					ID:     "turn_1",
					Status: appwire.TurnStatusCompleted,
					Items: []appwire.ThreadItem{
						{Type: "agent_message", ID: "agent-1", TurnID: "turn_1", Text: "subagent transcript answer", Status: "completed"},
					},
				}},
			}}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.messages = []chatMessage{{Kind: msgCommunicate, Text: "main answer"}}
	m.session.setInputValue("/agents")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/agents should fetch transcript targets through Hub")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)
	if got := m.View(); !strings.Contains(got, "Select transcript") || !strings.Contains(got, "subagent inspect") {
		t.Fatalf("transcript picker missing targets:\n%s", got)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd != nil {
		t.Fatal("picker navigation should be synchronous")
	}
	updated, cmd = updated.(hubModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("selecting a transcript should read it through Hub")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)
	if len(readRefs) != 1 || readRefs[0] != "local:01SUB" {
		t.Fatalf("read refs=%v, want local:01SUB", readRefs)
	}
	if got := m.View(); !strings.Contains(got, "Viewing subagent inspect") || !strings.Contains(got, "subagent transcript answer") {
		t.Fatalf("subagent transcript view missing:\n%s", got)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if cmd != nil {
		t.Fatal("leaving transcript view should be synchronous")
	}
	if got := updated.(hubModel).View(); !strings.Contains(got, "main answer") || strings.Contains(got, "subagent transcript answer") {
		t.Fatalf("main transcript was not restored:\n%s", got)
	}
}
