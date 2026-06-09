package jobstore

import "sort"

// Fold reconstructs the current JobRecord for each job by applying events in
// seq order. The first job_finished for a job fixes its terminal_generation and
// terminal status; later terminal writes do not overwrite them.
func Fold(events []Event) map[string]*JobRecord {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	recs := make(map[string]*JobRecord)
	for _, e := range sorted {
		r := recs[e.JobID]
		if r == nil {
			r = &JobRecord{JobID: e.JobID, NotifyState: NotifyNotArmed}
			recs[e.JobID] = r
		}
		applyEvent(r, e)
	}
	return recs
}

func applyEvent(r *JobRecord, e Event) {
	switch e.Kind {
	case EventJobStarted:
		r.Type = e.Type
		r.Command = e.Command
		r.Task = e.Task
		r.Description = e.Description
		r.ParentSessionID = e.ParentSessionID
		r.OwnerSessionID = e.OwnerSessionID
		r.VisibleToSession = e.VisibleToSession
		r.ParentJobID = e.ParentJobID
		r.OriginTurnID = e.OriginTurnID
		r.OriginToolCallID = e.OriginToolCallID
		if e.StartedAt != nil {
			r.StartedAt = *e.StartedAt
		}
		if r.Status == "" {
			r.Status = StatusRunning
		}
	case EventJobSessionAssigned:
		r.TranscriptRef = e.TranscriptRef
		r.Resumable = e.Resumable
		r.NotResumableWhy = e.NotResumableWhy
	case EventJobFinished:
		// First terminal write wins; later ones are duplicates/reconstructions.
		if r.Status.IsTerminal() {
			return
		}
		r.Status = e.Status
		r.Reason = e.Reason
		r.ExitCode = e.ExitCode
		r.EndedAt = e.EndedAt
		r.OutputBytes = e.OutputBytes
		r.TerminalGen = e.TerminalGen
	case EventJobMessageSent:
		// No record-field mutation; message events are diagnostic/history.
	case EventJobNotificationPending:
		if r.NotifyState == NotifyNotArmed {
			r.NotifyState = NotifyPending
		}
	case EventJobNotificationDelivered:
		r.NotifyState = NotifyDelivered
	}
}
