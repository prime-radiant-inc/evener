package hostops

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// This file owns §4's fencing-quarantine write: the durable per-host marker the
// fencing worker persists on a kill/wait timeout, landed in the same atomic
// store write as the fencing-timeout record and its remote-fencing boundary.
// "The timeout additionally persists a durable per-host fencing-quarantine
// marker in the operation-store file in the same atomic write that lands the
// fencing-timeout record." The marker closes the host until `orphan-resolve`
// clears it (the resolver itself is a later slice); a boot reads the marker
// with FencingQuarantine, and the same write is the one the resolver's clearing
// write must mirror.

// FencingQuarantine is the durable per-host fencing-quarantine marker: the
// host's open orphan-unverified record (the `orphan-resolve` way out) and when
// the fencing timeout landed it. The marker is keyed by host name — "the fence
// scopes to that host's name only" (§8) — and an open marker refuses the host's
// lifecycle and mutation calls until it clears.
type FencingQuarantine struct {
	// RecordID is the orphan-unverified record the timeout landed: the id
	// `orphan-resolve` accepts to clear the marker.
	RecordID string `json:"recordId"`
	// QuarantinedAt is when the fencing-timeout write landed.
	QuarantinedAt time.Time `json:"quarantinedAt"`
}

// FencingQuarantine returns the host's open fencing-quarantine marker, if any.
func (s *Store) FencingQuarantine(host string) (FencingQuarantine, bool) {
	if s == nil {
		return FencingQuarantine{}, false
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	marker, ok := s.cell.state.FencingQuarantines[host]
	return marker, ok
}

// QuarantineFencing persists §4's fencing-timeout write: the named operation
// record enters StateOrphanUnverified carrying the remote-fencing boundary, and
// the per-host quarantine marker lands in the same atomic write. A record
// already orphan-unverified is only this write's to report when the marker
// already names it (a replay after a lost response); a local-reap orphan
// record's boundary is never re-pointed by the fencing path.
//
// A refusal — an unknown or terminal record, a boundary that is not a
// non-empty BoundaryEntry array, a host already quarantined by another record,
// or a change that takes the record outside the schema — leaves the store
// untouched and persists nothing. A write whose rename lands is the durable
// quarantine even when the directory sync behind it failed, so the returned
// record is the caller's to reconcile with (see RenameLanded).
func (s *Store) QuarantineFencing(recordID string, boundary json.RawMessage) (Record, error) {
	if s == nil {
		return Record{}, errors.New("hostops: store is not configured")
	}
	var members []json.RawMessage
	if jsonFieldIsNull(boundary) || json.Unmarshal(boundary, &members) != nil || len(members) == 0 {
		return Record{}, fmt.Errorf("%w: a fencing-quarantine boundary is not a non-empty BoundaryEntry array", ErrInvalidRecord)
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	next := cloneSnapshot(s.cell.state)
	index := slices.IndexFunc(next.Records, func(record Record) bool { return record.ID == recordID })
	if index < 0 {
		return Record{}, fmt.Errorf("%w: %q", ErrRecordNotFound, recordID)
	}
	record := next.Records[index]
	if record.State.Terminal() {
		return Record{}, fmt.Errorf("%w: record %q is already %q", ErrRecordTerminal, recordID, record.State)
	}
	marker, marked := next.FencingQuarantines[record.Host]
	if record.State == StateOrphanUnverified {
		// The replay of a fencing-timeout write whose response was lost: the
		// stored record comes back unchanged. Only the record this host's
		// marker already names is the fencing path's; an orphan-unverified
		// record from another path (a local reap) is never re-pointed here.
		if !marked || marker.RecordID != record.ID {
			return Record{}, fmt.Errorf("%w: orphan-unverified record %q was not quarantined by the fencing path",
				ErrInvalidTransition, recordID)
		}
		return cloneRecord(record), nil
	}
	if marked && marker.RecordID != record.ID {
		return Record{}, fmt.Errorf("%w: host %q is already fencing-quarantined by record %s",
			ErrInvalidTransition, record.Host, marker.RecordID)
	}
	record.State = StateOrphanUnverified
	record.OrphanBoundary = append(json.RawMessage(nil), boundary...)
	record.UpdatedAt = nowUTC()
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	next.Records[index] = record
	if next.FencingQuarantines == nil {
		next.FencingQuarantines = map[string]FencingQuarantine{}
	}
	next.FencingQuarantines[record.Host] = FencingQuarantine{RecordID: record.ID, QuarantinedAt: nowUTC()}
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		// Nothing was written: the refusal reports no record.
		return Record{}, err
	}
	return cloneRecord(record), err
}
