package appserver

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// backpressureFixture builds a captureLogf server with the given inbound queue
// capacity, parks its serial worker on the first thread/list request (so a
// test owns the queue's depth exactly), and returns the raw transport with a
// frame collector. blockedEnqueue, when non-nil, observes a hard-cap refusal
// before the reader continues. release unparks one worker handler call at a time; closing it
// at cleanup unparks every present and future call.
func backpressureFixture(t *testing.T, queueCapacity int, blockedEnqueue func()) (
	*Server, func() string, *Connection, *appwire.WSTransport, <-chan appwire.Message, chan<- struct{},
) {
	t.Helper()
	server, logged := captureLogfServer()
	server.requestQueueCapacity = queueCapacity
	server.blockedEnqueue = blockedEnqueue
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	parked := make(chan struct{})
	var parkOnce sync.Once
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		parkOnce.Do(func() { close(parked) })
		<-release
		return appwire.ThreadListResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)
	frames := collectFrames(transport)

	// Park the serial worker on the first request; from here the queue is
	// the only thing that fills.
	sendRaw(t, transport, rawRequest(t, 1, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitFor(t, "the worker to park in its handler", parked)
	return server, logged, conn, transport, frames, release
}

// requireBackpressureRefusal asserts the frame is the typed high-water
// refusal for wantID: the unavailable code, the connectionBackpressure
// discriminant, the request's own clientMutationId, and the transient
// mutation envelope (notAccepted plus automatic) whose message states what
// happened on the hub without promising any client-side retry.
func requireBackpressureRefusal(t *testing.T, what string, msg appwire.Message, wantID int64, wantClientMutationID string) {
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
	// The wire round-trip renders Data as a generic map; decode it back
	// through the standard envelope the way the shared TypeScript parser
	// reads these fields.
	encoded, err := json.Marshal(msg.Error.Error.Data)
	if err != nil {
		t.Fatalf("%s: marshal refusal data: %v", what, err)
	}
	var data appwire.ErrorData
	if err := json.Unmarshal(encoded, &data); err != nil {
		t.Fatalf("%s: refusal data is not the standard envelope (%v): %s", what, err, encoded)
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
	if data.RetryDisposition != appwire.RetryDispositionAutomatic {
		t.Fatalf("%s: retryDisposition = %q, want automatic", what, data.RetryDisposition)
	}
	if data.Cause != appwire.BackpressureCauseQueueSaturated {
		t.Fatalf("%s: cause = %q, want queueSaturated", what, data.Cause)
	}
	if strings.TrimSpace(msg.Error.Error.Message) == "" {
		t.Fatalf("%s: refusal message is empty", what)
	}
	if strings.Contains(strings.ToLower(msg.Error.Error.Message), "retry") {
		t.Fatalf("%s: refusal message promises a client retry it cannot keep: %q", what, msg.Error.Error.Message)
	}
}

// countAdvisoryLines counts log lines containing marker, so a test can pin a
// one-shot advisory independently of the other advisory's wording.
func countAdvisoryLines(logged func() string, marker string) int {
	count := 0
	for line := range strings.SplitSeq(logged(), "\n") {
		if strings.Contains(line, marker) {
			count++
		}
	}
	return count
}

// TestServeWebSocketHighWaterRefusalAnswersMutationsInBand pins the core
// contract: with the worker parked and the inbound queue at its high-water
// depth, a retry-safe mutation is answered in band with the typed refusal
// carrying the request's own clientMutationId, the refusal consumes no queue
// depth, reads keep riding FIFO below full, the high-water advisory is a
// one-shot for the refusal branch, and a
// later full queue still fires the operator-critical full-queue advisory
// exactly once — the refusal cannot consume it.
func TestServeWebSocketHighWaterRefusalAnswersMutationsInBand(t *testing.T) {
	_, logged, conn, transport, frames, _ := backpressureFixture(t, 8, nil)

	// Reads fill the queue to the high-water depth; none is ever refused.
	for id := int64(2); id <= 5; id++ {
		sendRaw(t, transport, rawRequest(t, id, appwire.MethodThreadList, appwire.ThreadListParams{}))
	}
	waitForQueueDepth(t, conn, 4)
	if lines := countAdvisoryLines(logged, "high-water"); lines != 0 {
		t.Fatalf("reads at high water fired the high-water advisory %d times, want 0:\n%s", lines, logged())
	}

	// A retry-safe mutation arriving at high water is refused in band.
	sendRaw(t, transport, rawRequest(t, 6, appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "cmid-6"}))
	requireBackpressureRefusal(t, "the first high-water refusal", waitFor(t, "the high-water refusal", frames), 6, "cmid-6")
	if depth := len(conn.requests); depth != 4 {
		t.Fatalf("queue depth = %d after the refusal, want 4 (a refusal consumes no depth)", depth)
	}

	// The high-water refusal fires one advisory without parking the reader.
	if lines := countAdvisoryLines(logged, "high-water"); lines != 1 {
		t.Fatalf("high-water advisories = %d, want exactly 1:\n%s", lines, logged())
	}

	// A second mutation is refused the same way; the advisory stays one-shot.
	sendRaw(t, transport, rawRequest(t, 7, appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "cmid-7"}))
	requireBackpressureRefusal(t, "the second high-water refusal", waitFor(t, "the second high-water refusal", frames), 7, "cmid-7")
	if lines := countAdvisoryLines(logged, "high-water"); lines != 1 {
		t.Fatalf("high-water advisories = %d after a second refusal, want 1:\n%s", lines, logged())
	}
	if depth := len(conn.requests); depth != 4 {
		t.Fatalf("queue depth = %d after two refusals, want 4", depth)
	}

	// Reads below full still ride FIFO: nothing about them changed.
	sendRaw(t, transport, rawRequest(t, 8, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitForQueueDepth(t, conn, 5)

	// Filling the queue to full afterwards still fires the full-queue
	// advisory exactly once: the refusal's one-shot cannot consume it.
	for id := int64(9); id <= 11; id++ {
		sendRaw(t, transport, rawRequest(t, id, appwire.MethodThreadList, appwire.ThreadListParams{}))
	}
	sendRaw(t, transport, rawRequest(t, 12, appwire.MethodThreadList, appwire.ThreadListParams{}))
	requireQueueRefusal(t, "hard-cap refusal after high-water refusal", waitForFrameID(t, frames, 12), 12, "", appwire.RetryDispositionNone)
	if lines := countAdvisoryLines(logged, conn.id); lines != 2 {
		t.Fatalf("advisories after high-water and hard-cap refusals = %d, want 2:\n%s", lines, logged())
	}
	if lines := countAdvisoryLines(logged, "high-water"); lines != 1 {
		t.Fatalf("high-water advisories = %d after the queue filled, want 1:\n%s", lines, logged())
	}
}

// TestServeWebSocketHighWaterBelowWaterMutationsRide pins the honest
// ride-window: a mutation arriving below high water rides the queue exactly
// as today, and the trigger's guarantee is that a backlogged connection
// stops accepting new mutations — not that no mutation can ever queue.
func TestServeWebSocketHighWaterBelowWaterMutationsRide(t *testing.T) {
	_, _, conn, transport, frames, _ := backpressureFixture(t, 8, nil)

	for id := int64(2); id <= 4; id++ {
		sendRaw(t, transport, rawRequest(t, id, appwire.MethodThreadList, appwire.ThreadListParams{}))
	}
	waitForQueueDepth(t, conn, 3)

	// Below high water the mutation rides the queue, no refusal in band.
	sendRaw(t, transport, rawRequest(t, 5, appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "cmid-5"}))
	waitForQueueDepth(t, conn, 4)
	select {
	case msg := <-frames:
		t.Fatalf("frame arrived for a below-high-water mutation: %+v", msg)
	case <-time.After(150 * time.Millisecond):
	}

	// At high water the next mutation is refused.
	sendRaw(t, transport, rawRequest(t, 6, appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "cmid-6"}))
	requireBackpressureRefusal(t, "the at-water refusal", waitFor(t, "the high-water refusal", frames), 6, "cmid-6")
}

// TestServeWebSocketHighWaterRefusalWithoutClientMutationID pins the
// degradation path: a mutation whose params carry no extractable
// clientMutationId is still refused at high water, with an empty id, so the
// frame is never queued either way.
func TestServeWebSocketHighWaterRefusalWithoutClientMutationID(t *testing.T) {
	_, _, conn, transport, frames, _ := backpressureFixture(t, 4, nil)

	for id := int64(2); id <= 3; id++ {
		sendRaw(t, transport, rawRequest(t, id, appwire.MethodThreadList, appwire.ThreadListParams{}))
	}
	waitForQueueDepth(t, conn, 2)

	// No clientMutationId in the params: the refusal still answers the frame.
	sendRaw(t, transport, rawRequest(t, 4, appwire.MethodTurnStart, map[string]any{"threadId": "t"}))
	refusal := waitFor(t, "the refusal for a mutation without an id", frames)
	requireBackpressureRefusal(t, "the no-id refusal", refusal, 4, "")
	if depth := len(conn.requests); depth != 2 {
		t.Fatalf("queue depth = %d after the no-id refusal, want 2", depth)
	}
}

// TestServeWebSocketFullQueueAdvisoryDoesNotSuppressHighWaterRefusal pins the
// reverse advisory order: a connection that already fired the operator's
// full-queue advisory still refuses a mutation at high water with its own
// advisory — the two one-shots are independent flags, and neither consumes
// the other.
func TestServeWebSocketFullQueueAdvisoryDoesNotSuppressHighWaterRefusal(t *testing.T) {
	_, logged, conn, transport, frames, _ := backpressureFixture(t, 8, nil)

	// Fill the queue completely and refuse one more read without parking.
	for id := int64(2); id <= 9; id++ {
		sendRaw(t, transport, rawRequest(t, id, appwire.MethodThreadList, appwire.ThreadListParams{}))
	}
	sendRaw(t, transport, rawRequest(t, 10, appwire.MethodThreadList, appwire.ThreadListParams{}))
	requireQueueRefusal(t, "full queue", waitForFrameID(t, frames, 10), 10, "", appwire.RetryDispositionNone)
	if lines := countAdvisoryLines(logged, conn.id); lines != 1 {
		t.Fatalf("full-queue advisories = %d, want exactly 1:\n%s", lines, logged())
	}

	// The reader continues past the hard-cap refusal. A mutation at high water
	// still gets the automatic disposition before the hard-cap check.
	sendRaw(t, transport, rawRequest(t, 11, appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "cmid-11"}))
	requireBackpressureRefusal(t, "high-water after hard-cap", waitForFrameID(t, frames, 11), 11, "cmid-11")

	if lines := countAdvisoryLines(logged, conn.id); lines != 2 {
		t.Fatalf("advisories after high-water and hard-cap refusals = %d, want 2:\n%s", lines, logged())
	}
	if lines := countAdvisoryLines(logged, "high-water"); lines != 1 {
		t.Fatalf("high-water advisories = %d after the queue had already filled, want 1:\n%s", lines, logged())
	}
}

// TestServeWebSocketRefusedMutationRetriesIntoAFreedHub pins the journey's
// hub-side landing: once the worker drains, the mutation a client resends
// after its refusal rides the now-empty queue exactly once — the refusal
// consumed no depth, queued nothing, and the retried send executes a single
// time with its own clientMutationId, which is all the admission journal
// needs to dedup a raced double-apply.
func TestServeWebSocketRefusedMutationRetriesIntoAFreedHub(t *testing.T) {
	server, _, conn, transport, frames, release := backpressureFixture(t, 4, nil)
	turnStarts := make(chan appwire.TurnStartParams, 4)
	HandleTyped(server.Router(), appwire.MethodTurnStart,
		func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			turnStarts <- params
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_sent", Status: appwire.TurnStatusInProgress}}, nil
		})

	// Reads fill the queue to high water behind the parked worker.
	for id := int64(2); id <= 3; id++ {
		sendRaw(t, transport, rawRequest(t, id, appwire.MethodThreadList, appwire.ThreadListParams{}))
	}
	waitForQueueDepth(t, conn, 2)
	// The mutation is refused in band, never queued, never run.
	sendRaw(t, transport, rawRequest(t, 4, appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "cmid-4"}))
	requireBackpressureRefusal(t, "the high-water refusal", waitFor(t, "the high-water refusal", frames), 4, "cmid-4")
	select {
	case params := <-turnStarts:
		t.Fatalf("refused mutation ran: %+v", params)
	default:
	}

	// The worker drains: one release token per parked handler call.
	for range len(conn.requests) + 1 {
		release <- struct{}{}
	}
	waitForQueueDepth(t, conn, 0)

	// The retried send rides the empty queue and runs exactly once. Its
	// params validate now — the refusal answered before this validation only
	// under load, so a retry surfaces the real verdict.
	sendRaw(t, transport, rawRequest(t, 5, appwire.MethodTurnStart, appwire.TurnStartParams{
		ClientMutationID:   "cmid-4",
		ExpectedInstanceID: "instance-1",
	}))
	for {
		msg := waitFor(t, "the retried send's response", frames)
		if msg.Response != nil && msg.Response.ID.Int64() == 5 {
			break
		}
		if msg.Error != nil {
			t.Fatalf("the retried send failed: %+v", msg.Error)
		}
	}
	params := waitFor(t, "the retried send to execute", turnStarts)
	if params.ClientMutationID != "cmid-4" {
		t.Fatalf("executed mutation id = %q, want cmid-4", params.ClientMutationID)
	}
	select {
	case extra := <-turnStarts:
		t.Fatalf("the retried send executed a second time: %+v", extra)
	default:
	}
}

// TestConnectionHighWaterRefusalOnFullOutboundBufferCancels pins the
// nonblocking reply rule: full output cancels the connection and leaves the
// rejected mutation out of the request queue.
func TestConnectionHighWaterRefusalOnFullOutboundBufferCancels(t *testing.T) {
	server, logged := captureLogfServer()
	server.requestQueueCapacity = 2 // high water at 1 frame
	conn := server.NewConnection("test-conn")

	// No send loop runs for this bare connection, so the outbound buffer
	// stays as full as the test fills it.
	for i := range cap(conn.send) {
		conn.send <- appwire.ResponseMessage(appwire.NewIntID(int64(i)), appwire.EmptyResponse{})
	}
	conn.requests <- admittedRequest{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn.setCancel(cancel)
	done := make(chan bool, 1)
	go func() {
		done <- conn.enqueueRequest(ctx, rawRequest(t, 6, appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "cmid-6"}))
	}()

	// The refusal cannot enter the full outbound buffer: the reader returns.
	select {
	case ok := <-done:
		if ok {
			t.Fatal("enqueueRequest kept the connection alive after refusing full output")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader parked on full outbound buffer")
	}
	if ctx.Err() == nil {
		t.Fatal("failed refusal did not cancel the connection")
	}
	if depth := len(conn.requests); depth != 1 {
		t.Fatalf("queue depth = %d, want 1 (the refused mutation never queued)", depth)
	}
	if lines := countAdvisoryLines(logged, "high-water"); lines != 1 {
		t.Fatalf("high-water advisories = %d, want exactly 1:\n%s", lines, logged())
	}
}
