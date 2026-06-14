package agent

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func (s *Session) ownerJobManagerFor(jobID string) (*jobManager, *jobstore.JobRecord) {
	if s == nil || s.jobManager == nil || s.jobManager.store == nil {
		return nil, nil
	}
	recs, err := s.jobManager.store.Load()
	if err != nil {
		return nil, nil
	}
	rec := recs[jobID]
	if rec == nil || rec.ParentJobID == "" || rec.OwnerSessionID == "" || rec.OwnerSessionID == s.id {
		return nil, rec
	}
	if s.subagents == nil {
		return nil, rec
	}
	sub := s.subagents.get(rec.OwnerSessionID)
	if sub == nil || sub.sess == nil || sub.sess.jobManager == nil {
		return nil, rec
	}
	sub.mu.Lock()
	closed := sub.closed
	sub.mu.Unlock()
	if closed {
		return nil, rec
	}
	owner := sub.sess.jobManager
	if owner.store == nil {
		return nil, rec
	}
	if _, err := owner.store.Load(); errors.Is(err, jobstore.ErrStoreClosed) {
		return nil, rec
	}
	return owner, rec
}

// walkDescendantJobs implements job_list(include_descendants=true) (spec §2): it
// walks the LIVE descendant tree at read time, surfacing the caller's own jobs
// (depth 0) plus every live descendant's jobs, each annotated with its
// owner_session_id and the depth of the store it was surfaced from.
//
// Leaf-lock discipline: each store is read independently under its own lock (via
// listWithError, which loads + locks that one jobManager); no jobManager lock is
// held across the recursion. Children are enumerated through s.subagents and
// only LIVE (non-closed) children are recursed into — a dead descendant's
// terminal record survives only as the forwarded copy in an ancestor store.
//
// Dedupe rule: the owner session's record (OwnerSessionID == that store's
// session) is authoritative; a forwarded copy of the same job_id found in an
// ancestor store is suppressed in favor of the owner record discovered by
// recursing into the live child. Each job_id therefore appears once, at its
// real owner's depth — except a dead descendant's job, whose only surviving row
// is the forwarded copy at the ancestor store's depth.
func (s *Session) walkDescendantJobs(filter listFilter) ([]jobListEntry, error) {
	merged := map[string]descendantWalkRow{}
	if err := s.collectDescendantJobs(s, 0, filter, merged); err != nil {
		return nil, err
	}

	rows := make([]descendantWalkRow, 0, len(merged))
	for _, row := range merged {
		rows = append(rows, row)
	}
	// Match listWithError's ordering: started_at descending, tie-broken by
	// job_id, applied once over the fully merged set.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].rec.StartedAt.Equal(rows[j].rec.StartedAt) {
			return rows[i].rec.JobID < rows[j].rec.JobID
		}
		return rows[i].rec.StartedAt.After(rows[j].rec.StartedAt)
	})
	if filter.Limit > 0 && len(rows) > filter.Limit {
		rows = rows[:filter.Limit]
	}

	jobs := make([]jobListEntry, 0, len(rows))
	for _, row := range rows {
		// Project against the row's owner session (the T11 advisory), defaulting
		// to the caller for safety when no owner was recorded.
		projectSession := row.ownerSess
		if projectSession == nil {
			projectSession = s
		}
		entry := projectJobRecord(projectSession, row.rec)
		entry.Depth = row.depth
		jobs = append(jobs, entry)
	}
	return jobs, nil
}

type descendantWalkRow struct {
	rec       *jobstore.JobRecord
	depth     int
	isOwner   bool
	ownerSess *Session
}

// collectDescendantJobs reads node's store (leaf-lock), merges its rows into
// merged with the owner-authoritative dedupe rule, then recurses into node's
// LIVE direct children at depth+1.
func (s *Session) collectDescendantJobs(node *Session, depth int, filter listFilter, merged map[string]descendantWalkRow) error {
	jm, err := sessionJobManager(node)
	if err != nil {
		// The caller's own store (depth 0) error is surfaced — a depth-0 failure
		// is the same failure plain job_list reports, and swallowing it would
		// silently return an empty list with success. A descendant store error
		// (depth >= 1) stays best-effort: a dead descendant store is expected, and
		// its terminal copy survives as the forwarded copy in an ancestor store.
		if depth == 0 {
			return err
		}
		return nil
	}
	// IncludeNested surfaces forwarded copies in this store so a dead
	// descendant's terminal copy survives; Limit is cleared so the global sort
	// and limit apply once over the merged set, not per store.
	storeFilter := filter
	storeFilter.IncludeNested = true
	storeFilter.IncludeDescendants = false
	storeFilter.Limit = 0
	recs, err := jm.listWithError(storeFilter)
	if err != nil {
		if depth == 0 {
			return err
		}
		return nil
	}
	for _, rec := range recs {
		isOwner := rec.OwnerSessionID == jm.sessionID
		existing, seen := merged[rec.JobID]
		// Owner record wins over a forwarded copy. Between two records of the
		// same authority the shallower (already-recorded) one is kept.
		if seen && (existing.isOwner || !isOwner) {
			continue
		}
		// ownerSess is the session this row was read from (node), so the LIST
		// projection keys resumability on the owner, not the root caller (the
		// T11 advisory; mirrors the depth >= 2 read path).
		merged[rec.JobID] = descendantWalkRow{rec: rec, depth: depth, isOwner: isOwner, ownerSess: node}
	}

	if node.subagents == nil {
		return nil
	}
	for _, child := range node.liveSubagentSessions() {
		_ = s.collectDescendantJobs(child, depth+1, filter, merged)
	}
	return nil
}

// liveSubagentSessions returns the live (non-closed) direct-child sessions. The
// closed flag is read under each sub's own lock; the manager mutex is released
// before any child store is touched, preserving the manager-outer/sub-inner
// lock order and the leaf-lock discipline of the walk.
func (s *Session) liveSubagentSessions() []*Session {
	if s.subagents == nil {
		return nil
	}
	s.subagents.mu.Lock()
	subs := make([]*subagent, 0, len(s.subagents.subs))
	for _, sub := range s.subagents.subs {
		subs = append(subs, sub)
	}
	s.subagents.mu.Unlock()

	live := make([]*Session, 0, len(subs))
	for _, sub := range subs {
		if sub == nil || sub.sess == nil {
			continue
		}
		sub.mu.Lock()
		closed := sub.closed
		sub.mu.Unlock()
		if closed {
			continue
		}
		live = append(live, sub.sess)
	}
	return live
}

// liveDirectSubagents returns the live (non-closed) direct-child subagents,
// matching liveSubagentSessions's leaf-lock discipline: the manager mutex is
// released before each sub.mu is taken, and the closed flag is read under the
// sub's own lock. The drive-signal reader (spec §3) needs the *subagent (not just
// its Session) to apply the live/idle guard and launch the drive turn.
func (s *Session) liveDirectSubagents() []*subagent {
	if s.subagents == nil {
		return nil
	}
	s.subagents.mu.Lock()
	subs := make([]*subagent, 0, len(s.subagents.subs))
	for _, sub := range s.subagents.subs {
		subs = append(subs, sub)
	}
	s.subagents.mu.Unlock()

	live := make([]*subagent, 0, len(subs))
	for _, sub := range subs {
		if sub == nil || sub.sess == nil {
			continue
		}
		sub.mu.Lock()
		closed := sub.closed
		sub.mu.Unlock()
		if closed {
			continue
		}
		live = append(live, sub)
	}
	return live
}

// liveSubagentSession returns the live (non-closed) direct-child session for id,
// or nil. The closed flag is read under the sub's own lock, matching
// liveSubagentSessions's leaf-lock discipline.
func liveSubagentSession(mgr *subagentManager, id string) *Session {
	if mgr == nil {
		return nil
	}
	sub := mgr.get(id)
	if sub == nil || sub.sess == nil {
		return nil
	}
	sub.mu.Lock()
	closed := sub.closed
	sub.mu.Unlock()
	if closed {
		return nil
	}
	return sub.sess
}

func (s *Session) nestedOrLocalJobManager(jobID string) (*jobManager, *jobstore.JobRecord, error) {
	local, err := sessionJobManager(s)
	if err != nil {
		return nil, nil, err
	}
	owner, forwarded := s.ownerJobManagerFor(jobID)
	if owner == nil {
		if forwarded != nil {
			return local, forwarded, nil
		}
		rec, err := findJobRecord(local, jobID)
		return local, rec, err
	}
	rec, err := findJobRecord(owner, jobID)
	if err != nil {
		if errors.Is(err, jobstore.ErrStoreClosed) && forwarded != nil {
			return local, forwarded, nil
		}
		return nil, nil, err
	}
	return owner, rec, nil
}

// resolveDescendantJobOwner resolves a job owned by a session at depth >= 2 in
// the live subtree (spec §2): the one-hop ownerJobManagerFor reaches only direct
// children, so a grandchild-or-deeper owner is found by recursing the LIVE
// subtree (DFS through liveSubagentSessions, the same enumeration
// collectDescendantJobs uses) and applying the existing single-hop
// ownerJobManagerFor step at each hop until the owner store holds the job_id. It
// returns the owner's jobManager, the owner Session (the load-bearing T11
// advisory: projection and resumability must key on the owner, not the root
// caller), the owner's DIRECT PARENT session (where the single-hop forwarded
// terminal copy lands — the closed-store read fallback resolves against it; for
// a direct child of the caller the parent IS the caller), the owner's record,
// and whether the owner was found.
//
// Leaf-lock discipline matches the walk: each store is read under its own lock
// (ownerJobManagerFor loads the node's store; liveSubagentSessions takes each
// sub.mu only to read the closed flag), and NO jobManager or session lock is
// held across the recursion.
func (s *Session) resolveDescendantJobOwner(jobID string) (*jobManager, *Session, *Session, *jobstore.JobRecord, bool) {
	if s == nil {
		return nil, nil, nil, nil, false
	}
	for _, child := range s.liveSubagentSessions() {
		// One single-hop step at this node: a job owned by a DIRECT child of
		// child is reachable through child.ownerJobManagerFor. The owner's direct
		// parent is therefore child.
		if owner, rec := child.ownerJobManagerFor(jobID); owner != nil {
			ownerSess := liveSubagentSession(child.subagents, rec.OwnerSessionID)
			if found, err := findJobRecord(owner, jobID); err == nil && ownerSess != nil {
				return owner, ownerSess, child, found, true
			}
		}
		// The child itself may own the job (a depth >= 2 owner with no further
		// descendants below it). Its direct parent is s.
		if child.jobManager != nil && child.jobManager.store != nil {
			if recs, err := child.jobManager.store.Load(); err == nil {
				if rec := recs[jobID]; rec != nil && rec.OwnerSessionID == child.id {
					return child.jobManager, child, s, rec, true
				}
			}
		}
		// Recurse deeper into the live subtree.
		if owner, ownerSess, ownerParent, rec, ok := child.resolveDescendantJobOwner(jobID); ok {
			return owner, ownerSess, ownerParent, rec, ok
		}
	}
	return nil, nil, nil, nil, false
}

func (s *Session) stopNestedOrLocal(jobID string) (*jobstore.JobRecord, error) {
	local, err := sessionJobManager(s)
	if err != nil {
		return nil, err
	}
	owner, forwarded := s.ownerJobManagerFor(jobID)
	if owner != nil {
		// Spec 5.8's not_controllable path is reserved for future cross-process
		// owners; a live in-process owner routes directly to its job manager.
		return owner.stop(jobID)
	}
	if forwarded != nil && forwarded.ParentJobID != "" && forwarded.OwnerSessionID != s.id {
		if forwarded.Status.IsTerminal() {
			return cloneJobRecord(forwarded), nil
		}
		if guidance := s.notControllableDescendantError(jobID); guidance != nil {
			return nil, guidance
		}
		return nil, fmt.Errorf("not_controllable: nested job %q owner runtime is not live", jobID)
	}
	// A non-direct descendant (a grandchild-or-deeper job the caller neither owns
	// nor reaches through a direct child) is not directly controllable: the caller
	// must stop its direct delegate to cascade-stop the subtree (spec §2). This is
	// surfaced as guidance rather than silently routed.
	if guidance := s.notControllableDescendantError(jobID); guidance != nil {
		return nil, guidance
	}
	return local.stop(jobID)
}

// notControllableDescendantError returns the spec §2 non-direct-descendant
// guidance error if jobID is owned by a session in the caller's live subtree
// other than a direct child it owns the job through. It names the owning
// descendant session and the direct delegate the caller CAN stop to cascade-stop
// the subtree. Returns nil when jobID is not such a descendant.
func (s *Session) notControllableDescendantError(jobID string) error {
	child := s.directChildOwningDescendant(jobID)
	if child == nil {
		return nil
	}
	_, _, _, ownerRec, _ := s.resolveDescendantJobOwner(jobID)
	ownerID := child.id
	if ownerRec != nil && ownerRec.OwnerSessionID != "" {
		ownerID = ownerRec.OwnerSessionID
	}
	handle := s.directDelegateJobForChild(child.id)
	if handle == "" {
		return fmt.Errorf("not_controllable: job %q is owned by descendant session %q, not your direct delegate; stop your direct delegate for session %q to cascade-stop its subtree", jobID, ownerID, child.id)
	}
	return fmt.Errorf("not_controllable: job %q is owned by descendant session %q, not your direct delegate; stop your direct delegate job %q (which owns session %q) to cascade-stop its subtree", jobID, ownerID, handle, child.id)
}

func (s *Session) stopChildren(delegateJobID string) ([]*jobstore.JobRecord, error) {
	local, err := sessionJobManager(s)
	if err != nil {
		return nil, err
	}
	recs, err := local.store.Load()
	if err != nil {
		return nil, err
	}

	var stopped []*jobstore.JobRecord
	var stopErr error
	for jobID, rec := range recs {
		if rec.ParentJobID != delegateJobID || rec.Status.IsTerminal() {
			continue
		}
		stoppedRec, err := s.stopNestedOrLocal(jobID)
		if err != nil {
			stopErr = errors.Join(stopErr, err)
			continue
		}
		if stoppedRec != nil {
			stopped = append(stopped, stoppedRec)
		}
	}
	return stopped, stopErr
}

// delegateChildSessionToCascade resolves jobID to the live child session whose
// subtree the job_stop cascade must stop (spec §2). It returns a session only
// when jobID is the caller's OWN direct delegate job (a delegate record the
// caller owns, whose transcript_ref names a live direct child). Shell jobs,
// non-owned forwarded copies, and dead children resolve to nil — there is no
// live subtree to cascade into.
func (s *Session) delegateChildSessionToCascade(jobID string) *Session {
	local, err := sessionJobManager(s)
	if err != nil || local.store == nil {
		return nil
	}
	recs, err := local.store.Load()
	if err != nil {
		return nil
	}
	rec := recs[jobID]
	// No terminal-status gate: a fire-and-return coordinator's OWN delegate job is
	// terminal (completed) while its live subtree keeps running, and job_stop must
	// still cascade-stop that live subtree. The downstream stopDelegateSubtree only
	// signals jm.runningJobIDs(), so already-terminal subtree jobs are skipped
	// harmlessly; and liveSubagentSession (below) still returns nil for a genuinely
	// closed/gone coordinator session, so a truly-gone subtree is a safe no-op.
	if rec == nil || rec.Type != jobstore.JobDelegate || rec.OwnerSessionID != s.id {
		return nil
	}
	_, childID, err := decodeRef(rec.TranscriptRef)
	if err != nil || childID == "" {
		return nil
	}
	return liveSubagentSession(s.subagents, childID)
}

// stopDelegateSubtree implements the job_stop cascade (spec §2): stopping a
// coordinator's delegate job also stops the coordinator's own running jobs (its
// workers' delegate + shell jobs) and recurses into the live subtree, so the
// §0.2 "stop the subtree = stop its direct child" guidance is true. It is the
// fine-grained analogue of Session Close's recursive teardown: Close tears down
// whole sessions, while this stops the running jobs inside the subtree without
// closing the sessions (Session Close is unchanged).
//
// childSession is the delegate job's child session (resolved from the delegate
// record's transcript_ref); stopping the delegate job itself is the caller's job
// (stopNestedOrLocal). The traversal reuses the live-walk leaf-lock discipline:
// each store's running jobs are snapshotted under that store's own lock
// (runningJobIDs) and stopped one at a time; live direct children are enumerated
// through liveSubagentSessions; NO job-manager or session lock is held across the
// recursion. A no-longer-live (closed) child contributes nothing — its runtime is
// already gone.
func (s *Session) stopDelegateSubtree(childSession *Session) ([]*jobstore.JobRecord, error) {
	if childSession == nil {
		return nil, nil
	}
	var stopped []*jobstore.JobRecord
	var stopErr error
	// Recurse into the live subtree first so a worker delegate's own children are
	// stopped before the worker delegate's runtime is signalled.
	for _, grandchild := range childSession.liveSubagentSessions() {
		recs, err := s.stopDelegateSubtree(grandchild)
		stopped = append(stopped, recs...)
		stopErr = errors.Join(stopErr, err)
	}
	jm := childSession.jobManager
	if jm == nil || jm.store == nil {
		return stopped, stopErr
	}
	for _, jobID := range jm.runningJobIDs() {
		rec, err := jm.stop(jobID)
		if err != nil {
			stopErr = errors.Join(stopErr, err)
			continue
		}
		if rec != nil {
			stopped = append(stopped, rec)
		}
	}
	return stopped, stopErr
}

// directChildOwningDescendant returns the caller's direct-child session whose
// LIVE subtree owns jobID (spec §2's non-direct-descendant case). It is used to
// build the not_controllable guidance: a caller cannot directly control a job it
// does not own, but it CAN stop the direct delegate at the top of that job's
// subtree. Returns nil if jobID is not a known descendant of any live direct
// child. Leaf-lock: the per-hop resolution is the same single-hop step the live
// walk uses; no lock is held across the recursion.
func (s *Session) directChildOwningDescendant(jobID string) *Session {
	if s == nil {
		return nil
	}
	for _, child := range s.liveSubagentSessions() {
		// The direct child itself owns the job (a one-hop descendant).
		if child.jobManager != nil && child.jobManager.store != nil {
			if recs, err := child.jobManager.store.Load(); err == nil {
				if rec := recs[jobID]; rec != nil && rec.OwnerSessionID == child.id {
					return child
				}
			}
		}
		// A session deeper in this child's live subtree owns the job (two+ hops).
		if _, _, _, _, ok := child.resolveDescendantJobOwner(jobID); ok {
			return child
		}
	}
	return nil
}

// directDelegateJobForChild finds the caller's own delegate job whose child
// session is childID (its transcript_ref encodes childID). This is the
// controllable handle the not_controllable guidance names: stopping it
// cascade-stops the named descendant's subtree.
func (s *Session) directDelegateJobForChild(childID string) string {
	local, err := sessionJobManager(s)
	if err != nil {
		return ""
	}
	recs, err := local.store.Load()
	if err != nil {
		return ""
	}
	want := encodeRef("", childID)
	for jobID, rec := range recs {
		if rec.Type != jobstore.JobDelegate || rec.OwnerSessionID != s.id {
			continue
		}
		if rec.TranscriptRef == want {
			return jobID
		}
	}
	return ""
}

func (jm *jobManager) forwardLocked(e jobstore.Event) error {
	if jm.forward == nil || jm.parentJobID == "" {
		return nil
	}
	return jm.forward(e)
}

func (jm *jobManager) forwardSnapshot(e jobstore.Event) error {
	jm.mu.Lock()
	forward := jm.forward
	parentJobID := jm.parentJobID
	jm.mu.Unlock()
	if forward == nil || parentJobID == "" {
		return nil
	}
	return forward(e)
}

func (jm *jobManager) recoverForwardedTerminalEvents() error {
	jm.mu.Lock()
	forward := jm.forward
	parentJobID := jm.parentJobID
	jm.mu.Unlock()
	if forward == nil || parentJobID == "" {
		return nil
	}

	recs, err := jm.store.Load()
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if !jm.shouldRecoverForwardedTerminalRecord(rec, parentJobID) {
			continue
		}
		startedAt := rec.StartedAt
		if err := forward(jobstore.Event{
			Kind:             jobstore.EventJobStarted,
			TS:               startedAt,
			JobID:            rec.JobID,
			Type:             rec.Type,
			Command:          rec.Command,
			Task:             rec.Task,
			Description:      rec.Description,
			ParentSessionID:  rec.ParentSessionID,
			OwnerSessionID:   rec.OwnerSessionID,
			VisibleToSession: rec.VisibleToSession,
			ParentJobID:      rec.ParentJobID,
			OriginTurnID:     rec.OriginTurnID,
			OriginToolCallID: rec.OriginToolCallID,
			DelegateRestore:  rec.DelegateRestore,
			StartedAt:        &startedAt,
			OutputPath:       rec.OutputPath,
			TranscriptRef:    rec.TranscriptRef,
		}); err != nil {
			return err
		}
		finishedAt := jm.recoveredEventTime(rec)
		if err := forward(jobstore.Event{
			Kind:                   jobstore.EventJobFinished,
			TS:                     finishedAt,
			JobID:                  rec.JobID,
			Status:                 rec.Status,
			Reason:                 rec.Reason,
			ExitCode:               rec.ExitCode,
			EndedAt:                rec.EndedAt,
			OutputBytes:            rec.OutputBytes,
			StructuredResult:       rec.StructuredResult,
			StructuredResultValid:  rec.StructuredResultValid,
			StructuredResultReason: rec.StructuredResultReason,
			TerminalGen:            rec.TerminalGen,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (jm *jobManager) recoverForwardedPendingNotifications() error {
	jm.mu.Lock()
	forward := jm.forward
	parentJobID := jm.parentJobID
	jm.mu.Unlock()
	if forward == nil || parentJobID == "" {
		return nil
	}

	recs, err := jm.store.Load()
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if !jm.shouldRecoverForwardedTerminalRecord(rec, parentJobID) || rec.NotifyState != jobstore.NotifyPending {
			continue
		}
		if err := forward(jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          jm.recoveredEventTime(rec),
			JobID:       rec.JobID,
			TerminalGen: rec.TerminalGen,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (jm *jobManager) shouldRecoverForwardedTerminalRecord(rec *jobstore.JobRecord, parentJobID string) bool {
	if rec == nil ||
		rec.ParentJobID != parentJobID ||
		rec.OwnerSessionID != jm.sessionID ||
		!rec.Status.IsTerminal() ||
		rec.TerminalGen == "" {
		return false
	}
	return rec.Status != jobstore.StatusFailed || rec.Reason != "forward_failed"
}

func (jm *jobManager) recoveredEventTime(rec *jobstore.JobRecord) time.Time {
	if rec.EndedAt != nil {
		return *rec.EndedAt
	}
	return jm.now()
}

func (jm *jobManager) forwardEvent(e jobstore.Event) error {
	e.VisibleToSession = jm.sessionID
	if err := jm.store.Append(e); err != nil {
		return err
	}
	if e.Kind != jobstore.EventJobNotificationPending || jm.enqueue == nil {
		return nil
	}
	recs, err := jm.store.Load()
	if err != nil {
		return err
	}
	rec := recs[e.JobID]
	if rec == nil || !jobstore.ShouldDeliver(rec) {
		return nil
	}
	// Owner-scoped notifications (spec §3/§10 drive-down): a forwarded event only
	// ever arrives from a CHILD, so the record is owned by a descendant, never by
	// this session. Appending it above preserves visibility (the parent can still
	// job_list down the tree) and the drive signal, but pushing it onto this
	// session's rail would interrupt the parent about a job it did not create. The
	// owner (the subagent) renders it on its own rail and is driven; this session
	// is notified only about its own jobs (incl. its direct delegates, which
	// finalize through the parent's own jm.enqueue, not forwardEvent). This is the
	// live counterpart of the restore-path filter in armPendingTerminalNotifications.
	if rec.OwnerSessionID != "" && rec.OwnerSessionID != jm.sessionID {
		return nil
	}
	jm.enqueue(jobNotificationFromRecord(rec))
	return nil
}
