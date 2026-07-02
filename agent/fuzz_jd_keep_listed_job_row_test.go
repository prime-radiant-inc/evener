//go:build serffuzz

package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzJdKeepListedJobRow drives keepListedJobRow — the pure per-record filter
// decision lifted out of listWithError — with adversarial records, filters, and
// listing-session IDs. Oracles (beyond never-panic):
//   - determinism: the same (rec, filter, sessionID) yields the same decision;
//   - monotone nesting: a row kept with IncludeNested=false is always kept with
//     IncludeNested=true (relaxing the ownership gate only ever includes more);
//   - empty filter fields never exclude: an all-empty filter with nesting on
//     keeps every row;
//   - a locally-owned row (OwnerSessionID == sessionID) is never dropped by the
//     ownership clause.
func FuzzJdKeepListedJobRow(f *testing.F) {
	f.Add("sess_a", "job_parent", "sess_a", uint8(0), uint8(1), false, uint8(0), uint8(0), true, false)
	f.Add("sess_b", "", "sess_a", uint8(1), uint8(0), true, uint8(2), uint8(1), false, true)
	f.Add("", "job_p", "sess_a", uint8(3), uint8(2), false, uint8(5), uint8(2), true, true)

	f.Fuzz(func(t *testing.T, ownerID, parentID, sessionID string,
		statusSel, typeSel uint8, includeNested bool,
		filterStatusSel, filterTypeSel uint8, useStatuses, useTypes bool) {

		statuses := []jobstore.Status{
			jobstore.StatusRunning, jobstore.StatusCompleted, jobstore.StatusFailed,
			jobstore.StatusCancelled, jobstore.StatusStopped, "",
		}
		types := []jobstore.JobType{jobstore.JobShell, jobstore.JobDelegate, ""}

		recStatus := statuses[int(statusSel)%len(statuses)]
		recType := types[int(typeSel)%len(types)]
		filterStatus := statuses[int(filterStatusSel)%len(statuses)]
		filterType := types[int(filterTypeSel)%len(types)]

		rec := &jobstore.JobRecord{
			OwnerSessionID: ownerID,
			ParentJobID:    parentID,
			Status:         recStatus,
			Type:           recType,
		}
		filter := listFilter{
			IncludeNested: includeNested,
			Status:        filterStatus,
			Type:          filterType,
		}
		if useStatuses {
			filter.Statuses = []jobstore.Status{filterStatus}
		}
		if useTypes {
			filter.Types = []jobstore.JobType{filterType}
		}

		got := keepListedJobRow(rec, filter, sessionID)
		if got2 := keepListedJobRow(rec, filter, sessionID); got != got2 {
			t.Fatalf("non-deterministic: %v vs %v", got, got2)
		}

		// Monotone nesting: relaxing only the ownership gate can never exclude.
		nestOff := filter
		nestOff.IncludeNested = false
		nestOn := filter
		nestOn.IncludeNested = true
		if keepListedJobRow(rec, nestOff, sessionID) && !keepListedJobRow(rec, nestOn, sessionID) {
			t.Fatalf("nesting not monotone: kept without IncludeNested but dropped with it")
		}

		// An all-empty filter with nesting on never excludes.
		if !keepListedJobRow(rec, listFilter{IncludeNested: true}, sessionID) {
			t.Fatalf("empty filter excluded a row: %+v", rec)
		}

		// A locally-owned row is never dropped by the ownership clause: isolate
		// that clause by clearing the status/type filters.
		owned := &jobstore.JobRecord{
			OwnerSessionID: sessionID,
			ParentJobID:    parentID,
			Status:         recStatus,
			Type:           recType,
		}
		if !keepListedJobRow(owned, listFilter{IncludeNested: false}, sessionID) {
			t.Fatalf("locally-owned row dropped by ownership clause: %+v", owned)
		}
	})
}
