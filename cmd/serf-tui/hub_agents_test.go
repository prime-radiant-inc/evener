package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/internal/appserver"
)

func TestHubModelAgentsPickerReadsSelectedTranscriptThroughAppWire(t *testing.T) {
	var readRefs []string
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodSerfThreadTranscriptsList, func(_ context.Context, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
			if params.Ref != "local:01SEND" {
				t.Errorf("transcript list ref=%q, want local:01SEND", params.Ref)
				return appwire.ThreadTranscriptListResponse{}, fmt.Errorf("unexpected ref: %q", params.Ref)
			}
			return appwire.ThreadTranscriptListResponse{Data: []appwire.ThreadTranscriptTarget{
				{Ref: "local:01SEND", Title: "main session (live)", Kind: "main", Status: appwire.ThreadStatusIdle},
				{Ref: "local:01SUB", Title: "subagent inspect", Kind: "subagent", Status: appwire.ThreadStatusNotLoaded},
			}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			readRefs = append(readRefs, params.Ref)
			if params.Ref != "local:01SUB" || !params.IncludeTurns {
				t.Errorf("thread/read params=%+v, want subagent full transcript", params)
				return appwire.ThreadReadResponse{}, fmt.Errorf("unexpected params: %+v", params)
			}
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:            "01SUB",
				SessionID:     "01SUB",
				Name:          "subagent inspect",
				ModelProvider: "gpt-5",
				Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusNotLoaded},
				Source:        "local",
				Serf:          appwire.SerfThread{Ref: "local:01SUB", Kind: "subagent", ParentRef: "local:01SEND"},
				Turns: []appwire.Turn{{
					ID:     "turn_1",
					Status: appwire.TurnStatusCompleted,
					Items: []appwire.ThreadItem{
						{Type: "agentMessage", ID: "agent-1", TurnID: "turn_1", Text: "subagent transcript answer", Status: "completed"},
					},
				}},
			}}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.messages = []transcript.ChatMessage{{Kind: transcript.MsgCommunicate, Text: "main answer"}}
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

func TestHubModelUnavailableAgentTranscriptKeepsParentSession(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodSerfThreadTranscriptsList, func(_ context.Context, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
			if params.Ref != "local:01SEND" {
				t.Errorf("transcript list ref=%q, want local:01SEND", params.Ref)
				return appwire.ThreadTranscriptListResponse{}, fmt.Errorf("unexpected ref: %q", params.Ref)
			}
			return appwire.ThreadTranscriptListResponse{Data: []appwire.ThreadTranscriptTarget{
				{Ref: "local:01SEND", Title: "main session", Kind: "main", Status: appwire.ThreadStatusIdle},
				{Ref: "local:01SUB", Title: "subagent archived", Kind: "subagent", Status: appwire.ThreadStatusNotLoaded},
			}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			if params.Ref != "local:01SUB" {
				t.Errorf("thread/read ref=%q, want local:01SUB", params.Ref)
				return appwire.ThreadReadResponse{}, fmt.Errorf("unexpected ref: %q", params.Ref)
			}
			return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("transcript archived by source")
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.messages = []transcript.ChatMessage{{Kind: transcript.MsgCommunicate, Text: "main answer"}}
	m.session.setInputValue("/agents")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated, cmd = updated.(hubModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("selecting unavailable transcript should attempt a Hub read")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel).View()
	for _, want := range []string{"main answer", "Could not read transcript: transcript archived by source"} {
		if !strings.Contains(got, want) {
			t.Fatalf("unavailable transcript view missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Viewing subagent archived") {
		t.Fatalf("unavailable transcript should not replace parent session:\n%s", got)
	}
}

func TestHubTranscriptPickerItemsIncludeSourceStatusAndTurns(t *testing.T) {
	items := hubTranscriptPickerItems([]appwire.ThreadTranscriptTarget{
		{Ref: "codex:01MAIN", Title: "main session", Kind: "main", Status: appwire.ThreadStatusIdle, Source: "codex"},
		{Ref: "local:01SUB", Title: "subagent inspect", Kind: "subagent", Status: appwire.ThreadStatusActive, Source: "serf", TurnsUsed: 2},
	})
	if len(items) != 2 {
		t.Fatalf("items=%+v", items)
	}
	if items[0].ID != "codex:01MAIN" {
		t.Fatalf("main transcript ID=%q", items[0].ID)
	}
	if items[0].Display != "main session (codex, idle)" {
		t.Fatalf("main transcript display=%q", items[0].Display)
	}
	if items[1].ID != "local:01SUB" {
		t.Fatalf("subagent transcript ID=%q", items[1].ID)
	}
	if items[1].Display != "subagent inspect (serf, active, 2 turns)" {
		t.Fatalf("subagent transcript display=%q", items[1].Display)
	}
}

func TestHubModelAgentsPickerShowsCodexSourceAndLiveSubagent(t *testing.T) {
	var readRefs []string
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodSerfThreadTranscriptsList, func(_ context.Context, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
			if params.Ref != "codex:01CODEX" {
				t.Errorf("transcript list ref=%q, want codex:01CODEX", params.Ref)
				return appwire.ThreadTranscriptListResponse{}, fmt.Errorf("unexpected ref: %q", params.Ref)
			}
			return appwire.ThreadTranscriptListResponse{Data: []appwire.ThreadTranscriptTarget{
				{Ref: "codex:01CODEX", Title: "main session", Kind: "main", Status: appwire.ThreadStatusIdle, Source: "codex"},
				{Ref: "codex:01LIVE", Title: "live subagent", Kind: "subagent", Status: appwire.ThreadStatusActive, Source: "codex", TurnsUsed: 2},
			}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			readRefs = append(readRefs, params.Ref)
			if params.Ref != "codex:01LIVE" || !params.IncludeTurns {
				t.Errorf("thread/read params=%+v, want live Codex subagent full transcript", params)
				return appwire.ThreadReadResponse{}, fmt.Errorf("unexpected params: %+v", params)
			}
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:            "01LIVE",
				SessionID:     "01LIVE",
				Name:          "live subagent",
				ModelProvider: "gpt-5",
				Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
				Source:        "codex",
				Serf:          appwire.SerfThread{Ref: "codex:01LIVE", Kind: "subagent", ParentRef: "codex:01CODEX"},
				Turns: []appwire.Turn{{
					ID:     "turn_live",
					Status: appwire.TurnStatusInProgress,
					Items: []appwire.ThreadItem{
						{Type: "agentMessage", ID: "agent-live", TurnID: "turn_live", Text: "live codex subagent answer", Status: "inProgress"},
					},
				}},
			}}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.Ref = "codex:01CODEX"
	m.detail.SourceLabel = "codex"
	m.session.sessionID = "01CODEX"
	m.session.messages = []transcript.ChatMessage{{Kind: transcript.MsgCommunicate, Text: "main codex answer"}}
	m.session.setInputValue("/agents")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/agents should fetch Codex transcript targets through Hub")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)
	got := m.View()
	for _, want := range []string{"main session (codex, idle)", "live subagent (codex, active, 2 turns)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Codex transcript picker missing %q:\n%s", want, got)
		}
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd != nil {
		t.Fatal("picker navigation should be synchronous")
	}
	updated, cmd = updated.(hubModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("selecting live Codex subagent should read it through Hub")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)
	if len(readRefs) != 1 || readRefs[0] != "codex:01LIVE" {
		t.Fatalf("read refs=%v, want codex:01LIVE", readRefs)
	}
	got = m.View()
	for _, want := range []string{"src codex", "Viewing live subagent [codex]", "live codex subagent answer"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Codex transcript view missing %q:\n%s", want, got)
		}
	}
}
