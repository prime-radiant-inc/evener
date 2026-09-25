package server

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// historyReadTestCost prices "model-a" and nothing else.
var historyReadTestCost = &registry.Cost{Input: 3, Output: 15}

func historyReadPricer(model string) *registry.Cost {
	if model == "model-a" {
		return historyReadTestCost
	}
	return nil
}

func (hx *historyHarness) recordAssistant(t *testing.T, roundID, model, text string) {
	t.Helper()
	turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant(text))
	turn.RoundID, turn.Model = roundID, model
	turn.Usage = llm.Usage{InputTokens: 100_000, OutputTokens: 20_000, TotalTokens: 120_000}
	hx.recordTurn(t, turn)
}

// The latest window is the index's Latest regrouped into turn fragments, its
// snapshot the recorded length at projection, its turns priced by their model.
func TestHistoryReadLatestIsTheIndexWindowRegrouped(t *testing.T) {
	hx := newHistoryHarness(t)
	hx.history.cost = historyReadPricer
	hx.record(t, "one")
	hx.recordAssistant(t, "r_1", "model-a", "two")
	hx.record(t, "three")
	hx.recordAssistant(t, "r_2", "model-a", "four")
	recorded := hx.writer.RecordedLength()

	turns, older, snapshot, overlay, err := hx.history.latest(hx.history.capture(), "local:th_history", 3)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := hx.cache.Acquire(hx.path)
	if err != nil {
		t.Fatal(err)
	}
	window, err := idx.Latest(3)
	hx.cache.Release(idx)
	if err != nil {
		t.Fatal(err)
	}
	want, err := appitempaging.RegroupTurnFragments(appitempaging.NormalizeProjectedItemCompleteness(window.Candidates))
	if err != nil {
		t.Fatal(err)
	}
	priced := 0
	for i := range turns {
		if turns[i].Cost != "" {
			if turns[i].Cost != appwire.EstimateCost(historyReadTestCost, turns[i].Usage) {
				t.Fatalf("turn %s cost %q, want the model-a price of its usage", turns[i].ID, turns[i].Cost)
			}
			priced++
		}
		turns[i].Cost = ""
	}
	if priced == 0 {
		t.Fatal("no turn is priced; the assistant entries recorded model-a usage")
	}
	if !reflect.DeepEqual(turns, want) {
		t.Fatalf("latest turns = %+v\nwant the index window regrouped %+v", turns, want)
	}
	if snapshot != (appwire.SnapshotIdentity{Incarnation: window.Incarnation, Length: recorded}) {
		t.Fatalf("snapshot = %+v, want incarnation %q at the recorded length %d", snapshot, window.Incarnation, recorded)
	}
	if !window.HasOlder || older == "" {
		t.Fatalf("older cursor %q with HasOlder %v; four items in a window of three leave one older", older, window.HasOlder)
	}
	if len(overlay) != 0 {
		t.Fatalf("overlay = %+v, want none", overlay)
	}

	page, pageOlder, pageSnapshot, err := hx.history.before("local:th_history", older, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || len(page[0].Items) != 1 || page[0].Items[0].Text != "one" || pageOlder != "" || pageSnapshot != snapshot {
		t.Fatalf("older page = %+v cursor %q snapshot %+v, want the first item alone, no cursor, snapshot %+v", page, pageOlder, pageSnapshot, snapshot)
	}
}

// A stream captured in the cut whose round's ASSISTANT entry is recorded
// before the projection is history in the response, so the response's
// overlay drops it; overlay items still live stay.
func TestHistoryReadDropsAStreamCoveredAfterTheCut(t *testing.T) {
	hx := newHistoryHarness(t)
	hx.record(t, "question")
	hx.overlay.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	hx.overlay.Event(events.New(events.AssistantTextDeltaData{Delta: "partial"}))
	hx.overlay.Event(events.New(events.LoopDetectionData{Message: "loop"}))
	captured := hx.history.capture()
	if len(captured.overlay) != 2 || captured.overlay[0].Kind != appwire.OverlayStream {
		t.Fatalf("captured overlay %+v, want the stream and the notice", captured.overlay)
	}

	hx.recordAssistant(t, "r_1", "", "the whole answer")
	turns, _, _, overlay, err := hx.history.latest(captured, "local:th_history", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(overlay) != 1 || overlay[0].Key != captured.overlay[1].Key {
		t.Fatalf("response overlay %+v, want only the notice %s", overlay, captured.overlay[1].Key)
	}
	last := turns[len(turns)-1].Items
	if got := last[len(last)-1].Text; got != "the whole answer" {
		t.Fatalf("latest history ends with %q, want the recorded answer", got)
	}
}

// A backfill cursor from an incarnation a rebuild replaced is stale: the
// client re-reads the latest window.
func TestHistoryReadBeforeWithAnotherIncarnationsCursorIsStale(t *testing.T) {
	hx := newHistoryHarness(t)
	hx.record(t, "one")
	hx.record(t, "two")
	_, older, snapshot, _, err := hx.history.latest(hx.history.capture(), "local:th_history", 1)
	if err != nil || older == "" {
		t.Fatalf("latest = cursor %q, %v; want an older cursor", older, err)
	}
	if _, _, _, err := hx.history.before("local:th_history", older, 1); err != nil {
		t.Fatalf("before in the cursor's incarnation: %v", err)
	}
	if _, _, _, err := hx.history.before("local:th_other", older, 1); !isTranscriptItemCursorStale(err) {
		t.Fatalf("before with another thread's cursor = %v, want stale", err)
	}

	idx, err := hx.cache.Acquire(hx.path)
	if err != nil {
		t.Fatal(err)
	}
	err = idx.Rebuild(snapshot.Length)
	hx.cache.Release(idx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := hx.history.before("local:th_history", older, 1); !isTranscriptItemCursorStale(err) {
		t.Fatalf("before after a rebuild = %v, want stale", err)
	}
}

func isTranscriptItemCursorStale(err error) bool {
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		return false
	}
	data, ok := wireErr.Data.(appwire.ErrorData)
	return ok && data.EvenerErrorInfo == appwire.ErrorTranscriptItemCursorStale
}

// A failed thread's reads error, naming the entry that fails to project.
func TestHistoryReadOfAFailedThreadNamesTheOrdinal(t *testing.T) {
	hx := newHistoryHarness(t)
	hx.record(t, "zeroth")
	first := hx.record(t, "first")
	hx.updatesThrough(t, first.Offset+first.Length)
	_, older, _, _, err := hx.history.latest(hx.history.capture(), "local:th_history", 1)
	if err != nil || older == "" {
		t.Fatalf("latest = cursor %q, %v; want an older cursor", older, err)
	}
	hx.corruptNext(t)
	bad := hx.record(t, "second")
	hx.nextResync(t)
	hx.nextResync(t)

	assertFailed := func(label string, err error) {
		t.Helper()
		var wireErr appwire.WireError
		if !errors.As(err, &wireErr) {
			t.Fatalf("%s = %v, want a wire error", label, err)
		}
		data, _ := wireErr.Data.(appwire.ErrorData)
		if wireErr.Code != appwire.CodeInternalError || data.EvenerErrorInfo != appwire.ErrorHistoryFailed || !strings.Contains(wireErr.Message, strconv.FormatUint(bad.Ordinal, 10)) {
			t.Fatalf("%s = %+v, want historyFailed naming entry %d", label, wireErr, bad.Ordinal)
		}
	}
	_, _, _, _, err = hx.history.latest(hx.history.capture(), "local:th_history", 10)
	assertFailed("latest", err)
	_, _, _, err = hx.history.before("local:th_history", older, 10)
	assertFailed("before", err)
}
