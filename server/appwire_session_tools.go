package server

import (
	"context"
	"maps"
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
		expected := appwire.Ref{SourceID: "local", ThreadID: session.WorkspaceID()}.String()
		if binding.Ref != expected || binding.InstanceID != session.ID() {
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
			session.SetHubClient(clients)
			s.mu.Lock()
			s.resolveManagerActor = func(proof appwire.SessionActorProof) (schema.SessionActor, error) {
				clients.mu.Lock()
				defer clients.mu.Unlock()
				caller, err := appwire.ParseRef(proof.CallerRef)
				if err != nil || caller.String() != proof.CallerRef || proof.BindingID == "" || proof.Actor.Kind != "manager" || proof.Actor.ManagerRef != "" {
					return schema.SessionActor{}, appwire.InvalidRequest("Manager actor is not authenticated by the live binding")
				}
				for _, client := range clients.clients {
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
		clients.revision++
		session.HubBindingChanged()
		return nil
	})
}

// Selection follows a human-enabled durable setting, not the first or last
// socket to arrive. Once selected, disconnect never falls back to another hub.
// Conflicting enabled authorities fail closed instead of picking an order.
type runtimeHubClients struct {
	mu       sync.Mutex
	clients  map[string]*runtimeHubClient
	selected string
	revision uint64
}

func (c *runtimeHubClients) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, client := range c.clients {
		client.cancel()
	}
}

func (c *runtimeHubClients) State(ctx context.Context) (appwire.ManagerView, error) {
	c.mu.Lock()
	selected, revision := c.selected, c.revision
	var selectedClient *runtimeHubClient
	var clients map[string]*runtimeHubClient
	if selected != "" {
		selectedClient = c.clients[selected]
	} else {
		clients = maps.Clone(c.clients)
	}
	c.mu.Unlock()
	var view appwire.ManagerView
	chosen := ""
	if selected != "" {
		var err error
		view, err = selectedClient.State(ctx)
		if err != nil {
			return view, err
		}
		if view.Setting.Enabled {
			if view.Setting.HubID != selected {
				return view, appwire.Conflict("hub returned another authority")
			}
			chosen = selected
			// Capture the other clients without holding the lock across hub RPCs.
			// A revision change is rejected below, not silently reselected.
			type hubClient struct {
				hub    string
				client *runtimeHubClient
			}
			c.mu.Lock()
			var others []hubClient
			for hub, client := range c.clients {
				if hub != selected {
					others = append(others, hubClient{hub, client})
				}
			}
			c.mu.Unlock()
			for _, other := range others {
				if other.client.valid() != nil {
					continue
				}
				state, err := other.client.State(ctx)
				if err != nil {
					return view, err
				}
				if state.Setting.Enabled {
					if state.Setting.HubID != other.hub {
						return view, appwire.Conflict("hub returned another authority")
					}
					return view, appwire.Conflict("multiple hubs claim Manager authority")
				}
			}
		}
	} else {
		for hub, client := range clients {
			if client.valid() != nil {
				continue
			}
			state, err := client.State(ctx)
			if err != nil {
				return view, err
			}
			if state.Setting.Enabled {
				if state.Setting.HubID != hub {
					return view, appwire.Conflict("hub returned another authority")
				}
				if chosen != "" {
					return view, appwire.Conflict("multiple hubs claim Manager authority")
				}
				chosen = hub
				view = state
			} else if chosen == "" {
				view = state
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if revision != c.revision || selected != c.selected {
		return appwire.ManagerView{}, appwire.Unavailable("runtime bindings changed during admission")
	}
	c.selected = chosen
	return view, nil
}

func (c *runtimeHubClients) admitted(ctx context.Context) (*runtimeHubClient, error) {
	view, err := c.State(ctx)
	if err != nil {
		return nil, err
	}
	if !view.Setting.Enabled {
		return nil, appwire.InvalidRequest("Manager mode is not enabled")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.selected == "" {
		return nil, appwire.Unavailable("runtime authority changed")
	}
	return c.clients[c.selected], nil
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
