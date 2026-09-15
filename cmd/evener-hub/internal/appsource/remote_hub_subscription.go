package appsource

import (
	"context"
	"encoding/json"

	"primeradiant.com/evener/appwire"
)

// remoteHubSubBuffer sizes each subscription's two channels. It matches the
// relay-side buffer LocalDaemonSource uses, so a remote subscription tolerates
// the same scheduling jitter before the relay's consumer has to be reading.
const remoteHubSubBuffer = 128

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
	// in is the drain's delivery slot. It is NEVER closed.
	in chan appwire.Notification
	// out is returned to the relay and closed ONLY by the pump.
	out chan appwire.Notification
	// pumpDone is closed by the pump on exit so a fan-out sender racing the
	// pump's own teardown can give up without blocking.
	pumpDone chan struct{}
	// cancel stops a superseded subscription when a newer one replaces it for
	// the same remote thread. The common path never needs it: the controller
	// relay dedups per relay key.
	cancel context.CancelFunc
}

// SubscribeThread attaches the controller relay to a remote thread.
//
// It issues thread/read{Ref:"local:<thread>", Subscribe:true} on the shared
// per-host client, deliberately discarding the returned snapshot: the controller
// relay always read the thread first (the non-atomic prepareRelay branch), so
// re-translating a second snapshot would be redundant. From then on every
// ref-bearing notification the remote pushes is translated and routed to this
// subscription by remote thread ID.
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
		threadID: ref.ThreadID,
		in:       make(chan appwire.Notification, remoteHubSubBuffer),
		out:      make(chan appwire.Notification, remoteHubSubBuffer),
		pumpDone: make(chan struct{}),
		cancel:   cancel,
	}
	// Register and start draining before the subscribe request goes out, so a
	// notification the remote emits immediately after attaching cannot be
	// missed.
	s.registerSubscriber(sub, client)

	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	remote.Subscribe = true
	var snapshot appwire.ThreadReadResponse
	if err := client.Request(subCtx, appwire.MethodThreadRead, remote, &snapshot); err != nil {
		s.unregisterSubscriber(sub)
		cancel()
		return nil, s.mapCallError(err)
	}

	go s.pumpSubscription(subCtx, sub)
	return sub.out, nil
}

// registerSubscriber installs sub as the subscription for its remote thread ID
// and ensures this client's notification stream is being drained. It runs under
// subMu. A previous subscription for the same thread is cancelled and replaced;
// the old pump's unregister is a no-op because it only removes itself.
func (s *RemoteHubSource) registerSubscriber(sub *remoteHubSubscription, client *appwire.Client) {
	s.subMu.Lock()
	previous := s.subs[sub.threadID]
	s.subs[sub.threadID] = sub
	s.ensureDrainLocked(client)
	s.subMu.Unlock()
	if previous != nil && previous != sub {
		previous.cancel()
	}
}

// unregisterSubscriber removes sub from the routing table, but only if it is
// still the installed subscription. A replacement can land between a pump's
// context cancellation and its deferred unregister; removing blindly would
// delete the replacement.
func (s *RemoteHubSource) unregisterSubscriber(sub *remoteHubSubscription) {
	s.subMu.Lock()
	if s.subs[sub.threadID] == sub {
		delete(s.subs, sub.threadID)
	}
	s.subMu.Unlock()
}

// pumpSubscription owns sub.out (and closes it on exit) and sub.pumpDone. It is
// the only sender on out.
func (s *RemoteHubSource) pumpSubscription(ctx context.Context, sub *remoteHubSubscription) {
	defer close(sub.pumpDone)
	defer close(sub.out)
	defer s.unregisterSubscriber(sub)
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
// buffer overflow (ErrNotificationOverflow), so a busy thread would kill every
// subscription on the host. Unrouted notifications are dropped — but they are
// still read, which is what keeps the client healthy.
func (s *RemoteHubSource) drainLoop(client *appwire.Client) {
	defer func() {
		s.subMu.Lock()
		delete(s.drains, client)
		var orphaned []*remoteHubHostSubscription
		for sub := range s.hostSubs {
			if sub.client == client {
				delete(s.hostSubs, sub)
				orphaned = append(orphaned, sub)
			}
		}
		s.subMu.Unlock()
		// Closing out here (never from the consumer's context watcher) keeps
		// drainLoop the channel's only closer as well as its only sender, so a
		// send can never race a close. The consumer sees the close and
		// re-subscribes against the next client.
		for _, sub := range orphaned {
			close(sub.out)
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
// Blocking rather than dropping is deliberate: a dropped turn/completed is
// exactly the failure this component exists to prevent.
func (s *RemoteHubSource) routeNotification(notification appwire.Notification) {
	// Host-level consumers see every notification; the admin fan-out filters
	// to the config methods it owns. Thread routing below is unchanged.
	s.publishHostNotification(notification)
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
	select {
	case sub.in <- translated:
	case <-sub.pumpDone:
	}
}

// translateNotification rewrites a remote notification's ref-bearing fields
// into the controller namespace at the JSON level. It handles every
// ref-bearing notification uniformly without a method switch and preserves
// fields it does not understand.
//
// threadId is bare and unnamespaced, so it is never rewritten; it is used as
// the routing key directly. A notification whose ref names a nested remote
// hub's source (anything but "local") is DROPPED: it is not representable in
// the controller namespace and a notification has no error channel to report
// it on.
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
			if threadID == "" {
				if ref, err := appwire.ParseRef(translated); err == nil {
					threadID = ref.ThreadID
				}
			}
		}
	}

	if raw, ok := fields["thread"]; ok {
		var thread appwire.Thread
		if err := json.Unmarshal(raw, &thread); err != nil {
			return n, "", false
		}
		translated, err := s.fromRemoteThread(thread)
		if err != nil {
			return n, "", false
		}
		encoded, err := json.Marshal(translated)
		if err != nil {
			return n, "", false
		}
		fields["thread"] = encoded
	}

	encoded, err := json.Marshal(fields)
	if err != nil {
		return n, "", false
	}
	n.Params = encoded
	return n, threadID, true
}
