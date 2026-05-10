package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/hubapi"
)

func TestHubModelInitialFetchRendersRows(t *testing.T) {
	client, cleanup := newTestHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tree" {
			http.NotFound(w, r)
			return
		}
		writeJSON(t, w, hubapi.TreeResponse{
			Live: []hubapi.TreeNode{{
				Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "awaiting", Project: "serf", Live: true,
			}},
			Projects: []hubapi.TreeProject{{
				Name: "serf",
				Sessions: []hubapi.TreeNode{{
					Ref: "local:01PAST", SessionID: "01PAST", Title: "past task", State: "ended", Project: "serf",
				}},
			}},
		})
	}))
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	msg := m.Init()()
	updated, _ := m.Update(msg)
	got := updated.(hubModel).View()
	for _, want := range []string{"live task", "past task", "awaiting", "serf"} {
		if !strings.Contains(got, want) {
			t.Fatalf("view missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelEnterOpensSessionDetail(t *testing.T) {
	client, cleanup := newTestHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tree":
			writeJSON(t, w, hubapi.TreeResponse{Live: []hubapi.TreeNode{{
				Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true,
			}}})
		case "/api/sessions/local:01LIVE":
			writeJSON(t, w, hubapi.SessionDetail{
				Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Live: true, WorkingDir: "/tmp/serf",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	updated, _ := m.Update(m.Init()())
	updated, cmd := updated.(hubModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter did not return a session fetch command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel).View()
	for _, want := range []string{"live task", "01LIVE", "/tmp/serf"} {
		if !strings.Contains(got, want) {
			t.Fatalf("view missing %q:\n%s", want, got)
		}
	}
}

func TestModelSSEReplayUserInputRendersMessage(t *testing.T) {
	m := newModel("", "", nil)
	m.handleSSEEvent(SSEEvent{Event: "USER_INPUT", Data: `{"text":"hello from replay"}`})
	if len(m.messages) != 1 || m.messages[0].Kind != msgUser || m.messages[0].Text != "hello from replay" {
		t.Fatalf("messages=%+v", m.messages)
	}
}

func TestModelSSEReplayAssistantTextEndRendersMessage(t *testing.T) {
	m := newModel("", "", nil)
	m.handleSSEEvent(SSEEvent{Event: "ASSISTANT_TEXT_END", Data: `{"text":"assistant replay"}`})
	if len(m.messages) != 1 || m.messages[0].Kind != msgAssistant || m.messages[0].Text != "assistant replay" {
		t.Fatalf("messages=%+v", m.messages)
	}
}

func TestHubModelReplayDoneIsNotError(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	updated, _ := m.Update(sseEventMsg{Event: "REPLAY_DONE", Data: `{}`})
	got := updated.(hubModel)
	if got.err != nil {
		t.Fatalf("err=%v", got.err)
	}
}

func newTestHubClient(t *testing.T, handler http.Handler) (*hubapi.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	client, err := hubapi.NewClient(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, srv.Close
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatal(err)
	}
}
