package hostops

import (
	"errors"
	"fmt"
	"maps"
	"unicode/utf8"
)

// MaxIncarnationIDBytes is spec 08 §1's bound on an incarnation id: an opaque
// server-generated, non-empty value of at most 128 bytes. The generator pins
// its output to 36 bytes (canonical UUID text), so the bound is a schema check,
// not an allocation hint.
const MaxIncarnationIDBytes = 128

// Boundary is the per-host boundary record the registry owns and this store
// mirrors (registry spec 08 §7): the registry's current
// `{generation, incarnationId, presenceEpoch}` triple for one host name, "one
// record per host name — written in the same atomic store writes that mirror
// the generation, reconciled by the same boot rule, and rolled back by the same
// rollback rule" (deploy-pipeline spec §§4, 8 cite the schema). The store never
// interprets the values: it persists the triples the registry's writes commit,
// and cursor validation (a later slice) reads the mirrored value instead of
// re-reading hub.toml.
//
// The record outlives the host's live entry by design: §4's retention rule
// keeps "the historical (generation, incarnation id, presenceEpoch) boundary"
// in this record until the host's last operation record compacts, so a removed
// host's pagination still validates against a boundary. Nothing in this slice
// deletes a boundary record; compaction, the slice that owns retention, is
// where one goes.
type Boundary struct {
	// Generation is the registry generation the write committed: the live
	// entry's generation for a live host, or the removed incarnation's
	// generation for a removed one (registry spec 08 §1's high-water triple).
	Generation uint64 `json:"generation"`
	// IncarnationID is the never-reused incarnation id that generation belongs
	// to (spec 08 §1).
	IncarnationID string `json:"incarnationId"`
	// PresenceEpoch is the per-host presence counter the hub.toml write that
	// changed the name committed in the same atomic write (spec 08 §1).
	PresenceEpoch uint64 `json:"presenceEpoch"`
}

// ErrInvalidBoundary reports a boundary record outside the schema every writer
// of this store emits: an empty or non-UTF-8 host name, an empty or oversized
// or non-UTF-8 incarnation id, or a zero generation or presence epoch. The
// store refuses the write rather than persisting a triple no registry path can
// produce — and, since these bytes are what a later boot reconciles against,
// rather than a value its own next write would carry forward unexamined.
var ErrInvalidBoundary = errors.New("hostops: invalid boundary record")

// validateBoundary checks one mirrored triple against the schema. name is the
// host name the record is keyed by.
func validateBoundary(name string, boundary Boundary) error {
	if err := validateBoundaryName(name); err != nil {
		return err
	}
	if boundary.IncarnationID == "" {
		return fmt.Errorf("%w: boundary for %q carries no incarnation id", ErrInvalidBoundary, name)
	}
	if len(boundary.IncarnationID) > MaxIncarnationIDBytes {
		return fmt.Errorf("%w: boundary for %q carries a %d-byte incarnation id, over the %d-byte bound",
			ErrInvalidBoundary, name, len(boundary.IncarnationID), MaxIncarnationIDBytes)
	}
	if !utf8.ValidString(boundary.IncarnationID) {
		return fmt.Errorf("%w: boundary for %q carries an incarnation id that is not valid UTF-8", ErrInvalidBoundary, name)
	}
	if boundary.Generation == 0 {
		return fmt.Errorf("%w: boundary for %q pins no generation", ErrInvalidBoundary, name)
	}
	if boundary.PresenceEpoch == 0 {
		return fmt.Errorf("%w: boundary for %q pins no presence epoch", ErrInvalidBoundary, name)
	}
	return nil
}

// validateBoundaryName checks the host-name half of the schema, shared by the
// upsert and the removal paths: an empty or non-UTF-8 name is not a name any
// writer of these records emits.
func validateBoundaryName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty host name", ErrInvalidBoundary)
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: host name is not valid UTF-8", ErrInvalidBoundary)
	}
	return nil
}

// Boundary returns a copy of the mirrored boundary record for name.
func (s *Store) Boundary(name string) (Boundary, bool) {
	if s == nil {
		return Boundary{}, false
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	boundary, ok := s.cell.state.Boundaries[name]
	return boundary, ok
}

// Boundaries returns copies of every mirrored boundary record, keyed by host
// name.
func (s *Store) Boundaries() map[string]Boundary {
	if s == nil {
		return nil
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	return maps.Clone(s.cell.state.Boundaries)
}

// MirrorBoundaries persists every boundary record in updates and drops every
// name in remove, in one atomic store write, the same discipline Create and
// Transition use: the store mutex across the read-modify-write, a temp file
// fsynced and renamed over the store, then the containing directory fsynced.
// Names neither carried by updates nor named by remove keep the value they had,
// so one host's mirror write can never drop another host's boundary.
//
// One call is one write for every name it touches: a fault before the rename
// lands none of them, and the store's in-memory state follows the file, so the
// mirror can never hold a name the file does not (see RenameLanded for the
// post-rename failure case, which returns the boundary records the write
// committed). A name in remove that has no boundary is a no-op, and a call that
// changes nothing — no updates and no removals — writes nothing.
//
// A refusal — an empty name, an empty or oversized incarnation id, a zero
// generation or presence epoch, a value that is not valid UTF-8 — leaves the
// store untouched and persists nothing.
func (s *Store) MirrorBoundaries(updates map[string]Boundary, remove []string) error {
	if s == nil {
		return errors.New("hostops: store is not configured")
	}
	for name, boundary := range updates {
		if err := validateBoundary(name, boundary); err != nil {
			return err
		}
	}
	for _, name := range remove {
		if err := validateBoundaryName(name); err != nil {
			return err
		}
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	if len(updates) == 0 && len(remove) == 0 {
		// Nothing to mirror is not a write: the store file must not be created
		// (or rewritten) for a call that changes no value.
		return nil
	}
	next := cloneSnapshot(s.cell.state)
	if next.Boundaries == nil {
		next.Boundaries = make(map[string]Boundary, len(updates))
	}
	maps.Copy(next.Boundaries, updates)
	for _, name := range remove {
		delete(next.Boundaries, name)
	}
	if _, err := s.commitLocked(next); err != nil {
		// A refusal before the rename wrote nothing; a post-rename failure is a
		// landed write whose committed state memory adopted (commitLocked), so
		// the caller reconciles with the store rather than treating the mirror
		// as absent.
		return err
	}
	return nil
}
