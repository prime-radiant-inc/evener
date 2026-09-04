package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

type codexItemPage struct {
	Data       []map[string]any
	NextCursor string
}

type codexItemFixture struct {
	mu            sync.Mutex
	pages         map[string]codexItemPage
	nativeCursors []string
}

func (f *codexItemFixture) page(cursor string) codexItemPage {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nativeCursors = append(f.nativeCursors, cursor)
	return f.pages[cursor]
}

func (f *codexItemFixture) cursors() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.nativeCursors...)
}

func newCodexItemPagingSource(t *testing.T, fixture *codexItemFixture) *CodexSource {
	t.Helper()
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params map[string]any) (map[string]any, error) {
		return map[string]any{"thread": codexThreadMap(fmt.Sprint(params["threadId"]))}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params map[string]any) (map[string]any, error) {
		cursor, _ := params["cursor"].(string)
		page := fixture.page(cursor)
		return map[string]any{"data": page.Data, "nextCursor": page.NextCursor}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(httpServer.Close)
	return NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
}

func TestCodexItemPaging(t *testing.T) {
	turn := codexNativeTurn("turn_1", codexAgentItems(45))
	fixture := &codexItemFixture{pages: map[string]codexItemPage{
		"":            {Data: []map[string]any{turn}, NextCursor: "native-next"},
		"native-next": {},
	}}
	source := newCodexItemPagingSource(t, fixture)
	read, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{
		Ref: "codex:th_codex", IncludeTurns: true, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40,
	})
	if err != nil {
		t.Fatalf("item ReadThread: %v", err)
	}
	if read.PageUnit != appwire.TranscriptPageUnitItem || len(read.Thread.Turns) != 1 || len(read.Thread.Turns[0].Items) != 40 {
		t.Fatalf("item read = %+v, want one 40-item fragment", read)
	}
	if read.OlderCursor == "" {
		t.Fatal("item read has no older cursor")
	}
	if got := read.Thread.Turns[0].Items[0].ID; got != "item-05" {
		t.Fatalf("latest first item=%q, want item-05", got)
	}
	if got := read.Thread.Turns[0].Items[39].ID; got != "item-44" {
		t.Fatalf("latest last item=%q, want item-44", got)
	}
	if !read.Thread.Turns[0].HasEarlierItems || read.Thread.Turns[0].HasLaterItems {
		t.Fatalf("latest completeness=(%v,%v), want earlier=true/later=false", read.Thread.Turns[0].HasEarlierItems, read.Thread.Turns[0].HasLaterItems)
	}

	older, err := source.ListTurns(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "codex:th_codex", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40, Cursor: read.OlderCursor,
	})
	if err != nil {
		t.Fatalf("item ListTurns: %v", err)
	}
	if older.PageUnit != appwire.TranscriptPageUnitItem || len(older.Data) != 1 || len(older.Data[0].Items) != 5 {
		t.Fatalf("older item page = %+v, want one five-item fragment", older)
	}
	if got := older.Data[0].Items[0].ID; got != "item-00" {
		t.Fatalf("older first item=%q, want item-00", got)
	}
	if older.Data[0].HasEarlierItems || !older.Data[0].HasLaterItems {
		t.Fatalf("older completeness=(%v,%v), want earlier=false/later=true", older.Data[0].HasEarlierItems, older.Data[0].HasLaterItems)
	}
	for _, cursor := range fixture.cursors() {
		if cursor != "" && cursor != "native-next" {
			t.Fatalf("AppWire cursor leaked to native Codex turns/list: %q", cursor)
		}
	}
}

func TestCodexItemPagingMaterializesNativePagesChronologically(t *testing.T) {
	fixture := &codexItemFixture{pages: map[string]codexItemPage{
		"":      {Data: []map[string]any{codexNativeTurn("turn_new", codexAgentItems(1))}, NextCursor: "older"},
		"older": {Data: []map[string]any{codexNativeTurn("turn_old", codexAgentItems(1))}},
	}}
	source := newCodexItemPagingSource(t, fixture)
	latest, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{
		Ref: "codex:th_codex", IncludeTurns: true, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 1,
	})
	if err != nil {
		t.Fatalf("multi-page item ReadThread: %v", err)
	}
	if len(latest.Thread.Turns) != 1 || len(latest.Thread.Turns[0].Items) != 1 || latest.Thread.Turns[0].ID != "turn_new" {
		t.Fatalf("latest multi-page item = %+v, want newest turn_new", latest)
	}
	older, err := source.ListTurns(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "codex:th_codex", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 1, Cursor: latest.OlderCursor,
	})
	if err != nil {
		t.Fatalf("multi-page older ListTurns: %v", err)
	}
	if len(older.Data) != 1 || len(older.Data[0].Items) != 1 || older.Data[0].ID != "turn_old" {
		t.Fatalf("older multi-page item = %+v, want older turn_old", older)
	}
}

func TestCodexItemCandidatesOmitUnavailableTranscriptEntryIndex(t *testing.T) {
	rawItems := func(t *testing.T, ids ...string) []json.RawMessage {
		t.Helper()
		items := make([]json.RawMessage, len(ids))
		for i, id := range ids {
			raw, err := json.Marshal(map[string]any{"type": "agentMessage", "id": id, "text": id})
			if err != nil {
				t.Fatalf("marshal Codex item: %v", err)
			}
			items[i] = raw
		}
		return items
	}
	turns := []codexTurn{
		{ID: "turn_1", Items: rawItems(t, "item_1a", "item_1b")},
		{ID: "turn_2", Items: rawItems(t, "item_2a")},
	}

	candidates, err := codexItemCandidates(turns)
	if err != nil {
		t.Fatalf("codex item candidates: %v", err)
	}
	wantEntries := []uint64{0, 0, 1}
	if len(candidates) != len(wantEntries) {
		t.Fatalf("candidate count = %d, want %d", len(candidates), len(wantEntries))
	}
	for i, candidate := range candidates {
		if candidate.Position.Entry != wantEntries[i] {
			t.Fatalf("candidate %d position entry = %d, want stable ordinal %d", i, candidate.Position.Entry, wantEntries[i])
		}
		if candidate.Item.TranscriptEntryIndex != 0 {
			t.Fatalf("candidate %d transcript entry index = %d, want omitted/unavailable", i, candidate.Item.TranscriptEntryIndex)
		}
	}
}

func TestCodexItemPagingCycleErrorDoesNotLeakNativeCursor(t *testing.T) {
	const secret = "codex-secret-cursor"
	fixture := &codexItemFixture{pages: map[string]codexItemPage{
		"":     {NextCursor: secret},
		secret: {NextCursor: secret},
	}}
	source := newCodexItemPagingSource(t, fixture)
	_, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{
		Ref: "codex:th_codex", IncludeTurns: true, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 1,
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("cycle error = %v, want non-leaking error", err)
	}
}

func TestCodexItemPagingGeneration(t *testing.T) {
	fixture := &codexItemFixture{pages: map[string]codexItemPage{"": {}}}
	source := newCodexItemPagingSource(t, fixture)
	setCodexFixtureTurns(fixture, codexNativeTurn("turn_a", codexAgentItems(1)), codexNativeTurn("turn_b", codexAgentItems(1)))
	first, firstIdentity, err := source.latestItemWindow(context.Background(), "th_codex", 1, "full")
	if err != nil {
		t.Fatalf("first latestItemWindow: %v", err)
	}
	if first.OlderCursor == "" {
		t.Fatal("first item window has no older cursor")
	}
	setCodexFixtureTurns(fixture,
		codexNativeTurn("turn_a", codexAgentItems(1)),
		codexNativeTurn("turn_b", codexAgentItems(1)),
		codexNativeTurn("turn_c", codexAgentItems(1)),
	)
	_, appendedIdentity, err := source.latestItemWindow(context.Background(), "th_codex", 1, "full")
	if err != nil {
		t.Fatalf("appended latestItemWindow: %v", err)
	}
	if appendedIdentity.Incarnation != firstIdentity.Incarnation {
		t.Fatalf("append rotated incarnation: first=%q appended=%q", firstIdentity.Incarnation, appendedIdentity.Incarnation)
	}

	setCodexFixtureTurns(fixture,
		codexNativeTurn("turn_changed", codexAgentItems(1)),
		codexNativeTurn("turn_b", codexAgentItems(1)),
		codexNativeTurn("turn_c", codexAgentItems(1)),
	)
	_, changedIdentity, err := source.latestItemWindow(context.Background(), "th_codex", 1, "full")
	if err != nil {
		t.Fatalf("changed-prefix latestItemWindow: %v", err)
	}
	if changedIdentity.Incarnation == appendedIdentity.Incarnation {
		t.Fatalf("changed prefix preserved incarnation %q", changedIdentity.Incarnation)
	}
	if _, _, err := source.previousItemWindow(context.Background(), "th_codex", first.OlderCursor, 1, "full"); err == nil {
		t.Fatal("cursor from changed prefix remained valid")
	}
}

func TestLocalItemPagingPreservesCursorAcrossSlidingNewestWindow(t *testing.T) {
	source := newLocalItemReadConversionSource(t, "sliding-window")
	params := appwire.ThreadReadParams{Ref: "local:sliding-window", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40}
	nativeIdentity := appitempaging.CursorIdentity{ThreadRef: params.Ref, Incarnation: "native-sliding-window", ProjectionVersion: 1}
	firstNativeCursor, err := appitempaging.EncodeCursor(nativeIdentity, appwire.ThreadItemPosition{Entry: 1})
	if err != nil {
		t.Fatalf("encode first native cursor: %v", err)
	}
	shiftedNativeCursor, err := appitempaging.EncodeCursor(nativeIdentity, appwire.ThreadItemPosition{Entry: 2})
	if err != nil {
		t.Fatalf("encode shifted native cursor: %v", err)
	}

	first, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(1, 40, firstNativeCursor))
	if err != nil {
		t.Fatalf("first item response: %v", err)
	}
	oldCursor, err := appitempaging.EncodeCursor(first.Identity, appwire.ThreadItemPosition{Entry: 20})
	if err != nil {
		t.Fatalf("encode retained cursor: %v", err)
	}
	second, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(2, 41, shiftedNativeCursor))
	if err != nil {
		t.Fatalf("sliding item response: %v", err)
	}
	if second.Identity.Incarnation != first.Identity.Incarnation {
		t.Fatalf("sliding 1..40 -> 2..41 rotated incarnation: first=%q second=%q", first.Identity.Incarnation, second.Identity.Incarnation)
	}
	if _, err := appitempaging.DecodeCursor(oldCursor, second.Identity); err != nil {
		t.Fatalf("cursor from overlapping retained window became stale after append: %v", err)
	}
	state, ok := source.itemSnapshots.get(params.Ref)
	if !ok || state.NativeCursor != firstNativeCursor {
		t.Fatalf("retained native cursor = %q, want original same-generation token", state.NativeCursor)
	}
}

func TestLocalItemPagingIdentityBindsEveryItemPosition(t *testing.T) {
	source := newLocalItemReadConversionSource(t, "position-rewrite")
	params := appwire.ThreadReadParams{Ref: "local:position-rewrite", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 4}
	old := positionedItemReadResponseFor([]string{"A", "B", "C", "D"}, []uint64{0, 10, 15, 20}, "")
	first, err := source.ItemCandidatesFromRead(t.Context(), params, old)
	if err != nil {
		t.Fatalf("first item response: %v", err)
	}
	oldCursor, err := appitempaging.EncodeCursor(first.Identity, appwire.ThreadItemPosition{Entry: 10})
	if err != nil {
		t.Fatalf("encode old B cursor: %v", err)
	}

	rewritten := positionedItemReadResponseFor([]string{"A", "B", "C", "D"}, []uint64{0, 5, 10, 20}, "")
	second, err := source.ItemCandidatesFromRead(t.Context(), params, rewritten)
	if err != nil {
		t.Fatalf("rewritten item response: %v", err)
	}
	if second.Identity.Incarnation == first.Identity.Incarnation {
		t.Fatalf("middle-position rewrite preserved incarnation %q", first.Identity.Incarnation)
	}
	if _, err := appitempaging.DecodeCursor(oldCursor, second.Identity); err == nil {
		t.Fatal("cursor before old B@10 remained valid after B@5,C@10 rewrite")
	}
}

func TestLocalItemPagingRejectsNonIncreasingCandidatesBeforeStateMutation(t *testing.T) {
	source := newLocalItemReadConversionSource(t, "non-increasing")
	params := appwire.ThreadReadParams{Ref: "local:non-increasing", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 2}
	response := positionedItemReadResponseFor([]string{"A", "B"}, []uint64{2, 1}, "native-older")
	if _, err := source.ItemCandidatesFromRead(t.Context(), params, response); err == nil {
		t.Fatal("non-increasing candidates returned nil error")
	}
	if got := retainedPagingStateCount(t, source.itemSnapshots); got != 0 {
		t.Fatalf("invalid candidates retained %d paging states, want 0", got)
	}
}

func TestItemSnapshotStateFingerprintTailIsFixedAndBounded(t *testing.T) {
	stateType := reflect.TypeFor[itemSnapshotState]()
	tail, ok := stateType.FieldByName("FingerprintTail")
	if !ok {
		t.Fatal("retained state has no fixed FingerprintTail")
	}
	if tail.Type.Kind() != reflect.Array || tail.Type.Len() != appwire.TranscriptItemPageLimit {
		t.Fatalf("FingerprintTail type = %v, want fixed [%d] array", tail.Type, appwire.TranscriptItemPageLimit)
	}
	for field := range stateType.Fields() {
		kind := field.Type.Kind()
		if kind == reflect.Map || kind == reflect.Slice {
			t.Fatalf("retained state field %q is unbounded %v", field.Name, kind)
		}
	}
}

func TestCodexExposesCombinedExactItemRead(t *testing.T) {
	fixture := &codexItemFixture{pages: map[string]codexItemPage{"": {Data: []map[string]any{
		codexNativeTurn("turn_atomic", codexAgentItems(2)),
	}}}}
	source := newCodexItemPagingSource(t, fixture)
	atomicSource, ok := any(source).(interface {
		ReadThreadWithItemCandidates(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, ItemCandidateResult, error)
	})
	if !ok {
		t.Fatal("CodexSource has no combined exact-response item read seam")
	}
	response, candidates, err := atomicSource.ReadThreadWithItemCandidates(t.Context(), appwire.ThreadReadParams{
		Ref: "codex:thread-atomic", IncludeTurns: true, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 1,
	})
	if err != nil {
		t.Fatalf("combined item read: %v", err)
	}
	want, err := localDaemonItemCandidates(response.Thread.Turns)
	if err != nil {
		t.Fatalf("convert returned response: %v", err)
	}
	if !reflect.DeepEqual(candidates.Candidates.Candidates, want) {
		t.Fatalf("combined candidates do not belong to returned response: got=%+v want=%+v", candidates.Candidates.Candidates, want)
	}
	if got := retainedPagingLockCount(t, source); got != 0 {
		t.Fatalf("combined item read retained %d keyed locks after return, want 0", got)
	}
}

func TestCodexCombinedItemReadSerializesRewriteThroughResponseConversion(t *testing.T) {
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params map[string]any) (map[string]any, error) {
		return map[string]any{"thread": codexThreadMap(fmt.Sprint(params["threadId"]))}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(context.Context, map[string]any) (map[string]any, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			close(firstEntered)
			<-releaseFirst
			return map[string]any{"data": []map[string]any{codexNativeTurn("turn_before", codexAgentItems(2))}}, nil
		}
		close(secondEntered)
		return map[string]any{"data": []map[string]any{codexNativeTurn("turn_after", codexAgentItems(2))}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(httpServer.Close)
	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	atomicSource := any(source).(CombinedItemReadSource)
	params := appwire.ThreadReadParams{Ref: "codex:thread-rewrite", IncludeTurns: true, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 1}
	type result struct {
		response   appwire.ThreadReadResponse
		candidates ItemCandidateResult
		err        error
	}
	results := make(chan result, 2)
	go func() {
		response, candidates, err := atomicSource.ReadThreadWithItemCandidates(t.Context(), params)
		results <- result{response: response, candidates: candidates, err: err}
	}()
	<-firstEntered
	go func() {
		response, candidates, err := atomicSource.ReadThreadWithItemCandidates(t.Context(), params)
		results <- result{response: response, candidates: candidates, err: err}
	}()
	select {
	case <-secondEntered:
		close(releaseFirst)
		t.Fatal("same-thread rewrite entered materialization before the earlier response conversion completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("combined rewrite results: first=%v second=%v", first.err, second.err)
	}
	byTurn := map[string]result{}
	for _, got := range []result{first, second} {
		if len(got.response.Thread.Turns) != 1 || len(got.candidates.Candidates.Candidates) != 1 {
			t.Fatalf("combined result shape = response %+v candidates %+v", got.response, got.candidates)
		}
		turnID := got.response.Thread.Turns[0].ID
		if got.candidates.Candidates.Candidates[0].TurnID != turnID {
			t.Fatalf("response turn %q paired with candidate turn %q", turnID, got.candidates.Candidates.Candidates[0].TurnID)
		}
		byTurn[turnID] = got
	}
	if len(byTurn) != 2 || byTurn["turn_before"].candidates.Identity.Incarnation == byTurn["turn_after"].candidates.Identity.Incarnation {
		t.Fatalf("rewrite results did not preserve exact distinct identities: %+v", byTurn)
	}
	if got := retainedPagingLockCount(t, source); got != 0 {
		t.Fatalf("combined rewrite retained %d keyed locks after callers completed, want 0", got)
	}
}

func TestCodexCombinedItemReadReleasesLockOnError(t *testing.T) {
	fixture := &codexItemFixture{pages: map[string]codexItemPage{
		"":      {NextCursor: "cycle"},
		"cycle": {NextCursor: "cycle"},
	}}
	source := newCodexItemPagingSource(t, fixture)
	_, _, err := source.ReadThreadWithItemCandidates(t.Context(), appwire.ThreadReadParams{
		Ref: "codex:thread-error", IncludeTurns: true, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 1,
	})
	if err == nil {
		t.Fatal("combined item read with native cursor cycle returned nil error")
	}
	if got := retainedPagingLockCount(t, source); got != 0 {
		t.Fatalf("failed combined item read retained %d keyed locks, want 0", got)
	}
}

func TestCodexItemPagingKeepsSplitToolCallAndResult(t *testing.T) {
	items := codexAgentItems(45)
	items[4] = map[string]any{"type": "commandExecution", "id": "call-1", "command": "cat file", "cwd": "/tmp", "status": "completed"}
	items[5] = map[string]any{"type": "mcpToolCall", "id": "result-1", "tool": "read_file", "arguments": map[string]any{"path": "file"}, "result": "contents", "status": "completed", "toolCallId": "call-1"}
	fixture := &codexItemFixture{pages: map[string]codexItemPage{"": {Data: []map[string]any{codexNativeTurn("turn_tools", items)}}}}
	source := newCodexItemPagingSource(t, fixture)
	latest, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex", IncludeTurns: true, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40})
	if err != nil {
		t.Fatalf("split-tool ReadThread: %v", err)
	}
	older, err := source.ListTurns(context.Background(), appwire.ThreadTurnsListParams{Ref: "codex:th_codex", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40, Cursor: latest.OlderCursor})
	if err != nil {
		t.Fatalf("split-tool ListTurns: %v", err)
	}
	var call, result bool
	for _, turn := range append(append([]appwire.Turn(nil), latest.Thread.Turns...), older.Data...) {
		for _, item := range turn.Items {
			call = call || item.ID == "call-1"
			result = result || item.ID == "result-1"
		}
	}
	if !call || !result {
		t.Fatalf("split tool halves lost across pages: call=%v result=%v latest=%+v older=%+v", call, result, latest.Thread.Turns, older.Data)
	}
}

func TestCloneCodexItemSnapshotPositionsZeroCandidateWithNilItemPosition(t *testing.T) {
	original := codexItemSnapshot{Candidates: []appitempaging.TranscriptItemCandidate{{
		TurnID:   "turn_0",
		Turn:     appwire.Turn{ID: "turn_0"},
		Item:     appwire.ThreadItem{ID: "item_0"},
		Position: appwire.ThreadItemPosition{Entry: 0, Item: 0},
	}}}

	cloned := cloneCodexItemSnapshot(original)
	position := cloned.Candidates[0].Item.Position
	if position == nil {
		t.Fatal("cloned zero-position candidate has nil item position")
	}
	position.Entry = 9
	if original.Candidates[0].Position != (appwire.ThreadItemPosition{}) {
		t.Fatalf("mutating cloned item position changed original candidate position: %+v", original.Candidates[0].Position)
	}
	if original.Candidates[0].Item.Position != nil {
		t.Fatalf("mutating cloned item position populated original item position: %+v", original.Candidates[0].Item.Position)
	}
}

func TestCloneCodexItemSnapshotClonesEachUniqueTurnOnce(t *testing.T) {
	turn := appwire.Turn{ID: "turn_shared", Items: []appwire.ThreadItem{{
		ID: "turn-item", Text: "original", Raw: json.RawMessage(`{"value":"original"}`),
		Images: []appwire.InputItem{{Name: "image", Data: []byte("original-image"), Metadata: map[string]string{"owner": "original"}}},
	}}}
	original := codexItemSnapshot{Candidates: []appitempaging.TranscriptItemCandidate{
		{TurnID: turn.ID, Turn: turn, Item: appwire.ThreadItem{ID: "candidate-0"}},
		{TurnID: turn.ID, Turn: turn, Item: appwire.ThreadItem{ID: "candidate-1"}},
	}}

	cloned := cloneCodexItemSnapshot(original)
	first := &cloned.Candidates[0].Turn.Items[0]
	second := &cloned.Candidates[1].Turn.Items[0]
	if first != second {
		t.Fatal("same-turn candidates received distinct cloned turn backing; want one clone per unique turn")
	}
	first.Text = "mutated"
	first.Raw[0] = 'X'
	first.Images[0].Data[0] = 'X'
	first.Images[0].Metadata["owner"] = "mutated"
	if second.Text != "mutated" || second.Raw[0] != 'X' || second.Images[0].Data[0] != 'X' || second.Images[0].Metadata["owner"] != "mutated" {
		t.Fatal("same-turn candidates do not share their single returned-snapshot turn clone")
	}
	originalItem := original.Candidates[0].Turn.Items[0]
	if originalItem.Text != "original" || string(originalItem.Raw) != `{"value":"original"}` || string(originalItem.Images[0].Data) != "original-image" || originalItem.Images[0].Metadata["owner"] != "original" {
		t.Fatalf("mutating cloned turn changed caller-owned original: %+v", originalItem)
	}
}

func TestCodexItemPagingRetainsOnlyBoundedLightweightState(t *testing.T) {
	const payload = "retained-transcript-payload-sentinel"
	fixture := &codexItemFixture{pages: map[string]codexItemPage{"": {Data: []map[string]any{
		codexNativeTurn("turn_payload", []map[string]any{{"type": "agentMessage", "id": "payload-item", "text": payload}}),
	}}}}
	source := newCodexItemPagingSource(t, fixture)
	if _, _, err := source.latestItemWindow(t.Context(), "thread-payload", 1, "full"); err != nil {
		t.Fatalf("materialize payload thread: %v", err)
	}
	retained, err := json.Marshal(retainedItemSnapshotStates(source.itemSnapshots))
	if err != nil {
		t.Fatalf("marshal retained paging state: %v", err)
	}
	if strings.Contains(string(retained), payload) {
		t.Fatalf("retained paging state contains transcript payload (serialized bytes=%d)", len(retained))
	}

	for i := range 40 {
		if _, _, err := source.latestItemWindow(t.Context(), fmt.Sprintf("thread-%02d", i), 1, "full"); err != nil {
			t.Fatalf("materialize thread %d: %v", i, err)
		}
	}
	if got := retainedPagingStateCount(t, source.itemSnapshots); got > 32 {
		t.Fatalf("retained paging states = %d, want hard bound <= 32", got)
	}
}

func TestItemSnapshotStateTypeContainsNoTranscriptPayloadTypes(t *testing.T) {
	stateType := reflect.TypeFor[itemSnapshotState]()
	for _, forbidden := range []reflect.Type{
		reflect.TypeFor[appitempaging.TranscriptItemCandidate](),
		reflect.TypeFor[appwire.Turn](),
		reflect.TypeFor[appwire.ThreadItem](),
	} {
		if typeContains(stateType, forbidden, make(map[reflect.Type]bool)) {
			t.Fatalf("retained %v contains forbidden transcript payload type %v", stateType, forbidden)
		}
	}
}

func typeContains(current, target reflect.Type, seen map[reflect.Type]bool) bool {
	if current == target {
		return true
	}
	if seen[current] {
		return false
	}
	seen[current] = true
	switch current.Kind() {
	case reflect.Array, reflect.Pointer, reflect.Slice:
		return typeContains(current.Elem(), target, seen)
	case reflect.Map:
		return typeContains(current.Key(), target, seen) || typeContains(current.Elem(), target, seen)
	case reflect.Struct:
		for field := range current.Fields() {
			if typeContains(field.Type, target, seen) {
				return true
			}
		}
	}
	return false
}

func TestCodexItemPagingCursorStalesAfterStateEviction(t *testing.T) {
	fixture := &codexItemFixture{pages: map[string]codexItemPage{"": {Data: []map[string]any{
		codexNativeTurn("turn_eviction", codexAgentItems(2)),
	}}}}
	source := newCodexItemPagingSource(t, fixture)
	first, _, err := source.latestItemWindow(t.Context(), "evicted-thread", 1, "full")
	if err != nil {
		t.Fatalf("initial item window: %v", err)
	}
	if first.OlderCursor == "" {
		t.Fatal("initial item window has no cursor")
	}
	for i := range 32 {
		if _, _, err := source.latestItemWindow(t.Context(), fmt.Sprintf("evictor-%02d", i), 1, "full"); err != nil {
			t.Fatalf("materialize evictor %d: %v", i, err)
		}
	}
	_, _, err = source.previousItemWindow(t.Context(), "evicted-thread", first.OlderCursor, 1, "full")
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("cursor after state eviction error = %T %v, want stale WireError", err, err)
	}
	data, ok := wireErr.Data.(appwire.ErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptItemCursorStale {
		t.Fatalf("cursor after eviction error data = %#v, want stale cursor", wireErr.Data)
	}
}

func TestCodexItemPagingDifferentThreadsMaterializeConcurrently(t *testing.T) {
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var enterFirst sync.Once
	var enterSecond sync.Once
	var release sync.Once
	releaseBlocked := func() { release.Do(func() { close(releaseFirst) }) }
	t.Cleanup(releaseBlocked)

	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params map[string]any) (map[string]any, error) {
		threadID := fmt.Sprint(params["threadId"])
		switch threadID {
		case "thread-a":
			enterFirst.Do(func() { close(firstEntered) })
			<-releaseFirst
		case "thread-b":
			enterSecond.Do(func() { close(secondEntered) })
		}
		return map[string]any{"data": []map[string]any{codexNativeTurn("turn_"+threadID, codexAgentItems(1))}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(httpServer.Close)
	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())

	errs := make(chan error, 2)
	go func() {
		_, _, err := source.latestItemWindow(t.Context(), "thread-a", 1, "full")
		errs <- err
	}()
	<-firstEntered
	go func() {
		_, _, err := source.latestItemWindow(t.Context(), "thread-b", 1, "full")
		errs <- err
	}()
	select {
	case <-secondEntered:
		releaseBlocked()
	case <-time.After(time.Second):
		releaseBlocked()
		<-errs
		<-errs
		t.Fatal("thread-b did not enter materialization while unrelated thread-a was blocked")
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent materialization: %v", err)
		}
	}
}

func TestCodexItemPagingSameThreadConcurrentIdentity(t *testing.T) {
	fixture := &codexItemFixture{pages: map[string]codexItemPage{"": {Data: []map[string]any{
		codexNativeTurn("turn_same", codexAgentItems(2)),
	}}}}
	source := newCodexItemPagingSource(t, fixture)
	identities := make(chan appitempaging.CursorIdentity, 16)
	errs := make(chan error, 16)
	var callers sync.WaitGroup
	for range 16 {
		callers.Go(func() {
			_, identity, err := source.latestItemWindow(t.Context(), "same-thread", 1, "full")
			identities <- identity
			errs <- err
		})
	}
	callers.Wait()
	close(identities)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("same-thread materialization: %v", err)
		}
	}
	want := ""
	for identity := range identities {
		if want == "" {
			want = identity.Incarnation
		}
		if identity.Incarnation != want {
			t.Fatalf("same-thread concurrent incarnation = %q, want %q", identity.Incarnation, want)
		}
	}
	if got := retainedPagingLockCount(t, source); got != 0 {
		t.Fatalf("keyed paging lock entries after callers completed = %d, want 0", got)
	}
}

func retainedPagingStateCount(t *testing.T, storage any) int {
	t.Helper()
	value := reflect.ValueOf(storage)
	if value.Kind() == reflect.Map {
		return value.Len()
	}
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		t.Fatalf("retained paging state storage = %T, want map or struct pointer", storage)
	}
	entries := value.FieldByName("entries")
	if !entries.IsValid() || entries.Kind() != reflect.Map {
		t.Fatalf("retained paging state storage = %T, want entries map", storage)
	}
	return entries.Len()
}

func retainedItemSnapshotStates(cache *itemSnapshotStateCache) []itemSnapshotState {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	states := make([]itemSnapshotState, 0, len(cache.entries))
	for _, element := range cache.entries {
		states = append(states, element.Value.(itemSnapshotStateEntry).state)
	}
	return states
}

func retainedPagingLockCount(t *testing.T, source any) int {
	t.Helper()
	value := reflect.ValueOf(source)
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	locks := value.FieldByName("itemPagingLocks")
	if !locks.IsValid() {
		t.Fatalf("paging source %T has no lifecycle-managed itemPagingLocks", source)
	}
	if locks.Kind() == reflect.Pointer {
		locks = locks.Elem()
	}
	entries := locks.FieldByName("entries")
	if !entries.IsValid() || entries.Kind() != reflect.Map {
		t.Fatalf("paging locks %T have no entries map", source)
	}
	return entries.Len()
}

func setCodexFixtureTurns(fixture *codexItemFixture, turns ...map[string]any) {
	fixture.mu.Lock()
	fixture.pages = map[string]codexItemPage{"": {Data: []map[string]any{turns[0]}}}
	page := fixture.pages[""]
	page.Data = append(page.Data, turns[1:]...)
	fixture.pages[""] = page
	fixture.nativeCursors = nil
	fixture.mu.Unlock()
}

func newLocalItemReadConversionSource(t *testing.T, threadID string) *LocalDaemonSource {
	t.Helper()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, SourceID: "local", ThreadID: threadID, SessionID: threadID,
		WorkspaceRef: "local:" + threadID, InstanceID: "instance-1", Endpoint: "ws://unused.test",
	}
	return NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry}}
	}, nil)
}

func positionedItemReadResponse(first, last uint64, olderCursor string) appwire.ThreadReadResponse {
	keys := make([]string, 0, last-first+1)
	positions := make([]uint64, 0, last-first+1)
	for position := first; position <= last; position++ {
		keys = append(keys, fmt.Sprintf("key-%02d", position))
		positions = append(positions, position)
	}
	return positionedItemReadResponseFor(keys, positions, olderCursor)
}

func positionedItemReadResponseFor(keys []string, positions []uint64, olderCursor string) appwire.ThreadReadResponse {
	items := make([]appwire.ThreadItem, len(keys))
	for index, key := range keys {
		position := appwire.ThreadItemPosition{Entry: positions[index]}
		items[index] = appwire.ThreadItem{
			ID: key, TurnID: "turn_positioned", TranscriptKey: key, Position: &position,
		}
	}
	return appwire.ThreadReadResponse{
		Thread:   appwire.Thread{ID: "positioned", Turns: []appwire.Turn{{ID: "turn_positioned", Items: items}}},
		PageUnit: appwire.TranscriptPageUnitItem, OlderCursor: olderCursor,
	}
}

func codexNativeTurn(id string, items []map[string]any) map[string]any {
	return map[string]any{"id": id, "status": "completed", "items": items}
}

func codexAgentItems(count int) []map[string]any {
	items := make([]map[string]any, 0, count)
	for i := range count {
		items = append(items, map[string]any{"type": "agentMessage", "id": fmt.Sprintf("item-%02d", i), "text": fmt.Sprintf("text-%02d", i)})
	}
	return items
}
