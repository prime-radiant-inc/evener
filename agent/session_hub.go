package agent

import (
	"context"
	"slices"
	"time"

	"primeradiant.com/evener/appwire"
)

type SessionHubClient = appwire.SessionHubClient

// SetHubClient installs only a runtime-private connection. Spawn and fork
// options carry no binding, authority or enabled state.
func (s *Session) SetHubClient(client SessionHubClient) {
	s.mu.Lock()
	s.hubClient = client
	s.hubGeneration++
	s.signalHubBindingLocked()
	s.mu.Unlock()
}

// HubBindingChanged wakes suspended watches after the existing transport has
// installed a new binding. It does not grant authority or select another hub.
func (s *Session) HubBindingChanged() {
	s.mu.Lock()
	s.signalHubBindingLocked()
	s.mu.Unlock()
}

func (s *Session) signalHubBindingLocked() {
	if s.hubWatchSignal != nil {
		close(s.hubWatchSignal)
	}
	s.hubWatchSignal = make(chan struct{})
}

func (s *Session) refreshHubState(ctx context.Context) {
	s.mu.Lock()
	client, generation := s.hubClient, s.hubGeneration
	s.mu.Unlock()
	enabled := false
	if client != nil {
		stateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		state, err := client.State(stateCtx)
		cancel()
		enabled = err == nil && state.Setting.Enabled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.hubGeneration {
		return
	}
	if s.hubEnabled == enabled {
		return
	}
	s.hubEnabled = enabled
	s.rebuildToolDefsCache()
	// The same lock publishes the prompt and schema before request capture.
	s.refreshSystemPromptCache(s.env)
}

func (s *Session) hubToolDenied(name string) bool {
	return slices.Contains(s.cfg.spawn.deniedToolNames, name) ||
		(len(s.cfg.spawn.allowedToolNames) > 0 && !slices.Contains(s.cfg.spawn.allowedToolNames, name)) ||
		(len(s.cfg.spawn.toolNameCeiling) > 0 && !slices.Contains(s.cfg.spawn.toolNameCeiling, name))
}
