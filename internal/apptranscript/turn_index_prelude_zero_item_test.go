package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// preludeOnlyZeroItemGroupCommunicateFixture persists a transcript whose only
// content after the prelude is a text-less assistant turn carrying a single
// deferred communicate call with no paired result turn. The prelude
// (SystemPrompt) occupies visible slot 0; the assistant turn opens a group of
// its own with GroupItems == 0 (communicate deferred, no text). Unlike the
// trailing-zero-item-group case, there is NO item-bearing group before it, so
// the bounded reader's registry is never seeded on the item-bearing path and
// regSeeded stays false. Before the fix the zero-item branch skipped the group
// entirely when !regSeeded, leaving CommRawArgs empty, so the tail flush
// (which runs because logicalTurnCount()==1 counts only the prelude, making
// tail=true, and the prelude is turns[0]) rendered nothing on paged reads
// while the full read (groupedAppTurnProjection projects every record before
// dropping empty groups, then flushes) rendered the delivered message.
func preludeOnlyZeroItemGroupCommunicateFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prelude_zero.transcript.jsonl")
	header := transcript.Header{
		Kind:          "header",
		FormatVersion: transcript.FormatVersion,
		SessionID:     "prelude_zero",
		SystemPrompt:  "You are Evener.",
	}
	entries := []transcript.Entry{
		// The only content after the prelude: a text-less assistant turn with
		// a single deferred communicate call, unpaired (no result turn). This
		// opens a group with GroupItems == 0.
		{Kind: "entry", Seq: 1, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call_prelude_zero",
				Name:      "communicate",
				Arguments: json.RawMessage(`{"message":"delivered at the tail"}`),
			}},
		}}}},
	}
	var data []byte
	hline, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, hline...)
	data = append(data, '\n')
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPagedReadFlushesPreludeOnlyZeroItemGroupCommunicate verifies that when
// the only content after a prelude is a zero-item group (a text-less assistant
// turn with only a deferred, unpaired communicate call), the bounded turn-paged
// read seeds CommRawArgs and the tail flush renders the delivered message,
// matching the full read. Before the fix, the bounded reader's zero-item
// branch skipped the group when the registry was not already seeded (no
// item-bearing group preceded it), so CommRawArgs stayed empty and the message
// vanished on paged reads while the full read showed it.
func TestPagedReadFlushesPreludeOnlyZeroItemGroupCommunicate(t *testing.T) {
	path := preludeOnlyZeroItemGroupCommunicateFixture(t)

	// Full read with flush: project with a shared registry (the way the
	// server's appTurnProjectionFromTranscriptFile does), then flush.
	reg := NewToolCallRegistry()
	fullProjector := func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		return boundedTestProjector(turn, turnID, turnIndex, reg)
	}
	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, fullProjector)
	FlushUnpairedCommunicates(&full, reg)
	if len(full) == 0 {
		t.Fatalf("full read produced 0 turns")
	}
	fullFlushed, ok := findAnyFlushedCommunicateItem(allTurnsItems(full))
	if !ok {
		t.Fatalf("full read with flush must render the prelude-only unpaired communicate; items: %+v", allTurnsItems(full))
	}
	if fullFlushed.Text != "delivered at the tail" {
		t.Fatalf("full read flushed communicate Text = %q, want %q", fullFlushed.Text, "delivered at the tail")
	}

	// Paged read (projectIndexedGroup path — flushes internally at the tail).
	page := requirePageFromFile(t, NewTurnCache(), path, testMaxLineBytes, "", 50, boundedTestProjector)
	if len(page.Turns) == 0 {
		t.Fatalf("paged read produced 0 turns")
	}
	pageFlushed, ok := findAnyFlushedCommunicateItem(allTurnsItems(page.Turns))
	if !ok {
		t.Fatalf("paged read must render the prelude-only unpaired communicate (parity with full read); items: %+v", allTurnsItems(page.Turns))
	}
	if pageFlushed.Text != fullFlushed.Text {
		t.Errorf("paged flushed Text = %q, want %q (full read parity)", pageFlushed.Text, fullFlushed.Text)
	}

	// The flushed item IDs must match (same call ID → same flushed ID).
	if pageFlushed.ID != fullFlushed.ID {
		t.Errorf("paged flushed ID = %q, want %q (full read parity)", pageFlushed.ID, fullFlushed.ID)
	}
	if !strings.HasPrefix(fullFlushed.ID, "item_assistant_flushed_call_prelude_zero") {
		t.Errorf("full read flushed ID = %q, want prefix item_assistant_flushed_call_prelude_zero", fullFlushed.ID)
	}
}
