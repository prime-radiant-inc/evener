package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
)

func (r *hubRelay) acquireRelaySession(
	ctx context.Context,
	source appsource.RelaySessionSource,
	base appsource.Source,
	params appwire.ThreadReadParams,
	canonicalRef appwire.Ref,
) (*hubRelayHandle, *relayKeyState, *appsource.RelayRoutePublication, func(), error) {
	relayKey, _, err := threadRelayTarget(base, params)
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
		relayCtx, cancelRelay := context.WithCancel(r.server.Lifetime())
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
		r.startAcknowledgedFanout(handle, deliveries)
		publish, release := commandFunctions(handle, state)
		return handle, state, publish, release, nil
	}
}
