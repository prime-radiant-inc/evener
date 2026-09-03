package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func TestHubRPCItemListNativeAndLegacyParity(t *testing.T) {
	const ref = "codex:item-list-parity"
	identity := appitempaging.CursorIdentity{
		ThreadRef:         ref,
		Incarnation:       "item-list-parity",
		ProjectionVersion: 1,
	}
	candidates := testItemCandidates(45)
	for i := range candidates {
		candidates[i].HasEarlierItems = i > 0
		candidates[i].HasLaterItems = i+1 < len(candidates)
	}
	turns, err := appitempaging.RegroupTurnFragments(candidates)
	if err != nil {
		t.Fatalf("group fixture: %v", err)
	}
	for i := range turns {
		// The legacy page contains the complete turn. The conversion helper
		// derives per-item boundaries from the item indexes, so the turn-level
		// flags must not repeat the candidate-run flags used by the native case.
		turns[i].HasEarlierItems = false
		turns[i].HasLaterItems = false
	}
	olderCursor, err := appitempaging.EncodeCursor(identity, candidates[0].Position)
	if err != nil {
		t.Fatalf("encode fixture cursor: %v", err)
	}
	thread := appwire.Thread{
		ID:        "item-list-parity",
		SessionID: "item-list-parity",
		CWD:       "/tmp/item-list-parity",
		Evener:    appwire.EvenerThread{Ref: ref},
	}
	read := appwire.ThreadReadResponse{Thread: thread}
	list := appwire.ThreadTurnsListResponse{
		Data:       turns,
		PageUnit:   appwire.TranscriptPageUnitItem,
		NextCursor: olderCursor,
	}
	nativeResult := appsource.ItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates, OlderCursor: olderCursor},
		Identity:   identity,
		Exhausted:  false,
	}

	for _, tc := range []struct {
		name      string
		canceled  bool
		wantError bool
	}{
		{name: "packed page"},
		{name: "canceled source", canceled: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			native := &itemPackingRPCSource{
				read:                 read,
				readCandidates:       nativeResult,
				rejectLegacyItemList: true,
				listCandidates: func(ctx context.Context, _ appwire.ThreadTurnsListParams) (appsource.ItemCandidateResult, error) {
					if tc.canceled {
						return appsource.ItemCandidateResult{}, ctx.Err()
					}
					return nativeResult, nil
				},
			}
			legacy := &legacyItemListRPCSource{
				read: read,
				list: list,
			}

			nativeResponse, nativeErr := dispatchHubItemList(ctx, t, native, ref)
			legacyResponse, legacyErr := dispatchHubItemList(ctx, t, legacy, ref)
			if tc.wantError {
				if !errors.Is(nativeErr, context.Canceled) || !errors.Is(legacyErr, context.Canceled) {
					t.Fatalf("canceled errors = native %v, legacy %v; want context.Canceled", nativeErr, legacyErr)
				}
				return
			}
			if nativeErr != nil || legacyErr != nil {
				t.Fatalf("list errors = native %v, legacy %v", nativeErr, legacyErr)
			}
			if !reflect.DeepEqual(nativeResponse, legacyResponse) {
				t.Fatalf("native response = %+v, legacy response = %+v", nativeResponse, legacyResponse)
			}
			if got := len(flattenTestItems(nativeResponse.Data)); got != 40 {
				t.Fatalf("packed item count = %d, want 40", got)
			}
			if nativeResponse.NextCursor == "" {
				t.Fatal("packed response omitted older cursor")
			}
		})
	}
}

func dispatchHubItemList(ctx context.Context, t *testing.T, source appsource.Source, ref string) (appwire.ThreadTurnsListResponse, error) {
	t.Helper()
	sources := appsource.NewRegistry()
	sources.Add(source)
	server := newHubAppServer(hubcore.WebConfig{}, sources)
	value, err := server.Router().Dispatch(ctx, appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodThreadTurnsList,
		Params: mustJSON(t, appwire.ThreadTurnsListParams{Ref: ref, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40}),
	})
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	response, ok := value.(appwire.ThreadTurnsListResponse)
	if !ok {
		t.Fatalf("item list response = %T, want ThreadTurnsListResponse", value)
	}
	return response, nil
}

type legacyItemListRPCSource struct {
	relayLifecycleSource
	read appwire.ThreadReadResponse
	list appwire.ThreadTurnsListResponse
}

func (s *legacyItemListRPCSource) ReadThread(ctx context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if err := ctx.Err(); err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	return s.read, nil
}

func (*legacyItemListRPCSource) RelayOnThreadRead() bool { return false }

func (s *legacyItemListRPCSource) ListTurns(ctx context.Context, _ appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	if err := ctx.Err(); err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	return s.list, nil
}

// TestAppRPCAtomicItemPagingEndToEnd keeps the public hub boundary honest for
// every source family. The source fixture is deliberately positioned data,
// rather than a response fixture: the hub must select pages, regroup turn
// fragments, and mint the opaque continuation itself.
func TestAppRPCAtomicItemPagingEndToEnd(t *testing.T) {
	for _, sourceKind := range []string{"live-daemon", "ended-local", "codex"} {
		t.Run(sourceKind, func(t *testing.T) {
			ref := pagingSourceID(sourceKind) + ":paging-thread"
			fixture := newRPCAtomicPagingFixture(t, sourceKind, ref)
			switch sourceKind {
			case "codex":
				if _, ok := fixture.Source.(*appsource.CodexSource); !ok {
					t.Fatalf("source = %T, want production CodexSource", fixture.Source)
				}
			default:
				if _, ok := fixture.Source.(*appsource.LocalDaemonSource); !ok {
					t.Fatalf("source = %T, want production LocalDaemonSource", fixture.Source)
				}
			}
			sources := appsource.NewRegistry()
			sources.Add(fixture)
			server := newHubAppServer(hubcore.WebConfig{}, sources)

			readValue, err := server.Router().Dispatch(t.Context(), appwire.Request{
				ID: appwire.NewIntID(1), Method: appwire.MethodThreadRead,
				Params: mustJSON(t, appwire.ThreadReadParams{
					Ref: ref, IncludeTurns: true, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40,
				}),
			})
			if err != nil {
				t.Fatalf("initial thread/read: %v", err)
			}
			read, ok := readValue.(appwire.ThreadReadResponse)
			if !ok {
				t.Fatalf("initial response = %T, want ThreadReadResponse", readValue)
			}
			if got := len(flattenTestItems(read.Thread.Turns)); got != 40 {
				t.Fatalf("initial item count = %d, want 40", got)
			}
			if read.OlderCursor == "" || isDecimalPagingCursor(read.OlderCursor) {
				t.Fatalf("initial cursor = %q, want opaque non-decimal cursor", read.OlderCursor)
			}
			initialCalls := pagingPathCounts(fixture.sourcePathCalls())
			if sourceKind == "codex" && initialCalls["codex.read"] != 1 {
				t.Fatalf("initial codex read calls = %d, want 1; native sequence=%v", initialCalls["codex.read"], fixture.sourcePathCalls())
			}
			wantLists := 0
			if sourceKind == "codex" {
				wantLists = 1
			}
			if got := initialCalls[sourceKind+".list"]; got != wantLists {
				t.Fatalf("initial %s materialization list calls = %d, want %d; native sequence=%v", sourceKind, got, wantLists, fixture.sourcePathCalls())
			}
			assertPublicItemPage(t, read.Thread.Turns)
			assertItemPageFitsSoftLimit(t, read)
			all := append([]appwire.ThreadItem(nil), flattenTestItems(read.Thread.Turns)...)
			initialIDs := itemIDs(all)
			allTurns := append([]appwire.Turn(nil), read.Thread.Turns...)
			cursor := read.OlderCursor
			pageCount := 1
			for cursor != "" {
				value, listErr := server.Router().Dispatch(t.Context(), appwire.Request{
					ID: appwire.NewIntID(int64(pageCount + 1)), Method: appwire.MethodThreadTurnsList,
					Params: mustJSON(t, appwire.ThreadTurnsListParams{
						Ref: ref, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40, Cursor: cursor,
					}),
				})
				if listErr != nil {
					t.Fatalf("older page %d: %v", pageCount, listErr)
				}
				page, ok := value.(appwire.ThreadTurnsListResponse)
				if !ok {
					t.Fatalf("older page %d = %T, want ThreadTurnsListResponse", pageCount, value)
				}
				items := flattenTestItems(page.Data)
				if len(items) == 0 || len(items) > 40 {
					t.Fatalf("older page %d item count = %d, want 1..40", pageCount, len(items))
				}
				assertPublicItemPage(t, page.Data)
				assertItemPageFitsSoftLimit(t, page)
				if page.NextCursor != "" && isDecimalPagingCursor(page.NextCursor) {
					t.Fatalf("older page %d cursor = %q, want opaque non-decimal cursor", pageCount, page.NextCursor)
				}
				all = append(all, items...)
				allTurns = append(allTurns, page.Data...)
				if pageCount == 1 {
					if got := itemIDs(items); !slicesEqual(got, fixture.expectedIDs[:5]) {
						t.Fatalf("older page IDs = %v, want independently defined older page IDs", got)
					}
					if page.NextCursor != "" {
						t.Fatalf("final older page cursor = %q, want exhausted", page.NextCursor)
					}
				}
				cursor = page.NextCursor
				pageCount++
			}
			if pageCount != 2 {
				t.Fatalf("page count = %d, want initial plus one older page", pageCount)
			}

			expected := fixture.expectedIDs
			assertConcreteSourcePaths(t, sourceKind, fixture.sourcePathCalls())
			if !slicesEqual(initialIDs, expected[5:]) {
				t.Fatalf("initial page owns IDs %v, want independently defined newest IDs %v", initialIDs, expected[5:])
			}
			sort.SliceStable(all, func(i, j int) bool {
				if all[i].Position == nil || all[j].Position == nil {
					return i < j
				}
				return all[i].Position.Entry < all[j].Position.Entry ||
					(all[i].Position.Entry == all[j].Position.Entry && all[i].Position.Item < all[j].Position.Item)
			})
			if got := itemIDs(all); !slicesEqual(got, expected) {
				t.Fatalf("flattened IDs = %v, independently projected IDs = %v", got, expected)
			}
			if len(uniquePagingStrings(itemIDs(all))) != len(expected) {
				t.Fatalf("flattened pages duplicate item IDs: %v", itemIDs(all))
			}
			if !containsItem(all, "item_tool_call") || !containsItem(all, "item_tool_result_tool") {
				t.Fatal("tool call/result did not survive the page boundary")
			}
			call := itemByID(all, "item_tool_call")
			result := itemByID(all, "item_tool_result_tool")
			if call.ArgumentsJSON == "" || strings.Trim(result.Output, `"`) != "paging complete" || (sourceKind != "codex" && call.CallID != result.CallID) || (sourceKind == "codex" && (call.CallID == "" || result.CallID == "")) {
				t.Fatalf("split tool call/result = call=%+v result=%+v (args=%q output=%q callID=%q resultCallID=%q source=%q), want complete shared call", call, result, call.ArgumentsJSON, result.Output, call.CallID, result.CallID, sourceKind)
			}
			if !containsTurnItem(read.Thread.Turns, "turn_shared", "item-44") ||
				!containsTurnItem(allTurns, "turn_shared", "item_tool_call") {
				t.Fatal("shared turn did not cross the initial page boundary")
			}
			if !hasCrossBoundaryEntry(fixture.candidates, read.Thread.Turns, all) {
				t.Fatal("shared transcript entry did not cross the page boundary")
			}
			if !fixture.hasConcreteBoundary() {
				t.Fatal("fixture does not place an item, entry, and turn across the page boundary")
			}

			staleCursor := read.OlderCursor
			fixture.replaceIncarnation()
			_, staleErr := server.Router().Dispatch(t.Context(), appwire.Request{
				ID: appwire.NewIntID(99), Method: appwire.MethodThreadTurnsList,
				Params: mustJSON(t, appwire.ThreadTurnsListParams{
					Ref: ref, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40, Cursor: staleCursor,
				}),
			})
			var wireErr appwire.WireError
			if !errors.As(staleErr, &wireErr) || wireErr.Data == nil {
				t.Fatalf("replaced-incarnation error = %T %v, want typed stale cursor", staleErr, staleErr)
			}
			data, ok := wireErr.Data.(appwire.ErrorData)
			if !ok || data.EvenerErrorInfo != "transcriptItemCursorStale" {
				t.Fatalf("replaced-incarnation wire data = %#v, want transcriptItemCursorStale", wireErr.Data)
			}
			serialized, marshalErr := json.Marshal(appserver.WireError(staleErr))
			if marshalErr != nil {
				t.Fatalf("marshal stale cursor WireError: %v", marshalErr)
			}
			if strings.Contains(string(serialized), staleCursor) {
				t.Fatalf("serialized stale cursor WireError echoes opaque cursor bytes: %s", serialized)
			}
		})
	}
}

func TestHubRPCRealLocalItemReadUsesOneReadAndPreservesHandoff(t *testing.T) {
	const sessionID = "02wMz5Txv733WHFsVy66SR"
	var readCalls atomic.Int32
	var listCalls atomic.Int32
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		readCalls.Add(1)
		appserver.Subscribe(ctx, sessionID)
		position := appwire.ThreadItemPosition{Entry: 1}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref},
			Turns: []appwire.Turn{{ID: "turn_snapshot", Items: []appwire.ThreadItem{{
				ID: "item_snapshot", TurnID: "turn_snapshot", TranscriptKey: "snapshot-key", Position: &position, Text: "snapshot",
			}}}},
		}, PageUnit: appwire.TranscriptPageUnitItem}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadTurnsList, func(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		listCalls.Add(1)
		return appwire.ThreadTurnsListResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID: 21 * 1000, Protocol: appwire.ProtocolVersion, Endpoint: "ws" + daemonHTTP.URL[len("http"):],
		SourceID: "local", ThreadID: sessionID, SessionID: sessionID,
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
	t.Cleanup(hub.Close)
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	response, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{
		Ref: "local:" + sessionID, IncludeTurns: true, Subscribe: true,
		PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40,
	})
	if err != nil {
		t.Fatalf("item thread/read: %v", err)
	}
	if got := itemIDs(flattenTestItems(response.Thread.Turns)); !slicesEqual(got, []string{"item_snapshot"}) {
		t.Fatalf("snapshot item IDs = %v, want [item_snapshot]", got)
	}
	if got := readCalls.Load(); got != 1 {
		t.Fatalf("production Local item read calls = %d, want exactly 1", got)
	}
	if got := listCalls.Load(); got != 0 {
		t.Fatalf("production Local turns/list calls = %d, want 0", got)
	}

	daemon.Broadcast(sessionID, appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
		ThreadID: sessionID, Ref: "local:" + sessionID, TurnID: "turn_live",
		Item: appwire.ThreadItem{ID: "item_after_snapshot", TurnID: "turn_live", Status: appwire.TurnStatusCompleted},
	})
	deadline := time.After(2 * time.Second)
	for {
		select {
		case notification := <-client.Notifications():
			if notification.Method != appwire.NotifyItemCompleted {
				continue
			}
			var params appwire.ItemLifecycleParams
			if err := json.Unmarshal(notification.Params, &params); err != nil {
				t.Fatalf("unmarshal relayed item: %v", err)
			}
			if params.Item.ID != "item_after_snapshot" {
				t.Fatalf("relayed item ID = %q, want item_after_snapshot", params.Item.ID)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for post-snapshot notification; handoff was not committed")
		}
	}
}

func TestPackTranscriptItemPageByteFitOwnsContinuation(t *testing.T) {
	const count = 45
	const payloadBytes = 30000
	turn := appwire.Turn{ID: "byte-fit-turn", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull}
	candidates := make([]appitempaging.TranscriptItemCandidate, count)
	for i := range candidates {
		position := appwire.ThreadItemPosition{Entry: 11, Item: uint32(i)}
		item := appwire.ThreadItem{
			Type: "agentMessage", ID: fmt.Sprintf("byte-item-%02d", i), TranscriptKey: fmt.Sprintf("byte-key-%02d", i),
			Position: &position, TurnID: turn.ID, Text: strings.Repeat("x", payloadBytes), Status: appwire.TurnStatusCompleted,
		}
		candidates[i] = appitempaging.TranscriptItemCandidate{TurnID: turn.ID, Turn: turn, Item: item, Position: position, HasEarlierItems: i > 0, HasLaterItems: i+1 < count}
	}
	identity := appitempaging.CursorIdentity{ThreadRef: "local:byte-fit", Incarnation: "byte-fit-incarnation", ProjectionVersion: 1}
	got, err := packThreadReadItemCandidates(transcriptItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates}, Identity: identity,
	}, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) { return response, nil })
	if err != nil {
		t.Fatalf("byte-fit pack: %v", err)
	}
	initial := flattenTestItems(got.Thread.Turns)
	if len(initial) >= 40 || len(initial) == 0 {
		t.Fatalf("byte-fit initial count = %d, want a nonempty page smaller than count limit 40", len(initial))
	}
	if got.OlderCursor == "" || isDecimalPagingCursor(got.OlderCursor) {
		t.Fatalf("byte-fit cursor = %q, want opaque continuation", got.OlderCursor)
	}
	assertPublicItemPage(t, got.Thread.Turns)
	assertItemPageFitsSoftLimit(t, got)
	before, err := appitempaging.DecodeCursor(got.OlderCursor, identity)
	if err != nil {
		t.Fatalf("byte-fit cursor decode: %v", err)
	}
	if before != candidates[count-len(initial)].Position {
		t.Fatalf("byte-fit cursor owns position %+v, want %+v", before, candidates[count-len(initial)].Position)
	}
	older, hasOlder, err := appitempaging.SelectCandidates(candidates, &before, 40)
	if err != nil {
		t.Fatalf("byte-fit older select: %v", err)
	}
	if hasOlder {
		t.Fatalf("byte-fit older selection unexpectedly has another page: %d candidates", len(older))
	}
	olderResponse, err := packThreadReadItemCandidates(transcriptItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: older}, Identity: identity, Exhausted: true,
	}, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) { return response, nil })
	if err != nil {
		t.Fatalf("byte-fit older pack: %v", err)
	}
	if olderResponse.OlderCursor != "" {
		t.Fatalf("byte-fit older cursor = %q, want exhausted", olderResponse.OlderCursor)
	}
	olderItems := flattenTestItems(olderResponse.Thread.Turns)
	if len(olderItems) == 0 || len(olderItems)+len(initial) != count {
		t.Fatalf("byte-fit ownership counts = initial %d older %d, want total %d", len(initial), len(olderItems), count)
	}
	for _, item := range initial {
		if containsItem(olderItems, item.ID) {
			t.Fatalf("byte-fit item %q owned by both pages", item.ID)
		}
	}
	wantIDs := make([]string, count)
	for i := range wantIDs {
		wantIDs[i] = fmt.Sprintf("byte-item-%02d", i)
	}
	all := append(append([]appwire.ThreadItem(nil), olderItems...), initial...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].Position.Item < all[j].Position.Item })
	if !slicesEqual(itemIDs(all), wantIDs) {
		t.Fatalf("byte-fit IDs = %v, want independent fixture IDs %v", itemIDs(all), wantIDs)
	}
}

func TestHubRPCItemReadLiveEmptyUsesSavedPastFallback(t *testing.T) {
	cfg, params := seedBoundedPastThread(t)
	params.PageUnit = appwire.TranscriptPageUnitItem
	params.ItemLimit = 40
	params.TurnLimit = 0
	ref, err := appwire.ParseRef(params.Ref)
	if err != nil {
		t.Fatalf("parse saved ref: %v", err)
	}
	live := &localEmptyItemRPCSource{threadID: ref.ThreadID}
	sources := appsource.NewRegistry()
	sources.Add(live)
	server := newHubAppServer(cfg, sources)

	value, err := server.Router().Dispatch(t.Context(), appwire.Request{
		ID: appwire.NewIntID(1), Method: appwire.MethodThreadRead, Params: mustJSON(t, params),
	})
	if err != nil {
		t.Fatalf("live-empty saved fallback read: %v", err)
	}
	response, ok := value.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("live-empty saved fallback = %T, want ThreadReadResponse", value)
	}
	items := flattenTestItems(response.Thread.Turns)
	if len(items) == 0 || len(items) > appwire.TranscriptItemPageLimit {
		t.Fatalf("live-empty saved fallback items = %d, want 1..%d", len(items), appwire.TranscriptItemPageLimit)
	}
	if response.OlderCursor == "" {
		t.Fatal("live-empty saved fallback omitted older cursor")
	}
	if live.candidateReadCalls != 0 {
		t.Fatalf("live-empty saved fallback candidate reads = %d, want response/past-derived path", live.candidateReadCalls)
	}
}

type localEmptyItemRPCSource struct {
	relayLifecycleSource
	threadID           string
	candidateReadCalls int
}

func (*localEmptyItemRPCSource) ID() string { return "local" }

func (*localEmptyItemRPCSource) RelayOnThreadRead() bool { return false }

func (s *localEmptyItemRPCSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{Thread: appwire.Thread{
		ID: s.threadID, SessionID: s.threadID, Source: "local", Evener: appwire.EvenerThread{Ref: "local:" + s.threadID},
	}, PageUnit: appwire.TranscriptPageUnitItem}, nil
}

func (s *localEmptyItemRPCSource) ReadItemCandidates(context.Context, appwire.ThreadReadParams) (appsource.ItemCandidateResult, error) {
	s.candidateReadCalls++
	return appsource.ItemCandidateResult{Exhausted: true}, nil
}

func (*localEmptyItemRPCSource) ListItemCandidates(context.Context, appwire.ThreadTurnsListParams) (appsource.ItemCandidateResult, error) {
	return appsource.ItemCandidateResult{Exhausted: true}, nil
}

type rpcAtomicPagingFixture struct {
	appsource.Source
	candidateSource appsource.ItemCandidateSource
	id              string
	candidates      []appitempaging.TranscriptItemCandidate
	expectedIDs     []string
	paths           *[]string
	replace         func()
}

func newRPCAtomicPagingFixture(t *testing.T, sourceKind, ref string) *rpcAtomicPagingFixture {
	t.Helper()
	candidates := atomicPagingCandidates(sourceKind)
	paths := []string{}
	var source appsource.Source
	var candidateSource appsource.ItemCandidateSource
	var replace func()
	switch sourceKind {
	case "live-daemon":
		source, candidateSource, replace = newRealLocalPagingSource(t, sourceKind, ref, candidates, appwire.ThreadStatusActive, &paths)
	case "ended-local":
		source, candidateSource, replace = newRealLocalPagingSource(t, sourceKind, ref, candidates, appwire.ThreadStatusClosed, &paths)
	case "codex":
		source, candidateSource, replace = newRealCodexPagingSource(t, candidates, &paths)
	default:
		t.Fatalf("unknown source kind %q", sourceKind)
	}
	fixture := &rpcAtomicPagingFixture{
		Source: source, candidateSource: candidateSource, id: pagingSourceID(sourceKind),
		candidates: candidates, expectedIDs: independentAtomicProjection(), paths: &paths, replace: replace,
	}
	return fixture
}

func (s *rpcAtomicPagingFixture) ID() string { return s.id }

func (s *rpcAtomicPagingFixture) replaceIncarnation() {
	s.replace()
}

func (s *rpcAtomicPagingFixture) ReadItemCandidates(ctx context.Context, params appwire.ThreadReadParams) (appsource.ItemCandidateResult, error) {
	return s.candidateSource.ReadItemCandidates(ctx, params)
}

func (s *rpcAtomicPagingFixture) ListItemCandidates(ctx context.Context, params appwire.ThreadTurnsListParams) (appsource.ItemCandidateResult, error) {
	return s.candidateSource.ListItemCandidates(ctx, params)
}

func (s *rpcAtomicPagingFixture) ItemCandidatesFromRead(ctx context.Context, params appwire.ThreadReadParams, response appwire.ThreadReadResponse) (appsource.ItemCandidateResult, error) {
	responseSource, ok := s.Source.(appsource.ItemReadCandidateSource)
	if !ok {
		return appsource.ItemCandidateResult{}, fmt.Errorf("production source %T has no response candidate seam", s.Source)
	}
	return responseSource.ItemCandidatesFromRead(ctx, params, response)
}

func pagingSourceID(sourceKind string) string {
	if sourceKind == "codex" {
		return "codex"
	}
	return "local"
}

func newRealLocalPagingSource(
	t *testing.T,
	sourceKind, ref string,
	candidates []appitempaging.TranscriptItemCandidate,
	status string,
	paths *[]string,
) (*appsource.LocalDaemonSource, appsource.ItemCandidateSource, func()) {
	t.Helper()
	server := appserver.NewServer(appserver.ServerConfig{ServerName: sourceKind, SourceID: "local"})
	turn := wirePagingTurn(candidates)
	thread := appwire.Thread{ID: "paging-thread", SessionID: "paging-thread", Source: "local", CWD: "/tmp/paging", Evener: appwire.EvenerThread{Ref: ref}, Status: appwire.ThreadStatus{Type: status}, Turns: []appwire.Turn{turn}}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		*paths = append(*paths, sourceKind+".read")
		return appwire.ThreadReadResponse{Thread: thread}, nil
	})
	nativeCalls := 0
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, _ appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		nativeCalls++
		*paths = append(*paths, sourceKind+".list")
		return appwire.ThreadTurnsListResponse{Data: []appwire.Turn{turn}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(httpServer.Close)
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + httpServer.URL[len("http"):], SourceID: "local",
		ThreadID: "paging-thread", SessionID: "paging-thread", WorkspaceRef: ref,
		InstanceID: "instance-1", HubToken: "paging-token",
	}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry, Status: status}}
	}, httpServer.Client())
	return source, source, func() { entry.InstanceID = "instance-2" }
}

func newRealCodexPagingSource(
	t *testing.T,
	candidates []appitempaging.TranscriptItemCandidate,
	paths *[]string,
) (*appsource.CodexSource, appsource.ItemCandidateSource, func()) {
	t.Helper()
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex", SourceID: "codex", AdapterNativeInitialize: true})
	nativeItems := codexPagingItems(candidates)
	nativeTurn := map[string]any{"id": "turn_shared", "status": "completed", "items": nativeItems}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		*paths = append(*paths, "codex.read")
		return map[string]any{"thread": map[string]any{"id": "paging-thread", "sessionId": "paging-thread", "source": "codex", "status": map[string]any{"type": "completed"}}}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"thread": map[string]any{"id": "paging-thread", "sessionId": "paging-thread", "source": "codex", "status": map[string]any{"type": "completed"}}}, nil
	})
	nativeCalls := 0
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		nativeCalls++
		*paths = append(*paths, "codex.list")
		return map[string]any{"data": []map[string]any{nativeTurn}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(httpServer.Close)
	source := appsource.NewCodexSource(appsource.CodexSourceConfig{ID: "codex", Endpoint: "ws" + httpServer.URL[len("http"):]}, httpServer.Client())
	return source, source, func() { nativeTurn["id"] = "turn_changed" }
}

func wirePagingTurn(candidates []appitempaging.TranscriptItemCandidate) appwire.Turn {
	items := make([]appwire.ThreadItem, len(candidates))
	for i, candidate := range candidates {
		items[i] = candidate.Item
	}
	return appwire.Turn{ID: "turn_shared", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull, Items: items}
}

func codexPagingItems(candidates []appitempaging.TranscriptItemCandidate) []map[string]any {
	items := make([]map[string]any, len(candidates))
	for i, candidate := range candidates {
		item := map[string]any{"type": "agentMessage", "id": candidate.Item.ID, "text": candidate.Item.Text, "status": "completed"}
		if i == 4 {
			item = map[string]any{"type": "commandExecution", "id": candidate.Item.ID, "command": "printf paging", "status": "completed"}
		}
		if i == 5 {
			item = map[string]any{"type": "mcpToolCall", "id": candidate.Item.ID, "tool": "shell", "toolCallId": "tool", "result": "paging complete", "status": "completed"}
		}
		items[i] = item
	}
	return items
}

func (s *rpcAtomicPagingFixture) sourcePathCalls() []string {
	return append([]string(nil), (*s.paths)...)
}

func pagingPathCounts(paths []string) map[string]int {
	counts := make(map[string]int)
	for _, path := range paths {
		counts[path]++
	}
	return counts
}

func assertConcreteSourcePaths(t *testing.T, sourceKind string, paths []string) {
	t.Helper()
	if len(paths) < 2 || paths[0] != sourceKind+".read" {
		t.Fatalf("source endpoint path calls = %v, want concrete %s read followed by paging", paths, sourceKind)
	}
	wantList := sourceKind + ".list"
	for _, path := range paths {
		if path != sourceKind+".read" && path != wantList {
			t.Fatalf("source endpoint path calls = %v, contains non-native path %q", paths, path)
		}
		if path == wantList {
			return
		}
	}
	t.Fatalf("source endpoint path calls = %v, want native %s endpoint", paths, wantList)
}
func (s *rpcAtomicPagingFixture) hasConcreteBoundary() bool {
	if len(s.candidates) < 41 {
		return false
	}
	// The hub's item-limit boundary is between candidates 4 and 5: the older
	// page owns the call, while the initial page owns its result. Keep the
	// adjacent position, entry, and turn checks tied to that actual cursor
	// boundary rather than to an unrelated pair later in the fixture.
	left, right := s.candidates[4], s.candidates[5]
	return left.TurnID != "" && left.TurnID == right.TurnID && left.Position.Entry == right.Position.Entry && left.Position.Item+1 == right.Position.Item
}

func atomicPagingCandidates(sourceKind string) []appitempaging.TranscriptItemCandidate {
	turn := appwire.Turn{ID: "turn_shared", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull}
	candidates := make([]appitempaging.TranscriptItemCandidate, 45)
	for i := range candidates {
		position := appwire.ThreadItemPosition{Entry: 7, Item: uint32(i)}
		item := appwire.ThreadItem{
			Type: "agentMessage", ID: fmt.Sprintf("item-%02d", i), TranscriptKey: fmt.Sprintf("%s-key-%02d", sourceKind, i),
			Position: &position, TurnID: turn.ID, Text: fmt.Sprintf("message-%02d", i), Status: appwire.TurnStatusCompleted,
		}
		if i == 4 {
			item = appwire.ThreadItem{Type: "commandExecution", ID: "item_tool_call", TranscriptKey: sourceKind + "-tool-call", Position: &position, TurnID: turn.ID, ToolName: "shell", CallID: "tool", ArgumentsJSON: `{"command":"printf paging"}`, Status: appwire.TurnStatusCompleted}
		}
		if i == 5 {
			item = appwire.ThreadItem{Type: "commandExecution", ID: "item_tool_result_tool", TranscriptKey: sourceKind + "-tool-result", Position: &position, TurnID: turn.ID, ToolName: "shell", CallID: "tool", Output: "paging complete", Status: appwire.TurnStatusCompleted}
		}
		candidates[i] = appitempaging.TranscriptItemCandidate{TurnID: turn.ID, Turn: turn, Item: item, Position: position, HasEarlierItems: i > 0, HasLaterItems: i+1 < len(candidates)}
	}
	return candidates
}

func independentAtomicProjection() []string {
	return []string{
		"item-00", "item-01", "item-02", "item-03", "item_tool_call", "item_tool_result_tool",
		"item-06", "item-07", "item-08", "item-09", "item-10", "item-11", "item-12", "item-13", "item-14", "item-15", "item-16", "item-17", "item-18", "item-19", "item-20", "item-21", "item-22", "item-23", "item-24", "item-25", "item-26", "item-27", "item-28", "item-29", "item-30", "item-31", "item-32", "item-33", "item-34", "item-35", "item-36", "item-37", "item-38", "item-39", "item-40", "item-41", "item-42", "item-43", "item-44",
	}
}

func assertPublicItemPage(t *testing.T, turns []appwire.Turn) {
	t.Helper()
	encoded, err := json.Marshal(turns)
	if err != nil {
		t.Fatal(err)
	}
	public := string(encoded)
	for _, forbidden := range []string{"Candidates", "Identity", "Exhausted", "Incarnation", "candidate"} {
		if strings.Contains(public, forbidden) {
			t.Fatalf("public page leaked internal %q: %s", forbidden, public)
		}
	}
	if !strings.Contains(public, `"position"`) || !strings.Contains(public, `"transcriptKey"`) || !strings.Contains(public, `"id"`) || !strings.Contains(public, `"itemsView":"fragment"`) {
		t.Fatalf("public page omitted item position/identity or fragment flags: %s", public)
	}
}

func assertItemPageFitsSoftLimit(t *testing.T, response any) {
	t.Helper()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > transcriptRPCResultSoftLimit {
		t.Fatalf("item page response bytes = %d, want <= %d", len(encoded), transcriptRPCResultSoftLimit)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func isDecimalPagingCursor(cursor string) bool { _, err := strconv.Atoi(cursor); return err == nil }
func itemIDs(items []appwire.ThreadItem) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.ID
	}
	return out
}
func uniquePagingStrings(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		seen[value] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	return out
}
func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
func containsItem(items []appwire.ThreadItem, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
func itemByID(items []appwire.ThreadItem, id string) appwire.ThreadItem {
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	return appwire.ThreadItem{}
}
func containsTurnItem(turns []appwire.Turn, turnID, itemID string) bool {
	for _, turn := range turns {
		if turn.ID == turnID {
			for _, item := range turn.Items {
				if item.ID == itemID {
					return true
				}
			}
		}
	}
	return false
}
func hasCrossBoundaryEntry(candidates []appitempaging.TranscriptItemCandidate, initial []appwire.Turn, all []appwire.ThreadItem) bool {
	if len(candidates) < 6 || len(initial) == 0 || len(all) <= len(initial) {
		return false
	}
	left, right := candidates[4], candidates[5]
	if left.Position.Entry != right.Position.Entry || left.TurnID != right.TurnID || left.Position.Item+1 != right.Position.Item {
		return false
	}
	return containsItem(flattenTestItems(initial), right.Item.ID) && containsItem(all, left.Item.ID)
}
