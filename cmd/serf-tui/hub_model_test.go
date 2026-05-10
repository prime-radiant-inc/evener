package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	for _, want := range []string{"live task", "awaiting", "serf"} {
		if !strings.Contains(got, want) {
			t.Fatalf("view missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "past task") {
		t.Fatalf("dashboard rendered ended session:\n%s", got)
	}
}

func TestHubModelDashboardShowsOnlyLiveSessionsGroupedByProject(t *testing.T) {
	tree := hubapi.TreeResponse{
		Live: []hubapi.TreeNode{
			{Ref: "local:01LIVEA", SessionID: "01LIVEA", Title: "live alpha", State: "awaiting", Project: "serf", Live: true},
			{Ref: "local:01LIVEB", SessionID: "01LIVEB", Title: "live beta", State: "idle", Project: "serf", Live: true},
			{Ref: "local:01BRAIN", SessionID: "01BRAIN", Title: "brain live", State: "processing", Project: "brainstorm", Live: true},
		},
		Projects: []hubapi.TreeProject{
			{
				Key:         "serf",
				Name:        "serf",
				RollupState: "awaiting",
				Sessions: []hubapi.TreeNode{
					{Ref: "local:01LIVEA", SessionID: "01LIVEA", Title: "live alpha", State: "awaiting", Project: "serf", Live: true},
					{Ref: "local:01LIVEB", SessionID: "01LIVEB", Title: "live beta", State: "idle", Project: "serf", Live: true},
					{Ref: "local:01ENDED", SessionID: "01ENDED", Title: "ended history", State: "ended", Project: "serf", Live: false},
				},
			},
			{
				Key:         "brainstorm",
				Name:        "brainstorm",
				RollupState: "processing",
				Sessions: []hubapi.TreeNode{
					{Ref: "local:01BRAIN", SessionID: "01BRAIN", Title: "brain live", State: "processing", Project: "brainstorm", Live: true},
				},
			},
		},
	}

	rows := buildDashboardRows(tree)
	if len(rows) != 5 {
		t.Fatalf("rows=%d: %+v", len(rows), rows)
	}
	if rows[0].kind != hubRowProject || rows[0].project != "serf" {
		t.Fatalf("first row=%+v, want serf project header", rows[0])
	}
	if rows[1].kind != hubRowSession || rows[1].title != "live alpha" {
		t.Fatalf("second row=%+v, want live alpha session", rows[1])
	}
	if rows[3].kind != hubRowProject || rows[3].project != "brainstorm" {
		t.Fatalf("fourth row=%+v, want brainstorm project header", rows[3])
	}
	for _, row := range rows {
		if row.title == "ended history" {
			t.Fatalf("dashboard row included ended session: %+v", row)
		}
	}

	m := newHubModel(nil, "http://hub.test")
	m.tree = tree
	m.rows = rows
	got := m.dashboardView()
	for _, want := range []string{"serf", "live alpha", "live beta", "brainstorm", "brain live"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dashboard missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ended history") {
		t.Fatalf("dashboard rendered ended session:\n%s", got)
	}
}

func TestHubModelProjectViewShowsLiveThenRecent(t *testing.T) {
	project := hubapi.TreeProject{
		Key:         "serf",
		Name:        "serf",
		RollupState: "awaiting",
		Sessions: []hubapi.TreeNode{
			{Ref: "local:01ENDED", SessionID: "01ENDED", Title: "ended history", State: "ended", Project: "serf", Live: false},
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "awaiting", Project: "serf", Live: true},
		},
	}

	rows := buildProjectRows(project)
	if len(rows) != 2 {
		t.Fatalf("rows=%d: %+v", len(rows), rows)
	}
	if rows[0].title != "live task" || !rows[0].live {
		t.Fatalf("first row=%+v, want live task", rows[0])
	}
	if rows[1].title != "ended history" || rows[1].live {
		t.Fatalf("second row=%+v, want ended history", rows[1])
	}

	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeProject
	m.tree = hubapi.TreeResponse{Projects: []hubapi.TreeProject{project}}
	m.selectedProjectKey = "serf"
	m.projectRows = rows
	got := m.projectView()
	for _, want := range []string{"serf / project / serf", "Live now", "Recent in this project", "live task", "ended history"} {
		if !strings.Contains(got, want) {
			t.Fatalf("project view missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "live task") > strings.Index(got, "ended history") {
		t.Fatalf("project view rendered ended before live:\n%s", got)
	}
}

func TestHubModelDashboardProjectHeaderOpensProject(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubapi.TreeResponse{Projects: []hubapi.TreeProject{{
		Key:  "serf",
		Name: "serf",
		Sessions: []hubapi.TreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("project header enter should not fetch a session")
	}
	got := updated.(hubModel)
	if got.mode != hubModeProject || got.selectedProjectKey != "serf" {
		t.Fatalf("mode=%v project=%q", got.mode, got.selectedProjectKey)
	}
	if !strings.Contains(got.View(), "serf / project / serf") {
		t.Fatalf("project view not rendered:\n%s", got.View())
	}
}

func TestHubModelProjectEscReturnsDashboard(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubapi.TreeResponse{Projects: []hubapi.TreeProject{{
		Key:  "serf",
		Name: "serf",
		Sessions: []hubapi.TreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.openProject("serf")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("mode=%v, want dashboard", got.mode)
	}
	if !strings.Contains(got.View(), "serf live") {
		t.Fatalf("dashboard not rendered:\n%s", got.View())
	}
}

func TestHubModelSessionEscEntersBrowseInsteadOfDashboard(t *testing.T) {
	m := newSessionHubModel(nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(hubModel)
	if got.mode != hubModeSession {
		t.Fatalf("mode=%v, want session", got.mode)
	}
	if !got.session.scrollMode {
		t.Fatal("esc should enter browse focus")
	}
}

func TestHubModelCtrlOReturnsDashboardFromSession(t *testing.T) {
	m := newSessionHubModel(nil)
	m.rows = []hubRow{{kind: hubRowProject, project: "serf", projectKey: "serf"}}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	got := updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("mode=%v, want dashboard", got.mode)
	}
}

func TestHubModelSlashDashboardAndProjectNavigate(t *testing.T) {
	m := newSessionHubModel(nil)
	m.tree = hubapi.TreeResponse{Projects: []hubapi.TreeProject{{
		Key:  "serf",
		Name: "serf",
		Sessions: []hubapi.TreeNode{
			{Ref: "local:01SEND", SessionID: "01SEND", Title: "send task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.detail.Project = "serf"

	m.session.setInputValue("/dashboard")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/dashboard should not need an async command")
	}
	got := updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("/dashboard mode=%v", got.mode)
	}

	got.mode = hubModeSession
	got.session.setInputValue("/project")
	updated, cmd = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/project should not need an async command")
	}
	got = updated.(hubModel)
	if got.mode != hubModeProject || got.selectedProjectKey != "serf" {
		t.Fatalf("/project mode=%v project=%q", got.mode, got.selectedProjectKey)
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
	updated, _ = updated.(hubModel).Update(tea.KeyMsg{Type: tea.KeyDown})
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

func TestHubModelDashboardSpawnUsesSelectedProjectWorkingDir(t *testing.T) {
	var gotSpawn hubapi.SpawnRequest
	client, cleanup := newTestHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/spawn":
			if r.Method != http.MethodPost {
				t.Fatalf("spawn method=%s", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&gotSpawn); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, w, hubapi.SpawnResponse{Ref: "local:02NEW", HostID: "local", SessionID: "02NEW"})
		case "/api/sessions/local:02NEW":
			writeJSON(t, w, hubapi.SessionDetail{
				Ref: "local:02NEW", SessionID: "02NEW", Title: "new session", State: "idle", Live: true, WorkingDir: "/tmp/serf",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	m.tree = hubapi.TreeResponse{Projects: []hubapi.TreeProject{{
		Key: "serf", Name: "serf", WorkingDir: "/tmp/serf",
		Sessions: []hubapi.TreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cmd == nil {
		t.Fatal("dashboard spawn returned nil command")
	}
	updated, cmd = updated.(hubModel).Update(cmd())
	if cmd == nil {
		t.Fatal("spawn response did not fetch new session detail")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel)

	if gotSpawn.WorkingDir != "/tmp/serf" {
		t.Fatalf("working_dir=%q, want /tmp/serf", gotSpawn.WorkingDir)
	}
	if got.mode != hubModeSession || got.detail.SessionID != "02NEW" {
		t.Fatalf("mode=%v detail=%+v", got.mode, got.detail)
	}
}

func TestHubDashboardSpawnWaitsForSlowHubSpawn(t *testing.T) {
	var gotSpawn hubapi.SpawnRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			writeJSON(t, w, hubapi.HealthResponse{})
		case "/api/tree":
			writeJSON(t, w, hubapi.TreeResponse{Projects: []hubapi.TreeProject{{
				Key:        "serf",
				Name:       "serf",
				WorkingDir: "/tmp/serf",
				Sessions: []hubapi.TreeNode{
					{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
				},
			}}})
		case "/api/spawn":
			if r.Method != http.MethodPost {
				t.Fatalf("spawn method=%s", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&gotSpawn); err != nil {
				t.Fatal(err)
			}
			time.Sleep(1500 * time.Millisecond)
			writeJSON(t, w, hubapi.SpawnResponse{Ref: "local:02SLOW", HostID: "local", SessionID: "02SLOW"})
		case "/api/sessions/local:02SLOW":
			writeJSON(t, w, hubapi.SessionDetail{
				Ref: "local:02SLOW", SessionID: "02SLOW", Title: "spawned session", State: "idle", Live: true, WorkingDir: "/tmp/serf",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	runtime, err := startHubClient(context.Background(), hubStartConfig{
		RawAddr:   srv.URL,
		AutoStart: false,
	})
	if err != nil {
		t.Fatalf("start hub client: %v", err)
	}
	model := newHubModel(runtime.Client, runtime.Address.BaseURL)
	updated, cmd := model.Update(model.Init()())
	if cmd != nil {
		t.Fatal("initial tree load returned unexpected command")
	}
	model = updated.(hubModel)

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cmd == nil {
		t.Fatal("dashboard spawn returned nil command")
	}
	model = updated.(hubModel)

	updated, cmd = model.Update(cmd())
	model = updated.(hubModel)
	if model.err != nil {
		t.Fatalf("spawn failed: %v", model.err)
	}
	if cmd == nil {
		t.Fatal("spawn response did not fetch new session detail")
	}

	updated, cmd = model.Update(cmd())
	if cmd != nil {
		t.Fatal("session detail returned unexpected command")
	}
	model = updated.(hubModel)
	if model.mode != hubModeSession || model.detail.SessionID != "02SLOW" {
		t.Fatalf("mode=%v detail=%+v", model.mode, model.detail)
	}
	if gotSpawn.WorkingDir != "/tmp/serf" {
		t.Fatalf("working_dir=%q, want /tmp/serf", gotSpawn.WorkingDir)
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

func TestHubModelBrowseForkDraftPostsForkAndNavigatesToChild(t *testing.T) {
	var gotPath string
	var gotReq hubapi.ForkRequest
	client, cleanup := newTestHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		switch r.URL.Path {
		case "/api/sessions/local:01SEND/fork":
			if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, w, hubapi.RefResponse{Ref: "local:02CHILD", HostID: "local", SessionID: "02CHILD"})
		case "/api/sessions/local:02CHILD":
			writeJSON(t, w, hubapi.SessionDetail{Ref: "local:02CHILD", SessionID: "02CHILD", Title: "child"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.Capabilities.Fork = true
	m.session.messages = []chatMessage{
		{Kind: msgUser, Text: "original request", TurnIndex: 3},
		{Kind: msgAssistant, Text: "answer"},
	}
	m.session.scrollMode = true
	m.browseSelected = 0

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if cmd != nil {
		t.Fatal("starting a fork draft should be synchronous")
	}
	draft := updated.(hubModel)
	if draft.forkDraft == nil || draft.forkDraft.Turn != 3 {
		t.Fatalf("fork draft=%+v", draft.forkDraft)
	}
	if draft.session.scrollMode {
		t.Fatal("fork draft should return to compose focus")
	}
	if draft.session.input.Value() != "original request" {
		t.Fatalf("draft input=%q", draft.session.input.Value())
	}

	draft.session.setInputValue("edited request")
	updated, cmd = draft.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("confirming fork draft should post to hub")
	}
	updated, cmd = updated.(hubModel).Update(cmd())
	if cmd == nil {
		t.Fatal("successful fork should fetch child session")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel)
	if got.detail.SessionID != "02CHILD" {
		t.Fatalf("detail=%+v", got.detail)
	}
	if gotPath != "/api/sessions/local:02CHILD" {
		t.Fatalf("last path=%q", gotPath)
	}
	if gotReq.Turn != 3 || gotReq.EditedMessage != "edited request" || gotReq.Label != "original before fork" {
		t.Fatalf("fork request=%+v", gotReq)
	}
}

func TestHubModelBrowseForkRequiresUserTurnWithTurnIndex(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities.Fork = true
	m.session.messages = []chatMessage{{Kind: msgUser, Text: "not persisted"}}
	m.session.scrollMode = true
	m.browseSelected = 0

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	got := updated.(hubModel)
	if got.forkDraft != nil {
		t.Fatalf("fork draft=%+v, want nil", got.forkDraft)
	}
	view := got.View()
	if !strings.Contains(view, "fork requires persisted transcript") {
		t.Fatalf("missing fork reason:\n%s", view)
	}
}

func TestHubModelDashboardEmptyStateIsLiveOnly(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubapi.TreeResponse{Projects: []hubapi.TreeProject{{
		Key:  "serf",
		Name: "serf",
		Sessions: []hubapi.TreeNode{
			{Ref: "local:01ENDED", SessionID: "01ENDED", Title: "ended history", State: "ended", Project: "serf"},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)

	got := m.dashboardView()
	for _, want := range []string{"No live sessions are running", "s start a session", "/projects browse project history"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dashboard empty state missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ended history") {
		t.Fatalf("dashboard empty state rendered ended session:\n%s", got)
	}
}

func TestHubModelSessionFooterShowsBrowseAndDashboardKeys(t *testing.T) {
	m := newSessionHubModel(nil)
	got := m.sessionView()
	for _, want := range []string{"esc: browse", "ctrl+o: dashboard"} {
		if !strings.Contains(got, want) {
			t.Fatalf("session footer missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "esc: dashboard") {
		t.Fatalf("session footer still advertises esc dashboard:\n%s", got)
	}

	m.enterSessionBrowse(false)
	got = m.sessionView()
	for _, want := range []string{"esc/i/q: compose", "f: fork", "ctrl+o: dashboard"} {
		if !strings.Contains(got, want) {
			t.Fatalf("browse footer missing %q:\n%s", want, got)
		}
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
