package apptranscript

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/llm"
)

func TestIndexedItemPaging(t *testing.T) {
	parts := make([]llm.ContentPart, 0, 45)
	for i := range 45 {
		parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: fmt.Sprintf("item-%02d", i)})
	}
	path := writeEntries(t,
		transcript.Entry{Kind: "entry", Seq: 1, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: parts}}},
		transcript.Entry{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant}},
		transcript.Entry{Kind: "entry", Seq: 3, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "suppressed", Name: "communicate", Arguments: []byte(`{"message":""}`)}}}}}},
	)

	cache := NewTurnCache()
	latest, identity, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:test", Limit: 40}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.Candidates) != 40 {
		t.Fatalf("latest candidates=%d, want 40", len(latest.Candidates))
	}
	if latest.Candidates[0].Item.Text != "item-05" || latest.Candidates[39].Item.Text != "item-44" {
		t.Fatalf("latest range=%q..%q, want item-05..item-44", latest.Candidates[0].Item.Text, latest.Candidates[39].Item.Text)
	}
	if latest.OlderCursor == "" {
		t.Fatal("latest page has no older cursor")
	}
	previous, _, err := cache.PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:test", Cursor: latest.OlderCursor, Limit: 40}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if len(previous.Candidates) != 5 {
		t.Fatalf("previous candidates=%d, want 5", len(previous.Candidates))
	}
	seen := make(map[string]bool, 45)
	for _, candidate := range append(latest.Candidates, previous.Candidates...) {
		if seen[candidate.Item.TranscriptKey] {
			t.Fatalf("duplicate transcript key %q", candidate.Item.TranscriptKey)
		}
		seen[candidate.Item.TranscriptKey] = true
	}
	if len(seen) != 45 || identity.ProjectionVersion != appitempaging.TranscriptItemProjectionVersion {
		t.Fatalf("seen=%d identity=%+v", len(seen), identity)
	}
}

func TestProjectedItemPositions(t *testing.T) {
	path := writeEntries(t,
		transcript.Entry{Kind: "entry", Seq: 1, Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("first")}},
		transcript.Entry{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "visible"},
			{Kind: llm.ContentText, Text: ""},
		}}}},
	)
	window, _, err := NewTurnCache().LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:positions", Limit: 40}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Candidates) != 2 {
		t.Fatalf("candidates=%d, want 2", len(window.Candidates))
	}
	if got := window.Candidates[0].Position; got != (appwire.ThreadItemPosition{Entry: 0, Item: 0}) {
		t.Fatalf("first position=%+v, want entry 0 item 0", got)
	}
	if got := window.Candidates[1].Position; got != (appwire.ThreadItemPosition{Entry: 0, Item: 1}) {
		t.Fatalf("second position=%+v, want entry 0 item 1 (same logical turn)", got)
	}
	if window.Candidates[0].Item.TranscriptKey == window.Candidates[1].Item.TranscriptKey {
		t.Fatal("projected items reused transcript key")
	}
}

func TestItemPagingCompletenessFlagsAreTurnScoped(t *testing.T) {
	path := writeEntries(t, userEntry(1, "first"), userEntry(2, "second"))
	window, _, err := NewTurnCache().LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:flags", Limit: 40}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Candidates) != 2 {
		t.Fatalf("candidates=%d, want 2", len(window.Candidates))
	}
	for i, candidate := range window.Candidates {
		if candidate.HasEarlierItems || candidate.HasLaterItems {
			t.Fatalf("candidate %d flags earlier=%v later=%v, want false/false for a one-item turn", i, candidate.HasEarlierItems, candidate.HasLaterItems)
		}
	}
}

func TestItemPagingInteriorRewriteThenAppendRotatesGeneration(t *testing.T) {
	const itemText = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	entries := make([]transcript.Entry, 0, 80)
	for seq := 1; seq <= 80; seq++ {
		entries = append(entries, userEntry(seq, itemText))
	}
	path := writeEntries(t, entries...)
	cache := NewTurnCache()
	first, before, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:rewrite", Limit: 1}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if first.OlderCursor == "" {
		t.Fatal("initial page has no cursor")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oldLine := marshalEntryLine(t, userEntry(40, itemText))
	newLine := marshalEntryLine(t, userEntry(40, strings.Repeat("y", len(itemText))))
	if len(oldLine) != len(newLine) {
		t.Fatalf("rewrite changed line size: old=%d new=%d", len(oldLine), len(newLine))
	}
	offset := bytes.Index(raw, oldLine)
	if offset < 256 || offset+len(oldLine) > len(raw)-256 {
		t.Fatalf("interior rewrite offset=%d line=%d transcript=%d is not outside sparse anchors", offset, len(oldLine), len(raw))
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt(newLine, int64(offset)); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, marshalEntryLine(t, userEntry(81, itemText)))

	_, after, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:rewrite", Limit: 1}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if after.Incarnation == "" || after.Incarnation == before.Incarnation {
		t.Fatalf("rewrite-plus-append preserved incarnation: before=%q after=%q", before.Incarnation, after.Incarnation)
	}
	if _, _, err := cache.PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:rewrite", Cursor: first.OlderCursor, Limit: 1}, boundedTestProjector); err == nil {
		t.Fatal("old cursor remained valid after interior rewrite plus append")
	}
}

func TestIndexedItemPagingDoesNotProjectHistoricalPrefix(t *testing.T) {
	path := writeNumberedTranscript(t, 100)
	var observed ReadStats
	restore := InstallReadObserverForTesting(func(stats ReadStats) { observed = stats })
	t.Cleanup(restore)
	window, _, err := NewTurnCache().LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:bounded-items", Limit: 2}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Candidates) != 2 {
		t.Fatalf("candidates=%d, want 2", len(window.Candidates))
	}
	if observed.ProjectedTurns != 2 || observed.ProjectedItems != 2 {
		t.Fatalf("projected stats=%+v, want exactly the selected two records/items", observed)
	}
}

func TestItemPagingGeneration(t *testing.T) {
	path := writeEntries(t, userEntry(1, "one"), userEntry(2, "two"))
	cache := NewTurnCache()
	first, identity, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:generation", Limit: 1}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	index, err := readTurnIndex(path + ".appwire-index.json")
	if err != nil {
		t.Fatal(err)
	}
	if index.Incarnation == "" {
		t.Fatal("item index did not persist an incarnation")
	}
	appendFile(t, path, marshalEntryLine(t, userEntry(3, "three")))
	_, appendedIdentity, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:generation", Limit: 1}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if appendedIdentity.Incarnation != identity.Incarnation {
		t.Fatalf("append rotated incarnation: before=%q after=%q", identity.Incarnation, appendedIdentity.Incarnation)
	}
	previous, _, err := cache.PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:generation", Cursor: first.OlderCursor, Limit: 1}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if len(previous.Candidates) != 1 || previous.Candidates[0].Item.Text != "one" {
		t.Fatalf("append changed cursor boundary: previous=%+v", previous.Candidates)
	}
	if err := os.Remove(path + ".appwire-index.json"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".appwire-index.json.journal"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	_, rebuiltIdentity, err := NewTurnCache().LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:generation", Limit: 1}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if rebuiltIdentity.Incarnation == "" || rebuiltIdentity.Incarnation == appendedIdentity.Incarnation {
		t.Fatalf("rebuild did not rotate incarnation: %+v", rebuiltIdentity)
	}
	if _, _, err := NewTurnCache().PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:generation", Cursor: first.OlderCursor, Limit: 1}, boundedTestProjector); err == nil {
		t.Fatal("old cursor remained valid after index rebuild")
	}
}

func TestItemPagingToolPair(t *testing.T) {
	path := writeEntries(t,
		assistantToolCallEntry(1, "pair", "read_file", `{}`),
		userEntry(2, "middle"),
		toolResultEntry(3, "pair", "", "result"),
	)
	cache := NewTurnCache()
	latest, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:tools", Limit: 2}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.Candidates) != 2 || latest.Candidates[1].Item.CallID != "pair" {
		t.Fatalf("latest tool candidate=%+v", latest.Candidates)
	}
	previous, _, err := cache.PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:tools", Cursor: latest.OlderCursor, Limit: 1}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if len(previous.Candidates) != 1 || previous.Candidates[0].Item.CallID != "pair" {
		t.Fatalf("previous tool candidate=%+v", previous.Candidates)
	}
}

func TestItemPagingMissingResumeSidecarDoesNotDisablePaging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.transcript.jsonl")
	if err := os.WriteFile(path, transcriptHeaderLine(t), 0o644); err != nil {
		t.Fatal(err)
	}
	window, _, err := NewTurnCache().LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:resume", Limit: 40}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Candidates) != 0 {
		t.Fatalf("empty transcript candidates=%d", len(window.Candidates))
	}
}

func TestItemPagingCorruptResumeSidecarDoesNotDisableNonEmptyPaging(t *testing.T) {
	path := writeEntries(t, userEntry(1, "one"), userEntry(2, "two"), userEntry(3, "three"))
	if err := os.WriteFile(path+".resume.json", []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	window, _, err := NewTurnCache().LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:resume-nonempty", Limit: 2}, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Candidates) != 2 || window.Candidates[0].Item.Text != "two" || window.Candidates[1].Item.Text != "three" {
		t.Fatalf("paging with corrupt resume sidecar=%+v", window.Candidates)
	}
}

var _ appitempaging.TranscriptItemWindow
