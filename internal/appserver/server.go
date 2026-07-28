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
	msg := appwire.NotificationMessage(method, params)
	for _, connID := range s.subs.Connections(threadID) {
		s.mu.RLock()
		conn := s.conns[connID]
		s.mu.RUnlock()
		if conn != nil {
			if s.afterBroadcastConnectionLookup != nil {
				s.afterBroadcastConnectionLookup(conn)
			}
			if !conn.enqueue(msg) {
				s.unregisterConnection(conn)
			}
		}
	}
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
}

func (c *Connection) ID() string {
	return c.id
}

func (c *Connection) Subscribe(threadID string) {
	c.server.subs.Subscribe(c.id, threadID)
}

func (c *Connection) ReplaceSubscriptions(threadID string) {
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
	defer c.sendMu.RUnlock()
	if c.sendClosed {
		return context.Canceled
	}
	select {
	case c.send <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
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
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.conns[conn.id] != conn {
		return false
	}
	server.subs.ReplaceConnectionSubscriptions(conn.id, threadID)
	return true
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
