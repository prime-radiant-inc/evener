package jobstore

import (
	"errors"
	"fmt"
	"sort"

	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/invariant"
)

// Fold reconstructs the current JobRecord for each job by applying events in
// seq order. The first job_finished for a job fixes its terminal_generation and
// terminal status; later terminal writes do not overwrite them.
func Fold(events []Event) map[string]*JobRecord {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	recs := make(map[string]*JobRecord)
	for _, e := range sorted {
		applyJobEvent(recs, e)
	}
	return recs
}

// ErrIncompleteLifecycle reports a job history whose events do not describe a
// whole job: a terminal event with no preceding start, a start missing the
// fields a shell job cannot run without, or a start whose OwnerSessionID
// contradicts the owner the job ID encodes. Fold accepts these fragments by
// design — it reconstructs the best record the log supports, and the live
// runtime repairs what it can — so a reader that must report whether a history
// is COMPLETE checks this sentinel from FoldChecked instead.
var ErrIncompleteLifecycle = errors.New("jobstore: incomplete job lifecycle")

// FoldChecked folds events exactly like Fold and then reports the first
// structural gap in the lifecycle the log describes. The returned records are
// Fold's own, so a caller that wants both the best-effort history and an
// honest verdict about it needs one pass. Errors wrap ErrIncompleteLifecycle;
// match them with errors.Is.
func FoldChecked(events []Event) (map[string]*JobRecord, error) {
	recs := Fold(events)
	if err := checkJobLifecycles(events); err != nil {
		return nil, err
	}
	return recs, nil
}

// checkJobLifecycles reports the first event that leaves a job's lifecycle
// unaccounted for, in seq order so the report is deterministic. It validates
// what a reader can prove from the journal alone: a start exists before any
// terminal or notification event for that job, a start carries the fields a
// shell job needs (a control-plane job such as a delegate row may omit them),
// and a start's declared owner agrees with the owner its job ID encodes.
//
// An EMPTY owner is accepted: rows written before OwnerSessionID existed fold
// with it unset, and rejecting them would make every pre-owner journal
// unreadable rather than merely old.
func checkJobLifecycles(events []Event) error {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	started := make(map[string]bool, len(sorted))
	for _, e := range sorted {
		if e.JobID == "" || !IsJobRecordEvent(e.Kind) {
			continue
		}
		if e.Kind == EventJobStarted {
			if err := checkJobStart(e); err != nil {
				return err
			}
			started[e.JobID] = true
			continue
		}
		if !started[e.JobID] {
			return fmt.Errorf("%w: job %q has a %s event (seq %d) with no preceding start",
				ErrIncompleteLifecycle, e.JobID, e.Kind, e.Seq)
		}
	}
	return nil
}

// checkJobStart reports a start event that cannot describe a runnable job.
// Type is required of every start: a record with no type is what a projection
// classifies as unsupported, and a start that cannot say what it started is a
// fragment, not a lifecycle. StartedAt deliberately is NOT required: the fold
// treats it as optional (a start without one folds to the zero time and the
// activity projections render that), so demanding it here would report real
// journals as damaged.
func checkJobStart(e Event) error {
	if e.Type == "" {
		return fmt.Errorf("%w: job %q starts (seq %d) with no type",
			ErrIncompleteLifecycle, e.JobID, e.Seq)
	}
	if e.OwnerSessionID == "" {
		return nil
	}
	encoded, err := identifier.JobOwnerSessionID(e.JobID)
	if err != nil {
		// A non-conforming job ID is not this check's business: synthetic IDs
		// such as tests' and older tooling's "job_x" carry no owner, and the
		// owner comparison below is what the contract is about.
		return nil //nolint:nilerr // unparseable ID carries no owner to contradict
	}
	if encoded != e.OwnerSessionID {
		return fmt.Errorf("%w: job %q names owner %q but its job ID encodes %q",
			ErrIncompleteLifecycle, e.JobID, e.OwnerSessionID, encoded)
	}
	return nil
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
		if !IsJobRecordEvent(e.Kind) {
			continue
		}
		if recs[e.JobID] == nil {
			order = append(order, e.JobID)
		}
		applyJobEvent(recs, e)
	}
	ordered := make([]*JobRecord, 0, len(order))
	for _, id := range order {
		ordered = append(ordered, recs[id])
	}
	return ordered
}

// IsJobRecordEvent reports whether an event of kind folds into a job's
// record. Every other kind leaves job records untouched.
func IsJobRecordEvent(kind EventKind) bool {
	switch kind {
	case EventJobStarted,
		EventJobFinished,
		EventJobMessageSent,
		EventJobNotificationPending,
		EventJobNotificationDelivered,
		EventJobNotificationConsumed:
		return true
	default:
		return false
	}
}

func FoldWatches(events []Event) map[string]*WatchRecord {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	watches := make(map[string]*WatchRecord)
	for _, e := range sorted {
		applyWatchEvent(watches, e)
	}
	return watches
}

// Apply extends a privately owned read fold by one already ordered journal event.
// It uses the same terminal and watch-generation authority as the full folds.
func Apply(jobs map[string]*JobRecord, watches map[string]*WatchRecord, event Event) {
	applyJobEvent(jobs, event)
	applyWatchEvent(watches, event)
}

func applyJobEvent(recs map[string]*JobRecord, event Event) {
	if !IsJobRecordEvent(event.Kind) {
		return
	}
	recs[event.JobID] = ApplyJob(recs[event.JobID], event)
}

// ApplyJob folds one job-record event (see IsJobRecordEvent) into record in
// place, with the same terminal and notification authority as Fold, and
// returns it. A nil record starts the job's record. Events must arrive in seq
// order, as Fold sorts them.
func ApplyJob(record *JobRecord, event Event) *JobRecord {
	if record == nil {
		record = &JobRecord{JobID: event.JobID, NotifyState: NotifyNotArmed}
	}
	applyEvent(record, event)
	return record
}

func applyWatchEvent(watches map[string]*WatchRecord, e Event) {
	if e.WatchID == "" || e.Watch == nil {
		return
	}
	switch e.Kind {
	case EventWatchRegistered:
		if e.Watch.Generation == "" || e.Watch.OwnerSessionID == "" ||
			e.Watch.VisibleSessionID == "" || e.Watch.Target == "" || e.Watch.ConfigHash == "" {
			return
		}
		target := e.Watch.Target
		if target == "caller" {
			target = "self"
		}
		rec := &WatchRecord{
			WatchID:          e.WatchID,
			Generation:       e.Watch.Generation,
			OwnerSessionID:   e.Watch.OwnerSessionID,
			VisibleSessionID: e.Watch.VisibleSessionID,
			Target:           target,
			SendTo:           e.Watch.SendTo,
			ConfigHash:       e.Watch.ConfigHash,
			Condition:        e.Watch.Condition,
			Deliveries:       e.Watch.Deliveries,
			Active:           true,
		}
		// The receiver rides the config snapshot, which rows written before it
		// existed do not have; those fold to an empty receiver, not a fold error.
		if e.Watch.Config != nil {
			rec.HubRef = e.Watch.Config.HubRef
			rec.ReceiverSessionID = e.Watch.Config.ReceiverSessionID
			rec.ReceiverDelegateID = e.Watch.Config.ReceiverDelegateID
			rec.Source = e.Watch.Config.Source
			rec.SourceDelegateID = e.Watch.Config.SourceDelegateID
			rec.SourceDelegateGeneration = e.Watch.Config.SourceDelegateGeneration
			rec.StableReceiver = e.Watch.Config.StableReceiver
			rec.Note = e.Watch.Config.Note
		}
		watches[e.WatchID] = rec
	case EventWatchCleared:
		if e.Watch.Generation == "" {
			return
		}
		w := watches[e.WatchID]
		if w == nil || w.Generation != e.Watch.Generation || !w.Active {
			return
		}
		w.Active = false
		w.EndReason = e.Watch.EndReason
		w.EndedAt = e.TS
		maxSeq := 0
		for _, other := range watches {
			if other != nil && other.EndedSeq > maxSeq {
				maxSeq = other.EndedSeq
			}
		}
		w.EndedSeq = maxSeq + 1
		if e.Watch.Deliveries > 0 {
			w.Deliveries = e.Watch.Deliveries
		}
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
		r.Background = r.Background || e.Background
		r.Intent = e.Intent
		r.WorkingDir = e.WorkingDir
		r.Task = e.Task
		r.Description = e.Description
		r.ParentSessionID = e.ParentSessionID
		r.OwnerSessionID = e.OwnerSessionID
		r.VisibleToSession = e.VisibleToSession
		r.ParentJobID = e.ParentJobID
		r.ParentDelegateID = e.ParentDelegateID
		r.OriginTurnID = e.OriginTurnID
		r.OriginToolCallID = e.OriginToolCallID
		r.OriginItemID = e.OriginItemID
		r.Provenance = provenance.Clone(e.Provenance)
		r.OutputPath = e.OutputPath
		if e.StartedAt != nil {
			r.StartedAt = *e.StartedAt
		}
		if r.Status == "" {
			r.Status = StatusRunning
		}
	case EventJobFinished:
		// First terminal write wins; later ones are duplicates/reconstructions.
		if r.Status.IsTerminal() {
			return
		}
		r.Status = e.Status
		r.Reason = e.Reason
		r.ExhaustionBudget = e.ExhaustionBudget
		r.ExhaustionLimit = e.ExhaustionLimit
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
	case EventJobNotificationConsumed:
		if !notificationMatchesTerminalGeneration(r, e) {
			return
		}
		// Delivered wins over consumed: rendering the notification into the
		// caller's own turn is the stronger evidence, and a status read that
		// races an already-delivered notification must not overwrite that
		// record with the weaker claim.
		if r.NotifyState != NotifyDelivered {
			r.NotifyState = NotifyConsumed
		}
	}
}

func notificationMatchesTerminalGeneration(r *JobRecord, e Event) bool {
	return r.TerminalGen != "" && e.TerminalGen == r.TerminalGen
}

// notifyRank orders the notification states so the reducer's monotonicity
// invariant can compare them: not_armed (0) precedes pending (1) precedes
// told-the-caller (2). Delivered and consumed share rank 2 — they are two ways
// the caller learned the same fact, so neither is progress past the other, and
// the tie is what lets a delivered event settle a record a status read already
// consumed (and vice versa) without reading as a regression. NotifyState is
// only ever set to one of these constants by the reducer, never read from event
// bytes, so the default is unreachable in practice and ranks the zero value
// conservatively as the lowest state.
func notifyRank(s NotifyState) int {
	switch s {
	case NotifyPending:
		return 1
	case NotifyDelivered, NotifyConsumed:
		return 2
	default:
		return 0
	}
}
