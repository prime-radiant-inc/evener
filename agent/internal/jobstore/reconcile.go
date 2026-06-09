package jobstore

import (
	"sort"
	"time"
)

// Reconcile finalizes records whose durable state is running but which have no
// live in-memory runtime, returning one job_finished event per such job
// (stopped/runtime_lost, with a freshly minted terminal_generation). Records
// that are already terminal, or whose job id is in liveJobIDs, produce nothing.
// The returned events are sorted by job id for deterministic output.
func Reconcile(records map[string]*JobRecord, liveJobIDs map[string]bool, now time.Time) []Event {
	var lost []string
	for id, r := range records {
		if r.Status == StatusRunning && !liveJobIDs[id] {
			lost = append(lost, id)
		}
	}
	sort.Strings(lost)

	events := make([]Event, 0, len(lost))
	for _, id := range lost {
		ended := now
		events = append(events, Event{
			Kind:        EventJobFinished,
			JobID:       id,
			Status:      StatusStopped,
			Reason:      "runtime_lost",
			EndedAt:     &ended,
			TerminalGen: NewTerminalGeneration(),
		})
	}
	return events
}
