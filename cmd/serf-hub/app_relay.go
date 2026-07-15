package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/appserver"
)

type hubRelayHandle struct {
	ready  chan struct{}
	err    error
	cancel context.CancelFunc
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
	var retryClock relayRetryClock = newRelayRetryClock()
	if cfg.RelayHooks.RetryWait != nil {
		retryClock = relayRetryClockFunc(cfg.RelayHooks.RetryWait)
	}
	var relayMu sync.Mutex
	relayedThreads := map[string]*hubRelayHandle{}
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

		var relayHandle *hubRelayHandle
		for {
			relayMu.Lock()
			existing := relayedThreads[relayKey]
			if existing == nil {
				relayHandle = &hubRelayHandle{ready: make(chan struct{})}
				relayedThreads[relayKey] = relayHandle
				relayMu.Unlock()
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
			relayMu.Unlock()
			if active && err == nil {
				if subscribeParams.ReplaceSubscription {
					appserver.ReplaceSubscriptions(ctx, relayKey)
				} else {
					appserver.Subscribe(ctx, relayKey)
				}
				return nil
			}
			if err != nil {
				return err
			}
		}

		relayCtx, cancelRelay := context.WithCancel(context.WithoutCancel(ctx))
		relayMu.Lock()
		relayHandle.cancel = cancelRelay
		relayMu.Unlock()
		notifications, err := source.SubscribeThread(relayCtx, subscribeParams)
		if err != nil {
			cancelRelay()
			relayMu.Lock()
			if relayedThreads[relayKey] == relayHandle {
				delete(relayedThreads, relayKey)
			}
			relayHandle.err = err
			close(relayHandle.ready)
			relayMu.Unlock()
			return err
		}
		if subscribeParams.ReplaceSubscription {
			appserver.ReplaceSubscriptions(ctx, relayKey)
		} else {
			appserver.Subscribe(ctx, relayKey)
		}
		relayMu.Lock()
		close(relayHandle.ready)
		relayMu.Unlock()
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
			for {
				if relayCtx.Err() != nil {
					return
				}
				if notifications == nil {
					next, err := subscribeRelayRecovery(relayCtx, source, subscribeParams)
					if err != nil {
						if waitForRetry(backoff.Next()) {
							return
						}
						continue
					}
					notifications = next
					backoff.Reset()
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
					backoff.Reset()
					if source.ID() == "local" {
						notification = enrichOutputImageNotification(thread.SessionID, thread.CWD, argsByCallID, notification)
					}
					server.Broadcast(relayKey, notification.Method, notification.Params)
				}
			}
		}()
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
			cancel = handle.cancel
		}
		relayMu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	return hubRelayFunctions{startRelay, startTurn, startRelayForThread, stopRelay}
}
