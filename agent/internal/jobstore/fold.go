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
		if !isJobRecordEventKind(e.Kind) {
			continue
		}
		r := recs[e.JobID]
		if r == nil {
			r = &JobRecord{JobID: e.JobID, NotifyState: NotifyNotArmed}
			recs[e.JobID] = r
		}
		applyEvent(r, e)
	}
	return recs
}

// FoldOrdered folds events to records (like Fold) and returns them ordered by
// each job's FIRST event seq — the durable append order. Two records appended in
// sequence sort by which was created first; this is the total order the
// append-only log defines, independent of any wall-clock field.
func FoldOrdered(events []Event) []*JobRecord {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	recs := make(map[string]*JobRecord)
	order := make([]string, 0, len(sorted))
	for _, e := range sorted {
		if !isJobRecordEventKind(e.Kind) {
			continue
		}
		r := recs[e.JobID]
		if r == nil {
			r = &JobRecord{JobID: e.JobID, NotifyState: NotifyNotArmed}
			recs[e.JobID] = r
			order = append(order, e.JobID)
		}
		applyEvent(r, e)
	}
	ordered := make([]*JobRecord, 0, len(order))
	for _, id := range order {
		ordered = append(ordered, recs[id])
	}
	return ordered
}

func isJobRecordEventKind(kind EventKind) bool {
	switch kind {
	case EventJobStarted,
		EventJobSessionAssigned,
		EventJobFinished,
		EventJobMessageSent,
		EventJobNotificationPending,
		EventJobNotificationDelivered:
		return true
	default:
		return false
	}
}

// FoldWatchSends reconstructs pending watch-send frames from durable events.
func FoldWatchSends(events []Event) WatchSendRecord {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	rec := WatchSendRecord{Pending: make(map[WatchSendKey]*WatchSendState)}
	terminalSeq := make(map[WatchSendKey]uint64)
	for _, e := range sorted {
		if e.WatchSend == nil {
			continue
		}
		key := e.WatchSend.Key
		switch e.Kind {
		case EventWatchSendPending:
			if settled, ok := terminalSeq[key]; ok && e.WatchSend.UpdateSeq <= settled {
				continue
			}
			if pending := rec.Pending[key]; pending != nil && e.WatchSend.UpdateSeq < pending.UpdateSeq {
				continue
			}
			state := *e.WatchSend
			rec.Pending[key] = &state
		case EventWatchSendDelivered, EventWatchSendDropped, EventWatchSendEvicted:
			if settled, ok := terminalSeq[key]; !ok || e.WatchSend.UpdateSeq > settled {
				terminalSeq[key] = e.WatchSend.UpdateSeq
			}
			if pending := rec.Pending[key]; pending != nil && e.WatchSend.UpdateSeq >= pending.UpdateSeq {
				delete(rec.Pending, key)
			}
		}
	}
	return rec
}

// FoldGrants reconstructs the observer read-grant table from durable events:
// observer session id → watched job ids the observer may job_read_output.
// Grants are append-only capabilities, so the fold is order-insensitive and
// duplicate (observer, job) grants fold to one entry.
func FoldGrants(events []Event) map[string]map[string]bool {
	grants := make(map[string]map[string]bool)
	for _, e := range events {
		if e.Kind != EventWatchReadGrant || e.ObserverSessionID == "" || e.JobID == "" {
			continue
		}
		jobs := grants[e.ObserverSessionID]
		if jobs == nil {
			jobs = make(map[string]bool)
			grants[e.ObserverSessionID] = jobs
		}
		jobs[e.JobID] = true
	}
	return grants
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
		r.DelegateRestore = e.DelegateRestore
		r.OutputPath = e.OutputPath
		r.TranscriptRef = e.TranscriptRef
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
		r.StructuredResult = e.StructuredResult
		r.StructuredResultValid = e.StructuredResultValid
		if e.StructuredResultValid != nil && !*e.StructuredResultValid {
			r.StructuredResultReason = e.StructuredResultReason
		}
		r.TerminalGen = e.TerminalGen
	case EventJobMessageSent:
		// No record-field mutation; message events are diagnostic/history.
	case EventJobNotificationPending:
		if !notificationMatchesTerminalGeneration(r, e) {
			return
		}
		if r.NotifyState == NotifyNotArmed {
			r.NotifyState = NotifyPending
		}
	case EventJobNotificationDelivered:
		if !notificationMatchesTerminalGeneration(r, e) {
			return
		}
		r.NotifyState = NotifyDelivered
	}
}

func notificationMatchesTerminalGeneration(r *JobRecord, e Event) bool {
	return r.TerminalGen != "" && e.TerminalGen == r.TerminalGen
}
