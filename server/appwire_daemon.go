package server

import (
	"context"
	"errors"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

// SetDaemonLifecycle installs the process lifecycle hooks behind
// evener/daemon/status and evener/daemon/retire. Status must return a detached
// snapshot (no session borrows, no activity marks) so it can answer while the
// process is preparing or retiring; retire drives the controller claim and
// reports blockers when the process is not eligible. Handlers copy the hooks
// under the server mutex and invoke them without it.
func (s *Server) SetDaemonLifecycle(status func() appwire.DaemonLifecycle, retire func(context.Context, appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error)) {
	s.mu.Lock()
	s.daemonStatusFunc = status
	s.daemonRetireFunc = retire
	s.mu.Unlock()
}

func (s *Server) daemonLifecycleHooks() (func() appwire.DaemonLifecycle, func(context.Context, appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.daemonStatusFunc, s.daemonRetireFunc
}

// DaemonLifecycleFromSnapshot converts the process retirement snapshot into
// the wire lifecycle contract. Timestamps are UTC RFC3339Nano strings (valid
// RFC3339, lossless instants); durations are integer milliseconds. Blockers
// are always a non-nil array so "none" never decodes as "unknown".
func DaemonLifecycleFromSnapshot(snap agent.RetirementSnapshot) appwire.DaemonLifecycle {
	out := appwire.DaemonLifecycle{
		Phase:         snap.Phase,
		TimeoutMillis: snap.Timeout.Milliseconds(),
		Blockers:      make([]appwire.DaemonBlocker, 0, len(snap.Blockers)),
		Failure:       snap.Failure,
	}
	if !snap.EligibleSince.IsZero() {
		out.EligibleSince = snap.EligibleSince.UTC().Format(time.RFC3339Nano)
	}
	if !snap.Deadline.IsZero() {
		out.Deadline = snap.Deadline.UTC().Format(time.RFC3339Nano)
	}
	for _, blocker := range snap.Blockers {
		out.Blockers = append(out.Blockers, appwire.DaemonBlocker{
			Category:   blocker.Category,
			SessionID:  blocker.SessionID,
			DelegateID: blocker.DelegateID,
		})
	}
	return out
}

func (s *Server) handleAppDaemonStatus(_ context.Context, _ appwire.DaemonStatusParams) (appwire.DaemonStatusResponse, error) {
	status, _ := s.daemonLifecycleHooks()
	if status == nil {
		return appwire.DaemonStatusResponse{}, appwire.Unavailable("daemon lifecycle status not available")
	}
	return appwire.DaemonStatusResponse{Lifecycle: status()}, nil
}

func (s *Server) handleAppDaemonRetire(ctx context.Context, params appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error) {
	status, retire := s.daemonLifecycleHooks()
	if retire == nil {
		return appwire.DaemonRetireResponse{}, appwire.Unavailable("daemon retirement not available")
	}
	resp, err := retire(ctx, params)
	if err != nil && errors.Is(err, agent.ErrRetirementUnavailable) && status != nil {
		// A lifecycle race (already preparing/retiring) is typed so the caller
		// can retry automatically; its mutation outcome is unknown, not a
		// rejection — a lost response is not proof the claim was refused.
		return appwire.DaemonRetireResponse{}, appwire.LifecycleUnavailable(status().Phase)
	}
	return resp, err
}
