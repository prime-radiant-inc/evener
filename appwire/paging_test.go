package appwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
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

func TestThreadTurnsListItemModeRequiresCursor(t *testing.T) {
	const wantCursorMessage = "cursor is required for thread/turns/list"
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
}

func TestThreadTurnsListItemModeRejectsWhitespaceOnlyCursor(t *testing.T) {
	for _, cursor := range []string{" ", "\t", "\n", "\u2003", "\u00a0"} {
		t.Run(fmt.Sprintf("%U", []rune(cursor)[0]), func(t *testing.T) {
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
