package appserver

import (
	"context"
	"fmt"
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
	defer s.projectionMu.Unlock()
	s.unregisterConnectionLocked(conn)
}

func (s *Server) unregisterConnectionLocked(conn *Connection) {
	s.mu.Lock()
	if s.conns[conn.id] != conn {
		s.mu.Unlock()
		return
	}
	delete(s.conns, conn.id)
	conn.cancelContext()
	conn.closeSend()
	if s.afterUnregisterDelete != nil {
		s.afterUnregisterDelete()
	}
	s.subs.RemoveConnection(conn.id)
	s.mu.Unlock()
}

func (s *Server) Broadcast(threadID, method string, params any) {
	s.projectionMu.Lock()
	record := SequencedNotification{
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

func (s *Server) routeSequencedLocked(record SequencedNotification) []notificationDelivery {
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
	id            string
	server        *Server
	send          chan appwire.Message
	sendMu        sync.RWMutex
	sendClosed    bool
	mu            sync.RWMutex
	initialized   bool
	cancel        context.CancelFunc
	responseMu    sync.Mutex
	afterResponse []func()
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
	c.sendMu.RLock()
	if c.sendClosed {
		c.sendMu.RUnlock()
		return context.Canceled
	}
	select {
	case c.send <- msg:
		c.sendMu.RUnlock()
		c.runAfterResponse()
		return nil
	case <-ctx.Done():
		c.sendMu.RUnlock()
		return ctx.Err()
	}
}

func (c *Connection) addAfterResponse(fn func()) {
	c.responseMu.Lock()
	c.afterResponse = append(c.afterResponse, fn)
	c.responseMu.Unlock()
}

func (c *Connection) runAfterResponse() {
	c.responseMu.Lock()
	callbacks := c.afterResponse
	c.afterResponse = nil
	c.responseMu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
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
	result, err := c.server.router.Dispatch(context.WithValue(ctx, connectionContextKey{}, c), req)
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
	conn, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok || conn == nil {
		return snapshot()
	}
	server := conn.server
	if server.beforeSubscriptionRegistration != nil {
		server.beforeSubscriptionRegistration()
	}
	server.projectionMu.Lock()
	defer server.projectionMu.Unlock()
	server.deliveryMu.Lock()
	defer server.deliveryMu.Unlock()

	server.mu.RLock()
	registered := server.conns[conn.id] == conn
	server.mu.RUnlock()
	if !registered {
		return false
	}
	targetThreadID := threadID()
	if targetThreadID == "" {
		return false
	}
	server.nextHydrationGeneration++
	generation := server.nextHydrationGeneration
	previous := server.subs.BeginBuffered(conn.id, targetThreadID, replace, generation)
	if !snapshot() {
		server.subs.RestoreConnection(conn.id, previous)
		return false
	}
	if !server.subs.SetCut(conn.id, targetThreadID, generation, currentSequence()) {
		server.subs.RestoreConnection(conn.id, previous)
		return false
	}
	conn.addAfterResponse(func() {
		server.releaseHydration(conn, targetThreadID, generation)
	})
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
