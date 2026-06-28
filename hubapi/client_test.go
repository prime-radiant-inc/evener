package hubapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/hubapi"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*hubapi.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	client, err := hubapi.NewClient(srv.URL, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, srv
}

func TestClientURLPreservesQueryString(t *testing.T) {
	client, err := hubapi.NewClient("http://127.0.0.1:9180", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := client.URL("/api/sessions/local:01ABC?include=details")
	want := "http://127.0.0.1:9180/api/sessions/local:01ABC?include=details"
	if got != want {
		t.Fatalf("URL()=%q, want %q", got, want)
	}
}

func TestClientHealth(t *testing.T) {
	want := hubapi.HealthResponse{
		Version:   "1.0.0",
		StartedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		HubAddr:   "127.0.0.1:9180",
		Capabilities: hubapi.HealthCapabilities{
			Tree:  true,
			Spawn: true,
		},
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method: got %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/health" {
			t.Errorf("path: got %s, want /api/health", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got.Version != want.Version {
		t.Errorf("version: got %q, want %q", got.Version, want.Version)
	}
	if got.HubAddr != want.HubAddr {
		t.Errorf("hub_addr: got %q, want %q", got.HubAddr, want.HubAddr)
	}
	if !got.Capabilities.Tree {
		t.Error("expected Tree capability")
	}
	if !got.Capabilities.Spawn {
		t.Error("expected Spawn capability")
	}
}

func TestClientHealth_Error(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	})
	defer srv.Close()

	_, err := client.Health(context.Background())
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}

func TestClientTree(t *testing.T) {
	want := hubapi.TreeResponse{
		Projects: []hubapi.TreeProject{
			{Key: "proj1", Name: "Project 1"},
		},
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method: got %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/tree" {
			t.Errorf("path: got %s, want /api/tree", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.Tree(context.Background())
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(got.Projects) != 1 || got.Projects[0].Key != "proj1" {
		t.Errorf("projects: got %+v", got.Projects)
	}
}

func TestClientTree_Error(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	_, err := client.Tree(context.Background())
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestClientSession(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	want := hubapi.SessionDetail{
		Ref:   "local:test",
		Title: "Test Session",
		State: "idle",
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method: got %s, want GET", r.Method)
		}
		wantPath := "/api/sessions/" + ref.PathEscaped()
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.Session(context.Background(), ref)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if got.Ref != want.Ref {
		t.Errorf("ref: got %q, want %q", got.Ref, want.Ref)
	}
	if got.Title != want.Title {
		t.Errorf("title: got %q, want %q", got.Title, want.Title)
	}
	if got.State != want.State {
		t.Errorf("state: got %q, want %q", got.State, want.State)
	}
}

func TestClientSession_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()

	_, err := client.Session(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestClientSpawnSchema(t *testing.T) {
	want := hubapi.SpawnSchema{
		Fields: []hubapi.SpawnField{
			{Name: "prompt", Type: "string", Required: true},
		},
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method: got %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/spawn-schema" {
			t.Errorf("path: got %s, want /api/spawn-schema", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.SpawnSchema(context.Background())
	if err != nil {
		t.Fatalf("SpawnSchema: %v", err)
	}
	if len(got.Fields) != 1 || got.Fields[0].Name != "prompt" {
		t.Errorf("fields: got %+v", got.Fields)
	}
}

func TestClientSpawnSchema_Error(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer srv.Close()

	_, err := client.SpawnSchema(context.Background())
	if err == nil {
		t.Fatal("expected error for 503 response")
	}
}

func TestClientSpawn(t *testing.T) {
	want := hubapi.SpawnResponse{
		Ref:       "local:abc123",
		HostID:    "local",
		SessionID: "abc123",
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/spawn" {
			t.Errorf("path: got %s, want /api/spawn", r.URL.Path)
		}
		var req hubapi.SpawnRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if req.Prompt != "do something" {
			t.Errorf("prompt: got %q, want %q", req.Prompt, "do something")
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	req := hubapi.SpawnRequest{Prompt: "do something"}
	got, err := client.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got.Ref != want.Ref {
		t.Errorf("ref: got %q, want %q", got.Ref, want.Ref)
	}
	if got.HostID != want.HostID {
		t.Errorf("host_id: got %q, want %q", got.HostID, want.HostID)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("session_id: got %q, want %q", got.SessionID, want.SessionID)
	}
}

func TestClientSpawn_Error(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	_, err := client.Spawn(context.Background(), hubapi.SpawnRequest{})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestClientModels(t *testing.T) {
	want := []hubapi.ModelOption{
		{Provider: "openai", Model: "gpt-4o"},
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method: got %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/models" {
			t.Errorf("path: got %s, want /api/models", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(got) != 1 || got[0].Model != "gpt-4o" {
		t.Errorf("models: got %+v", got)
	}
}

func TestClientModels_Error(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	defer srv.Close()

	_, err := client.Models(context.Background())
	if err == nil {
		t.Fatal("expected error for 502 response")
	}
}

func TestClientSend(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		wantPath := "/api/sessions/" + ref.PathEscaped() + "/send"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["text"] != "hello" {
			t.Errorf("text: got %q, want %q", body["text"], "hello")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	err := client.Send(context.Background(), ref, "hello")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestClientSend_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	defer srv.Close()

	err := client.Send(context.Background(), ref, "hello")
	if err == nil {
		t.Fatal("expected error for 409 response")
	}
}

func TestClientTasks(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	want := []task.Task{
		{ID: 1, Description: "task one", Status: task.TaskOpen},
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method: got %s, want GET", r.Method)
		}
		wantPath := "/api/sessions/" + ref.PathEscaped() + "/tasks"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.Tasks(context.Background(), ref)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("tasks: got %+v", got)
	}
}

func TestClientTasks_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()

	_, err := client.Tasks(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestClientInterrupt(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		wantPath := "/api/sessions/" + ref.PathEscaped() + "/interrupt"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	err := client.Interrupt(context.Background(), ref)
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
}

func TestClientInterrupt_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer srv.Close()

	err := client.Interrupt(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error for 503 response")
	}
}

func TestClientCompact(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		wantPath := "/api/sessions/" + ref.PathEscaped() + "/compact"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	err := client.Compact(context.Background(), ref)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
}

func TestClientCompact_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	err := client.Compact(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestClientClear(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	want := hubapi.RefResponse{
		Ref:       "local:test",
		HostID:    "local",
		SessionID: "test",
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		wantPath := "/api/sessions/" + ref.PathEscaped() + "/clear"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.Clear(context.Background(), ref)
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if got.Ref != want.Ref {
		t.Errorf("ref: got %q, want %q", got.Ref, want.Ref)
	}
	if got.HostID != want.HostID {
		t.Errorf("host_id: got %q, want %q", got.HostID, want.HostID)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("session_id: got %q, want %q", got.SessionID, want.SessionID)
	}
}

func TestClientClear_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	defer srv.Close()

	_, err := client.Clear(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error for 409 response")
	}
}

func TestClientFork(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	want := hubapi.RefResponse{
		Ref:       "local:fork123",
		HostID:    "local",
		SessionID: "fork123",
	}
	req := hubapi.ForkRequest{Turn: 5, EditedMessage: "edited", Label: "fork-label"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		wantPath := "/api/sessions/" + ref.PathEscaped() + "/fork"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		var gotReq hubapi.ForkRequest
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if gotReq.Turn != req.Turn {
			t.Errorf("turn: got %d, want %d", gotReq.Turn, req.Turn)
		}
		if gotReq.EditedMessage != req.EditedMessage {
			t.Errorf("edited_message: got %q, want %q", gotReq.EditedMessage, req.EditedMessage)
		}
		if gotReq.Label != req.Label {
			t.Errorf("label: got %q, want %q", gotReq.Label, req.Label)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.Fork(context.Background(), ref, req)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if got.Ref != want.Ref {
		t.Errorf("ref: got %q, want %q", got.Ref, want.Ref)
	}
	if got.HostID != want.HostID {
		t.Errorf("host_id: got %q, want %q", got.HostID, want.HostID)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("session_id: got %q, want %q", got.SessionID, want.SessionID)
	}
}

func TestClientFork_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	defer srv.Close()

	_, err := client.Fork(context.Background(), ref, hubapi.ForkRequest{})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}

func TestClientSetModel(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		wantPath := "/api/sessions/" + ref.PathEscaped() + "/model"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["model"] != "gpt-4o" {
			t.Errorf("model: got %q, want %q", body["model"], "gpt-4o")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	err := client.SetModel(context.Background(), ref, "gpt-4o")
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
}

func TestClientSetModel_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	defer srv.Close()

	err := client.SetModel(context.Background(), ref, "gpt-4o")
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}
