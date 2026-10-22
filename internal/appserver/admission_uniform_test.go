package appserver

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// requireQueueRefusal asserts the frame is a pre-execution queue refusal for
// wantID: the unavailable code, the connectionBackpressure discriminant, the
// request's own clientMutationId, the notAccepted outcome, the given retry
// disposition and the one queue-saturation cause.
func requireQueueRefusal(t *testing.T, what string, msg appwire.Message, wantID int64, wantClientMutationID string, wantDisposition appwire.RetryDisposition) {
	t.Helper()
	if msg.Error == nil {
		t.Fatalf("%s: frame = %+v, want an error frame", what, msg)
	}
	if msg.Error.ID.Int64() != wantID {
		t.Fatalf("%s: refusal id = %d, want %d", what, msg.Error.ID.Int64(), wantID)
	}
	if msg.Error.Error.Code != appwire.CodeUnavailable {
		t.Fatalf("%s: code = %d, want CodeUnavailable (-32014)", what, msg.Error.Error.Code)
	}
	encoded, err := json.Marshal(msg.Error.Error.Data)
	if err != nil {
		t.Fatalf("%s: encode refusal data: %v", what, err)
	}
	var data appwire.ErrorData
	if err := json.Unmarshal(encoded, &data); err != nil {
		t.Fatalf("%s: decode refusal data: %v", what, err)
	}
	if data.EvenerErrorInfo != appwire.ErrorConnectionBackpressure {
		t.Fatalf("%s: evenerErrorInfo = %q, want connectionBackpressure", what, data.EvenerErrorInfo)
	}
	if data.ClientMutationID != wantClientMutationID {
		t.Fatalf("%s: clientMutationId = %q, want %q", what, data.ClientMutationID, wantClientMutationID)
	}
	if data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
		t.Fatalf("%s: mutationOutcome = %q, want notAccepted", what, data.MutationOutcome)
	}
	if data.RetryDisposition != wantDisposition {
		t.Fatalf("%s: retryDisposition = %q, want %q", what, data.RetryDisposition, wantDisposition)
	}
	if data.Cause != appwire.BackpressureCauseQueueSaturated {
		t.Fatalf("%s: cause = %q, want queueSaturated", what, data.Cause)
	}
	if msg.Error.Error.Message == "" {
		t.Fatalf("%s: refusal message is empty", what)
	}
}

// waitForFrameID waits for the collected frame carrying id, ignoring the
// unrelated frames other requests on the same connection owe.
func waitForFrameID(t *testing.T, frames <-chan appwire.Message, id int64) appwire.Message {
	t.Helper()
	want := appwire.NewIntID(id).String()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case msg, ok := <-frames:
			if !ok {
				t.Fatalf("frame stream closed before the frame answering request %d", id)
			}
			if msg.IDString() == want {
				return msg
			}
		case <-deadline:
			t.Fatalf("timed out waiting for the frame answering request %d", id)
		}
	}
}

// TestServeWebSocketHardCapRefusalPrecedesAdmittedOrdering pins the admission
// rule at the wire: admitted work keeps FIFO order and runs; work arriving with
// no room is refused before execution, each refusal correlated to its own
// request and marked not-accepted; and the refused work executes zero times
// even after the queue drains.
func TestServeWebSocketHardCapRefusalPrecedesAdmittedOrdering(t *testing.T) {
	server, _ := captureLogfServer()
	server.requestQueueCapacity = 2
	var mu sync.Mutex
	var executed []int64
	parkStarted := make(chan struct{})
	releasePark := make(chan struct{})
	release := sync.OnceFunc(func() { close(releasePark) })
	t.Cleanup(release)
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		mu.Lock()
		executed = append(executed, requestOrdinal(params.SearchTerm))
		first := len(executed) == 1
		mu.Unlock()
		if first {
			close(parkStarted)
			<-releasePark
		}
		return appwire.ThreadListResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)
	frames := collectFrames(transport)

	// 2 executes, 3 and 4 fill the two-entry queue, 5 has no room.
	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodThreadList, appwire.ThreadListParams{SearchTerm: "req-2"}))
	waitFor(t, "the parked serial handler", parkStarted)
	for _, id := range []int64{3, 4, 5} {
		sendRaw(t, transport, rawRequest(t, id, appwire.MethodThreadList, appwire.ThreadListParams{SearchTerm: "req-" + string(rune('0'+id))}))
	}
	waitForQueueDepth(t, conn, 2)
	requireQueueRefusal(t, "the hard-cap refusal", waitForFrameID(t, frames, 5), 5, "", appwire.RetryDispositionNone)

	// The reader kept going: a ping answered while the queue stayed full.
	sendRaw(t, transport, rawRequest(t, 6, appwire.MethodPing, appwire.EmptyParams{}))
	if pong := waitForFrameID(t, frames, 6); pong.Response == nil {
		t.Fatalf("ping while the queue was full = %+v, want a response", pong)
	}
	if depth := len(conn.requests); depth != 2 {
		t.Fatalf("refusals consumed queue depth: depth = %d, want 2", depth)
	}

	release()
	for _, id := range []int64{2, 3, 4} {
		msg := waitForFrameID(t, frames, id)
		if msg.Response == nil || msg.Error != nil {
			t.Fatalf("admitted request %d = %+v, want a response", id, msg)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(executed, []int64{2, 3, 4}) {
		t.Fatalf("executed requests = %v, want [2 3 4] (the refused one must never run)", executed)
	}
}

// TestServeWebSocketHardCapRefusalUsesNotAcceptedEnvelope pins the hard-cap
// envelope: a request that is not a retry-safe mutation carries the
// not-accepted outcome and never an automatic retry promise.
func TestServeWebSocketHardCapRefusalUsesNotAcceptedEnvelope(t *testing.T) {
	server, _ := captureLogfServer()
	server.requestQueueCapacity = 1
	parkStarted := make(chan struct{})
	releasePark := make(chan struct{})
	release := sync.OnceFunc(func() { close(releasePark) })
	t.Cleanup(release)
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		close(parkStarted)
		<-releasePark
		return appwire.ThreadListResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)
	frames := collectFrames(transport)

	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitFor(t, "the parked serial handler", parkStarted)
	sendRaw(t, transport, rawRequest(t, 3, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitForQueueDepth(t, conn, 1)
	// thread/read is not a retry-safe mutation, so it must not be answered
	// with an automatic retry promise.
	sendRaw(t, transport, rawRequest(t, 4, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}))
	requireQueueRefusal(t, "the hard-cap refusal", waitForFrameID(t, frames, 4), 4, "", appwire.RetryDispositionNone)
}

// TestServeWebSocketCapacityKeepsPingAndNotificationReadable pins that
// notifications never enter admission and ping never waits on it: at a full
// request queue a notification is consumed inline with no reply and no
// refusal, and a ping still answers.
func TestServeWebSocketCapacityKeepsPingAndNotificationReadable(t *testing.T) {
	server, _ := captureLogfServer()
	server.requestQueueCapacity = 1
	parkStarted := make(chan struct{})
	releasePark := make(chan struct{})
	release := sync.OnceFunc(func() { close(releasePark) })
	t.Cleanup(release)
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		close(parkStarted)
		<-releasePark
		return appwire.ThreadListResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)
	frames := collectFrames(transport)

	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitFor(t, "the parked serial handler", parkStarted)
	sendRaw(t, transport, rawRequest(t, 3, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitForQueueDepth(t, conn, 1)

	sendRaw(t, transport, appwire.NotificationMessage(appwire.MethodInitialized, appwire.EmptyParams{}))
	sendRaw(t, transport, rawRequest(t, 4, appwire.MethodPing, appwire.EmptyParams{}))
	if pong := waitForFrameID(t, frames, 4); pong.Response == nil {
		t.Fatalf("ping at capacity = %+v, want a response", pong)
	}
	select {
	case msg := <-frames:
		t.Fatalf("a notification at capacity produced a frame: %+v", msg)
	case <-time.After(150 * time.Millisecond):
	}
	if depth := len(conn.requests); depth != 1 {
		t.Fatalf("notification entered admission: queue depth = %d, want 1", depth)
	}
}

// TestServeWebSocketPreBindSaturationRefusesThenBindSucceeds pins the rule from
// connection creation: saturation on a connection with no Manager binding
// refuses with the same hard-cap envelope, and once the queue drains the bind
// still succeeds and the reverse call answers.
func TestServeWebSocketPreBindSaturationRefusesThenBindSucceeds(t *testing.T) {
	server, _ := captureLogfServer()
	server.requestQueueCapacity = 1
	bound := make(chan appwire.SessionHubClient, 1)
	server.InstallSessionTools(func(_ context.Context, _ appwire.SessionBinding, client appwire.SessionHubClient) error {
		bound <- client
		return nil
	})
	parkStarted := make(chan struct{})
	var parkOnce sync.Once
	releasePark := make(chan struct{})
	release := sync.OnceFunc(func() { close(releasePark) })
	t.Cleanup(release)
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		parkOnce.Do(func() { close(parkStarted) })
		<-releasePark
		return appwire.ThreadListResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)
	frames := collectFrames(transport)

	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitFor(t, "the parked serial handler", parkStarted)
	sendRaw(t, transport, rawRequest(t, 3, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitForQueueDepth(t, conn, 1)
	sendRaw(t, transport, rawRequest(t, 4, appwire.MethodRuntimeSessionBind, appwire.SessionBinding{Ref: "local:workspace", InstanceID: "instance", HubID: "hub", BindingID: "binding"}))
	requireQueueRefusal(t, "the pre-bind refusal", waitForFrameID(t, frames, 4), 4, "", appwire.RetryDispositionNone)

	release()
	waitForQueueDepth(t, conn, 0)
	for _, id := range []int64{2, 3} {
		if msg := waitForFrameID(t, frames, id); msg.Response == nil {
			t.Fatalf("admitted request %d = %+v, want a response", id, msg)
		}
	}
	sendRaw(t, transport, rawRequest(t, 5, appwire.MethodRuntimeSessionBind, appwire.SessionBinding{Ref: "local:workspace", InstanceID: "instance", HubID: "hub", BindingID: "binding"}))
	if bind := waitForFrameID(t, frames, 5); bind.Error != nil {
		t.Fatalf("bind after the queue drained: %+v", bind.Error)
	}
	client := waitFor(t, "the bound runtime client", bound)
	result := make(chan appwire.ManagerView, 1)
	go func() {
		view, err := client.State(t.Context())
		if err == nil {
			result <- view
		}
	}()
	reverse := waitFor(t, "the reverse state request after binding", frames)
	if reverse.Request == nil || reverse.Request.Method != appwire.MethodRuntimeSessionState {
		t.Fatalf("reverse state frame = %+v", reverse)
	}
	sendRaw(t, transport, appwire.ResponseMessage(reverse.Request.ID, appwire.ManagerView{Ref: "local:workspace"}))
	view := waitFor(t, "the reverse state result", result)
	if view.Ref != "local:workspace" {
		t.Fatalf("reverse state ref = %q", view.Ref)
	}
}

// gateReaderReplyTransport parks one transport Send after the initialize
// response, so a test can fill the outbound buffer behind it while the send
// loop waits.
type gateReaderReplyTransport struct {
	webSocketTransport
	armed   chan struct{}
	blocked chan struct{}
	release chan struct{}
}

func (g *gateReaderReplyTransport) Send(ctx context.Context, msg appwire.Message) error {
	select {
	case <-g.armed:
	default:
		return g.webSocketTransport.Send(ctx, msg)
	}
	select {
	case g.blocked <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
		return g.webSocketTransport.Send(ctx, msg)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestServeWebSocketReaderReplyOnFullOutputEndsConnection pins bounded output
// failure: a reader-generated reply that cannot enter a completely full
// outbound buffer cancels the connection and existing teardown runs — the
// reader never parks on output.
func TestServeWebSocketReaderReplyOnFullOutputEndsConnection(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	gate := &gateReaderReplyTransport{armed: make(chan struct{}), blocked: make(chan struct{}, 1), release: make(chan struct{})}
	server.wrapWebSocketTransport = func(inner webSocketTransport) webSocketTransport {
		gate.webSocketTransport = inner
		return gate
	}
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	t.Cleanup(func() { close(gate.release) })
	close(gate.armed)
	conn := registeredConnection(t, server)
	_ = collectFrames(transport)

	// Park the send loop and fill every remaining outbound slot.
	if !conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
		t.Fatal("could not enqueue the first fill notification")
	}
	waitFor(t, "the send loop parked in Send", gate.blocked)
	for conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
	}
	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodPing, appwire.EmptyParams{}))

	waitFor(t, "worker to exit after the reader reply could not be enqueued", conn.workerExited)
	waitUnregistered(t, server)
}

// TestServeWebSocketTransientOutputBacklogKeepsConnection pins the other side
// of the boundary: an outbound backlog that does not fill the buffer never
// tears the connection down.
func TestServeWebSocketTransientOutputBacklogKeepsConnection(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	gate := &gateReaderReplyTransport{armed: make(chan struct{}), blocked: make(chan struct{}, 1), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(gate.release) })
	server.wrapWebSocketTransport = func(inner webSocketTransport) webSocketTransport {
		gate.webSocketTransport = inner
		return gate
	}
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	t.Cleanup(release)
	close(gate.armed)
	conn := registeredConnection(t, server)
	frames := collectFrames(transport)

	// One notification parks the send loop; the buffer stays far from full.
	if !conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
		t.Fatal("could not enqueue the gating notification")
	}
	waitFor(t, "the send loop parked in Send", gate.blocked)
	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodPing, appwire.EmptyParams{}))
	select {
	case <-conn.workerExited:
		t.Fatal("a transient outbound backlog tore the connection down")
	case <-time.After(150 * time.Millisecond):
	}
	release()
	if msg := waitForFrameID(t, frames, 2); msg.Response == nil {
		t.Fatalf("ping under transient backlog = %+v, want response", msg)
	}
	sendRaw(t, transport, rawRequest(t, 3, appwire.MethodPing, appwire.EmptyParams{}))
	if msg := waitForFrameID(t, frames, 3); msg.Response == nil {
		t.Fatalf("ping after transient backlog = %+v, want response", msg)
	}
	select {
	case <-conn.workerExited:
		t.Fatal("a transient outbound backlog tore the connection down after the send resumed")
	case <-time.After(150 * time.Millisecond):
	}
}

// TestReaderRefusalNeverWaitsOnFullOutputBuffer pins at unit level that a
// reader-generated refusal gets one nonblocking attempt: with the outbound
// buffer full and no send loop, the reader returns false at once instead of
// parking, and the connection is canceled.
func TestReaderRefusalNeverWaitsOnFullOutputBuffer(t *testing.T) {
	server, logged := captureLogfServer()
	server.requestQueueCapacity = 2 // high water at 1 frame
	conn := server.NewConnection("test-conn")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn.setCancel(cancel)

	for range cap(conn.send) {
		conn.send <- appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})
	}
	conn.requests <- admittedRequest{message: rawRequest(t, 99, appwire.MethodThreadList, appwire.ThreadListParams{})}

	done := make(chan bool, 1)
	go func() {
		done <- conn.enqueueRequest(ctx, rawRequest(t, 6, appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "cmid-6"}))
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("the refusal reported the connection alive after failing to enqueue it")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the refusal parked the reader on a full outbound buffer")
	}
	if ctx.Err() == nil {
		t.Fatal("the failed refusal did not cancel the connection")
	}
	if depth := len(conn.requests); depth != 1 {
		t.Fatalf("refused request entered the queue: depth = %d, want 1", depth)
	}
	if lines := countAdvisoryLines(logged, "high-water"); lines != 1 {
		t.Fatalf("high-water advisories = %d, want exactly 1:\n%s", lines, logged())
	}
}

// TestServeWebSocketForceStopBusyRefusalNeverWaitsOnOutput pins that the
// force-stop busy refusal, a reader reply, follows the same nonblocking rule.
func TestServeWebSocketForceStopBusyRefusalNeverWaitsOnOutput(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	gate := &gateReaderReplyTransport{armed: make(chan struct{}), blocked: make(chan struct{}, 1), release: make(chan struct{})}
	server.wrapWebSocketTransport = func(inner webSocketTransport) webSocketTransport {
		gate.webSocketTransport = inner
		return gate
	}
	HandleTyped(server.Router(), appwire.MethodEvenerThreadForceStop, func(context.Context, appwire.ThreadForceStopParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	t.Cleanup(func() { close(gate.release) })
	close(gate.armed)
	conn := registeredConnection(t, server)
	_ = collectFrames(transport)
	conn.mu.Lock()
	conn.recoveryRunning = true
	conn.mu.Unlock()

	if !conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
		t.Fatal("could not enqueue the first fill notification")
	}
	waitFor(t, "the send loop parked in Send", gate.blocked)
	for conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
	}
	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: "local:owner"}))

	waitFor(t, "worker to exit after the busy refusal could not be enqueued", conn.workerExited)
	waitUnregistered(t, server)
}

// TestServeWebSocketRefusalAdvisoryStaysOneShot pins that the surviving
// advisory is the refusal's: sustained hard-cap refusals produce exactly one
// advisory event.
func TestServeWebSocketRefusalAdvisoryStaysOneShot(t *testing.T) {
	server, logged := captureLogfServer()
	server.requestQueueCapacity = 1
	parkStarted := make(chan struct{})
	releasePark := make(chan struct{})
	release := sync.OnceFunc(func() { close(releasePark) })
	t.Cleanup(release)
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		close(parkStarted)
		<-releasePark
		return appwire.ThreadListResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)
	frames := collectFrames(transport)

	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitFor(t, "the parked serial handler", parkStarted)
	sendRaw(t, transport, rawRequest(t, 3, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitForQueueDepth(t, conn, 1)
	for id := int64(4); id <= 7; id++ {
		sendRaw(t, transport, rawRequest(t, id, appwire.MethodThreadList, appwire.ThreadListParams{}))
		requireQueueRefusal(t, "a repeated hard-cap refusal", waitForFrameID(t, frames, id), id, "", appwire.RetryDispositionNone)
	}
	advisory := countAdvisoryLines(logged, conn.id)
	if advisory != 1 {
		t.Fatalf("advisory lines = %d, want exactly one refusal advisory:\n%s", advisory, logged())
	}
	release()
}
