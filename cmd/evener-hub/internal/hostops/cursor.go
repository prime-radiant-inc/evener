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
// current boundary triple — the staleness reference a continuation validates
// by full equality, so a generation OR presence advance between pages refuses
// (§8 "Refusals"; note `update` advances the generation without advancing the
// presence epoch, so presence alone would not be enough). A continuation whose
// envelope version is not 2, whose pinned quarantine epoch no longer equals
// the live one, whose stored entry no longer matches the host's current
// boundary, or which names an unknown host is a typed stale-entry re-list
// refusal — never a mixed page. A host created after the mint has no stored
// triple to validate and is skipped on later pages.
//
// The pair a host-pinned page was actually served under is recovered on the
// continuation from the row `pos` names: that row was listed by the page, so it
// carries the listed pair. The continuation pins its window to that pair
// whether the request repeats it or omits it — "An omitted filter on a later
// page reads as the pinned-cursor window, never as a fresh unpinned query" — and
// a repeated pair that is not it is refused. That is how a page under a
// superseded incarnation stays pageable without the envelope carrying a second
// triple, while the stored entry still detects every boundary advance. An
// unfiltered page lists each host's current pair (§10: "generation ... omitted:
// the current generation"), so the same rule refuses a cursor minted for one
// pair presented as an unfiltered query; history stays reachable through the
// host-pinned pair filter and the `id` detail filter. A `pos` row the store no
// longer holds (S6 compaction's territory) is a stale re-list here.
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

// cursorEnvelopeKeys is the exact key set §8's envelope declares, for the
// strict decode: a payload with any other key, a key named twice, or a missing
// key is not an envelope this codec minted.
var cursorEnvelopeKeys = map[string]map[string]struct{}{
	"": {
		"v":               {},
		"pos":             {},
		"compactSeq":      {},
		"bounds":          {},
		"quarantineEpoch": {},
	},
}

// DecodeCursor reads §8's opaque cursor. A payload that is not base64url, not
// JSON, or not exactly a structurally valid envelope of this codec — over the
// encoded cap, a key outside the literal, a key named twice, a missing key, a
// malformed bounds entry or host key — is ErrInvalidOperationsQuery; an
// envelope whose version is not 2 is *CursorStaleError — §8's typed re-list
// refusal, never a best-effort decode.
func DecodeCursor(cursor string) (CursorEnvelope, error) {
	if len(cursor) > MaxCursorBytes {
		// The cap is checked before any decode: an over-cap value is no cursor
		// this codec minted, and decoding an arbitrarily large one first would
		// be exactly the best-effort path §8 refuses.
		return CursorEnvelope{}, fmt.Errorf("%w: the cursor is %d bytes, over the %d-byte encoded cap",
			ErrInvalidOperationsQuery, len(cursor), MaxCursorBytes)
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return CursorEnvelope{}, fmt.Errorf("%w: the cursor is not base64url: %w", ErrInvalidOperationsQuery, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return CursorEnvelope{}, fmt.Errorf("%w: the cursor payload is not the envelope: %w", ErrInvalidOperationsQuery, err)
	}
	for key := range fields {
		if _, ok := cursorEnvelopeKeys[""][key]; !ok {
			return CursorEnvelope{}, fmt.Errorf("%w: the cursor envelope carries the key %q, which is not one this codec writes",
				ErrInvalidOperationsQuery, key)
		}
	}
	for key := range cursorEnvelopeKeys[""] {
		if _, ok := fields[key]; !ok {
			return CursorEnvelope{}, fmt.Errorf("%w: the cursor envelope carries no %q", ErrInvalidOperationsQuery, key)
		}
	}
	if err := validateKeys(raw, cursorEnvelopeKeys); err != nil {
		// The walk catches a key named twice, which the map above collapses.
		return CursorEnvelope{}, fmt.Errorf("%w: the cursor envelope: %w", ErrInvalidOperationsQuery, err)
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
	for host := range decoded.Bounds {
		if err := validateBoundaryName(host); err != nil {
			return CursorEnvelope{}, fmt.Errorf("%w: cursor bounds: %w", ErrInvalidOperationsQuery, err)
		}
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
//
// The cursor's bounds entry for a host is the host's CURRENT boundary triple at
// mint (§8: "its current (generation, incarnation id, presenceEpoch)
// boundary"), or the "absent" marker when the host held no records. That entry
// is the staleness reference: a continuation requires full triple equality with
// the host's current entry, so any generation or presence advance between pages
// refuses. The pair a host-pinned page was actually served under is not lost,
// though: the row the cursor's position names — the last row the page listed —
// carries it, so the continuation recovers the pinned-cursor window from `pos`
// even when the listed pair is a superseded incarnation.
func readOperationsLocked(state *snapshot, q OperationsQuery, live CursorEpoch) (OperationsPage, error) {
	limit := q.Limit
	if limit == 0 {
		limit = DefaultOperationsLimit
	}
	if limit > MaxOperationsLimit {
		limit = MaxOperationsLimit
	}

	// The current entry of every host the store knows, computed once: the
	// continuation validation and the unfiltered host universe both read it, so
	// no pass scans the record set per host.
	current, err := currentCursorBoundsLocked(state)
	if err != nil {
		return OperationsPage{}, err
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
	// the host's current entry. A host the store has never seen is a refusal
	// too: a forged "absent" entry must not be echoed as authoritative, and a
	// host whose boundary the store no longer holds has changed under the
	// cursor.
	if continuing {
		for host, stored := range window.Bounds {
			currentEntry, known := current[host]
			if !known {
				return OperationsPage{}, &CursorStaleError{
					Reason:  fmt.Sprintf("host %q is not a host this store knows; re-list from the first page", host),
					Binding: StaleBindingGeneration,
				}
			}
			if currentEntry != stored {
				return OperationsPage{}, &CursorStaleError{
					Reason:  fmt.Sprintf("host %q's stored boundary no longer matches its current boundary; re-list from the first page", host),
					Binding: StaleBindingGeneration,
				}
			}
		}
	}

	page := OperationsPage{}
	var (
		pinned         = q.Host != ""
		pageHosts      = map[string]bool{}
		windowPairs    = map[string]OperationPair{}
		pinnedPair     OperationPair
		havePinnedPair bool
		mintBounds     map[string]CursorBound
	)
	// The `id` detail filter addresses one record directly (§10's "detail
	// filter ... resolves through it directly"): it is not a window, so the
	// pair window stands down for it. A named pair is an explicit window and
	// keeps its filter.
	detailOnly := q.ID != "" && q.Generation == nil

	if pinned {
		pageHosts[q.Host] = true
		if continuing {
			if _, ok := window.Bounds[q.Host]; !ok {
				return OperationsPage{}, &CursorStaleError{
					Reason:  fmt.Sprintf("host %q was not in the cursor's pinned window; re-list from the first page", q.Host),
					Binding: StaleBindingGeneration,
				}
			}
			// The pinned-cursor window: the pair of the row the cursor resumes
			// after — the last row the page listed. Never the host's current
			// pair, so an omitted filter reads as the pinned window (§8).
			anchor, ok := recordAtLocked(state, window.Position)
			if !ok {
				// S6 seam: once compaction exists, a row at or before `pos`
				// removed since mint is §8's `cursor-invalidated` arm; until
				// then a missing row is a stale re-list.
				return OperationsPage{}, &CursorStaleError{
					Reason:  fmt.Sprintf("the row this cursor resumes after (%s) is not in the store; re-list from the first page", window.Position),
					Binding: StaleBindingGeneration,
				}
			}
			if anchor.Host != q.Host {
				return OperationsPage{}, &CursorStaleError{
					Reason: fmt.Sprintf("the row this cursor resumes after belongs to host %q, not %q; re-list from the first page",
						anchor.Host, q.Host),
					Binding: StaleBindingGeneration,
				}
			}
			pinnedPair = OperationPair{Generation: anchor.Generation, IncarnationID: anchor.IncarnationID}
			havePinnedPair = true
			if q.Generation != nil && (*q.Generation != pinnedPair.Generation ||
				(q.IncarnationID != "" && q.IncarnationID != pinnedPair.IncarnationID)) {
				return OperationsPage{}, &CursorStaleError{
					Reason: fmt.Sprintf("the cursor pinned host %q at generation %d/%s, not %d/%s; re-list from the first page",
						q.Host, pinnedPair.Generation, pinnedPair.IncarnationID, *q.Generation, q.IncarnationID),
					Binding: StaleBindingGeneration,
				}
			}
		} else {
			pinnedPair, havePinnedPair = resolvePinnedPair(state, q)
		}
		if havePinnedPair && !detailOnly {
			windowPairs[q.Host] = pinnedPair
		}
		// The cursor's bounds pin the host's current entry even though the
		// response carries the top-level pair instead of a hostBoundaries map.
		if continuing {
			mintBounds = window.Bounds
		} else if entry, ok := current[q.Host]; ok {
			mintBounds = map[string]CursorBound{q.Host: entry}
		} else {
			mintBounds = map[string]CursorBound{q.Host: {Absent: true}}
		}
	} else {
		if continuing {
			for host, stored := range window.Bounds {
				pageHosts[host] = true
				if !stored.Absent {
					windowPairs[host] = OperationPair{
						Generation:    stored.Boundary.Generation,
						IncarnationID: stored.Boundary.IncarnationID,
					}
				}
			}
			page.HostBoundaries = window.Bounds
			mintBounds = window.Bounds
			// An unfiltered continuation must be a plain unfiltered window: the
			// row at the cursor's position carries the pair its host was listed
			// under, and §8 refuses "a cursor minted for one pair validated
			// against an unfiltered query". A cursor minted under a host-pinned
			// window presented without the host name lands here, and that
			// mismatch is the refusal — never a mixed page.
			if !detailOnly {
				anchor, ok := recordAtLocked(state, window.Position)
				if !ok {
					return OperationsPage{}, &CursorStaleError{
						Reason:  fmt.Sprintf("the row this cursor resumes after (%s) is not in the store; re-list from the first page", window.Position),
						Binding: StaleBindingGeneration,
					}
				}
				stored, known := window.Bounds[anchor.Host]
				if !known || stored.Absent ||
					stored.Boundary.Generation != anchor.Generation || stored.Boundary.IncarnationID != anchor.IncarnationID {
					return OperationsPage{}, &CursorStaleError{
						Reason:  "this cursor was minted for one pair; pass the host name and pair back to continue it, or re-list from the first page",
						Binding: StaleBindingGeneration,
					}
				}
			}
		} else {
			hosts := slices.Sorted(maps.Keys(current))
			for _, host := range hosts {
				pageHosts[host] = true
				if entry := current[host]; !entry.Absent && !detailOnly {
					windowPairs[host] = OperationPair{
						Generation:    entry.Boundary.Generation,
						IncarnationID: entry.Boundary.IncarnationID,
					}
				}
			}
			page.HostBoundaries = current
			mintBounds = current
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
		if pair, ok := windowPairs[record.Host]; ok {
			if record.Generation != pair.Generation || record.IncarnationID != pair.IncarnationID {
				continue
			}
		} else if !detailOnly {
			// A host with no pinned pair — the "absent" marker, or a host
			// outside the window — has no records to list.
			continue
		}
		records = append(records, cloneRecord(record))
		if len(records) == limit {
			break
		}
	}
	page.Records = records

	if pinned {
		// §8: the host-pinned response carries "the effective generation and
		// incarnationId actually listed". The window pair is that; a detail
		// lookup that named no pair echoes the row it listed instead.
		generation, incarnation := uint64(0), ""
		switch {
		case havePinnedPair:
			generation, incarnation = pinnedPair.Generation, pinnedPair.IncarnationID
		case detailOnly && len(records) == 1:
			generation, incarnation = records[0].Generation, records[0].IncarnationID
		case q.Generation != nil:
			generation, incarnation = *q.Generation, q.IncarnationID
		}
		page.Generation = &generation
		page.IncarnationID = incarnation
	}

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

// resolvePinnedPair answers the effective pair a host-pinned first page lists
// under (§8: "a host-pinned response carries the effective generation and
// incarnationId actually listed"; §10: "generation selects the incarnation
// after client operation-ID reuse (omitted: the current generation)").
//
// The named pair is taken exactly. A named generation resolves to one
// incarnation — the boundary's when that generation is current, else the
// newest retained record's for that generation, so a same-generation collision
// (§12's "colliding same-generation incarnation") is disambiguated by naming
// both halves — and an omitted pair is the host's current pair, the mirrored
// boundary. ok is false only when the host has no pair at all, which is also a
// host with no records to list.
func resolvePinnedPair(state *snapshot, q OperationsQuery) (OperationPair, bool) {
	boundary, hasBoundary := state.Boundaries[q.Host]
	switch {
	case q.Generation != nil && q.IncarnationID != "":
		return OperationPair{Generation: *q.Generation, IncarnationID: q.IncarnationID}, true
	case q.Generation != nil:
		if hasBoundary && boundary.Generation == *q.Generation {
			return OperationPair{Generation: *q.Generation, IncarnationID: boundary.IncarnationID}, true
		}
		if record, ok := newestRecordForGeneration(state, q.Host, *q.Generation); ok {
			return OperationPair{Generation: record.Generation, IncarnationID: record.IncarnationID}, true
		}
		return OperationPair{}, false
	case hasBoundary:
		return OperationPair{Generation: boundary.Generation, IncarnationID: boundary.IncarnationID}, true
	}
	return OperationPair{}, false
}

// newestRecordForGeneration returns the newest retained record pinned to host
// under generation, the incarnation a generation-only filter selects when the
// generation is not the host's current one.
func newestRecordForGeneration(state *snapshot, host string, generation uint64) (Record, bool) {
	for _, record := range slices.Backward(state.Records) {
		if record.Host == host && record.Generation == generation {
			return record, true
		}
	}
	return Record{}, false
}

// recordAtLocked returns the stored record with the controller-assigned id.
func recordAtLocked(state *snapshot, id string) (Record, bool) {
	for i := range state.Records {
		if state.Records[i].ID == id {
			return state.Records[i], true
		}
	}
	return Record{}, false
}

// currentCursorBoundsLocked computes every known host's current bounds entry in
// one pass: the record-holding hosts get their mirrored boundary triple, and
// the mirrored-boundary names that hold no records get the "absent" marker. A
// host that holds records but no mirrored boundary is the state no writer of
// this store produces, refused as ErrMissingHostBoundary rather than minting or
// validating an entry the store cannot ground.
func currentCursorBoundsLocked(state *snapshot) (map[string]CursorBound, error) {
	bounds := make(map[string]CursorBound, len(state.Boundaries))
	for name := range state.Boundaries {
		bounds[name] = CursorBound{Absent: true}
	}
	for i := range state.Records {
		host := state.Records[i].Host
		if entry, known := bounds[host]; known && !entry.Absent {
			continue
		}
		boundary, ok := state.Boundaries[host]
		if !ok {
			return nil, fmt.Errorf("%w: host %q", ErrMissingHostBoundary, host)
		}
		bounds[host] = CursorBound{Boundary: boundary}
	}
	return bounds, nil
}
