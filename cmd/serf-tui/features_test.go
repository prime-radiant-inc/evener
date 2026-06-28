package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/cmd/serf-tui/internal/msgrender"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
)

// ---------------------------------------------------------------------------
// msgrender.RenderToolCall: focused parameter changes arrow character
// ---------------------------------------------------------------------------

func TestRenderToolCall_Collapsed(t *testing.T) {
	tc := transcript.ToolCallInfo{
		Name:        "shell",
		Description: "list files",
		Expanded:    false,
		Done:        true,
		Duration:    1 * time.Second,
	}
	out := msgrender.RenderToolCall(tc, 80, false)

	// New format uses state bar ▍ and checkmark ✓
	if !strings.Contains(out, "▍") {
		t.Errorf("collapsed tool should show ▍ state bar, got: %s", out)
	}
	// Duration should be shown (new format: "1.0s" not "[1.0s]")
	if !strings.Contains(out, "1.0s") {
		t.Errorf("collapsed tool should show duration, got: %s", out)
	}
	// Verb should be present (shell renderer returns verb "shell")
	if !strings.Contains(out, "shell") {
		t.Errorf("collapsed tool should show verb, got: %s", out)
	}
}

func TestRenderToolCall_Expanded(t *testing.T) {
	tc := transcript.ToolCallInfo{
		Name:        "shell",
		Description: "list files",
		Detail:      "full args here",
		Output:      "file1.go\nfile2.go",
		Expanded:    true,
		Done:        true,
		Duration:    500 * time.Millisecond,
	}
	out := msgrender.RenderToolCall(tc, 80, false)

	// New format: state bar ▍ and checkmark ✓ (no longer ▾ arrow)
	if !strings.Contains(out, "▍") {
		t.Errorf("expanded tool should show ▍ state bar, got: %s", out)
	}
	// Shell now has a Body renderer (msgrender.ShellBody) which renders Output via chroma.
	// Output content is shown (possibly ANSI-escaped); Detail is suppressed by Body.
	if !strings.Contains(out, "file1.go") {
		t.Errorf("expanded tool should show output via msgrender.ShellBody, got: %s", out)
	}
}

func TestRenderToolCall_FocusedChangesArrow(t *testing.T) {
	tc := transcript.ToolCallInfo{
		Name:        "shell",
		Description: "list files",
		Expanded:    false,
		Done:        true,
		Duration:    1 * time.Second,
	}

	// Unfocused: single state bar ▍
	unfocused := msgrender.RenderToolCall(tc, 80, false)

	// Focused: double state bar ▍▍ (tuiprim.FocusedStateBar)
	tc.Expanded = false
	focusedCollapsed := msgrender.RenderToolCall(tc, 80, true)
	// tuiprim.FocusedStateBar renders the glyph twice
	if strings.Count(focusedCollapsed, "▍") < 2 {
		t.Errorf("focused collapsed tool should show double ▍▍, got: %s", focusedCollapsed)
	}

	tc.Expanded = true
	focusedExpanded := msgrender.RenderToolCall(tc, 80, true)
	if strings.Count(focusedExpanded, "▍") < 2 {
		t.Errorf("focused expanded tool should show double ▍▍, got: %s", focusedExpanded)
	}

	// Unfocused and focused must differ (different bar width)
	if unfocused == focusedCollapsed {
		t.Error("unfocused and focused output should differ")
	}
}

func TestRenderToolCall_DoneVsInProgress(t *testing.T) {
	done := transcript.ToolCallInfo{Name: "x", Done: true, Duration: 1 * time.Second}
	pending := transcript.ToolCallInfo{Name: "x", Done: false}

	doneOut := msgrender.RenderToolCall(done, 80, false)
	pendingOut := msgrender.RenderToolCall(pending, 80, false)

	// Done shows duration (new format: "1.0s" not "[1.0s]")
	if !strings.Contains(doneOut, "1.0s") {
		t.Errorf("done tool should show duration: %s", doneOut)
	}
	// Pending shows "…" (ellipsis character)
	if !strings.Contains(pendingOut, "…") {
		t.Errorf("pending tool should show …: %s", pendingOut)
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

	out2 := renderTasks([]taskpkg.Task{}, 80)
	if out2 != "No tasks." {
		t.Errorf("empty slice = %q, want %q", out2, "No tasks.")
	}
}

func TestRenderTasks_SingleTask(t *testing.T) {
	tasks := []taskpkg.Task{
		{ID: 1, Type: taskpkg.TaskTypeImplement, Description: "add login handler", Status: taskpkg.TaskDone},
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
	tasks := []taskpkg.Task{
		{ID: 1, Type: taskpkg.TaskTypeResearch, Description: "research", Status: taskpkg.TaskOpen},
		{ID: 2, Type: taskpkg.TaskTypeImplement, Description: "implement", Status: taskpkg.TaskInProgress},
		{ID: 3, Type: taskpkg.TaskTypeVerify, Description: "verify", Status: taskpkg.TaskDone},
		{ID: 4, Type: taskpkg.TaskTypeFix, Description: "fix", Status: taskpkg.TaskCancelled},
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
	tasks := []taskpkg.Task{
		{ID: 2, Type: taskpkg.TaskTypeImplement, Description: "step 2", Status: taskpkg.TaskOpen, DependsOn: []int{1}},
		{ID: 3, Type: taskpkg.TaskTypeVerify, Description: "step 3", Status: taskpkg.TaskOpen, DependsOn: []int{1, 2}},
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
	tasks := []taskpkg.Task{
		{ID: 1, Type: taskpkg.TaskTypeResearch, Description: "hard problem", Status: taskpkg.TaskInProgress, ReasoningEffort: "high"},
	}
	out := renderTasks(tasks, 80)

	if !strings.Contains(out, "[high]") {
		t.Errorf("should show reasoning effort [high], got: %s", out)
	}
}

func TestRenderTasks_WidthMinimum(t *testing.T) {
	// renderTasks should handle width <= 0 gracefully by using default
	tasks := []taskpkg.Task{{ID: 1, Type: taskpkg.TaskTypeImplement, Description: "x", Status: taskpkg.TaskOpen}}
	out := renderTasks(tasks, 0)
	if !strings.Contains(out, "Tasks (1)") {
		t.Errorf("should not crash on width=0, got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// toolIndices helper
// ---------------------------------------------------------------------------

func TestToolIndices_Empty(t *testing.T) {
	m := model{messages: []transcript.ChatMessage{}}
	indices := m.toolIndices()
	if len(indices) != 0 {
		t.Errorf("empty messages: got %d indices, want 0", len(indices))
	}
}

func TestToolIndices_MixedMessages(t *testing.T) {
	m := model{
		messages: []transcript.ChatMessage{
			{Kind: transcript.MsgUser, Text: "hello"},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "shell"}},
			{Kind: transcript.MsgAssistant, Text: "thinking"},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "read_file"}},
			{Kind: transcript.MsgSystem, Text: "note"},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "write_file"}},
		},
	}
	indices := m.toolIndices()
	if len(indices) != 3 {
		t.Errorf("got %d tool indices, want 3: %v", len(indices), indices)
	}
	// Verify they point to transcript.MsgTool messages
	for _, idx := range indices {
		if m.messages[idx].Kind != transcript.MsgTool {
			t.Errorf("index %d is not a transcript.MsgTool", idx)
		}
	}
	// Check order is preserved
	if indices[0] != 1 || indices[1] != 3 || indices[2] != 5 {
		t.Errorf("indices = %v, want [1, 3, 5]", indices)
	}
}

func TestToolIndices_NoTools(t *testing.T) {
	m := model{
		messages: []transcript.ChatMessage{
			{Kind: transcript.MsgUser, Text: "hello"},
			{Kind: transcript.MsgAssistant, Text: "thinking"},
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
		messages: []transcript.ChatMessage{
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "a", Done: true}},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "b", Done: true}},
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
		messages: []transcript.ChatMessage{
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "a", Done: true}},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "b", Done: true}},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "c", Done: true}},
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
		messages: []transcript.ChatMessage{
			{Kind: transcript.MsgUser, Text: "hello"},
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
		messages: []transcript.ChatMessage{
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "a", Done: true}},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "b", Done: true}},
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
		messages: []transcript.ChatMessage{
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "a", Done: true}},
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
// msgrender.RenderMessage passes focused to msgrender.RenderToolCall
// ---------------------------------------------------------------------------

func TestRenderMessage_PassesFocusedToTool(t *testing.T) {
	// Test that msgrender.RenderMessage accepts focused parameter
	msg := transcript.ChatMessage{
		Kind: transcript.MsgTool,
		Tool: &transcript.ToolCallInfo{
			Name:        "test",
			Description: "desc",
			Expanded:    false,
			Done:        true,
			Duration:    0,
		},
	}

	// Unfocused rendering: single state bar ▍
	unfocused := msgrender.RenderMessage(msg, 80, false)
	if !strings.Contains(unfocused, "▍") {
		t.Errorf("unfocused should use ▍ state bar, got: %s", unfocused)
	}

	// Focused rendering: double state bar ▍▍
	focused := msgrender.RenderMessage(msg, 80, true)
	if strings.Count(focused, "▍") < 2 {
		t.Errorf("focused should use double ▍▍ bar, got: %s", focused)
	}
}

func TestTaskStatus_Constants(t *testing.T) {
	// Verify task status constants have the exact string values the JSON API and
	// renderers depend on.
	for want, got := range map[string]taskpkg.TaskStatus{
		"open":        taskpkg.TaskOpen,
		"in_progress": taskpkg.TaskInProgress,
		"done":        taskpkg.TaskDone,
		"cancelled":   taskpkg.TaskCancelled,
	} {
		if string(got) != want {
			t.Errorf("constant value = %q, want %q", got, want)
		}
	}
}

func TestTaskType_Constants(t *testing.T) {
	// Verify task type constants have the exact string values the JSON API and
	// renderers depend on.
	for want, got := range map[string]taskpkg.TaskType{
		"research":  taskpkg.TaskTypeResearch,
		"implement": taskpkg.TaskTypeImplement,
		"verify":    taskpkg.TaskTypeVerify,
		"fix":       taskpkg.TaskTypeFix,
	} {
		if string(got) != want {
			t.Errorf("constant value = %q, want %q", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// msgrender.RenderToolCall hides output when not expanded
// ---------------------------------------------------------------------------

func TestRenderToolCall_OutputHiddenWhenCollapsed(t *testing.T) {
	tc := transcript.ToolCallInfo{
		Name:        "shell",
		Description: "short",
		Output:      "LOTS OF OUTPUT THAT SHOULD BE HIDDEN",
		Expanded:    false,
		Done:        true,
		Duration:    1 * time.Second,
	}
	out := msgrender.RenderToolCall(tc, 80, false)

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
	tc := transcript.ToolCallInfo{
		Name:        "shell",
		Description: "short",
		Output:      "OUTPUT HERE",
		Expanded:    true,
		Done:        true,
		Duration:    1 * time.Second,
	}
	out := msgrender.RenderToolCall(tc, 80, false)

	// msgrender.ShellBody renders output via chroma, which may split tokens across ANSI codes.
	// Check for both words to confirm output is present.
	if !strings.Contains(out, "OUTPUT") || !strings.Contains(out, "HERE") {
		t.Errorf("expanded tool should show output, got: %s", out)
	}
}

// Verify task store serialization roundtrip
func TestTask_JSON(t *testing.T) {
	task := taskpkg.Task{
		ID:              5,
		Type:            taskpkg.TaskTypeImplement,
		Description:     "write tests",
		Prompt:          "prompt text",
		Status:          taskpkg.TaskInProgress,
		DependsOn:       []int{1, 2},
		Notes:           []string{"note1"},
		ReasoningEffort: "medium",
	}
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var roundtrip taskpkg.Task
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
	if roundtrip.Description != task.Description {
		t.Errorf("Description = %q, want %q", roundtrip.Description, task.Description)
	}
	if roundtrip.Prompt != task.Prompt {
		t.Errorf("Prompt = %q, want %q", roundtrip.Prompt, task.Prompt)
	}
	if !reflect.DeepEqual(roundtrip.DependsOn, task.DependsOn) {
		t.Errorf("DependsOn = %v, want %v", roundtrip.DependsOn, task.DependsOn)
	}
	if !reflect.DeepEqual(roundtrip.Notes, task.Notes) {
		t.Errorf("Notes = %v, want %v", roundtrip.Notes, task.Notes)
	}
}
