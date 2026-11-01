//go:build browserguard

package hub

import (
	"context"
	"testing"
)

func TestComicGuardContextFollowsOwningTest(t *testing.T) {
	var owned context.Context
	var cancel context.CancelFunc
	t.Run("owner", func(t *testing.T) {
		owned, cancel = comicGuardContext(t)
		deadline, bounded := t.Deadline()
		got, hasDeadline := owned.Deadline()
		if hasDeadline != bounded {
			t.Fatalf("context deadline present = %v, test deadline present = %v", hasDeadline, bounded)
		}
		if bounded {
			// One browser join and four possible daemon stops: two seeded
			// helpers and the two real resume launches checked by the journey.
			want := deadline.Add(-backgroundDriverJoinReserve - 4*backgroundHelperStopWait)
			if !got.Equal(want) {
				t.Errorf("context deadline = %v, want owning deadline less cleanup reserves %v", got, want)
			}
		}
	})
	defer cancel()
	if owned.Err() != context.Canceled {
		t.Errorf("context after owning test ended = %v, want cancellation", owned.Err())
	}
}
