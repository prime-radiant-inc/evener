package hub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func newHubRelayFunctions(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry) hubRelayFunctions {
	logf := hubLogfFor(cfg)
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
	r := newHubRelay()
	startAcknowledgedFanout := func(
		handle *hubRelayHandle,
		deliveries <-chan appsource.RelayDelivery,
	) {
		go func() {
			ticker := time.NewTicker(relayIdleInterval)
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
						ownsFork := applyHubForkCapability(cfg, target.thread).Evener.Capabilities.ForkFromTurn
						notification = stampClosedThreadCapabilities(notification, ownsFork)
						notification = stampForkCapability(notification, ownsFork)
						notification = stampPageBeforeCapability(notification, hubPagesBefore(cfg, target.thread.Evener.Ref))
					}
				}
				if cfg.RelayHooks.BeforeCanonicalPublish != nil {
					cfg.RelayHooks.BeforeCanonicalPublish(target.relayKey, notification)
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
	acquireRelaySession := func(
		ctx context.Context,
		source appsource.RelaySessionSource,
		base appsource.Source,
		params appwire.ThreadReadParams,
		canonicalRef appwire.Ref,
	) (*hubRelayHandle, *relayKeyState, *appsource.RelayRoutePublication, func(), error) {
		relayKey, _, err := relayTarget(base, params)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		stoppedRelayKeyLocked := func(preferred *hubRelayHandle) <-chan struct{} {
			owners := r.pending[relayKey]
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
			if currentHandle := r.relayed[relayKey]; currentHandle != nil {
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
			owners := r.pending[relayKey]
			state := handle.relayKeys[relayKey]
			if r.relayed[relayKey] != handle || state == nil {
				state = handle.pendingKeys[relayKey]
				if state != nil && (owners[state] != handle || state.generation != r.generations[relayKey]) {
					state = nil
				}
			}
			if state == nil {
				r.generations[relayKey]++
				state = &relayKeyState{
					relayKey:     relayKey,
					generation:   r.generations[relayKey],
					argsByCallID: make(map[string]string),
					routingKeys:  make(map[string]struct{}),
					done:         make(chan struct{}),
				}
				r.registerPendingStateLocked(handle, state)
			}
			state.commands++
			handle.commandOwners++
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
		commandFunctions := func(handle *hubRelayHandle, state *relayKeyState) (*appsource.RelayRoutePublication, func()) {
			routesPending := true
			discovering := false
			begin := func() {
				r.mu.Lock()
				defer r.mu.Unlock()
				if routesPending && !discovering {
					discovering = true
					handle.pendingRoutes++
				}
			}
			resolveRoutesLocked := func() {
				if !routesPending {
					return
				}
				routesPending = false
				if discovering {
					handle.pendingRoutes--
				}
				r.signalRouteChangeLocked(handle)
			}
			publish := func(publicationCtx context.Context, thread appwire.Thread) error {
				var retirementHandle *hubRelayHandle
				var retirementState *relayKeyState
				restoreRetirementLocked := func() {
					if retirementState != nil && retirementState.retireOwner == state &&
						retirementHandle != nil && r.relayed[relayKey] == retirementHandle &&
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
						r.mu.Lock()
						if routesPending {
							if r.pending[relayKey][state] == handle {
								r.unregisterPendingStateLocked(handle, state)
								close(state.done)
							}
							restoreRetirementLocked()
							resolveRoutesLocked()
						}
						r.mu.Unlock()
						panic(recovered)
					}
				}()
				// Test-only: hold authoritative publication before any route
				// binding or resolveRoutesLocked decrement of pendingRoutes.
				if observe := observeHubRelayRoutePublication.Load(); observe != nil {
					(*observe)()
				}
				for {
					if err := publicationCtx.Err(); err != nil {
						r.mu.Lock()
						if routesPending {
							if r.pending[relayKey][state] == handle {
								r.unregisterPendingStateLocked(handle, state)
								close(state.done)
							}
							restoreRetirementLocked()
							resolveRoutesLocked()
						}
						r.mu.Unlock()
						return err
					}
					var drain <-chan struct{}
					var closeHandles []*hubRelayHandle
					r.mu.Lock()
					if !routesPending {
						r.mu.Unlock()
						return nil
					}
					current := r.relayed[relayKey] == handle && handle.relayKeys[relayKey] == state
					pending := r.pending[relayKey][state] == handle
					eligible := !handle.stopping && !state.stopRequested && !state.retiring &&
						(current || (pending && state.generation == r.generations[relayKey]))
					if !eligible {
						restoreRetirementLocked()
					}
					if eligible && pending {
						if previous := r.relayed[relayKey]; previous != nil && previous != handle {
							retirementHandle = previous
							retirementState = previous.relayKeys[relayKey]
							if retirementState != nil {
								retirementState.retiring = true
								retirementState.removeOnDrain = true
								retirementState.retireOwner = state
								if retirementState.publications != 0 {
									drain = retirementState.publicationDone
								} else {
									r.removeRelayKeyStateLocked(previous, retirementState)
									if r.maybeFinishHandleLocked(previous) {
										closeHandles = append(closeHandles, previous)
									}
								}
							}
						}
						if drain == nil {
							r.unregisterPendingStateLocked(handle, state)
						}
					}
					if drain != nil {
						r.mu.Unlock()
						for _, closeHandle := range closeHandles {
							r.closeRelayHandle(closeHandle)
						}
						if observeHubRelayWait != nil {
							observeHubRelayWait()
						}
						select {
						case <-drain:
							continue
						case <-publicationCtx.Done():
							r.mu.Lock()
							if routesPending {
								if r.pending[relayKey][state] == handle {
									r.unregisterPendingStateLocked(handle, state)
									close(state.done)
								}
								restoreRetirementLocked()
								resolveRoutesLocked()
							}
							r.mu.Unlock()
							return publicationCtx.Err()
						}
					}
					if eligible {
						r.removeStateRoutesLocked(handle, state)
						state.thread = thread
						r.bindStateRouteLocked(handle, relayKey, state)
						sourceID := thread.Source
						if sourceID == "" {
							sourceID = handle.canonical.SourceID
						}
						if thread.ID != "" {
							r.bindStateRouteLocked(handle, sourceID+":"+thread.ID, state)
						}
						r.bindStateRouteLocked(handle, thread.Evener.Ref, state)
						handle.relayKeys[relayKey] = state
						// Route identity and downstream ownership publish in one
						// r.mu critical section, after the displaced generation's
						// already-entered publications have drained.
						r.relayed[relayKey] = handle
					} else if pending && !state.stopRequested && !handle.stopping {
						r.unregisterPendingStateLocked(handle, state)
						close(state.done)
					}
					// Unbound ordinary deliveries can now be resolved without
					// waiting on the read's own acknowledgement barrier. The
					// command owner still retains departure recovery until the
					// downstream subscription has been installed.
					resolveRoutesLocked()
					r.mu.Unlock()
					for _, closeHandle := range closeHandles {
						r.closeRelayHandle(closeHandle)
					}
					return nil
				}
			}
			release := func() {
				var closeHandle bool
				r.mu.Lock()
				if state.commands > 0 && handle.commandOwners > 0 {
					state.commands--
					handle.commandOwners--
				}
				if routesPending {
					resolveRoutesLocked()
				} else {
					r.signalRouteChangeLocked(handle)
				}
				if state.commands == 0 && r.pending[relayKey][state] == handle {
					r.unregisterPendingStateLocked(handle, state)
					close(state.done)
				}
				if state.stopRequested && state.commands == 0 && state.publications == 0 {
					r.removeStoppedStateLocked(handle, state)
				}
				closeHandle = r.maybeFinishHandleLocked(handle)
				r.mu.Unlock()
				if closeHandle {
					r.closeRelayHandle(handle)
				}
			}
			return &appsource.RelayRoutePublication{Begin: begin, Publish: publish}, release
		}
		for {
			r.mu.Lock()
			existing := r.canonical[canonicalRef]
			if stopped := stoppedRelayKeyLocked(existing); stopped != nil {
				r.mu.Unlock()
				if err := waitForStoppedGeneration(stopped); err != nil {
					return nil, nil, nil, nil, err
				}
				continue
			}
			if existing != nil {
				ready := existing.ready
				r.mu.Unlock()
				select {
				case <-ready:
				case <-ctx.Done():
					return nil, nil, nil, nil, ctx.Err()
				}
				r.mu.Lock()
				if r.canonical[canonicalRef] != existing || existing.err != nil {
					err := existing.err
					r.mu.Unlock()
					if err != nil {
						return nil, nil, nil, nil, err
					}
					continue
				}
				state, stopped := beginRelayKeyLocked(existing)
				r.mu.Unlock()
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
			r.canonical[canonicalRef] = handle
			r.mu.Unlock()

			lease, acquireErr := source.AcquireRelaySession(canonicalRef)
			var deliveries <-chan appsource.RelayDelivery
			if acquireErr == nil && lease == nil {
				acquireErr = appwire.SessionUnavailable("source returned no RelaySession lease")
			}
			if acquireErr == nil {
				r.mu.Lock()
				active := r.canonical[canonicalRef] == handle && !handle.stopping
				if active {
					handle.lease = lease
				}
				r.mu.Unlock()
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
			r.mu.Lock()
			handle.initializing = false
			if acquireErr != nil || r.canonical[canonicalRef] != handle || handle.stopping {
				if acquireErr == nil {
					acquireErr = context.Canceled
				}
				r.removeRelayHandleLocked(handle)
				r.finishHandleLocked(handle, acquireErr)
				r.mu.Unlock()
				r.closeRelayHandle(handle)
				return nil, nil, nil, nil, acquireErr
			}
			state, stopped := beginRelayKeyLocked(handle)
			if stopped != nil {
				delete(r.canonical, canonicalRef)
				r.finishHandleLocked(handle, context.Canceled)
				r.mu.Unlock()
				r.closeRelayHandle(handle)
				if err := waitForStoppedGeneration(stopped); err != nil {
					return nil, nil, nil, nil, err
				}
				continue
			}
			handle.established = true
			r.finishHandleLocked(handle, nil)
			r.mu.Unlock()
			startAcknowledgedFanout(handle, deliveries)
			publish, release := commandFunctions(handle, state)
			return handle, state, publish, release, nil
		}
	}
	readThread := func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams) (*hubThreadReadResult, error) {
		needsRelay := params.Subscribe || relayOnThreadRead(source)
		relaySource, atomic := source.(appsource.RelaySessionSource)
		if !atomic || !needsRelay {
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
		if err := publish.Publish(ctx, result.Response.Thread); err != nil {
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
	prepareRelayOnce := func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
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
	prepareRelay := func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appserver.RetrySubscription(ctx, func(ctx context.Context) (appwire.ThreadReadResponse, error) {
			return prepareRelayOnce(ctx, source, params)
		})
	}
	startRelayOnce := func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams, thread appwire.Thread) error {
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
			r.mu.Lock()
			existing := r.relayed[relayKey]
			if existing == nil {
				relayCtx, cancelRelay = context.WithCancel(context.WithoutCancel(ctx))
				// relayCtx keeps the starting request's values but not its
				// cancellation, since the relay outlives that request. The
				// server that serves the relay owns it instead, from the first
				// subscribe on: its Shutdown ends the relay rather than leaving
				// it to an idle tick, which never comes while a subscriber stays
				// registered. The hook is released once the relay itself ends.
				stopOnServerShutdown := context.AfterFunc(server.Lifetime(), cancelRelay)
				context.AfterFunc(relayCtx, func() { stopOnServerShutdown() })
				// Label every attach this relay issues with its own key. A source
				// that keys its subscriptions by remote thread identity uses it to
				// tell this relay re-attaching from a second relay that reached the
				// same thread by another address (appsource.WithRelayIdentity).
				relayCtx = appsource.WithRelayIdentity(relayCtx, relayKey)
				relayHandle = &hubRelayHandle{ready: make(chan struct{}), cancel: cancelRelay}
				r.relayed[relayKey] = relayHandle
				stopInitialCancellation = context.AfterFunc(ctx, func() {
					var cancel context.CancelFunc
					r.mu.Lock()
					if r.relayed[relayKey] == relayHandle && !relayHandle.established {
						delete(r.relayed, relayKey)
						relayHandle.err = ctx.Err()
						cancel = relayHandle.cancel
					}
					r.mu.Unlock()
					if cancel != nil {
						cancel()
					}
				})
				r.mu.Unlock()
				if cfg.RelayHooks.AfterPlaceholder != nil {
					cfg.RelayHooks.AfterPlaceholder(threadID)
				}
				break
			}
			ready := existing.ready
			r.mu.Unlock()
			if observeHubRelayWait != nil {
				observeHubRelayWait()
			}

			select {
			case <-ready:
			case <-ctx.Done():
				return ctx.Err()
			}

			registerExisting := func() (bool, error) {
				r.mu.Lock()
				defer r.mu.Unlock()
				active := r.relayed[relayKey] == existing
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

		r.mu.Lock()
		active := r.relayed[relayKey] == relayHandle && relayCtx.Err() == nil
		err := relayHandle.err
		if !active {
			if err == nil {
				err = context.Canceled
			}
			r.finishHandleLocked(relayHandle, err)
			r.mu.Unlock()
			cancelRelay()
			return err
		}
		r.mu.Unlock()
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
			r.mu.Lock()
			if r.relayed[relayKey] == relayHandle {
				delete(r.relayed, relayKey)
			}
			if relayHandle.err != nil {
				err = relayHandle.err
			}
			r.finishHandleLocked(relayHandle, err)
			r.mu.Unlock()
			return err
		}
		r.mu.Lock()
		if r.relayed[relayKey] != relayHandle || relayCtx.Err() != nil || ctx.Err() != nil {
			err = relayHandle.err
			if err == nil {
				err = ctx.Err()
				if err == nil {
					err = context.Canceled
				}
			}
			if r.relayed[relayKey] == relayHandle {
				delete(r.relayed, relayKey)
			}
			r.finishHandleLocked(relayHandle, err)
			r.mu.Unlock()
			cancelRelay()
			return err
		}
		if err = registerSubscription(ctx, relayKey, subscribeParams.ReplaceSubscription); err != nil {
			delete(r.relayed, relayKey)
			r.finishHandleLocked(relayHandle, err)
			r.mu.Unlock()
			cancelRelay()
			return err
		}
		relayHandle.established = true
		stopInitialCancellation()
		r.finishHandleLocked(relayHandle, nil)
		r.mu.Unlock()
		if cfg.RelayHooks.AfterReady != nil {
			cfg.RelayHooks.AfterReady(threadID)
		}
		r.mu.Lock()
		active = r.relayed[relayKey] == relayHandle && relayCtx.Err() == nil
		err = relayHandle.err
		r.mu.Unlock()
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
		r.mu.Lock()
		active = r.relayed[relayKey] == relayHandle && relayCtx.Err() == nil
		err = relayHandle.err
		if !active {
			r.mu.Unlock()
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
			ticker := time.NewTicker(relayIdleInterval)
			cleanupRelay := func() {
				cancelRelay()
				r.mu.Lock()
				if r.relayed[relayKey] == relayHandle {
					delete(r.relayed, relayKey)
				}
				r.mu.Unlock()
			}
			defer ticker.Stop()
			defer cleanupRelay()
			argsByCallID := map[string]string{}
			var backoff relayRetryBackoff
			// turnRunning mirrors whether the thread is running a turn, tracked
			// from the thread/status/changed frames this loop already forwards,
			// so giveUpOnRunningTurn knows whether a re-dial failure is happening
			// mid-turn (spinner visibly stalled) or between turns (nothing on
			// screen is waiting, so nothing needs to be told).
			var turnRunning bool
			var runningTurnID string
			// noticeAnchorEntry is the last relayed entry's ordinal + 1, 0
			// before any (the spec's notice-anchor rule, appoverlay.go's
			// anchorEntry): the entry a give-up notice anchors after.
			var noticeAnchorEntry uint64
			var consecutiveFailures int
			trackRelayedThread := func(notification appwire.Notification) {
				switch notification.Method {
				case appwire.NotifyThreadStatusChanged:
					var params appwire.ThreadStatusChangedParams
					if json.Unmarshal(notification.Params, &params) == nil {
						turnRunning = params.Status.Type == appwire.ThreadStatusActive
						runningTurnID = params.ActiveTurnID
					}
				case appwire.NotifyHistoryUpdated:
					var params struct {
						Items []struct {
							Position *appwire.ThreadItemPosition `json:"position"`
						} `json:"items"`
					}
					if json.Unmarshal(notification.Params, &params) != nil {
						return
					}
					for _, item := range params.Items {
						if item.Position != nil {
							noticeAnchorEntry = max(noticeAnchorEntry, item.Position.Entry+1)
						}
					}
				}
			}
			// giveUpOnRunningTurn tells the reader what the unreachable source can
			// no longer say: nothing runs the turn any more, and why. It publishes
			// the idle status, which owns the session status; an error notice in
			// the running turn naming the last re-dial failure; and a resync, so
			// the client re-reads: the turn never completed reads as an open turn
			// with nothing running it. It fires at most once per stall: clearing
			// turnRunning makes every later call in the same stall a no-op, so
			// continued backoff never re-broadcasts them.
			//
			// The notice is published live and nowhere else. This loop serves
			// only sources without an atomic relay session (remote hubs and other
			// federated sources; a local daemon's death takes the relay session's
			// DaemonGoneResync path instead), and the client's re-read goes to the
			// same unreachable source and fails, which leaves its history and
			// overlay, the notice included, as they were. The notice anchors after
			// the latest history this relay forwarded, and at the start of history
			// only when it forwarded none.
			//
			// Both frames name the relay's target. The synthesized failure is the
			// turn's; the session STATUS is thread/status/changed's, never
			// turn/completed's — the daemon's real failure exit emits the same
			// pair (agent/session_lifecycle.go's endInputAtTurnFailure announces
			// EventSessionEnd{Reason:"turn_failed"} as
			// thread/status/changed(systemError)). The synthesized frame keeps
			// idle: a lost connection is not a recorded turn failure, and the
			// roster marks a dead daemon errored. Without the status frame the
			// client would keep the session active with Stop and Steer still
			// showing and Send withheld — the exact stall this exists to end. The
			// capabilities are the hub's own answer for a session whose daemon is
			// gone (relayGaveUpCapabilities, the set a past read returns), because
			// the departing daemon's set describes the turn that is over.
			giveUpOnRunningTurn := func(cause error) {
				if !turnRunning {
					return
				}
				turnRunning = false
				logf("relay for %s gave up on its running turn after %d failed re-dials: %v", relayKey, consecutiveFailures, cause)
				server.Broadcast(relayKey, appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
					ThreadID:     threadID,
					Ref:          subscribeParams.Ref,
					Status:       appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
					Capabilities: relayGaveUpCapabilities(cfg, relayKey, thread),
				})
				server.Broadcast(relayKey, appwire.NotifyOverlayUpserted, appwire.OverlayUpsertedParams{
					ThreadID: threadID,
					Ref:      subscribeParams.Ref,
					Item:     relayGaveUpNotice(runningTurnID, noticeAnchorEntry, cause),
				})
				server.Broadcast(relayKey, appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
					ThreadID: threadID,
					Ref:      subscribeParams.Ref,
				})
			}
			recordFailure := func(cause error) {
				consecutiveFailures++
				if consecutiveFailures >= relayGiveUpAfterFailures {
					giveUpOnRunningTurn(cause)
				}
			}
			broadcastNotification := func(notification appwire.Notification) {
				backoff.Reset()
				consecutiveFailures = 0
				trackRelayedThread(notification)
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
				r.mu.Lock()
				if server.SubscriberCount(relayKey) != 0 {
					r.mu.Unlock()
					return false
				}
				if r.relayed[relayKey] == relayHandle {
					delete(r.relayed, relayKey)
				}
				r.mu.Unlock()
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
						recordFailure(errors.New("the source returned no notification stream"))
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
							recordFailure(errors.New("the source closed its notification stream"))
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
		r.mu.Unlock()
		return nil
	}
	startRelay = func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams, thread appwire.Thread) error {
		_, err := appserver.RetrySubscription(ctx, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, startRelayOnce(ctx, source, params, thread)
		})
		return err
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
		if err := appwire.ValidateCommandInputSupport(params.Input, prepared.Thread.Evener.Capabilities.CommandInput); err != nil {
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
		r.mu.Lock()
		handle := r.canonical[ref]
		if handle != nil && !handle.removed {
			handle.stopping = true
			if handle.initializing {
				cancelInitializing = handle.cancel
			}
			for _, state := range handle.relayKeys {
				state.stopRequested = true
				state.retiring = true
				state.retireOwner = nil
				r.removeStoppedStateLocked(handle, state)
			}
			for state := range handle.pendingStates {
				state.stopRequested = true
				r.removeStoppedStateLocked(handle, state)
			}
			r.signalRouteChangeLocked(handle)
			closeHandle = r.maybeFinishHandleLocked(handle)
		}
		r.mu.Unlock()
		if cancelInitializing != nil {
			cancelInitializing()
		}
		if closeHandle {
			r.closeRelayHandle(handle)
		}
	}
	stopRelay := func(relayKey string) {
		var closeHandles []*hubRelayHandle
		r.mu.Lock()
		handle := r.relayed[relayKey]
		if handle != nil && handle.canonical == (appwire.Ref{}) {
			if r.removeRelayHandleLocked(handle) {
				r.finishHandleLocked(handle, context.Canceled)
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
					r.removeStoppedStateLocked(handle, state)
				}
			}
			for state, pendingHandle := range r.pending[relayKey] {
				handles[pendingHandle] = struct{}{}
				if state != nil {
					state.stopRequested = true
					r.removeStoppedStateLocked(pendingHandle, state)
				}
			}
			for currentHandle := range handles {
				r.signalRouteChangeLocked(currentHandle)
				if r.maybeFinishHandleLocked(currentHandle) {
					closeHandles = append(closeHandles, currentHandle)
				}
			}
		}
		r.mu.Unlock()
		for _, closeHandle := range closeHandles {
			r.closeRelayHandle(closeHandle)
		}
	}
	relayCommandCount := func(key string) int {
		r.mu.Lock()
		defer r.mu.Unlock()
		count := 0
		if handle := r.relayed[key]; handle != nil {
			if state := handle.relayKeys[key]; state != nil {
				count += state.commands
			}
		}
		for state := range r.pending[key] {
			count += state.commands
		}
		return count
	}
	relayPublished := func(key string) bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		handle := r.relayed[key]
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
