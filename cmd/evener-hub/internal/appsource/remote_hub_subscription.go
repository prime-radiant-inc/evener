package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
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
	// threadID is this hub's routing key for the subscription: the key
	// routeNotification looks the subscription up by. It starts as the effective
	// remote thread identity the attach request resolves to — a bare caller
	// threadId when the caller sent one, otherwise the translated ref's suffix,
	// the same precedence the remote hub's own delivery identity resolves with
	// (threadRelayTarget) — so a notification the remote emits while the attach is
	// still in flight is routed under the identity the remote keyed it by.
	// settleSubscriber re-keys it to the canonical identity the successful
	// snapshot named, which is what ownership and routing compare from then on.
	threadID string
	// remoteRef is the ref this subscription issued to the remote hub
	// ("local:<thread>"). thread/unsubscribe names it to drop the remote side;
	// without that the long-lived shared client would keep forwarding this
	// thread's notifications for the rest of the connection's life. Until the
	// subscribe answers it is the caller-derived provisional target
	// (remoteSubscriptionTarget); settleSubscriber replaces it with the canonical
	// ref the successful snapshot named, because the remote hub keys the
	// subscription under the ref it resolved — a current root thread ID is
	// canonicalized to its stable ref — not necessarily under the caller's
	// threadId. Ownership and teardown must compare that canonical identity.
	remoteRef string
	// provisionalRef is the caller-derived remote target this subscription was
	// created with (remoteSubscriptionTarget), retained for the life of the
	// subscription. remoteRef moves to the canonical identity at settlement, so
	// the provisional value is how two subscriptions for the same remote thread
	// are recognized as such while one of them is still attaching: a replacement
	// created with the same caller addressing shares this value, and an identity
	// replacement that moves the live thread ID makes the canonical ref differ
	// from it. It is not the only signal — an attaching sibling addressed by
	// another alias of the same thread cannot be told apart from one for a
	// different thread, and is claimed on that possibility alone. See
	// refClaimLocked.
	provisionalRef string
	// settled reports whether the subscribe request answered and remoteRef is the
	// canonical identity the remote keyed. A subscription that has not settled is
	// still attaching: it may adopt (or abandon) the remote ref a retiring
	// predecessor held, so a predecessor defers its unsubscribe to it rather than
	// dropping a feed it may yet come to own. Written under subMu.
	settled bool
	// relayIdentity is the controller relay key this subscription was attached
	// for (WithRelayIdentity), or "" when the caller carried none. Remote
	// subscriptions live per (connection, thread), so two controller relays that
	// address one remote thread by different refs cannot both hold the remote's
	// subscription: the second attach would displace the first and the first's
	// recovery would displace it back. The identity is what lets a still-attaching
	// sibling be recognized as the same relay re-attaching instead of a second
	// relay, and it is what refuses the second (foreignRelayLocked). Written
	// before the subscription is published and never after.
	relayIdentity string
	// deferred are remote refs a retired predecessor left for this subscription to
	// release. It inherited them rather than unsubscribing them because it may
	// adopt the same remote thread; settle (releaseDeferredLocked) and discard
	// (releaseRefsLocked) resolve them once this subscription's own identity is
	// known, unsubscribing any it did not adopt. Written under subMu.
	deferred []string
	// client is the shared per-host client this subscription attached to. When
	// that client's notification stream closes, every subscription bound to it
	// is a dead end and must be retired (see drainLoop): its pump would
	// otherwise block forever and its out would never close, so the relay's
	// recovery path would never re-attach to the reconnected client.
	client *appwire.Client
	// ctx is this subscription's own context, derived from the caller's in
	// SubscribeThread. It is what ends the subscription: the pump exits when it
	// is done (closing out, which is how the caller observes subscription end),
	// the drain retires a stalled or stranded subscription by cancelling it, and
	// a replacement cancels the predecessor it displaces. A subscription whose
	// context is already done (or that was built without one) can never serve
	// again, and liveness must say so: one whose pump never started — the caller
	// gave up while its subscribe was still in flight — has no pumpDone for
	// anything to close, so without the context it would keep looking live and a
	// failed replacement could restore it as a routing target nothing pumps. See
	// subscriberLiveLocked.
	ctx context.Context
	// in is the drain's delivery slot. It is NEVER closed.
	in chan appwire.Notification
	// out is returned to the relay and closed ONLY by the pump.
	out chan appwire.Notification
	// pumpDone is closed by the pump on exit so a fan-out sender racing the
	// pump's own teardown can give up without blocking. A failed install closes
	// it directly, because no pump was started to do so.
	pumpDone chan struct{}
	// cancel stops the subscription's pump. It is called when the relay context
	// is cancelled, when a newer subscription replaces this one, when the
	// owning client's notification stream ends, or when the shared drain finds
	// its consumer so far behind that its buffers are full.
	cancel context.CancelFunc
	// onBufferedHandoff, when set, runs at the start of
	// forwardBufferedNotifications while subMu is held. It is an ordering seam
	// for tests — production never sets it — so a test can park the hand-off
	// with its critical section open and prove a concurrent routeNotification
	// cannot slip a send past it.
	onBufferedHandoff func()
}

// relayIdentityContextKey is the context key WithRelayIdentity stores under. The
// type is unexported so only the constructor below can set the value.
type relayIdentityContextKey struct{}

// WithRelayIdentity returns a context carrying the controller relay key a
// subscription is being attached for.
//
// The hub's relay supervisor labels every attach it issues with its own relay
// key (app_relay.go startRelay), which is what lets a source that keys its
// subscriptions by remote thread identity tell one relay re-attaching its thread
// apart from a second relay that reached the same thread by another address. A
// caller that attaches no identity keeps the unconditional replacement
// semantics.
func WithRelayIdentity(ctx context.Context, relayKey string) context.Context {
	if ctx == nil || strings.TrimSpace(relayKey) == "" {
		return ctx
	}
	return context.WithValue(ctx, relayIdentityContextKey{}, relayKey)
}

// RelayIdentityFromContext returns the controller relay key carried by ctx, or
// "" when its caller attached none.
func RelayIdentityFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	identity, _ := ctx.Value(relayIdentityContextKey{}).(string)
	return strings.TrimSpace(identity)
}

// SubscribeThread attaches the controller relay to a remote thread.
//
// It issues thread/read{Ref:"local:<thread>", Subscribe:true} on the shared
// per-host client. The returned snapshot is the remote's atomic attach point:
// the controller relay read the thread before subscribing (its non-atomic
// prepareRelay branch), so the snapshot is what proves a delta in between
// belongs to this subscription, and settleSubscriber turns it into a leading
// resync that folds it into the controller's copy. From then on every
// ref-bearing notification the remote pushes is translated and routed to this
// subscription by remote thread identity.
func (s *RemoteHubSource) SubscribeThread(ctx context.Context, params appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return nil, err
	}
	client, err := s.resolveClient(ctx)
	if err != nil {
		return nil, s.mapCallError(err)
	}

	subCtx, cancel := context.WithCancel(ctx)
	// The caller-derived provisional identity is the same target in two forms:
	// the thread alone, which is the initial routing key, and that thread as a
	// remote-namespace ref, which doubles as the initial remoteRef. Until the
	// subscribe answers the target is the best identity available, and it is
	// retained separately so a same-thread replacement can be recognized while
	// this subscription is still attaching (see refClaimLocked). The effective
	// thread it resolves to — a bare caller threadId in preference to the ref's
	// suffix — is what the remote keys this connection's subscription by, so it is
	// also the key a notification emitted before settleSubscriber re-keys to the
	// snapshot's canonical identity has to be routed under; see
	// remoteSubscriptionTargetThread.
	targetThread := remoteSubscriptionTargetThread(ref, params.ThreadID)
	target := remoteSubscriptionTarget(ref, params.ThreadID)
	sub := &remoteHubSubscription{
		threadID:       targetThread,
		provisionalRef: target,
		// The remote hub keys this connection's subscription by the thread it
		// resolved, preferring a bare threadId over the ref (threadRelayTarget
		// drives both thread/read's relay and thread/unsubscribe). Teardown must
		// name that same identity or it unsubscribes a ref the remote never
		// subscribed, so the effective target — the caller's threadId when it sent
		// one — is what is recorded here.
		remoteRef: target,
		client:    client,
		ctx:       subCtx,
		in:        make(chan appwire.Notification, remoteHubSubBuffer),
		out:       make(chan appwire.Notification, remoteHubSubBuffer),
		pumpDone:  make(chan struct{}),
		cancel:    cancel,
		// The relay this attach belongs to, when the hub's relay supervisor is
		// the caller. A source that keys remote subscriptions by remote thread
		// identity uses it to recognize its own relay's re-attach.
		relayIdentity: RelayIdentityFromContext(ctx),
	}

	remote := params
	remote.Ref = ref.String()
	// Forward the caller's ThreadID verbatim, empty included. The remote hub
	// resolves a bare non-empty ThreadID in preference to Ref, so substituting
	// the translated ref's suffix would address the thread by its stable
	// identity — which the remote rejects as an unknown bare ID the moment an
	// identity replacement has moved the live thread ID (appRef survives, the
	// thread ID does not). An empty ThreadID lets the remote resolve the ref,
	// which is stable-aware. Which of the two the remote keys this connection's
	// subscription by is the effective thread (remoteSubscriptionTargetThread),
	// and that is this hub's provisional routing key until settleSubscriber
	// re-keys it to the canonical identity the subscription snapshot names.
	remote.ThreadID = params.ThreadID
	remote.Subscribe = true
	// The remote client is shared by every controller relay for this host, so
	// controller-level replacement semantics must never reach it: a
	// replaceSubscription read would scope the whole remote connection to this
	// one thread and silently drop the other threads' remote subscriptions
	// while their local routing entries stayed live. Remote subscriptions are
	// managed independently here, one per remote thread identity.
	remote.ReplaceSubscription = false
	// The attach is deliberately minimal. The snapshot's own thread identity is
	// the only thing this source reads from it (settleSubscriber keys routing by
	// it and canonicalizes the remote ref from it), and the leading resync makes
	// the relay re-read the turns itself, so the request asks for no turn
	// payload. Forwarding the caller's IncludeTurns made the remote assemble a
	// full turn/item page before it could answer, which widens the interval
	// between the remote installing the subscription and the response that
	// confirms it — the interval the cleanup release must never cut short (see
	// requestSubscribe).
	remote.IncludeTurns = false
	remote.ItemsView = ""
	remote.ItemLimit = 0

	// Register before the request goes out, so a notification the remote emits
	// immediately after attaching cannot be missed. installSubscriber does not
	// cancel a subscription it displaces: the replacement is only committed once
	// its own subscribe succeeds.
	//
	// A live subscription attached for a different relay is not a replacement to
	// displace but a second relay for one remote thread, and the two would trade
	// the subscription until one of them stopped watching. That attach is refused
	// before any request reaches the remote when it addresses the incumbent's
	// routing slot directly (see admitSubscriber), and at settlement when it
	// reaches the same thread through another alias and only the snapshot reveals
	// the shared identity (see settleSubscriber); the refused relay's own backoff
	// decides when it tries again.
	previous, admitErr := s.admitSubscriber(sub)
	if admitErr != nil {
		cancel()
		return nil, admitErr
	}
	// The request is issued on a context detached from the caller's, so a
	// cancellation cannot lose its outcome: the remote may have installed a
	// subscription the cleanup path must not unsubscribe before the subscribe
	// itself has finished. See requestSubscribe — the outcome is the only thing
	// that may release the remote side, so nothing local may cut the request
	// short.
	outcome := s.requestSubscribe(subCtx, client, remote)

	select {
	case result := <-outcome:
		if result.err != nil {
			s.discardSubscriber(sub, previous)
			callerCanceled := ctx.Err() != nil
			cancel()
			// A cancellation that did not come from the caller is the owning
			// client's notification stream closing mid-request (drainLoop retires
			// subscriptions bound to a dead client). That is a transport loss, and
			// mapping it as one keeps the auto-resume gate working; reporting
			// context.Canceled would read as the caller giving up.
			if !callerCanceled && errors.Is(result.err, context.Canceled) {
				return nil, appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": subscription ended before it attached")
			}
			return nil, s.mapCallError(result.err)
		}
		if installed, refusal := s.settleSubscriber(sub, result.snapshot); !installed {
			// A concurrent replacement won this thread's routing slot while the
			// subscribe was in flight. Installing this subscription anyway would
			// hand the relay a channel nothing routes to — a live-looking but
			// permanently silent subscription — so discard it and report the
			// loss. The relay treats the error as a failed subscribe and
			// re-attaches through its recovery path.
			s.discardSubscriber(sub, previous)
			cancel()
			if refusal != nil {
				// Another relay reached this remote thread through an alias of its
				// address, so it never collided with that relay at admission: only
				// the snapshot revealed the identity the two share. Nothing was
				// displaced, and the relay already serving the thread keeps serving
				// it — surface the same refusal admission produces.
				return nil, refusal
			}
			return nil, appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": subscription was replaced before it attached")
		}
		go s.pumpSubscription(subCtx, sub)
		if previous != nil && previous != sub {
			previous.cancel()
		}
		return sub.out, nil
	case <-subCtx.Done():
		// The caller (or the drain retiring this client's connection) cancelled
		// after the request was sent. Return at once, but keep the request alive:
		// the remote may still install a subscription for it, and the cleanup
		// unsubscribe must not be issued until that request has finished. The entry
		// stays in the routing table until then, but its cancelled context keeps it
		// from ever being mistaken for a live target (see subscriberLiveLocked): no
		// pump will ever run for it.
		callerCanceled := ctx.Err() != nil
		cancel()
		go s.retireCanceledSubscribe(sub, previous, outcome)
		if !callerCanceled {
			return nil, appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": subscription ended before it attached")
		}
		return nil, s.mapCallError(subCtx.Err())
	}
}

// subscribeOutcome is one detached subscribe request's result.
type subscribeOutcome struct {
	snapshot appwire.ThreadReadResponse
	err      error
}

// requestSubscribe issues the subscribed thread/read on a context detached from
// the caller's (and the drain's). A request cancelled after it reaches the
// remote may already have installed a remote subscription, and its outcome is
// what tells cleanup whether there is something to unsubscribe — so it has to
// survive the cancellation that ends the caller's wait. It still observes the
// client closing, because appwire fails a pending request when its read loop
// exits.
//
// There is deliberately no local deadline. A local deadline cancels only this
// wait: the remote may still be working the request and can install its
// subscription after cleanup has already sent thread/unsubscribe, leaving a
// remote feed with no local owner for the rest of the connection's life. The
// outcome this channel carries is the only release trigger, and it is settled
// by the remote's own answer or by the connection ending — the one cancellation
// the remote hub actually observes (it reaps a connection's subscriptions when
// that connection goes away). That is the attachment's bound: a wedged remote
// holds one cleanup goroutine and one routing entry until its connection dies,
// never a subscription the remote created after this side gave up.
func (s *RemoteHubSource) requestSubscribe(subCtx context.Context, client *appwire.Client, remote appwire.ThreadReadParams) <-chan subscribeOutcome {
	outcome := make(chan subscribeOutcome, 1)
	reqCtx := context.WithoutCancel(subCtx)
	go func() {
		var snapshot appwire.ThreadReadResponse
		err := client.Request(reqCtx, appwire.MethodThreadRead, remote, &snapshot)
		outcome <- subscribeOutcome{snapshot: snapshot, err: err}
	}()
	return outcome
}

// retireCanceledSubscribe waits for a subscribe request that outlived its
// caller, then releases the local and remote state it may have installed. The
// wait is the point: thread/unsubscribe names the subscription the request
// created, and the remote hub handles the subscribe and the unsubscribe
// concurrently, so an unsubscribe issued while the subscribe is still in flight
// can land first and leave the later subscribe active with no local owner.
//
// A subscribe that completed successfully reveals the canonical ref the remote
// keyed, and cleanup must unsubscribe that identity rather than the caller's
// provisional target: the remote canonicalizes a current root thread ID to its
// stable ref, so unsubscribing the caller's threadId names a subscription the
// remote never held and leaks the one it did.
func (s *RemoteHubSource) retireCanceledSubscribe(sub, previous *remoteHubSubscription, outcome <-chan subscribeOutcome) {
	result := <-outcome
	if result.err == nil {
		s.canonicalizeRemoteRef(sub, result.snapshot.Thread.Evener.Ref)
	}
	s.discardSubscriber(sub, previous)
}

// remoteSubscriptionTargetThread is the remote thread identity an attach request
// resolves to: a bare non-empty threadId in preference to the translated ref's
// suffix. That is the precedence the remote hub's own delivery identity resolves
// with (threadRelayTarget — both thread/read's relay and thread/unsubscribe go
// through it), so it is both the identity this subscription is initially routed
// under and the thread of the remote-namespace ref it is unsubscribed by until
// the subscribe answers (remoteSubscriptionTarget).
func remoteSubscriptionTargetThread(ref appwire.Ref, threadID string) string {
	if trimmed := strings.TrimSpace(threadID); trimmed != "" {
		return trimmed
	}
	return ref.ThreadID
}

// remoteSubscriptionTarget is the caller-derived provisional identity this
// subscription is unsubscribed by until the subscribe answers. A caller that sent
// both a ref and a bare threadId must be unsubscribed by the threadId it sent —
// not by the translated ref's suffix, which would name a subscription the remote
// never created. settleSubscriber replaces it with the canonical ref the
// successful subscription snapshot named, which is what ownership and teardown
// ultimately compare.
func remoteSubscriptionTarget(ref appwire.Ref, threadID string) string {
	return appwire.Ref{SourceID: remoteHubNamespace, ThreadID: remoteSubscriptionTargetThread(ref, threadID)}.String()
}

// settleSubscriber installs the authoritative routing identity a successful
// subscribe revealed and hands the relay a leading resync.
//
// The remote hub resolves a bare threadId in preference to the ref, so a caller
// that sent both can be subscribed to a thread the ref's suffix does not name.
// The subscription snapshot's own ref is the only authority on which thread the
// subscription is for, so routing is keyed from it; when it differs from the
// provisional key the entry is re-keyed before the pump starts.
//
// The resync is the other half of the atomic handoff: the controller relay read
// this thread before subscribing (its non-atomic prepareRelay branch), so any
// delta the remote emitted between that read and this subscription is in the
// snapshot but never in the controller's copy. The controller relay answers a
// resync by re-reading the thread, which folds the snapshot in; the notification
// stream then carries every delta after it. Writing it directly to out before
// the pump starts makes it the consumer's first frame.
//
// Both halves are driven by the snapshot actually carrying the thread it
// attached to. A real hub's subscribed read always returns it; a snapshot with
// no thread at all is a degenerate attach with no authoritative identity to key
// on and nothing to fold in, so the caller-derived key stands and the
// controller's copy is left alone.
//
// It reports whether sub is the installed routing target when it returns, and,
// when it is not, the refusal the caller has to surface. false means sub must
// not be installed: the caller must not start sub's pump or hand the relay sub's
// channel, because nothing will ever route to it. A re-key that finds sub
// displaced reports false with no refusal — a concurrent replacement took the
// slot — and a snapshot key that already matches sub's key still checks that sub
// is the installed subscription, since a replacement for the same thread under
// the same key leaves the snapshot key equal to sub.threadID. A re-key that
// finds a live subscription attached for a different controller relay reports
// false with that Conflict: aliases of one remote thread occupy different
// provisional keys, so the collision admission refuses on a shared key only
// becomes visible here, and displacing the incumbent instead would be the
// flapping the refusal exists to prevent (see foreignRelayLocked).
//
// The re-key, the canonical remote identity, and the installed check all happen
// under one remoteMu+subMu hold, so a replacement can never observe sub at its
// new key with a stale provisional ref: that interleaving is what let a retiring
// predecessor conclude the ref was unowned and unsubscribe the remote
// subscription its live replacement had just adopted.
func (s *RemoteHubSource) settleSubscriber(sub *remoteHubSubscription, snapshot appwire.ThreadReadResponse) (bool, error) {
	degenerate := snapshot.Thread.ID == "" && snapshot.Thread.Evener.Ref == ""
	canonical := strings.TrimSpace(snapshot.Thread.Evener.Ref)
	key := ""
	if !degenerate {
		key = s.snapshotRoutingKey(snapshot)
	}

	s.remoteMu.Lock()
	s.subMu.Lock()
	if s.subs[sub.threadID] != sub {
		// A concurrent replacement owns this thread's routing slot. The subscribe
		// still succeeded and the remote keyed it under the snapshot's canonical
		// ref, so record that identity before the caller's cleanup releases: the
		// caller-derived provisional target may be a thread ID the remote
		// canonicalizes away, and unsubscribing it would leave the stable-ref
		// subscription active with no local owner.
		if canonical != "" {
			sub.remoteRef = canonical
		}
		s.subMu.Unlock()
		s.remoteMu.Unlock()
		return false, nil
	}
	var displaced *remoteHubSubscription
	if key != "" && key != sub.threadID {
		// Aliases of one remote thread occupy different provisional keys, so
		// admission cannot see that two relays are attaching to the same thread:
		// only the snapshot names the identity the aliases share. A live
		// subscription attached for another relay already serves that identity,
		// and displacing it is exactly the flapping the admission refusal exists
		// to prevent — the displaced relay recovers, re-attaches under its own
		// identity, is refused in turn, and its client starves until this one
		// ends. Refuse this attach now that the collision is visible, with the
		// incumbent left untouched.
		if incumbent := s.subs[key]; s.foreignRelayLocked(sub, incumbent) {
			// The refused attach's subscribe request has already reached the remote,
			// which keys this connection's registration by the address that request
			// carried (relayDeliveryTarget on its side) — the delivery identity, not
			// the canonical ref the snapshot named. Recording the snapshot's ref here
			// replaced that identity with the incumbent's, and cleanup releases only
			// remoteRef and any deferred refs (remoteRefsLocked): because the
			// incumbent holds the canonical ref, refClaimLocked called it owned and
			// nothing was unsubscribed, so the rejected request's own registration
			// stayed active and the remote kept forwarding the thread to this
			// connection with no local owner — duplicate delivery for the life of the
			// connection. remoteRef already holds exactly the identity this attach is
			// responsible for (its caller-derived delivery target, per this field's
			// contract), so it is deliberately left in place: discardSubscriber
			// releases it, the incumbent's canonical ref is not in that release set,
			// and refClaimLocked still protects any identity a live sibling holds or
			// a still-attaching one may yet adopt.
			refusal := s.foreignRelayErrorLocked(incumbent)
			s.subMu.Unlock()
			s.remoteMu.Unlock()
			return false, refusal
		}
		delete(s.subs, sub.threadID)
		displaced = s.subs[key]
		sub.threadID = key
		s.subs[key] = sub
		s.ensureDrainLocked(sub.client)
	}
	// The snapshot's own ref is the identity the remote actually attached this
	// connection's subscription to. The caller-derived target is only a
	// provisional routing key: the remote canonicalizes a current root thread ID
	// to its stable ref (and ignores the caller's bare threadId once a ref is
	// present), so a replacement that sent a different threadId would otherwise
	// appear to own a different remote ref. Ownership and teardown are decided on
	// the canonical identity, so the predecessor can recognize the replacement
	// and the unsubscribe names the remote's own ref rather than a stale one.
	if canonical != "" {
		sub.remoteRef = canonical
	}
	sub.settled = true
	s.subMu.Unlock()
	s.remoteMu.Unlock()

	if displaced != nil && displaced != sub {
		displaced.cancel()
	}
	// A concurrent replacement (or the cancel above) may have taken this thread's
	// routing slot after the atomic settle. A subscription that is no longer
	// installed must not hand the relay a channel nothing routes to.
	if !s.subscriberInstalled(sub) {
		return false, nil
	}
	// Refs a retired predecessor deferred to this subscription are resolved now
	// that its own canonical identity is known: the one it adopted is its own,
	// and any it did not is handed to a still-attaching sibling or unsubscribed.
	s.releaseDeferredLocked(sub)
	if degenerate {
		// No authoritative identity to key on and nothing to fold in: the
		// caller-derived key stands and the controller's copy is left alone.
		return true, nil
	}
	resync := *appwire.NotificationMessage(appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
		ThreadID: sub.threadID,
		Ref:      appwire.Ref{SourceID: s.id, ThreadID: sub.threadID}.String(),
	}).Notification
	sub.out <- resync
	return true, nil
}

// canonicalizeRemoteRef records the remote-namespace ref a completed subscribe
// named as sub's remote identity. It is the identity the remote hub keyed the
// subscription under, so it is what ownership and teardown must name. Unlike the
// settlement path it does not require sub to still be the installed routing
// target: cleanup has to unsubscribe the identity the remote actually created
// even when a replacement has since taken sub's slot. An empty ref leaves the
// caller-derived provisional target in place, the best identity available when
// the attach revealed none.
//
// remoteMu serializes it against retireSubscription's check-then-unsubscribe so a
// predecessor observes the canonical ref; subMu orders the write against
// refClaimLocked's read.
func (s *RemoteHubSource) canonicalizeRemoteRef(sub *remoteHubSubscription, remoteRef string) {
	remoteRef = strings.TrimSpace(remoteRef)
	if remoteRef == "" {
		return
	}
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	s.subMu.Lock()
	defer s.subMu.Unlock()
	sub.remoteRef = remoteRef
}

// subscriberInstalled reports whether sub is the routing target the caller's
// identity currently maps to. A concurrent replacement installs itself under
// the same key before its own subscribe succeeds, so this is how a stale
// subscribe learns it has already been displaced.
func (s *RemoteHubSource) subscriberInstalled(sub *remoteHubSubscription) bool {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	s.subMu.Lock()
	defer s.subMu.Unlock()
	return s.subs[sub.threadID] == sub
}

// snapshotRoutingKey returns the controller-namespace routing key named by the
// atomic subscription snapshot's own ref. An empty, unparseable, or
// unrepresentable snapshot ref yields "", so the caller keeps the
// caller-derived key it already registered.
func (s *RemoteHubSource) snapshotRoutingKey(snapshot appwire.ThreadReadResponse) string {
	remoteRef := strings.TrimSpace(snapshot.Thread.Evener.Ref)
	if remoteRef == "" {
		return ""
	}
	translated, err := s.fromRemoteRefString(remoteRef)
	if err != nil {
		return ""
	}
	ref, err := appwire.ParseRef(translated)
	if err != nil {
		return ""
	}
	return ref.ThreadID
}

// installSubscriber publishes sub as the routing target for its remote thread
// and ensures this client's notification stream is being drained. It publishes
// unconditionally, ignoring the foreign-relay admission SubscribeThread applies
// (admitSubscriber): callers that publish directly are not attaching a second
// relay. It holds remoteMu so a concurrent retireSubscription's
// check-then-unsubscribe cannot interleave with the install: that ordering is
// what keeps a replacement's remote subscription from being dropped by its
// predecessor's teardown.
func (s *RemoteHubSource) installSubscriber(sub *remoteHubSubscription) *remoteHubSubscription {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	s.subMu.Lock()
	defer s.subMu.Unlock()
	return s.publishLocked(sub)
}

// admitSubscriber publishes sub as the routing target for its remote thread
// unless a live subscription attached for a different controller relay already
// serves that thread, returning that subscription and the refusal. A refused
// subscription was never published, so its caller must not send a subscribe
// request for it: the remote's subscription belongs to the incumbent, and a
// request for the same thread would take it over.
//
// Remote subscriptions live per (connection, thread), so two controller relays
// that address one remote thread by different refs cannot both hold the remote's
// subscription. Letting the second take the routing slot cancels the first,
// whose relay re-attaches and displaces the newcomer in turn — and because a
// recovery that succeeds resets that relay's backoff, the two would trade the
// subscription for as long as both are watched, with a remote round trip per
// swap and a flapping stream for both clients. Refusing the second relay keeps
// one stable subscription and one relay serving it; the refused relay retries on
// its own backoff and takes over once the incumbent has ended.
func (s *RemoteHubSource) admitSubscriber(sub *remoteHubSubscription) (*remoteHubSubscription, error) {
	if sub.relayIdentity == "" {
		// A caller that attached no relay identity cannot be a second relay's
		// attach, so it keeps the unconditional replacement semantics exactly.
		return s.installSubscriber(sub), nil
	}
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	s.subMu.Lock()
	defer s.subMu.Unlock()
	previous := s.subs[sub.threadID]
	if s.foreignRelayLocked(sub, previous) {
		return previous, s.foreignRelayErrorLocked(previous)
	}
	return s.publishLocked(sub), nil
}

// publishLocked publishes sub as the routing target for its remote thread and
// ensures this client's notification stream is being drained. Called with
// remoteMu and subMu held.
func (s *RemoteHubSource) publishLocked(sub *remoteHubSubscription) *remoteHubSubscription {
	previous := s.subs[sub.threadID]
	s.subs[sub.threadID] = sub
	s.ensureDrainLocked(sub.client)
	return previous
}

// foreignRelayLocked reports whether a live subscription attached for a
// different controller relay already serves sub's thread. Called with subMu
// held.
//
// The relay identity is the hub's relay key for the attach (WithRelayIdentity).
// The same relay re-attaching its thread — the recovery after its subscription
// ended, a client reconnecting through the same relay — keeps its identity, and
// this reports false for it, so the replacement semantics are untouched. A
// second relay that reached the same remote thread by another ref carries a
// different key. When the two addresses share a provisional routing slot
// admission sees the collision and refuses it there (admitSubscriber); when they
// reach one remote thread through different aliases — a current root thread ID
// and the stable ref, which the remote resolves onto one subscription identity —
// only the subscribe snapshot reveals the shared identity, and settlement makes
// the same refusal before it re-keys the newcomer onto the incumbent's slot
// (settleSubscriber). Either way one relay keeps serving the thread and the
// other retries on its own backoff. A subscription published without an identity
// (a direct caller) is never treated as a foreign relay.
func (s *RemoteHubSource) foreignRelayLocked(sub, existing *remoteHubSubscription) bool {
	if existing == nil || existing == sub {
		return false
	}
	if sub.relayIdentity == "" || existing.relayIdentity == "" || sub.relayIdentity == existing.relayIdentity {
		return false
	}
	return s.subscriberLiveLocked(existing)
}

// foreignRelayErrorLocked reports a refused attach, naming the identity the
// incumbent holds so the caller can address the thread the way the relay serving
// it already does. Called with remoteMu and subMu held; it reads the incumbent's
// remote identity under that lock.
func (s *RemoteHubSource) foreignRelayErrorLocked(incumbent *remoteHubSubscription) error {
	ref := strings.TrimSpace(incumbent.remoteRef)
	if translated, err := s.fromRemoteRefString(ref); err == nil && translated != "" {
		ref = translated
	}
	if ref == "" {
		ref = s.id
	}
	return appwire.Conflict("thread is already relayed by " + ref + "; re-read it by that ref")
}

// discardSubscriber undoes a failed install. It restores the subscription it
// displaced so a transient subscribe failure leaves a healthy previous
// subscription serving — but only while that subscription can still serve. A
// displaced subscription whose pump has exited (its relay context was
// cancelled, the drain retired it as stalled), or whose client's notification
// stream has ended, can never deliver again: putting it back in the routing
// table would leave the relay waiting forever on an out nothing will close, and
// its own retireSubscription already skipped the remote-side unsubscribe
// because sub had displaced it. Such a subscription is dropped and cancelled
// here instead.
//
// It closes sub.pumpDone because no pump was started to close it, so sub is
// never mistaken for a live subscription; subscriberLiveLocked keys on that.
//
// A subscribe request that was cancelled (or lost) after reaching the server
// can leave a remote-side subscription behind that nothing on this side will
// ever retire, so the discard also sends a best-effort thread/unsubscribe —
// unless a live sibling still owns that remote ref on the same client, where
// the remote subscription is the sibling's to keep. The same release covers any
// ref a retired predecessor deferred to this subscription (releaseRefsLocked):
// the failed attach will never adopt them, so they are dropped now unless a
// sibling has taken them over.
//
// When a live previous subscription is restored, the notifications this failed
// replacement buffered while it was the routing target are handed to it first.
// The replacement displaced routing before its own subscribe succeeded, so the
// shared drain delivered the thread's in-flight deltas to sub.in; restoring the
// previous without forwarding them would lose those deltas with no resync to
// cover the gap. The table swap and that hand-off share one subMu critical
// section, so a fan-out send can never land between them: routeNotification
// either inserts into sub.in before the swap (and the hand-off below forwards
// it) or observes the restored previous as the routing target. Releasing subMu
// between the two left a window where the delta was inserted into a channel no
// pump would ever read, which the relay could not recover from — previous stayed
// live, so it never re-read the thread.
//
// A restore is only possible while sub still owns this thread's routing slot. A
// third subscription can install itself at sub's key while sub's own subscribe
// is in flight (installSubscriber does not cancel what it displaces); that
// subscription owns the slot now, so previous has been superseded and restoring
// it would hand the relay a subscription nothing routes to. previous is then
// retired like any other displaced subscription: without the gate it is neither
// restored nor cancelled, and its relay waits forever on an out nothing will
// ever close, never observing subscription end.
func (s *RemoteHubSource) discardSubscriber(sub, previous *remoteHubSubscription) {
	close(sub.pumpDone)
	s.remoteMu.Lock()
	s.subMu.Lock()
	installed := s.subs[sub.threadID] == sub
	restore := installed && previous != nil && previous != sub && s.subscriberLiveLocked(previous)
	if installed {
		if restore {
			s.subs[sub.threadID] = previous
		} else {
			delete(s.subs, sub.threadID)
		}
	}
	if restore {
		// Held across the hand-off: see the doc comment. routeNotification takes
		// the same lock around its capture and send, so its send cannot land
		// after this loop has drained sub.in into previous.
		forwardBufferedNotifications(sub, previous)
	}
	s.subMu.Unlock()
	if previous != nil && previous != sub && !restore {
		// The displaced subscription is dead, so unlike installSubscriber's
		// successful path nothing else will cancel it. Cancel it here so a
		// relay already blocked on its out unwinds and re-subscribes instead of
		// hanging on a pump that will never deliver again.
		previous.cancel()
	}
	s.releaseRefsLocked(sub)
	s.remoteMu.Unlock()
}

// forwardBufferedNotifications moves the notifications sub buffered while it was
// the routing target into the restored previous subscription. A non-blocking
// hand-off keeps the discard from parking; if previous's own buffer is full its
// consumer is far behind, so previous is retired and the relay resyncs — the
// deltas are then recovered by the resync rather than dropped silently.
//
// Called with subMu held, which is what makes the hand-off complete: no sender
// can insert into sub.in after this loop returns.
func forwardBufferedNotifications(sub, previous *remoteHubSubscription) {
	if previous == nil {
		return
	}
	if sub.onBufferedHandoff != nil {
		sub.onBufferedHandoff()
	}
	for len(sub.in) > 0 {
		notification := <-sub.in
		select {
		case previous.in <- notification:
		default:
			previous.cancel()
			return
		}
	}
}

// subscriberLiveLocked reports whether sub can still deliver. Called with subMu
// held. A subscription is live while its own context is alive and its pump — the
// running one, or the one it is about to start — has not been retired: once the
// context is done, nothing will ever feed sub.in again and the pump would block
// forever, and once the drain has exited for that client the same is true, so
// restoring such a subscription would strand the relay. The context is what
// catches a subscription whose pump never started at all — its caller gave up
// while the subscribe was still in flight — because nothing but the pump's
// discard closes pumpDone for it.
func (s *RemoteHubSource) subscriberLiveLocked(sub *remoteHubSubscription) bool {
	if sub == nil {
		return false
	}
	if sub.ctx == nil || sub.ctx.Err() != nil {
		return false
	}
	select {
	case <-sub.pumpDone:
		return false
	default:
	}
	if _, draining := s.drains[sub.client]; !draining {
		return false
	}
	return true
}

// remoteRefsLocked returns every remote ref sub is responsible for releasing:
// its own remote identity plus any ref a retired predecessor deferred to it.
// Called with subMu held.
func (sub *remoteHubSubscription) remoteRefsLocked() []string {
	refs := make([]string, 0, 1+len(sub.deferred))
	if sub.remoteRef != "" {
		refs = append(refs, sub.remoteRef)
	}
	for _, ref := range sub.deferred {
		if ref == "" || slices.Contains(refs, ref) {
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}

// refClaimLocked reports whether some subscription other than sub on sub's
// client is responsible for ref, and if not, a still-attaching sibling that may
// become responsible for it. Remote subscriptions live per (connection, thread),
// so unsubscribing a ref a live sibling still needs would drop that sibling's
// feed.
//
// A sibling holds ref when it names ref as its remote identity or has already
// inherited it. A sibling that is not settled may hold it: its remoteRef is only
// the caller-derived provisional target (remoteSubscriptionTarget), while the
// remote canonicalizes a current root thread ID to its stable ref, so the ref it
// will actually adopt can differ from the one it recorded. Two subscriptions for
// the same remote thread are recognized by their caller addressing — the shared
// provisionalRef — and, for the aliasing that addressing cannot express, by the
// fact that ANY settling sibling on this connection may be the one that adopts
// ref: the remote resolves several volatile identities (a current root thread
// ID and the stable ref, an instance ID before and after an identity
// replacement) onto one thread, and a retiring predecessor cannot see which of
// them an attaching replacement was addressed by. Refusing to claim on that
// basis unsubscribed the canonical ref a replacement was about to adopt, leaving
// it open, silent, and — because its own out never closes — permanently
// un-recovered. A ref that is not held but is claimed is returned so the caller
// can defer, not unsubscribe. Called with subMu held.
func (s *RemoteHubSource) refClaimLocked(sub *remoteHubSubscription, ref string) (bool, *remoteHubSubscription) {
	var claimant, possible *remoteHubSubscription
	for _, other := range s.subs {
		if other == sub || other.client != sub.client {
			continue
		}
		if other.remoteRef == ref || slices.Contains(other.deferred, ref) {
			return true, nil
		}
		if other.settled {
			continue
		}
		// Same-thread attachments share a provisional target, so that match is
		// exact rather than possible. Everything else that is still attaching on
		// this connection is only a possible claimant: its caller addressing may
		// be a different alias of the same remote thread. Either way the ref is
		// deferred, not released: the claimant resolves it when its own identity
		// is known.
		if claimant == nil && other.provisionalRef != "" &&
			(other.provisionalRef == ref || other.provisionalRef == sub.provisionalRef) {
			claimant = other
			continue
		}
		if possible == nil && other.provisionalRef != "" {
			possible = other
		}
	}
	if claimant != nil {
		return false, claimant
	}
	return false, possible
}

// releaseRefsLocked tells the remote hub to drop every remote ref sub is
// responsible for and no sibling has taken over. It is the retiring/failed
// subscription's last act: sub must already be removed from (or never have been
// the target of) the routing table. A ref a still-attaching sibling may yet
// adopt is handed to that sibling (sub.deferred) instead of unsubscribed, so a
// replacement's subscribe is never dropped out from under it. Called with
// remoteMu held (so the wire unsubscribe cannot overtake a sibling's subscribe);
// subMu is taken internally and released before the wire call.
func (s *RemoteHubSource) releaseRefsLocked(sub *remoteHubSubscription) {
	s.subMu.Lock()
	refs := sub.remoteRefsLocked()
	s.subMu.Unlock()
	s.releaseLocked(sub, refs, "")
}

// releaseDeferredLocked resolves the refs a retired predecessor deferred to sub
// now that sub's own canonical identity is known. It holds remoteMu across the
// release so the unsubscribe cannot overtake a concurrent replacement's
// subscribe for the same ref.
func (s *RemoteHubSource) releaseDeferredLocked(sub *remoteHubSubscription) {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	s.subMu.Lock()
	deferred := sub.deferred
	sub.deferred = nil
	s.subMu.Unlock()
	s.releaseLocked(sub, deferred, sub.remoteRef)
}

// releaseLocked unsubscribes the refs among refs that sub did not adopt and no
// sibling holds, deferring the ones a still-attaching sibling may yet adopt.
// adopted names the ref sub itself now holds and must not release here. Called
// with remoteMu held and subMu released; it takes subMu for the decision.
func (s *RemoteHubSource) releaseLocked(sub *remoteHubSubscription, refs []string, adopted string) {
	s.subMu.Lock()
	var orphans []string
	for _, ref := range refs {
		if ref == "" || (adopted != "" && ref == adopted) {
			continue
		}
		owned, claimant := s.refClaimLocked(sub, ref)
		switch {
		case owned:
		case claimant != nil:
			claimant.deferred = append(claimant.deferred, ref)
		default:
			orphans = append(orphans, ref)
		}
	}
	s.subMu.Unlock()
	for _, ref := range orphans {
		s.unsubscribeRefLocked(ref, sub.client)
	}
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
// installed subscription and tells the remote hub to drop the matching
// remote-side subscription, unless a live replacement owns it or a still-
// attaching replacement may yet adopt it. The remote client is long-lived, so
// without the unsubscribe the remote hub would keep forwarding the thread's
// notifications for the rest of the connection's life.
//
// The unsubscribe is skipped when another subscription on this same client
// already holds this remote ref, and deferred (releaseRefsLocked) when a
// still-attaching same-client subscription may adopt it: remote subscriptions
// live per (connection, thread), so only a replacement on the same connection
// can own this one. A replacement on a *different* client — a reconnect that
// installed its replacement before the old connection was closed — cannot, and
// skipping there would leave the old connection forwarding a thread nothing
// local watches. A deferred ref is released by that replacement when it settles
// or fails, so it is never leaked.
//
// remoteMu serializes this check-then-unsubscribe against installSubscriber, so
// a replacement's subscribe request can never land before its predecessor's
// unsubscribe and be dropped by it.
func (s *RemoteHubSource) retireSubscription(sub *remoteHubSubscription) {
	s.remoteMu.Lock()
	s.subMu.Lock()
	if s.subs[sub.threadID] == sub {
		delete(s.subs, sub.threadID)
	}
	s.subMu.Unlock()
	s.releaseRefsLocked(sub)
	s.remoteMu.Unlock()
}

// unsubscribeRefLocked drops one remote-side subscription with a bounded,
// best-effort thread/unsubscribe. The shared client is long-lived, so without it
// the remote hub would keep forwarding the thread's notifications for the rest
// of the connection's life. Called with remoteMu held, so it can never overtake a
// replacement's subscribe request.
func (s *RemoteHubSource) unsubscribeRefLocked(ref string, client *appwire.Client) {
	ref = strings.TrimSpace(ref)
	if ref == "" || client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteHubUnsubscribeTimeout)
	defer cancel()
	_, _ = client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: ref})
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
// never stop reading while the client is alive: appwire tears the connection
// down on buffer overflow (ErrNotificationOverflow), so a busy or stalled
// thread would kill every subscription on the host. routeNotification therefore
// never blocks — a subscription whose consumer stops reading is retired rather
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
		var orphaned []*remoteHubHostSubscription
		for sub := range s.hostSubs {
			if sub.client == client {
				delete(s.hostSubs, sub)
				orphaned = append(orphaned, sub)
			}
		}
		s.subMu.Unlock()
		for _, sub := range stranded {
			sub.cancel()
		}
		// Signal teardown on a dedicated channel and never close in: in stays
		// the drain goroutine's alone, so a host-fan-out send can never race a
		// close, and a pump parked on a full out is still unblocked (it selects
		// on clientDone) so the consumer observes the close and re-subscribes
		// against the next client.
		for _, sub := range orphaned {
			close(sub.clientDone)
		}
	}()
	for {
		notification, ok := <-client.Notifications()
		if !ok {
			return
		}
		// Host-level consumers see the notifications their filter accepts;
		// delivery is scoped to the owning client so one connection's traffic
		// never reaches another's subscriptions. It is prompt (non-blocking) and
		// stays in read order: it happens before this notification's thread
		// routing.
		s.publishHostNotification(client, notification)
		s.routeNotification(client, notification)
	}
}

// routeNotification delivers one remote notification to the subscription
// installed for its translated routing key, and does it without ever blocking
// the shared drain.
//
// A frame translateNotification cannot address in the controller namespace is
// dropped, as is one whose key has no installed subscription: a notification
// has no error channel to report either on.
//
// Delivery is scoped to the client the notification arrived on. A torn-down
// client can still have frames buffered in its notification channel when it
// closes, and the drain reading them runs concurrently with the relay
// re-subscribing against the replacement client: without this check those
// stale-generation events would be interleaved into a recovered feed, and a
// full buffer would even cancel the healthy replacement subscription below.
//
// The capture and the hand-off share one subMu critical section. sub.in is a
// per-subscription bounded read-ahead buffer, and the send into it is taken
// only when there is room — a full buffer is never waited on, because this is
// the single drain goroutine shared by every thread on the host and appwire
// tears the whole connection down once its own notification buffer
// (NotificationBufferCap) fills, so no consumer may stop this goroutine from
// reading client.Notifications(). Sharing the critical section with the
// capture is the ordering guarantee: discardSubscriber forwards a failed
// replacement's buffer and swaps the routing table back under that same lock,
// so with the lock released in between, a delta captured here could be inserted
// into the replaced subscription's buffer after its contents had already been
// forwarded to the restored previous, where no pump would ever read it and no
// resync would cover it.
//
// A subscription whose in buffer is full has a consumer at least
// 2*remoteHubSubBuffer notifications behind, far past the scheduling jitter
// those buffers exist to absorb, so it is retired instead of waited for: cancel
// runs after the lock is released, its pump exits, out closes, and the
// controller relay treats that as subscription end and re-reads the thread, so
// the notification is resynced rather than silently lost.
func (s *RemoteHubSource) routeNotification(client *appwire.Client, notification appwire.Notification) {
	translated, threadID, ok := s.translateNotification(notification)
	if !ok || threadID == "" {
		return
	}
	s.subMu.Lock()
	sub := s.subs[threadID]
	if sub != nil && sub.client != client {
		sub = nil
	}
	if sub == nil {
		s.subMu.Unlock()
		return
	}
	delivered := false
	select {
	case sub.in <- translated:
		delivered = true
	default:
	}
	s.subMu.Unlock()
	if !delivered {
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
//
// Nested session handles the payload carries — a job's or delegate's
// transcriptRef, a job-activity ownerRef/childRef — are translated too, while
// opaque handles ("job:<id>", "proj:<project>:<thread>") pass through untouched;
// see translateNestedRefs. The routing "ref" stays strict so a frame that cannot
// be addressed in the controller namespace is refused rather than mis-routed.
//
// The capability set a payload carries is masked to the actions this source can
// forward, in both of the shapes appwire has (see capabilitiesField): the evener
// object of a thread-bearing frame, by translateThreadRaw, and the top-level
// "capabilities" of a thread/status/changed frame, here. A status transition is
// exactly where a client refreshes the action set the snapshot gave it, so a set
// that disagreed with the read path would re-enable a mutation that then fails
// with an internal error.
//
// Image URLs are rewritten as well, on every carrier a notification can use:
// the thread of thread/started, the turn of turn/started and turn/completed, and
// the item of item/started and item/completed (see rewriteRemoteImageURL).
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
		fields["thread"] = s.rewriteRawThreadImageURLs(translated)
	}
	// turn/started and turn/completed carry a Turn; item/started and
	// item/completed carry a ThreadItem. Both are image carriers, so the shared
	// visitor runs on them here exactly as it runs on a thread.
	if raw, ok := fields["turn"]; ok {
		fields["turn"] = s.rewriteRawTurnImageURLs(raw)
	}
	if raw, ok := fields["item"]; ok {
		fields["item"] = s.rewriteRawItemImageURLs(raw)
	}
	if n.Method == appwire.NotifyThreadStatusChanged {
		if rawCapabilities, ok := fields[capabilitiesField]; ok {
			fields[capabilitiesField] = maskRemoteCapabilitiesRaw(rawCapabilities)
		}
	}

	encoded, err := json.Marshal(fields)
	if err != nil {
		return n, "", false
	}
	n.Params = s.translateNestedRefs(encoded)
	return n, threadID, true
}
