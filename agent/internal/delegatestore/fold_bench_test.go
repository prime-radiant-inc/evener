package delegatestore

import (
	"fmt"
	"testing"
	"time"
)

// BenchmarkFoldWideTree folds one root's journal of width direct children,
// the shape of the activity-tree fixtures that fill a 2000-unit work budget.
// Apply's per-event cost must scale with the aggregates the event touches,
// not with the number of delegates already folded.
func BenchmarkFoldWideTree(b *testing.B) {
	for _, width := range []int{100, 2000} {
		b.Run(fmt.Sprintf("width=%d", width), func(b *testing.B) {
			events := make([]Event, 0, width)
			for i := range width {
				event := createdEvent(fmt.Sprintf("dlg_wide_%d", i), "")
				event.TS = time.Unix(int64(i+1), 0).UTC()
				events = append(events, event)
			}
			events = sequence(events...)

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := Fold(events); err != nil {
					b.Fatalf("Fold: %v", err)
				}
			}
		})
	}
}
