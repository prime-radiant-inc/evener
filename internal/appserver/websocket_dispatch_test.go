package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// serveWebSocketHTTP spins up an httptest server speaking AppWire over
// WebSocket for the given server and closes it at test cleanup.
func serveWebSocketHTTP(t *testing.T, server *Server) *httptest.Server {
	t.Helper()
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(httpServer.Close)
	return httpServer
}

// dialAppWireClient dials the ServeWebSocket endpoint and returns an
// initialized client. The transport is closed at test cleanup.
func dialAppWireClient(t *testing.T, httpServer *httptest.Server) *appwire.Client {
	t.Helper()
	ctx := context.Background()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return client
}

// waitFor is the one shared "block until this channel yields or the test
// deadline passes" helper; what names the thing being waited for.
func waitFor[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return *new(T)
	}
}

// dispatchSlowFastServer builds a server whose thread/read handler — a
// slow-read method that dispatches concurrently — blocks until released, plus
// an http server speaking AppWire over WebSocket. The returned release is
// idempotent and also runs at test cleanup, so a parked handler can never
// outlive the test.
func dispatchSlowFastServer(t *testing.T) (*Server, *httptest.Server, chan struct{}, func()) {
	t.Helper()
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		select {
		case <-handlerStarted:
		default:
			close(handlerStarted)
		}
		<-releaseHandler
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_held"}}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	release := sync.OnceFunc(func() { close(releaseHandler) })
	t.Cleanup(release)
	return server, httpServer, handlerStarted, release
}

// TestServeWebSocketSlowHandlerDoesNotDelayPing pins the fix itself: with one
// slow-read handler parked mid-turn, the browser's app-level ping heartbeat
// completes on the same connection while the slow handler is still running.
func TestServeWebSocketSlowHandlerDoesNotDelayPing(t *testing.T) {
	_, httpServer, handlerStarted, _ := dispatchSlowFastServer(t)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()

	slowDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_held"})
		slowDone <- err
	}()
	waitFor(t, "slow handler to start", handlerStarted)

	pingDone := make(chan error, 1)
	go func() {
		var out appwire.EmptyResponse
		pingDone <- client.Request(ctx, appwire.MethodPing, appwire.EmptyParams{}, &out)
	}()
	select {
	case err := <-pingDone:
		if err != nil {
			t.Fatalf("ping failed while a slow handler was busy: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ping was starved by a busy handler")
	}

	select {
	case err := <-slowDone:
		t.Fatalf("slow handler completed before it was released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestServeWebSocketFastRequestCompletesWhileSlowHandlerRuns pins the other
// half of the fix: a request issued after a parked slow read returns its own
// response, correctly paired to its id, without waiting for the slow one —
// the slow read dispatched on its own goroutine, so the receive loop stayed
// free to handle the later request inline.
func TestServeWebSocketFastRequestCompletesWhileSlowHandlerRuns(t *testing.T) {
	server, httpServer, handlerStarted, _ := dispatchSlowFastServer(t)
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_fast"}}}, nil
	})
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()

	slowDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_held"})
		slowDone <- err
	}()
	waitFor(t, "slow handler to start", handlerStarted)

	fastDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadList(ctx, appwire.ThreadListParams{})
		fastDone <- err
	}()
	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatalf("fast request failed while slow handler was busy: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fast request was head-of-line blocked behind a slow read")
	}

	select {
	case err := <-slowDone:
		t.Fatalf("slow handler completed before it was released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestServeWebSocketRejectsRequestsBeforeInitialize pins the initialize
// ordering invariant under concurrent dispatch over a real WebSocket: a
// request that arrives before initialize completes is rejected ("initialize
// required"), and once initialize lands the same request succeeds. The
// handshake cannot ride dialAppWireClient because that helper initializes.
func TestServeWebSocketRejectsRequestsBeforeInitialize(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_1"}}}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)

	ctx := context.Background()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	client := appwire.NewClient(transport)
	client.Start(ctx)

	if _, err := client.ThreadList(ctx, appwire.ThreadListParams{}); err == nil {
		t.Fatal("ThreadList before initialize succeeded")
	}
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadList(ctx, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ThreadList after initialize: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "th_1" {
		t.Fatalf("resp=%+v", resp)
	}
}

// TestServeWebSocketResponsesPairToIDsWhenHandlersCompleteOutOfOrder pins
// response correlation: two concurrent requests whose handlers complete in
// the opposite order they were issued both return their own result, not each
// other's.
func TestServeWebSocketResponsesPairToIDsWhenHandlersCompleteOutOfOrder(t *testing.T) {
	server, httpServer, slowStarted, releaseSlow := dispatchSlowFastServer(t)
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_fast"}}}, nil
	})
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()

	slowDone := make(chan appwire.ThreadReadResponse, 1)
	slowErr := make(chan error, 1)
	go func() {
		resp, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_held"})
		slowDone <- resp
		slowErr <- err
	}()
	waitFor(t, "slow handler to start", slowStarted)
	fastDone := make(chan appwire.ThreadListResponse, 1)
	fastErr := make(chan error, 1)
	go func() {
		resp, err := client.ThreadList(ctx, appwire.ThreadListParams{})
		fastDone <- resp
		fastErr <- err
	}()

	select {
	case err := <-fastErr:
		if err != nil {
			t.Fatalf("fast request failed before the slow one completed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fast response was blocked behind the slow request")
	}
	resp := waitFor(t, "fast response payload", fastDone)
	if len(resp.Data) != 1 || resp.Data[0].ID != "th_fast" {
		t.Fatalf("fast response paired to wrong result: %+v", resp.Data)
	}

	releaseSlow()
	if err := waitFor(t, "slow request to complete after release", slowErr); err != nil {
		t.Fatalf("slow request failed after release: %v", err)
	}
	slowResp := waitFor(t, "slow response payload", slowDone)
	if slowResp.Thread.ID != "th_held" {
		t.Fatalf("slow response paired to wrong result: %+v", slowResp.Thread)
	}
}

// TestServeWebSocketBurstOfConcurrentRequestsAllPairCorrectly drives many
// concurrent requests over one connection and checks every response pairs to
// its own request — a stress form of the correlation invariant above. Each
// request carries a distinct nonce the handler echoes back, so a response
// paired to the wrong request cannot pass.
func TestServeWebSocketBurstOfConcurrentRequestsAllPairCorrectly(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: params.ThreadID}}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()

	const requests = 30
	var wg sync.WaitGroup
	wg.Add(requests)
	for i := range requests {
		go func() {
			defer wg.Done()
			nonce := "th_burst_" + strconv.Itoa(i)
			resp, err := client.ThreadRead(ctx, appwire.ThreadReadParams{ThreadID: nonce})
			if err != nil {
				t.Errorf("concurrent request %d failed: %v", i, err)
				return
			}
			if resp.Thread.ID != nonce {
				t.Errorf("concurrent response %d paired wrong: got %q, want %q", i, resp.Thread.ID, nonce)
			}
		}()
	}
	wgDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(wgDone)
	}()
	select {
	case <-wgDone:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent request burst did not all complete")
	}
}

// TestConcurrentDispatchMethodsAreExactlyTheSlowReads pins the dispatch
// policy's method set against the full appwire catalog: only the known-slow
// read methods leave the receive loop; every mutation — and every other
// read, present or future — stays serial per connection. A method added to
// the catalog is classified here automatically, so the concurrent set cannot
// silently acquire (or lose) a member.
func TestConcurrentDispatchMethodsAreExactlyTheSlowReads(t *testing.T) {
	slowReads := map[string]bool{
		appwire.MethodThreadRead:            true,
		appwire.MethodThreadTurnsList:       true,
		appwire.MethodEvenerSubagentPreview: true,
	}
	for _, spec := range appwire.Methods {
		if got, want := concurrentDispatchMethod(spec.Name), slowReads[spec.Name]; got != want {
			t.Errorf("concurrentDispatchMethod(%s) = %v, want %v", spec.Name, got, want)
		}
	}
	for method := range slowReads {
		if !concurrentDispatchMethod(method) {
			t.Errorf("%s should dispatch concurrently", method)
		}
	}
}

// TestServeWebSocketMutationsDispatchSeriallyPerConnection pins the serial
// half of the dispatch contract: a later request on the same connection does
// not begin until an earlier non-slow-read request completes, so handlers
// outside the slow-read set keep the per-connection ordering they were
// written against.
func TestServeWebSocketMutationsDispatchSeriallyPerConnection(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	HandleTyped(server.Router(), appwire.MethodThreadModelSet, func(_ context.Context, _ appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		close(firstStarted)
		<-releaseFirst
		return appwire.EmptyResponse{}, nil
	})
	secondStarted := make(chan struct{})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		close(secondStarted)
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_1"}}}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()
	release := sync.OnceFunc(func() { close(releaseFirst) })
	t.Cleanup(release)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- client.ThreadModelSet(ctx, appwire.ThreadModelSetParams{Ref: "local:th_1", ModelProvider: "p", Model: "m"})
	}()
	waitFor(t, "first request to start", firstStarted)

	secondDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadList(ctx, appwire.ThreadListParams{})
		secondDone <- err
	}()
	select {
	case <-secondStarted:
		t.Fatal("a later request began while an earlier one was still in flight on the same connection")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	if err := waitFor(t, "first request to complete", firstDone); err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	waitFor(t, "second handler to start after the first completed", secondStarted)
	if err := waitFor(t, "second request to complete", secondDone); err != nil {
		t.Fatalf("second request failed: %v", err)
	}
}

// hydrationMarkerParams is the notification payload the read-then-mutation
// interleaving test commits: a thread-scoped marker identifying exactly which
// mutation produced the record, so the test can assert every record arrived
// exactly once across the buffered replay set and the live stream.
type hydrationMarkerParams struct {
	ThreadID string `json:"threadId"`
	Marker   string `json:"marker"`
}

// TestServeWebSocketHydrationThenMutationDeliversEveryNotificationExactlyOnce
// pins the record accounting when one connection issues a hydrating read
// immediately followed by mutations: the read dispatches on its own
// goroutine, so the mutation can run (and commit notifications) while the
// hydration read is still in flight, and only the sequence-cut discipline
// — not dispatch order — decides which
// set each record lands in. Whatever the interleaving, every notification
// must reach the client exactly once, in whichever set (the hydration's
// buffered replay or the live stream), and none may be lost or duplicated.
func TestServeWebSocketHydrationThenMutationDeliversEveryNotificationExactlyOnce(t *testing.T) {
	const threadID = "th_hydration"
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	notifier := NewNotifier(64)

	// releaseHydrationRead parks the hydrating read AFTER its buffering
	// subscription is open (the capture has returned, locks released), which is
	// exactly the in-flight window a follow-up request can now dispatch into:
	// before concurrent dispatch it would have queued behind the read.
	hydrationReadParked := make(chan struct{})
	releaseHydrationRead := make(chan struct{})
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		var response appwire.ThreadReadResponse
		response.Thread = appwire.Thread{ID: params.ThreadID}
		if !CaptureSubscription(
			ctx,
			false,
			func() string { return threadID },
			notifier.CurrentSequence,
			func() bool { return true },
		) {
			t.Error("hydration capture was rejected")
		}
		// Park with the buffering subscription open: the cut is already taken,
		// so records committed from here on land in the buffered replay set.
		close(hydrationReadParked)
		<-releaseHydrationRead
		return response, nil
	})
	// The mutation commits its notification through CommitProjection from the
	// handler itself — the same path hub handlers use — so the commit runs
	// while the hydration read is still parked in flight. Each mutation names
	// itself through the Model field so its record is identifiable on arrival.
	HandleTyped(server.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		server.CommitProjection(func() []SequencedNotification {
			record := notifier.Record(threadID, appwire.NotifyThreadStatusChanged, hydrationMarkerParams{
				ThreadID: threadID,
				Marker:   params.Model,
			})
			return []SequencedNotification{record}
		})
		return appwire.EmptyResponse{}, nil
	})

	httpServer := serveWebSocketHTTP(t, server)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()
	t.Cleanup(func() {
		select {
		case <-releaseHydrationRead:
		default:
			close(releaseHydrationRead)
		}
	})

	// One connection issues the hydrating read, then — while that read is
	// still in flight — two mutations. Both commits land above the open
	// capture's cut, so both records must be buffered into the replay set.
	hydrateDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(ctx, appwire.ThreadReadParams{ThreadID: threadID})
		hydrateDone <- err
	}()
	waitFor(t, "hydrating read to park in flight", hydrationReadParked)

	runMutation := func(marker string) chan error {
		done := make(chan error, 1)
		go func() {
			done <- client.ThreadModelSet(ctx, appwire.ThreadModelSetParams{
				Ref:           "local:" + threadID,
				ModelProvider: "test-provider",
				Model:         marker,
			})
		}()
		return done
	}
	for i := range 2 {
		if err := waitFor(t, "in-flight mutation to complete", runMutation("inflight-"+strconv.Itoa(i))); err != nil {
			t.Fatalf("mutation during hydration failed: %v", err)
		}
	}

	// Let the hydrating read finish. Its response enqueues, the capture
	// commits, and the buffered records flush behind it — after this the
	// subscription is live again.
	close(releaseHydrationRead)
	if err := waitFor(t, "hydrating read to complete", hydrateDone); err != nil {
		t.Fatalf("hydrating read failed: %v", err)
	}

	// Two more mutations on the same connection, now against the live
	// subscription: their records must stream directly. Together with the
	// buffered pair above, both delivery sets are exercised.
	for i := range 2 {
		if err := waitFor(t, "live mutation to complete", runMutation("live-"+strconv.Itoa(i))); err != nil {
			t.Fatalf("live mutation failed: %v", err)
		}
	}

	// Every notification must arrive exactly once, in whichever set. The
	// buffered pair rides the replay flush; the live pair streams; a record
	// crossing both sets (or neither) fails by count or duplicate marker.
	seen := make(map[string]bool)
	deadline := time.Now().Add(5 * time.Second)
	for len(seen) < 4 && time.Now().Before(deadline) {
		select {
		case notif := <-client.Notifications():
			if notif.Method != appwire.NotifyThreadStatusChanged {
				continue
			}
			var marker hydrationMarkerParams
			if err := json.Unmarshal(notif.Params, &marker); err != nil {
				t.Fatalf("decode notification params: %v", err)
			}
			if marker.ThreadID != threadID {
				continue
			}
			if seen[marker.Marker] {
				t.Fatalf("notification marker %q delivered twice", marker.Marker)
			}
			seen[marker.Marker] = true
		case <-time.After(50 * time.Millisecond):
		}
	}
	if len(seen) != 4 {
		t.Fatalf("distinct notifications received=%d of 4 (markers %v): records lost", len(seen), seenMarkers(seen))
	}
}

func TestServeWebSocketReadAdmissionCanceledByFollowingUnsubscribe(t *testing.T) {
	const threadID = "th_admission"
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local", SubscriptionAdmissionResolver: func(msg appwire.Message) (string, bool) {
		if msg.Request != nil && msg.Request.Method == appwire.MethodThreadRead {
			return threadID, true
		}
		return "", false
	}})
	notifier := NewNotifier(16)
	entered := make(chan struct{})
	release := make(chan struct{})
	unsubStarted := make(chan struct{})
	var once sync.Once
	server.beforeSubscriptionRegistration = func() {
		once.Do(func() { close(entered) })
		<-release
	}
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if !Subscribe(ctx, "th_unrelated") {
			return appwire.ThreadReadResponse{}, nil
		}
		if !CaptureSubscription(ctx, false, func() string { return params.ThreadID }, notifier.CurrentSequence, func() bool { return true }) {
			return appwire.ThreadReadResponse{}, nil
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: params.ThreadID}}, nil
	})
	HandleTyped(server.Router(), appwire.MethodThreadUnsubscribe, func(ctx context.Context, params appwire.ThreadUnsubscribeParams) (appwire.EmptyResponse, error) {
		close(unsubStarted)
		Unsubscribe(ctx, params.ThreadID)
		return appwire.EmptyResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()
	readDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(ctx, appwire.ThreadReadParams{ThreadID: threadID, Subscribe: true})
		readDone <- err
	}()
	waitFor(t, "read admission barrier", entered)
	unsubDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{ThreadID: threadID})
		unsubDone <- err
	}()
	if err := waitFor(t, "following unsubscribe response", unsubDone); err != nil {
		t.Fatalf("unsubscribe failed: %v", err)
	}
	close(release)
	if err := waitFor(t, "older read response", readDone); err != nil {
		t.Fatalf("read failed: %v", err)
	}
	server.subs.mu.RLock()
	if len(server.subs.byThread[threadID]) != 0 {
		server.subs.mu.RUnlock()
		t.Fatal("canceled older read registered a subscription")
	}
	if len(server.subs.byThread["th_unrelated"]) != 1 {
		server.subs.mu.RUnlock()
		t.Fatal("unrelated subscription was removed")
	}
	server.subs.mu.RUnlock()

	server.beforeSubscriptionRegistration = nil
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{ThreadID: threadID, Subscribe: true}); err != nil {
		t.Fatalf("later read failed: %v", err)
	}
	server.subs.mu.RLock()
	defer server.subs.mu.RUnlock()
	if len(server.subs.byThread[threadID]) != 1 {
		t.Fatalf("later read subscription count = %d, want 1", len(server.subs.byThread[threadID]))
	}
}

func TestServeWebSocketUnsubscribeWaitsThroughSubscriptionRegistration(t *testing.T) {
	const threadID = "th_registration_barrier"
	entered := make(chan struct{})
	release := make(chan struct{})
	unsubStarted := make(chan struct{})
	server := NewServer(ServerConfig{
		ServerName: "test-server", SourceID: "local",
		SubscriptionAdmissionResolver: func(msg appwire.Message) (string, bool) {
			if msg.Request != nil && msg.Request.Method == appwire.MethodThreadRead {
				return threadID, true
			}
			return "", false
		},
	})
	server.beforeSubscriptionBeginBuffered = func() {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	}
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if !CaptureSubscription(ctx, false, func() string { return params.ThreadID }, func() uint64 { return 0 }, func() bool { return true }) {
			return appwire.ThreadReadResponse{}, nil
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: params.ThreadID}}, nil
	})
	HandleTyped(server.Router(), appwire.MethodThreadUnsubscribe, func(ctx context.Context, params appwire.ThreadUnsubscribeParams) (appwire.EmptyResponse, error) {
		close(unsubStarted)
		Unsubscribe(ctx, params.ThreadID)
		return appwire.EmptyResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	client := dialAppWireClient(t, httpServer)
	readDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{ThreadID: threadID, Subscribe: true})
		readDone <- err
	}()
	waitFor(t, "claim/register barrier", entered)
	unsubDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{ThreadID: threadID})
		unsubDone <- err
	}()
	waitFor(t, "unsubscribe handler", unsubStarted)
	select {
	case err := <-unsubDone:
		t.Fatalf("unsubscribe completed before registration release: %v", err)
	default:
	}
	close(release)
	if err := waitFor(t, "unsubscribe after registration", unsubDone); err != nil {
		t.Fatalf("unsubscribe failed: %v", err)
	}
	if err := waitFor(t, "read after registration", readDone); err != nil {
		t.Fatalf("read failed: %v", err)
	}
	server.subs.mu.RLock()
	defer server.subs.mu.RUnlock()
	if len(server.subs.byThread[threadID]) != 0 {
		t.Fatal("unsubscribe left a stale registered subscription")
	}
}

// seenMarkers sorts the delivered markers for stable failure messages.
func seenMarkers(seen map[string]bool) []string {
	markers := make([]string, 0, len(seen))
	for marker := range seen {
		markers = append(markers, marker)
	}
	sort.Strings(markers)
	return markers
}

// requireInternalError asserts one request error is the panic barrier's
// InternalError wire error.
func requireInternalError(t *testing.T, what string, err error) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInternalError {
		t.Fatalf("%s error = %v, want InternalError wire error", what, err)
	}
}

// TestServeWebSocketPanickingHandlerAnswersInternalErrorAndConnectionSurvives
// pins the panic barrier on both dispatch paths: a panic in an
// inline-dispatched handler (thread/list) and in a concurrently dispatched
// slow read (thread/read) each answer an InternalError response, are logged
// with a stack, and leave the connection — and the process — alive for
// subsequent requests.
func TestServeWebSocketPanickingHandlerAnswersInternalErrorAndConnectionSurvives(t *testing.T) {
	var logMu sync.Mutex
	var logBuf strings.Builder
	server := NewServer(ServerConfig{
		ServerName: "test-server", Version: "test", SourceID: "local",
		Logf: func(format string, args ...any) {
			logMu.Lock()
			fmt.Fprintf(&logBuf, format+"\n", args...)
			logMu.Unlock()
		},
	})
	logged := func() string {
		logMu.Lock()
		defer logMu.Unlock()
		return logBuf.String()
	}
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		panic("inline handler blew up")
	})
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		panic("slow-read handler blew up")
	})
	HandleTyped(server.Router(), appwire.MethodThreadModelSet, func(_ context.Context, _ appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()

	_, err := client.ThreadList(ctx, appwire.ThreadListParams{})
	requireInternalError(t, "inline panicking request", err)

	_, err = client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_1"})
	requireInternalError(t, "concurrent panicking request", err)

	// The same connection keeps serving: a routed request and the app-level
	// ping both still answer.
	if err := client.ThreadModelSet(ctx, appwire.ThreadModelSetParams{Ref: "local:th_1", ModelProvider: "p", Model: "m"}); err != nil {
		t.Fatalf("routed request after panics failed: %v", err)
	}
	var out appwire.EmptyResponse
	if err := client.Request(ctx, appwire.MethodPing, appwire.EmptyParams{}, &out); err != nil {
		t.Fatalf("ping after panics failed: %v", err)
	}

	output := logged()
	for _, want := range []string{
		"panic handling " + appwire.MethodThreadList,
		"inline handler blew up",
		"panic handling " + appwire.MethodThreadRead,
		"slow-read handler blew up",
		"goroutine", // the stack trace made it into the log
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("panic log missing %q in:\n%s", want, output)
		}
	}
}

// TestPanicLogfFallsBackToStandardLoggerWhenNoSinkIsConfigured pins the
// never-silent guarantee: with no ServerConfig.Logf, a panic report goes to
// the standard logger instead of being dropped the way logf drops advisory
// lines.
func TestPanicLogfFallsBackToStandardLoggerWhenNoSinkIsConfigured(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	var buf strings.Builder
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	server.panicLogf("appserver: panic handling %s: %v", appwire.MethodThreadList, "boom")
	if !strings.Contains(buf.String(), "panic handling thread/list: boom") {
		t.Fatalf("standard logger output = %q, want the panic line", buf.String())
	}
}
