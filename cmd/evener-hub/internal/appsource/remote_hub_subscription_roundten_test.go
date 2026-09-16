package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// Test 54: two controller relays that address one remote thread by different
// alias refs occupy different provisional routing keys, so admission cannot see
// the collision — only the subscribe snapshot names the canonical identity both
// aliases resolve onto. Settling the newcomer onto that identity must not
// displace the live relay that already serves it. Displacing it is the flapping
// the admission refusal exists to prevent: the incumbent's relay recovers,
// re-attaches under its own identity, and is refused in turn, so its client is
// starved until the newcomer ends. The alias attach is refused with the same
// Conflict admission produces, before it can take the canonical slot, and the
// incumbent keeps serving.
func TestRemoteHubAliasedForeignRelayRefusedWithoutDisplacingIncumbent(t *testing.T) {
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
	// provisional key is free, so admission lets it through; only settlement learns
	// the identity the two addresses share.
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

	// The incumbent was not displaced: it still owns the canonical slot, its
	// channel is still live, and its remote ref was not released.
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
	if sawRemoteUnsubscribe(t, remote, "local:stable") {
		t.Fatalf("the refused alias attach unsubscribed the incumbent's remote ref; calls = %+v", remote.calls())
	}
}

// Test 55: the settle-time refusal is for foreign relays only. A subscription
// carrying the SAME relay identity as the live one in the canonical slot is that
// relay's own aliased re-attach — its recovery after a subscription ended, or a
// reconnect through another address — and keeps the replacement semantics the
// relay's supervision depends on: it takes the canonical slot and the
// predecessor observes subscription end.
func TestRemoteHubAliasedSameRelayReattachStillReplaces(t *testing.T) {
	remote := newPushableRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.EmptyResponse{}}
	})
	source := remote.source

	incumbentCanceled := make(chan struct{})
	incumbent := &remoteHubSubscription{
		threadID:       "stable",
		provisionalRef: "local:stable",
		remoteRef:      "local:stable",
		settled:        true,
		relayIdentity:  "host:stable",
		client:         remote.client,
		cancel:         func() { close(incumbentCanceled) },
	}
	source.subs["stable"] = incumbent

	replacement := &remoteHubSubscription{
		threadID:       "current",
		provisionalRef: "local:current",
		remoteRef:      "local:current",
		relayIdentity:  "host:stable",
		client:         remote.client,
		in:             make(chan appwire.Notification, 1),
		out:            make(chan appwire.Notification, 1),
		pumpDone:       make(chan struct{}),
		cancel:         func() {},
	}
	if previous := source.installSubscriber(replacement); previous != nil {
		t.Fatalf("replacement displaced %+v, want the provisional key free", previous)
	}

	if installed, refusal := source.settleSubscriber(replacement, appwire.ThreadReadResponse{Thread: appwire.Thread{
		ID: "stable", Source: "local", Evener: appwire.EvenerThread{Ref: "local:stable"},
	}}); !installed || refusal != nil {
		t.Fatalf("settleSubscriber rejected the same relay's aliased re-attach: %v", refusal)
	}
	expectResync(t, replacement.out, "stable", "host:stable")
	if installedSubscriberIn(t, source, "stable") != replacement {
		t.Fatal("the settled re-attach is not installed under the canonical key")
	}
	select {
	case <-incumbentCanceled:
	default:
		t.Fatal("the displaced predecessor was not cancelled: its relay waits forever on an out that never closes")
	}
}

// Test 56: a subscribe whose caller gave up while its request was still in
// flight is not a live routing target. Its context is cancelled and no pump will
// ever run for it, so a failed replacement must not restore it: restoring hands
// the routing slot to a subscription nothing pumps, keeps a dead entry that
// refuses the next relay's admission with a Conflict it does not deserve, and
// forwards the failed replacement's buffered deltas into a buffer no consumer
// will ever read. The entry stays in the table only until the detached request
// answers, which is what releases the remote side it may have installed.
//
// The parked request and the failing replacement are answered concurrently, so
// the test drives its own per-request server loop rather than the serial
// scripted remote.
func TestRemoteHubCanceledPendingSubscriberIsNotRestorable(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	server := appwire.NewStreamTransport(serverConn)
	ctx := t.Context()

	parkedSeen := make(chan struct{})
	failingSeen := make(chan struct{})
	releaseFailing := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseFailing) }) }
	defer unblock()

	var readsMu sync.Mutex
	reads := 0
	go func() {
		for {
			msg, err := server.Recv(ctx)
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			req := *msg.Request
			index := -1
			if req.Method == appwire.MethodThreadRead {
				readsMu.Lock()
				index = reads
				reads++
				readsMu.Unlock()
			}
			go func() {
				switch req.Method {
				case appwire.MethodThreadRead:
					switch index {
					case 1:
						// The abandoned attach: the remote never answers, which is the
						// wedged state that keeps its local entry in the table.
						close(parkedSeen)
						<-ctx.Done()
						return
					case 2:
						// The failing replacement: parked until the delta below has
						// landed in its buffer.
						close(failingSeen)
						select {
						case <-releaseFailing:
						case <-ctx.Done():
							return
						}
						_ = server.Send(ctx, appwire.ErrorMessage(req.ID, appwire.InvalidParams("subscribe refused")))
						return
					default:
						data, _ := json.Marshal(appwire.ThreadReadResponse{Thread: appwire.Thread{
							ID: "S", Source: "local", Evener: appwire.EvenerThread{Ref: "local:S"},
						}})
						_ = server.Send(ctx, appwire.ResponseMessage(req.ID, json.RawMessage(data)))
					}
				case appwire.MethodThreadUnsubscribe:
					data, _ := json.Marshal(appwire.EmptyResponse{})
					_ = server.Send(ctx, appwire.ResponseMessage(req.ID, json.RawMessage(data)))
				}
			}()
		}
	}()

	client := appwire.NewClient(appwire.NewStreamTransport(clientConn))
	client.Start(ctx)
	t.Cleanup(func() { _ = client.Close() })

	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	// The first attach is live and installed under "S".
	out1, err := source.SubscribeThread(t.Context(), appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("first SubscribeThread: %v", err)
	}
	expectResync(t, out1, "S", "host:S")

	// A replacement attaches and is abandoned while its request is still in
	// flight, so its entry stays in the routing table until the request answers.
	abandonedCtx, abandon := context.WithCancel(t.Context())
	abandoned := make(chan error, 1)
	go func() {
		_, err := source.SubscribeThread(abandonedCtx, appwire.ThreadReadParams{Ref: "host:S"})
		abandoned <- err
	}()
	select {
	case <-parkedSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("the abandoned attach never reached the remote")
	}
	abandon()
	if err := <-abandoned; !errors.Is(err, context.Canceled) {
		t.Fatalf("abandoned SubscribeThread = %v, want the caller's cancellation", err)
	}
	pending := installedSubscriberIn(t, source, "S")
	if pending == nil {
		t.Fatal("the abandoned attach left the routing table before its request answered")
	}

	// A later replacement installs itself at the same key and fails. The entry it
	// displaced is dead — its caller is gone and its pump will never start — so it
	// must not be restored.
	failed := make(chan error, 1)
	go func() {
		_, err := source.SubscribeThread(t.Context(), appwire.ThreadReadParams{Ref: "host:S"})
		failed <- err
	}()
	select {
	case <-failingSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("the failing replacement never reached the remote")
	}
	// A delta arrives while the failing replacement owns routing.
	if err := server.Send(ctx, appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: "S", Ref: "local:S",
	})); err != nil {
		t.Fatalf("push: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		replacement := installedSubscriberIn(t, source, "S")
		if replacement != nil && replacement != pending && len(replacement.in) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the delta never reached the in-flight replacement's buffer")
		}
		time.Sleep(5 * time.Millisecond)
	}
	unblock()
	if err := <-failed; err == nil {
		t.Fatal("the failing replacement SubscribeThread succeeded despite the refused subscribe")
	}

	// The dead entry must be gone, and nothing may have been handed into the dead
	// subscription's buffer: no pump will ever read it, and the thread's next live
	// subscription re-reads the thread through its resync instead.
	if restored := installedSubscriberIn(t, source, "S"); restored != nil {
		t.Fatalf("discardSubscriber restored a canceled, never-pumped subscription: %+v", restored)
	}
	if len(pending.in) != 0 {
		t.Fatalf("the failed replacement handed %d notifications to a dead subscription's buffer", len(pending.in))
	}

	// A second relay must be able to take the thread: a dead entry that still
	// reports live refuses it with a Conflict it does not deserve.
	other, err := source.SubscribeThread(WithRelayIdentity(t.Context(), "host:other"), appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("a second relay was refused admission by a dead routing entry: %v", err)
	}
	expectResync(t, other, "S", "host:S")
	if installedSubscriberIn(t, source, "S") == nil {
		t.Fatal("the second relay's attach is not installed")
	}
}
