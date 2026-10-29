package appsource

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// The tests here drive the relay session's publication and dispatch machinery
// through the arms its own routing cannot always order deterministically:
// cancellation mid-flush, listeners dying mid-acknowledgement, and the guard
// arms of the handoff and idle paths.

func relayDispatchListener(ctx context.Context, id uint64) *relayListener {
	return &relayListener{
		id:   id,
		ctx:  ctx,
		in:   make(chan RelayDelivery),
		out:  make(chan RelayDelivery),
		done: make(chan struct{}),
	}
}

func (s *relaySession) registerTestListener(l *relayListener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextListener++
	if l.id == 0 {
		l.id = s.nextListener
	}
	s.listeners[l.id] = l
}

func TestRelayLeaseReadWithRoutePublicationNilArms(t *testing.T) {
	t.Parallel()
	params := appwire.ThreadReadParams{Ref: "local:thread"}
	var nilLease *relaySessionLease
	if _, err := nilLease.ReadWithRoutePublication(t.Context(), params, nil); err == nil {
		t.Fatal("nil lease ReadWithRoutePublication should fail")
	}
	emptySession := &relaySessionLease{}
	if _, err := emptySession.ReadWithRoutePublication(t.Context(), params, &RelayRoutePublication{}); err == nil {
		t.Fatal("nil session ReadWithRoutePublication should fail")
	}
}

func TestRelayListenerForwardStopsWhenDoneClosesMidDelivery(t *testing.T) {
	t.Parallel()
	listener := relayDispatchListener(context.Background(), 1)
	forwardDone := make(chan struct{})
	go func() {
		listener.forward()
		close(forwardDone)
	}()
	// The unbuffered send completes only once forward has received the
	// delivery, so forward holds it when done closes. Nobody receives on out
	// until the goroutine is joined, so the done arm is the only ready case
	// of the inner select and the closed-channel read below is deterministic.
	listener.in <- RelayDelivery{}
	listener.close()
	<-forwardDone
	if _, ok := <-listener.out; ok {
		t.Fatal("forward should close out without delivering after done")
	}
}

func TestRelayListenerForwardStopsWhenContextCancelsMidDelivery(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	listener := relayDispatchListener(ctx, 1)
	forwardDone := make(chan struct{})
	go func() {
		listener.forward()
		close(forwardDone)
	}()
	listener.in <- RelayDelivery{}
	cancel()
	// Withhold the out receiver until the goroutine has exited, so the
	// cancellation arm is the only ready case of the inner select.
	<-forwardDone
	if _, ok := <-listener.out; ok {
		t.Fatal("forward should close out without delivering after context cancellation")
	}
}

func TestRelaySessionReadRejectsClosedSessionAfterGate(t *testing.T) {
	t.Parallel()
	s := newRelaySessionForTest()
	// The gate must be passable for the read to observe the closed flag, so
	// hand it the token the production constructor primes.
	s.commandGate <- struct{}{}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	_, err := s.read(context.Background(), appwire.ThreadReadParams{Ref: "local:thread"})
	assertSessionUnavailable(t, err, "read on closed session")

	select {
	case <-s.commandGate:
	default:
		t.Fatal("closed-session read did not release the command gate")
	}
}

func TestRelaySessionRoutePublicationRejectedWhenCaptureIsInvalidated(t *testing.T) {
	t.Parallel()
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-rp")})
	lease, err := source.AcquireRelaySession(appwire.Ref{SourceID: "local", ThreadID: "thread-rp"})
	if err != nil {
		t.Fatalf("AcquireRelaySession: %v", err)
	}
	session := relaySessionFor(t, source)

	params := appwire.ThreadReadParams{Ref: "local:thread-rp", ThreadID: "thread-rp", Subscribe: true}
	published := false
	outcome := make(chan relayReadOutcome, 1)
	go func() {
		result, readErr := lease.ReadWithRoutePublication(context.Background(), params, &RelayRoutePublication{
			// The read's capture must still be the session's current capture
			// when routes publish; replace it, as a superseding disconnect
			// would, so publication is refused and the read fails closed.
			Begin: func() {
				session.mu.Lock()
				session.capture = nil
				session.mu.Unlock()
			},
			Publish: func(context.Context, appwire.Thread) error {
				published = true
				return nil
			},
		})
		outcome <- relayReadOutcome{result: result, err: readErr}
	}()

	call := <-daemon.reads
	call.transport.recv <- appwire.ResponseMessage(call.request.ID, relaySnapshot("thread-rp", "snapshot"))

	got := <-outcome
	assertSessionUnavailable(t, got.err, "route publication with a superseded capture")
	if published {
		t.Fatal("routes were published for a superseded capture")
	}
	select {
	case <-session.commandGate:
	default:
		t.Fatal("refused route publication did not release the command gate")
	}
	session.mu.Lock()
	owners := session.commandOwners
	session.mu.Unlock()
	if owners != 0 {
		t.Fatalf("command owners after refused publication = %d, want 0", owners)
	}
	lease.Close()
}

// flushWaitFixture stages a subscribed read whose pre-cut notification is
// parked inside the publisher's entry hook: the flush acknowledgement cannot
// complete while the hook holds the publisher, so the read's flush wait is
// deterministically open when the test cancels its context.
type flushWait struct {
	session    *relaySession
	deliveries <-chan RelayDelivery
	barrier    *relayPublisherEntryBarrier
	cancelRead context.CancelFunc
	outcome    <-chan relayReadOutcome
}

func flushWaitFixture(t *testing.T) flushWait {
	t.Helper()
	source, daemon := newRelayTestSource(t, []rendezvous.Entry{relayEntry("thread-flush")})
	lease, err := source.AcquireRelaySession(appwire.Ref{SourceID: "local", ThreadID: "thread-flush"})
	if err != nil {
		t.Fatalf("AcquireRelaySession: %v", err)
	}
	t.Cleanup(lease.Close)
	session := relaySessionFor(t, source)
	listenerDeliveries, err := lease.Listen(t.Context())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	barrier := newRelayPublisherEntryBarrier(t, session)

	readCtx, cancel := context.WithCancel(context.Background())
	results := readRelayAsync(readCtx, lease, appwire.ThreadReadParams{
		Ref: "local:thread-flush", ThreadID: "thread-flush", Subscribe: true,
	})

	call := <-daemon.reads
	observeRelayFrame(t, session, relayDelta("thread-flush", "delta-1"))
	call.transport.recv <- appwire.ResponseMessage(call.request.ID, relaySnapshot("thread-flush", "snapshot"))
	<-barrier.entered
	return flushWait{session: session, deliveries: listenerDeliveries, barrier: barrier, cancelRead: cancel, outcome: results}
}

func TestRelaySessionFlushWaitReturnsCallerCancellation(t *testing.T) {
	t.Parallel()
	fx := flushWaitFixture(t)

	fx.cancelRead()
	got := <-fx.outcome
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("flush wait error = %v, want context.Canceled", got.err)
	}

	// Release the parked pre-cut delivery and acknowledge it: the cut had
	// already moved it out of the capture into the parked job, so the
	// cancelled read's cleanup had nothing left to requeue.
	fx.barrier.releasePublisher()
	delivery := <-fx.deliveries
	delivery.Acknowledge()
}

func TestRelaySessionFlushWaitReturnsSessionClosure(t *testing.T) {
	t.Parallel()
	fx := flushWaitFixture(t)

	// Close the session the way production's closure paths do (maybeIdle sets
	// closed under the lock it cancels from). A bare context cancellation
	// leaves closed false — a state production never produces — so once the
	// released publication lets the dying listener drop the pre-cut delivery,
	// the flush barrier can close before the parked read resumes, and a read
	// that selects the flushed arm would pass the rejoin validity check and
	// report success. With the closed flag established first, either select
	// arm refuses the read.
	//
	// The publisher is released right after: the session's epoch revocation
	// needs the publication boundary the entry hook holds, so parking it
	// past the cancellation would wedge the disconnect chain.
	fx.session.mu.Lock()
	fx.session.closed = true
	fx.session.cancel()
	fx.session.mu.Unlock()
	fx.barrier.releasePublisher()

	got := <-fx.outcome
	assertSessionUnavailable(t, got.err, "flush wait ended by session closure")
}

func TestRelaySessionEnsureConnectionStopsWhenCallerCancelsDuringConnect(t *testing.T) {
	t.Parallel()
	readCtx, cancelRead := context.WithCancel(context.Background())
	entered := make(chan struct{})
	var transport *scriptedAppwireTransport
	connect := func(ctx context.Context, _ uint64, _ func(uint64, appwire.Message, error)) (*appwire.Client, appwire.Transport, error) {
		// Signal that ensureConnection has admitted the connect attempt, then
		// wait for the caller's cancellation to propagate through the
		// session's AfterFunc before reporting a successful connect anyway:
		// the caller is gone, so ensureConnection must tear the transport down.
		close(entered)
		<-ctx.Done()
		transport = newScriptedAppwireTransport(nil)
		return appwire.NewClient(transport), transport, nil
	}
	s := newRelaySession(connect, nil)
	defer s.cancel()

	result := make(chan error, 1)
	go func() {
		_, err := s.ensureConnection(readCtx)
		result <- err
	}()
	<-entered
	cancelRead()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("ensureConnection error = %v, want context.Canceled", err)
	}
	if transport == nil {
		t.Fatal("connect was never invoked")
	}
	select {
	case <-transport.closed:
	default:
		t.Fatal("superseded caller cancellation did not close the transport")
	}
}

func TestRelaySessionObserveWithoutCaptureQueuesNotification(t *testing.T) {
	t.Parallel()
	s := newRelaySessionForTest()
	s.mu.Lock()
	s.connection = &relayConnection{epoch: 3}
	s.mu.Unlock()

	notification := appwire.Notification{Method: appwire.NotifyOverlayDelta}
	s.observe(3, appwire.Message{Notification: &notification}, nil)

	s.publishMu.Lock()
	jobs := len(s.publishJobs)
	s.publishMu.Unlock()
	if jobs != 1 {
		t.Fatalf("queued publish jobs = %d, want the notification queued without a capture", jobs)
	}
}

func TestRelaySessionDisconnectKeepsForeignEpochConnection(t *testing.T) {
	t.Parallel()
	s := newRelaySessionForTest()
	s.mu.Lock()
	s.connection = &relayConnection{epoch: 2}
	s.mu.Unlock()

	s.disconnect(1)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connection == nil || s.connection.epoch != 2 {
		t.Fatalf("disconnect with a foreign epoch dropped the connection: %+v", s.connection)
	}
}

func TestRelaySessionDaemonGoneResyncSkipsClosedSession(t *testing.T) {
	t.Parallel()
	s := newRelaySessionForTest()
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	s.publishDaemonGoneResync()

	s.publishMu.Lock()
	jobs := len(s.publishJobs)
	s.publishMu.Unlock()
	if jobs != 0 {
		t.Fatalf("closed session queued %d publish jobs, want none", jobs)
	}
}

func TestRelaySessionRecoveryBacksOffBetweenFailedReads(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	attempted := make(chan struct{}, 4)
	release := make(chan struct{})
	connect := func(context.Context, uint64, func(uint64, appwire.Message, error)) (*appwire.Client, appwire.Transport, error) {
		if attempts.Add(1) == 2 {
			// Park the second dial: while it is held, no later backoff can
			// fire a third attempt and race the test's exact-two assertion.
			select {
			case attempted <- struct{}{}:
			default:
			}
			<-release
			return nil, nil, appwire.SessionUnavailable("dial refused")
		}
		select {
		case attempted <- struct{}{}:
		default:
		}
		return nil, nil, appwire.SessionUnavailable("dial refused")
	}
	s := newRelaySession(connect, nil)
	defer s.cancel()
	lease := s.acquire()
	listener := relayDispatchListener(s.ctx, 0)
	listener.leaseID = lease.id
	s.registerTestListener(listener)
	s.mu.Lock()
	s.readParams = appwire.ThreadReadParams{Ref: "local:thread-recovery"}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.recoverCanonicalFeed()
		close(done)
	}()

	// Each attempt signals as it happens; the second is parked inside the
	// dial until the test releases it, so both receives await real
	// completion and the loop cannot run past the second attempt first.
	<-attempted
	<-attempted
	// Closing the lease removes the recovery loop's only listener
	// synchronously and idle-closes the session, so once the parked dial is
	// released the loop-top check ends recovery instead of dialing again.
	lease.Close()
	close(release)
	<-done

	if got := attempts.Load(); got != 2 {
		t.Fatalf("recovery connect attempts = %d, want 2", got)
	}
}

func TestRelayHandoffGuardArms(t *testing.T) {
	t.Parallel()
	s := newRelaySessionForTest()
	var nilHandoff *relayHandoff
	if nilHandoff.Prepare() {
		t.Fatal("nil handoff Prepare should fail")
	}
	if nilHandoff.Commit() {
		t.Fatal("nil handoff Commit should fail")
	}
	if nilHandoff.Abort() {
		t.Fatal("nil handoff Abort should fail")
	}
	terminal := &relayHandoff{session: s, terminal: true}
	if terminal.Prepare() {
		t.Fatal("terminal handoff Prepare should fail")
	}
	if terminal.Abort() {
		t.Fatal("terminal handoff Abort should fail")
	}
	prepared := &relayHandoff{session: s, prepared: true}
	if prepared.Prepare() {
		t.Fatal("prepared handoff Prepare should fail")
	}
}

func TestRelayDispatchToListenerFirstSelectArms(t *testing.T) {
	t.Parallel()
	notification := appwire.Notification{Method: appwire.NotifyOverlayDelta}

	t.Run("listener context cancelled", func(t *testing.T) {
		t.Parallel()
		s := newRelaySessionForTest()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		listener := relayDispatchListener(ctx, 1)
		s.registerTestListener(listener)

		published, wait := s.dispatchToListenerAtEpoch(relayPublishJob{}, listener, notification)
		if published || wait != nil {
			t.Fatalf("dispatch = (%v, %v), want refusal", published, wait)
		}
		s.mu.Lock()
		_, still := s.listeners[1]
		s.mu.Unlock()
		if still {
			t.Fatal("dispatch kept a listener whose context was cancelled")
		}
	})

	t.Run("listener closed", func(t *testing.T) {
		t.Parallel()
		s := newRelaySessionForTest()
		listener := relayDispatchListener(context.Background(), 1)
		listener.close()
		s.registerTestListener(listener)

		published, wait := s.dispatchToListenerAtEpoch(relayPublishJob{}, listener, notification)
		if published || wait != nil {
			t.Fatalf("dispatch = (%v, %v), want refusal", published, wait)
		}
	})

	t.Run("publication fence revoked", func(t *testing.T) {
		t.Parallel()
		s := newRelaySessionForTest()
		listener := relayDispatchListener(context.Background(), 1)
		s.registerTestListener(listener)
		revoked := make(chan struct{})
		close(revoked)

		job := relayPublishJob{fence: &relayPublicationFence{revoked: revoked}}
		published, wait := s.dispatchToListenerAtEpoch(job, listener, notification)
		if published || wait != nil {
			t.Fatalf("dispatch = (%v, %v), want refusal", published, wait)
		}
	})

	t.Run("session closed", func(t *testing.T) {
		t.Parallel()
		s := newRelaySessionForTest()
		s.cancel()
		listener := relayDispatchListener(context.Background(), 1)
		s.registerTestListener(listener)

		published, wait := s.dispatchToListenerAtEpoch(relayPublishJob{}, listener, notification)
		if published || wait != nil {
			t.Fatalf("dispatch = (%v, %v), want refusal", published, wait)
		}
	})
}

func TestRelayDispatchToListenerAcknowledgementWaitArms(t *testing.T) {
	t.Parallel()
	notification := appwire.Notification{Method: appwire.NotifyOverlayDelta}

	t.Run("acknowledged", func(t *testing.T) {
		t.Parallel()
		s := newRelaySessionForTest()
		listener := relayDispatchListener(context.Background(), 1)
		s.registerTestListener(listener)
		go func() {
			delivery := <-listener.in
			delivery.Acknowledge()
		}()

		published, wait := s.dispatchToListenerAtEpoch(relayPublishJob{}, listener, notification)
		if !published || wait != nil {
			t.Fatalf("dispatch = (%v, %v), want published without a wait", published, wait)
		}
	})

	t.Run("listener context cancelled while waiting", func(t *testing.T) {
		t.Parallel()
		s := newRelaySessionForTest()
		ctx, cancel := context.WithCancel(context.Background())
		listener := relayDispatchListener(ctx, 1)
		s.registerTestListener(listener)
		go func() {
			<-listener.in
			cancel()
		}()

		published, wait := s.dispatchToListenerAtEpoch(relayPublishJob{}, listener, notification)
		if published || wait != nil {
			t.Fatalf("dispatch = (%v, %v), want refusal", published, wait)
		}
		s.mu.Lock()
		_, still := s.listeners[1]
		s.mu.Unlock()
		if still {
			t.Fatal("dispatch kept a listener that died awaiting acknowledgement")
		}
	})

	t.Run("listener closed while waiting", func(t *testing.T) {
		t.Parallel()
		s := newRelaySessionForTest()
		listener := relayDispatchListener(context.Background(), 1)
		s.registerTestListener(listener)
		go func() {
			<-listener.in
			listener.close()
		}()

		published, wait := s.dispatchToListenerAtEpoch(relayPublishJob{}, listener, notification)
		if published || wait != nil {
			t.Fatalf("dispatch = (%v, %v), want refusal", published, wait)
		}
	})

	t.Run("session closed while waiting", func(t *testing.T) {
		t.Parallel()
		s := newRelaySessionForTest()
		listener := relayDispatchListener(context.Background(), 1)
		s.registerTestListener(listener)
		go func() {
			<-listener.in
			s.cancel()
		}()

		published, wait := s.dispatchToListenerAtEpoch(relayPublishJob{}, listener, notification)
		if published || wait != nil {
			t.Fatalf("dispatch = (%v, %v), want refusal", published, wait)
		}
	})
}

func TestRelayWaitForDeliveryArms(t *testing.T) {
	t.Parallel()
	open := make(chan struct{})
	closed := make(chan struct{})
	close(closed)

	s := newRelaySessionForTest()
	if !s.waitForDelivery(relayDeliveryWait{ack: closed, listenerDone: open, listenerCtx: open}) {
		t.Fatal("acknowledged delivery should report true")
	}
	if s.waitForDelivery(relayDeliveryWait{ack: open, listenerDone: open, listenerCtx: closed}) {
		t.Fatal("delivery to a dead listener context should report false")
	}
	if s.waitForDelivery(relayDeliveryWait{ack: open, listenerDone: closed, listenerCtx: open}) {
		t.Fatal("delivery to a closed listener should report false")
	}
	cancelled := newRelaySessionForTest()
	cancelled.cancel()
	if cancelled.waitForDelivery(relayDeliveryWait{ack: open, listenerDone: open, listenerCtx: open}) {
		t.Fatal("delivery on a closed session should report false")
	}
}

func TestRelayPruneDeliveryWaitsArms(t *testing.T) {
	t.Parallel()
	open := make(chan struct{})
	closed := make(chan struct{})
	close(closed)

	s := newRelaySessionForTest()
	kept := s.pruneDeliveryWaits([]relayDeliveryWait{
		{ack: closed, listenerDone: open, listenerCtx: open},
		{ack: open, listenerDone: open, listenerCtx: closed},
		{ack: open, listenerDone: closed, listenerCtx: open},
		{ack: open, listenerDone: open, listenerCtx: open},
	})
	if len(kept) != 1 {
		t.Fatalf("pruned waits = %d, want only the live one", len(kept))
	}

	cancelled := newRelaySessionForTest()
	cancelled.cancel()
	if got := cancelled.pruneDeliveryWaits([]relayDeliveryWait{{ack: open}}); got != nil {
		t.Fatalf("prune on a closed session = %v, want nil", got)
	}
}

func TestRelayPublishToListenerWaitsForProceededDelivery(t *testing.T) {
	t.Parallel()
	s := newRelaySessionForTest()
	listener := relayDispatchListener(context.Background(), 1)
	s.registerTestListener(listener)
	go func() {
		delivery := <-listener.in
		delivery.Proceed()
		delivery.Acknowledge()
	}()

	if !s.publishToListener(listener, appwire.Notification{Method: appwire.NotifyOverlayDelta}) {
		t.Fatal("publishToListener should report the acknowledged delivery")
	}
}

func TestRelaySessionFinishPublicationBarrierStopsWhenSessionCloses(t *testing.T) {
	t.Parallel()
	s := newRelaySessionForTest()
	s.cancel()
	previous := make(chan struct{}) // never closed

	done, barrier := make(chan struct{}), make(chan struct{})
	s.finishPublicationBarrier(previous, nil, done, barrier)
	<-done
	<-barrier

	ack := make(chan struct{})
	listenerCtx := make(chan struct{})
	close(listenerCtx)
	waitingDone, waitingBarrier := make(chan struct{}), make(chan struct{})
	s.finishPublicationBarrier(previous, []relayDeliveryWait{{
		ack: ack, listenerDone: make(chan struct{}), listenerCtx: listenerCtx,
	}}, waitingDone, waitingBarrier)
	<-waitingDone
	<-waitingBarrier
}

func TestRelaySessionMaybeIdleClosesRegisteredListeners(t *testing.T) {
	t.Parallel()
	s := newRelaySessionForTest()
	listener := relayDispatchListener(context.Background(), 1)
	s.registerTestListener(listener)

	s.maybeIdle()

	s.mu.Lock()
	closed := s.closed
	_, still := s.listeners[1]
	s.mu.Unlock()
	if !closed {
		t.Fatal("maybeIdle did not close the idle session")
	}
	if still {
		t.Fatal("maybeIdle kept a registered listener")
	}
	select {
	case <-listener.done:
	default:
		t.Fatal("maybeIdle did not close the listener")
	}
}

func TestRelaySessionAdvanceEpochCreatesMissingFence(t *testing.T) {
	t.Parallel()
	s := newRelaySessionForTest()
	s.mu.Lock()
	if s.publicationFence != nil {
		s.mu.Unlock()
		t.Fatal("test session should start without a publication fence")
	}
	s.advanceEpochLocked()
	epoch, fence, published := s.epoch, s.publicationFence, s.publicationEpoch
	s.mu.Unlock()

	if epoch != 1 || fence == nil || published != 1 {
		t.Fatalf("advanced epoch = %d fence=%v published=%d, want 1 with a fresh fence", epoch, fence, published)
	}
}
