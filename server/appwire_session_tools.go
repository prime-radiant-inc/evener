package server

import (
	"context"
	"maps"
	"sync"

	"primeradiant.com/evener/agent"
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
		}
		lifetime, cancel := context.WithCancel(ctx)
		clients.mu.Lock()
		defer clients.mu.Unlock()
		if previous := clients.clients[binding.HubID]; previous != nil {
			previous.cancel()
		} else if len(clients.clients) >= appwire.SessionToolsPendingLimit {
			cancel()
			return appwire.Unavailable("runtime hub binding limit reached")
		}
		clients.clients[binding.HubID] = &runtimeHubClient{current: current, session: session, binding: binding, client: client, ctx: lifetime, cancel: cancel}
		clients.revision++
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
	clients := maps.Clone(c.clients)
	c.mu.Unlock()
	var view appwire.ManagerView
	chosen := ""
	if selected != "" {
		var err error
		view, err = clients[selected].State(ctx)
		if err != nil {
			return view, err
		}
		if view.Setting.Enabled {
			if view.Setting.HubID != selected {
				return view, appwire.Conflict("hub returned another authority")
			}
			chosen = selected
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
