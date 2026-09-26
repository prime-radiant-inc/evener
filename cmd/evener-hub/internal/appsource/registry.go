package appsource

import (
	"fmt"
	"sort"
	"sync"

	"primeradiant.com/evener/appwire"
)

type Registry struct {
	mu       sync.RWMutex
	sources  map[string]Source
	onAdd    func(Source)
	onRemove func(Source)
}

func NewRegistry() *Registry {
	return &Registry{sources: map[string]Source{}}
}

// SetOnAdd registers a callback invoked once for each source Add inserts,
// after the insert is visible to lookups. It is how a long-lived consumer — the
// host-notification fan-out — learns about sources registered after it
// started, instead of a one-shot enumeration at construction missing every
// host added at runtime. Sources added before the hook is set never fire it:
// the caller installing the hook enumerates the current set itself.
//
// The callback fires after the registry's lock is released, so a delayed
// delivery may observe a newer same-name registration that replaced the
// source: a consumer that keys state by the source's ID must re-validate the
// delivered instance against the registry before acting on it (the
// host-notification fan-out does — see hubHostAdminController.launchFanOut).
func (r *Registry) SetOnAdd(fn func(Source)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onAdd = fn
}

// SetOnRemove registers a callback invoked once for each source Remove
// deletes, after the deletion is visible to lookups. It is how a long-lived
// consumer — the host-notification fan-out — learns about sources removed
// after it started (the host-management surface's remove), so the per-name
// state it built for the source can be torn down with it instead of outliving
// it until shutdown. It mirrors SetOnAdd: the callback runs outside the
// registry's lock, so it may call back into the registry — and a delayed
// delivery may observe a newer same-name registration the removal's delete
// let re-add, so the consumer must re-validate the name's current entry
// before tearing the removed source's state down
// (hubHostAdminController.stopFanOut does).
func (r *Registry) SetOnRemove(fn func(Source)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onRemove = fn
}

func (r *Registry) Add(source Source) {
	r.mu.Lock()
	r.sources[source.ID()] = source
	onAdd := r.onAdd
	r.mu.Unlock()
	if onAdd != nil {
		onAdd(source)
	}
}

// Remove deletes the source registered under id, reporting nothing when no
// such source exists; the on-remove notification fires only for a source that
// was actually deleted.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	source, ok := r.sources[id]
	if ok {
		delete(r.sources, id)
	}
	onRemove := r.onRemove
	r.mu.Unlock()
	if ok && onRemove != nil {
		onRemove(source)
	}
}

func (r *Registry) Source(id string) (Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	source, ok := r.sources[id]
	return source, ok
}

func (r *Registry) All() []Source {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.sources))
	for id := range r.sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	sources := make([]Source, 0, len(ids))
	for _, id := range ids {
		sources = append(sources, r.sources[id])
	}
	return sources
}

func (r *Registry) SourceForRef(raw string) (Source, error) {
	ref, err := appwire.ParseRef(raw)
	if err != nil {
		return nil, err
	}
	source, ok := r.Source(ref.SourceID)
	if !ok {
		return nil, fmt.Errorf("source not found: %s", ref.SourceID)
	}
	return source, nil
}
