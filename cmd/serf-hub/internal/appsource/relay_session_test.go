package appsource

import (
	"context"
	"encoding/json"
	"fmt"
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
	listenCtx, stopListening := context.WithCancel(context.Background())
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
	listenCtx, stopListening := context.WithCancel(context.Background())
	defer stopListening()
	deliveries, err := lease.Listen(listenCtx)
	if err != nil {
		t.Fatal(err)
	}

	readCtx, cancelRead := context.WithCancel(context.Background())
	first := readRelayAsync(readCtx, lease, params)
	firstCall := <-daemon.reads
	firstCall.transport.recv <- appwire.Message{Notification: notificationPointer(relayDelta("thread-1", "before cancel"))}
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	for i := 0; i < 2; i++ {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	listenCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deliveries, err := lease.Listen(listenCtx)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan struct{})
	go func() {
		for i := 0; i < 4097; i++ {
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

	for i := 0; i < 4097; i++ {
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
	listenCtx, cancel := context.WithCancel(context.Background())
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
