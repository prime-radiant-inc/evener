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
func (s *Session) walkDescendantJobs(filter listFilter) []jobListEntry {
	merged := map[string]descendantWalkRow{}
	s.collectDescendantJobs(s, 0, filter, merged)

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
		entry := projectJobRecord(s, row.rec)
		entry.Depth = row.depth
		jobs = append(jobs, entry)
	}
	return jobs
}

type descendantWalkRow struct {
	rec     *jobstore.JobRecord
	depth   int
	isOwner bool
}

// collectDescendantJobs reads node's store (leaf-lock), merges its rows into
// merged with the owner-authoritative dedupe rule, then recurses into node's
// LIVE direct children at depth+1.
func (s *Session) collectDescendantJobs(node *Session, depth int, filter listFilter, merged map[string]descendantWalkRow) {
	jm, err := sessionJobManager(node)
	if err != nil {
		return
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
		return
	}
	for _, rec := range recs {
		isOwner := rec.OwnerSessionID == jm.sessionID
		existing, seen := merged[rec.JobID]
		// Owner record wins over a forwarded copy. Between two records of the
		// same authority the shallower (already-recorded) one is kept.
		if seen && (existing.isOwner || !isOwner) {
			continue
		}
		merged[rec.JobID] = descendantWalkRow{rec: rec, depth: depth, isOwner: isOwner}
	}

	if node.subagents == nil {
		return
	}
	for _, child := range node.liveSubagentSessions() {
		s.collectDescendantJobs(child, depth+1, filter, merged)
	}
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
// caller), the owner's record, and whether the owner was found.
//
// Leaf-lock discipline matches the walk: each store is read under its own lock
// (ownerJobManagerFor loads the node's store; liveSubagentSessions takes each
// sub.mu only to read the closed flag), and NO jobManager or session lock is
// held across the recursion.
func (s *Session) resolveDescendantJobOwner(jobID string) (*jobManager, *Session, *jobstore.JobRecord, bool) {
	if s == nil {
		return nil, nil, nil, false
	}
	for _, child := range s.liveSubagentSessions() {
		// One single-hop step at this node: a job owned by a DIRECT child of
		// child is reachable through child.ownerJobManagerFor.
		if owner, rec := child.ownerJobManagerFor(jobID); owner != nil {
			ownerSess := liveSubagentSession(child.subagents, rec.OwnerSessionID)
			if found, err := findJobRecord(owner, jobID); err == nil && ownerSess != nil {
				return owner, ownerSess, found, true
			}
		}
		// The child itself may own the job (a depth >= 2 owner with no further
		// descendants below it).
		if child.jobManager != nil && child.jobManager.store != nil {
			if recs, err := child.jobManager.store.Load(); err == nil {
				if rec := recs[jobID]; rec != nil && rec.OwnerSessionID == child.id {
					return child.jobManager, child, rec, true
				}
			}
		}
		// Recurse deeper into the live subtree.
		if owner, ownerSess, rec, ok := child.resolveDescendantJobOwner(jobID); ok {
			return owner, ownerSess, rec, ok
		}
	}
	return nil, nil, nil, false
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
		return nil, fmt.Errorf("not_controllable: nested job %q owner runtime is not live", jobID)
	}
	return local.stop(jobID)
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
	jm.enqueue(jobNotificationFromRecord(rec))
	return nil
}
