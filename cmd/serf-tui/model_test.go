package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/server"
)

func TestRenderDetailedStatus_BasicFields(t *testing.T) {
	info := server.StatusInfo{
		SessionID:       "abc123",
		Model:           "gpt-5",
		Profile:         "openai",
		Turns:           12,
		ContextPressure: 0.42,
	}
	out := renderDetailedStatus(info, 80)

	for _, want := range []string{"Session:  abc123", "Model:    gpt-5 (openai)", "Turns:    12", "Context:  42% used"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestRenderDetailedStatus_ToolCategories(t *testing.T) {
	info := server.StatusInfo{
		SessionID: "s1",
		Model:     "m",
		Profile:   "p",
		Detailed: &server.DetailedStatus{
			Tools: []server.ToolInfo{
				{Name: "shell", Source: "core"},
				{Name: "read_file", Source: "core"},
				{Name: "linear__search", Source: "mcp:streamlinear"},
				{Name: "my_tool", Source: "custom"},
			},
		},
	}
	out := renderDetailedStatus(info, 80)

	if !strings.Contains(out, "Tools (4):") {
		t.Errorf("missing tools header in:\n%s", out)
	}
	if !strings.Contains(out, "Core: shell, read_file") {
		t.Errorf("missing core tools in:\n%s", out)
	}
	if !strings.Contains(out, "MCP [streamlinear]:") || !strings.Contains(out, "linear__search") {
		t.Errorf("missing MCP tools in:\n%s", out)
	}
	if !strings.Contains(out, "Custom: my_tool") {
		t.Errorf("missing custom tools in:\n%s", out)
	}
}

func TestRenderDetailedStatus_Plugins(t *testing.T) {
	info := server.StatusInfo{
		SessionID: "s1",
		Model:     "m",
		Profile:   "p",
		Detailed: &server.DetailedStatus{
			Plugins: []server.PluginStatusInfo{
				{Name: "superpowers", Version: "4.3.0", SkillCount: 8, AgentCount: 2, HookCount: 12},
			},
		},
	}
	out := renderDetailedStatus(info, 80)

	if !strings.Contains(out, "Plugins (1):") {
		t.Errorf("missing plugins header in:\n%s", out)
	}
	if !strings.Contains(out, "superpowers v4.3.0 (8 skills, 2 agents, 12 hooks)") {
		t.Errorf("missing plugin details in:\n%s", out)
	}
}

func TestRenderDetailedStatus_ShowsEmptySections(t *testing.T) {
	info := server.StatusInfo{
		SessionID: "s1",
		Model:     "m",
		Profile:   "p",
		Detailed:  &server.DetailedStatus{},
	}
	out := renderDetailedStatus(info, 80)

	for _, section := range []string{"Tools (0)", "MCP Servers (0)", "Skills (0)", "Plugins (0)", "Hooks (0)", "Subagents (0)", "Agents (0)"} {
		if !strings.Contains(out, section) {
			t.Errorf("missing %q in output:\n%s", section, out)
		}
	}
}

func TestRenderDetailedStatus_NilDetailed(t *testing.T) {
	info := server.StatusInfo{
		SessionID:       "s1",
		Model:           "m",
		Profile:         "p",
		Turns:           5,
		ContextPressure: 0.1,
	}
	out := renderDetailedStatus(info, 80)

	// Should still show basic info.
	if !strings.Contains(out, "Session:  s1") {
		t.Errorf("missing session in:\n%s", out)
	}
	// Should not have any detailed sections (no Detailed struct at all).
	if strings.Contains(out, "Tools") {
		t.Errorf("should not contain Tools when Detailed is nil:\n%s", out)
	}
}

func TestRenderDetailedStatus_Hooks(t *testing.T) {
	info := server.StatusInfo{
		SessionID: "s1",
		Model:     "m",
		Profile:   "p",
		Detailed: &server.DetailedStatus{
			Hooks: map[string]int{
				"PreToolUse":   3,
				"SessionStart": 1,
			},
		},
	}
	out := renderDetailedStatus(info, 80)

	if !strings.Contains(out, "Hooks (2):") {
		t.Errorf("missing hooks header in:\n%s", out)
	}
	if !strings.Contains(out, "PreToolUse: 3") {
		t.Errorf("missing PreToolUse count in:\n%s", out)
	}
	if !strings.Contains(out, "SessionStart: 1") {
		t.Errorf("missing SessionStart count in:\n%s", out)
	}
}

func TestRenderDetailedStatus_ToolsWrapAtWidth(t *testing.T) {
	// Create enough tools that they'd exceed a narrow width.
	tools := []server.ToolInfo{
		{Name: "shell", Source: "core"},
		{Name: "read_file", Source: "core"},
		{Name: "write_file", Source: "core"},
		{Name: "edit_file", Source: "core"},
		{Name: "glob", Source: "core"},
		{Name: "grep", Source: "core"},
		{Name: "web_fetch", Source: "core"},
	}
	info := server.StatusInfo{
		SessionID: "s1",
		Model:     "m",
		Profile:   "p",
		Detailed:  &server.DetailedStatus{Tools: tools},
	}
	// Width 40 means "  Core: " (8 chars) + tools must wrap.
	out := renderDetailedStatus(info, 40)

	// The core tools line should be split across multiple lines.
	lines := strings.Split(out, "\n")
	coreLines := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Count lines that are part of the core tools listing.
		if strings.HasPrefix(trimmed, "Core:") || (coreLines > 0 && !strings.HasPrefix(trimmed, "MCP") && !strings.HasPrefix(trimmed, "Custom") && !strings.Contains(trimmed, ":") && trimmed != "" && !strings.HasPrefix(trimmed, "Tools") && !strings.HasPrefix(trimmed, "MCP Servers") && !strings.HasPrefix(trimmed, "Skills") && !strings.HasPrefix(trimmed, "Plugins") && !strings.HasPrefix(trimmed, "Hooks") && !strings.HasPrefix(trimmed, "Subagents") && !strings.HasPrefix(trimmed, "Agents")) {
			coreLines++
		} else if coreLines > 0 {
			break
		}
	}
	if coreLines < 2 {
		t.Errorf("expected core tools to wrap across multiple lines at width 40, got %d lines:\n%s", coreLines, out)
	}
	// No line should exceed the width.
	for i, line := range lines {
		if len(line) > 40 {
			t.Errorf("line %d exceeds width 40 (%d chars): %q", i, len(line), line)
		}
	}
}

func TestTUIEnterSubmitsInput(t *testing.T) {
	initTheme()

	inputCh := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/input":
			var req struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode /input body: %v", err)
			}
			inputCh <- req.Text
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && r.URL.Path == "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	addr := strings.TrimPrefix(ts.URL, "http://")
	asyncCh := make(chan tea.Msg, 8)
	m := newConfiguredModel(addr, t.TempDir(), nil, embeddedConfig{
		provider: "openai",
		model:    "gpt-5.5",
	}, nil, asyncCh, false)

	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(io.Discard))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.Run()
	}()
	defer func() {
		p.Quit()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for Bubble Tea program to stop")
		}
	}()

	p.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	p.Send(tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case got := <-inputCh:
		if got != "hi" {
			t.Fatalf("/input text = %q, want %q", got, "hi")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for /input request")
	}
}

func TestUpdateAsyncSSEConnectedKeepsWaitingForAsync(t *testing.T) {
	initTheme()

	asyncCh := make(chan tea.Msg, 64)
	m := newConfiguredModel("127.0.0.1:1234", t.TempDir(), nil, embeddedConfig{}, nil, asyncCh, false)
	m.streamID = 1

	asyncCh <- sseEventMsg{
		streamID: 1,
		event: SSEEvent{
			Event: "COMMUNICATE",
			Data:  `{"message":"Hi. What can I help with?"}`,
		},
	}
	asyncCh <- sseEventMsg{
		streamID: 1,
		event: SSEEvent{
			Event: "SESSION_END",
			Data:  `{"reason":"input_complete","state":"IDLE","turns":1}`,
		},
	}

	updated, cmd := m.Update(asyncMsg{msg: sseConnectedMsg{streamID: 1}})
	if cmd == nil {
		t.Fatal("Update() returned nil cmd; want waitForAsync to remain armed")
	}

	next := cmd()
	wrapped, ok := next.(asyncMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want asyncMsg", next)
	}
	ev, ok := wrapped.msg.(sseEventMsg)
	if !ok {
		t.Fatalf("wrapped.msg = %T, want sseEventMsg", wrapped.msg)
	}
	if ev.event.Event != "COMMUNICATE" {
		t.Fatalf("event type = %q, want COMMUNICATE", ev.event.Event)
	}

	m2 := updated.(model)
	if !m2.connected {
		t.Fatal("connected = false, want true")
	}

	updated, cmd = m2.Update(next)
	if cmd == nil {
		t.Fatal("Update() returned nil cmd after COMMUNICATE; want waitForAsync to remain armed")
	}
	m3 := updated.(model)
	if len(m3.messages) != 1 || m3.messages[0].Text != "Hi. What can I help with?" {
		t.Fatalf("messages = %+v, want communicate reply", m3.messages)
	}

	next = cmd()
	wrapped, ok = next.(asyncMsg)
	if !ok {
		t.Fatalf("second cmd() returned %T, want asyncMsg", next)
	}
	ev, ok = wrapped.msg.(sseEventMsg)
	if !ok {
		t.Fatalf("second wrapped.msg = %T, want sseEventMsg", wrapped.msg)
	}
	if ev.event.Event != "SESSION_END" {
		t.Fatalf("second event type = %q, want SESSION_END", ev.event.Event)
	}
}
