package hostops

// Boot reconciliation for the deploy pipeline's durable state (deploy-pipeline
// spec 08b §7's boot order, §4's generation mirror and tombstone pass). Three
// passes live here, each one atomic store write:
//
//   - the tombstone-derived `host-removed` pass (§4): every record whose pinned
//     (generation, incarnation id) pair matches a loaded tombstone's pair is
//     marked `host-removed`, and the tombstoned name's token rows drop with it
//     (§7: "Any token whose host resolves at boot to removed (tombstoned) is
//     dropped with it. The tombstone reconciliation runs before token revival
//     is even considered").
//   - the reverse-direction token-row pass (§9's closing paragraph): store rows
//     with no covering intent whose hub.toml generation already advanced past
//     them — or whose host resolves to a tombstone — are dropped.
//   - the bidirectional generation-mirror reconciliation (§4/§7): a store
//     mirror newer than the hub.toml mark with no matching commit marker rolls
//     back to the file's mark (discarding the mirror's incarnation, preserving
//     the discarded number as the high-water the caller writes back), a file
//     mark newer than the mirror pushes forward, a name the file carries no
//     entry for keeps its mirror, and the discarded generation's in-flight
//     records transition to `interrupted` with a note naming the torn write
//     while its dedup tombstones drop — never a startup refusal.
//
// The hub.toml half of the mirror reconciliation is the caller's: the store has
// no access to that file. The caller writes MirrorReconcileResult.HighWater into
// [generations] in its own atomic hub.toml write, which is what preserves the
// discarded numbers as the names' high-water marks.

import (
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"
)

// TornWriteNote is the terminal note §4's torn-write recovery stamps on every
// record whose pinned generation the mirror reconciliation discarded: the
// hub.toml commit that generation belonged to did not survive, so the
// record's outcome is unknown.
const TornWriteNote = "interrupted: the hub.toml commit that pinned this operation's generation did not survive (torn write); its outcome is unknown"

// HostRemovedMark is one tombstone's identity pair, as §4's `host-removed`
// pass matches it: the removed generation and the never-reused incarnation id
// that generation belongs to. A record matches only when BOTH agree — a
// tombstone colliding with a live re-add carrying a different incarnation id
// matches no record.
type HostRemovedMark struct {
	Generation    uint64
	IncarnationID string
}

// validateHostRemovedMark checks one tombstone pair against the schema the pass
// consumes: a named host, a pinned generation, and an incarnation id inside the
// boundary schema's bound.
func validateHostRemovedMark(name string, mark HostRemovedMark) error {
	if err := validateBoundaryName(name); err != nil {
		return err
	}
	if mark.Generation == 0 {
		return fmt.Errorf("%w: tombstone for %q pins no generation", ErrInvalidBoundary, name)
	}
	if mark.IncarnationID == "" {
		return fmt.Errorf("%w: tombstone for %q carries no incarnation id", ErrInvalidBoundary, name)
	}
	if len(mark.IncarnationID) > MaxIncarnationIDBytes {
		return fmt.Errorf("%w: tombstone for %q carries a %d-byte incarnation id, over the %d-byte bound",
			ErrInvalidBoundary, name, len(mark.IncarnationID), MaxIncarnationIDBytes)
	}
	if !utf8.ValidString(mark.IncarnationID) {
		return fmt.Errorf("%w: tombstone for %q carries an incarnation id that is not valid UTF-8", ErrInvalidBoundary, name)
	}
	return nil
}

// ApplyHostRemovedPass applies §4's tombstone-derived pass in one atomic write:
// every record whose pinned pair matches its tombstone's pair is marked
// `host-removed`, and every token row of a tombstoned name is dropped. It is
// one-shot — a second pass with nothing left to mark or drop writes nothing —
// and returns the counts the boot log reports.
//
// The pass marks terminal records too: the mark is provenance ("this record
// belongs to a removed incarnation"), not a lifecycle transition, so it never
// touches a record's state or its sequence stamp.
func (s *Store) ApplyHostRemovedPass(marks map[string]HostRemovedMark) (marked, droppedTokens int, err error) {
	if s == nil {
		return 0, 0, errors.New("hostops: store is not configured")
	}
	for name, mark := range marks {
		if err := validateHostRemovedMark(name, mark); err != nil {
			return 0, 0, err
		}
	}
	if len(marks) == 0 {
		return 0, 0, nil
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	next := cloneSnapshot(s.cell.state)
	for name, mark := range marks {
		for i := range next.Records {
			record := &next.Records[i]
			if record.Host != name || record.Generation != mark.Generation || record.IncarnationID != mark.IncarnationID {
				continue
			}
			if record.HostRemoved {
				continue
			}
			record.HostRemoved = true
			marked++
		}
		kept := make([]Token, 0, len(next.Tokens))
		for _, row := range next.Tokens {
			if row.Host == name {
				droppedTokens++
				continue
			}
			kept = append(kept, row)
		}
		next.Tokens = kept
	}
	if marked == 0 && droppedTokens == 0 {
		return 0, 0, nil
	}
	landed, err := s.commitLocked(next)
	if err != nil {
		if landed {
			return marked, droppedTokens, err
		}
		return 0, 0, err
	}
	return marked, droppedTokens, nil
}

// TokenRowReconcile is the reverse-direction pass's view of hub.toml (§9's
// ClearHostRemovedMarks reverses §4's host-removed pass for one incarnation:
// every record whose pinned pair matches the mark is unmarked, in one atomic
// write. A compensation that restores a removed incarnation's records calls it,
// because a mark that survived would make a same-key retry read as a
// current-generation `host-removed` record (spec §11's
// `conflicting-operation-id`) instead of replaying the interrupted record, and
// the row would render removed. It returns how many records it unmarked; a pass
// that finds nothing to change writes nothing.
func (s *Store) ClearHostRemovedMarks(name string, mark HostRemovedMark) (int, error) {
	if s == nil {
		return 0, errors.New("hostops: store is not configured")
	}
	if err := validateHostRemovedMark(name, mark); err != nil {
		return 0, err
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	next := cloneSnapshot(s.cell.state)
	cleared := 0
	for i := range next.Records {
		record := &next.Records[i]
		if record.Host != name || record.Generation != mark.Generation || record.IncarnationID != mark.IncarnationID {
			continue
		}
		if !record.HostRemoved {
			continue
		}
		record.HostRemoved = false
		cleared++
	}
	if cleared == 0 {
		return 0, nil
	}
	landed, err := s.commitLocked(next)
	if err != nil {
		if landed {
			return cleared, err
		}
		return 0, err
	}
	return cleared, nil
}

// TokenRowReconcile is the reverse-direction pass's view of hub.toml (§9's
// closing paragraph): the generation hub.toml currently records for each live
// name, the names that resolve to a tombstone, and the values a live
// pendingStoreSync intent already covers.
type TokenRowReconcile struct {
	// Live maps a live name to the generation hub.toml currently records for
	// it. A row pinned below that generation is stale: the store committed the
	// removal, hub.toml's compensation already restored, and the row must go.
	Live map[string]uint64
	// Removed names the hosts that resolve at boot to a tombstone: every token
	// row pinned to the tombstoned incarnation is dropped (§7). The pair
	// matters: a live re-add carries a different incarnation id, and its rows
	// are not this tombstone's to delete.
	Removed map[string]HostRemovedMark
	// Covered maps a host to the row values a live intent names. The forward
	// pass owns those rows; this pass leaves them for it, so the two can never
	// fight over one row.
	Covered map[string]map[string]struct{}
}

// ReconcileTokenRows drops the token rows §9's closing paragraph and §7's
// token reaping name as dead, in one atomic write: rows for a tombstoned host,
// and rows whose host's hub.toml generation advanced past the row's pin. Rows
// for a host hub.toml carries no entry for are left alone — the mirror is
// preserved there, never rolled back — and rows a live intent covers are left
// to the forward pass.
func (s *Store) ReconcileTokenRows(view TokenRowReconcile) (int, error) {
	if s == nil {
		return 0, errors.New("hostops: store is not configured")
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	next := cloneSnapshot(s.cell.state)
	kept := make([]Token, 0, len(next.Tokens))
	dropped := 0
	for _, row := range next.Tokens {
		if covered := view.Covered[row.Host]; covered != nil {
			if _, ok := covered[row.Value]; ok {
				kept = append(kept, row)
				continue
			}
		}
		mark, removed := view.Removed[row.Host]
		liveGeneration, live := view.Live[row.Host]
		switch {
		case removed && !live && row.Generation == mark.Generation && row.IncarnationID == mark.IncarnationID:
			// The tombstone names this exact removed incarnation. A tombstone a
			// live re-add superseded (the name is live again) matches nothing:
			// the re-add's rows carry a different pair, and generation-only
			// matching would delete a live incarnation's token.
			dropped++
		case live && row.Generation < liveGeneration:
			dropped++
		default:
			kept = append(kept, row)
		}
	}
	if dropped == 0 {
		return 0, nil
	}
	next.Tokens = kept
	landed, err := s.commitLocked(next)
	if err != nil {
		if landed {
			return dropped, err
		}
		return 0, err
	}
	return dropped, nil
}

// MirrorCommit is hub.toml's generation-mirror commit marker (§4): the
// (name, hub.toml generation, store-mirror generation) triple a hub.toml commit
// that advanced a mirrored store-side generation writes. A store mirror at the
// marker's store generation was authorized by a durable hub.toml write; a
// mirror newer than the file's mark with no such marker was not.
type MirrorCommit struct {
	HubTOMLGeneration uint64
	StoreGeneration   uint64
}

// MirrorView is hub.toml's side of the generation-mirror reconciliation: the
// file's per-name generation marks, its live and tombstoned names, and its
// commit markers. The store owns the mirror half; nothing here reads hub.toml.
type MirrorView struct {
	// Marks is the [generations] mark per name, when the file carries one.
	Marks map[string]Boundary
	// Live names the names the file carries a live [[hosts]] entry for.
	Live map[string]struct{}
	// Removed names the names the file carries a tombstone for. Either makes a
	// mismatch a rollback (never a startup refusal); neither leaves the mirror
	// preserved.
	Removed map[string]struct{}
	// Commits is the file's commit-marker set, keyed by name.
	Commits map[string]MirrorCommit
}

// MirrorReconcileResult reports what one boot pass did, for the boot log and
// for the caller's hub.toml write.
type MirrorReconcileResult struct {
	// RolledBack names the names whose store mirror was ahead of the file's
	// mark with no matching commit marker: the mirror's incarnation was
	// discarded and the discarded number is preserved as the name's high-water.
	RolledBack []string
	// PushedForward names the names whose file mark was ahead of the store
	// mirror: the mirror took the file's triple.
	PushedForward []string
	// Preserved names the names whose mirror was kept: the file carries no
	// entry, or a newer mirror stands on a marker's authority.
	Preserved []string
	// HighWater is the per-name triple the caller must write into hub.toml's
	// [generations] in its own atomic write — the max-of-both restoration for a
	// preserved mirror, and the raised mark a rollback left behind. A name
	// absent from the map needs no file change.
	HighWater map[string]Boundary
	// RecordsMoved counts the in-flight records the rollback moved to
	// `interrupted` with TornWriteNote.
	RecordsMoved int
	// NotesUpgraded counts the records §7's interrupted transition had already
	// moved whose plain crash note this pass replaced with the torn-write note:
	// their outcome was unknown for the same reason, and the discarded
	// generation is the precise fact.
	NotesUpgraded int
	// TombstonesDropped counts the dedup tombstones pinned to a discarded
	// generation that dropped in the same write.
	TombstonesDropped int
}

// ReconcileMirror runs §4/§7's bidirectional generation-mirror reconciliation
// in one atomic store write, before the store serves any request:
//
//   - a store mirror newer than the file's mark for the same name with no
//     matching commit marker rolls back — the mirror's incarnation is
//     discarded, the discarded number stays as the name's generation (the
//     high-water the caller writes back), the discarded generation's in-flight
//     records transition to `interrupted` naming the torn write, and its dedup
//     tombstones drop so a same-key replay refuses `stale-entry`;
//   - a file mark newer than the store mirror pushes forward into the mirror;
//   - a name the file carries no entry for keeps its mirror, and the
//     max-of-both restoration reads the surviving mirror as the high-water;
//   - a mirror the file's own commit marker authorizes is kept as it stands.
//
// The rollback applies only when the file carries a live entry or a tombstone
// for the name and the file's mark is complete: a name the file only carries a
// bare [generations] record for keeps its mirror (preserved), exactly as §4
// requires.
//
// ReconcileMirror refuses nothing a load accepted: every split converges, so
// boot serves (never a startup refusal for a torn write).
func (s *Store) ReconcileMirror(view MirrorView) (MirrorReconcileResult, error) {
	result := MirrorReconcileResult{HighWater: map[string]Boundary{}}
	if s == nil {
		return result, errors.New("hostops: store is not configured")
	}
	for name, mark := range view.Marks {
		if err := validateBoundary(name, mark); err != nil {
			return result, err
		}
	}
	for name, commit := range view.Commits {
		if err := validateBoundaryName(name); err != nil {
			return result, err
		}
		if commit.HubTOMLGeneration == 0 || commit.StoreGeneration == 0 {
			return result, fmt.Errorf("%w: commit marker for %q carries a zero generation", ErrInvalidBoundary, name)
		}
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	now := s.now()
	policy := s.retentionPolicy()
	next := cloneSnapshot(s.cell.state)
	if next.Boundaries == nil {
		// A store that has mirrored nothing yet can still have a file mark to
		// push forward: the write needs a map to land it in.
		next.Boundaries = map[string]Boundary{}
	}
	names := make([]string, 0, len(view.Marks)+len(next.Boundaries))
	for name := range view.Marks {
		names = append(names, name)
	}
	for name := range next.Boundaries {
		if _, seen := view.Marks[name]; !seen {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	changed := false
	for _, name := range names {
		mark, hasMark := view.Marks[name]
		mirror, hasMirror := next.Boundaries[name]
		switch {
		case !hasMirror && hasMark:
			// The file's mark is newer than the mirror (there is none): push it
			// forward. A removed name whose history is gone keeps no mirror —
			// the same rule the live mirror write applies, so a boot cannot
			// recreate a boundary the next mutation's write would prune again.
			if s.mirrorPushBlocked(&next, name, now, policy) {
				continue
			}
			next.Boundaries[name] = mark
			result.PushedForward = append(result.PushedForward, name)
			changed = true
		case hasMirror && !hasMark:
			// hub.toml carries no entry for the name: preserve the mirror, and
			// read the surviving mirror as the high-water mark (max-of-both).
			result.Preserved = append(result.Preserved, name)
			result.HighWater[name] = mirror
		case hasMirror && hasMark:
			if commit, ok := view.Commits[name]; ok &&
				commit.HubTOMLGeneration == mark.Generation && commit.StoreGeneration == mirror.Generation {
				// The file's own marker authorizes this mirror value only when
				// both of its generations name the state as it stands: the
				// file's mark and the store mirror. A stale marker — one whose
				// hub.toml generation names an older write — authorizes nothing.
				continue
			}
			switch {
			case mirror.Generation > mark.Generation:
				_, live := view.Live[name]
				_, removed := view.Removed[name]
				if !live && !removed {
					// The file carries no live entry or tombstone for the name:
					// preserve, never roll back.
					result.Preserved = append(result.Preserved, name)
					result.HighWater[name] = mirror
					continue
				}
				discarded := mirror.Generation
				// The rollback discards the mirror's incarnation: the file's
				// mark is the identity that survives, and the discarded number
				// stays on as the generation so no later mutation reuses it.
				final := Boundary{
					Generation:    discarded,
					IncarnationID: mark.IncarnationID,
					PresenceEpoch: mark.PresenceEpoch,
				}
				next.Boundaries[name] = final
				result.RolledBack = append(result.RolledBack, name)
				result.HighWater[name] = final
				changed = true
				for i := range next.Records {
					record := &next.Records[i]
					if record.Host != name || record.Generation != discarded {
						continue
					}
					if !record.State.InFlight() {
						if record.State == StateInterrupted && record.Result != nil && record.Result.Message == InterruptedNote {
							// §7's interrupted transition runs before this pass,
							// so a record the crash left in flight already carries
							// the plain crash note. §4 asks for the note naming the
							// torn write, which is the more precise fact: the
							// generation was discarded. The state and its sequence
							// stamp do not move.
							record.Result = &Result{OK: false, Message: TornWriteNote}
							result.NotesUpgraded++
						}
						// Any other terminal record's outcome is recorded, not
						// unknown: it stays history, exactly as the store's own
						// terminal-state rule requires.
						continue
					}
					record.State = StateInterrupted
					record.Result = &Result{OK: false, Message: TornWriteNote}
					record.UpdatedAt = now
					next.advanceSequence(record)
					result.RecordsMoved++
				}
				dropped := 0
				kept := make([]Tombstone, 0, len(next.Tombstones))
				for _, tombstone := range next.Tombstones {
					if tombstone.Host == name && tombstone.Generation == discarded {
						dropped++
						continue
					}
					kept = append(kept, tombstone)
				}
				if dropped > 0 {
					next.Tombstones = kept
					result.TombstonesDropped += dropped
				}
			case mirror.Generation < mark.Generation:
				if s.mirrorPushBlocked(&next, name, now, policy) {
					continue
				}
				next.Boundaries[name] = mark
				result.PushedForward = append(result.PushedForward, name)
				changed = true
			default:
				// Converged: the file's mark and the store mirror agree.
			}
		}
	}
	if !changed && result.RecordsMoved == 0 && result.NotesUpgraded == 0 && result.TombstonesDropped == 0 {
		return result, nil
	}
	landed, err := s.commitLocked(next)
	if err != nil {
		if landed {
			return result, err
		}
		result.RecordsMoved = 0
		result.NotesUpgraded = 0
		result.TombstonesDropped = 0
		result.RolledBack = nil
		result.PushedForward = nil
		result.HighWater = map[string]Boundary{}
		return result, err
	}
	return result, nil
}

// mirrorPushBlocked reports whether pushing a boundary for name forward would
// recreate a boundary the store's own retention rule discards again: the name
// is genuinely removed, past the tombstoneRetention horizon, and holds no
// records — the state in which the historical boundary goes with the host's
// last record. It is the same rule MirrorHostState applies to a proposed
// boundary, so a boot and a mutation cannot disagree about whether a removed
// name keeps a mirror.
func (s *Store) mirrorPushBlocked(state *snapshot, name string, now time.Time, policy RetentionPolicy) bool {
	marker, marked := state.RemovedHosts[name]
	if !marked || now.Before(marker.RemovedAt.Add(policy.RemovedHostHorizon)) {
		return false
	}
	if !removedMarkerAgrees(state, name, marker) {
		return false
	}
	return !slices.ContainsFunc(state.Records, func(record Record) bool { return record.Host == name })
}
