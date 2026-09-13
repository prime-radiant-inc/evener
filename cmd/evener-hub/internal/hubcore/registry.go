package hubcore

import (
	"sync"

	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// RegistryLoader loads the provider registry with extra options applied on
// top of the process-wide ones — cmdutil.LoadRegistry in production, a
// hermetic loader in tests.
type RegistryLoader func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error)

// ProviderRegistry is the hub's live view of the provider registry: the
// current instance set, reloaded after every providers.toml write, and the
// diagnostics the web UI shows (spec §11.3). When the user layer fails to
// load (an old-schema file) it holds an implicit-only registry, keeps the
// error for the diagnostics, and refuses writes until a reload succeeds
// (spec §10, §14.1).
type ProviderRegistry struct {
	load    RegistryLoader
	mu      sync.RWMutex
	current *registry.Registry
	loadErr error
	// generation counts successful holder swaps: every Reload that
	// installs a new current bumps it. Live fetches capture the holder
	// alongside the registry pointer, so a fetch that returns against a
	// detached registry can re-apply its listing to the current one
	// instead of losing it.
	generation uint64
}

// NewProviderRegistry returns a holder that loads through load. Nothing is
// read until the first Reload.
func NewProviderRegistry(load RegistryLoader) *ProviderRegistry {
	return &ProviderRegistry{load: load}
}

// Reload re-reads the registry and returns the load error, if any. A failing
// user layer leaves the holder on an implicit-only registry so sessions still
// launch, and the error is what refuses instance writes until the file is
// fixed. Cached live listings carry over to the fresh object either way, so
// an instance write never wipes what background prefetch and manual
// refreshes already fetched.
func (h *ProviderRegistry) Reload() error {
	r, _, err := h.load()
	if err != nil {
		fallback, _, ferr := h.load(registry.WithNoUserLayer())
		h.mu.Lock()
		defer h.mu.Unlock()
		old := h.current
		h.loadErr = err
		if ferr == nil {
			carryLive(old, fallback)
			h.current = fallback
			h.generation++
		}
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	carryLive(h.current, r)
	h.current, h.loadErr = r, nil
	h.generation++
	return nil
}

// carryLive re-applies old's cached live listings onto r. Both nil-safe;
// ApplyLive re-filters, so the round trip is idempotent.
func carryLive(old, r *registry.Registry) {
	if old == nil || r == nil {
		return
	}
	for instance, rows := range old.SnapshotLive() {
		r.ApplyLive(instance, rows)
	}
}

// Get returns the registry currently held; nil before the first successful load.
func (h *ProviderRegistry) Get() *registry.Registry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

// Current returns the held registry with its generation: a live fetch
// captures both before the request and hands them to ReapplyLive after,
// so a concurrent Reload cannot strand the listing on a detached object.
func (h *ProviderRegistry) Current() (*registry.Registry, uint64) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current, h.generation
}

// ReapplyLive applies rows fetched against generation gen to the current
// registry: when no Reload landed in between it is the same object the
// fetch already wrote, and ApplyLive re-filters idempotently; when a
// reload did land, this carries the listing forward onto the fresh
// object. Rows are the listing's resolved ids; ApplyLive keeps only
// advertised facts per id, so the round trip holds exactly what the
// holder would have kept had the fetch run against the fresh object.
func (h *ProviderRegistry) ReapplyLive(gen uint64, instance string, rows []registry.Resolved) {
	models := make([]registry.Model, 0, len(rows))
	for _, row := range rows {
		models = append(models, registry.Model{ID: row.ModelID})
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.current == nil {
		return
	}
	h.current.ApplyLive(instance, models)
}

// LoadError is the error from the last Reload, or nil when it succeeded.
func (h *ProviderRegistry) LoadError() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.loadErr
}

// WritesRefused reports whether providers.toml may be rewritten: a file the
// registry could not read is never rewritten over (spec §10).
func (h *ProviderRegistry) WritesRefused() bool { return h.LoadError() != nil }

// Diagnostics is what the credentials pane shows above the instance list:
// the load error, the user-layer note, stray OAuth records, and warnings.
func (h *ProviderRegistry) Diagnostics() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []string
	if h.loadErr != nil {
		out = append(out, "providers.toml: "+h.loadErr.Error()+" (instance writes are refused until the file is fixed)")
	}
	if h.current != nil {
		out = append(out, h.current.UserLayerNote())
		out = append(out, h.current.StrayOAuthRecords()...)
		out = append(out, h.current.Warnings()...)
	}
	return out
}
