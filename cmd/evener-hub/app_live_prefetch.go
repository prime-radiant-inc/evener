package hub

import (
	"context"
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

// prefetchAllLiveModels fetches every visible instance's live listing into
// the held registry, concurrently so one slow endpoint cannot starve the
// rest. Best-effort: a failed instance keeps its catalog rows, and the pass
// never fails — the sheet reads whatever is cached.
func prefetchAllLiveModels(ctx context.Context, holder *hubcore.ProviderRegistry) {
	reg := holder.Get()
	if reg == nil {
		return
	}
	client := cmdutil.NewRegistryClient(reg, "")
	var wg sync.WaitGroup
	for _, inst := range reg.Instances() {
		if inst.Hidden {
			continue
		}
		wg.Go(func() {
			_ = fetchInstanceLiveWith(ctx, client, inst.Name)
		})
	}
	wg.Wait()
}

// startLiveModelsPrefetch warms the holder's live cache once at startup and
// refreshes it on livePrefetchInterval, so instance sheets read cached
// inventory instead of fetching on open. Failures are silent — the next
// tick retries — and cancellation stops the loop.
func startLiveModelsPrefetch(ctx context.Context, holder *hubcore.ProviderRegistry, interval time.Duration, startBackground func(func())) {
	startBackground(func() {
		prefetchAllLiveModels(ctx, holder)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				prefetchAllLiveModels(ctx, holder)
			}
		}
	})
}
