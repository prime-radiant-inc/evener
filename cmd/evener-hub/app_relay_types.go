package hub

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/apptranscript"
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
	// pendingRoutes counts executing reads until their authoritative routing
	// is published. Queued commands and downstream handoffs are not discovery.
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
	relayKey     string
	lifecycleKey string
	response     appwire.ThreadReadResponse
	handoff      appsource.RelayHandoff
	release      func()
	once         sync.Once
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
// resolve, bounded so the caller cannot be parked indefinitely.
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
// egress (server/appwire_status_stamps.go's stampCapabilitiesOnStatusChange). The
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
// relayGaveUpNotice is the error notice a relay publishes when it gives up
// on turnID's source: the cause of the last failed re-dial, anchored after
// entry (an entry position, ordinal + 1). The key is the hub's own, apart from
// the daemon's numbered notice keys.
func relayGaveUpNotice(turnID string, entry uint64, cause error) appwire.OverlayItem {
	const key = "notice:hub:relay-gave-up"
	// The announcement always has text, so SystemMessage always builds it.
	item, _ := apptranscript.SystemMessage(apptranscript.NoticeAnnouncement{
		EventKind:   appwire.ThreadItemEventKindError,
		Description: "Hub lost the connection to the session",
		Text:        "Hub lost the connection to the session: " + cause.Error(),
	}, key, turnID)
	return appwire.OverlayItem{
		Key:    key,
		Kind:   appwire.OverlayNotice,
		TurnID: turnID,
		Anchor: &appwire.ThreadItemPosition{Entry: entry, Item: appwire.NoticeAnchorItem},
		Item:   item,
	}
}

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
	return patchStatusCapabilities(notification, func(status appwire.ThreadStatus, capabilities map[string]json.RawMessage) {
		if allowFork && !hubForkRecoveryFenced(appwire.Thread{Status: status}) {
			capabilities["forkFromTurn"] = json.RawMessage("true")
		} else {
			// Clear a stale daemon value as well as refusing to add the action. The
			// notification is the client's authoritative status transition.
			capabilities["forkFromTurn"] = json.RawMessage("false")
		}
	})
}

// stampPageBeforeCapability sets the hub's pageBefore answer (hubPagesBefore)
// on a capability update, which carries the daemon's set, and the daemon never
// names it. Without it a client reading the frame's set would lose the answer
// at the thread's first status change.
func stampPageBeforeCapability(notification appwire.Notification, pageBefore bool) appwire.Notification {
	return patchStatusCapabilities(notification, func(_ appwire.ThreadStatus, capabilities map[string]json.RawMessage) {
		capabilities["pageBefore"] = json.RawMessage(strconv.FormatBool(pageBefore))
	})
}

// patchStatusCapabilities edits a thread/status/changed frame's capability
// update in place, keeping every other field as the daemon sent it. Any other
// frame, or one with no capability update, passes through unchanged.
func patchStatusCapabilities(notification appwire.Notification, patch func(appwire.ThreadStatus, map[string]json.RawMessage)) appwire.Notification {
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
	patch(params.Status, params.Capabilities)
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

var observeHubRelay func(*hubRelay)
var observeHubRelayWait func()

// Test-only observations for the bounded pending-delivery queue, its delivery
// input, and authoritative route publication. Production never stores them;
// each call site is one load and nil check.
var observeHubRelayPending atomic.Pointer[func(int)]
var observeHubRelayPendingBackpressure atomic.Pointer[func()]
var observeHubRelayRoutePublication atomic.Pointer[func()]
var observeHubRelayDeliveryRead atomic.Pointer[func(string)]

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
