package hub

import (
	"context"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

// hubRelay owns the relay bookkeeping (mu guards the four maps; the *Locked
// helpers are in app_relay_state.go) and the values its background goroutines
// capture when the relay is built.
type hubRelay struct {
	server  *appserver.Server
	cfg     hubcore.WebConfig
	sources *appsource.Registry
	// idleInterval and targetState are read once here, never from the live
	// seam: the relay's background goroutines outlive the request that
	// started them and fence against the lookup captured at construction.
	idleInterval time.Duration
	targetState  deletionTargetLookup
	logf         func(format string, args ...any)
	retryClock   relayRetryClock

	mu          sync.Mutex
	relayed     map[string]*hubRelayHandle
	pending     map[string]map[*relayKeyState]*hubRelayHandle
	canonical   map[appwire.Ref]*hubRelayHandle
	generations map[string]uint64
}

func newHubRelay(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry) *hubRelay {
	retryClock := newRelayRetryClock()
	if cfg.RelayHooks.RetryWait != nil {
		retryClock = relayRetryClockFunc(cfg.RelayHooks.RetryWait)
	}
	return &hubRelay{
		server:       server,
		cfg:          cfg,
		sources:      sources,
		idleInterval: hubRelayIdleInterval,
		targetState:  deletionTargetState,
		logf:         hubLogfFor(cfg),
		retryClock:   retryClock,
		relayed:      map[string]*hubRelayHandle{},
		pending:      map[string]map[*relayKeyState]*hubRelayHandle{},
		canonical:    map[appwire.Ref]*hubRelayHandle{},
		generations:  map[string]uint64{},
	}
}

func (r *hubRelay) registerSubscription(ctx context.Context, relayKey string, replace bool, expectedLifecycle ...string) error {
	if r.cfg.RelayHooks.RegisterSubscription != nil {
		if !r.cfg.RelayHooks.RegisterSubscription(ctx, relayKey, replace) {
			return context.Canceled
		}
		return nil
	}
	return appserver.SubscribeWithAdmission(ctx, relayKey, replace, expectedLifecycle...)
}

func (r *hubRelay) backgroundFenceError(ref, threadID string) error {
	return r.targetState.fenceError(r.cfg, ref, threadID, ref, "")
}

func newHubRelayFunctions(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry) hubRelayFunctions {
	r := newHubRelay(server, cfg, sources)
	startTurn := func(ctx context.Context, source appsource.Source, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		readParams := appwire.ThreadReadParams{Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: false}
		prepared, err := r.prepareRelay(ctx, source, readParams)
		if err != nil {
			if isTargetDeletedError(err) {
				if fenceErr := deletionFenceError(cfg, params.Ref, params.ThreadID, params.ClientMutationID); fenceErr != nil {
					return appwire.TurnStartResponse{}, fenceErr
				}
			}
			if daemonOwnershipMayHaveChanged(err) {
				if restartErr := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, params.ThreadID, params.ClientMutationID); restartErr != nil {
					return appwire.TurnStartResponse{}, restartErr
				}
			}
			return appwire.TurnStartResponse{}, err
		}
		// The relay's own read doubles as the skill-input capability check, so a
		// selection is judged by the same read that established the live relay —
		// including after an auto-resume, where the recheck runs against the
		// replacement daemon this prepareRelay just read. The validator is a
		// no-op without a skill item, so ordinary sends are unchanged.
		if err := appwire.ValidateSkillInputSupport(params.Input, prepared.Thread.Evener.Capabilities.SkillInput); err != nil {
			return appwire.TurnStartResponse{}, appwire.InvalidParams(err.Error())
		}
		if err := appwire.ValidateCommandInputSupport(params.Input, prepared.Thread.Evener.Capabilities.CommandInput); err != nil {
			return appwire.TurnStartResponse{}, appwire.InvalidParams(err.Error())
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appwire.TurnStartResponse, error) {
			return source.StartTurn(ctx, params)
		})
	}
	startRelayForThread := func(ctx context.Context, thread appwire.Thread) error {
		if thread.ID == "" {
			thread.ID = thread.SessionID
		}
		if thread.ID == "" {
			return nil
		}
		ref := thread.Evener.Ref
		if ref == "" {
			sourceID := strings.TrimSpace(thread.Source)
			if sourceID == "" {
				sourceID = "local"
			}
			ref = appwire.Ref{SourceID: sourceID, ThreadID: thread.ID}.String()
		}
		source, err := sourceForThreadWithDeletionFence(ctx, cfg, sources, ref, thread.ID)
		if err != nil {
			return nil //nolint:nilerr // best-effort relay: an unresolvable source means nothing to relay, not a caller error
		}
		var relayErr error
		if _, atomic := source.(appsource.RelaySessionSource); atomic {
			_, relayErr = r.prepareRelay(ctx, source, appwire.ThreadReadParams{Ref: ref, ThreadID: thread.ID, IncludeTurns: false})
		} else {
			relayErr = r.startRelay(ctx, source, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false}, thread)
		}
		if relayErr != nil {
			if isSessionUnavailableError(relayErr) {
				return nil
			}
			return relayErr
		}
		return nil
	}
	stopCanonicalRelay := func(ref appwire.Ref) {
		var closeHandle bool
		var cancelInitializing context.CancelFunc
		r.mu.Lock()
		handle := r.canonical[ref]
		if handle != nil && !handle.removed {
			handle.stopping = true
			if handle.initializing {
				cancelInitializing = handle.cancel
			}
			for _, state := range handle.relayKeys {
				state.stopRequested = true
				state.retiring = true
				state.retireOwner = nil
				r.removeStoppedStateLocked(handle, state)
			}
			for state := range handle.pendingStates {
				state.stopRequested = true
				r.removeStoppedStateLocked(handle, state)
			}
			r.signalRouteChangeLocked(handle)
			closeHandle = r.maybeFinishHandleLocked(handle)
		}
		r.mu.Unlock()
		if cancelInitializing != nil {
			cancelInitializing()
		}
		if closeHandle {
			r.closeRelayHandle(handle)
		}
	}
	stopRelay := func(relayKey string) {
		var closeHandles []*hubRelayHandle
		r.mu.Lock()
		handle := r.relayed[relayKey]
		if handle != nil && handle.canonical == (appwire.Ref{}) {
			if r.removeRelayHandleLocked(handle) {
				r.finishHandleLocked(handle, context.Canceled)
				closeHandles = append(closeHandles, handle)
			}
		} else {
			handles := make(map[*hubRelayHandle]struct{})
			if handle != nil {
				handles[handle] = struct{}{}
				state := handle.relayKeys[relayKey]
				if state != nil {
					state.stopRequested = true
					state.retiring = true
					state.retireOwner = nil
					r.removeStoppedStateLocked(handle, state)
				}
			}
			for state, pendingHandle := range r.pending[relayKey] {
				handles[pendingHandle] = struct{}{}
				if state != nil {
					state.stopRequested = true
					r.removeStoppedStateLocked(pendingHandle, state)
				}
			}
			for currentHandle := range handles {
				r.signalRouteChangeLocked(currentHandle)
				if r.maybeFinishHandleLocked(currentHandle) {
					closeHandles = append(closeHandles, currentHandle)
				}
			}
		}
		r.mu.Unlock()
		for _, closeHandle := range closeHandles {
			r.closeRelayHandle(closeHandle)
		}
	}
	relayCommandCount := func(key string) int {
		r.mu.Lock()
		defer r.mu.Unlock()
		count := 0
		if handle := r.relayed[key]; handle != nil {
			if state := handle.relayKeys[key]; state != nil {
				count += state.commands
			}
		}
		for state := range r.pending[key] {
			count += state.commands
		}
		return count
	}
	relayPublished := func(key string) bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		handle := r.relayed[key]
		return handle != nil && handle.relayKeys[key] != nil
	}
	return hubRelayFunctions{
		startRelay:          r.startRelay,
		readThread:          r.readThread,
		captureThreadRead:   r.captureThreadRead,
		startTurn:           startTurn,
		startRelayForThread: startRelayForThread,
		stopRelay:           stopRelay,
		stopCanonicalRelay:  stopCanonicalRelay,
		relayCommandCount:   relayCommandCount,
		relayPublished:      relayPublished,
	}
}
