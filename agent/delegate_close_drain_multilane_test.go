package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// A close pass disposes every collectible lane. When the announcements of that
// disposal are published inside the per-lane loop, the first lane's announcement
// becomes a precondition for the second lane's removal: on a stalled
// authoritative stream it takes the slots the loop has left, and every lane after
// it reaches its turn with an expired cascade and stays on disk for a prune.
//
// The observable is the pair of lanes, not a duration. With a stream holding
// exactly the slots one lane's updates need and a consumer that never resumes, a
// per-lane publisher cannot remove the second lane, while a publisher that waits
// for the whole pass removes both. The durable closures are still written before
// their removals, so the record and the filesystem agree in both cases.
func TestCloseDisposalCollectsEveryLaneBeforeAnnouncingClosures(t *testing.T) {
	r := newWorktreeRepo(t)
	firstID, firstPath, _ := r.seedIsolationLane(t)
	secondID, secondPath, _ := r.seedIsolationLane(t)
	thirdID, thirdPath, _ := r.seedIsolationLane(t)
	root := r.s

	// Two slots for three lanes, and the consumer never resumes. Under a
	// per-lane publisher the early lanes' announcements fill the stream, and the
	// lane that arrives after them waits on a send that cannot land.
	root.events = make(chan events.SessionEvent, 2)
	root.authoritativeConsumer = true
	defer func() {
		root.authoritativeConsumer = false
		for {
			select {
			case <-root.events:
				continue
			default:
			}
			break
		}
	}()

	restoreBudget := SetLaneClosePassBudget(closeDrainCascadeBudget)
	defer restoreBudget()
	cascade, cancelCascade := ensureCloseBudget(context.Background())
	defer cancelCascade()
	// Publish the budget the way a close does. A short stop half, so a publisher
	// that parks on its announcement gives up quickly and leaves the remaining
	// lanes for the non-expiring tail, which does not remove them.
	root.closeCtxMu.Lock()
	root.closeCtx = cascade
	root.closeCtxMu.Unlock()

	root.disposeDelegateLanesAtClose(cascade)

	// Both lanes must be gone. Neither may be left on disk for a later prune,
	// because both were collectible when the pass began.
	if lanePresent(firstPath) {
		t.Fatalf("isolation lane %s was left on disk: the pass did not remove it", firstPath)
	}
	if lanePresent(secondPath) {
		t.Fatalf("isolation lane %s was left on disk: an earlier lane's closure announcement took the slots this lane's removal needed", secondPath)
	}
	if lanePresent(thirdPath) {
		t.Fatalf("isolation lane %s was left on disk: an earlier lane's closure announcement took the slots this lane's removal needed", thirdPath)
	}
	if !r.stableDisposalClosurePresent(t, firstID) {
		t.Fatalf("isolation lane %s was removed without a durable disposed mark", firstID)
	}
	if !r.stableDisposalClosurePresent(t, secondID) {
		t.Fatalf("isolation lane %s was removed without a durable disposed mark", secondID)
	}
	if !r.stableDisposalClosurePresent(t, thirdID) {
		t.Fatalf("isolation lane %s was removed without a durable disposed mark", thirdID)
	}
}
