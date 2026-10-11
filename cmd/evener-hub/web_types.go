package hub

import (
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"primeradiant.com/evener/appwire"
)

// modelsCache is a per-WebServer TTL cache of the raw live model list. Provider
// configuration overlays are applied to fresh descriptors on each response.
type modelsCache struct {
	mu      sync.Mutex
	expires time.Time
	models  []appwire.ModelDescriptor
	// gen is the holder generation the cached descriptors were filled
	// at; any Reload or live re-apply bumps it and misses the cache.
	gen uint64
}

const liveModelsTTL = 5 * time.Minute

// launchModelsCache is a per-WebServer cache of the evener launch model list
// (the `evener launch-check --models` contract), keyed by the working directory
// that scopes it ("" for the unscoped list). The launch check spawns a child
// and re-lists every provider live, which takes seconds; serving the picker
// from this cache is what keeps model/list from paying that cost on every open.
type launchModelsCache struct {
	mu      sync.Mutex
	entries map[string]*launchModelsEntry
	// loading collapses concurrent loads of one working-dir key onto one launch
	// check: a burst of cold picker opens, or a read racing the startup warm,
	// would otherwise each spawn their own child and live provider listing.
	loading singleflight.Group
	// refreshing marks a key whose stale entry is already being re-fetched, so
	// a stale read does not start a second refresh behind the first — the guard
	// singleflight's own coalescing cannot give, because a later reader's
	// goroutine may reach the group only after the earlier flight cleared.
	refreshing map[string]bool
}

type launchModelsEntry struct {
	resp     appwire.ModelListResponse
	gen      uint64
	filledAt time.Time
}

// launchModelsMaxEntries bounds the working-dir keys one server caches: the
// spawn pane scopes its list by the directory being typed, so the key space is
// user-driven. The oldest entry is evicted past the cap.
const launchModelsMaxEntries = 64
