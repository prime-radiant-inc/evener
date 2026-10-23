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

const sessionToolsAuthorityConflict = "runtime binding and identified peer cannot share a connection"

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
	// A local binding installation in flight has claimed this connection even
	// though it has not committed yet, so identification must be refused for the
	// same reason a committed local binding refuses it.
	if c.sessionTools != nil || c.sessionToolsInstalling != nil {
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
	// The installation wait is bounded by the connection lifetime, not by this
	// request. Holding sessionMu across that wait would park the frame reader,
	// so reserve the single slot under the lock and install outside it.
	c.sessionMu.Lock()
	if SessionToolsPeerID(ctx) != "" {
		c.sessionMu.Unlock()
		cancel()
		return appwire.ErrorMessage(req.ID, appwire.Conflict(sessionToolsAuthorityConflict))
	}
	if c.sessionTools != nil || c.sessionToolsInstalling != nil {
		c.sessionMu.Unlock()
		cancel()
		return appwire.ErrorMessage(req.ID, appwire.Conflict("connection is already bound"))
	}
	// The pending binding claims the slot and is cancelled on connection close,
	// but frames cannot route into it until installation succeeds.
	c.sessionToolsInstalling = bound
	c.sessionMu.Unlock()

	err := c.server.sessionToolsBind(lifetime, binding, bound)
	if err != nil {
		cancel()
		c.sessionMu.Lock()
		if c.sessionToolsInstalling == bound {
			c.sessionToolsInstalling = nil
		}
		c.sessionMu.Unlock()
		return appwire.ErrorMessage(req.ID, WireError(err))
	}

	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if c.sessionToolsInstalling != bound {
		// The connection was torn down while installation was in flight.
		cancel()
		return appwire.ErrorMessage(req.ID, appwire.Conflict("binding installation abandoned"))
	}
	c.sessionToolsInstalling = nil
	c.sessionTools = bound
	return appwire.ResponseMessage(req.ID, binding)
}

// receiveSessionFrame runs before the request queue: a reverse response must not
// wait behind a handler whose runtime is waiting for that response.
func (c *Connection) receiveSessionFrame(msg appwire.Message) bool {
	c.sessionMu.Lock()
	bound := c.sessionTools
	c.sessionMu.Unlock()
	if bound == nil {
		return false
	}
	if msg.Response != nil || msg.Error != nil {
		bound.mu.Lock()
		ch := bound.pending[msg.IDString()]
		delete(bound.pending, msg.IDString())
		bound.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
		return true
	}
	if msg.Notification != nil && msg.Notification.Method == appwire.NotificationRuntimeSessionObservation {
		var p appwire.SessionRuntimeObservation
		if json.Unmarshal(msg.Notification.Params, &p) != nil || p.BindingID != bound.binding.BindingID {
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
	installing := c.sessionToolsInstalling
	c.sessionToolsInstalling = nil
	c.sessionMu.Unlock()
	closeSessionBinding(installing)
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
