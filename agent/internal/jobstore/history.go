package jobstore

import (
	"errors"
	"fmt"
	"sort"

	"primeradiant.com/evener/identifier"
)

// JournalSource keeps a folded journal tied to the store it came from. Events
// are folded here, rather than by callers, so source identity cannot be lost.
type JournalSource struct {
	SessionID   string
	Root        bool
	Available   bool
	Events      []Event
	Diagnostics ReadDiagnostics
}

type AuthorityDiagnostics struct {
	Incomplete      bool
	Mismatches      []string
	InvalidOwners   []string
	LifecycleErrors []string
	MissingOwners   []string
	TornTails       []string
	CorruptBranches []string
}

var ErrRootCorruption = errors.New("jobstore: root journal corruption")

// MergeJournals folds and merges source-tagged retained journals. Owner rows
// win over forwarded rows independent of input order. Forwarded rows survive
// only when no valid owner journal is available, and that result is incomplete.
func MergeJournals(sources []JournalSource) (map[string]*JobRecord, AuthorityDiagnostics, error) {
	type candidate struct {
		rec    *JobRecord
		source string
		owner  bool
	}
	var d AuthorityDiagnostics
	candidates := make(map[string][]candidate)
	for _, src := range sources {
		if !src.Available {
			continue
		}
		if src.Diagnostics.Corrupt {
			if src.Root {
				return nil, d, fmt.Errorf("%w: %s", ErrRootCorruption, src.SessionID)
			}
			d.Incomplete = true
			d.CorruptBranches = append(d.CorruptBranches, src.SessionID)
			continue
		}
		if src.Diagnostics.TornTail {
			d.Incomplete = true
			d.TornTails = append(d.TornTails, src.SessionID)
		}
		recs, lifecycle := FoldWithDiagnostics(src.Events)
		d.LifecycleErrors = append(d.LifecycleErrors, lifecycle...)
		for id, rec := range recs {
			if rec == nil || id == "" || rec.JobID != id {
				continue
			}
			owner := rec.OwnerSessionID == "" || rec.OwnerSessionID == src.SessionID
			if embedded, err := identifier.JobOwnerSessionID(id); err == nil && rec.OwnerSessionID != "" && embedded != rec.OwnerSessionID {
				d.InvalidOwners = append(d.InvalidOwners, id)
				continue
			}
			if rec.OwnerSessionID != "" && rec.OwnerSessionID != src.SessionID {
				d.InvalidOwners = append(d.InvalidOwners, id)
			}
			candidates[id] = append(candidates[id], candidate{rec: rec, source: src.SessionID, owner: owner})
		}
	}
	out := make(map[string]*JobRecord)
	for id, list := range candidates {
		var owners, forwarded []candidate
		for _, c := range list {
			if c.owner {
				owners = append(owners, c)
			} else {
				forwarded = append(forwarded, c)
			}
		}
		if len(owners) > 0 {
			chosen := owners[0]
			for _, c := range owners[1:] {
				if !sameRecord(chosen.rec, c.rec) {
					d.Mismatches = append(d.Mismatches, id)
				}
			}
			for _, c := range forwarded {
				if !sameRecord(chosen.rec, c.rec) {
					d.Mismatches = append(d.Mismatches, id)
				}
			}
			out[id] = chosen.rec
			continue
		}
		if len(forwarded) > 0 {
			out[id] = forwarded[0].rec
			d.Incomplete = true
			d.MissingOwners = append(d.MissingOwners, id)
		}
	}
	if len(d.LifecycleErrors) > 0 {
		d.Incomplete = true
	}
	return out, d, nil
}

func sameRecord(a, b *JobRecord) bool {
	return a.JobID == b.JobID && a.Status == b.Status && a.Reason == b.Reason && a.TerminalGen == b.TerminalGen && a.OutputBytes == b.OutputBytes
}

// FoldWithDiagnostics preserves Fold's tolerant projection while reporting
// lifecycle fragments. Empty owner fields are accepted for legacy journals.
func FoldWithDiagnostics(events []Event) (map[string]*JobRecord, []string) {
	recs := Fold(events)
	started := make(map[string]bool)
	var issues []string
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
	for _, e := range sorted {
		switch e.Kind {
		case EventJobStarted:
			if e.JobID == "" || e.Type == "" {
				issues = append(issues, fmt.Sprintf("job %q: invalid start fields", e.JobID))
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
