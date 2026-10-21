package appwire

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
)

const SessionToolsPendingLimit = 32

type clientSessionTools struct {
	ctx     context.Context
	cancel  context.CancelFunc
	binding SessionBinding
	owner   SessionHubClient
	slots   chan struct{}
	mu      sync.Mutex
	watches map[string]context.CancelFunc
}

// SetSessionTools installs only the fixed runtime methods on this discovered
// daemon connection. Call before Start. The caller identity is in owner, not
// accepted from the wire.
func (c *Client) SetSessionTools(binding SessionBinding, owner SessionHubClient) {
	ctx, cancel := context.WithCancel(context.Background())
	c.sessionTools = &clientSessionTools{ctx: ctx, cancel: cancel, binding: binding, owner: owner, slots: make(chan struct{}, SessionToolsPendingLimit), watches: map[string]context.CancelFunc{}}
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

func (c *Client) sendSessionFrame(ctx context.Context, message Message) error {
	if err := c.acquireSendSlot(ctx); err != nil {
		return err
	}
	defer c.releaseSendSlot()
	return c.transport.Send(ctx, message)
}

func (c *Client) receiveSessionRequest(ctx context.Context, req Request) {
	state := c.sessionTools
	if state == nil {
		_ = c.sendSessionFrame(ctx, ErrorMessage(req.ID, MethodNotFound(req.Method)))
		return
	}
	var p SessionRuntimeRequest
	decoder := json.NewDecoder(bytes.NewReader(req.Params))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		_ = c.sendSessionFrame(ctx, ErrorMessage(req.ID, InvalidParams(err.Error())))
		return
	}
	if p.BindingID == "" || p.BindingID != state.binding.BindingID {
		_ = c.sendSessionFrame(ctx, ErrorMessage(req.ID, InvalidRequest("stale runtime binding")))
		return
	}
	// Cleanup must remain available even when all operation slots are occupied.
	if req.Method == MethodRuntimeSessionUnwatch {
		value, err := c.handleSessionRequest(ctx, req.Method, p, nil, nil)
		c.respondSessionRequest(ctx, req.ID, value, err)
		return
	}
	select {
	case state.slots <- struct{}{}:
	default:
		_ = c.sendSessionFrame(ctx, ErrorMessage(req.ID, Unavailable("session request limit reached")))
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
	go func() {
		defer func() { <-state.slots }()
		callCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(state.ctx, cancel)
		defer stop()
		defer cancel()
		value, err := c.handleSessionRequest(callCtx, req.Method, p, watchCtx, watchCancel)
		c.respondSessionRequest(ctx, req.ID, value, err)
	}()
}

func (c *Client) respondSessionRequest(ctx context.Context, id ID, value any, err error) {
	if err != nil {
		wire, ok := err.(WireError)
		if !ok {
			wire = Unavailable(err.Error())
		}
		_ = c.sendSessionFrame(ctx, ErrorMessage(id, wire))
	} else {
		_ = c.sendSessionFrame(ctx, ResponseMessage(id, value))
	}
}

func (c *Client) handleSessionRequest(ctx context.Context, method string, p SessionRuntimeRequest, watchCtx context.Context, cancel context.CancelFunc) (any, error) {
	s := c.sessionTools
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
					if err := c.sendSessionFrame(watchCtx, NotificationMessage(NotificationRuntimeSessionObservation, observation)); err != nil || !ok {
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
