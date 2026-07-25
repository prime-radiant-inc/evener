package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
)

// An active session is listed TWICE in the rail: once in the auto-grouped Live
// tier and again under its own project. That double-listing is a standing
// decision (kata b8m6), and reviewing it turned up the failure mode that would
// actually make it harmful: not the duplication itself, which nobody mistakes
// for two sessions, but the two rows DISAGREEING. Two rows that can each
// independently claim "needs you" are worse than one row listed twice, because
// then the reader cannot tell which is stale.
//
// They agree today because both tiers read the same stateFor/askPendingFor
// closures (tree.go:583 for the project path, :792 for the live path). Nothing
// enforced that. These pin it, so a second computation cannot creep into either
// path without failing here - whichever way the keep-or-drop question is
// eventually settled.

func liveAndProjectRowsFor(tree Tree, sessionID string) (live *TreeNode, project *TreeNode) {
	for i := range tree.Live {
		if tree.Live[i].ID == sessionID {
			live = &tree.Live[i]
			break
		}
	}
	for i := range tree.Projects {
		for _, node := range allSessions(tree.Projects[i]) {
			if node.ID == sessionID {
				found := node
				project = &found
				break
			}
		}
	}
	return live, project
}

func fuzzScenarioBuildTree_LiveAndProjectRowsAgreeOnState(t *testing.T) {
	now := time.Date(2026, 7, 25, 20, 0, 0, 0, time.UTC)
	// One entry per state the two paths could disagree about, including the
	// attention states an out-of-sync copy would most visibly get wrong.
	//
	// ThreadStatusSystemError earns its place: it is the one here whose wire
	// spelling ("systemError") differs from its display state ("errored"), so
	// it is the case that catches a path reading le.Status raw instead of
	// through stateFor's NormalizeState. The other three normalize to
	// themselves, so they would let that substitution pass.
	for _, status := range []string{
		appwire.ThreadStatusActive,
		appwire.ThreadStatusIdle,
		appwire.ThreadStatusAwaiting,
		appwire.ThreadStatusSystemError,
	} {
		t.Run(status, func(t *testing.T) {
			metas := []schema.SessionMeta{{
				ID:        "01DOUBLELISTED",
				CreatedAt: now,
				UpdatedAt: now,
				EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/serf"},
			}}
			live := []LiveEntry{{
				Entry:     rendezvous.Entry{PID: 1},
				SessionID: "01DOUBLELISTED",
				Status:    status,
			}}

			tree := buildTree(metas, live)
			liveRow, projectRow := liveAndProjectRowsFor(tree, "01DOUBLELISTED")
			if liveRow == nil {
				t.Fatalf("status %q: session missing from the Live tier", status)
			}
			if projectRow == nil {
				t.Fatalf("status %q: session missing from its project", status)
			}
			if liveRow.State != projectRow.State {
				t.Errorf("status %q: Live row state %q, project row state %q - the two listings of one session disagree",
					status, liveRow.State, projectRow.State)
			}
			if liveRow.AskPending != projectRow.AskPending {
				t.Errorf("status %q: Live row AskPending %v, project row AskPending %v - the two listings of one session disagree",
					status, liveRow.AskPending, projectRow.AskPending)
			}
		})
	}
}

func fuzzScenarioBuildTree_LiveAndProjectRowsAgreeOnAPendingAsk(t *testing.T) {
	now := time.Date(2026, 7, 25, 20, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{{
		ID:        "01ASKING",
		CreatedAt: now,
		UpdatedAt: now,
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/serf"},
	}}
	live := []LiveEntry{{
		Entry:      rendezvous.Entry{PID: 1},
		SessionID:  "01ASKING",
		Status:     appwire.ThreadStatusAwaiting,
		PendingAsk: true,
	}}

	tree := buildTree(metas, live)
	liveRow, projectRow := liveAndProjectRowsFor(tree, "01ASKING")
	if liveRow == nil || projectRow == nil {
		t.Fatalf("session missing: live=%v project=%v", liveRow != nil, projectRow != nil)
	}
	// The specific harm: one row claiming the session wants you while the other
	// says it does not. Assert the flag is actually SET, so this cannot pass by
	// both rows being equally and quietly wrong.
	if !liveRow.AskPending {
		t.Errorf("Live row AskPending = false, want true - a pending ask must reach the row")
	}
	if liveRow.AskPending != projectRow.AskPending {
		t.Errorf("Live row AskPending %v, project row AskPending %v - the two listings of one session disagree",
			liveRow.AskPending, projectRow.AskPending)
	}
}
