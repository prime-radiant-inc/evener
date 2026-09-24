package hubcore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	authopenai "primeradiant.com/evener/auth/openai"
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

// LiveToken is what one fetch holds from begin to publish: the holder
// fetch clock it began at — which orders it against newer fetches for
// the same instance — and the incarnation of the instance name at that
// moment, which invalidates it when the name was removed and recreated
// since, however identical the new entry looks. Its fields are private:
// only the holder judges a token, so callers can hold and pass one but
// never forge it.
type LiveToken struct {
	order       uint64
	incarnation uint64
}

type ProviderRegistry struct {
	load RegistryLoader
	mu   sync.RWMutex
	// reloadMu serializes whole reloads - the load and the commit that follows
	// it. Two reloads that load concurrently return in an order nothing
	// controls, and one that read first can commit last, leaving the holder on
	// the older view; mu alone does not order them, because a commit that waits
	// on it still writes whatever that reload read. Callers do overlap: Reload
	// is called directly at startup and by tests, and nothing but the credential
	// lock's call discipline keeps a future caller from reloading beside a
	// credential write - so the load and its commit are serialized as one
	// operation regardless of who calls it. Reads stay on mu, so Get is never
	// held up for the length of a load.
	reloadMu sync.Mutex
	current  *registry.Registry
	loadErr  error
	// generation is the monotonic install clock: every Reload that
	// installs a new current and every landed live re-apply bumps it.
	// Readers that cache derived inventory (the model-list endpoint)
	// record it at fill and miss when it moves, so no explicit
	// invalidation call sites are needed.
	generation uint64
	// fetchClock orders live fetches for one instance: BeginLiveFetchReg
	// bumps it, and a fetch mints its token at request start, so a slower
	// fetch that returns after a newer one began holds a lower token.
	// lastApplied holds the highest order actually applied per instance;
	// ReapplyLive lands only above it, so stale responses are discarded
	// while a failed newer fetch - which applies nothing - never blocks an
	// older in-flight success. It is deliberately NOT generation:
	// beginning a fetch changes nothing a reader can observe, and the
	// prefetch begins one per instance on every pass, so counting starts
	// as installs would defeat the model-list cache's TTL.
	fetchClock uint64
	// lastApplied holds the highest fetch order applied per instance.
	lastApplied map[string]uint64
	// lastGoodLive is the live snapshot of the most recent
	// successfully loaded registry, identity and all: a failed reload
	// parks the holder on the implicit-only fallback, which knows none
	// of the explicit instances, so carryLive alone would drop their
	// rows - the next successful Reload re-applies this snapshot
	// (identity-checked) instead.
	lastGoodLive map[string]liveSnapshot
	// incarnations counts, per instance name, how many successful
	// reloads have found that name in the file's instance set: a name
	// absent from the last successful snapshot and present again is a
	// new incarnation. A remove + re-add that keeps the name, endpoint,
	// and credentials leaves the endpoint identity byte-identical, so
	// only this counter distinguishes the replacement from the instance
	// a fetch began against. A failed reload's fallback install
	// deliberately does not count: recovery restores the same
	// incarnation's live rows, so it must not invalidate them.
	incarnations map[string]uint64
	// goodNames is the instance-name set of the last successful reload,
	// the reference incarnations counts against.
	goodNames map[string]bool
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
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	// The snapshot this reload starts from, read under the holder's own lock:
	// writers of current hold both locks, so reloadMu alone would exclude them
	// today - but then this read's safety would rest on every future commit
	// path remembering reloadMu. The fingerprints below describe the very
	// snapshots this reload commits.
	old := h.snapshot()
	oldIDs := instanceIdentities(old)
	r, _, err := h.load()
	if err != nil {
		fallback, _, ferr := h.load(registry.WithNoUserLayer())
		var fallbackIDs map[string]string
		if ferr == nil {
			fallbackIDs = instanceIdentities(fallback)
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.loadErr = err
		if ferr == nil {
			// A failed reload bumps no incarnations: the fallback is not a
			// successful snapshot, so the names it shares with old keep
			// their rows for the recovery that follows.
			carryLive(old, fallback, nil, oldIDs, fallbackIDs)
			h.current = fallback
			h.generation++
		}
		return err
	}
	// Fingerprinting reads credential material (a Codex record, the ADC
	// file): computed here, from snapshots no one else can see yet, so a
	// slow state root never stalls readers behind this commit's write lock.
	newIDs := instanceIdentities(r)
	h.mu.Lock()
	defer h.mu.Unlock()
	// Incarnations first: a name new to this snapshot is a new incarnation
	// (see bumpNewIncarnations), and rows an earlier registry holds for it —
	// a failed reload's fallback can carry some — belong to an incarnation
	// the file no longer has. Carrying them forward would hand the fresh
	// instance a dead one's inventory, which is the same staleness the
	// fetch tokens refuse; only the identity check cannot see it.
	recreated := h.bumpNewIncarnations(r)
	// A recreated name keeps no cached inventory at all: the snapshot a
	// failed reload's fallback recorded for that name belongs to the
	// incarnation the file no longer has, and leaving it here would let a
	// later recovery hand it to whatever instance takes the name next.
	for instance := range recreated {
		delete(h.lastGoodLive, instance)
	}
	carryLive(old, r, recreated, oldIDs, newIDs)
	// The fallback between a failure and its fix knew none of the
	// explicit instances: re-apply the last-good snapshot for every
	// instance whose identity still matches, so live-only ids survive
	// failed-reload recovery without leaking across a re-point.
	for instance, snap := range h.lastGoodLive {
		if recreated[instance] {
			continue
		}
		if _, ok := r.Instance(instance); !ok {
			continue
		}
		if newIDs[instance] != snap.identity {
			continue
		}
		r.ApplyLive(instance, snap.rows)
	}
	for instance, rows := range r.SnapshotLive() {
		if _, ok := r.Instance(instance); !ok {
			continue
		}
		h.noteLive(instance, newIDs[instance], rows)
	}
	// Drop snapshots for names the fresh registry no longer knows, or
	// whose identity changed: a later re-add under the same name must
	// not resurrect the old endpoint's rows.
	h.pruneLastGoodLive(r, newIDs)
	h.current, h.loadErr = r, nil
	h.generation++
	return nil
}

// bumpNewIncarnations bumps and returns the incarnation of every instance
// name the last successful snapshot did not have, so a fetch that began
// before a remove/re-add cannot publish onto the replacement and no other
// carry path hands that replacement the old incarnation's rows, and
// records r's names as the next reference. Call with the holder lock held.
func (h *ProviderRegistry) bumpNewIncarnations(r *registry.Registry) map[string]bool {
	instances := r.Instances()
	recreated := map[string]bool{}
	names := make(map[string]bool, len(instances))
	for _, inst := range instances {
		names[inst.Name] = true
		if h.goodNames[inst.Name] {
			continue
		}
		if h.incarnations == nil {
			h.incarnations = map[string]uint64{}
		}
		h.incarnations[inst.Name]++
		recreated[inst.Name] = true
	}
	h.goodNames = names
	return recreated
}

// instanceIdentities fingerprints every instance of r, or nil for a nil
// registry. Identity reads credential material, so callers take it from a
// snapshot they hold rather than under the holder's lock: a slow or
// network-mounted state root must not stall Get/Instances/List.
func instanceIdentities(r *registry.Registry) map[string]string {
	if r == nil {
		return nil
	}
	out := map[string]string{}
	for _, inst := range r.Instances() {
		out[inst.Name] = instanceIdentity(r, inst.Name)
	}
	return out
}

// pruneLastGoodLive deletes cached snapshots for instances absent
// from r or identity-mismatched with it, so a later re-add under the
// same name cannot resurrect the old endpoint's rows. Call with the
// holder lock held.
func (h *ProviderRegistry) pruneLastGoodLive(r *registry.Registry, ids map[string]string) {
	for instance, snap := range h.lastGoodLive {
		if _, ok := r.Instance(instance); !ok {
			delete(h.lastGoodLive, instance)
			continue
		}
		if ids[instance] != snap.identity {
			delete(h.lastGoodLive, instance)
		}
	}
}

// carryLive re-applies old's cached live listings onto r — but only
// for instances whose identity survived the swap. A removed, renamed,
// or re-pointed instance starts with no live rows rather than the old
// endpoint's, and recreated names this reload — a remove/re-add that kept
// the identity byte-identical — are skipped outright: a new incarnation
// starts empty however identical its transport looks. Both nil-safe;
// ApplyLive re-filters, so the round trip is idempotent.
func carryLive(old, r *registry.Registry, recreated map[string]bool, oldIDs, newIDs map[string]string) {
	if old == nil || r == nil {
		return
	}
	for instance, rows := range old.SnapshotLive() {
		if recreated[instance] {
			continue
		}
		if _, ok := old.Instance(instance); !ok {
			continue
		}
		if _, ok := r.Instance(instance); !ok {
			continue
		}
		if oldIDs[instance] != newIDs[instance] {
			continue
		}
		r.ApplyLive(instance, rows)
	}
}

// instanceIdentity fingerprints what a live listing is fetched from:
// the provider, the protocol and endpoint routing the listing's own
// resolve carries (the default row's, falling back to the provider's
// own shape — a models_endpoint, base_url, or row protocol change
// re-points the fetch all the same), and a non-secret fingerprint of
// the credential material behind the source label. A Reload that
// removes, renames, re-points, or re-credentials an instance changes
// its identity, and rows fetched from the old transport must not
// publish into the new one. Display fields (default, warnings, vars)
// do not affect where rows come from and are not part of it. The
// fingerprint hashes stable material (SHA-256, in-memory only) so even
// a same-length rotation changes the identity, while command-bearing
// material contributes its authored text — the minted value rotates
// with the cache TTL, and an identity that followed it would prune the
// cached live rows on every rollover and force a re-fetch. Only the
// digest enters the identity string, never the secrets.
func instanceIdentity(r *registry.Registry, name string) string {
	inst, ok := r.Instance(name)
	if !ok {
		return "unknown\x00" + name
	}
	endpoint := ""
	authprint := ""
	proto := inst.Protocol
	// The endpoint view the listing resolves through: row-aware and
	// mint-free, the same shape ResolveInstanceListing fetches with.
	if res, err := r.ResolveInstanceTransport(name); err == nil {
		endpoint = res.Transport.ModelsEndpoint
		proto = res.Protocol
		if fp, ok := r.AuthFingerprint(name); ok {
			authprint = fp
		}
		if res.Transport.Auth == registry.AuthOAuthOpenAICodex {
			authprint += "\x00" + oauthAccountFingerprint(r.StateRoot(), name)
		}
		if res.Transport.Auth == registry.AuthGCPADC {
			// A stored credential JSON outranks the ADC file (spec
			// §4.2) and already rotates the identity through
			// AuthFingerprint; the file's bytes count only when the
			// file is the material the launch would actually read.
			if pres, perr := r.ResolveInstancePresence(name); perr != nil || pres.Credential.Source != "store" {
				authprint += "\x00" + adcFingerprint()
			}
		}
	}
	return strings.Join([]string{inst.ProviderID, proto, inst.BaseURL, endpoint, inst.Auth, inst.CredentialSource, authprint}, "\x00")
}

// CredentialConfigRevision returns name's effective credential-configuration
// revision: a stable, keyed MAC over the configuration a conditional credential
// write is fenced against (design §07 "credential push"). It is derived from
// the same registry snapshot a credential write re-resolves under the
// credential lock, so a client that captured it from a read-only
// evener/auth/status or evener/instance/list answer can echo it back as
// ApiKeyConditionalSetParams.ExpectedRevision and have the host refuse the
// write when anything it covers has changed since. It covers the instance's
// structural identity (provider, protocol, surface), the endpoint it routes to
// (base URL and models endpoint), the credential source that resolves, and the
// credential-header names in force. It deliberately does NOT cover the secret
// value: it must not let a reader tell one stored key from another (design's
// "Honest limitation"), and every source transition it needs to fence — none
// to store, providers.toml, or the environment — is already a change to the
// resolved source.
//
// key is the hub-held secret the endpoint fingerprints are already keyed with
// (fingerprintWithKey): the MAC is what keeps a reader who can see the revision
// from recovering a secret the covered configuration carries. The destination
// half covers the base URL's userinfo and query string, which a listing strips
// because a short token or a password in either can be low-entropy, and an
// unkeyed digest of a guessable secret is a guessable function of it. An empty
// key (this hub could not resolve one) yields no revision, exactly as it yields
// no fingerprint; the credential write that would fence on it then refuses
// rather than reading the empty value as "no fence"
// (hubAuthController.verifyConfigRevision). Empty for a name the registry
// cannot resolve too: there is nothing to fence, and the empty value is what a
// client reads as "no revision fence" where the hub has no state root to key
// with at all.
func CredentialConfigRevision(key []byte, r *registry.Registry, name string) string {
	if len(key) == 0 || r == nil || strings.TrimSpace(name) == "" {
		return ""
	}
	// Presence depth: the revision covers the credential's source label
	// and the credential-header names, neither of which needs the value
	// materialized — and the status and listing paths that serve this
	// digest render on every pane refresh, where a full resolve could
	// execute a command expression (spec §10.1). Every path that feeds
	// CredentialConfigRevisionResolved resolves at this same depth, so
	// one configuration MACs to one revision wherever it is read.
	res, err := r.ResolveInstancePresence(name)
	if err != nil {
		return ""
	}
	return CredentialConfigRevisionResolved(key, res)
}

// DestinationIdentity is where a resolved instance's credential-bearing
// requests go, as one string: the base URL it resolves, the protocol that
// selects its request templates, and every request path those templates
// contribute. A credential-bearing request is built from exactly these, so a
// change to any of them moves where the secret is sent - and a change to the
// protocol or a path template can leave the sanitized URL a listing displays
// byte-identical, which is why neither consumer of this identity can work from
// that URL alone. Nothing secret-bearing is here: not the credential, not
// either header map, not vars.
//
// It has two consumers, and they must not drift: the listing's endpoint
// fingerprint digests a keyed MAC of it (the hub's own destinationFingerprint),
// and CredentialConfigRevisionResolved MACs it, with the same hub-held key,
// into the revision the credential push fences against. Sharing this one
// function is what keeps the revision covering every field the fingerprint
// covers.
func DestinationIdentity(resolved registry.Resolved) string {
	t := resolved.Transport
	return strings.Join([]string{
		strings.TrimSpace(t.BaseURL),
		resolved.Protocol,
		strings.TrimSpace(t.Endpoint),
		strings.TrimSpace(t.StreamEndpoint),
		strings.TrimSpace(t.ModelsEndpoint),
		strings.TrimSpace(t.CountTokensEndpoint),
	}, "\x00")
}

// CredentialConfigRevisionResolved is CredentialConfigRevision over an instance
// the caller has already resolved, so a caller that resolved it for another
// field of the same row - the instances listing's endpoint fingerprint, say -
// does not resolve the name a second time. It contributes exactly what the
// resolving form contributes: the same fields, in the same order, with the same
// separators, and the same keyed MAC, so the two agree byte for byte on the
// same resolution. An unresolved Resolved (no instance name) has no revision,
// matching the empty string CredentialConfigRevision returns for a name the
// registry cannot resolve or a hub key that is unavailable.
//
// The revision is the credential push's only no-clobber fence: the conditional
// set it calls checks no endpoint fingerprint of its own, so everything that
// decides where a stored key is sent has to be in here. DestinationIdentity
// carries the endpoint half of that (the same bytes the listing's fingerprint
// digests) and AuthHeader the rest, because the header a scheme writes decides
// both where the key goes and - when the instance authors that same header
// through credential_headers - whether the scheme derives from it at all.
//
// It is a keyed MAC, not a bare digest: a destination can carry a secret in a
// part a listing strips (base_url's userinfo or query string), and a reader who
// can see the revision could otherwise recover a low-entropy secret from it
// offline. key is the same hub-held secret the endpoint fingerprints use
// (fingerprintWithKey); an empty key yields no revision.
func CredentialConfigRevisionResolved(key []byte, res registry.Resolved) string {
	if len(key) == 0 || strings.TrimSpace(res.Instance) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "%s\x01%s\x01", "instance", res.Instance)
	_, _ = fmt.Fprintf(mac, "%s\x01%s\x01", "provider", res.ProviderID)
	_, _ = fmt.Fprintf(mac, "%s\x01%s\x01", "protocol", res.Protocol)
	_, _ = fmt.Fprintf(mac, "%s\x01%s\x01", "surface", res.Surface)
	_, _ = fmt.Fprintf(mac, "%s\x01%s\x01", "auth", res.Transport.Auth)
	_, _ = fmt.Fprintf(mac, "%s\x01%s\x01", "authHeader", res.Transport.AuthHeader)
	_, _ = fmt.Fprintf(mac, "%s\x01%s\x01", "destination", DestinationIdentity(res))
	_, _ = fmt.Fprintf(mac, "%s\x01%s\x01", "source", res.Credential.Source)
	// The authored layer that resolved to nothing is part of the configuration
	// even though it moves the source to "none": an instance that authors a
	// credential whose variables are unset is not the same instance as one that
	// authors nothing, and it is the difference between "a stored key would be
	// sent" and "a stored key is dead". The value is the layer's name, never its
	// variable or a secret.
	_, _ = fmt.Fprintf(mac, "%s\x01%s\x01", "authoredLayer", res.Credential.AuthoredLayer)
	_, _ = fmt.Fprintf(mac, "%s\x01%s\x01", "credentialHeaders", strings.Join(res.CredentialHeaderNames, ","))
	return hex.EncodeToString(mac.Sum(nil))
}

// oauthAccountFingerprint folds the Codex OAuth record's stable
// account-identity claims (email, account and workspace ids) into the
// fingerprint: swapping the signed-in account behind an unchanged record
// filename must change the identity even though the credential source
// label ("oauth") and the transport stay the same. Tokens are
// deliberately excluded — a refresh rotates them without changing whose
// account the listing belongs to, and the secret bytes are already
// covered by authFingerprint's value hash. Missing top-level fields
// fall back to the record's id_token claims, mirroring the OAuth
// request path (ParseIDTokenClaims + applyClaims order: top-level
// first, token claims fill the gaps): legacy records that predate the
// persisted fields still change identity on an account switch. A
// missing, unreadable, or identity-less record contributes nothing: the
// source label in the identity already distinguishes "no record" from
// "record".
func oauthAccountFingerprint(stateRoot, instance string) string {
	raw, err := os.ReadFile(authopenai.AuthFilePath(stateRoot, instance))
	if err != nil {
		return ""
	}
	var rec struct {
		Email       string `json:"email"`
		AccountID   string `json:"account_id"`
		WorkspaceID string `json:"workspace_id"`
		IDToken     string `json:"id_token"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return ""
	}
	email, account, workspace := rec.Email, rec.AccountID, rec.WorkspaceID
	if email == "" || account == "" || workspace == "" {
		if claims, err := authopenai.ParseIDTokenClaims(rec.IDToken); err == nil {
			if email == "" {
				email = claims.Email
			}
			if account == "" {
				account = claims.AccountID
			}
			if workspace == "" {
				workspace = claims.WorkspaceID
			}
		}
	}
	if email == "" && account == "" && workspace == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{email, account, workspace}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// adcFingerprint folds the ADC credential file's bytes (SHA-256,
// in-memory only) into the identity: an ADC account swap rewrites the
// file behind a stable source label ("adc", no credential value in the
// resolution), so without it rows fetched under the old account publish
// into the newly-credentialed instance. The caller decides whether the
// file is the material the launch reads at all — a stored credential
// JSON outranks it (spec §4.2). Missing/unreadable contributes
// nothing: the source label already distinguishes "no ADC" from "ADC".
func adcFingerprint() string {
	// No public accessor reaches the registry's injected env from
	// here: the hub runs with the real process environment, and that
	// lookup is what the authenticator resolves, so read it directly
	// (adcFilePath keeps the resolution order testable).
	return adcFileFingerprint(adcFilePath(os.Getenv, os.UserHomeDir))
}

// adcFilePath mirrors the file the registry's own adcAvailable consults
// — GOOGLE_APPLICATION_CREDENTIALS first, then the well-known gcloud
// path under HOME — the same order the GCP authenticator's
// FindDefaultCredentials resolves. lookup/homeDir are seams so tests
// pin the swap without touching the process environment.
func adcFilePath(lookup func(string) string, homeDir func() (string, error)) string {
	if p := lookup("GOOGLE_APPLICATION_CREDENTIALS"); p != "" {
		return p
	}
	if home, err := homeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	}
	return ""
}

// adcFileFingerprint hashes the ADC file at path (SHA-256, in-memory
// only). A missing path or unreadable file contributes nothing: the
// source label already distinguishes "no ADC" from "ADC".
func adcFileFingerprint(path string) string {
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Generation is the holder's monotonic mutation clock: every Reload
// (success or fallback install) and every landed live re-apply bumps
// it. Readers that cache derived inventory (the model-list endpoint)
// record it at fill and miss when it moves, so no explicit
// invalidation call sites are needed.
func (h *ProviderRegistry) Generation() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.generation
}

// snapshot returns the registry currently held, under the holder's own lock.
func (h *ProviderRegistry) snapshot() *registry.Registry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

// Get returns the registry currently held; nil before the first successful load.
func (h *ProviderRegistry) Get() *registry.Registry {
	return h.snapshot()
}

// BeginLiveFetchReg atomically pairs instance's fetch token with the
// registry snapshot the fetch must run against AND the endpoint
// identity that fetch queried: the client is built from the returned
// registry, so a Reload landing between the two cannot strand a
// new-generation token on an old-registry fetch (or vice versa), and
// ReapplyLive judges each fetch against its own identity — a newer
// begin cannot overwrite what an older in-flight fetch is checked
// against. The token also carries the name's incarnation, so a remove
// and re-add that leaves the endpoint identity unchanged still retires
// every fetch begun against the instance that is gone.
func (h *ProviderRegistry) BeginLiveFetchReg(instance string) (*registry.Registry, LiveToken, string) {
	h.mu.Lock()
	h.fetchClock++
	tok := LiveToken{order: h.fetchClock, incarnation: h.incarnations[instance]}
	reg := h.current
	h.mu.Unlock()
	// The endpoint identity reads credential material (a Codex record, the
	// ADC file), so it is taken OUTSIDE the lock: a slow or network-mounted
	// state root must not stall every Get/Instances/List behind it. It is
	// derived from the snapshot this fetch will run against, which is the
	// registry the token was paired with above.
	id := ""
	if reg != nil {
		id = instanceIdentity(reg, instance)
	}
	return reg, tok, id
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
func (h *ProviderRegistry) ReapplyLive(tok LiveToken, instance, identity string, rows []registry.Model) {
	// The identity check below reads credential material, so it is taken
	// outside the lock and re-judged if a Reload moves the snapshot in
	// between: only the install itself holds the lock.
	for {
		h.mu.RLock()
		reg := h.current
		h.mu.RUnlock()
		if reg == nil {
			return
		}
		// Judged against THIS fetch's endpoint identity — not the latest
		// recorded for the name. A remove/rename/re-point since the fetch
		// began drops its rows instead of planting the old transport's
		// listing on the new one, however many newer fetches began after.
		// An empty fetched identity (a registry that never knew the name,
		// as in the hermetic holder tests) matches an empty current one.
		if identity != instanceIdentity(reg, instance) {
			return
		}
		h.mu.Lock()
		if h.current != reg {
			// A Reload landed while the identity was being read: judge
			// against the snapshot that is current now.
			h.mu.Unlock()
			continue
		}
		if tok.order <= h.lastApplied[instance] {
			h.mu.Unlock()
			return
		}
		// The name was removed and recreated since this fetch began: a new
		// entry under the same name, endpoint, and credentials is a new
		// instance, so the old incarnation's rows must not publish onto it.
		if tok.incarnation != h.incarnations[instance] {
			h.mu.Unlock()
			return
		}
		if h.lastApplied == nil {
			h.lastApplied = map[string]uint64{}
		}
		h.lastApplied[instance] = tok.order
		reg.ApplyLive(instance, rows)
		h.noteLive(instance, identity, rows)
		h.generation++
		h.mu.Unlock()
		return
	}
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
