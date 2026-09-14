package hubcore

import (
	"strings"
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
	// generation is the monotonic token source: every Reload that
	// installs a new current and every BeginLiveFetch bumps it. A fetch
	// mints its token at request start, so a slower fetch that returns
	// after a newer one began holds a lower token. lastApplied holds
	// the highest token actually applied per instance; ReapplyLive
	// lands only above it, so stale responses are discarded while a
	// failed newer fetch — which applies nothing — never blocks an
	// older in-flight success. snapshots holds the endpoint identity
	// each in-flight fetch ran against; a remove/rename/re-point
	// since then drops its rows. lastGoodLive is the live snapshot of
	// the most recent successfully loaded registry: a failed reload
	// parks the holder on the implicit-only fallback, which knows none
	// of the explicit instances, so carryLive alone would drop their
	// rows — the next successful Reload re-applies this snapshot
	// (identity-checked) instead.
	generation   uint64
	lastApplied  map[string]uint64
	snapshots    map[string]string
	lastGoodLive map[string][]registry.Model
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
	// The fallback between a failure and its fix knew none of the
	// explicit instances: re-apply the last-good snapshot for every
	// instance whose identity still matches, so live-only ids survive
	// failed-reload recovery.
	for instance, rows := range h.lastGoodLive {
		after, ok := r.Instance(instance)
		if !ok {
			continue
		}
		if before, ok := h.current.Instance(instance); ok && instanceIdentity(before) != instanceIdentity(after) {
			continue
		}
		r.ApplyLive(instance, rows)
	}
	for instance, rows := range r.SnapshotLive() {
		h.noteLive(instance, rows)
	}
	h.current, h.loadErr = r, nil
	h.generation++
	return nil
}

// carryLive re-applies old's cached live listings onto r — but only
// for instances whose identity survived the swap. A removed, renamed,
// or re-pointed instance starts with no live rows rather than the old
// endpoint's. Both nil-safe; ApplyLive re-filters, so the round trip is
// idempotent.
func carryLive(old, r *registry.Registry) {
	if old == nil || r == nil {
		return
	}
	for instance, rows := range old.SnapshotLive() {
		before, ok := old.Instance(instance)
		if !ok {
			continue
		}
		after, ok := r.Instance(instance)
		if !ok || instanceIdentity(before) != instanceIdentity(after) {
			continue
		}
		r.ApplyLive(instance, rows)
	}
}

// instanceIdentity fingerprints what a live listing is fetched from:
// the provider, protocol, endpoint, and credential source. A Reload
// that removes, renames, or re-points an instance changes its identity,
// and rows fetched from the old transport must not publish into the
// new one. Auth-mode and display fields (default, warnings, vars) do
// not affect where rows come from and are not part of it.
func instanceIdentity(inst registry.Instance) string {
	return strings.Join([]string{inst.ProviderID, inst.Protocol, inst.BaseURL, inst.Auth, inst.CredentialSource}, "\x00")
}

// Get returns the registry currently held; nil before the first successful load.
func (h *ProviderRegistry) Get() *registry.Registry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

// BeginLiveFetchReg atomically pairs instance's fetch token with the
// registry snapshot the fetch must run against: the client is built
// from the returned registry, so a Reload landing between the two
// cannot strand a new-generation token on an old-registry fetch (or
// vice versa). It also records the instance's endpoint identity, so
// ReapplyLive drops rows when a remove/rename/re-point changed what
// the name points at. ReapplyLive's lastApplied check then orders the
// result against every overlapping fetch and swap.
func (h *ProviderRegistry) BeginLiveFetchReg(instance string) (*registry.Registry, uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.generation++
	if h.snapshots == nil {
		h.snapshots = map[string]string{}
	}
	if h.current != nil {
		if inst, ok := h.current.Instance(instance); ok {
			h.snapshots[instance] = instanceIdentity(inst)
		} else {
			delete(h.snapshots, instance)
		}
	}
	return h.current, h.generation
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
	// The fetch ran against the snapshot BeginLiveFetchReg returned:
	// publish only while the instance still identifies the same
	// endpoint. A remove/rename/re-point since then drops the rows
	// instead of planting the old transport's listing on the new one.
	// A fetch against a registry that never knew the name (no
	// snapshot recorded) applies normally: the hermetic holder tests
	// and any pre-identity caller take this path.
	if snap, recorded := h.snapshots[instance]; recorded {
		cur, ok := h.current.Instance(instance)
		if !ok || snap != instanceIdentity(cur) {
			return
		}
	}
	if h.lastApplied == nil {
		h.lastApplied = map[string]uint64{}
	}
	h.lastApplied[instance] = tok
	h.current.ApplyLive(instance, rows)
	h.noteLive(instance, rows)
}

// noteLive records rows as the holder's last-good snapshot for
// instance: every path that lands a listing — ReapplyLive and the
// successful Reload below — reports here, so failed-reload recovery
// always has the freshest rows to restore.
func (h *ProviderRegistry) noteLive(instance string, rows []registry.Model) {
	if h.lastGoodLive == nil {
		h.lastGoodLive = map[string][]registry.Model{}
	}
	cp := make([]registry.Model, len(rows))
	copy(cp, rows)
	h.lastGoodLive[instance] = cp
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
