package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"primeradiant.com/evener/appwire"
)

const sessionToolsAuthorityConflict = "local and identified peer Manager bindings cannot share a connection"

// IdentifySessionToolsPeer records an honest cooperating peer's persisted ID
// only on this initialized authenticated connection. The caller must first
// verify bridge origin and negotiated support. A token alone authenticates
// access, not the particular hub named by this nonsecret ID.
func IdentifySessionToolsPeer(ctx context.Context, id string) error {
	c, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok || !c.isInitialized() {
		return appwire.InvalidRequest("initialized peer connection is required")
	}
	if strings.TrimSpace(id) == "" {
		return appwire.InvalidParams("hub identity is required")
	}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if c.sessionTools != nil {
		return appwire.Conflict(sessionToolsAuthorityConflict)
	}
	c.peerMu.Lock()
	defer c.peerMu.Unlock()
	if c.peerHubID != "" && c.peerHubID != id {
		return appwire.Conflict("connection already identified as another hub")
	}
	c.peerHubID = id
	return nil
}

// SessionToolsPeerID reads the completed exchange on the current connection.
func SessionToolsPeerID(ctx context.Context) string {
	c, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok {
		return ""
	}
	c.peerMu.Lock()
	defer c.peerMu.Unlock()
	return c.peerHubID
}

// SessionToolsPeerBinding resolves only an already-installed binding on this
// very connection, never an ID supplied in another connection's arguments.
func SessionToolsPeerBinding(ctx context.Context, bindingID string) (appwire.SessionBinding, bool) {
	c, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok || bindingID == "" {
		return appwire.SessionBinding{}, false
	}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	bound := c.sessionBindings[bindingID]
	if bound == nil || bound.ctx.Err() != nil {
		return appwire.SessionBinding{}, false
	}
	return bound.binding, true
}

// InstallSessionTools explicitly admits binding on a daemon server. The daemon
// validates its live identity before installing the connection-private client.
func (s *Server) InstallSessionTools(bind func(context.Context, appwire.SessionBinding, appwire.SessionHubClient) error) {
	s.cfg.Features.SessionTools = bind != nil
	s.sessionToolsBind = bind
}

type sessionToolsConnection struct {
	conn    *Connection
	binding appwire.SessionBinding
	ctx     context.Context
	cancel  context.CancelFunc
	next    atomic.Uint64
	mu      sync.Mutex
	pending map[string]chan appwire.Message
	watches map[string]*sessionWatchConsumer
}

type sessionWatchConsumer struct {
	updates chan appwire.SessionWatchUpdate
	done    chan struct{}
}

func (c *Connection) bindSessionTools(ctx context.Context, req appwire.Request) appwire.Message {
	if c.server.sessionToolsBind == nil {
		return appwire.ErrorMessage(req.ID, appwire.MethodNotFound(req.Method))
	}
	var binding appwire.SessionBinding
	decoder := json.NewDecoder(bytes.NewReader(req.Params))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return appwire.ErrorMessage(req.ID, appwire.InvalidParams(err.Error()))
	}
	if binding.BindingID == "" || binding.Ref == "" || binding.InstanceID == "" || binding.HubID == "" {
		return appwire.ErrorMessage(req.ID, appwire.InvalidParams("binding identity is required"))
	}
	if c.sessionLifetime != nil {
		ctx = context.WithValue(c.sessionLifetime, connectionContextKey{}, c)
	}
	lifetime, cancel := context.WithCancel(ctx)
	bound := &sessionToolsConnection{conn: c, binding: binding, ctx: lifetime, cancel: cancel, pending: map[string]chan appwire.Message{}, watches: map[string]*sessionWatchConsumer{}}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	peerID := SessionToolsPeerID(ctx)
	if c.sessionTools != nil {
		cancel()
		if peerID != "" {
			return appwire.ErrorMessage(req.ID, appwire.Conflict(sessionToolsAuthorityConflict))
		}
		return appwire.ErrorMessage(req.ID, appwire.Conflict("connection is already bound"))
	}
	if c.sessionBindings[binding.BindingID] != nil {
		cancel()
		return appwire.ErrorMessage(req.ID, appwire.Conflict("binding ID already installed"))
	}
	if peerID != "" && binding.HubID != peerID {
		cancel()
		return appwire.ErrorMessage(req.ID, appwire.Conflict(sessionToolsAuthorityConflict))
	}
	if len(c.sessionBindings) != 0 && peerID == "" {
		cancel()
		return appwire.ErrorMessage(req.ID, appwire.Conflict(sessionToolsAuthorityConflict))
	}
	if err := c.server.sessionToolsBind(lifetime, binding, bound); err != nil {
		cancel()
		return appwire.ErrorMessage(req.ID, WireError(err))
	}
	if peerID == "" {
		c.sessionTools = bound
	} else {
		if c.sessionBindings == nil {
			c.sessionBindings = map[string]*sessionToolsConnection{}
		}
		c.sessionBindings[binding.BindingID] = bound
	}
	return appwire.ResponseMessage(req.ID, binding)
}

// receiveSessionFrame runs before the request queue: a reverse response must not
// wait behind a handler whose runtime is waiting for that response.
func (c *Connection) receiveSessionFrame(msg appwire.Message) bool {
	c.sessionMu.Lock()
	bound := c.sessionTools
	peers := make([]*sessionToolsConnection, 0, len(c.sessionBindings))
	for _, peer := range c.sessionBindings {
		peers = append(peers, peer)
	}
	c.sessionMu.Unlock()
	if bound == nil && len(peers) == 0 {
		return false
	}
	if msg.Response != nil || msg.Error != nil {
		for _, current := range append(peers, bound) {
			if current == nil {
				continue
			}
			current.mu.Lock()
			ch := current.pending[msg.IDString()]
			delete(current.pending, msg.IDString())
			current.mu.Unlock()
			if ch != nil {
				ch <- msg
				return true
			}
		}
		return true
	}
	if msg.Notification != nil && msg.Notification.Method == appwire.NotificationRuntimeSessionObservation {
		var p appwire.SessionRuntimeObservation
		if json.Unmarshal(msg.Notification.Params, &p) != nil || p.BindingID == "" {
			return true
		}
		if bound == nil || p.BindingID != bound.binding.BindingID {
			c.sessionMu.Lock()
			bound = c.sessionBindings[p.BindingID]
			c.sessionMu.Unlock()
		}
		if bound == nil {
			return true
		}
		bound.mu.Lock()
		defer bound.mu.Unlock()
		ch := bound.watches[p.WatchID]
		if ch == nil {
			return true
		}
		if p.Ended {
			delete(bound.watches, p.WatchID)
			close(ch.updates)
			close(ch.done)
			return true
		}
		if p.Update != nil {
			select {
			case ch.updates <- *p.Update:
			default:
				bound.cancel()
				c.cancelContext()
			}
		}
		return true
	}
	return false
}

func (c *Connection) closeSessionTools() {
	c.sessionMu.Lock()
	bound := c.sessionTools
	c.sessionTools = nil
	peers := c.sessionBindings
	c.sessionBindings = nil
	c.sessionMu.Unlock()
	for _, peer := range peers {
		closeSessionBinding(peer)
	}
	closeSessionBinding(bound)
}

func closeSessionBinding(bound *sessionToolsConnection) {
	if bound == nil {
		return
	}
	bound.cancel()
	bound.mu.Lock()
	defer bound.mu.Unlock()
	for id, ch := range bound.watches {
		close(ch.updates)
		close(ch.done)
		delete(bound.watches, id)
	}
}

func (b *sessionToolsConnection) request(ctx context.Context, method string, p appwire.SessionRuntimeRequest, result any) error {
	p.BindingID = b.binding.BindingID
	requestID := -int64(b.conn.sessionNext.Add(1))
	id := appwire.NewIntID(requestID)
	ch := make(chan appwire.Message, 1)
	b.mu.Lock()
	if len(b.pending) >= appwire.SessionToolsPendingLimit {
		b.mu.Unlock()
		return appwire.Unavailable("session request limit reached")
	}
	b.pending[id.String()] = ch
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.pending, id.String()); b.mu.Unlock() }()
	select {
	case <-b.ctx.Done():
		return appwire.Unavailable("runtime binding disconnected")
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if !b.conn.enqueue(appwire.RequestMessage(id, method, p)) {
		b.conn.cancelContext()
		return appwire.Unavailable("runtime connection unavailable")
	}
	select {
	case <-b.ctx.Done():
		return appwire.Unavailable("runtime binding disconnected")
	case <-ctx.Done():
		// A local waiter ending must also release the remote owner. This fixed
		// cleanup request needs no pending slot, even when all calls are busy.
		cancelID := appwire.NewIntID(-int64(b.conn.sessionNext.Add(1)))
		if !b.conn.enqueue(appwire.RequestMessage(cancelID, appwire.MethodRuntimeSessionCancel, appwire.SessionRuntimeCancel{BindingID: b.binding.BindingID, RequestID: requestID})) {
			b.conn.cancelContext()
		}
		return ctx.Err()
	case msg := <-ch:
		if msg.Error != nil {
			return msg.Error.Error
		}
		data, err := json.Marshal(msg.Response.Result)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, result)
	}
}

func (b *sessionToolsConnection) State(ctx context.Context) (appwire.ManagerView, error) {
	var out appwire.ManagerView
	err := b.request(ctx, appwire.MethodRuntimeSessionState, appwire.SessionRuntimeRequest{}, &out)
	return out, err
}
func (b *sessionToolsConnection) Invoke(ctx context.Context, p appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	var out appwire.SessionOperationResponse
	if err := p.Validate(); err != nil {
		return out, err
	}
	err := b.request(ctx, appwire.MethodRuntimeSessionInvoke, appwire.SessionRuntimeRequest{Operation: &p}, &out)
	return out, err
}
func (b *sessionToolsConnection) Watch(ctx context.Context, p appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	id := fmt.Sprintf("observation-%d", b.next.Add(1))
	ch := make(chan appwire.SessionWatchUpdate, 32)
	done := make(chan struct{})
	b.mu.Lock()
	if len(b.watches) >= appwire.SessionToolsPendingLimit {
		b.mu.Unlock()
		return nil, appwire.Unavailable("observation limit reached")
	}
	b.watches[id] = &sessionWatchConsumer{updates: ch, done: done}
	b.mu.Unlock()
	cleanup := func() {
		b.mu.Lock()
		if b.watches[id] != nil {
			delete(b.watches, id)
			close(ch)
			close(done)
		}
		b.mu.Unlock()
	}
	var result struct{}
	if err := b.request(ctx, appwire.MethodRuntimeSessionWatch, appwire.SessionRuntimeRequest{Watch: &p, WatchID: id}, &result); err != nil {
		cleanup()
		b.unwatch(id)
		return nil, err
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-b.ctx.Done():
		case <-done:
			return
		}
		cleanup()
		b.unwatch(id)
	}()
	return ch, nil
}

// Cleanup needs no waiter or operation slot. The fixed response is ignored by
// receiveSessionFrame, and a disconnected peer already owns teardown.
func (b *sessionToolsConnection) unwatch(watchID string) {
	if b.ctx.Err() != nil {
		return
	}
	id := appwire.NewIntID(-int64(b.conn.sessionNext.Add(1)))
	if !b.conn.enqueue(appwire.RequestMessage(id, appwire.MethodRuntimeSessionUnwatch, appwire.SessionRuntimeRequest{BindingID: b.binding.BindingID, WatchID: watchID})) {
		b.conn.cancelContext()
	}
}
