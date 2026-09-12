package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestFlagDaySavedReadDefaultsToBoundedItems(t *testing.T) {
	cfg, seeded := seedBoundedPastThread(t)
	params := appwire.ThreadReadParams{
		Ref: seeded.Ref, IncludeTurns: true,
	}
	response, found, err := pastThreadReadResponse(context.Background(), cfg, params)
	if err != nil {
		t.Fatalf("default saved item read: %v", err)
	}
	if !found {
		t.Fatal("default saved item read did not find seeded thread")
	}

	itemCount := 0
	for _, turn := range response.Thread.Turns {
		itemCount += len(turn.Items)
	}
	if itemCount > appwire.TranscriptItemPageLimit {
		t.Fatalf("default saved read returned %d items, want at most %d", itemCount, appwire.TranscriptItemPageLimit)
	}
	if response.OlderCursor == "" {
		t.Fatal("default saved item read omitted opaque older cursor for truncated history")
	}
	for turnIndex, turn := range response.Thread.Turns {
		for itemIndex, item := range turn.Items {
			if item.Position == nil {
				t.Fatalf("default saved item turn %d item %d has no position", turnIndex, itemIndex)
			}
			if item.TranscriptKey == "" {
				t.Fatalf("default saved item turn %d item %d has no transcript key", turnIndex, itemIndex)
			}
		}
	}
}
