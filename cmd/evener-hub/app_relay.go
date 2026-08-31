package hub

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

type hubRelayHandle struct {
	ready       chan struct{}
	err         error
	ctx         context.Context
	cancel      context.CancelFunc
	established bool
	lease       appsource.RelaySessionLease
	closeOnce   sync.Once
	canonical   appwire.Ref
	relayKeys   map[string]*relayKeyState
	routes      map[string]*relayKeyState
	// commandOwners includes commands whose relay-key generation was remapped
	// while they were in flight, so the displaced handle cannot close early.
	commandOwners int
}

type relayKeyState struct {
	commands      int
	relayKey      string
	thread        appwire.Thread
	argsByCallID  map[string]string
	routingKeys   map[string]struct{}
	stopRequested bool
}

type hubThreadReadResult struct {
	response appwire.ThreadReadResponse
	handoff  appsource.RelayHandoff
	release  func()
	once     sync.Once
}

func (r *hubThreadReadResult) finish(commit bool) bool {
	if r == nil || r.handoff == nil {
		return false
	}
	finished := false
	r.once.Do(func() {
		if commit {
			finished = r.handoff.Commit()
		} else {
			finished = r.handoff.Abort()
		}
		if r.release != nil {
			r.release()
		}
	})
	return finished
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

// stampClosedThreadCapabilities fills in the one status frame a daemon cannot
// describe: its own close (kata pk2d).
//
// Every other thread/status/changed already carries the action set that goes
// with the status it announces, stamped by the daemon at its own notification
// egress (server/appwire_runtime.go's stampCapabilitiesOnStatusChange). The
// close frame deliberately carries none, and rightly so: what a thread can
// still be asked to do once its daemon is gone is not the daemon's to say. It
// is this hub's — the next read is answered from the past index and a send
// there resumes the session (kata qp94), which is exactly what
// pastThreadCapabilities advertises.
//
// Left unstamped, a client keeps whatever the departing daemon last pushed. For
// a session that shut down MID-TURN that set says send=false, because the
// daemon gates Send on "no turn in flight" — and an ended thread's composer is
// a follow-up card gated on precisely that bit, so the whole composer unmounts:
// no card, no textarea, no Send, until the page is reloaded. The reload heals
// it by asking the hub, so the hub answers here instead, at the moment of
// close. A status and the capabilities beside it then agree however the thread
// ended, and what the close pushes is what the next read would return.
//
// Local threads only, which is every thread that reaches this relay: the past
// index is the local source's, and a source the hub does NOT answer from it
// (the Codex bridge) would be told a resume story that is not true.
//
// One key is replaced, the rest of the payload is passed through as raw JSON —
// re-minting it from the fields this hub understands would silently drop
// anything a newer daemon added (the shape enrichOutputImageNotification uses
// on this same stream, for the same reason).
func stampClosedThreadCapabilities(notification appwire.Notification) appwire.Notification {
	if notification.Method != appwire.NotifyThreadStatusChanged {
		return notification
	}
	var params map[string]json.RawMessage
	if len(notification.Params) == 0 || json.Unmarshal(notification.Params, &params) != nil {
		return notification
	}
	var status appwire.ThreadStatus
	if raw := params["status"]; len(raw) == 0 || json.Unmarshal(raw, &status) != nil {
		return notification
	}
	if status.Type != appwire.ThreadStatusClosed {
		return notification
	}
	capabilities, err := json.Marshal(pastThreadCapabilities())
	if err != nil {
		return notification
	}
	params["capabilities"] = capabilities
	stamped, err := json.Marshal(params)
	if err != nil {
		return notification
	}
	notification.Params = stamped
	return notification
}

type hubRelayFunctions struct {
	startRelay          func(context.Context, appsource.Source, appwire.ThreadReadParams, appwire.Thread) error
	readThread          func(context.Context, appsource.Source, appwire.ThreadReadParams) (*hubThreadReadResult, error)
	captureThreadRead   func(context.Context, appwire.ThreadReadParams, *hubThreadReadResult) bool
	startTurn           func(context.Context, appsource.Source, appwire.TurnStartParams) (appwire.TurnStartResponse, error)
	startRelayForThread func(context.Context, appwire.Thread) error
	stopRelay           func(string)
	stopCanonicalRelay  func(appwire.Ref)
	relayCommandCount   func(string) int
}

var observeHubRelayFunctions func(hubRelayFunctions)
var observeHubRelayWait func()

// threadRelayTarget resolves the relay key (source.ID()+":"+threadID) and the
// bare threadID for a read-shaped request. thread/read's relay, its recovery
// paths, and thread/unsubscribe all derive the key here so a subscribe and
// its unsubscribe can never disagree about which registry entry they name.
func threadRelayTarget(source appsource.Source, params appwire.ThreadReadParams) (string, string, error) {
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

type relayNotificationRouting int

const (
	relayNotificationUntargeted relayNotificationRouting = iota
	relayNotificationMalformed
	relayNotificationTargeted
)

// relayNotificationRoutingKey normalizes the authoritative identity carried
// by a relay frame. String ref has the same precedence as the browser reducer,
// including empty and syntactically invalid strings. Wrong-typed present
// fields are malformed rather than absent, so they cannot fall through.
func relayNotificationRoutingKey(notification appwire.Notification, sourceID string) (string, relayNotificationRouting) {
	var params map[string]json.RawMessage
	if len(notification.Params) == 0 || json.Unmarshal(notification.Params, &params) != nil {
		return "", relayNotificationMalformed
	}
	if raw, ok := params["ref"]; ok {
		var value any
		if json.Unmarshal(raw, &value) != nil {
			return "", relayNotificationMalformed
		}
		ref, ok := value.(string)
		if !ok {
			return "", relayNotificationMalformed
		}
		return ref, relayNotificationTargeted
	}
	if raw, ok := params["threadId"]; ok {
		var value any
		if json.Unmarshal(raw, &value) != nil {
			return "", relayNotificationMalformed
		}
		threadID, ok := value.(string)
		if !ok {
			return "", relayNotificationMalformed
		}
		return sourceID + ":" + threadID, relayNotificationTargeted
	}
	return "", relayNotificationUntargeted
}

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
	relayTarget := threadRelayTarget
	var relayMu sync.Mutex
	relayedThreads := map[string]*hubRelayHandle{}
	canonicalRelays := map[appwire.Ref]*hubRelayHandle{}
	removeStateRoutesLocked := func(handle *hubRelayHandle, state *relayKeyState) {
		if state == nil {
			return
		}
		for routingKey := range state.routingKeys {
			if handle.routes[routingKey] == state {
				delete(handle.routes, routingKey)
			}
			delete(state.routingKeys, routingKey)
		}
	}
	bindStateRouteLocked := func(handle *hubRelayHandle, routingKey string, state *relayKeyState) {
		if routingKey == "" {
			return
		}
		if previous := handle.routes[routingKey]; previous != nil && previous != state {
			delete(previous.routingKeys, routingKey)
		}
		handle.routes[routingKey] = state
		state.routingKeys[routingKey] = struct{}{}
	}
	removeRelayKeyStateLocked := func(handle *hubRelayHandle, state *relayKeyState) bool {
		if state == nil || relayedThreads[state.relayKey] != handle || handle.relayKeys[state.relayKey] != state {
			return false
		}
		removeStateRoutesLocked(handle, state)
		delete(handle.relayKeys, state.relayKey)
		delete(relayedThreads, state.relayKey)
		return true
	}
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
	removeRelayHandleLocked := func(handle *hubRelayHandle) {
		if handle.canonical != (appwire.Ref{}) && canonicalRelays[handle.canonical] == handle {
			delete(canonicalRelays, handle.canonical)
		}
		for relayKey, current := range relayedThreads {
			if current == handle {
				delete(relayedThreads, relayKey)
			}
		}
		for relayKey, state := range handle.relayKeys {
			removeStateRoutesLocked(handle, state)
			delete(handle.relayKeys, relayKey)
		}
		for routingKey := range handle.routes {
			delete(handle.routes, routingKey)
		}
	}
	retireRelayHandle := func(handle *hubRelayHandle) {
		relayMu.Lock()
		removeRelayHandleLocked(handle)
		relayMu.Unlock()
		closeRelayHandle(handle)
	}
	startAcknowledgedFanout := func(
		handle *hubRelayHandle,
		deliveries <-chan appsource.RelayDelivery,
	) {
		go func() {
			ticker := time.NewTicker(relayIdleInterval)
			defer ticker.Stop()
			defer retireRelayHandle(handle)
			for {
				select {
				case <-handle.ctx.Done():
					return
				case <-ticker.C:
					type idleCandidate struct {
						relayKey string
						state    *relayKeyState
						commands int
					}
					relayMu.Lock()
					active := canonicalRelays[handle.canonical] == handle
					candidates := make([]idleCandidate, 0, len(handle.relayKeys))
					if active {
						for relayKey, state := range handle.relayKeys {
							if relayedThreads[relayKey] == handle && state.commands == 0 {
								candidates = append(candidates, idleCandidate{
									relayKey: relayKey,
									state:    state,
									commands: state.commands,
								})
							}
						}
						if len(handle.relayKeys) == 0 && handle.commandOwners == 0 {
							delete(canonicalRelays, handle.canonical)
							active = false
						}
					}
					relayMu.Unlock()
					if !active {
						return
					}
					idleCandidates := candidates[:0]
					for _, candidate := range candidates {
						if server.SubscriberCount(candidate.relayKey) == 0 {
							idleCandidates = append(idleCandidates, candidate)
						}
					}
					if len(idleCandidates) == 0 {
						continue
					}
					if cfg.RelayHooks.IdleExit != nil {
						cfg.RelayHooks.IdleExit(handle.canonical.ThreadID)
					}
					revalidated := idleCandidates[:0]
					for _, candidate := range idleCandidates {
						if server.SubscriberCount(candidate.relayKey) == 0 {
							revalidated = append(revalidated, candidate)
						}
					}
					if len(revalidated) == 0 {
						continue
					}
					relayMu.Lock()
					removed := 0
					if canonicalRelays[handle.canonical] == handle {
						for _, candidate := range revalidated {
							if relayedThreads[candidate.relayKey] == handle &&
								handle.relayKeys[candidate.relayKey] == candidate.state &&
								candidate.state.commands == candidate.commands {
								removeStateRoutesLocked(handle, candidate.state)
								delete(handle.relayKeys, candidate.relayKey)
								delete(relayedThreads, candidate.relayKey)
								removed++
							}
						}
					}
					retired := canonicalRelays[handle.canonical] == handle &&
						len(handle.relayKeys) == 0 && handle.commandOwners == 0
					if retired {
						delete(canonicalRelays, handle.canonical)
					}
					relayMu.Unlock()
					for range removed {
						if cfg.RelayHooks.AfterIdleDelete != nil {
							cfg.RelayHooks.AfterIdleDelete(handle.canonical.ThreadID)
						}
					}
					if retired {
						return
					}
				case delivery, ok := <-deliveries:
					if !ok {
						return
					}
					type relayTargetState struct {
						relayKey     string
						threadID     string
						ref          string
						thread       appwire.Thread
						argsByCallID map[string]string
					}
					targetState := func(relayKey string, state *relayKeyState) relayTargetState {
						parsedRef, _ := appwire.ParseRef(relayKey)
						target := relayTargetState{
							relayKey:     relayKey,
							threadID:     parsedRef.ThreadID,
							ref:          relayKey,
							thread:       state.thread,
							argsByCallID: state.argsByCallID,
						}
						if state.thread.ID != "" {
							target.threadID = state.thread.ID
						}
						if state.thread.Evener.Ref != "" {
							target.ref = state.thread.Evener.Ref
						}
						return target
					}
					relayKey, routing := relayNotificationRoutingKey(delivery.Notification, handle.canonical.SourceID)
					relayMu.Lock()
					var targets []relayTargetState
					if routing == relayNotificationTargeted {
						state := handle.routes[relayKey]
						if state != nil && handle.relayKeys[state.relayKey] == state && relayedThreads[state.relayKey] == handle {
							targets = append(targets, targetState(state.relayKey, state))
						}
					} else {
						targets = make([]relayTargetState, 0, len(handle.relayKeys))
						for currentKey, state := range handle.relayKeys {
							if relayedThreads[currentKey] == handle {
								targets = append(targets, targetState(currentKey, state))
							}
						}
					}
					relayMu.Unlock()
					for _, target := range targets {
						notification := delivery.Notification
						// The edits only this hub can make to a local daemon's
						// notification on its way to a browser: the images it can
						// resolve off disk, and the answer to what a thread can still
						// be asked to do once the daemon announcing its own close is
						// gone.
						if strings.HasPrefix(target.relayKey, "local:") {
							notification = enrichOutputImageNotification(target.thread.SessionID, target.thread.CWD, target.argsByCallID, notification)
							notification = stampClosedThreadCapabilities(notification)
						}
						_, publicationErr := withDeletionTargetOwnership(cfg, target.ref, target.threadID, "", func() (struct{}, error) {
							server.Broadcast(target.relayKey, notification.Method, notification.Params)
							return struct{}{}, nil
						})
						if publicationErr != nil && isTargetDeletedError(publicationErr) {
							continue
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
	) (*hubRelayHandle, *relayKeyState, func(), error) {
		relayKey, _, err := relayTarget(base, params)
		if err != nil {
			return nil, nil, nil, err
		}
		canonicalRef, err := source.ResolveRelaySession(params)
		if err != nil {
			return nil, nil, nil, err
		}
		bindRelayKeyLocked := func(handle *hubRelayHandle) *relayKeyState {
			var inheritedRoutes []string
			if previous := relayedThreads[relayKey]; previous != nil && previous != handle {
				previousState := previous.relayKeys[relayKey]
				if previousState != nil {
					inheritedRoutes = make([]string, 0, len(previousState.routingKeys))
					for routingKey := range previousState.routingKeys {
						inheritedRoutes = append(inheritedRoutes, routingKey)
					}
				}
				removeStateRoutesLocked(previous, previousState)
				delete(previous.relayKeys, relayKey)
			}
			if handle.routes == nil {
				handle.routes = make(map[string]*relayKeyState)
			}
			state := handle.relayKeys[relayKey]
			if relayedThreads[relayKey] != handle || state == nil {
				state = &relayKeyState{
					relayKey:     relayKey,
					argsByCallID: make(map[string]string),
					routingKeys:  make(map[string]struct{}),
				}
				handle.relayKeys[relayKey] = state
			}
			for _, routingKey := range inheritedRoutes {
				bindStateRouteLocked(handle, routingKey, state)
			}
			bindStateRouteLocked(handle, relayKey, state)
			// Publish downstream ownership only after the replacement state owns
			// every route inherited from the displaced generation. Fanout takes
			// this same mutex, so it observes the complete transition or neither.
			relayedThreads[relayKey] = handle
			state.commands++
			handle.commandOwners++
			return state
		}
		releaseRelayKey := func(handle *hubRelayHandle, state *relayKeyState) func() {
			return func() {
				var closeHandle *hubRelayHandle
				relayMu.Lock()
				if state.commands > 0 && handle.commandOwners > 0 {
					state.commands--
					handle.commandOwners--
				}
				if state.stopRequested && state.commands == 0 {
					removeRelayKeyStateLocked(handle, state)
				}
				if handle.canonical != (appwire.Ref{}) &&
					canonicalRelays[handle.canonical] == handle &&
					len(handle.relayKeys) == 0 && handle.commandOwners == 0 {
					removeRelayHandleLocked(handle)
					finishHandleLocked(handle, context.Canceled)
					closeHandle = handle
				}
				relayMu.Unlock()
				if closeHandle != nil {
					closeRelayHandle(closeHandle)
				}
			}
		}
		for {
			relayMu.Lock()
			existing := canonicalRelays[canonicalRef]
			if existing != nil {
				ready := existing.ready
				relayMu.Unlock()
				select {
				case <-ready:
				case <-ctx.Done():
					return nil, nil, nil, ctx.Err()
				}
				relayMu.Lock()
				if canonicalRelays[canonicalRef] != existing || existing.err != nil {
					err := existing.err
					relayMu.Unlock()
					if err != nil {
						return nil, nil, nil, err
					}
					continue
				}
				state := bindRelayKeyLocked(existing)
				relayMu.Unlock()
				return existing, state, releaseRelayKey(existing, state), nil
			}
			relayCtx, cancelRelay := context.WithCancel(context.Background())
			handle := &hubRelayHandle{
				ready:     make(chan struct{}),
				ctx:       relayCtx,
				cancel:    cancelRelay,
				canonical: canonicalRef,
				relayKeys: make(map[string]*relayKeyState),
				routes:    make(map[string]*relayKeyState),
			}
			canonicalRelays[canonicalRef] = handle
			relayMu.Unlock()

			lease, acquireErr := source.AcquireRelaySession(canonicalRef)
			var deliveries <-chan appsource.RelayDelivery
			if acquireErr == nil && lease == nil {
				acquireErr = appwire.SessionUnavailable("source returned no RelaySession lease")
			}
			if acquireErr == nil {
				relayMu.Lock()
				active := canonicalRelays[canonicalRef] == handle
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
			if acquireErr != nil || canonicalRelays[canonicalRef] != handle {
				if canonicalRelays[canonicalRef] == handle {
					delete(canonicalRelays, canonicalRef)
				}
				if acquireErr == nil {
					acquireErr = context.Canceled
				}
				finishHandleLocked(handle, acquireErr)
				relayMu.Unlock()
				closeRelayHandle(handle)
				return nil, nil, nil, acquireErr
			}
			handle.established = true
			state := bindRelayKeyLocked(handle)
			finishHandleLocked(handle, nil)
			relayMu.Unlock()
			startAcknowledgedFanout(handle, deliveries)
			return handle, state, releaseRelayKey(handle, state), nil
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
		handle, state, release, err := acquireRelaySession(ctx, relaySource, source, params)
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
		for relayKey, current := range handle.relayKeys {
			if current == state && relayedThreads[relayKey] == handle {
				removeStateRoutesLocked(handle, current)
				current.thread = result.Response.Thread
				bindStateRouteLocked(handle, relayKey, current)
				sourceID := result.Response.Thread.Source
				if sourceID == "" {
					sourceID = handle.canonical.SourceID
				}
				if result.Response.Thread.ID != "" {
					bindStateRouteLocked(handle, sourceID+":"+result.Response.Thread.ID, current)
				}
				bindStateRouteLocked(handle, result.Response.Thread.Evener.Ref, current)
				break
			}
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
		// Keyed on the response's Thread.ID rather than the request's
		// ref.ThreadID: the daemon maps a stable ref back to the live session
		// id before answering (server/appwire_runtime.go appThreadIDForRead),
		// so the two coincide on every reachable path, and thread/unsubscribe
		// resolves through the same mapping. Kept explicit so a future source
		// that lets them diverge shows exactly where to look.
		relayKey := sourceID + ":" + read.response.Thread.ID
		captured, err := withDeletionTargetOwnership(
			cfg,
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
				), nil
			},
		)
		if err != nil || !captured {
			read.finish(false)
			return false
		}
		return true
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
				if !read.handoff.Prepare() {
					return false, appwire.SessionUnavailable("relay handoff could not be prepared")
				}
				if !registerSubscription(ctx, relayKey, params.ReplaceSubscription) {
					return false, nil
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
	startRelay = func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams, thread appwire.Thread) error {
		threadID := thread.ID
		if threadID == "" {
			return nil
		}
		relayKey := source.ID() + ":" + threadID

		subscribeParams := params
		if subscribeParams.Ref == "" {
			subscribeParams.Ref = thread.Evener.Ref
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
			registered, err := withDeletionTargetOwnership(cfg, subscribeParams.Ref, threadID, "", registerExisting)
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
					server.Broadcast(relayKey, appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
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
		ref := thread.Evener.Ref
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
	stopCanonicalRelay := func(ref appwire.Ref) {
		relayMu.Lock()
		handle := canonicalRelays[ref]
		if handle != nil {
			removeRelayHandleLocked(handle)
			finishHandleLocked(handle, context.Canceled)
		}
		relayMu.Unlock()
		if handle != nil {
			closeRelayHandle(handle)
		}
	}
	stopRelay := func(relayKey string) {
		var closeHandle *hubRelayHandle
		relayMu.Lock()
		handle := relayedThreads[relayKey]
		if handle != nil && handle.canonical == (appwire.Ref{}) {
			removeRelayHandleLocked(handle)
			finishHandleLocked(handle, context.Canceled)
			closeHandle = handle
		} else if handle != nil {
			state := handle.relayKeys[relayKey]
			if state != nil && state.commands != 0 {
				state.stopRequested = true
			} else {
				removeRelayKeyStateLocked(handle, state)
			}
			if len(handle.relayKeys) == 0 && handle.commandOwners == 0 {
				removeRelayHandleLocked(handle)
				finishHandleLocked(handle, context.Canceled)
				closeHandle = handle
			}
		}
		relayMu.Unlock()
		if closeHandle != nil {
			closeRelayHandle(closeHandle)
		}
	}
	relayCommandCount := func(key string) int {
		relayMu.Lock()
		defer relayMu.Unlock()
		handle := relayedThreads[key]
		if handle == nil {
			return 0
		}
		state := handle.relayKeys[key]
		if state == nil {
			return 0
		}
		return state.commands
	}
	return hubRelayFunctions{
		startRelay:          startRelay,
		readThread:          readThread,
		captureThreadRead:   captureThreadRead,
		startTurn:           startTurn,
		startRelayForThread: startRelayForThread,
		stopRelay:           stopRelay,
		stopCanonicalRelay:  stopCanonicalRelay,
		relayCommandCount:   relayCommandCount,
	}
}
