package hubcore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
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

// liveSnapshot is one live listing with the endpoint identity it was
// fetched from: rows from endpoint X must never publish onto an
// instance now pointing at endpoint Y, however the swap happened
// (remove/re-add, rename, re-point, failed-reload recovery).
type liveSnapshot struct {
	identity string
	rows     []registry.Model
}

type ProviderRegistry struct {
	load    RegistryLoader
	mu      sync.RWMutex
	current *registry.Registry
	loadErr error
	// generation is the monotonic token source: every Reload that
	// installs a new current and every BeginLiveFetchReg bumps it. A
	// fetch mints its token at request start, so a slower fetch that
	// returns after a newer one began holds a lower token.
	// lastApplied holds the highest token actually applied per
	// instance; ReapplyLive lands only above it, so stale responses
	// are discarded while a failed newer fetch — which applies
	// nothing — never blocks an older in-flight success.
	// lastGoodLive is the live snapshot of the most recent
	// successfully loaded registry, identity and all: a failed reload
	// parks the holder on the implicit-only fallback, which knows none
	// of the explicit instances, so carryLive alone would drop their
	// rows — the next successful Reload re-applies this snapshot
	// (identity-checked) instead.
	generation   uint64
	lastApplied  map[string]uint64
	lastGoodLive map[string]liveSnapshot
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
	// failed-reload recovery without leaking across a re-point.
	for instance, snap := range h.lastGoodLive {
		if _, ok := r.Instance(instance); !ok {
			continue
		}
		if instanceIdentity(r, instance) != snap.identity {
			continue
		}
		r.ApplyLive(instance, snap.rows)
	}
	for instance, rows := range r.SnapshotLive() {
		if _, ok := r.Instance(instance); !ok {
			continue
		}
		h.noteLive(instance, instanceIdentity(r, instance), rows)
	}
	// Drop snapshots for names the fresh registry no longer knows, or
	// whose identity changed: a later re-add under the same name must
	// not resurrect the old endpoint's rows.
	h.pruneLastGoodLive(r)
	h.current, h.loadErr = r, nil
	h.generation++
	return nil
}

// pruneLastGoodLive deletes cached snapshots for instances absent
// from r or identity-mismatched with it, so a later re-add under the
// same name cannot resurrect the old endpoint's rows. Call with the
// holder lock held.
func (h *ProviderRegistry) pruneLastGoodLive(r *registry.Registry) {
	for instance, snap := range h.lastGoodLive {
		if _, ok := r.Instance(instance); !ok {
			delete(h.lastGoodLive, instance)
			continue
		}
		if instanceIdentity(r, instance) != snap.identity {
			delete(h.lastGoodLive, instance)
		}
	}
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
		if _, ok := old.Instance(instance); !ok {
			continue
		}
		if _, ok := r.Instance(instance); !ok {
			continue
		}
		if instanceIdentity(old, instance) != instanceIdentity(r, instance) {
			continue
		}
		r.ApplyLive(instance, rows)
	}
}

// instanceIdentity fingerprints what a live listing is fetched from:
// the provider, protocol, fully resolved endpoint routing (base URL
// plus the protocol models endpoint -- a models_endpoint change
// re-points the fetch as surely as a base_url change), and a
// non-secret fingerprint of the credential material behind the source
// label. A Reload that removes, renames, re-points, or re-credentials
// an instance changes its identity, and rows fetched from the old
// transport must not publish into the new one. Display fields
// (default, warnings, vars) do not affect where rows come from and
// are not part of it. The fingerprint hashes the secret bytes
// themselves (SHA-256, in-memory only), so even a same-length rotation
// changes the identity; only the digest enters the identity string,
// never the secrets.
func instanceIdentity(r *registry.Registry, name string) string {
	inst, ok := r.Instance(name)
	if !ok {
		return "unknown\x00" + name
	}
	endpoint := ""
	authprint := ""
	if res, err := r.ResolveInstance(name); err == nil {
		endpoint = res.Transport.ModelsEndpoint
		authprint = authFingerprint(res)
		if res.Transport.Auth == registry.AuthOAuthOpenAICodex {
			authprint += "\x00" + oauthAccountFingerprint(r.StateRoot(), name)
		}
	}
	return strings.Join([]string{inst.ProviderID, inst.Protocol, inst.BaseURL, endpoint, inst.Auth, inst.CredentialSource, authprint}, "\x00")
}

// authFingerprint hashes the resolved authentication material
// non-reversibly (SHA-256 over the actual secret bytes and the stable
// account-identity fields, never the lengths alone): a same-length key
// rotation, an OAuth account swap, or a header change alters the
// fingerprint, so rows fetched under the old credential never publish
// into the newly-credentialed instance. The digest never leaves the
// process — it lives only in identity strings compared in-memory — so a
// strong hash of the value is safe where a length was not sufficient.
func authFingerprint(res registry.Resolved) string {
	sum := sha256.New()
	cred := res.Credential
	_, _ = fmt.Fprintf(sum, "%s\x01%s\x01", cred.Source, cred.Value)
	_, _ = fmt.Fprintf(sum, "%s\x01%s\x01", res.Transport.Auth, res.Transport.AuthHeader)
	for _, k := range slices.Sorted(maps.Keys(res.CredentialHeaders)) {
		_, _ = fmt.Fprintf(sum, "%s\x01%s\x01", k, res.CredentialHeaders[k])
	}
	for _, k := range slices.Sorted(maps.Keys(res.Headers)) {
		_, _ = fmt.Fprintf(sum, "%s\x01%s\x01", k, res.Headers[k])
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// oauthAccountFingerprint folds the Codex OAuth record's stable
// account-identity claims (email, account and workspace ids) into the
// fingerprint: swapping the signed-in account behind an unchanged record
// filename must change the identity even though the credential source
// label ("oauth") and the transport stay the same. Tokens are
// deliberately excluded — a refresh rotates them without changing whose
// account the listing belongs to, and the secret bytes are already
// covered by authFingerprint's value hash. A missing or unreadable
// record contributes nothing: the source label in the identity already
// distinguishes "no record" from "record".
func oauthAccountFingerprint(stateRoot, instance string) string {
	raw, err := os.ReadFile(filepath.Join(stateRoot, "auth", filepath.Base(instance)+".json"))
	if err != nil {
		return ""
	}
	var rec struct {
		Email       string `json:"email"`
		AccountID   string `json:"account_id"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return ""
	}
	if rec.Email == "" && rec.AccountID == "" && rec.WorkspaceID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{rec.Email, rec.AccountID, rec.WorkspaceID}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// Get returns the registry currently held; nil before the first successful load.
func (h *ProviderRegistry) Get() *registry.Registry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

// BeginLiveFetchReg atomically pairs instance's fetch token with the
// registry snapshot the fetch must run against AND the endpoint
// identity that fetch queried: the client is built from the returned
// registry, so a Reload landing between the two cannot strand a
// new-generation token on an old-registry fetch (or vice versa), and
// ReapplyLive judges each fetch against its own identity — a newer
// begin cannot overwrite what an older in-flight fetch is checked
// against.
func (h *ProviderRegistry) BeginLiveFetchReg(instance string) (*registry.Registry, uint64, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.generation++
	id := ""
	if h.current != nil {
		id = instanceIdentity(h.current, instance)
	}
	return h.current, h.generation, id
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
func (h *ProviderRegistry) ReapplyLive(tok uint64, instance, identity string, rows []registry.Model) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current == nil {
		return
	}
	if tok <= h.lastApplied[instance] {
		return
	}
	// Judged against THIS fetch's endpoint identity — not the latest
	// recorded for the name. A remove/rename/re-point since the fetch
	// began drops its rows instead of planting the old transport's
	// listing on the new one, however many newer fetches began after.
	// An empty fetched identity (a registry that never knew the name,
	// as in the hermetic holder tests) matches an empty current one.
	if identity != instanceIdentity(h.current, instance) {
		return
	}
	if h.lastApplied == nil {
		h.lastApplied = map[string]uint64{}
	}
	h.lastApplied[instance] = tok
	h.current.ApplyLive(instance, rows)
	h.noteLive(instance, identity, rows)
}

// noteLive records rows as the holder's last-good snapshot for
// instance: every path that lands a listing — ReapplyLive and the
// successful Reload below — reports here, so failed-reload recovery
// always has the freshest rows to restore.
func (h *ProviderRegistry) noteLive(instance, identity string, rows []registry.Model) {
	if h.lastGoodLive == nil {
		h.lastGoodLive = map[string]liveSnapshot{}
	}
	cp := make([]registry.Model, len(rows))
	copy(cp, rows)
	h.lastGoodLive[instance] = liveSnapshot{identity: identity, rows: cp}
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
