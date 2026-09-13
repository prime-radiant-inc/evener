package hub

import (
	"context"
	"slices"
	"sync"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// livePrefetchInterval is how often the background loop refreshes every
// instance's cached live listing: the model picker's own live cache TTL.
const livePrefetchInterval = liveModelsTTL

// instanceLiveListTimeout bounds one instance's live /models fetch, the
// same per-instance budget launch-check and the model picker use.
const instanceLiveListTimeout = 8 * time.Second

// fetchInstanceLive fetches one instance's live listing into reg, so later
// InstanceModels calls include the live ids. It is the shared core behind
// the manual refresh RPC and the background prefetch below.
func fetchInstanceLive(ctx context.Context, reg *registry.Registry, name string) error {
	return fetchInstanceLiveWith(ctx, cmdutil.NewRegistryClient(reg, ""), name)
}

// fetchInstanceLiveWith is fetchInstanceLive against a caller-supplied
// client, so one prefetch pass shares a single client instead of building
// one per instance.
func fetchInstanceLiveWith(ctx context.Context, client *llm.Client, name string) error {
	fetchCtx, cancel := context.WithTimeout(ctx, instanceLiveListTimeout)
	defer cancel()
	_, err := client.Models(fetchCtx, name)
	return err
}

// visibleModelIDs snapshots an instance's currently visible model ids: the
// same ids the sheet renders, so a before/after compare says whether the
// pass changed what any client shows.
func visibleModelIDs(reg *registry.Registry, name string) []string {
	ids, err := reg.ModelIDs(name)
	if err != nil {
		return nil
	}
	return ids
}

// prefetchAllLiveModels fetches every visible instance's live listing into
// the held registry, concurrently so one slow endpoint cannot starve the
// rest. Best-effort: a failed instance keeps its catalog rows, and the pass
// never fails — the sheet reads whatever is cached. changed runs once when
// at least one instance's visible listing differs from its before snapshot,
// so the caller broadcasts once per pass instead of per row.
func prefetchAllLiveModels(ctx context.Context, holder *hubcore.ProviderRegistry, changed func()) {
	reg := holder.Get()
	if reg == nil {
		return
	}
	client := cmdutil.NewRegistryClient(reg, "")
	var wg sync.WaitGroup
	var mu sync.Mutex
	anyChanged := false
	for _, inst := range reg.Instances() {
		if inst.Hidden {
			continue
		}
		before := visibleModelIDs(reg, inst.Name)
		wg.Go(func() {
			if err := fetchInstanceLiveWith(ctx, client, inst.Name); err != nil {
				return
			}
			if !slices.Equal(before, visibleModelIDs(reg, inst.Name)) {
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

// startLiveModelsPrefetch warms the holder's live cache once at startup and
// refreshes it on livePrefetchInterval, so instance sheets read cached
// inventory instead of fetching on open. A pass that changes what any
// client shows announces it once, so every browser refetches; a no-op pass
// stays silent. Failures are silent — the next tick retries — and
// cancellation stops the loop.
func startLiveModelsPrefetch(ctx context.Context, holder *hubcore.ProviderRegistry, interval time.Duration, startBackground func(func()), changed func()) {
	startBackground(func() {
		prefetchAllLiveModels(ctx, holder, changed)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				prefetchAllLiveModels(ctx, holder, changed)
			}
		}
	})
}
