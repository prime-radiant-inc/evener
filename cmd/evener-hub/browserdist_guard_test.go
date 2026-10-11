//go:build browserguard

package hub

import (
	"io/fs"
	"testing"
)

// requireFreshDist fails a browser guard when the embedded production
// frontend is missing or plausibly stale. The guard serves the built dist,
// not the source tree, so running `go test` after a source edit silently
// exercises yesterday's bundle. Rebuilding is cheap; a stale verdict is worse
// than none. The tagged guards are this helper's only callers, so it lives
// behind their build tag; the staleness logic it composes stays untagged
// where its unit tests run.
func requireFreshDist(t *testing.T) {
	t.Helper()
	if _, err := fs.ReadFile(distFS(), "index.html"); err != nil {
		t.Fatalf("production frontend is required, run make build-web: %v", err)
	}
	if err := distStalenessError(newestMtime(t, "frontend/dist"), buildInputNewest(t)); err != nil {
		t.Fatal(err)
	}
}
