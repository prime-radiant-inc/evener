package appprojector

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func TestDelegateActivityAfterEmptyCandidate(t *testing.T) {
	if delegateActivityAfter("", "2024-01-01T00:00:00Z") {
		t.Fatal("empty candidate should return false")
	}
}

func TestDelegateActivityAfterEmptyCurrent(t *testing.T) {
	if !delegateActivityAfter("2024-01-01T00:00:00Z", "") {
		t.Fatal("non-empty candidate with empty current should return true")
	}
}

func TestDelegateActivityAfterNewerCandidate(t *testing.T) {
	if !delegateActivityAfter("2024-01-02T00:00:00Z", "2024-01-01T00:00:00Z") {
		t.Fatal("newer candidate should return true")
	}
}

func TestDelegateActivityAfterOlderCandidate(t *testing.T) {
	if delegateActivityAfter("2024-01-01T00:00:00Z", "2024-01-02T00:00:00Z") {
		t.Fatal("older candidate should return false")
	}
}

func TestDelegateActivityAfterInvalidTimestamp(t *testing.T) {
	if delegateActivityAfter("not-a-timestamp", "2024-01-01T00:00:00Z") {
		t.Fatal("invalid candidate should return false")
	}
	if delegateActivityAfter("2024-01-01T00:00:00Z", "not-a-timestamp") {
		t.Fatal("invalid current should return false")
	}
}

func TestDelegateActivityAfterSameTimestamp(t *testing.T) {
	ts := "2024-01-01T00:00:00Z"
	if delegateActivityAfter(ts, ts) {
		t.Fatal("same timestamp should return false (not strictly after)")
	}
}

func TestMergeAppwireDelegateInfoNewIncoming(t *testing.T) {
	current := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 2, Status: "running"}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("higher projection revision should trigger merge")
	}
	if merged.ProjectionRevision != 2 || merged.Status != "running" {
		t.Fatal("should merge incoming fields")
	}
}

func TestMergeAppwireDelegateInfoSameRevisionNewerActivity(t *testing.T) {
	current := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-02T00:00:00Z"}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-03T00:00:00Z"}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("newer activity at same revision should trigger update")
	}
	if merged.LatestActivityAt != "2024-01-03T00:00:00Z" {
		t.Fatalf("should use newer activity timestamp, got %q", merged.LatestActivityAt)
	}
}

func TestMergeAppwireDelegateInfoNoChange(t *testing.T) {
	current := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-02T00:00:00Z"}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-01T00:00:00Z"}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if changed {
		t.Fatal("older activity at same revision should not trigger update")
	}
	if merged.LatestActivityAt != "2024-01-02T00:00:00Z" {
		t.Fatal("should keep current activity timestamp")
	}
}

func TestMergeAppwireDelegateInfoEmptyCurrent(t *testing.T) {
	current := appwire.EvenerDelegateInfo{}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("empty current DelegateID should trigger merge")
	}
	if merged.DelegateID != "dlg_1" {
		t.Fatal("should use incoming")
	}
}

// The drain hold is its own facet, outside the projection-revision ladder:
// entering or leaving the drain appends no durable delegate event and moves no
// durable field, and the hold freezes LatestActivityAt for its whole span, so
// both edges arrive at the revision already recorded. Before this the merge
// returned changed=false and the projector dropped the frame, which is exactly
// the update a loaded row needs.
func TestMergeAppwireDelegateInfoHoldAtUnchangedRevision(t *testing.T) {
	current := appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_1",
		ProjectionRevision: 4,
		Status:             "running",
		LatestActivityAt:   "2024-01-01T00:00:00Z",
	}
	held := appwire.EvenerDelegateInfo{
		DelegateID:                "dlg_1",
		ProjectionRevision:        4,
		Status:                    "running",
		LatestActivityAt:          "2024-01-01T00:00:00Z",
		DrainHeldByJobs:           true,
		RunningJobCount:           1,
		OldestRunningJobStartedAt: "2024-01-01T00:00:00Z",
	}
	merged, changed := mergeAppwireDelegateInfo(current, held)
	if !changed {
		t.Fatal("the hold at an unchanged revision was dropped; a loaded row would never learn the wait")
	}
	if !merged.DrainHeldByJobs || merged.RunningJobCount != 1 || merged.OldestRunningJobStartedAt == "" {
		t.Fatalf("merged hold = %v, count = %d, oldest = %q; want the frame's hold",
			merged.DrainHeldByJobs, merged.RunningJobCount, merged.OldestRunningJobStartedAt)
	}

	// The release edge, at the same revision again: the row must stop
	// claiming the wait rather than keeping the last hold forever.
	released, changed := mergeAppwireDelegateInfo(merged, current)
	if !changed {
		t.Fatal("the release at an unchanged revision was dropped; the row would keep claiming the wait")
	}
	if released.DrainHeldByJobs || released.RunningJobCount != 0 || released.OldestRunningJobStartedAt != "" {
		t.Fatalf("released hold = %v, count = %d, oldest = %q; want all three cleared",
			released.DrainHeldByJobs, released.RunningJobCount, released.OldestRunningJobStartedAt)
	}

	// A frame that repeats the hold states nothing new.
	if _, changed := mergeAppwireDelegateInfo(held, held); changed {
		t.Fatal("a repeated hold frame published an update")
	}
}

// The hold is ordered by the source's own sequence, not by arrival. A publisher
// that sampled the hold and then paused can deliver an older sample after a
// newer one; the row must keep the newer sample.
func TestMergeAppwireDelegateInfoOrdersTheHoldBySourceSequence(t *testing.T) {
	base := appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_1",
		ProjectionRevision: 4,
		Status:             "running",
		LatestActivityAt:   "2024-01-01T00:00:00Z",
	}
	held := base
	held.DrainHeldByJobs = true
	held.RunningJobCount = 2
	held.OldestRunningJobStartedAt = "2024-01-01T00:00:00Z"
	held.HoldSequence = 9

	merged, changed := mergeAppwireDelegateInfo(base, held)
	if !changed || !merged.DrainHeldByJobs || merged.HoldSequence != 9 {
		t.Fatalf("the held sample did not land: changed = %v, held = %v, sequence = %d; want the source's 9",
			changed, merged.DrainHeldByJobs, merged.HoldSequence)
	}

	// A sample captured before the hold, delivered after it: a read that sampled
	// this delegate before its drain started cannot clear the wait.
	delayed := base
	delayed.HoldSequence = 3
	kept, changed := mergeAppwireDelegateInfo(merged, delayed)
	if changed {
		t.Fatal("a sample older than the hold cleared it; the row would lose the wait for its whole duration")
	}
	if !kept.DrainHeldByJobs || kept.HoldSequence != 9 {
		t.Fatalf("after the delayed sample: held = %v, sequence = %d; want the 9 the source assigned",
			kept.DrainHeldByJobs, kept.HoldSequence)
	}

	// The release edge carries a later sequence than the hold it ends, so the
	// same rule that rejected the stale sample still lets the release through.
	released := base
	released.HoldSequence = 11
	done, changed := mergeAppwireDelegateInfo(merged, released)
	if !changed || done.DrainHeldByJobs || done.HoldSequence != 11 {
		t.Fatalf("the release did not land: changed = %v, held = %v, sequence = %d; want the source's 11",
			changed, done.DrainHeldByJobs, done.HoldSequence)
	}
}

// A newer projection revision does not mean a newer hold sample. A shell
// attention plan can sample the hold at a new revision and pause; an older
// heartbeat plan then samples a reduced count after another shell finished;
// the heartbeat lands first and the newer-revision frame arrives second
// carrying the older sample. Durable fields follow the revision and the hold
// follows the sequence, so the durable update lands without rolling the wait
// back.
func TestMergeAppwireDelegateInfoKeepsTheNewerHoldAcrossAHigherRevision(t *testing.T) {
	// The heartbeat's sample: two shells running, sampled later.
	current := appwire.EvenerDelegateInfo{
		DelegateID:                "dlg_1",
		ProjectionRevision:        4,
		Status:                    "running",
		LatestActivityAt:          "2024-01-01T00:00:00Z",
		DrainHeldByJobs:           true,
		RunningJobCount:           2,
		OldestRunningJobStartedAt: "2024-01-01T00:00:00Z",
		HoldSequence:              12,
	}
	// One shell finished: the frame carries the reduced count at a higher
	// revision, but its hold was sampled before the heartbeat's.
	incoming := current
	incoming.ProjectionRevision = 5
	incoming.Status = "resumable"
	incoming.RunningJobCount = 1
	incoming.HoldSequence = 10

	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("the durable update at a higher revision was dropped")
	}
	if merged.Status != "resumable" || merged.ProjectionRevision != 5 {
		t.Fatalf("durable fields = status %q, revision %d; want the higher-revision snapshot",
			merged.Status, merged.ProjectionRevision)
	}
	if merged.RunningJobCount != 2 || merged.HoldSequence != 12 {
		t.Fatalf("hold = count %d, sequence %d; want the later sample's count 2 and sequence 12",
			merged.RunningJobCount, merged.HoldSequence)
	}
}

// A strictly newer hold sequence is news even when the displayed fields match
// what the projector already holds. A delegates read can sample a short hold
// before any held frame reaches the projector; when the shell finishes and the
// clear arrives at the same revision and activity, the fields match the unheld
// values the projector started with — so discarding the frame would leave a
// browser that saw the wait showing it forever.
func TestMergeAppwireDelegateInfoForwardsANewerHoldSequenceWithIdenticalFields(t *testing.T) {
	unheld := appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_1",
		ProjectionRevision: 4,
		Status:             "running",
		LatestActivityAt:   "2024-01-01T00:00:00Z",
	}
	// The web row learned of the wait from a read, not from this projector.
	clearedHold := unheld
	clearedHold.HoldSequence = 20

	merged, changed := mergeAppwireDelegateInfo(unheld, clearedHold)
	if !changed {
		t.Fatal("a newer hold sequence with identical displayed fields was dropped; a client that saw the wait could never clear it")
	}
	if merged.HoldSequence != 20 {
		t.Fatalf("forwarded sequence = %d, want 20", merged.HoldSequence)
	}
	if merged.DrainHeldByJobs || merged.RunningJobCount != 0 {
		t.Fatalf("forwarded hold = %v, count = %d; want the cleared sample", merged.DrainHeldByJobs, merged.RunningJobCount)
	}

	// An exact duplicate is still suppressed: nothing changed, nothing to send.
	if _, changed := mergeAppwireDelegateInfo(merged, clearedHold); changed {
		t.Fatal("an exact duplicate hold frame published an update")
	}
}

// A coordinator parked in its finalization drain on open descendant subagents
// reports that wait instead of its frozen pre-drain intent. The wait is a hold
// sample like the job hold: it must survive the unchanged-revision merge, clear
// on release, and obey the source sequence rather than arrival order. Before
// the fields were carried through the projector the frame was dropped, so a
// loaded row kept the intent it had when the coordinator parked.
func TestMergeAppwireDelegateInfoSubagentHoldRidesTheSameSequence(t *testing.T) {
	base := appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_1",
		ProjectionRevision: 4,
		Status:             "running",
		LatestActivityAt:   "2024-01-01T00:00:00Z",
	}
	held := base
	held.DrainHeldBySubagents = true
	held.RunningSubagentCount = 2
	held.HoldSequence = 7

	merged, changed := mergeAppwireDelegateInfo(base, held)
	if !changed {
		t.Fatal("the subagent hold at an unchanged revision was dropped; a loaded row would never learn the wait")
	}
	if !merged.DrainHeldBySubagents || merged.RunningSubagentCount != 2 || merged.HoldSequence != 7 {
		t.Fatalf("merged subagent hold = %v, count = %d, sequence = %d; want the source's two-subagent sample at 7",
			merged.DrainHeldBySubagents, merged.RunningSubagentCount, merged.HoldSequence)
	}

	// One descendant finished: the same hold with a smaller count and a later
	// sequence. It is news, and it must replace the count.
	one := base
	one.DrainHeldBySubagents = true
	one.RunningSubagentCount = 1
	one.HoldSequence = 8
	reduced, changed := mergeAppwireDelegateInfo(merged, one)
	if !changed || reduced.RunningSubagentCount != 1 || reduced.HoldSequence != 8 {
		t.Fatalf("the reduced subagent hold did not land: changed = %v, count = %d, sequence = %d; want count 1 at 8",
			changed, reduced.RunningSubagentCount, reduced.HoldSequence)
	}

	// A sample captured before the hold, delivered after it, cannot clear it.
	stale := base
	stale.HoldSequence = 3
	kept, changed := mergeAppwireDelegateInfo(reduced, stale)
	if changed {
		t.Fatal("a sample older than the subagent hold cleared it; the row would lose the wait for its whole duration")
	}
	if !kept.DrainHeldBySubagents || kept.RunningSubagentCount != 1 {
		t.Fatalf("after the stale sample: hold = %v, count = %d; want the held sample kept",
			kept.DrainHeldBySubagents, kept.RunningSubagentCount)
	}

	// The release carries a later sequence than the hold it ends.
	released := base
	released.HoldSequence = 12
	done, changed := mergeAppwireDelegateInfo(reduced, released)
	if !changed || done.DrainHeldBySubagents || done.RunningSubagentCount != 0 {
		t.Fatalf("the subagent release did not land: changed = %v, hold = %v, count = %d; want it cleared",
			changed, done.DrainHeldBySubagents, done.RunningSubagentCount)
	}

	// A strictly newer sequence is news even when the displayed fields match the
	// cleared ones, so a client that saw the wait from a read can clear it.
	newerSequence := base
	newerSequence.HoldSequence = 13
	if _, changed := mergeAppwireDelegateInfo(done, newerSequence); !changed {
		t.Fatal("a newer subagent hold sequence with identical displayed fields was dropped")
	}
}

// The frame carries the subagent hold into the info the projector merges; a
// frame that dropped it would erase a wait the capture had filled.
func TestAppwireDelegateInfoCarriesTheSubagentHold(t *testing.T) {
	info := appwireDelegateInfo(events.DelegateUpdatedData{
		DelegateID:           "dlg_1",
		DrainHeldBySubagents: true,
		RunningSubagentCount: 3,
		DrainHeldByJobs:      true,
		RunningJobCount:      1,
		HoldSequence:         5,
		ProjectionRevision:   2,
		Status:               "running",
	})
	if !info.DrainHeldBySubagents || info.RunningSubagentCount != 3 {
		t.Fatalf("projected subagent hold = %v, count = %d; want the frame's three-subagent sample",
			info.DrainHeldBySubagents, info.RunningSubagentCount)
	}
	if !info.DrainHeldByJobs || info.RunningJobCount != 1 || info.HoldSequence != 5 {
		t.Fatalf("projected job hold = %v, count = %d, sequence = %d; want the frame's sample",
			info.DrainHeldByJobs, info.RunningJobCount, info.HoldSequence)
	}
}
