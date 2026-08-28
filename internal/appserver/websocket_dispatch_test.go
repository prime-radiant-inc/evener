package appserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// dispatchSlowFastServer builds a server whose thread/list handler blocks
// until released, plus an http server speaking AppWire over WebSocket. The
// release channel is closed by test cleanup so a parked handler can never
// outlive the test.
func dispatchSlowFastServer(t *testing.T) (*Server, *httptest.Server, chan struct{}) {
	t.Helper()
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		select {
		case <-handlerStarted:
		default:
			close(handlerStarted)
		}
		<-releaseHandler
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_held"}}}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	t.Cleanup(func() {
		select {
		case <-releaseHandler:
		default:
			close(releaseHandler)
		}
	})
	return server, httpServer, handlerStarted
}

// TestServeWebSocketSlowHandlerDoesNotDelayPing pins the fix itself: with one
// handler parked mid-turn, the browser's app-level ping heartbeat completes
// on the same connection while the slow handler is still running.
func TestServeWebSocketSlowHandlerDoesNotDelayPing(t *testing.T) {
	_, httpServer, handlerStarted := dispatchSlowFastServer(t)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()

	slowDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadList(ctx, appwire.ThreadListParams{})
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
// half of the fix: a fast request issued after a slow one returns its own
// response, correctly paired to its id, without waiting for the slow one.
func TestServeWebSocketFastRequestCompletesWhileSlowHandlerRuns(t *testing.T) {
	server, httpServer, handlerStarted := dispatchSlowFastServer(t)
	fastStarted := make(chan struct{})
	fastRelease := make(chan struct{})
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		close(fastStarted)
		<-fastRelease
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_fast"}}, nil
	})
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()

	slowDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadList(ctx, appwire.ThreadListParams{})
		slowDone <- err
	}()
	waitFor(t, "slow handler to start", handlerStarted)

	fastDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_fast"})
		fastDone <- err
	}()
	waitFor(t, "fast handler to start while the slow handler was busy", fastStarted)

	// Release only the fast handler; the slow one stays parked. If dispatch
	// were still serial, the fast response could never arrive.
	close(fastRelease)
	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatalf("fast request failed while slow handler was busy: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fast request was head-of-line blocked behind a slow handler")
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
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	releaseSlow := make(chan struct{})
	slowResult := appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_slow"}}}
	fastResult := appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_fast"}}
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		<-releaseSlow
		return slowResult, nil
	})
	fastStarted := make(chan struct{})
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		close(fastStarted)
		return fastResult, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()

	slowDone := make(chan appwire.ThreadListResponse, 1)
	slowErr := make(chan error, 1)
	go func() {
		resp, err := client.ThreadList(ctx, appwire.ThreadListParams{})
		slowDone <- resp
		slowErr <- err
	}()
	fastDone := make(chan appwire.ThreadReadResponse, 1)
	fastErr := make(chan error, 1)
	go func() {
		resp, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_fast"})
		fastDone <- resp
		fastErr <- err
	}()

	waitFor(t, "fast handler to start while the slow handler was pending", fastStarted)
	select {
	case err := <-fastErr:
		if err != nil {
			t.Fatalf("fast request failed before the slow one completed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fast response was blocked behind the slow request")
	}
	resp := waitFor(t, "fast response payload", fastDone)
	if resp.Thread.ID != "th_fast" {
		t.Fatalf("fast response paired to wrong result: %+v", resp.Thread)
	}

	close(releaseSlow)
	if err := waitFor(t, "slow request to complete after release", slowErr); err != nil {
		t.Fatalf("slow request failed after release: %v", err)
	}
	slowResp := waitFor(t, "slow response payload", slowDone)
	if len(slowResp.Data) != 1 || slowResp.Data[0].ID != "th_slow" {
		t.Fatalf("slow response paired to wrong result: %+v", slowResp.Data)
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

// TestServeWebSocketInFlightOverflowAnswersUnavailableAndPingStillAnswered
// pins the in-flight limiter: with every slot held by a blocking handler, the
// next request is answered promptly with a retryable Unavailable wire error —
// not parked, not disconnected — and ping still answers on the same
// connection, because the limiter never blocks the receive loop.
func TestServeWebSocketInFlightOverflowAnswersUnavailableAndPingStillAnswered(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(maxConcurrentRequestsPerConnection)
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		started.Done()
		<-release
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_held"}}, nil
	})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_list"}}}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()
	t.Cleanup(func() { close(release) })

	// Park one request per limiter slot; each takes its own dispatch
	// goroutine and its own slot.
	for range maxConcurrentRequestsPerConnection {
		go func() {
			_, _ = client.ThreadRead(ctx, appwire.ThreadReadParams{ThreadID: "th_held"})
		}()
	}
	startedDone := make(chan struct{})
	go func() {
		started.Wait()
		close(startedDone)
	}()
	waitFor(t, "all in-flight slots to be held", startedDone)

	overflowDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadList(ctx, appwire.ThreadListParams{})
		overflowDone <- err
	}()
	select {
	case err := <-overflowDone:
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
			t.Fatalf("overflow request error=%v, want Unavailable wire error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("overflow request was parked instead of answered with Unavailable")
	}

	pingDone := make(chan error, 1)
	go func() {
		var out appwire.EmptyResponse
		pingDone <- client.Request(ctx, appwire.MethodPing, appwire.EmptyParams{}, &out)
	}()
	select {
	case err := <-pingDone:
		if err != nil {
			t.Fatalf("ping failed while all in-flight slots were held: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ping was starved while all in-flight slots were held")
	}
}
