package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
)

func (r *hubRelay) removeStateRoutesLocked(handle *hubRelayHandle, state *relayKeyState) {
	if state == nil {
		return
	}
	for routingKey := range state.routingKeys {
		if handle.routes[routingKey] == state {
			delete(handle.routes, routingKey)
		}
		delete(state.routingKeys, routingKey)
	}
}

func (r *hubRelay) bindStateRouteLocked(handle *hubRelayHandle, routingKey string, state *relayKeyState) {
	if routingKey == "" {
		return
	}
	if previous := handle.routes[routingKey]; previous != nil && previous != state {
		delete(previous.routingKeys, routingKey)
	}
	handle.routes[routingKey] = state
	state.routingKeys[routingKey] = struct{}{}
}

func (r *hubRelay) removeRelayKeyStateLocked(handle *hubRelayHandle, state *relayKeyState) bool {
	if state == nil || state.publications != 0 || r.relayed[state.relayKey] != handle || handle.relayKeys[state.relayKey] != state {
		return false
	}
	state.retiring = true
	r.removeStateRoutesLocked(handle, state)
	delete(handle.relayKeys, state.relayKey)
	delete(r.relayed, state.relayKey)
	close(state.done)
	return true
}

func (r *hubRelay) signalRouteChangeLocked(handle *hubRelayHandle) {
	close(handle.routeChanged)
	handle.routeChanged = make(chan struct{})
}

func (r *hubRelay) registerPendingStateLocked(handle *hubRelayHandle, state *relayKeyState) {
	owners := r.pending[state.relayKey]
	if owners == nil {
		owners = make(map[*relayKeyState]*hubRelayHandle)
		r.pending[state.relayKey] = owners
	}
	owners[state] = handle
	handle.pendingStates[state] = struct{}{}
	handle.pendingKeys[state.relayKey] = state
}

func (r *hubRelay) unregisterPendingStateLocked(handle *hubRelayHandle, state *relayKeyState) bool {
	owners := r.pending[state.relayKey]
	if owners == nil || owners[state] != handle {
		return false
	}
	delete(owners, state)
	if len(owners) == 0 {
		delete(r.pending, state.relayKey)
	}
	delete(handle.pendingStates, state)
	if handle.pendingKeys[state.relayKey] == state {
		delete(handle.pendingKeys, state.relayKey)
	}
	return true
}

func (r *hubRelay) finishHandleLocked(handle *hubRelayHandle, err error) {
	select {
	case <-handle.ready:
		handle.err = err
	default:
		handle.err = err
		close(handle.ready)
	}
}

func (r *hubRelay) closeRelayHandle(handle *hubRelayHandle) {
	handle.closeOnce.Do(func() {
		handle.cancel()
		if handle.lease != nil {
			handle.lease.Close()
		}
	})
}

func (r *hubRelay) removeRelayHandleLocked(handle *hubRelayHandle) bool {
	if handle == nil || handle.removed {
		return false
	}
	for _, state := range handle.relayKeys {
		if state.publications != 0 {
			return false
		}
	}
	if handle.canonical != (appwire.Ref{}) && r.canonical[handle.canonical] == handle {
		delete(r.canonical, handle.canonical)
	}
	for relayKey, current := range r.relayed {
		if current == handle {
			delete(r.relayed, relayKey)
		}
	}
	for relayKey, state := range handle.relayKeys {
		r.removeStateRoutesLocked(handle, state)
		delete(handle.relayKeys, relayKey)
		close(state.done)
	}
	for state := range handle.pendingStates {
		r.unregisterPendingStateLocked(handle, state)
		close(state.done)
	}
	for routingKey := range handle.routes {
		delete(handle.routes, routingKey)
	}
	handle.removed = true
	if handle.done != nil {
		close(handle.done)
	}
	return true
}

func (r *hubRelay) maybeFinishHandleLocked(handle *hubRelayHandle) bool {
	if handle == nil || handle.removed || handle.initializing || len(handle.relayKeys) != 0 || len(handle.pendingStates) != 0 || handle.commandOwners != 0 {
		return false
	}
	if !r.removeRelayHandleLocked(handle) {
		return false
	}
	r.finishHandleLocked(handle, context.Canceled)
	return true
}

func (r *hubRelay) removeStoppedStateLocked(handle *hubRelayHandle, state *relayKeyState) bool {
	if state == nil || state.commands != 0 || state.publications != 0 {
		return false
	}
	if r.pending[state.relayKey][state] == handle {
		r.unregisterPendingStateLocked(handle, state)
		close(state.done)
		return true
	}
	return r.removeRelayKeyStateLocked(handle, state)
}

func (r *hubRelay) retireRelayHandle(handle *hubRelayHandle) {
	var closeHandle bool
	r.mu.Lock()
	handle.stopping = true
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
	closeHandle = r.maybeFinishHandleLocked(handle)
	r.mu.Unlock()
	if closeHandle {
		r.closeRelayHandle(handle)
	}
}
