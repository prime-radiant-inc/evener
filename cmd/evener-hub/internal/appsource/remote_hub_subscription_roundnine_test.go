package appsource

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// countRemoteSubscribes counts the subscribed thread/read requests the source has
// forwarded, so a test can assert an attach never reached the remote.
func countRemoteSubscribes(t *testing.T, remote *pushableRemote) int {
	t.Helper()
	seen := 0
	for _, call := range remote.calls() {
		if call.method != appwire.MethodThreadRead {
			continue
		}
		var params appwire.ThreadReadParams
		if err := json.Unmarshal(call.params, &params); err != nil {
			t.Fatalf("decode forwarded thread/read params: %v", err)
		}
		if params.Subscribe {
			seen++
		}
	}
	return seen
}

// Test 48: a retiring predecessor must defer to an attaching replacement even
// when the two were addressed by different volatile thread IDs. The remote
// resolves several aliases of one thread — a current root ID before and after an
// identity replacement, or a current ID and the stable ref — onto one
// subscription identity, so their provisional targets differ while the canonical
// ref they resolve to is the same one. Recognizing a replacement only by its
// caller addressing let the predecessor unsubscribe that canonical ref, and the
// replacement was then open, silent, and never recovered: its own out never
// closes, so nothing triggers a re-attach.
func TestRemoteHubRetireDefersToAliasedStillAttachingReplacement(t *testing.T) {
	remote := newPushableRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.EmptyResponse{}}
	})
	source := remote.source

	// The predecessor settled on the canonical identity while it was still being
	// addressed by the volatile thread ID the session had before the replacement
	// moved it.
	predecessor := &remoteHubSubscription{
		threadID:       "stable",
		provisionalRef: "local:current1",
		remoteRef:      "local:stable",
		settled:        true,
		client:         remote.client,
		cancel:         func() {},
	}
	source.subs["stable"] = predecessor

	// The replacement for the same thread was addressed by the volatile thread ID
	// the identity replacement moved to, so its provisional target differs from
	// both the predecessor's and the canonical ref it will adopt.
	replacement := &remoteHubSubscription{
		threadID:       "current2",
		provisionalRef: "local:current2",
		remoteRef:      "local:current2",
		client:         remote.client,
		in:             make(chan appwire.Notification, 1),
		out:            make(chan appwire.Notification, 1),
		pumpDone:       make(chan struct{}),
		cancel:         func() {},
	}
	if previous := source.installSubscriber(replacement); previous != nil {
		t.Fatalf("replacement displaced %+v, want the provisional key free", previous)
	}

	// The predecessor retires while the replacement is still attaching. The
	// replacement's caller addressing is a different alias of the same thread, so
	// the canonical ref must be deferred to it rather than unsubscribed.
	source.retireSubscription(predecessor)
	if sawRemoteUnsubscribe(t, remote, "local:stable") {
		t.Fatalf("predecessor unsubscribed the canonical ref its aliased replacement will adopt; calls = %+v", remote.calls())
	}
	if _, ok := source.subs["stable"]; ok {
		t.Fatal("the retiring predecessor stayed in the routing table")
	}

	// When the replacement settles it adopts local:stable, so the deferred ref is
	// its own and must not be unsubscribed.
	if !source.settleSubscriber(replacement, appwire.ThreadReadResponse{Thread: appwire.Thread{
		ID: "stable", Source: "local", Evener: appwire.EvenerThread{Ref: "local:stable"},
	}}) {
		t.Fatal("settleSubscriber rejected the installed replacement")
	}
	expectResync(t, replacement.out, "stable", "host:stable")
	if sawRemoteUnsubscribe(t, remote, "local:stable") {
		t.Fatalf("settlement unsubscribed the ref the aliased replacement adopted; calls = %+v", remote.calls())
	}
	if installedSubscriberIn(t, source, "stable") != replacement {
		t.Fatal("the settled replacement is not installed under the canonical key")
	}
}

// Test 49: the aliased deferral of Test 48 must not leak the ref. A sibling that
// settles onto some other thread never adopts the deferred canonical ref, so
// settlement releases it — otherwise the remote would keep forwarding a thread
// nothing local watches for the rest of the connection's life.
func TestRemoteHubAliasedDeferralReleasedWhenNotAdopted(t *testing.T) {
	remote := newPushableRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.EmptyResponse{}}
	})
	source := remote.source

	predecessor := &remoteHubSubscription{
		threadID:       "stable",
		provisionalRef: "local:current1",
		remoteRef:      "local:stable",
		settled:        true,
		client:         remote.client,
		cancel:         func() {},
	}
	source.subs["stable"] = predecessor

	// The attaching sibling was addressed by another volatile ID but turns out to
	// belong to a different thread: its snapshot names local:other.
	sibling := &remoteHubSubscription{
		threadID:       "current2",
		provisionalRef: "local:current2",
		remoteRef:      "local:current2",
		client:         remote.client,
		in:             make(chan appwire.Notification, 1),
		out:            make(chan appwire.Notification, 1),
		pumpDone:       make(chan struct{}),
		cancel:         func() {},
	}
	source.installSubscriber(sibling)
	source.retireSubscription(predecessor)
	if sawRemoteUnsubscribe(t, remote, "local:stable") {
		t.Fatal("predecessor unsubscribed a ref its attaching sibling may still adopt")
	}

	if !source.settleSubscriber(sibling, appwire.ThreadReadResponse{Thread: appwire.Thread{
		ID: "other", Source: "local", Evener: appwire.EvenerThread{Ref: "local:other"},
	}}) {
		t.Fatal("settleSubscriber rejected the installed sibling")
	}
	if !sawRemoteUnsubscribe(t, remote, "local:stable") {
		t.Fatalf("the deferred canonical ref leaked; calls = %+v", remote.calls())
	}
}

// Test 50: the failed-replacement hand-off and the shared drain's fan-out send
// share one subMu critical section. routeNotification captures the routing target
// and inserts into it under subMu; discardSubscriber swaps the table back to the
// restored previous and forwards the replacement's buffer under the same lock. A
// lock released in between let the drain insert a captured delta into the
// replaced subscription after its buffer had already been forwarded, where no
// pump would ever read it and no resync would cover it (previous stayed live, so
// its relay never re-read the thread).
func TestRemoteHubFailedReplacementHandoffCannotLoseConcurrentRoute(t *testing.T) {
	client := &appwire.Client{}
	source := NewRemoteHubSource("host", nil, nil)
	source.drains[client] = struct{}{}

	previous := &remoteHubSubscription{
		threadID: "S",
		client:   client,
		in:       make(chan appwire.Notification, 4),
		pumpDone: make(chan struct{}),
		cancel:   func() {},
	}
	sub := &remoteHubSubscription{
		threadID: "S",
		client:   client,
		in:       make(chan appwire.Notification, 4),
		pumpDone: make(chan struct{}),
		cancel:   func() {},
	}
	source.subs["S"] = sub

	entered, release := make(chan struct{}), make(chan struct{})
	sub.onBufferedHandoff = func() {
		close(entered)
		<-release
	}

	discarded := make(chan struct{})
	go func() {
		defer close(discarded)
		source.discardSubscriber(sub, previous)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the failed replacement's hand-off never ran")
	}

	// The table has been swapped back to previous and the hand-off is still to
	// run, so a route cannot be between its capture and its send.
	if source.subMu.TryLock() {
		source.subMu.Unlock()
		t.Fatal("the buffered hand-off released subMu: a concurrent route can insert after it")
	}

	// The delta the drain captured while the replacement owned routing must reach
	// the restored previous, not the replaced subscription's dead buffer.
	routed := make(chan struct{})
	go func() {
		defer close(routed)
		source.routeNotification(client, appwire.Notification{
			Method: appwire.NotifyThreadStatusChanged,
			Params: json.RawMessage(`{"threadId":"S","ref":"local:S"}`),
		})
	}()
	close(release)
	<-discarded
	<-routed

	if len(sub.in) != 0 {
		t.Fatalf("the delta landed in the replaced subscription's buffer (%d buffered): no pump will ever read it", len(sub.in))
	}
	if len(previous.in) != 1 {
		t.Fatalf("the restored previous buffered %d notifications, want the routed delta", len(previous.in))
	}
	if installedSubscriberIn(t, source, "S") != previous {
		t.Fatal("the discarded replacement was not swapped back to the previous subscription")
	}
}

// Test 51: a live subscription attached for one controller relay must not be
// displaced by a second relay that reached the same remote thread by another
// address. Remote subscriptions live per (connection, thread), so the two would
// otherwise trade the one subscription for as long as both are watched — the
// remote resolves a current root ID and the stable ref onto one identity — with a
// remote round trip per swap. The second attach is refused before any request
// reaches the remote, and the incumbent keeps serving.
func TestRemoteHubSubscribeThreadRefusesSecondRelayForSameThread(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadRead {
			return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "S", Source: "local", Evener: appwire.EvenerThread{Ref: "local:S"},
			}}}
		}
		return scriptedReply{result: appwire.EmptyResponse{}}
	})
	ctx := t.Context()

	incumbent, err := remote.source.SubscribeThread(WithRelayIdentity(ctx, "host:current"), appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("first SubscribeThread: %v", err)
	}
	expectResync(t, incumbent, "S", "host:S")
	subscribes := countRemoteSubscribes(t, remote)

	second, err := remote.source.SubscribeThread(WithRelayIdentity(ctx, "host:stable"), appwire.ThreadReadParams{Ref: "host:S"})
	if err == nil {
		t.Fatal("a second relay attached to a remote thread a live relay already serves")
	}
	if second != nil {
		t.Fatal("the refused attach returned a notification channel")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("refusal = %T %v, want an appwire.WireError with conflict code", err, err)
	}
	if !strings.Contains(wire.Message, "host:S") {
		t.Fatalf("refusal = %q, want it to name the identity serving the thread", wire.Message)
	}
	if got := countRemoteSubscribes(t, remote); got != subscribes {
		t.Fatalf("the refused attach sent %d subscribed remote reads, want none", got-subscribes)
	}
	if installedSubscriberIn(t, remote.source, "S") == nil {
		t.Fatal("the refused attach left the thread unserved")
	}

	// The incumbent keeps serving the thread it owns.
	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "S", Ref: "local:S"}); err != nil {
		t.Fatalf("push: %v", err)
	}
	status := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, recvOrFatal(t, incumbent))
	if status.Ref != "host:S" {
		t.Fatalf("incumbent received ref %q, want host:S", status.Ref)
	}
}

// Test 52: the control for Test 51 — the SAME relay re-attaching its thread is
// still a replacement. Its own recovery after a subscription ended, or a client
// reconnecting through it, must keep the displacement semantics the relay's
// supervision depends on.
func TestRemoteHubSubscribeThreadAdmitsSameRelayReattach(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadRead {
			return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "S", Source: "local", Evener: appwire.EvenerThread{Ref: "local:S"},
			}}}
		}
		return scriptedReply{result: appwire.EmptyResponse{}}
	})
	ctx := t.Context()

	first, err := remote.source.SubscribeThread(WithRelayIdentity(ctx, "host:S"), appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("first SubscribeThread: %v", err)
	}
	expectResync(t, first, "S", "host:S")

	second, err := remote.source.SubscribeThread(WithRelayIdentity(ctx, "host:S"), appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("the relay's own re-attach was refused: %v", err)
	}
	expectResync(t, second, "S", "host:S")

	// The displaced subscription observes subscription end so the relay does not
	// hang on a pump that will never deliver again.
	select {
	case n, ok := <-first:
		if ok {
			t.Fatalf("the displaced subscription delivered %+v instead of closing", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the displaced subscription's out never closed")
	}
}

// Test 53: the refusal of Test 51 is not permanent. Once the incumbent relay's
// subscription ends, a second relay that addressed the same thread attaches
// normally — that is the recovery the refused relay's own backoff retries lead
// to.
func TestRemoteHubSubscribeThreadSecondRelayAttachesAfterIncumbentEnds(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadRead {
			return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "S", Source: "local", Evener: appwire.EvenerThread{Ref: "local:S"},
			}}}
		}
		return scriptedReply{result: appwire.EmptyResponse{}}
	})
	ctx := t.Context()

	if _, err := remote.source.SubscribeThread(WithRelayIdentity(ctx, "host:current"), appwire.ThreadReadParams{Ref: "host:S"}); err != nil {
		t.Fatalf("first SubscribeThread: %v", err)
	}
	incumbent := installedSubscriberIn(t, remote.source, "S")
	if incumbent == nil {
		t.Fatal("the first attach is not installed")
	}

	// The incumbent relay's subscription ends (its relay went away, its client
	// stopped reading, the connection dropped): its pump exits and retires it.
	incumbent.cancel()
	remote.source.retireSubscription(incumbent)
	if installedSubscriberIn(t, remote.source, "S") != nil {
		t.Fatal("the ended incumbent is still installed")
	}

	second, err := remote.source.SubscribeThread(WithRelayIdentity(ctx, "host:stable"), appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("the second relay was still refused after the incumbent ended: %v", err)
	}
	expectResync(t, second, "S", "host:S")
}
