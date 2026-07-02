package agent

import (
	"fmt"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// This file fuzzes the pure durable-notification classification core extracted
// from filterDeliverableJobNotifications. classifyDurableNotifications takes only
// data snapshots (the drained durable notifications, the loaded job records, and
// a per-job already-injected map) and partitions the durable entries into fresh
// survivors and already-injected replays under the ShouldDeliver gate and the
// terminal-generation dedupe. Fuzzing it directly exercises the dedupe/partition
// logic that the live path buries under a store Load() and a history scan.
//
// The lx_ prefix marks helpers owned by this refactor/fuzz lane.

// lx_buildDurableInputs decodes the fuzz corpus into an adversarial classifier
// input: a list of durable notifications (with repeated job ids to force token
// dedupe), a records map keyed by job id (some job ids intentionally absent to
// exercise the rec==nil skip), and a per-job already-injected snapshot.
func lx_buildDurableInputs(data []byte) ([]jobNotification, map[string]*jobstore.JobRecord, map[string]bool) {
	durableRaw := make([]jobNotification, 0, len(data)/2+1)
	recs := make(map[string]*jobstore.JobRecord)
	alreadyInjected := make(map[string]bool)
	for i := 0; i+1 < len(data); i += 2 {
		b0 := data[i]
		b1 := data[i+1]
		jobID := fmt.Sprintf("job%d", b0%4)
		durableRaw = append(durableRaw, jobNotification{JobID: jobID})
		if _, ok := recs[jobID]; !ok {
			// A record is only introduced for some job ids; the rest resolve to
			// rec==nil and must be skipped by the classifier.
			if b0&0x40 == 0 {
				rec := &jobstore.JobRecord{
					JobID:            jobID,
					Type:             jobstore.JobDelegate,
					VisibleToSession: fmt.Sprintf("v%d", b1%2),
					TerminalGen:      fmt.Sprintf("g%d", (b1>>1)%2),
				}
				switch (b1 >> 2) % 3 {
				case 0:
					rec.NotifyState = jobstore.NotifyPending
				case 1:
					rec.NotifyState = jobstore.NotifyDelivered
				default:
					rec.NotifyState = jobstore.NotifyNotArmed
				}
				recs[jobID] = rec
				alreadyInjected[jobID] = (b1>>4)&1 == 1
			}
		}
	}
	return durableRaw, recs, alreadyInjected
}

func lx_sameDeliverables(a, b []deliverableJobNotification) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].notification.JobID != b[i].notification.JobID ||
			a[i].terminalGen != b[i].terminalGen {
			return false
		}
	}
	return true
}

func FuzzLxClassifyDurableNotifications(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0})
	f.Add([]byte{0, 0, 0, 0x10, 64, 0})
	f.Add([]byte{0, 0, 1, 4, 2, 8, 3, 16})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		durableRaw, recs, alreadyInjected := lx_buildDurableInputs(data)

		survivors, injected := classifyDurableNotifications(durableRaw, recs, alreadyInjected)

		// Determinism: identical snapshots yield identical partitions.
		s2, i2 := classifyDurableNotifications(durableRaw, recs, alreadyInjected)
		if !lx_sameDeliverables(survivors, s2) || !lx_sameDeliverables(injected, i2) {
			t.Fatalf("classifyDurableNotifications nondeterministic")
		}

		outKeys := make(map[jobstore.DedupeKey]bool)
		checkOutput := func(kind string, d deliverableJobNotification, wantInjected bool) {
			rec := recs[d.notification.JobID]
			// Validity: every output must resolve to a deliverable record.
			if rec == nil {
				t.Fatalf("%s references job %q with no record", kind, d.notification.JobID)
			}
			if !jobstore.ShouldDeliver(rec) {
				t.Fatalf("%s emitted non-deliverable record for job %q (state=%v)", kind, rec.JobID, rec.NotifyState)
			}
			// Well-formedness: the deliverable is rebuilt from the record.
			if d.notification.JobID != rec.JobID || d.terminalGen != rec.TerminalGen {
				t.Fatalf("%s deliverable does not match its record: %+v vs %+v", kind, d, rec)
			}
			// Routing: survivors are fresh, injected are already present.
			if alreadyInjected[rec.JobID] != wantInjected {
				t.Fatalf("%s misrouted job %q (alreadyInjected=%v)", kind, rec.JobID, alreadyInjected[rec.JobID])
			}
			key := rec.DedupeKey()
			// Dedupe + disjointness: each terminal identity appears at most once
			// across the whole partition.
			if outKeys[key] {
				t.Fatalf("%s duplicate dedupe key %+v across partition", kind, key)
			}
			outKeys[key] = true
		}
		for _, d := range survivors {
			checkOutput("survivor", d, false)
		}
		for _, d := range injected {
			checkOutput("injected", d, true)
		}

		// Completeness: the set of terminal identities in the partition equals the
		// set of distinct deliverable dedupe keys referenced by the input. This is
		// computed as an order-independent set union, independent of the ordered
		// first-wins dedupe the function performs.
		wantKeys := make(map[jobstore.DedupeKey]bool)
		for _, n := range durableRaw {
			rec := recs[n.JobID]
			if rec == nil || !jobstore.ShouldDeliver(rec) {
				continue
			}
			wantKeys[rec.DedupeKey()] = true
		}
		if len(wantKeys) != len(outKeys) {
			t.Fatalf("partition dropped or invented keys: want %d distinct, got %d", len(wantKeys), len(outKeys))
		}
		for k := range wantKeys {
			if !outKeys[k] {
				t.Fatalf("partition missing eligible dedupe key %+v", k)
			}
		}
	})
}
