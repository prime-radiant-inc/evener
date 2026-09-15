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

// remoteHubSubscribeTimeout bounds the detached subscribe request a cancelled
// SubscribeThread leaves running. It is the longest this source waits for a
// remote subscribe to complete before issuing its cleanup unsubscribe, so a
// remote that never answers cannot leak the request goroutine forever. A live
// hub answers far inside it; the bound exists only for a wedged one.
const remoteHubSubscribeTimeout = 30 * time.Second

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
	// the provisional value is the only way two subscriptions for the same remote
	// thread can be recognized as such while one of them is still attaching: a
	// replacement created with the same caller addressing shares this value, and
	// an identity replacement that moves the live thread ID makes the canonical
	// ref differ from it. See refClaimLocked.
	provisionalRef string
	// settled reports whether the subscribe request answered and remoteRef is the
	// canonical identity the remote keyed. A subscription that has not settled is
	// still attaching: it may adopt (or abandon) the remote ref a retiring
	// predecessor held, so a predecessor defers its unsubscribe to it rather than
	// dropping a feed it may yet come to own. Written under subMu.
	settled bool
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
	client, err := s.client(ctx, s.id)
	if err != nil {
		return nil, s.mapCallError(err)
	}

	subCtx, cancel := context.WithCancel(ctx)
	// The caller-derived provisional identity doubles as the initial remoteRef:
	// until the subscribe answers it is the best identity available, and it is
	// retained separately so a same-thread replacement can be recognized while
	// this subscription is still attaching (see refClaimLocked).
	target := remoteSubscriptionTarget(ref, params.ThreadID)
	sub := &remoteHubSubscription{
		threadID:       ref.ThreadID,
		provisionalRef: target,
		// The remote hub keys this connection's subscription by the thread it
		// resolved, preferring a bare threadId over the ref (threadRelayTarget
		// drives both thread/read's relay and thread/unsubscribe). Teardown must
		// name that same identity or it unsubscribes a ref the remote never
		// subscribed, so the effective target — the caller's threadId when it sent
		// one — is what is recorded here.
		remoteRef: target,
		client:    client,
		in:        make(chan appwire.Notification, remoteHubSubBuffer),
		out:       make(chan appwire.Notification, remoteHubSubBuffer),
		pumpDone:  make(chan struct{}),
		cancel:    cancel,
	}

	remote := params
	remote.Ref = ref.String()
	// Forward the caller's ThreadID verbatim, empty included. The remote hub
	// resolves a bare non-empty ThreadID in preference to Ref, so substituting
	// the translated ref's suffix would address the thread by its stable
	// identity — which the remote rejects as an unknown bare ID the moment an
	// identity replacement has moved the live thread ID (appRef survives, the
	// thread ID does not). An empty ThreadID lets the remote resolve the ref,
	// which is stable-aware. The ref's suffix is only this hub's provisional
	// routing key: settleSubscriber re-keys it to the identity the subscription
	// snapshot actually names.
	remote.ThreadID = params.ThreadID
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
	// The request is issued on a context detached from the caller's, so a
	// cancellation cannot lose its outcome: the remote may have installed a
	// subscription the cleanup path must not unsubscribe before the subscribe
	// itself has finished. See requestSubscribe.
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
		if !s.settleSubscriber(sub, result.snapshot) {
			// A concurrent replacement won this thread's routing slot while the
			// subscribe was in flight. Installing this subscription anyway would
			// hand the relay a channel nothing routes to — a live-looking but
			// permanently silent subscription — so discard it and report the
			// loss. The relay treats the error as a failed subscribe and
			// re-attaches through its recovery path.
			s.discardSubscriber(sub, previous)
			cancel()
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
		// unsubscribe must not be issued until that request has finished.
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
func (s *RemoteHubSource) requestSubscribe(subCtx context.Context, client *appwire.Client, remote appwire.ThreadReadParams) <-chan subscribeOutcome {
	outcome := make(chan subscribeOutcome, 1)
	reqCtx, reqCancel := context.WithTimeout(context.WithoutCancel(subCtx), remoteHubSubscribeTimeout)
	go func() {
		defer reqCancel()
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

// remoteSubscriptionTarget is the caller-derived provisional identity this
// subscription is unsubscribed by until the subscribe answers. Both thread/read's
// relay and thread/unsubscribe resolve through threadRelayTarget, which prefers a
// bare non-empty threadId over the ref, so a caller that sent both must be
// unsubscribed by the threadId it sent — not by the translated ref's suffix,
// which would name a subscription the remote never created. settleSubscriber
// replaces it with the canonical ref the successful subscription snapshot named,
// which is what ownership and teardown ultimately compare.
func remoteSubscriptionTarget(ref appwire.Ref, threadID string) string {
	target := ref.ThreadID
	if trimmed := strings.TrimSpace(threadID); trimmed != "" {
		target = trimmed
	}
	return appwire.Ref{SourceID: remoteHubNamespace, ThreadID: target}.String()
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
// It reports whether sub is the installed routing target when it returns.
// false means a concurrent replacement displaced sub before its own subscribe
// response arrived: the caller must not start sub's pump or hand the relay sub's
// channel, because nothing will ever route to it. A re-key that finds sub
// displaced reports false; a snapshot key that already matches sub's key still
// checks that sub is the installed subscription, since a replacement for the
// same thread under the same key leaves the snapshot key equal to sub.threadID.
//
// The re-key, the canonical remote identity, and the installed check all happen
// under one remoteMu+subMu hold, so a replacement can never observe sub at its
// new key with a stale provisional ref: that interleaving is what let a retiring
// predecessor conclude the ref was unowned and unsubscribe the remote
// subscription its live replacement had just adopted.
func (s *RemoteHubSource) settleSubscriber(sub *remoteHubSubscription, snapshot appwire.ThreadReadResponse) bool {
	degenerate := snapshot.Thread.ID == "" && snapshot.Thread.Evener.Ref == ""
	canonical := strings.TrimSpace(snapshot.Thread.Evener.Ref)
	key := ""
	if !degenerate {
		key = s.snapshotRoutingKey(snapshot)
	}

	s.remoteMu.Lock()
	s.subMu.Lock()
	if s.subs[sub.threadID] != sub {
		s.subMu.Unlock()
		s.remoteMu.Unlock()
		return false
	}
	var displaced *remoteHubSubscription
	if key != "" && key != sub.threadID {
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
		return false
	}
	// Refs a retired predecessor deferred to this subscription are resolved now
	// that its own canonical identity is known: the one it adopted is its own,
	// and any it did not is handed to a still-attaching sibling or unsubscribed.
	s.releaseDeferredLocked(sub)
	if degenerate {
		// No authoritative identity to key on and nothing to fold in: the
		// caller-derived key stands and the controller's copy is left alone.
		return true
	}
	resync := *appwire.NotificationMessage(appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
		ThreadID: sub.threadID,
		Ref:      appwire.Ref{SourceID: s.id, ThreadID: sub.threadID}.String(),
	}).Notification
	sub.out <- resync
	return true
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
// cover the gap.
func (s *RemoteHubSource) discardSubscriber(sub, previous *remoteHubSubscription) {
	close(sub.pumpDone)
	s.remoteMu.Lock()
	s.subMu.Lock()
	restore := previous != nil && previous != sub && s.subscriberLiveLocked(previous)
	if s.subs[sub.threadID] == sub {
		if restore {
			s.subs[sub.threadID] = previous
		} else {
			delete(s.subs, sub.threadID)
		}
	}
	s.subMu.Unlock()
	if restore {
		forwardBufferedNotifications(sub, previous)
	}
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
func forwardBufferedNotifications(sub, previous *remoteHubSubscription) {
	if previous == nil {
		return
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
// held. A subscription is live while its pump is running and its client's
// notification stream is still being drained: once the drain has exited for
// that client, nothing will ever feed sub.in again and the pump would block
// forever, so restoring such a subscription would strand the relay.
func (s *RemoteHubSource) subscriberLiveLocked(sub *remoteHubSubscription) bool {
	if sub == nil {
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
// provisionalRef — which is what lets a retirement defer to a replacement
// instead of unsubscribing the ref that replacement is about to adopt. A ref
// that is not held but is claimed is returned so the caller can defer, not
// unsubscribe. Called with subMu held.
func (s *RemoteHubSource) refClaimLocked(sub *remoteHubSubscription, ref string) (bool, *remoteHubSubscription) {
	var claimant *remoteHubSubscription
	for _, other := range s.subs {
		if other == sub || other.client != sub.client {
			continue
		}
		if other.remoteRef == ref || slices.Contains(other.deferred, ref) {
			return true, nil
		}
		if !other.settled && other.provisionalRef != "" &&
			(other.provisionalRef == ref || other.provisionalRef == sub.provisionalRef) {
			if claimant == nil {
				claimant = other
			}
		}
	}
	return false, claimant
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
		s.routeNotification(client, notification)
	}
}

// routeNotification delivers one remote notification to the subscription for
// its (translated) thread. It observes the subscriber under the lock and sends
// outside it; a stale reference is harmless because in is never closed.
//
// A notification is only ever delivered to a subscription bound to the client
// it arrived on. A torn-down client can still have frames buffered in its
// notification channel when it closes, and the drain reading them runs
// concurrently with the relay re-subscribing against the replacement client:
// without this check those stale-generation events would be interleaved into a
// recovered feed, and a full buffer would even cancel the healthy replacement
// subscription below.
//
// The hand-off never blocks. This is the single drain goroutine shared by every
// thread on the host, and appwire tears the whole connection down once its own
// notification buffer (NotificationBufferCap) fills — so no consumer may stop
// this goroutine from reading client.Notifications(). A subscription whose in
// buffer is full has a consumer at least 2*remoteHubSubBuffer notifications
// behind, far past the scheduling jitter those buffers exist to absorb, so it
// is retired instead of waited for: its pump exits, out closes, and the
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
	s.subMu.Unlock()
	if sub == nil {
		return
	}
	select {
	case sub.in <- translated:
	default:
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
	n.Params = s.translateNestedRefs(encoded)
	return n, threadID, true
}
