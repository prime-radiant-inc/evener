package appwire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const SessionToolsPendingLimit = 32

// Chosen independently to match internal/appserver's webSocketWriteTimeout.
// Each phase receives its own full budget; these packages share no constant.
const (
	sessionReplySlotTimeout  = 30 * time.Second
	sessionReplyWriteTimeout = 30 * time.Second
)

type clientSessionTools struct {
	ctx     context.Context
	cancel  context.CancelFunc
	binding SessionBinding
	owner   SessionHubClient
	slots   chan struct{}
	mu      sync.Mutex
	watches map[string]context.CancelFunc
	calls   map[string]context.CancelFunc
}

func newClientSessionTools(binding SessionBinding, owner SessionHubClient) *clientSessionTools {
	ctx, cancel := context.WithCancel(context.Background())
	return &clientSessionTools{ctx: ctx, cancel: cancel, binding: binding, owner: owner, slots: make(chan struct{}, SessionToolsPendingLimit), watches: map[string]context.CancelFunc{}, calls: map[string]context.CancelFunc{}}
}

// SetSessionTools installs only the fixed runtime methods on this discovered
// daemon connection. Call before Start. The caller identity is in owner, not
// accepted from the wire.
func (c *Client) SetSessionTools(binding SessionBinding, owner SessionHubClient) {
	c.sessionTools = newClientSessionTools(binding, owner)
}

func (c *Client) BindSessionTools(ctx context.Context) error {
	if c.sessionTools == nil || !c.Features().SessionTools {
		return Unavailable("peer does not support session tools")
	}
	var result SessionBinding
	if err := c.Request(ctx, MethodRuntimeSessionBind, c.sessionTools.binding, &result); err != nil {
		return err
	}
	if result != c.sessionTools.binding {
		return InvalidRequest("runtime binding identity changed")
	}
	return nil
}

// IdentifySessionToolsPeer runs after initialize negotiated SessionTools and
// before any input forwarding on this bridge. Reconnect repeats it.
func (c *Client) IdentifySessionToolsPeer(ctx context.Context, params SessionToolsPeerParams) (SessionToolsPeerResponse, error) {
	var response SessionToolsPeerResponse
	if !c.Features().SessionTools {
		return response, Unavailable("peer does not support session tools")
	}
	if params.HubID == "" {
		return response, InvalidParams("hub identity is required")
	}
	err := c.Request(ctx, MethodHubSessionToolsIdentify, params, &response)
	return response, err
}

func (c *Client) sendSessionFrame(ctx context.Context, message Message) error {
	return c.sendSessionFrameWithBudgets(ctx, message, sessionReplySlotTimeout, sessionReplyWriteTimeout)
}

func (c *Client) sendSessionFrameWithBudgets(ctx context.Context, message Message, slotBudget, writeBudget time.Duration) error {
	slotCtx, cancelSlot := context.WithTimeout(ctx, slotBudget)
	defer cancelSlot()
	select {
	case c.sendSlotChan() <- struct{}{}:
	case <-slotCtx.Done():
		return RequestNotSentError{Err: slotCtx.Err()}
	case <-c.closed:
		return RequestNotSentError{Err: c.closedError()}
	}
	defer c.releaseSendSlot()
	if err := slotCtx.Err(); err != nil {
		return RequestNotSentError{Err: err}
	}
	select {
	case <-c.closed:
		return RequestNotSentError{Err: c.closedError()}
	default:
	}
	writeCtx, cancelWrite := context.WithTimeout(ctx, writeBudget)
	defer cancelWrite()
	return c.transport.Send(writeCtx, message)
}

// Failed replies cannot be silently dropped: the connection owns all calls
// and watches, so its existing close/read-loop failure path releases them.
// A cancelled watch owns no further observations and does not retire its peer.
func (c *Client) sendSessionFrameOrClose(ctx context.Context, message Message) error {
	err := c.sendSessionFrame(ctx, message)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil && c.sessionTools != nil && c.sessionTools.ctx.Err() == nil && errors.Is(err, context.Canceled) {
		return err
	}
	c.markClosed(err)
	c.failPending(err)
	_ = c.Close()
	return err
}

func (c *Client) receiveSessionRequest(ctx context.Context, req Request) {
	// Every reverse request, including cleanup, must identify this connection's
	// single binding. An absent or stale ID cannot reach its owner.
	var key struct {
		BindingID string `json:"bindingId"`
	}
	if err := json.Unmarshal(req.Params, &key); err != nil {
		c.respondSessionRequest(ctx, req.ID, nil, InvalidParams(err.Error()))
		return
	}
	var state *clientSessionTools
	if key.BindingID != "" && c.sessionTools != nil && key.BindingID == c.sessionTools.binding.BindingID {
		state = c.sessionTools
	}
	if state == nil {
		c.respondSessionRequest(ctx, req.ID, nil, InvalidRequest("unknown runtime binding"))
		return
	}
	if req.Method == MethodRuntimeSessionCancel {
		var p SessionRuntimeCancel
		decoder := json.NewDecoder(bytes.NewReader(req.Params))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&p); err != nil {
			c.respondSessionRequest(ctx, req.ID, nil, InvalidParams(err.Error()))
			return
		}
		if p.BindingID == "" || p.BindingID != state.binding.BindingID {
			c.respondSessionRequest(ctx, req.ID, nil, InvalidRequest("stale runtime binding"))
			return
		}
		if p.RequestID >= 0 {
			c.respondSessionRequest(ctx, req.ID, nil, InvalidParams("requestId must be a negative reverse request ID"))
			return
		}
		state.mu.Lock()
		cancel := state.calls[NewIntID(p.RequestID).String()]
		state.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		c.respondSessionRequest(ctx, req.ID, EmptyResponse{}, nil)
		return
	}
	var p SessionRuntimeRequest
	decoder := json.NewDecoder(bytes.NewReader(req.Params))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		_ = c.sendSessionFrameOrClose(ctx, ErrorMessage(req.ID, InvalidParams(err.Error())))
		return
	}
	if p.BindingID == "" || p.BindingID != state.binding.BindingID {
		_ = c.sendSessionFrameOrClose(ctx, ErrorMessage(req.ID, InvalidRequest("stale runtime binding")))
		return
	}
	// Cleanup must remain available even when all operation slots are occupied.
	if req.Method == MethodRuntimeSessionUnwatch {
		value, err := c.handleSessionRequest(ctx, nil, state, req.Method, p, nil)
		c.respondSessionRequest(ctx, req.ID, value, err)
		return
	}
	select {
	case state.slots <- struct{}{}:
	default:
		_ = c.sendSessionFrameOrClose(ctx, ErrorMessage(req.ID, Unavailable("session request limit reached")))
		return
	}
	var watchCtx context.Context
	var watchCancel context.CancelFunc
	if req.Method == MethodRuntimeSessionWatch {
		if p.Watch == nil || p.WatchID == "" || p.Operation != nil {
			<-state.slots
			c.respondSessionRequest(ctx, req.ID, nil, InvalidParams("watch requires only watch and watchId"))
			return
		}
		state.mu.Lock()
		if len(state.watches) >= SessionToolsPendingLimit || state.watches[p.WatchID] != nil {
			state.mu.Unlock()
			<-state.slots
			c.respondSessionRequest(ctx, req.ID, nil, Unavailable("watch limit or duplicate watch"))
			return
		}
		// Reserve in receive order before starting the potentially blocking owner.
		// A following unwatch can cancel setup as well as an established watch.
		watchCtx, watchCancel = context.WithCancel(state.ctx)
		state.watches[p.WatchID] = watchCancel
		state.mu.Unlock()
	}
	// Reserve cancellation in receive order, before the owner goroutine starts.
	// Following cancellation can therefore stop even work not yet scheduled.
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(state.ctx, cancel)
	state.mu.Lock()
	if state.calls[req.ID.String()] != nil {
		state.mu.Unlock()
		stop()
		cancel()
		if watchCancel != nil {
			watchCancel()
			state.mu.Lock()
			delete(state.watches, p.WatchID)
			state.mu.Unlock()
		}
		<-state.slots
		c.respondSessionRequest(ctx, req.ID, nil, InvalidRequest("duplicate in-flight request ID"))
		return
	}
	state.calls[req.ID.String()] = func() {
		cancel()
		if watchCancel != nil {
			watchCancel()
		}
	}
	state.mu.Unlock()
	go func() {
		defer func() {
			state.mu.Lock()
			delete(state.calls, req.ID.String())
			state.mu.Unlock()
			stop()
			cancel()
			<-state.slots
		}()
		value, err := c.handleSessionRequest(callCtx, watchCtx, state, req.Method, p, watchCancel)
		c.respondSessionRequest(ctx, req.ID, value, err)
	}()
}

func (c *Client) respondSessionRequest(ctx context.Context, id ID, value any, err error) {
	if err != nil {
		var wire WireError
		if !errors.As(err, &wire) {
			wire = Unavailable(err.Error())
		}
		_ = c.sendSessionFrameOrClose(ctx, ErrorMessage(id, wire))
	} else {
		_ = c.sendSessionFrameOrClose(ctx, ResponseMessage(id, value))
	}
}

func (c *Client) handleSessionRequest(ctx, watchCtx context.Context, s *clientSessionTools, method string, p SessionRuntimeRequest, cancel context.CancelFunc) (any, error) {
	switch method {
	case MethodRuntimeSessionState:
		if p.Operation != nil || p.Watch != nil || p.WatchID != "" {
			return nil, InvalidParams("state accepts only bindingId")
		}
		return s.owner.State(ctx)
	case MethodRuntimeSessionInvoke:
		if p.Operation == nil || p.Watch != nil || p.WatchID != "" {
			return nil, InvalidParams("invoke requires only operation")
		}
		if err := p.Operation.Validate(); err != nil {
			return nil, err
		}
		return s.owner.Invoke(ctx, *p.Operation)
	case MethodRuntimeSessionWatch:
		updates, err := s.owner.Watch(watchCtx, *p.Watch)
		if err != nil {
			cancel()
			s.mu.Lock()
			delete(s.watches, p.WatchID)
			s.mu.Unlock()
			return nil, err
		}
		go func() {
			defer cancel()
			defer func() { s.mu.Lock(); delete(s.watches, p.WatchID); s.mu.Unlock() }()
			for {
				select {
				case <-c.closed:
					return
				case <-watchCtx.Done():
					return
				case update, ok := <-updates:
					observation := SessionRuntimeObservation{BindingID: s.binding.BindingID, WatchID: p.WatchID, Ended: !ok}
					if ok {
						observation.Update = &update
					}
					if err := c.sendSessionFrameOrClose(watchCtx, NotificationMessage(NotificationRuntimeSessionObservation, observation)); err != nil || !ok {
						return
					}
				}
			}
		}()
		return struct{}{}, nil
	case MethodRuntimeSessionUnwatch:
		if p.Operation != nil || p.Watch != nil || p.WatchID == "" {
			return nil, InvalidParams("unwatch requires only watchId")
		}
		s.mu.Lock()
		cancel := s.watches[p.WatchID]
		delete(s.watches, p.WatchID)
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return struct{}{}, nil
	default:
		return nil, MethodNotFound(method)
	}
}
