package hubcore

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
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

// Returns each listing by value with its own found flag, rather than pointers:
// a caller that forgets one of the flags gets a zero TreeNode, which fails an
// equality assertion loudly, instead of dereferencing nil.
func liveAndProjectRowsFor(tree Tree, sessionID string) (live TreeNode, liveFound bool, project TreeNode, projectFound bool) {
	for _, node := range tree.Live {
		if node.ID == sessionID {
			live, liveFound = node, true
			break
		}
	}
	for i := range tree.Projects {
		for _, node := range allSessions(tree.Projects[i]) {
			if node.ID == sessionID {
				project, projectFound = node, true
				break
			}
		}
	}
	return live, liveFound, project, projectFound
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
				EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/evener"},
			}}
			live := []LiveEntry{{
				PID:       1,
				SessionID: "01DOUBLELISTED",
				Status:    status,
			}}

			tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
			liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01DOUBLELISTED")
			if !inLive {
				t.Fatalf("status %q: session missing from the Live tier", status)
			}
			if !inProject {
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
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/evener"},
	}}
	live := []LiveEntry{{
		PID:        1,
		SessionID:  "01ASKING",
		Status:     appwire.ThreadStatusAwaiting,
		PendingAsk: true,
	}}

	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01ASKING")
	if !inLive || !inProject {
		t.Fatalf("session missing: live=%v project=%v", inLive, inProject)
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

// fuzzScenarioBuildTree_EveryRowCarriesApprovalPending: an escalation-promoted
// session reports the approval on its NeedsYou, Live and project rows alike,
// and keeps its real state on all of them: promotion changes membership, not
// state.
func fuzzScenarioBuildTree_EveryRowCarriesApprovalPending(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{{ID: "01APPROVAL", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}}
	live := []LiveEntry{{PID: 1, SessionID: "01APPROVAL", Status: appwire.ThreadStatusActive, PendingEscalation: true}}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	if len(tree.NeedsYou) != 1 || !tree.NeedsYou[0].ApprovalPending || tree.NeedsYou[0].State != "active" {
		t.Fatalf("NeedsYou = %+v, want one active row carrying ApprovalPending", tree.NeedsYou)
	}
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01APPROVAL")
	if !inLive || !inProject {
		t.Fatalf("session missing: live=%v project=%v", inLive, inProject)
	}
	if !liveRow.ApprovalPending || !projectRow.ApprovalPending {
		t.Fatalf("Live row %v, project row %v: both must carry the approval", liveRow.ApprovalPending, projectRow.ApprovalPending)
	}
	if liveRow.State != "active" || projectRow.State != "active" {
		t.Fatalf("Live row state %q, project row state %q: both must keep the real state active", liveRow.State, projectRow.State)
	}
}

// fuzzScenarioBuildTree_EveryRowCarriesTheFirstApproval: a session blocked on
// two escalations names the oldest one, the first card in raise order, on its
// NeedsYou, Live and project rows alike: the tool that asked and the full path
// it was denied.
func fuzzScenarioBuildTree_EveryRowCarriesTheFirstApproval(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{{ID: "01APPROVAL", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}}
	live := []LiveEntry{{PID: 1, SessionID: "01APPROVAL", Status: appwire.ThreadStatusActive, PendingEscalation: true, PendingEscalations: []appwire.SandboxEscalationRequested{
		{EscalationID: "esc_1", Tool: "write_file", Kind: "file_tool", DeniedPath: "/home/me/sites/docs/index.md"},
		{EscalationID: "esc_2", Tool: "edit_file", Kind: "file_tool", DeniedPath: "/etc/hosts"},
	}}}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01APPROVAL")
	if len(tree.NeedsYou) != 1 || !inLive || !inProject {
		t.Fatalf("session missing: needs-you rows=%d live=%v project=%v", len(tree.NeedsYou), inLive, inProject)
	}
	for name, row := range map[string]TreeNode{"NeedsYou": tree.NeedsYou[0], "Live": liveRow, "project": projectRow} {
		if row.ApprovalTool != "write_file" || row.ApprovalTarget != "/home/me/sites/docs/index.md" {
			t.Fatalf("%s row approval = %q %q, want the first card's write_file and its full path", name, row.ApprovalTool, row.ApprovalTarget)
		}
	}
}

// fuzzScenarioBuildTree_NoApprovalDetailWithoutTheFlag: a row's approval
// detail describes the approval the row says is pending, so an entry that
// carries a card while its approval flag is clear names no tool and no target
// on any row. The awaiting session keeps a NeedsYou row without the flag; the
// meta-less one is built as a live-only leaf.
func fuzzScenarioBuildTree_NoApprovalDetailWithoutTheFlag(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cards := []appwire.SandboxEscalationRequested{{EscalationID: "esc_1", Tool: "write_file", Kind: "file_tool", DeniedPath: "/home/me/sites/docs/index.md"}}
	metas := []schema.SessionMeta{{ID: "01AWAITING", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}}
	live := []LiveEntry{
		{PID: 1, SessionID: "01AWAITING", Status: appwire.ThreadStatusAwaiting, PendingEscalations: cards},
		{PID: 2, SessionID: "01NOMETA", Status: appwire.ThreadStatusActive, PendingEscalations: cards},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01AWAITING")
	leaf, inLeaf, _, _ := liveAndProjectRowsFor(tree, "01NOMETA")
	if len(tree.NeedsYou) != 1 || tree.NeedsYou[0].ID != "01AWAITING" || !inLive || !inProject || !inLeaf {
		t.Fatalf("rows missing: needs-you=%+v live=%v project=%v leaf=%v", tree.NeedsYou, inLive, inProject, inLeaf)
	}
	for name, row := range map[string]TreeNode{"NeedsYou": tree.NeedsYou[0], "Live": liveRow, "project": projectRow, "live-only leaf": leaf} {
		if row.ApprovalPending || row.ApprovalTool != "" || row.ApprovalTarget != "" {
			t.Fatalf("%s row approval = %v %q %q, want no approval detail without the flag", name, row.ApprovalPending, row.ApprovalTool, row.ApprovalTarget)
		}
	}
}

// fuzzScenarioBuildTree_EveryRowCarriesEveryLiveFact: every live-derived row
// field - Ref, State, the ask and approval flags with their detail, the
// question, the failure, dormancy, jobs, watches, tasks, the subagent tally,
// the turn end, the last message and the model - resolves through one helper
// (tree.go's liveFieldsFor) for all three builders: buildNode (the Live and
// project rows), the meta-less Live leaf, and the NeedsYou node. This pins
// that the builders cannot drift apart as a new live fact lands; before #2506
// each builder copied the facts by hand, so a miss showed up only when an
// agreement scenario happened to cover that field.
func fuzzScenarioBuildTree_EveryRowCarriesEveryLiveFact(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cards := []appwire.SandboxEscalationRequested{{EscalationID: "esc_1", Tool: "write_file", Kind: "file_tool", DeniedPath: "/home/me/sites/docs/index.md"}}
	question := &appwire.PendingQuestion{Question: "Keep the implied options?", Options: []string{"Drop them", "Keep them"}, Count: 2}
	failure := &appwire.ThreadFailure{Title: "Provider error", Cause: &appwire.DiagnosticCause{Kind: "provider"}}
	metas := []schema.SessionMeta{{ID: "01FULL", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}}
	live := []LiveEntry{
		{
			WorkspaceRef:       "local:01FULL",
			PID:                1,
			SessionID:          "01FULL",
			Status:             appwire.ThreadStatusSystemError,
			PendingAsk:         true,
			PendingQuestion:    question,
			PendingEscalation:  true,
			PendingEscalations: cards,
			Failure:            failure,
			RunningJobs:        []appwire.EvenerJobInfo{{JobID: "job_shell", JobType: "shell", Status: "running"}},
			CompletedJobs:      []appwire.EvenerJobInfo{{JobID: "job_done", JobType: "shell", Status: "done"}},
			Watches:            []appwire.EvenerWatchInfo{{ID: "watch_1", Source: "self", Active: true}},
			Tasks:              &appwire.TaskAggregate{Total: 3, Done: 1, Remaining: 2},
			Subagents:          appwire.SubagentTally{Running: 2, Failed: 1, Done: 57},
			LastTurnEndedAt:    now.Add(-time.Hour),
			LastMessage:        "the agent's last words",
			CurrentModel:       "gpt-oss",
		},
		// A live-only session the past index has not caught up with: the
		// meta-less Live leaf and its NeedsYou node must agree on the same
		// facts.
		{
			WorkspaceRef:       "local:01LEAF",
			PID:                2,
			SessionID:          "01LEAF",
			Status:             appwire.ThreadStatusSystemError,
			PendingEscalation:  true,
			PendingEscalations: cards,
			RunningJobs:        []appwire.EvenerJobInfo{{JobID: "job_leaf", JobType: "shell", Status: "running"}},
			Watches:            []appwire.EvenerWatchInfo{{ID: "watch_leaf", Source: "self", Active: true}},
			Subagents:          appwire.SubagentTally{Done: 4},
			LastMessage:        "leaf last words",
			CurrentModel:       "gpt-oss-leaf",
		},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)

	// Zero the fields no builder derives from the live roster, leaving only the
	// live-derived ones that must agree across tiers.
	liveFields := func(n TreeNode) TreeNode {
		n.ID, n.Title, n.Project, n.Branch, n.Kind = "", "", "", "", ""
		n.CreatedAt, n.UpdatedAt, n.Age = time.Time{}, time.Time{}, ""
		n.Children = nil
		return n
	}

	fullLive, inFullLive, fullProject, inFullProject := liveAndProjectRowsFor(tree, "01FULL")
	leaf, inLeaf, _, _ := liveAndProjectRowsFor(tree, "01LEAF")
	needsByID := map[string]TreeNode{}
	for _, n := range tree.NeedsYou {
		needsByID[n.ID] = n
	}
	fullNeeds, okFullNeeds := needsByID["01FULL"]
	leafNeeds, okLeafNeeds := needsByID["01LEAF"]
	if !inFullLive || !inFullProject || !inLeaf {
		t.Fatalf("rows missing: full live=%v project=%v leaf=%v", inFullLive, inFullProject, inLeaf)
	}
	if !okFullNeeds || !okLeafNeeds {
		t.Fatalf("NeedsYou missing a row: full=%v leaf=%v (tier=%+v)", okFullNeeds, okLeafNeeds, tree.NeedsYou)
	}

	for _, pair := range []struct {
		name string
		a, b TreeNode
	}{
		{"Live vs project", fullLive, fullProject},
		{"Live vs NeedsYou", fullLive, fullNeeds},
		{"project vs NeedsYou", fullProject, fullNeeds},
		{"meta-less leaf vs NeedsYou", leaf, leafNeeds},
	} {
		if !reflect.DeepEqual(liveFields(pair.a), liveFields(pair.b)) {
			t.Errorf("%s disagree on live-derived fields:\n a=%+v\n b=%+v",
				pair.name, liveFields(pair.a), liveFields(pair.b))
		}
	}

	// Guard against the agreement passing because every row is empty: the
	// fixture's facts must actually reach the rows.
	if fullNeeds.Ref != "local:01FULL" || fullNeeds.Failure == nil || fullNeeds.Question == nil ||
		fullNeeds.ApprovalTool != "write_file" || fullNeeds.ApprovalTarget != "/home/me/sites/docs/index.md" ||
		len(fullNeeds.RunningJobs) == 0 || len(fullNeeds.CompletedJobs) == 0 || len(fullNeeds.Watches) == 0 ||
		fullNeeds.Tasks == nil || fullNeeds.Subagents == (appwire.SubagentTally{}) ||
		fullNeeds.TurnEndedAt.IsZero() || fullNeeds.LastMessage == "" || fullNeeds.Model == "" ||
		fullNeeds.State != "errored" || !fullNeeds.AskPending || !fullNeeds.ApprovalPending {
		t.Errorf("NeedsYou row dropped a live fact: %+v", fullNeeds)
	}
}
