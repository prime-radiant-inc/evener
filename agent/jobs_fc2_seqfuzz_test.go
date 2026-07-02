package agent

import (
	"testing"

	"pgregory.net/rapid"
)

// TestJobsFc2DescendantMergeSeqFuzz is a stateful/sequence fuzz of the
// descendant-walk dedupe (jobs_nested.go). collectDescendantJobs merges records
// from every store in the live descendant tree into one row per job_id, using
// keepIncomingDescendantRow to resolve collisions: an owner record wins over a
// forwarded copy, and between two same-authority records the already-recorded
// (shallower) one is kept. The bug class here is a SEQUENCE bug — a record
// presented in the wrong order overwriting the authoritative owner row, or a
// job_id silently dropped — invisible to a single-collision unit test.
//
// The model draws a sequence of (job_id, isOwner) record presentations (job_ids
// drawn from a tiny pool so collisions are frequent) and replays each against the
// real merge decision, maintaining a parallel model of which job_ids have been
// seen and which have EVER carried an owner record. After each op it checks,
// weakest-first:
//
//	O1 (no orphans): every job_id in the merged set was actually presented, and
//	   every presented job_id is present (the walk never invents or drops a row).
//	O2 (owner authority): the merged row for a job_id is an owner row iff an owner
//	   record for that job_id has been presented at least once — the owner-wins
//	   guarantee, which must hold no matter the presentation order.
//
// The final metamorphic check replays the SAME multiset of presentations in a
// drawn permutation and asserts the owner-authority outcome per job_id is
// identical to the in-order run: owner-wins is order-independent (only the kept
// DEPTH may differ with order, never the authority).
func TestJobsFc2DescendantMergeSeqFuzz(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 30).Draw(rt, "nrecs")
		type present struct {
			jobID   string
			isOwner bool
		}
		jobPool := []string{"job_a", "job_b", "job_c", "job_d"}

		presented := make([]present, 0, n)
		for i := 0; i < n; i++ {
			presented = append(presented, present{
				jobID:   rapid.SampledFrom(jobPool).Draw(rt, "jobID"),
				isOwner: rapid.Bool().Draw(rt, "isOwner"),
			})
		}

		// merge applies the real decision core over a presentation order, returning
		// the final owner-authority per job_id.
		merge := func(order []present) map[string]bool {
			merged := map[string]bool{}   // job_id -> merged row's isOwner
			sawOwner := map[string]bool{} // model: any owner record ever presented
			seenAny := map[string]bool{}  // model: job_id ever presented
			for step, p := range order {
				existingIsOwner, seen := merged[p.jobID]
				if keepIncomingDescendantRow(seen, existingIsOwner, p.isOwner) {
					merged[p.jobID] = p.isOwner
				}
				seenAny[p.jobID] = true
				if p.isOwner {
					sawOwner[p.jobID] = true
				}

				// O1: no orphans and no drops.
				if len(merged) != len(seenAny) {
					rt.Fatalf("step %d: merged has %d rows, presented %d distinct", step, len(merged), len(seenAny))
				}
				for id := range merged {
					if !seenAny[id] {
						rt.Fatalf("step %d: merged row %q was never presented", step, id)
					}
				}
				// O2: owner authority matches the model.
				for id, isOwner := range merged {
					if isOwner != sawOwner[id] {
						rt.Fatalf("step %d: job %q merged isOwner=%v, model sawOwner=%v", step, id, isOwner, sawOwner[id])
					}
				}
			}
			return merged
		}

		inOrder := merge(presented)

		// Metamorphic: the same multiset in a drawn permutation yields the same
		// owner-authority outcome per job_id (order-independence of owner-wins).
		perm := rapid.Permutation(presented).Draw(rt, "perm")
		permed := merge(perm)

		if len(inOrder) != len(permed) {
			rt.Fatalf("permutation changed job set size: %d vs %d", len(inOrder), len(permed))
		}
		for id, isOwner := range inOrder {
			permOwner, ok := permed[id]
			if !ok {
				rt.Fatalf("permutation dropped job %q", id)
			}
			if isOwner != permOwner {
				rt.Fatalf("permutation changed owner-authority for %q: %v vs %v", id, isOwner, permOwner)
			}
		}
	})
}
