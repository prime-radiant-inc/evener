package appsource

import (
	"context"
	"sync"
	"time"

	"primeradiant.com/serf/appwire"
)

type relaySessionConnect func(context.Context, uint64, func(uint64, appwire.Message, error)) (*appwire.Client, appwire.Transport, error)

type relaySession struct {
	ctx     context.Context
	cancel  context.CancelFunc
	connect relaySessionConnect
	onIdle  func(*relaySession)

	commandGate chan struct{}
	publishWake chan struct{}
	publishMu   sync.Mutex
	publishJobs []relayPublishJob

	mu             sync.Mutex
	epoch          uint64
	connection     *relayConnection
	capture        *relayCapture
	nextGeneration uint64
	nextLease      uint64
	nextListener   uint64
	leases         map[uint64]*relaySessionLease
	listeners      map[uint64]*relayListener
	commandOwners  int
	readParams     appwire.ThreadReadParams
	recovering     bool
	closed         bool
}

type relayConnection struct {
	epoch        uint64
	client       *appwire.Client
	transport    appwire.Transport
	disconnected bool
}

// relayCapture classifies frames around the exact upstream response marker.
// Existing listeners must acknowledge every pre-cut delivery before Read can
// return; post-cut frames remain private until the downstream handoff resolves.
type relayCapture struct {
	epoch      uint64
	generation uint64
	prepared   bool
	cutSeen    bool
	beforeCut  []appwire.Notification
	afterCut   []appwire.Notification
	flushed    chan struct{}
	release    sync.Once
}

type relayPublishJob struct {
	notifications []appwire.Notification
	done          chan struct{}
}

type relayListener struct {
	id      uint64
	leaseID uint64
	ctx     context.Context
	in      chan RelayDelivery
	out     chan RelayDelivery
	done    chan struct{}
	stop    sync.Once
}

type relaySessionLease struct {
	session *relaySession
	id      uint64
	once    sync.Once
}

type relayHandoff struct {
	session    *relaySession
	epoch      uint64
	generation uint64
	mu         sync.Mutex
	prepared   bool
	terminal   bool
}

func newRelaySession(connect relaySessionConnect, onIdle func(*relaySession)) *relaySession {
	ctx, cancel := context.WithCancel(context.Background())
	session := &relaySession{
		ctx:         ctx,
		cancel:      cancel,
		connect:     connect,
		onIdle:      onIdle,
		commandGate: make(chan struct{}, 1),
		publishWake: make(chan struct{}, 1),
		leases:      map[uint64]*relaySessionLease{},
		listeners:   map[uint64]*relayListener{},
	}
	session.commandGate <- struct{}{}
	go session.publishLoop()
	return session
}

func (s *relaySession) acquire() RelaySessionLease {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.nextLease++
	lease := &relaySessionLease{session: s, id: s.nextLease}
	s.leases[lease.id] = lease
	return lease
}

func (l *relaySessionLease) Read(ctx context.Context, params appwire.ThreadReadParams) (RelayReadResult, error) {
	if l == nil || l.session == nil {
		return RelayReadResult{}, appwire.SessionUnavailable("relay session is unavailable")
	}
	return l.session.read(ctx, params)
}

func (l *relaySessionLease) Listen(ctx context.Context) (<-chan RelayDelivery, error) {
	if l == nil || l.session == nil {
		return nil, appwire.SessionUnavailable("relay session is unavailable")
	}
	return l.session.listen(ctx, l.id)
}

func (l *relaySessionLease) Close() {
	if l == nil || l.session == nil {
		return
	}
	l.once.Do(func() {
		l.session.releaseLease(l.id)
	})
}

func (s *relaySession) listen(ctx context.Context, leaseID uint64) (<-chan RelayDelivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed || s.leases[leaseID] == nil {
		s.mu.Unlock()
		return nil, appwire.SessionUnavailable("relay session is unavailable")
	}
	s.nextListener++
	listener := &relayListener{
		id:      s.nextListener,
		leaseID: leaseID,
		ctx:     ctx,
		in:      make(chan RelayDelivery),
		out:     make(chan RelayDelivery),
		done:    make(chan struct{}),
	}
	s.listeners[listener.id] = listener
	s.mu.Unlock()
	go listener.forward()
	context.AfterFunc(ctx, func() {
		s.removeListener(listener.id)
	})
	return listener.out, nil
}

func (l *relayListener) forward() {
	// This goroutine is the sole closer of out. Publishers use the private in
	// channel and done signal, so cancellation cannot race a send with close.
	defer close(l.out)
	for {
		select {
		case <-l.done:
			return
		case <-l.ctx.Done():
			return
		case delivery := <-l.in:
			select {
			case l.out <- delivery:
			case <-l.done:
				return
			case <-l.ctx.Done():
				return
			}
		}
	}
}

func (l *relayListener) close() {
	l.stop.Do(func() {
		close(l.done)
	})
}

func (s *relaySession) read(ctx context.Context, params appwire.ThreadReadParams) (RelayReadResult, error) {
	select {
	case <-ctx.Done():
		return RelayReadResult{}, ctx.Err()
	case <-s.ctx.Done():
		return RelayReadResult{}, appwire.SessionUnavailable("relay session is closed")
	case <-s.commandGate:
	}
	if err := ctx.Err(); err != nil {
		s.commandGate <- struct{}{}
		return RelayReadResult{}, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.releaseCommand()
		return RelayReadResult{}, appwire.SessionUnavailable("relay session is closed")
	}
	s.commandOwners++
	s.readParams = params
	s.mu.Unlock()

	connection, err := s.ensureConnection(ctx)
	if err != nil {
		s.releaseCommand()
		return RelayReadResult{}, err
	}

	s.mu.Lock()
	s.nextGeneration++
	capture := &relayCapture{
		epoch:      connection.epoch,
		generation: s.nextGeneration,
		flushed:    make(chan struct{}),
	}
	s.capture = capture
	s.mu.Unlock()

	readParams := params
	readParams.Subscribe = true
	response, err := connection.client.ThreadRead(ctx, readParams)
	if err != nil {
		s.cancelCapture(capture)
		return RelayReadResult{}, localDaemonSubscribeReadError(err)
	}

	select {
	case <-capture.flushed:
	case <-ctx.Done():
		s.cancelCapture(capture)
		return RelayReadResult{}, ctx.Err()
	case <-s.ctx.Done():
		s.cancelCapture(capture)
		return RelayReadResult{}, appwire.SessionUnavailable("relay session is closed")
	}

	s.mu.Lock()
	valid := !s.closed && s.capture == capture && s.connection == connection && capture.cutSeen
	s.mu.Unlock()
	if !valid {
		s.cancelCapture(capture)
		return RelayReadResult{}, appwire.SessionUnavailable("relay connection ended during thread rejoin")
	}

	return RelayReadResult{
		Response: response,
		Handoff: &relayHandoff{
			session:    s,
			epoch:      capture.epoch,
			generation: capture.generation,
		},
	}, nil
}

func (s *relaySession) ensureConnection(ctx context.Context) (*relayConnection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if connection := s.connection; connection != nil && !connection.disconnected {
		s.mu.Unlock()
		return connection, nil
	}
	s.epoch++
	epoch := s.epoch
	s.mu.Unlock()

	connectionCtx, cancelConnection := context.WithCancel(s.ctx)
	stopCallerCancellation := context.AfterFunc(ctx, cancelConnection)
	client, transport, err := s.connect(connectionCtx, epoch, s.observe)
	if err != nil {
		cancelConnection()
		if callerErr := ctx.Err(); callerErr != nil {
			return nil, callerErr
		}
		return nil, err
	}
	if !stopCallerCancellation() {
		_ = transport.Close()
		return nil, ctx.Err()
	}
	connection := &relayConnection{epoch: epoch, client: client, transport: transport}

	s.mu.Lock()
	if s.closed || s.epoch != epoch || s.connection != nil {
		s.mu.Unlock()
		_ = transport.Close()
		return nil, appwire.SessionUnavailable("relay connection was superseded")
	}
	s.connection = connection
	s.mu.Unlock()
	return connection, nil
}

func (s *relaySession) observe(epoch uint64, message appwire.Message, recvErr error) {
	if recvErr != nil {
		s.disconnect(epoch)
		return
	}

	s.mu.Lock()
	if s.closed || s.connection == nil || s.connection.epoch != epoch || s.connection.disconnected {
		s.mu.Unlock()
		return
	}
	capture := s.capture
	switch {
	case message.Notification != nil:
		notification := *message.Notification
		if capture == nil {
			s.queuePublishLocked([]appwire.Notification{notification}, nil)
		} else if capture.epoch != epoch {
			// A revoked epoch never contributes to the current feed.
		} else if capture.cutSeen {
			capture.afterCut = append(capture.afterCut, notification)
		} else {
			capture.beforeCut = append(capture.beforeCut, notification)
		}
	case (message.Response != nil || message.Error != nil) && capture != nil && capture.epoch == epoch && !capture.cutSeen:
		// The ordered client invokes this synchronously before waking the
		// request waiter, so later frames cannot cross this source cut. The
		// empty job is also a FIFO barrier for notifications accepted before
		// this capture was installed.
		capture.cutSeen = true
		s.queuePublishLocked(capture.beforeCut, capture.flushed)
		capture.beforeCut = nil
	}
	s.mu.Unlock()
}

func (s *relaySession) disconnect(epoch uint64) {
	s.mu.Lock()
	if s.closed || s.epoch != epoch {
		s.mu.Unlock()
		return
	}
	if s.connection == nil {
		// Revoke a connection attempt whose receive loop ended before the
		// initialized client could be installed.
		s.epoch++
		s.mu.Unlock()
		return
	}
	if s.connection.epoch != epoch {
		s.mu.Unlock()
		return
	}
	connection := s.connection
	capture := s.capture
	if capture != nil && capture.epoch == epoch && capture.prepared {
		connection.disconnected = true
		s.mu.Unlock()
		_ = connection.transport.Close()
		return
	}
	s.connection = nil
	s.epoch++
	if capture != nil && capture.epoch == epoch {
		s.capture = nil
		capture.release.Do(func() {
			if !capture.cutSeen {
				close(capture.flushed)
			}
			s.commandOwners--
			s.commandGate <- struct{}{}
		})
	}
	startRecovery := len(s.listeners) > 0 && !s.recovering
	if startRecovery {
		s.recovering = true
	}
	s.mu.Unlock()
	_ = connection.transport.Close()
	s.maybeIdle()
	if startRecovery {
		go s.recoverCanonicalFeed()
	}
}

func (s *relaySession) recoverCanonicalFeed() {
	// Recovery uses the same serialized atomic read as a downstream rejoin.
	// Only after the replacement connection has a live subscribed continuation
	// do listeners receive resync and resume the canonical feed.
	attempt := 0
	for {
		s.mu.Lock()
		if s.closed || len(s.listeners) == 0 {
			s.recovering = false
			s.mu.Unlock()
			return
		}
		params := s.readParams
		s.mu.Unlock()

		result, err := s.read(s.ctx, params)
		if err == nil {
			resync := *appwire.NotificationMessage(appwire.NotifySerfThreadResync, appwire.ThreadResyncParams{
				ThreadID: result.Response.Thread.ID,
				Ref:      result.Response.Thread.Serf.Ref,
			}).Notification
			if s.publishAndWait(resync) {
				handoffResolved := result.Handoff.Abort()
				s.mu.Lock()
				live := handoffResolved &&
					s.connection != nil &&
					!s.connection.disconnected
				if live {
					s.recovering = false
				}
				s.mu.Unlock()
				if live {
					return
				}
				continue
			}
			result.Handoff.Abort()
		}

		attempt++
		delay := 100 * time.Millisecond
		for i := 1; i < attempt && delay < 5*time.Second; i++ {
			delay *= 2
		}
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-s.ctx.Done():
			timer.Stop()
			s.mu.Lock()
			s.recovering = false
			s.mu.Unlock()
			return
		}
	}
}

func (s *relaySession) publishAndWait(notification appwire.Notification) bool {
	done := make(chan struct{})
	s.mu.Lock()
	if s.closed || len(s.listeners) == 0 {
		s.mu.Unlock()
		return false
	}
	s.queuePublishLocked([]appwire.Notification{notification}, done)
	s.mu.Unlock()
	select {
	case <-done:
		return true
	case <-s.ctx.Done():
		return false
	}
}

func (s *relaySession) cancelCapture(capture *relayCapture) {
	s.mu.Lock()
	if s.capture == capture {
		s.capture = nil
		notifications := append(append([]appwire.Notification{}, capture.beforeCut...), capture.afterCut...)
		if len(notifications) > 0 {
			s.queuePublishLocked(notifications, nil)
		}
	}
	capture.release.Do(func() {
		if !capture.cutSeen {
			close(capture.flushed)
		}
		s.commandOwners--
		s.commandGate <- struct{}{}
	})
	s.mu.Unlock()
	s.maybeIdle()
}

func (h *relayHandoff) Prepare() bool {
	if h == nil || h.session == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.terminal || h.prepared {
		return false
	}
	h.session.mu.Lock()
	capture := h.session.capture
	valid := !h.session.closed &&
		h.session.connection != nil &&
		h.session.connection.epoch == h.epoch &&
		!h.session.connection.disconnected &&
		capture != nil &&
		capture.epoch == h.epoch &&
		capture.generation == h.generation
	if !valid {
		h.session.mu.Unlock()
		return false
	}
	capture.prepared = true
	h.session.mu.Unlock()
	h.prepared = true
	return true
}

func (h *relayHandoff) Commit() bool {
	return h.finish()
}

func (h *relayHandoff) Abort() bool {
	return h.finish()
}

func (h *relayHandoff) finish() bool {
	if h == nil || h.session == nil {
		return false
	}
	h.mu.Lock()
	if h.terminal {
		h.mu.Unlock()
		return false
	}
	won := h.session.finishHandoff(h.epoch, h.generation)
	h.terminal = true
	h.mu.Unlock()
	if won {
		h.session.maybeIdle()
	}
	return won
}

func (s *relaySession) finishHandoff(epoch, generation uint64) bool {
	s.mu.Lock()
	capture := s.capture
	if capture == nil || capture.epoch != epoch || capture.generation != generation {
		s.mu.Unlock()
		return false
	}
	s.capture = nil
	if s.connection != nil && s.connection.epoch == epoch && len(capture.afterCut) > 0 {
		s.queuePublishLocked(capture.afterCut, nil)
	}
	capture.release.Do(func() {
		s.commandOwners--
		s.commandGate <- struct{}{}
	})
	startRecovery := false
	if s.connection != nil && s.connection.epoch == epoch && s.connection.disconnected {
		s.connection = nil
		s.epoch++
		startRecovery = len(s.listeners) > 0 && !s.recovering
		if startRecovery {
			s.recovering = true
		}
	}
	s.mu.Unlock()
	if startRecovery {
		go s.recoverCanonicalFeed()
	}
	return true
}

func (s *relaySession) queuePublishLocked(notifications []appwire.Notification, done chan struct{}) {
	job := relayPublishJob{
		notifications: append([]appwire.Notification(nil), notifications...),
		done:          done,
	}
	s.publishMu.Lock()
	s.publishJobs = append(s.publishJobs, job)
	s.publishMu.Unlock()
	select {
	case s.publishWake <- struct{}{}:
	default:
	}
}

func (s *relaySession) publishLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.publishWake:
			for {
				s.publishMu.Lock()
				if len(s.publishJobs) == 0 {
					s.publishMu.Unlock()
					break
				}
				job := s.publishJobs[0]
				s.publishJobs[0] = relayPublishJob{}
				s.publishJobs = s.publishJobs[1:]
				s.publishMu.Unlock()
				for _, notification := range job.notifications {
					s.publishNotification(notification)
				}
				if job.done != nil {
					close(job.done)
				}
			}
		}
	}
}

func (s *relaySession) publishNotification(notification appwire.Notification) {
	s.mu.Lock()
	listeners := make([]*relayListener, 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	s.mu.Unlock()

	for _, listener := range listeners {
		if !s.publishToListener(listener, notification) && s.ctx.Err() != nil {
			return
		}
	}
}

func (s *relaySession) publishToListener(listener *relayListener, notification appwire.Notification) bool {
	ack := make(chan struct{})
	var once sync.Once
	delivery := RelayDelivery{
		Notification: notification,
		Acknowledge: func() {
			once.Do(func() { close(ack) })
		},
	}
	select {
	case listener.in <- delivery:
	case <-listener.ctx.Done():
		s.removeListener(listener.id)
		return false
	case <-listener.done:
		return false
	case <-s.ctx.Done():
		return false
	}
	select {
	case <-ack:
		return true
	case <-listener.ctx.Done():
		s.removeListener(listener.id)
		return false
	case <-listener.done:
		return false
	case <-s.ctx.Done():
		return false
	}
}

func (s *relaySession) removeListener(id uint64) {
	s.mu.Lock()
	listener := s.listeners[id]
	if listener != nil {
		delete(s.listeners, id)
		listener.close()
	}
	s.mu.Unlock()
}

func (s *relaySession) releaseLease(id uint64) {
	s.mu.Lock()
	if s.leases[id] == nil {
		s.mu.Unlock()
		return
	}
	delete(s.leases, id)
	for listenerID, listener := range s.listeners {
		if listener.leaseID == id {
			delete(s.listeners, listenerID)
			listener.close()
		}
	}
	s.mu.Unlock()
	s.maybeIdle()
}

func (s *relaySession) releaseCommand() {
	s.mu.Lock()
	s.commandOwners--
	s.commandGate <- struct{}{}
	s.mu.Unlock()
	s.maybeIdle()
}

func (s *relaySession) maybeIdle() {
	s.mu.Lock()
	if s.closed || len(s.leases) != 0 || s.commandOwners != 0 || s.capture != nil {
		s.mu.Unlock()
		return
	}
	s.closed = true
	connection := s.connection
	s.connection = nil
	s.cancel()
	for id, listener := range s.listeners {
		delete(s.listeners, id)
		listener.close()
	}
	onIdle := s.onIdle
	s.mu.Unlock()
	if connection != nil {
		_ = connection.transport.Close()
	}
	if onIdle != nil {
		onIdle(s)
	}
}
