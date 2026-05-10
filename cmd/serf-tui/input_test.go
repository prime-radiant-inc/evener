package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendCompact_Success(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/compact" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	// Strip "http://" prefix to get addr.
	addr := ts.URL[len("http://"):]
	cmd := sendCompact(addr)
	msg := cmd()

	result, ok := msg.(compactDoneMsg)
	if !ok {
		t.Fatalf("expected compactDoneMsg, got %T", msg)
	}
	if result.err != nil {
		t.Errorf("unexpected error: %v", result.err)
	}
	if !called {
		t.Error("server handler not called")
	}
}

func TestSendCompact_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	addr := ts.URL[len("http://"):]
	cmd := sendCompact(addr)
	msg := cmd()

	result, ok := msg.(compactDoneMsg)
	if !ok {
		t.Fatalf("expected compactDoneMsg, got %T", msg)
	}
	if result.err == nil {
		t.Error("expected error, got nil")
	}
}

func TestSendClear_Success(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/clear" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	addr := ts.URL[len("http://"):]
	cmd := sendClear(addr)
	msg := cmd()

	result, ok := msg.(clearDoneMsg)
	if !ok {
		t.Fatalf("expected clearDoneMsg, got %T", msg)
	}
	if result.err != nil {
		t.Errorf("unexpected error: %v", result.err)
	}
	if !called {
		t.Error("server handler not called")
	}
}

func TestSendModel_Success(t *testing.T) {
	var gotModel string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req struct{ Model string }
		json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	addr := ts.URL[len("http://"):]
	cmd := sendModel(addr, "gpt-4o-mini")
	msg := cmd()

	result, ok := msg.(modelDoneMsg)
	if !ok {
		t.Fatalf("expected modelDoneMsg, got %T", msg)
	}
	if result.err != nil {
		t.Errorf("unexpected error: %v", result.err)
	}
	if gotModel != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", gotModel)
	}
}

func TestFetchStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"session_id":"abc","state":"IDLE","turns":5,"model":"gpt-4o","profile":"openai","context_pressure":0.35}`))
	}))
	defer ts.Close()

	addr := ts.URL[len("http://"):]
	cmd := fetchStatus(addr)
	msg := cmd()

	result, ok := msg.(statusResult)
	if !ok {
		t.Fatalf("expected statusResult, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected error: %v", result.err)
	}
	if result.info.SessionID != "abc" {
		t.Errorf("session_id = %q, want abc", result.info.SessionID)
	}
	if result.info.ContextPressure != 0.35 {
		t.Errorf("context_pressure = %f, want 0.35", result.info.ContextPressure)
	}
}

func TestFetchStatus_WithDetailedFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"session_id":"xyz","state":"IDLE","turns":10,"model":"gpt-5","profile":"openai",
			"context_pressure":0.42,
			"detailed":{
				"tools":[{"name":"shell","source":"core"},{"name":"linear__search","source":"mcp:streamlinear"}],
				"mcp":[{"name":"streamlinear","tools":["linear__search"]}],
				"skills":[{"name":"brainstorming","description":"brainstorm"}],
				"plugins":[{"name":"superpowers","version":"4.3.0","skill_count":8,"agent_count":0,"hook_count":12,"mcp_count":0}],
				"hooks":{"PreToolUse":3,"SessionStart":1},
				"agents":["superpowers:code-reviewer"]
			}
		}`))
	}))
	defer ts.Close()

	addr := ts.URL[len("http://"):]
	cmd := fetchStatus(addr)
	msg := cmd()

	result, ok := msg.(statusResult)
	if !ok {
		t.Fatalf("expected statusResult, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected error: %v", result.err)
	}
	if result.info.Detailed == nil {
		t.Fatal("expected detailed status")
	}
	if len(result.info.Detailed.Tools) != 2 {
		t.Errorf("tools: got %d, want 2", len(result.info.Detailed.Tools))
	}
	if len(result.info.Detailed.MCP) != 1 {
		t.Errorf("mcp: got %d, want 1", len(result.info.Detailed.MCP))
	}
	if len(result.info.Detailed.Plugins) != 1 {
		t.Errorf("plugins: got %d, want 1", len(result.info.Detailed.Plugins))
	}
	if result.info.Detailed.Hooks["PreToolUse"] != 3 {
		t.Errorf("hooks PreToolUse: got %d, want 3", result.info.Detailed.Hooks["PreToolUse"])
	}
}

func TestFetchTranscriptTargets(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"session_id":"root-123",
			"state":"IDLE",
			"turns":5,
			"model":"gpt-5",
			"profile":"openai",
			"context_pressure":0.12,
			"detailed":{
				"subagents":[
					{"id":"sub-1","status":"running","turns_used":2}
				]
			}
		}`))
	}))
	defer ts.Close()

	addr := ts.URL[len("http://"):]
	cmd := fetchTranscriptTargets(addr)
	msg := cmd()

	result, ok := msg.(transcriptTargetsResult)
	if !ok {
		t.Fatalf("expected transcriptTargetsResult, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected error: %v", result.err)
	}
	if result.info.SessionID != "root-123" {
		t.Errorf("session_id = %q, want root-123", result.info.SessionID)
	}
	if result.info.Detailed == nil || len(result.info.Detailed.Subagents) != 1 {
		t.Fatalf("expected 1 subagent in detailed status, got %+v", result.info.Detailed)
	}
	if result.info.Detailed.Subagents[0].ID != "sub-1" {
		t.Errorf("subagent id = %q, want sub-1", result.info.Detailed.Subagents[0].ID)
	}
}

func TestSlashCommandHelp(t *testing.T) {
	help := slashCommandHelp()
	for _, cmd := range []string{"/help", "/compact", "/status", "/agents", "/model", "/auth", "/theme", "/clear", "/dashboard", "/project", "/quit"} {
		if !strings.Contains(help, cmd) {
			t.Errorf("help text missing %q", cmd)
		}
	}
}

func TestSlashCommandHelpMentionsDashboardProjectAndBrowse(t *testing.T) {
	help := slashCommandHelp()
	for _, want := range []string{"  /dashboard Go to live dashboard", "  /project   Go to this session's project", "  esc              Browse transcript / select turns"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help text missing %q:\n%s", want, help)
		}
	}
}

func TestParseSlashCommand(t *testing.T) {
	tests := []struct {
		input   string
		wantCmd string
		wantArg string
	}{
		{"/compact", "compact", ""},
		{" /compact ", "compact", ""},
		{"/compact extra args", "compact", "extra args"},
		{"/quit", "quit", ""},
		{"/help", "help", ""},
		{"/model gpt-4o", "model", "gpt-4o"},
		{"/clear", "clear", ""},
		{"/status", "status", ""},
		{"hello /compact", "", ""},
		{"", "", ""},
		{"no slash", "", ""},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.input), func(t *testing.T) {
			cmd, args := parseSlashCommand(tt.input)
			if cmd != tt.wantCmd {
				t.Errorf("parseSlashCommand(%q) cmd = %q, want %q", tt.input, cmd, tt.wantCmd)
			}
			if args != tt.wantArg {
				t.Errorf("parseSlashCommand(%q) args = %q, want %q", tt.input, args, tt.wantArg)
			}
		})
	}
}
