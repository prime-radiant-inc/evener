package hub

import (
	"context"
	"sync"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/internal/appserver"
)

func (r *hubRelay) readThread(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams) (*hubThreadReadResult, error) {
	if r.cfg.RelayHooks.BeforeReadThread != nil {
		r.cfg.RelayHooks.BeforeReadThread()
	}
	needsRelay := params.AnswerContinuation == nil && (params.Subscribe || relayOnThreadRead(source))
	relaySource, atomic := source.(appsource.RelaySessionSource)
	if !atomic || !needsRelay {
		response, err := source.ReadThread(ctx, params)
		return &hubThreadReadResult{response: response}, err
	}
	if err := deletionFenceError(r.cfg, params.Ref, params.ThreadID, ""); err != nil {
		return nil, err
	}
	var canonicalRef appwire.Ref
	var lifecycleKey string
	var err error
	if paired, ok := relaySource.(interface {
		ResolveRelaySessionWithAdmission(appwire.ThreadReadParams) (appwire.Ref, string, error)
	}); ok {
		canonicalRef, lifecycleKey, err = paired.ResolveRelaySessionWithAdmission(params)
	} else {
		canonicalRef, err = relaySource.ResolveRelaySession(params)
	}
	if err != nil {
		return nil, err
	}
	handle, state, publish, release, err := r.acquireRelaySession(ctx, relaySource, source, params, canonicalRef)
	if err != nil {
		return nil, err
	}
	if lifecycleKey == "" {
		lifecycleKey = state.relayKey
	}
	var releaseOnce sync.Once
	releaseCommand := func() { releaseOnce.Do(release) }
	var read *hubThreadReadResult
	defer func() {
		if recovered := recover(); recovered != nil {
			if read != nil {
				read.finish(false)
			} else {
				releaseCommand()
			}
			panic(recovered)
		}
	}()
	readParams := params
	readParams.Subscribe = true
	result, err := handle.lease.ReadWithRoutePublication(ctx, readParams, publish)
	if result.Handoff != nil {
		read = &hubThreadReadResult{
			relayKey:     state.relayKey,
			lifecycleKey: lifecycleKey,
			response:     result.Response,
			handoff:      result.Handoff,
			release:      releaseCommand,
		}
	}
	if err != nil {
		if read != nil {
			read.finish(false)
		} else {
			releaseCommand()
		}
		return nil, err
	}
	if read == nil {
		releaseCommand()
		return nil, appwire.SessionUnavailable("atomic thread read returned no live continuation")
	}
	// Canonical leases invoke publish before their pre-cut acknowledgement
	// barrier. This idempotent call verifies the returned response without a
	// base-only compatibility path.
	if err := publish.Publish(ctx, result.Response.Thread); err != nil {
		read.finish(false)
		return nil, err
	}
	if err := deletionFenceError(r.cfg, params.Ref, read.response.Thread.ID, ""); err != nil {
		read.finish(false)
		return nil, err
	}
	return read, nil
}

func (r *hubRelay) captureThreadRead(ctx context.Context, params appwire.ThreadReadParams, read *hubThreadReadResult) bool {
	if read == nil || read.handoff == nil {
		return true
	}
	relayKey := read.relayKey
	captured, err := withDeletionTargetOwnership(
		ctx, r.cfg,
		params.Ref,
		read.response.Thread.ID,
		"",
		func() (bool, error) {
			if !read.handoff.Prepare() {
				return false, nil
			}
			// A cut of zero releases every frame the hub buffered during
			// this capture. That is right for a relay: the hub does not
			// sequence its own projection -- the upstream RelaySession
			// already decided which frames the response embodies and which
			// follow it, and every frame reaching this server through
			// Broadcast is by construction one that follows. Snapshot is a
			// no-op for the same reason: the response was materialized
			// upstream before this capture opened.
			return appserver.CaptureSubscriptionWithHandoff(
				ctx,
				params.ReplaceSubscription,
				func() string { return relayKey },
				func() uint64 { return 0 },
				func() bool { return true },
				appserver.CaptureSubscriptionHandoff{
					Commit: func() { read.finish(true) },
					Abort:  func() { read.finish(false) },
				},
				func() appserver.SubscriptionTarget {
					return appserver.SubscriptionTarget{ThreadID: relayKey, LifecycleKey: read.lifecycleKey}
				},
			), nil
		},
	)
	if err != nil || !captured {
		read.finish(false)
		return false
	}
	return true
}

func (r *hubRelay) prepareRelayOnce(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if _, atomic := source.(appsource.RelaySessionSource); !atomic {
		response, err := source.ReadThread(ctx, params)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		if err := r.startRelay(ctx, source, params, response.Thread); err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		return response, nil
	}
	params.Subscribe = true
	read, err := r.readThread(ctx, source, params)
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	threadID := read.response.Thread.ID
	relayKey := read.relayKey
	registered, err := withDeletionTargetOwnership(
		ctx, r.cfg,
		params.Ref,
		threadID,
		"",
		func() (bool, error) {
			if !read.handoff.Prepare() {
				return false, appwire.SessionUnavailable("relay handoff could not be prepared")
			}
			if err := r.registerSubscription(ctx, relayKey, params.ReplaceSubscription, read.lifecycleKey); err != nil {
				return false, err
			}
			if !read.finish(true) {
				return false, appwire.SessionUnavailable("relay handoff could not be committed")
			}
			return true, nil
		},
	)
	if err != nil || !registered {
		read.finish(false)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		return appwire.ThreadReadResponse{}, context.Canceled
	}
	return read.response, nil
}

func (r *hubRelay) prepareRelay(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appserver.RetrySubscription(ctx, func(ctx context.Context) (appwire.ThreadReadResponse, error) {
		return r.prepareRelayOnce(ctx, source, params)
	})
}
