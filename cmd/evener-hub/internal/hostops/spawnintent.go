package hostops

// Crash-fencing spec 08c §3's pending-spawn intent. Before a worker spawns an
// SSH subprocess it pre-creates a durable ownership boundary plus a
// server-generated per-spawn nonce, and persists the boundary identity and the
// nonce "in the operation-store file alongside the record". That durable record
// is this intent: it is armed BEFORE the spawn, so a crash between the intent
// and the spawn leaves a persisted-but-empty boundary the reap converges, never
// an invisible orphan; it is matched after the spawn with the launcher-observed
// (pid, start time) instance marker; and it drops only on a clean local reap or
// on orphan-resolve. "An intent that never got a spawn must converge (cleared by
// the reap) — never wedge a name forever."
//
// The intent is a field on the operation record it belongs to (Record.PendingSpawns),
// because §3 calls the record carrying it "the `pending-spawn` record" and §9
// keys the record's boundary variant to the persisted state. The store owns the
// intent's schema and the four lifecycle writes below; the reap that drives
// them lives with the fencing code (hostfence), and the boundary it enumerates
// lives in agent/execenv.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// MaxSpawnNonceBytes bounds a per-spawn nonce. It is 128 like the other opaque
// controller identities (§1's client mutation and operation ids): the minted
// value is far inside it, and the bound keeps a malformed record from
// inflating the store file.
const MaxSpawnNonceBytes = 128

// MaxPendingSpawnsPerRecord bounds one record's open intent set. A worker's
// operation spawns a handful of SSH subprocesses at most; the bound keeps a
// malformed record from growing the store without limit.
const MaxPendingSpawnsPerRecord = 64

// SpawnPlatform discriminates the pre-spawn boundary arm a SpawnIntent holds.
// The literals are §9's local-linux / local-darwin discriminators, and the
// agent/execenv boundary identity uses the same spellings.
type SpawnPlatform string

const (
	// SpawnPlatformLinux is the cgroup arm: CgroupID is the boundary.
	SpawnPlatformLinux SpawnPlatform = "linux"
	// SpawnPlatformDarwin is the (pgid, session id) arm.
	SpawnPlatformDarwin SpawnPlatform = "darwin"
)

// ErrInvalidSpawnIntent reports a pending-spawn intent outside the schema the
// reap consumes: an empty or oversized or non-UTF-8 nonce, an unknown or
// mixed-arm boundary, a half-persisted launcher marker, or a duplicated nonce.
var ErrInvalidSpawnIntent = errors.New("hostops: invalid spawn intent")

// SpawnIntent is §3's `pending-spawn` intent: the pre-spawn boundary identity
// plus the per-spawn nonce, extended after the spawn with the launcher-observed
// kernel instance marker.
//
// PID and StartTime are a pair: both absent means the crash landed between the
// pre-spawn persist and the marker persist, which §3 reads as a markerless
// boundary (unverifiable but for emptiness); both present means the launcher
// observed the exact process instance. One without the other is a value no
// writer emits.
type SpawnIntent struct {
	// Nonce is the server-generated per-spawn nonce (§3).
	Nonce string `json:"nonce"`
	// Platform selects the boundary arm.
	Platform SpawnPlatform `json:"platform"`
	// CgroupID is the Linux arm's cgroup v2 directory path.
	CgroupID string `json:"cgroupId,omitempty"`
	// PGID and SessionID are the Darwin arm's boundary pair.
	PGID      *int `json:"pgid,omitempty"`
	SessionID *int `json:"sessionId,omitempty"`
	// PID is the launcher-observed child pid, absent until the post-spawn
	// marker persist.
	PID *int `json:"pid,omitempty"`
	// StartTime is the child's kernel-owned start token observed beside PID.
	StartTime string `json:"startTime,omitempty"`
}

// ValidMarker reports whether the intent carries the launcher-observed marker
// pair. A false result is §3's markerless variant.
func (intent SpawnIntent) ValidMarker() bool {
	return intent.PID != nil && intent.StartTime != ""
}

// validateSpawnIntent checks one intent against the schema: a named nonce, one
// boundary arm with exactly that arm's fields, and a launcher marker that is
// either wholly present or wholly absent.
func validateSpawnIntent(intent SpawnIntent) error {
	if intent.Nonce == "" {
		return fmt.Errorf("%w: an intent carries no nonce", ErrInvalidSpawnIntent)
	}
	if len(intent.Nonce) > MaxSpawnNonceBytes {
		return fmt.Errorf("%w: nonce is %d bytes, over the %d-byte bound", ErrInvalidSpawnIntent, len(intent.Nonce), MaxSpawnNonceBytes)
	}
	if !utf8.ValidString(intent.Nonce) {
		return fmt.Errorf("%w: nonce is not valid UTF-8", ErrInvalidSpawnIntent)
	}
	switch intent.Platform {
	case SpawnPlatformLinux:
		if intent.CgroupID == "" {
			return fmt.Errorf("%w: nonce %q is a linux intent with no cgroup id", ErrInvalidSpawnIntent, intent.Nonce)
		}
		if intent.PGID != nil || intent.SessionID != nil {
			return fmt.Errorf("%w: nonce %q is a linux intent carrying a darwin pair", ErrInvalidSpawnIntent, intent.Nonce)
		}
	case SpawnPlatformDarwin:
		if intent.PGID == nil || intent.SessionID == nil || *intent.PGID <= 0 || *intent.SessionID <= 0 {
			return fmt.Errorf("%w: nonce %q is a darwin intent with no (pgid, session id) pair", ErrInvalidSpawnIntent, intent.Nonce)
		}
		if intent.CgroupID != "" {
			return fmt.Errorf("%w: nonce %q is a darwin intent carrying a cgroup id", ErrInvalidSpawnIntent, intent.Nonce)
		}
	default:
		return fmt.Errorf("%w: nonce %q carries platform %q", ErrInvalidSpawnIntent, intent.Nonce, intent.Platform)
	}
	if (intent.PID == nil) != (intent.StartTime == "") {
		return fmt.Errorf("%w: nonce %q carries half a launcher marker", ErrInvalidSpawnIntent, intent.Nonce)
	}
	if intent.PID != nil && *intent.PID <= 0 {
		return fmt.Errorf("%w: nonce %q carries pid %d", ErrInvalidSpawnIntent, intent.Nonce, *intent.PID)
	}
	return nil
}

// cloneSpawnIntents copies the intent set deeply: the pid and pair pointers are
// what a caller holding a copy could otherwise mutate into store state.
func cloneSpawnIntents(intents []SpawnIntent) []SpawnIntent {
	if intents == nil {
		return nil
	}
	out := make([]SpawnIntent, len(intents))
	for i, intent := range intents {
		out[i] = cloneSpawnIntent(intent)
	}
	return out
}

// cloneSpawnIntent copies one intent, keeping the nonce string and deep-copying
// every pointer field.
func cloneSpawnIntent(intent SpawnIntent) SpawnIntent {
	out := intent
	if intent.PID != nil {
		pid := *intent.PID
		out.PID = &pid
	}
	if intent.PGID != nil {
		pgid := *intent.PGID
		out.PGID = &pgid
	}
	if intent.SessionID != nil {
		session := *intent.SessionID
		out.SessionID = &session
	}
	return out
}

// NewSpawnNonce mints one server-generated per-spawn nonce: 128 bits of
// crypto/rand, hex-rendered. §3 requires the nonce be server-generated, and the
// mint refuses on an entropy failure rather than reuse a value.
func NewSpawnNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("hostops: mint a spawn nonce: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// ArmSpawnIntent persists one pre-spawn intent on recordID, in its own store
// write, BEFORE the caller spawns. The write is what makes the boundary
// reapable: a crash between this write and the spawn leaves a persisted-but-empty
// boundary, and a crash after the spawn but before MatchSpawnIntent leaves a
// markerless intent — both converge at the next boot's local reap, never as an
// invisible orphan.
//
// A terminal record refuses the arm: a finished operation starts no new
// subprocess. A duplicate nonce refuses: it would name two boundaries with one
// identity.
func (s *Store) ArmSpawnIntent(recordID string, intent SpawnIntent) (Record, error) {
	if s == nil {
		return Record{}, errors.New("hostops: store is not configured")
	}
	if err := validateSpawnIntent(intent); err != nil {
		return Record{}, err
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
		return Record{}, fmt.Errorf("%w: record %q is terminal and starts no spawn", ErrInvalidRecord, recordID)
	}
	if len(record.PendingSpawns) >= MaxPendingSpawnsPerRecord {
		return Record{}, fmt.Errorf("%w: record %q already carries %d open intents", ErrInvalidRecord, recordID, len(record.PendingSpawns))
	}
	for _, existing := range record.PendingSpawns {
		if existing.Nonce == intent.Nonce {
			return Record{}, fmt.Errorf("%w: record %q already carries nonce %q", ErrInvalidSpawnIntent, recordID, intent.Nonce)
		}
	}
	record.PendingSpawns = append(record.PendingSpawns, cloneSpawnIntent(intent))
	record.UpdatedAt = nowUTC()
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	next.Records[index] = cloneRecord(record)
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		return Record{}, err
	}
	return cloneRecord(record), err
}

// MatchSpawnIntent persists the launcher-observed (pid, start token) marker on
// an armed intent, in its own store write, right after the spawn. The marker is
// the instance proof §3 requires before any signal; without it the intent stays
// markerless and unverifiable but for emptiness.
//
// The match is idempotent for the same pair — a retried post-spawn persist
// rewrites nothing — and refuses a different pair on an already-matched intent:
// a second marker would let the persisted instance proof disagree with the
// process the launcher actually saw.
func (s *Store) MatchSpawnIntent(recordID, nonce string, pid int, startTime string) (Record, error) {
	if s == nil {
		return Record{}, errors.New("hostops: store is not configured")
	}
	if nonce == "" || pid <= 0 || startTime == "" {
		return Record{}, fmt.Errorf("%w: a launcher marker names nonce, pid and start token", ErrInvalidSpawnIntent)
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	next := cloneSnapshot(s.cell.state)
	index := slices.IndexFunc(next.Records, func(record Record) bool { return record.ID == recordID })
	if index < 0 {
		return Record{}, fmt.Errorf("%w: %q", ErrRecordNotFound, recordID)
	}
	record := next.Records[index]
	intentIndex := slices.IndexFunc(record.PendingSpawns, func(intent SpawnIntent) bool { return intent.Nonce == nonce })
	if intentIndex < 0 {
		return Record{}, fmt.Errorf("%w: record %q carries no intent with nonce %q", ErrInvalidSpawnIntent, recordID, nonce)
	}
	if record.PendingSpawns[intentIndex].ValidMarker() {
		if record.PendingSpawns[intentIndex].PID != nil && *record.PendingSpawns[intentIndex].PID == pid && record.PendingSpawns[intentIndex].StartTime == startTime {
			// The same marker already landed: a retried persist, not a rewrite.
			return cloneRecord(record), nil
		}
		return Record{}, fmt.Errorf("%w: record %q intent %q already carries a different marker", ErrInvalidSpawnIntent, recordID, nonce)
	}
	markerPID := pid
	record.PendingSpawns[intentIndex].PID = &markerPID
	record.PendingSpawns[intentIndex].StartTime = startTime
	record.UpdatedAt = nowUTC()
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	next.Records[index] = cloneRecord(record)
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		return Record{}, err
	}
	return cloneRecord(record), err
}

// DropSpawnIntent is the clean-convergence write: it removes one open intent
// with no other change. The reap drops an intent once its boundary is proven
// clean; orphan-resolve drops it once it clears the record. A record that is
// gone, or an intent that was already dropped, is a no-op: the write is
// idempotent so a crash between the drop and its caller's next step converges
// on the retry.
func (s *Store) DropSpawnIntent(recordID, nonce string) error {
	if s == nil {
		return errors.New("hostops: store is not configured")
	}
	if nonce == "" {
		return fmt.Errorf("%w: a drop names an intent's nonce", ErrInvalidSpawnIntent)
	}
	return s.ClearSpawnIntents(recordID, []string{nonce})
}

// ClearSpawnIntents removes every named open intent from recordID in one atomic
// write — the reap's clean-convergence step for a record whose several
// spawned boundaries all proved clean. Unknown nonces and a record that is gone
// are no-ops, exactly as the single drop is: the write stays idempotent so a
// crash between it and its caller's next step converges on the retry.
func (s *Store) ClearSpawnIntents(recordID string, nonces []string) error {
	if s == nil {
		return errors.New("hostops: store is not configured")
	}
	if slices.Contains(nonces, "") {
		return fmt.Errorf("%w: a clear names an intent's nonce", ErrInvalidSpawnIntent)
	}
	if len(nonces) == 0 {
		return nil
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	next := cloneSnapshot(s.cell.state)
	index := slices.IndexFunc(next.Records, func(record Record) bool { return record.ID == recordID })
	if index < 0 {
		return nil
	}
	record := next.Records[index]
	kept := make([]SpawnIntent, 0, len(record.PendingSpawns))
	removed := 0
	for _, intent := range record.PendingSpawns {
		if slices.Contains(nonces, intent.Nonce) {
			removed++
			continue
		}
		kept = append(kept, intent)
	}
	if removed == 0 {
		return nil
	}
	record.PendingSpawns = kept
	record.UpdatedAt = nowUTC()
	if err := validateRecord(record); err != nil {
		return err
	}
	next.Records[index] = cloneRecord(record)
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		return err
	}
	return err
}

// SpawnIntentRecords returns copies of every record carrying at least one open
// pending-spawn intent, in record-id order. It is the boot reap's read: §7 runs
// the local reap over exactly these records before the interrupted transition.
func (s *Store) SpawnIntentRecords() []Record {
	if s == nil {
		return nil
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	var records []Record
	for _, record := range s.cell.state.Records {
		if len(record.PendingSpawns) == 0 {
			continue
		}
		records = append(records, cloneRecord(record))
	}
	slices.SortFunc(records, func(a, b Record) int { return strings.Compare(a.ID, b.ID) })
	return records
}

// ResolveReapedSpawn is §3/§5's clean resolution in one atomic write: it drops
// every named open intent AND moves the record from `orphan-unverified` to
// `interrupted`, clearing the boundary and writing the interrupted outcome,
// with the durable sequence advanced once. One write is what §5 requires of the
// resolution ("under the store mutex in one atomic write"), and it is also what
// keeps the store's invariant: a terminal record never carries a pending-spawn
// intent, so no later boot can find an interrupted record whose boundary is
// still unaccounted for.
//
// Every open intent on the record must be named. A record with an intent left
// unnamed refuses: resolving half a boundary would record a terminal outcome
// while an unverified spawn's intent still names live-or-unknown work. A record
// not in `orphan-unverified` refuses; the fencing paths, not a stray caller,
// own that state.
func (s *Store) ResolveReapedSpawn(recordID string, nonces []string) (Record, error) {
	if s == nil {
		return Record{}, errors.New("hostops: store is not configured")
	}
	if slices.Contains(nonces, "") {
		return Record{}, fmt.Errorf("%w: a resolve names an intent's nonce", ErrInvalidSpawnIntent)
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	next := cloneSnapshot(s.cell.state)
	index := slices.IndexFunc(next.Records, func(record Record) bool { return record.ID == recordID })
	if index < 0 {
		return Record{}, fmt.Errorf("%w: %q", ErrRecordNotFound, recordID)
	}
	record := next.Records[index]
	if record.State != StateOrphanUnverified {
		return Record{}, fmt.Errorf("%w: record %q is %q, not %q", ErrInvalidTransition, recordID, record.State, StateOrphanUnverified)
	}
	kept := make([]SpawnIntent, 0, len(record.PendingSpawns))
	for _, intent := range record.PendingSpawns {
		if slices.Contains(nonces, intent.Nonce) {
			continue
		}
		kept = append(kept, intent)
	}
	if len(kept) > 0 {
		return Record{}, fmt.Errorf("%w: record %q still carries %d unnamed open intent(s)", ErrInvalidTransition, recordID, len(kept))
	}
	record.PendingSpawns = nil
	record.State = StateInterrupted
	record.OrphanBoundary = nil
	record.Result = &Result{OK: false, Message: InterruptedNote}
	record.UpdatedAt = nowUTC()
	next.advanceSequence(&record)
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	next.Records[index] = cloneRecord(record)
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		return Record{}, err
	}
	return cloneRecord(record), err
}

// SetOrphanBoundary persists a fresh `orphan-unverified` boundary on recordID
// and drops the named open intents, all in one atomic write. It is the local
// reap's fail-closed write, and it exists because Store.Transition refuses a
// same-state move: a record already in `orphan-unverified` whose boundary must
// be narrowed (one spawned group cleared while another still held) cannot go
// through Transition. It also covers the pending/running→orphan-unverified
// move, so the reap has one writer for both arms.
//
// `orphan-unverified` is not terminal, so the durable sequence does not move.
// A terminal record refuses: it cannot be marked. An empty boundary refuses:
// this write only ever records an unverified boundary, never a verified-empty
// one, which is what ResolveReapedSpawn is for.
func (s *Store) SetOrphanBoundary(recordID string, boundary json.RawMessage, dropNonces []string) (Record, error) {
	if s == nil {
		return Record{}, errors.New("hostops: store is not configured")
	}
	if slices.Contains(dropNonces, "") {
		return Record{}, fmt.Errorf("%w: a set names an intent's nonce", ErrInvalidSpawnIntent)
	}
	// The boundary is parsed, not string-matched: `[ ]`, null and any other
	// zero-entry form all unmarshal to an empty array, and an empty boundary
	// must never be written here — a mark with no entry reads as "verified
	// empty" to the reap, which is the opposite of the fail-closed disposition
	// this write exists to record.
	var entries []json.RawMessage
	if err := json.Unmarshal(boundary, &entries); err != nil || len(entries) == 0 {
		return Record{}, fmt.Errorf("%w: an orphan boundary is never empty here", ErrInvalidRecord)
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
		return Record{}, fmt.Errorf("%w: terminal record %q cannot be marked orphan-unverified", ErrInvalidTransition, recordID)
	}
	kept := make([]SpawnIntent, 0, len(record.PendingSpawns))
	for _, intent := range record.PendingSpawns {
		if slices.Contains(dropNonces, intent.Nonce) {
			continue
		}
		kept = append(kept, intent)
	}
	record.PendingSpawns = kept
	record.State = StateOrphanUnverified
	record.OrphanBoundary = append(json.RawMessage(nil), boundary...)
	record.UpdatedAt = nowUTC()
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	next.Records[index] = cloneRecord(record)
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		return Record{}, err
	}
	return cloneRecord(record), err
}

// OrphanUnverified returns copies of every open `orphan-unverified` record, in
// record-id order. It is the read the admission fence and orphan-resolve stand
// on: while a host has a record here, §8 admits no new lifecycle or mutation
// call for that name, and the record's persisted `OrphanBoundary` is what
// orphan-resolve enumerates.
func (s *Store) OrphanUnverified() []Record {
	if s == nil {
		return nil
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	var records []Record
	for _, record := range s.cell.state.Records {
		if record.State != StateOrphanUnverified {
			continue
		}
		records = append(records, cloneRecord(record))
	}
	slices.SortFunc(records, func(a, b Record) int { return strings.Compare(a.ID, b.ID) })
	return records
}
