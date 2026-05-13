package appsource

import (
	"fmt"
	"sync"

	"primeradiant.com/serf/internal/appwire"
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

func (r *Registry) Source(id string) (Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	source, ok := r.sources[id]
	return source, ok
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
