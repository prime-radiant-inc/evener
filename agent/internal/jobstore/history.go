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
	Incomplete       bool
	Mismatches       []string
	InvalidOwners    []string
	LifecycleErrors  []string
	MissingOwners    []string
	TornTails        []string
	CorruptBranches  []string
	Compatibility    []string
	TruncatedSources []string
}

var ErrRootCorruption = errors.New("jobstore: root journal corruption")

// MergeJournals folds and merges source-tagged retained journals. Owner rows
// win over forwarded rows independent of input order. Forwarded rows survive
// only when no valid owner journal is available, and that result is incomplete.
func MergeJournals(sources []JournalSource) (map[string]*JobRecord, AuthorityDiagnostics, error) {
	type candidate struct {
		rec           *JobRecord
		source        string
		owner         bool
		legacyUnknown bool
		damaged       bool
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
		recs, invalid, lifecycle := foldJournalWithDiagnostics(src.SessionID, src.Events)
		d.LifecycleErrors = append(d.LifecycleErrors, lifecycle...)
		for id, rec := range recs {
			if rec == nil || id == "" || rec.JobID != id {
				continue
			}
			damaged := invalid[id] || (src.Diagnostics.TornTail && rec.Status == StatusRunning)
			embedded, parseErr := identifier.JobOwnerSessionID(id)
			ownerID := rec.OwnerSessionID
			if ownerID == "" && parseErr == nil {
				// Legacy record with no recorded owner: recover it from the ID.
				// This is the only notable compatibility event; an explicit
				// owner beside a JobID that simply does not embed one (the
				// ordinary case for non-owner-embedding IDs) is authoritative
				// and must not be reported, or a large journal would emit one
				// diagnostic per job and swamp the projection envelope.
				ownerID = embedded
				d.Compatibility = append(d.Compatibility, id)
			}
			mismatch := rec.OwnerSessionID != "" && parseErr == nil && embedded != rec.OwnerSessionID
			if mismatch {
				d.InvalidOwners = append(d.InvalidOwners, id)
				damaged = true
			}
			owner := ownerID == src.SessionID && !damaged
			if rec.OwnerSessionID == "" && parseErr != nil {
				d.Incomplete = true
			}
			candidates[id] = append(candidates[id], candidate{rec: rec, source: src.SessionID, owner: owner, damaged: damaged, legacyUnknown: rec.OwnerSessionID == "" && parseErr != nil})
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
					chosen.rec.IntegrityReasons = append(chosen.rec.IntegrityReasons, "forwarded_mismatch")
				}
			}
			chosen.rec.Authority = AuthorityOwner
			chosen.rec.Incomplete = chosen.damaged
			out[id] = chosen.rec
			continue
		}
		if len(forwarded) > 0 {
			sort.SliceStable(forwarded, func(i, j int) bool {
				if forwarded[i].damaged != forwarded[j].damaged {
					return !forwarded[i].damaged
				}
				if forwarded[i].rec.Status.IsTerminal() != forwarded[j].rec.Status.IsTerminal() {
					return forwarded[i].rec.Status.IsTerminal()
				}
				return forwarded[i].source < forwarded[j].source
			})
			if forwarded[0].legacyUnknown {
				forwarded[0].rec.Authority = AuthorityLegacyUnknown
			} else {
				forwarded[0].rec.Authority = AuthorityForwardedFallback
			}
			forwarded[0].rec.Incomplete = true
			if forwarded[0].damaged {
				forwarded[0].rec.IntegrityReasons = append(forwarded[0].rec.IntegrityReasons, "lifecycle_invalid")
			}
			forwarded[0].rec.IntegrityReasons = append(forwarded[0].rec.IntegrityReasons, "owner_unavailable")
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
	if a.JobID != b.JobID || a.Type != b.Type || a.Status != b.Status || a.Reason != b.Reason || a.TerminalGen != b.TerminalGen || a.OutputBytes != b.OutputBytes {
		return false
	}
	if a.ExitCode == nil || b.ExitCode == nil {
		return a.ExitCode == nil && b.ExitCode == nil
	}
	return *a.ExitCode == *b.ExitCode
}

// foldJournalWithDiagnostics folds one source's events with Fold's tolerant
// projection while running the lifecycle validation pass exactly once. It
// returns the projected records, the set of job IDs whose lifecycle is invalid,
// and per-source attributed, de-duplicated issue messages. Empty owner fields
// are accepted for legacy journals.
func foldJournalWithDiagnostics(source string, events []Event) (map[string]*JobRecord, map[string]bool, []string) {
	recs := Fold(events)
	bad := make(map[string]bool)
	started := make(map[string]bool)
	finished := make(map[string]bool)
	seen := make(map[string]bool)
	var issues []string
	attribute := func(format string, args ...any) {
		message := fmt.Sprintf("owner %s: %s", source, fmt.Sprintf(format, args...))
		if seen[message] {
			return
		}
		seen[message] = true
		issues = append(issues, message)
	}
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
	for _, e := range sorted {
		switch e.Kind {
		case EventJobStarted:
			if e.JobID == "" || e.Type != JobShell || e.StartedAt == nil {
				bad[e.JobID] = true
				attribute("job %q: invalid start fields", e.JobID)
			}
			if started[e.JobID] {
				bad[e.JobID] = true
				attribute("job %q: duplicate start", e.JobID)
			}
			started[e.JobID] = true
		case EventJobFinished:
			if e.JobID == "" || !started[e.JobID] {
				bad[e.JobID] = true
				attribute("job %q: finish without start", e.JobID)
			}
			if e.JobID != "" && e.Status == "" {
				bad[e.JobID] = true
				attribute("job %q: finish without status", e.JobID)
			}
			if finished[e.JobID] {
				bad[e.JobID] = true
				attribute("job %q: duplicate finish", e.JobID)
			}
			finished[e.JobID] = true
		}
	}
	return recs, bad, issues
}
