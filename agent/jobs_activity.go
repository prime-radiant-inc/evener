package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/appwire"
)

// activitySessionSnapshot is the lock-free input to the activity projection.
// Traversal owns constructing it; projection only consumes cloned job records.
type activitySessionSnapshot struct {
	SessionID string
	Ref       string
	Label     string
	Jobs      []*jobstore.JobRecord
	LiveJobs  map[string]*jobstore.JobRecord
	Delegates map[string]*jobstore.DelegateRecord
	Children  map[string]*activitySessionSnapshot // child session ID
	Errors    map[string]error                    // child session ID
}

// activityBudget carries projection-local traversal state. Response bounds are
// added by the traversal layer; the projection already uses the same object to
// prevent malformed snapshot cycles from recursing indefinitely.
type activityBudget struct {
	visiting map[string]bool
}

func newActivityBudget() *activityBudget {
	return &activityBudget{visiting: make(map[string]bool)}
}

// mergeActivityRecords overlays live records on durable history without
// mutating either input. Durable records retain their append positions. Jobs
// visible only in the live map are inserted by (StartedAt, JobID).
func mergeActivityRecords(durable []*jobstore.JobRecord, live map[string]*jobstore.JobRecord) []*jobstore.JobRecord {
	durableOrder := make([]string, 0, len(durable))
	durableByID := make(map[string]*jobstore.JobRecord, len(durable))
	for _, rec := range durable {
		if rec == nil || rec.JobID == "" {
			continue
		}
		if _, seen := durableByID[rec.JobID]; !seen {
			durableOrder = append(durableOrder, rec.JobID)
		}
		durableByID[rec.JobID] = rec
	}

	liveByID := make(map[string]*jobstore.JobRecord, len(live))
	liveKeys := make([]string, 0, len(live))
	for key := range live {
		liveKeys = append(liveKeys, key)
	}
	sort.Strings(liveKeys)
	for _, key := range liveKeys {
		rec := live[key]
		if rec == nil || rec.JobID == "" {
			continue
		}
		// A malformed map with duplicate record IDs still has a deterministic
		// winner. Normal manager snapshots are keyed by rec.JobID.
		if _, seen := liveByID[rec.JobID]; !seen {
			liveByID[rec.JobID] = rec
		}
	}

	merged := make([]*jobstore.JobRecord, 0, len(durableByID)+len(liveByID))
	seen := make(map[string]bool, len(durableByID)+len(liveByID))
	for _, jobID := range durableOrder {
		rec := durableByID[jobID]
		if liveRec := liveByID[jobID]; liveRec != nil {
			rec = liveRec
		}
		merged = append(merged, cloneActivityRecord(rec))
		seen[jobID] = true
	}

	liveOnly := make([]*jobstore.JobRecord, 0, len(liveByID))
	for jobID, rec := range liveByID {
		if !seen[jobID] {
			liveOnly = append(liveOnly, cloneActivityRecord(rec))
		}
	}
	sort.Slice(liveOnly, func(i, j int) bool {
		return activityRecordBefore(liveOnly[i], liveOnly[j])
	})
	for _, rec := range liveOnly {
		at := len(merged)
		for i, current := range merged {
			if activityRecordBefore(rec, current) {
				at = i
				break
			}
		}
		merged = append(merged, nil)
		copy(merged[at+1:], merged[at:])
		merged[at] = rec
	}
	return merged
}

func cloneActivityRecord(rec *jobstore.JobRecord) *jobstore.JobRecord {
	if rec == nil {
		return nil
	}
	clone := *rec
	if rec.EndedAt != nil {
		ended := *rec.EndedAt
		clone.EndedAt = &ended
	}
	if rec.ExitCode != nil {
		exit := *rec.ExitCode
		clone.ExitCode = &exit
	}
	if rec.LastActivity != nil {
		last := *rec.LastActivity
		clone.LastActivity = &last
	}
	if rec.Resumable != nil {
		resumable := *rec.Resumable
		clone.Resumable = &resumable
	}
	if rec.StructuredResultValid != nil {
		valid := *rec.StructuredResultValid
		clone.StructuredResultValid = &valid
	}
	if rec.DelegateRestore != nil {
		restore := *rec.DelegateRestore
		restore.FrozenToolNames = append([]string(nil), rec.DelegateRestore.FrozenToolNames...)
		restore.FrozenSkillNames = append([]string(nil), rec.DelegateRestore.FrozenSkillNames...)
		restore.FrozenSkillBodies = append([]string(nil), rec.DelegateRestore.FrozenSkillBodies...)
		restore.ExplicitToolGrants = append([]string(nil), rec.DelegateRestore.ExplicitToolGrants...)
		clone.DelegateRestore = &restore
	}
	return &clone
}

func activityRecordBefore(left, right *jobstore.JobRecord) bool {
	if left.StartedAt.Equal(right.StartedAt) {
		return left.JobID < right.JobID
	}
	return left.StartedAt.Before(right.StartedAt)
}

func projectActivitySession(snapshot activitySessionSnapshot, budget *activityBudget) appwire.JobActivitySession {
	if budget == nil {
		budget = newActivityBudget()
	}
	projected := appwire.JobActivitySession{
		SessionID: snapshot.SessionID,
		Ref:       snapshot.Ref,
		Label:     snapshot.Label,
		Entries:   make([]appwire.JobActivityEntry, 0),
	}

	cycleKey := snapshot.SessionID + "\x00" + snapshot.Ref
	if budget.visiting == nil {
		budget.visiting = make(map[string]bool)
	}
	if budget.visiting[cycleKey] {
		projected.Branch.Error = fmt.Sprintf("activity cycle at session %q", snapshot.SessionID)
		projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
		return projected
	}
	budget.visiting[cycleKey] = true
	defer delete(budget.visiting, cycleKey)

	records := mergeActivityRecords(snapshot.Jobs, snapshot.LiveJobs)
	delegateEntries := make(map[string]int)
	for _, rec := range records {
		if rec == nil {
			continue
		}
		switch rec.Type {
		case jobstore.JobShell:
			job := projectActivityJob(rec, snapshot.Ref)
			projected.Entries = append(projected.Entries, appwire.JobActivityEntry{Kind: "shell", Job: &job})
		case jobstore.JobDelegate:
			if rec.DelegateID == "" {
				appendActivityBranchError(&projected.Branch, fmt.Sprintf("delegate job %q has no delegate id", rec.JobID))
				continue
			}
			if index, ok := delegateEntries[rec.DelegateID]; ok {
				delegate := projected.Entries[index].Delegate
				delegate.Turns = append(delegate.Turns, projectActivityJob(rec, snapshot.Ref))
				if delegate.Mandate == "" {
					delegate.Mandate = activityMandate(rec)
				}
				continue
			}

			delegate := projectActivityDelegate(snapshot, rec, budget)
			projected.Entries = append(projected.Entries, appwire.JobActivityEntry{Kind: "delegate", Delegate: &delegate})
			delegateEntries[rec.DelegateID] = len(projected.Entries) - 1
		default:
			appendActivityBranchError(&projected.Branch, fmt.Sprintf("job %q has unsupported type %q", rec.JobID, rec.Type))
		}
	}

	projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
	return projected
}

func projectActivityDelegate(snapshot activitySessionSnapshot, anchor *jobstore.JobRecord, budget *activityBudget) appwire.JobActivityDelegate {
	delegate := appwire.JobActivityDelegate{
		DelegateID: anchor.DelegateID,
		Mandate:    activityMandate(anchor),
		Turns:      []appwire.JobActivityJob{projectActivityJob(anchor, snapshot.Ref)},
	}
	record := snapshot.Delegates[anchor.DelegateID]
	if record == nil {
		delegate.Branch.Error = fmt.Sprintf("delegate %q record unavailable", anchor.DelegateID)
		return delegate
	}
	if record.DelegateID != anchor.DelegateID {
		delegate.Branch.Error = fmt.Sprintf("delegate %q record identifies %q", anchor.DelegateID, record.DelegateID)
		return delegate
	}
	if record.ChildSessionID == "" || record.TranscriptRef == "" {
		delegate.Branch.Error = fmt.Sprintf("delegate %q has an incomplete child link", anchor.DelegateID)
		return delegate
	}

	delegate.ChildSessionID = record.ChildSessionID
	delegate.ChildRef = record.TranscriptRef
	if err := snapshot.Errors[record.ChildSessionID]; err != nil {
		delegate.Branch.Error = err.Error()
		return delegate
	}
	child := snapshot.Children[record.ChildSessionID]
	if child == nil {
		delegate.Branch.Error = fmt.Sprintf("child session %q unavailable", record.ChildSessionID)
		return delegate
	}
	if child.SessionID != record.ChildSessionID || child.Ref != record.TranscriptRef {
		delegate.Branch.Error = fmt.Sprintf("delegate %q child link does not match loaded session", anchor.DelegateID)
		return delegate
	}
	projectedChild := projectActivitySession(*child, budget)
	delegate.Child = &projectedChild
	return delegate
}

func activityMandate(rec *jobstore.JobRecord) string {
	if rec == nil {
		return ""
	}
	if rec.Task != "" {
		return rec.Task
	}
	return rec.Description
}

func appendActivityBranchError(branch *appwire.JobActivityBranchState, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if branch.Error == "" {
		branch.Error = message
		return
	}
	branch.Error += "; " + message
}

func activityOutcome(status jobstore.Status) (bool, string) {
	switch status {
	case jobstore.StatusRunning:
		return false, ""
	case jobstore.StatusFailed, jobstore.StatusExhausted:
		return true, "failure"
	case jobstore.StatusCompleted:
		return true, "success"
	case jobstore.StatusCancelled, jobstore.StatusStopped:
		return true, "neutral"
	default:
		return status.IsTerminal(), ""
	}
}

func aggregateActivity(entries []appwire.JobActivityEntry, branch appwire.JobActivityBranchState) (appwire.JobActivityCounts, string) {
	counts := appwire.JobActivityCounts{Complete: activityBranchComplete(branch)}
	addJob := func(job appwire.JobActivityJob) {
		if !job.Terminal {
			counts.Active++
			return
		}
		if job.Outcome == "failure" {
			counts.Failed++
			return
		}
		counts.Completed++
	}
	for _, entry := range entries {
		if entry.Job != nil {
			addJob(*entry.Job)
		}
		if entry.Delegate == nil {
			continue
		}
		for _, turn := range entry.Delegate.Turns {
			addJob(turn)
		}
		if !activityBranchComplete(entry.Delegate.Branch) {
			counts.Complete = false
		}
		if entry.Delegate.Child != nil {
			counts.Active += entry.Delegate.Child.Counts.Active
			counts.Failed += entry.Delegate.Child.Counts.Failed
			counts.Completed += entry.Delegate.Child.Counts.Completed
			if !entry.Delegate.Child.Counts.Complete {
				counts.Complete = false
			}
		}
	}

	switch {
	case counts.Active > 0:
		return counts, "working"
	case counts.Failed > 0:
		return counts, "failed"
	case !counts.Complete:
		return counts, "unavailable"
	case counts.Completed > 0:
		return counts, "ended"
	default:
		return counts, "idle"
	}
}

func activityBranchComplete(branch appwire.JobActivityBranchState) bool {
	return branch.Error == "" && !branch.Truncated && branch.Continuation == ""
}

// projectActivityJob projects the shared job fields used by both the activity
// tree and the temporary flat jobs-list compatibility path.
func projectActivityJob(rec *jobstore.JobRecord, ownerRef string) appwire.JobActivityJob {
	if rec == nil {
		return appwire.JobActivityJob{}
	}
	description := rec.Description
	if description == "" {
		description = rec.Command
	}
	if description == "" {
		description = rec.Task
	}
	terminal, outcome := activityOutcome(rec.Status)
	job := appwire.JobActivityJob{
		JobID:          rec.JobID,
		OwnerSessionID: rec.OwnerSessionID,
		OwnerRef:       ownerRef,
		Type:           string(rec.Type),
		Status:         string(rec.Status),
		Outcome:        outcome,
		Terminal:       terminal,
		Background:     rec.Background,
		HasOutput:      rec.OutputPath != "" || rec.OutputBytes > 0,
		Description:    description,
		Command:        rec.Command,
		Task:           rec.Task,
		Reason:         rec.Reason,
		StartedAt:      rec.StartedAt.UTC().Format(time.RFC3339),
		OutputBytes:    rec.OutputBytes,
	}
	if rec.EndedAt != nil {
		job.EndedAt = rec.EndedAt.UTC().Format(time.RFC3339)
	}
	if rec.ExitCode != nil {
		exit := *rec.ExitCode
		job.ExitCode = &exit
	}
	return job
}
