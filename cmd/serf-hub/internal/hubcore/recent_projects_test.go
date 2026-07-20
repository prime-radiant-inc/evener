package hubcore

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
)

// TestPastIndex_RecentProjectDirs_DedupesGlobalRecencyLastN covers the session
// creation flows' recent-project source (issue #35): distinct working dirs in
// the index's most-recently-updated-first order, deduped on first (most
// recent) occurrence, blank dirs skipped, capped at the limit.
func TestPastIndex_RecentProjectDirs_DedupesGlobalRecencyLastN(t *testing.T) {
	idx := NewPastIndex("")
	now := time.Now().UTC()
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: now.Add(-1 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/alpha"}},
		{ID: "02wMz5Txv2enqVTitaig6F", UpdatedAt: now.Add(-2 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/beta"}},
		{ID: "02wMz5Txv47YP64RR3B9YJ", UpdatedAt: now.Add(-3 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: "  "}}, // blank — skipped
		{ID: "02wMz5Txv5aIxgf9yVdd0N", UpdatedAt: now.Add(-4 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/alpha"}}, // dup of /alpha, older — dropped
		{ID: "02wMz5Txv733WHFsVy66SR", UpdatedAt: now.Add(-5 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/gamma"}},
	})
	got := idx.RecentProjectDirs(15)
	want := []string{"/alpha", "/beta", "/gamma"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentProjectDirs(15) = %v, want %v", got, want)
	}
}

func TestPastIndex_RecentProjectDirs_CapsAtLimit(t *testing.T) {
	idx := NewPastIndex("")
	now := time.Now().UTC()
	metas := make([]schema.SessionMeta, 0, 20)
	for n := 0; n < 20; n++ {
		metas = append(metas, schema.SessionMeta{
			ID:        fmt.Sprintf("02wMz5Txv1C3Hut0M8GC%02d", n),
			UpdatedAt: now.Add(-time.Duration(n) * time.Minute),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: fmt.Sprintf("/proj-%02d", n)},
		})
	}
	idx.SeedForTest(metas)
	got := idx.RecentProjectDirs(15)
	if len(got) != 15 {
		t.Fatalf("RecentProjectDirs(15) returned %d dirs, want 15", len(got))
	}
	if got[0] != "/proj-00" || got[14] != "/proj-14" {
		t.Fatalf("RecentProjectDirs(15) = %v…%v, want /proj-00…/proj-14 (most recent first)", got[0], got[14])
	}
}

func TestPastIndex_RecentProjectDirs_EmptyIndexOrNonPositiveLimitReturnsNil(t *testing.T) {
	idx := NewPastIndex("")
	if got := idx.RecentProjectDirs(15); got != nil {
		t.Fatalf("RecentProjectDirs on empty index = %v, want nil", got)
	}
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: time.Now().UTC(), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/alpha"}},
	})
	if got := idx.RecentProjectDirs(0); got != nil {
		t.Fatalf("RecentProjectDirs(0) = %v, want nil", got)
	}
}
