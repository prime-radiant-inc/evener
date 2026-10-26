package appsource

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// Test 57: a settlement refusal must release the identity the rejected attach's
// OWN request created on the remote, while leaving the incumbent's subscription
// alone. The remote keys this connection's registration by the address the
// request carried (relayDeliveryTarget on its side), so the refused alias attach
// has its own remote-side entry; recording the snapshot's canonical ref in
// remoteRef instead — the incumbent's identity — made cleanup's release a no-op,
// because remoteRefsLocked names only remoteRef and the incumbent owns the
// canonical ref (refClaimLocked calls it owned and skips it). The rejected
// registration then stayed active for the life of the connection, which is a
// remote relay with no local owner: duplicate delivery of the thread to this
// connection and a remote subscription nothing will ever retire.
func TestRemoteHubAliasedForeignRelayRefusalReleasesOwnRemoteIdentity(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadRead {
			// The remote resolves every alias of this thread onto its stable ref.
			// That canonical identity is what the snapshot names, and it is the only
			// place the aliases' collision is visible.
			return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "stable", Source: "local", Evener: appwire.EvenerThread{Ref: "local:stable"},
			}}}
		}
		return scriptedReply{result: appwire.EmptyResponse{}}
	})
	ctx := t.Context()

	// The relay watching this remote thread by its stable address serves it.
	incumbent, err := remote.source.SubscribeThread(WithRelayIdentity(ctx, "host:stable"), appwire.ThreadReadParams{Ref: "host:stable"})
	if err != nil {
		t.Fatalf("stable-ref SubscribeThread: %v", err)
	}
	expectResync(t, incumbent, "stable", "host:stable")
	incumbentSub := installedSubscriberIn(t, remote.source, "stable")
	if incumbentSub == nil {
		t.Fatal("the stable-ref attach is not installed")
	}

	// A second relay reaches the same remote thread by a volatile alias. Its
	// provisional key is free, so admission lets it through and the subscribe
	// request reaches the remote; only settlement learns the identity the two
	// addresses share.
	second, err := remote.source.SubscribeThread(WithRelayIdentity(ctx, "host:current"), appwire.ThreadReadParams{Ref: "host:current"})
	if err == nil {
		t.Fatal("a second relay displaced the live relay serving the same remote thread by another alias")
	}
	if second != nil {
		t.Fatal("the refused attach returned a notification channel")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("refusal = %T %v, want an appwire.WireError with conflict code", err, err)
	}
	if !strings.Contains(wire.Message, "host:stable") {
		t.Fatalf("refusal = %q, want it to name the identity serving the thread", wire.Message)
	}

	// The refused attach's own remote identity is the alias it addressed the
	// remote with, and that is what its cleanup has to release.
	waitForRemoteUnsubscribe(t, remote, "local:current")

	// The incumbent's canonical identity is not the refused attach's to release:
	// it still owns the remote subscription and keeps delivering.
	if sawRemoteUnsubscribe(t, remote, "local:stable") {
		t.Fatalf("the refused alias attach unsubscribed the incumbent's remote ref; calls = %+v", remote.calls())
	}
	if got := installedSubscriberIn(t, remote.source, "stable"); got != incumbentSub {
		t.Fatalf("canonical slot = %+v, want the incumbent left installed", got)
	}
	if installedSubscriberIn(t, remote.source, "current") != nil {
		t.Fatal("the refused alias attach left its provisional routing entry behind")
	}
	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "stable", Ref: "local:stable"}); err != nil {
		t.Fatalf("push: %v", err)
	}
	status := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, recvOrFatal(t, incumbent))
	if status.Ref != "host:stable" {
		t.Fatalf("incumbent received ref %q, want host:stable", status.Ref)
	}
}

// Test 58: a notification the remote emits between the install and the
// settlement re-key must reach the subscriber. The subscription is routed under
// its effective remote thread identity while it attaches — a bare caller
// threadId in preference to the ref's suffix, which is the precedence the remote
// keys this connection's subscription by (threadRelayTarget) — and only
// settlement re-keys it to the canonical identity the snapshot names. Keying the
// attach by the caller's ref suffix left the window between the two under an
// identity the remote never uses for this subscription, so a frame emitted in it
// matched no entry and was dropped: the relay never saw it, and nothing
// re-delivered it, because the resync that follows settlement only re-reads the
// thread once, at the moment it settles.
func TestRemoteHubPreSettlementNotificationRoutesUnderEffectiveIdentity(t *testing.T) {
	subscribeSeen := make(chan struct{})
	releaseSubscribe := make(chan struct{})
	var releaseOnce sync.Once
	// The parked handler must be releasable however the test exits: a Fatal
	// before the release below would otherwise leave the fake remote's loop
	// parked in its handler, and its cleanup waits for that loop to return.
	// Registered after newPushableRemote so it runs BEFORE the harness's own
	// cleanup (cleanups are LIFO) — the harness cleanup is what waits on the
	// parked loop.
	release := func() { releaseOnce.Do(func() { close(releaseSubscribe) }) }
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadRead {
			// Hold the subscribed read open so the notification below is emitted
			// while this attach is still in flight.
			close(subscribeSeen)
			<-releaseSubscribe
			return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "current", Source: "local", Evener: appwire.EvenerThread{Ref: "local:current"},
			}}}
		}
		return scriptedReply{result: appwire.EmptyResponse{}}
	})
	t.Cleanup(release)
	ctx := t.Context()

	// The caller addresses the thread by a stale alias ref plus the live thread ID
	// it currently holds. The remote resolves the bare threadId in preference to
	// the ref, so "current" is the identity this subscription is keyed by until it
	// settles.
	out := make(chan (<-chan appwire.Notification), 1)
	failed := make(chan error, 1)
	go func() {
		channel, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:stale", ThreadID: "current"})
		if err != nil {
			failed <- err
			return
		}
		out <- channel
	}()
	select {
	case <-subscribeSeen:
	case err := <-failed:
		t.Fatalf("SubscribeThread: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("the subscribe request never reached the remote")
	}

	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "current", Ref: "local:current"}); err != nil {
		t.Fatalf("push: %v", err)
	}

	// The frame is routed while the attach is still in flight: it must land in the
	// in-flight subscription's buffer, under whichever key that subscription
	// currently holds.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if sub := inFlightSubscriber(t, remote.source); sub != nil && len(sub.in) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the notification emitted during the attach window was never routed to the in-flight subscription")
		}
		time.Sleep(5 * time.Millisecond)
	}

	release()
	var channel <-chan appwire.Notification
	select {
	case channel = <-out:
	case err := <-failed:
		t.Fatalf("SubscribeThread: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("SubscribeThread did not return after the remote answered")
	}

	// The attach settled onto the identity the snapshot named, and the frame
	// buffered during the window is delivered to the subscriber — the resync
	// first, then the delta.
	expectResync(t, channel, "current", "host:current")
	status := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, recvOrFatal(t, channel))
	if status.Ref != "host:current" || status.ThreadID != "current" {
		t.Fatalf("delivered frame = ref %q threadId %q, want the delta emitted during the attach window", status.Ref, status.ThreadID)
	}
	if got := installedSubscriberIn(t, remote.source, "current"); got == nil {
		t.Fatal("the settled subscription is not installed under the snapshot's canonical identity")
	}
}

// inFlightSubscriber returns the subscription currently published in the routing
// table, whatever key it holds, so a test can observe an attach that has not
// settled yet without assuming which identity it is keyed under.
func inFlightSubscriber(t *testing.T, source *RemoteHubSource) *remoteHubSubscription {
	t.Helper()
	source.remoteMu.Lock()
	defer source.remoteMu.Unlock()
	source.subMu.Lock()
	defer source.subMu.Unlock()
	for _, sub := range source.subs {
		return sub
	}
	return nil
}
