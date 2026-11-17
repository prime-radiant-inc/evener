package appsource

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// pagedHistoryDaemon serves one long session the way a real daemon does: a
// thread/read answers the newest page and names the rest through OlderCursor, and
// a thread/turns/list serves one page per call going backward. It counts those
// paging calls, which is the work a hub read performs beyond the one page the
// daemon already returned.
type pagedHistoryDaemon struct {
	items     []appwire.ThreadItem
	readPage  int
	listCalls atomic.Int64
}

func (d *pagedHistoryDaemon) identity() appitempaging.CursorIdentity {
	return appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "native", ProjectionVersion: 1}
}

func (d *pagedHistoryDaemon) handler() http.HandlerFunc {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		start := max(0, len(d.items)-d.readPage)
		older := ""
		if start > 0 {
			cursor, err := appitempaging.EncodeCursor(d.identity(), *d.items[start].Position)
			if err != nil {
				return appwire.ThreadReadResponse{}, err
			}
			older = cursor
		}
		turn := appwire.Turn{ID: "turn-1", Items: d.items[start:], ItemsView: appwire.TurnItemsViewFragment, HasEarlierItems: older != ""}
		return appwire.ThreadReadResponse{
			Thread:      appwire.Thread{ID: "thread", Evener: appwire.EvenerThread{Ref: "local:thread"}, Turns: []appwire.Turn{turn}},
			OlderCursor: older,
		}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		d.listCalls.Add(1)
		before, err := appitempaging.DecodeCursor(params.Cursor, d.identity())
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, appwire.InvalidParams(err.Error())
		}
		selected, older, err := appitempaging.SelectCandidates(pagedHistoryCandidates(d.items), &before, params.ItemLimit)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		next := ""
		if older {
			cursor, err := appitempaging.EncodeCursor(d.identity(), selected[0].Position)
			if err != nil {
				return appwire.ThreadTurnsListResponse{}, err
			}
			next = cursor
		}
		turn := appwire.Turn{ID: "turn-1", Items: pagedHistoryItems(selected), ItemsView: appwire.TurnItemsViewFragment, HasEarlierItems: older}
		return appwire.ThreadTurnsListResponse{Data: []appwire.Turn{turn}, NextCursor: next}, nil
	})
	return server.ServeWebSocket
}

func pagedHistoryCandidates(items []appwire.ThreadItem) []appitempaging.TranscriptItemCandidate {
	candidates := make([]appitempaging.TranscriptItemCandidate, 0, len(items))
	for _, item := range items {
		candidates = append(candidates, appitempaging.TranscriptItemCandidate{Item: item, Position: *item.Position, TurnID: item.TurnID})
	}
	return candidates
}

func pagedHistoryItems(candidates []appitempaging.TranscriptItemCandidate) []appwire.ThreadItem {
	items := make([]appwire.ThreadItem, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, candidate.Item)
	}
	return items
}

func newPagedHistorySource(tb testing.TB, itemCount, readPage int) (*LocalDaemonSource, *pagedHistoryDaemon) {
	tb.Helper()
	items := make([]appwire.ThreadItem, 0, itemCount)
	for i := range itemCount {
		position := appwire.ThreadItemPosition{Entry: uint64(i)}
		items = append(items, appwire.ThreadItem{
			ID: fmt.Sprintf("item-%d", i), Type: "agentMessage", TurnID: "turn-1",
			TranscriptKey: fmt.Sprintf("key-%d", i), Position: &position, Version: 1,
			Text: fmt.Sprintf("body %d", i),
		})
	}
	daemon := &pagedHistoryDaemon{items: items, readPage: readPage}
	httpServer := httptest.NewServer(daemon.handler())
	tb.Cleanup(httpServer.Close)
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + httpServer.URL[len("http"):],
		SourceID: "local", ThreadID: "thread", SessionID: "thread", WorkspaceRef: "local:thread",
		InstanceID: "paged-history-instance",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry}}
	}, httpServer.Client())
	return source, daemon
}

// TestBoundedRefreshDoesNotWalkHiddenHistory pins how much paging work one live
// read performs on a session whose transcript grew past the daemon's page limit.
//
// The sequence is the one a long-running session goes through. Its first read
// returns the whole transcript, so the hub records a complete snapshot. The
// transcript then grows, and every later read returns only a bounded page, so the
// prefix retained from the complete snapshot is now hidden. Authenticating that
// hidden prefix by walking it costs one daemon round trip per page — work that
// grows with the session's length and repeats on the read that follows each
// growth. A bounded read must instead rotate the snapshot identity, which makes
// every retained cursor a typed stale error at no paging cost.
func TestBoundedRefreshDoesNotWalkHiddenHistory(t *testing.T) {
	const (
		itemCount = 20_000
		readPage  = 40
		// A read may authenticate a short hidden prefix, because a session that
		// just grew past one page is the common case and the walk is cheap there.
		// Anything approaching a page per session page is the unbounded walk.
		maxPagingRoundTripsPerRead = 8
	)
	source, daemon := newPagedHistorySource(t, itemCount, itemCount)
	params := appwire.ThreadReadParams{Ref: "local:thread", IncludeTurns: true, ItemLimit: 20}
	ctx := context.Background()

	complete, err := source.ReadThread(ctx, params)
	if err != nil {
		t.Fatalf("ReadThread (complete): %v", err)
	}
	if complete.OlderCursor != "" {
		t.Fatalf("fixture: the first read must be complete, got cursor %q", complete.OlderCursor)
	}
	if _, err := source.ItemCandidatesFromRead(ctx, params, complete); err != nil {
		t.Fatalf("ItemCandidatesFromRead (complete): %v", err)
	}

	// The transcript grew past the page limit: later reads are bounded and must
	// authenticate a hidden prefix.
	daemon.readPage = readPage
	for read := 1; read <= 2; read++ {
		bounded, err := source.ReadThread(ctx, params)
		if err != nil {
			t.Fatalf("ReadThread (bounded, read %d): %v", read, err)
		}
		if bounded.OlderCursor == "" {
			t.Fatalf("fixture: bounded read %d must carry an older cursor", read)
		}
		before := daemon.listCalls.Load()
		if _, err := source.ItemCandidatesFromRead(ctx, params, bounded); err != nil {
			t.Fatalf("ItemCandidatesFromRead (bounded, read %d): %v", read, err)
		}
		if walked := daemon.listCalls.Load() - before; walked > maxPagingRoundTripsPerRead {
			t.Fatalf("bounded read %d walked %d hidden-history pages on a %d-item session, want at most %d: the read's paging cost must not grow with the transcript",
				read, walked, itemCount, maxPagingRoundTripsPerRead)
		}
	}
}
