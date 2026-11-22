package agent

import (
	"path/filepath"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/execsupport/procgroup"
)

const (
	// backgroundReapGrace is how long a reaped group gets to end on SIGTERM
	// before SIGKILL. It is a courtesy to traps and cleanup, not a wait for
	// completion: a dead daemon's background job owes nobody its output.
	backgroundReapGrace = 300 * time.Millisecond
	// backgroundReapPoll is how often the grace window asks whether the group
	// ended. Each check re-reads the process start time, so it is not free;
	// three checks across the window are enough for a group that honors SIGTERM.
	backgroundReapPoll = 100 * time.Millisecond
)

// ReapSessionBackgroundJobs ends the process groups of sessionID's background
// shell jobs that a daemon left running when it died without teardown (SIGKILL,
// crash, OOM: #4843). A background job outlives a turn but not its Evener
// session; the daemon's own teardown covers a clean exit, and this covers the
// exits that skip it.
//
// Only a running background record with a recorded group is a candidate, and a
// group is signaled only while its leader's start time still matches the
// record, so a recycled pid/pgid is never signaled. A detached command leaves no
// job record at all (it runs outside Evener's process set), so it is never
// reaped here. A journal with nothing decodable reaps nothing.
//
// Best-effort by design: recovery must not fail on cleanup. Safe to run more
// than once — a second pass finds the group's identity gone.
func ReapSessionBackgroundJobs(stateDir, sessionID string) int {
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
// The identity is checked again before the SIGKILL escalation: between the two
// signals the leader can end and free its pid, and a new process could lead a
// group with the number. A member that ignores SIGTERM therefore survives when
// the leader has already gone — SIGTERM already reached every member, and a
// group nobody can verify is not force-killed.
func reapBackgroundProcessGroup(rec *jobstore.JobRecord) bool {
	if rec == nil || !rec.Background || rec.Status != jobstore.StatusRunning ||
		rec.ProcessGroupID <= 0 || rec.ProcessStartTime == nil {
		return false
	}
	pgid, started := rec.ProcessGroupID, *rec.ProcessStartTime
	if !processIdentityMatches(pgid, started) {
		return false
	}
	procgroup.Terminate(pgid)
	deadline := time.Now().Add(backgroundReapGrace)
	for time.Now().Before(deadline) {
		if !processIdentityMatches(pgid, started) {
			return true
		}
		time.Sleep(backgroundReapPoll)
	}
	if processIdentityMatches(pgid, started) {
		procgroup.Kill(pgid)
	}
	return true
}

// processIdentityMatches reports whether pid is still the process the durable
// record names. An unreadable start time is a mismatch: doubt never signals.
func processIdentityMatches(pid int, started time.Time) bool {
	live, ok := processStartedAt(pid)
	return ok && live.Equal(started)
}
