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
	ctx         context.Context
	cancel      context.CancelFunc
	established bool
	lease       appsource.RelaySessionLease
	commands    int
	closeOnce   sync.Once
	thread      appwire.Thread
}

type hubThreadReadResult struct {
	response appwire.ThreadReadResponse
	handoff  appsource.RelayHandoff
	release  func()
	once     sync.Once
}

func (r *hubThreadReadResult) finish(commit bool) {
	if r == nil || r.handoff == nil {
		return
	}
	r.once.Do(func() {
		if commit {
			r.handoff.Commit()
		} else {
			r.handoff.Abort()
		}
		if r.release != nil {
			r.release()
		}
	})
}

type hubRelaySubscriptionResult struct {
	notifications <-chan appwire.Notification
	err           error
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
	readThread          func(context.Context, appsource.Source, appwire.ThreadReadParams) (*hubThreadReadResult, error)
	captureThreadRead   func(context.Context, appwire.ThreadReadParams, *hubThreadReadResult) bool
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
	closeRelayHandle := func(handle *hubRelayHandle) {
		handle.closeOnce.Do(func() {
			handle.cancel()
			if handle.lease != nil {
				handle.lease.Close()
			}
		})
	}
	retireRelayHandle := func(relayKey string, handle *hubRelayHandle) {
		relayMu.Lock()
		if relayedThreads[relayKey] == handle {
			delete(relayedThreads, relayKey)
		}
		relayMu.Unlock()
		closeRelayHandle(handle)
	}
	relayTarget := func(source appsource.Source, params appwire.ThreadReadParams) (string, string, error) {
		threadID := strings.TrimSpace(params.ThreadID)
		if threadID == "" && params.Ref != "" {
			ref, err := appwire.ParseRef(params.Ref)
			if err != nil {
				return "", "", err
			}
			threadID = ref.ThreadID
		}
		if threadID == "" {
			return "", "", appwire.InvalidParams("threadId or ref is required")
		}
		return source.ID() + ":" + threadID, threadID, nil
	}
	startAcknowledgedFanout := func(
		relayKey string,
		threadID string,
		ref string,
		handle *hubRelayHandle,
		deliveries <-chan appsource.RelayDelivery,
	) {
		go func() {
			ticker := time.NewTicker(relayIdleInterval)
			defer ticker.Stop()
			defer retireRelayHandle(relayKey, handle)
			argsByCallID := map[string]string{}
			for {
				select {
				case <-handle.ctx.Done():
					return
				case <-ticker.C:
					relayMu.Lock()
					active := relayedThreads[relayKey] == handle
					commands := handle.commands
					candidate := active && commands == 0 && server.SubscriberCount(relayKey) == 0
					relayMu.Unlock()
					if !active {
						return
					}
					if !candidate {
						continue
					}
					if cfg.RelayHooks.IdleExit != nil {
						cfg.RelayHooks.IdleExit(threadID)
					}
					relayMu.Lock()
					retired := relayedThreads[relayKey] == handle &&
						handle.commands == 0 &&
						server.SubscriberCount(relayKey) == 0
					if retired {
						delete(relayedThreads, relayKey)
					}
					relayMu.Unlock()
					if retired {
						if cfg.RelayHooks.AfterIdleDelete != nil {
							cfg.RelayHooks.AfterIdleDelete(threadID)
						}
						return
					}
				case delivery, ok := <-deliveries:
					if !ok {
						return
					}
					relayMu.Lock()
					active := relayedThreads[relayKey] == handle
					thread := handle.thread
					relayMu.Unlock()
					if active {
						notification := delivery.Notification
						if strings.HasPrefix(relayKey, "local:") {
							notification = enrichOutputImageNotification(thread.SessionID, thread.CWD, argsByCallID, notification)
						}
						_, publicationErr := withDeletionTargetOwnership(cfg, ref, threadID, "", func() (struct{}, error) {
							server.Broadcast(relayKey, notification.Method, notification.Params)
							return struct{}{}, nil
						})
						if publicationErr != nil && isTargetDeletedError(publicationErr) {
							if delivery.Acknowledge != nil {
								delivery.Acknowledge()
							}
							return
						}
					}
					if delivery.Acknowledge != nil {
						delivery.Acknowledge()
					}
				}
			}
		}()
	}
	acquireRelaySession := func(
		ctx context.Context,
		source appsource.RelaySessionSource,
		base appsource.Source,
		params appwire.ThreadReadParams,
	) (*hubRelayHandle, func(), error) {
		relayKey, threadID, err := relayTarget(base, params)
		if err != nil {
			return nil, nil, err
		}
		for {
			relayMu.Lock()
			existing := relayedThreads[relayKey]
			if existing != nil {
				ready := existing.ready
				relayMu.Unlock()
				select {
				case <-ready:
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				}
				relayMu.Lock()
				if relayedThreads[relayKey] != existing || existing.err != nil {
					err := existing.err
					relayMu.Unlock()
					if err != nil {
						return nil, nil, err
					}
					continue
				}
				existing.commands++
				relayMu.Unlock()
				return existing, func() {
					relayMu.Lock()
					if existing.commands > 0 {
						existing.commands--
					}
					relayMu.Unlock()
				}, nil
			}
			relayCtx, cancelRelay := context.WithCancel(context.Background())
			handle := &hubRelayHandle{
				ready:    make(chan struct{}),
				ctx:      relayCtx,
				cancel:   cancelRelay,
				commands: 1,
			}
			relayedThreads[relayKey] = handle
			relayMu.Unlock()

			lease, acquireErr := source.AcquireRelaySession(params)
			var deliveries <-chan appsource.RelayDelivery
			if acquireErr == nil && lease == nil {
				acquireErr = appwire.SessionUnavailable("source returned no RelaySession lease")
			}
			if acquireErr == nil {
				relayMu.Lock()
				active := relayedThreads[relayKey] == handle
				if active {
					handle.lease = lease
				}
				relayMu.Unlock()
				if !active {
					acquireErr = context.Canceled
				}
			}
			if acquireErr == nil {
				deliveries, acquireErr = lease.Listen(relayCtx)
			}
			if acquireErr == nil && deliveries == nil {
				acquireErr = appwire.SessionUnavailable("RelaySession returned no delivery stream")
			}
			relayMu.Lock()
			if acquireErr != nil || relayedThreads[relayKey] != handle {
				if relayedThreads[relayKey] == handle {
					delete(relayedThreads, relayKey)
				}
				if acquireErr == nil {
					acquireErr = context.Canceled
				}
				finishHandleLocked(handle, acquireErr)
				relayMu.Unlock()
				closeRelayHandle(handle)
				return nil, nil, acquireErr
			}
			handle.established = true
			finishHandleLocked(handle, nil)
			relayMu.Unlock()
			startAcknowledgedFanout(relayKey, threadID, params.Ref, handle, deliveries)
			return handle, func() {
				relayMu.Lock()
				if handle.commands > 0 {
					handle.commands--
				}
				relayMu.Unlock()
			}, nil
		}
	}
	readThread := func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams) (*hubThreadReadResult, error) {
		needsRelay := params.Subscribe || relayOnThreadRead(source)
		relaySource, atomic := source.(appsource.RelaySessionSource)
		if !atomic || !needsRelay {
			response, err := source.ReadThread(ctx, params)
			return &hubThreadReadResult{response: response}, err
		}
		if err := deletionFenceError(cfg, params.Ref, params.ThreadID, ""); err != nil {
			return nil, err
		}
		handle, release, err := acquireRelaySession(ctx, relaySource, source, params)
		if err != nil {
			return nil, err
		}
		readParams := params
		readParams.Subscribe = true
		result, err := handle.lease.Read(ctx, readParams)
		if err != nil {
			release()
			return nil, err
		}
		if result.Handoff == nil {
			release()
			return nil, appwire.SessionUnavailable("atomic thread read returned no live continuation")
		}
		relayMu.Lock()
		if handle.lease != nil {
			handle.thread = result.Response.Thread
		}
		relayMu.Unlock()
		read := &hubThreadReadResult{
			response: result.Response,
			handoff:  result.Handoff,
			release:  release,
		}
		if err := deletionFenceError(cfg, params.Ref, read.response.Thread.ID, ""); err != nil {
			read.finish(false)
			return nil, err
		}
		return read, nil
	}
	captureThreadRead := func(ctx context.Context, params appwire.ThreadReadParams, read *hubThreadReadResult) bool {
		if read == nil || read.handoff == nil {
			return true
		}
		sourceID := strings.TrimSpace(read.response.Thread.Source)
		if ref, err := appwire.ParseRef(params.Ref); err == nil {
			sourceID = ref.SourceID
		}
		if sourceID == "" {
			sourceID = "local"
		}
		relayKey := sourceID + ":" + read.response.Thread.ID
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
		)
	}
	var startRelay func(context.Context, appsource.Source, appwire.ThreadReadParams, appwire.Thread) error
	prepareRelay := func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if _, atomic := source.(appsource.RelaySessionSource); !atomic {
			response, err := source.ReadThread(ctx, params)
			if err != nil {
				return appwire.ThreadReadResponse{}, err
			}
			if err := startRelay(ctx, source, params, response.Thread); err != nil {
				return appwire.ThreadReadResponse{}, err
			}
			return response, nil
		}
		params.Subscribe = true
		read, err := readThread(ctx, source, params)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		threadID := read.response.Thread.ID
		relayKey := source.ID() + ":" + threadID
		registered, err := withDeletionTargetOwnership(
			cfg,
			params.Ref,
			threadID,
			"",
			func() (bool, error) {
				return registerSubscription(ctx, relayKey, params.ReplaceSubscription), nil
			},
		)
		if err != nil || !registered {
			read.finish(false)
			if err != nil {
				return appwire.ThreadReadResponse{}, err
			}
			return appwire.ThreadReadResponse{}, context.Canceled
		}
		read.finish(true)
		return read.response, nil
	}
	startRelay = func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams, thread appwire.Thread) error {
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

			registerExisting := func() (bool, error) {
				relayMu.Lock()
				defer relayMu.Unlock()
				active := relayedThreads[relayKey] == existing
				err := existing.err
				if !active || err != nil {
					return false, err
				}
				if cfg.RelayHooks.BeforeExistingRegistration != nil {
					cfg.RelayHooks.BeforeExistingRegistration(threadID)
				}
				if !registerSubscription(ctx, relayKey, subscribeParams.ReplaceSubscription) {
					return false, context.Canceled
				}
				return true, nil
			}
			var registered bool
			var err error
			if contextOwnsDeletionTarget(ctx, subscribeParams.Ref, threadID) {
				if err := deletionFenceError(cfg, subscribeParams.Ref, threadID, ""); err != nil {
					return err
				}
				registered, err = registerExisting()
			} else {
				registered, err = withDeletionTargetOwnership(cfg, subscribeParams.Ref, threadID, "", registerExisting)
			}
			if err != nil {
				return err
			}
			if registered {
				return nil
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
		var notifications <-chan appwire.Notification
		if err := deletionFenceError(cfg, subscribeParams.Ref, threadID, ""); err != nil {
			return err
		}
		notifications, err = source.SubscribeThread(relayCtx, subscribeParams)
		if fenceErr := deletionFenceError(cfg, subscribeParams.Ref, threadID, ""); fenceErr != nil {
			err = fenceErr
		}
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
			subscribeForRecovery := func() (hubRelaySubscriptionResult, bool) {
				result := make(chan hubRelaySubscriptionResult, 1)
				go func() {
					if err := deletionFenceError(cfg, subscribeParams.Ref, threadID, ""); err != nil {
						result <- hubRelaySubscriptionResult{err: err}
						return
					}
					notifications, err := subscribeRelayRecovery(relayCtx, source, subscribeParams)
					if fenceErr := deletionFenceError(cfg, subscribeParams.Ref, threadID, ""); fenceErr != nil {
						err = fenceErr
					}
					result <- hubRelaySubscriptionResult{notifications: notifications, err: err}
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
						if isTargetDeletedError(result.err) {
							return
						}
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
					var firstNotification appwire.Notification
					hasFirstNotification := false
					select {
					case notification, ok := <-result.notifications:
						if !ok {
							recordFailure(nil)
							if waitForRetry(backoff.Next()) {
								return
							}
							continue
						}
						firstNotification = notification
						hasFirstNotification = true
					default:
					}
					server.Broadcast(relayKey, appwire.NotifySerfThreadResync, appwire.ThreadResyncParams{
						ThreadID: threadID,
						Ref:      subscribeParams.Ref,
					})
					if hasFirstNotification {
						broadcastNotification(firstNotification)
					} else {
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
		if _, err := prepareRelay(ctx, source, readParams); err != nil {
			if isTargetDeletedError(err) {
				if fenceErr := deletionFenceError(cfg, params.Ref, params.ThreadID, params.ClientMutationID); fenceErr != nil {
					return appwire.TurnStartResponse{}, fenceErr
				}
			}
			return appwire.TurnStartResponse{}, err
		}
		return withDeletionTargetOwnership(cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appwire.TurnStartResponse, error) {
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
		var relayErr error
		if _, atomic := source.(appsource.RelaySessionSource); atomic {
			_, relayErr = prepareRelay(ctx, source, appwire.ThreadReadParams{Ref: ref, ThreadID: thread.ID, IncludeTurns: false})
		} else {
			relayErr = startRelay(ctx, source, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false}, thread)
		}
		if relayErr != nil {
			if isSessionUnavailableError(relayErr) {
				return nil
			}
			return relayErr
		}
		return nil
	}
	stopRelay := func(key string) {
		relayMu.Lock()
		handle := relayedThreads[key]
		if handle != nil {
			delete(relayedThreads, key)
			finishHandleLocked(handle, context.Canceled)
		}
		relayMu.Unlock()
		if handle != nil {
			closeRelayHandle(handle)
		}
	}
	return hubRelayFunctions{
		startRelay:          startRelay,
		readThread:          readThread,
		captureThreadRead:   captureThreadRead,
		startTurn:           startTurn,
		startRelayForThread: startRelayForThread,
		stopRelay:           stopRelay,
	}
}
