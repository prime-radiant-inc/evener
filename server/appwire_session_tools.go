package server

import (
	"context"
	"sync"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// SetSessionTools connects the fixed transport to the live runtime getter. Clear
// replaces the runtime, so neither the getter nor the binding is a launch option.
func (s *Server) SetSessionTools(current func() *agent.Session) {
	var mu sync.Mutex
	var selectedSession *agent.Session
	var clients *runtimeHubClients
	s.AppServer().InstallSessionTools(func(ctx context.Context, binding appwire.SessionBinding, client appwire.SessionHubClient) error {
		session := current()
		if session == nil {
			return appwire.Unavailable("runtime unavailable")
		}
		s.mu.RLock()
		expectedRef, threadID := s.appRef, s.appThreadID
		s.mu.RUnlock()
		if expectedRef == "" || threadID != session.ID() || binding.Ref != expectedRef || binding.InstanceID != session.ID() {
			return appwire.InvalidRequest("binding does not name this live runtime")
		}
		mu.Lock()
		defer mu.Unlock()
		if selectedSession != session {
			if clients != nil {
				clients.close()
			}
			selectedSession = session
			clients = &runtimeHubClients{clients: map[string]*runtimeHubClient{}}
			registry := clients
			session.SetHubClient(registry)
			s.mu.Lock()
			s.resolveManagerActor = func(proof appwire.SessionActorProof) (schema.SessionActor, error) {
				registry.mu.Lock()
				defer registry.mu.Unlock()
				caller, err := appwire.ParseRef(proof.CallerRef)
				if err != nil || caller.String() != proof.CallerRef || proof.BindingID == "" || proof.Actor.Kind != "manager" || proof.Actor.ManagerRef != "" {
					return schema.SessionActor{}, appwire.InvalidRequest("Manager actor is not authenticated by the live binding")
				}
				for _, client := range registry.clients {
					if client.binding.BindingID == proof.BindingID && client.valid() == nil && client.binding.Ref != proof.CallerRef {
						return schema.SessionActor{Kind: "manager", ManagerRef: proof.CallerRef}, nil
					}
				}
				return schema.SessionActor{}, appwire.InvalidRequest("Manager actor is not authenticated by the live binding")
			}
			s.mu.Unlock()
		}
		lifetime, cancel := context.WithCancel(ctx)
		clients.mu.Lock()
		defer clients.mu.Unlock()
		if previous := clients.clients[binding.HubID]; previous != nil {
			if previous.ctx.Err() == nil {
				cancel()
				return appwire.Conflict("hub already has a live runtime binding")
			}
			previous.cancel()
		} else if len(clients.clients) >= appwire.SessionToolsPendingLimit {
			cancel()
			return appwire.Unavailable("runtime hub binding limit reached")
		}
		clients.clients[binding.HubID] = &runtimeHubClient{current: current, session: session, binding: binding, client: client, ctx: lifetime, cancel: cancel}
		session.HubBindingChanged()
		return nil
	})
}

// Selection follows the live binding, not a hub-side setting: a session
// started as a Manager carries its own authority. Once selected, disconnect
// never falls back to another hub. Simultaneous live bindings from distinct
// hubs fail closed instead of picking an order.
type runtimeHubClients struct {
	mu       sync.Mutex
	clients  map[string]*runtimeHubClient
	selected string
}

func (c *runtimeHubClients) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, client := range c.clients {
		client.cancel()
	}
}

// State answers from the session itself, not from any hub preference: the
// session's start-time Manager property is the whole truth, reported through
// the wire shape hub-side admission reads.
func (c *runtimeHubClients) State(ctx context.Context) (appwire.ManagerView, error) {
	client, err := c.admitted(ctx)
	if err != nil {
		return appwire.ManagerView{}, err
	}
	session := client.session
	return appwire.ManagerView{Ref: client.binding.Ref, InstanceID: client.binding.InstanceID, Manager: session != nil && session.Meta().Manager}, nil
}

// admitted returns the hub client operations run through. A Manager session is
// admitted because it was started as one; nothing here reads hub state. Two
// simultaneous live bindings fail closed rather than picking an order, even
// after one was already selected: a second hub installing a live binding means
// two hubs each believe they own this session.
func (c *runtimeHubClients) admitted(ctx context.Context) (*runtimeHubClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var live []*runtimeHubClient
	if c.selected != "" {
		client := c.clients[c.selected]
		if client == nil || client.valid() != nil {
			return nil, appwire.Unavailable("selected runtime hub binding is not live")
		}
		live = append(live, client)
	}
	for hub, client := range c.clients {
		if hub == c.selected || client.valid() != nil {
			continue
		}
		live = append(live, client)
	}
	switch {
	case len(live) == 1:
		c.selected = live[0].binding.HubID
		return live[0], nil
	case len(live) == 0:
		return nil, appwire.Unavailable("runtime hub binding is not live")
	default:
		return nil, appwire.Conflict("multiple hubs hold live runtime bindings")
	}
}
func (c *runtimeHubClients) Invoke(ctx context.Context, p appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	client, err := c.admitted(ctx)
	if err != nil {
		return appwire.SessionOperationResponse{}, err
	}
	return client.Invoke(ctx, p)
}
func (c *runtimeHubClients) Watch(ctx context.Context, p appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	client, err := c.admitted(ctx)
	if err != nil {
		return nil, err
	}
	return client.Watch(ctx, p)
}

type runtimeHubClient struct {
	current func() *agent.Session
	session *agent.Session
	binding appwire.SessionBinding
	client  appwire.SessionHubClient
	ctx     context.Context
	cancel  context.CancelFunc
}

type sessionActorContextKey struct{}

// withSessionActor marks an internally authenticated input request. Incoming
// AppWire requests cannot set this key; hub-to-runtime requests use a proof.
func withSessionActor(ctx context.Context, actor schema.SessionActor) context.Context {
	return context.WithValue(ctx, sessionActorContextKey{}, actor)
}

func (s *Server) inputActor(ctx context.Context, proof *appwire.SessionActorProof) (schema.SessionActor, bool, error) {
	if proof != nil {
		s.mu.RLock()
		resolve := s.resolveManagerActor
		s.mu.RUnlock()
		if resolve == nil {
			return schema.SessionActor{}, false, appwire.InvalidRequest("Manager input has no live binding")
		}
		actor, err := resolve(*proof)
		return actor, err == nil, err
	}
	if actor, ok := ctx.Value(sessionActorContextKey{}).(schema.SessionActor); ok {
		if actor.Kind != "manager" || actor.ManagerRef == "" {
			return schema.SessionActor{}, false, appwire.InvalidRequest("invalid internal Manager actor")
		}
		return actor, true, nil
	}
	return schema.SessionActor{Kind: "human"}, false, nil
}

func (c *runtimeHubClient) valid() error {
	if c.ctx.Err() != nil || c.current() != c.session || c.session.ID() != c.binding.InstanceID {
		return appwire.Unavailable("runtime binding replaced or disconnected")
	}
	return nil
}
func (c *runtimeHubClient) State(ctx context.Context) (appwire.ManagerView, error) {
	if err := c.valid(); err != nil {
		return appwire.ManagerView{}, err
	}
	ctx, cancel := c.callContext(ctx)
	defer cancel()
	return c.client.State(ctx)
}
func (c *runtimeHubClient) Invoke(ctx context.Context, request appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	if err := c.valid(); err != nil {
		return appwire.SessionOperationResponse{}, err
	}
	ctx, cancel := c.callContext(ctx)
	defer cancel()
	return c.client.Invoke(ctx, request)
}
func (c *runtimeHubClient) Watch(ctx context.Context, request appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	if err := c.valid(); err != nil {
		return nil, err
	}
	ctx, cancel := c.callContext(ctx)
	updates, err := c.client.Watch(ctx, request)
	if err != nil {
		cancel()
		return nil, err
	}
	out := make(chan appwire.SessionWatchUpdate)
	go func() {
		defer cancel()
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case update, ok := <-updates:
				if !ok {
					return
				}
				select {
				case out <- update:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

func (c *runtimeHubClient) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	call, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	return call, func() { stop(); cancel() }
}
