package hub

import (
	"context"
	"log/slog"
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
	log          *slog.Logger
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
		log:          hubLogFor(cfg),
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

func (r *hubRelay) startTurn(ctx context.Context, source appsource.Source, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	readParams := appwire.ThreadReadParams{Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: false}
	prepared, err := r.prepareRelay(ctx, source, readParams)
	if err != nil {
		if isTargetDeletedError(err) {
			if fenceErr := deletionFenceError(r.cfg, params.Ref, params.ThreadID, params.ClientMutationID); fenceErr != nil {
				return appwire.TurnStartResponse{}, fenceErr
			}
		}
		if daemonOwnershipMayHaveChanged(err) {
			if restartErr := refreshDaemonRestartRequiredError(ctx, r.cfg, params.Ref, params.ThreadID, params.ClientMutationID); restartErr != nil {
				return appwire.TurnStartResponse{}, restartErr
			}
		}
		return appwire.TurnStartResponse{}, err
	}
	// The relay's read also supplies the input capability verdict, including
	// after a resume, without taking a second daemon snapshot.
	if err := prepareInputMode(ctx, source, params.Ref, params.ThreadID, params.Input, params.ExpectedAskRevision, params.ExpectedAskBootGeneration, &prepared.Thread); err != nil {
		return appwire.TurnStartResponse{}, err
	}
	return withDeletionTargetOwnership(ctx, r.cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appwire.TurnStartResponse, error) {
		return source.StartTurn(ctx, params)
	})
}

// relayThreadRef normalizes the thread's delivery ref the relay attaches
// under: the evener ref when present, else the thread's source and id.
func relayThreadRef(thread appwire.Thread) (ref, threadID string) {
	if thread.ID == "" {
		thread.ID = thread.SessionID
	}
	if thread.Evener.Ref != "" {
		return thread.Evener.Ref, thread.ID
	}
	sourceID := strings.TrimSpace(thread.Source)
	if sourceID == "" {
		sourceID = "local"
	}
	return appwire.Ref{SourceID: sourceID, ThreadID: thread.ID}.String(), thread.ID
}

func (r *hubRelay) startRelayForThread(ctx context.Context, thread appwire.Thread) error {
	ref, threadID := relayThreadRef(thread)
	if threadID == "" {
		return nil
	}
	source, err := sourceForThreadWithDeletionFence(ctx, r.cfg, r.sources, ref, thread.ID)
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

// resyncThreadView publishes the per-thread resync after a cold pick's relay
// attach commits. A pane whose first subscription was to saved history rides
// the relay only once the attach adopts the capture onto the relay key, so
// the relay session's own startup resync can have been published first and
// missed; the pane re-reads instead of keeping the pre-pick model and
// reasoning controls until some other read. Members that already saw the
// startup resync re-read once more — the signal is idempotent.
func (r *hubRelay) resyncThreadView(ctx context.Context, thread appwire.Thread) {
	ref, threadID := relayThreadRef(thread)
	if threadID == "" {
		return
	}
	source, err := sourceForThreadWithDeletionFence(ctx, r.cfg, r.sources, ref, threadID)
	if err != nil {
		return // best-effort: an unresolvable source means nothing to signal
	}
	relayKey, _, err := threadRelayTarget(source, appwire.ThreadReadParams{Ref: ref, ThreadID: threadID})
	if err != nil {
		return
	}
	r.server.Broadcast(relayKey, appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
		ThreadID: threadID,
		Ref:      ref,
	})
}
