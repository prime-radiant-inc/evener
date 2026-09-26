package apptranscript

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// TestItemWindowFlushesPreludeOnlyZeroItemGroupCommunicate verifies that the
// item-window paged path (LatestItemWindowFromFile — the production item-paging
// API the hub uses) renders the flushed agentMessage for a prelude-only
// zero-item group, matching the full read and the turn-paged path.
//
// The prelude-only fixture's sole content after the prelude is a text-less
// assistant turn carrying a single deferred, unpaired communicate call
// (call_prelude_zero). The full read projects every record before dropping
// empty groups and flushes the unpaired communicate, rendering the delivered
// message. Before the fix, the item-window path dropped zero-item groups
// entirely (indexedItemRanges skipped them; projectIndexedItemRangesContext
// skipped them), so reg was never seeded, the tail flush rendered nothing, and
// with zero ranges (prelude-only) there was no flush site at all.
//
// RED at 51ab28db9a: LatestItemWindowFromFile returns no flushed agentMessage.
// GREEN at head: the trailing zero-item group is projected before the tail
// flush, seeding reg; the flushed agentMessage appears among the candidates.
func TestItemWindowFlushesPreludeOnlyZeroItemGroupCommunicate(t *testing.T) {
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

	// Item-window bounded read (LatestItemWindowFromFile — the production
	// item-paging path). Before the fix, zero-item groups were dropped entirely
	// and the tail flush rendered nothing.
	window, _, err := NewTurnCache().LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_prelude_zero_item",
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
		t.Fatalf("item-window read must render the prelude-only unpaired communicate (parity with full read); candidates: %+v", candidatesToItems(window.Candidates))
	}
	if windowFlushed.Text != fullFlushed.Text {
		t.Errorf("item-window flushed Text = %q, want %q (full read parity)", windowFlushed.Text, fullFlushed.Text)
	}
	if windowFlushed.ID != fullFlushed.ID {
		t.Errorf("item-window flushed ID = %q, want %q (full read parity)", windowFlushed.ID, fullFlushed.ID)
	}
}

// candidatesToItems extracts the Item field from each candidate for debugging.
func candidatesToItems(candidates []appitempaging.TranscriptItemCandidate) []appwire.ThreadItem {
	items := make([]appwire.ThreadItem, 0, len(candidates))
	for _, c := range candidates {
		items = append(items, c.Item)
	}
	return items
}
