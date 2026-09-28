package hostops

// Retention, compaction, and dedup tombstones (spec 08b §4, §11; §12's
// compaction/tombstone rows; the registry spec §15 `tombstoneRetention`
// horizon §4 cites).
//
// This file owns the durable half of §4's retention paragraph: the per-host
// and global terminal bounds with their owner-knob family and shipped
// defaults, the oldest-terminal-first compaction that runs in the same atomic
// write that lands a new terminal state, the bounded store dedup tombstone
// each compacted record leaves behind, the removed-host-first ordering past
// the `tombstoneRetention` horizon, and the durable `compactSeq` every
// compacting write advances (pagination's mid-compaction detector, §8).
//
// What this file deliberately does not own: the live `quarantineEpoch` (S8,
// persisted outside the store file), the registry's own tombstones and
// high-water pruning (the registry spec's §15 derivation, which consumes this
// store's mirrored boundaries as "mirrored store generation" referrers), and
// the cursor comparison itself (cursor.go's checkCursorCompactionLocked,
// which reads what this file persists).

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// §4's shipped defaults: "at most 50 terminal records per host ... at most
// 500 terminal records store-wide, at most 64 MiB of serialized store bytes,
// and at most 30 days of terminal-record age", "At most 50 tombstones per
// host", and the `tombstoneRetention` horizon's 7-day default (registry spec
// §15).
const (
	// DefaultTerminalPerHost is §4's per-host terminal-record bound.
	DefaultTerminalPerHost = 50
	// DefaultTerminalStoreWide is §4's global terminal-record bound.
	DefaultTerminalStoreWide = 500
	// DefaultStoreMaxBytes is §4's serialized store-byte bound.
	DefaultStoreMaxBytes int64 = 64 << 20
	// DefaultTerminalMaxAge is §4's terminal-record age bound.
	DefaultTerminalMaxAge = 30 * 24 * time.Hour
	// DefaultTombstonesPerHost is §4's per-host dedup-tombstone bound.
	DefaultTombstonesPerHost = 50
	// DefaultRemovedHostHorizon is the `tombstoneRetention` horizon §4 cites
	// from the registry spec §15: seven days.
	DefaultRemovedHostHorizon = 7 * 24 * time.Hour
)

// RetentionPolicy is §4's owner-knob family for the operation store. Each
// non-positive value takes its documented default, so a zero policy (a plain
// Open, tests, embedders) uses the shipped numbers.
type RetentionPolicy struct {
	// TerminalPerHost bounds one host's terminal records. §4 also keeps every
	// non-terminal record regardless of count.
	TerminalPerHost int
	// TerminalStoreWide bounds the store's terminal records across all hosts.
	TerminalStoreWide int
	// StoreMaxBytes bounds the serialized store file's size.
	StoreMaxBytes int64
	// TerminalMaxAge bounds a terminal record's age, measured from its last
	// update (the terminal transition).
	TerminalMaxAge time.Duration
	// TombstonesPerHost bounds one host's dedup tombstones, oldest-first past
	// the bound. A removed host inside its replay horizon keeps its tombstones:
	// the horizon is the lost-response retry contract, and only past it does a
	// replay open fresh (§4).
	TombstonesPerHost int
	// RemovedHostHorizon is the `tombstoneRetention` horizon: a removed host's
	// history is compacted first only once its removal is at least this old.
	RemovedHostHorizon time.Duration
}

// withDefaults floors every unset knob to the shipped default.
func (p RetentionPolicy) withDefaults() RetentionPolicy {
	if p.TerminalPerHost <= 0 {
		p.TerminalPerHost = DefaultTerminalPerHost
	}
	if p.TerminalStoreWide <= 0 {
		p.TerminalStoreWide = DefaultTerminalStoreWide
	}
	if p.StoreMaxBytes <= 0 {
		p.StoreMaxBytes = DefaultStoreMaxBytes
	}
	if p.TerminalMaxAge <= 0 {
		p.TerminalMaxAge = DefaultTerminalMaxAge
	}
	if p.TombstonesPerHost <= 0 {
		p.TombstonesPerHost = DefaultTombstonesPerHost
	}
	if p.RemovedHostHorizon <= 0 {
		p.RemovedHostHorizon = DefaultRemovedHostHorizon
	}
	return p
}

// Tombstone is §4's bounded store dedup tombstone: "the client operation ID
// with its (host, kind, generation, incarnation id) scope plus the full replay
// fields (controller-assigned id, bounded progress, `createdAt`/`updatedAt`,
// `hostRemoved`, terminal outcome and result, `compactedAt`)". CompactedSeq is
// the durable `compactSeq` value of the compacting write that left it — the
// value §8's `cursor-invalidated` refusal names — and is this store's own
// bookkeeping, never replayed to the wire.
type Tombstone struct {
	ID                string          `json:"id"`
	ClientOperationID string          `json:"clientOperationId"`
	Host              string          `json:"host"`
	Kind              Kind            `json:"kind"`
	Generation        uint64          `json:"generation"`
	IncarnationID     string          `json:"incarnationId"`
	State             State           `json:"state"`
	Progress          []ProgressEntry `json:"progress,omitempty"`
	Result            *Result         `json:"result,omitempty"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
	HostRemoved       bool            `json:"hostRemoved"`
	CompactedAt       time.Time       `json:"compactedAt"`
	CompactedSeq      uint64          `json:"compactedSeq"`
}

// tombstoneOf snapshots a compacted record into its tombstone. compactedSeq is
// the compacting write's durable value, shared by every tombstone the write
// leaves.
func tombstoneOf(record Record, compactedAt time.Time, compactedSeq uint64) Tombstone {
	return Tombstone{
		ID:                record.ID,
		ClientOperationID: record.ClientOperationID,
		Host:              record.Host,
		Kind:              record.Kind,
		Generation:        record.Generation,
		IncarnationID:     record.IncarnationID,
		State:             record.State,
		Progress:          slices.Clone(record.Progress),
		Result:            cloneResult(record.Result),
		CreatedAt:         record.CreatedAt,
		UpdatedAt:         record.UpdatedAt,
		HostRemoved:       record.HostRemoved,
		CompactedAt:       compactedAt,
		CompactedSeq:      compactedSeq,
	}
}

// record rebuilds the retained replay record a tombstone stands for: the full
// record with the `compacted: true` marker §4's replay returns. The rebuilt
// record is read-only (it is never written or validated): its sequence stamp
// is not among §4's replay fields.
func (t Tombstone) record() Record {
	return Record{
		ID:                t.ID,
		ClientOperationID: t.ClientOperationID,
		Host:              t.Host,
		Kind:              t.Kind,
		State:             t.State,
		Generation:        t.Generation,
		IncarnationID:     t.IncarnationID,
		Progress:          slices.Clone(t.Progress),
		Result:            cloneResult(t.Result),
		CreatedAt:         t.CreatedAt,
		UpdatedAt:         t.UpdatedAt,
		HostRemoved:       t.HostRemoved,
		Compacted:         true,
	}
}

// cloneResult copies a terminal result so a tombstone's value never aliases a
// caller's.
func cloneResult(result *Result) *Result {
	if result == nil {
		return nil
	}
	copied := *result
	return &copied
}

// validateTombstone checks one dedup tombstone against the schema the
// compacting write emits. compactSeq is the store's durable value: a tombstone
// is left by a compacting write, so it always carries a nonzero CompactedSeq
// at or below it.
func validateTombstone(tombstone Tombstone, compactSeq uint64) error {
	if tombstone.ID == "" {
		return fmt.Errorf("%w: tombstone carries no id", ErrInvalidRecord)
	}
	if _, err := parseAllocatorID(tombstone.ID); err != nil {
		return fmt.Errorf("%w: tombstone id %q is not a controller-assigned id", ErrInvalidRecord, tombstone.ID)
	}
	if tombstone.ClientOperationID == "" || len(tombstone.ClientOperationID) > MaxClientOperationIDBytes {
		return fmt.Errorf("%w: tombstone %q carries no valid client operation id", ErrInvalidRecord, tombstone.ID)
	}
	for _, text := range []struct {
		value string
		what  string
	}{
		{tombstone.ClientOperationID, "client operation id"},
		{tombstone.Host, "host"},
		{tombstone.IncarnationID, "incarnation id"},
	} {
		if !utf8.ValidString(text.value) {
			return fmt.Errorf("%w: tombstone %q %s is not valid UTF-8", ErrInvalidRecord, tombstone.ID, text.what)
		}
	}
	if tombstone.Host == "" {
		return fmt.Errorf("%w: tombstone %q names no host", ErrInvalidRecord, tombstone.ID)
	}
	if len(tombstone.IncarnationID) > MaxIncarnationIDBytes {
		return fmt.Errorf("%w: tombstone %q carries a %d-byte incarnation id, over the %d-byte bound",
			ErrInvalidRecord, tombstone.ID, len(tombstone.IncarnationID), MaxIncarnationIDBytes)
	}
	if !tombstone.Kind.Valid() {
		return fmt.Errorf("%w: tombstone %q has kind %q", ErrInvalidRecord, tombstone.ID, tombstone.Kind)
	}
	if !tombstone.State.Terminal() {
		return fmt.Errorf("%w: tombstone %q carries non-terminal state %q", ErrInvalidRecord, tombstone.ID, tombstone.State)
	}
	if tombstone.Generation == 0 || tombstone.IncarnationID == "" {
		return fmt.Errorf("%w: tombstone %q pins no (generation, incarnation id) pair", ErrInvalidRecord, tombstone.ID)
	}
	if tombstone.CreatedAt.IsZero() || tombstone.UpdatedAt.IsZero() || tombstone.CompactedAt.IsZero() {
		return fmt.Errorf("%w: tombstone %q carries no timestamps", ErrInvalidRecord, tombstone.ID)
	}
	if tombstone.CompactedSeq == 0 || tombstone.CompactedSeq > compactSeq {
		return fmt.Errorf("%w: tombstone %q carries compactedSeq %d outside the store's %d",
			ErrInvalidRecord, tombstone.ID, tombstone.CompactedSeq, compactSeq)
	}
	for _, entry := range tombstone.Progress {
		if entry.TS.IsZero() || entry.Message == "" || !utf8.ValidString(entry.Message) {
			return fmt.Errorf("%w: tombstone %q carries an invalid progress entry", ErrInvalidRecord, tombstone.ID)
		}
	}
	if tombstone.Result == nil {
		// §10: "`result` is present exactly on terminal records" — and a
		// tombstone is terminal by construction, so a replay can never lose the
		// outcome it retained.
		return fmt.Errorf("%w: terminal tombstone %q carries no terminal result", ErrInvalidRecord, tombstone.ID)
	}
	if tombstone.Result.Message == "" || !utf8.ValidString(tombstone.Result.Message) {
		return fmt.Errorf("%w: tombstone %q carries an invalid terminal result", ErrInvalidRecord, tombstone.ID)
	}
	return nil
}

// MaxCompactionMarks bounds the compaction ledger: one mark per compacting
// write, oldest dropped first. The ledger is deliberately independent of the
// dedup tombstones' own bound, because §8's refusal must not lose its evidence
// when a tombstone is evicted; beyond this many later compacting writes a
// cursor older than the whole ledger reads as the stale re-list instead, which
// still restarts the client from the first page.
const MaxCompactionMarks = 500

// CompactionMark records, for one compacting write, the smallest row id it
// removed and that row's host. A cursor pinned at `pos` was invalidated by
// this write exactly when mark.Seq is above the cursor's pinned compactSeq and
// mark.ID is at or before `pos`: some removed row sat at or before the
// cursor's position, which is §8's condition. The host names the affected host
// whose stored bounds entry the refusal carries.
type CompactionMark struct {
	Seq  uint64 `json:"seq"`
	ID   string `json:"id"`
	Host string `json:"host"`
}

// validateCompactionMark checks one compaction ledger entry.
func validateCompactionMark(mark CompactionMark, compactSeq uint64) error {
	if mark.Seq == 0 || mark.Seq > compactSeq {
		return fmt.Errorf("%w: compaction mark carries seq %d outside the store's %d", ErrInvalidRecord, mark.Seq, compactSeq)
	}
	if _, err := parseAllocatorID(mark.ID); err != nil {
		return fmt.Errorf("%w: compaction mark id %q is not a controller-assigned id", ErrInvalidRecord, mark.ID)
	}
	if err := validateBoundaryName(mark.Host); err != nil {
		return err
	}
	return nil
}

// RemovedHost is §4's removal marker for one name: when the registry reported
// the removal, and the (generation, incarnation id) pair the removal tombstoned.
// The pair is the comparison pair a tombstone replay uses for a removed host —
// "the tombstone's own removed pair for a removed host (which has no live
// current pair)" — so a tombstone pinned to an older removal never replays
// after a re-add/remove cycle.
type RemovedHost struct {
	RemovedAt     time.Time `json:"removedAt"`
	Generation    uint64    `json:"generation"`
	IncarnationID string    `json:"incarnationId"`
}

// validateRemovedHost checks one removal marker against the schema the mirror
// write emits.
func validateRemovedHost(name string, removed RemovedHost) error {
	if removed.RemovedAt.IsZero() {
		return fmt.Errorf("%w: removed host %q carries a zero removal instant", ErrInvalidBoundary, name)
	}
	if removed.Generation == 0 || removed.IncarnationID == "" {
		return fmt.Errorf("%w: removed host %q carries no removed (generation, incarnation id) pair", ErrInvalidBoundary, name)
	}
	if len(removed.IncarnationID) > MaxIncarnationIDBytes {
		return fmt.Errorf("%w: removed host %q carries a %d-byte incarnation id, over the %d-byte bound",
			ErrInvalidBoundary, name, len(removed.IncarnationID), MaxIncarnationIDBytes)
	}
	if !utf8.ValidString(removed.IncarnationID) {
		return fmt.Errorf("%w: removed host %q carries an incarnation id that is not valid UTF-8", ErrInvalidBoundary, name)
	}
	return nil
}

// HostMirror is one store mirror write's inputs: the boundary triples to
// upsert, the names whose boundary records to drop (a compensated add, a name
// the registry dropped mid-write), the live names whose removal markers are
// cleared, and the removed names currently carrying a removal tombstone.
//
// A name the caller cannot classify — a removed name whose tombstone the
// registry already expired — belongs to neither Live nor Removed: its existing
// marker is left untouched, so the store never invents a removal it cannot
// date and never forgets one it knew.
type HostMirror struct {
	Boundaries map[string]Boundary
	Remove     []string
	Live       []string
	Removed    map[string]RemovedHost
}

// Tombstones returns copies of every retained dedup tombstone, in stored
// order.
func (s *Store) Tombstones() []Tombstone {
	if s == nil {
		return nil
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	out := make([]Tombstone, len(s.cell.state.Tombstones))
	for i, tombstone := range s.cell.state.Tombstones {
		out[i] = cloneTombstone(tombstone)
	}
	return out
}

// CompactSeq returns the durable compaction sequence: the value every
// compacting write advances and every minted cursor pins (§4, §8).
func (s *Store) CompactSeq() uint64 {
	if s == nil {
		return 0
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	return s.cell.state.CompactSeq
}

// RemovedHosts returns copies of the store's removal markers: the names the
// registry has reported removed, with their removal instants and the removed
// pair the replay comparison uses. §4's removed-host ordering reads them; the
// hub test surface reads them too.
func (s *Store) RemovedHosts() map[string]RemovedHost {
	if s == nil {
		return nil
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	return maps.Clone(s.cell.state.RemovedHosts)
}

// cloneTombstone copies a tombstone deeply enough that a caller cannot reach
// the store's in-memory value.
func cloneTombstone(tombstone Tombstone) Tombstone {
	out := tombstone
	out.Progress = slices.Clone(tombstone.Progress)
	out.Result = cloneResult(tombstone.Result)
	return out
}

// MirrorHostState persists one mirror write's boundaries and removal markers
// in one atomic store write, the same discipline MirrorBoundaries uses. It is
// the hub's path for forwarding the registry's live names and removal
// tombstones into the store §4's removed-host ordering reads.
func (s *Store) MirrorHostState(mirror HostMirror) error {
	if s == nil {
		return errors.New("hostops: store is not configured")
	}
	for name, boundary := range mirror.Boundaries {
		if err := validateBoundary(name, boundary); err != nil {
			return err
		}
	}
	for _, name := range mirror.Remove {
		if err := validateBoundaryName(name); err != nil {
			return err
		}
	}
	for _, name := range mirror.Live {
		if err := validateBoundaryName(name); err != nil {
			return err
		}
	}
	for name, removed := range mirror.Removed {
		if err := validateBoundaryName(name); err != nil {
			return err
		}
		if err := validateRemovedHost(name, removed); err != nil {
			return err
		}
		if _, live := mirrorLiveSet(mirror.Live)[name]; live {
			return fmt.Errorf("%w: host %q is both live and removed in one mirror write", ErrInvalidBoundary, name)
		}
	}

	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	next := cloneSnapshot(s.cell.state)
	changed := false
	if len(mirror.Boundaries) > 0 && next.Boundaries == nil {
		next.Boundaries = make(map[string]Boundary, len(mirror.Boundaries))
	}
	for name, boundary := range mirror.Boundaries {
		if existing, ok := next.Boundaries[name]; ok && existing == boundary {
			continue
		}
		next.Boundaries[name] = boundary
		changed = true
	}
	for _, name := range mirror.Remove {
		if _, ok := next.Boundaries[name]; ok {
			delete(next.Boundaries, name)
			changed = true
		}
	}
	for name, removed := range mirror.Removed {
		removed.RemovedAt = removed.RemovedAt.UTC()
		if existing, ok := next.RemovedHosts[name]; ok && existing == removed {
			continue
		}
		if next.RemovedHosts == nil {
			next.RemovedHosts = map[string]RemovedHost{}
		}
		next.RemovedHosts[name] = removed
		changed = true
	}
	for _, name := range mirror.Live {
		if _, ok := next.RemovedHosts[name]; ok {
			delete(next.RemovedHosts, name)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if _, err := s.commitLocked(next); err != nil {
		return err
	}
	return nil
}

// mirrorLiveSet is a small lookup helper for the one-mirror-write validation.
func mirrorLiveSet(live []string) map[string]struct{} {
	set := make(map[string]struct{}, len(live))
	for _, name := range live {
		set[name] = struct{}{}
	}
	return set
}

// compactionCandidate is one removable terminal record in victim order: the
// store's durable terminal sequence first (removed-host history before live
// history, §4), then the id as the total tie-break.
type compactionCandidate struct {
	id           string
	removedFirst bool
	sequence     uint64
}

// compactLocked applies §4's retention rules to next — the caller's cloned
// snapshot, held under the store mutex — and leaves in it exactly what the
// write will commit: the victims removed, their dedup tombstones added, the
// durable `compactSeq` advanced once, and the tombstones bounded. A pass that
// removes nothing writes nothing new.
//
// The rules, in §4's own order:
//
//   - a terminal record past `TerminalMaxAge` compacts ("at most 30 days of
//     terminal-record age");
//   - once a removed host's removal is past the `tombstoneRetention` horizon
//     its terminal records all compact ("Safe compaction of removed-host
//     history"), before any live host's and regardless of the other bounds;
//   - every host keeps at most `TerminalPerHost` terminal records and the
//     store at most `TerminalStoreWide`, oldest-terminal-first;
//   - the serialized store is kept at or below `StoreMaxBytes` by compacting
//     oldest-terminal-first until it fits (or nothing removable is left —
//     non-terminal records and tombstones are never bound victims).
//
// Terminal order is the durable state-transition sequence stamp: it is the
// order the store itself recorded for the moves into the terminal state, and
// §4 says race scans compare sequence values only. The age bound reads the
// record's `updatedAt` because age is a wall-clock property no sequence value
// carries.
func (s *Store) compactLocked(next *snapshot) {
	policy := s.retentionPolicy()
	now := s.now()

	pastHorizon := make(map[string]bool)
	for host, removed := range next.RemovedHosts {
		if !removed.RemovedAt.IsZero() && !now.Before(removed.RemovedAt.Add(policy.RemovedHostHorizon)) {
			pastHorizon[host] = true
		}
	}

	candidates := make([]compactionCandidate, 0, len(next.Records))
	byID := make(map[string]Record, len(next.Records))
	for i := range next.Records {
		record := next.Records[i]
		byID[record.ID] = record
		if !record.State.Terminal() {
			continue
		}
		candidates = append(candidates, compactionCandidate{
			id:           record.ID,
			removedFirst: pastHorizon[record.Host],
			sequence:     record.Sequence,
		})
	}
	sortCompactionCandidates(candidates)

	victim := make(map[string]bool, len(candidates))
	// The age bound and the removed-host horizon compact unconditionally; the
	// count and byte bounds then trim oldest-first until they hold.
	for _, candidate := range candidates {
		record := byID[candidate.id]
		if candidate.removedFirst || now.After(record.UpdatedAt.Add(policy.TerminalMaxAge)) {
			victim[candidate.id] = true
		}
	}
	// Per-host terminal bound: the newest TerminalPerHost survive.
	byHost := map[string][]compactionCandidate{}
	for _, candidate := range candidates {
		if victim[candidate.id] {
			continue
		}
		host := byID[candidate.id].Host
		byHost[host] = append(byHost[host], candidate)
	}
	for _, kept := range byHost {
		if len(kept) <= policy.TerminalPerHost {
			continue
		}
		// kept is oldest-first, so the overflow is its prefix.
		for _, candidate := range kept[:len(kept)-policy.TerminalPerHost] {
			victim[candidate.id] = true
		}
	}
	// Store-wide terminal bound, oldest-first across hosts.
	var surviving []compactionCandidate
	for _, candidate := range candidates {
		if !victim[candidate.id] {
			surviving = append(surviving, candidate)
		}
	}
	if len(surviving) > policy.TerminalStoreWide {
		for _, candidate := range surviving[:len(surviving)-policy.TerminalStoreWide] {
			victim[candidate.id] = true
		}
	}

	// Byte bound: compact oldest-first until the serialized store fits, one
	// victim at a time so the bound is measured against the bytes the write
	// will actually commit (tombstones included).
	s.compactionVictimsForBytes(next, candidates, victim, policy, now)

	if len(victim) == 0 {
		return
	}
	next.CompactSeq++
	compactedSeq := next.CompactSeq

	// The compaction ledger: one bounded mark per compacting write, carrying
	// the smallest row id it removed and that row's host. A cursor is
	// invalidated exactly when some compacting write after its pin removed a
	// row at or before its position — minID <= pos — so the mark survives the
	// dedup tombstones' own bound, which must not erase the evidence §8's
	// refusal needs (retention.go's own CompactSeq advances independently).
	smallestID, smallestHost := "", ""
	remaining := make([]Record, 0, len(next.Records))
	for i := range next.Records {
		record := next.Records[i]
		if victim[record.ID] {
			next.Tombstones = append(next.Tombstones, tombstoneOf(record, now, compactedSeq))
			if smallestID == "" || record.ID < smallestID {
				smallestID, smallestHost = record.ID, record.Host
			}
			continue
		}
		remaining = append(remaining, record)
	}
	next.Records = remaining
	if smallestID != "" {
		next.CompactionMarks = append(next.CompactionMarks, CompactionMark{Seq: compactedSeq, ID: smallestID, Host: smallestHost})
	}
	if len(next.CompactionMarks) > MaxCompactionMarks {
		next.CompactionMarks = next.CompactionMarks[len(next.CompactionMarks)-MaxCompactionMarks:]
	}

	// Tombstone bound: "At most 50 tombstones per host ... oldest-first past
	// the bound", with removed hosts inside their replay horizon exempt — only
	// past the horizon does a replay open fresh (§4).
	next.Tombstones = boundedTombstones(next.Tombstones, next.RemovedHosts, pastHorizon, policy)

	// §4: the historical boundary "persists in the store's per-host boundary
	// record until the host's last record compacts". The removal marker stays:
	// it is what keeps a removed host's tombstones replaying under the removed
	// pair, and it is cleared only by a live mirror write.
	for host := range pastHorizon {
		if _, ok := next.Boundaries[host]; !ok {
			continue
		}
		if slices.ContainsFunc(next.Records, func(record Record) bool { return record.Host == host }) {
			continue
		}
		delete(next.Boundaries, host)
	}
}

// sortCompactionCandidates orders candidates removed-host-first (§4: "removed
// host history compacts first once its replay horizon expires"), then by the
// durable terminal sequence, then by id.
func sortCompactionCandidates(candidates []compactionCandidate) {
	slices.SortStableFunc(candidates, func(a, b compactionCandidate) int {
		if a.removedFirst != b.removedFirst {
			if a.removedFirst {
				return -1
			}
			return 1
		}
		if a.sequence != b.sequence {
			if a.sequence < b.sequence {
				return -1
			}
			return 1
		}
		return strings.Compare(a.id, b.id)
	})
}

// compactionVictimsForBytes trims the oldest surviving terminal victims until
// the serialized snapshot fits StoreMaxBytes or no removable terminal record
// remains. It works on a scratch copy of the snapshot so a size check never
// leaks a half-applied removal; it reports whether it marked any new victim.
func (s *Store) compactionVictimsForBytes(next *snapshot, candidates []compactionCandidate, victim map[string]bool, policy RetentionPolicy, now time.Time) bool {
	size := func() int64 {
		scratch := *next
		scratch.Records = make([]Record, 0, len(next.Records))
		scratch.Tombstones = slices.Clone(next.Tombstones)
		for i := range next.Records {
			record := next.Records[i]
			if victim[record.ID] {
				// The compacting write's own value is what the tombstones will
				// carry; the exact counter does not matter for the byte size.
				scratch.Tombstones = append(scratch.Tombstones, tombstoneOf(record, now, next.CompactSeq+1))
				continue
			}
			scratch.Records = append(scratch.Records, record)
		}
		raw, err := json.Marshal(scratch)
		if err != nil {
			// A snapshot of this store's own values cannot fail to marshal; a
			// failure means no byte bound can be evaluated, so treat the form as
			// unbounded rather than looping.
			return 0
		}
		return int64(len(raw))
	}
	changed := false
	for {
		if size() <= policy.StoreMaxBytes {
			return changed
		}
		nextVictim := ""
		for _, candidate := range candidates {
			if victim[candidate.id] {
				continue
			}
			// candidates is sorted in victim order, so the first survivor is the
			// next oldest.
			nextVictim = candidate.id
			break
		}
		if nextVictim == "" {
			return changed
		}
		victim[nextVictim] = true
		changed = true
	}
}

// boundedTombstones applies the per-host tombstone bound, oldest-first, and
// returns the surviving set in its original order. A removed host inside its
// replay horizon keeps every tombstone it has; every other host (and a removed
// host past the horizon) drops oldest-first past the bound.
func boundedTombstones(tombstones []Tombstone, removedHosts map[string]RemovedHost, pastHorizon map[string]bool, policy RetentionPolicy) []Tombstone {
	byHost := map[string][]int{}
	for i, tombstone := range tombstones {
		byHost[tombstone.Host] = append(byHost[tombstone.Host], i)
	}
	drop := map[int]bool{}
	for host, indices := range byHost {
		if len(indices) <= policy.TombstonesPerHost {
			continue
		}
		if _, removed := removedHosts[host]; removed && !pastHorizon[host] {
			// Inside the replay horizon a removed host's tombstones are the
			// lost-response retry contract: nothing drops before it expires.
			continue
		}
		sort.SliceStable(indices, func(a, b int) bool {
			left, right := tombstones[indices[a]], tombstones[indices[b]]
			if !left.CompactedAt.Equal(right.CompactedAt) {
				return left.CompactedAt.Before(right.CompactedAt)
			}
			return left.ID < right.ID
		})
		for _, index := range indices[:len(indices)-policy.TombstonesPerHost] {
			drop[index] = true
		}
	}
	out := make([]Tombstone, 0, len(tombstones))
	for i, tombstone := range tombstones {
		if drop[i] {
			continue
		}
		out = append(out, tombstone)
	}
	return out
}
