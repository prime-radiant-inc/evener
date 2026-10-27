package appserver

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apphistory"
)

type Server struct {
	cfg                    ServerConfig
	router                 *Router
	subs                   *Subscriptions
	webSocketHandlers      int
	webSocketShuttingDown  bool
	webSocketDrained       chan struct{}
	keepaliveTickerFactory func(time.Duration) webSocketKeepaliveTicker
	keepaliveDecision      func(bool)
	// requestQueueCapacity is requestQueueCap, overridable by tests that
	// need to saturate the queue without pipelining 65 real frames.
	requestQueueCapacity int
	// blockedEnqueue runs when the receive loop is about to park on a full
	// request queue, so a saturation test can wait for the loop to actually
	// block instead of sleeping. Production leaves it nil.
	blockedEnqueue func()
	// afterWorkerDequeue runs on the worker goroutine after each dequeue and
	// before the post-dequeue cancellation re-check, so a test can pin the
	// re-check deterministically. Production leaves it nil.
	afterWorkerDequeue func(appwire.Message)
	// afterSlowReadAcquire runs on the worker goroutine after a slow-read
	// cap slot is acquired and before the post-acquire cancellation
	// re-check, so a test can pin that re-check deterministically.
	// Production leaves it nil.
	afterSlowReadAcquire func()
	// slowReadStallThreshold is slowReadCapStallAdvisory, overridable by
	// tests that drive the stall advisory without waiting 30 seconds.
	slowReadStallThreshold time.Duration
	// wrapWebSocketTransport lets a test interpose on the transport
	// ServeWebSocket builds — e.g. a blocking Send — before the loops start.
	// Production leaves it nil.
	wrapWebSocketTransport func(webSocketTransport) webSocketTransport
	// sendWriteTimeout is webSocketWriteTimeout, overridable by tests that
	// drive the write-timeout cascade without waiting 30 seconds.
	sendWriteTimeout               time.Duration
	projectionMu                   sync.Mutex
	deliveryMu                     sync.Mutex
	nextHydrationGeneration        uint64
	routedSeq                      uint64
	mu                             sync.RWMutex
	conns                          map[string]*Connection
	afterUnregisterDelete          func()
	beforeSubscriptionRegistration func()
	// beforeSubscriptionBeginBuffered pins the short claim-to-registration
	// admission section in package tests; production leaves it nil.
	beforeSubscriptionBeginBuffered func()
	// beforeHydrationCommit runs after a response removes its hydration
	// finalizer and before that finalizer commits. Production leaves it nil;
	// package tests use it to pin the response-visible activation boundary.
	beforeHydrationCommit          func()
	afterBroadcastConnectionLookup func(*Connection)
	// lifetime is canceled by Shutdown so a background consumer a server owns
	// can end with it; lifetimeCancel is never nil after NewServer. Both are
	// inert unless something reads Lifetime — see there.
	lifetime       context.Context
	lifetimeCancel context.CancelFunc
}

func NewServer(cfg ServerConfig) *Server {
	lifetime, lifetimeCancel := context.WithCancel(context.Background())
	s := &Server{
		cfg:                    cfg,
		router:                 NewRouter(),
		subs:                   NewSubscriptions(),
		conns:                  map[string]*Connection{},
		webSocketDrained:       make(chan struct{}),
		keepaliveTickerFactory: newRealWebSocketKeepaliveTicker,
		requestQueueCapacity:   requestQueueCap,
		slowReadStallThreshold: slowReadCapStallAdvisory,
		sendWriteTimeout:       webSocketWriteTimeout,
		lifetime:               lifetime,
		lifetimeCancel:         lifetimeCancel,
	}
	HandleTyped(s.router, appwire.MethodInitialize, s.initialize)
	return s
}

// Lifetime reports a context that is canceled when Shutdown begins. It is the
// server's one lifecycle signal for a consumer that outlives individual
// connections — a notification fan-out registered on the server, say — so the
// consumer can be bound to the server it serves instead of to
// context.Background(), which nothing ever cancels and which therefore outlives
// an in-process server recreation as a leaked goroutine still holding the old
// server's sources.
//
// It is strictly additive: the returned context is never nil and is canceled
// only by Shutdown, so a server nobody shuts down leaves it open — the same
// observable behavior a context.Background() consumer had before this existed.
// Shutdown cancels it before it waits for handlers, so an expired Shutdown
// context does not keep the signal from firing.
func (s *Server) Lifetime() context.Context { return s.lifetime }

// Shutdown stops accepting AppWire WebSockets, cancels every active
// connection, and waits for their handlers to finish.
func (s *Server) Shutdown(ctx context.Context) error {
	// Cancel the lifetime first: a background consumer must stop when shutdown
	// begins, not after the connection handlers drain, and must still stop when
	// this call gives up early on an already-expired context.
	s.lifetimeCancel()
	s.mu.Lock()
	if !s.webSocketShuttingDown {
		s.webSocketShuttingDown = true
		if s.webSocketHandlers == 0 {
			close(s.webSocketDrained)
		}
	}
	connections := make([]*Connection, 0, len(s.conns))
	for _, conn := range s.conns {
		connections = append(connections, conn)
	}
	drained := s.webSocketDrained
	s.mu.Unlock()
	for _, conn := range connections {
		conn.cancelContext()
	}

	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) beginWebSocket() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.webSocketShuttingDown {
		return false
	}
	s.webSocketHandlers++
	return true
}

func (s *Server) endWebSocket() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.webSocketHandlers--
	if s.webSocketShuttingDown && s.webSocketHandlers == 0 {
		close(s.webSocketDrained)
	}
}

func (s *Server) webSocketShutdownStarted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.webSocketShuttingDown
}

func (s *Server) Router() *Router {
	return s.router
}

// CommitProjection serializes one source projector transition with sequence
// allocation and insertion into every subscriber's live stream or hydration
// buffer.
func (s *Server) CommitProjection(commit func() []SequencedNotification) {
	s.projectionMu.Lock()
	records := commit()
	deliveries := make([]notificationDelivery, 0, len(records))
	for _, record := range records {
		deliveries = append(deliveries, s.routeSequencedLocked(record)...)
	}
	s.deliveryMu.Lock()
	s.projectionMu.Unlock()
	disconnected := s.deliverNotifications(deliveries)
	s.deliveryMu.Unlock()
	for _, conn := range disconnected {
		s.evictSlowConsumer(conn, "a projection commit")
	}
}

func (s *Server) NewConnection(id string) *Connection {
	// The outbound buffer is the hub-side half of the contract
	// appwire.NotificationBufferCap documents for the client: it must hold any
	// single legitimate burst even while the send loop waits for a scheduling
	// slice, because a full buffer is answered with eviction. The two sides
	// share one constant so neither peer can quietly become the smaller pipe
	// (at 32 this side evicted live clients whose send loop napped through a
	// turn-boundary burst on a loaded machine).
	return &Connection{
		id:                           id,
		server:                       s,
		send:                         make(chan appwire.Message, appwire.NotificationBufferCap),
		requests:                     make(chan admittedRequest, s.requestQueueCapacity),
		slowReadSlots:                make(chan struct{}, slowReadDispatchCap),
		requestPools:                 make(map[string]int),
		requestPoolSaturationAdvised: make(map[string]bool),
		slowReadInflight:             map[string]int{},
		workerExited:                 make(chan struct{}),
		pendingAdmissions:            map[*subscriptionAdmission]struct{}{},
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
	}
}

// Logf reports a server-side diagnostic through the configured sink. It is a
// no-op when ServerConfig.Logf is nil.
func (s *Server) Logf(format string, args ...any) {
	s.logf(format, args...)
}

// panicLogf reports a handler panic. It prefers the embedder's configured
// sink so panic lines ride the same channel as every other server-initiated
// event, but unlike logf it never drops the line: a panic must stay visible
// even when the embedder configured no logger.
func (s *Server) panicLogf(format string, args ...any) {
	if s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// evictSlowConsumer unregisters a connection whose outbound buffer is full.
// The buffer holds appwire.NotificationBufferCap frames — any legitimate
// burst fits — so a full one means the peer stopped draining, not that its
// send loop lost a scheduling slice. Say so out loud: the socket closes with
// a NORMAL status, so this line is the only artifact that tells an eviction
// apart from the client hanging up.
func (s *Server) evictSlowConsumer(conn *Connection, during string) {
	s.logf("appserver: evicting connection %s during %s: outbound buffer full (%d frames), the consumer stopped draining", conn.id, during, cap(conn.send))
	s.unregisterConnection(conn)
}

func (s *Server) registerConnection(conn *Connection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[conn.id] = conn
}

func (s *Server) unregisterConnection(conn *Connection) {
	if conn == nil {
		return
	}
	s.projectionMu.Lock()
	aborted := s.unregisterConnectionLocked(conn)
	s.projectionMu.Unlock()
	for _, finalizer := range aborted {
		finalizer.abortAfterWithdrawal()
	}
}

func (s *Server) unregisterConnectionLocked(conn *Connection) []*hydrationResponseFinalizer {
	s.mu.Lock()
	if s.conns[conn.id] != conn {
		s.mu.Unlock()
		return nil
	}
	delete(s.conns, conn.id)
	conn.cancelContext()
	conn.closeSend()
	if s.afterUnregisterDelete != nil {
		s.afterUnregisterDelete()
	}
	s.subs.RemoveConnection(conn.id)
	aborted := conn.takeAllHydrations()
	s.mu.Unlock()
	return aborted
}

// Broadcast publishes a notification that no caller sequenced -- a relay frame,
// a resync, or a synthesized turn failure. It allocates a sequence above every
// sequence this server has already routed, because a hydration cut only ever
// names a sequence that was routed before the cut was taken. An unsequenced
// record would carry Seq 0, and Subscriptions.Release keeps only records above
// the cut, so a buffering subscriber would discard it on release.
func (s *Server) Broadcast(threadID, method string, params any) {
	s.projectionMu.Lock()
	s.routedSeq++
	record := SequencedNotification{
		Seq:          s.routedSeq,
		ThreadID:     threadID,
		Notification: *appwire.NotificationMessage(method, params).Notification,
	}
	deliveries := s.routeSequencedLocked(record)
	s.deliveryMu.Lock()
	s.projectionMu.Unlock()
	disconnected := s.deliverNotifications(deliveries)
	s.deliveryMu.Unlock()
	for _, conn := range disconnected {
		s.evictSlowConsumer(conn, "a sequenced broadcast")
	}
}

type notificationDelivery struct {
	connection *Connection
	record     SequencedNotification
	content    *apphistory.Filter
}

// routeSequencedLocked runs under projectionMu, the same gate a capture holds
// while it reads its cut. Tracking the high-water sequence here is what lets
// Broadcast allocate above every cut the server can have handed out.
func (s *Server) routeSequencedLocked(record SequencedNotification) []notificationDelivery {
	if record.Seq > s.routedSeq {
		s.routedSeq = record.Seq
	}
	deliveries := make([]notificationDelivery, 0)
	for _, connID := range s.subs.Route(record) {
		s.mu.RLock()
		conn := s.conns[connID]
		s.mu.RUnlock()
		if conn == nil {
			continue
		}
		deliveries = append(deliveries, notificationDelivery{
			connection: conn,
			record:     record,
			content:    s.subs.content(connID, record.ThreadID),
		})
	}
	return deliveries
}

func (s *Server) deliverNotifications(deliveries []notificationDelivery) []*Connection {
	var disconnected []*Connection
	for _, delivery := range deliveries {
		if s.afterBroadcastConnectionLookup != nil {
			s.afterBroadcastConnectionLookup(delivery.connection)
		}
		if !delivery.connection.enqueueHistory(delivery.record, delivery.content) {
			disconnected = append(disconnected, delivery.connection)
		}
	}
	return disconnected
}

// BroadcastAll sends a notification to every currently-connected client,
// regardless of thread subscription. Used for hub-wide state-change
// notifications such as evener/auth/updated and evener/launch/updated.
func (s *Server) BroadcastAll(method string, params any) {
	msg := appwire.NotificationMessage(method, params)
	s.mu.RLock()
	conns := make([]*Connection, 0, len(s.conns))
	for _, c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.RUnlock()
	for _, conn := range conns {
		if !conn.enqueue(msg) {
			s.evictSlowConsumer(conn, "a hub-wide broadcast")
		}
	}
}

func (s *Server) SubscriberCount(threadID string) int {
	return s.subs.ConnectionCount(threadID)
}

// SetBeforeSubscriptionGate installs a callback run immediately before a
// subscribe, a subscription replacement, or a capture acquires the projection
// gate. It holds no lock, so a test outside this package can park there to
// order a concurrent commit against exactly that boundary -- which is the one
// boundary that tells an atomic capture apart from a snapshot taken beside the
// subscription rather than with it. Production leaves it nil. Install it before
// the server serves anything.
func (s *Server) SetBeforeSubscriptionGate(fn func()) {
	s.beforeSubscriptionRegistration = fn
}

func (s *Server) initialize(_ context.Context, params appwire.InitializeParams) (appwire.InitializeResponse, error) {
	if !s.cfg.AdapterNativeInitialize && params.ProtocolVersion != appwire.ProtocolVersion {
		if olderProtocolVersion(params.ProtocolVersion) {
			return appwire.InitializeResponse{}, appwire.UpgradeRequired(params.ProtocolVersion, appwire.ProtocolVersion)
		}
		return appwire.InitializeResponse{}, appwire.InvalidRequest(
			fmt.Sprintf("protocol version %q is incompatible; want %q", params.ProtocolVersion, appwire.ProtocolVersion),
		)
	}
	protocolVersion := appwire.ProtocolVersion
	if s.cfg.AdapterNativeInitialize {
		protocolVersion = ""
	}
	capability := s.cfg.Navigation
	if s.cfg.NavigationCapability != nil {
		capability = s.cfg.NavigationCapability()
	}
	return appwire.InitializeResponse{
		ServerInfo: appwire.ServerInfo{
			Name:         s.cfg.ServerName,
			Version:      s.cfg.Version,
			BuildVersion: s.cfg.BuildVersion,
			OS:           s.cfg.OS,
			Arch:         s.cfg.Arch,
			Roots:        append([]string(nil), s.cfg.Roots...),
		},
		ProtocolVersion: protocolVersion,
		SourceID:        s.cfg.SourceID,
		Features:        s.cfg.Features,
		Navigation:      navigationCapability(capability),
	}, nil
}

// olderProtocolVersion reports whether version is an AppWire protocol version
// ("evener-appwire-v<N>") older than the one this server speaks.
func olderProtocolVersion(version string) bool {
	return appwire.OlderProtocolVersion(version)
}

func navigationCapability(capability *appwire.NavigationCapability) *appwire.NavigationCapability {
	if capability == nil {
		return nil
	}
	clone := *capability
	return &clone
}
