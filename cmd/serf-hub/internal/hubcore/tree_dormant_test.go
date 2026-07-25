package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
)

// A session can now exist having never run a turn: an empty-prompt spawn starts
// one dormant (kata ytpa). The daemon reports it "idle" — the same word a
// session that ran and finished gets — so the state vocabulary alone cannot
// tell the two apart, and every row-level consequence of that follows: no dot,
// no second line, and an age counting up from a session that has done nothing.
//
// Dormant is that missing fact, carried BESIDE the state rather than folded
// into it. "Has this ever run" and "what is it doing right now" are independent
// questions: a session prompted a moment ago is legitimately active with
// nothing in its history yet. A new state VALUE would have to rank those two
// answers against each other in one slot, and would drag a rule for itself
// through every rollup, every attention rank, and both languages' switches.
//
// The predicate is deliberately conservative: dormant only when the meta
// records NEITHER a model response (TurnCount) NOR an accepted user input
// (AcceptedInputTurns). That way
//   - a session that ran before accepted_input_turns was ever persisted still
//     carries a TurnCount, so it is never mislabelled as never-run; and
//   - a session whose first turn is in flight, or which failed before any
//     model response, carries an accepted input — the user DID ask it
//     something, which is exactly the claim the row must never deny.
func fuzzScenarioBuildTree_MarksNeverRunSessionDormant(t *testing.T) {
	now := time.Date(2026, 7, 25, 20, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		meta  schema.SessionMeta
		want  bool
		cause string
	}{
		{
			name:  "never ran",
			meta:  schema.SessionMeta{TurnCount: 0, AcceptedInputTurns: 0},
			want:  true,
			cause: "no model response and no accepted input",
		},
		{
			name:  "ran and went idle",
			meta:  schema.SessionMeta{TurnCount: 3, AcceptedInputTurns: 2},
			want:  false,
			cause: "the session has answered before",
		},
		{
			name:  "legacy meta with no accepted-input count",
			meta:  schema.SessionMeta{TurnCount: 7},
			want:  false,
			cause: "a TurnCount alone proves it ran",
		},
		{
			name:  "asked but no response yet",
			meta:  schema.SessionMeta{TurnCount: 0, AcceptedInputTurns: 1},
			want:  false,
			cause: "the user did ask it something",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := tc.meta
			meta.ID = "01DORMANCY"
			meta.CreatedAt = now
			meta.UpdatedAt = now
			meta.EnvInfo = schema.EnvironmentInfo{WorkingDir: "/projects/serf"}
			live := []LiveEntry{{
				Entry:     rendezvous.Entry{PID: 1},
				SessionID: "01DORMANCY",
				Status:    appwire.ThreadStatusIdle,
			}}

			tree := buildTree([]schema.SessionMeta{meta}, live)
			liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01DORMANCY")
			if !inLive || !inProject {
				t.Fatalf("session missing: live=%v project=%v", inLive, inProject)
			}
			if projectRow.Dormant != tc.want {
				t.Errorf("project row Dormant = %v, want %v (%s)", projectRow.Dormant, tc.want, tc.cause)
			}
			if liveRow.Dormant != tc.want {
				t.Errorf("Live row Dormant = %v, want %v (%s)", liveRow.Dormant, tc.want, tc.cause)
			}
			// The state itself must be untouched: dormancy is reported beside
			// the state, never in place of it, so nothing downstream that
			// switches on the state vocabulary has a new case to learn.
			if projectRow.State != "idle" {
				t.Errorf("project row State = %q, want idle — dormancy must not change the state vocabulary", projectRow.State)
			}
		})
	}
}

// The same double-listing invariant tree_live_agreement_test.go pins for state
// and askPending, for the fact this kata adds. Asserts the flag is actually SET
// on both rows, so it cannot pass by both listings being equally and quietly
// wrong.
func fuzzScenarioBuildTree_LiveAndProjectRowsAgreeOnDormancy(t *testing.T) {
	now := time.Date(2026, 7, 25, 20, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{{
		ID:        "01NEVERRAN",
		CreatedAt: now,
		UpdatedAt: now,
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/serf"},
	}}
	live := []LiveEntry{{
		Entry:     rendezvous.Entry{PID: 1},
		SessionID: "01NEVERRAN",
		Status:    appwire.ThreadStatusIdle,
	}}

	tree := buildTree(metas, live)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01NEVERRAN")
	if !inLive || !inProject {
		t.Fatalf("session missing: live=%v project=%v", inLive, inProject)
	}
	if !liveRow.Dormant {
		t.Errorf("Live row Dormant = false, want true — a never-run session must reach the row")
	}
	if liveRow.Dormant != projectRow.Dormant {
		t.Errorf("Live row Dormant %v, project row Dormant %v — the two listings of one session disagree",
			liveRow.Dormant, projectRow.Dormant)
	}
}
