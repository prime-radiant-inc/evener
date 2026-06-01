package appsource

import (
	"fmt"
	"sort"
	"sync"

	"primeradiant.com/serf/appwire"
)

type Registry struct {
	mu      sync.RWMutex
	sources map[string]Source
}

func NewRegistry() *Registry {
	return &Registry{sources: map[string]Source{}}
}

func (r *Registry) Add(source Source) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources[source.ID()] = source
}

func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sources, id)
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
