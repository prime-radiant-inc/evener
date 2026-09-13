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
	// generation orders holder swaps and live fetches: every Reload
	// that installs a new current and every BeginLiveFetch bumps it.
	// A fetch mints its token at request start, so a slower fetch that
	// returns after a newer one began holds a lower token. ReapplyLive
	// applies only above lastApplied, so stale responses are discarded
	// while a failed newer fetch — which applies nothing — never blocks
	// an older in-flight success.
	generation uint64
	// liveTokens holds each instance's latest fetch token: minted by
	// BeginLiveFetch at request start, so overlapping fetches stay
	// ordered. lastApplied holds the highest token actually applied by
	// ReapplyLive. The split is what keeps a failed newer fetch from
	// invalidating an older in-flight success: the failure applies
	// nothing and leaves lastApplied untouched, while a
	// later-started success still wins by applying a higher token.
	liveTokens  map[string]uint64
	lastApplied map[string]uint64
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

// Current returns the held registry a live fetch runs against.
func (h *ProviderRegistry) Current() *registry.Registry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

// BeginLiveFetch mints instance's fetch token at request start. A fetch
// that returns after a newer fetch for the same instance began — or
// after a Reload swapped the registry — holds a stale token, and
// ReapplyLive discards it. Minting at start (not at apply) is what
// orders overlapping fetches; ReapplyLive staying claim-free is what
// keeps a failed fetch from superseding a successful concurrent one.
func (h *ProviderRegistry) BeginLiveFetch(instance string) uint64 {
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
// registry. Rows are the fetch's raw live snapshot, so advertised
// capability facts survive the round trip the way the direct ApplyLive
// inside the fetch did. The apply lands only above lastApplied: a
// slower fetch that returns after a newer success began holds a lower
// token and is discarded, while a failed newer fetch — which never
// reaches here — leaves lastApplied untouched so the older success it
// overtook still lands. A Reload swaps the registry (whose carryLive
// kept the before snapshot); a fetch that began before the swap holds
// a token from an older generation and still applies its listing
// forward, which is the carry-forward the refresh path needs. An
// unsupported listing (Live == false) must not reach here either: its
// rows are catalog data, not a live listing, and applying them would
// plant an empty snapshot over a real one.
func (h *ProviderRegistry) ReapplyLive(tok uint64, instance string, rows []registry.Model) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current == nil {
		return
	}
	if tok <= h.lastApplied[instance] {
		return
	}
	if h.lastApplied == nil {
		h.lastApplied = map[string]uint64{}
	}
	h.lastApplied[instance] = tok
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
