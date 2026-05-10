package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/agent"
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

func TestHubModelSendPostsThroughHub(t *testing.T) {
	var gotPath, gotText string
	client, cleanup := newTestHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotText = body.Text
		w.WriteHeader(http.StatusAccepted)
	}))
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.setInputValue("ship it")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("send returned nil command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel)
	if gotPath != "/api/sessions/local:01SEND/send" || gotText != "ship it" {
		t.Fatalf("path=%q text=%q", gotPath, gotText)
	}
	if got.session.input.Value() != "" {
		t.Fatalf("input not reset: %q", got.session.input.Value())
	}
}

func TestHubModelBusySendPreservesInput(t *testing.T) {
	client, cleanup := newTestHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusConflict)
	}))
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.setInputValue("keep me")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("send returned nil command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel)
	if got.session.input.Value() != "keep me" {
		t.Fatalf("input=%q", got.session.input.Value())
	}
}

func TestHubModelTasksAndDetailsUseHubEndpoints(t *testing.T) {
	var paths []string
	client, cleanup := newTestHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/sessions/local:01SEND/tasks":
			writeJSON(t, w, []agent.Task{{ID: 1, Type: agent.TaskTypeImplement, Description: "wire actions", Status: agent.TaskDone}})
		case "/api/sessions/local:01SEND":
			writeJSON(t, w, hubapi.SessionDetail{Ref: "local:01SEND", SessionID: "01SEND", Title: "send task", WorkingDir: "/tmp/details"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.setInputValue("/tasks")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = updated.(hubModel).Update(cmd())
	if !strings.Contains(updated.(hubModel).View(), "wire actions") {
		t.Fatalf("tasks view missing:\n%s", updated.(hubModel).View())
	}

	got := updated.(hubModel)
	got.session.setInputValue("/details")
	updated, cmd = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = updated.(hubModel).Update(cmd())
	if !strings.Contains(updated.(hubModel).View(), "/tmp/details") {
		t.Fatalf("details view missing:\n%s", updated.(hubModel).View())
	}
	if strings.Join(paths, ",") != "/api/sessions/local:01SEND/tasks,/api/sessions/local:01SEND" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestHubModelActionsAndClearUseHubEndpoints(t *testing.T) {
	var paths []string
	client, cleanup := newTestHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/sessions/local:01SEND/interrupt", "/api/sessions/local:01SEND/compact", "/api/sessions/local:01SEND/model":
			w.WriteHeader(http.StatusNoContent)
		case "/api/sessions/local:01SEND/clear":
			writeJSON(t, w, hubapi.RefResponse{Ref: "local:02NEW", HostID: "local", SessionID: "02NEW"})
		case "/api/sessions/local:02NEW":
			writeJSON(t, w, hubapi.SessionDetail{Ref: "local:02NEW", SessionID: "02NEW", Title: "new session"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cleanup()

	m := newSessionHubModel(client)
	for _, input := range []string{"/interrupt", "/compact", "/model gpt-5.5"} {
		m.session.setInputValue(input)
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatalf("%s returned nil command", input)
		}
		updated, _ = updated.(hubModel).Update(cmd())
		m = updated.(hubModel)
	}
	m.session.setInputValue("/clear")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, cmd = updated.(hubModel).Update(cmd())
	if cmd == nil {
		t.Fatal("clear did not return detail fetch command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	if updated.(hubModel).detail.SessionID != "02NEW" {
		t.Fatalf("detail=%+v", updated.(hubModel).detail)
	}
	want := "/api/sessions/local:01SEND/interrupt,/api/sessions/local:01SEND/compact,/api/sessions/local:01SEND/model,/api/sessions/local:01SEND/clear,/api/sessions/local:02NEW"
	if strings.Join(paths, ",") != want {
		t.Fatalf("paths=%v", paths)
	}
}

func newSessionHubModel(client *hubapi.Client) hubModel {
	m := newHubModel(client, "http://hub.test")
	m.mode = hubModeSession
	m.detail = hubapi.SessionDetail{
		Ref:       "local:01SEND",
		SessionID: "01SEND",
		Title:     "send task",
		State:     "idle",
		Capabilities: hubapi.SessionCapabilities{
			Send:        true,
			Interrupt:   true,
			Compact:     true,
			Clear:       true,
			ChangeModel: true,
		},
	}
	m.session.sessionID = "01SEND"
	return m
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
