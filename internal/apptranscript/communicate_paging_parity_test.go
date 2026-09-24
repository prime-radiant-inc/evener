package apptranscript

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Round 5: paged/indexed reads built a fresh ToolCallRegistry per transcript
// entry, so the deferred communicate bytes (CommRawArgs) the assistant turn
// seeds never reached the paired result turn on those paths — rejected and
// healed communicates rendered correctly only on the full read. These tests
// hold the bounded reads to the full read's contract.

// rejectedCommunicateFixture persists one logical turn containing a rejected
// communicate call. A user input opens the group; an assistant turn issues a
// communicate call with malformed raw bytes (Arguments={}, RawArguments set)
// alongside visible text; the paired tool result rejects it
// (IsError=true, PrevalOnly=true); a final assistant turn closes the turn. The
// full read defers the raw bytes from the assistant turn to the result turn and
// renders a commandExecution error item. Before the fix the bounded reads
// rebuilt the registry per entry, so CommRawArgs was empty and the error item
// vanished on those paths.
func rejectedCommunicateFixture() []transcript.Entry {
	const rawArgs = `{message: "hello"}` // malformed JSON — bare key
	return []transcript.Entry{
		userEntry(1, "do the thing"),
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "thinking about it"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:           "call_comm_rej",
				Name:         "communicate",
				Arguments:    json.RawMessage(`{}`),
				RawArguments: rawArgs,
			},
			}}}}},
		{Kind: "entry", Seq: 3, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call_comm_rej",
				Name:       "communicate",
				IsError:    true,
				PrevalOnly: true,
			}},
		}}}},
		assistantTextEntry(4, "all done"),
	}
}

// findCommunicateErrorItem returns the rejected-communicate commandExecution
// error item from a slice of items, if present.
func findCommunicateErrorItem(items []appwire.ThreadItem) (appwire.ThreadItem, bool) {
	for _, item := range items {
		if item.Type == "commandExecution" && item.ToolName == "communicate" && item.PrevalOnly {
			return item, true
		}
	}
	return appwire.ThreadItem{}, false
}

func assertCommunicateErrorItemMatches(t *testing.T, got, want appwire.ThreadItem, label string) {
	t.Helper()
	if got.Status != want.Status {
		t.Errorf("%s communicate error Status = %q, want %q", label, got.Status, want.Status)
	}
	if got.ArgumentsJSON != want.ArgumentsJSON {
		t.Errorf("%s communicate error ArgumentsJSON = %q, want %q", label, got.ArgumentsJSON, want.ArgumentsJSON)
	}
	if got.TranscriptKey != want.TranscriptKey {
		t.Errorf("%s communicate error TranscriptKey = %q, want %q", label, got.TranscriptKey, want.TranscriptKey)
	}
	if got.PrevalOnly != want.PrevalOnly {
		t.Errorf("%s communicate error PrevalOnly = %v, want %v", label, got.PrevalOnly, want.PrevalOnly)
	}
}

// TestPagedReadsRenderRejectedCommunicateParityWithFullRead is the round-5
// parity oracle: the same session projected via the full read and via the
// bounded (turn-paged and item-window) reads must render the rejected
// communicate identically — a commandExecution error item present in ALL of
// them, not just the full read. Matching TranscriptKeys also pins
// ItemCount/position consistency: the index scan's per-record counts must agree
// with the seeded full read, or the bounded read's merged count diverges and the
// candidate's key shifts.
func TestPagedReadsRenderRejectedCommunicateParityWithFullRead(t *testing.T) {
	path := writeEntries(t, rejectedCommunicateFixture()...)

	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	if len(full) != 1 {
		t.Fatalf("full read produced %d turns, want 1 logical turn", len(full))
	}
	fullItem, ok := findCommunicateErrorItem(full[0].Items)
	if !ok {
		t.Fatalf("full read must render the rejected communicate as a commandExecution error item; items: %+v", full[0].Items)
	}
	wantKeys := keysFor(full[0])

	// Turn-paging bounded read (projectIndexedGroup path).
	page := requirePageFromFile(t, NewTurnCache(), path, testMaxLineBytes, "", 50, boundedTestProjector)
	if len(page.Turns) != 1 {
		t.Fatalf("paged read produced %d turns, want 1 logical turn", len(page.Turns))
	}
	pageItem, ok := findCommunicateErrorItem(page.Turns[0].Items)
	if !ok {
		t.Fatalf("paged read must render the rejected communicate (parity with full read); items: %+v", page.Turns[0].Items)
	}
	assertCommunicateErrorItemMatches(t, pageItem, fullItem, "paged")
	if got := keysFor(page.Turns[0]); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("paged keys = %v, want full read keys %v", got, wantKeys)
	}

	// Item-window bounded read (projectIndexedItemRanges path).
	window, _, err := NewTurnCache().LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_comm_parity",
		Limit:     40,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile: %v", err)
	}
	var windowKeys []string
	var windowItem appwire.ThreadItem
	for _, c := range window.Candidates {
		windowKeys = append(windowKeys, c.Item.TranscriptKey)
		if c.Item.Type == "commandExecution" && c.Item.ToolName == "communicate" && c.Item.PrevalOnly {
			windowItem = c.Item
		}
	}
	if windowItem.Type == "" {
		t.Fatalf("item-window read must render the rejected communicate (parity with full read); candidates: %+v", window.Candidates)
	}
	assertCommunicateErrorItemMatches(t, windowItem, fullItem, "item-window")
	if !reflect.DeepEqual(windowKeys, wantKeys) {
		t.Errorf("item-window keys = %v, want full read keys %v", windowKeys, wantKeys)
	}
}

// TestItemWindowResolvesDeferredCommunicateAcrossPageBoundary proves the
// deferred communicate state survives a page boundary that splits the
// assistant turn (which seeds CommRawArgs) from its paired result turn. The
// item window is walked one item at a time so the rejected communicate's error
// item — produced by the result turn — lands on a page of its own, with the
// seeding assistant turn's own item on a different page. The error item must
// still carry the deferred raw bytes.
func TestItemWindowResolvesDeferredCommunicateAcrossPageBoundary(t *testing.T) {
	path := writeEntries(t, rejectedCommunicateFixture()...)
	const rawArgs = `{message: "hello"}`

	cache := NewTurnCache()
	window, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_comm_boundary",
		Limit:     1,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile: %v", err)
	}
	// Walk oldest-to-newest, one item per page.
	var pages [][]appwire.ThreadItem
	for {
		if len(window.Candidates) > 0 {
			pages = append([][]appwire.ThreadItem{{window.Candidates[0].Item}}, pages...)
		}
		if window.OlderCursor == "" {
			break
		}
		window, _, err = cache.PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
			ThreadRef: "local:th_comm_boundary",
			Cursor:    window.OlderCursor,
			Limit:     1,
		}, boundedTestProjector)
		if err != nil {
			t.Fatalf("PreviousItemWindowFromFile: %v", err)
		}
	}
	// The fixture has four items across one logical turn: user message,
	// assistant text, the rejected-communicate error item, and a final
	// assistant text. Each occupies its own one-item page.
	if len(pages) != 4 {
		t.Fatalf("expected 4 one-item pages, got %d", len(pages))
	}
	var errorItem appwire.ThreadItem
	found := false
	for _, p := range pages {
		if item, ok := findCommunicateErrorItem(p); ok {
			errorItem = item
			found = true
		}
	}
	if !found {
		t.Fatalf("no one-item page rendered the rejected communicate; pages: %+v", pages)
	}
	if errorItem.ArgumentsJSON != rawArgs {
		t.Errorf("page-boundary error item ArgumentsJSON = %q, want deferred raw bytes %q", errorItem.ArgumentsJSON, rawArgs)
	}
	if errorItem.Status != appwire.TurnStatusFailed {
		t.Errorf("page-boundary error item Status = %q, want %q", errorItem.Status, appwire.TurnStatusFailed)
	}
	if !errorItem.PrevalOnly {
		t.Error("page-boundary error item PrevalOnly = false, want true")
	}
}

// TestIncrementalAppendResolvesDeferredCommunicateOnResume proves the
// incremental index scan reconstructs CommRawArgs when a group's opener (the
// assistant turn that seeds the deferred communicate bytes) lives in the
// previously indexed prefix and the paired result turn is appended later.
// Before the fix, the resumed scan initialized openReg with empty CommRawArgs,
// so the result turn missed the deferred bytes and its ItemCount diverged
// from the full read (no error item rendered).
func TestIncrementalAppendResolvesDeferredCommunicateOnResume(t *testing.T) {
	const rawArgs = `{message: "hello"}` // malformed JSON — bare key
	// Group opener: user + assistant turn with the communicate call.
	// These are indexed first (cold read).
	opener := []transcript.Entry{
		userEntry(1, "do the thing"),
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "thinking about it"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:           "call_comm_rej",
				Name:         "communicate",
				Arguments:    json.RawMessage(`{}`),
				RawArguments: rawArgs,
			},
			}}}}},
	}
	path := writeEntries(t, opener...)
	cache := NewTurnCache()
	// Cold read: builds the index over the opener records.
	requireLatestFromFile(t, cache, path, testMaxLineBytes, 10, boundedTestProjector)

	// Append the result turn (rejects the communicate) and a closing
	// assistant text turn.
	suffix := []transcript.Entry{
		{Kind: "entry", Seq: 3, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call_comm_rej",
				Name:       "communicate",
				IsError:    true,
				PrevalOnly: true,
			}},
		}}}},
		assistantTextEntry(4, "all done"),
	}
	for _, e := range suffix {
		appendFile(t, path, marshalEntryLine(t, e))
	}

	// Warm append: the scan resumes from the indexed prefix and must
	// reconstruct CommRawArgs so the result turn renders the error item.
	got, _ := requireLatestFromFile(t, cache, path, testMaxLineBytes, 10, boundedTestProjector)
	if len(got) != 1 {
		t.Fatalf("warm append produced %d turns, want 1 logical turn", len(got))
	}
	item, ok := findCommunicateErrorItem(got[0].Items)
	if !ok {
		t.Fatalf("warm append must render the rejected communicate error item (CommRawArgs reconstructed on resume); items: %+v", got[0].Items)
	}
	if item.ArgumentsJSON != rawArgs {
		t.Errorf("resume error item ArgumentsJSON = %q, want deferred raw bytes %q", item.ArgumentsJSON, rawArgs)
	}
	if item.Status != appwire.TurnStatusFailed {
		t.Errorf("resume error item Status = %q, want %q", item.Status, appwire.TurnStatusFailed)
	}
	if !item.PrevalOnly {
		t.Error("resume error item PrevalOnly = false, want true")
	}

	// Full read parity: the resumed/merged index must agree with a fresh
	// full read of the same transcript.
	fresh := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	if len(fresh) != 1 {
		t.Fatalf("full read produced %d turns, want 1", len(fresh))
	}
	freshItem, ok := findCommunicateErrorItem(fresh[0].Items)
	if !ok {
		t.Fatalf("full read must render the rejected communicate error item; items: %+v", fresh[0].Items)
	}
	assertCommunicateErrorItemMatches(t, item, freshItem, "resume-vs-full")
}

// unpairedCommunicateFixture persists one logical turn whose assistant entry
// issues a communicate call with no paired result turn: the communicate is
// unpaired at end-of-transcript. The full read's FlushUnpairedCommunicates
// and the bounded turn-window path's projectIndexedGroup both flush it as an
// agentMessage; the bounded item-window path (projectIndexedItemRangesContext)
// must do the same.
func unpairedCommunicateFixture() []transcript.Entry {
	return []transcript.Entry{
		userEntry(1, "do the thing"),
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "thinking about it"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call_comm_unpaired",
				Name:      "communicate",
				Arguments: json.RawMessage(`{"message":"hello there"}`),
			}},
		}}}},
	}
}

// findFlushedCommunicateItem returns the flushed communicate agentMessage item
// from a slice of items, if present. The flushed item's ID is
// "item_assistant_flushed_" + callID.
func findFlushedCommunicateItem(items []appwire.ThreadItem) (appwire.ThreadItem, bool) {
	for _, item := range items {
		if item.Type == "agentMessage" && item.ID == "item_assistant_flushed_call_comm_unpaired" {
			return item, true
		}
	}
	return appwire.ThreadItem{}, false
}

// TestItemWindowFlushesUnpairedCommunicate proves the bounded item-window read
// (projectIndexedItemRangesContext) flushes unpaired communicates, matching the
// full read's FlushUnpairedCommunicates and the bounded turn-window path's
// projectIndexedGroup. Without the flush, the item-window read silently drops
// the flushed agentMessage — the text item is the only candidate, and the
// communicate's deferred bytes never render.
func TestItemWindowFlushesUnpairedCommunicate(t *testing.T) {
	path := writeEntries(t, unpairedCommunicateFixture()...)

	// Full read with flush: project with a shared registry (the way the
	// server's appTurnProjectionFromTranscriptFile does), then flush.
	reg := NewToolCallRegistry()
	fullProjector := func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		return boundedTestProjector(turn, turnID, turnIndex, reg)
	}
	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, fullProjector)
	FlushUnpairedCommunicates(&full, reg)
	if len(full) != 1 {
		t.Fatalf("full read produced %d turns, want 1 logical turn", len(full))
	}
	fullFlushed, ok := findFlushedCommunicateItem(full[0].Items)
	if !ok {
		t.Fatalf("full read with flush must render the unpaired communicate; items: %+v", full[0].Items)
	}
	if fullFlushed.Text != "hello there" {
		t.Fatalf("full read flushed communicate Text = %q, want %q", fullFlushed.Text, "hello there")
	}
	wantKeys := keysFor(full[0])

	// Turn-paging bounded read (projectIndexedGroup path — already flushes).
	page := requirePageFromFile(t, NewTurnCache(), path, testMaxLineBytes, "", 50, boundedTestProjector)
	if len(page.Turns) != 1 {
		t.Fatalf("paged read produced %d turns, want 1 logical turn", len(page.Turns))
	}
	pageFlushed, ok := findFlushedCommunicateItem(page.Turns[0].Items)
	if !ok {
		t.Fatalf("paged read must render the unpaired communicate (parity with full read); items: %+v", page.Turns[0].Items)
	}
	if pageFlushed.Text != fullFlushed.Text {
		t.Errorf("paged flushed Text = %q, want %q", pageFlushed.Text, fullFlushed.Text)
	}
	if got := keysFor(page.Turns[0]); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("paged keys = %v, want full read keys %v", got, wantKeys)
	}

	// Item-window bounded read (projectIndexedItemRangesContext path).
	window, _, err := NewTurnCache().LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_comm_unpaired",
		Limit:     40,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile: %v", err)
	}
	var windowKeys []string
	var windowFlushed appwire.ThreadItem
	for _, c := range window.Candidates {
		windowKeys = append(windowKeys, c.Item.TranscriptKey)
		if item, ok := findFlushedCommunicateItem([]appwire.ThreadItem{c.Item}); ok {
			windowFlushed = item
		}
	}
	if windowFlushed.Type == "" {
		t.Fatalf("item-window read must render the unpaired communicate (parity with full read); candidates: %+v", window.Candidates)
	}
	if windowFlushed.Text != fullFlushed.Text {
		t.Errorf("item-window flushed Text = %q, want %q", windowFlushed.Text, fullFlushed.Text)
	}
	if !reflect.DeepEqual(windowKeys, wantKeys) {
		t.Errorf("item-window keys = %v, want full read keys %v", windowKeys, wantKeys)
	}
}

// TestIncrementalAppendSuppressesEchoedCommunicateOnResume proves the
// incremental index scan reconstructs LastAssistantText when a group's
// opener (the assistant turn that seeds the deferred communicate bytes AND
// sets LastAssistantText) lives in the previously indexed prefix and the
// paired result turn is appended later. The healed-communicate branch
// (apptranscript.go ProjectTurn) suppresses an echoed agentMessage when the
// healed message equals the assistant text the model already showed
// (EchoesAssistantText(reg.LastAssistantText, msg)). The full read's single
// shared registry carries LastAssistantText across records, so the echo is
// suppressed. Before the fix, the resumed scan's openReg had empty
// LastAssistantText (replayCommRawArgs returned only CommRawArgs), so the
// echo was NOT suppressed — the result record's ItemCount was +1 vs the full
// read, the item-window deficit check fired, and the resumed item-window
// read hard-errored.
func TestIncrementalAppendSuppressesEchoedCommunicateOnResume(t *testing.T) {
	const rawArgs = `{message: "the answer"}` // malformed JSON — bare key, healed
	// Group opener: user + assistant turn that shows "the answer" as visible
	// text AND issues a communicate call whose raw bytes echo that same text.
	// The communicate is healed (Arguments={}, RawArguments=rawArgs); when the
	// paired result arrives (IsError=false), the healed message "the answer"
	// matches the assistant text — the full read suppresses the echo.
	opener := []transcript.Entry{
		userEntry(1, "do the thing"),
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "the answer"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:           "call_comm_echo",
				Name:         "communicate",
				Arguments:    json.RawMessage(`{}`),
				RawArguments: rawArgs,
			},
			}}}}},
	}
	path := writeEntries(t, opener...)
	cache := NewTurnCache()
	// Cold read: builds the index over the opener records.
	if _, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_comm_echo",
		Limit:     40,
	}, boundedTestProjector); err != nil {
		t.Fatalf("cold: %v", err)
	}

	// Append the result turn (healed, IsError=false, no PrevalOnly) and a
	// closing assistant text turn. The result turn's healed communicate
	// message echoes the assistant text — the full read suppresses it.
	suffix := []transcript.Entry{
		{Kind: "entry", Seq: 3, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call_comm_echo",
				Name:       "communicate",
				IsError:    false,
			}},
		}}}},
		assistantTextEntry(4, "all done"),
	}
	for _, e := range suffix {
		appendFile(t, path, marshalEntryLine(t, e))
	}

	// Warm resume: the item-window read must NOT hard-error. Before the fix,
	// openReg.LastAssistantText was empty, the echo was not suppressed, the
	// result record's ItemCount was +1 vs the fresh full read, and the
	// deficit check (item_paging.go) returned a hard error.
	window, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_comm_echo",
		Limit:     40,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("warm resume item-window read hard-errored (echoed communicate deficit): %v", err)
	}

	// Fresh full read: the shared registry carries LastAssistantText, so the
	// echo is suppressed. The resumed scan must agree.
	fresh := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	if len(fresh) != 1 {
		t.Fatalf("full read produced %d turns, want 1", len(fresh))
	}

	// The echoed healed agentMessage must NOT appear in the resumed window's
	// candidates. The assistant's own ContentText "the answer" is a legitimate
	// agentMessage (one occurrence); the healed communicate echo would be a
	// SECOND agentMessage with the same text — the full read suppresses it via
	// EchoesAssistantText, and the resumed scan must match. Count the
	// occurrences: exactly 1 (the assistant text), not 2 (assistant text + echo).
	echoCount := 0
	for _, c := range window.Candidates {
		if c.Item.Type == "agentMessage" && c.Item.Text == "the answer" {
			echoCount++
		}
	}
	if echoCount != 1 {
		t.Errorf("resume agentMessage count for %q = %d, want 1 (assistant text only, echo suppressed)", "the answer", echoCount)
	}

	// Parity: the resumed window's candidate keys must match the fresh full
	// read's turn keys.
	wantKeys := keysFor(fresh[0])
	var windowKeys []string
	for _, c := range window.Candidates {
		windowKeys = append(windowKeys, c.Item.TranscriptKey)
	}
	if !reflect.DeepEqual(windowKeys, wantKeys) {
		t.Errorf("resume keys = %v, want full read keys %v", windowKeys, wantKeys)
	}
}
