package agent

import (
	"slices"

	"primeradiant.com/evener/appwire"
)

// SessionHubClient is the AppWire owner used by a session Manager.
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

func (s *Session) hubToolDenied(name string) bool {
	return slices.Contains(s.cfg.spawn.deniedToolNames, name) ||
		(len(s.cfg.spawn.allowedToolNames) > 0 && !slices.Contains(s.cfg.spawn.allowedToolNames, name)) ||
		(len(s.cfg.spawn.toolNameCeiling) > 0 && !slices.Contains(s.cfg.spawn.toolNameCeiling, name))
}
