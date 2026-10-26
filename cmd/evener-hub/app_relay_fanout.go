package hub

import (
	"context"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
)

func (r *hubRelay) startAcknowledgedFanout(
	handle *hubRelayHandle,
	deliveries <-chan appsource.RelayDelivery,
) {
	go func() {
		ticker := time.NewTicker(r.idleInterval)
		defer ticker.Stop()
		defer r.retireRelayHandle(handle)
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
		// Test-only: report the retained count after every mutation.
		recordPending := func() {
			if observe := observeHubRelayPending.Load(); observe != nil {
				(*observe)(len(pendingDeliveries))
			}
		}
		// routeChangeWait is the wake-up a parked frame waits on.
		// r.signalRouteChangeLocked closes handle.routeChanged and installs a
		// fresh one, so this must be taken before the routes are read that
		// decide to park: a capture taken afterwards can be the replacement
		// channel, and the frame then sleeps through the very publication it
		// is waiting for until unrelated traffic on the same key wakes it.
		// A capture that is already closed by the time the loop selects on
		// it just costs one extra resolution pass.
		var routeChangeWait <-chan struct{}
		captureRouteChange := func() {
			r.mu.Lock()
			routeChangeWait = handle.routeChanged
			r.mu.Unlock()
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
			r.mu.Lock()
			defer r.mu.Unlock()
			var targets []relayTargetState
			if routing == relayNotificationTargeted {
				state := handle.routes[routingKey]
				if state != nil && !state.retiring && handle.relayKeys[state.relayKey] == state && r.relayed[state.relayKey] == handle {
					targets = append(targets, targetState(routingKey, state))
				}
			} else {
				targets = make([]relayTargetState, 0, len(handle.relayKeys))
				for currentKey, state := range handle.relayKeys {
					if !state.retiring && r.relayed[currentKey] == handle {
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
					ownsFork := applyHubForkCapability(r.cfg, target.thread).Evener.Capabilities.ForkFromTurn
					notification = stampClosedThreadCapabilities(notification, ownsFork)
					notification = stampForkCapability(notification, ownsFork)
					notification = stampPageBeforeCapability(notification, hubPagesBefore(r.cfg, target.thread.Evener.Ref))
				}
			}
			if r.cfg.RelayHooks.BeforeCanonicalPublish != nil {
				r.cfg.RelayHooks.BeforeCanonicalPublish(target.relayKey, notification)
			}
			r.mu.Lock()
			current := !handle.stopping && !target.state.retiring &&
				handle.relayKeys[target.relayKey] == target.state &&
				r.relayed[target.relayKey] == handle
			if current && target.routingKey != "" {
				current = handle.routes[target.routingKey] == target.state
			}
			if current {
				if target.state.publications == 0 {
					target.state.publicationDone = make(chan struct{})
				}
				target.state.publications++
			}
			r.mu.Unlock()
			if !current {
				return
			}
			if r.cfg.RelayHooks.AfterCanonicalPublishEntry != nil {
				r.cfg.RelayHooks.AfterCanonicalPublishEntry(target.relayKey, notification)
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
			if fastRelease, ok := tryLockDeletionTarget(r.cfg, target.ref, target.threadID); ok {
				release = fastRelease
			} else {
				guardCtx, cancelGuard := context.WithTimeout(context.Background(), relayPublicationGuardTimeout)
				release, lockErr = lockDeletionTarget(guardCtx, r.cfg, target.ref, target.threadID)
				cancelGuard()
			}
			if lockErr == nil {
				if r.backgroundFenceError(target.ref, target.threadID) == nil {
					r.server.Broadcast(target.relayKey, notification.Method, notification.Params)
				}
				release()
			} else {
				r.server.Broadcast(target.relayKey, appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
					ThreadID: target.threadID,
					Ref:      target.ref,
				})
			}
			var closeHandle bool
			r.mu.Lock()
			target.state.publications--
			if target.state.publications == 0 {
				close(target.state.publicationDone)
				target.state.publicationDone = nil
				if (target.state.removeOnDrain && target.state.retireOwner == nil) ||
					(target.state.stopRequested && target.state.commands == 0) {
					r.removeRelayKeyStateLocked(target.handle, target.state)
				}
			}
			closeHandle = r.maybeFinishHandleLocked(target.handle)
			r.mu.Unlock()
			if closeHandle {
				r.closeRelayHandle(target.handle)
			}
		}
		publishDelivery := func(delivery appsource.RelayDelivery, targets []relayTargetState) {
			for _, target := range targets {
				publishTarget(delivery, target)
			}
			acknowledge(delivery)
		}
		routesPending := func() bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return handle.commandOwners != 0 && !handle.stopping
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
				if r.server.SubscriberCount(target.relayKey) == 0 {
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
			recordPending()
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
					recordPending()
				}
				return
			}
			routingKey, routing := relayNotificationRoutingKey(delivery.Notification, handle.canonical.SourceID)
			// Test-only: observe each delivery the fan-out reads, before
			// routing. Daemon-gone deliveries return above and are not seen.
			if observe := observeHubRelayDeliveryRead.Load(); observe != nil {
				(*observe)(routingKey)
			}
			if routing == relayNotificationTargeted && hasPendingTarget(routingKey) {
				pendingDeliveries = append(pendingDeliveries, pendingRelayDelivery{delivery: delivery, routingKey: routingKey, routing: routing})
				recordPending()
				if delivery.Proceed != nil {
					delivery.Proceed()
				}
				processPending()
				return
			}
			targets, pending := lookupTargets(routingKey, routing)
			if pending {
				pendingDeliveries = append(pendingDeliveries, pendingRelayDelivery{delivery: delivery, routingKey: routingKey, routing: routing})
				recordPending()
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
			r.mu.Lock()
			active := r.canonical[handle.canonical] == handle && !handle.stopping
			candidates := make([]idleCandidate, 0, len(handle.relayKeys))
			if active {
				for relayKey, state := range handle.relayKeys {
					if r.relayed[relayKey] == handle && state.commands == 0 && !state.retiring {
						candidates = append(candidates, idleCandidate{relayKey: relayKey, state: state, commands: state.commands})
					}
				}
				if len(handle.relayKeys) == 0 && handle.commandOwners == 0 {
					handle.stopping = true
					active = false
				}
			}
			r.mu.Unlock()
			if !active {
				return true
			}
			idleCandidates := candidates[:0]
			for _, candidate := range candidates {
				if r.server.SubscriberCount(candidate.relayKey) == 0 {
					idleCandidates = append(idleCandidates, candidate)
				}
			}
			if len(idleCandidates) == 0 {
				return false
			}
			if r.cfg.RelayHooks.IdleExit != nil {
				r.cfg.RelayHooks.IdleExit(handle.canonical.ThreadID)
			}
			revalidated := idleCandidates[:0]
			for _, candidate := range idleCandidates {
				if r.server.SubscriberCount(candidate.relayKey) == 0 {
					revalidated = append(revalidated, candidate)
				}
			}
			if len(revalidated) == 0 {
				return false
			}
			r.mu.Lock()
			removed := 0
			if r.canonical[handle.canonical] == handle && !handle.stopping {
				for _, candidate := range revalidated {
					if r.relayed[candidate.relayKey] == handle &&
						handle.relayKeys[candidate.relayKey] == candidate.state &&
						candidate.state.commands == candidate.commands {
						candidate.state.removeOnDrain = true
						if r.removeRelayKeyStateLocked(handle, candidate.state) {
							removed++
						}
					}
				}
			}
			retired := r.canonical[handle.canonical] == handle && len(handle.relayKeys) == 0 && handle.commandOwners == 0
			if retired {
				handle.stopping = true
			}
			r.mu.Unlock()
			for range removed {
				if r.cfg.RelayHooks.AfterIdleDelete != nil {
					r.cfg.RelayHooks.AfterIdleDelete(handle.canonical.ThreadID)
				}
			}
			return retired
		}
		for {
			var deliveryInput <-chan appsource.RelayDelivery
			if len(pendingDeliveries) < hubRelayPendingDeliveryLimit {
				deliveryInput = deliveries
			} else if observe := observeHubRelayPendingBackpressure.Load(); observe != nil {
				// Test-only: the bound has stopped the delivery input.
				(*observe)()
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
