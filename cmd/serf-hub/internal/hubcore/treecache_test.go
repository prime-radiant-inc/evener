package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
)

func fuzzScenarioTreeCacheMemoizesByVersionAndBucket(t *testing.T) {
	cache := &TreeCache{}
	now := time.Unix(1_700_000_000, 0)
	calls := 0
	compute := func() TreeCacheValue {
		calls++
		return TreeCacheValue{
			Tree:             Tree{Live: []TreeNode{{ID: "cached"}}},
			AttentionSummary: appwire.AttentionSummary{Working: calls},
			Live:             []LiveEntry{{SessionID: "cached"}},
			FavoriteAuthority: FavoriteAuthority{Sessions: []FavoriteSessionAuthority{{
				ID: "cached", TopLevel: true,
				Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete,
			}}},
		}
	}

	first := cache.Get(TreeCacheKey{InputsVersion: 1}, now, compute)
	second := cache.Get(TreeCacheKey{InputsVersion: 1}, now.Add(5*time.Second), compute) // same version, same 30s bucket
	if calls != 1 {
		t.Fatalf("same version+bucket should compute once, got %d", calls)
	}
	if second.Tree.Live[0].ID != first.Tree.Live[0].ID || second.Live[0].SessionID != first.Live[0].SessionID || second.FavoriteAuthority.Sessions[0].ID != first.FavoriteAuthority.Sessions[0].ID {
		t.Fatalf("same version+bucket did not return one cached composite value: first=%+v second=%+v", first, second)
	}
	cache.Get(TreeCacheKey{InputsVersion: 2}, now, compute) // version bump busts
	if calls != 2 {
		t.Fatalf("version bump should recompute, got %d", calls)
	}
	cache.Get(TreeCacheKey{InputsVersion: 2, RemoteGeneration: 1}, now, compute) // remote generation busts
	if calls != 3 {
		t.Fatalf("remote generation change should recompute, got %d", calls)
	}
	cache.Get(TreeCacheKey{InputsVersion: 2, RemoteGeneration: 2}, now, compute) // independently advancing remote generation remains distinct
	if calls != 4 {
		t.Fatalf("second remote generation should recompute, got %d", calls)
	}
	cache.Get(TreeCacheKey{InputsVersion: 2, RemoteGeneration: 2}, now.Add(31*time.Second), compute) // next time bucket busts
	if calls != 5 {
		t.Fatalf("bucket roll should recompute, got %d", calls)
	}
}

func fuzzScenarioInputsVersionBump(t *testing.T) {
	iv := &InputsVersion{}
	if iv.Load() != 0 {
		t.Fatal("fresh version should be 0")
	}
	iv.Bump()
	iv.Bump()
	if iv.Load() != 2 {
		t.Fatalf("want 2 after two bumps, got %d", iv.Load())
	}
}
