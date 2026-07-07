//go:build serffuzz

package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// This file fuzzes the pure delegate-restore validation gate extracted from
// assessDelegateResumability. validateDelegateRestoreState is a pure function of
// (record, parent session id, has-state-dir): it runs the linkage / transcript /
// env-policy / working-dir / frozen-skill / state-dir preconditions that precede
// any on-disk restore and returns the first failing notResumable reason, or "" to
// proceed. Fuzzing it directly exercises the whole validation lattice, which the
// live path reaches only after real session-meta and transcript I/O.
//
// The lx_ prefix marks helpers owned by this refactor/fuzz lane.

const lx_delegateParentSessionID = "parentX"

// lx_validReason is the set of outcomes validateDelegateRestoreState may return.
var lx_validReasons = map[string]bool{
	"": true,
	notResumableMissingDelegateResumeMetadata: true,
	notResumableParentLinkageUnavailable:      true,
	notResumableCorruptChildSessionMeta:       true,
	notResumableMissingChildSessionMeta:       true,
}

// lx_validDelegateRestoreRecord builds a record+descriptor that passes every pure
// precondition, so validateDelegateRestoreState(rec, lx_delegateParentSessionID,
// true) returns "".
func lx_validDelegateRestoreRecord() *jobstore.JobRecord {
	const childID = "childABC"
	const ref = "local:childABC"
	return &jobstore.JobRecord{
		JobID:            "job1",
		Type:             jobstore.JobDelegate,
		OwnerSessionID:   "owner1",
		VisibleToSession: "vis1",
		TranscriptRef:    ref,
		DelegateRestore: &jobstore.DelegateRestoreDescriptor{
			ChildSessionID:   childID,
			TranscriptRef:    ref,
			ParentSessionID:  lx_delegateParentSessionID,
			ParentJobID:      "job1",
			OwnerSessionID:   "owner1",
			VisibleSessionID: "vis1",
			LocalEnvPolicy:   "default",
			WorkingDir:       "/work",
		},
	}
}

func FuzzLxValidateDelegateRestoreState(f *testing.F) {
	f.Add(uint32(0))          // fully valid baseline
	f.Add(^uint32(0))         // every mutator active
	f.Add(uint32(1))          // rec nil
	f.Add(uint32(1 << 4))     // transcript mismatch
	f.Add(uint32(1 << 12))    // no state dir
	f.Add(uint32(0xA5A5A5A5)) // scattered breaks

	f.Fuzz(func(t *testing.T, flags uint32) {
		bit := func(i uint) bool { return flags&(1<<i) != 0 }

		recNil := bit(0)
		descNil := bit(1)
		broken := false

		var rec *jobstore.JobRecord
		hasStateDir := true

		if recNil {
			rec = nil
			broken = true
		} else {
			rec = lx_validDelegateRestoreRecord()
			desc := rec.DelegateRestore
			if descNil {
				rec.DelegateRestore = nil
				broken = true
			} else {
				if bit(2) { // wrong job type
					rec.Type = jobstore.JobShell
					broken = true
				}
				if bit(3) { // empty child session id
					desc.ChildSessionID = "   "
					broken = true
				}
				if bit(4) { // transcript ref linkage mismatch
					desc.TranscriptRef = "local:other"
					broken = true
				}
				if bit(5) { // parent session mismatch
					desc.ParentSessionID = "wrong"
					broken = true
				}
				if bit(6) { // parent job mismatch
					desc.ParentJobID = "wrong"
					broken = true
				}
				if bit(7) { // owner mismatch
					desc.OwnerSessionID = "wrong"
					broken = true
				}
				if bit(8) { // visible mismatch
					desc.VisibleSessionID = "wrong"
					broken = true
				}
				if bit(9) { // undecodable transcript ref (both sides equal)
					rec.TranscriptRef = "bogus"
					desc.TranscriptRef = "bogus"
					broken = true
				}
				if bit(10) { // invalid local env policy
					desc.LocalEnvPolicy = "not-a-policy"
					broken = true
				}
				if bit(11) { // empty working dir
					desc.WorkingDir = "   "
					broken = true
				}
				if bit(12) { // inconsistent frozen skills (bodies without names)
					desc.FrozenSkillNames = nil
					desc.FrozenSkillBodies = []string{"x"}
					broken = true
				}
				if bit(13) { // missing state dir
					hasStateDir = false
					broken = true
				}
			}
		}

		reason := validateDelegateRestoreState(rec, lx_delegateParentSessionID, hasStateDir)

		// Determinism.
		if again := validateDelegateRestoreState(rec, lx_delegateParentSessionID, hasStateDir); again != reason {
			t.Fatalf("validateDelegateRestoreState nondeterministic: %q vs %q", reason, again)
		}
		// Total function: only known outcomes.
		if !lx_validReasons[reason] {
			t.Fatalf("unknown reason %q", reason)
		}
		// Validation invariant: proceed ("") iff every precondition holds.
		if broken && reason == "" {
			t.Fatalf("broken input accepted (flags=%#x)", flags)
		}
		if !broken && reason != "" {
			t.Fatalf("valid input rejected with %q (flags=%#x)", reason, flags)
		}
		// Missing record or descriptor is specifically the metadata reason.
		if (recNil || descNil) && reason != notResumableMissingDelegateResumeMetadata {
			t.Fatalf("nil rec/desc gave %q, want %q", reason, notResumableMissingDelegateResumeMetadata)
		}
	})
}