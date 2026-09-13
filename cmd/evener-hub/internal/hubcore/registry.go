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
	// generation counts successful holder swaps plus live-fetch
	// applications: every Reload that installs a new current and every
	// ReapplyLive that lands a listing bumps it. A fetch captures the
	// registry before the request and mints its apply token only when it
	// has a listing to publish, so a failed fetch never supersedes a
	// successful concurrent one, and a stale response can neither be
	// lost on a detached registry nor overwrite a newer listing.
	generation uint64
	liveTokens map[string]uint64
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

// Current returns the held registry a live fetch runs against. The
// apply token is minted separately by ClaimLiveApply once the fetch has
// a listing to publish, so a failed fetch never supersedes a successful
// concurrent one.
func (h *ProviderRegistry) Current() *registry.Registry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

// ClaimLiveApply mints the apply token for instance's just-fetched
// listing. Call it after the request succeeds, immediately before
// ReapplyLive: any newer claim (a concurrent fetch that finished first,
// or a Reload, which bumps the generation on swap) supersedes this one,
// and ReapplyLive then discards it.
func (h *ProviderRegistry) ClaimLiveApply(instance string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.generation++
	if h.liveTokens == nil {
		h.liveTokens = map[string]uint64{}
	}
	h.liveTokens[instance] = h.generation
	return h.generation
}

// ReapplyLive applies rows fetched under token tok to the current
// registry. Rows are the fetch's raw live snapshot — reg.LiveModels
// after the request — so advertised capability facts survive the round
// trip the way the direct ApplyLive inside the fetch does. The apply is
// skipped when the token is stale: a Reload or a newer fetch for the
// same instance already moved on (Reload's carryLive kept the before
// snapshot; the newer fetch owns the after). A failed fetch never
// reaches here, and an unsupported listing (Live == false) must not
// either: its rows are catalog data, not a live listing, and applying
// them would plant an empty snapshot over a real one.
func (h *ProviderRegistry) ReapplyLive(tok uint64, instance string, rows []registry.Model) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current == nil {
		return
	}
	if cur, ok := h.liveTokens[instance]; !ok || cur != tok {
		return
	}
	h.current.ApplyLive(instance, rows)
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
