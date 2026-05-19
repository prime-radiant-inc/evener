package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
)

// ---------------------------------------------------------------------------
// renderToolCall: focused parameter changes arrow character
// ---------------------------------------------------------------------------

func TestRenderToolCall_Collapsed(t *testing.T) {
	tc := toolCallInfo{
		Name:        "shell",
		Description: "list files",
		Expanded:    false,
		Done:        true,
		Duration:    1 * time.Second,
	}
	out := renderToolCall(tc, 80, false)

	// Collapsed tool uses ▸ arrow
	if !strings.Contains(out, "▸") {
		t.Errorf("collapsed tool should show ▸, got: %s", out)
	}
	// Duration should be shown
	if !strings.Contains(out, "[1.0s]") {
		t.Errorf("collapsed tool should show duration, got: %s", out)
	}
	// Name should be present
	if !strings.Contains(out, "shell") {
		t.Errorf("collapsed tool should show name, got: %s", out)
	}
}

func TestRenderToolCall_Expanded(t *testing.T) {
	tc := toolCallInfo{
		Name:        "shell",
		Description: "list files",
		Detail:      "full args here",
		Output:      "file1.go\nfile2.go",
		Expanded:    true,
		Done:        true,
		Duration:    500 * time.Millisecond,
	}
	out := renderToolCall(tc, 80, false)

	// Expanded tool uses ▾ arrow
	if !strings.Contains(out, "▾") {
		t.Errorf("expanded tool should show ▾, got: %s", out)
	}
	// Detail and output should be visible
	if !strings.Contains(out, "full args here") {
		t.Errorf("expanded tool should show detail, got: %s", out)
	}
	if !strings.Contains(out, "file1.go") {
		t.Errorf("expanded tool should show output, got: %s", out)
	}
}

func TestRenderToolCall_FocusedChangesArrow(t *testing.T) {
	tc := toolCallInfo{
		Name:        "shell",
		Description: "list files",
		Expanded:    false,
		Done:        true,
		Duration:    1 * time.Second,
	}

	// Unfocused: uses ▸ or ▾ depending on expanded state
	unfocused := renderToolCall(tc, 80, false)

	// Focused: uses ▶ regardless of expanded state
	tc.Expanded = false
	focusedCollapsed := renderToolCall(tc, 80, true)
	if !strings.Contains(focusedCollapsed, "▶") {
		t.Errorf("focused collapsed tool should show ▶, got: %s", focusedCollapsed)
	}

	tc.Expanded = true
	focusedExpanded := renderToolCall(tc, 80, true)
	if !strings.Contains(focusedExpanded, "▶") {
		t.Errorf("focused expanded tool should show ▶, got: %s", focusedExpanded)
	}

	// Unfocused and focused must differ (different arrow)
	if unfocused == focusedCollapsed {
		t.Error("unfocused and focused output should differ")
	}
}

func TestRenderToolCall_DoneVsInProgress(t *testing.T) {
	done := toolCallInfo{Name: "x", Done: true, Duration: 1 * time.Second}
	pending := toolCallInfo{Name: "x", Done: false}

	doneOut := renderToolCall(done, 80, false)
	pendingOut := renderToolCall(pending, 80, false)

	// Done shows duration
	if !strings.Contains(doneOut, "[1.0s]") {
		t.Errorf("done tool should show duration: %s", doneOut)
	}
	// Pending shows "..."
	if !strings.Contains(pendingOut, "...") {
		t.Errorf("pending tool should show ...: %s", pendingOut)
	}
}

// ---------------------------------------------------------------------------
// renderTasks
// ---------------------------------------------------------------------------

func TestRenderTasks_Empty(t *testing.T) {
	out := renderTasks(nil, 80)
	if out != "No tasks." {
		t.Errorf("empty tasks = %q, want %q", out, "No tasks.")
	}

	out2 := renderTasks([]agent.Task{}, 80)
	if out2 != "No tasks." {
		t.Errorf("empty slice = %q, want %q", out2, "No tasks.")
	}
}

func TestRenderTasks_SingleTask(t *testing.T) {
	tasks := []agent.Task{
		{ID: 1, Type: agent.TaskTypeImplement, Description: "add login handler", Status: agent.TaskDone},
	}
	out := renderTasks(tasks, 80)

	if !strings.Contains(out, "Tasks (1):") {
		t.Errorf("missing header in: %s", out)
	}
	if !strings.Contains(out, "●") {
		t.Errorf("done task should show ● icon, got: %s", out)
	}
	if !strings.Contains(out, "[1]") {
		t.Errorf("task should show ID, got: %s", out)
	}
	if !strings.Contains(out, "implement") {
		t.Errorf("task should show type, got: %s", out)
	}
	if !strings.Contains(out, "add login handler") {
		t.Errorf("task should show description, got: %s", out)
	}
}

func TestRenderTasks_AllStatuses(t *testing.T) {
	tasks := []agent.Task{
		{ID: 1, Type: agent.TaskTypeResearch, Description: "research", Status: agent.TaskOpen},
		{ID: 2, Type: agent.TaskTypeImplement, Description: "implement", Status: agent.TaskInProgress},
		{ID: 3, Type: agent.TaskTypeVerify, Description: "verify", Status: agent.TaskDone},
		{ID: 4, Type: agent.TaskTypeFix, Description: "fix", Status: agent.TaskCancelled},
	}
	out := renderTasks(tasks, 80)

	if !strings.Contains(out, "○") {
		t.Errorf("open task should show ○, got: %s", out)
	}
	if !strings.Contains(out, "◐") {
		t.Errorf("in-progress task should show ◐, got: %s", out)
	}
	if !strings.Contains(out, "●") {
		t.Errorf("done task should show ●, got: %s", out)
	}
	if !strings.Contains(out, "✗") {
		t.Errorf("cancelled task should show ✗, got: %s", out)
	}
}

func TestRenderTasks_Dependencies(t *testing.T) {
	tasks := []agent.Task{
		{ID: 2, Type: agent.TaskTypeImplement, Description: "step 2", Status: agent.TaskOpen, DependsOn: []int{1}},
		{ID: 3, Type: agent.TaskTypeVerify, Description: "step 3", Status: agent.TaskOpen, DependsOn: []int{1, 2}},
	}
	out := renderTasks(tasks, 80)

	if !strings.Contains(out, "depends on: [1]") {
		t.Errorf("should show depends on [1], got: %s", out)
	}
	if !strings.Contains(out, "depends on: [1 2]") {
		t.Errorf("should show depends on [1 2], got: %s", out)
	}
}

func TestRenderTasks_ReasoningEffort(t *testing.T) {
	tasks := []agent.Task{
		{ID: 1, Type: agent.TaskTypeResearch, Description: "hard problem", Status: agent.TaskInProgress, ReasoningEffort: "high"},
	}
	out := renderTasks(tasks, 80)

	if !strings.Contains(out, "[high]") {
		t.Errorf("should show reasoning effort [high], got: %s", out)
	}
}

func TestRenderTasks_WidthMinimum(t *testing.T) {
	// renderTasks should handle width <= 0 gracefully by using default
	tasks := []agent.Task{{ID: 1, Type: agent.TaskTypeImplement, Description: "x", Status: agent.TaskOpen}}
	out := renderTasks(tasks, 0)
	if !strings.Contains(out, "Tasks (1)") {
		t.Errorf("should not crash on width=0, got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// /tasks slash command
// ---------------------------------------------------------------------------

func TestSlashCommandHelp_Tasks(t *testing.T) {
	help := slashCommandHelp()
	if !strings.Contains(help, "/tasks") {
		t.Errorf("help text should mention /tasks, got: %s", help)
	}
}

func TestParseSlashCommand_Tasks(t *testing.T) {
	cmd, args := parseSlashCommand("/tasks")
	if cmd != "tasks" {
		t.Errorf("cmd = %q, want %q", cmd, "tasks")
	}
	if args != "" {
		t.Errorf("args = %q, want %q", args, "")
	}

	// With args (should still parse but not be used by /tasks handler)
	cmd2, args2 := parseSlashCommand("/tasks extra")
	if cmd2 != "tasks" {
		t.Errorf("cmd = %q, want %q", cmd2, "tasks")
	}
	// Args are kept for the handler to decide what to do with
	if args2 != "extra" {
		t.Errorf("args = %q, want %q", args2, "extra")
	}
}

func TestFetchTasks_Success(t *testing.T) {
	tasksJSON := `[
		{"id":1,"type":"implement","description":"add feature","status":"done"},
		{"id":2,"type":"research","description":"investigate","status":"open","depends_on":[1]}
	]`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, tasksJSON)
	}))
	defer ts.Close()

	addr := ts.URL[len("http://"):]
	cmd := fetchTasks(addr)
	msg := cmd()

	result, ok := msg.(tasksResult)
	if !ok {
		t.Fatalf("expected tasksResult, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected error: %v", result.err)
	}
	if len(result.tasks) != 2 {
		t.Errorf("tasks count = %d, want 2", len(result.tasks))
	}
	if result.tasks[0].ID != 1 || result.tasks[0].Status != agent.TaskDone {
		t.Errorf("task[0] = %+v, want id=1 status=done", result.tasks[0])
	}
	if len(result.tasks[1].DependsOn) != 1 || result.tasks[1].DependsOn[0] != 1 {
		t.Errorf("task[1].DependsOn = %v, want [1]", result.tasks[1].DependsOn)
	}
}

func TestFetchTasks_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	addr := ts.URL[len("http://"):]
	cmd := fetchTasks(addr)
	msg := cmd()

	result, ok := msg.(tasksResult)
	if !ok {
		t.Fatalf("expected tasksResult, got %T", msg)
	}
	if result.err == nil {
		t.Error("expected error, got nil")
	}
}

func TestFetchTasks_NilResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return empty array (nil fn case)
		w.Write([]byte(`[]`))
	}))
	defer ts.Close()

	addr := ts.URL[len("http://"):]
	cmd := fetchTasks(addr)
	msg := cmd()

	result, ok := msg.(tasksResult)
	if !ok {
		t.Fatalf("expected tasksResult, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected error: %v", result.err)
	}
	if len(result.tasks) != 0 {
		t.Errorf("tasks count = %d, want 0", len(result.tasks))
	}
}

// ---------------------------------------------------------------------------
// toolIndices helper
// ---------------------------------------------------------------------------

func TestToolIndices_Empty(t *testing.T) {
	m := model{messages: []chatMessage{}}
	indices := m.toolIndices()
	if len(indices) != 0 {
		t.Errorf("empty messages: got %d indices, want 0", len(indices))
	}
}

func TestToolIndices_MixedMessages(t *testing.T) {
	m := model{
		messages: []chatMessage{
			{Kind: msgUser, Text: "hello"},
			{Kind: msgTool, Tool: &toolCallInfo{Name: "shell"}},
			{Kind: msgAssistant, Text: "thinking"},
			{Kind: msgTool, Tool: &toolCallInfo{Name: "read_file"}},
			{Kind: msgSystem, Text: "note"},
			{Kind: msgTool, Tool: &toolCallInfo{Name: "write_file"}},
		},
	}
	indices := m.toolIndices()
	if len(indices) != 3 {
		t.Errorf("got %d tool indices, want 3: %v", len(indices), indices)
	}
	// Verify they point to msgTool messages
	for _, idx := range indices {
		if m.messages[idx].Kind != msgTool {
			t.Errorf("index %d is not a msgTool", idx)
		}
	}
	// Check order is preserved
	if indices[0] != 1 || indices[1] != 3 || indices[2] != 5 {
		t.Errorf("indices = %v, want [1, 3, 5]", indices)
	}
}

func TestToolIndices_NoTools(t *testing.T) {
	m := model{
		messages: []chatMessage{
			{Kind: msgUser, Text: "hello"},
			{Kind: msgAssistant, Text: "thinking"},
		},
	}
	indices := m.toolIndices()
	if len(indices) != 0 {
		t.Errorf("no tools: got %d indices, want 0", len(indices))
	}
}

// ---------------------------------------------------------------------------
// focusTool navigation
// ---------------------------------------------------------------------------

func TestFocusTool_InitialDown(t *testing.T) {
	m := model{
		messages: []chatMessage{
			{Kind: msgTool, Tool: &toolCallInfo{Name: "a", Done: true}},
			{Kind: msgTool, Tool: &toolCallInfo{Name: "b", Done: true}},
		},
		focusedToolIdx: -1,
	}
	// Down from -1 goes to last tool
	m.focusTool(1)
	if m.focusedToolIdx != 1 {
		t.Errorf("focusTool(1) from -1: got idx=%d, want 1", m.focusedToolIdx)
	}
}

func TestFocusTool_UpDown(t *testing.T) {
	m := model{
		messages: []chatMessage{
			{Kind: msgTool, Tool: &toolCallInfo{Name: "a", Done: true}},
			{Kind: msgTool, Tool: &toolCallInfo{Name: "b", Done: true}},
			{Kind: msgTool, Tool: &toolCallInfo{Name: "c", Done: true}},
		},
		focusedToolIdx: 1, // pointing at "b"
	}
	// Down from middle
	m.focusTool(1)
	if m.focusedToolIdx != 2 {
		t.Errorf("focusTool(1) from middle: got idx=%d, want 2", m.focusedToolIdx)
	}
	// Down from last clamps to last
	m.focusTool(1)
	if m.focusedToolIdx != 2 {
		t.Errorf("focusTool(1) at last clamps: got idx=%d, want 2", m.focusedToolIdx)
	}
	// Up from last goes to middle
	m.focusTool(-1)
	if m.focusedToolIdx != 1 {
		t.Errorf("focusTool(-1) from last: got idx=%d, want 1", m.focusedToolIdx)
	}
	// Up from first clamps to first
	m.focusedToolIdx = 0
	m.focusTool(-1)
	if m.focusedToolIdx != 0 {
		t.Errorf("focusTool(-1) at first clamps: got idx=%d, want 0", m.focusedToolIdx)
	}
	// Up from middle goes to first
	m.focusedToolIdx = 1
	m.focusTool(-1)
	if m.focusedToolIdx != 0 {
		t.Errorf("focusTool(-1) from middle: got idx=%d, want 0", m.focusedToolIdx)
	}
}

func TestFocusTool_NoTools(t *testing.T) {
	m := model{
		messages: []chatMessage{
			{Kind: msgUser, Text: "hello"},
		},
		focusedToolIdx: -1,
	}
	// Should not crash
	m.focusTool(1)
	if m.focusedToolIdx != -1 {
		t.Errorf("focusTool with no tools: got idx=%d, want -1", m.focusedToolIdx)
	}
}

// ---------------------------------------------------------------------------
// isToolFocused
// ---------------------------------------------------------------------------

func TestIsToolFocused(t *testing.T) {
	m := model{
		messages: []chatMessage{
			{Kind: msgTool, Tool: &toolCallInfo{Name: "a", Done: true}},
			{Kind: msgTool, Tool: &toolCallInfo{Name: "b", Done: true}},
		},
		scrollMode:     true,
		focusedToolIdx: 1,
	}

	if m.isToolFocused(0) {
		t.Error("isToolFocused(0) = true, want false")
	}
	if !m.isToolFocused(1) {
		t.Error("isToolFocused(1) = false, want true")
	}
}

func TestIsToolFocused_NotInScrollMode(t *testing.T) {
	m := model{
		messages: []chatMessage{
			{Kind: msgTool, Tool: &toolCallInfo{Name: "a", Done: true}},
		},
		scrollMode:     false,
		focusedToolIdx: 0,
	}
	// When not in scroll mode, nothing should be focused
	if m.isToolFocused(0) {
		t.Error("isToolFocused when not in scrollMode = true, want false")
	}
}

// ---------------------------------------------------------------------------
// renderMessage passes focused to renderToolCall
// ---------------------------------------------------------------------------

func TestRenderMessage_PassesFocusedToTool(t *testing.T) {
	// Test that renderMessage accepts focused parameter
	msg := chatMessage{
		Kind: msgTool,
		Tool: &toolCallInfo{
			Name:        "test",
			Description: "desc",
			Expanded:    false,
			Done:        true,
			Duration:    0,
		},
	}

	// Unfocused rendering
	unfocused := renderMessage(msg, 80, false)
	if !strings.Contains(unfocused, "▸") {
		t.Errorf("unfocused should use ▸, got: %s", unfocused)
	}

	// Focused rendering
	focused := renderMessage(msg, 80, true)
	if !strings.Contains(focused, "▶") {
		t.Errorf("focused should use ▶, got: %s", focused)
	}
}

// ---------------------------------------------------------------------------
// Server GET /tasks endpoint
// ---------------------------------------------------------------------------

func TestHandleTasks_GET(t *testing.T) {
	// Verify fetchTasks correctly deserializes a /tasks response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]agent.Task{
			{ID: 1, Type: agent.TaskTypeImplement, Description: "do thing", Status: agent.TaskDone},
			{ID: 2, Type: agent.TaskTypeVerify, Description: "check thing", Status: agent.TaskOpen},
		})
	}))
	defer srv.Close()

	// fetchTasks returns a tea.Cmd; call it to get the message.
	cmd := fetchTasks(srv.Listener.Addr().String())
	msg := cmd()
	result, ok := msg.(tasksResult)
	if !ok {
		t.Fatalf("expected tasksResult, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected error: %v", result.err)
	}
	if len(result.tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(result.tasks))
	}
	if result.tasks[0].ID != 1 || result.tasks[0].Status != agent.TaskDone {
		t.Errorf("task[0]: %+v", result.tasks[0])
	}
	if result.tasks[1].ID != 2 || result.tasks[1].Status != agent.TaskOpen {
		t.Errorf("task[1]: %+v", result.tasks[1])
	}
}

func TestTaskStatus_Constants(t *testing.T) {
	// Verify task status constants are distinct strings
	statuses := []agent.TaskStatus{
		agent.TaskOpen,
		agent.TaskInProgress,
		agent.TaskDone,
		agent.TaskCancelled,
	}
	seen := make(map[string]bool)
	for _, s := range statuses {
		if seen[string(s)] {
			t.Errorf("duplicate status: %s", s)
		}
		seen[string(s)] = true
	}
}

func TestTaskType_Constants(t *testing.T) {
	types := []agent.TaskType{
		agent.TaskTypeResearch,
		agent.TaskTypeImplement,
		agent.TaskTypeVerify,
		agent.TaskTypeFix,
	}
	seen := make(map[string]bool)
	for _, typ := range types {
		if seen[string(typ)] {
			t.Errorf("duplicate type: %s", typ)
		}
		seen[string(typ)] = true
	}
}

// ---------------------------------------------------------------------------
// renderToolCall hides output when not expanded
// ---------------------------------------------------------------------------

func TestRenderToolCall_OutputHiddenWhenCollapsed(t *testing.T) {
	tc := toolCallInfo{
		Name:        "shell",
		Description: "short",
		Output:      "LOTS OF OUTPUT THAT SHOULD BE HIDDEN",
		Expanded:    false,
		Done:        true,
		Duration:    1 * time.Second,
	}
	out := renderToolCall(tc, 80, false)

	// Output should not appear when collapsed
	if strings.Contains(out, "LOTS OF OUTPUT") {
		t.Errorf("collapsed tool should hide output, got: %s", out)
	}
	// But name and description should
	if !strings.Contains(out, "shell") {
		t.Errorf("should show name when collapsed, got: %s", out)
	}
}

func TestRenderToolCall_OutputShownWhenExpanded(t *testing.T) {
	tc := toolCallInfo{
		Name:        "shell",
		Description: "short",
		Output:      "OUTPUT HERE",
		Expanded:    true,
		Done:        true,
		Duration:    1 * time.Second,
	}
	out := renderToolCall(tc, 80, false)

	if !strings.Contains(out, "OUTPUT HERE") {
		t.Errorf("expanded tool should show output, got: %s", out)
	}
}

// Verify task store serialization roundtrip
func TestTask_JSON(t *testing.T) {
	task := agent.Task{
		ID:              5,
		Type:            agent.TaskTypeImplement,
		Description:     "write tests",
		Prompt:          "prompt text",
		Status:          agent.TaskInProgress,
		DependsOn:       []int{1, 2},
		Notes:           []string{"note1"},
		ReasoningEffort: "medium",
	}
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var roundtrip agent.Task
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if roundtrip.ID != task.ID {
		t.Errorf("ID = %d, want %d", roundtrip.ID, task.ID)
	}
	if roundtrip.Type != task.Type {
		t.Errorf("Type = %s, want %s", roundtrip.Type, task.Type)
	}
	if roundtrip.Status != task.Status {
		t.Errorf("Status = %s, want %s", roundtrip.Status, task.Status)
	}
	if roundtrip.ReasoningEffort != task.ReasoningEffort {
		t.Errorf("ReasoningEffort = %s, want %s", roundtrip.ReasoningEffort, task.ReasoningEffort)
	}
}
