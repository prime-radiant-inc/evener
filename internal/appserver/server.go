package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
)

type ServerConfig struct {
	ServerName string
	Version    string
	SourceID   string
	Features   appwire.FeatureSet
	// WebSocketTrace records raw data frames and lifecycle events for each
	// accepted connection. Nil keeps tracing disabled.
	WebSocketTrace *WebSocketTrace
	// Navigation is absent until a server supports navigation HTTP resources.
	Navigation *appwire.NavigationCapability
	// NavigationCapability is evaluated for every initialize request. It lets a
	// hub advertise a current generation/sequence rather than freezing those
	// values when its RPC server was constructed.
	NavigationCapability func() *appwire.NavigationCapability
	// AdapterNativeInitialize keeps the shared JSON-RPC server usable in tests
	// for adapters whose upstream protocol owns a different initialize shape.
	AdapterNativeInitialize bool
	// Logf reports server-initiated diagnostics a peer cannot see from its side
	// of the socket, including handler failures and connection lifecycle events.
	// Nil means silent.
	Logf func(format string, args ...any)
	// SubscriptionAdmissionResolver returns the canonical pending-admission key
	// for a subscribing thread/read or a thread/unsubscribe request. It must be
	// pure and synchronous; ordered dispatch uses it before starting the read or
	// invoking the unsubscribe handler. It does not determine delivery routing.
	SubscriptionAdmissionResolver func(appwire.Message) (string, bool)
}

// requestQueueCap bounds each connection's inbound request queue. It must
// hold any legitimate pipelined burst from one client (observed bursts are a
// handful of requests) while keeping the queue's worst-case memory boring; a
// full queue applies blocking backpressure to the receive loop, never a wire
// error. Deliberately not appwire.NotificationBufferCap: that constant sizes
// the outbound notification firehose, where overflow means eviction; this one
// sizes inbound pipelining, where overflow means flow control. Coupling them
// would let the wrong contract resize this one.
const requestQueueCap = 64

// slowReadDispatchCap bounds how many concurrent slow reads one connection
// holds in flight. 16 is the operating point the slice-0 audit's client sweep
// chose (Jesse, 2026-08-30): routine flows exceed the earlier draft's 4 — the
// web delegate rail mounts every visible delegate card with a full
// thread/read watch, reconnect resync reads every tracked and watched ref at
// once, and the TUI subscribes N+1 children on session entry — and 16 covers
// those bursts while still closing the formerly unbounded goroutine/params
// retention. A full cap blocks the worker's next slow-read dispatch — no wire
// error, no Unavailable — so later requests head-of-line wait for a read to
// finish; that wait is the design's second deliberate scheduling change.
const slowReadDispatchCap = 16

// slowReadCapStallAdvisory is how long a single blocked slow-read acquire
// parks before the worker reports the wedged lane — the same scale as
// webSocketWriteTimeout. The ping bypass keeps a connection looking healthy
// while its serial lane is dead behind a saturated cap, so the wedge needs an
// operational signature even though the client sees a live heartbeat.
const slowReadCapStallAdvisory = 30 * time.Second

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
}

func NewServer(cfg ServerConfig) *Server {
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
	}
	HandleTyped(s.router, appwire.MethodInitialize, s.initialize)
	return s
}

// Shutdown stops accepting AppWire WebSockets, cancels every active
// connection, and waits for their handlers to finish.
func (s *Server) Shutdown(ctx context.Context) error {
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
		id:                id,
		server:            s,
		send:              make(chan appwire.Message, appwire.NotificationBufferCap),
		requests:          make(chan appwire.Message, s.requestQueueCapacity),
		slowReadSlots:     make(chan struct{}, slowReadDispatchCap),
		slowReadInflight:  map[string]int{},
		workerExited:      make(chan struct{}),
		pendingAdmissions: map[*subscriptionAdmission]struct{}{},
	}
}

type subscriptionAdmission struct {
	requestID string
	threadID  string
	canceled  bool
}

func (c *Connection) beginSubscriptionAdmission(requestID, threadID string) *subscriptionAdmission {
	admission := &subscriptionAdmission{requestID: requestID, threadID: threadID}
	c.admissionMu.Lock()
	if c.pendingAdmissions == nil {
		c.pendingAdmissions = map[*subscriptionAdmission]struct{}{}
	}
	c.pendingAdmissions[admission] = struct{}{}
	c.admissionMu.Unlock()
	return admission
}

func (c *Connection) finishSubscriptionAdmission(admission *subscriptionAdmission) {
	if admission == nil {
		return
	}
	c.admissionMu.Lock()
	delete(c.pendingAdmissions, admission)
	c.admissionMu.Unlock()
}

func (c *Connection) cancelSubscriptionAdmissions(threadID string) {
	c.admissionMu.Lock()
	for admission := range c.pendingAdmissions {
		if admission.threadID == threadID {
			admission.canceled = true
		}
	}
	c.admissionMu.Unlock()
}

func (c *Connection) claimSubscriptionAdmission(requestID string) (*subscriptionAdmission, bool) {
	for admission := range c.pendingAdmissions {
		if admission.requestID == requestID {
			return admission, !admission.canceled
		}
	}
	return nil, true
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
		msg := appwire.Message{Notification: &delivery.record.Notification}
		if !delivery.connection.enqueue(msg) {
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
		ServerInfo:      appwire.ServerInfo{Name: s.cfg.ServerName, Version: s.cfg.Version},
		ProtocolVersion: protocolVersion,
		SourceID:        s.cfg.SourceID,
		Features:        s.cfg.Features,
		Navigation:      navigationCapability(capability),
	}, nil
}

func navigationCapability(capability *appwire.NavigationCapability) *appwire.NavigationCapability {
	if capability == nil {
		return nil
	}
	clone := *capability
	return &clone
}

type Connection struct {
	id         string
	server     *Server
	send       chan appwire.Message
	sendMu     sync.RWMutex
	sendClosed bool
	// requests is the bounded inbound queue between the transport's receive
	// loop (the only producer) and the serial worker (the only consumer).
	// Neither side ever closes it — producer teardown is "stop sending",
	// consumer teardown is context cancellation — and ServeWebSocket purges
	// its buffered frames at teardown so an orphaned handler retains at most
	// the one frame it is executing.
	requests chan appwire.Message
	// queueSaturationAdvised makes the queue-saturation advisory one-shot per
	// connection. Only the receive-loop goroutine touches it.
	queueSaturationAdvised bool
	// slowReadSlots is the buffered-channel semaphore capping in-flight slow
	// reads per connection (slowReadDispatchCap). The worker acquires a slot
	// before spawning a slow read; the slow-read goroutine releases it when
	// handleAndEnqueue returns.
	slowReadSlots chan struct{}
	// capSaturationAdvised makes the cap-saturation advisory one-shot per
	// connection. Only the worker goroutine touches it.
	capSaturationAdvised bool
	// slowReadMu guards slowReadInflight, the per-method tally of in-flight
	// slow reads the stall advisory names.
	slowReadMu       sync.Mutex
	slowReadInflight map[string]int
	// workerExited closes when the serial worker returns; tests assert
	// worker teardown against it instead of sleeping.
	workerExited chan struct{}
	// pendingAdmissions covers the wire-admitted-to-capture interval. Entries
	// are removed on claim or read completion, so cancellation leaves no
	// unbounded tombstones.
	admissionMu       sync.Mutex
	pendingAdmissions map[*subscriptionAdmission]struct{}
	mu                sync.RWMutex
	initialized       bool
	cancel            context.CancelFunc
	responseMu        sync.Mutex
	hydrationMu       sync.Mutex
	hydrations        map[string]*hydrationResponseFinalizer
}

func (c *Connection) ID() string {
	return c.id
}

func (c *Connection) Subscribe(threadID string) {
	c.server.projectionMu.Lock()
	defer c.server.projectionMu.Unlock()
	c.server.subs.Subscribe(c.id, threadID)
}

func (c *Connection) ReplaceSubscriptions(threadID string) {
	c.server.projectionMu.Lock()
	defer c.server.projectionMu.Unlock()
	c.server.deliveryMu.Lock()
	defer c.server.deliveryMu.Unlock()
	c.server.subs.ReplaceConnectionSubscriptions(c.id, threadID)
}

func (c *Connection) setCancel(cancel context.CancelFunc) {
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
}

func (c *Connection) cancelContext() {
	c.mu.RLock()
	cancel := c.cancel
	c.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Connection) enqueue(msg appwire.Message) bool {
	c.sendMu.RLock()
	defer c.sendMu.RUnlock()
	if c.sendClosed {
		return false
	}
	select {
	case c.send <- msg:
		return true
	default:
		return false
	}
}

func (c *Connection) enqueueResponse(ctx context.Context, msg appwire.Message) error {
	responseID, responseSucceeded := responseHydrationOutcome(msg)
	c.hydrationMu.Lock()
	c.sendMu.RLock()
	if c.sendClosed {
		finalizer := c.takeHydration(responseID)
		c.sendMu.RUnlock()
		if finalizer != nil {
			finalizer.abort()
			c.hydrationMu.Unlock()
			finalizer.abortAfterWithdrawal()
		} else {
			c.hydrationMu.Unlock()
		}
		return context.Canceled
	}
	select {
	case c.send <- msg:
		finalizer := c.takeHydration(responseID)
		c.sendMu.RUnlock()
		if finalizer != nil {
			if responseSucceeded || finalizer.releaseOnErrorResponse {
				if c.server.beforeHydrationCommit != nil {
					c.server.beforeHydrationCommit()
				}
				finalizer.commit()
				c.hydrationMu.Unlock()
				finalizer.commitAfterRelease()
			} else {
				finalizer.abort()
				c.hydrationMu.Unlock()
				finalizer.abortAfterWithdrawal()
			}
		} else {
			c.hydrationMu.Unlock()
		}
		return nil
	case <-ctx.Done():
		finalizer := c.takeHydration(responseID)
		c.sendMu.RUnlock()
		if finalizer != nil {
			finalizer.abort()
			c.hydrationMu.Unlock()
			finalizer.abortAfterWithdrawal()
		} else {
			c.hydrationMu.Unlock()
		}
		return ctx.Err()
	}
}

func (c *Connection) addHydration(finalizer *hydrationResponseFinalizer) {
	c.responseMu.Lock()
	if c.hydrations == nil {
		c.hydrations = map[string]*hydrationResponseFinalizer{}
	}
	c.hydrations[finalizer.responseID] = finalizer
	c.responseMu.Unlock()
}

func (c *Connection) takeHydration(responseID string) *hydrationResponseFinalizer {
	c.responseMu.Lock()
	finalizer := c.hydrations[responseID]
	delete(c.hydrations, responseID)
	c.responseMu.Unlock()
	return finalizer
}

func (c *Connection) takeSupersededHydrations(
	responseID, threadID string,
	replace bool,
) []*hydrationResponseFinalizer {
	c.responseMu.Lock()
	var superseded []*hydrationResponseFinalizer
	for key, finalizer := range c.hydrations {
		if key != responseID && !replace && !finalizer.replace && finalizer.threadID != threadID {
			continue
		}
		superseded = append(superseded, finalizer)
		delete(c.hydrations, key)
	}
	c.responseMu.Unlock()
	sort.Slice(superseded, func(i, j int) bool {
		return superseded[i].generation > superseded[j].generation
	})
	return superseded
}

func (c *Connection) takeAllHydrations() []*hydrationResponseFinalizer {
	c.responseMu.Lock()
	pending := make([]*hydrationResponseFinalizer, 0, len(c.hydrations))
	for key, finalizer := range c.hydrations {
		pending = append(pending, finalizer)
		delete(c.hydrations, key)
	}
	c.responseMu.Unlock()
	return pending
}

func responseHydrationOutcome(msg appwire.Message) (string, bool) {
	switch {
	case msg.Response != nil:
		return requestIDKey(msg.Response.ID), true
	case msg.Error != nil:
		return requestIDKey(msg.Error.ID), false
	default:
		return "", false
	}
}

func requestIDKey(id appwire.ID) string {
	raw, _ := json.Marshal(id)
	return string(raw)
}

func (c *Connection) closeSend() {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.sendClosed {
		return
	}
	c.sendClosed = true
	close(c.send)
}

func (c *Connection) HandleMessage(ctx context.Context, msg appwire.Message) appwire.Message {
	if msg.Notification != nil {
		return c.handleNotification(*msg.Notification)
	}
	if msg.Request == nil {
		return appwire.ErrorMessage(appwire.NewIntID(0), appwire.InvalidRequest("request message required"))
	}
	req := *msg.Request
	if _, ok := ctx.Value(subscriptionAdmissionResolverPanicContextKey{}).(bool); ok {
		return appwire.ErrorMessage(req.ID, appwire.InternalError("internal error handling request"))
	}
	if reason, ok := ctx.Value(subscriptionAdmissionFailureContextKey{}).(string); ok {
		return appwire.ErrorMessage(req.ID, appwire.WireError{Code: appwire.CodeUnavailable, Message: reason})
	}
	// ping is the browser's app-level heartbeat (browsers cannot send WS ping
	// frames from JS). It bypasses the router and is answered here, before
	// the initialize gate; the receive loop answers it inline, bypassing the
	// request queue, so no handler can starve it.
	if req.Method == appwire.MethodPing {
		return appwire.ResponseMessage(req.ID, struct{}{})
	}
	if !c.isInitialized() && req.Method != appwire.MethodInitialize {
		return appwire.ErrorMessage(req.ID, appwire.InvalidRequest("initialize required"))
	}
	if c.isInitialized() && req.Method == appwire.MethodInitialize {
		return appwire.ErrorMessage(req.ID, appwire.InvalidRequest("already initialized"))
	}
	if !c.server.cfg.AdapterNativeInitialize {
		if err := appwire.ValidateMutationParams(req.Method, req.Params); err != nil {
			return appwire.ErrorMessage(req.ID, appwire.InvalidParams(err.Error()))
		}
	}
	handlerCtx := context.WithValue(ctx, connectionContextKey{}, c)
	handlerCtx = context.WithValue(handlerCtx, requestIDContextKey{}, requestIDKey(req.ID))
	result, err := c.server.router.Dispatch(handlerCtx, req)
	if err != nil {
		return appwire.ErrorMessage(req.ID, WireError(err))
	}
	if req.Method == appwire.MethodInitialize {
		c.setInitialized()
	}
	return appwire.ResponseMessage(req.ID, result)
}

func (c *Connection) handleNotification(notification appwire.Notification) appwire.Message {
	switch notification.Method {
	case appwire.MethodInitialized:
		return appwire.Message{}
	default:
		return appwire.Message{}
	}
}

type connectionContextKey struct{}
type requestIDContextKey struct{}
type subscriptionAdmissionFailureContextKey struct{}
type subscriptionAdmissionResolverPanicContextKey struct{}

// CaptureSubscriptionHandoff is the one-shot continuation paired with a
// buffering subscription capture. Commit runs only after the matching response
// enters the connection send queue. Abort runs after that capture has been
// withdrawn because its response cannot be enqueued or a newer lifecycle
// supersedes it.
type CaptureSubscriptionHandoff struct {
	Commit func()
	Abort  func()
}

type hydrationResponseFinalizer struct {
	server                 *Server
	conn                   *Connection
	responseID             string
	threadID               string
	generation             uint64
	replace                bool
	rollback               subscriptionCaptureRollback
	handoff                CaptureSubscriptionHandoff
	releaseOnErrorResponse bool
}

func (f *hydrationResponseFinalizer) commit() {
	f.server.releaseHydration(f.conn, f.threadID, f.generation)
}

func (f *hydrationResponseFinalizer) commitAfterRelease() {
	if f.handoff.Commit != nil {
		f.handoff.Commit()
	}
}

func (f *hydrationResponseFinalizer) abort() {
	f.server.withdrawHydration(f.conn, f.threadID, f.generation, f.rollback)
}

func (f *hydrationResponseFinalizer) abortAfterWithdrawal() {
	if f.handoff.Abort != nil {
		f.handoff.Abort()
	}
}

func Subscribe(ctx context.Context, threadID string) bool {
	conn, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok || conn == nil {
		return true
	}
	if threadID == "" {
		return false
	}
	server := conn.server
	if server.beforeSubscriptionRegistration != nil {
		server.beforeSubscriptionRegistration()
	}
	server.projectionMu.Lock()
	defer server.projectionMu.Unlock()
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.conns[conn.id] != conn {
		return false
	}
	server.subs.Subscribe(conn.id, threadID)
	return true
}

// Unsubscribe drops the calling connection's subscription to one thread so a
// browser switching views stops receiving its live updates and the relay can
// idle out once no connection remains. Quietly a no-op when there is nothing
// to remove (no connection on the context, an empty threadID, a connection
// already replaced): teardown never needs to distinguish those. Unlike
// Subscribe it takes no projection gate: removing a subscription only
// shrinks delivery.
func Unsubscribe(ctx context.Context, threadID string) {
	conn, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok || conn == nil || threadID == "" {
		return
	}
	server := conn.server
	conn.cancelSubscriptionAdmissions(threadID)
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.conns[conn.id] != conn {
		return
	}
	server.subs.Unsubscribe(conn.id, threadID)
}

type subscriptionLifecycleContextKey struct{}

// UnsubscribeLifecycle uses the identity resolved at ordered request ingress.
// Without a resolved identity, preserve the caller's raw delivery-key fallback.
func UnsubscribeLifecycle(ctx context.Context, threadID string) {
	conn, _ := ctx.Value(connectionContextKey{}).(*Connection)
	if conn == nil || threadID == "" {
		return
	}
	key, _ := ctx.Value(subscriptionLifecycleContextKey{}).(string)
	if key == "" {
		Unsubscribe(ctx, threadID)
		return
	}
	threadID = key
	conn.cancelSubscriptionAdmissions(threadID)
	conn.server.mu.Lock()
	defer conn.server.mu.Unlock()
	if conn.server.conns[conn.id] == conn {
		conn.server.subs.UnsubscribeLifecycle(conn.id, threadID)
	}
}

// SubscribeWithAdmission installs an immediate subscription and consumes the
// request's admission at one boundary. Registration gates precede admissionMu,
// matching capture; unsubscribe cannot pass eligibility before installation.
// An optional expectedLifecycle is the identity already resolved by the handler.
// A mismatch rejects without consuming admission or changing subscriptions.
func SubscribeWithAdmission(ctx context.Context, threadID string, replace bool, expectedLifecycle ...string) error {
	conn, _ := ctx.Value(connectionContextKey{}).(*Connection)
	if conn == nil {
		return nil
	}
	if threadID == "" {
		return context.Canceled
	}
	server := conn.server
	if server.beforeSubscriptionRegistration != nil {
		server.beforeSubscriptionRegistration()
	}
	conn.hydrationMu.Lock()
	defer conn.hydrationMu.Unlock()
	server.projectionMu.Lock()
	defer server.projectionMu.Unlock()
	server.deliveryMu.Lock()
	defer server.deliveryMu.Unlock()
	conn.admissionMu.Lock()
	defer conn.admissionMu.Unlock()
	responseID, _ := ctx.Value(requestIDContextKey{}).(string)
	admission, allowed := conn.claimSubscriptionAdmission(responseID)
	if !allowed || (admission != nil && len(expectedLifecycle) != 0 && admission.threadID != expectedLifecycle[0]) {
		return appwire.SessionUnavailable("thread subscription is unavailable")
	}
	server.mu.RLock()
	defer server.mu.RUnlock()
	if server.conns[conn.id] != conn {
		return context.Canceled
	}
	owner := threadID
	if admission != nil {
		owner = admission.threadID
	}
	server.subs.subscribeOwned(conn.id, threadID, owner, replace)
	if admission != nil {
		delete(conn.pendingAdmissions, admission)
	}
	return nil
}

func ReplaceSubscriptions(ctx context.Context, threadID string) bool {
	conn, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok || conn == nil {
		return true
	}
	server := conn.server
	if server.beforeSubscriptionRegistration != nil {
		server.beforeSubscriptionRegistration()
	}
	server.projectionMu.Lock()
	defer server.projectionMu.Unlock()
	server.deliveryMu.Lock()
	defer server.deliveryMu.Unlock()
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.conns[conn.id] != conn {
		return false
	}
	server.subs.ReplaceConnectionSubscriptions(conn.id, threadID)
	return true
}

// SubscriptionTarget is one resolved delivery/lifecycle pair. Capture resolves
// it under the projection and delivery gates, before taking admissionMu.
// LifecycleKey is compared with ingress admission ownership, never substituted
// for the delivery key or used to retarget a pending admission.
type SubscriptionTarget struct {
	ThreadID     string
	LifecycleKey string
}

// CaptureSubscription registers a buffering hydration generation, captures the
// authoritative snapshot under the projection gate, and arranges to release
// only post-cut records after the matching response enters the connection's
// send queue. A false snapshot result restores the previous ownership.
// When supplied, resolveTarget replaces threadID and atomically resolves D+A1
// under the registration gates, before admission validation or hydration changes.
func CaptureSubscription(
	ctx context.Context,
	replace bool,
	threadID func() string,
	currentSequence func() uint64,
	snapshot func() bool,
	resolveTarget ...func() SubscriptionTarget,
) bool {
	return captureSubscription(
		ctx,
		replace,
		threadID,
		currentSequence,
		snapshot,
		CaptureSubscriptionHandoff{},
		true,
		resolveTarget...,
	)
}

// CaptureSubscriptionWithHandoff extends CaptureSubscription with the narrow
// downstream response boundary required by a materialized upstream handoff.
// It is deliberately specific to hydration rather than a general response
// transaction mechanism.
func CaptureSubscriptionWithHandoff(
	ctx context.Context,
	replace bool,
	threadID func() string,
	currentSequence func() uint64,
	snapshot func() bool,
	handoff CaptureSubscriptionHandoff,
	resolveTarget ...func() SubscriptionTarget,
) bool {
	return captureSubscription(
		ctx,
		replace,
		threadID,
		currentSequence,
		snapshot,
		handoff,
		false,
		resolveTarget...,
	)
}

func captureSubscription(
	ctx context.Context,
	replace bool,
	threadID func() string,
	currentSequence func() uint64,
	snapshot func() bool,
	handoff CaptureSubscriptionHandoff,
	releaseOnErrorResponse bool,
	resolveTarget ...func() SubscriptionTarget,
) bool {
	conn, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok || conn == nil {
		if handoff.Commit == nil && handoff.Abort == nil {
			if len(resolveTarget) != 0 && resolveTarget[0]().ThreadID == "" {
				return false
			}
			return snapshot()
		}
		if handoff.Abort != nil {
			handoff.Abort()
		}
		return false
	}
	server := conn.server
	if server.beforeSubscriptionRegistration != nil {
		server.beforeSubscriptionRegistration()
	}
	conn.hydrationMu.Lock()
	server.projectionMu.Lock()
	server.deliveryMu.Lock()
	var superseded []*hydrationResponseFinalizer
	abortAfterUnlock := func() bool {
		server.deliveryMu.Unlock()
		server.projectionMu.Unlock()
		conn.hydrationMu.Unlock()
		for _, finalizer := range superseded {
			finalizer.abortAfterWithdrawal()
		}
		if handoff.Abort != nil {
			handoff.Abort()
		}
		return false
	}

	server.mu.RLock()
	registered := server.conns[conn.id] == conn
	server.mu.RUnlock()
	if !registered {
		return abortAfterUnlock()
	}
	var target SubscriptionTarget
	if len(resolveTarget) != 0 {
		target = resolveTarget[0]()
	} else {
		target.ThreadID = threadID()
	}
	targetThreadID := target.ThreadID
	if targetThreadID == "" {
		return abortAfterUnlock()
	}
	responseID, _ := ctx.Value(requestIDContextKey{}).(string)
	conn.admissionMu.Lock()
	admission, allowed := conn.claimSubscriptionAdmission(responseID)
	if admission != nil && (!allowed || (len(resolveTarget) != 0 && admission.threadID != target.LifecycleKey)) {
		conn.admissionMu.Unlock()
		return abortAfterUnlock()
	}
	if conn.server.beforeSubscriptionBeginBuffered != nil {
		conn.server.beforeSubscriptionBeginBuffered()
	}
	superseded = conn.takeSupersededHydrations(responseID, targetThreadID, replace)
	for _, finalizer := range superseded {
		server.subs.withdrawBuffered(
			conn.id,
			finalizer.threadID,
			finalizer.generation,
			finalizer.rollback,
		)
	}
	server.nextHydrationGeneration++
	generation := server.nextHydrationGeneration
	owner := targetThreadID
	if admission != nil {
		owner = admission.threadID
	}
	rollback := server.subs.beginBuffered(conn.id, targetThreadID, replace, generation, owner)
	if admission != nil {
		delete(conn.pendingAdmissions, admission)
	}
	conn.admissionMu.Unlock()
	if !snapshot() {
		server.subs.withdrawBuffered(conn.id, targetThreadID, generation, rollback)
		return abortAfterUnlock()
	}
	if !server.subs.SetCut(conn.id, targetThreadID, generation, currentSequence()) {
		server.subs.withdrawBuffered(conn.id, targetThreadID, generation, rollback)
		return abortAfterUnlock()
	}
	if ctx.Err() != nil {
		server.subs.withdrawBuffered(conn.id, targetThreadID, generation, rollback)
		return abortAfterUnlock()
	}
	conn.addHydration(&hydrationResponseFinalizer{
		server:                 server,
		conn:                   conn,
		responseID:             responseID,
		threadID:               targetThreadID,
		generation:             generation,
		replace:                replace,
		rollback:               rollback,
		handoff:                handoff,
		releaseOnErrorResponse: releaseOnErrorResponse,
	})
	server.deliveryMu.Unlock()
	server.projectionMu.Unlock()
	conn.hydrationMu.Unlock()
	for _, finalizer := range superseded {
		finalizer.abortAfterWithdrawal()
	}
	return true
}

func (s *Server) releaseHydration(conn *Connection, threadID string, generation uint64) {
	s.projectionMu.Lock()
	records, ok := s.subs.Release(conn.id, threadID, generation)
	s.deliveryMu.Lock()
	s.projectionMu.Unlock()
	disconnected := false
	if ok {
		for _, record := range records {
			msg := appwire.Message{Notification: &record.Notification}
			if !conn.enqueue(msg) {
				disconnected = true
				break
			}
		}
	}
	s.deliveryMu.Unlock()
	if disconnected {
		s.evictSlowConsumer(conn, "a hydration release replay")
	}
}

func (s *Server) withdrawHydration(
	conn *Connection,
	threadID string,
	generation uint64,
	rollback subscriptionCaptureRollback,
) {
	s.projectionMu.Lock()
	s.deliveryMu.Lock()
	s.subs.withdrawBuffered(conn.id, threadID, generation, rollback)
	s.deliveryMu.Unlock()
	s.projectionMu.Unlock()
}

func Notify(ctx context.Context, method string, params any) {
	conn, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok || conn == nil || method == "" {
		return
	}
	conn.enqueue(appwire.NotificationMessage(method, params))
}

func (c *Connection) isInitialized() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.initialized
}

func (c *Connection) setInitialized() {
	c.mu.Lock()
	c.initialized = true
	c.mu.Unlock()
}

// concurrentDispatchMethod reports whether a request method leaves the
// receive loop for its own goroutine. The set is exactly the known-slow read
// methods — the ones that scan a full transcript and can park for a while
// (thread/read is the handler the original ping-starvation bug named;
// thread/turns/list and evener/subagentPreview walk the same saved
// transcripts). Everything else — every mutation and every other read — is
// bounded work and runs inline, keeping the per-connection ordering those
// handlers were written against. A method added here must be safe to run
// out of order against every other request on the connection.
func concurrentDispatchMethod(method string) bool {
	switch method {
	case appwire.MethodThreadRead, appwire.MethodThreadTurnsList, appwire.MethodEvenerSubagentPreview:
		return true
	}
	return false
}

// receiveInbound applies the connection's inbound policy to one frame on
// behalf of the transport's receive loop, which holds only transport
// concerns: a ping request is answered inline through the shared
// handleAndEnqueue barrier, bypassing the request queue — one ping
// implementation, one panic barrier, no hand-built response beside the real
// path that could drift from it — and every other frame is enqueued for the
// serial worker. False means the connection died while blocked on a full
// queue and the loop should return. A second transport inherits the whole
// policy by driving this same entry point.
//
// The inline ping response enqueues through enqueueResponse, which parks on
// a full outbound channel until the send loop drains it or the write-timeout
// cascade cancels the connection: ping liveness is guaranteed against
// handler behavior, not against a peer that stopped draining its own socket.
func (c *Connection) receiveInbound(ctx context.Context, msg appwire.Message) bool {
	if msg.Request != nil && msg.Request.Method == appwire.MethodPing {
		c.handleAndEnqueue(ctx, msg)
		return true
	}
	return c.enqueueRequest(ctx, msg)
}

// enqueueRequest pushes one inbound frame onto the connection's bounded
// request queue on behalf of the transport's receive loop, which is the only
// producer. A full queue blocks until the worker frees a slot or the
// connection context ends; false means the connection died while blocked and
// the loop should return. Blocking is the pressure valve: the parked loop
// stops calling Recv, inbound frames accumulate in the kernel socket buffer,
// and TCP flow control eventually reaches the client — no wire error, no
// eviction. A client that pipelines deeper than the queue experiences
// exactly the ordering it asked for, applied at the transport instead of in
// server memory.
func (c *Connection) enqueueRequest(ctx context.Context, msg appwire.Message) bool {
	select {
	case c.requests <- msg:
		return true
	default:
	}
	// Saturation is a healthy server applying flow control, but it is also
	// the one state where ping and dead-peer detection wait on the client's
	// own backlog, so its first occurrence per connection gets an advisory —
	// the same channel evictSlowConsumer uses, and the proportionate version
	// of a metric this package does not have.
	if !c.queueSaturationAdvised {
		c.queueSaturationAdvised = true
		c.server.logf("appserver: connection %s inbound request queue is full (%d frames); blocking the receive loop until the worker frees a slot", c.id, cap(c.requests))
	}
	if c.server.blockedEnqueue != nil {
		c.server.blockedEnqueue()
	}
	select {
	case c.requests <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

// runWorker is the connection's serial worker: the only consumer of the
// request queue, started by the transport beside its send loop. It drains
// the queue strictly in arrival order and applies the dispatch policy in
// executeOrdered, so per-connection ordering is preserved for every queued
// frame while the receive loop stays parked in Recv — no handler can starve
// the transport's ping answering, close handling, or dead-peer detection.
//
// The ordering contract, for two requests A before B on one connection:
// earlier serial requests block all later work; nothing waits for a slow
// read; ping (answered in the receive loop, never queued) waits for no
// queued work. Slow reads are unordered among themselves, and a serial
// request issued after a slow read runs without waiting for it. Responses
// always pair by request id, and only the sequence-cut discipline governs
// how notifications interleave with a hydration response.
//
// Cancellation: the connection context ending is the only cancellation this
// transport has. No request begins executing after the worker has observed
// cancellation, and it observes at every dequeue — the post-dequeue re-check
// below, because select chooses randomly when both cases are ready. Requests
// still queued behind the observation point never execute; a request that
// never started has no side effects and no peer remains to answer.
func (c *Connection) runWorker(ctx context.Context) {
	defer close(c.workerExited)
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-c.requests:
			if c.server.afterWorkerDequeue != nil {
				c.server.afterWorkerDequeue(msg)
			}
			// select chooses randomly when both cases are ready, so a
			// canceled connection can still win a dequeue; re-check before
			// executing so no request starts after cancellation is
			// observable here.
			if ctx.Err() != nil {
				return
			}
			c.executeOrdered(ctx, msg)
		}
	}
}

// purgeRequestQueue discards every frame still buffered in the request queue
// at teardown. The transport calls it after its receive loop — the queue's
// only producer — has returned and the connection context is canceled; that
// ownership rule is what makes draining safe. The worker may race the purge
// by winning a dequeue, but its post-dequeue re-check discards the message
// just the same: both sides only ever discard. Without the purge, an
// orphaned handler that ignores its canceled context would retain the worker
// goroutine, through it the Connection, and through that up to a full
// queue's worth of decoded frames; after it, such a handler retains exactly
// the one frame it is executing.
//
// A non-empty purge reports one bounded advisory line: the count plus a
// per-method tally, catalog methods by name and everything else aggregated
// as unknown — never params, which can carry user content. This is an
// aggregate teardown advisory, not request-level attribution; "did my
// mutation run?" belongs to the ClientMutationID dedup on reconnect.
func (c *Connection) purgeRequestQueue() {
	discarded := 0
	tally := map[string]int{}
drain:
	for {
		select {
		case msg := <-c.requests:
			discarded++
			// Tally only cataloged names verbatim: the method field is
			// client-controlled and the transport read limit admits very
			// large strings, so an uncataloged value could carry arbitrary
			// size or control characters into the log.
			name := methodOf(msg)
			if !appwire.KnownWireName(name) {
				name = "unknown"
			}
			tally[name]++
		default:
			break drain
		}
	}
	if discarded == 0 {
		return
	}
	c.server.logf("appserver: connection %s discarded %d undispatched queued frames at teardown (%s)", c.id, discarded, formatTally(tally))
}

// formatTally renders a per-method tally as a deterministic "name=count"
// list, shared by the teardown purge and the stall advisory.
func formatTally(tally map[string]int) string {
	names := make([]string, 0, len(tally))
	for name := range tally {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, tally[name]))
	}
	return strings.Join(parts, " ")
}

// executeOrdered applies the dispatch policy to one dequeued frame: the
// slow-read methods concurrentDispatchMethod names spawn onto their own
// goroutine — a full-transcript read cannot head-of-line block the
// connection — and everything else, initialize and notifications included,
// executes inline in the worker so handlers keep the per-connection ordering
// they were written against.
//
// Ordering constraints in detail:
//
//   - initialize must be the first request. Pre-initialize frames ride the
//     queue like everything else; the worker executes them in order, and
//     HandleMessage's gate answers non-initialize requests with "initialize
//     required" while the handshake itself completes — response enqueued —
//     before any later frame is dequeued. Later dispatch therefore cannot
//     observe a half-initialized connection; the isInitialized check below
//     is exact rather than racy because the worker is the goroutine that
//     sets it.
//   - responses enter the connection send queue through the same
//     enqueueResponse path on both dispatch modes, so hydration capture
//     commit/abort ordering is unchanged.
//
// Error responses from enqueueResponse are terminal for the connection, but
// a handler goroutine must not tear the worker down out from under it;
// canceling the shared context is enough — the worker exits at its next
// dequeue and the receive loop's next Recv fails into normal close handling.
func (c *Connection) executeOrdered(ctx context.Context, msg appwire.Message) {
	var resolverPanic bool
	var resolvedKey string
	var resolved bool
	if resolve := c.server.cfg.SubscriptionAdmissionResolver; resolve != nil && msg.Request != nil && c.isInitialized() && (msg.Request.Method == appwire.MethodThreadRead || msg.Request.Method == appwire.MethodThreadUnsubscribe) {
		resolvedKey, resolved, resolverPanic = safeResolveAdmission(c.server, resolve, msg)
		if resolverPanic {
			ctx = context.WithValue(ctx, subscriptionAdmissionResolverPanicContextKey{}, true)
		} else if msg.Request.Method == appwire.MethodThreadRead && threadReadSubscribes(msg.Request.Params) && (!resolved || resolvedKey == "") {
			ctx = context.WithValue(ctx, subscriptionAdmissionFailureContextKey{}, "subscription admission is unavailable")
		}
	}
	if msg.Request != nil && msg.Request.Method == appwire.MethodThreadUnsubscribe && c.isInitialized() {
		if resolved && resolvedKey != "" {
			c.cancelSubscriptionAdmissions(resolvedKey)
			ctx = context.WithValue(ctx, subscriptionLifecycleContextKey{}, resolvedKey)
		}
	}
	if msg.Request != nil && concurrentDispatchMethod(msg.Request.Method) && c.isInitialized() {
		method := msg.Request.Method
		var admission *subscriptionAdmission
		if method == appwire.MethodThreadRead {
			if resolved && resolvedKey != "" {
				admission = c.beginSubscriptionAdmission(requestIDKey(msg.Request.ID), resolvedKey)
			}
		}
		if !c.acquireSlowReadSlot(ctx, method) {
			c.finishSubscriptionAdmission(admission)
			// Canceled while dequeued-awaiting-dispatch-capacity; the worker
			// loop observes the same cancellation at its next select and
			// exits without executing anything further.
			return
		}
		go func() {
			defer c.finishSubscriptionAdmission(admission)
			defer c.releaseSlowReadSlot(method)
			c.handleAndEnqueue(ctx, msg)
		}()
		return
	}
	c.handleAndEnqueue(ctx, msg)
}

func safeResolveAdmission(server *Server, resolve func(appwire.Message) (string, bool), msg appwire.Message) (key string, ok, panicked bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			server.panicLogf("appserver: panic resolving subscription admission for %s: %v\n%s", methodOf(msg), recovered, debug.Stack())
			key, ok, panicked = "", false, true
		}
	}()
	key, ok = resolve(msg)
	return key, ok, false
}

func threadReadSubscribes(raw json.RawMessage) bool {
	var params struct {
		Subscribe bool `json:"subscribe"`
	}
	return json.Unmarshal(raw, &params) == nil && params.Subscribe
}

// acquireSlowReadSlot takes one slowReadDispatchCap slot on behalf of the
// worker before it spawns a slow read — the request's third lifecycle state,
// dequeued awaiting dispatch capacity. A full cap parks the worker here, so
// the blocked slow read and every request queued behind it wait for one of
// the in-flight reads to finish: the design's second deliberate scheduling
// change (ping bypass is the first), blocking backpressure like the request
// queue and explicitly not the dead 128-slot limiter's wire error. The
// acquire gets the same cancellation discipline as the dequeue: it selects on
// ctx.Done so teardown is never held, and because select chooses randomly
// when a slot release and cancellation are simultaneously ready, it re-checks
// ctx.Err after acquiring — releasing the slot and reporting false on cancel,
// so no slow read starts after cancellation is observable here.
//
// Saturation is observable like queue saturation: the first blocked acquire
// per connection reports through Server.logf. And because the ping bypass
// keeps the connection's heartbeat answering while the serial lane is dead
// behind a saturated cap — a wedge PR #667 would have surfaced as a stalled
// heartbeat — a single acquire parked past the stall threshold reports again,
// naming the wait duration and the in-flight methods, so a wedged lane has an
// operational signature even though the connection looks healthy.
func (c *Connection) acquireSlowReadSlot(ctx context.Context, method string) bool {
	acquired := false
	select {
	case c.slowReadSlots <- struct{}{}:
		acquired = true
	default:
	}
	if !acquired {
		if !c.capSaturationAdvised {
			c.capSaturationAdvised = true
			c.server.logf("appserver: connection %s slow-read dispatch cap is full (%d in flight); holding the next slow read until one finishes", c.id, cap(c.slowReadSlots))
		}
		start := time.Now()
		stall := time.NewTimer(c.server.slowReadStallThreshold)
		defer stall.Stop()
		for !acquired {
			select {
			case c.slowReadSlots <- struct{}{}:
				acquired = true
			case <-stall.C:
				c.server.logf("appserver: connection %s slow-read acquire for %s has been parked %s behind in-flight reads (%s)", c.id, method, time.Since(start).Round(time.Second), c.inflightSlowReads())
			case <-ctx.Done():
				return false
			}
		}
	}
	if c.server.afterSlowReadAcquire != nil {
		c.server.afterSlowReadAcquire()
	}
	if ctx.Err() != nil {
		<-c.slowReadSlots
		return false
	}
	c.slowReadMu.Lock()
	c.slowReadInflight[method]++
	c.slowReadMu.Unlock()
	return true
}

// releaseSlowReadSlot frees the cap slot when a slow read's handleAndEnqueue
// returns — completion, not response delivery, is what drains the cap.
func (c *Connection) releaseSlowReadSlot(method string) {
	c.slowReadMu.Lock()
	c.slowReadInflight[method]--
	if c.slowReadInflight[method] <= 0 {
		delete(c.slowReadInflight, method)
	}
	c.slowReadMu.Unlock()
	<-c.slowReadSlots
}

// inflightSlowReads renders the per-method tally the stall advisory names.
// Every key is a concurrentDispatchMethod member, so the strings are ours,
// not client-controlled.
func (c *Connection) inflightSlowReads() string {
	c.slowReadMu.Lock()
	defer c.slowReadMu.Unlock()
	return formatTally(c.slowReadInflight)
}

// handleAndEnqueue runs one request through HandleMessage and enqueues its
// response. It is the panic barrier for handler code: a panicking handler is
// logged with its stack and answered with an InternalError response, and the
// connection lives on. Handlers dispatched on their own goroutine have no
// other recover between them and the runtime (the net/http barrier only
// covers the receive loop's goroutine), and the inline path shares the
// barrier so both dispatch modes contain a panic identically.
func (c *Connection) handleAndEnqueue(ctx context.Context, msg appwire.Message) {
	defer func() {
		if r := recover(); r != nil {
			c.server.panicLogf("appserver: panic handling %s: %v\n%s", methodOf(msg), r, debug.Stack())
			if msg.Request != nil {
				c.enqueueDispatched(ctx, appwire.ErrorMessage(msg.Request.ID, appwire.InternalError("internal error handling request")))
			}
		}
	}()
	c.enqueueDispatched(ctx, c.HandleMessage(ctx, msg))
}

// methodOf names a message's method for the panic log; a frame that is
// neither request nor notification cannot reach a handler, but the barrier
// covers it anyway.
func methodOf(msg appwire.Message) string {
	switch {
	case msg.Request != nil:
		return msg.Request.Method
	case msg.Notification != nil:
		return msg.Notification.Method
	}
	return "invalid frame"
}

// enqueueDispatched is the one enqueue body every dispatch path shares:
// enqueue a response, canceling the connection when it cannot be enqueued.
func (c *Connection) enqueueDispatched(ctx context.Context, resp appwire.Message) {
	if resp.Kind() == appwire.MessageInvalid {
		return
	}
	if err := c.enqueueResponse(ctx, resp); err != nil {
		c.cancelContext()
	}
}
