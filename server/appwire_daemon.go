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

// SetDaemonIdleTimeoutSet installs the process hook behind
// evener/daemon/idle-timeout/set. The hook retargets the retirement deadline
// and answers with the current lifecycle; like the other lifecycle hooks it is
// copied under the server mutex and invoked without it.
func (s *Server) SetDaemonIdleTimeoutSet(fn func(context.Context, appwire.DaemonIdleTimeoutSetParams) (appwire.DaemonIdleTimeoutSetResponse, error)) {
	s.mu.Lock()
	s.daemonIdleTimeoutSetFunc = fn
	s.mu.Unlock()
}

func (s *Server) daemonIdleTimeoutHooks() (func(context.Context, appwire.DaemonIdleTimeoutSetParams) (appwire.DaemonIdleTimeoutSetResponse, error), func() appwire.DaemonLifecycle) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.daemonIdleTimeoutSetFunc, s.daemonStatusFunc
}

// normalizeLifecycleRace types an ErrRetirementUnavailable from a daemon
// control hook as the retryable CodeUnavailable a lost response deserves: a
// lost response is not proof the action was refused, and
// evener/daemon/status remains the authority. Every other error (and nil)
// passes through unchanged.
func normalizeLifecycleRace(status func() appwire.DaemonLifecycle, err error) error {
	if err != nil && errors.Is(err, agent.ErrRetirementUnavailable) && status != nil {
		return appwire.LifecycleUnavailable(status().Phase)
	}
	return err
}

// DaemonLifecycleFromSnapshot converts the process retirement snapshot into
// the wire lifecycle contract. Timestamps are UTC RFC3339Nano strings (valid
// RFC3339, lossless instants); durations are integer milliseconds. Blockers
// are always a non-nil array so "none" never decodes as "unknown".
func DaemonLifecycleFromSnapshot(snap agent.RetirementSnapshot) appwire.DaemonLifecycle {
	out := appwire.DaemonLifecycle{
		Phase:         snap.Phase,
		TimeoutMillis: appwire.DurationMillis(snap.Timeout),
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
	return resp, normalizeLifecycleRace(status, err)
}

func (s *Server) handleAppDaemonIdleTimeoutSet(ctx context.Context, params appwire.DaemonIdleTimeoutSetParams) (appwire.DaemonIdleTimeoutSetResponse, error) {
	set, status := s.daemonIdleTimeoutHooks()
	if set == nil {
		return appwire.DaemonIdleTimeoutSetResponse{}, appwire.Unavailable("daemon idle-timeout control not available")
	}
	resp, err := set(ctx, params)
	return resp, normalizeLifecycleRace(status, err)
}
