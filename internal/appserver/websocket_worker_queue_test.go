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

// waitUntil polls cond until it holds or the test deadline passes — the
// condition-shaped sibling of waitFor, for state without a channel to block
// on; what names the condition being waited for.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting until %s", what)
}

// registeredConnection returns the server's single registered connection,
// waiting for it to appear.
func registeredConnection(t *testing.T, server *Server) *Connection {
	t.Helper()
	var conn *Connection
	waitUntil(t, "a connection registers", func() bool {
		server.mu.RLock()
		defer server.mu.RUnlock()
		for _, c := range server.conns {
			conn = c
		}
		return conn != nil
	})
	return conn
}

// waitUnregistered waits until the server has no registered connections.
func waitUnregistered(t *testing.T, server *Server) {
	t.Helper()
	waitUntil(t, "the connection unregisters", func() bool {
		server.mu.RLock()
		defer server.mu.RUnlock()
		return len(server.conns) == 0
	})
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

// TestServeWebSocketQueueFullRefusesExcessAndPreservesAdmittedOrder pins the
// bounded queue: admitted work executes in order, excess work is refused
// before execution, and neither refusal consumes a worker slot.
func TestServeWebSocketQueueFullRefusesExcessAndPreservesAdmittedOrder(t *testing.T) {
	server, logged := captureLogfServer()
	server.requestQueueCapacity = 2

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
	conn := registeredConnection(t, server)
	frames := collectFrames(transport)

	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: "req-2"}))
	waitFor(t, "first serial handler to start", firstStarted)
	for i := int64(3); i <= 6; i++ {
		sendRaw(t, transport, rawRequest(t, i, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: fmt.Sprintf("req-%d", i)}))
	}
	waitForQueueDepth(t, conn, 2)
	for _, id := range []int64{5, 6} {
		requireQueueRefusal(t, "excess request", waitForFrameID(t, frames, id), id, "", appwire.RetryDispositionNone)
	}
	if logs := logged(); strings.Contains(logs, "evicting") {
		t.Fatalf("saturation caused an eviction:\n%s", logs)
	}

	release()
	for i := int64(2); i <= 4; i++ {
		msg := waitFor(t, fmt.Sprintf("response %d", i), frames)
		if msg.Response == nil || msg.Response.ID.Int64() != i {
			t.Fatalf("response out of order: got %+v, want response id %d", msg, i)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(executed) != 3 {
		t.Fatalf("executed = %v, want only three admitted requests", executed)
	}
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
// pins that closing a saturated connection tears down without waiting for the
// parked handler to free a queue slot, and queued requests do not execute.
func TestServeWebSocketTeardownUnderQueueSaturationCompletesAfterHandlerRelease(t *testing.T) {
	server, _ := captureLogfServer()
	server.requestQueueCapacity = 2

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

	frames := collectFrames(transport)
	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: "n"}))
	waitFor(t, "first serial handler to start", firstStarted)
	for i := int64(3); i < 7; i++ {
		sendRaw(t, transport, rawRequest(t, i, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: "n"}))
	}
	waitForQueueDepth(t, conn, 2)
	for _, id := range []int64{5, 6} {
		requireQueueRefusal(t, "excess request before close", waitForFrameID(t, frames, id), id, "", appwire.RetryDispositionNone)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	// Unregistration follows receive-loop cancellation and queue purge. Keep
	// the handler parked until then so queued work cannot run before cancel.
	waitUnregistered(t, server)
	release()
	waitFor(t, "worker to exit after teardown", conn.workerExited)
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls after teardown = %d, want only the parked handler", got)
	}
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
	waitUntil(t, "the purge advisory reaches Logf", func() bool {
		return strings.Contains(logged(), "discarded")
	})
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
	waitUntil(t, fmt.Sprintf("request queue depth reaches %d", want), func() bool {
		return len(conn.requests) == want
	})
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

// TestServeWebSocketQueueSaturationAdvisoryFiresOncePerConnection pins that
// repeated hard-cap refusals on one connection produce one advisory.
func TestServeWebSocketQueueSaturationAdvisoryFiresOncePerConnection(t *testing.T) {
	server, logged := captureLogfServer()
	server.requestQueueCapacity = 1

	releases := make(chan struct{}, 8)
	started := make(chan struct{}, 4)
	HandleTyped(server.Router(), appwire.MethodEvenerThreadNameSet, func(_ context.Context, _ appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
		started <- struct{}{}
		<-releases
		return appwire.EmptyResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)
	frames := collectFrames(transport)

	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: "n"}))
	waitFor(t, "the parked handler", started)
	for i := int64(3); i <= 7; i++ {
		sendRaw(t, transport, rawRequest(t, i, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: "n"}))
	}
	waitForQueueDepth(t, conn, 1)
	for id := int64(4); id <= 7; id++ {
		requireQueueRefusal(t, "repeated hard-cap refusal", waitForFrameID(t, frames, id), id, "", appwire.RetryDispositionNone)
	}
	for range 2 {
		releases <- struct{}{}
	}
	for i := int64(2); i <= 3; i++ {
		msg := waitFor(t, fmt.Sprintf("response %d", i), frames)
		if msg.Response == nil || msg.Response.ID.Int64() != i {
			t.Fatalf("response out of order: got %+v, want id %d", msg, i)
		}
	}
	// The first two admitted handlers finished, so the queue drained. Fill
	// it again behind another parked handler and check the same advisory flag.
	waitForQueueDepth(t, conn, 0)
	waitFor(t, "second admitted handler", started)
	sendRaw(t, transport, rawRequest(t, 8, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: "n"}))
	waitFor(t, "handler in second saturation cycle", started)
	sendRaw(t, transport, rawRequest(t, 9, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: "n"}))
	waitForQueueDepth(t, conn, 1)
	sendRaw(t, transport, rawRequest(t, 10, appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Ref: "local:th_1", Name: "n"}))
	requireQueueRefusal(t, "refusal after drain and refill", waitForFrameID(t, frames, 10), 10, "", appwire.RetryDispositionNone)
	for range 2 {
		releases <- struct{}{}
	}
	for i := int64(8); i <= 9; i++ {
		msg := waitFor(t, fmt.Sprintf("response %d", i), frames)
		if msg.Response == nil || msg.Response.ID.Int64() != i {
			t.Fatalf("response out of order after refill: got %+v, want id %d", msg, i)
		}
	}

	lines := strings.Count(logged(), conn.id)
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

// parkSendLoopWithFullBuffer engages the gate, parks the send loop on one
// notification, and fills the outbound buffer — the shared setup for both
// full-outbound-buffer tests. Call after the connection initialized (the
// gate must not block the initialize response).
func parkSendLoopWithFullBuffer(t *testing.T, server *Server, gate *gatedSendTransport) *Connection {
	t.Helper()
	conn := registeredConnection(t, server)
	gate.gated.Store(true)
	if !conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
		t.Fatal("could not enqueue the first fill notification")
	}
	waitFor(t, "send loop to park in the gated Send", gate.blocked)
	for conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
	}
	return conn
}

// TestServeWebSocketPingOnFullOutboundBufferEndsConnection pins the reader's
// nonblocking output rule: a full send channel ends the connection promptly.
func TestServeWebSocketPingOnFullOutboundBufferEndsConnection(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	gate := &gatedSendTransport{blocked: make(chan struct{}, 1), release: make(chan struct{})}
	server.wrapWebSocketTransport = func(inner webSocketTransport) webSocketTransport {
		gate.webSocketTransport = inner
		return gate
	}
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := parkSendLoopWithFullBuffer(t, server, gate)
	t.Cleanup(func() { close(gate.release) })

	_ = collectFrames(transport)
	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodPing, appwire.EmptyParams{}))
	waitFor(t, "worker to exit after output refusal", conn.workerExited)
	waitUnregistered(t, server)
}

// TestServeWebSocketWriteTimeoutTearsDownConnectionWithPingParked also pins
// send-loop write-timeout teardown while the reader refuses full output.
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
	conn := parkSendLoopWithFullBuffer(t, server, gate)
	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodPing, appwire.EmptyParams{}))
	// Keep draining the client side: the receive loop's error-path Close
	// performs a close handshake, and a peer that never reads would stall it
	// for coder/websocket's own close timeout — incidental to the cascade
	// under test.
	_ = collectFrames(transport)

	waitFor(t, "worker to exit after the write-timeout cascade", conn.workerExited)
	waitUnregistered(t, server)
}
