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
// incarnationId, presenceEpoch] | "absent"}, window: "host" | "all",
// quarantineEpoch: number}`. `pos`
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
// The envelope's `window` key records whether the cursor was minted
// host-pinned ("host") or unfiltered ("all"). It is part of the cursor's
// identity because a pinned current pair and an unfiltered entry are
// byte-identical, so the bounds map alone cannot tell the two windows apart: a
// continuation whose request shape does not match the recorded window — a
// pinned cursor presented without its host, a pinned cursor naming a different
// host, or an unfiltered cursor narrowed to one host — is a typed stale-entry
// re-list refusal, exactly §8's "a cursor minted for one pair validated
// against an unfiltered query is a typed `stale-entry` re-list refusal". (The
// window key is this slice's one addition to §8's five-key literal; the spec
// carries the amendment.)
//
// Within a host-pinned window, the pair the page was actually served under is
// recovered on the continuation from the row `pos` names: that row was listed
// by the page, so it carries the listed pair. The continuation pins its window
// to that pair whether the request repeats it or omits it — "An omitted filter
// on a later page reads as the pinned-cursor window, never as a fresh unpinned
// query" — and a repeated pair that is not it is refused. That is how a page
// under a superseded incarnation stays pageable without the envelope carrying a
// second triple, while the stored entry still detects every boundary advance. An
// unfiltered page lists each host's current pair (§10: "generation ... omitted:
// the current generation"), and history stays reachable through the host-pinned
// pair filter and the `id` detail filter. A `pos` row the store no longer holds
// (S6 compaction's territory) is a stale re-list here.
//
// What this file deliberately does not own, and the named seams its read path
// leaves:
//
//   - Post-cursor compaction (`cursor-invalidated`) is implemented here
//     (checkCursorCompactionLocked), reading the tombstones and `compactSeq`
//     retention.go persists: the comparison is envelope-global, exactly once,
//     and the refusal names the compacting `compactSeq` plus the affected
//     host's stored bounds entry (§8 "Refusals", §11 `cursor-invalidated`).
//   - The live `quarantineEpoch`: the custody-first quarantine (S8) persists
//     and advances the counter outside the store file. The comparison itself
//     is implemented here; CursorEpoch returns zero until that slice lands.
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
	"strings"
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

// CursorInvalidatedError is §8's mid-pagination compaction refusal: a
// compaction removed rows at or before the cursor's `pos` since the cursor was
// minted, so the continuation must restart from the first page. CompactSeq is
// the compacting write's durable value (the envelope-global value §8 requires,
// never the live one when a later compaction advanced it further), Host is the
// affected (compacted) host, and Bound is that host's bounds entry as stored
// at mint — the triple or the "absent" marker. It is distinct from the
// stale-entry re-list refusal.
type CursorInvalidatedError struct {
	CompactSeq uint64
	Host       string
	Bound      CursorBound
}

func (e *CursorInvalidatedError) Error() string {
	return fmt.Sprintf("hostops: a compaction (compactSeq %d) removed rows at or before the cursor's position on host %q; re-list from the first page",
		e.CompactSeq, e.Host)
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
	// Unmirrored marks an entry minted from the host's own records because no
	// mirrored triple exists: §4's custody import is the one writer of such a
	// host (custody ownership carries no presence epoch, so no valid mirror can
	// be seeded), and grounding the entry in the imported record keeps the
	// closed name addressable by id. The mark never reaches the wire — the
	// envelope carries the triple — and comparison is by value (sameEntry), so a
	// cursor round-tripped through the client still continues.
	Unmirrored bool
}

// sameEntry compares two bounds entries by the values §8 pins, not by how they
// were grounded: a synthesized entry and the same triple decoded back from a
// cursor are the same entry, because the envelope can only carry the triple.
func (b CursorBound) sameEntry(other CursorBound) bool {
	return b.Absent == other.Absent && b.Boundary == other.Boundary
}

// CursorWindow names the scope a cursor was minted over: "host" for a
// host-pinned read, "all" for an unfiltered cross-host read. §8's envelope
// otherwise cannot tell a pinned window from an unfiltered one over the same
// host set — a pinned current pair and an unfiltered entry are byte-identical —
// so the window is part of the cursor's identity and a continuation whose
// request does not match the recorded window is a typed stale-entry re-list.
type CursorWindow string

const (
	// CursorWindowHost is a cursor minted by a host-pinned read.
	CursorWindowHost CursorWindow = "host"
	// CursorWindowAll is a cursor minted by an unfiltered cross-host read.
	CursorWindowAll CursorWindow = "all"
)

// valid reports whether w is one of the two window values this codec mints.
func (w CursorWindow) valid() bool { return w == CursorWindowHost || w == CursorWindowAll }

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
// triple, which has no host name of its own: a zero generation, or an empty or
// oversized or non-UTF-8 incarnation id, is no triple any writer of this store
// emits. A zero presence epoch is allowed here and only here: it is the
// placeholder a synthesized entry carries for a mirror-less host (§4's custody
// import, whose record grounds the pair but not the presence), while every
// mirror triple carries a non-zero epoch (validateBoundary refuses a zero one).
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
	// Window records whether the cursor was minted host-pinned or unfiltered.
	Window CursorWindow
}

// cursorEnvelopeJSON is the wire literal. Field order is the spec's
// (`{v, pos, compactSeq, bounds, quarantineEpoch}`), and every key is always
// present — a cursor is never best-effort decoded.
type cursorEnvelopeJSON struct {
	V               int                    `json:"v"`
	Pos             string                 `json:"pos"`
	CompactSeq      uint64                 `json:"compactSeq"`
	Bounds          map[string]CursorBound `json:"bounds"`
	Window          CursorWindow           `json:"window"`
	QuarantineEpoch uint64                 `json:"quarantineEpoch"`
}

// EncodeCursor renders §8's opaque cursor: the base64url JSON envelope. It
// refuses an envelope this codec does not mint (a version other than 2, a
// window outside the two values, a position outside the allocator form, a
// malformed bounds entry) and an envelope whose encoded form would exceed
// MaxCursorBytes — the latter as *CursorTooLargeError, §8's distinct over-cap
// refusal.
func EncodeCursor(envelope CursorEnvelope) (string, error) {
	if envelope.Version != cursorEnvelopeVersion {
		return "", fmt.Errorf("%w: cannot encode cursor envelope version %d", ErrInvalidOperationsQuery, envelope.Version)
	}
	if !envelope.Window.valid() {
		return "", fmt.Errorf("%w: cannot encode cursor window %q", ErrInvalidOperationsQuery, envelope.Window)
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
		Window:          envelope.Window,
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
		"window":          {},
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
	// The version first: an envelope this codec did not mint is §8's typed
	// stale-entry re-list — including an older version whose key set this codec
	// would otherwise refuse as malformed, so a v1 cursor still reads as stale.
	if rawVersion, ok := fields["v"]; ok {
		var version int
		if err := json.Unmarshal(rawVersion, &version); err != nil {
			return CursorEnvelope{}, fmt.Errorf("%w: the cursor's version is not a number: %w", ErrInvalidOperationsQuery, err)
		}
		if version != cursorEnvelopeVersion {
			return CursorEnvelope{}, &CursorStaleError{
				Reason:  fmt.Sprintf("the envelope version %d is not %d; re-list from the first page", version, cursorEnvelopeVersion),
				Binding: StaleBindingGeneration,
			}
		}
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
	if !decoded.Window.valid() {
		return CursorEnvelope{}, fmt.Errorf("%w: the cursor carries the window %q, which is not one this codec writes",
			ErrInvalidOperationsQuery, decoded.Window)
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
		Window:          decoded.Window,
	}, nil
}

// CursorEpoch is the store-wide epoch pair every minted cursor pins: the
// compaction position (§4's `compactSeq`) and the quarantine epoch.
type CursorEpoch struct {
	CompactSeq      uint64
	QuarantineEpoch uint64
}

// CursorEpoch returns the live epoch pair: the compaction position, persisted
// in the store file and advanced by every compacting write (§4), and the
// quarantine epoch, persisted outside the quarantined file and advanced by
// exactly one per corrupt-store quarantine (§4).
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
	return CursorEpoch{CompactSeq: s.cell.state.CompactSeq, QuarantineEpoch: s.cell.quarantineEpoch}
}

// checkCursorCompactionLocked implements §8's post-cursor compaction refusal:
// "A compaction that removed rows at or before the cursor's `pos` since the
// cursor was minted surfaces a typed `cursor-invalidated` refusal naming the
// compacting `compactSeq` (the envelope-global value) plus the affected host's
// `bounds` entry (`[generation, incarnationId, presenceEpoch]` as stored at
// mint, or `"absent"`)"; the client restarts from the first page. "A
// host-pinned page names the single listed host's entry; an unfiltered
// cross-host page names the compacted host's entry."
//
// The evidence is the compaction ledger, deliberately independent of the
// bounded dedup tombstones: one mark per compacting write carries every
// affected host's smallest removed row id, so the predicate "some compacting
// write after the cursor's pin removed a row at or before `pos` in this
// cursor's window" is "a mark with Seq above the pin names a window host whose
// smallest removed id is at or before `pos`", evaluated across every retained
// mark — exact for every compaction the ledger still covers, and per host, so
// a write that removed rows on several hosts cannot hide one window's removal
// behind another's. The window is the cursor's own bounds map: a pinned cursor
// lists one host, an unfiltered one every host in its query. The earliest such
// write (its compacting `compactSeq`) is named.
//
// The ledger is bounded (MaxCompactionMarks), so once marks have been dropped
// above the cursor's pin the exact answer is unknowable. The store persists
// that as the dropped-marks floor, and the check refuses coarsely — naming the
// oldest retained in-window mark, or, when the window kept none, the live
// compaction value with the window's own host. Over-refusing only re-lists the
// client from page one; silently serving on could skip removed rows. The
// caller holds the store mutex.
func checkCursorCompactionLocked(state *snapshot, envelope CursorEnvelope, live CursorEpoch) error {
	// Evidence is complete exactly while the dropped-marks floor sits at or
	// below the cursor's pin: every compacting write after the pin still has
	// its per-host marks. Once the floor is above the pin, some write in the
	// cursor's range lost its marks, the exact answer is unknowable, and a
	// retained later mark must not be presented as "the compacting" write —
	// refuse coarsely instead of serving on.
	if state.CompactionFloor > envelope.CompactSeq {
		return coarseCursorInvalidated(state, envelope, live)
	}
	exact := -1
	exactHost := ""
	for i := range state.CompactionMarks {
		mark := state.CompactionMarks[i]
		if mark.Seq <= envelope.CompactSeq {
			continue
		}
		host, invalidates := mark.invalidatingHost(envelope)
		if !invalidates {
			continue
		}
		if exact < 0 || mark.Seq < state.CompactionMarks[exact].Seq {
			exact, exactHost = i, host
		}
	}
	if exact >= 0 {
		return cursorInvalidatedFor(state.CompactionMarks[exact].Seq, exactHost, envelope)
	}
	return nil
}

// coarseCursorInvalidated is the lost-evidence refusal: the oldest retained
// in-window mark if the window kept one, else the live compaction value with
// the window's own host. Over-refusing only re-lists the client from page one;
// silently serving on could skip removed rows.
func coarseCursorInvalidated(state *snapshot, envelope CursorEnvelope, live CursorEpoch) error {
	oldest := -1
	oldestHost := ""
	for i := range state.CompactionMarks {
		mark := state.CompactionMarks[i]
		if mark.Seq <= envelope.CompactSeq {
			continue
		}
		host, affects := mark.affectsWindow(envelope)
		if !affects {
			continue
		}
		if oldest < 0 || mark.Seq < state.CompactionMarks[oldest].Seq {
			oldest, oldestHost = i, host
		}
	}
	if oldest >= 0 {
		return cursorInvalidatedFor(state.CompactionMarks[oldest].Seq, oldestHost, envelope)
	}
	host := coarseWindowHost(envelope)
	if host == "" {
		// An empty window has no host to name; no page over it can list a row.
		return nil
	}
	return &CursorInvalidatedError{CompactSeq: live.CompactSeq, Host: host, Bound: envelope.Bounds[host]}
}

// invalidatingHost returns the host whose removal a mark evidences for the
// cursor: an affected host in the cursor's window with a removed row id at or
// before the cursor's position. When several qualify, the smallest removed id
// (then the host name) is the deterministic choice.
func (m CompactionMark) invalidatingHost(envelope CursorEnvelope) (string, bool) {
	return m.windowHost(envelope, true)
}

// affectsWindow reports whether a mark removed rows on any host in the
// cursor's window, without requiring the row to sit at or before its position.
// It is the coarse fallback's test for a write whose exact evidence is gone.
func (m CompactionMark) affectsWindow(envelope CursorEnvelope) (string, bool) {
	return m.windowHost(envelope, false)
}

// windowHost picks one affected host from a mark's per-host evidence, smallest
// removed id first; with atMostPos only hosts whose smallest removed id is at
// or before the cursor's position qualify.
func (m CompactionMark) windowHost(envelope CursorEnvelope, atMostPos bool) (string, bool) {
	best, bestID := "", ""
	for host, id := range m.Hosts {
		if _, inWindow := envelope.Bounds[host]; !inWindow {
			continue
		}
		if atMostPos && id > envelope.Position {
			continue
		}
		if best == "" || id < bestID || (id == bestID && host < best) {
			best, bestID = host, id
		}
	}
	return best, best != ""
}

// cursorInvalidatedFor builds the refusal one compacting write names, with the
// affected host's bounds entry as stored at the cursor's mint.
func cursorInvalidatedFor(compactSeq uint64, host string, envelope CursorEnvelope) error {
	bound, stored := envelope.Bounds[host]
	if !stored {
		// Unreachable by the window checks above; keep the refusal typed rather
		// than guessing a triple the cursor never carried.
		bound = CursorBound{Absent: true}
	}
	return &CursorInvalidatedError{CompactSeq: compactSeq, Host: host, Bound: bound}
}

// coarseWindowHost names the host a lost-evidence refusal carries: the pinned
// cursor's single host, or the first host of an unfiltered window, preferring
// a host whose entry is a triple over an "absent" marker. It is deliberately
// the cursor's own data — no dropped ledger entry can be asked for its host.
func coarseWindowHost(envelope CursorEnvelope) string {
	if envelope.Window == CursorWindowHost || len(envelope.Bounds) == 1 {
		for host := range envelope.Bounds {
			return host
		}
		return ""
	}
	hosts := slices.Sorted(maps.Keys(envelope.Bounds))
	for _, host := range hosts {
		if !envelope.Bounds[host].Absent {
			return host
		}
	}
	if len(hosts) > 0 {
		return hosts[0]
	}
	return ""
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

	// The current entry of every host the store knows, computed once: the
	// continuation validation and the unfiltered host universe both read it, so
	// no pass scans the record set per host. It runs after the cursor's
	// store-wide checks, so a pre-quarantine cursor is the typed stale-entry
	// refusal even when the replacement store's imported hosts carry no mirrored
	// boundary yet.
	current := currentCursorBoundsLocked(state)

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
			if !currentEntry.sameEntry(stored) {
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
	// The window marker travels with the cursor: a first page records whether
	// the read named a host, and a continuation preserves the recorded window.
	mintWindow := window.Window
	if !continuing {
		if pinned {
			mintWindow = CursorWindowHost
		} else {
			mintWindow = CursorWindowAll
		}
	}

	if pinned {
		pageHosts[q.Host] = true
		if continuing {
			// The window is part of the cursor's identity (§8): a cursor minted
			// unfiltered cannot be narrowed to one host mid-pagination, because
			// that would truncate the authoritative map and silently drop every
			// other host's records.
			if window.Window != CursorWindowHost {
				return OperationsPage{}, &CursorStaleError{
					Reason:  "the cursor was minted for the unfiltered cross-host window, not a host-pinned one; re-list from the first page",
					Binding: StaleBindingGeneration,
				}
			}
			if _, ok := window.Bounds[q.Host]; !ok || len(window.Bounds) != 1 {
				return OperationsPage{}, &CursorStaleError{
					Reason:  fmt.Sprintf("host %q is not the host this cursor pinned; re-list from the first page", q.Host),
					Binding: StaleBindingGeneration,
				}
			}
			// The pinned-cursor window: the pair of the row the cursor resumes
			// after — the last row the page listed. Never the host's current
			// pair, so an omitted filter reads as the pinned window (§8).
			anchor, ok := anchorRecordLocked(state, window.Position)
			if !ok {
				// A row at or before `pos` removed by a compaction since mint
				// is §8's `cursor-invalidated` arm, and checkCursorCompactionLocked
				// answered it above before this point. What remains here is a
				// position this store never held (a forged or truncated
				// cursor): the stale re-list.
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
		} else if !detailOnly {
			// A detail lookup resolves no window pair: the row it returns is the
			// pair that page lists under, and the response echoes that row.
			resolved, ok, err := resolvePinnedPair(state, q)
			if err != nil {
				return OperationsPage{}, err
			}
			pinnedPair, havePinnedPair = resolved, ok
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
			if !havePinnedPair && !detailOnly && entry.Unmirrored {
				// A mirror-less host (the custody import) has one ground truth:
				// its own record set's pair. Listing under it keeps a host-pinned
				// read of a closed name from being a silently empty page. A detail
				// lookup resolves no window pair — the row it names is the pair
				// that page lists under — so it is left to address its own row,
				// exactly as a mirrored host's detail lookup is.
				windowPairs[q.Host] = OperationPair{
					Generation:    entry.Boundary.Generation,
					IncarnationID: entry.Boundary.IncarnationID,
				}
			}
		} else {
			mintBounds = map[string]CursorBound{q.Host: {Absent: true}}
		}
	} else {
		if continuing {
			// §8: "a cursor minted for one pair validated against an unfiltered
			// query is a typed `stale-entry` re-list refusal." The recorded window
			// is the only way to tell a pinned cursor from an unfiltered one over
			// the same host set, so the request shape must match it exactly.
			if window.Window != CursorWindowAll {
				return OperationsPage{}, &CursorStaleError{
					Reason:  "this cursor was minted for one host-pinned window; pass its host name and pair back, or re-list from the first page",
					Binding: StaleBindingGeneration,
				}
			}
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
				anchor, ok := anchorRecordLocked(state, window.Position)
				if !ok {
					// The compaction arm is checkCursorCompactionLocked's,
					// answered above; this is the never-held position.
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

	// §4's replay is retrievable: a detail lookup — the `id` filter for the
	// controller-assigned id, or `operationId` for the client operation id —
	// resolves a compacted record out of its dedup tombstone with
	// `compacted: true`, because the record itself no longer sits in the
	// record set and without this the contract has no retrieval path. Listing
	// pages stay record-only: a tombstone is a replay source, not a listed
	// row, and pagination never resumes on one.
	if q.ID != "" || q.ClientOperationID != "" {
		for i := range state.Tombstones {
			tombstone := state.Tombstones[i]
			if continuing && tombstone.ID <= window.Position {
				// A continuation resumes after its cursor's position: the replay
				// this page already answered is never served again, or a page
				// whose only result is a tombstone would mint the identical
				// cursor forever.
				continue
			}
			if q.ID != "" {
				// A direct id lookup addresses the row itself: a retained
				// tombstone is retrievable even when its host no longer appears
				// in the current host set — a removed host's boundary drops
				// with its last record while its tombstones stay — so the
				// unfiltered and host-pinned spellings of the same lookup agree.
				if q.Host != "" && tombstone.Host != q.Host {
					continue
				}
			} else if !pageHosts[tombstone.Host] {
				// The operationId filter stays a listing filter over the
				// cursor's window: retrieval of a compacted record is by its
				// controller-assigned id, which deploy/restart return.
				continue
			}
			if q.ClientOperationID != "" && tombstone.ClientOperationID != q.ClientOperationID {
				continue
			}
			if q.State != "" && tombstone.State != q.State {
				continue
			}
			if q.ID != "" && tombstone.ID != q.ID {
				continue
			}
			if pair, ok := windowPairs[tombstone.Host]; ok {
				if tombstone.Generation != pair.Generation || tombstone.IncarnationID != pair.IncarnationID {
					continue
				}
			} else if !detailOnly {
				continue
			}
			records = append(records, tombstone.record())
		}
		slices.SortStableFunc(records, func(a, b Record) int { return strings.Compare(a.ID, b.ID) })
		if len(records) > limit {
			records = records[:limit]
		}
		page.Records = records
	}

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
		case !havePinnedPair && !detailOnly && q.Generation == nil:
			if entry, ok := current[q.Host]; ok && entry.Unmirrored {
				generation, incarnation = entry.Boundary.Generation, entry.Boundary.IncarnationID
			}
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
			Window:          mintWindow,
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
// The named pair is taken exactly. A named generation resolves to the current
// incarnation when that generation is the host's current one; a generation the
// host has moved past is a superseded selection, and §10 requires the
// incarnationId alongside it ("incarnationId narrows that selection to the
// exact incarnation — required alongside generation whenever the caller names a
// superseded pair") — a generation-only superseded read would silently hide
// every other incarnation of that generation, so it is refused as an invalid
// query. An omitted pair is the host's current pair, the mirrored boundary. ok
// is false only when the host has no pair at all, which is also a host with no
// records to list.
func resolvePinnedPair(state *snapshot, q OperationsQuery) (OperationPair, bool, error) {
	boundary, hasBoundary := state.Boundaries[q.Host]
	switch {
	case q.Generation != nil && q.IncarnationID != "":
		return OperationPair{Generation: *q.Generation, IncarnationID: q.IncarnationID}, true, nil
	case q.Generation != nil:
		if hasBoundary && boundary.Generation == *q.Generation {
			return OperationPair{Generation: *q.Generation, IncarnationID: boundary.IncarnationID}, true, nil
		}
		if !hasBoundary {
			// A mirror-less host — §4's custody import — is pinned by its own
			// newest record, and a generation-only read naming that pair resolves
			// like any other current pair instead of refusing.
			if pair, ok := currentHostPairLocked(state, q.Host); ok && pair.Generation == *q.Generation {
				return pair, true, nil
			}
		}
		return OperationPair{}, false, fmt.Errorf(
			"%w: host %q has moved past generation %d, so the read needs its incarnationId",
			ErrInvalidOperationsQuery, q.Host, *q.Generation)
	case hasBoundary:
		return OperationPair{Generation: boundary.Generation, IncarnationID: boundary.IncarnationID}, true, nil
	}
	return OperationPair{}, false, nil
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

// anchorRecordLocked resolves a cursor's anchor row: the retained record with
// that id, or — when the previous page's last row was itself a tombstone replay
// (the `id`/`operationId` detail filters resolve those) — the replay rebuilt
// from its tombstone. The anchor's host and pair are what a continuation
// recovers its pinned window from, and a compacted replay carries both.
func anchorRecordLocked(state *snapshot, id string) (Record, bool) {
	if record, ok := recordAtLocked(state, id); ok {
		return record, true
	}
	for i := range state.Tombstones {
		if state.Tombstones[i].ID == id {
			return state.Tombstones[i].record(), true
		}
	}
	return Record{}, false
}

// currentCursorBoundsLocked computes every known host's current bounds entry in
// one pass: the record-holding hosts get their mirrored boundary triple, and
// the mirrored-boundary names that hold no records get the "absent" marker. A
// host that holds records but no mirrored boundary — the state §4's custody
// import produces, and the one state no other writer of this store creates —
// gets an entry minted from its own newest record: its pinned pair with the
// documented zero presence placeholder, marked Unmirrored. Grounding the entry
// in the record keeps every closed name addressable by id, which §4 requires of
// the operations detail filter, and never fabricates a mirror triple: any later
// mirror write moves the host's entry and refuses stale continuations.
func currentCursorBoundsLocked(state *snapshot) map[string]CursorBound {
	bounds := make(map[string]CursorBound, len(state.Boundaries))
	for name := range state.Boundaries {
		bounds[name] = CursorBound{Absent: true}
	}
	newest := newestRecordByHostLocked(state)
	for host, boundary := range state.Boundaries {
		if _, holdsRecords := newest[host]; holdsRecords {
			bounds[host] = CursorBound{Boundary: boundary}
		}
	}
	for host, record := range newest {
		if _, mirrored := state.Boundaries[host]; mirrored {
			continue
		}
		bounds[host] = CursorBound{
			Boundary:   Boundary{Generation: record.Generation, IncarnationID: record.IncarnationID},
			Unmirrored: true,
		}
	}
	return bounds
}

// newestRecordByHostLocked returns, per host, the record whose identity is the
// host's current one: the highest generation, ties broken by the newest record
// id. It is the ground truth a mirror-less host's bounds entry and pair are
// minted from (§4's custody import), and mirrors do not participate.
func newestRecordByHostLocked(state *snapshot) map[string]Record {
	newest := make(map[string]Record, len(state.Records))
	for i := range state.Records {
		record := state.Records[i]
		current, held := newest[record.Host]
		if !held || record.Generation > current.Generation ||
			(record.Generation == current.Generation && record.ID > current.ID) {
			newest[record.Host] = record
		}
	}
	return newest
}

// currentHostPairLocked returns the pair a host is pinned by: its mirrored
// triple when one exists, and otherwise the pair its own newest record carries —
// the state §4's custody import produces, which a host-and-generation read must
// resolve like any other current pair.
func currentHostPairLocked(state *snapshot, host string) (OperationPair, bool) {
	if boundary, ok := state.Boundaries[host]; ok {
		return OperationPair{Generation: boundary.Generation, IncarnationID: boundary.IncarnationID}, true
	}
	record, ok := newestRecordByHostLocked(state)[host]
	if !ok {
		return OperationPair{}, false
	}
	return OperationPair{Generation: record.Generation, IncarnationID: record.IncarnationID}, true
}
