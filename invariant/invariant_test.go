//go:build !serffuzz

package invariant

import "testing"

// In a normal build (no serffuzz tag) invariants are compiled out: Enabled is
// false and Hold never panics, even on a false condition. This is the build
// `make test` and `go build ./...` use, so the assertions must be inert.
func TestProductionBuildIsInert(t *testing.T) {
	if Enabled {
		t.Fatal("Enabled must be false in a non-serffuzz build")
	}
	// A violated invariant must NOT panic without the tag; if this recovers a
	// value the no-op contract is broken.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Hold panicked in a production build: %v", r)
		}
	}()
	Hold(false, "this must not fire in production: %d", 7)
	Hold(true, "always holds")
}
