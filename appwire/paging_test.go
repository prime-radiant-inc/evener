package appwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeTranscriptItemLimit(t *testing.T) {
	for _, tc := range []struct {
		in, want int
	}{{0, TranscriptItemPageLimit}, {-1, TranscriptItemPageLimit}, {1, 1}, {7, 7}, {TranscriptItemPageLimit, TranscriptItemPageLimit}} {
		if got, err := NormalizeTranscriptItemLimit(tc.in); err != nil || got != tc.want {
			t.Fatalf("NormalizeTranscriptItemLimit(%d) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
	got, err := NormalizeTranscriptItemLimit(TranscriptItemPageLimit + 1)
	if err == nil {
		t.Fatalf("item limit above %d accepted", TranscriptItemPageLimit)
	}
	if got != 0 {
		t.Fatalf("invalid item limit returned %d, want zero unusable value", got)
	}
	if got, want := err.Error(), fmt.Sprintf("itemLimit must be between 1 and %d", TranscriptItemPageLimit); got != want {
		t.Fatalf("over-limit error = %q, want %q", got, want)
	}
}

func TestThreadItemModeValidation(t *testing.T) {
	validRead := ThreadReadParams{Ref: "local:thread", IncludeTurns: true, ItemLimit: 40}
	if err := ValidateThreadReadParams(validRead); err != nil {
		t.Fatalf("valid item read: %v", err)
	}
	validList := ThreadTurnsListParams{Ref: "local:thread", ItemLimit: 7, Cursor: "opaque"}
	if err := ValidateThreadTurnsListParams(validList); err != nil {
		t.Fatalf("valid item list: %v", err)
	}
	for _, limit := range []int{0, -1, 1, 40} {
		if err := ValidateThreadReadParams(ThreadReadParams{ItemLimit: limit}); err != nil {
			t.Errorf("item read limit %d: %v", limit, err)
		}
	}
	for name, err := range map[string]error{
		"read over max":       ValidateThreadReadParams(ThreadReadParams{ItemLimit: 41}),
		"list over max":       ValidateThreadTurnsListParams(ThreadTurnsListParams{ItemLimit: 41, Cursor: "opaque"}),
		"list numeric cursor": ValidateThreadTurnsListParams(ThreadTurnsListParams{ItemLimit: 4, Cursor: "42"}),
		"list empty cursor":   ValidateThreadTurnsListParams(ThreadTurnsListParams{ItemLimit: 4}),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestThreadReadFetchModeIsExclusive(t *testing.T) {
	valid := ThreadReadParams{ThreadID: "thread", FetchCursor: "opaque"}
	if err := ValidateThreadReadParams(valid); err != nil {
		t.Fatalf("valid fetch read: %v", err)
	}

	includeThinking := false
	invalid := []ThreadReadParams{
		{FetchCursor: "opaque"},
		{ThreadID: "thread", Ref: "local:thread", FetchCursor: "opaque"},
		{ThreadID: "thread", FetchCursor: "opaque", IncludeTurns: true},
		{ThreadID: "thread", FetchCursor: "opaque", ItemLimit: 1},
		{ThreadID: "thread", FetchCursor: "opaque", Subscribe: true},
		{ThreadID: "thread", FetchCursor: "opaque", TurnWindow: &ThreadTurnWindow{Latest: 1}},
		{ThreadID: "thread", FetchCursor: "opaque", ThreadContentParams: ThreadContentParams{IncludeThinking: &includeThinking}},
	}
	for i, params := range invalid {
		if err := ValidateThreadReadParams(params); err == nil {
			t.Errorf("invalid fetch read %d accepted", i)
		}
	}

	var decoded ThreadReadParams
	if err := json.Unmarshal([]byte(`{"threadId":"thread","fetchCursor":"opaque"}`), &decoded); err != nil {
		t.Fatalf("fetch read wire form rejected: %v", err)
	}
	if decoded.FetchCursor != "opaque" {
		t.Fatalf("fetch cursor = %q", decoded.FetchCursor)
	}
}

func TestThreadReadFetchResponseReusesSessionPayload(t *testing.T) {
	fetch := &SessionFetchResponse{Position: ThreadItemPosition{Entry: 3, Item: 1}, Version: 2, TotalLength: 9, Data: "Ynl0ZXM="}
	encoded, err := json.Marshal(ThreadReadResponse{Fetch: fetch})
	if err != nil {
		t.Fatal(err)
	}
	var got ThreadReadResponse
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.Fetch == nil || *got.Fetch != *fetch {
		t.Fatalf("fetch response = %+v, want %+v", got.Fetch, fetch)
	}
}

func TestThreadTurnsListItemModeRequiresCursor(t *testing.T) {
	const wantCursorMessage = "cursor or before is required for thread/turns/list"
	err := ValidateThreadTurnsListParams(ThreadTurnsListParams{ItemLimit: 4})
	var wireErr WireError
	if !errors.As(err, &wireErr) || wireErr.Code != CodeInvalidParams || wireErr.Message != wantCursorMessage {
		t.Fatalf("empty item cursor validation = %T %v, want code %d message %q", err, err, CodeInvalidParams, wantCursorMessage)
	}

	const wantLimitMessage = "itemLimit must be between 1 and 40"
	err = ValidateThreadTurnsListParams(ThreadTurnsListParams{ItemLimit: TranscriptItemPageLimit + 1, Cursor: "opaque"})
	if !errors.As(err, &wireErr) || wireErr.Code != CodeInvalidParams || wireErr.Message != wantLimitMessage {
		t.Fatalf("empty cursor over-limit validation = %T %v, want code %d message %q", err, err, CodeInvalidParams, wantLimitMessage)
	}

	if err := ValidateThreadTurnsListParams(ThreadTurnsListParams{ItemLimit: 4, Cursor: "opaque"}); err != nil {
		t.Fatalf("opaque item cursor validation: %v", err)
	}
	// A before boundary stands in for a missing cursor: the source mints one
	// from the thread's current identity.
	before := &ThreadItemPosition{Entry: 3}
	if err := ValidateThreadTurnsListParams(ThreadTurnsListParams{ItemLimit: 4, Before: before}); err != nil {
		t.Fatalf("cursorless before validation: %v", err)
	}
	// before doesn't excuse a cursor that is present but isn't one, and the
	// message names the cursor as the problem.
	for name, cursor := range map[string]string{"whitespace": " ", "legacy numeric": "42"} {
		err := ValidateThreadTurnsListParams(ThreadTurnsListParams{ItemLimit: 4, Cursor: cursor, Before: before})
		var wireErr WireError
		if !errors.As(err, &wireErr) || wireErr.Message != "cursor must be an opaque item cursor" {
			t.Errorf("%s cursor with before: error = %v, want the malformed-cursor message", name, err)
		}
	}
}

func TestThreadTurnsListItemModeRejectsWhitespaceOnlyCursor(t *testing.T) {
	for _, cursor := range []string{" ", "\t", "\n", "\u2003", "\u00a0"} {
		first, _ := utf8.DecodeRuneInString(cursor)
		t.Run(fmt.Sprintf("%U", first), func(t *testing.T) {
			if err := ValidateThreadTurnsListParams(ThreadTurnsListParams{ItemLimit: 4, Cursor: cursor}); err == nil {
				t.Fatalf("whitespace-only cursor %q accepted", cursor)
			}
		})
	}
}

func TestThreadItemModeJSON(t *testing.T) {
	position := &ThreadItemPosition{Entry: 12, Item: 4}
	item := ThreadItem{Type: "agentMessage", ID: "display-1", TranscriptKey: "entry-12-item-4", Position: position}
	turn := Turn{ID: "turn_12", Items: []ThreadItem{item}, ItemsView: TurnItemsViewFragment, HasEarlierItems: true, HasLaterItems: true}
	read := ThreadReadResponse{Thread: Thread{ID: "thread", Turns: []Turn{turn}}, OlderCursor: "opaque"}
	encoded, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"transcriptKey":"entry-12-item-4"`, `"position":{"entry":12,"item":4}`, `"itemsView":"fragment"`, `"hasEarlierItems":true`, `"hasLaterItems":true`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("item response JSON %s missing %s", encoded, want)
		}
	}
	legacy, err := json.Marshal(ThreadReadResponse{Thread: Thread{ID: "thread"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), `"pageUnit"`) {
		t.Fatalf("legacy response unexpectedly contains pageUnit: %s", legacy)
	}
	if err := ValidateThreadReadItemResponse(read); err != nil {
		t.Fatalf("valid item response: %v", err)
	}
	for _, invalid := range []ThreadReadResponse{
		{Thread: Thread{Turns: []Turn{{ID: "turn", ItemsView: TurnItemsViewFragment, Items: []ThreadItem{{ID: "item", Position: position}}}}}},
		{Thread: Thread{Turns: []Turn{{ID: "turn", ItemsView: TurnItemsViewFull, Items: []ThreadItem{{ID: "item", TranscriptKey: "key", Position: position}}}}}},
	} {
		if err := ValidateThreadReadItemResponse(invalid); err == nil {
			t.Errorf("invalid item response accepted: %+v", invalid)
		}
	}
}
