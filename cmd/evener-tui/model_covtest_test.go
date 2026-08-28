package tui

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/cmd/evener-tui/internal/inputhistory"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
)

// ---- focusTool: various scenarios -------------------------------------------

func TestCovFocusTool_NoToolsNoOp(t *testing.T) {
	m := model{
		messages:       nil,
		focusedToolIdx: -1,
	}
	m.focusTool(1)
	if m.focusedToolIdx != -1 {
		t.Fatalf("focusTool with no tools should be no-op: %d", m.focusedToolIdx)
	}
}

func TestCovFocusTool_NoFocusDefaultsToLast(t *testing.T) {
	m := model{
		messages: []transcript.ChatMessage{
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "a"}},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "b"}},
		},
		focusedToolIdx: -1,
	}
	m.focusTool(1)
	if m.focusedToolIdx != 1 {
		t.Fatalf("no focus should default to last tool: %d, want 1", m.focusedToolIdx)
	}
}

func TestCovFocusTool_FocusedIndexNotInList(t *testing.T) {
	m := model{
		messages: []transcript.ChatMessage{
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "a"}},
		},
		focusedToolIdx: 99, // not in the list
	}
	m.focusTool(1)
	if m.focusedToolIdx != 0 {
		t.Fatalf("out-of-range focus should reset to last: %d, want 0", m.focusedToolIdx)
	}
}

func TestCovFocusTool_DirNextWraps(t *testing.T) {
	m := model{
		messages: []transcript.ChatMessage{
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "a"}},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "b"}},
		},
		focusedToolIdx: 1, // at the last
	}
	m.focusTool(1) // next beyond last — should clamp
	if m.focusedToolIdx != 1 {
		t.Fatalf("next at last should clamp: %d, want 1", m.focusedToolIdx)
	}
}

func TestCovFocusTool_DirPrevClamps(t *testing.T) {
	m := model{
		messages: []transcript.ChatMessage{
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "a"}},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "b"}},
		},
		focusedToolIdx: 0,
	}
	m.focusTool(-1) // prev before first — should clamp
	if m.focusedToolIdx != 0 {
		t.Fatalf("prev at first should clamp: %d, want 0", m.focusedToolIdx)
	}
}

func TestCovFocusTool_DirNextNormal(t *testing.T) {
	m := model{
		messages: []transcript.ChatMessage{
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "a"}},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "b"}},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "c"}},
		},
		focusedToolIdx: 0,
	}
	m.focusTool(1)
	if m.focusedToolIdx != 1 {
		t.Fatalf("next from 0 = %d, want 1", m.focusedToolIdx)
	}
	m.focusTool(1)
	if m.focusedToolIdx != 2 {
		t.Fatalf("next from 1 = %d, want 2", m.focusedToolIdx)
	}
}

func TestCovFocusTool_HiddenToolsSkipped(t *testing.T) {
	m := model{
		messages: []transcript.ChatMessage{
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "visible-before"}},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "hidden", Hidden: true}},
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "visible-after"}},
		},
		focusedToolIdx: 0,
	}
	m.focusTool(1)
	if m.focusedToolIdx != 2 {
		t.Fatalf("next focus stopped on hidden tool: index=%d, want visible index 2", m.focusedToolIdx)
	}
}

// ---- clampTextareaHeight ----------------------------------------------------

func TestCovClampTextareaHeight_BelowMin(t *testing.T) {
	if got := clampTextareaHeight(-5, 5); got != 1 {
		t.Fatalf("below min = %d, want 1", got)
	}
}

func TestCovClampTextareaHeight_AboveMax(t *testing.T) {
	if got := clampTextareaHeight(100, 5); got != 5 {
		t.Fatalf("above max = %d, want 5", got)
	}
}

func TestCovClampTextareaHeight_InRange(t *testing.T) {
	if got := clampTextareaHeight(3, 5); got != 3 {
		t.Fatalf("in range = %d, want 3", got)
	}
}

// ---- addHistory --------------------------------------------------------------

func TestCovAddHistory_EscapesNewlines(t *testing.T) {
	m := model{}
	m.addHistory("line1\nline2")
	if len(m.history) != 1 || m.history[0] != `line1\nline2` {
		t.Fatalf("addHistory = %#v, want escaped newlines", m.history)
	}
}

func TestCovAddHistory_CapsAtMaxEntries(t *testing.T) {
	m := model{}
	for i := range inputhistory.MaxHistoryEntries + 10 {
		m.addHistory("entry" + itoa(i))
	}
	if len(m.history) != inputhistory.MaxHistoryEntries {
		t.Fatalf("history count = %d, want exactly %d", len(m.history), inputhistory.MaxHistoryEntries)
	}
	if got, want := m.history[0], "entry10"; got != want {
		t.Fatalf("oldest retained history = %q, want %q", got, want)
	}
	if got, want := m.history[len(m.history)-1], "entry"+itoa(inputhistory.MaxHistoryEntries+9); got != want {
		t.Fatalf("newest retained history = %q, want %q", got, want)
	}
}

// ---- writeWrappedList -------------------------------------------------------

func TestCovWriteWrappedList_SingleItem(t *testing.T) {
	var b strings.Builder
	writeWrappedList(&b, "Label:", []string{"item1"}, 80)
	got := b.String()
	if !strings.Contains(got, "Label:") || !strings.Contains(got, "item1") {
		t.Fatalf("writeWrappedList single = %q", got)
	}
}

func TestCovWriteWrappedList_MultipleItemsNoWrap(t *testing.T) {
	var b strings.Builder
	writeWrappedList(&b, "Items:", []string{"a", "b", "c"}, 200)
	got := b.String()
	if !strings.Contains(got, "a,") || !strings.Contains(got, "b,") || !strings.Contains(got, "c") {
		t.Fatalf("writeWrappedList multiple = %q", got)
	}
}

func TestCovWriteWrappedList_WrapsAtWidth(t *testing.T) {
	var b strings.Builder
	writeWrappedList(&b, "L:", []string{"item1", "item2", "item3", "item4"}, 20)
	got := b.String()
	// Should contain newlines from wrapping
	if strings.Count(got, "\n") < 2 {
		t.Fatalf("writeWrappedList should wrap at narrow width:\n%s", got)
	}
}

func TestCovWriteWrappedList_EmptyItems(t *testing.T) {
	var b strings.Builder
	writeWrappedList(&b, "L:", nil, 80)
	got := b.String()
	if !strings.Contains(got, "L:") {
		t.Fatalf("empty items should still show label: %q", got)
	}
}

// ---- renderTasks -------------------------------------------------------------

func TestCovRenderTasks_WithDependenciesAndEffort(t *testing.T) {
	tasks := []task.Task{
		{ID: 1, Type: "implement", Description: "do thing", DependsOn: []int{2, 3}, ReasoningEffort: "high"},
	}
	got := renderTasks(tasks, 80)
	if !strings.Contains(got, "depends on:") {
		t.Fatalf("renderTasks should show dependencies: %s", got)
	}
	if !strings.Contains(got, "high") {
		t.Fatalf("renderTasks should show reasoning effort: %s", got)
	}
}

func TestCovRenderTasks_UnknownStatusDefault(t *testing.T) {
	tasks := []task.Task{
		{ID: 1, Type: "fix", Description: "x", Status: task.TaskStatus("unknown")},
	}
	got := renderTasks(tasks, 80)
	if !strings.Contains(got, "?") {
		t.Fatalf("unknown status should show '?': %s", got)
	}
}
