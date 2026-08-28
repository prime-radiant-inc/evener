package appserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

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
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(func() {
		select {
		case <-releaseHandler:
		default:
			close(releaseHandler)
		}
	})
	t.Cleanup(httpServer.Close)
	return server, httpServer, handlerStarted
}

func dispatchDialClient(t *testing.T, httpServer *httptest.Server) *appwire.Client {
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

// TestServeWebSocketSlowHandlerDoesNotDelayPing pins the fix itself: with one
// handler parked mid-turn, the browser's app-level ping heartbeat completes
// on the same connection while the slow handler is still running.
func TestServeWebSocketSlowHandlerDoesNotDelayPing(t *testing.T) {
	_, httpServer, handlerStarted := dispatchSlowFastServer(t)
	client := dispatchDialClient(t, httpServer)
	ctx := context.Background()

	slowDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadList(ctx, appwire.ThreadListParams{})
		slowDone <- err
	}()
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("slow handler did not start")
	}

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
	client := dispatchDialClient(t, httpServer)
	ctx := context.Background()

	slowDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadList(ctx, appwire.ThreadListParams{})
		slowDone <- err
	}()
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("slow handler did not start")
	}

	fastDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_fast"})
		fastDone <- err
	}()
	select {
	case <-fastStarted:
	case <-time.After(time.Second):
		t.Fatal("fast handler did not start while the slow handler was busy")
	}

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
// required"), and once initialize lands the same request succeeds.
func TestServeWebSocketRejectsRequestsBeforeInitialize(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_1"}}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	ctx := context.Background()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close() //nolint:errcheck // test cleanup
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
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	ctx := context.Background()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close() //nolint:errcheck // test cleanup
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

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

	select {
	case <-fastStarted:
	case <-time.After(time.Second):
		t.Fatal("fast handler did not start while the slow handler was pending")
	}
	select {
	case err := <-fastErr:
		if err != nil {
			t.Fatalf("fast request failed before the slow one completed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fast response was blocked behind the slow request")
	}
	select {
	case resp := <-fastDone:
		if resp.Thread.ID != "th_fast" {
			t.Fatalf("fast response paired to wrong result: %+v", resp.Thread)
		}
	case <-time.After(time.Second):
		t.Fatal("fast response payload missing")
	}

	close(releaseSlow)
	select {
	case err := <-slowErr:
		if err != nil {
			t.Fatalf("slow request failed after release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow request did not complete after release")
	}
	select {
	case resp := <-slowDone:
		if len(resp.Data) != 1 || resp.Data[0].ID != "th_slow" {
			t.Fatalf("slow response paired to wrong result: %+v", resp.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("slow response payload missing")
	}
}

// TestServeWebSocketBurstOfConcurrentRequestsAllPairCorrectly drives many
// concurrent requests over one connection and checks every response pairs to
// its own request id — a stress form of the correlation invariant above.
func TestServeWebSocketBurstOfConcurrentRequestsAllPairCorrectly(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		// Echo the raw params through the response so the test can tell each
		// request's own answer apart.
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_1"}}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	ctx := context.Background()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close() //nolint:errcheck // test cleanup
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	const requests = 30
	var wg sync.WaitGroup
	wg.Add(requests)
	for range requests {
		go func() {
			defer wg.Done()
			resp, err := client.ThreadList(ctx, appwire.ThreadListParams{})
			if err != nil {
				t.Errorf("concurrent request failed: %v", err)
				return
			}
			if len(resp.Data) != 1 || resp.Data[0].ID != "th_1" {
				t.Errorf("concurrent response paired wrong: %+v", resp.Data)
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
