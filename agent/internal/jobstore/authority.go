package jobstore

import (
	"fmt"
	"sort"
)

// RecordSource is one retained session's folded view. Records whose owner is
// another session are forwarded copies; the source's own records are primary.
type RecordSource struct {
	SessionID string
	Available bool
	Records   map[string]*JobRecord
	Events    []Event
	Read      ReadDiagnostics
}

// HistoryDiagnostics describes evidence that made a merged view incomplete.
type HistoryDiagnostics struct {
	Incomplete      bool
	Mismatches      []string
	InvalidOwners   []string
	LifecycleErrors []string
}

// MergeRecords applies the retained-history authority rule. A valid record
// from its owner replaces forwarded copies. Forwarded records remain usable
// when the owner source is unavailable; they are marked incomplete.
func MergeRecords(sources []RecordSource) (map[string]*JobRecord, HistoryDiagnostics) {
	out := make(map[string]*JobRecord)
	var d HistoryDiagnostics
	ownerAvailable := make(map[string]bool)
	for _, src := range sources {
		if src.Available {
			for id, rec := range src.Records {
				if rec != nil && rec.JobID == id && (rec.OwnerSessionID == "" || rec.OwnerSessionID == src.SessionID) && validRecord(rec) {
					ownerAvailable[id] = true
				}
			}
		}
	}
	for _, src := range sources {
		if !src.Available {
			continue
		}
		if src.Read.TornTail || src.Read.Corrupt {
			d.Incomplete = true
		}
		for id, rec := range src.Records {
			if rec == nil || id == "" || rec.JobID != id {
				continue
			}
			if rec.OwnerSessionID != "" && rec.OwnerSessionID != src.SessionID && ownerAvailable[id] {
				d.Mismatches = append(d.Mismatches, fmt.Sprintf("job %q: forwarded record conflicts with available owner", id))
				continue
			}
			if rec.OwnerSessionID != "" && rec.OwnerSessionID != src.SessionID && !ownerAvailable[id] {
				d.Incomplete = true
			}
			if existing, ok := out[id]; !ok || (rec.OwnerSessionID == src.SessionID && existing.OwnerSessionID != src.SessionID) {
				out[id] = rec
			}
		}
	}
	for _, src := range sources {
		if !src.Available {
			continue
		}
		for _, e := range src.Events {
			if e.Kind == EventJobStarted && e.JobID != "" && e.OwnerSessionID != "" && e.OwnerSessionID != src.SessionID {
				d.InvalidOwners = append(d.InvalidOwners, e.JobID)
			}
		}
		_, fd := FoldWithDiagnostics(src.Events)
		d.LifecycleErrors = append(d.LifecycleErrors, fd...)
	}
	if len(d.InvalidOwners)+len(d.LifecycleErrors) > 0 {
		d.Incomplete = true
	}
	return out, d
}

func validRecord(rec *JobRecord) bool {
	return rec != nil && rec.JobID != "" && rec.Type != "" && !rec.StartedAt.IsZero()
}

// FoldWithDiagnostics preserves Fold's tolerant projection while reporting
// lifecycle fragments that cannot establish a complete shell-job history.
func FoldWithDiagnostics(events []Event) (map[string]*JobRecord, []string) {
	recs := Fold(events)
	started := make(map[string]bool)
	var issues []string
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
	for _, e := range sorted {
		switch e.Kind {
		case EventJobStarted:
			if e.JobID == "" || e.Type == "" || e.OwnerSessionID == "" {
				issues = append(issues, fmt.Sprintf("job %q: invalid start identity or required fields", e.JobID))
				continue
			}
			started[e.JobID] = true
		case EventJobFinished:
			if e.JobID == "" || !started[e.JobID] {
				issues = append(issues, fmt.Sprintf("job %q: finish without start", e.JobID))
			}
		}
	}
	return recs, issues
}
