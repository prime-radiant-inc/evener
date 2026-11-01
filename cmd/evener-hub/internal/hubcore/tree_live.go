package hubcore

import (
	"slices"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

// dedupeMetasByID keeps the first record for each session ID, in order.
func dedupeMetasByID(metas []schema.SessionMeta) []schema.SessionMeta {
	// Metadata can be malformed or concurrently assembled with duplicate IDs.
	// The sorted (newest-first) order makes the canonical policy deterministic:
	// retain the first record for each ID, then build all lineage indexes from
	// that canonical set so a duplicate cannot be emitted or hoisted elsewhere.
	canonicalMetas := metas[:0]
	seenMetaIDs := make(map[string]struct{}, len(metas))
	for _, m := range metas {
		if _, seen := seenMetaIDs[m.ID]; seen {
			continue
		}
		seenMetaIDs[m.ID] = struct{}{}
		canonicalMetas = append(canonicalMetas, m)
	}
	return canonicalMetas
}

// treeLive resolves a session's live-derived row facts from one cut of the
// roster and the past index, so every builder of one tree names the same facts.
type treeLive struct {
	liveMap               map[string]LiveEntry
	liveRefMap            map[string]string
	runningSubagentIDs    map[string]bool
	reportedBy            map[string]string
	runningSubagentStates map[string]string
	metaMap               map[string]schema.SessionMeta
	sessionIsIndexed      map[string]bool
}

func newTreeLive(metas []schema.SessionMeta, live []LiveEntry, sessionIsIndexed map[string]bool) *treeLive {
	// Index live entries by SessionID.
	liveMap := make(map[string]LiveEntry, len(live))
	liveRefMap := make(map[string]string, len(live))
	runningSubagentIDs := make(map[string]bool)
	// reportedBy names the live session that lists each running child, the
	// only parent link a child has before its meta reaches the past index.
	reportedBy := make(map[string]string)
	runningSubagentStates := make(map[string]string)
	for _, le := range live {
		if le.SessionID != "" {
			liveMap[le.SessionID] = le
			if _, ref, ok := liveWorkspaceIdentity(le); ok {
				liveRefMap[le.SessionID] = ref.String()
			}
		}
		// A crash-retained record keeps the child list its daemon reported
		// before the process died (Roster.Refresh copies the last snapshot onto
		// it), and List hands it over unfiltered. That daemon runs nothing, so
		// its children are not in-process anywhere — the same rule
		// Roster.SubagentState applies for the read and workspace projections.
		if le.Crashed {
			continue
		}
		for _, childID := range le.RunningSubagentIDs {
			if childID != "" {
				runningSubagentIDs[childID] = true
				reportedBy[childID] = le.SessionID
				if state := le.RunningSubagentStates[childID]; state != "" {
					runningSubagentStates[childID] = state
				}
			}
		}
	}
	metaMap := make(map[string]schema.SessionMeta, len(metas))
	for _, m := range metas {
		if m.ID != "" {
			metaMap[m.ID] = m
		}
	}

	return &treeLive{
		liveMap:               liveMap,
		liveRefMap:            liveRefMap,
		runningSubagentIDs:    runningSubagentIDs,
		reportedBy:            reportedBy,
		runningSubagentStates: runningSubagentStates,
		metaMap:               metaMap,
		sessionIsIndexed:      sessionIsIndexed,
	}
}

// runningSubagentState resolves a live in-process child's display state:
// the child's own projected status when its daemon carried one, else "idle".
// Liveness alone must not read as activity — a settled, resumable delegate
// stays listed while doing nothing, and an old daemon that carried no state
// is no excuse to present a quiet child as working. Folding to idle keeps a
// no-state child in the rail's inactive list where it belongs, one click
// away — the conservative side, matching CURRENT_SUBAGENT_STATES' own logic.
func (lv *treeLive) runningSubagentState(id string) string {
	if state, ok := lv.runningSubagentStates[id]; ok {
		return NormalizeState(state)
	}
	return "idle"
}

// watchesFor resolves a session's own live watches from its live entry.
// CloneEvenerWatches copies the slices, so a node never aliases the roster
// snapshot or another node.
func (lv *treeLive) watchesFor(id string) []appwire.EvenerWatchInfo {
	return appwire.CloneEvenerWatches(lv.liveMap[id].Watches)
}

// stateFor resolves the display state for a session ID.
func (lv *treeLive) stateFor(id string) string {
	if le, ok := lv.liveMap[id]; ok {
		return NormalizeState(le.Status)
	}
	if lv.runningSubagentIDs[id] {
		return lv.runningSubagentState(id)
	}
	return "ended"
}

// askPendingFor resolves the ask-pending marker for a session ID from the
// same live map stateFor reads — every TreeNode builder below must set
// AskPending from this, not just the NeedsYou triage tier, or a session
// rendered elsewhere in the sidebar disagrees with its own NeedsYou tile.
func (lv *treeLive) askPendingFor(id string) bool {
	return lv.liveMap[id].PendingAsk
}

// approvalPendingFor resolves the pending-approval marker for a session ID
// from the same live map, for the same reason askPendingFor does.
func (lv *treeLive) approvalPendingFor(id string) bool {
	return lv.liveMap[id].PendingEscalation
}

// firstApprovalFor resolves the oldest pending escalation card for a
// session ID from the same live map, for the same reason: every builder
// names the same card. The daemon lists cards in raise order, so the
// oldest is first. It answers only while the entry's approval flag is set,
// so a row's approval detail always describes the approval the row says is
// pending; any other session gets the zero card.
func (lv *treeLive) firstApprovalFor(id string) appwire.SandboxEscalationRequested {
	if entry := lv.liveMap[id]; entry.PendingEscalation && len(entry.PendingEscalations) > 0 {
		return entry.PendingEscalations[0]
	}
	return appwire.SandboxEscalationRequested{}
}

// pendingQuestionFor resolves the first pending question for a session ID
// from the same live map, for the reason firstApprovalFor does: every
// builder names the same question, and only while the entry's ask flag is
// set. Each row gets its own copy.
func (lv *treeLive) pendingQuestionFor(id string) *appwire.PendingQuestion {
	if entry := lv.liveMap[id]; entry.PendingAsk {
		return appwire.ClonePendingQuestion(entry.PendingQuestion)
	}
	return nil
}

// failureFor resolves why a session failed from the same live map, so its
// rows agree (S1c). A crash-retained entry's daemon is gone, so its rows
// say it crashed whatever it last reported; an errored entry names the
// summary its daemon reported; any other session names none, whatever its
// entry still carries. Each row gets its own copy.
func (lv *treeLive) failureFor(id string) *appwire.ThreadFailure {
	entry := lv.liveMap[id]
	if entry.Crashed {
		return &appwire.ThreadFailure{Cause: &appwire.DiagnosticCause{Kind: hubapi.NavigationFailureCrashed}}
	}
	if NormalizeState(entry.Status) != "errored" {
		return nil
	}
	return appwire.CloneThreadFailure(entry.Failure)
}

// tasksFor resolves a session's task-list progress from its own live
// entry, the same live map stateFor reads, so every builder below puts the
// same progress on every row of one session and a child row never borrows
// its parent's. Each row gets its own copy.
func (lv *treeLive) tasksFor(id string) *appwire.TaskAggregate {
	return appwire.CloneTaskAggregate(lv.liveMap[id].Tasks)
}

// subagentsFor resolves a live root's subagent tally from the same live map,
// so its Live, project and NeedsYou rows agree (S3). A crash-retained entry
// answers none: its daemon runs nothing, the rule that already drops a
// crashed entry's listed children above.
func (lv *treeLive) subagentsFor(id string) appwire.SubagentTally {
	if entry := lv.liveMap[id]; !entry.Crashed {
		return entry.Subagents
	}
	return appwire.SubagentTally{}
}

// lastMessageFor resolves the opening of a session's last agent message
// (S1d): a live session's from its daemon's probe, which outranks the meta
// the past index may still hold, and an ended one's from that meta, so
// every row of one session agrees.
func (lv *treeLive) lastMessageFor(id string) string {
	if entry, live := lv.liveMap[id]; live {
		return entry.LastMessage
	}
	return lv.metaMap[id].LastMessage
}

// modelFor resolves the model a session's rows name (S17): a live
// session's current model from its daemon's probe, which follows a model
// switch and outranks the meta the past index may still hold, then the
// model its daemon started on (an entry no probe has reached yet), and
// then the meta's — for an ended session, or a live entry whose daemon has
// reported neither model — so every row of one session agrees.
func (lv *treeLive) modelFor(id string) string {
	if entry, live := lv.liveMap[id]; live {
		if entry.CurrentModel != "" {
			return entry.CurrentModel
		}
		if entry.Model != "" {
			return entry.Model // the model its daemon started on
		}
	}
	return lv.metaMap[id].Model
}

// turnEndedAtFor resolves a live session's last turn end from the same live
// map stateFor reads, so its Live, project and NeedsYou rows agree (S4).
func (lv *treeLive) turnEndedAtFor(id string) time.Time {
	return lv.liveMap[id].LastTurnEndedAt
}

// dormantFor resolves "this session has never run" for a session ID, from
// the same metaMap every builder below already consults — one closure, for
// the same reason stateFor and askPendingFor are: a session listed in both
// the Live tier and under its project must report the identical fact in
// both places (tree_live_agreement_test.go).
//
// Deliberately conservative — dormant only when the meta records NEITHER a
// model response NOR an accepted user input. A session that ran before
// AcceptedInputTurns was persisted still carries a TurnCount, so it is
// never mislabelled; and a session whose first turn is in flight, or which
// failed before any response, carries an accepted input, so the row never
// denies that the user asked it something. A synthetic live-only meta (or a
// live entry the past index has not caught up with) reports false: the claim
// is only ever made from indexed evidence.
func (lv *treeLive) dormantFor(id string) bool {
	m, ok := lv.metaMap[id]
	if !ok {
		return false
	}
	indexed, known := lv.sessionIsIndexed[id]
	if !known || !indexed {
		return false
	}
	return m.TurnCount == 0 && m.AcceptedInputTurns == 0
}

// fieldsFor resolves every live-derived row field for a session ID in
// one place, so buildNode, the meta-less Live leaf and the NeedsYou node
// cannot drift apart and a new live fact lands on all three at once
// (#2506). Each earlier fact already resolved through its own closure
// (stateFor, askPendingFor, ...); this is the single helper the next fact
// joins. It reads liveMap and the per-child indexes rather than a caller's
// loop entry: Roster.listLocked keeps one entry per session ID, so live
// never repeats a SessionID and every row of one session resolves the
// identical facts. The returned node carries only live-derived fields; the
// caller overlays the meta-derived and node-specific ones.
func (lv *treeLive) fieldsFor(id string) TreeNode {
	approval := lv.firstApprovalFor(id)
	return TreeNode{
		Ref:             lv.liveRefMap[id],
		State:           lv.stateFor(id),
		AskPending:      lv.askPendingFor(id),
		ApprovalPending: lv.approvalPendingFor(id),
		ApprovalTool:    approval.Tool,
		ApprovalTarget:  approval.DeniedPath,
		Question:        lv.pendingQuestionFor(id),
		Failure:         lv.failureFor(id),
		Dormant:         lv.dormantFor(id),
		RunningJobs:     appwire.CloneEvenerJobs(lv.liveMap[id].RunningJobs),
		CompletedJobs:   appwire.CloneEvenerJobs(lv.liveMap[id].CompletedJobs),
		Watches:         lv.watchesFor(id),
		Tasks:           lv.tasksFor(id),
		Subagents:       lv.subagentsFor(id),
		TurnEndedAt:     lv.turnEndedAtFor(id),
		LastMessage:     lv.lastMessageFor(id),
		Model:           lv.modelFor(id),
	}
}

// sessionNode is the flat row of a live session outside its project:
// the Live tier's leaf for a session with no meta and the Needs-you row.
// The live facts come from fieldsFor; identity, title and times come
// from the session's meta when the past index has one, else from the live
// entry itself.
func (lv *treeLive) sessionNode(le LiveEntry, meta *schema.SessionMeta) TreeNode {
	node := lv.fieldsFor(le.SessionID)
	node.ID = le.SessionID
	node.Kind = "session"
	if meta != nil {
		node.Title = nodeTitle(*meta, nodeKind(*meta))
		node.Project = projectName(*meta)
		node.Branch = meta.EnvInfo.GitBranch
		node.CreatedAt = OrderCreatedAt(meta.CreatedAt, meta.UpdatedAt)
		node.UpdatedAt = OrderUpdatedAt(meta.UpdatedAt, meta.CreatedAt)
		node.Age = AgeString(node.UpdatedAt)
	} else {
		node.Title = ShortID(le.SessionID)
		node.CreatedAt = le.StartedAt
		node.UpdatedAt = le.StartedAt
		node.Age = AgeString(le.StartedAt)
	}
	return node
}

// workingRoots holds the top-level rows whose task tree a live subagent
// keeps working: the subagent is active or has running jobs. It stands in
// for the walk over subagent child rows the rollup below used to make, and
// reads live data only. A subagent whose parent is ended counts as ended
// for state, but its running jobs still count. A crash-retained entry
// lists no running children (see the live index above), so it contributes
// nothing. A subagent whose ancestry does not reach a row, or cycles, is an
// orphan and affects nothing.
func (lv *treeLive) workingRoots(metas []schema.SessionMeta, roots *RootIndex, isSubagent func(string) bool) map[string]bool {
	workingRoots := make(map[string]bool)
	// parentOf is a subagent's parent: its meta's link, else the live session
	// that lists it as running.
	parentOf := func(id string) string {
		if m := lv.metaMap[id]; m.IsSubagent && m.ParentSessionID != "" {
			return m.ParentSessionID
		}
		return lv.reportedBy[id]
	}
	candidates := make(map[string]bool, len(lv.runningSubagentIDs))
	for id := range lv.runningSubagentIDs {
		candidates[id] = true
	}
	for _, m := range metas {
		if _, live := lv.liveMap[m.ID]; live && m.IsSubagent {
			candidates[m.ID] = true
		}
	}
	// The walk itself is RootIndex.WalkToTopLevel, the one implementation the
	// rollup and search's grouping share; only the parent/subagent/known
	// rules are the tree's own.
	known := func(id string) bool {
		_, ok := lv.metaMap[id]
		return ok
	}
	for id := range candidates {
		walk, ok := roots.WalkToTopLevel(id, parentOf, isSubagent, known)
		if !ok {
			continue
		}
		ancestorID := walk.Ancestor
		// Ended is sticky down the chain: a subagent under an ended parent
		// counts as ended whatever its own entry reports.
		state := lv.stateFor(id)
		if lv.stateFor(ancestorID) == "ended" || slices.ContainsFunc(walk.Chain[1:], func(c string) bool { return lv.stateFor(c) == "ended" }) {
			state = "ended"
		}
		if state != "active" && len(lv.liveMap[id].RunningJobs) == 0 {
			continue
		}
		// A fork original renders under its continuation, so its subagents
		// raise the continuation's task tree.
		workingRoots[walk.Top] = true
	}
	return workingRoots
}
