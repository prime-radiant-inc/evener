package hostops

// The cross-file commit intent (deploy-pipeline spec 08b §9). Token rows live
// in this store's file while tombstones and mutation receipts live in hub.toml,
// and no shared mutex makes the two atomic. Two durable records bridge them:
//
//   - `pendingStoreSync`, hub.toml's intent: the exact store rows to delete
//     plus the hub.toml generation the intent belongs to. hub.toml's atomic
//     write carries it; this store's purge applies it (post-swap, never
//     before); a follow-up hub.toml atomic write clears it. This file owns the
//     store half: ApplyStoreSync.
//   - `pendingCompensation`, this store's armed record: the rows about to be
//     purged (the preimage), the stash reference the hub.toml restore applies,
//     and the hub.toml generation the purge belongs to, in a phase machine
//     `compensating-armed` → `compensating-hubtoml` → `compensating-rows` →
//     `compensating-runtime` → `compensating-clear`. The preimage persists
//     BEFORE the purge write and the purge write advances the record past
//     `armed`, so a crash at any point leaves the rows recoverable.
//
// The record is store-local by construction: the hub.toml restore overwrites
// hub.toml's bytes, and it cannot touch this store's file, so a compensation
// survives its own restore.

import (
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"
)

// MaxStoreSyncValueBytes bounds one intent value (a confirmation token's wire
// form). The mint's value is far inside it; the bound is what the schema
// enforces.
const MaxStoreSyncValueBytes = 256

// MaxStashReferenceBytes bounds the stash reference a compensation record
// carries: a path beside hub.toml, far inside the bound.
const MaxStashReferenceBytes = 4096

// ErrInvalidStoreSync reports a pendingStoreSync intent outside the schema this
// store's writers consume: an empty host, a zero hub.toml generation, or an
// empty/oversized/non-UTF-8 row value.
var ErrInvalidStoreSync = errors.New("hostops: invalid store-sync intent")

// ErrInvalidCompensation reports a pendingCompensation record outside the
// schema the committer writes.
var ErrInvalidCompensation = errors.New("hostops: invalid compensation record")

// StoreSyncIntent is hub.toml's `pendingStoreSync` intent as this store
// consumes it (spec §9): the exact token rows to delete, plus the hub.toml
// generation the intent belongs to.
type StoreSyncIntent struct {
	Host       string
	Generation uint64
	Values     []string
}

// validateStoreSyncIntent checks one intent against the schema. Values are
// unique: one intent that named the same row twice would be a shape no writer
// emits.
func validateStoreSyncIntent(intent StoreSyncIntent) error {
	if err := validateBoundaryName(intent.Host); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidStoreSync, err)
	}
	if intent.Generation == 0 {
		return fmt.Errorf("%w: intent for %q pins no hub.toml generation", ErrInvalidStoreSync, intent.Host)
	}
	return validateStoreSyncValues(intent.Host, intent.Values)
}

// validateStoreSyncValues checks the row-value half of one intent (or one
// purge): at least one value, each non-empty, bounded, valid UTF-8 and unique.
func validateStoreSyncValues(host string, values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("%w: intent for %q names no rows", ErrInvalidStoreSync, host)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return fmt.Errorf("%w: intent for %q names an empty row value", ErrInvalidStoreSync, host)
		}
		if len(value) > MaxStoreSyncValueBytes {
			return fmt.Errorf("%w: intent for %q names a %d-byte row value, over the %d-byte bound",
				ErrInvalidStoreSync, host, len(value), MaxStoreSyncValueBytes)
		}
		if !utf8.ValidString(value) {
			return fmt.Errorf("%w: intent for %q names a row value that is not valid UTF-8", ErrInvalidStoreSync, host)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%w: intent for %q names row %q twice", ErrInvalidStoreSync, host, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

// ApplyStoreSync applies one hub.toml intent: it deletes every token row the
// intent names, in one atomic write. It is idempotent — the store-already-
// applied case finds nothing to delete and writes nothing — and it returns how
// many rows it purged, so boot can tell "re-applied the purge" from "already
// converged".
//
// This is the store half of §9's two-phase protocol: hub.toml's committed bytes
// are the authority, and this purge lands only after that commit's swap (the
// caller owns the ordering — the boot pass and the remove path are its two
// callers).
func (s *Store) ApplyStoreSync(intent StoreSyncIntent) (int, error) {
	if s == nil {
		return 0, errors.New("hostops: store is not configured")
	}
	if err := validateStoreSyncIntent(intent); err != nil {
		return 0, err
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	next := cloneSnapshot(s.cell.state)
	purged := 0
	kept := make([]Token, 0, len(next.Tokens))
	for _, row := range next.Tokens {
		if row.Host == intent.Host && slices.Contains(intent.Values, row.Value) {
			purged++
			continue
		}
		kept = append(kept, row)
	}
	if purged == 0 {
		return 0, nil
	}
	next.Tokens = kept
	landed, err := s.commitLocked(next)
	if err != nil {
		if landed {
			// The rename landed: the rows are durably gone and memory adopted
			// that state, so the count is the truth the caller reports (see
			// RenameLanded). A failure before the rename purged nothing.
			return purged, err
		}
		return 0, err
	}
	return purged, nil
}

// CompensationPhase is §9's phase machine spelling.
type CompensationPhase string

const (
	// CompensationArmed is the phase the committer persists BEFORE the purge
	// write: the preimage is durable and the purge has not landed.
	CompensationArmed CompensationPhase = "compensating-armed"
	// CompensationHubTOML is the phase the purge write advances the record to
	// (`armed` → the hub.toml restore comes next). `compensating-sidecar` is
	// the pre-storage-decision spelling of the same phase and reads as this
	// value (NormalizeCompensationPhase).
	CompensationHubTOML CompensationPhase = "compensating-hubtoml"
	// CompensationRows is the phase after the hub.toml restore landed: the row
	// re-insert is next.
	CompensationRows CompensationPhase = "compensating-rows"
	// CompensationRuntime is the phase after the row re-insert landed: the
	// runtime re-apply of the restored hub.toml's set is next.
	CompensationRuntime CompensationPhase = "compensating-runtime"
	// CompensationClear is the phase after the runtime revert landed: the
	// record clear is next. A record in this phase clears without resurrecting
	// rows — the rows are already converged.
	CompensationClear CompensationPhase = "compensating-clear"
	// compensationSidecarLegacy is the retired spelling §9 names.
	compensationSidecarLegacy CompensationPhase = "compensating-sidecar"
)

// NormalizeCompensationPhase maps a persisted phase to its live spelling: the
// retired `compensating-sidecar` literal is an alias for `compensating-hubtoml`
// (same arm, same restore), never an unknown phase. Every other value reads as
// itself.
func NormalizeCompensationPhase(phase CompensationPhase) CompensationPhase {
	if phase == compensationSidecarLegacy {
		return CompensationHubTOML
	}
	return phase
}

// valid reports whether phase is one of the machine's literal values, the
// legacy alias included.
func (phase CompensationPhase) valid() bool {
	switch NormalizeCompensationPhase(phase) {
	case CompensationArmed, CompensationHubTOML, CompensationRows, CompensationRuntime, CompensationClear:
		return true
	}
	return false
}

// next reports the phase one step forward, and whether a step exists. The
// machine is strictly forward.
func (phase CompensationPhase) next() (CompensationPhase, bool) {
	switch NormalizeCompensationPhase(phase) {
	case CompensationArmed:
		return CompensationHubTOML, true
	case CompensationHubTOML:
		return CompensationRows, true
	case CompensationRows:
		return CompensationRuntime, true
	case CompensationRuntime:
		return CompensationClear, true
	}
	return "", false
}

// Compensation is spec §9's `pendingCompensation` record: the rows about to be
// purged (the preimage), the stash reference the hub.toml restore must apply,
// and the hub.toml generation the purge belongs to, in the phase machine above.
type Compensation struct {
	// Host is the host name the purge belongs to. It is also the record's key
	// in the store file; the two must agree.
	Host string `json:"host"`
	// Phase is the durable phase (§9). A read normalizes the retired spelling.
	Phase CompensationPhase `json:"phase"`
	// Rows is the preimage: the exact token rows the purge is about to delete.
	// It is written before the purge and never after it.
	Rows []Token `json:"rows"`
	// Stash is the reference to the durable copy of the prior hub.toml bytes
	// the restore applies. The record carries it so a crash between the two
	// restores still names its restore source.
	Stash string `json:"stashReference"`
	// Generation is the hub.toml generation the purge belongs to.
	Generation uint64 `json:"generation"`
}

// normalized returns the record with its phase mapped to the live spelling and
// its row slice non-nil.
func (c Compensation) normalized() Compensation {
	out := c
	out.Phase = NormalizeCompensationPhase(c.Phase)
	out.Rows = cloneTokens(c.Rows)
	if out.Rows == nil {
		out.Rows = []Token{}
	}
	return out
}

// validateCompensation checks one record against the schema: a named host, a
// known phase, a preimage whose every row validates and belongs to the host, a
// stash reference and a hub.toml generation.
func validateCompensation(c Compensation) error {
	if err := validateBoundaryName(c.Host); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCompensation, err)
	}
	if !c.Phase.valid() {
		return fmt.Errorf("%w: record for %q carries phase %q", ErrInvalidCompensation, c.Host, c.Phase)
	}
	for _, row := range c.Rows {
		if err := validateTokenRow(row); err != nil {
			return fmt.Errorf("%w: preimage row: %w", ErrInvalidCompensation, err)
		}
		if row.Host != c.Host {
			return fmt.Errorf("%w: record for %q carries a preimage row of %q", ErrInvalidCompensation, c.Host, row.Host)
		}
	}
	if c.Stash == "" {
		return fmt.Errorf("%w: record for %q carries no stash reference", ErrInvalidCompensation, c.Host)
	}
	if len(c.Stash) > MaxStashReferenceBytes {
		return fmt.Errorf("%w: record for %q carries a %d-byte stash reference, over the %d-byte bound",
			ErrInvalidCompensation, c.Host, len(c.Stash), MaxStashReferenceBytes)
	}
	if !utf8.ValidString(c.Stash) {
		return fmt.Errorf("%w: record for %q carries a stash reference that is not valid UTF-8", ErrInvalidCompensation, c.Host)
	}
	if c.Generation == 0 {
		return fmt.Errorf("%w: record for %q pins no hub.toml generation", ErrInvalidCompensation, c.Host)
	}
	return nil
}

// ArmCompensation persists one record in its own store-local write, before the
// purge write (§9: "the committer first persists a record ... in its own
// store-local write before the purge write"; "The preimage persists before the
// purge, never after it"). A record already open for the host refuses — a
// second commit for one host would clobber a live compensation the boot pass
// has not converged.
func (s *Store) ArmCompensation(record Compensation) error {
	if s == nil {
		return errors.New("hostops: store is not configured")
	}
	normalized := record.normalized()
	if err := validateCompensation(normalized); err != nil {
		return err
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	if _, open := s.cell.state.Compensations[normalized.Host]; open {
		return fmt.Errorf("%w: a compensation for %q is already open", ErrInvalidCompensation, normalized.Host)
	}
	next := cloneSnapshot(s.cell.state)
	if next.Compensations == nil {
		next.Compensations = map[string]Compensation{}
	}
	next.Compensations[normalized.Host] = normalized
	_, err := s.commitLocked(next)
	return err
}

// Compensation returns a copy of the open record for host, phase normalized.
func (s *Store) Compensation(host string) (Compensation, bool) {
	if s == nil {
		return Compensation{}, false
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	record, ok := s.cell.state.Compensations[host]
	if !ok {
		return Compensation{}, false
	}
	return record.normalized(), true
}

// Compensations returns copies of every open record, keyed by host.
func (s *Store) Compensations() map[string]Compensation {
	if s == nil {
		return nil
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	out := make(map[string]Compensation, len(s.cell.state.Compensations))
	for host, record := range s.cell.state.Compensations {
		out[host] = record.normalized()
	}
	return out
}

// AdvanceCompensation moves one record to the next phase in its own store write
// (the restorer advances the phase in its own store write per step, §9). Only
// the forward step is legal; a missing record refuses.
func (s *Store) AdvanceCompensation(host string, to CompensationPhase) error {
	if s == nil {
		return errors.New("hostops: store is not configured")
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	record, ok := s.cell.state.Compensations[host]
	if !ok {
		return fmt.Errorf("%w: no compensation for %q", ErrInvalidCompensation, host)
	}
	next, ok := record.Phase.next()
	if !ok {
		return fmt.Errorf("%w: record for %q is in terminal phase %q", ErrInvalidCompensation, host, record.Phase)
	}
	if NormalizeCompensationPhase(to) != next {
		return fmt.Errorf("%w: record for %q in phase %q cannot advance to %q", ErrInvalidCompensation, host, record.Phase, to)
	}
	nextState := cloneSnapshot(s.cell.state)
	nextRecord := nextState.Compensations[host]
	nextRecord.Phase = next
	nextState.Compensations[host] = nextRecord
	_, err := s.commitLocked(nextState)
	return err
}

// ClearCompensation drops one record in its own store write. A record that is
// not there is a no-op: a crash after the clear re-runs it as nothing.
func (s *Store) ClearCompensation(host string) error {
	if s == nil {
		return errors.New("hostops: store is not configured")
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	if _, ok := s.cell.state.Compensations[host]; !ok {
		return nil
	}
	next := cloneSnapshot(s.cell.state)
	delete(next.Compensations, host)
	_, err := s.commitLocked(next)
	return err
}

// PurgeCompensated is §9's purge write: it deletes the named rows AND advances
// the host's armed record past `armed` (to `compensating-hubtoml`) in one
// atomic write, so the preimage's durability and the purge's landing cannot
// drift apart. It refuses when no armed record exists: the purge that must be
// compensable always arms first.
func (s *Store) PurgeCompensated(host string, values []string) (int, error) {
	if s == nil {
		return 0, errors.New("hostops: store is not configured")
	}
	if err := validateBoundaryName(host); err != nil {
		return 0, err
	}
	if err := validateStoreSyncValues(host, values); err != nil {
		return 0, err
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	record, ok := s.cell.state.Compensations[host]
	if !ok {
		return 0, fmt.Errorf("%w: no compensation for %q to advance", ErrInvalidCompensation, host)
	}
	if NormalizeCompensationPhase(record.Phase) != CompensationArmed {
		return 0, fmt.Errorf("%w: record for %q is in phase %q, not %s", ErrInvalidCompensation, host, record.Phase, CompensationArmed)
	}
	next := cloneSnapshot(s.cell.state)
	purged := 0
	kept := make([]Token, 0, len(next.Tokens))
	for _, row := range next.Tokens {
		if row.Host == host && slices.Contains(values, row.Value) {
			purged++
			continue
		}
		kept = append(kept, row)
	}
	next.Tokens = kept
	advanced := next.Compensations[host]
	advanced.Phase = CompensationHubTOML
	next.Compensations[host] = advanced
	landed, err := s.commitLocked(next)
	if err != nil {
		if landed {
			return purged, err
		}
		return 0, err
	}
	return purged, nil
}

// ReinsertCompensationRows is §9's rows arm, run after the hub.toml restore
// landed (the record is in `compensating-rows`): it re-inserts exactly the rows
// keep revalidates — the restored hub.toml's generation decides — and advances
// the record to `compensating-runtime` in the same atomic write. A row whose
// value a live row already carries is not duplicated; `keep` nil re-inserts the
// whole preimage.
func (s *Store) ReinsertCompensationRows(host string, keep func(Token) bool) (int, error) {
	if s == nil {
		return 0, errors.New("hostops: store is not configured")
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	record, ok := s.cell.state.Compensations[host]
	if !ok {
		return 0, fmt.Errorf("%w: no compensation for %q", ErrInvalidCompensation, host)
	}
	if NormalizeCompensationPhase(record.Phase) != CompensationRows {
		return 0, fmt.Errorf("%w: record for %q is in phase %q, not %s", ErrInvalidCompensation, host, record.Phase, CompensationRows)
	}
	next := cloneSnapshot(s.cell.state)
	present := make(map[string]struct{}, len(next.Tokens))
	for _, row := range next.Tokens {
		present[row.Value] = struct{}{}
	}
	inserted := 0
	for _, row := range record.Rows {
		if keep != nil && !keep(row) {
			continue
		}
		if _, exists := present[row.Value]; exists {
			continue
		}
		// The store keeps at most one row per host name and one per value: a
		// host that already holds a row keeps it (a fresh mint is the row's
		// current, and the preimage's copy is superseded history).
		if slices.ContainsFunc(next.Tokens, func(existing Token) bool { return existing.Host == row.Host }) {
			continue
		}
		next.Tokens = append(next.Tokens, cloneToken(row))
		present[row.Value] = struct{}{}
		inserted++
	}
	advanced := next.Compensations[host]
	advanced.Phase = CompensationRuntime
	next.Compensations[host] = advanced
	landed, err := s.commitLocked(next)
	if err != nil {
		if landed {
			return inserted, err
		}
		return 0, err
	}
	return inserted, nil
}

// cloneCompensations deep-copies the record map, so a caller mutating the copy
// it got can never reach the store's in-memory rows.
func cloneCompensations(records map[string]Compensation) map[string]Compensation {
	if records == nil {
		return nil
	}
	out := make(map[string]Compensation, len(records))
	for host, record := range records {
		out[host] = record.normalized()
	}
	return out
}
