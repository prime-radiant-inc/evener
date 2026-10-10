package hostops

// Dedup and the atomic consume-and-create write (spec 08b §4, §6 steps 1 and 4).
//
// This file owns the durable half of `deploy`/`restart`'s operation model: the
// (host, kind, client operation ID, pinned generation, pinned incarnation id)
// dedup scope, and the one atomic store write that re-reads the host's token
// row, compare-and-consumes its nonce, re-checks its deadline in that same
// transaction, deletes the row, and promotes the persisted probe epoch to the
// `pending` operation record carrying the worker's fencing epoch (§6 step 4).
// Consume is delete in that write, never a mark. Records reach the store only
// through that write, through restart's atomic record creation, or through the
// worker's transitions of a record it owns.
//
// What this file deliberately does not own: the per-host gate and the probe
// (§6 step 3, the hub package), the worker's execution and post-operation
// refresh (§6, the hub package), and the cross-file commit intents (§9, S7).
// Retention and compaction live in retention.go; the compacted-ID tombstone
// replay rides the dedup lookup below.

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxProgressEntries bounds one record's progress list (spec §10's
// "timestamped, bounded"): the oldest entries drop first, so a long operation's
// record still carries its tail.
const MaxProgressEntries = 50

// OperationPair is the (generation, incarnation id) pair a token, a record or a
// request pins: §1's guarded-mutation and dedup identity.
type OperationPair struct {
	Generation    uint64
	IncarnationID string
}

// equal reports whether two pairs name the same incarnation.
func (p OperationPair) equal(other OperationPair) bool {
	return p.Generation == other.Generation && p.IncarnationID == other.IncarnationID
}

// matches reports whether a record's pinned pair equals p.
func (p OperationPair) matches(record Record) bool {
	return record.Generation == p.Generation && record.IncarnationID == p.IncarnationID
}

// ErrConflictingOperationID reports a client operation ID already used up by a
// current-generation record of a different host or kind, or by a
// current-generation `host-removed` record (§4, §11's
// `conflicting-operation-id`).
var ErrConflictingOperationID = errors.New("hostops: client operation id already used")

// ConflictingOperationIDError is ErrConflictingOperationID with the record the
// collision was found against, so the caller can render the reference it saw.
type ConflictingOperationIDError struct {
	// Record is the colliding current-generation record.
	Record Record
}

func (e *ConflictingOperationIDError) Error() string {
	return fmt.Sprintf("%v: operation id %q is already %s %q (%s)",
		ErrConflictingOperationID, e.Record.ClientOperationID, e.Record.Kind, e.Record.ID, e.Record.Host)
}

// Unwrap makes every conflicting-operation-id refusal answer errors.Is against
// ErrConflictingOperationID.
func (e *ConflictingOperationIDError) Unwrap() error { return ErrConflictingOperationID }

// ErrProbeEpochUnavailable reports a deploy consume whose probe epoch row is
// absent or bound to a pair other than the operation's: the record's fencing
// epoch would be unpromotable, so the write refuses and nothing is consumed.
// The handler persists the epoch immediately before, so this is a programming
// error in the caller, never a state a well-formed flow reaches.
var ErrProbeEpochUnavailable = errors.New("hostops: the host's durable probe epoch cannot be promoted")

// OperationDedupQuery is one dedup lookup's inputs (§4, §6 step 1). Current is
// the registry's current pair for Host, as the caller resolved it; Intended,
// when set, is the pair the request names (restart's request pair, or the pair
// of the host's outstanding token row when there is one). A request that names
// no pair leaves Intended nil.
type OperationDedupQuery struct {
	ClientOperationID string
	Host              string
	Kind              Kind
	Current           OperationPair
	Intended          *OperationPair
}

// LookupOperation answers §6 step 1's dedup check, under the store mutex. It is
// a read: nothing is consumed and nothing is created.
//
// The rules are §4's exactly:
//
//   - a same-key record — same host, kind and client operation ID — whose
//     pinned pair equals the registry's current pair for the name is the
//     existing record: it is returned with hit true. A current-pair
//     `host-removed` record never matches, and is refused with
//     *ConflictingOperationIDError instead.
//   - a client operation ID colliding with a current-generation record of a
//     different host or kind is refused with *ConflictingOperationIDError. A
//     record of another name counts as current when the store's mirrored
//     boundary for that name still carries the record's pair; a name the store
//     has no boundary for is history, never a collision.
//   - a request whose only matches are retained superseded-generation records
//     of the same (host, kind) returns the newest retained record when it names
//     no intended pair, replays the record pinned to the intended pair when it
//     names one, and refuses *StaleEntryError (pruned-generation) when its
//     intended pair is older than the registry's current pair and matches no
//     retained record.
//   - a request naming the current pair opens fresh: re-add's clean slate, and
//     restart's reuse for a new incarnation.
//
// hit is false with a nil error exactly when the caller may open a fresh
// operation.
func (s *Store) LookupOperation(q OperationDedupQuery) (Record, bool, error) {
	if s == nil {
		return Record{}, false, errors.New("hostops: store is not configured")
	}
	if err := validateDedupQuery(q); err != nil {
		return Record{}, false, err
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	return lookupOperationLocked(&s.cell.state, q)
}

// lookupOperationLocked is LookupOperation's body; the caller holds the store
// mutex so a lookup that leads to a write and the write itself cannot see two
// different record sets.
func lookupOperationLocked(state *snapshot, q OperationDedupQuery) (Record, bool, error) {
	var matches []Record
	for _, record := range state.Records {
		if record.ClientOperationID == q.ClientOperationID {
			matches = append(matches, record)
		}
	}
	// The same-key current-generation record: a replay.
	for _, record := range matches {
		if record.Host != q.Host || record.Kind != q.Kind || !q.Current.matches(record) {
			continue
		}
		if record.HostRemoved {
			// §4: "A `host-removed` record never matches a dedup lookup." Its ID
			// is used up, so the reuse is a conflict rather than a replay.
			return Record{}, false, &ConflictingOperationIDError{Record: cloneRecord(record)}
		}
		return cloneRecord(record), true, nil
	}
	// The same-key dedup tombstone (§4): "A replay naming a tombstoned ID
	// returns the full retained record with `compacted: true` instead of
	// opening a fresh operation, but only while the tombstone's pinned pair
	// still equals the comparison pair for the name: the registry's current
	// pair for a live host, the tombstone's own removed pair for a removed host
	// (which has no live current pair)." No deploy/restart caller ever runs for
	// a detached name, so for a removed host the store's own retained
	// historical boundary — the removal marker's pair, which mirrors the
	// registry tombstone the mirror write carried — IS the comparison pair: a
	// tombstone pinned to an older removal (a re-add followed by a second
	// remove) never equals it and never replays. The newest matching tombstone
	// wins.
	best := -1
	for i := range state.Tombstones {
		tombstone := state.Tombstones[i]
		if tombstone.Host != q.Host || tombstone.Kind != q.Kind || tombstone.ClientOperationID != q.ClientOperationID {
			continue
		}
		comparison := q.Current
		if removed, ok := state.RemovedHosts[tombstone.Host]; ok {
			marker := OperationPair{Generation: removed.Generation, IncarnationID: removed.IncarnationID}
			if q.Current.equal(marker) {
				// The request names the removal the marker records: for a
				// removed host the comparison pair is the marker's own pair.
				comparison = marker
			}
			// A differing marker is stale — a re-add whose clear mirror trailed
			// — so the comparison stays the caller's current pair: the live
			// incarnation's tombstone replays, while a tombstone pinned to the
			// older removal never equals the live pair and never replays.
		}
		if !comparison.equal(OperationPair{Generation: tombstone.Generation, IncarnationID: tombstone.IncarnationID}) {
			// "A tombstone pinned to a superseded pair on a live host never
			// replays; the clean-slate re-add rule wins over the tombstone."
			continue
		}
		if best < 0 || state.Tombstones[best].CompactedSeq < tombstone.CompactedSeq ||
			(state.Tombstones[best].CompactedSeq == tombstone.CompactedSeq && state.Tombstones[best].ID < tombstone.ID) {
			best = i
		}
	}
	if best >= 0 {
		return state.Tombstones[best].record(), true, nil
	}
	// Collisions: any current-generation record with this ID that is not the
	// same-key record above. A record of this name is judged against the
	// request's current pair; another name's record against the store's
	// mirrored boundary for that name.
	for _, record := range matches {
		if record.HostRemoved {
			continue
		}
		if record.Host == q.Host && record.Kind == q.Kind {
			continue
		}
		current := q.Current
		if record.Host != q.Host {
			boundary, ok := state.Boundaries[record.Host]
			if !ok || boundary.Generation != record.Generation || boundary.IncarnationID != record.IncarnationID {
				continue
			}
			current = OperationPair{Generation: boundary.Generation, IncarnationID: boundary.IncarnationID}
		}
		if current.matches(record) {
			return Record{}, false, &ConflictingOperationIDError{Record: cloneRecord(record)}
		}
	}
	// Retained superseded-generation records for the same (host, kind): history
	// the request may replay, never silently replace. Records are stored in
	// ascending id order, so the last match is the newest retained one.
	var retained []Record
	for _, record := range matches {
		if record.Host == q.Host && record.Kind == q.Kind && !record.HostRemoved && !q.Current.matches(record) {
			retained = append(retained, record)
		}
	}
	if q.Intended != nil {
		for i := range len(retained) {
			index := len(retained) - 1 - i
			if q.Intended.matches(retained[index]) {
				return cloneRecord(retained[index]), true, nil
			}
		}
		if !q.Intended.equal(q.Current) {
			// §4: "No fresh operation ever opens on a superseded pair" — an
			// intended pair older than the registry's current pair with no
			// retained record to replay is §11's pruned-generation refusal.
			return Record{}, false, &StaleEntryError{Binding: StaleBindingPrunedGeneration}
		}
		return Record{}, false, nil
	}
	if len(retained) > 0 {
		return cloneRecord(retained[len(retained)-1]), true, nil
	}
	return Record{}, false, nil
}

// validateDedupQuery checks the caller-supplied half of a dedup lookup.
func validateDedupQuery(q OperationDedupQuery) error {
	if q.ClientOperationID == "" || len(q.ClientOperationID) > MaxClientOperationIDBytes || !utf8.ValidString(q.ClientOperationID) {
		return fmt.Errorf("%w: a dedup lookup needs a client operation id of at most %d bytes",
			ErrInvalidRecord, MaxClientOperationIDBytes)
	}
	if q.Host == "" || !utf8.ValidString(q.Host) {
		return fmt.Errorf("%w: a dedup lookup needs a host name", ErrInvalidRecord)
	}
	if !q.Kind.Valid() {
		return fmt.Errorf("%w: a dedup lookup needs a deploy or restart kind", ErrInvalidRecord)
	}
	if q.Current.Generation == 0 || q.Current.IncarnationID == "" {
		return fmt.Errorf("%w: a dedup lookup needs the registry's current pair for the name", ErrInvalidRecord)
	}
	if q.Intended != nil && (q.Intended.Generation == 0 || q.Intended.IncarnationID == "") {
		return fmt.Errorf("%w: a named intended pair must be complete", ErrInvalidRecord)
	}
	return nil
}

// TerminalOperationSince reports the first operation on host whose terminal
// transition carries a state-transition sequence above sequenceBefore. It is
// §6 step 3's close-of-probe-window scan: the caller records the store's
// sequence position before the gated probe and refuses a plan whose host
// finished an operation while the probe ran. The consume write re-runs the same
// check inside its own locked read, so a transition landing between this scan
// and the write cannot slip past.
func (s *Store) TerminalOperationSince(host string, sequenceBefore uint64) (string, bool) {
	if s == nil {
		return "", false
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	return terminalOperationSinceLocked(&s.cell.state, host, sequenceBefore)
}

// EffectiveNow is the instant every deploy-path comparison reads: the later of
// the store's clock and its durable wall-clock high-water mark, so a rollback
// can never shorten a deadline or make aged facts look fresh (§3's one
// algorithm).
func (s *Store) EffectiveNow() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	return later(s.now(), wallClockMark(s.cell.state.WallClockHighWaterMark))
}

// OperationCreateRequest is the caller-supplied half of a fresh operation. For
// a deploy, TokenValue is the presented confirmation token, which the write
// consumes; for a restart there is no token — the request carries the intended
// pair and the controller boot id its record's fencing epoch is minted under.
type OperationCreateRequest struct {
	ClientOperationID string
	Host              string
	Kind              Kind
	// Pair is the operation's pinned (generation, incarnation id) pair: the
	// registry's current pair at resolution.
	Pair OperationPair
	// TokenValue is the presented confirmation token (deploy only).
	TokenValue string
	// BootID is the controller boot id restart's minted fencing epoch carries
	// (restart only; a deploy promotes the persisted probe epoch's boot id).
	BootID string
	// SequenceBefore is the state-transition sequence position the caller read
	// before its gated probe; a terminal transition above it refuses the write.
	SequenceBefore uint64
	// Dedup, when set, is the same-key/conflict lookup re-run inside the
	// write's own locked read (§4: "any dedup lookup that leads to a write —
	// holds one store-wide mutex across the read and the atomic write"). The
	// handler's pre-gate lookup is dedup-first; this one closes the window
	// between that lookup and the write, so two concurrent calls with one
	// client operation ID can never append two records.
	Dedup *OperationDedupQuery
}

// OperationCreateOutcome is what a consume-and-create (or tokenless create)
// write produced: the record, the consumed token (deploy only), and whether the
// write replayed an existing record instead of creating one.
type OperationCreateOutcome struct {
	Record Record
	Token  Token
	// Replayed reports that the in-write dedup check found an existing record:
	// nothing was created, the token was not consumed, and the record is the
	// one the caller must answer with.
	Replayed bool
}

// ConsumeTokenAndCreateOperation is §6 step 4: one atomic store write that
// re-reads the host's current token row, compare-and-consumes its nonce against
// the presented one (constant time), re-checks `expiresAt` against the store's
// clock in that same transaction, and — on a match — deletes the row and
// promotes the host's persisted probe epoch to the `pending` operation record
// carrying the worker's fencing epoch.
//
// Refusals, all with the token unconsumed and no record: a token missing,
// mismatched, superseded or expired (the same discriminator set §6 step 4
// names; an `expiresAt` at or before the effective now is expired even when the
// nonce still matches), a terminal operation above SequenceBefore
// (*ConcurrentTerminalOpError), and an unpromotable probe epoch
// (ErrProbeEpochUnavailable). The write also reaps the host's expired token
// rows and advances the durable wall-clock mark, exactly as every other token
// pass does.
//
// The returned Token is the row the write consumed: the caller's worker carries
// its bindings (target, fingerprint, running state, revision) for the
// operation's life. A RenameLanded failure still returns both — the write
// landed, so the caller must reconcile with what it returns rather than retry
// as though nothing was written.
func (s *Store) ConsumeTokenAndCreateOperation(req OperationCreateRequest) (OperationCreateOutcome, error) {
	if s == nil {
		return OperationCreateOutcome{}, errors.New("hostops: store is not configured")
	}
	if req.Kind != KindDeploy {
		return OperationCreateOutcome{}, fmt.Errorf("%w: a consume-and-create write is a deploy path", ErrInvalidRecord)
	}
	if err := validateOperationCreateRequest(req); err != nil {
		return OperationCreateOutcome{}, err
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	if outcome, replayed, err := replayDedupLocked(&s.cell.state, req.Dedup); err != nil || replayed {
		return outcome, err
	}
	next := cloneSnapshot(s.cell.state)
	now := s.now()
	mark := wallClockMark(next.WallClockHighWaterMark)
	effectiveNow := later(now, mark)

	token, passErr := classifyToken(next.Tokens, req.Host, req.TokenValue, effectiveNow, mark)
	if passErr != nil {
		// Nothing is written: the refusal reports the token it classified, with
		// the row — if the pass would have reaped one — untouched.
		return OperationCreateOutcome{}, passErr
	}
	if recordID, concurrent := terminalOperationSinceLocked(&next, req.Host, req.SequenceBefore); concurrent {
		return OperationCreateOutcome{}, &ConcurrentTerminalOpError{ID: recordID}
	}
	epoch, err := promoteProbeEpochLocked(&next, req)
	if err != nil {
		return OperationCreateOutcome{}, err
	}

	// Consume is delete in the same write that carries the reap, the mark and
	// the record (§6 step 4: "Consume is delete in that same write, never a
	// mark").
	next.Tokens = dropTokenValue(next.Tokens, token.Value)
	reapHostTokensLocked(&next, req.Host, effectiveNow, mark)
	if now.After(mark) {
		// The write observes the clock like every other pass, so a later rollback
		// can never present a rewound clock as new time.
		next.WallClockHighWaterMark = now
	}

	record, err := appendPendingRecordLocked(&next, req, epoch)
	if err != nil {
		return OperationCreateOutcome{}, err
	}
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		// Nothing was written: the refusal reports no record and leaves the
		// token, the epoch and the file exactly as they were.
		return OperationCreateOutcome{}, err
	}
	// See Create: a landed rename is this operation's durable record even when
	// the directory sync behind it failed.
	return OperationCreateOutcome{Record: cloneRecord(record), Token: cloneToken(token)}, err
}

// CreateOperation is the tokenless paths' atomic record creation (§6): a
// restart (no install step, no target, no plan to re-verify) and an
// Ensure-triggered deploy (the Ensure ladder owns its own facts and decision).
// The one write mints the record's fencing epoch from the host's durable
// per-host op-sequence counter and lands the `pending` record, so the record
// carrying its epoch exists before any remote write the operation authorizes
// (persisted-before-launch). A terminal operation above
// SequenceBefore refuses (*ConcurrentTerminalOpError) with no record, the same
// scan deploy's consume-and-create re-runs.
//
// The host's persisted probe-epoch row, when one exists (an abandoned plan's),
// is superseded in the same write: the record's own epoch is the host's current
// one from here on.
func (s *Store) CreateOperation(req OperationCreateRequest) (OperationCreateOutcome, error) {
	if s == nil {
		return OperationCreateOutcome{}, errors.New("hostops: store is not configured")
	}
	if req.Kind != KindRestart && req.Kind != KindDeploy {
		return OperationCreateOutcome{}, fmt.Errorf("%w: an atomic record creation without a token records a restart or an Ensure-triggered deploy", ErrInvalidRecord)
	}
	if err := validateOperationCreateRequest(req); err != nil {
		return OperationCreateOutcome{}, err
	}
	if strings.TrimSpace(req.BootID) == "" {
		return OperationCreateOutcome{}, fmt.Errorf("%w: a tokenless operation needs the controller boot id for its fencing epoch", ErrInvalidProbeEpoch)
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	if outcome, replayed, err := replayDedupLocked(&s.cell.state, req.Dedup); err != nil || replayed {
		return outcome, err
	}
	next := cloneSnapshot(s.cell.state)
	if recordID, concurrent := terminalOperationSinceLocked(&next, req.Host, req.SequenceBefore); concurrent {
		return OperationCreateOutcome{}, &ConcurrentTerminalOpError{ID: recordID}
	}
	if next.ProbeEpochSeq == nil {
		next.ProbeEpochSeq = map[string]uint64{}
	}
	next.ProbeEpochSeq[req.Host]++
	seq := next.ProbeEpochSeq[req.Host]
	next.ProbeEpochs = dropProbeEpochHost(next.ProbeEpochs, req.Host)

	record, err := appendPendingRecordLocked(&next, req, fencingEpochJSON(req.BootID, seq))
	if err != nil {
		return OperationCreateOutcome{}, err
	}
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		return OperationCreateOutcome{}, err
	}
	return OperationCreateOutcome{Record: cloneRecord(record)}, err
}

// replayDedupLocked runs the caller's in-write dedup check against the state
// the write is built from. The caller holds the store mutex, so the lookup and
// the write it guards see one record set: a hit returns the existing record
// with replayed true and the write must not proceed (§4's "any dedup lookup
// that leads to a write" holds one mutex across both).
func replayDedupLocked(state *snapshot, query *OperationDedupQuery) (OperationCreateOutcome, bool, error) {
	if query == nil {
		return OperationCreateOutcome{}, false, nil
	}
	record, hit, err := lookupOperationLocked(state, *query)
	switch {
	case err != nil:
		return OperationCreateOutcome{}, false, err
	case hit:
		return OperationCreateOutcome{Record: record, Replayed: true}, true, nil
	}
	return OperationCreateOutcome{}, false, nil
}

// promoteProbeEpochLocked promotes the host's persisted probe-epoch row into
// the operation's fencing epoch and deletes the row, in the caller's cloned
// state. The row must exist and be bound to the operation's pair: the handler
// persisted it for exactly this probe window, so a missing or mismatched row is
// a caller error (ErrProbeEpochUnavailable) and nothing is consumed.
func promoteProbeEpochLocked(next *snapshot, req OperationCreateRequest) (json.RawMessage, error) {
	index := slices.IndexFunc(next.ProbeEpochs, func(row ProbeEpoch) bool { return row.Host == req.Host })
	if index < 0 {
		return nil, fmt.Errorf("%w: host %q carries no probe epoch", ErrProbeEpochUnavailable, req.Host)
	}
	row := next.ProbeEpochs[index]
	if row.Generation != req.Pair.Generation || row.IncarnationID != req.Pair.IncarnationID {
		return nil, fmt.Errorf("%w: host %q's probe epoch is bound to generation %d/%s, not %d/%s",
			ErrProbeEpochUnavailable, req.Host, row.Generation, row.IncarnationID, req.Pair.Generation, req.Pair.IncarnationID)
	}
	next.ProbeEpochs = slices.Delete(next.ProbeEpochs, index, index+1)
	return fencingEpochJSON(row.BootID, row.OpSeq), nil
}

// appendPendingRecordLocked appends the fresh `pending` record req describes
// with the fencing epoch epoch, assigning the next controller id. The caller
// holds the store mutex and passes the state the write will commit.
func appendPendingRecordLocked(next *snapshot, req OperationCreateRequest, epoch json.RawMessage) (Record, error) {
	next.AllocatorHighWaterMark++
	now := nowUTC()
	record := Record{
		ID:                formatAllocatorID(next.AllocatorHighWaterMark),
		ClientOperationID: req.ClientOperationID,
		Host:              req.Host,
		Kind:              req.Kind,
		State:             StatePending,
		Generation:        req.Pair.Generation,
		IncarnationID:     req.Pair.IncarnationID,
		FencingEpoch:      epoch,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	next.Records = append(next.Records, record)
	return record, nil
}

// fencingEpochJSON renders the worker's epoch (the controller boot id plus
// per-host op sequence pair) in the record's raw field.
func fencingEpochJSON(bootID string, opSeq uint64) json.RawMessage {
	raw, err := json.Marshal(GuardEpoch{BootID: bootID, OpSeq: opSeq})
	if err != nil {
		// A struct of a string and an integer cannot fail to marshal.
		return nil
	}
	return raw
}

// validateOperationCreateRequest checks the caller-supplied half of a fresh
// operation.
func validateOperationCreateRequest(req OperationCreateRequest) error {
	if req.ClientOperationID == "" || len(req.ClientOperationID) > MaxClientOperationIDBytes || !utf8.ValidString(req.ClientOperationID) {
		return fmt.Errorf("%w: an operation needs a client operation id of at most %d bytes",
			ErrInvalidRecord, MaxClientOperationIDBytes)
	}
	if req.Host == "" || !utf8.ValidString(req.Host) {
		return fmt.Errorf("%w: an operation needs a host name", ErrInvalidRecord)
	}
	if !req.Kind.Valid() {
		return fmt.Errorf("%w: an operation needs a deploy or restart kind", ErrInvalidRecord)
	}
	if req.Pair.Generation == 0 || req.Pair.IncarnationID == "" {
		return fmt.Errorf("%w: an operation needs its pinned (generation, incarnation id) pair", ErrInvalidRecord)
	}
	return nil
}

// FencingEpochValue returns the fencing epoch a record carries as the typed
// (controller boot id, per-host op sequence) pair the store writes, so a caller
// never has to decode the record's raw field itself. ok is false for a record
// this store did not create (the field is empty), which a caller must treat as
// "no bound epoch", never as a zero one.
func (r Record) FencingEpochValue() (GuardEpoch, bool) {
	if len(r.FencingEpoch) == 0 {
		return GuardEpoch{}, false
	}
	var epoch GuardEpoch
	if err := json.Unmarshal(r.FencingEpoch, &epoch); err != nil {
		return GuardEpoch{}, false
	}
	return epoch, epoch.BootID != "" && epoch.OpSeq != 0
}

// AppendProgress appends one timestamped progress entry to the record and
// returns the updated record, trimming the oldest entries past
// MaxProgressEntries in the same write. The record's state is untouched: a
// progress line is a note on the operation, not a state move — the worker's
// state moves go through TransitionToState. A record that is not there, or that
// has already finished, refuses like every other transition.
func (s *Store) AppendProgress(id, message string) (Record, error) {
	if s == nil {
		return Record{}, errors.New("hostops: store is not configured")
	}
	if strings.TrimSpace(message) == "" {
		return Record{}, fmt.Errorf("%w: a progress entry needs a message", ErrInvalidRecord)
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	next := cloneSnapshot(s.cell.state)
	index := slices.IndexFunc(next.Records, func(record Record) bool { return record.ID == id })
	if index < 0 {
		return Record{}, fmt.Errorf("%w: %q", ErrRecordNotFound, id)
	}
	record := next.Records[index]
	if record.State.Terminal() {
		return Record{}, fmt.Errorf("%w: record %q is already %q", ErrRecordTerminal, id, record.State)
	}
	record.Progress = append(record.Progress, ProgressEntry{TS: nowUTC(), Message: message})
	if len(record.Progress) > MaxProgressEntries {
		record.Progress = record.Progress[len(record.Progress)-MaxProgressEntries:]
	}
	record.UpdatedAt = nowUTC()
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	next.Records[index] = cloneRecord(record)
	adopted, err := s.commitLocked(next)
	if err != nil && !adopted {
		return Record{}, err
	}
	return cloneRecord(record), err
}

// TransitionToState moves a record to state to with an optional progress entry
// and result — the worker's own state machine helper: `running` on launch,
// `complete`/`failed` on a terminal outcome, each in one atomic write.
func (s *Store) TransitionToState(id string, to State, result *Result, progress string) (Record, error) {
	return s.Transition(id, to, func(r *Record) {
		if progress != "" {
			r.Progress = append(r.Progress, ProgressEntry{TS: nowUTC(), Message: progress})
			if len(r.Progress) > MaxProgressEntries {
				r.Progress = r.Progress[len(r.Progress)-MaxProgressEntries:]
			}
		}
		if result != nil {
			r.Result = result
		}
	})
}

// InterruptInFlight is the controller-shutdown half of §6's worker lifetime:
// every record still `pending`/`running` transitions to `interrupted` with the
// caller's note (naming the shutdown), in one atomic write, exactly as the boot
// pass does with its crash note. A record already terminal is untouched. The
// returned count is how many records moved, for the shutdown log.
func (s *Store) InterruptInFlight(note string) (int, error) {
	if s == nil {
		return 0, errors.New("hostops: store is not configured")
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return 0, fmt.Errorf("%w: an interrupted transition needs a note naming why", ErrInvalidRecord)
	}
	return s.interruptInFlight(note)
}

// interruptInFlight runs one pass moving every pending/running record to
// `interrupted` with note, in one atomic write.
func (s *Store) interruptInFlight(note string) (int, error) {
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	next := cloneSnapshot(s.cell.state)
	moved := 0
	// One clock read for the pass: the whole pass is one atomic write, and
	// these timestamps are display-only.
	now := nowUTC()
	for i := range next.Records {
		record := &next.Records[i]
		if !record.State.InFlight() {
			continue
		}
		record.State = StateInterrupted
		record.Result = &Result{OK: false, Message: note}
		record.UpdatedAt = now
		next.advanceSequence(record)
		moved++
	}
	if moved == 0 {
		return 0, nil
	}
	landed, err := s.commitLocked(next)
	if err != nil {
		if landed {
			// The rename landed: the records are durably interrupted and memory
			// adopted that state, so the count is the truth the shutdown log
			// reports (see RenameLanded). A failure before the rename moved
			// nothing.
			return moved, err
		}
		return 0, err
	}
	return moved, nil
}
