package hub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/internal/appserver"
)

// relayStartTarget is the identity one startRelayOnce call subscribes under:
// the thread the read named, the key its downstream registry entry uses, and
// the two address forms (initial subscribe, recovery re-attach).
type relayStartTarget struct {
	source          appsource.Source
	thread          appwire.Thread
	threadID        string
	relayKey        string
	subscribeParams appwire.ThreadReadParams
	recoveryParams  appwire.ThreadReadParams
}

// relayClaim is the placeholder handle one startRelayOnce call owns while it
// establishes the relay. relayCtx outlives the starting request; cancelRelay
// ends it; stopInitialCancellation detaches the request-cancellation hook once
// the relay is established.
type relayClaim struct {
	relayCtx                context.Context
	relayHandle             *hubRelayHandle
	cancelRelay             context.CancelFunc
	stopInitialCancellation func() bool
}

func (r *hubRelay) startRelayOnce(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams, thread appwire.Thread) error {
	if thread.ID == "" {
		return nil
	}
	target, err := resolveRelayStartTarget(source, params, thread)
	if err != nil {
		return err
	}
	claim, registered, err := r.claimRelayHandle(ctx, target)
	if err != nil || registered {
		return err
	}
	defer claim.stopInitialCancellation()
	notifications, err := r.publishRelay(ctx, target, claim)
	if err != nil {
		return err
	}
	return r.launchRelaySupervisor(target, claim, notifications)
}

// resolveRelayStartTarget names the thread a relay start subscribes and the
// key it registers under.
func resolveRelayStartTarget(source appsource.Source, params appwire.ThreadReadParams, thread appwire.Thread) (relayStartTarget, error) {
	threadID := thread.ID

	subscribeParams := params
	if subscribeParams.Ref == "" {
		subscribeParams.Ref = thread.Evener.Ref
	}
	if subscribeParams.Ref == "" {
		subscribeParams.Ref = appwire.Ref{SourceID: source.ID(), ThreadID: threadID}.String()
	}
	// Key the downstream registry entry by the same identity thread/unsubscribe
	// resolves (relayDeliveryTarget), not by thread.ID. A remote source's
	// stable ref survives an identity replacement while its current Thread.ID
	// moves, so keying by Thread.ID registers under "host:<currentID>" while a
	// ref-only unsubscribe resolves "host:<stableRef>": the downstream entry
	// (and, through it, the remote-side subscription) would never be dropped,
	// and a replacement could leave a second relay behind. The request's own
	// address decides the key, so this agrees with the subscription admission
	// thread/read itself resolved and with the source's own SubscribeThread
	// routing, which is keyed from the ref.
	keyParams := params
	if strings.TrimSpace(keyParams.Ref) == "" && strings.TrimSpace(keyParams.ThreadID) == "" {
		// An internal caller (turn/start's relay, a recovery that named no
		// address) has no request identity of its own, so key by the ref
		// derived from the thread this relay read.
		keyParams = appwire.ThreadReadParams{Ref: subscribeParams.Ref}
	}
	relayKey, _, keyErr := relayDeliveryTarget(source, keyParams)
	if keyErr != nil {
		return relayStartTarget{}, keyErr
	}

	// Recovery re-addresses the thread by the authoritative ref the read
	// named, not by the caller's bare threadId. A source may resolve a bare
	// threadId in preference to the ref beside it (the remote hub's daemon
	// edge does), and that ID is the volatile current identity: once an
	// identity replacement moves it, re-sending it names no thread while the
	// stable ref still resolves, so every recovery attempt in the stale state
	// fails. Only the initial request keeps the caller's own addressing; the
	// first read already named the subscription's authoritative identity.
	recoveryParams := subscribeParams
	if thread.Evener.Ref != "" {
		recoveryParams.Ref = thread.Evener.Ref
		recoveryParams.ThreadID = ""
	}
	return relayStartTarget{
		source:          source,
		thread:          thread,
		threadID:        threadID,
		relayKey:        relayKey,
		subscribeParams: subscribeParams,
		recoveryParams:  recoveryParams,
	}, nil
}

// claimRelayHandle installs the placeholder handle for the target's relay key,
// or, when a relay for the key already exists, waits for it and registers this
// subscriber on it (registered reports that case).
func (r *hubRelay) claimRelayHandle(ctx context.Context, target relayStartTarget) (claim relayClaim, registered bool, err error) {
	relayKey, threadID, subscribeParams := target.relayKey, target.threadID, target.subscribeParams

	var relayCtx context.Context
	var relayHandle *hubRelayHandle
	var cancelRelay context.CancelFunc
	var stopInitialCancellation func() bool
	for {
		r.mu.Lock()
		existing := r.relayed[relayKey]
		if existing == nil {
			relayCtx, cancelRelay = context.WithCancel(context.WithoutCancel(ctx))
			// relayCtx keeps the starting request's values but not its
			// cancellation, since the relay outlives that request. The
			// server that serves the relay owns it instead, from the first
			// subscribe on: its Shutdown ends the relay rather than leaving
			// it to an idle tick, which never comes while a subscriber stays
			// registered. The hook is released once the relay itself ends.
			stopOnServerShutdown := context.AfterFunc(r.server.Lifetime(), cancelRelay)
			context.AfterFunc(relayCtx, func() { stopOnServerShutdown() })
			// Label every attach this relay issues with its own key. A source
			// that keys its subscriptions by remote thread identity uses it to
			// tell this relay re-attaching from a second relay that reached the
			// same thread by another address (appsource.WithRelayIdentity).
			relayCtx = appsource.WithRelayIdentity(relayCtx, relayKey)
			relayHandle = &hubRelayHandle{ready: make(chan struct{}), cancel: cancelRelay}
			r.relayed[relayKey] = relayHandle
			stopInitialCancellation = context.AfterFunc(ctx, func() {
				var cancel context.CancelFunc
				r.mu.Lock()
				if r.relayed[relayKey] == relayHandle && !relayHandle.established {
					delete(r.relayed, relayKey)
					relayHandle.err = ctx.Err()
					cancel = relayHandle.cancel
				}
				r.mu.Unlock()
				if cancel != nil {
					cancel()
				}
			})
			r.mu.Unlock()
			if r.cfg.RelayHooks.AfterPlaceholder != nil {
				r.cfg.RelayHooks.AfterPlaceholder(threadID)
			}
			break
		}
		ready := existing.ready
		r.mu.Unlock()
		if observeHubRelayWait != nil {
			observeHubRelayWait()
		}

		select {
		case <-ready:
		case <-ctx.Done():
			return relayClaim{}, false, ctx.Err()
		}

		registerExisting := func() (bool, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			active := r.relayed[relayKey] == existing
			err := existing.err
			if !active || err != nil {
				return false, err
			}
			if r.cfg.RelayHooks.BeforeExistingRegistration != nil {
				r.cfg.RelayHooks.BeforeExistingRegistration(threadID)
			}
			if err := r.registerSubscription(ctx, relayKey, subscribeParams.ReplaceSubscription); err != nil {
				return false, err
			}
			return true, nil
		}
		registered, err := withDeletionTargetOwnership(ctx, r.cfg, subscribeParams.Ref, threadID, "", registerExisting)
		if err != nil {
			return relayClaim{}, false, err
		}
		if registered {
			return relayClaim{}, true, nil
		}
	}
	return relayClaim{relayCtx: relayCtx, relayHandle: relayHandle, cancelRelay: cancelRelay, stopInitialCancellation: stopInitialCancellation}, false, nil
}

// publishRelay subscribes the source, registers the subscriber and publishes
// the handle as established, returning the source's notification stream.
func (r *hubRelay) publishRelay(ctx context.Context, target relayStartTarget, claim relayClaim) (<-chan appwire.Notification, error) {
	source, relayKey, threadID, subscribeParams := target.source, target.relayKey, target.threadID, target.subscribeParams
	relayCtx, relayHandle, cancelRelay, stopInitialCancellation := claim.relayCtx, claim.relayHandle, claim.cancelRelay, claim.stopInitialCancellation

	r.mu.Lock()
	active := r.relayed[relayKey] == relayHandle && relayCtx.Err() == nil
	err := relayHandle.err
	if !active {
		if err == nil {
			err = context.Canceled
		}
		r.finishHandleLocked(relayHandle, err)
		r.mu.Unlock()
		cancelRelay()
		return nil, err
	}
	r.mu.Unlock()
	var notifications <-chan appwire.Notification
	if err := deletionFenceError(r.cfg, subscribeParams.Ref, threadID, ""); err != nil {
		return nil, err
	}
	notifications, err = source.SubscribeThread(relayCtx, subscribeParams)
	if fenceErr := deletionFenceError(r.cfg, subscribeParams.Ref, threadID, ""); fenceErr != nil {
		err = fenceErr
	}
	if err != nil {
		cancelRelay()
		r.mu.Lock()
		if r.relayed[relayKey] == relayHandle {
			delete(r.relayed, relayKey)
		}
		if relayHandle.err != nil {
			err = relayHandle.err
		}
		r.finishHandleLocked(relayHandle, err)
		r.mu.Unlock()
		return nil, err
	}
	r.mu.Lock()
	if r.relayed[relayKey] != relayHandle || relayCtx.Err() != nil || ctx.Err() != nil {
		err = relayHandle.err
		if err == nil {
			err = ctx.Err()
			if err == nil {
				err = context.Canceled
			}
		}
		if r.relayed[relayKey] == relayHandle {
			delete(r.relayed, relayKey)
		}
		r.finishHandleLocked(relayHandle, err)
		r.mu.Unlock()
		cancelRelay()
		return nil, err
	}
	if err = r.registerSubscription(ctx, relayKey, subscribeParams.ReplaceSubscription); err != nil {
		delete(r.relayed, relayKey)
		r.finishHandleLocked(relayHandle, err)
		r.mu.Unlock()
		cancelRelay()
		return nil, err
	}
	relayHandle.established = true
	stopInitialCancellation()
	r.finishHandleLocked(relayHandle, nil)
	r.mu.Unlock()
	if r.cfg.RelayHooks.AfterReady != nil {
		r.cfg.RelayHooks.AfterReady(threadID)
	}
	return notifications, nil
}

// launchRelaySupervisor starts the goroutine that forwards the stream to the
// relay key's subscribers and re-attaches it when the source drops. It
// returns with the supervisor running, or with the relay canceled when the
// handle went stale first.
func (r *hubRelay) launchRelaySupervisor(target relayStartTarget, claim relayClaim, notifications <-chan appwire.Notification) error {
	relayKey, threadID := target.relayKey, target.threadID
	relayCtx, relayHandle, cancelRelay := claim.relayCtx, claim.relayHandle, claim.cancelRelay

	r.mu.Lock()
	active := r.relayed[relayKey] == relayHandle && relayCtx.Err() == nil
	err := relayHandle.err
	r.mu.Unlock()
	if !active {
		if err == nil {
			err = context.Canceled
		}
		cancelRelay()
		return err
	}
	if r.cfg.RelayHooks.BeforeLaunchCommit != nil {
		r.cfg.RelayHooks.BeforeLaunchCommit(threadID)
	}
	r.mu.Lock()
	active = r.relayed[relayKey] == relayHandle && relayCtx.Err() == nil
	err = relayHandle.err
	if !active {
		r.mu.Unlock()
		if err == nil {
			err = context.Canceled
		}
		cancelRelay()
		return err
	}
	if r.cfg.RelayHooks.BeforeSupervisor != nil {
		r.cfg.RelayHooks.BeforeSupervisor(threadID)
	}
	supervisor := &relaySupervisor{r: r, relayStartTarget: target, relayClaim: claim, notifications: notifications}
	go supervisor.run()
	r.mu.Unlock()
	return nil
}

// relaySupervisor forwards a relay's notification stream to the subscribers of
// its relay key and re-attaches the stream when the source drops. Its run
// goroutine is the only reader and writer of the fields below the embedded
// target and claim.
type relaySupervisor struct {
	r *hubRelay
	relayStartTarget
	relayClaim
	notifications <-chan appwire.Notification
	ticker        *time.Ticker
	argsByCallID  map[string]string
	backoff       relayRetryBackoff
	// turnRunning mirrors whether the thread is running a turn, tracked
	// from the thread/status/changed frames this loop already forwards,
	// so giveUpOnRunningTurn knows whether a re-dial failure is happening
	// mid-turn (spinner visibly stalled) or between turns (nothing on
	// screen is waiting, so nothing needs to be told).
	turnRunning   bool
	runningTurnID string
	// noticeAnchorEntry is the last relayed entry's ordinal + 1, 0
	// before any (the spec's notice-anchor rule, appoverlay.go's
	// anchorEntry): the entry a give-up notice anchors after.
	noticeAnchorEntry   uint64
	consecutiveFailures int
}

func (s *relaySupervisor) run() {
	s.ticker = time.NewTicker(s.r.idleInterval)
	defer s.ticker.Stop()
	defer s.cleanupRelay()
	s.argsByCallID = map[string]string{}
	for {
		if s.relayCtx.Err() != nil {
			return
		}
		if s.notifications == nil {
			result, stopped := s.subscribeForRecovery()
			if stopped {
				return
			}
			if result.err != nil {
				if isTargetDeletedError(result.err) {
					return
				}
				s.recordFailure(result.err)
				if s.waitForRetry(s.backoff.Next()) {
					return
				}
				continue
			}
			if result.notifications == nil {
				s.recordFailure(errors.New("the source returned no notification stream"))
				if s.waitForRetry(s.backoff.Next()) {
					return
				}
				continue
			}
			var firstNotification appwire.Notification
			hasFirstNotification := false
			select {
			case notification, ok := <-result.notifications:
				if !ok {
					s.recordFailure(errors.New("the source closed its notification stream"))
					if s.waitForRetry(s.backoff.Next()) {
						return
					}
					continue
				}
				firstNotification = notification
				hasFirstNotification = true
			default:
			}
			// The relay owns the recovery resync, but a source may
			// generate one itself as the first frame of a new
			// subscription (the remote hub source hands the relay the
			// subscribed read's atomic snapshot this way). Emitting the
			// relay's own resync as well would deliver two resyncs to the
			// client and force two redundant thread reads. Give the
			// source's frame precedence: it carries the subscription's
			// own authoritative identity, which survives a stable-ref
			// identity replacement that the original subscribeParams may
			// not name anymore.
			sourceResynced := hasFirstNotification && firstNotification.Method == appwire.NotifyEvenerThreadResync
			if !sourceResynced {
				s.r.server.Broadcast(s.relayKey, appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
					ThreadID: s.threadID,
					Ref:      s.subscribeParams.Ref,
				})
			}
			if hasFirstNotification {
				s.broadcastNotification(firstNotification)
			} else {
				s.backoff.Reset()
				s.consecutiveFailures = 0
			}
			s.notifications = result.notifications
		}
		select {
		case <-s.relayCtx.Done():
			return
		case <-s.ticker.C:
			if s.retireIfIdle() {
				return
			}
		case notification, ok := <-s.notifications:
			if !ok {
				s.notifications = nil
				continue
			}
			s.broadcastNotification(notification)
		}
	}
}

func (s *relaySupervisor) cleanupRelay() {
	s.cancelRelay()
	s.r.mu.Lock()
	if s.r.relayed[s.relayKey] == s.relayHandle {
		delete(s.r.relayed, s.relayKey)
	}
	s.r.mu.Unlock()
}

func (s *relaySupervisor) trackRelayedThread(notification appwire.Notification) {
	switch notification.Method {
	case appwire.NotifyThreadStatusChanged:
		var params appwire.ThreadStatusChangedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			s.turnRunning = params.Status.Type == appwire.ThreadStatusActive
			s.runningTurnID = params.ActiveTurnID
		}
	case appwire.NotifyHistoryUpdated:
		var params struct {
			Items []struct {
				Position *appwire.ThreadItemPosition `json:"position"`
			} `json:"items"`
		}
		if json.Unmarshal(notification.Params, &params) != nil {
			return
		}
		for _, item := range params.Items {
			if item.Position != nil {
				s.noticeAnchorEntry = max(s.noticeAnchorEntry, item.Position.Entry+1)
			}
		}
	}
}

// giveUpOnRunningTurn tells the reader what the unreachable source can
// no longer say: nothing runs the turn any more, and why. It publishes
// the idle status, which owns the session status; an error notice in
// the running turn naming the last re-dial failure; and a resync, so
// the client re-reads: the turn never completed reads as an open turn
// with nothing running it. It fires at most once per stall: clearing
// turnRunning makes every later call in the same stall a no-op, so
// continued backoff never re-broadcasts them.
//
// The notice is published live and nowhere else. This loop serves
// only sources without an atomic relay session (remote hubs and other
// federated sources; a local daemon's death takes the relay session's
// DaemonGoneResync path instead), and the client's re-read goes to the
// same unreachable source and fails, which leaves its history and
// overlay, the notice included, as they were. The notice anchors after
// the latest history this relay forwarded, and at the start of history
// only when it forwarded none.
//
// Both frames name the relay's target. The synthesized failure is the
// turn's; the session STATUS is thread/status/changed's, never
// turn/completed's — the daemon's real failure exit emits the same
// pair (agent/session_lifecycle.go's endInputAtTurnFailure announces
// EventSessionEnd{Reason:"turn_failed"} as
// thread/status/changed(systemError)). The synthesized frame keeps
// idle: a lost connection is not a recorded turn failure, and the
// roster marks a dead daemon errored. Without the status frame the
// client would keep the session active with Stop and Steer still
// showing and Send withheld — the exact stall this exists to end. The
// capabilities are the hub's own answer for a session whose daemon is
// gone (relayGaveUpCapabilities, the set a past read returns), because
// the departing daemon's set describes the turn that is over.
func (s *relaySupervisor) giveUpOnRunningTurn(cause error) {
	if !s.turnRunning {
		return
	}
	s.turnRunning = false
	s.r.logf("relay for %s gave up on its running turn after %d failed re-dials: %v", s.relayKey, s.consecutiveFailures, cause)
	s.r.server.Broadcast(s.relayKey, appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID:     s.threadID,
		Ref:          s.subscribeParams.Ref,
		Status:       appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Capabilities: relayGaveUpCapabilities(s.r.cfg, s.relayKey, s.thread),
	})
	s.r.server.Broadcast(s.relayKey, appwire.NotifyOverlayUpserted, appwire.OverlayUpsertedParams{
		ThreadID: s.threadID,
		Ref:      s.subscribeParams.Ref,
		Item:     relayGaveUpNotice(s.runningTurnID, s.noticeAnchorEntry, cause),
	})
	s.r.server.Broadcast(s.relayKey, appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
		ThreadID: s.threadID,
		Ref:      s.subscribeParams.Ref,
	})
}

func (s *relaySupervisor) recordFailure(cause error) {
	s.consecutiveFailures++
	if s.consecutiveFailures >= relayGiveUpAfterFailures {
		s.giveUpOnRunningTurn(cause)
	}
}

func (s *relaySupervisor) broadcastNotification(notification appwire.Notification) {
	s.backoff.Reset()
	s.consecutiveFailures = 0
	s.trackRelayedThread(notification)
	if s.source.ID() == appwire.LocalSourceID {
		notification = enrichOutputImageNotification(s.thread.SessionID, s.thread.CWD, s.argsByCallID, notification)
	}
	s.r.server.Broadcast(s.relayKey, notification.Method, notification.Params)
}

func (s *relaySupervisor) retireIfIdle() bool {
	if s.r.server.SubscriberCount(s.relayKey) != 0 {
		return false
	}
	if s.r.cfg.RelayHooks.IdleExit != nil {
		s.r.cfg.RelayHooks.IdleExit(s.threadID)
	}
	s.r.mu.Lock()
	if s.r.server.SubscriberCount(s.relayKey) != 0 {
		s.r.mu.Unlock()
		return false
	}
	if s.r.relayed[s.relayKey] == s.relayHandle {
		delete(s.r.relayed, s.relayKey)
	}
	s.r.mu.Unlock()
	if s.r.cfg.RelayHooks.AfterIdleDelete != nil {
		s.r.cfg.RelayHooks.AfterIdleDelete(s.threadID)
	}
	s.cancelRelay()
	return true
}

func (s *relaySupervisor) waitForRetry(delay time.Duration) bool {
	waitCtx, cancelWait := context.WithCancel(s.relayCtx)
	waitResult := make(chan error, 1)
	go func() {
		waitResult <- s.r.retryClock.Wait(waitCtx, delay)
	}()
	for {
		select {
		case err := <-waitResult:
			cancelWait()
			return err != nil
		case <-s.relayCtx.Done():
			cancelWait()
			<-waitResult
			return true
		case <-s.ticker.C:
			if s.retireIfIdle() {
				cancelWait()
				<-waitResult
				return true
			}
		}
	}
}

func (s *relaySupervisor) subscribeForRecovery() (hubRelaySubscriptionResult, bool) {
	result := make(chan hubRelaySubscriptionResult, 1)
	go func() {
		if err := s.r.backgroundFenceError(s.subscribeParams.Ref, s.threadID); err != nil {
			result <- hubRelaySubscriptionResult{err: err}
			return
		}
		notifications, err := subscribeRelayRecovery(s.relayCtx, s.source, s.recoveryParams)
		if fenceErr := s.r.backgroundFenceError(s.subscribeParams.Ref, s.threadID); fenceErr != nil {
			err = fenceErr
		}
		result <- hubRelaySubscriptionResult{notifications: notifications, err: err}
	}()
	for {
		select {
		case got := <-result:
			return got, false
		case <-s.relayCtx.Done():
			return <-result, true
		case <-s.ticker.C:
			if s.retireIfIdle() {
				return <-result, true
			}
		}
	}
}

func (r *hubRelay) startRelay(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams, thread appwire.Thread) error {
	_, err := appserver.RetrySubscription(ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, r.startRelayOnce(ctx, source, params, thread)
	})
	return err
}
