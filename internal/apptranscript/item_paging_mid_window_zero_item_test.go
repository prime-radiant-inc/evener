package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Round 4: a zero-item group G1 (text-less assistant turn whose only content
// is a deferred communicate call) sitting BETWEEN two item-bearing groups was
// never projected by the item-window path. indexedItemRanges skips zero-item
// groups (they carry no items), and the only projection site for them was the
// trailing tail flush — but G1 is not trailing (G1.start < G2.end). Its
// communicate's CommRawArgs never entered the threaded registry, so a later
// in-window TOOL_RESULTS found no entry and rendered no agentMessage (M), and
// the tail flush found no entry and rendered no flushed agentMessage (L). The
// full read and turn-paged read project every group, so they are correct.

// midWindowPairedCommunicateFixture persists a transcript with a zero-item
// group G1 between G_hook (HOOK_COMPLETED standalone) and G2 (USER_INPUT +
// healed TOOL_RESULTS). HOOK_COMPLETED closes G0 so G1 opens its own group.
// The communicate call (call_paired_mid) is paired by G2's TOOL_RESULTS
// (IsError=false → rendered as a regular agentMessage). Total items:
// prelude(1) + G0(1) + G_hook(1) + G1(0) + G2(2) = 5.
func midWindowPairedCommunicateFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mid_window_paired.transcript.jsonl")
	header := transcript.Header{
		Kind:          "header",
		FormatVersion: transcript.FormatVersion,
		SessionID:     "mid_window_paired",
		SystemPrompt:  "You are Evener.",
	}
	entries := []transcript.Entry{
		userEntry(1, "hi"),    // G0: opener, item-bearing (userMessage)
		hookCompletedEntry(2), // G_hook: standalone, closes G0, item-bearing (systemMessage)
		// G1: text-less assistant with a deferred communicate call — zero-item.
		{Kind: "entry", Seq: 3, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call_paired_mid",
				Name:      "communicate",
				Arguments: json.RawMessage(`{"message":"paired delivered message","end_turn":true}`),
			}},
		}}}},
		userEntry(4, "ok"), // G2: opener, closes G1, item-bearing (userMessage)
		// G2 continuation: healed TOOL_RESULTS renders the agentMessage.
		{Kind: "entry", Seq: 5, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call_paired_mid",
				Name:       "communicate",
				IsError:    false,
			}},
		}}}},
	}
	return writeMidWindowFixture(t, path, header, entries)
}

// midWindowOrphanedCommunicateFixture is the unpaired variant: G1's
// communicate (call_orphaned_mid) has NO paired result turn. The tail flush
// must render it as a flushed agentMessage. Total items: prelude(1) + G0(1) +
// G_hook(1) + G1(0) + G2(1) = 4 (plus 1 flushed at the tail).
func midWindowOrphanedCommunicateFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mid_window_orphaned.transcript.jsonl")
	header := transcript.Header{
		Kind:          "header",
		FormatVersion: transcript.FormatVersion,
		SessionID:     "mid_window_orphaned",
		SystemPrompt:  "You are Evener.",
	}
	entries := []transcript.Entry{
		userEntry(1, "hi"),    // G0: opener, item-bearing
		hookCompletedEntry(2), // G_hook: standalone, closes G0, item-bearing
		// G1: text-less assistant with a deferred, unpaired communicate call.
		{Kind: "entry", Seq: 3, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call_orphaned_mid",
				Name:      "communicate",
				Arguments: json.RawMessage(`{"message":"orphaned unpaired message","end_turn":true}`),
			}},
		}}}},
		userEntry(4, "ok"), // G2: opener, closes G1, item-bearing (no result turn)
	}
	return writeMidWindowFixture(t, path, header, entries)
}

func writeMidWindowFixture(t *testing.T, path string, header transcript.Header, entries []transcript.Entry) string {
	t.Helper()
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

// findAgentMessageByText returns the first agentMessage item whose Text matches.
func findAgentMessageByText(items []appwire.ThreadItem, text string) (appwire.ThreadItem, bool) {
	for _, item := range items {
		if item.Type == "agentMessage" && item.Text == text {
			return item, true
		}
	}
	return appwire.ThreadItem{}, false
}

// TestItemWindowProjectsMidWindowZeroItemGroupCommunicate verifies that the
// item-window paged path (LatestItemWindowFromFile) projects a mid-window
// zero-item group's deferred communicate, so a later in-window healed
// TOOL_RESULTS finds reg.CommRawArgs[callID] and renders the agentMessage —
// matching the full read and the turn-paged read.
//
// Before the fix, indexedItemRanges skipped zero-item groups (no range), and
// the only projection site for them was the trailing tail flush. G1 sat
// between G_hook and G2 — not trailing (G1.start < G2.end) — so it was never
// projected. reg.CommRawArgs[call_paired_mid] stayed empty, and G2's
// TOOL_RESULTS (IsError=false) rendered no agentMessage. The full read
// projects every record before dropping empty groups, so it rendered the
// agentMessage — a parity divergence.
//
// RED at 975c338f74: the item-window read returns no agentMessage for the
// paired communicate.
// GREEN at head: G1 is interleaved before G2, seeding reg, so the TOOL_RESULTS
// renders the agentMessage.
//
// The small-window variant (Limit=2, selects only G2) is a regression guard:
// G2's StartsGroup snapshot carries CommRawArgs from all preceding groups
// (including G1's call_paired_mid), so the agentMessage renders from the
// snapshot at BOTH the old and new heads.
func TestItemWindowProjectsMidWindowZeroItemGroupCommunicate(t *testing.T) {
	path := midWindowPairedCommunicateFixture(t)

	// Full read: the reference. Threads one registry, pairs the communicate.
	reg := NewToolCallRegistry()
	fullProjector := func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		return boundedTestProjector(turn, turnID, turnIndex, reg)
	}
	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, fullProjector)
	FlushUnpairedCommunicates(&full, reg)
	if len(full) == 0 {
		t.Fatalf("full read produced 0 turns")
	}
	fullItem, ok := findAgentMessageByText(allTurnsItems(full), "paired delivered message")
	if !ok {
		t.Fatalf("full read must render the paired communicate agentMessage; items: %+v", allTurnsItems(full))
	}

	// Item-window LARGE (Limit=40): selects all items. Before the fix, G1
	// (zero-item, between G_hook and G2) was never projected, so
	// reg.CommRawArgs[call_paired_mid] was empty and G2's TOOL_RESULTS
	// rendered no agentMessage.
	window, _, err := NewTurnCache().LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_mid_paired",
		Limit:     40,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile: %v", err)
	}
	windowItem, ok := findAgentMessageByText(candidatesToItems(window.Candidates), "paired delivered message")
	if !ok {
		t.Fatalf("item-window read must render the paired communicate agentMessage (parity with full read); candidates: %+v", candidatesToItems(window.Candidates))
	}
	if windowItem.Text != fullItem.Text {
		t.Errorf("item-window agentMessage Text = %q, want %q (full read parity)", windowItem.Text, fullItem.Text)
	}
	if windowItem.ID != fullItem.ID {
		t.Errorf("item-window agentMessage ID = %q, want %q (full read parity)", windowItem.ID, fullItem.ID)
	}
	// The communicate is paired (consumed by the TOOL_RESULTS), not unpaired —
	// no flushed phantom should appear.
	for _, c := range window.Candidates {
		if item, ok := findAnyFlushedCommunicateItem([]appwire.ThreadItem{c.Item}); ok {
			t.Errorf("item-window read rendered a spurious flushed communicate agentMessage: %+v", item)
		}
	}

	// Item-window SMALL (Limit=2): selects only G2's 2 items. The seed comes
	// from G2's StartsGroup snapshot, which carries CommRawArgs from all
	// preceding groups (including G1's call_paired_mid). The agentMessage
	// renders from the snapshot — a regression guard that the fix does not
	// break the existing snapshot-based seeding.
	smallWindow, _, err := NewTurnCache().LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_mid_paired_small",
		Limit:     2,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile (small): %v", err)
	}
	smallItem, ok := findAgentMessageByText(candidatesToItems(smallWindow.Candidates), "paired delivered message")
	if !ok {
		t.Fatalf("small item-window read must render the paired communicate agentMessage (snapshot seeding); candidates: %+v", candidatesToItems(smallWindow.Candidates))
	}
	if smallItem.Text != fullItem.Text {
		t.Errorf("small item-window agentMessage Text = %q, want %q (full read parity)", smallItem.Text, fullItem.Text)
	}
}

// TestItemWindowFlushesMidWindowZeroItemGroupCommunicate verifies that the
// item-window paged path projects a mid-window zero-item group's unpaired
// communicate so the tail flush renders the flushed agentMessage — matching
// the full read and the turn-paged read. Closes the L finding (#2416).
//
// Before the fix, G1 (zero-item, between G_hook and G2) was never projected —
// not a range, not trailing (G1.start < G2.end) — so reg was never seeded and
// the tail flush rendered nothing. The full read and turn-paged read project
// every group, so they rendered the flushed agentMessage.
//
// RED at 975c338f74: the item-window read returns no flushed agentMessage.
// GREEN at head: G1 is interleaved before G2, seeding reg, so the tail flush
// renders the flushed agentMessage.
func TestItemWindowFlushesMidWindowZeroItemGroupCommunicate(t *testing.T) {
	path := midWindowOrphanedCommunicateFixture(t)

	// Full read with flush: the reference.
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
		t.Fatalf("full read with flush must render the mid-window unpaired communicate; items: %+v", allTurnsItems(full))
	}
	if fullFlushed.Text != "orphaned unpaired message" {
		t.Fatalf("full read flushed communicate Text = %q, want %q", fullFlushed.Text, "orphaned unpaired message")
	}

	// Turn-paged read: projects all groups (including zero-item G1) and
	// flushes at the tail. Parity with the full read.
	page := requirePageFromFile(t, NewTurnCache(), path, testMaxLineBytes, "", 50, boundedTestProjector)
	if len(page.Turns) == 0 {
		t.Fatalf("paged read produced 0 turns")
	}
	pageFlushed, ok := findAnyFlushedCommunicateItem(allTurnsItems(page.Turns))
	if !ok {
		t.Fatalf("paged read must render the mid-window unpaired communicate (parity with full read); items: %+v", allTurnsItems(page.Turns))
	}
	if pageFlushed.Text != fullFlushed.Text {
		t.Errorf("paged flushed Text = %q, want %q (full read parity)", pageFlushed.Text, fullFlushed.Text)
	}
	if pageFlushed.ID != fullFlushed.ID {
		t.Errorf("paged flushed ID = %q, want %q (full read parity)", pageFlushed.ID, fullFlushed.ID)
	}

	// Item-window LARGE (Limit=40): before the fix, G1 was never projected
	// (not a range, not trailing), so reg was never seeded and the tail flush
	// rendered nothing.
	window, _, err := NewTurnCache().LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_mid_orphaned",
		Limit:     40,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile: %v", err)
	}
	var windowFlushed appwire.ThreadItem
	for _, c := range window.Candidates {
		if item, ok := findAnyFlushedCommunicateItem([]appwire.ThreadItem{c.Item}); ok {
			windowFlushed = item
		}
	}
	if windowFlushed.Type == "" {
		t.Fatalf("item-window read must render the mid-window unpaired communicate (parity with full read); candidates: %+v", candidatesToItems(window.Candidates))
	}
	if windowFlushed.Text != fullFlushed.Text {
		t.Errorf("item-window flushed Text = %q, want %q (full read parity)", windowFlushed.Text, fullFlushed.Text)
	}
	if windowFlushed.ID != fullFlushed.ID {
		t.Errorf("item-window flushed ID = %q, want %q (full read parity)", windowFlushed.ID, fullFlushed.ID)
	}
}
