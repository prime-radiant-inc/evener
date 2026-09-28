package hub

// evener/host/orphan-resolve (crash-fencing spec 08c §5, §8, §10): the
// operator-facing resolve of one `orphan-unverified` record. It is the orphan
// admission fence's way out — "The call itself is never fenced by the orphan
// admission gate" — and it never creates an operation record of its own and
// never advances the host generation: "it transitions the named record
// (`orphan-unverified` → `interrupted` on a clean boundary) under the store
// mutex in one atomic write" (the store's dedicated ResolveOrphan write,
// which also clears the fencing-quarantine marker, the boundary, and every
// open pending-spawn intent, and persists the `orphanResolved` marker and the
// attestation).
//
// The handler's order is §5's, with §10's ordering pin:
//
//  1. the origin guard FIRST — "orphan-resolve refuses honestly-marked
//     peer-forwarded requests before admission — before dedup and before any
//     record lookup";
//  2. the record lookup by the controller-assigned id: an unknown id is the
//     typed not-found; a lost-response retry of an already-resolved record
//     replays the persisted resolution, never a second transition and never a
//     refusal; an ordinary (unmarked) interrupted id — the boot transition,
//     the local reap's resolve — is a typed validation refusal;
//  3. the attestation's bindings: shape, caller identity, record id, the
//     `boundary-unavailable` custody reference, and freshness. A
//     `boundary-unavailable` record never clears on an id alone; on any other
//     record a boundary reference is accepted-but-unneeded and never refused;
//  4. the persisted-boundary enumeration under the caller's session
//     authentication — the same §5 clean rule the boot reap applies, made
//     read-only, plus the per-entry remote lease check. Members still present
//     (and an enumeration that cannot prove clean, which fails closed) refuse
//     with the transient busy form; resolve never force-clears and never
//     kills.
//  5. the store's one atomic resolve write, then the updated OperationRecord.
//
// A `boundary-unavailable` record's attestation is the proof: §9's entry is
// "boundary lost to corruption, never verified empty", so no enumeration can
// clear it and the validated attestation is what authorizes the write.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// defaultOrphanAttestationMaxAge is the shipped maximum attestation age: an
// observation older than this refuses validation with no clearance (the
// spec leaves the number to the implementing PR). An hour is generous enough
// for the operator's kill-then-resolve round trip and tight enough that a
// stale observation cannot clear a record whose members may have restarted.
const defaultOrphanAttestationMaxAge = time.Hour

// OrphanResolve implements evener/host/orphan-resolve (§5, §9).
func (m *hubHostManager) OrphanResolve(ctx context.Context, params appwire.HostOrphanResolveParams) (appwire.OperationRecord, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.OperationRecord{}, err
	}
	id := strings.TrimSpace(params.ID)
	if id == "" {
		return appwire.OperationRecord{}, appwire.InvalidParams("evener/host/orphan-resolve needs a record id")
	}
	if m.cfg.ops == nil {
		return appwire.OperationRecord{}, appwire.InternalError("the host operation store is not configured, so no orphan record can be resolved")
	}
	record, ok := m.cfg.ops.Record(id)
	if !ok {
		return appwire.OperationRecord{}, appwire.ResourceNotFound(fmt.Sprintf("unknown operation record %q", id))
	}
	// The lost-response replay comes first: §5's "never a second transition and
	// never a refusal". The persisted resolution is the whole answer — the
	// presented attestation cannot clear anything a second time — so a marked
	// resolved id replays whether or not the retry re-presents its attestation.
	if record.OrphanResolved && record.State == hostops.StateInterrupted {
		return operationRecordWire(record)
	}
	if record.State != hostops.StateOrphanUnverified {
		return appwire.OperationRecord{}, appwire.InvalidParams(fmt.Sprintf(
			"record %s is %q, not %q: only an orphan-unverified record resolves through evener/host/orphan-resolve",
			record.ID, record.State, hostops.StateOrphanUnverified))
	}
	entries, err := decodeOrphanBoundary(record.OrphanBoundary)
	if err != nil {
		return appwire.OperationRecord{}, appwire.InternalError(fmt.Sprintf("record %s carries an unreadable orphan boundary: %v", record.ID, err))
	}
	custodyRef, unavailable := orphanBoundaryCustodyRef(entries)
	attestation, err := m.validateOrphanAttestation(ctx, record, params.Attestation, custodyRef, unavailable)
	if err != nil {
		return appwire.OperationRecord{}, err
	}
	if !unavailable {
		// §5's clean rule, per variant. A boundary the enumeration cannot prove
		// clean refuses transient busy — "On members still present it refuses with
		// the transient busy form, never a force-clear" — and so does an
		// unavailable enumeration, which fails closed.
		verify := m.cfg.orphanVerify
		if verify == nil {
			verify = defaultOrphanVerify
		}
		if err := verify(record); err != nil {
			return appwire.OperationRecord{}, appwire.HostBusyTransient(fmt.Sprintf(
				"host %q: orphan-unverified record %s cannot be resolved yet: %v; confirm the listed boundary members are gone, then retry",
				record.Host, record.ID, err))
		}
	}
	resolved, err := m.cfg.ops.ResolveOrphan(record.ID, orphanAttestationStore(attestation))
	switch {
	case errors.Is(err, hostops.ErrRecordNotFound):
		return appwire.OperationRecord{}, appwire.ResourceNotFound(fmt.Sprintf("unknown operation record %q", id))
	case errors.Is(err, hostops.ErrInvalidTransition), errors.Is(err, hostops.ErrInvalidRecord),
		errors.Is(err, hostops.ErrOrphanAttestationRequired):
		return appwire.OperationRecord{}, appwire.InvalidParams(fmt.Sprintf("record %s cannot be resolved: %v", record.ID, err))
	case err != nil:
		return appwire.OperationRecord{}, appwire.InternalError(fmt.Sprintf("resolving record %s failed: %v", record.ID, err))
	}
	return operationRecordWire(resolved)
}

// validateOrphanAttestation checks §5's bindings for a present attestation and
// the boundary-unavailable requirement, returning the attestation to persist
// (nil when none was presented) or the typed validation refusal. custodyRef
// and unavailable are the record's `boundary-unavailable` reading.
//
// The checks are §5's three bindings plus freshness: the claimed operator must
// equal the session's authenticated identity, recordId must equal the call's
// id, and — when the record carries the boundary-unavailable entry — boundaryRef
// must equal that entry's custodyRef. A stale observedAt refuses "with no
// clearance". An attestation on any other record is accepted-but-unneeded:
// its boundaryRef is never refused for a reference it cannot match.
func (m *hubHostManager) validateOrphanAttestation(ctx context.Context, record hostops.Record, presented *appwire.HostOrphanResolveAttestation, custodyRef string, unavailable bool) (*appwire.HostOrphanResolveAttestation, error) {
	if presented == nil {
		if unavailable {
			return nil, appwire.InvalidParams(fmt.Sprintf(
				"record %s carries the boundary-unavailable entry (%s): the boundary was lost to corruption, so it never clears on an id alone; confirm the absence out-of-band and present the operator attestation",
				record.ID, custodyRef))
		}
		return nil, nil
	}
	attestation := appwire.HostOrphanResolveAttestation{
		Operator:    strings.TrimSpace(presented.Operator),
		Statement:   strings.TrimSpace(presented.Statement),
		RecordID:    strings.TrimSpace(presented.RecordID),
		BoundaryRef: strings.TrimSpace(presented.BoundaryRef),
		ObservedAt:  strings.TrimSpace(presented.ObservedAt),
	}
	if attestation.Statement != hostops.OrphanResolveStatement {
		return nil, appwire.InvalidParams(fmt.Sprintf(
			"orphan-resolve %s: attestation statement %q is not %q",
			record.ID, attestation.Statement, hostops.OrphanResolveStatement))
	}
	if attestation.Operator == "" {
		return nil, appwire.InvalidParams(fmt.Sprintf("orphan-resolve %s: attestation names no operator", record.ID))
	}
	if attestation.RecordID != record.ID {
		return nil, appwire.InvalidParams(fmt.Sprintf(
			"orphan-resolve %s: attestation names record %q, not the resolved record",
			record.ID, attestation.RecordID))
	}
	observedAt, err := time.Parse(time.RFC3339, attestation.ObservedAt)
	if err != nil {
		return nil, appwire.InvalidParams(fmt.Sprintf(
			"orphan-resolve %s: attestation observedAt %q is not an RFC3339 instant: %v",
			record.ID, attestation.ObservedAt, err))
	}
	if identity := sessionOperator(ctx); identity != "" && identity != attestation.Operator {
		return nil, appwire.InvalidParams(fmt.Sprintf(
			"orphan-resolve %s: attestation operator %q is not the session's authenticated identity",
			record.ID, attestation.Operator))
	}
	maxAge := m.cfg.orphanAttestationMaxAge
	if maxAge <= 0 {
		maxAge = defaultOrphanAttestationMaxAge
	}
	if age := m.nowTime().Sub(observedAt); age > maxAge {
		return nil, appwire.InvalidParams(fmt.Sprintf(
			"orphan-resolve %s: attestation observed %s is %s old, over the maximum attestation age %s",
			record.ID, attestation.ObservedAt, age.Round(time.Second), maxAge))
	}
	if unavailable && attestation.BoundaryRef != custodyRef {
		return nil, appwire.InvalidParams(fmt.Sprintf(
			"orphan-resolve %s: attestation boundaryRef %q does not name the record's custody boundary %q",
			record.ID, attestation.BoundaryRef, custodyRef))
	}
	return &attestation, nil
}

// decodeOrphanBoundary reads one persisted boundary array into §9's typed
// union. A member outside the union's schema is refused loudly: a response
// must never render a boundary it could not read.
func decodeOrphanBoundary(raw json.RawMessage) ([]appwire.BoundaryEntry, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var entries []appwire.BoundaryEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// orphanBoundaryCustodyRef reports whether the decoded boundary carries §9's
// `boundary-unavailable` entry, and the custody reference it names. That entry
// is proof of nothing — "An empty array means no spawned subprocess survived
// the crash — verified empty, never unknown. A boundary the corruption
// destroyed is never an empty array" — so it resolves only through the
// operator attestation.
func orphanBoundaryCustodyRef(entries []appwire.BoundaryEntry) (string, bool) {
	for _, entry := range entries {
		if entry.BoundaryEntryUnavailable != nil {
			return entry.CustodyRef, true
		}
	}
	return "", false
}

// orphanAttestationStore maps the validated wire attestation onto the store's
// persisted shape.
func orphanAttestationStore(wire *appwire.HostOrphanResolveAttestation) *hostops.OrphanResolveAttestation {
	if wire == nil {
		return nil
	}
	return &hostops.OrphanResolveAttestation{
		Operator:    wire.Operator,
		Statement:   wire.Statement,
		RecordID:    wire.RecordID,
		BoundaryRef: wire.BoundaryRef,
		ObservedAt:  wire.ObservedAt,
	}
}
