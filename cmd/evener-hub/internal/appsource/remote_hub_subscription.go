package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"primeradiant.com/evener/appwire"
)

// remoteHubSubBuffer sizes each subscription's two channels. It matches the
// relay-side buffer LocalDaemonSource uses, so a remote subscription tolerates
// the same scheduling jitter before the relay's consumer has to be reading.
const remoteHubSubBuffer = 128

// remoteHubUnsubscribeTimeout bounds the thread/unsubscribe a retiring
// subscription sends. A live remote hub answers promptly; a wedged one must not
// pin teardown (and, because the request is serialized with installs, the
// subscription path) open indefinitely.
const remoteHubUnsubscribeTimeout = 2 * time.Second

// remoteHubRouteStallTimeout bounds how long the shared drain waits to hand a
// notification to one subscription before treating that subscription's
// consumer as stalled and retiring it. It is a var rather than a const only so
// tests can shorten it (the same seam hubRelayIdleInterval provides); the
// value is deliberately far longer than any scheduling jitter the 128-slot
// per-subscription buffer is sized to absorb, so a merely slow consumer is
// still served from the buffer instead of being resynced.
var remoteHubRouteStallTimeout = 5 * time.Second

// remoteHubSubscription is one controller relay's live view of one remote
// thread.
//
// Two channels, each with a single writer, are the whole point. The drain
// goroutine writes to in and never closes it, so a fan-out send can never
// panic on a closed channel. out is written and closed only by its own pump
// goroutine, so the pump is the sole owner of out's lifetime. When out closes
// the controller relay treats that as subscription end and re-subscribes via
// its recovery path (app_relay.go), so the pump MUST close out on context
// cancellation.
type remoteHubSubscription struct {
	threadID string
	// remoteRef is the ref this subscription issued to the remote hub
	// ("local:<thread>"). thread/unsubscribe names it to drop the remote side;
	// without that the long-lived shared client would keep forwarding this
	// thread's notifications for the rest of the connection's life.
	remoteRef string
	// client is the shared per-host client this subscription attached to. When
	// that client's notification stream closes, every subscription bound to it
	// is a dead end and must be retired (see drainLoop): its pump would
	// otherwise block forever and its out would never close, so the relay's
	// recovery path would never re-attach to the reconnected client.
	client *appwire.Client
	// in is the drain's delivery slot. It is NEVER closed.
	in chan appwire.Notification
	// out is returned to the relay and closed ONLY by the pump.
	out chan appwire.Notification
	// pumpDone is closed by the pump on exit so a fan-out sender racing the
	// pump's own teardown can give up without blocking. A failed install closes
	// it directly, because no pump was started to do so.
	pumpDone chan struct{}
	// cancel stops the subscription's pump. It is called when the relay context
	// is cancelled, when a newer subscription replaces this one, or when the
	// owning client's notification stream ends.
	cancel context.CancelFunc
}

// SubscribeThread attaches the controller relay to a remote thread.
//
// It issues thread/read{Ref:"local:<thread>", Subscribe:true} on the shared
// per-host client, deliberately discarding the returned snapshot: the controller
// relay always read the thread first (the non-atomic prepareRelay branch), so
// re-translating a second snapshot would be redundant. From then on every
// ref-bearing notification the remote pushes is translated and routed to this
// subscription by remote thread identity.
func (s *RemoteHubSource) SubscribeThread(ctx context.Context, params appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return nil, err
	}
	client, err := s.client(ctx, s.id)
	if err != nil {
		return nil, s.mapCallError(err)
	}

	subCtx, cancel := context.WithCancel(ctx)
	sub := &remoteHubSubscription{
		threadID:  ref.ThreadID,
		remoteRef: ref.String(),
		client:    client,
		in:        make(chan appwire.Notification, remoteHubSubBuffer),
		out:       make(chan appwire.Notification, remoteHubSubBuffer),
		pumpDone:  make(chan struct{}),
		cancel:    cancel,
	}

	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	remote.Subscribe = true
	// The remote client is shared by every controller relay for this host, so
	// controller-level replacement semantics must never reach it: a
	// replaceSubscription read would scope the whole remote connection to this
	// one thread and silently drop the other threads' remote subscriptions
	// while their local routing entries stayed live. Remote subscriptions are
	// managed independently here, one per remote thread identity.
	remote.ReplaceSubscription = false

	// Register before the request goes out, so a notification the remote emits
	// immediately after attaching cannot be missed. installSubscriber does not
	// cancel a subscription it displaces: the replacement is only committed once
	// its own subscribe succeeds.
	previous := s.installSubscriber(sub)
	var snapshot appwire.ThreadReadResponse
	if err := client.Request(subCtx, appwire.MethodThreadRead, remote, &snapshot); err != nil {
		s.discardSubscriber(sub, previous)
		callerCanceled := ctx.Err() != nil
		cancel()
		// A cancellation that did not come from the caller is the owning
		// client's notification stream closing mid-request (drainLoop retires
		// subscriptions bound to a dead client). That is a transport loss, and
		// mapping it as one keeps the auto-resume gate working; reporting
		// context.Canceled would read as the caller giving up.
		if !callerCanceled && errors.Is(err, context.Canceled) {
			return nil, appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": subscription ended before it attached")
		}
		return nil, s.mapCallError(err)
	}

	go s.pumpSubscription(subCtx, sub)
	if previous != nil && previous != sub {
		previous.cancel()
	}
	return sub.out, nil
}

// installSubscriber publishes sub as the routing target for its remote thread
// and ensures this client's notification stream is being drained. It holds
// remoteMu so a concurrent retireSubscription's check-then-unsubscribe cannot
// interleave with the install: that ordering is what keeps a replacement's
// remote subscription from being dropped by its predecessor's teardown.
func (s *RemoteHubSource) installSubscriber(sub *remoteHubSubscription) *remoteHubSubscription {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	s.subMu.Lock()
	defer s.subMu.Unlock()
	previous := s.subs[sub.threadID]
	s.subs[sub.threadID] = sub
	s.ensureDrainLocked(sub.client)
	return previous
}

// discardSubscriber undoes a failed install. It restores the subscription it
// displaced, which was never cancelled, so a transient subscribe failure leaves
// the healthy previous subscription serving. It also closes pumpDone because no
// pump was started to close it: a fan-out that already grabbed sub and filled
// its in buffer would otherwise wait on in/pumpDone forever and freeze the
// shared drain for every thread on the host.
func (s *RemoteHubSource) discardSubscriber(sub, previous *remoteHubSubscription) {
	close(sub.pumpDone)
	s.remoteMu.Lock()
	s.subMu.Lock()
	if s.subs[sub.threadID] == sub {
		if previous == nil {
			delete(s.subs, sub.threadID)
		} else {
			s.subs[sub.threadID] = previous
		}
	}
	s.subMu.Unlock()
	s.remoteMu.Unlock()
}

// pumpSubscription owns sub.out (and closes it on exit) and sub.pumpDone. It is
// the only sender on out.
func (s *RemoteHubSource) pumpSubscription(ctx context.Context, sub *remoteHubSubscription) {
	// Declaration order is teardown order reversed: out closes (waking the
	// relay's recovery) and pumpDone closes before the remote-side unsubscribe
	// is issued, so a dead remote never delays the relay re-attaching.
	defer s.retireSubscription(sub)
	defer close(sub.pumpDone)
	defer close(sub.out)
	for {
		select {
		case <-ctx.Done():
			return
		case notification := <-sub.in:
			select {
			case sub.out <- notification:
			case <-ctx.Done():
				return
			}
		}
	}
}

// retireSubscription removes sub from the routing table if it is still the
// installed subscription and, only then, tells the remote hub to drop the
// matching remote-side subscription. The remote client is long-lived, so
// without the unsubscribe the remote hub would keep forwarding the thread's
// notifications for the rest of the connection's life. A subscription that was
// already replaced is skipped: the replacement owns that remote subscription
// now.
//
// remoteMu serializes this check-then-unsubscribe against installSubscriber, so
// a replacement's subscribe request can never land before its predecessor's
// unsubscribe and be dropped by it.
func (s *RemoteHubSource) retireSubscription(sub *remoteHubSubscription) {
	s.remoteMu.Lock()
	s.subMu.Lock()
	latest := s.subs[sub.threadID] == sub
	if latest {
		delete(s.subs, sub.threadID)
	}
	s.subMu.Unlock()
	if latest && sub.remoteRef != "" && sub.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), remoteHubUnsubscribeTimeout)
		_, _ = sub.client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: sub.remoteRef})
		cancel()
	}
	s.remoteMu.Unlock()
}

// ensureDrainLocked starts the single notification-drain goroutine for client
// exactly once. Called with subMu held. The entry is removed when the client's
// notification channel closes; a client whose channel is somehow never closed
// parks one harmless goroutine.
func (s *RemoteHubSource) ensureDrainLocked(client *appwire.Client) {
	if _, ok := s.drains[client]; ok {
		return
	}
	s.drains[client] = struct{}{}
	go s.drainLoop(client)
}

// drainLoop consumes the shared client's single notification stream. It must
// never stop while the client is alive: appwire tears the connection down on
// buffer overflow (ErrNotificationOverflow), so a busy or stalled thread would
// kill every subscription on the host. routeNotification's bounded wait is what
// enforces that — a subscription whose consumer stops reading is retired rather
// than allowed to wedge this goroutine. Unrouted notifications are dropped — but
// they are still read, which is what keeps the client healthy.
//
// When the stream closes the client is dead, and every subscription still bound
// to it is a stall: subCtx derives from the relay's context, not the client, so
// its pump would block forever and its out would never close to trigger the
// relay's re-subscribe against the replacement client. Cancel them here so the
// pumps unwind and the relays re-attach.
func (s *RemoteHubSource) drainLoop(client *appwire.Client) {
	defer func() {
		s.subMu.Lock()
		delete(s.drains, client)
		var stranded []*remoteHubSubscription
		for _, sub := range s.subs {
			if sub.client == client {
				stranded = append(stranded, sub)
			}
		}
		s.subMu.Unlock()
		for _, sub := range stranded {
			sub.cancel()
		}
	}()
	for {
		notification, ok := <-client.Notifications()
		if !ok {
			return
		}
		s.routeNotification(notification)
	}
}

// routeNotification delivers one remote notification to the subscription for
// its (translated) thread. It observes the subscriber under the lock and sends
// outside it; a stale reference is harmless because in is never closed.
//
// Waiting rather than dropping is deliberate: a dropped turn/completed is
// exactly the failure this component exists to prevent, and the buffer is
// sized so ordinary jitter is absorbed here. The wait is BOUNDED, though,
// because this is the single drain goroutine shared by every thread on the
// host: a consumer that stops reading its own out blocks its pump, fills the
// 128-slot in buffer, and would otherwise block this goroutine for all threads
// — and, because a blocked drain stops reading client.Notifications(), the
// client's frame buffer eventually overflows and tears the whole connection
// down, killing every subscription on the host. On expiry the stalled
// subscription is retired: its pump exits, out closes, and the controller
// relay treats that as subscription end and re-reads the thread, so the
// notification is resynced rather than silently lost. A pump that has already
// exited unblocks via pumpDone instead.
func (s *RemoteHubSource) routeNotification(notification appwire.Notification) {
	translated, threadID, ok := s.translateNotification(notification)
	if !ok || threadID == "" {
		return
	}
	s.subMu.Lock()
	sub := s.subs[threadID]
	s.subMu.Unlock()
	if sub == nil {
		return
	}
	// Fast path: the buffer has room, which is the overwhelmingly common case,
	// so no timer is created for a healthy hand-off.
	select {
	case sub.in <- translated:
		return
	default:
	}
	stall := time.NewTimer(remoteHubRouteStallTimeout)
	defer stall.Stop()
	select {
	case sub.in <- translated:
	case <-sub.pumpDone:
	case <-stall.C:
		sub.cancel()
	}
}

// translateNotification rewrites a remote notification's ref-bearing fields
// into the controller namespace at the JSON level. It handles every
// ref-bearing notification uniformly without a method switch and preserves
// fields it does not understand.
//
// The translated ref is the authoritative routing identity whenever a
// notification carries one. It is NOT safe to key on the bare threadId: after a
// stable-ref identity swap the remote hub keeps pushing the stable ref while
// threadId names the replacement session instance (server. ReplaceAppIdentity
// moves s.appThreadID while s.appRef survives), so keying on threadId would
// miss the subscription and silently drop every delta. This mirrors the
// controller relay's own relayNotificationRoutingKey precedence. A notification
// with no ref falls back to its bare threadId.
//
// A notification whose ref names a nested remote hub's source (anything but
// "local") is DROPPED: it is not representable in the controller namespace and
// a notification has no error channel to report it on.
func (s *RemoteHubSource) translateNotification(n appwire.Notification) (appwire.Notification, string, bool) {
	if len(n.Params) == 0 {
		return n, "", false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(n.Params, &fields); err != nil {
		return n, "", false
	}
	if len(fields) == 0 {
		return n, "", false
	}

	threadID := ""
	if raw, ok := fields["threadId"]; ok {
		var id string
		if err := json.Unmarshal(raw, &id); err == nil {
			threadID = id
		}
	}

	if raw, ok := fields["ref"]; ok {
		var rawRef string
		if err := json.Unmarshal(raw, &rawRef); err != nil {
			return n, "", false
		}
		if rawRef != "" {
			translated, err := s.fromRemoteRefString(rawRef)
			if err != nil {
				return n, "", false
			}
			encoded, err := json.Marshal(translated)
			if err != nil {
				return n, "", false
			}
			fields["ref"] = encoded
			if ref, err := appwire.ParseRef(translated); err == nil {
				threadID = ref.ThreadID
			}
		}
	}

	if raw, ok := fields["thread"]; ok {
		translated, err := s.translateThreadRaw(raw)
		if err != nil {
			return n, "", false
		}
		fields["thread"] = translated
	}

	encoded, err := json.Marshal(fields)
	if err != nil {
		return n, "", false
	}
	n.Params = encoded
	return n, threadID, true
}
