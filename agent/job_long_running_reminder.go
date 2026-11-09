package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// longRunningJobReminderInterval is both the age at which a shell job counts as
// long-running and the cadence at which its owner is reminded about it. Shell
// jobs have no default deadline, so this reminder is what keeps a forgotten job
// from running unnoticed.
const longRunningJobReminderInterval = 30 * time.Minute

const longRunningJobCommandMaxChars = 120

// longRunningJobsReason tags the reminder notification.
const longRunningJobsReason = "long_running_jobs"

// armLongRunningReminderLocked starts the owner's reminder cadence if it is not
// already running. The cadence runs while the manager has shell jobs and ends
// itself when none remain, so a session without shell jobs holds no timer. The
// caller holds jm.mu.
func (jm *jobManager) armLongRunningReminderLocked() {
	if jm.longRunningTimer != nil || jm.closing {
		return
	}
	jm.longRunningTimer = jm.clock.AfterFunc(longRunningJobReminderInterval, jm.fireLongRunningReminder)
}

// stopLongRunningReminderLocked ends the cadence. The caller holds jm.mu.
func (jm *jobManager) stopLongRunningReminderLocked() {
	if jm.longRunningTimer != nil {
		jm.longRunningTimer.Stop()
		jm.longRunningTimer = nil
	}
}

// fireLongRunningReminder sends the owner ONE notification listing every shell
// job that has run for at least the reminder interval, on the same rail a job
// completion uses, so an idle session is woken and an active turn receives it
// with its next model request. The cadence is fixed: no backoff, no cap.
func (jm *jobManager) fireLongRunningReminder() {
	jm.mu.Lock()
	if jm.closing || jm.longRunningTimer == nil {
		jm.mu.Unlock()
		return
	}
	now := jm.now()
	var due []*jobstore.JobRecord
	shells := 0
	for _, run := range jm.running {
		if run == nil || run.rec == nil || run.rec.Type != jobstore.JobShell || run.terminal != nil {
			continue
		}
		shells++
		if now.Sub(run.rec.StartedAt) >= longRunningJobReminderInterval {
			rec := *run.rec
			due = append(due, &rec)
		}
	}
	if shells == 0 {
		jm.longRunningTimer = nil
	} else {
		jm.longRunningTimer = jm.clock.AfterFunc(longRunningJobReminderInterval, jm.fireLongRunningReminder)
	}
	jm.mu.Unlock()

	if len(due) == 0 {
		return
	}
	jm.enqueueNotifications([]jobNotification{longRunningJobsNotification(due, now)})
}

func longRunningJobsNotification(jobs []*jobstore.JobRecord, now time.Time) jobNotification {
	sort.Slice(jobs, func(i, j int) bool {
		if !jobs[i].StartedAt.Equal(jobs[j].StartedAt) {
			return jobs[i].StartedAt.Before(jobs[j].StartedAt)
		}
		return jobs[i].JobID < jobs[j].JobID
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%d shell job(s) have been running for over %d minutes and are still running. If one is unexpected, stop it with job_stop.", len(jobs), int(longRunningJobReminderInterval.Minutes()))
	for _, rec := range jobs {
		lastOutput := rec.StartedAt
		if rec.LastActivity != nil {
			lastOutput = *rec.LastActivity
		}
		command := limitWatchText(strings.Join(strings.Fields(rec.Command), " "), longRunningJobCommandMaxChars)
		fmt.Fprintf(&b, "\n- %s: running %dm, last output %dm ago: %s", rec.JobID, int(now.Sub(rec.StartedAt).Minutes()), int(now.Sub(lastOutput).Minutes()), command)
	}
	n := watchNotification("", longRunningJobsReason)
	n.Note = b.String()
	return n
}

// isLongRunningJobsReminder reports whether n is the reminder built by
// longRunningJobsNotification. Its identity on the wire is the reason attr.
func (n jobNotification) isLongRunningJobsReminder() bool {
	return n.isWatch() && n.JobID == "" && n.WatchID == "" && n.Reason == longRunningJobsReason
}
