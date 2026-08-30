package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// captureLogfServer builds a server whose Logf appends to a mutex-guarded
// buffer, returning the server and a snapshot accessor.
func captureLogfServer() (*Server, func() string) {
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
	return server, func() string {
		logMu.Lock()
		defer logMu.Unlock()
		return logBuf.String()
	}
}

// dialRawAppWire dials the ServeWebSocket endpoint and returns the bare
// transport, for tests that need deterministic frame-by-frame pipelining
// (the appwire.Client writes concurrently issued requests in whatever order
// its goroutines win).
func dialRawAppWire(t *testing.T, httpServer *httptest.Server) *appwire.WSTransport {
	t.Helper()
	transport, err := appwire.DialWebSocket(context.Background(), "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

// rawRequest builds one request frame with a numeric id.
func rawRequest(t *testing.T, id int64, method string, params any) appwire.Message {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s params: %v", method, err)
	}
	return appwire.Message{Request: &appwire.Request{ID: appwire.NewIntID(id), Method: method, Params: raw}}
}

// sendRaw writes one frame, failing the test on error.
func sendRaw(t *testing.T, transport *appwire.WSTransport, msg appwire.Message) {
	t.Helper()
	if err := transport.Send(context.Background(), msg); err != nil {
		t.Fatalf("send %s: %v", methodOf(msg), err)
	}
}

// initializeRaw completes the initialize handshake on a bare transport using
// request id 1.
func initializeRaw(t *testing.T, transport *appwire.WSTransport) {
	t.Helper()
	sendRaw(t, transport, rawRequest(t, 1, appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	for {
		msg, err := transport.Recv(context.Background())
		if err != nil {
			t.Fatalf("recv initialize response: %v", err)
		}
		if msg.Response != nil && msg.Response.ID.Int64() == 1 {
			return
		}
		if msg.Error != nil {
			t.Fatalf("initialize failed: %+v", msg.Error)
		}
	}
}

// collectFrames drains the transport into a channel until Recv fails, so a
// test can assert on arrival order without blocking in Recv itself.
func collectFrames(transport *appwire.WSTransport) <-chan appwire.Message {
	frames := make(chan appwire.Message, 256)
	go func() {
		defer close(frames)
		for {
			msg, err := transport.Recv(context.Background())
			if err != nil {
				return
			}
			frames <- msg
		}
	}()
	return frames
}

// registeredConnection returns the server's single registered connection,
// waiting for it to appear.
func registeredConnection(t *testing.T, server *Server) *Connection {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.mu.RLock()
		var conn *Connection
		for _, c := range server.conns {
			conn = c
		}
		server.mu.RUnlock()
		if conn != nil {
			return conn
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no connection registered")
	return nil
}

// waitUnregistered waits until the server has no registered connections.
func waitUnregistered(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		server.mu.RLock()
		n := len(server.conns)
		server.mu.RUnlock()
		if n == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("connection was not unregistered")
}

// TestServeWebSocketSlowReadWaitsForEarlierSerialRequest pins the
// serial→slow-read cell of the ordering contract: a slow read queued behind a
// parked serial request does not start until that request completes — the
// worker, not the receive loop, is the dispatch point for the concurrent set.
func TestServeWebSocketSlowReadWaitsForEarlierSerialRequest(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	serialStarted := make(chan struct{})
	releaseSerial := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseSerial) })
	t.Cleanup(release)
	HandleTyped(server.Router(), appwire.MethodThreadModelSet, func(_ context.Context, _ appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		close(serialStarted)
		<-releaseSerial
		return appwire.EmptyResponse{}, nil
	})
	readStarted := make(chan struct{})
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		close(readStarted)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_after_serial"}}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()

	serialDone := make(chan error, 1)
	go func() {
		serialDone <- client.ThreadModelSet(ctx, appwire.ThreadModelSetParams{Ref: "local:th_1", ModelProvider: "p", Model: "m"})
	}()
	waitFor(t, "serial request to start", serialStarted)

	readDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_1"})
		readDone <- err
	}()
	select {
	case <-readStarted:
		t.Fatal("a slow read started while an earlier serial request was still executing")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	if err := waitFor(t, "serial request to complete", serialDone); err != nil {
		t.Fatalf("serial request failed: %v", err)
	}
	waitFor(t, "slow read to start after the serial request completed", readStarted)
	if err := waitFor(t, "slow read to complete", readDone); err != nil {
		t.Fatalf("slow read failed: %v", err)
	}
}

// TestServeWebSocketTwoSlowReadsRunConcurrently pins the
// slow-read→slow-read cell: two pipelined slow reads are in flight
// simultaneously — the worker spawns each and moves on rather than waiting.
func TestServeWebSocketTwoSlowReadsRunConcurrently(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	started := make(chan struct{}, 2)
	releaseReads := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseReads) })
	t.Cleanup(release)
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		started <- struct{}{}
		<-releaseReads
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: params.ThreadID}}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()

	done := make(chan error, 2)
	for i := range 2 {
		go func() {
			_, err := client.ThreadRead(ctx, appwire.ThreadReadParams{ThreadID: "th_" + string(rune('a'+i))})
			done <- err
		}()
	}
	waitFor(t, "first slow read to start", started)
	waitFor(t, "second slow read to start while the first is parked", started)

	release()
	for range 2 {
		if err := waitFor(t, "slow read to complete", done); err != nil {
			t.Fatalf("slow read failed: %v", err)
		}
	}
}

// TestServeWebSocketWorkerSurvivesPanicAndExecutesQueuedRequests extends the
// carried panic-barrier test to the worker: a request queued behind a
// panicking one still executes and answers, so the panic was contained by
// handleAndEnqueue without killing the worker goroutine — the worker
// survived, not just the connection.
func TestServeWebSocketWorkerSurvivesPanicAndExecutesQueuedRequests(t *testing.T) {
	server, logged := captureLogfServer()
	panicStarted := make(chan struct{})
	releasePanic := make(chan struct{})
	release := sync.OnceFunc(func() { close(releasePanic) })
	t.Cleanup(release)
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		close(panicStarted)
		<-releasePanic
		panic("queued-behind panic")
	})
	HandleTyped(server.Router(), appwire.MethodThreadModelSet, func(_ context.Context, _ appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)

	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitFor(t, "panicking handler to start", panicStarted)
	sendRaw(t, transport, rawRequest(t, 3, appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{Ref: "local:th_1", ModelProvider: "p", Model: "m"}))
	sendRaw(t, transport, rawRequest(t, 4, appwire.MethodPing, appwire.EmptyParams{}))
	frames := collectFrames(transport)

	// Ping bypasses the queue, so it answers while the panicking handler is
	// still parked and the model/set request waits behind it.
	first := waitFor(t, "ping response while the panicking handler is parked", frames)
	if first.Response == nil || first.Response.ID.Int64() != 4 {
		t.Fatalf("first frame = %+v, want ping response id 4", first)
	}

	release()
	second := waitFor(t, "InternalError for the panicking request", frames)
	if second.Error == nil || second.Error.ID.Int64() != 2 {
		t.Fatalf("second frame = %+v, want error response id 2", second)
	}
	if second.Error.Error.Code != appwire.CodeInternalError {
		t.Fatalf("panicking request error code = %d, want InternalError", second.Error.Error.Code)
	}
	third := waitFor(t, "queued request behind the panic to answer", frames)
	if third.Response == nil || third.Response.ID.Int64() != 3 {
		t.Fatalf("third frame = %+v, want response id 3", third)
	}
	if !strings.Contains(logged(), "panic handling "+appwire.MethodThreadList) {
		t.Fatalf("panic was not logged:\n%s", logged())
	}
}

// TestServeWebSocketQueueFullAppliesBackpressureWithoutWireError pins queue
// saturation as flow control: with a small injected capacity and a parked
// serial handler, pipelining past capacity blocks the receive loop — no wire
// error, no eviction — and once the handler releases, every response arrives,
// in request order.
func TestServeWebSocketQueueFullAppliesBackpressureWithoutWireError(t *testing.T) {
	server, logged := captureLogfServer()
	server.requestQueueCapacity = 2
	blocked := make(chan struct{}, 4)
	server.blockedEnqueue = func() { blocked <- struct{}{} }

	var mu sync.Mutex
	var executed []int64
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseFirst) })
	t.Cleanup(release)
	HandleTyped(server.Router(), appwire.MethodEvenerThreadNameSet, func(ctx context.Context, params appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
		mu.Lock()
		executed = append(executed, requestOrdinal(params.Name))
		first := len(executed) == 1
		mu.Unlock()
		if first {
			close(firstStarted)
			<-releaseFirst
		}
		return appwire.EmptyResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)

	const requests = 5
	for i := int64(2); i < 2+requests; i++ {
		sendRaw(t, transport, rawRequest(t, i, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: fmt.Sprintf("req-%d", i)}))
	}
	waitFor(t, "first serial handler to start", firstStarted)
	waitFor(t, "receive loop to park on the full queue", blocked)

	frames := collectFrames(transport)
	select {
	case msg := <-frames:
		t.Fatalf("frame arrived while the queue was saturated and the handler parked: %+v", msg)
	case <-time.After(100 * time.Millisecond):
	}
	if logs := logged(); strings.Contains(logs, "evicting") {
		t.Fatalf("saturation caused an eviction:\n%s", logs)
	}

	release()
	for i := int64(2); i < 2+requests; i++ {
		msg := waitFor(t, fmt.Sprintf("response %d", i), frames)
		if msg.Response == nil || msg.Response.ID.Int64() != i {
			t.Fatalf("response out of order: got %+v, want response id %d", msg, i)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for idx, ordinal := range executed {
		if ordinal != int64(idx+2) {
			t.Fatalf("execution order = %v, want ascending request order", executed)
		}
	}
}

// requestOrdinal parses the trailing integer out of a "req-N" marker.
func requestOrdinal(name string) int64 {
	var n int64
	_, _ = fmt.Sscanf(name, "req-%d", &n)
	return n
}

// TestServeWebSocketTeardownUnderQueueSaturationCompletesAfterHandlerRelease
// pins the teardown path out of saturation: a close that arrives while the
// receive loop is parked on the full queue is observed only when a queue slot
// frees and the loop re-enters Recv — the same window PR #667's inline
// handlers had — after which the connection tears down cleanly and the worker
// exits.
func TestServeWebSocketTeardownUnderQueueSaturationCompletesAfterHandlerRelease(t *testing.T) {
	server, _ := captureLogfServer()
	server.requestQueueCapacity = 2
	blocked := make(chan struct{}, 4)
	server.blockedEnqueue = func() { blocked <- struct{}{} }

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseFirst) })
	t.Cleanup(release)
	var calls atomic.Int64
	HandleTyped(server.Router(), appwire.MethodEvenerThreadNameSet, func(_ context.Context, _ appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		return appwire.EmptyResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)

	for i := int64(2); i < 7; i++ {
		sendRaw(t, transport, rawRequest(t, i, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: "n"}))
	}
	waitFor(t, "first serial handler to start", firstStarted)
	waitFor(t, "receive loop to park on the full queue", blocked)

	// Close from a goroutine: the WebSocket close handshake cannot complete
	// until the server observes the close frame, which — the contract under
	// test — happens only when the parked handler finishes and a queue slot
	// frees, so a synchronous Close would stall until its own timeout.
	go func() { _ = transport.Close() }()
	// The release is part of the contract under test: the close is observed
	// only when the parked handler finishes and a queue slot frees.
	release()
	waitFor(t, "worker to exit after teardown", conn.workerExited)
	waitUnregistered(t, server)
}

// TestServeWebSocketCloseAbandonsQueuedRequestsWithPurgeAdvisory pins
// abandonment: requests queued behind a parked handler when the client
// disconnects never execute; the teardown purge empties the queue while the
// parked handler still runs, and reports one bounded advisory line — catalog
// methods tallied by name, unknown methods aggregated, no params content and
// no raw uncataloged method strings in the log.
func TestServeWebSocketCloseAbandonsQueuedRequestsWithPurgeAdvisory(t *testing.T) {
	server, logged := captureLogfServer()
	parkStarted := make(chan struct{})
	releasePark := make(chan struct{})
	release := sync.OnceFunc(func() { close(releasePark) })
	t.Cleanup(release)
	HandleTyped(server.Router(), appwire.MethodEvenerThreadNameSet, func(_ context.Context, _ appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
		close(parkStarted)
		<-releasePark
		return appwire.EmptyResponse{}, nil
	})
	var listCalls atomic.Int64
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		listCalls.Add(1)
		return appwire.ThreadListResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)

	const sentinel = "SENTINEL-user-content-9d2f"
	garbageMethod := strings.Repeat("garbage-method-", 300)
	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: "parked"}))
	waitFor(t, "parked handler to start", parkStarted)
	sendRaw(t, transport, rawRequest(t, 3, appwire.MethodThreadList, appwire.ThreadListParams{}))
	sendRaw(t, transport, rawRequest(t, 4, appwire.MethodThreadList, appwire.ThreadListParams{}))
	sendRaw(t, transport, rawRequest(t, 5, garbageMethod, map[string]string{"payload": sentinel}))
	sendRaw(t, transport, rawRequest(t, 6, garbageMethod, map[string]string{"payload": sentinel}))

	// The queued frames must be in the queue — not the kernel — before the
	// close, or the purge has nothing to discard.
	waitForQueueDepth(t, conn, 4)
	if err := transport.Close(); err != nil {
		t.Fatalf("close transport: %v", err)
	}

	// The purge must empty the queue while the parked handler still pins the
	// worker inside executeOrdered.
	waitForQueueDepth(t, conn, 0)
	select {
	case <-conn.workerExited:
		t.Fatal("worker exited while its handler was still parked")
	default:
	}

	// The queue empties before the advisory line is written; wait for the
	// line rather than racing it.
	logDeadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logged(), "discarded") && time.Now().Before(logDeadline) {
		time.Sleep(2 * time.Millisecond)
	}
	logs := logged()
	advisories := 0
	var advisory string
	for line := range strings.SplitSeq(logs, "\n") {
		if strings.Contains(line, "discarded") {
			advisories++
			advisory = line
		}
	}
	if advisories != 1 {
		t.Fatalf("purge advisories = %d, want exactly 1 in:\n%s", advisories, logs)
	}
	if !strings.Contains(advisory, appwire.MethodThreadList) {
		t.Fatalf("purge advisory does not tally %s by name: %s", appwire.MethodThreadList, advisory)
	}
	if !strings.Contains(advisory, "unknown") {
		t.Fatalf("purge advisory does not aggregate uncataloged methods as unknown: %s", advisory)
	}
	if strings.Contains(logs, sentinel) {
		t.Fatalf("purge advisory leaked params content:\n%s", logs)
	}
	if strings.Contains(logs, "garbage-method-") {
		t.Fatalf("purge advisory leaked a raw uncataloged method string:\n%s", logs)
	}

	release()
	waitFor(t, "worker to exit after the parked handler returned", conn.workerExited)
	if got := listCalls.Load(); got != 0 {
		t.Fatalf("abandoned queued requests executed %d times, want 0", got)
	}
}

// waitForQueueDepth polls the connection's request queue length.
func waitForQueueDepth(t *testing.T, conn *Connection, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(conn.requests) == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("request queue depth = %d, want %d", len(conn.requests), want)
}

// TestServeWebSocketWorkerObservesCancellationAtDequeue pins the post-dequeue
// re-check deterministically: with the worker parked in the after-dequeue
// hook holding a dequeued request, cancellation lands, the hook releases, and
// the handler never starts — no request begins executing after the worker has
// observed cancellation.
func TestServeWebSocketWorkerObservesCancellationAtDequeue(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	hookEntered := make(chan struct{})
	hookRelease := make(chan struct{})
	server.afterWorkerDequeue = func(msg appwire.Message) {
		if msg.Request == nil || msg.Request.Method != appwire.MethodThreadList {
			return
		}
		close(hookEntered)
		<-hookRelease
	}
	var listCalls atomic.Int64
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		listCalls.Add(1)
		return appwire.ThreadListResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)

	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitFor(t, "worker to park in the after-dequeue hook", hookEntered)

	if err := transport.Close(); err != nil {
		t.Fatalf("close transport: %v", err)
	}
	waitUnregistered(t, server)
	close(hookRelease)

	waitFor(t, "worker to exit at the post-dequeue re-check", conn.workerExited)
	if got := listCalls.Load(); got != 0 {
		t.Fatalf("handler ran %d times after cancellation was observable at the dequeue, want 0", got)
	}
}

// TestServeWebSocketQueueSaturationAdvisoryFiresOncePerConnection pins the
// advisory's shape: repeated saturation on one connection produces exactly
// one log line — an implementation can neither flood the log under sustained
// backpressure nor skip the advisory.
func TestServeWebSocketQueueSaturationAdvisoryFiresOncePerConnection(t *testing.T) {
	server, logged := captureLogfServer()
	server.requestQueueCapacity = 1
	blocked := make(chan struct{}, 8)
	server.blockedEnqueue = func() { blocked <- struct{}{} }

	releases := make(chan struct{}, 8)
	HandleTyped(server.Router(), appwire.MethodEvenerThreadNameSet, func(_ context.Context, _ appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
		<-releases
		return appwire.EmptyResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)
	frames := collectFrames(transport)

	// First saturation: one executing, one queued, one blocking the loop.
	for i := int64(2); i < 5; i++ {
		sendRaw(t, transport, rawRequest(t, i, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: "n"}))
	}
	waitFor(t, "receive loop to park on the full queue", blocked)
	releases <- struct{}{}

	// Second saturation on the same connection.
	sendRaw(t, transport, rawRequest(t, 5, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: "n"}))
	waitFor(t, "receive loop to park on the refilled queue", blocked)
	for range 3 {
		releases <- struct{}{}
	}

	for i := int64(2); i < 6; i++ {
		msg := waitFor(t, fmt.Sprintf("response %d", i), frames)
		if msg.Response == nil || msg.Response.ID.Int64() != i {
			t.Fatalf("response out of order: got %+v, want id %d", msg, i)
		}
	}

	lines := 0
	for line := range strings.SplitSeq(logged(), "\n") {
		if strings.Contains(line, conn.id) {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("saturation advisory lines for %s = %d, want exactly 1 in:\n%s", conn.id, lines, logged())
	}
}

// TestServeWebSocketOrphanedHandlerFencesRefuseAfterTeardown pins the seam
// fences from the handler's side: a context-ignoring serial handler that
// resumes after teardown completed finds every connection-owned entry point
// refusing it — subscription and capture rejected on the unregistered
// connection, notifications dropped on the closed send channel — and nothing
// leaks into Subscriptions.
func TestServeWebSocketOrphanedHandlerFencesRefuseAfterTeardown(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	started := make(chan struct{})
	releasePark := make(chan struct{})
	release := sync.OnceFunc(func() { close(releasePark) })
	t.Cleanup(release)
	type fenceResults struct {
		subscribe bool
		capture   bool
	}
	results := make(chan fenceResults, 1)
	HandleTyped(server.Router(), appwire.MethodEvenerThreadNameSet, func(ctx context.Context, _ appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
		close(started)
		<-releasePark // deliberately ignores ctx: the orphaned-handler shape
		var r fenceResults
		r.subscribe = Subscribe(ctx, "th_orphan")
		Unsubscribe(ctx, "th_orphan")
		r.capture = CaptureSubscription(ctx, false,
			func() string { return "th_orphan" },
			func() uint64 { return 0 },
			func() bool { return true },
		)
		Notify(ctx, appwire.NotifyThreadStatusChanged, struct{}{})
		results <- r
		return appwire.EmptyResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)

	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: "orphan"}))
	waitFor(t, "context-ignoring handler to start", started)

	if err := transport.Close(); err != nil {
		t.Fatalf("close transport: %v", err)
	}
	waitUnregistered(t, server)

	release()
	r := waitFor(t, "orphaned handler to run its fence probes", results)
	if r.subscribe {
		t.Fatal("Subscribe succeeded on an unregistered connection")
	}
	if r.capture {
		t.Fatal("CaptureSubscription succeeded on an unregistered connection")
	}
	if got := server.SubscriberCount("th_orphan"); got != 0 {
		t.Fatalf("subscriptions leaked after orphaned-handler probes: count=%d", got)
	}
	if conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
		t.Fatal("notification enqueue succeeded on a closed send channel")
	}
	waitFor(t, "worker to exit after the orphaned handler returned", conn.workerExited)
}

// gatedSendTransport wraps the real transport with a Send that parks while
// gated, so a test can hold the send loop mid-write. Recv passes through.
type gatedSendTransport struct {
	webSocketTransport
	gated   atomic.Bool
	blocked chan struct{}
	release chan struct{}
}

func (t *gatedSendTransport) Send(ctx context.Context, msg appwire.Message) error {
	if !t.gated.Load() {
		return t.webSocketTransport.Send(ctx, msg)
	}
	select {
	case t.blocked <- struct{}{}:
	default:
	}
	select {
	case <-t.release:
		return t.webSocketTransport.Send(ctx, msg)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestServeWebSocketPingParksOnFullOutboundBufferUntilSendDrains pins the
// stated precondition on ping liveness: with the write side parked and the
// outbound channel full, the inline ping response parks in enqueueResponse —
// no response arrives — and once the write side drains, the ping answers.
func TestServeWebSocketPingParksOnFullOutboundBufferUntilSendDrains(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	gate := &gatedSendTransport{blocked: make(chan struct{}, 1), release: make(chan struct{})}
	server.wrapWebSocketTransport = func(inner webSocketTransport) webSocketTransport {
		gate.webSocketTransport = inner
		return gate
	}
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)

	gate.gated.Store(true)
	// Park the send loop on one notification, then fill the outbound buffer.
	if !conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
		t.Fatal("could not enqueue the first fill notification")
	}
	waitFor(t, "send loop to park in the gated Send", gate.blocked)
	for conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
	}

	frames := collectFrames(transport)
	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodPing, appwire.EmptyParams{}))
	select {
	case msg := <-frames:
		t.Fatalf("frame arrived while the outbound buffer was full and the write side parked: %+v", msg)
	case <-time.After(150 * time.Millisecond):
	}

	close(gate.release)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case msg := <-frames:
			if msg.Response != nil && msg.Response.ID.Int64() == 2 {
				return
			}
		case <-deadline:
			t.Fatal("ping response did not arrive after the write side drained")
		}
	}
}

// TestServeWebSocketWriteTimeoutTearsDownConnectionWithPingParked pins the
// other half of the precondition: a peer that never drains does not leave the
// receive loop parked in enqueueResponse forever — the send loop's write
// timeout fails the parked write, cancels the connection, and teardown runs.
func TestServeWebSocketWriteTimeoutTearsDownConnectionWithPingParked(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	server.sendWriteTimeout = 150 * time.Millisecond
	gate := &gatedSendTransport{blocked: make(chan struct{}, 1), release: make(chan struct{})}
	server.wrapWebSocketTransport = func(inner webSocketTransport) webSocketTransport {
		gate.webSocketTransport = inner
		return gate
	}
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)

	gate.gated.Store(true)
	if !conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
		t.Fatal("could not enqueue the first fill notification")
	}
	waitFor(t, "send loop to park in the gated Send", gate.blocked)
	for conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
	}
	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodPing, appwire.EmptyParams{}))
	// Keep draining the client side: the receive loop's error-path Close
	// performs a close handshake, and a peer that never reads would stall it
	// for coder/websocket's own close timeout — incidental to the cascade
	// under test.
	_ = collectFrames(transport)

	waitFor(t, "worker to exit after the write-timeout cascade", conn.workerExited)
	waitUnregistered(t, server)
}
