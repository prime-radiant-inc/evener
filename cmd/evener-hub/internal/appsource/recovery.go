package appsource

import (
	"context"
	"time"

	"primeradiant.com/evener/rendezvous"
)

// daemonIdentity excludes session aliases: clear can change them while an RPC
// is outstanding, but it does not change the process that recovery must stop.
type daemonIdentity struct {
	pid       int
	startedAt time.Time
	stateDir  string
}

type daemonCalls struct {
	blocked int
	calls   map[*context.CancelFunc]struct{}
}

func recoveryIdentity(entry rendezvous.Entry) daemonIdentity {
	return daemonIdentity{entry.PID, entry.StartedAt.UTC(), entry.StateDir}
}

// BeginRecovery cancels outstanding direct RPCs and rejects new ones for this
// process until the caller releases its ownership reservation. Cancellation is
// deliberately not session-unavailable: callers must not auto-resume it.
// Read-only relay feeds own their connection epochs separately. Observing a
// replacement neither launches a daemon nor acknowledges hub recovery authority.
func (s *LocalDaemonSource) BeginRecovery(entry rendezvous.Entry) func() {
	s.callsMu.Lock()
	key := recoveryIdentity(entry)
	state := s.daemonCallsLocked(key)
	state.blocked++
	for cancel := range state.calls {
		(*cancel)()
	}
	s.callsMu.Unlock()
	return func() {
		s.callsMu.Lock()
		defer s.callsMu.Unlock()
		state.blocked--
		if state.blocked == 0 && len(state.calls) == 0 {
			delete(s.calls, key)
		}
	}
}

func (s *LocalDaemonSource) daemonCallsLocked(key daemonIdentity) *daemonCalls {
	if s.calls == nil {
		s.calls = make(map[daemonIdentity]*daemonCalls)
	}
	state := s.calls[key]
	if state == nil {
		state = &daemonCalls{calls: make(map[*context.CancelFunc]struct{})}
		s.calls[key] = state
	}
	return state
}

func (s *LocalDaemonSource) beginDaemonCall(ctx context.Context, entry rendezvous.Entry) (context.Context, func()) {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	key := recoveryIdentity(entry)
	state := s.daemonCallsLocked(key)
	ctx, cancel := context.WithCancel(ctx)
	state.calls[&cancel] = struct{}{}
	if state.blocked > 0 {
		cancel()
	}
	return ctx, func() {
		cancel()
		s.callsMu.Lock()
		defer s.callsMu.Unlock()
		delete(state.calls, &cancel)
		if state.blocked == 0 && len(state.calls) == 0 {
			delete(s.calls, key)
		}
	}
}
