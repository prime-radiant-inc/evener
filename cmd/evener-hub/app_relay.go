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
	ready         chan struct{}
	err           error
	ctx           context.Context
	cancel        context.CancelFunc
	established   bool
	lease         appsource.RelaySessionRoutePublicationLease
	closeOnce     sync.Once
	canonical     appwire.Ref
	done          chan struct{}
	initializing  bool
	stopping      bool
	removed       bool
	relayKeys     map[string]*relayKeyState
	pendingKeys   map[string]*relayKeyState
	pendingStates map[*relayKeyState]struct{}
	routes        map[string]*relayKeyState
	// commandOwners includes commands whose relay-key generation was remapped
	// while they were in flight, so the displaced handle cannot close early.
	commandOwners int
	pendingRoutes int
	routeChanged  chan struct{}
}

type relayKeyState struct {
	commands        int
	generation      uint64
	relayKey        string
	thread          appwire.Thread
	argsByCallID    map[string]string
	routingKeys     map[string]struct{}
	stopRequested   bool
	retiring        bool
	removeOnDrain   bool
	retireOwner     *relayKeyState
	publications    int
	publicationDone chan struct{}
	done            chan struct{}
}

type hubThreadReadResult struct {
	// relayKey is the acquired delivery owner, not the response's session ID.
	relayKey          string
	lifecycleKey      string
	response          appwire.ThreadReadResponse
	itemCandidates    appsource.ItemCandidateResult
	hasItemCandidates bool
	handoff           appsource.RelayHandoff
	release           func()
	once              sync.Once
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

// hubRelayPendingDeliveryLimit bounds unresolved targeted frames per canonical
// listener. At the limit the listener applies upstream backpressure; it never
// acknowledges speculatively or allocates a worker per attacker-chosen target.
const hubRelayPendingDeliveryLimit = 64

// hubTransientOwnershipBudget bounds how long a path without a request
// context of its own waits for a session alias that a deletion or a
// long-running Resume may hold: long enough for transient ownership to
// resolve, bounded so the caller cannot be parked indefinitely. The
// workspaceData reads in web_workspace.go keep their own inline 3s budgets;
// those sites predate this constant.
const hubTransientOwnershipBudget = 3 * time.Second

// relayPublicationGuardTimeout bounds how long the per-frame publication guard
// waits for the target alias. A deletion or a long-running Resume holds that
// alias; a bounded wait lets transient ownership resolve so an acknowledged
// frame is still published, while the fan-out and its publicationDone drain can
// never be parked indefinitely.
const relayPublicationGuardTimeout = hubTransientOwnershipBudget

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
// (a non-local source) would be told a resume story that is not true.
//
// One key is replaced, the rest of the payload is passed through as raw JSON —
// re-minting it from the fields this hub understands would silently drop
// anything a newer daemon added (the shape enrichOutputImageNotification uses
// on this same stream, for the same reason).
func stampClosedThreadCapabilities(notification appwire.Notification, allowFork bool) appwire.Notification {
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
	set := pastThreadCapabilities()
	if !allowFork {
		set.ForkFromTurn = false
	}
	capabilities, err := json.Marshal(set)
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

// relayGaveUpCapabilities is the action set the relay advertises beside the
// idle status it synthesizes when a mid-turn daemon stops answering. It is the
// hub's own answer, so it applies the same gate stampClosedThreadCapabilities
// uses: only a local thread is this hub's to answer for, because only a local
// session is the past index's to resume. A non-local (federated) source keeps
// the masked set it sent — nil means "no update", and advertising a resume
// story the hub cannot honour would offer actions it would refuse. The fork
// field is resolved through the same ownership fence a past read uses, so the
// pushed set and the read that follows it cannot drift.
func relayGaveUpCapabilities(cfg hubcore.WebConfig, relayKey string, thread appwire.Thread) *appwire.ThreadCapabilities {
	if !strings.HasPrefix(relayKey, "local:") {
		return nil
	}
	set := pastThreadCapabilities()
	set.ForkFromTurn = applyHubForkCapability(cfg, thread).Evener.Capabilities.ForkFromTurn
	return &set
}

// stampResyncTarget names the route a fanned-out daemon-gone resync is being
// delivered to. The relay publishes that frame with no target so it reaches
// every route the relay serves (a read-only child alias shares the root's
// relay session), and each subscriber acts on the ref it names, so each copy
// names the subscriber's own.
func stampResyncTarget(notification appwire.Notification, threadID, ref string) appwire.Notification {
	params, err := json.Marshal(appwire.ThreadResyncParams{ThreadID: threadID, Ref: ref})
	if err != nil {
		return notification
	}
	notification.Params = params
	return notification
}

// stampForkCapability adds the hub-owned action to an existing capability
// update. Other permissions and fields remain the daemon's current values.
func stampForkCapability(notification appwire.Notification, allowFork bool) appwire.Notification {
	if notification.Method != appwire.NotifyThreadStatusChanged {
		return notification
	}
	var params struct {
		Status       appwire.ThreadStatus       `json:"status"`
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if json.Unmarshal(notification.Params, &params) != nil || params.Capabilities == nil {
		return notification
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(notification.Params, &raw); err != nil {
		return notification
	}
	fenced := hubForkRecoveryFenced(appwire.Thread{Status: params.Status})
	if allowFork && !fenced {
		params.Capabilities["forkFromTurn"] = json.RawMessage("true")
	} else {
		// Clear a stale daemon value as well as refusing to add the action. The
		// notification is the client's authoritative status transition.
		params.Capabilities["forkFromTurn"] = json.RawMessage("false")
	}
	encoded, err := json.Marshal(params.Capabilities)
	if err != nil {
		return notification
	}
	raw["capabilities"] = encoded
	stamped, err := json.Marshal(raw)
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
	relayPublished      func(string) bool
}

var observeHubRelayFunctions func(hubRelayFunctions)
var observeHubRelayWait func()

// threadRelayTarget resolves the relay key (source.ID()+":"+threadID) and the
// bare threadID for a read-shaped request. This is the delivery routing
// identity, not the canonical session identity used to cancel pending reads.
// thread/read's relay, its recovery paths, and thread/unsubscribe use it.
func threadRelayTarget(source appsource.Source, params appwire.ThreadReadParams) (string, string, error) {
	threadID := strings.TrimSpace(params.ThreadID)
	refInput := strings.TrimSpace(params.Ref)
	if threadID == "" && refInput != "" {
		ref, err := appwire.ParseRef(refInput)
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

// relayDeliveryTarget resolves the relay key a subscription is registered under,
// identically for thread/read's relay and the thread/unsubscribe that drops it.
//
// A federated source (anything but the local daemon) is addressed by its ref:
// the ref's suffix is the stable identity that survives an identity replacement,
// while a caller's bare threadId is the volatile current ID that moves across
// one. Keying such a read by the caller's threadId registered the relay under
// "host:<currentID>" while a ref-only thread/unsubscribe resolves
// "host:<stableRef>", so the downstream entry — and the source-side subscription
// behind it — was never dropped. The ref therefore wins whenever the request
// carries one, which is also what the local daemon path achieves through
// ResolveSubscriptionAdmission. The local daemon keeps threadRelayTarget's
// threadId preference: its admission already canonicalizes the identities, and
// its SecondaryKey deliberately names the delivery identity threadRelayTarget
// resolves.
func relayDeliveryTarget(source appsource.Source, params appwire.ThreadReadParams) (string, string, error) {
	if _, local := source.(*appsource.LocalDaemonSource); !local && strings.TrimSpace(params.Ref) != "" {
		ref, err := appwire.ParseRef(strings.TrimSpace(params.Ref))
		if err != nil {
			return "", "", err
		}
		if ref.ThreadID == "" {
			return "", "", appwire.InvalidParams("threadId or ref is required")
		}
		return source.ID() + ":" + ref.ThreadID, ref.ThreadID, nil
	}
	return threadRelayTarget(source, params)
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
	// The relay's background goroutines fence against the lookup captured
	// here, never the live seam: they outlive the request that started them.
	relayTargetState := deletionTargetState
	backgroundFenceError := func(ref, threadID string) error {
		return relayTargetState.fenceError(cfg, ref, threadID, ref, "")
	}
	retryClock := newRelayRetryClock()
	if cfg.RelayHooks.RetryWait != nil {
		retryClock = relayRetryClockFunc(cfg.RelayHooks.RetryWait)
	}
	registerSubscription := func(ctx context.Context, relayKey string, replace bool, expectedLifecycle ...string) error {
		if cfg.RelayHooks.RegisterSubscription != nil {
			if !cfg.RelayHooks.RegisterSubscription(ctx, relayKey, replace) {
				return context.Canceled
			}
			return nil
		}
		return appserver.SubscribeWithAdmission(ctx, relayKey, replace, expectedLifecycle...)
	}
	relayTarget := threadRelayTarget
	var relayMu sync.Mutex
	relayedThreads := map[string]*hubRelayHandle{}
	pendingRelays := map[string]map[*relayKeyState]*hubRelayHandle{}
	canonicalRelays := map[appwire.Ref]*hubRelayHandle{}
	relayGenerations := map[string]uint64{}
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
		if state == nil || state.publications != 0 || relayedThreads[state.relayKey] != handle || handle.relayKeys[state.relayKey] != state {
			return false
		}
		state.retiring = true
		removeStateRoutesLocked(handle, state)
		delete(handle.relayKeys, state.relayKey)
		delete(relayedThreads, state.relayKey)
		close(state.done)
		return true
	}
	signalRouteChangeLocked := func(handle *hubRelayHandle) {
		close(handle.routeChanged)
		handle.routeChanged = make(chan struct{})
	}
	registerPendingStateLocked := func(handle *hubRelayHandle, state *relayKeyState) {
		owners := pendingRelays[state.relayKey]
		if owners == nil {
			owners = make(map[*relayKeyState]*hubRelayHandle)
			pendingRelays[state.relayKey] = owners
		}
		owners[state] = handle
		handle.pendingStates[state] = struct{}{}
		handle.pendingKeys[state.relayKey] = state
	}
	unregisterPendingStateLocked := func(handle *hubRelayHandle, state *relayKeyState) bool {
		owners := pendingRelays[state.relayKey]
		if owners == nil || owners[state] != handle {
			return false
		}
		delete(owners, state)
		if len(owners) == 0 {
			delete(pendingRelays, state.relayKey)
		}
		delete(handle.pendingStates, state)
		if handle.pendingKeys[state.relayKey] == state {
			delete(handle.pendingKeys, state.relayKey)
		}
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
	removeRelayHandleLocked := func(handle *hubRelayHandle) bool {
		if handle == nil || handle.removed {
			return false
		}
		for _, state := range handle.relayKeys {
			if state.publications != 0 {
				return false
			}
		}
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
			close(state.done)
		}
		for state := range handle.pendingStates {
			unregisterPendingStateLocked(handle, state)
			close(state.done)
		}
		for routingKey := range handle.routes {
			delete(handle.routes, routingKey)
		}
		handle.removed = true
		if handle.done != nil {
			close(handle.done)
		}
		return true
	}
	maybeFinishHandleLocked := func(handle *hubRelayHandle) bool {
		if handle == nil || handle.removed || handle.initializing || len(handle.relayKeys) != 0 || len(handle.pendingStates) != 0 || handle.commandOwners != 0 {
			return false
		}
		if !removeRelayHandleLocked(handle) {
			return false
		}
		finishHandleLocked(handle, context.Canceled)
		return true
	}
	removeStoppedStateLocked := func(handle *hubRelayHandle, state *relayKeyState) bool {
		if state == nil || state.commands != 0 || state.publications != 0 {
			return false
		}
		if pendingRelays[state.relayKey][state] == handle {
			unregisterPendingStateLocked(handle, state)
			close(state.done)
			return true
		}
		return removeRelayKeyStateLocked(handle, state)
	}
	retireRelayHandle := func(handle *hubRelayHandle) {
		var closeHandle bool
		relayMu.Lock()
		handle.stopping = true
		for _, state := range handle.relayKeys {
			state.stopRequested = true
			state.retiring = true
			state.retireOwner = nil
			removeStoppedStateLocked(handle, state)
		}
		for state := range handle.pendingStates {
			state.stopRequested = true
			removeStoppedStateLocked(handle, state)
		}
		closeHandle = maybeFinishHandleLocked(handle)
		relayMu.Unlock()
		if closeHandle {
			closeRelayHandle(handle)
		}
	}
	startAcknowledgedFanout := func(
		handle *hubRelayHandle,
		deliveries <-chan appsource.RelayDelivery,
	) {
		go func() {
			ticker := time.NewTicker(relayIdleInterval)
			defer ticker.Stop()
			defer retireRelayHandle(handle)
			type relayTargetState struct {
				handle       *hubRelayHandle
				state        *relayKeyState
				routingKey   string
				relayKey     string
				threadID     string
				ref          string
				thread       appwire.Thread
				argsByCallID map[string]string
			}
			type pendingRelayDelivery struct {
				delivery   appsource.RelayDelivery
				routingKey string
				routing    relayNotificationRouting
				// served names the routes a retained daemon-gone delivery has
				// already reached; the replay after a pending route binds
				// serves only the rest.
				served map[string]bool
			}
			pendingDeliveries := make([]pendingRelayDelivery, 0, hubRelayPendingDeliveryLimit)
			// routeChangeWait is the wake-up a parked frame waits on.
			// signalRouteChangeLocked closes handle.routeChanged and installs a
			// fresh one, so this must be taken before the routes are read that
			// decide to park: a capture taken afterwards can be the replacement
			// channel, and the frame then sleeps through the very publication it
			// is waiting for until unrelated traffic on the same key wakes it.
			// A capture that is already closed by the time the loop selects on
			// it just costs one extra resolution pass.
			var routeChangeWait <-chan struct{}
			captureRouteChange := func() {
				relayMu.Lock()
				routeChangeWait = handle.routeChanged
				relayMu.Unlock()
			}
			acknowledge := func(delivery appsource.RelayDelivery) {
				if delivery.Acknowledge != nil {
					delivery.Acknowledge()
				}
			}
			defer func() {
				for _, pending := range pendingDeliveries {
					acknowledge(pending.delivery)
				}
			}()
			targetState := func(routingKey string, state *relayKeyState) relayTargetState {
				parsedRef, _ := appwire.ParseRef(state.relayKey)
				target := relayTargetState{
					handle:       handle,
					state:        state,
					routingKey:   routingKey,
					relayKey:     state.relayKey,
					threadID:     parsedRef.ThreadID,
					ref:          state.relayKey,
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
			lookupTargets := func(routingKey string, routing relayNotificationRouting) ([]relayTargetState, bool) {
				relayMu.Lock()
				defer relayMu.Unlock()
				var targets []relayTargetState
				if routing == relayNotificationTargeted {
					state := handle.routes[routingKey]
					if state != nil && !state.retiring && handle.relayKeys[state.relayKey] == state && relayedThreads[state.relayKey] == handle {
						targets = append(targets, targetState(routingKey, state))
					}
				} else {
					targets = make([]relayTargetState, 0, len(handle.relayKeys))
					for currentKey, state := range handle.relayKeys {
						if !state.retiring && relayedThreads[currentKey] == handle {
							targets = append(targets, targetState("", state))
						}
					}
				}
				pending := routing == relayNotificationTargeted && len(targets) == 0 && handle.pendingRoutes != 0 && !handle.stopping
				return targets, pending
			}
			publishTarget := func(delivery appsource.RelayDelivery, target relayTargetState) {
				notification := delivery.Notification
				// The relay's own daemon-gone resync is addressed to every
				// route; the copy each route gets names that route. Keyed on
				// the delivery's mark, never on an absent target, which a
				// malformed daemon frame could share.
				if delivery.DaemonGone {
					notification = stampResyncTarget(notification, target.threadID, target.ref)
				}
				// The edits only this hub can make to a local daemon's
				// notification on its way to a browser: the images it can
				// resolve off disk, and the answer to what a thread can still
				// be asked to do once the daemon announcing its own close is
				// gone.
				if strings.HasPrefix(target.relayKey, "local:") {
					notification = enrichOutputImageNotification(target.thread.SessionID, target.thread.CWD, target.argsByCallID, notification)
					// Both stampers return the frame untouched for any other
					// method, so the method is checked before the answer is
					// computed rather than after. This is the hottest path the
					// projection sits on — every frame of every subscribed
					// session — and the projection is not free: it scans the
					// roster, resolves the target session and reads the
					// deletion store. enrichOutputImageNotification above
					// guards itself the same way.
					if notification.Method == appwire.NotifyThreadStatusChanged {
						ownsFork := applyHubForkCapability(cfg, target.thread).Evener.Capabilities.ForkFromTurn
						notification = stampClosedThreadCapabilities(notification, ownsFork)
						notification = stampForkCapability(notification, ownsFork)
					}
				}
				if cfg.RelayHooks.BeforeCanonicalPublish != nil {
					cfg.RelayHooks.BeforeCanonicalPublish(target.relayKey, notification)
				}
				relayMu.Lock()
				current := !handle.stopping && !target.state.retiring &&
					handle.relayKeys[target.relayKey] == target.state &&
					relayedThreads[target.relayKey] == handle
				if current && target.routingKey != "" {
					current = handle.routes[target.routingKey] == target.state
				}
				if current {
					if target.state.publications == 0 {
						target.state.publicationDone = make(chan struct{})
					}
					target.state.publications++
				}
				relayMu.Unlock()
				if !current {
					return
				}
				if cfg.RelayHooks.AfterCanonicalPublishEntry != nil {
					cfg.RelayHooks.AfterCanonicalPublishEntry(target.relayKey, notification)
				}
				// The guard is best-effort: it only keeps a frame from being
				// published while the target is being deleted, and its error is
				// already discarded. Wait for the alias with a bounded timeout so
				// transient deletion or Resume ownership does not drop an
				// acknowledged frame, while the fan-out and its publicationDone
				// drain still cannot be parked indefinitely. On expiry the frame
				// is skipped as before — an explicit Resume can hold the alias
				// longer than the guard — but the delivery is acknowledged
				// afterwards, so the skipped frame would be a silent gap in the
				// subscriber's projection: broadcast a resync naming the target
				// first, and the subscriber re-reads instead of missing an
				// acknowledged notification.
				// Fast path: an immediately free alias needs no guard context or
				// timer. A contended or unresolved target falls through to the
				// bounded wait unchanged.
				var release func()
				var lockErr error
				if fastRelease, ok := tryLockDeletionTarget(cfg, target.ref, target.threadID); ok {
					release = fastRelease
				} else {
					guardCtx, cancelGuard := context.WithTimeout(context.Background(), relayPublicationGuardTimeout)
					release, lockErr = lockDeletionTarget(guardCtx, cfg, target.ref, target.threadID)
					cancelGuard()
				}
				if lockErr == nil {
					if backgroundFenceError(target.ref, target.threadID) == nil {
						server.Broadcast(target.relayKey, notification.Method, notification.Params)
					}
					release()
				} else {
					server.Broadcast(target.relayKey, appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
						ThreadID: target.threadID,
						Ref:      target.ref,
					})
				}
				var closeHandle bool
				relayMu.Lock()
				target.state.publications--
				if target.state.publications == 0 {
					close(target.state.publicationDone)
					target.state.publicationDone = nil
					if (target.state.removeOnDrain && target.state.retireOwner == nil) ||
						(target.state.stopRequested && target.state.commands == 0) {
						removeRelayKeyStateLocked(target.handle, target.state)
					}
				}
				closeHandle = maybeFinishHandleLocked(target.handle)
				relayMu.Unlock()
				if closeHandle {
					closeRelayHandle(target.handle)
				}
			}
			publishDelivery := func(delivery appsource.RelayDelivery, targets []relayTargetState) {
				for _, target := range targets {
					publishTarget(delivery, target)
				}
				acknowledge(delivery)
			}
			routesPending := func() bool {
				relayMu.Lock()
				defer relayMu.Unlock()
				return handle.pendingRoutes != 0 && !handle.stopping
			}
			// serveDaemonGone publishes a daemon-gone delivery to every current
			// route it has not reached yet. It reports whether a route is
			// still being bound, in which case the delivery stays retained so
			// that route hears it too once bound: the announcement is
			// addressed to every subscriber of the daemon, including one whose
			// subscription is mid-flight.
			serveDaemonGone := func(pending *pendingRelayDelivery) bool {
				targets, _ := lookupTargets("", relayNotificationUntargeted)
				for _, target := range targets {
					if pending.served[target.relayKey] {
						continue
					}
					// A route published but not yet subscribed to has nobody
					// to hear this yet; it is served once its read releases.
					if server.SubscriberCount(target.relayKey) == 0 {
						continue
					}
					pending.served[target.relayKey] = true
					publishTarget(pending.delivery, target)
				}
				return routesPending()
			}
			processPending := func() {
				captureRouteChange()
				kept := pendingDeliveries[:0]
				for _, pending := range pendingDeliveries {
					if pending.delivery.DaemonGone {
						if serveDaemonGone(&pending) {
							kept = append(kept, pending)
						}
						continue
					}
					targets, wait := lookupTargets(pending.routingKey, pending.routing)
					if wait {
						kept = append(kept, pending)
						continue
					}
					publishDelivery(pending.delivery, targets)
				}
				pendingDeliveries = kept
			}
			hasPendingTarget := func(routingKey string) bool {
				for _, pending := range pendingDeliveries {
					if pending.routingKey == routingKey {
						return true
					}
				}
				return false
			}
			acceptDelivery := func(delivery appsource.RelayDelivery) {
				captureRouteChange()
				if delivery.DaemonGone {
					// Acknowledged as soon as every subscribed route has it: the
					// relay session needs the acknowledgement only to release the
					// capture barrier of a read in flight - and a route still
					// being bound IS such a read, so holding the acknowledgement
					// for it would wait on itself. The copy kept for that route
					// is the hub's own.
					pending := pendingRelayDelivery{delivery: delivery, routing: relayNotificationUntargeted, served: map[string]bool{}}
					stillPending := serveDaemonGone(&pending)
					acknowledge(delivery)
					if stillPending {
						pendingDeliveries = append(pendingDeliveries, pending)
					}
					return
				}
				routingKey, routing := relayNotificationRoutingKey(delivery.Notification, handle.canonical.SourceID)
				if routing == relayNotificationTargeted && hasPendingTarget(routingKey) {
					pendingDeliveries = append(pendingDeliveries, pendingRelayDelivery{delivery: delivery, routingKey: routingKey, routing: routing})
					if delivery.Proceed != nil {
						delivery.Proceed()
					}
					processPending()
					return
				}
				targets, pending := lookupTargets(routingKey, routing)
				if pending {
					pendingDeliveries = append(pendingDeliveries, pendingRelayDelivery{delivery: delivery, routingKey: routingKey, routing: routing})
					if observeHubRelayWait != nil {
						observeHubRelayWait()
					}
					if delivery.Proceed != nil {
						delivery.Proceed()
					}
					return
				}
				publishDelivery(delivery, targets)
			}
			retireIdle := func() bool {
				type idleCandidate struct {
					relayKey string
					state    *relayKeyState
					commands int
				}
				relayMu.Lock()
				active := canonicalRelays[handle.canonical] == handle && !handle.stopping
				candidates := make([]idleCandidate, 0, len(handle.relayKeys))
				if active {
					for relayKey, state := range handle.relayKeys {
						if relayedThreads[relayKey] == handle && state.commands == 0 && !state.retiring {
							candidates = append(candidates, idleCandidate{relayKey: relayKey, state: state, commands: state.commands})
						}
					}
					if len(handle.relayKeys) == 0 && handle.commandOwners == 0 {
						handle.stopping = true
						active = false
					}
				}
				relayMu.Unlock()
				if !active {
					return true
				}
				idleCandidates := candidates[:0]
				for _, candidate := range candidates {
					if server.SubscriberCount(candidate.relayKey) == 0 {
						idleCandidates = append(idleCandidates, candidate)
					}
				}
				if len(idleCandidates) == 0 {
					return false
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
					return false
				}
				relayMu.Lock()
				removed := 0
				if canonicalRelays[handle.canonical] == handle && !handle.stopping {
					for _, candidate := range revalidated {
						if relayedThreads[candidate.relayKey] == handle &&
							handle.relayKeys[candidate.relayKey] == candidate.state &&
							candidate.state.commands == candidate.commands {
							candidate.state.removeOnDrain = true
							if removeRelayKeyStateLocked(handle, candidate.state) {
								removed++
							}
						}
					}
				}
				retired := canonicalRelays[handle.canonical] == handle && len(handle.relayKeys) == 0 && handle.commandOwners == 0
				if retired {
					handle.stopping = true
				}
				relayMu.Unlock()
				for range removed {
					if cfg.RelayHooks.AfterIdleDelete != nil {
						cfg.RelayHooks.AfterIdleDelete(handle.canonical.ThreadID)
					}
				}
				return retired
			}
			for {
				var deliveryInput <-chan appsource.RelayDelivery
				if len(pendingDeliveries) < hubRelayPendingDeliveryLimit {
					deliveryInput = deliveries
				}
				var routeChanged <-chan struct{}
				if len(pendingDeliveries) != 0 {
					routeChanged = routeChangeWait
				}
				select {
				case <-handle.ctx.Done():
					return
				case <-routeChanged:
					processPending()
				case <-ticker.C:
					if retireIdle() {
						return
					}
				case delivery, ok := <-deliveryInput:
					if !ok {
						return
					}
					acceptDelivery(delivery)
				}
			}
		}()
	}
	acquireRelaySession := func(
		ctx context.Context,
		source appsource.RelaySessionSource,
		base appsource.Source,
		params appwire.ThreadReadParams,
		canonicalRef appwire.Ref,
	) (*hubRelayHandle, *relayKeyState, func(context.Context, appwire.Thread) error, func(), error) {
		relayKey, _, err := relayTarget(base, params)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		stoppedRelayKeyLocked := func(preferred *hubRelayHandle) <-chan struct{} {
			owners := pendingRelays[relayKey]
			if preferred != nil {
				if preferred.stopping {
					return preferred.done
				}
				if current := preferred.relayKeys[relayKey]; current != nil && (current.stopRequested || current.retiring) {
					return current.done
				}
				if pending := preferred.pendingKeys[relayKey]; pending != nil && owners[pending] == preferred && pending.stopRequested {
					return pending.done
				}
			}
			if currentHandle := relayedThreads[relayKey]; currentHandle != nil {
				if current := currentHandle.relayKeys[relayKey]; current != nil && current.stopRequested {
					return current.done
				}
			}
			for pending := range owners {
				if pending.stopRequested {
					return pending.done
				}
			}
			return nil
		}
		beginRelayKeyLocked := func(handle *hubRelayHandle) (*relayKeyState, <-chan struct{}) {
			if handle.routes == nil {
				handle.routes = make(map[string]*relayKeyState)
			}
			if handle.stopping {
				return nil, handle.done
			}
			if stopped := stoppedRelayKeyLocked(handle); stopped != nil {
				return nil, stopped
			}
			owners := pendingRelays[relayKey]
			state := handle.relayKeys[relayKey]
			if relayedThreads[relayKey] != handle || state == nil {
				state = handle.pendingKeys[relayKey]
				if state != nil && (owners[state] != handle || state.generation != relayGenerations[relayKey]) {
					state = nil
				}
			}
			if state == nil {
				relayGenerations[relayKey]++
				state = &relayKeyState{
					relayKey:     relayKey,
					generation:   relayGenerations[relayKey],
					argsByCallID: make(map[string]string),
					routingKeys:  make(map[string]struct{}),
					done:         make(chan struct{}),
				}
				registerPendingStateLocked(handle, state)
			}
			state.commands++
			handle.commandOwners++
			handle.pendingRoutes++
			return state, nil
		}
		waitForStoppedGeneration := func(done <-chan struct{}) error {
			if observeHubRelayWait != nil {
				observeHubRelayWait()
			}
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		commandFunctions := func(handle *hubRelayHandle, state *relayKeyState) (func(context.Context, appwire.Thread) error, func()) {
			routesPending := true
			resolveRoutesLocked := func() {
				if !routesPending {
					return
				}
				routesPending = false
				handle.pendingRoutes--
				signalRouteChangeLocked(handle)
			}
			publish := func(publicationCtx context.Context, thread appwire.Thread) error {
				var retirementHandle *hubRelayHandle
				var retirementState *relayKeyState
				restoreRetirementLocked := func() {
					if retirementState != nil && retirementState.retireOwner == state &&
						retirementHandle != nil && relayedThreads[relayKey] == retirementHandle &&
						retirementHandle.relayKeys[relayKey] == retirementState && !retirementState.stopRequested {
						retirementState.retiring = false
						retirementState.removeOnDrain = false
						retirementState.retireOwner = nil
					}
					retirementHandle = nil
					retirementState = nil
				}
				defer func() {
					if recovered := recover(); recovered != nil {
						relayMu.Lock()
						if routesPending {
							if pendingRelays[relayKey][state] == handle {
								unregisterPendingStateLocked(handle, state)
								close(state.done)
							}
							restoreRetirementLocked()
							resolveRoutesLocked()
						}
						relayMu.Unlock()
						panic(recovered)
					}
				}()
				for {
					if err := publicationCtx.Err(); err != nil {
						relayMu.Lock()
						if routesPending {
							if pendingRelays[relayKey][state] == handle {
								unregisterPendingStateLocked(handle, state)
								close(state.done)
							}
							restoreRetirementLocked()
							resolveRoutesLocked()
						}
						relayMu.Unlock()
						return err
					}
					var drain <-chan struct{}
					var closeHandles []*hubRelayHandle
					relayMu.Lock()
					if !routesPending {
						relayMu.Unlock()
						return nil
					}
					current := relayedThreads[relayKey] == handle && handle.relayKeys[relayKey] == state
					pending := pendingRelays[relayKey][state] == handle
					eligible := !handle.stopping && !state.stopRequested && !state.retiring &&
						(current || (pending && state.generation == relayGenerations[relayKey]))
					if !eligible {
						restoreRetirementLocked()
					}
					if eligible && pending {
						if previous := relayedThreads[relayKey]; previous != nil && previous != handle {
							retirementHandle = previous
							retirementState = previous.relayKeys[relayKey]
							if retirementState != nil {
								retirementState.retiring = true
								retirementState.removeOnDrain = true
								retirementState.retireOwner = state
								if retirementState.publications != 0 {
									drain = retirementState.publicationDone
								} else {
									removeRelayKeyStateLocked(previous, retirementState)
									if maybeFinishHandleLocked(previous) {
										closeHandles = append(closeHandles, previous)
									}
								}
							}
						}
						if drain == nil {
							unregisterPendingStateLocked(handle, state)
						}
					}
					if drain != nil {
						relayMu.Unlock()
						for _, closeHandle := range closeHandles {
							closeRelayHandle(closeHandle)
						}
						if observeHubRelayWait != nil {
							observeHubRelayWait()
						}
						select {
						case <-drain:
							continue
						case <-publicationCtx.Done():
							relayMu.Lock()
							if routesPending {
								if pendingRelays[relayKey][state] == handle {
									unregisterPendingStateLocked(handle, state)
									close(state.done)
								}
								restoreRetirementLocked()
								resolveRoutesLocked()
							}
							relayMu.Unlock()
							return publicationCtx.Err()
						}
					}
					if eligible {
						removeStateRoutesLocked(handle, state)
						state.thread = thread
						bindStateRouteLocked(handle, relayKey, state)
						sourceID := thread.Source
						if sourceID == "" {
							sourceID = handle.canonical.SourceID
						}
						if thread.ID != "" {
							bindStateRouteLocked(handle, sourceID+":"+thread.ID, state)
						}
						bindStateRouteLocked(handle, thread.Evener.Ref, state)
						handle.relayKeys[relayKey] = state
						// Route identity and downstream ownership publish in one
						// relayMu critical section, after the displaced generation's
						// already-entered publications have drained.
						relayedThreads[relayKey] = handle
					} else if pending && !state.stopRequested && !handle.stopping {
						unregisterPendingStateLocked(handle, state)
						close(state.done)
					}
					// The route stays pending past this publication, until the
					// read releases it (release, below): the appserver installs
					// the client's subscription only after the read returns, and
					// a departure announced in between must still reach it.
					relayMu.Unlock()
					for _, closeHandle := range closeHandles {
						closeRelayHandle(closeHandle)
					}
					return nil
				}
			}
			release := func() {
				var closeHandle bool
				relayMu.Lock()
				resolveRoutesLocked()
				if state.commands > 0 && handle.commandOwners > 0 {
					state.commands--
					handle.commandOwners--
				}
				if state.commands == 0 && pendingRelays[relayKey][state] == handle {
					unregisterPendingStateLocked(handle, state)
					close(state.done)
				}
				if state.stopRequested && state.commands == 0 && state.publications == 0 {
					removeStoppedStateLocked(handle, state)
				}
				closeHandle = maybeFinishHandleLocked(handle)
				relayMu.Unlock()
				if closeHandle {
					closeRelayHandle(handle)
				}
			}
			return publish, release
		}
		for {
			relayMu.Lock()
			existing := canonicalRelays[canonicalRef]
			if stopped := stoppedRelayKeyLocked(existing); stopped != nil {
				relayMu.Unlock()
				if err := waitForStoppedGeneration(stopped); err != nil {
					return nil, nil, nil, nil, err
				}
				continue
			}
			if existing != nil {
				ready := existing.ready
				relayMu.Unlock()
				select {
				case <-ready:
				case <-ctx.Done():
					return nil, nil, nil, nil, ctx.Err()
				}
				relayMu.Lock()
				if canonicalRelays[canonicalRef] != existing || existing.err != nil {
					err := existing.err
					relayMu.Unlock()
					if err != nil {
						return nil, nil, nil, nil, err
					}
					continue
				}
				state, stopped := beginRelayKeyLocked(existing)
				relayMu.Unlock()
				if stopped != nil {
					if err := waitForStoppedGeneration(stopped); err != nil {
						return nil, nil, nil, nil, err
					}
					continue
				}
				publish, release := commandFunctions(existing, state)
				return existing, state, publish, release, nil
			}
			// The server owns the canonical relay: its Shutdown ends the
			// fan-out rather than leaving it to an idle tick.
			relayCtx, cancelRelay := context.WithCancel(server.Lifetime())
			handle := &hubRelayHandle{
				ready:         make(chan struct{}),
				ctx:           relayCtx,
				cancel:        cancelRelay,
				canonical:     canonicalRef,
				done:          make(chan struct{}),
				initializing:  true,
				relayKeys:     make(map[string]*relayKeyState),
				pendingKeys:   make(map[string]*relayKeyState),
				pendingStates: make(map[*relayKeyState]struct{}),
				routes:        make(map[string]*relayKeyState),
				routeChanged:  make(chan struct{}),
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
				active := canonicalRelays[canonicalRef] == handle && !handle.stopping
				if active {
					handle.lease = lease
				}
				relayMu.Unlock()
				if !active {
					lease.Close()
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
			handle.initializing = false
			if acquireErr != nil || canonicalRelays[canonicalRef] != handle || handle.stopping {
				if acquireErr == nil {
					acquireErr = context.Canceled
				}
				removeRelayHandleLocked(handle)
				finishHandleLocked(handle, acquireErr)
				relayMu.Unlock()
				closeRelayHandle(handle)
				return nil, nil, nil, nil, acquireErr
			}
			state, stopped := beginRelayKeyLocked(handle)
			if stopped != nil {
				delete(canonicalRelays, canonicalRef)
				finishHandleLocked(handle, context.Canceled)
				relayMu.Unlock()
				closeRelayHandle(handle)
				if err := waitForStoppedGeneration(stopped); err != nil {
					return nil, nil, nil, nil, err
				}
				continue
			}
			handle.established = true
			finishHandleLocked(handle, nil)
			relayMu.Unlock()
			startAcknowledgedFanout(handle, deliveries)
			publish, release := commandFunctions(handle, state)
			return handle, state, publish, release, nil
		}
	}
	readThread := func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams) (*hubThreadReadResult, error) {
		needsRelay := params.Subscribe || relayOnThreadRead(source)
		relaySource, atomic := source.(appsource.RelaySessionSource)
		if !atomic || !needsRelay {
			if combined, ok := source.(appsource.CombinedItemReadSource); ok &&
				params.IncludeTurns {
				response, candidates, err := combined.ReadThreadWithItemCandidates(ctx, params)
				return &hubThreadReadResult{
					response: response, itemCandidates: candidates, hasItemCandidates: err == nil,
				}, err
			}
			response, err := source.ReadThread(ctx, params)
			return &hubThreadReadResult{response: response}, err
		}
		if err := deletionFenceError(cfg, params.Ref, params.ThreadID, ""); err != nil {
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
		handle, state, publish, release, err := acquireRelaySession(ctx, relaySource, source, params, canonicalRef)
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
		if err := publish(ctx, result.Response.Thread); err != nil {
			read.finish(false)
			return nil, err
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
		relayKey := read.relayKey
		captured, err := withDeletionTargetOwnership(
			ctx, cfg,
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
		relayKey := read.relayKey
		registered, err := withDeletionTargetOwnership(
			ctx, cfg,
			params.Ref,
			threadID,
			"",
			func() (bool, error) {
				if !read.handoff.Prepare() {
					return false, appwire.SessionUnavailable("relay handoff could not be prepared")
				}
				if err := registerSubscription(ctx, relayKey, params.ReplaceSubscription, read.lifecycleKey); err != nil {
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
	startRelay = func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams, thread appwire.Thread) error {
		threadID := thread.ID
		if threadID == "" {
			return nil
		}

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
			return keyErr
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

		var relayCtx context.Context
		var relayHandle *hubRelayHandle
		var cancelRelay context.CancelFunc
		var stopInitialCancellation func() bool
		for {
			relayMu.Lock()
			existing := relayedThreads[relayKey]
			if existing == nil {
				relayCtx, cancelRelay = context.WithCancel(context.WithoutCancel(ctx))
				// Label every attach this relay issues with its own key. A source
				// that keys its subscriptions by remote thread identity uses it to
				// tell this relay re-attaching from a second relay that reached the
				// same thread by another address (appsource.WithRelayIdentity).
				relayCtx = appsource.WithRelayIdentity(relayCtx, relayKey)
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
				if err := registerSubscription(ctx, relayKey, subscribeParams.ReplaceSubscription); err != nil {
					return false, err
				}
				return true, nil
			}
			registered, err := withDeletionTargetOwnership(ctx, cfg, subscribeParams.Ref, threadID, "", registerExisting)
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
		if err = registerSubscription(ctx, relayKey, subscribeParams.ReplaceSubscription); err != nil {
			delete(relayedThreads, relayKey)
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
			// relayCtx keeps the starting request's values but not its
			// cancellation, since the relay outlives that request. The server
			// that serves the relay owns it instead: its Shutdown ends the
			// relay rather than leaving it to an idle tick, which never comes
			// while a subscriber stays registered.
			stopOnServerShutdown := context.AfterFunc(server.Lifetime(), cancelRelay)
			defer stopOnServerShutdown()
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
			//
			// Both frames name the relay's target. The synthesized failure is the
			// turn's; the session STATUS is thread/status/changed's, never
			// turn/completed's — the daemon's real failure exit emits the same
			// pair (agent/session_lifecycle.go's endInputAtTurnFailure announces
			// EventSessionEnd{Reason:"turn_failed"} as
			// thread/status/changed(idle)). Without the status frame a client
			// that leaves the status to the status frame, as the shared web and
			// mobile reducer now does, would keep the session active with Stop
			// and Steer still showing and Send withheld — the exact stall this
			// synthesis exists to end. The capabilities are the hub's own answer
			// for a session whose daemon is gone (relayGaveUpCapabilities, the set
			// a past read returns), because the departing daemon's set describes
			// the turn that is over.
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
				server.Broadcast(relayKey, appwire.NotifyTurnCompleted, appwire.TurnCompletedParams{
					ThreadID: threadID,
					Ref:      subscribeParams.Ref,
					Turn: appwire.Turn{
						ID:     turnID,
						Status: appwire.TurnStatusFailed,
						Error: &appwire.TurnError{
							Message: message,
							Source:  "hub",
						},
					},
				})
				server.Broadcast(relayKey, appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
					ThreadID:     threadID,
					Ref:          subscribeParams.Ref,
					Status:       appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
					Capabilities: relayGaveUpCapabilities(cfg, relayKey, thread),
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
					if err := backgroundFenceError(subscribeParams.Ref, threadID); err != nil {
						result <- hubRelaySubscriptionResult{err: err}
						return
					}
					notifications, err := subscribeRelayRecovery(relayCtx, source, recoveryParams)
					if fenceErr := backgroundFenceError(subscribeParams.Ref, threadID); fenceErr != nil {
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
						server.Broadcast(relayKey, appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
							ThreadID: threadID,
							Ref:      subscribeParams.Ref,
						})
					}
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
		prepared, err := prepareRelay(ctx, source, readParams)
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
		var closeHandle bool
		var cancelInitializing context.CancelFunc
		relayMu.Lock()
		handle := canonicalRelays[ref]
		if handle != nil && !handle.removed {
			handle.stopping = true
			if handle.initializing {
				cancelInitializing = handle.cancel
			}
			for _, state := range handle.relayKeys {
				state.stopRequested = true
				state.retiring = true
				state.retireOwner = nil
				removeStoppedStateLocked(handle, state)
			}
			for state := range handle.pendingStates {
				state.stopRequested = true
				removeStoppedStateLocked(handle, state)
			}
			signalRouteChangeLocked(handle)
			closeHandle = maybeFinishHandleLocked(handle)
		}
		relayMu.Unlock()
		if cancelInitializing != nil {
			cancelInitializing()
		}
		if closeHandle {
			closeRelayHandle(handle)
		}
	}
	stopRelay := func(relayKey string) {
		var closeHandles []*hubRelayHandle
		relayMu.Lock()
		handle := relayedThreads[relayKey]
		if handle != nil && handle.canonical == (appwire.Ref{}) {
			if removeRelayHandleLocked(handle) {
				finishHandleLocked(handle, context.Canceled)
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
					removeStoppedStateLocked(handle, state)
				}
			}
			for state, pendingHandle := range pendingRelays[relayKey] {
				handles[pendingHandle] = struct{}{}
				if state != nil {
					state.stopRequested = true
					removeStoppedStateLocked(pendingHandle, state)
				}
			}
			for currentHandle := range handles {
				signalRouteChangeLocked(currentHandle)
				if maybeFinishHandleLocked(currentHandle) {
					closeHandles = append(closeHandles, currentHandle)
				}
			}
		}
		relayMu.Unlock()
		for _, closeHandle := range closeHandles {
			closeRelayHandle(closeHandle)
		}
	}
	relayCommandCount := func(key string) int {
		relayMu.Lock()
		defer relayMu.Unlock()
		count := 0
		if handle := relayedThreads[key]; handle != nil {
			if state := handle.relayKeys[key]; state != nil {
				count += state.commands
			}
		}
		for state := range pendingRelays[key] {
			count += state.commands
		}
		return count
	}
	relayPublished := func(key string) bool {
		relayMu.Lock()
		defer relayMu.Unlock()
		handle := relayedThreads[key]
		return handle != nil && handle.relayKeys[key] != nil
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
		relayPublished:      relayPublished,
	}
}
