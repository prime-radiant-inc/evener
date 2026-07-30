package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"primeradiant.com/serf/appwire"
)

type ServerConfig struct {
	ServerName string
	Version    string
	SourceID   string
	Features   appwire.FeatureSet
	// AdapterNativeInitialize keeps the shared JSON-RPC server usable in tests
	// for adapters whose upstream protocol owns a different initialize shape.
	AdapterNativeInitialize bool
}

type Server struct {
	cfg                            ServerConfig
	router                         *Router
	subs                           *Subscriptions
	projectionMu                   sync.Mutex
	deliveryMu                     sync.Mutex
	nextHydrationGeneration        uint64
	routedSeq                      uint64
	mu                             sync.RWMutex
	conns                          map[string]*Connection
	afterUnregisterDelete          func()
	beforeSubscriptionRegistration func()
	afterBroadcastConnectionLookup func(*Connection)
}

func NewServer(cfg ServerConfig) *Server {
	s := &Server{
		cfg:    cfg,
		router: NewRouter(),
		subs:   NewSubscriptions(),
		conns:  map[string]*Connection{},
	}
	HandleTyped(s.router, appwire.MethodInitialize, s.initialize)
	return s
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
		s.unregisterConnection(conn)
	}
}

func (s *Server) NewConnection(id string) *Connection {
	return &Connection{id: id, server: s, send: make(chan appwire.Message, 32)}
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
		s.unregisterConnection(conn)
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
// notifications such as serf/auth/updated and serf/launch/updated.
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
			s.unregisterConnection(conn)
		}
	}
}

func (s *Server) SubscriberCount(threadID string) int {
	return s.subs.ConnectionCount(threadID)
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
	return appwire.InitializeResponse{
		ServerInfo:      appwire.ServerInfo{Name: s.cfg.ServerName, Version: s.cfg.Version},
		ProtocolVersion: protocolVersion,
		SourceID:        s.cfg.SourceID,
		Features:        s.cfg.Features,
	}, nil
}

type Connection struct {
	id          string
	server      *Server
	send        chan appwire.Message
	sendMu      sync.RWMutex
	sendClosed  bool
	mu          sync.RWMutex
	initialized bool
	cancel      context.CancelFunc
	responseMu  sync.Mutex
	hydrations  map[string]*hydrationResponseFinalizer
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
	c.sendMu.RLock()
	if c.sendClosed {
		finalizer := c.takeHydration(responseID)
		c.sendMu.RUnlock()
		if finalizer != nil {
			finalizer.abort()
		}
		return context.Canceled
	}
	select {
	case c.send <- msg:
		finalizer := c.takeHydration(responseID)
		c.sendMu.RUnlock()
		if finalizer != nil {
			if responseSucceeded || finalizer.releaseOnErrorResponse {
				finalizer.commit()
			} else {
				finalizer.abort()
			}
		}
		return nil
	case <-ctx.Done():
		finalizer := c.takeHydration(responseID)
		c.sendMu.RUnlock()
		if finalizer != nil {
			finalizer.abort()
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
	// ping is a connection-level keepalive (the browser's app-level heartbeat,
	// since browsers can't send WS ping frames from JS). Answer it directly,
	// before the initialize gate and without the router, so it stays cheap and
	// can't be starved by a busy handler.
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
	if f.handoff.Commit != nil {
		f.handoff.Commit()
	}
}

func (f *hydrationResponseFinalizer) abort() {
	f.server.withdrawHydration(f.conn, f.threadID, f.generation, f.rollback)
	f.abortAfterWithdrawal()
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

// CaptureSubscription registers a buffering hydration generation, captures the
// authoritative snapshot under the projection gate, and arranges to release
// only post-cut records after the matching response enters the connection's
// send queue. A false snapshot result restores the previous ownership.
func CaptureSubscription(
	ctx context.Context,
	replace bool,
	threadID func() string,
	currentSequence func() uint64,
	snapshot func() bool,
) bool {
	return captureSubscription(
		ctx,
		replace,
		threadID,
		currentSequence,
		snapshot,
		CaptureSubscriptionHandoff{},
		true,
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
) bool {
	return captureSubscription(
		ctx,
		replace,
		threadID,
		currentSequence,
		snapshot,
		handoff,
		false,
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
) bool {
	conn, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok || conn == nil {
		if handoff.Commit == nil && handoff.Abort == nil {
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
	server.projectionMu.Lock()
	server.deliveryMu.Lock()
	var superseded []*hydrationResponseFinalizer
	abortAfterUnlock := func() bool {
		server.deliveryMu.Unlock()
		server.projectionMu.Unlock()
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
	targetThreadID := threadID()
	if targetThreadID == "" {
		return abortAfterUnlock()
	}
	responseID, _ := ctx.Value(requestIDContextKey{}).(string)
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
	rollback := server.subs.beginBuffered(conn.id, targetThreadID, replace, generation)
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
		s.unregisterConnection(conn)
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
