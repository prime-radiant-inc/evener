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
	if len(tombstone.Host) > MaxHostNameBytes {
		return fmt.Errorf("%w: tombstone %q carries a %d-byte host name, over the %d-byte bound",
			ErrInvalidRecord, tombstone.ID, len(tombstone.Host), MaxHostNameBytes)
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
		if len(entry.Message) > MaxOperationMessageBytes {
			return fmt.Errorf("%w: tombstone %q carries a %d-byte progress message, over the %d-byte bound",
				ErrInvalidRecord, tombstone.ID, len(entry.Message), MaxOperationMessageBytes)
		}
	}
	if len(tombstone.Progress) > MaxProgressEntries {
		return fmt.Errorf("%w: tombstone %q carries %d progress entries, over the %d-entry bound",
			ErrInvalidRecord, tombstone.ID, len(tombstone.Progress), MaxProgressEntries)
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
	if len(tombstone.Result.Message) > MaxOperationMessageBytes {
		return fmt.Errorf("%w: tombstone %q carries a %d-byte terminal result message, over the %d-byte bound",
			ErrInvalidRecord, tombstone.ID, len(tombstone.Result.Message), MaxOperationMessageBytes)
	}
	return nil
}

// MaxCompactionMarks bounds the compaction ledger: one mark per compacting
// write, oldest dropped first, with the highest dropped write's sequence
// persisted as the dropped-marks floor (CompactionFloor) so §8's check can
// tell exact coverage from lost evidence. The ledger is deliberately
// independent of the dedup tombstones' own bound, because the refusal must not
// lose its evidence when a tombstone is evicted; beyond this many later
// compacting writes a cursor older than the whole ledger refuses coarsely
// instead, which still restarts the client from the first page.
const MaxCompactionMarks = 500

// CompactionMark records, for one compacting write, every host whose rows it
// removed and that host's smallest removed row id. A cursor pinned at `pos`
// was invalidated by this write exactly when mark.Seq is above the cursor's
// pinned compactSeq and some entry names a row at or before `pos` in the
// cursor's window: that row sat at or before the cursor's position, which is
// §8's condition, and the named host is the affected host whose stored bounds
// entry the refusal carries. A single write routinely removes rows on several
// hosts (age, removed-host horizon, byte bound), so the evidence is per host —
// a cursor window on any affected host must see its own removal.
type CompactionMark struct {
	Seq   uint64            `json:"seq"`
	Hosts map[string]string `json:"hosts"`
}

// validateCompactionMark checks one compaction ledger entry.
func validateCompactionMark(mark CompactionMark, compactSeq uint64) error {
	if mark.Seq == 0 || mark.Seq > compactSeq {
		return fmt.Errorf("%w: compaction mark carries seq %d outside the store's %d", ErrInvalidRecord, mark.Seq, compactSeq)
	}
	if len(mark.Hosts) == 0 {
		return fmt.Errorf("%w: compaction mark carries no affected hosts", ErrInvalidRecord)
	}
	for host, id := range mark.Hosts {
		if err := validateBoundaryName(host); err != nil {
			return err
		}
		if _, err := parseAllocatorID(id); err != nil {
			return fmt.Errorf("%w: compaction mark id %q is not a controller-assigned id", ErrInvalidRecord, id)
		}
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

	now := s.now()
	policy := s.retentionPolicy()
	next := cloneSnapshot(s.cell.state)
	changed := false
	if len(mirror.Boundaries) > 0 && next.Boundaries == nil {
		next.Boundaries = make(map[string]Boundary, len(mirror.Boundaries))
	}
	for name, boundary := range mirror.Boundaries {
		if existing, ok := next.Boundaries[name]; ok && existing == boundary {
			continue
		}
		if s.skipBoundaryProposal(&next, name, mirror, now, policy) {
			// The name is a removed host whose history is gone: re-proposing
			// the boundary would commit a write that §4's own rule discards
			// with the host's last record, once per hub.toml mutation.
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
		if existing, ok := next.RemovedHosts[name]; ok &&
			existing.Generation == removed.Generation &&
			existing.IncarnationID == removed.IncarnationID &&
			existing.RemovedAt.Equal(removed.RemovedAt) {
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
// skipBoundaryProposal reports whether a boundary the caller proposes for name
// would be discarded again by §4's own rule: the name's removal marker agrees
// with the store's data (the host is genuinely removed), its removal is past
// the tombstoneRetention horizon, and it holds no records — the state in which
// the historical boundary is dropped with the host's last record, whatever
// evidence of that history survives. Re-proposing it commits a write that
// changes nothing (and, when a later write carries unrelated victims, flips the
// boundary in and out across mutations), so it is skipped. A name with records,
// or inside its horizon, still mirrors normally; the marker itself is not
// affected.
func (s *Store) skipBoundaryProposal(state *snapshot, name string, mirror HostMirror, now time.Time, policy RetentionPolicy) bool {
	marker, marked := mirror.Removed[name]
	if !marked {
		marker, marked = state.RemovedHosts[name]
	}
	if !marked || now.Before(marker.RemovedAt.Add(policy.RemovedHostHorizon)) {
		return false
	}
	if !removedMarkerAgrees(state, name, marker) {
		return false
	}
	return !slices.ContainsFunc(state.Records, func(record Record) bool { return record.Host == name })
}

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

	// Removed-host history is consumed only while the store's own data still
	// agrees with the marker: a re-add whose clear mirror write trailed leaves
	// the marker stale, and treating that live host as removed would compact
	// its history unconditionally and drop its boundary. The agreement check is
	// the store-side reconciliation; the marker refreshes on the next
	// successful mirror write.
	removed := make(map[string]RemovedHost)
	for host, marker := range next.RemovedHosts {
		if removedMarkerAgrees(next, host, marker) {
			removed[host] = marker
		}
	}
	pastHorizon := make(map[string]bool)
	for host, marker := range removed {
		if !now.Before(marker.RemovedAt.Add(policy.RemovedHostHorizon)) {
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
	// candidate at a time, with the incremental per-candidate estimator and a
	// bounded number of full-store measurements, so the bound is held against
	// the exact bytes the write will commit — the ledger marks, the advanced
	// compactSeq and the writer's own normalizations included — without a full
	// marshal per removed row inside the store mutex.
	s.compactionVictimsForBytes(next, candidates, byID, victim, pastHorizon, removed, policy, now)

	if len(victim) == 0 {
		return
	}
	*next = s.applyCompaction(*next, victim, pastHorizon, removed, policy, now)
}

// removedMarkerAgrees reports whether the store's own data still describes host
// as the removal the marker names: the mirrored boundary, when present, and
// every retained record for the host must carry the marker's pair. A pair from
// any other incarnation is live evidence — the name was re-added after the
// removal — so the host is compacted as live history, never as removed
// history, and never has its boundary dropped as removed.
func removedMarkerAgrees(state *snapshot, host string, marker RemovedHost) bool {
	if boundary, ok := state.Boundaries[host]; ok {
		if boundary.Generation != marker.Generation || boundary.IncarnationID != marker.IncarnationID {
			return false
		}
	}
	for i := range state.Records {
		record := state.Records[i]
		if record.Host != host {
			continue
		}
		if record.Generation != marker.Generation || record.IncarnationID != marker.IncarnationID {
			return false
		}
	}
	return true
}

// applyCompaction returns the exact state the compacting write commits for the
// victim set: the victims removed, one tombstone per victim, the durable
// compactSeq advanced once, one ledger mark per affected host, the tombstone
// bound applied, and a removed host's historical boundary dropped with its
// last record. The caller's snapshot is not mutated, so the byte bound can
// measure this same transformation on a scratch copy.
func (s *Store) applyCompaction(state snapshot, victim map[string]bool, pastHorizon map[string]bool, removed map[string]RemovedHost, policy RetentionPolicy, now time.Time) snapshot {
	if len(victim) > 0 {
		state.CompactSeq++
	}
	compactedSeq := state.CompactSeq
	state.Tombstones = slices.Clone(state.Tombstones)
	state.CompactionMarks = slices.Clone(state.CompactionMarks)
	state.Boundaries = maps.Clone(state.Boundaries)

	perHostSmallest := map[string]string{}
	remaining := make([]Record, 0, len(state.Records))
	for i := range state.Records {
		record := state.Records[i]
		if victim[record.ID] {
			state.Tombstones = append(state.Tombstones, tombstoneOf(record, now, compactedSeq))
			if smallest, ok := perHostSmallest[record.Host]; !ok || record.ID < smallest {
				perHostSmallest[record.Host] = record.ID
			}
			continue
		}
		remaining = append(remaining, record)
	}
	state.Records = remaining
	if len(perHostSmallest) > 0 {
		state.CompactionMarks = append(state.CompactionMarks, CompactionMark{Seq: compactedSeq, Hosts: perHostSmallest})
	}
	if len(state.CompactionMarks) > MaxCompactionMarks {
		dropped := state.CompactionMarks[:len(state.CompactionMarks)-MaxCompactionMarks]
		for _, mark := range dropped {
			if mark.Seq > state.CompactionFloor {
				state.CompactionFloor = mark.Seq
			}
		}
		state.CompactionMarks = state.CompactionMarks[len(state.CompactionMarks)-MaxCompactionMarks:]
	}

	// Tombstone bound: "At most 50 tombstones per host ... oldest-first past
	// the bound", with removed hosts inside their replay horizon exempt — only
	// past the horizon does a replay open fresh (§4).
	state.Tombstones = boundedTombstones(state.Tombstones, removed, pastHorizon, policy)

	// §4: the historical boundary "persists in the store's per-host boundary
	// record until the host's last record compacts". The removal marker stays:
	// it is what keeps a removed host's tombstones replaying under the removed
	// pair, and it is cleared only by a live mirror write.
	for host := range pastHorizon {
		if _, ok := state.Boundaries[host]; !ok {
			continue
		}
		if slices.ContainsFunc(state.Records, func(record Record) bool { return record.Host == host }) {
			continue
		}
		delete(state.Boundaries, host)
	}
	normalizeSnapshotCollections(&state)
	return state
}

// normalizeSnapshotCollections mirrors saveFS's normalization of the
// collection fields, so a byte measurement taken before the write matches the
// bytes saveFS serializes.
func normalizeSnapshotCollections(state *snapshot) {
	if state.Boundaries == nil {
		state.Boundaries = map[string]Boundary{}
	}
	if state.Tokens == nil {
		state.Tokens = []Token{}
	}
	if state.Tombstones == nil {
		state.Tombstones = []Tombstone{}
	}
	if state.CompactionMarks == nil {
		state.CompactionMarks = []CompactionMark{}
	}
	if state.RemovedHosts == nil {
		state.RemovedHosts = map[string]RemovedHost{}
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

// compactionMeasureHook, when set, observes one full-store byte measurement of
// the byte-bound loop: the tests use it to prove the measurement count stays
// bounded rather than one marshal per removed row. Nil in production.
var compactionMeasureHook func()

// maxByteMeasurements bounds the full-store byte measurements one byte-bound
// victim search may take. A package variable so a test can exhaust the budget
// on a small store.
var maxByteMeasurements = 64

// compactionVictimsForBytes trims the oldest surviving terminal victims until
// the exact post-compaction snapshot fits StoreMaxBytes or no removable
// terminal record remains. It measures the same transformation the write will
// commit — ledger marks, the advanced compactSeq and the writer's
// normalizations included — but does so incrementally: each candidate's record
// and tombstone bytes are measured once, and a full-store marshal happens only
// near the boundary (bounded by maxByteMeasurements), so a store whose bulk is
// many records cannot force one full marshal per removed row. It reports
// whether it marked any new victim.
//
// The per-candidate delta is a deliberate upper bound of the bytes the write
// gains: the removed record's bytes are credited without its JSON separator,
// the tombstone's are charged with slack, and each newly affected host's
// ledger entry is charged from its actual marshaled form plus a scaffold
// margin. So whenever `estimated` is at or below the cap, the committed size
// is too — the exact checks exist to give victims back and tighten the fit,
// and the budget-exhausted path can never return an over-cap write.
func (s *Store) compactionVictimsForBytes(next *snapshot, candidates []compactionCandidate, byID map[string]Record, victim map[string]bool, pastHorizon map[string]bool, removed map[string]RemovedHost, policy RetentionPolicy, now time.Time) bool {
	exactSize := func() int64 {
		if compactionMeasureHook != nil {
			compactionMeasureHook()
		}
		scratch := s.applyCompaction(*next, victim, pastHorizon, removed, policy, now)
		raw, err := json.Marshal(scratch)
		if err != nil {
			// A snapshot of this store's own values cannot fail to marshal; a
			// failure means no byte bound can be evaluated, so treat the form as
			// unbounded rather than looping.
			return 0
		}
		return int64(len(raw))
	}
	measurements := 1
	if len(candidates) == 0 {
		// No terminal record can be a victim: skip the baseline measurement
		// entirely (this runs on every commit that lands no terminal state).
		return false
	}
	estimated := exactSize() // one exact baseline, then per-candidate deltas
	if estimated <= policy.StoreMaxBytes {
		// The store already fits: nothing to remove. Without this, the walk
		// below would charge each host's first ledger entry, cross the cap and
		// — because the estimate only falls again if tombstones are smaller
		// than their records — mark every terminal candidate as a victim.
		return false
	}

	changed := false
	finalChecked := false
	var added []string
	hostsMarked := map[string]bool{}
	for _, candidate := range candidates {
		if victim[candidate.id] {
			continue
		}
		record := byID[candidate.id]
		recordBytes, err := json.Marshal(record)
		if err != nil {
			continue
		}
		tombstoneBytes, err := json.Marshal(tombstoneOf(record, now, next.CompactSeq+1))
		if err != nil {
			continue
		}
		delta := int64(len(tombstoneBytes) + 4 - len(recordBytes))
		if !hostsMarked[record.Host] {
			// The write's ledger mark gains one host entry; charge its actual
			// marshaled form plus room for the surrounding mark object.
			entry, err := json.Marshal(map[string]string{record.Host: candidate.id})
			if err != nil {
				continue
			}
			delta += int64(len(entry) + 32)
			hostsMarked[record.Host] = true
		}
		victim[candidate.id] = true
		added = append(added, candidate.id)
		changed = true
		estimated += delta
		if estimated > policy.StoreMaxBytes {
			continue
		}
		if measurements >= maxByteMeasurements {
			if finalChecked {
				// The estimate is a provable upper bound, so a fit it reports is
				// real: keep this prefix rather than spend another measurement.
				return changed
			}
			finalChecked = true
			exact := exactSize()
			if exact <= policy.StoreMaxBytes {
				return changed
			}
			// The exact form is still over: keep removing on the upper-bound
			// estimate, which guarantees the fit it next reports is real.
			estimated = exact + 1
			continue
		}
		exact := exactSize()
		measurements++
		if exact > policy.StoreMaxBytes {
			// The estimate crossed early (JSON structure drift): correct it and
			// keep removing.
			estimated = exact
			continue
		}
		// The prefix fits exactly; give back the victims the estimate overshot,
		// newest first, while the smaller prefix still fits.
		for len(added) > 0 && measurements < maxByteMeasurements {
			last := added[len(added)-1]
			// Unmarking must remove the key, not store false: the emptiness
			// checks read len(victim), and a false entry still counts.
			delete(victim, last)
			shrunk := exactSize()
			measurements++
			if shrunk <= policy.StoreMaxBytes {
				added = added[:len(added)-1]
				continue
			}
			victim[last] = true
			break
		}
		return changed
	}
	return changed
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
