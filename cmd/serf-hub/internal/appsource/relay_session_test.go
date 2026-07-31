package appsource

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
)

type relayReadCall struct {
	request   appwire.Request
	transport *scriptedAppwireTransport
}

type relayTestDaemon struct {
	reads chan relayReadCall
	dials atomic.Int32
	mu    sync.Mutex
	open  int
}

func newRelayTestSource(t *testing.T, entries []rendezvous.Entry) (*LocalDaemonSource, *relayTestDaemon) {
	t.Helper()
	daemon := &relayTestDaemon{reads: make(chan relayReadCall, 16)}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		out := make([]LocalDaemonEntry, 0, len(entries))
		for _, entry := range entries {
			out = append(out, LocalDaemonEntry{Entry: entry})
		}
		return out
	}, nil)
	source.dial = func(_ context.Context, endpoint string, _ *http.Client, _ http.Header) (appwire.Transport, error) {
		daemon.dials.Add(1)
		var transport *scriptedAppwireTransport
		transport = newScriptedAppwireTransport(func(_ context.Context, message appwire.Message) error {
			if message.Request == nil {
				return nil
			}
			switch message.Request.Method {
			case appwire.MethodInitialize:
				transport.recv <- appwire.ResponseMessage(message.Request.ID, appwire.InitializeResponse{
					ProtocolVersion: appwire.ProtocolVersion,
				})
			case appwire.MethodThreadRead:
				daemon.reads <- relayReadCall{request: *message.Request, transport: transport}
			default:
				return fmt.Errorf("unexpected %s request to %s", message.Request.Method, endpoint)
			}
			return nil
		})
		daemon.mu.Lock()
		daemon.open++
		daemon.mu.Unlock()
		return &countedRelayTransport{
			scriptedAppwireTransport: transport,
			onClose: func() {
				daemon.mu.Lock()
				daemon.open--
				daemon.mu.Unlock()
			},
		}, nil
	}
	return source, daemon
}

type countedRelayTransport struct {
	*scriptedAppwireTransport
	once    sync.Once
	onClose func()
}

func (t *countedRelayTransport) Close() error {
	err := t.scriptedAppwireTransport.Close()
	t.once.Do(t.onClose)
	return err
}

func relayEntry(threadID string) rendezvous.Entry {
	return rendezvous.Entry{
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://" + threadID,
		SourceID:  "local",
		ThreadID:  threadID,
		SessionID: threadID,
	}
}

func readRelayAsync(ctx context.Context, lease RelaySessionLease, params appwire.ThreadReadParams) <-chan relayReadOutcome {
	result := make(chan relayReadOutcome, 1)
	go func() {
		read, err := lease.Read(ctx, params)
		result <- relayReadOutcome{result: read, err: err}
	}()
	return result
}

type relayReadOutcome struct {
	result RelayReadResult
	err    error
}

func relaySnapshot(threadID, text string) appwire.ThreadReadResponse {
	return appwire.ThreadReadResponse{Thread: appwire.Thread{
		ID:      threadID,
		Source:  "local",
		Serf:    appwire.SerfThread{Ref: "local:" + threadID},
		Preview: text,
	}}
}

// relaySessionFor reaches the single live actor a test source owns. Frames can
// then be handed to its ordered observer directly, which is the only way to
// know a notification has actually landed in the open capture: the scripted
// transport's receive channel is buffered, so writing to it proves nothing
// about when -- or whether -- the actor consumed the frame.
func relaySessionFor(t *testing.T, source *LocalDaemonSource) *relaySession {
	t.Helper()
	source.relayMu.Lock()
	defer source.relayMu.Unlock()
	for _, session := range source.relaySessions {
		return session
	}
	t.Fatal("no relay session")
	return nil
}

// observeRelayFrame delivers a notification through the actor's ordered frame
// handler and returns only once it has been accepted, so the caller can order
// what follows against it.
func observeRelayFrame(t *testing.T, session *relaySession, notification appwire.Notification) {
	t.Helper()
	session.mu.Lock()
	epoch := session.epoch
	session.mu.Unlock()
	session.observe(epoch, appwire.Message{Notification: &notification}, nil)
}

func relayDelta(threadID, delta string) appwire.Notification {
	return *appwire.NotificationMessage(appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: threadID,
		Ref:      "local:" + threadID,
		Delta:    delta,
	}).Notification
}

func TestRelaySessionSnapshotCutFlushesPreCutAndHoldsPostCut(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	lease, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatalf("AcquireRelaySession: %v", err)
	}
	defer lease.Close()
	listenCtx, stopListening := context.WithCancel(t.Context())
	defer stopListening()
	deliveries, err := lease.Listen(listenCtx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	read := readRelayAsync(context.Background(), lease, params)
	call := <-daemon.reads
	call.transport.recv <- appwire.Message{Notification: notificationPointer(relayDelta("thread-1", "before"))}
	call.transport.recv <- appwire.ResponseMessage(call.request.ID, relaySnapshot("thread-1", "snapshot includes before"))
	call.transport.recv <- appwire.Message{Notification: notificationPointer(relayDelta("thread-1", "after"))}

	before := <-deliveries
	if got := decodeRelayDelta(t, before.Notification); got != "before" {
		t.Fatalf("pre-cut delivery = %q, want before", got)
	}
	select {
	case outcome := <-read:
		t.Fatalf("Read returned before existing-reader acknowledgment: %+v", outcome)
	default:
	}
	before.Acknowledge()

	outcome := <-read
	if outcome.err != nil {
		t.Fatalf("Read: %v", outcome.err)
	}
	if outcome.result.Response.Thread.Preview != "snapshot includes before" {
		t.Fatalf("snapshot = %+v", outcome.result.Response.Thread)
	}
	select {
	case delivery := <-deliveries:
		t.Fatalf("post-cut delivery escaped before handoff commit: %+v", delivery.Notification)
	default:
	}
	if !outcome.result.Handoff.Commit() {
		t.Fatal("handoff commit lost")
	}
	after := <-deliveries
	if got := decodeRelayDelta(t, after.Notification); got != "after" {
		t.Fatalf("post-cut delivery = %q, want after", got)
	}
	after.Acknowledge()
}

func TestRelaySessionSnapshotCutWaitsForQueuedPreCaptureNotification(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	leaseValue, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatalf("AcquireRelaySession: %v", err)
	}
	lease := leaseValue.(*relaySessionLease)
	defer lease.Close()
	listenCtx, stopListening := context.WithCancel(t.Context())
	defer stopListening()
	deliveries, err := lease.Listen(listenCtx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	initial := readRelayAsync(t.Context(), lease, params)
	initialCall := <-daemon.reads
	initialCall.transport.recv <- appwire.ResponseMessage(
		initialCall.request.ID,
		relaySnapshot("thread-1", "initial"),
	)
	initialResult := <-initial
	if initialResult.err != nil {
		t.Fatalf("initial Read: %v", initialResult.err)
	}
	if !initialResult.result.Handoff.Commit() {
		t.Fatal("initial handoff commit lost")
	}

	lease.session.mu.Lock()
	epoch := lease.session.connection.epoch
	lease.session.mu.Unlock()
	lease.session.observe(epoch, appwire.Message{
		Notification: notificationPointer(relayDelta("thread-1", "blocker")),
	}, nil)
	blocker := <-deliveries
	defer blocker.Acknowledge()
	if got := decodeRelayDelta(t, blocker.Notification); got != "blocker" {
		t.Fatalf("blocking delivery = %q, want blocker", got)
	}

	// This notification is accepted while no capture exists and queues behind
	// the unacknowledged delivery. The next snapshot already contains it.
	lease.session.observe(epoch, appwire.Message{
		Notification: notificationPointer(relayDelta("thread-1", "included")),
	}, nil)
	read := readRelayAsync(t.Context(), lease, params)
	call := <-daemon.reads

	lease.session.mu.Lock()
	capture := lease.session.capture
	lease.session.mu.Unlock()
	if capture == nil {
		t.Fatal("Read did not install a capture")
	}
	response := appwire.ResponseMessage(
		call.request.ID,
		relaySnapshot("thread-1", "snapshot includes included"),
	)
	// Drive the ordered cut synchronously so the assertion below is about actor
	// queue ownership, not receive-loop scheduling.
	lease.session.observe(epoch, response, nil)
	select {
	case <-capture.flushed:
		t.Fatal("snapshot cut stopped waiting for a notification queued before capture")
	default:
	}
	call.transport.recv <- response

	blocker.Acknowledge()
	included := <-deliveries
	if got := decodeRelayDelta(t, included.Notification); got != "included" {
		t.Fatalf("queued pre-capture delivery = %q, want included", got)
	}
	select {
	case <-capture.flushed:
		t.Fatal("Read flush completed before the queued pre-capture delivery was acknowledged")
	default:
	}
	included.Acknowledge()

	result := <-read
	if result.err != nil {
		t.Fatalf("Read: %v", result.err)
	}
	if result.result.Response.Thread.Preview != "snapshot includes included" {
		t.Fatalf("snapshot = %+v", result.result.Response.Thread)
	}
	if !result.result.Handoff.Commit() {
		t.Fatal("handoff commit lost")
	}
	select {
	case delivery := <-deliveries:
		t.Fatalf("queued pre-capture notification duplicated after Read: %+v", delivery.Notification)
	default:
	}
}

func TestRelaySessionRacingReadsDoNotOverlap(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	lease, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()

	first := readRelayAsync(context.Background(), lease, params)
	firstCall := <-daemon.reads
	second := readRelayAsync(context.Background(), lease, params)
	select {
	case call := <-daemon.reads:
		t.Fatalf("second read overlapped first request: %+v", call.request)
	default:
	}
	firstCall.transport.recv <- appwire.ResponseMessage(firstCall.request.ID, relaySnapshot("thread-1", "first"))
	firstResult := <-first
	if firstResult.err != nil {
		t.Fatal(firstResult.err)
	}
	select {
	case call := <-daemon.reads:
		t.Fatalf("second read started before first handoff resolved: %+v", call.request)
	default:
	}
	if !firstResult.result.Handoff.Commit() {
		t.Fatal("first commit lost")
	}

	secondCall := <-daemon.reads
	secondCall.transport.recv <- appwire.ResponseMessage(secondCall.request.ID, relaySnapshot("thread-1", "second"))
	secondResult := <-second
	if secondResult.err != nil {
		t.Fatal(secondResult.err)
	}
	if !secondResult.result.Handoff.Abort() {
		t.Fatal("second abort lost")
	}
}

func TestRelaySessionCanceledListenerCannotStrandPreCutFlush(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	lease, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	listenCtx, cancelListener := context.WithCancel(context.Background())
	deliveries, err := lease.Listen(listenCtx)
	if err != nil {
		t.Fatal(err)
	}

	read := readRelayAsync(context.Background(), lease, params)
	call := <-daemon.reads
	call.transport.recv <- appwire.Message{Notification: notificationPointer(relayDelta("thread-1", "before"))}
	call.transport.recv <- appwire.ResponseMessage(call.request.ID, relaySnapshot("thread-1", "snapshot"))
	<-deliveries
	cancelListener()

	outcome := <-read
	if outcome.err != nil {
		t.Fatalf("Read remained stranded after listener cancellation: %v", outcome.err)
	}
	outcome.result.Handoff.Abort()
}

func TestRelaySessionCanceledListenerIsSafeAfterPublisherSnapshotsIt(t *testing.T) {
	source, _ := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	leaseValue, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseValue.(*relaySessionLease)
	defer lease.Close()
	listenCtx, cancel := context.WithCancel(context.Background())
	if _, err := lease.Listen(listenCtx); err != nil {
		t.Fatal(err)
	}
	lease.session.mu.Lock()
	var snapshotted *relayListener
	for _, listener := range lease.session.listeners {
		snapshotted = listener
	}
	lease.session.mu.Unlock()
	if snapshotted == nil {
		t.Fatal("listener was not registered")
	}

	cancel()
	<-snapshotted.done
	if lease.session.publishToListener(snapshotted, relayDelta("thread-1", "late")) {
		t.Fatal("delivery succeeded after the snapshotted listener was canceled")
	}
}

func TestRelaySessionCancellationBeforeCutResumesFeedAndFencesLateResponse(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	lease, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	listenCtx, stopListening := context.WithCancel(t.Context())
	defer stopListening()
	deliveries, err := lease.Listen(listenCtx)
	if err != nil {
		t.Fatal(err)
	}

	readCtx, cancelRead := context.WithCancel(context.Background())
	first := readRelayAsync(readCtx, lease, params)
	firstCall := <-daemon.reads
	// The frame is handed to the actor synchronously: it is in the open
	// capture's pre-cut buffer before the cancellation below, which is the
	// whole point. Queueing it on the transport instead would let the
	// cancellation win the race, and the notification would then be published
	// by the ordinary no-capture path rather than requeued by the cancellation.
	observeRelayFrame(t, relaySessionFor(t, source), relayDelta("thread-1", "before cancel"))
	cancelRead()
	firstResult := <-first
	if firstResult.err == nil {
		t.Fatal("canceled pre-cut read succeeded")
	}
	preserved := <-deliveries
	if got := decodeRelayDelta(t, preserved.Notification); got != "before cancel" {
		t.Fatalf("preserved delta = %q", got)
	}
	preserved.Acknowledge()

	second := readRelayAsync(context.Background(), lease, params)
	secondCall := <-daemon.reads
	firstCall.transport.recv <- appwire.ResponseMessage(firstCall.request.ID, relaySnapshot("thread-1", "stale response"))
	secondCall.transport.recv <- appwire.Message{Notification: notificationPointer(relayDelta("thread-1", "second pre-cut"))}
	secondCall.transport.recv <- appwire.ResponseMessage(secondCall.request.ID, relaySnapshot("thread-1", "current response"))
	secondPreCut := <-deliveries
	if got := decodeRelayDelta(t, secondPreCut.Notification); got != "second pre-cut" {
		t.Fatalf("second pre-cut delta = %q", got)
	}
	secondPreCut.Acknowledge()
	secondResult := <-second
	if secondResult.err != nil {
		t.Fatalf("second Read: %v", secondResult.err)
	}
	if secondResult.result.Response.Thread.Preview != "current response" {
		t.Fatalf("second snapshot = %q, want current response", secondResult.result.Response.Thread.Preview)
	}
	secondResult.result.Handoff.Commit()
}

func TestRelaySessionAbortAfterCutReleasesPostCutFeedOnce(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	lease, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	ctx := t.Context()
	deliveries, err := lease.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}

	read := readRelayAsync(context.Background(), lease, params)
	call := <-daemon.reads
	call.transport.recv <- appwire.ResponseMessage(call.request.ID, relaySnapshot("thread-1", "snapshot"))
	call.transport.recv <- appwire.Message{Notification: notificationPointer(relayDelta("thread-1", "after"))}
	result := <-read
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.result.Handoff.Abort() {
		t.Fatal("abort lost")
	}
	after := <-deliveries
	if got := decodeRelayDelta(t, after.Notification); got != "after" {
		t.Fatalf("released delta = %q", got)
	}
	after.Acknowledge()
	if result.result.Handoff.Abort() || result.result.Handoff.Commit() {
		t.Fatal("repeated terminal call released the feed again")
	}
}

func TestRelaySessionDisconnectDuringReadCannotReturnSnapshot(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	lease, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()

	read := readRelayAsync(context.Background(), lease, params)
	call := <-daemon.reads
	if err := call.transport.Close(); err != nil {
		t.Fatal(err)
	}
	outcome := <-read
	if outcome.err == nil {
		t.Fatalf("Read returned a snapshot after its continuation disconnected: %+v", outcome.result.Response)
	}

	recovered := readRelayAsync(context.Background(), lease, params)
	recoveryCall := <-daemon.reads
	daemon.mu.Lock()
	openDuringRecovery := daemon.open
	daemon.mu.Unlock()
	if openDuringRecovery != 1 {
		t.Fatalf("open connections during recovery = %d, want one canonical stream", openDuringRecovery)
	}
	recoveryCall.transport.recv <- appwire.ResponseMessage(recoveryCall.request.ID, relaySnapshot("thread-1", "recovered"))
	recoveryResult := <-recovered
	if recoveryResult.err != nil {
		t.Fatalf("recovery Read: %v", recoveryResult.err)
	}
	if !recoveryResult.result.Handoff.Commit() {
		t.Fatal("recovery handoff did not commit")
	}
	if got := daemon.dials.Load(); got != 2 {
		t.Fatalf("dial count = %d, want one replacement canonical connection", got)
	}
}

func TestRelaySessionEOFBeforeConnectionInstallForcesNextReadToRedial(t *testing.T) {
	var dials atomic.Int32
	liveReads := make(chan relayReadCall, 1)
	deadClientReused := make(chan struct{}, 1)

	session := newRelaySession(
		func(ctx context.Context, epoch uint64, observe func(uint64, appwire.Message, error)) (*appwire.Client, appwire.Transport, error) {
			dial := dials.Add(1)
			eofObserved := make(chan struct{})
			var eofOnce sync.Once
			var deadReadAttempts atomic.Int32
			var transport *scriptedAppwireTransport
			transport = newScriptedAppwireTransport(func(_ context.Context, message appwire.Message) error {
				if message.Request != nil {
					switch message.Request.Method {
					case appwire.MethodInitialize:
						transport.recv <- appwire.ResponseMessage(message.Request.ID, appwire.InitializeResponse{
							ProtocolVersion: appwire.ProtocolVersion,
						})
						return nil
					case appwire.MethodThreadRead:
						if dial == 1 {
							if deadReadAttempts.Add(1) == 2 {
								deadClientReused <- struct{}{}
							}
							return io.EOF
						}
						liveReads <- relayReadCall{request: *message.Request, transport: transport}
						return nil
					default:
						return fmt.Errorf("unexpected request method %q", message.Request.Method)
					}
				}
				if message.Notification != nil && message.Notification.Method == appwire.MethodInitialized && dial == 1 {
					if err := transport.Close(); err != nil {
						return err
					}
					<-eofObserved
				}
				return nil
			})
			client := appwire.NewClient(transport)
			client.SetOrderedFrameHandler(func(message appwire.Message, err error) {
				observe(epoch, message, err)
				if err != nil {
					eofOnce.Do(func() { close(eofObserved) })
				}
			})
			client.Start(ctx)
			if _, err := client.Initialize(ctx, appwire.InitializeParams{
				ClientInfo: appwire.ClientInfo{Name: "serf-hub"},
			}); err != nil {
				return nil, nil, err
			}
			return client, transport, nil
		},
		nil,
	)
	lease := session.acquire()
	if lease == nil {
		t.Fatal("relay session did not issue a lease")
	}
	defer lease.Close()
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}

	if _, err := lease.Read(t.Context(), params); err == nil {
		t.Fatal("Read on connection that reached EOF before installation succeeded")
	}

	retried := readRelayAsync(t.Context(), lease, params)
	select {
	case <-deadClientReused:
		t.Fatalf("retried Read reused the dead client; dials = %d, want 2", dials.Load())
	case call := <-liveReads:
		call.transport.recv <- appwire.ResponseMessage(
			call.request.ID,
			relaySnapshot("thread-1", "redialed"),
		)
	}
	result := <-retried
	if result.err != nil {
		t.Fatalf("retried Read: %v", result.err)
	}
	if !result.result.Handoff.Commit() {
		t.Fatal("retried Read handoff commit lost")
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("dial count = %d, want 2", got)
	}
}

func TestRelaySessionCommitAbortRaceHasOneWinner(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	lease, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	read := readRelayAsync(context.Background(), lease, params)
	call := <-daemon.reads
	call.transport.recv <- appwire.ResponseMessage(call.request.ID, relaySnapshot("thread-1", "snapshot"))
	result := <-read
	if result.err != nil {
		t.Fatal(result.err)
	}

	start := make(chan struct{})
	winners := make(chan bool, 2)
	go func() {
		<-start
		winners <- result.result.Handoff.Commit()
	}()
	go func() {
		<-start
		winners <- result.result.Handoff.Abort()
	}()
	close(start)
	winnerCount := 0
	for range 2 {
		if <-winners {
			winnerCount++
		}
	}
	if winnerCount != 1 {
		t.Fatalf("terminal winner count = %d, want 1", winnerCount)
	}
	if result.result.Handoff.Commit() || result.result.Handoff.Abort() {
		t.Fatal("repeated terminal calls reported another winner")
	}
}

func TestRelaySessionPreparedHandoffPinsEpochUntilResponseOutcome(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	leaseValue, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseValue.(*relaySessionLease)
	defer lease.Close()

	read := readRelayAsync(context.Background(), lease, params)
	call := <-daemon.reads
	call.transport.recv <- appwire.ResponseMessage(call.request.ID, relaySnapshot("thread-1", "snapshot"))
	result := <-read
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.result.Handoff.Prepare() {
		t.Fatal("current handoff could not pin its live continuation")
	}
	lease.session.mu.Lock()
	epoch := lease.session.connection.epoch
	lease.session.mu.Unlock()
	lease.session.disconnect(epoch)
	if !result.result.Handoff.Commit() {
		t.Fatal("disconnect invalidated a prepared handoff before its response outcome")
	}
	lease.session.mu.Lock()
	if lease.session.connection != nil {
		t.Fatal("committed handoff did not apply its deferred disconnect")
	}
	lease.session.mu.Unlock()
}

func TestRelaySessionPreparedHandoffAbortAppliesDeferredDisconnectAndRecoversListener(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	leaseValue, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseValue.(*relaySessionLease)
	defer lease.Close()
	listenCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	deliveries, err := lease.Listen(listenCtx)
	if err != nil {
		t.Fatal(err)
	}

	read := readRelayAsync(context.Background(), lease, params)
	call := <-daemon.reads
	call.transport.recv <- appwire.ResponseMessage(call.request.ID, relaySnapshot("thread-1", "snapshot"))
	result := <-read
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.result.Handoff.Prepare() {
		t.Fatal("current handoff could not pin its live continuation")
	}
	lease.session.mu.Lock()
	epoch := lease.session.connection.epoch
	lease.session.mu.Unlock()
	lease.session.disconnect(epoch)
	if !result.result.Handoff.Abort() {
		t.Fatal("disconnect invalidated a prepared abort before its response outcome")
	}

	recoveryCall := <-daemon.reads
	recoveryCall.transport.recv <- appwire.ResponseMessage(
		recoveryCall.request.ID,
		relaySnapshot("thread-1", "recovered"),
	)
	resync := <-deliveries
	if resync.Notification.Method != appwire.NotifySerfThreadResync {
		t.Fatalf("recovery delivery method = %q, want %q", resync.Notification.Method, appwire.NotifySerfThreadResync)
	}
	resync.Acknowledge()
}

func TestRelaySessionHandoffCannotPrepareAfterEpochDisconnect(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	leaseValue, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseValue.(*relaySessionLease)
	defer lease.Close()

	read := readRelayAsync(context.Background(), lease, params)
	call := <-daemon.reads
	call.transport.recv <- appwire.ResponseMessage(call.request.ID, relaySnapshot("thread-1", "snapshot"))
	result := <-read
	if result.err != nil {
		t.Fatal(result.err)
	}
	lease.session.mu.Lock()
	epoch := lease.session.connection.epoch
	lease.session.mu.Unlock()
	lease.session.disconnect(epoch)

	if result.result.Handoff.Prepare() {
		t.Fatal("stale handoff pinned a disconnected epoch")
	}
	if result.result.Handoff.Commit() || result.result.Handoff.Abort() {
		t.Fatal("stale handoff reported a terminal winner after disconnect")
	}
}

func TestRelaySessionStaleEpochNotificationCannotPublish(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	leaseValue, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseValue.(*relaySessionLease)
	defer lease.Close()
	ctx := t.Context()
	deliveries, err := lease.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	read := readRelayAsync(context.Background(), lease, params)
	call := <-daemon.reads
	call.transport.recv <- appwire.ResponseMessage(call.request.ID, relaySnapshot("thread-1", "snapshot"))
	result := <-read
	result.result.Handoff.Commit()

	lease.session.mu.Lock()
	staleEpoch := lease.session.connection.epoch
	lease.session.mu.Unlock()
	lease.session.disconnect(staleEpoch)
	lease.session.observe(staleEpoch, appwire.Message{Notification: notificationPointer(relayDelta("thread-1", "stale"))}, nil)
	select {
	case delivery := <-deliveries:
		t.Fatalf("stale epoch published %+v", delivery.Notification)
	default:
	}
}

func TestRelaySessionIdleClosesCanonicalConnectionAfterLastOwner(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	lease, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	read := readRelayAsync(context.Background(), lease, params)
	call := <-daemon.reads
	call.transport.recv <- appwire.ResponseMessage(call.request.ID, relaySnapshot("thread-1", "snapshot"))
	result := <-read
	lease.Close()
	daemon.mu.Lock()
	openBeforeTerminal := daemon.open
	daemon.mu.Unlock()
	if openBeforeTerminal != 1 {
		t.Fatalf("open connections before handoff terminal = %d, want 1", openBeforeTerminal)
	}
	result.result.Handoff.Abort()
	daemon.mu.Lock()
	openAfterTerminal := daemon.open
	daemon.mu.Unlock()
	if openAfterTerminal != 0 {
		t.Fatalf("open connections after last owner = %d, want 0", openAfterTerminal)
	}
}

func TestRelaySessionPreparedHandoffDefersIdleUntilResponseOutcome(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	lease, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	read := readRelayAsync(context.Background(), lease, params)
	call := <-daemon.reads
	call.transport.recv <- appwire.ResponseMessage(call.request.ID, relaySnapshot("thread-1", "snapshot"))
	result := <-read
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.result.Handoff.Prepare() {
		t.Fatal("current handoff could not pin its live continuation")
	}

	lease.Close()
	daemon.mu.Lock()
	openBeforeTerminal := daemon.open
	daemon.mu.Unlock()
	if openBeforeTerminal != 1 {
		t.Fatalf("open connections before prepared handoff terminal = %d, want 1", openBeforeTerminal)
	}
	if !result.result.Handoff.Abort() {
		t.Fatal("prepared abort lost")
	}
	daemon.mu.Lock()
	openAfterTerminal := daemon.open
	daemon.mu.Unlock()
	if openAfterTerminal != 0 {
		t.Fatalf("open connections after prepared handoff terminal = %d, want 0", openAfterTerminal)
	}
}

func TestRelaySessionAcquireReplacesClosedActorBeforeIdleMapRemoval(t *testing.T) {
	source, _ := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	firstValue, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	first := firstValue.(*relaySessionLease)
	idleEntered := make(chan struct{})
	releaseIdle := make(chan struct{})
	first.session.onIdle = func(*relaySession) {
		close(idleEntered)
		<-releaseIdle
	}
	closed := make(chan struct{})
	go func() {
		first.Close()
		close(closed)
	}()
	<-idleEntered

	secondValue, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatalf("AcquireRelaySession while closed actor awaited map removal: %v", err)
	}
	if secondValue == nil {
		t.Fatal("AcquireRelaySession returned a nil lease for a closed mapped actor")
	}
	second := secondValue.(*relaySessionLease)
	if second.session == first.session {
		t.Fatal("AcquireRelaySession reused the closed actor")
	}
	second.Close()
	close(releaseIdle)
	<-closed
}

func TestRelaySessionUnrelatedActorProgressesWhileSnapshotBlocked(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1"), relayEntry("thread-2")})
	firstParams := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	secondParams := appwire.ThreadReadParams{Ref: "local:thread-2", Subscribe: true}
	firstLease, err := source.AcquireRelaySession(firstParams)
	if err != nil {
		t.Fatal(err)
	}
	defer firstLease.Close()
	secondLease, err := source.AcquireRelaySession(secondParams)
	if err != nil {
		t.Fatal(err)
	}
	defer secondLease.Close()

	first := readRelayAsync(context.Background(), firstLease, firstParams)
	firstCall := <-daemon.reads
	second := readRelayAsync(context.Background(), secondLease, secondParams)
	secondCall := <-daemon.reads
	if secondCall.request.Params == nil {
		t.Fatal("unrelated actor did not issue its snapshot request")
	}
	secondCall.transport.recv <- appwire.ResponseMessage(secondCall.request.ID, relaySnapshot("thread-2", "second"))
	secondResult := <-second
	if secondResult.err != nil {
		t.Fatal(secondResult.err)
	}
	secondResult.result.Handoff.Commit()

	firstCall.transport.recv <- appwire.ResponseMessage(firstCall.request.ID, relaySnapshot("thread-1", "first"))
	firstResult := <-first
	if firstResult.err != nil {
		t.Fatal(firstResult.err)
	}
	firstResult.result.Handoff.Commit()
}

func TestRelaySessionPreparedHandoffDoesNotBlockUnrelatedActor(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1"), relayEntry("thread-2")})
	firstParams := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	secondParams := appwire.ThreadReadParams{Ref: "local:thread-2", Subscribe: true}
	firstLease, err := source.AcquireRelaySession(firstParams)
	if err != nil {
		t.Fatal(err)
	}
	defer firstLease.Close()
	secondLease, err := source.AcquireRelaySession(secondParams)
	if err != nil {
		t.Fatal(err)
	}
	defer secondLease.Close()

	first := readRelayAsync(context.Background(), firstLease, firstParams)
	firstCall := <-daemon.reads
	firstCall.transport.recv <- appwire.ResponseMessage(firstCall.request.ID, relaySnapshot("thread-1", "first"))
	firstResult := <-first
	if firstResult.err != nil {
		t.Fatal(firstResult.err)
	}
	if !firstResult.result.Handoff.Prepare() {
		t.Fatal("first actor could not prepare its handoff")
	}

	second := readRelayAsync(context.Background(), secondLease, secondParams)
	secondCall := <-daemon.reads
	secondCall.transport.recv <- appwire.ResponseMessage(secondCall.request.ID, relaySnapshot("thread-2", "second"))
	secondResult := <-second
	if secondResult.err != nil {
		t.Fatal(secondResult.err)
	}
	if !secondResult.result.Handoff.Commit() {
		t.Fatal("unrelated actor did not complete while first actor handoff remained prepared")
	}
	if !firstResult.result.Handoff.Abort() {
		t.Fatal("first actor could not resolve its prepared handoff")
	}
}

func TestRelaySessionCanonicalFeedDoesNotOverflowUnusedClientNotificationBuffer(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	lease, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	listenCtx := t.Context()
	deliveries, err := lease.Listen(listenCtx)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan struct{})
	go func() {
		for range 4097 {
			delivery := <-deliveries
			delivery.Acknowledge()
		}
		close(received)
	}()

	initial := readRelayAsync(context.Background(), lease, params)
	initialCall := <-daemon.reads
	initialCall.transport.recv <- appwire.ResponseMessage(initialCall.request.ID, relaySnapshot("thread-1", "initial"))
	initialResult := <-initial
	if initialResult.err != nil {
		t.Fatal(initialResult.err)
	}
	initialResult.result.Handoff.Commit()

	for range 4097 {
		initialCall.transport.recv <- appwire.Message{Notification: notificationPointer(relayDelta("thread-1", "x"))}
	}
	<-received

	stillLive := readRelayAsync(context.Background(), lease, params)
	liveCall := <-daemon.reads
	liveCall.transport.recv <- appwire.ResponseMessage(liveCall.request.ID, relaySnapshot("thread-1", "still live"))
	liveResult := <-stillLive
	if liveResult.err != nil {
		t.Fatalf("canonical connection overflowed an unused client notification buffer: %v", liveResult.err)
	}
	liveResult.result.Handoff.Commit()
	if got := daemon.dials.Load(); got != 1 {
		t.Fatalf("dial count = %d, want the original canonical connection", got)
	}
}

func TestRelaySessionRecoversCanonicalFeedAndEmitsResyncWithoutAnotherRead(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	lease, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	listenCtx := t.Context()
	deliveries, err := lease.Listen(listenCtx)
	if err != nil {
		t.Fatal(err)
	}

	initial := readRelayAsync(context.Background(), lease, params)
	initialCall := <-daemon.reads
	initialCall.transport.recv <- appwire.ResponseMessage(initialCall.request.ID, relaySnapshot("thread-1", "initial"))
	initialResult := <-initial
	if initialResult.err != nil {
		t.Fatal(initialResult.err)
	}
	initialResult.result.Handoff.Commit()

	if err := initialCall.transport.Close(); err != nil {
		t.Fatal(err)
	}
	recoveryCall := <-daemon.reads
	recoveryCall.transport.recv <- appwire.ResponseMessage(recoveryCall.request.ID, relaySnapshot("thread-1", "recovered"))

	resync := <-deliveries
	if resync.Notification.Method != appwire.NotifySerfThreadResync {
		t.Fatalf("recovery delivery method = %q, want %q", resync.Notification.Method, appwire.NotifySerfThreadResync)
	}
	resync.Acknowledge()
	recoveryCall.transport.recv <- appwire.Message{Notification: notificationPointer(relayDelta("thread-1", "live again"))}
	live := <-deliveries
	if got := decodeRelayDelta(t, live.Notification); got != "live again" {
		t.Fatalf("recovered live delta = %q", got)
	}
	live.Acknowledge()
	if got := daemon.dials.Load(); got != 2 {
		t.Fatalf("dial count = %d, want exactly one replacement connection", got)
	}
}

func TestRelaySessionRecoveryDisconnectBeforeHandoffResolutionStartsSuccessor(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	leaseValue, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseValue.(*relaySessionLease)
	defer lease.Close()
	listenCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	deliveries, err := lease.Listen(listenCtx)
	if err != nil {
		t.Fatal(err)
	}

	initial := readRelayAsync(context.Background(), lease, params)
	initialCall := <-daemon.reads
	initialCall.transport.recv <- appwire.ResponseMessage(initialCall.request.ID, relaySnapshot("thread-1", "initial"))
	initialResult := <-initial
	if initialResult.err != nil {
		t.Fatal(initialResult.err)
	}
	if !initialResult.result.Handoff.Commit() {
		t.Fatal("initial handoff did not commit")
	}

	if err := initialCall.transport.Close(); err != nil {
		t.Fatal(err)
	}
	firstRecovery := <-daemon.reads
	firstRecovery.transport.recv <- appwire.ResponseMessage(
		firstRecovery.request.ID,
		relaySnapshot("thread-1", "first recovery"),
	)
	firstResync := <-deliveries
	if firstResync.Notification.Method != appwire.NotifySerfThreadResync {
		t.Fatalf("first recovery delivery method = %q, want %q", firstResync.Notification.Method, appwire.NotifySerfThreadResync)
	}

	lease.session.mu.Lock()
	firstRecoveryEpoch := lease.session.connection.epoch
	lease.session.mu.Unlock()
	lease.session.disconnect(firstRecoveryEpoch)
	firstResync.Acknowledge()

	successor := <-daemon.reads
	successor.transport.recv <- appwire.ResponseMessage(
		successor.request.ID,
		relaySnapshot("thread-1", "successor recovery"),
	)
	successorResync := <-deliveries
	if successorResync.Notification.Method != appwire.NotifySerfThreadResync {
		t.Fatalf("successor recovery delivery method = %q, want %q", successorResync.Notification.Method, appwire.NotifySerfThreadResync)
	}
	successorResync.Acknowledge()
	successor.transport.recv <- appwire.Message{
		Notification: notificationPointer(relayDelta("thread-1", "live after successor")),
	}
	live := <-deliveries
	if got := decodeRelayDelta(t, live.Notification); got != "live after successor" {
		t.Fatalf("successor live delta = %q", got)
	}
	live.Acknowledge()
}

func notificationPointer(notification appwire.Notification) *appwire.Notification {
	return &notification
}

func decodeRelayDelta(t *testing.T, notification appwire.Notification) string {
	t.Helper()
	var params appwire.AgentMessageDeltaParams
	if err := json.Unmarshal(notification.Params, &params); err != nil {
		t.Fatalf("decode delta: %v", err)
	}
	return params.Delta
}

// TestRelaySessionCommandReadResyncsListenersOnReplacementConnection (kata
// 8nyk) pins the resync to the RECONNECT rather than to the goroutine that
// usually drives it.
//
// A daemon that exits and is relaunched is a new turn-id generation. Live
// "turn_%d" ids and the transcript's entry-index "turn_%d" ids are one
// namespace with two counters, and only the live one can run ahead: a
// no-active-turn announcement between two real turns mints a turn id that
// never becomes a transcript entry (preTurnAnnouncementTurnID, kata 9ekv), and
// a released turn reservation burns one too. The replacement daemon seeds its
// projector from the transcript (PrepareAppIdentity -> SeedPersistedTurns), so
// its first live turn can carry an id the DEAD generation already published.
// A browser still holding the dead generation's model then takes turn/started
// for a turn it already has, which is the turn-id-uniqueness invariant the
// reducer logs.
//
// The repair for that is the resync this session already publishes when it
// resumes a feed on a replacement connection: the client re-reads and its
// model is replaced. The defect is WHERE that publication lives. Today only
// recoverCanonicalFeed emits it, and recovery is asleep in backoff for up to
// five seconds while a user's send relaunches the daemon and drives a
// command-path read through the very same reconnect -- so the send that
// resumes the feed is exactly the one that skips the resync.
//
// The listener is attached AFTER the drop here so recoverCanonicalFeed never
// starts (it is gated on listeners existing at disconnect). In the live hub
// the fanout listener is attached throughout and recovery merely loses the
// race; removing the goroutine from the test removes the race instead of
// depending on which side of it wins.
func TestRelaySessionCommandReadResyncsListenersOnReplacementConnection(t *testing.T) {
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-1")})
	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	lease, err := source.AcquireRelaySession(params)
	if err != nil {
		t.Fatalf("AcquireRelaySession: %v", err)
	}
	defer lease.Close()

	// Generation 1: the daemon whose turns the browser is holding.
	read := readRelayAsync(context.Background(), lease, params)
	call := <-daemon.reads
	call.transport.recv <- appwire.ResponseMessage(call.request.ID, relaySnapshot("thread-1", "generation 1"))
	outcome := <-read
	if outcome.err != nil {
		t.Fatalf("first Read: %v", outcome.err)
	}
	if !outcome.result.Handoff.Commit() {
		t.Fatal("first handoff commit lost")
	}

	// The daemon exits.
	session := relaySessionFor(t, source)
	session.mu.Lock()
	epoch := session.connection.epoch
	session.mu.Unlock()
	session.disconnect(epoch)

	// The hub's fanout is delivering to browser subscribers that hydrated from
	// generation 1.
	listenCtx, stopListening := context.WithCancel(t.Context())
	defer stopListening()
	deliveries, err := lease.Listen(listenCtx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// The user's send relaunches the daemon and reads through it. That read is
	// what puts generation 2's frames on the feed.
	replacementRead := readRelayAsync(context.Background(), lease, params)
	replacementCall := <-daemon.reads
	replacementCall.transport.recv <- appwire.Message{
		Notification: notificationPointer(relayDelta("thread-1", "generation 2")),
	}
	replacementCall.transport.recv <- appwire.ResponseMessage(
		replacementCall.request.ID,
		relaySnapshot("thread-1", "generation 2"),
	)

	first := <-deliveries
	if first.Notification.Method != appwire.NotifySerfThreadResync {
		t.Fatalf("first delivery after the replacement connection = %q, want %q -- generation 2's frames reached a listener still holding generation 1's model",
			first.Notification.Method, appwire.NotifySerfThreadResync)
	}
	first.Acknowledge()

	second := <-deliveries
	if got := decodeRelayDelta(t, second.Notification); got != "generation 2" {
		t.Fatalf("delivery after the resync = %q, want the replacement connection's own frame", got)
	}
	second.Acknowledge()

	replacementOutcome := <-replacementRead
	if replacementOutcome.err != nil {
		t.Fatalf("replacement Read: %v", replacementOutcome.err)
	}
	if !replacementOutcome.result.Handoff.Commit() {
		t.Fatal("replacement handoff commit lost")
	}
}
