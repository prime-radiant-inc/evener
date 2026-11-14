//go:build ignore

// catalog-retention measures offline registry construction and retained heap.
// Run from the repository root: go run /absolute/path/catalog-retention.go
// It uses only the embedded catalog, never user configuration or network access.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"primeradiant.com/evener/llm/registry"
)

type checkpoint struct {
	Phase           string `json:"phase"`
	Registries      int    `json:"registries_held"`
	Providers       int    `json:"providers_per_registry"`
	HeapAlloc       uint64 `json:"heap_alloc_bytes"`
	HeapObjects     uint64 `json:"heap_objects"`
	TotalAlloc      uint64 `json:"total_alloc_bytes"`
	LoadNanoseconds int64  `json:"last_load_wall_ns,omitempty"`
}

func record(phase string, held []*registry.Registry, loadTime time.Duration, gc bool) {
	if gc {
		runtime.GC()
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	providers := 0
	if len(held) != 0 {
		providers = len(held[0].ProviderIDs())
	}
	if err := json.NewEncoder(os.Stdout).Encode(checkpoint{phase, len(held), providers, stats.HeapAlloc, stats.HeapObjects, stats.TotalAlloc, int64(loadTime)}); err != nil {
		panic(err)
	}
	runtime.KeepAlive(held)
}

func load() (*registry.Registry, time.Duration) {
	start := time.Now()
	r, err := registry.Load(registry.WithNoUserLayer(), registry.WithOffline(true), registry.WithoutCache(), registry.WithEnv(func(string) (string, bool) { return "", false }))
	if err != nil {
		panic(err)
	}
	return r, time.Since(start)
}

func measure() {
	held := make([]*registry.Registry, 0, 8)
	record("before_load_post_gc", held, 0, true)
	for i := range 8 {
		r, elapsed := load()
		held = append(held, r)
		if i == 0 {
			record("first_load_before_forced_gc", held, elapsed, false)
		}
		if i == 0 || i == 1 || i == 7 {
			record(fmt.Sprintf("holding_%d_post_gc", len(held)), held, elapsed, true)
		}
	}
	runtime.KeepAlive(held)
}

func main() {
	measure()
	record("released_post_gc", nil, 0, true)
}
