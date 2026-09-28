package hostops

// Operations pagination and cursors (deploy pipeline 08b §8, §10).
//
// This file owns the read half of the operation store: `evener/host/operations`
// answers from here. The read is a pure read — no gate, no dial, no write — and
// every record it returns comes from the store's own snapshot under the store
// mutex (§4's one store mutex, §8's ordering). Records sort and resume by the
// controller-assigned id ascending; `createdAt`/`updatedAt` stay display-only
// and never decide the order (§8 "Ordering").
//
// The cursor is §8's versioned base64url JSON envelope
// `{v: 2, pos: id, compactSeq: number, bounds: {[host]: [generation,
// incarnationId, presenceEpoch] | "absent"}, quarantineEpoch: number}`. `pos`
// is the last row's controller-assigned id — never a bare offset — so a
// wall-clock rollback that stamps a later record with an earlier `createdAt`
// can never move it before the cursor. The bounds map pins every host in the
// query at mint, not just the hosts on the page: a host holding no records at
// all contributes the literal "absent" marker, and every other host its
// current triple. A continuation whose envelope version is not 2, whose pinned
// quarantine epoch no longer equals the live one, or whose stored bounds entry
// no longer matches the host's current boundary is a typed stale-entry re-list
// refusal — never a mixed page (§8 "Refusals"). A host created after the mint
// has no stored triple to validate and is skipped on later pages.
//
// What this file deliberately does not own, and the named seams its read path
// leaves:
//
//   - Post-cursor compaction (`cursor-invalidated`): S6 owns retention and
//     compaction. No write advances a `compactSeq` yet, so the live value is
//     always 0 and checkCursorCompactionLocked is the single seam where S6's
//     envelope-global comparison — and the refusal's `{compactSeq, host,
//     bounds}` data — lands (§8 "Refusals", §11 `cursor-invalidated`).
//   - The live `quarantineEpoch`: the custody-first quarantine (S8) persists
//     and advances the counter outside the store file. The comparison itself
//     is implemented here; CursorEpoch returns zero until that slice lands.
//   - The compacted-ID tombstone replay's `compacted: true` field (S6) and the
//     fencing paths' `orphanResolved`/`attestation` fields: the read passes
//     records through untouched and the wire carries those fields when their
//     owning slices add them (see appwire.OperationRecord's own comment).
//
// §11 pins a closed stale-entry value set with no cursor-specific value; every
// cursor stale-entry arm names `generation`, the value the token paths use for
// any move of the pinned (generation, incarnationId) identity. The handler
// maps it onto appwire's `stale-entry` discriminator.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"unicode/utf8"
)

const (
	// DefaultOperationsLimit is §8's default page size when the request names
	// no limit.
	DefaultOperationsLimit = 50
	// MaxOperationsLimit is §8's cap on a page: responses never exceed it.
	MaxOperationsLimit = 200
	// MaxCursorBytes is §8's encoded cap on the cursor: a first page whose
	// boundary map would exceed it refuses `cursor-too-large` with
	// `{capBytes: 8192}`, never a truncated cursor.
	MaxCursorBytes = 8192
	// cursorEnvelopeVersion is §8's envelope version. The only version this
	// codec mints; any other presented version is a stale-entry re-list.
	cursorEnvelopeVersion = 2
	// cursorAbsentMarker is the literal §8 encodes a host with no records at
	// all as, in the envelope's bounds map.
	cursorAbsentMarker = "absent"
)

// ErrInvalidOperationsQuery reports an operations read whose caller-supplied
// inputs fall outside §10's params — or a cursor that is not a structurally
// valid envelope of this codec. A structurally valid envelope whose version or
// pins are stale is CursorStaleError instead.
var ErrInvalidOperationsQuery = errors.New("hostops: invalid operations query")

// ErrMissingHostBoundary reports a host that holds operation records but no
// mirrored boundary triple. The store's own writes never produce this state —
// records are created under a registry pair whose boundary mirror lands in the
// same registry write, and §4 keeps the boundary until the host's last record
// compacts — so the read refuses rather than minting a bounds entry it cannot
// validate.
var ErrMissingHostBoundary = errors.New("hostops: host holds records but no mirrored boundary")

// CursorStaleError is §8's typed stale-entry re-list refusal for a cursor: the
// envelope's version is not 2, the pinned quarantine epoch no longer equals the
// live one, a stored bounds entry no longer matches the host's current
// boundary, or the request's pair is not the pair the cursor pinned. Binding is
// the §11 value the refusal names (see this file's header for the cursor arms'
// value).
type CursorStaleError struct {
	Reason  string
	Binding StaleBinding
}

func (e *CursorStaleError) Error() string {
	return "hostops: stale cursor: " + e.Reason
}

// CursorTooLargeError reports §8's over-cap first-page refusal: the minted
// cursor's boundary map would exceed MaxCursorBytes, so no cursor is minted and
// the client must re-list with a narrower query. CapBytes is the encoded cap
// the refusal's data carries.
type CursorTooLargeError struct {
	CapBytes int
}

func (e *CursorTooLargeError) Error() string {
	return fmt.Sprintf("hostops: the cursor's bounds map would exceed the %d-byte encoded cap", e.CapBytes)
}

// CursorBound is one host's entry in a cursor's bounds map or a page's
// hostBoundaries map: the {generation, incarnationId, presenceEpoch} triple, or
// §8's literal "absent" marker for a host that holds no records at all. Exactly
// one half is set.
type CursorBound struct {
	// Absent marks a host that held no records at the cursor's mint. It
	// encodes as the literal string "absent", never as an omission: §10's
	// hostBoundaries is authoritative for unfiltered pages, so a host with no
	// records must be distinguishable from a host the map forgot.
	Absent bool
	// Boundary is the host's pinned triple when Absent is false.
	Boundary Boundary
}

// MarshalJSON renders §8's bounds value union: the [generation, incarnationId,
// presenceEpoch] array, or the literal "absent" string.
func (b CursorBound) MarshalJSON() ([]byte, error) {
	if b.Absent {
		return json.Marshal(cursorAbsentMarker)
	}
	return json.Marshal([]any{b.Boundary.Generation, b.Boundary.IncarnationID, b.Boundary.PresenceEpoch})
}

// UnmarshalJSON reads §8's bounds value union. Anything but the literal
// "absent" or a three-element [number, string, number] array is a malformed
// cursor, refused as ErrInvalidOperationsQuery by DecodeCursor.
func (b *CursorBound) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte(`"absent"`)) {
		*b = CursorBound{Absent: true}
		return nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return fmt.Errorf("bounds entry %s is neither an array nor %q: %w", raw, cursorAbsentMarker, err)
	}
	if len(parts) != 3 {
		return fmt.Errorf("bounds entry %s carries %d elements, want 3", raw, len(parts))
	}
	var generation, presenceEpoch uint64
	var incarnationID string
	if err := json.Unmarshal(parts[0], &generation); err != nil {
		return fmt.Errorf("bounds entry %s: generation: %w", raw, err)
	}
	if err := json.Unmarshal(parts[1], &incarnationID); err != nil {
		return fmt.Errorf("bounds entry %s: incarnationId: %w", raw, err)
	}
	if err := json.Unmarshal(parts[2], &presenceEpoch); err != nil {
		return fmt.Errorf("bounds entry %s: presenceEpoch: %w", raw, err)
	}
	boundary := Boundary{Generation: generation, IncarnationID: incarnationID, PresenceEpoch: presenceEpoch}
	if err := validateBoundaryContainsIncarnation(boundary); err != nil {
		return fmt.Errorf("bounds entry %s: %w", raw, err)
	}
	*b = CursorBound{Boundary: boundary}
	return nil
}

// validateBoundaryContainsIncarnation checks the three values of a bounds
// triple, which has no host name of its own: a zero generation or presence
// epoch, or an empty or oversized or non-UTF-8 incarnation id, is no triple any
// writer of this store emits.
func validateBoundaryContainsIncarnation(boundary Boundary) error {
	switch {
	case boundary.Generation == 0:
		return errors.New("no generation")
	case boundary.IncarnationID == "":
		return errors.New("no incarnation id")
	case len(boundary.IncarnationID) > MaxIncarnationIDBytes:
		return fmt.Errorf("incarnation id is %d bytes, over the %d-byte bound", len(boundary.IncarnationID), MaxIncarnationIDBytes)
	case !utf8.ValidString(boundary.IncarnationID):
		return errors.New("incarnation id is not valid UTF-8")
	case boundary.PresenceEpoch == 0:
		return errors.New("no presence epoch")
	}
	return nil
}

// CursorEnvelope is §8's decoded cursor: the versioned envelope's five values.
type CursorEnvelope struct {
	// Version is the envelope version; the codec mints 2 and refuses any other
	// presented version with CursorStaleError.
	Version int
	// Position is the last row's controller-assigned id: the durable sequence
	// position a continuation resumes after. Never a bare offset.
	Position string
	// CompactSeq is the store-global compaction position the cursor pinned
	// (§4's `compactSeq`). S6 owns advancing it.
	CompactSeq uint64
	// QuarantineEpoch is the §4 quarantine counter the cursor pinned. S8 owns
	// the quarantine that advances it.
	QuarantineEpoch uint64
	// Bounds pins one entry per host in the query at mint: the triple, or the
	// "absent" marker for a host that held no records.
	Bounds map[string]CursorBound
}

// cursorEnvelopeJSON is the wire literal. Field order is the spec's
// (`{v, pos, compactSeq, bounds, quarantineEpoch}`), and every key is always
// present — a cursor is never best-effort decoded.
type cursorEnvelopeJSON struct {
	V               int                    `json:"v"`
	Pos             string                 `json:"pos"`
	CompactSeq      uint64                 `json:"compactSeq"`
	Bounds          map[string]CursorBound `json:"bounds"`
	QuarantineEpoch uint64                 `json:"quarantineEpoch"`
}

// EncodeCursor renders §8's opaque cursor: the base64url JSON envelope. It
// refuses an envelope this codec does not mint (a version other than 2, a
// position outside the allocator form, a malformed bounds entry) and an
// envelope whose encoded form would exceed MaxCursorBytes — the latter as
// *CursorTooLargeError, §8's distinct over-cap refusal.
func EncodeCursor(envelope CursorEnvelope) (string, error) {
	if envelope.Version != cursorEnvelopeVersion {
		return "", fmt.Errorf("%w: cannot encode cursor envelope version %d", ErrInvalidOperationsQuery, envelope.Version)
	}
	if _, err := parseAllocatorID(envelope.Position); err != nil {
		return "", fmt.Errorf("%w: cursor position %q: %w", ErrInvalidOperationsQuery, envelope.Position, err)
	}
	bounds := make(map[string]CursorBound, len(envelope.Bounds))
	for host, bound := range envelope.Bounds {
		if err := validateBoundaryName(host); err != nil {
			return "", fmt.Errorf("%w: cursor bounds: %w", ErrInvalidOperationsQuery, err)
		}
		if !bound.Absent {
			if err := validateBoundaryContainsIncarnation(bound.Boundary); err != nil {
				return "", fmt.Errorf("%w: cursor bounds[%s]: %w", ErrInvalidOperationsQuery, host, err)
			}
		}
		bounds[host] = bound
	}
	raw, err := json.Marshal(cursorEnvelopeJSON{
		V:               envelope.Version,
		Pos:             envelope.Position,
		CompactSeq:      envelope.CompactSeq,
		Bounds:          bounds,
		QuarantineEpoch: envelope.QuarantineEpoch,
	})
	if err != nil {
		// A struct of numbers, strings and a comparable map cannot fail to
		// marshal.
		return "", fmt.Errorf("%w: encoding the cursor: %w", ErrInvalidOperationsQuery, err)
	}
	cursor := base64.RawURLEncoding.EncodeToString(raw)
	if len(cursor) > MaxCursorBytes {
		return "", &CursorTooLargeError{CapBytes: MaxCursorBytes}
	}
	return cursor, nil
}

// DecodeCursor reads §8's opaque cursor. A payload that is not base64url, not
// JSON, or not a structurally valid envelope of this codec is
// ErrInvalidOperationsQuery; an envelope whose version is not 2 is
// *CursorStaleError — §8's typed re-list refusal, never a best-effort decode.
func DecodeCursor(cursor string) (CursorEnvelope, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return CursorEnvelope{}, fmt.Errorf("%w: the cursor is not base64url: %w", ErrInvalidOperationsQuery, err)
	}
	var decoded cursorEnvelopeJSON
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return CursorEnvelope{}, fmt.Errorf("%w: the cursor payload is not the envelope: %w", ErrInvalidOperationsQuery, err)
	}
	if decoded.V != cursorEnvelopeVersion {
		return CursorEnvelope{}, &CursorStaleError{
			Reason:  fmt.Sprintf("the envelope version %d is not %d; re-list from the first page", decoded.V, cursorEnvelopeVersion),
			Binding: StaleBindingGeneration,
		}
	}
	if decoded.Bounds == nil {
		return CursorEnvelope{}, fmt.Errorf("%w: the cursor carries no bounds map", ErrInvalidOperationsQuery)
	}
	if _, err := parseAllocatorID(decoded.Pos); err != nil {
		return CursorEnvelope{}, fmt.Errorf("%w: cursor position %q: %w", ErrInvalidOperationsQuery, decoded.Pos, err)
	}
	return CursorEnvelope{
		Version:         decoded.V,
		Position:        decoded.Pos,
		CompactSeq:      decoded.CompactSeq,
		QuarantineEpoch: decoded.QuarantineEpoch,
		Bounds:          decoded.Bounds,
	}, nil
}

// CursorEpoch is the store-wide epoch pair every minted cursor pins: the
// compaction position (§4's `compactSeq`) and the quarantine epoch.
type CursorEpoch struct {
	CompactSeq      uint64
	QuarantineEpoch uint64
}

// CursorEpoch returns the live epoch pair. It is the S6/S8 seam: compaction
// (S6) and the custody-first quarantine (S8) do not exist yet, so no write
// advances either counter and both read zero. The cursor codec, the mint and
// the continuation comparison already thread the values through, so the owning
// slices replace this body — `compactSeq` persisted in the store file with
// every compacting write (§4), `quarantineEpoch` persisted outside the
// quarantined file (§4) — without touching the read path.
func (s *Store) CursorEpoch() CursorEpoch {
	if s == nil {
		return CursorEpoch{}
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	return s.cursorEpochLocked()
}

// cursorEpochLocked is CursorEpoch's locked body, which ReadOperations' caller
// already holds the store mutex for.
func (s *Store) cursorEpochLocked() CursorEpoch {
	return CursorEpoch{}
}

// checkCursorCompactionLocked is the named S6 seam for §8's post-cursor
// compaction refusal. Compaction (§4's retention half) does not exist yet: no
// write removes rows and no write advances a `compactSeq`, so no cursor can be
// invalidated by one and the answer is always nil. When S6 lands, the live
// CompactSeq is compared once, envelope-globally, against the cursor's, and a
// compaction that removed rows at or before the cursor's `pos` refuses
// `cursor-invalidated` naming the compacting `compactSeq` plus the affected
// host's stored bounds entry (§8 "Refusals", §11) — never per-host, and never
// a mixed page. The caller holds the store mutex.
func checkCursorCompactionLocked(_ *snapshot, _ CursorEnvelope, _ CursorEpoch) error {
	return nil
}

// OperationsQuery is one `evener/host/operations` read (§10's params, mapped
// onto the store): the host filter, the query filters, the page size and the
// presented cursor.
type OperationsQuery struct {
	// Host pins the read to one host. Empty is the unfiltered cross-host read;
	// a generation filter requires it (§8: "A generation-pinned page requires
	// `name`").
	Host string
	// ClientOperationID matches the client-supplied clientOperationId, never
	// the controller-assigned id.
	ClientOperationID string
	// State, when set, selects records in exactly that durable state.
	State State
	// Generation, when set, selects the incarnation after client operation-ID
	// reuse; IncarnationID narrows it to the exact incarnation.
	Generation    *uint64
	IncarnationID string
	// ID is the detail filter for the controller-assigned record id.
	ID string
	// Limit is the page size: 0 reads the default, values above the cap are
	// capped, and a negative limit is refused.
	Limit int
	// Cursor is the opaque continuation. Empty starts the pinned first page.
	Cursor string
}

// OperationsPage is one bounded page of records plus the identity the response
// must carry: the effective (generation, incarnationId) pair on host-pinned
// pages, the authoritative hostBoundaries map on unfiltered cross-host pages,
// and the continuation cursor when a page listed at least one record.
type OperationsPage struct {
	// Records are ascending by controller-assigned id.
	Records []Record
	// Generation and IncarnationID are set exactly on host-pinned pages: the
	// effective pair actually listed (§8, §10).
	Generation    *uint64
	IncarnationID string
	// HostBoundaries is set exactly on unfiltered cross-host pages: one entry
	// per every host in the query at cursor creation, triple or "absent".
	HostBoundaries map[string]CursorBound
	// NextCursor is the opaque continuation, present exactly when the page
	// listed at least one record.
	NextCursor string
}

// ReadOperations is the store's `evener/host/operations` read (§8, §10): a pure
// read of the records in ascending id order under the store mutex, one bounded
// page per call, resumed by the opaque cursor the previous page returned.
func (s *Store) ReadOperations(q OperationsQuery) (OperationsPage, error) {
	if s == nil {
		return OperationsPage{}, errors.New("hostops: store is not configured")
	}
	if err := validateOperationsQuery(q); err != nil {
		return OperationsPage{}, err
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	return readOperationsLocked(&s.cell.state, q, s.cursorEpochLocked())
}

// validateOperationsQuery checks the caller-supplied half of a read against
// §10's params: a closed state set, a negative limit, an incarnation filter
// without its generation, and a pair filter without the host name it pins.
func validateOperationsQuery(q OperationsQuery) error {
	if q.Limit < 0 {
		return fmt.Errorf("%w: limit %d is negative", ErrInvalidOperationsQuery, q.Limit)
	}
	if q.State != "" && !q.State.Valid() {
		return fmt.Errorf("%w: state %q is outside the record state set", ErrInvalidOperationsQuery, q.State)
	}
	if q.Generation != nil && *q.Generation == 0 {
		return fmt.Errorf("%w: generation 0 names no incarnation", ErrInvalidOperationsQuery)
	}
	if q.IncarnationID != "" && q.Generation == nil {
		return fmt.Errorf("%w: incarnationId needs generation", ErrInvalidOperationsQuery)
	}
	if q.Host == "" && (q.Generation != nil || q.IncarnationID != "") {
		if q.Cursor != "" {
			// §8: "a cursor minted for one pair validated against an
			// unfiltered query is a typed stale-entry re-list refusal."
			return &CursorStaleError{
				Reason:  "a pair filter cannot validate a cursor without the host name it was pinned to; re-list from the first page",
				Binding: StaleBindingGeneration,
			}
		}
		return fmt.Errorf("%w: a generation filter needs a host name", ErrInvalidOperationsQuery)
	}
	if q.Host != "" && !utf8.ValidString(q.Host) {
		return fmt.Errorf("%w: host name is not valid UTF-8", ErrInvalidOperationsQuery)
	}
	return nil
}

// readOperationsLocked is ReadOperations' body; the caller holds the store
// mutex across the whole read, so the page and the cursor it mints describe one
// record set. live carries the store-wide epoch pair the mint pins.
func readOperationsLocked(state *snapshot, q OperationsQuery, live CursorEpoch) (OperationsPage, error) {
	limit := q.Limit
	if limit == 0 {
		limit = DefaultOperationsLimit
	}
	if limit > MaxOperationsLimit {
		limit = MaxOperationsLimit
	}

	// The presented cursor: decode, then the two store-wide checks — the pinned
	// quarantine epoch first (§8: "before any boundary comparison"), then the
	// S6 compaction seam.
	var window CursorEnvelope
	continuing := q.Cursor != ""
	if continuing {
		decoded, err := DecodeCursor(q.Cursor)
		if err != nil {
			return OperationsPage{}, err
		}
		if decoded.QuarantineEpoch != live.QuarantineEpoch {
			return OperationsPage{}, &CursorStaleError{
				Reason: fmt.Sprintf("the cursor pinned quarantine epoch %d but the store is at %d; re-list from the first page",
					decoded.QuarantineEpoch, live.QuarantineEpoch),
				Binding: StaleBindingGeneration,
			}
		}
		if err := checkCursorCompactionLocked(state, decoded, live); err != nil {
			return OperationsPage{}, err
		}
		window = decoded
	}

	// §8's bounds validation: every host in the cursor's map must still match
	// the host's current entry, triple equality included, so a generation or
	// presence advance between pages rejects the continuation instead of
	// serving the pinned incarnation past the boundary change.
	if continuing {
		for host, stored := range window.Bounds {
			current, err := cursorBoundLocked(state, host)
			if err != nil {
				return OperationsPage{}, err
			}
			if current != stored {
				return OperationsPage{}, &CursorStaleError{
					Reason:  fmt.Sprintf("host %q's stored boundary no longer matches its current boundary; re-list from the first page", host),
					Binding: StaleBindingGeneration,
				}
			}
		}
	}

	page := OperationsPage{}
	var (
		pinned       = q.Host != ""
		pageHosts    map[string]bool
		mintBounds   map[string]CursorBound
		filterGen    uint64
		filterInc    string
		pairFiltered bool
	)
	if pinned {
		// A host-pinned continuation must stay inside the window it pinned.
		if continuing {
			if _, ok := window.Bounds[q.Host]; !ok {
				return OperationsPage{}, &CursorStaleError{
					Reason:  fmt.Sprintf("host %q was not in the cursor's pinned window; re-list from the first page", q.Host),
					Binding: StaleBindingGeneration,
				}
			}
		}
		generation, incarnation, filtered, err := resolvePinnedWindow(state, q, window, continuing)
		if err != nil {
			return OperationsPage{}, err
		}
		filterGen, filterInc, pairFiltered = generation, incarnation, filtered
		pageHosts = map[string]bool{q.Host: true}
		echoGen, echoInc := generation, incarnation
		if continuing && q.Generation == nil {
			// An omitted filter on a later page reads as the pinned-cursor
			// window: the stored entry's pair, never a fresh unpinned query.
			if stored := window.Bounds[q.Host]; !stored.Absent {
				echoGen, echoInc = stored.Boundary.Generation, stored.Boundary.IncarnationID
			}
		}
		page.Generation = &echoGen
		page.IncarnationID = echoInc
		// The cursor's bounds still pin the host even though the response
		// carries the top-level pair instead of a hostBoundaries map.
		bound, err := cursorBoundLocked(state, q.Host)
		if err != nil {
			return OperationsPage{}, err
		}
		mintBounds = map[string]CursorBound{q.Host: bound}
	} else {
		if continuing {
			pageHosts = make(map[string]bool, len(window.Bounds))
			for host := range window.Bounds {
				pageHosts[host] = true
			}
			page.HostBoundaries = window.Bounds
			mintBounds = window.Bounds
		} else {
			hosts := operationsHostUniverse(state)
			pageHosts = make(map[string]bool, len(hosts))
			bounds := make(map[string]CursorBound, len(hosts))
			for _, host := range hosts {
				pageHosts[host] = true
				bound, err := cursorBoundLocked(state, host)
				if err != nil {
					return OperationsPage{}, err
				}
				bounds[host] = bound
			}
			page.HostBoundaries = bounds
			mintBounds = bounds
		}
	}

	// The page itself: stored order is ascending id order (store.go's Records
	// contract, §8's "one ordering for both"), so resuming after the cursor's
	// position is a linear scan of that order — the fixed allocator width makes
	// the id-string comparison the numeric one (formatAllocatorID). createdAt
	// never participates. The envelope pins the window — the hosts, the pair
	// and the position — not the other filters: operationId, state and id are
	// applied exactly as on a first page, so a continuation repeats them.
	records := make([]Record, 0, limit)
	for i := range state.Records {
		record := state.Records[i]
		if continuing && record.ID <= window.Position {
			continue
		}
		if !pageHosts[record.Host] {
			continue
		}
		if q.ClientOperationID != "" && record.ClientOperationID != q.ClientOperationID {
			continue
		}
		if q.State != "" && record.State != q.State {
			continue
		}
		if q.ID != "" && record.ID != q.ID {
			continue
		}
		if pairFiltered {
			if record.Generation != filterGen {
				continue
			}
			if filterInc != "" && record.IncarnationID != filterInc {
				continue
			}
		}
		records = append(records, cloneRecord(record))
		if len(records) == limit {
			break
		}
	}
	page.Records = records

	if len(records) > 0 {
		// The next cursor pins the same window the page was served under: a
		// continuation reuses the validated map, never a freshly enumerated
		// one, so hosts created mid-pagination stay skipped.
		cursor, err := EncodeCursor(CursorEnvelope{
			Version:         cursorEnvelopeVersion,
			Position:        records[len(records)-1].ID,
			CompactSeq:      live.CompactSeq,
			QuarantineEpoch: live.QuarantineEpoch,
			Bounds:          mintBounds,
		})
		if err != nil {
			return OperationsPage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

// resolvePinnedWindow answers the effective pair a host-pinned page lists under
// (§8: "a host-pinned response carries the effective generation and
// incarnationId actually listed"; §10: "generation selects the incarnation
// after client operation-ID reuse (omitted: the current generation)").
//
// A first page resolves from the request: the named pair exactly, a named
// generation with the boundary's incarnation when that generation is current,
// or the host's current pair (the mirrored boundary; the newest retained
// record's pair when the boundary is gone). A continuation resolves against the
// cursor's stored entry: the request's pair must be the pair the cursor pinned
// — a cursor minted under one pair never lists the other — and an omitted pair
// reads as the stored window. pairFiltered is false only when the host has no
// pair at all to filter by, which is also a host with no records to list.
//
// The envelope carries one triple per host, and §8 mints it from the host's
// current boundary, so a page read under a superseded pair serves that one page
// and a continuation of it is a stale-entry re-list once the current pair
// differs: the literal "stored bounds entry no longer matches the host's
// current boundary" rule, never a mixed page.
func resolvePinnedWindow(state *snapshot, q OperationsQuery, window CursorEnvelope, continuing bool) (generation uint64, incarnationID string, pairFiltered bool, err error) {
	if continuing {
		stored := window.Bounds[q.Host]
		if q.Generation != nil {
			if stored.Absent {
				return 0, "", false, &CursorStaleError{
					Reason:  fmt.Sprintf("the cursor pinned host %q with no records, so it cannot list the named pair; re-list from the first page", q.Host),
					Binding: StaleBindingGeneration,
				}
			}
			if stored.Boundary.Generation != *q.Generation {
				return 0, "", false, &CursorStaleError{
					Reason: fmt.Sprintf("the cursor pinned host %q at generation %d, not %d; re-list from the first page",
						q.Host, stored.Boundary.Generation, *q.Generation),
					Binding: StaleBindingGeneration,
				}
			}
			if q.IncarnationID != "" && stored.Boundary.IncarnationID != q.IncarnationID {
				return 0, "", false, &CursorStaleError{
					Reason: fmt.Sprintf("the cursor pinned host %q at incarnation %q, not %q; re-list from the first page",
						q.Host, stored.Boundary.IncarnationID, q.IncarnationID),
					Binding: StaleBindingGeneration,
				}
			}
			if q.IncarnationID == "" {
				return *q.Generation, stored.Boundary.IncarnationID, true, nil
			}
			return *q.Generation, q.IncarnationID, true, nil
		}
		if stored.Absent {
			return 0, "", false, nil
		}
		return stored.Boundary.Generation, stored.Boundary.IncarnationID, true, nil
	}
	boundary, hasBoundary := state.Boundaries[q.Host]
	switch {
	case q.Generation != nil && q.IncarnationID != "":
		return *q.Generation, q.IncarnationID, true, nil
	case q.Generation != nil:
		if hasBoundary && boundary.Generation == *q.Generation {
			return *q.Generation, boundary.IncarnationID, true, nil
		}
		return *q.Generation, "", true, nil
	case hasBoundary:
		return boundary.Generation, boundary.IncarnationID, true, nil
	default:
		// No boundary for the name. A host that holds records is refused by
		// cursorBoundLocked (the read mints its bounds entry in the same pass),
		// so this is a host with nothing to list and no pair to echo.
		return 0, "", false, nil
	}
}

// cursorBoundLocked computes one host's bounds entry: the mirrored boundary
// triple when the host holds records, or the literal "absent" marker when it
// holds none (§8). A host that holds records but no mirrored boundary is the
// state no writer of this store produces, refused as ErrMissingHostBoundary
// rather than minting an entry later pages cannot validate.
func cursorBoundLocked(state *snapshot, host string) (CursorBound, error) {
	holdsRecords := false
	for i := range state.Records {
		if state.Records[i].Host == host {
			holdsRecords = true
			break
		}
	}
	if !holdsRecords {
		return CursorBound{Absent: true}, nil
	}
	boundary, ok := state.Boundaries[host]
	if !ok {
		return CursorBound{}, fmt.Errorf("%w: host %q", ErrMissingHostBoundary, host)
	}
	return CursorBound{Boundary: boundary}, nil
}

// operationsHostUniverse is every host the store knows — the hosts holding
// records plus the mirrored boundary names — in a stable order. This is the
// unfiltered query's host set: §8 pins every host in the query at cursor
// creation, and a host the store has never heard of has no stored triple, so
// its later records are skipped rather than admitted mid-pagination.
func operationsHostUniverse(state *snapshot) []string {
	hosts := make(map[string]bool, len(state.Boundaries))
	for i := range state.Records {
		hosts[state.Records[i].Host] = true
	}
	for name := range state.Boundaries {
		hosts[name] = true
	}
	return slices.Sorted(maps.Keys(hosts))
}
