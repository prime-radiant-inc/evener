package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime/pprof"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

// This exercises the source's real response-cut acknowledgement barrier. A
// scripted lease returning a response independently of delivery acknowledgements
// cannot reproduce the circular wait between a read and its unbound child event.
func TestHubRelayRetiredChildDoesNotBlockRootRead(t *testing.T) {
	testHubRelayRetiredChildReadProgress(t, false, false, 1)
}

func TestHubRelayRetiredChildReadWithBackgroundConsumer(t *testing.T) {
	testHubRelayRetiredChildReadProgress(t, false, true, 1)
}

func TestHubRelayQueuedReadDoesNotHoldPreCutAcknowledgement(t *testing.T) {
	testHubRelayRetiredChildReadProgress(t, true, false, 1)
}

func TestHubRelayPendingTargetBurstMakesProgress(t *testing.T) {
	testHubRelayRetiredChildReadProgress(t, true, false, hubRelayPendingDeliveryLimit+1)
}

func TestHubRelayPendingTargetBurstAtCapacity(t *testing.T) {
	// Not parallel: installs package-wide relay seams.
	const pendingCapacity = 64
	var maxOccupancy, currentOccupancy, burstAccepted atomic.Int64
	occupancyViolation := make(chan int, 1)
	backpressure := make(chan struct{})
	backpressureReached := sync.OnceFunc(func() { close(backpressure) })
	publicationEntered := make(chan struct{})
	publicationReleased := make(chan struct{})
	releasePublication := sync.OnceFunc(func() { close(publicationReleased) })
	var publicationArmed atomic.Bool
	var readStarted bool
	readFinished := make(chan struct{})

	pendingObservation := func(n int) {
		currentOccupancy.Store(int64(n))
		for {
			seen := maxOccupancy.Load()
			if int64(n) <= seen || maxOccupancy.CompareAndSwap(seen, int64(n)) {
				break
			}
		}
		if n > pendingCapacity {
			select {
			case occupancyViolation <- n:
			default:
			}
		}
	}
	backpressureObservation := func() {
		backpressureReached()
	}
	publicationObservation := func() {
		if publicationArmed.CompareAndSwap(true, false) {
			close(publicationEntered)
			<-publicationReleased
		}
	}
	deliveryObservation := func(routingKey string) {
		if routingKey == childRef {
			burstAccepted.Add(1)
		}
	}
	previousPending := observeHubRelayPending.Swap(&pendingObservation)
	previousBackpressure := observeHubRelayPendingBackpressure.Swap(&backpressureObservation)
	previousPublication := observeHubRelayRoutePublication.Swap(&publicationObservation)
	previousDelivery := observeHubRelayDeliveryRead.Swap(&deliveryObservation)
	// Register the restore before the fixture so LIFO runs it after the
	// fixture's stopCanonicalRelay cleanup. Releasing the publication here also
	// unblocks every failure path, and the read wait leaves no in-flight read
	// behind before the observations are dropped.
	t.Cleanup(func() {
		releasePublication()
		if readStarted {
			<-readFinished
		}
		observeHubRelayPending.Store(previousPending)
		observeHubRelayPendingBackpressure.Store(previousBackpressure)
		observeHubRelayRoutePublication.Store(previousPublication)
		observeHubRelayDeliveryRead.Store(previousDelivery)
	})

	f := newHubRelayProgressFixture(t, hubcore.RelayLifecycleHooks{})
	ctx, daemon := f.ctx, f.daemon
	root, _ := openRootWithRetiredChild(t, f, false)

	f.gateArmed.Store(true)
	readDone := make(chan readOutcome, 1)
	readStarted = true
	go func() {
		defer close(readFinished)
		response, err := root.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true})
		readDone <- readOutcome{response, err}
	}()
	awaitActivityRelaySignal(ctx, t, f.gateEntered, "root read did not reach the daemon subscription gate")

	// The source captures notifications received during the in-flight read and
	// publishes them at the response cut, so this burst is ordered behind the
	// held authoritative publication rather than racing it.
	burstAccepted.Store(0)
	for i := range pendingCapacity + 1 {
		daemon.RecordDescendantAppEvent(rootID, events.SessionEvent{
			Kind: events.EventNotesUpdated, SessionID: childID,
			Data: events.NotesUpdatedData{HumanNote: fmt.Sprintf("pending-capacity-%02d", i)},
		})
	}

	publicationArmed.Store(true)
	f.releaseGate()
	awaitActivityRelaySignal(ctx, t, publicationEntered, "authoritative publication did not reach the held barrier")

	select {
	case <-backpressure:
	case n := <-occupancyViolation:
		t.Fatalf("pending occupancy %d exceeded capacity %d before publication resolved routing", n, pendingCapacity)
	case <-ctx.Done():
		var stacks bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&stacks, 1)
		t.Fatalf("pending buffer never reached its %d-delivery bound: max=%d\n%s", pendingCapacity, maxOccupancy.Load(), stacks.String())
	}
	if got := maxOccupancy.Load(); got != pendingCapacity {
		t.Fatalf("max pending occupancy = %d, want exactly %d", got, pendingCapacity)
	}
	if got := burstAccepted.Load(); got != pendingCapacity {
		t.Fatalf("child deliveries accepted before publication release = %d, want exactly %d", got, pendingCapacity)
	}

	releasePublication()
	select {
	case result := <-readDone:
		if result.err != nil || result.response.Thread.ID != rootID {
			t.Fatalf("root read: thread=%s err=%v", result.response.Thread.ID, result.err)
		}
	case <-ctx.Done():
		t.Fatal("root read did not complete after publication release")
	}
	if _, err := root.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true}); err != nil {
		t.Fatalf("subsequent root read: %v", err)
	}

	daemon.RecordAppEvent(events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: rootID, Data: events.NotesUpdatedData{HumanNote: "root-sentinel"}})
	daemon.RecordAppEvent(events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: rootID, Data: events.NotesUpdatedData{HumanNote: "root-fence"}})
	assertHubRelayRootNotes(ctx, t, root)

	if got := burstAccepted.Load(); got < pendingCapacity+1 {
		t.Fatalf("child deliveries accepted after publication release = %d, want at least %d", got, pendingCapacity+1)
	}
	awaitActivityRelayCondition(ctx, t, func() bool { return currentOccupancy.Load() == 0 }, "pending deliveries did not drain after publication")
	select {
	case n := <-occupancyViolation:
		t.Fatalf("pending occupancy %d exceeded capacity %d", n, pendingCapacity)
	default:
	}
}

func openRootWithRetiredChild(t *testing.T, f hubRelayProgressFixture, backgroundConsumer bool) (*appwire.Client, []*appwire.Client) {
	t.Helper()
	ctx := f.ctx
	root, child := f.open(), f.open()
	for _, target := range []struct {
		client *appwire.Client
		ref    string
	}{{root, rootRef}, {child, childRef}} {
		if _, err := target.client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: target.ref, Subscribe: true}); err != nil {
			t.Fatal(err)
		}
	}
	consumers := []*appwire.Client{root}
	if backgroundConsumer {
		background := f.open()
		if _, err := background.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true}); err != nil {
			t.Fatal(err)
		}
		consumers = append(consumers, background)
	}
	if _, err := child.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: childRef}); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-f.idleDeleted:
		if id != rootID || f.relays.relayPublished(childRef) || !f.relays.relayPublished(rootRef) {
			t.Fatalf("unexpected retirement: canonical=%s child=%v root=%v", id, f.relays.relayPublished(childRef), f.relays.relayPublished(rootRef))
		}
	case <-ctx.Done():
		t.Fatal("child route did not retire")
	}
	if got := f.daemon.AppServer().SubscriberCount(childID); got != 1 {
		t.Fatalf("retained upstream child subscriptions = %d, want 1", got)
	}
	return root, consumers
}

func testHubRelayRetiredChildReadProgress(t *testing.T, queuedRead, backgroundConsumer bool, childEvents int) {
	t.Helper()
	f := newHubRelayProgressFixture(t, hubcore.RelayLifecycleHooks{})
	ctx, daemon, relays, open := f.ctx, f.daemon, f.relays, f.open
	gateEntered, releaseGate := f.gateEntered, f.releaseGate
	root, consumers := openRootWithRetiredChild(t, f, backgroundConsumer)

	f.gateArmed.Store(true)
	readDone := make(chan readOutcome, 1)
	go func() {
		response, err := root.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true})
		readDone <- readOutcome{response, err}
	}()
	awaitActivityRelaySignal(ctx, t, gateEntered, "root read did not reach the daemon subscription gate")
	queuedDone := make(chan readOutcome, 1)
	if queuedRead {
		second := open()
		go func() {
			response, err := second.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true})
			queuedDone <- readOutcome{response, err}
		}()
		// No admission hook exists here. Observe ownership, not elapsed time,
		// before releasing the source command ahead of this one.
		awaitActivityRelayCondition(ctx, t, func() bool {
			return relays.relayCommandCount(rootRef) == 2
		}, "second read did not acquire its hub owner")
	}
	// CommitProjection sends the real child notification before the gated read
	// can capture its response. The source publishes it at that response's cut.
	for range childEvents {
		daemon.RecordDescendantAppEvent(rootID, events.SessionEvent{
			Kind: events.EventExecutionStarted, SessionID: childID,
			Data: events.ExecutionStartedData{TurnID: "child-execution"},
		})
	}
	releaseGate()
	select {
	case result := <-readDone:
		if result.err != nil || result.response.Thread.ID != rootID {
			t.Fatalf("root read: thread=%s err=%v", result.response.Thread.ID, result.err)
		}
	case <-time.After(time.Second):
		var stacks bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&stacks, 1)
		t.Fatalf("root read blocked after the response cut: pendingDelivery=%v commands=%d\n%s", f.pendingWait.Load(), relays.relayCommandCount(rootRef), stacks.String())
	}
	if queuedRead {
		select {
		case result := <-queuedDone:
			if result.err != nil || result.response.Thread.ID != rootID {
				t.Fatalf("queued root read: thread=%s err=%v", result.response.Thread.ID, result.err)
			}
		case <-ctx.Done():
			t.Fatal("queued root read did not finish")
		}
	}
	if _, err := root.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true}); err != nil {
		t.Fatalf("subsequent root read: %v", err)
	}

	// A following root note is the stream fence for both duplicate and
	// cross-target leakage checks. These events traverse the real projector.
	daemon.RecordAppEvent(events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: rootID, Data: events.NotesUpdatedData{HumanNote: "root-sentinel"}})
	daemon.RecordAppEvent(events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: rootID, Data: events.NotesUpdatedData{HumanNote: "root-fence"}})
	for _, consumer := range consumers {
		assertHubRelayRootNotes(ctx, t, consumer)
	}
}

func assertHubRelayRootNotes(ctx context.Context, t *testing.T, root *appwire.Client) {
	t.Helper()
	sentinels := 0
	for {
		select {
		case note, ok := <-root.Notifications():
			if !ok {
				t.Fatal("root notification stream closed")
			}
			if note.Method == appwire.NotifyThreadStatusChanged {
				var params appwire.ThreadStatusChangedParams
				if err := json.Unmarshal(note.Params, &params); err != nil {
					t.Fatal(err)
				}
				if params.ThreadID != rootID || params.Ref != rootRef {
					t.Fatalf("foreign status reached root: %+v", params)
				}
			}
			if note.Method != appwire.NotifyEvenerNotesUpdated {
				continue
			}
			var params appwire.NotesUpdatedParams
			if err := json.Unmarshal(note.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.ThreadID != rootID || params.Ref != rootRef {
				t.Fatalf("foreign note reached root: %+v", params)
			}
			if params.HumanNote == "root-sentinel" {
				sentinels++
			}
			if params.HumanNote == "root-fence" {
				if sentinels != 1 {
					t.Fatalf("root sentinel count = %d, want 1", sentinels)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("root live note did not arrive after the reads")
		}
	}
}

const rootID, childID = "read-progress-root", "read-progress-child"
const rootRef, childRef = "local:" + rootID, "local:" + childID

type readOutcome struct {
	response appwire.ThreadReadResponse
	err      error
}

func TestHubRelayRoutePublicationUnblocksPendingDelivery(t *testing.T) {
	oldEntered, oldRelease := make(chan struct{}), make(chan struct{})
	var oldArmed atomic.Bool
	releaseOld := sync.OnceFunc(func() { close(oldRelease) })
	f := newHubRelayProgressFixture(t, hubcore.RelayLifecycleHooks{
		AfterCanonicalPublishEntry: func(key string, _ appwire.Notification) {
			if key == childRef && oldArmed.CompareAndSwap(true, false) {
				close(oldEntered)
				<-oldRelease
			}
		},
	})
	t.Cleanup(releaseOld)
	existing := f.open()
	if _, err := existing.ThreadRead(f.ctx, appwire.ThreadReadParams{Ref: childRef, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	oldArmed.Store(true)
	f.daemon.RecordDescendantAppEvent(rootID, events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: childID, Data: events.NotesUpdatedData{HumanNote: "old-owner"}})
	awaitActivityRelaySignal(f.ctx, t, oldEntered, "old publication did not enter")

	const replacementID = "read-progress-replacement"
	replacement := daemonserver.NewServer(daemonserver.ServerConfig{})
	t.Cleanup(replacement.Close)
	identity, err := daemonserver.PrepareAppIdentity("local", replacementID, "")
	if err != nil {
		t.Fatal(err)
	}
	replacement.ReplaceAppIdentity(identity.WithBootGeneration("1"), nil)
	seedDaemonStatus(replacement, daemonserver.StatusInfo{SessionID: replacementID, State: "idle"})
	replacement.RecordDescendantAppEvent(replacementID, events.SessionEvent{Kind: events.EventSessionStart, SessionID: childID, Data: events.SessionStartData{}})
	gateArmed, gateEntered, releaseGate := installRelayProgressGate(f.ctx, replacement)
	replacementHTTP := httptest.NewServer(http.HandlerFunc(replacement.AppServer().ServeWebSocket))
	t.Cleanup(replacementHTTP.Close)
	t.Cleanup(func() { releaseOld(); releaseGate() })
	f.remapChild(rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: replacementHTTP.URL, SourceID: "local", ThreadID: replacementID, SessionID: replacementID, WorkspaceRef: "local:" + replacementID})
	canonical := appwire.Ref{SourceID: "local", ThreadID: replacementID}
	t.Cleanup(func() { f.relays.stopCanonicalRelay(canonical) })
	// Retain a real upstream subscription before the hub learns the replacement
	// route. This is the same source-owned subscription lifetime as a retired
	// downstream route, without a scripted lease or fabricated wire frame.
	lease, err := f.source.AcquireRelaySession(canonical)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Close)
	primed, err := lease.Read(f.ctx, appwire.ThreadReadParams{Ref: childRef, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	if !primed.Handoff.Prepare() || !primed.Handoff.Commit() {
		t.Fatal("upstream subscription handoff failed")
	}
	joining := f.open()
	gateArmed.Store(true)
	// Publication waits once for the displaced route, and the first child
	// notification parks once behind unresolved routing. Later same-target
	// notifications join that pending queue without another wait callback.
	waits := make(chan struct{}, 8)
	observeHubRelayWait = func() { waits <- struct{}{} }
	type outcome struct {
		response appwire.ThreadReadResponse
		err      error
	}
	joined := make(chan outcome, 1)
	go func() {
		response, err := joining.ThreadRead(f.ctx, appwire.ThreadReadParams{Ref: childRef, Subscribe: true})
		joined <- outcome{response, err}
	}()
	awaitActivityRelaySignal(f.ctx, t, gateEntered, "replacement read did not enter")
	for _, note := range []string{"replacement-first", "replacement-second"} {
		replacement.RecordDescendantAppEvent(replacementID, events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: childID, Data: events.NotesUpdatedData{HumanNote: note}})
	}
	releaseGate()
	for i := range 2 {
		select {
		case <-waits:
		case <-time.After(time.Second):
			var stacks bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&stacks, 1)
			t.Fatalf("replacement wait count = %d, want 2\n%s", i, stacks.String())
		}
	}
	releaseOld()
	select {
	case result := <-joined:
		if result.err != nil || result.response.Thread.Evener.HumanNote != "replacement-second" {
			t.Fatalf("replacement snapshot: note=%q err=%v", result.response.Thread.Evener.HumanNote, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("route publication did not wake its retained deliveries")
	}
	for _, want := range []string{"old-owner", "replacement-first", "replacement-second"} {
		if got := nextRelayProgressNote(f.ctx, t, existing); got.Ref != childRef || got.HumanNote != want {
			t.Fatalf("existing consumer got %+v, want %s on %s", got, want, childRef)
		}
	}
	replacement.RecordDescendantAppEvent(replacementID, events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: childID, Data: events.NotesUpdatedData{HumanNote: "replacement-live"}})
	for _, client := range []*appwire.Client{existing, joining} {
		if got := nextRelayProgressNote(f.ctx, t, client); got.Ref != childRef || got.HumanNote != "replacement-live" {
			t.Fatalf("post-handoff consumer got %+v", got)
		}
	}
}

func TestHubRelayReadCancellationAllowsNextRead(t *testing.T) {
	testHubRelayInterruptedRead(t, false)
}

func TestHubRelayReconnectAfterPendingRead(t *testing.T) {
	testHubRelayInterruptedRead(t, true)
}

func testHubRelayInterruptedRead(t *testing.T, disconnect bool) {
	t.Helper()
	f := newHubRelayProgressFixture(t, hubcore.RelayLifecycleHooks{})
	client := f.open()
	if _, err := client.ThreadRead(f.ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	// The hub releases a read's command owner when the response enters the
	// connection's send queue, which can follow the client receiving it. Wait
	// for that release so the count below belongs to the interrupted read.
	awaitActivityRelayCondition(f.ctx, t, func() bool {
		return f.relays.relayCommandCount(rootRef) == 0
	}, "initial read did not release its owner")
	readCtx, cancelRead := context.WithCancel(f.ctx)
	t.Cleanup(cancelRead)
	f.gateArmed.Store(true)
	finished := make(chan error, 1)
	go func() {
		// Calling the actual hub read boundary lets cancellation reach the
		// server-side owner. Canceling an AppWire client call alone only removes
		// that client's response waiter, not the server's dispatched request.
		read, err := f.relays.readThread(readCtx, f.source, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true})
		if read != nil {
			read.finish(false)
		}
		finished <- err
	}()
	awaitActivityRelaySignal(f.ctx, t, f.gateEntered, "read did not acquire the upstream subscription gate")
	if disconnect {
		f.daemon.RecordAppEvent(events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: rootID, Data: events.NotesUpdatedData{HumanNote: "old-epoch"}})
		f.closeUpstream()
	} else {
		cancelRead()
	}
	select {
	case err := <-finished:
		if err == nil || (!disconnect && !errors.Is(err, context.Canceled)) {
			t.Fatalf("interrupted read returned %v", err)
		}
	case <-f.ctx.Done():
		t.Fatal("interrupted read did not release its owner")
	}
	f.releaseGate()
	if got := f.relays.relayCommandCount(rootRef); got != 0 {
		t.Fatalf("interrupted read retained %d command owners", got)
	}
	if _, err := client.ThreadRead(f.ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true}); err != nil {
		t.Fatalf("read after interruption: %v", err)
	}
	for _, value := range []string{"new-epoch-first", "new-epoch-second"} {
		f.daemon.RecordAppEvent(events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: rootID, Data: events.NotesUpdatedData{HumanNote: value}})
	}
	for _, want := range []string{"new-epoch-first", "new-epoch-second"} {
		got := nextRelayProgressNote(f.ctx, t, client)
		if got.Ref != rootRef || got.ThreadID != rootID || got.HumanNote != want {
			t.Fatalf("notification after interruption = %+v, want %s on %s", got, want, rootRef)
		}
	}
}

func nextRelayProgressNote(ctx context.Context, t *testing.T, client *appwire.Client) appwire.NotesUpdatedParams {
	t.Helper()
	for {
		select {
		case note, ok := <-client.Notifications():
			if !ok {
				t.Fatal("notification stream closed")
			}
			if note.Method != appwire.NotifyEvenerNotesUpdated {
				continue
			}
			var params appwire.NotesUpdatedParams
			if err := json.Unmarshal(note.Params, &params); err != nil {
				t.Fatal(err)
			}
			return params
		case <-ctx.Done():
			t.Fatal("ordered live note did not arrive")
		}
	}
}

func installRelayProgressGate(ctx context.Context, daemon *daemonserver.Server) (*atomic.Bool, <-chan struct{}, func()) {
	armed := new(atomic.Bool)
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	daemon.AppServer().SetBeforeSubscriptionGate(func() {
		if armed.CompareAndSwap(true, false) {
			close(entered)
			select {
			case <-released:
			case <-ctx.Done():
			}
		}
	})
	return armed, entered, release
}

type hubRelayProgressFixture struct {
	ctx           context.Context
	daemon        *daemonserver.Server
	source        *appsource.LocalDaemonSource
	relays        *hubRelay
	open          func() *appwire.Client
	idleDeleted   <-chan string
	gateArmed     *atomic.Bool
	gateEntered   <-chan struct{}
	releaseGate   func()
	pendingWait   *atomic.Bool
	closeUpstream func()
	remapChild    func(rendezvous.Entry)
}

func newHubRelayProgressFixture(t *testing.T, hooks hubcore.RelayLifecycleHooks) hubRelayProgressFixture {
	t.Helper()
	// Not parallel: observes the package's relay functions and pending wait.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	var relays *hubRelay
	var pendingWait atomic.Bool
	previousFunctions, previousWait := observeHubRelay, observeHubRelayWait
	observeHubRelay = func(f *hubRelay) { relays = f }
	observeHubRelayWait = func() { pendingWait.Store(true) }
	t.Cleanup(func() {
		observeHubRelay, observeHubRelayWait = previousFunctions, previousWait
	})

	daemon := daemonserver.NewServer(daemonserver.ServerConfig{})
	t.Cleanup(daemon.Close)
	prepared, err := daemonserver.PrepareAppIdentity("local", rootID, "")
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(prepared.WithBootGeneration("1"), nil)
	seedDaemonStatus(daemon, daemonserver.StatusInfo{SessionID: rootID, State: "idle"})
	daemon.RecordDescendantAppEvent(rootID, events.SessionEvent{
		Kind: events.EventSessionStart, SessionID: childID, Data: events.SessionStartData{},
	})

	gateArmed, gateEntered, releaseGate := installRelayProgressGate(ctx, daemon)
	t.Cleanup(releaseGate)
	var connectionsMu sync.Mutex
	var connections []net.Conn
	daemonHTTP := httptest.NewUnstartedServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	daemonHTTP.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateHijacked {
			connectionsMu.Lock()
			connections = append(connections, conn)
			connectionsMu.Unlock()
		}
	}
	daemonHTTP.Start()
	closeUpstream := func() {
		connectionsMu.Lock()
		old := connections
		connections = nil
		connectionsMu.Unlock()
		for _, conn := range old {
			_ = conn.Close()
		}
	}
	t.Cleanup(closeUpstream)
	t.Cleanup(daemonHTTP.Close)
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: daemonHTTP.URL, SourceID: "local", ThreadID: rootID, SessionID: rootID, WorkspaceRef: rootRef}
	var inventoryMu sync.Mutex
	inventory := []appsource.LocalDaemonEntry{
		{Entry: entry, SessionID: rootID},
		{Entry: entry, SessionID: childID, OwnerSessionID: rootID, ReadOnlyAlias: true},
	}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		inventoryMu.Lock()
		defer inventoryMu.Unlock()
		return slices.Clone(inventory)
	}, daemonHTTP.Client())
	configureObservationTargetSessionTools(daemon, source)
	remapChild := func(replacement rendezvous.Entry) {
		inventoryMu.Lock()
		defer inventoryMu.Unlock()
		inventory = []appsource.LocalDaemonEntry{
			{Entry: entry, SessionID: rootID},
			{Entry: replacement, SessionID: replacement.SessionID},
			{Entry: replacement, SessionID: childID, OwnerSessionID: replacement.SessionID, ReadOnlyAlias: true},
		}
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	idleDeleted := make(chan string, 4)
	hooks.AfterIdleDelete = func(id string) { idleDeleted <- id }
	hubServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""),
		RelayHooks: hooks,
	}, sources)
	hubHTTP := httptest.NewServer(http.HandlerFunc(hubServer.ServeWebSocket))
	t.Cleanup(hubHTTP.Close)
	t.Cleanup(func() { relays.stopCanonicalRelay(appwire.Ref{SourceID: "local", ThreadID: rootID}) })
	open := func() *appwire.Client {
		client := dialHubRPC(t, hubHTTP)
		t.Cleanup(func() { _ = client.Close() })
		if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
		return client
	}

	// Release any test-owned barrier before waiting for server shutdown.
	t.Cleanup(func() { releaseGate(); cancel() })
	return hubRelayProgressFixture{
		ctx: ctx, daemon: daemon, source: source, relays: relays, open: open,
		idleDeleted: idleDeleted, gateArmed: gateArmed, gateEntered: gateEntered,
		releaseGate: releaseGate, pendingWait: &pendingWait, closeUpstream: closeUpstream, remapChild: remapChild,
	}
}
