package agent

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
)

// TestToolNameListsCannotCrowdOutUsageOnAnySurface pins that the tool-name
// lists alone cannot cost any surface its usage or worktree details.
//
// An MCP-heavy child registers hundreds of tool names, and those names ride in
// the terminal packet's metadata. The Activity tree stripped them before
// measuring the 16 KiB cap, so a long list could not crowd out the usage and
// worktree details it renders. The compact delegate read and the pushed update
// frame did NOT strip first: they measured the raw document, gave up when it was
// over, and dropped usage and worktree — for the same packet, at the same
// revision. So one reader showed a row with usage and a second showed the same
// row without it.
//
// All three read one projection now. This test pins that the tool lists alone
// cannot cost any surface its usage or worktree details.
func TestToolNameListsCannotCrowdOutUsageOnAnySurface(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

	// A realistic MCP namespace: hundreds of names, which is what a heavy child
	// registers. These are all the metadata needs to exceed the cap — the
	// details below them are small and must survive.
	names := make([]string, 0, 600)
	for i := range 600 {
		names = append(names, "plugin_team_tools_svc__tool_"+strings.Repeat("x", 20)+string(rune('a'+i%26)))
	}
	metadata, err := json.Marshal(delegateTerminalPacketMetadata{
		ObservedTools:   &names,
		Tools:           names,
		CumulativeUsage: &schema.CumulativeUsage{InputTokens: 7, OutputTokens: 9, CacheReadTokens: 1, TotalTokens: 17},
		Worktree:        &delegateTerminalWorktreeReport{Path: "/tmp/wt", Branch: "b", HeadSHA: "abc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) <= activityMaxDelegatePayloadBytes {
		t.Fatalf("fixture metadata is %d bytes, want it over the %d-byte cap", len(metadata), activityMaxDelegatePayloadBytes)
	}

	row := stableActivitySnapshot("dlg_1", "root", "child", "brief")
	row.latestPacket = &delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Metadata: metadata}

	// The pushed update frame.
	frame := delegateFrameStatusInfo(at, "root", row)
	if frame.Usage == nil {
		t.Errorf("the pushed frame dropped Usage because the tool list was long")
	}
	if frame.Worktree == nil {
		t.Errorf("the pushed frame dropped Worktree because the tool list was long")
	}

	// The compact delegate read, through the capture the compact endpoint calls.
	// delegateStatusInfoFromSnapshot is the UNCAPPED diagnostics path, so using
	// it here would let this test pass while the real compact read dropped the
	// same details.
	aggregate := &delegatestore.Aggregate{
		DelegateID: "dlg_1",
		Descriptor: delegatestore.Descriptor{ChildSessionID: "child", OwnerSessionID: "root", Task: "brief"},
		Phase:      delegatestore.PhaseIdle,
		LatestPacket: &delegatestore.TerminalPacket{
			Kind:     delegatestore.PacketReported,
			Metadata: metadata,
		},
	}
	read := captureSessionActivityDelegate("root", nil, aggregate, at).project()
	if read.Usage == nil {
		t.Errorf("the compact activity read dropped Usage because the tool list was long")
	}
	if read.Worktree == nil {
		t.Errorf("the compact activity read dropped Worktree because the tool list was long")
	}

	// The Activity tree.
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		Delegates: map[string]delegateSnapshot{"dlg_1": row},
	}
	delegate := projectActivitySession(snap, newActivityBudget()).Entries[0].Delegate
	if delegate.Usage == nil {
		t.Errorf("the Activity tree dropped Usage because the tool list was long")
	}
	if delegate.Worktree == nil {
		t.Errorf("the Activity tree dropped Worktree because the tool list was long")
	}

	// And the details the projection keeps are the real ones, not zero values.
	if frame.Usage != nil && frame.Usage.InputTokens != 7 {
		t.Errorf("frame Usage.InputTokens = %d, want 7", frame.Usage.InputTokens)
	}
	if delegate.Worktree != nil && delegate.Worktree.Branch != "b" {
		t.Errorf("activity Worktree.Branch = %q, want %q", delegate.Worktree.Branch, "b")
	}
}

// TestCompactActivityDoesNotProjectMetadataUnderTheControllerLock pins WHERE
// the terminal-metadata projection runs, not what it returns.
//
// The compact Activity read holds the delegate controller mutex while it
// captures each row. The projection decodes the packet's metadata, which can
// carry a long task brief and a tool-name list of hundreds of entries, so
// running it inside that loop made a page of delegates copy and measure their
// documents while every start, stop and settlement waited on the lock. That is
// a latency defect no assertion about the returned bytes can see: the row comes
// out the same either way.
//
// So this holds the mutex and asserts the capture alone does not project, then
// releases it and asserts project() does. A future refactor that folds the
// projection back into capture — for tidiness, most likely — fails here.
func TestCompactActivityDoesNotProjectMetadataUnderTheControllerLock(t *testing.T) {
	t.Parallel()
	names := make([]string, 0, 8)
	for i := range 8 {
		names = append(names, "plugin_team_tools_svc__tool_"+string(rune('a'+i)))
	}
	metadata, err := json.Marshal(delegateTerminalPacketMetadata{
		ObservedTools:   &names,
		Tools:           names,
		CumulativeUsage: &schema.CumulativeUsage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
		Worktree:        &delegateTerminalWorktreeReport{Path: "/tmp/wt", Branch: "b", HeadSHA: "abc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate := &delegatestore.Aggregate{
		DelegateID: "dlg_1",
		Descriptor: delegatestore.Descriptor{ChildSessionID: "child", OwnerSessionID: "root", Task: "brief"},
		Phase:      delegatestore.PhaseIdle,
		LatestPacket: &delegatestore.TerminalPacket{
			Kind:     delegatestore.PacketReported,
			Metadata: metadata,
		},
	}

	candidate := captureSessionActivityDelegate("root", nil, aggregate, time.Now().UTC())
	if candidate.row.Usage != nil || candidate.row.Worktree != nil {
		t.Fatalf("the capture projected the metadata itself; that work belongs to project(), after the controller mutex is released")
	}
	if len(candidate.metadata) == 0 {
		t.Fatal("the capture did not take the metadata with the row, so the projection after unlock has nothing to read")
	}
	// A REFERENCE, not a copy. The packet is durable and immutable, and copying
	// its bytes here allocates the whole document — briefs and tool lists
	// included — inside the mutex, for a document the bounded projection may then
	// reject. Sharing the backing array is the identity the finding asks for: the
	// captured slice must be the packet's own slice, not a duplicate of it.
	if len(aggregate.LatestPacket.Metadata) > 0 && &candidate.metadata[0] != &aggregate.LatestPacket.Metadata[0] {
		t.Error("the capture copied the metadata bytes under the controller mutex; it must hold a read-only reference to the immutable packet and let the projection after the unlock bound it")
	}

	projected := candidate.project()
	if projected.Usage == nil {
		t.Error("the projection after unlock dropped Usage")
	}
	if projected.Worktree == nil {
		t.Error("the projection after unlock dropped Worktree")
	}
}

// TestDroppedToolListCostsNoAllocation is the allocation half of the metadata
// finding, and it is the one assertion that can see it.
//
// The scanner runs once per captured row inside the live delegate controller
// mutex, so every byte it copies there is copied while starts, stops and
// settlement wait. The values it DROPS are the largest members in the document —
// an MCP-heavy child registers hundreds of names — so a scanner that decodes
// them before discarding them allocates the whole list inside that section for a
// value nothing reads.
//
// Return values cannot show this: dropping a list that was copied and dropping
// one that was skipped produce the same output. Allocated BYTES can, and that is
// the quantity at stake. The two documents below differ only in the size of the
// tool list, which is dropped either way, so the bytes the scan allocates must
// not grow with it.
func TestDroppedToolListCostsNoAllocation(t *testing.T) {
	// Not parallel: this reads process-wide allocation counters.
	build := func(names int) json.RawMessage {
		list := make([]string, 0, names)
		for i := range names {
			list = append(list, "plugin_team_tools_svc__tool_"+strings.Repeat("x", 24)+string(rune('a'+i%26)))
		}
		encoded, err := json.Marshal(map[string]any{
			"tools":   list,
			"summary": "kept",
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return encoded
	}
	small, large := build(4), build(400)
	if len(large) < 4*len(small) {
		t.Fatalf("the large fixture is %d bytes and the small one %d; it must be much larger to show scaling", len(large), len(small))
	}

	// Bytes allocated per scan, scraped from the process counters around a fixed
	// number of runs. The scan is deterministic and single-goroutine here, so the
	// delta is the scan's own allocation.
	measure := func(payload json.RawMessage) uint64 {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for range 20 {
			if _, changed := stripDelegateToolLists(payload); !changed {
				t.Fatalf("the tool list was not dropped")
			}
		}
		runtime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / 20
	}
	smallBytes, largeBytes := measure(small), measure(large)
	// A scan that materializes the list it drops allocates proportionally to the
	// list, so the large document would cost several times the small one. Allow
	// slack for the map iteration and buffer growth that both documents share;
	// what must not happen is scaling with the list.
	if largeBytes > smallBytes+uint64(len(large)-len(small))/2 {
		t.Errorf("scanning a %d-byte tool list allocated %d bytes against %d for a %d-byte one, so about %d bytes of the dropped list are being materialized inside the controller lock", len(large), largeBytes, smallBytes, len(small), largeBytes-smallBytes)
	}
}
