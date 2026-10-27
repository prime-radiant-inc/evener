package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
)

// The relay is stopped in production by idle exit and server shutdown; these
// give tests a direct way to stop it and to read its bookkeeping.

func (r *hubRelay) stopCanonicalRelay(ref appwire.Ref) {
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

func (r *hubRelay) stopRelay(relayKey string) {
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

func (r *hubRelay) relayCommandCount(key string) int {
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

func (r *hubRelay) relayPublished(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	handle := r.relayed[key]
	return handle != nil && handle.relayKeys[key] != nil
}
