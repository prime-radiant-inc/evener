package main

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestSubagentPreviewBoundsLatestDirectItems(t *testing.T) {
	resp := subagentPreviewFromThread(appwire.Thread{
		Serf: appwire.SerfThread{Ref: "local:child-preview"},
		Turns: []appwire.Turn{
			{ID: "turn_1", Items: []appwire.ThreadItem{
				{Type: "agentMessage", Text: "older item"},
				{Type: "agentMessage", Text: "found three callers"},
			}},
			{ID: "turn_2", Items: []appwire.ThreadItem{
				{Type: "commandExecution", ToolName: "grep_files", Description: "search billing", Status: "completed"},
				{Type: "agentMessage", Text: "recommended fix"},
			}},
		},
	}, "local:child-preview", 3)

	if resp.Ref != "local:child-preview" {
		t.Fatalf("ref=%q", resp.Ref)
	}
	if !resp.Truncated {
		t.Fatal("expected truncated preview when older direct items are hidden")
	}
	if len(resp.Items) != 3 {
		t.Fatalf("items=%d, want 3: %+v", len(resp.Items), resp.Items)
	}
	texts := []string{resp.Items[0].Text, resp.Items[1].Description, resp.Items[2].Text}
	want := []string{"found three callers", "search billing", "recommended fix"}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("item %d=%q, want %q (items=%+v)", i, texts[i], want[i], resp.Items)
		}
	}
	if resp.Items[0].ID != "" || resp.Items[0].TurnID != "" || resp.Items[0].Raw != nil {
		t.Fatalf("preview leaked non-display identity/raw fields: %+v", resp.Items[0])
	}
}

func TestSubagentPreviewClampLimit(t *testing.T) {
	thread := appwire.Thread{Turns: []appwire.Turn{{ID: "turn_1", Items: []appwire.ThreadItem{
		{Type: "agentMessage", Text: "one"},
		{Type: "agentMessage", Text: "two"},
		{Type: "agentMessage", Text: "three"},
		{Type: "agentMessage", Text: "four"},
		{Type: "agentMessage", Text: "five"},
		{Type: "agentMessage", Text: "six"},
	}}}}
	if got := len(subagentPreviewFromThread(thread, "local:child", 0).Items); got != 3 {
		t.Fatalf("default limit items=%d, want 3", got)
	}
	if got := len(subagentPreviewFromThread(thread, "local:child", 99).Items); got != 5 {
		t.Fatalf("clamped max items=%d, want 5", got)
	}
	if got := len(subagentPreviewFromThread(thread, "local:child", -2).Items); got != 3 {
		t.Fatalf("negative limit should default to 3, got %d", got)
	}
}
