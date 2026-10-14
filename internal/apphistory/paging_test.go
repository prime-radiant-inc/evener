package apphistory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

func conversationParams() appwire.ThreadContentParams {
	no := false
	return appwire.ThreadContentParams{IncludeThinking: &no, IncludeToolCalls: &no, IncludeToolCallIntents: &no, IncludeSystemMessages: &no, IncludeDiagnostics: &no}
}

// A source page uses the same positioned-candidate selection and opaque cursor
// as a transcript producer. The adapter is exercised across real page boundaries.
type historyFixture struct {
	candidates   []appitempaging.TranscriptItemCandidate
	reads, lists int
	held         *appwire.SnapshotIdentity
}

func fixtureHistory() *historyFixture {
	f := &historyFixture{}
	for i := 1; i <= 205; i++ {
		kind := "commandExecution"
		if i%20 == 1 {
			kind = "agentMessage"
		}
		position := appwire.ThreadItemPosition{Entry: uint64(i), Item: 0}
		f.candidates = append(f.candidates, appitempaging.TranscriptItemCandidate{TurnID: "turn", Turn: appwire.Turn{ID: "turn"}, Position: position, Item: appwire.ThreadItem{Type: kind, ID: strconv.Itoa(i), TranscriptKey: strconv.Itoa(i), Position: &position, TurnID: "turn", Text: strconv.Itoa(i), Output: "tool-data"}})
	}
	return f
}

func (f *historyFixture) page(before *appwire.ThreadItemPosition, limit int) (appwire.ThreadTurnsListResponse, error) {
	selected, older, err := appitempaging.SelectCandidates(f.candidates, before, limit)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	turns, err := appitempaging.RegroupTurnFragments(appitempaging.NormalizeProjectedItemCompleteness(selected))
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	cursor := ""
	if older {
		cursor, err = appitempaging.EncodeCursor(appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "inc", ProjectionVersion: 1}, selected[0].Position)
	}
	return appwire.ThreadTurnsListResponse{Data: turns, NextCursor: cursor, Snapshot: &appwire.SnapshotIdentity{Incarnation: "inc", Length: 205}, BootGeneration: "7", Epoch: 3, Authoritative: true}, err
}
func (f *historyFixture) read(_ context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	f.reads++
	f.held = p.HeldSnapshot
	if p.Selection() != 0 {
		return appwire.ThreadReadResponse{}, errors.New("shared source was filtered")
	}
	page, err := f.page(nil, p.ItemLimit)
	response := appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "thread", Turns: page.Data}, OlderCursor: page.NextCursor, Snapshot: page.Snapshot, BootGeneration: page.BootGeneration, Epoch: page.Epoch, Authoritative: true, RequestGeneration: p.RequestGeneration}
	response.Thread.Evener.Diagnostics = &appwire.EvenerDiagnostics{}
	response.Thread.Evener.MutationStateAuthoritative = true
	if !p.IncludeTurns {
		response.Thread.Turns = nil
		response.OlderCursor = ""
	}
	return response, err
}
func (f *historyFixture) list(_ context.Context, p appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	f.lists++
	if p.Selection() != 0 {
		return appwire.ThreadTurnsListResponse{}, errors.New("shared source was filtered")
	}
	var before appwire.ThreadItemPosition
	if p.Before != nil {
		before = *p.Before
	} else {
		var err error
		before, err = appitempaging.DecodeCursor(p.Cursor, appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "inc", ProjectionVersion: 1})
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
	}
	return f.page(&before, p.ItemLimit)
}
func ids(turns []appwire.Turn) []string {
	var out []string
	for _, turn := range turns {
		for _, item := range turn.Items {
			out = append(out, item.ID)
		}
	}
	return out
}

func TestSelectedHistoryFillsPagesAndReachesOldest(t *testing.T) {
	t.Parallel()
	f := fixtureHistory()
	content := conversationParams()
	r, err := Read(t.Context(), appwire.ThreadReadParams{ThreadContentParams: content, Ref: "local:thread", IncludeTurns: true, ItemLimit: 3, RequestGeneration: 12}, f.read, f.list)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(r.Thread.Turns); !reflect.DeepEqual(got, []string{"161", "181", "201"}) {
		t.Fatalf("first page=%v", got)
	}
	if r.Thread.Evener.Diagnostics != nil || !r.Thread.Evener.MutationStateAuthoritative || r.RequestGeneration != 12 || r.BootGeneration != "7" || r.Epoch != 3 || !r.Authoritative {
		t.Fatal("envelope filtering lost authority or retained diagnostics")
	}
	if r.Snapshot.Incarnation == "inc" || sourceSnapshot(r.Snapshot, content.Selection()).Incarnation != "inc" {
		t.Fatal("snapshot not bound to selection")
	}
	all := ids(r.Thread.Turns)
	for cursor := r.OlderCursor; cursor != ""; {
		page, err := List(t.Context(), appwire.ThreadTurnsListParams{ThreadContentParams: content, Ref: "local:thread", Cursor: cursor, ItemLimit: 3}, f.list)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids(page.Data)) == 0 {
			t.Fatal("empty intermediate page")
		}
		all = append(ids(page.Data), all...)
		cursor = page.NextCursor
	}
	var want []string
	for i := 1; i <= 201; i += 20 {
		want = append(want, strconv.Itoa(i))
	}
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("paged ids=%v want=%v", all, want)
	}
}

func TestSelectedHistoryViewChangesFenceCursorAndHeldSnapshot(t *testing.T) {
	t.Parallel()
	f := fixtureHistory()
	p := appwire.ThreadReadParams{ThreadContentParams: conversationParams(), Ref: "local:thread", IncludeTurns: true, ItemLimit: 2}
	r, err := Read(t.Context(), p, f.read, f.list)
	if err != nil {
		t.Fatal(err)
	}
	changed := p.ThreadContentParams
	changed.IncludeThinking = nil
	_, err = List(t.Context(), appwire.ThreadTurnsListParams{ThreadContentParams: changed, Ref: p.Ref, Cursor: r.OlderCursor}, f.list)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.TranscriptItemCursorStale().Code {
		t.Fatalf("different view cursor accepted: %v", err)
	}
	p.HeldSnapshot = r.Snapshot
	p.ThreadContentParams = changed
	_, err = Read(t.Context(), p, f.read, f.list)
	if err != nil {
		t.Fatal(err)
	}
	if f.held != nil {
		t.Fatal("incompatible held snapshot sent to source")
	}
	p.ThreadContentParams = conversationParams()
	_, err = Read(t.Context(), p, f.read, f.list)
	if err != nil {
		t.Fatal(err)
	}
	if f.held == nil || f.held.Incarnation != "inc" {
		t.Fatal("compatible held snapshot not restored to source identity")
	}
}

func TestSelectedHistoryDefaultAndMetadataReadsDoNoExtraPaging(t *testing.T) {
	t.Parallel()
	f := fixtureHistory()
	p := appwire.ThreadReadParams{IncludeTurns: true, ItemLimit: 3}
	want, err := f.read(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Read(t.Context(), p, f.read, f.list)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	if !bytes.Equal(a, b) || f.lists != 0 {
		t.Fatal("default response changed")
	}
	p.IncludeTurns = false
	p.ThreadContentParams = conversationParams()
	_, err = Read(t.Context(), p, f.read, f.list)
	if err != nil {
		t.Fatal(err)
	}
	if f.lists != 0 {
		t.Fatal("metadata-only read fetched history")
	}
}

func TestSelectedHistoryCancellationAndNoProgress(t *testing.T) {
	t.Parallel()
	f := fixtureHistory()
	p := appwire.ThreadReadParams{ThreadContentParams: conversationParams(), IncludeTurns: true, ItemLimit: 8}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Read(ctx, p, f.read, f.list)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
	if f.reads != 0 {
		t.Fatal("canceled read reached source")
	}
	_, err = Read(t.Context(), p, f.read, func(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		return f.page(nil, 40)
	})
	if err == nil {
		t.Fatal("non-progressing source accepted")
	}
}

func TestSelectedHistoryPreservesChangesOutsideSelectedWindow(t *testing.T) {
	t.Parallel()
	f := fixtureHistory()
	p := appwire.ThreadReadParams{ThreadContentParams: conversationParams(), Ref: "local:thread", IncludeTurns: true, ItemLimit: 1}
	read := func(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		r, err := f.read(ctx, p)
		r.Changes = &appwire.HistoryChanges{Items: []appwire.ThreadItem{f.candidates[20].Item, f.candidates[21].Item}}
		return r, err
	}
	r, err := Read(t.Context(), p, read, f.list)
	if err != nil {
		t.Fatal(err)
	}
	var changed []string
	for _, item := range r.Changes.Items {
		changed = append(changed, item.ID)
	}
	// 181 was in the source's forty-item window, so the source omitted it
	// from Changes. The selected one-item window must not silently drop it.
	if !reflect.DeepEqual(changed, []string{"21", "181"}) {
		t.Fatalf("changes=%v", changed)
	}
}

func TestSelectedHistoryByteLimitPagesDroppedItemExactlyOnce(t *testing.T) {
	t.Parallel()
	f := fixtureHistory()
	for i := range f.candidates {
		f.candidates[i].Item.Type = "agentMessage"
		f.candidates[i].Item.Text = strings.Repeat("x", 600000)
	}
	p := appwire.ThreadReadParams{ThreadContentParams: conversationParams(), Ref: "local:thread", IncludeTurns: true, ItemLimit: 3}
	r, err := Read(t.Context(), p, f.read, f.list)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(r.Thread.Turns); !reflect.DeepEqual(got, []string{"205"}) {
		t.Fatalf("byte-limited page=%v", got)
	}
	page, err := List(t.Context(), appwire.ThreadTurnsListParams{ThreadContentParams: p.ThreadContentParams, Ref: p.Ref, Cursor: r.OlderCursor, ItemLimit: 1}, f.list)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page.Data); !reflect.DeepEqual(got, []string{"204"}) {
		t.Fatalf("byte-limited backfill=%v", got)
	}
}

func TestSelectedHistoryPreservesTurnChangesOutsideSelectedWindow(t *testing.T) {
	t.Parallel()
	f := fixtureHistory()
	for i := range f.candidates {
		id := fmt.Sprintf("turn-%d", i/20)
		f.candidates[i].TurnID = id
		f.candidates[i].Turn = appwire.Turn{ID: id, Status: appwire.TurnStatusCompleted, Version: 206, Usage: &appwire.EvenerUsage{InputTokens: 42}}
		f.candidates[i].Item.TurnID = id
	}
	read := func(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		r, err := f.read(ctx, p)
		r.Changes = &appwire.HistoryChanges{}
		return r, err
	}
	r, err := Read(t.Context(), appwire.ThreadReadParams{ThreadContentParams: conversationParams(), Ref: "local:thread", IncludeTurns: true, ItemLimit: 1}, read, f.list)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Thread.Turns) != 1 || r.Thread.Turns[0].ID != "turn-10" {
		t.Fatal("latest selected turn changed")
	}
	if len(r.Changes.Turns) != 2 {
		t.Fatalf("dropped turn scalar updates=%d want=2", len(r.Changes.Turns))
	}
	for i, turn := range r.Changes.Turns {
		if turn.ID != fmt.Sprintf("turn-%d", i+8) || turn.Status != appwire.TurnStatusCompleted || turn.Version != 206 || turn.Usage == nil || turn.Usage.InputTokens != 42 || len(turn.Items) != 0 {
			t.Fatalf("incorrect dropped turn scalar update: %+v", turn)
		}
	}
}

func TestSelectedHistoryRefusesChangedSourceIdentity(t *testing.T) {
	t.Parallel()
	f := fixtureHistory()
	p := appwire.ThreadReadParams{ThreadContentParams: conversationParams(), Ref: "local:thread", IncludeTurns: true, ItemLimit: 4}
	list := func(ctx context.Context, p appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		r, err := f.list(ctx, p)
		r.Snapshot.Incarnation = "replacement"
		return r, err
	}
	_, err := Read(t.Context(), p, f.read, list)
	if _, ok := errors.AsType[appwire.WireError](err); !ok {
		t.Fatalf("mixed incarnations accepted: %v", err)
	}
}

func TestSelectedHistoryAdvancesAcrossSubPositions(t *testing.T) {
	t.Parallel()
	f := fixtureHistory()
	for i := range f.candidates {
		p := appwire.ThreadItemPosition{Entry: 1, Item: 0, Sub: uint32(i)}
		f.candidates[i].Position = p
		f.candidates[i].Item.Position = &p
	}
	r, err := Read(t.Context(), appwire.ThreadReadParams{ThreadContentParams: conversationParams(), IncludeTurns: true, ItemLimit: 4}, f.read, f.list)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(r.Thread.Turns); !reflect.DeepEqual(got, []string{"141", "161", "181", "201"}) {
		t.Fatalf("sub-position page=%v", got)
	}
}

func TestSelectedHistoryContinuationFencesTargetAndLifetime(t *testing.T) {
	for _, change := range []string{"ref", "thread-id", "boot", "epoch", "snapshot"} {
		t.Run(change, func(t *testing.T) {
			f := fixtureHistory()
			params := appwire.ThreadReadParams{ThreadContentParams: conversationParams(), Ref: "local:thread", ThreadID: "thread", IncludeTurns: true, ItemLimit: 2}
			read, err := Read(t.Context(), params, f.read, f.list)
			if err != nil {
				t.Fatal(err)
			}
			older := appwire.ThreadTurnsListParams{ThreadContentParams: params.ThreadContentParams, Ref: params.Ref, ThreadID: params.ThreadID, Cursor: read.OlderCursor, ItemLimit: 2}
			if change == "ref" {
				older.Ref = "another:thread"
			}
			if change == "thread-id" {
				older.ThreadID = "another"
			}
			_, err = List(t.Context(), older, func(ctx context.Context, p appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
				page, err := f.list(ctx, p)
				switch change {
				case "boot":
					page.BootGeneration = "8"
				case "epoch":
					page.Epoch++
				case "snapshot":
					page.Snapshot.Incarnation = "replacement"
				}
				return page, err
			})
			wire, ok := errors.AsType[appwire.WireError](err)
			data, hasData := appwire.ErrorDataOf(wire.Data)
			if !ok || !hasData || data.EvenerErrorInfo != appwire.ErrorTranscriptItemCursorStale {
				t.Fatalf("changed %s returned %v, want a stale cursor", change, err)
			}
		})
	}
}

func TestSelectedHistoryContinuationAllowsAppendAndBefore(t *testing.T) {
	f := fixtureHistory()
	params := appwire.ThreadReadParams{ThreadContentParams: conversationParams(), Ref: "local:thread", IncludeTurns: true, ItemLimit: 2}
	read, err := Read(t.Context(), params, f.read, f.list)
	if err != nil {
		t.Fatal(err)
	}
	before := appwire.ThreadItemPosition{Entry: 141}
	page, err := List(t.Context(), appwire.ThreadTurnsListParams{ThreadContentParams: params.ThreadContentParams, Ref: params.Ref, Cursor: read.OlderCursor, Before: &before, ItemLimit: 2}, func(ctx context.Context, p appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		page, err := f.list(ctx, p)
		page.Snapshot.Length++
		return page, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page.Data); !reflect.DeepEqual(got, []string{"101", "121"}) {
		t.Fatalf("before override after append=%v", got)
	}
}
