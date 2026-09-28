package hostops

// Crash-fencing spec 08c §5's `orphan-resolve` store half: the operator-facing
// resolution of an `orphan-unverified` record. The write is dedicated (never
// Store.Transition, which refuses this exit by type — see store.go's quarantined
// resolve guard) and atomic: the record's persisted boundary, every open
// pending-spawn intent, the per-host fencing-quarantine marker, and the state
// all move in ONE store write, and the `orphanResolved` marker plus the
// operator attestation land in that same write.
//
// §5: "On a clean boundary the call drops the intent and transitions the record
// to `interrupted`, persisting an `orphanResolved: true` marker on the resolved
// record." ... "A quarantine resolve clears the quarantine marker in the same
// atomic write." ... "The attestation persists on the resolved record beside
// the `orphanResolved` marker." ... "retrying the already-resolved record's
// `id` replays the resolved `OperationRecord` (state `interrupted`, no
// `orphanBoundary`) from the persisted resolution, never a second transition
// and never a refusal." ... "A boundary-unavailable record never clears on an
// id alone."

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"
)

// OrphanResolveStatement is the one attestation statement §5/§9 accepts:
// `statement: "orphan-verified-absent"`.
const OrphanResolveStatement = "orphan-verified-absent"

// MaxOrphanAttestationFieldBytes bounds each attestation string the resolved
// record persists. The claimed identities are operator-facing text, not
// controller-minted opaques, so the bound is generous — it exists so a
// malformed record cannot inflate the store file without limit.
const MaxOrphanAttestationFieldBytes = 1024

// OrphanResolveAttestation is §5's operator attestation, persisted verbatim on
// the resolved record beside the `orphanResolved` marker (and carried on the
// wire by `evener/host/orphan-resolve`'s params): the operator identity, the
// one accepted statement, the record the attestation names, the custody
// boundary reference a `boundary-unavailable` record is matched through, and
// the RFC3339 instant the absence was observed.
type OrphanResolveAttestation struct {
	Operator    string `json:"operator"`
	Statement   string `json:"statement"`
	RecordID    string `json:"recordId"`
	BoundaryRef string `json:"boundaryRef,omitempty"`
	ObservedAt  string `json:"observedAt"`
	// Unattributed marks an attestation recorded as given because the transport
	// carried no session principal to bind the claimed operator to: the durable
	// record then says the name was not verified against an authenticated
	// identity, never that it was. With a principal present the handler binds the
	// operator exactly and this marker stays false. It is controller-side
	// bookkeeping and never rides §9's wire attestation.
	Unattributed bool `json:"unattributed,omitempty"`
}

// ErrOrphanAttestationRequired reports an id-only resolve of a record whose
// persisted boundary is the `boundary-unavailable` custody sentinel: the lost
// boundary is never proof of an empty one, so the record clears only with the
// operator attestation present.
var ErrOrphanAttestationRequired = errors.New("hostops: a boundary-unavailable record requires the operator attestation")

// validateOrphanResolveAttestation checks one attestation against the schema
// this build persists: the accepted statement, named and valid fields, an
// RFC3339 observation, and the record the attestation names being the record
// it resolves. recordID is the record being resolved.
func validateOrphanResolveAttestation(attestation OrphanResolveAttestation, recordID string) error {
	if attestation.Statement != OrphanResolveStatement {
		return fmt.Errorf("%w: attestation statement %q is not %q", ErrInvalidRecord, attestation.Statement, OrphanResolveStatement)
	}
	for _, field := range []struct {
		value string
		what  string
	}{
		{attestation.Operator, "operator"},
		{attestation.RecordID, "recordId"},
		{attestation.BoundaryRef, "boundaryRef"},
	} {
		if len(field.value) > MaxOrphanAttestationFieldBytes {
			return fmt.Errorf("%w: attestation %s is %d bytes, over the %d-byte bound",
				ErrInvalidRecord, field.what, len(field.value), MaxOrphanAttestationFieldBytes)
		}
		if !utf8.ValidString(field.value) {
			return fmt.Errorf("%w: attestation %s is not valid UTF-8", ErrInvalidRecord, field.what)
		}
	}
	if attestation.Operator == "" {
		return fmt.Errorf("%w: attestation names no operator", ErrInvalidRecord)
	}
	if attestation.RecordID == "" {
		return fmt.Errorf("%w: attestation names no record", ErrInvalidRecord)
	}
	if attestation.RecordID != recordID {
		return fmt.Errorf("%w: attestation names record %q, not the resolved record %q",
			ErrInvalidRecord, attestation.RecordID, recordID)
	}
	if _, err := time.Parse(time.RFC3339, attestation.ObservedAt); err != nil {
		return fmt.Errorf("%w: attestation observedAt %q is not an RFC3339 instant: %w",
			ErrInvalidRecord, attestation.ObservedAt, err)
	}
	return nil
}

// boundaryUnavailableCustodyRef reports whether a persisted boundary carries
// §9's single `boundary-unavailable` entry, and the custody reference it names.
// The entry never selects an enumeration: it is the operator's reference, the
// one thing a `boundary-unavailable` record's attestation is matched through.
func boundaryUnavailableCustodyRef(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var members []struct {
		Kind       string `json:"kind"`
		CustodyRef string `json:"custodyRef"`
	}
	if err := json.Unmarshal(raw, &members); err != nil {
		return "", false
	}
	for _, member := range members {
		if member.Kind == "boundary-unavailable" {
			return member.CustodyRef, true
		}
	}
	return "", false
}

// boundaryCustodyRefMismatch reports the boundary-attestation binding §5
// defines for the `boundary-unavailable` variant: when the record carries that
// entry, the attestation's boundaryRef must equal the entry's custodyRef. An
// attestation on any other variant is accepted-but-unneeded and never checked
// against a reference it cannot match.
func boundaryCustodyRefMismatch(raw json.RawMessage, attestation OrphanResolveAttestation) error {
	custodyRef, unavailable := boundaryUnavailableCustodyRef(raw)
	if !unavailable {
		return nil
	}
	if attestation.BoundaryRef != custodyRef {
		return fmt.Errorf("%w: attestation boundaryRef %q does not name the record's custody boundary %q",
			ErrInvalidRecord, attestation.BoundaryRef, custodyRef)
	}
	return nil
}

// ResolveOrphan is §5's `orphan-resolve` write: the named `orphan-unverified`
// record's boundary, every open pending-spawn intent, the state, the
// `orphanResolved` marker, the operator attestation, and — when this record's
// host carries one — the per-host fencing-quarantine marker all move in one
// atomic store write, with the durable sequence advanced once.
//
// The caller has already verified the persisted boundary is clean (§5's clean
// rule, per variant) and validated the attestation's caller and freshness
// bindings; this write owns the schema's half: the record must be
// `orphan-unverified`, an attestation that is present must be well-formed and
// name exactly this record, and a `boundary-unavailable` record refuses
// without one. A partial resolve is not representable here: the write drops
// every intent and the whole boundary together, never a subset.
//
// A record this write already resolved replays unchanged — the lost-response
// retry §5 defines — with no second transition, no rewritten marker, and no
// refusal.
func (s *Store) ResolveOrphan(recordID string, attestation *OrphanResolveAttestation) (Record, error) {
	if s == nil {
		return Record{}, errors.New("hostops: store is not configured")
	}
	if recordID == "" {
		return Record{}, fmt.Errorf("%w: empty record id", ErrRecordNotFound)
	}
	var presented *OrphanResolveAttestation
	if attestation != nil {
		copied := *attestation
		presented = &copied
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	next := cloneSnapshot(s.cell.state)
	index := slices.IndexFunc(next.Records, func(record Record) bool { return record.ID == recordID })
	if index < 0 {
		// The record may have compacted into a tombstone. A resolved record's
		// replay must still answer from the persisted resolution (§5's replay
		// horizon), never not-found; an unmarked compacted record is still not
		// this call's to move, and refuses as any other non-unverified record does.
		if replay, ok := anchorRecordLocked(&next, recordID); ok {
			if replay.OrphanResolved && replay.State == StateInterrupted {
				return cloneRecord(replay), nil
			}
			return Record{}, fmt.Errorf("%w: record %q is %q (compacted), not %q",
				ErrInvalidTransition, recordID, replay.State, StateOrphanUnverified)
		}
		return Record{}, fmt.Errorf("%w: %q", ErrRecordNotFound, recordID)
	}
	record := next.Records[index]
	// The lost-response replay: the persisted resolution comes back unchanged.
	if record.OrphanResolved && record.State == StateInterrupted {
		return cloneRecord(record), nil
	}
	if record.State != StateOrphanUnverified {
		return Record{}, fmt.Errorf("%w: record %q is %q, not %q", ErrInvalidTransition, recordID, record.State, StateOrphanUnverified)
	}
	if _, unavailable := boundaryUnavailableCustodyRef(record.OrphanBoundary); unavailable {
		if presented == nil {
			return Record{}, fmt.Errorf("%w: record %q carries the boundary-unavailable entry, so it never clears on an id alone",
				ErrOrphanAttestationRequired, recordID)
		}
	}
	if presented != nil {
		if err := validateOrphanResolveAttestation(*presented, recordID); err != nil {
			return Record{}, err
		}
		if err := boundaryCustodyRefMismatch(record.OrphanBoundary, *presented); err != nil {
			return Record{}, err
		}
	}
	record.PendingSpawns = nil
	record.OrphanBoundary = nil
	record.State = StateInterrupted
	record.OrphanResolved = true
	record.OrphanAttestation = presented
	record.Result = &Result{OK: false, Message: InterruptedNote}
	record.UpdatedAt = nowUTC()
	next.advanceSequence(&record)
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	next.Records[index] = cloneRecord(record)
	// The quarantine marker clears in the same write, and only when it is this
	// record's: a host's marker names exactly one open record, so a marker
	// naming another record is never this resolve's to drop.
	if marker, marked := next.FencingQuarantines[record.Host]; marked && marker.RecordID == record.ID {
		delete(next.FencingQuarantines, record.Host)
	}
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		return Record{}, err
	}
	return cloneRecord(record), err
}
