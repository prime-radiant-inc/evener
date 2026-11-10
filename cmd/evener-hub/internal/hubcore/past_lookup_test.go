package hubcore

import (
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// An index with no state glob has nothing to scan: Rebuild reports no change
// and Find resolves nothing.
func TestPastIndexWithoutStateGlobIsEmpty(t *testing.T) {
	idx := NewPastIndex("")
	changed, err := idx.Rebuild()
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if changed {
		t.Fatal("Rebuild with no state glob reported a change")
	}
	if _, ok := idx.Find(hubtest.SessionID(t)); ok {
		t.Fatal("Find resolved a session with no state glob")
	}
}

func TestPastIndexFindRejectsAnInvalidID(t *testing.T) {
	idx := NewPastIndex("")
	// Seed the cache with the invalid key. If validation moves behind the cache
	// lookup, this fixture makes the test fail instead of reaching the same
	// false result through an empty index.
	idx.byID["not-a-valid-ulid"] = PastEntry{Meta: schema.SessionMeta{ID: "not-a-valid-ulid"}}
	if _, ok := idx.Find("not-a-valid-ulid"); ok {
		t.Fatal("Find admitted an invalid ID already present in the cache")
	}
}

func TestPastIndexRecentModelsWithoutALimitIsEmpty(t *testing.T) {
	if got := NewPastIndex("").RecentModels(0); got != nil {
		t.Fatalf("RecentModels(0) = %v, want nil", got)
	}
}

func TestPastIndexMergeSearchResultsOfNothingIsEmpty(t *testing.T) {
	if got := NewPastIndex("").mergeSearchResults(nil, nil, 1, 0); got != nil {
		t.Fatalf("mergeSearchResults(nil, nil) = %v, want nil", got)
	}
}

// A project with no sessions directory has nothing the index could have
// skipped, so it reports no skips.
func TestReportUnlistedMetasWithoutASessionsDir(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := fs.MkdirAll("/project", 0o755); err != nil {
		t.Fatal(err)
	}
	skipped := map[string]string{}
	reportUnlistedMetas(fs, "/project", map[string]bool{}, skipped)
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want none for a project with no sessions directory", skipped)
	}
}
