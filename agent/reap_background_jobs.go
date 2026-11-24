package agent

import (
	"context"
	"path/filepath"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/execsupport/procgroup"
)

// ReapSessionBackgroundJobs ends the process groups of the background shell jobs
// that a daemon left running when it died without teardown (SIGKILL, crash, OOM:
// #4843). A background job outlives a turn but not its Evener session; the
// daemon's own teardown covers a clean exit, and this covers the exits that skip
// it.
//
// It covers sessionID's own journal and the journals of the delegate sessions
// under it: a job's start event is forwarded only to the immediate parent, so a
// nested delegate's record sits in its parent's journal rather than the root's,
// and a delegate's own journal holds it too. skip names a session whose jobs are
// not this call's to reap — a daemon has taken that session back — and nil reaps
// every session named.
//
// Only a running background record with a recorded group is a candidate, and a
// group is signaled only while its leader's start time still matches the record,
// so a recycled pid/pgid is never signaled. A detached command leaves no job
// record at all (it runs outside Evener's process set), so it is never reaped
// here. A journal with nothing decodable reaps nothing.
//
// Best-effort by design: recovery must not fail on cleanup. Safe to run more
// than once — a second pass finds the group's identity gone.
func ReapSessionBackgroundJobs(stateDir, sessionID string, skip func(sessionID string) bool) int {
	reaped := 0
	for _, id := range sessionSubtreeJournals(stateDir, sessionID) {
		if skip != nil && skip(id) {
			continue
		}
		reaped += reapBackgroundJournal(stateDir, id)
	}
	return reaped
}

// sessionSubtreeJournals names sessionID and the delegate sessions owned under
// it. The journal is the only parentage record that survives a crash, and a
// missing one simply means the session has no delegates.
func sessionSubtreeJournals(stateDir, sessionID string) []string {
	ids := []string{sessionID}
	descendants, err := SessionOwnedDelegateIDs(context.Background(), stateDir, sessionID)
	if err != nil {
		return ids
	}
	return append(ids, descendants...)
}

// reapBackgroundJournal reaps one session's surviving groups.
func reapBackgroundJournal(stateDir, sessionID string) int {
	// ReadEvents returns the events it decoded alongside a corruption error: a
	// damaged journal still states true things in its valid prefix, and a running
	// background record there names a group to reap. Only a journal with nothing
	// decodable names none.
	events, _ := jobstore.ReadEvents(filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl"))
	reaped := 0
	for _, rec := range jobstore.Fold(events) {
		if reapBackgroundProcessGroup(rec) {
			reaped++
		}
	}
	return reaped
}

// reapBackgroundProcessGroup ends the record's process group when the record
// names a running background shell job and the group's leader still carries the
// recorded start time. It reports whether it signaled the group at all.
//
// The group is killed at once, with no SIGTERM grace: the owning daemon is gone,
// so the job owes nobody a graceful stop, and a grace period cannot be made
// safe. A SIGTERM that ended the leader would let init reap it and free its pid,
// and the fresh identity check needed before any escalation would then refuse to
// signal — leaving descendants that ignore SIGTERM alive and holding their
// ports. One verified SIGKILL ends the whole group instead.
func reapBackgroundProcessGroup(rec *jobstore.JobRecord) bool {
	if rec == nil || !rec.Background || rec.Status != jobstore.StatusRunning ||
		rec.ProcessGroupID <= 0 || rec.ProcessStartTime == nil {
		return false
	}
	pgid, started := rec.ProcessGroupID, *rec.ProcessStartTime
	if !processIdentityMatches(pgid, started) {
		return false
	}
	procgroup.Kill(pgid)
	return true
}

// processIdentityMatches reports whether pid is still the process the durable
// record names. An unreadable start time is a mismatch: doubt never signals.
func processIdentityMatches(pid int, started time.Time) bool {
	live, ok := processStartedAt(pid)
	return ok && live.Equal(started)
}
