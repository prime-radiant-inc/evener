package evener_test

import (
	"os"
	"strings"
	"testing"
)

// TestLazyTranscriptLoadingCardNamesThePagingTriggers pins the paging card to
// the mechanism the transcript actually runs. #2963 replaced LoadOlderRow's
// IntersectionObserver sentinel with two triggers: the near-top scroll rule
// every transcript surface's scroll coordinator runs, and the port-geometry
// fill (scrollMetrics.shouldAutoLoadOlder) for a page too short to fill its
// port. The card kept describing the removed sentinel and its 400px prefetch
// margin, pointing a reader at code that no longer exists (issue #3057).
func TestLazyTranscriptLoadingCardNamesThePagingTriggers(t *testing.T) {
	const card = "test/scenarios/lazy-transcript-loading.md"
	raw, err := os.ReadFile(card)
	if err != nil {
		t.Fatalf("reading %s: %v", card, err)
	}
	text := string(raw)
	// The sentinel and the observer that watched it are gone; a card that still
	// names one describes paging the code does not do.
	if strings.Contains(text, "IntersectionObserver") {
		t.Errorf("%s still names an IntersectionObserver, the paging sentinel "+
			"#2963 removed", card)
	}
	// Both triggers the current code runs, named plainly enough that a reader
	// can point at them.
	for _, want := range []string{"near-top", "too short to scroll"} {
		if !strings.Contains(text, want) {
			t.Errorf("%s must name the paging trigger %q; it described the removed "+
				"sentinel instead (issue #3057)", card, want)
		}
	}
}
