package appserver

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// parkedSlowReadServer builds a captureLogf server whose thread/read handler
// parks each call until it receives one release token, reporting each start
// with its ThreadID marker. Cleanup floods the token channel so no handler
// outlives the test.
func parkedSlowReadServer(t *testing.T) (*Server, func() string, chan string, chan struct{}) {
	t.Helper()
	server, logged := captureLogfServer()
	started := make(chan string, slowReadDispatchCap*2+4)
	releases := make(chan struct{}, slowReadDispatchCap*2+4)
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		started <- params.ThreadID
		<-releases
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: params.ThreadID}}, nil
	})
	t.Cleanup(func() {
		for range cap(releases) {
			select {
			case releases <- struct{}{}:
			default:
			}
		}
	})
	return server, logged, started, releases
}

// fillSlowReadCap issues slowReadDispatchCap concurrent slow reads through
// the client and waits until every one is parked in its handler.
func fillSlowReadCap(t *testing.T, client *appwire.Client, started chan string, done chan error) {
	t.Helper()
	ctx := context.Background()
	for i := range slowReadDispatchCap {
		go func() {
			_, err := client.ThreadRead(ctx, appwire.ThreadReadParams{ThreadID: fmt.Sprintf("read-%d", i)})
			done <- err
		}()
	}
	for range slowReadDispatchCap {
		waitFor(t, "a slow read to park in its handler", started)
	}
}

// TestServeWebSocketSlowReadCapBlocksNextReadWithOneAdvisory pins test 10(a):
// with slowReadDispatchCap slow reads parked, the next slow read does not
// start; no wire error or eviction occurred — the connection is healthy, just
// full, and ping still answers — and the one-shot cap advisory reached Logf.
func TestServeWebSocketSlowReadCapBlocksNextReadWithOneAdvisory(t *testing.T) {
	server, logged, started, releases := parkedSlowReadServer(t)
	httpServer := serveWebSocketHTTP(t, server)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()

	done := make(chan error, slowReadDispatchCap+1)
	fillSlowReadCap(t, client, started, done)

	go func() {
		_, err := client.ThreadRead(ctx, appwire.ThreadReadParams{ThreadID: "read-beyond-cap"})
		done <- err
	}()
	select {
	case id := <-started:
		t.Fatalf("slow read %q started beyond the cap", id)
	case <-time.After(150 * time.Millisecond):
	}

	var out appwire.EmptyResponse
	if err := client.Request(ctx, appwire.MethodPing, appwire.EmptyParams{}, &out); err != nil {
		t.Fatalf("ping failed while the cap was saturated: %v", err)
	}
	logs := logged()
	if strings.Contains(logs, "evicting") {
		t.Fatalf("cap saturation caused an eviction:\n%s", logs)
	}
	advisories := 0
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "slow-read") {
			advisories++
		}
	}
	if advisories != 1 {
		t.Fatalf("cap advisories = %d, want exactly 1 in:\n%s", advisories, logs)
	}

	for range slowReadDispatchCap + 1 {
		releases <- struct{}{}
	}
	for range slowReadDispatchCap + 1 {
		if err := waitFor(t, "a released slow read to complete", done); err != nil {
			t.Fatalf("slow read failed after release: %v", err)
		}
	}
	waitFor(t, "the blocked read to start once a slot freed", started)
}

// rawParkedCapConnection stands up the raw-transport fixture the ordering and
// teardown cases share: slowReadDispatchCap reads parked via pipelined frames
// (ids 2..cap+1), a dequeue signal for when the worker has dequeued the
// beyond-cap read (id cap+2), which is the deterministic "worker is parked in
// the acquire" marker.
func rawParkedCapConnection(t *testing.T, server *Server, started chan string) (*appwire.WSTransport, *Connection, chan struct{}) {
	t.Helper()
	beyondCapDequeued := make(chan struct{})
	var readDequeues atomic.Int64
	server.afterWorkerDequeue = func(msg appwire.Message) {
		if msg.Request == nil || msg.Request.Method != appwire.MethodThreadRead {
			return
		}
		if readDequeues.Add(1) == slowReadDispatchCap+1 {
			close(beyondCapDequeued)
		}
	}
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)

	for i := int64(2); i < slowReadDispatchCap+2; i++ {
		sendRaw(t, transport, rawRequest(t, i, appwire.MethodThreadRead, appwire.ThreadReadParams{ThreadID: fmt.Sprintf("read-%d", i)}))
	}
	for range slowReadDispatchCap {
		waitFor(t, "a slow read to park in its handler", started)
	}
	sendRaw(t, transport, rawRequest(t, slowReadDispatchCap+2, appwire.MethodThreadRead, appwire.ThreadReadParams{ThreadID: "read-beyond-cap"}))
	waitFor(t, "the worker to dequeue the beyond-cap read", beyondCapDequeued)
	return transport, conn, beyondCapDequeued
}

// TestServeWebSocketSlowReadCapHeadOfLineBlocksAndDrainsOnCompletion pins
// test 10(b) — the design's second deliberate scheduling change: with the cap
// full and a beyond-cap slow read parked in the acquire, a serial request
// queued behind it does not execute until a slot frees; releasing one parked
// read lets the blocked read start and the serial request complete.
func TestServeWebSocketSlowReadCapHeadOfLineBlocksAndDrainsOnCompletion(t *testing.T) {
	server, _, started, releases := parkedSlowReadServer(t)
	serialStarted := make(chan struct{})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		close(serialStarted)
		return appwire.ThreadListResponse{}, nil
	})
	transport, _, _ := rawParkedCapConnection(t, server, started)
	serialID := int64(slowReadDispatchCap + 3)
	sendRaw(t, transport, rawRequest(t, serialID, appwire.MethodThreadList, appwire.ThreadListParams{}))

	select {
	case <-serialStarted:
		t.Fatal("a serial request overtook a cap-blocked slow read")
	case id := <-started:
		t.Fatalf("slow read %q started beyond the cap", id)
	case <-time.After(150 * time.Millisecond):
	}

	frames := collectFrames(transport)
	releases <- struct{}{}
	if id := waitFor(t, "the blocked slow read to start once a slot freed", started); id != "read-beyond-cap" {
		t.Fatalf("started read = %q, want the cap-blocked one", id)
	}
	waitFor(t, "the serial request to execute after the blocked read dispatched", serialStarted)

	for range slowReadDispatchCap {
		releases <- struct{}{}
	}
	responses := 0
	deadline := time.After(5 * time.Second)
	for responses < slowReadDispatchCap+2 {
		select {
		case msg := <-frames:
			if msg.Error != nil {
				t.Fatalf("wire error under cap saturation: %+v", msg.Error)
			}
			if msg.Response != nil {
				responses++
			}
		case <-deadline:
			t.Fatalf("responses = %d of %d after draining", responses, slowReadDispatchCap+2)
		}
	}
}

// TestServeWebSocketSlowReadCapAcquireExitsOnClose pins test 10(c): a close
// arriving while the worker is parked in the cap acquire exits the worker
// promptly — the acquire selects on ctx.Done — and teardown completes without
// waiting for the parked reads.
func TestServeWebSocketSlowReadCapAcquireExitsOnClose(t *testing.T) {
	server, _, started, releases := parkedSlowReadServer(t)
	transport, conn, _ := rawParkedCapConnection(t, server, started)

	if err := transport.Close(); err != nil {
		t.Fatalf("close transport: %v", err)
	}
	waitFor(t, "the worker to exit from the parked acquire", conn.workerExited)
	waitUnregistered(t, server)
	select {
	case id := <-started:
		t.Fatalf("slow read %q started during teardown", id)
	default:
	}
	// The sixteen reads are still parked; release them so the test does not
	// leak goroutines — "never returns" is the property under test, not a
	// goroutine bequeathed to the next test.
	for range slowReadDispatchCap {
		releases <- struct{}{}
	}
}

// TestServeWebSocketSlowReadCapAcquireRaceNeverStartsAfterCancel pins test
// 10(d) deterministically: park the worker in the after-acquire hook with a
// slot held, cancel the connection, release the hook — the slow read never
// starts and the slot was released.
func TestServeWebSocketSlowReadCapAcquireRaceNeverStartsAfterCancel(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	hookEntered := make(chan struct{})
	hookRelease := make(chan struct{})
	server.afterSlowReadAcquire = func() {
		close(hookEntered)
		<-hookRelease
	}
	var readCalls atomic.Int64
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		readCalls.Add(1)
		return appwire.ThreadReadResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)

	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodThreadRead, appwire.ThreadReadParams{ThreadID: "read-racing-cancel"}))
	waitFor(t, "the worker to park in the after-acquire hook", hookEntered)

	if err := transport.Close(); err != nil {
		t.Fatalf("close transport: %v", err)
	}
	waitUnregistered(t, server)
	close(hookRelease)

	waitFor(t, "the worker to exit at the post-acquire re-check", conn.workerExited)
	if got := readCalls.Load(); got != 0 {
		t.Fatalf("slow read ran %d times after cancellation was observable at the acquire, want 0", got)
	}
	if held := len(conn.slowReadSlots); held != 0 {
		t.Fatalf("cap slots still held after the canceled acquire = %d, want 0", held)
	}
}

// TestServeWebSocketSlowReadCapStallAdvisoryNamesWedgedLane pins test 10(e):
// with the cap full of parked reads, a beyond-cap read parking the worker in
// the acquire, and a serial request queued behind it, ping still answers (the
// masking interaction is real), the stall advisory reaches Logf naming the
// in-flight methods, and the serial request has not run.
func TestServeWebSocketSlowReadCapStallAdvisoryNamesWedgedLane(t *testing.T) {
	server, logged, started, _ := parkedSlowReadServer(t)
	server.slowReadStallThreshold = 100 * time.Millisecond
	serialStarted := make(chan struct{})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		close(serialStarted)
		return appwire.ThreadListResponse{}, nil
	})
	transport, _, _ := rawParkedCapConnection(t, server, started)
	sendRaw(t, transport, rawRequest(t, slowReadDispatchCap+3, appwire.MethodThreadList, appwire.ThreadListParams{}))
	frames := collectFrames(transport)

	// The lane is wedged; the client's only liveness signal keeps answering.
	sendRaw(t, transport, rawRequest(t, 100, appwire.MethodPing, appwire.EmptyParams{}))
	pong := waitFor(t, "ping response while the lane is wedged", frames)
	if pong.Response == nil || pong.Response.ID.Int64() != 100 {
		t.Fatalf("frame = %+v, want ping response id 100", pong)
	}

	stallDeadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logged(), "parked") && time.Now().Before(stallDeadline) {
		time.Sleep(5 * time.Millisecond)
	}
	var stall string
	for _, line := range strings.Split(logged(), "\n") {
		if strings.Contains(line, "parked") {
			stall = line
		}
	}
	if stall == "" {
		t.Fatalf("no stall advisory reached Logf:\n%s", logged())
	}
	if !strings.Contains(stall, appwire.MethodThreadRead) {
		t.Fatalf("stall advisory does not name the in-flight methods: %s", stall)
	}
	select {
	case <-serialStarted:
		t.Fatal("the serial request ran while the lane was wedged")
	default:
	}
}

// TestServeWebSocketSlowReadCapComposedSaturationRecoversOnRelease pins test
// 10(f), the composed corner: with the worker parked in the acquire, frames
// pile into the request queue until the receive loop blocks — ping stops
// answering only once the queue is full, the keepalive gate defers transport
// pings for exactly that window, the queue-saturation advisory fired — and
// releasing one parked read drains the whole composition.
func TestServeWebSocketSlowReadCapComposedSaturationRecoversOnRelease(t *testing.T) {
	server, logged, started, releases := parkedSlowReadServer(t)
	server.requestQueueCapacity = 2
	// Non-blocking sends: the setup phase pipelines the sixteen parked reads
	// through the same two-slot queue, so it can transiently block the loop
	// many times before the phase under test.
	blocked := make(chan struct{}, 4)
	server.blockedEnqueue = func() {
		select {
		case blocked <- struct{}{}:
		default:
		}
	}
	ticker := newControlledKeepaliveTicker()
	decision := make(chan bool, 8)
	server.keepaliveTickerFactory = func(time.Duration) webSocketKeepaliveTicker { return ticker }
	server.keepaliveDecision = func(ok bool) { decision <- ok }
	var serialRuns atomic.Int64
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		serialRuns.Add(1)
		return appwire.ThreadListResponse{}, nil
	})
	transport, _, _ := rawParkedCapConnection(t, server, started)
	frames := collectFrames(transport)

	// Below queue saturation, ping answers even though the worker is parked.
	sendRaw(t, transport, rawRequest(t, 100, appwire.MethodPing, appwire.EmptyParams{}))
	pong := waitFor(t, "ping response before the queue filled", frames)
	if pong.Response == nil || pong.Response.ID.Int64() != 100 {
		t.Fatalf("frame = %+v, want ping response id 100", pong)
	}

	// The setup phase's transient blocks are over — the worker dequeued the
	// beyond-cap read, so every earlier enqueue completed — and the worker is
	// now parked in the acquire; discard stale signals so the wait below
	// observes the serial-fill phase, not the setup.
	for {
		select {
		case <-blocked:
			continue
		default:
		}
		break
	}

	// Fill the queue behind the parked worker until the receive loop blocks.
	base := int64(slowReadDispatchCap + 3)
	for i := base; i < base+3; i++ {
		sendRaw(t, transport, rawRequest(t, i, appwire.MethodThreadList, appwire.ThreadListParams{}))
	}
	waitFor(t, "the receive loop to park on the full queue", blocked)
	if !strings.Contains(logged(), "request queue is full") {
		t.Fatalf("queue-saturation advisory did not fire:\n%s", logged())
	}

	// The loop is out of Recv: the keepalive gate defers transport pings and
	// an app-level ping cannot even be read.
	ticker.Tick()
	if got := waitFor(t, "a keepalive decision while the loop is blocked", decision); got {
		t.Fatal("keepalive reported the reader available while the receive loop was parked on the full queue")
	}
	sendRaw(t, transport, rawRequest(t, 101, appwire.MethodPing, appwire.EmptyParams{}))
	select {
	case msg := <-frames:
		t.Fatalf("frame arrived while the queue was full: %+v", msg)
	case <-time.After(150 * time.Millisecond):
	}

	// One completed read drains the whole composition.
	releases <- struct{}{}
	if id := waitFor(t, "the blocked read to start once a slot freed", started); id != "read-beyond-cap" {
		t.Fatalf("started read = %q, want the cap-blocked one", id)
	}
	// The released read's own response rides along; wait for the specific
	// ids the composition owes — the three serial requests and the ping.
	want := map[int64]bool{base: true, base + 1: true, base + 2: true, 101: true}
	deadline := time.After(5 * time.Second)
	got := map[int64]bool{}
	for len(got) < len(want) {
		select {
		case msg := <-frames:
			if msg.Error != nil {
				t.Fatalf("wire error during composed-saturation drain: %+v", msg.Error)
			}
			if msg.Response != nil && want[msg.Response.ID.Int64()] {
				got[msg.Response.ID.Int64()] = true
			}
		case <-deadline:
			t.Fatalf("drain incomplete: got responses %v of %v", got, want)
		}
	}
	if runs := serialRuns.Load(); runs != 3 {
		t.Fatalf("serial requests ran %d times, want 3", runs)
	}
	// The loop is back in Recv; the keepalive gate recovers.
	recovered := false
	recoverDeadline := time.NewTimer(5 * time.Second)
	defer recoverDeadline.Stop()
	for !recovered {
		ticker.Tick()
		select {
		case ok := <-decision:
			recovered = ok
		case <-recoverDeadline.C:
			t.Fatal("keepalive never observed the recovered reader")
		}
	}
	_ = releases
}
