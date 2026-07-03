package jobstore

import (
	"sort"

	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/invariant"
)

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

func FoldDelegates(events []Event) map[string]*DelegateRecord {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	delegates := make(map[string]*DelegateRecord)
	jobToDelegate := make(map[string]string)
	for _, e := range sorted {
		switch e.Kind {
		case EventDelegateCreated:
			if e.DelegateID == "" || e.Delegate == nil {
				continue
			}
			d := delegates[e.DelegateID]
			if d == nil {
				d = &DelegateRecord{DelegateID: e.DelegateID, Status: DelegateIdle}
				delegates[e.DelegateID] = d
			}
			d.ChildSessionID = e.Delegate.ChildSessionID
			d.TranscriptRef = e.Delegate.TranscriptRef
			d.OwnerSessionID = e.Delegate.OwnerSessionID
			d.VisibleSessionID = e.Delegate.VisibleSessionID
			d.ParentDelegateID = e.Delegate.ParentDelegateID
			d.AgentType = e.Delegate.AgentType
			d.Generation = e.Delegate.Generation
			d.Resumable = e.Delegate.Resumable
			d.NotResumableWhy = e.Delegate.NotResumableWhy
		case EventJobStarted:
			if e.DelegateID == "" {
				continue
			}
			d := delegates[e.DelegateID]
			if d == nil {
				d = &DelegateRecord{DelegateID: e.DelegateID}
				delegates[e.DelegateID] = d
			}
			jobToDelegate[e.JobID] = e.DelegateID
			if e.OwnerSessionID != "" {
				d.OwnerSessionID = e.OwnerSessionID
			}
			if e.VisibleToSession != "" {
				d.VisibleSessionID = e.VisibleToSession
			}
			d.CurrentJobID = e.JobID
			d.LatestJobID = e.JobID
			d.Status = DelegateRunning
			d.StopGateClosed = false
		case EventJobSessionAssigned:
			delegateID := jobToDelegate[e.JobID]
			if delegateID == "" {
				continue
			}
			d := delegates[delegateID]
			if d == nil {
				continue
			}
			if e.TranscriptRef != "" {
				d.TranscriptRef = e.TranscriptRef
			}
			if e.Resumable != nil {
				d.Resumable = *e.Resumable
				d.NotResumableWhy = e.NotResumableWhy
				switch d.Status {
				case "", DelegateIdle, DelegateNotResumable:
					if d.Resumable {
						d.Status = DelegateIdle
					} else {
						d.Status = DelegateNotResumable
					}
				}
			}
		case EventJobFinished:
			delegateID := jobToDelegate[e.JobID]
			if delegateID == "" {
				continue
			}
			d := delegates[delegateID]
			if d == nil {
				continue
			}
			if d.CurrentJobID != e.JobID {
				continue
			}
			d.CurrentJobID = ""
			if e.Status == StatusStopped || e.Status == StatusCancelled {
				d.Status = DelegateStopped
			} else if d.Resumable {
				d.Status = DelegateIdle
			} else {
				d.Status = DelegateNotResumable
			}
		case EventDelegateStopGateClosed:
			if e.DelegateID == "" || e.Delegate == nil {
				continue
			}
			d := delegates[e.DelegateID]
			if d == nil {
				d = &DelegateRecord{DelegateID: e.DelegateID}
				delegates[e.DelegateID] = d
			}
			if d.CurrentJobID != "" && d.CurrentJobID != e.Delegate.StopJobID {
				continue
			}
			if d.CurrentJobID == "" && d.LatestJobID != "" && d.LatestJobID != e.Delegate.StopJobID {
				continue
			}
			d.Generation = e.Delegate.Generation
			d.StopGateClosed = true
			d.StopGateClosedJobID = e.Delegate.StopJobID
			if d.CurrentJobID == e.Delegate.StopJobID {
				d.CurrentJobID = ""
			}
			if d.Status == "" || d.Status == DelegateRunning {
				d.Status = DelegateStopped
			}
		}
	}
	return delegates
}

func FoldWatches(events []Event) map[string]*WatchRecord {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	watches := make(map[string]*WatchRecord)
	for _, e := range sorted {
		if e.WatchID == "" || e.Watch == nil {
			continue
		}
		switch e.Kind {
		case EventWatchRegistered:
			if e.Watch.Generation == "" || e.Watch.OwnerSessionID == "" ||
				e.Watch.VisibleSessionID == "" || e.Watch.Target == "" || e.Watch.ConfigHash == "" {
				continue
			}
			watches[e.WatchID] = &WatchRecord{
				WatchID:          e.WatchID,
				Generation:       e.Watch.Generation,
				OwnerSessionID:   e.Watch.OwnerSessionID,
				VisibleSessionID: e.Watch.VisibleSessionID,
				Target:           e.Watch.Target,
				SendTo:           e.Watch.SendTo,
				ConfigHash:       e.Watch.ConfigHash,
				Condition:        e.Watch.Condition,
				Deliveries:       e.Watch.Deliveries,
				Active:           true,
			}
		case EventWatchCleared:
			if e.Watch.Generation == "" {
				continue
			}
			w := watches[e.WatchID]
			if w == nil || w.Generation != e.Watch.Generation || !w.Active {
				continue
			}
			w.Active = false
			w.EndReason = e.Watch.EndReason
		}
	}
	return watches
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
			state.Provenance = provenance.Clone(e.WatchSend.Provenance)
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
	// A job's lifecycle only advances: once its status is terminal the first
	// terminal write is final (no later event reopens it), and the
	// notification state climbs not_armed -> pending -> delivered and never
	// falls back. These are the reducer's load-bearing monotonicity guarantees;
	// asserting them at the mutation point traps a regression here rather than at
	// some distant reader. The condition re-reads r after the switch, so it is
	// guarded out of production builds along with the no-op Hold.
	if invariant.Enabled {
		prevStatus := r.Status
		prevNotify := notifyRank(r.NotifyState)
		defer func() {
			invariant.Hold(!prevStatus.IsTerminal() || r.Status == prevStatus,
				"job %s terminal status regressed: %s -> %s", r.JobID, prevStatus, r.Status)
			invariant.Hold(notifyRank(r.NotifyState) >= prevNotify,
				"job %s notification state regressed: rank %d -> %d", r.JobID, prevNotify, notifyRank(r.NotifyState))
		}()
	}
	switch e.Kind {
	case EventJobStarted:
		r.Type = e.Type
		r.Command = e.Command
		r.WorkingDir = e.WorkingDir
		r.Task = e.Task
		r.Description = e.Description
		r.ParentSessionID = e.ParentSessionID
		r.OwnerSessionID = e.OwnerSessionID
		r.VisibleToSession = e.VisibleToSession
		r.ParentJobID = e.ParentJobID
		r.DelegateID = e.DelegateID
		r.OriginTurnID = e.OriginTurnID
		r.OriginToolCallID = e.OriginToolCallID
		r.OriginItemID = e.OriginItemID
		r.DelegateRestore = e.DelegateRestore
		r.Provenance = provenance.Clone(e.Provenance)
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
		if e.Provenance != nil {
			r.Provenance = provenance.Clone(e.Provenance)
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
		if e.Provenance != nil {
			r.NotificationProvenance = provenance.Clone(e.Provenance)
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

// notifyRank orders the notification states so the reducer's monotonicity
// invariant can compare them: not_armed (0) precedes pending (1) precedes
// delivered (2). NotifyState is only ever set to one of these constants by the
// reducer, never read from event bytes, so the default is unreachable in
// practice and ranks the zero value conservatively as the lowest state.
func notifyRank(s NotifyState) int {
	switch s {
	case NotifyPending:
		return 1
	case NotifyDelivered:
		return 2
	default:
		return 0
	}
}
