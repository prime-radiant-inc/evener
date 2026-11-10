package agent

import (
	"context"
	"slices"
	"time"

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

// hubClientBindingWait bounds how long a Manager tool waits for the hub's
// runtime-private binding. The hub installs the binding asynchronously once it
// discovers a session, and a thread/clear replacement is discovered only after
// the daemon publishes its new identity. A tool invoked from the replacement's
// first turn can therefore run before the binding lands; waiting briefly keeps
// that turn from refusing a Manager tool the session is about to own. The
// budget matches the watch read budget, and only an operation that would
// otherwise fail sees the delay.
const hubClientBindingWait = 2 * time.Second

// waitForHubClient returns the session's hub client, waiting up to budget for
// the binding to be installed. It stops early when the session is not a
// Manager (no binding will arrive) or is closing, and returns nil at the
// deadline so callers keep their own refusal.
func (s *Session) waitForHubClient(ctx context.Context, budget time.Duration) SessionHubClient {
	deadline := time.Now().Add(budget)
	for {
		s.mu.Lock()
		client, enabled, closing := s.hubClient, s.hubEnabled, s.closing
		signal := s.hubWatchSignal
		s.mu.Unlock()
		if client != nil || !enabled || closing {
			return client
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-signal:
			timer.Stop()
		case <-timer.C:
			timer.Stop()
		}
	}
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
