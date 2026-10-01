package hub

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

// instanceLiveListTimeout bounds one instance's live /models fetch, the
// same per-instance budget launch-check and the model picker use.
const instanceLiveListTimeout = 8 * time.Second

// fetchInstanceLive fetches one instance's live listing into the holder,
// so later InstanceModels calls include the live ids. It is the shared
// core behind the manual refresh RPC and the background prefetch below.
// The fetch never writes the registry itself: ListLive returns the raw
// rows and ReapplyLive publishes them only while this fetch's token —
// minted at request start — is still current. A Reload landing mid-fetch
// swaps in a fresh object (whose carryLive only knows the before
// snapshot); the re-apply carries the listing forward instead of losing
// it on the detached registry. An unusable provider result publishes an
// empty snapshot so registry reads take the static fallback. Caller
// cancellation leaves the last healthy snapshot untouched.
func fetchInstanceLive(ctx context.Context, holder *hubcore.ProviderRegistry, auth *hubAuthController, name string) error {
	// The listing sends the instance's credential, so what the provider
	// answers is recorded as a probe of it (#3539). The probe begins before
	// the snapshot below is taken, since that is where the credential is read.
	probe := auth.beginCredentialProbe(name)
	// Paired atomically: the client is built from the same snapshot the
	// token belongs to, so no Reload can slip between the two.
	reg, tok, id := holder.BeginLiveFetchReg(name)
	if reg == nil {
		return nil
	}
	if reg.LaunchMintsCredentialCommand(name) {
		// The hub never executes a credential command (spec §10.1): an
		// instance whose launch sends command material has no fetchable
		// live listing here, and the holder keeps its last-known rows.
		return nil
	}
	// Call-scoped authenticator: this fetch's requests bind a Codex
	// value carrying reg's state root, so no global rewire — from any
	// concurrent fetch, probe, or model-list RPC — can move the
	// records under it. No lock is held across the request: fetches
	// for different instances run fully concurrently again.
	fetchCtx := withScopedCodexAuth(ctx, reg)
	rows, ok, err := fetchInstanceLiveWith(fetchCtx, newLiveClient(reg), name)
	auth.settleCredentialProbe(probe, llm.ModelListing{Live: ok}, err)
	if err != nil {
		if ctx.Err() == nil {
			holder.ReapplyLive(tok, name, id, nil)
		}
		return err
	}
	if !ok {
		holder.ReapplyLive(tok, name, id, nil)
		return nil
	}
	holder.ReapplyLive(tok, name, id, rows)
	return nil
}

// withScopedCodexAuth binds the call-scoped Codex authenticator for one
// registry snapshot: every hub path that issues a registry-client request
// — prefetch fetches, the manual refresh, the credential-test probe, the
// model-list loader — carries it, so the request reads the OAuth record
// under THAT registry's state root instead of whatever root the
// process-global Codex happens to hold. A nil registry (a bare client in
// a test) keeps the global lookup: there is no root to scope to.
func withScopedCodexAuth(ctx context.Context, reg *registry.Registry) context.Context {
	if reg == nil {
		return ctx
	}
	return llm.WithAuthenticatorOverride(ctx, tokenauth.ScopedCodex(reg.StateRoot()))
}

// LiveRegistryClient builds a registry client for one fetch without
// touching process globals: the request's authenticator travels on its
// context (see fetchInstanceLive), and the build User-Agent is stamped
// once at hub startup (see runMain), not per request. Every hub path
// that builds a registry client — prefetch fetches, the
// credential-test probe, the model-list loader — comes through here,
// so no fetch-path write can race a Codex request's read.
func LiveRegistryClient(reg *registry.Registry) *llm.Client {
	return llm.NewClient(llm.WithRegistry(reg), llm.WithClientStateDir(""))
}

// newLiveClient builds a registry client for one fetch (see
// LiveRegistryClient): a thin alias kept for call-site readability.
func newLiveClient(reg *registry.Registry) *llm.Client {
	return LiveRegistryClient(reg)
}

// fetchInstanceLiveWith is fetchInstanceLive against a caller-supplied
// client. Each fetch builds its own client from its own registry
// snapshot (see the call sites): snapshots differ per fetch, so one
// pass cannot share a single client. It never writes the client's
// registry: the caller publishes the raw rows through the holder's
// token-validated path.
func fetchInstanceLiveWith(ctx context.Context, client *llm.Client, name string) ([]registry.Model, bool, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, instanceLiveListTimeout)
	defer cancel()
	rows, ok, err := client.ListLive(fetchCtx, name)
	if err != nil || !ok {
		return rows, ok, err
	}
	return rows, client.LiveListingUsable(name, rows), nil
}

// visibleModelFacts snapshots the full observable facts behind the
// visible ids — id, disabled flag, and every advertised live fact —
// so a capability-only change (same ids, new tools/context/cost)
// still counts as a change. encoding/json on the structs the sheet
// and picker read keeps the compare honest without hand-rolling
// field lists that rot.
func visibleModelFacts(reg *registry.Registry, name string) string {
	models, err := reg.InstanceModels(name)
	if err != nil {
		return ""
	}
	live := reg.LiveModels(name)
	raw, err := json.Marshal(struct {
		Models []registry.InstanceModel `json:"models"`
		Live   []registry.Model         `json:"live"`
	}{Models: models, Live: live})
	if err != nil {
		return ""
	}
	return string(raw)
}

// prefetchAllLiveModels fetches every visible instance's live listing into
// the held registry, concurrently so one slow endpoint cannot starve the
// rest. Best-effort: a failed instance keeps its catalog rows, and the pass
// never fails — the sheet reads whatever is cached. changed runs once when
// at least one instance's visible listing differs from its before snapshot,
// so the caller broadcasts once per pass instead of per row.
func prefetchAllLiveModels(ctx context.Context, holder *hubcore.ProviderRegistry, auth *hubAuthController, changed func()) {
	names := []string{}
	before := map[string]string{}
	// ONE snapshot for both: reading the holder twice would let a Reload
	// land between the reads, pairing a name set from one generation with
	// before-facts from another — a just-created instance would be skipped
	// by the pass, and a re-pointed one compared against a stale
	// before, which reads as a change and broadcasts spuriously.
	if reg := holder.Get(); reg != nil {
		for _, inst := range reg.Instances() {
			if !inst.Hidden {
				names = append(names, inst.Name)
			}
		}
		for _, name := range names {
			before[name] = visibleModelFacts(reg, name)
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	anyChanged := false
	for _, name := range names {
		wg.Go(func() {
			// The same fetch core the manual refresh uses: paired snapshot,
			// call-scoped Codex value (so this goroutine's requests carry
			// their own), and a token-validated publish. Failures are this
			// pass's normal case — a dead endpoint keeps its catalog rows.
			_ = fetchInstanceLive(ctx, holder, auth, name)
			if before[name] != visibleModelFacts(holder.Get(), name) {
				mu.Lock()
				anyChanged = true
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if anyChanged {
		changed()
	}
}

// startLiveModelsPrefetch warms the holder's live cache once at startup, so
// instance sheets read cached inventory instead of fetching on open. It runs
// once and never on a timer: the hub lists a provider only when someone asks
// (a refresh, the picker) or at startup, never by polling. A pass that changes
// what any client shows announces it once, so every browser refetches; a
// no-op pass stays silent. Failures are silent.
func startLiveModelsPrefetch(ctx context.Context, holder *hubcore.ProviderRegistry, auth *hubAuthController, startBackground func(func()), changed func()) {
	startBackground(func() {
		prefetchAllLiveModels(ctx, holder, auth, changed)
	})
}
