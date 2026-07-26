package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/appserver"
)

type hubRelayHandle struct {
	ready       chan struct{}
	err         error
	cancel      context.CancelFunc
	established bool
}

type relayRetryClock interface {
	Wait(context.Context, time.Duration) error
}

type relayTimerClock struct{}

type relayRetryClockFunc func(context.Context, time.Duration) error

func newRelayRetryClock() relayRetryClock {
	return relayTimerClock{}
}

func (f relayRetryClockFunc) Wait(ctx context.Context, delay time.Duration) error {
	return f(ctx, delay)
}

func (relayTimerClock) Wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var hubRelayIdleInterval = 250 * time.Millisecond

const (
	relayRetryMinDelay = 100 * time.Millisecond
	relayRetryMaxDelay = 5 * time.Second

	// relayGiveUpAfterFailures bounds how many consecutive re-dial failures
	// the recovery loop tolerates before it stops retrying in silence and
	// tells the reader their turn died (kata 3h02: a SIGKILLed daemon left an
	// open tab's spinner stalled forever with no diagnostic). A daemon that
	// answers localDaemonDialError never recovers on its own — recovery
	// needs the reader to act, via reload or a new turn — so nothing is
	// gained by waiting longer; three keeps a single transient blip from
	// firing a false alarm while still surfacing a genuinely dead session in
	// well under a second of backoff (100ms + 200ms + 400ms).
	relayGiveUpAfterFailures = 3
)

type relayRetryBackoff struct {
	delay time.Duration
}

func (b *relayRetryBackoff) Next() time.Duration {
	if b.delay == 0 {
		b.delay = relayRetryMinDelay
	} else {
		b.delay *= 2
		if b.delay > relayRetryMaxDelay {
			b.delay = relayRetryMaxDelay
		}
	}
	return b.delay
}

func (b *relayRetryBackoff) Reset() {
	b.delay = 0
}

func subscribeRelayRecovery(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return source.SubscribeThread(ctx, params)
}

type hubRelayFunctions struct {
	startRelay          func(context.Context, appsource.Source, appwire.ThreadReadParams, appwire.Thread) error
	startTurn           func(context.Context, appsource.Source, appwire.TurnStartParams) (appwire.TurnStartResponse, error)
	startRelayForThread func(context.Context, appwire.Thread) error
	stopRelay           func(string)
}

var observeHubRelayFunctions func(hubRelayFunctions)
var observeHubRelayWait func()

func newHubRelayFunctions(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry) hubRelayFunctions {
	relayIdleInterval := hubRelayIdleInterval
	retryClock := newRelayRetryClock()
	if cfg.RelayHooks.RetryWait != nil {
		retryClock = relayRetryClockFunc(cfg.RelayHooks.RetryWait)
	}
	registerSubscription := func(ctx context.Context, relayKey string, replace bool) bool {
		if cfg.RelayHooks.RegisterSubscription != nil {
			return cfg.RelayHooks.RegisterSubscription(ctx, relayKey, replace)
		}
		if replace {
			return appserver.ReplaceSubscriptions(ctx, relayKey)
		}
		return appserver.Subscribe(ctx, relayKey)
	}
	var relayMu sync.Mutex
	relayedThreads := map[string]*hubRelayHandle{}
	finishHandleLocked := func(handle *hubRelayHandle, err error) {
		select {
		case <-handle.ready:
			handle.err = err
		default:
			handle.err = err
			close(handle.ready)
		}
	}
	startRelay := func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams, thread appwire.Thread) error {
		threadID := thread.ID
		if threadID == "" {
			return nil
		}
		relayKey := source.ID() + ":" + threadID

		subscribeParams := params
		if subscribeParams.Ref == "" {
			subscribeParams.Ref = thread.Serf.Ref
		}
		if subscribeParams.Ref == "" {
			subscribeParams.Ref = appwire.Ref{SourceID: source.ID(), ThreadID: threadID}.String()
		}

		var relayCtx context.Context
		var relayHandle *hubRelayHandle
		var cancelRelay context.CancelFunc
		var stopInitialCancellation func() bool
		for {
			relayMu.Lock()
			existing := relayedThreads[relayKey]
			if existing == nil {
				relayCtx, cancelRelay = context.WithCancel(context.WithoutCancel(ctx))
				relayHandle = &hubRelayHandle{ready: make(chan struct{}), cancel: cancelRelay}
				relayedThreads[relayKey] = relayHandle
				stopInitialCancellation = context.AfterFunc(ctx, func() {
					var cancel context.CancelFunc
					relayMu.Lock()
					if relayedThreads[relayKey] == relayHandle && !relayHandle.established {
						delete(relayedThreads, relayKey)
						relayHandle.err = ctx.Err()
						cancel = relayHandle.cancel
					}
					relayMu.Unlock()
					if cancel != nil {
						cancel()
					}
				})
				relayMu.Unlock()
				if cfg.RelayHooks.AfterPlaceholder != nil {
					cfg.RelayHooks.AfterPlaceholder(threadID)
				}
				break
			}
			ready := existing.ready
			relayMu.Unlock()
			if observeHubRelayWait != nil {
				observeHubRelayWait()
			}

			select {
			case <-ready:
			case <-ctx.Done():
				return ctx.Err()
			}

			relayMu.Lock()
			active := relayedThreads[relayKey] == existing
			err := existing.err
			if active && err == nil {
				if cfg.RelayHooks.BeforeExistingRegistration != nil {
					cfg.RelayHooks.BeforeExistingRegistration(threadID)
				}
				if !registerSubscription(ctx, relayKey, subscribeParams.ReplaceSubscription) {
					relayMu.Unlock()
					return context.Canceled
				}
				relayMu.Unlock()
				return nil
			}
			relayMu.Unlock()
			if err != nil {
				return err
			}
		}
		defer stopInitialCancellation()

		relayMu.Lock()
		active := relayedThreads[relayKey] == relayHandle && relayCtx.Err() == nil
		err := relayHandle.err
		if !active {
			if err == nil {
				err = context.Canceled
			}
			finishHandleLocked(relayHandle, err)
			relayMu.Unlock()
			return err
		}
		relayMu.Unlock()
		notifications, err := source.SubscribeThread(relayCtx, subscribeParams)
		if err != nil {
			cancelRelay()
			relayMu.Lock()
			if relayedThreads[relayKey] == relayHandle {
				delete(relayedThreads, relayKey)
			}
			if relayHandle.err != nil {
				err = relayHandle.err
			}
			finishHandleLocked(relayHandle, err)
			relayMu.Unlock()
			return err
		}
		relayMu.Lock()
		if relayedThreads[relayKey] != relayHandle || relayCtx.Err() != nil || ctx.Err() != nil {
			err = relayHandle.err
			if err == nil {
				err = ctx.Err()
				if err == nil {
					err = context.Canceled
				}
			}
			if relayedThreads[relayKey] == relayHandle {
				delete(relayedThreads, relayKey)
			}
			finishHandleLocked(relayHandle, err)
			relayMu.Unlock()
			cancelRelay()
			return err
		}
		if !registerSubscription(ctx, relayKey, subscribeParams.ReplaceSubscription) {
			delete(relayedThreads, relayKey)
			err = context.Canceled
			finishHandleLocked(relayHandle, err)
			relayMu.Unlock()
			cancelRelay()
			return err
		}
		relayHandle.established = true
		stopInitialCancellation()
		finishHandleLocked(relayHandle, nil)
		relayMu.Unlock()
		if cfg.RelayHooks.AfterReady != nil {
			cfg.RelayHooks.AfterReady(threadID)
		}
		relayMu.Lock()
		active = relayedThreads[relayKey] == relayHandle && relayCtx.Err() == nil
		err = relayHandle.err
		relayMu.Unlock()
		if !active {
			if err == nil {
				err = context.Canceled
			}
			cancelRelay()
			return err
		}
		if cfg.RelayHooks.BeforeLaunchCommit != nil {
			cfg.RelayHooks.BeforeLaunchCommit(threadID)
		}
		relayMu.Lock()
		active = relayedThreads[relayKey] == relayHandle && relayCtx.Err() == nil
		err = relayHandle.err
		if !active {
			relayMu.Unlock()
			if err == nil {
				err = context.Canceled
			}
			cancelRelay()
			return err
		}
		if cfg.RelayHooks.BeforeSupervisor != nil {
			cfg.RelayHooks.BeforeSupervisor(threadID)
		}
		go func() {
			ticker := time.NewTicker(relayIdleInterval)
			cleanupRelay := func() {
				cancelRelay()
				relayMu.Lock()
				if relayedThreads[relayKey] == relayHandle {
					delete(relayedThreads, relayKey)
				}
				relayMu.Unlock()
			}
			defer ticker.Stop()
			defer cleanupRelay()
			argsByCallID := map[string]string{}
			var backoff relayRetryBackoff
			// activeTurnID mirrors the thread's in-progress turn, tracked from the
			// same turn/started + turn/completed notifications this loop already
			// forwards, so giveUpOnActiveTurn knows whether a re-dial failure is
			// happening mid-turn (spinner visibly stalled) or between turns
			// (nothing on screen is waiting, so nothing needs to be told).
			var activeTurnID string
			var consecutiveFailures int
			trackActiveTurn := func(notification appwire.Notification) {
				switch notification.Method {
				case appwire.NotifyTurnStarted:
					var params struct {
						Turn struct {
							ID string `json:"id"`
						} `json:"turn"`
					}
					if json.Unmarshal(notification.Params, &params) == nil {
						activeTurnID = params.Turn.ID
					}
				case appwire.NotifyTurnCompleted:
					activeTurnID = ""
				}
			}
			// giveUpOnActiveTurn synthesizes the failed turn/completed the daemon
			// itself can no longer send (it is dead), so TurnFailureEndCap's
			// existing danger chip + "Reconnect & retry" button light up in place
			// of the spinner the reader has been watching. It fires at most once
			// per stall: clearing activeTurnID makes every later call in the same
			// stall a no-op, so continued backoff never re-broadcasts the same
			// failure.
			giveUpOnActiveTurn := func(cause error) {
				if activeTurnID == "" {
					return
				}
				turnID := activeTurnID
				activeTurnID = ""
				message := "Hub lost the connection to the session"
				if cause != nil {
					message += ": " + cause.Error()
				}
				server.Broadcast(relayKey, appwire.NotifyTurnCompleted, map[string]any{
					"turn": appwire.Turn{
						ID:     turnID,
						Status: appwire.TurnStatusFailed,
						Error: &appwire.TurnError{
							Message: message,
							Source:  "hub",
						},
					},
				})
			}
			recordFailure := func(cause error) {
				consecutiveFailures++
				if consecutiveFailures >= relayGiveUpAfterFailures {
					giveUpOnActiveTurn(cause)
				}
			}
			broadcastNotification := func(notification appwire.Notification) {
				backoff.Reset()
				consecutiveFailures = 0
				trackActiveTurn(notification)
				if source.ID() == "local" {
					notification = enrichOutputImageNotification(thread.SessionID, thread.CWD, argsByCallID, notification)
				}
				server.Broadcast(relayKey, notification.Method, notification.Params)
			}
			retireIfIdle := func() bool {
				if server.SubscriberCount(relayKey) != 0 {
					return false
				}
				if cfg.RelayHooks.IdleExit != nil {
					cfg.RelayHooks.IdleExit(threadID)
				}
				relayMu.Lock()
				if server.SubscriberCount(relayKey) != 0 {
					relayMu.Unlock()
					return false
				}
				if relayedThreads[relayKey] == relayHandle {
					delete(relayedThreads, relayKey)
				}
				relayMu.Unlock()
				if cfg.RelayHooks.AfterIdleDelete != nil {
					cfg.RelayHooks.AfterIdleDelete(threadID)
				}
				cancelRelay()
				return true
			}
			waitForRetry := func(delay time.Duration) bool {
				waitCtx, cancelWait := context.WithCancel(relayCtx)
				waitResult := make(chan error, 1)
				go func() {
					waitResult <- retryClock.Wait(waitCtx, delay)
				}()
				for {
					select {
					case err := <-waitResult:
						cancelWait()
						return err != nil
					case <-relayCtx.Done():
						cancelWait()
						<-waitResult
						return true
					case <-ticker.C:
						if retireIfIdle() {
							cancelWait()
							<-waitResult
							return true
						}
					}
				}
			}
			type subscribeResult struct {
				notifications <-chan appwire.Notification
				err           error
			}
			subscribeForRecovery := func() (subscribeResult, bool) {
				result := make(chan subscribeResult, 1)
				go func() {
					notifications, err := subscribeRelayRecovery(relayCtx, source, subscribeParams)
					result <- subscribeResult{notifications: notifications, err: err}
				}()
				for {
					select {
					case got := <-result:
						return got, false
					case <-relayCtx.Done():
						return <-result, true
					case <-ticker.C:
						if retireIfIdle() {
							return <-result, true
						}
					}
				}
			}
			for {
				if relayCtx.Err() != nil {
					return
				}
				if notifications == nil {
					result, stopped := subscribeForRecovery()
					if stopped {
						return
					}
					if result.err != nil {
						recordFailure(result.err)
						if waitForRetry(backoff.Next()) {
							return
						}
						continue
					}
					if result.notifications == nil {
						recordFailure(nil)
						if waitForRetry(backoff.Next()) {
							return
						}
						continue
					}
					select {
					case notification, ok := <-result.notifications:
						if !ok {
							recordFailure(nil)
							if waitForRetry(backoff.Next()) {
								return
							}
							continue
						}
						broadcastNotification(notification)
					default:
						backoff.Reset()
						consecutiveFailures = 0
					}
					notifications = result.notifications
				}
				select {
				case <-relayCtx.Done():
					return
				case <-ticker.C:
					if retireIfIdle() {
						return
					}
				case notification, ok := <-notifications:
					if !ok {
						notifications = nil
						continue
					}
					broadcastNotification(notification)
				}
			}
		}()
		relayMu.Unlock()
		return nil
	}
	startTurn := func(ctx context.Context, source appsource.Source, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		readParams := appwire.ThreadReadParams{Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: false}
		threadResp, err := source.ReadThread(ctx, readParams)
		if err != nil {
			return appwire.TurnStartResponse{}, err
		}
		if !threadActionAvailable(threadResp.Thread.Serf.Capabilities, "send") {
			return appwire.TurnStartResponse{}, appwire.Unavailable("send is not available for this session")
		}
		if err := startRelay(ctx, source, readParams, threadResp.Thread); err != nil {
			return appwire.TurnStartResponse{}, err
		}
		return source.StartTurn(ctx, params)
	}
	startRelayForThread := func(ctx context.Context, thread appwire.Thread) error {
		if thread.ID == "" {
			thread.ID = thread.SessionID
		}
		if thread.ID == "" {
			return nil
		}
		ref := thread.Serf.Ref
		if ref == "" {
			sourceID := strings.TrimSpace(thread.Source)
			if sourceID == "" {
				sourceID = "local"
			}
			ref = appwire.Ref{SourceID: sourceID, ThreadID: thread.ID}.String()
		}
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, ref, thread.ID)
		if err != nil {
			return nil //nolint:nilerr // best-effort relay: an unresolvable source means nothing to relay, not a caller error
		}
		if err := startRelay(ctx, source, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false}, thread); err != nil {
			if isSessionUnavailableError(err) {
				return nil
			}
			return err
		}
		return nil
	}
	stopRelay := func(key string) {
		relayMu.Lock()
		handle := relayedThreads[key]
		var cancel context.CancelFunc
		if handle != nil {
			delete(relayedThreads, key)
			finishHandleLocked(handle, context.Canceled)
			cancel = handle.cancel
		}
		relayMu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	return hubRelayFunctions{startRelay, startTurn, startRelayForThread, stopRelay}
}
