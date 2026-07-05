package hubcore

import (
	"testing"
	"time"
)

func TestTreeCacheMemoizesByVersionAndBucket(t *testing.T) {
	cache := &TreeCache{}
	now := time.Unix(1_700_000_000, 0)
	calls := 0
	compute := func() (Tree, AttentionSummary) { calls++; return Tree{}, AttentionSummary{} }

	cache.Get(1, now, compute)
	cache.Get(1, now.Add(5*time.Second), compute) // same version, same 30s bucket
	if calls != 1 {
		t.Fatalf("same version+bucket should compute once, got %d", calls)
	}
	cache.Get(2, now, compute) // version bump busts
	if calls != 2 {
		t.Fatalf("version bump should recompute, got %d", calls)
	}
	cache.Get(2, now.Add(31*time.Second), compute) // next time bucket busts
	if calls != 3 {
		t.Fatalf("bucket roll should recompute, got %d", calls)
	}
}

func TestInputsVersionBump(t *testing.T) {
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
