package appserver

import (
	"context"
	"sync"

	"primeradiant.com/serf/internal/appwire"
)

type ServerConfig struct {
	ServerName string
	Version    string
	SourceID   string
	Features   appwire.FeatureSet
}

type Server struct {
	cfg    ServerConfig
	router *Router
	subs   *Subscriptions
	mu     sync.RWMutex
	conns  map[string]*Connection
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

func (s *Server) unregisterConnection(id string) {
	s.mu.Lock()
	conn := s.conns[id]
	delete(s.conns, id)
	if conn != nil {
		conn.cancelContext()
		conn.closeSend()
	}
	s.mu.Unlock()
	s.subs.RemoveConnection(id)
}

func (s *Server) Broadcast(threadID, method string, params any) {
	msg := appwire.NotificationMessage(method, params)
	for _, connID := range s.subs.Connections(threadID) {
		s.mu.RLock()
		conn := s.conns[connID]
		s.mu.RUnlock()
		if conn != nil {
			if !conn.enqueue(msg) {
				s.unregisterConnection(connID)
			}
		}
	}
}

func (s *Server) SubscriberCount(threadID string) int {
	return s.subs.ConnectionCount(threadID)
}

func (s *Server) initialize(context.Context, appwire.InitializeParams) (appwire.InitializeResponse, error) {
	return appwire.InitializeResponse{
		ServerInfo:      appwire.ServerInfo{Name: s.cfg.ServerName, Version: s.cfg.Version},
		ProtocolVersion: appwire.ProtocolVersion,
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
	if msg.Request == nil {
		return appwire.ErrorMessage(appwire.NewIntID(0), appwire.InvalidRequest("request message required"))
	}
	req := *msg.Request
	if !c.isInitialized() && req.Method != appwire.MethodInitialize {
		return appwire.ErrorMessage(req.ID, appwire.InvalidRequest("initialize required"))
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

type connectionContextKey struct{}

func Subscribe(ctx context.Context, threadID string) {
	conn, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok || conn == nil || threadID == "" {
		return
	}
	conn.Subscribe(threadID)
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
