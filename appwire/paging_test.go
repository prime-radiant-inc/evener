package appwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func turnsN(n int) []Turn {
	out := make([]Turn, n)
	for i := range out {
		out[i] = Turn{ID: "turn_" + itoa(i)}
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func ids(turns []Turn) []string {
	out := make([]string, len(turns))
	for i, t := range turns {
		out[i] = t.ID
	}
	return out
}

func TestWindowTurnsBoundsToLatest(t *testing.T) {
	all := turnsN(10)
	page, cursor := WindowTurns(all, 3)
	if got := ids(page); len(got) != 3 || got[0] != "turn_7" || got[2] != "turn_9" {
		t.Fatalf("window = %v, want latest 3 (turn_7..turn_9)", ids(page))
	}
	if cursor != "7" {
		t.Fatalf("olderCursor = %q, want 7", cursor)
	}
}

func TestWindowTurnsUnboundedWhenLimitZeroOrLarger(t *testing.T) {
	all := turnsN(4)
	for _, lim := range []int{0, -1, 4, 99} {
		page, cursor := WindowTurns(all, lim)
		if len(page) != 4 || cursor != "" {
			t.Fatalf("limit %d: page=%d cursor=%q, want all 4 and no cursor", lim, len(page), cursor)
		}
	}
}

func TestPageTurnsWalksBackwardToHead(t *testing.T) {
	all := turnsN(10)

	// Empty cursor starts from the newest turn.
	p1 := PageTurns(all, "", 4)
	if got := ids(p1.Data); len(got) != 4 || got[0] != "turn_6" || got[3] != "turn_9" {
		t.Fatalf("page1 = %v, want turn_6..turn_9", ids(p1.Data))
	}
	if p1.NextCursor != "6" {
		t.Fatalf("page1 nextCursor = %q, want 6", p1.NextCursor)
	}

	// Next page is the four turns just older.
	p2 := PageTurns(all, p1.NextCursor, 4)
	if got := ids(p2.Data); len(got) != 4 || got[0] != "turn_2" || got[3] != "turn_5" {
		t.Fatalf("page2 = %v, want turn_2..turn_5", ids(p2.Data))
	}
	if p2.NextCursor != "2" {
		t.Fatalf("page2 nextCursor = %q, want 2", p2.NextCursor)
	}

	// Final partial page reaches the head and clears the cursor.
	p3 := PageTurns(all, p2.NextCursor, 4)
	if got := ids(p3.Data); len(got) != 2 || got[0] != "turn_0" || got[1] != "turn_1" {
		t.Fatalf("page3 = %v, want turn_0..turn_1", ids(p3.Data))
	}
	if p3.NextCursor != "" {
		t.Fatalf("page3 nextCursor = %q, want empty (head reached)", p3.NextCursor)
	}
}

func TestPageTurnsEmptyAndClamped(t *testing.T) {
	if r := PageTurns(nil, "", 5); len(r.Data) != 0 || r.NextCursor != "" {
		t.Fatalf("empty thread: data=%d cursor=%q", len(r.Data), r.NextCursor)
	}
	// A cursor past the end clamps to the end; a garbage cursor is treated as
	// "from newest".
	all := turnsN(3)
	if r := PageTurns(all, "99", 10); len(r.Data) != 3 {
		t.Fatalf("over-cursor: data=%d, want 3", len(r.Data))
	}
	if r := PageTurns(all, "garbage", 10); len(r.Data) != 3 {
		t.Fatalf("garbage cursor: data=%d, want 3 (from newest)", len(r.Data))
	}
	// A negative cursor clamps the high bound to 0, yielding an empty page with
	// no further cursor. This exercises the hi < 0 branch.
	if r := PageTurns(all, "-5", 10); len(r.Data) != 0 || r.NextCursor != "" {
		t.Fatalf("negative cursor: data=%d cursor=%q, want empty page and no cursor", len(r.Data), r.NextCursor)
	}
}

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

func TestThreadPagingMigration(t *testing.T) {
	legacyRead := ThreadReadParams{Ref: "local:thread", IncludeTurns: true, ItemsView: "full", TurnLimit: 3}
	encoded, err := json.Marshal(legacyRead)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"ref":"local:thread","includeTurns":true,"itemsView":"full","turnLimit":3}`; got != want {
		t.Fatalf("legacy read JSON = %s, want %s", got, want)
	}
	if err := ValidateThreadReadParams(legacyRead); err != nil {
		t.Fatalf("legacy read validation: %v", err)
	}

	legacyList := ThreadTurnsListParams{Ref: "local:thread", Cursor: "7", Limit: 3, ItemsView: "full"}
	encoded, err = json.Marshal(legacyList)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"ref":"local:thread","cursor":"7","limit":3,"itemsView":"full"}`; got != want {
		t.Fatalf("legacy list JSON = %s, want %s", got, want)
	}
	if err := ValidateThreadTurnsListParams(legacyList); err != nil {
		t.Fatalf("legacy list validation: %v", err)
	}

	all := turnsN(5)
	page, cursor := WindowTurns(all, legacyRead.TurnLimit)
	if len(page) != 3 || cursor != "2" {
		t.Fatalf("legacy window = %d turns, cursor %q; want 3, 2", len(page), cursor)
	}
	listPage := PageTurns(all, cursor, legacyList.Limit)
	if len(listPage.Data) != 2 || listPage.NextCursor != "" {
		t.Fatalf("legacy page = %d turns, cursor %q; want 2, empty", len(listPage.Data), listPage.NextCursor)
	}
}

func TestThreadItemModeValidation(t *testing.T) {
	validRead := ThreadReadParams{Ref: "local:thread", IncludeTurns: true, PageUnit: TranscriptPageUnitItem, ItemLimit: 40}
	if err := ValidateThreadReadParams(validRead); err != nil {
		t.Fatalf("valid item read: %v", err)
	}
	validList := ThreadTurnsListParams{Ref: "local:thread", PageUnit: TranscriptPageUnitItem, ItemLimit: 7, Cursor: "opaque"}
	if err := ValidateThreadTurnsListParams(validList); err != nil {
		t.Fatalf("valid item list: %v", err)
	}
	for _, limit := range []int{0, -1, 1, 40} {
		if err := ValidateThreadReadParams(ThreadReadParams{PageUnit: TranscriptPageUnitItem, ItemLimit: limit}); err != nil {
			t.Errorf("item read limit %d: %v", limit, err)
		}
	}
	for name, err := range map[string]error{
		"read over max": ValidateThreadReadParams(ThreadReadParams{PageUnit: TranscriptPageUnitItem, ItemLimit: 41}),
		"list over max": ValidateThreadTurnsListParams(ThreadTurnsListParams{PageUnit: TranscriptPageUnitItem, ItemLimit: 41}),
		"read both":     ValidateThreadReadParams(ThreadReadParams{PageUnit: TranscriptPageUnitItem, ItemLimit: 4, TurnLimit: 2}),
		"list both":     ValidateThreadTurnsListParams(ThreadTurnsListParams{PageUnit: TranscriptPageUnitItem, ItemLimit: 4, Limit: 2}),
		"read outside":  ValidateThreadReadParams(ThreadReadParams{ItemLimit: 4}),
		"list outside":  ValidateThreadTurnsListParams(ThreadTurnsListParams{ItemLimit: 4}),
		"read unit":     ValidateThreadReadParams(ThreadReadParams{PageUnit: "bytes"}),
		"list unit":     ValidateThreadTurnsListParams(ThreadTurnsListParams{PageUnit: "bytes"}),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestThreadTurnsListItemModeRequiresCursor(t *testing.T) {
	const wantCursorMessage = "cursor is required for item-mode thread/turns/list"
	err := ValidateThreadTurnsListParams(ThreadTurnsListParams{PageUnit: TranscriptPageUnitItem, ItemLimit: 4})
	var wireErr WireError
	if !errors.As(err, &wireErr) || wireErr.Code != CodeInvalidParams || wireErr.Message != wantCursorMessage {
		t.Fatalf("empty item cursor validation = %T %v, want code %d message %q", err, err, CodeInvalidParams, wantCursorMessage)
	}

	const wantLimitMessage = "itemLimit must be between 1 and 40"
	err = ValidateThreadTurnsListParams(ThreadTurnsListParams{PageUnit: TranscriptPageUnitItem, ItemLimit: TranscriptItemPageLimit + 1})
	if !errors.As(err, &wireErr) || wireErr.Code != CodeInvalidParams || wireErr.Message != wantLimitMessage {
		t.Fatalf("empty cursor over-limit validation = %T %v, want code %d message %q", err, err, CodeInvalidParams, wantLimitMessage)
	}

	if err := ValidateThreadTurnsListParams(ThreadTurnsListParams{PageUnit: TranscriptPageUnitItem, ItemLimit: 4, Cursor: "opaque"}); err != nil {
		t.Fatalf("opaque item cursor validation: %v", err)
	}
}

func TestThreadItemModeJSON(t *testing.T) {
	position := &ThreadItemPosition{Entry: 12, Item: 4}
	item := ThreadItem{Type: "agentMessage", ID: "display-1", TranscriptKey: "entry-12-item-4", Position: position}
	turn := Turn{ID: "turn_12", Items: []ThreadItem{item}, ItemsView: TurnItemsViewFragment, HasEarlierItems: true, HasLaterItems: true}
	read := ThreadReadResponse{PageUnit: TranscriptPageUnitItem, Thread: Thread{ID: "thread", Turns: []Turn{turn}}, OlderCursor: "opaque"}
	encoded, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"pageUnit":"item"`, `"transcriptKey":"entry-12-item-4"`, `"position":{"entry":12,"item":4}`, `"itemsView":"fragment"`, `"hasEarlierItems":true`, `"hasLaterItems":true`} {
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
		{PageUnit: TranscriptPageUnitItem, Thread: Thread{Turns: []Turn{{ID: "turn", ItemsView: TurnItemsViewFragment, Items: []ThreadItem{{ID: "item", Position: position}}}}}},
		{PageUnit: TranscriptPageUnitItem, Thread: Thread{Turns: []Turn{{ID: "turn", ItemsView: TurnItemsViewFull, Items: []ThreadItem{{ID: "item", TranscriptKey: "key", Position: position}}}}}},
	} {
		if err := ValidateThreadReadItemResponse(invalid); err == nil {
			t.Errorf("invalid item response accepted: %+v", invalid)
		}
	}
}
