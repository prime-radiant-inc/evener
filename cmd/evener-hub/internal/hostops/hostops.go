// Package hostops owns the hub's durable operation store for host lifecycle
// operations: the controller-side record of every `deploy`/`restart` that must
// outlive the AppWire RPC that asked for it (component spec 08b §4), the
// durable per-store state-transition sequence race scans compare, and the boot
// pass that moves a record a crash left `pending`/`running` to `interrupted`
// (§7).
//
// What this package deliberately does not own, because the spec hands each to a
// later slice: confirmation tokens (§3), dedup and the create-from-consume
// write (§6), the per-host gate (§5), operations pagination (§8), retention and
// compaction (§4), cross-file commit intents (§9), generation mirroring (§4),
// and the custody-first quarantine of a corrupt store file (§4, crash-fencing
// spec). The store's job here is the substrate those paths stand on: a record
// schema, states, one atomic write discipline, one store mutex, and a load that
// refuses anything it cannot prove is its own.
//
// Corrupt or unreadable store at this layer: spec §4 quarantines a corrupt or
// schema-invalid store file in custody-first order, and §7 runs the operation
// store's load before anything else at boot. That custody hand-off belongs to
// the crash-fencing slice; until it lands, Open refuses to load a corrupt or
// unreadable file (ErrStoreCorrupt for a file that parses wrong, the wrapped
// I/O error otherwise) and the caller must not serve hosts from it.
package hostops

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MaxClientOperationIDBytes is spec §1's bound on a client operation ID: an
// opaque, non-empty value of at most 128 bytes with no required structure.
const MaxClientOperationIDBytes = 128

// Kind is the operation kind a record describes: spec §4's `deploy`/`restart`
// pair. The kind is part of a record's dedup scope and never changes after
// creation.
type Kind string

const (
	// KindDeploy is a `deploy` operation: install/refresh the target's tree
	// under a confirmation token.
	KindDeploy Kind = "deploy"
	// KindRestart is a `restart` operation on an already-attached host.
	KindRestart Kind = "restart"
)

// Valid reports whether k is one of the kinds this store records.
func (k Kind) Valid() bool { return k == KindDeploy || k == KindRestart }

// State is a record's durable lifecycle state (spec §4). The set is closed: a
// state outside it never enters the store.
type State string

const (
	// StatePending is a created record whose worker has not started: the
	// consume-and-create write lands here (§6 step 4).
	StatePending State = "pending"
	// StateRunning is a record whose worker was launched.
	StateRunning State = "running"
	// StateComplete is a terminal success.
	StateComplete State = "complete"
	// StateFailed is a terminal failure.
	StateFailed State = "failed"
	// StateInterrupted is a terminal unknown outcome: a crash left the record
	// pending/running and boot moved it here (§7).
	StateInterrupted State = "interrupted"
	// StateOrphanUnverified is the durable per-record state the fencing paths
	// create and only they resolve (spec §7).
	StateOrphanUnverified State = "orphan-unverified"
)

// Valid reports whether s is one of the states this store records.
func (s State) Valid() bool {
	switch s {
	case StatePending, StateRunning, StateComplete, StateFailed, StateInterrupted, StateOrphanUnverified:
		return true
	}
	return false
}

// Terminal reports whether moving a record into s is one of the transitions
// spec §4's state-transition sequence counts: the terminal states
// `complete`/`failed`/`interrupted`, and the `orphan-unverified`→`interrupted`
// resolution with them. StateOrphanUnverified is itself not terminal — only its
// resolution advances the sequence — so boot leaves it alone (§7).
func (s State) Terminal() bool {
	switch s {
	case StateComplete, StateFailed, StateInterrupted:
		return true
	}
	return false
}

// InFlight reports whether a state is one boot's interrupted transition owns: a
// record still `pending`/`running` when the controller came up (spec §7). Every
// other state is not boot's to move, and `orphan-unverified` in particular is
// resolved only through the fencing paths.
func (s State) InFlight() bool { return s == StatePending || s == StateRunning }

// ProgressEntry is one timestamped progress line on a record (spec §10). The
// per-record bound on the list is owned by the deploy slice, which is what
// appends them.
type ProgressEntry struct {
	TS      time.Time `json:"ts"`
	Message string    `json:"message"`
}

// Result is a record's terminal result (spec §10).
type Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// Record is the durable controller-side record of one `deploy`/`restart`, spec
// §4's record schema. `CreatedAt`/`UpdatedAt` are display-only and never decide
// a race: the state-transition sequence does.
//
// FencingEpoch and OrphanBoundary carry data whose shapes are defined by the
// crash-fencing spec (§9) and never restated here, so the store keeps them
// verbatim: the worker's fencing epoch, persisted before the first `running`
// probe, and the per-member boundary array an `orphan-unverified` record
// carries.
type Record struct {
	ID                string          `json:"id"`
	ClientOperationID string          `json:"clientOperationId"`
	Host              string          `json:"host"`
	Kind              Kind            `json:"kind"`
	State             State           `json:"state"`
	Generation        uint64          `json:"generation"`
	IncarnationID     string          `json:"incarnationId"`
	FencingEpoch      json.RawMessage `json:"fencingEpoch,omitempty"`
	OrphanBoundary    json.RawMessage `json:"orphanBoundary,omitempty"`
	Progress          []ProgressEntry `json:"progress,omitempty"`
	Result            *Result         `json:"result,omitempty"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
	HostRemoved       bool            `json:"hostRemoved"`
	// Sequence is the store's state-transition sequence value the record was
	// stamped with when it entered a terminal state; 0 until then.
	Sequence uint64 `json:"sequence,omitempty"`
}

// NewRecord is the caller-supplied half of a record: everything the store
// records about a fresh operation except the fields the store itself assigns
// (id, state, timestamps). The pinned (generation, incarnation id) pair is
// spec §4's dedup and validity identity for the operation.
type NewRecord struct {
	ClientOperationID string
	Host              string
	Kind              Kind
	Generation        uint64
	IncarnationID     string
}

// ErrStoreReadableBeyondOwner reports a store file whose mode lets anyone but
// its owner read it. Spec §4: startup refuses to load a store readable beyond
// its owner.
var ErrStoreReadableBeyondOwner = errors.New("hostops: store is readable beyond its owner")

// ErrStoreCorrupt reports a store file that is unparseable or schema-invalid.
// The custody-first quarantine spec §4 takes for such a file belongs to the
// crash-fencing slice; here the load refuses and the caller must not serve.
var ErrStoreCorrupt = errors.New("hostops: store is corrupt")

// ErrInvalidRecord reports a record that falls outside the store's schema.
var ErrInvalidRecord = errors.New("hostops: invalid record")

// ErrRecordNotFound reports a transition target that names no stored record.
var ErrRecordNotFound = errors.New("hostops: record not found")

// ErrInvalidState reports a transition to a state outside the closed set.
var ErrInvalidState = errors.New("hostops: invalid state")

// InterruptedNote is the terminal note boot writes into every record it moves
// to StateInterrupted: spec §7's "note naming the crash".
const InterruptedNote = "interrupted: the hub crashed or restarted while this operation was in flight"

// validateRecord checks one record against the store's schema. Records reach
// the store only through Create and Transition, so this is the single place the
// schema's field-level rules live; validateSnapshot adds the cross-record ones.
func validateRecord(record Record) error {
	if record.ID == "" {
		return fmt.Errorf("%w: empty record id", ErrInvalidRecord)
	}
	if _, err := parseAllocatorID(record.ID); err != nil {
		return fmt.Errorf("%w: record id %q is not a controller-assigned id", ErrInvalidRecord, record.ID)
	}
	if record.ClientOperationID == "" {
		return fmt.Errorf("%w: record %q carries no client operation id", ErrInvalidRecord, record.ID)
	}
	if len(record.ClientOperationID) > MaxClientOperationIDBytes {
		return fmt.Errorf("%w: record %q client operation id is %d bytes, over the %d-byte bound",
			ErrInvalidRecord, record.ID, len(record.ClientOperationID), MaxClientOperationIDBytes)
	}
	if record.Host == "" {
		return fmt.Errorf("%w: record %q names no host", ErrInvalidRecord, record.ID)
	}
	if !record.Kind.Valid() {
		return fmt.Errorf("%w: record %q has kind %q", ErrInvalidRecord, record.ID, record.Kind)
	}
	if !record.State.Valid() {
		return fmt.Errorf("%w: record %q has state %q", ErrInvalidRecord, record.ID, record.State)
	}
	if record.Generation == 0 {
		return fmt.Errorf("%w: record %q pins no generation", ErrInvalidRecord, record.ID)
	}
	if record.IncarnationID == "" {
		return fmt.Errorf("%w: record %q pins no incarnation id", ErrInvalidRecord, record.ID)
	}
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: record %q carries no timestamps", ErrInvalidRecord, record.ID)
	}
	if len(record.FencingEpoch) > 0 && !json.Valid(record.FencingEpoch) {
		return fmt.Errorf("%w: record %q carries an unparseable fencing epoch", ErrInvalidRecord, record.ID)
	}
	if len(record.OrphanBoundary) > 0 && !json.Valid(record.OrphanBoundary) {
		return fmt.Errorf("%w: record %q carries an unparseable orphan boundary", ErrInvalidRecord, record.ID)
	}
	switch {
	case record.State == StateOrphanUnverified && len(record.OrphanBoundary) == 0:
		return fmt.Errorf("%w: orphan-unverified record %q carries no boundary", ErrInvalidRecord, record.ID)
	case record.State != StateOrphanUnverified && len(record.OrphanBoundary) > 0:
		return fmt.Errorf("%w: record %q carries an orphan boundary in state %q", ErrInvalidRecord, record.ID, record.State)
	}
	for _, entry := range record.Progress {
		if entry.TS.IsZero() || entry.Message == "" {
			return fmt.Errorf("%w: record %q carries an empty progress entry", ErrInvalidRecord, record.ID)
		}
	}
	if record.Result != nil && record.Result.Message == "" {
		return fmt.Errorf("%w: record %q carries an empty terminal result", ErrInvalidRecord, record.ID)
	}
	switch {
	case record.Sequence > 0 && !record.State.Terminal():
		return fmt.Errorf("%w: record %q carries sequence %d in non-terminal state %q",
			ErrInvalidRecord, record.ID, record.Sequence, record.State)
	case record.Sequence == 0 && record.State.Terminal():
		return fmt.Errorf("%w: terminal record %q carries no sequence stamp", ErrInvalidRecord, record.ID)
	}
	return nil
}

// cloneRecord copies a record deeply enough that a caller holding the returned
// value cannot reach into the store's in-memory record.
func cloneRecord(record Record) Record {
	out := record
	if record.FencingEpoch != nil {
		out.FencingEpoch = append(json.RawMessage(nil), record.FencingEpoch...)
	}
	if record.OrphanBoundary != nil {
		out.OrphanBoundary = append(json.RawMessage(nil), record.OrphanBoundary...)
	}
	if record.Progress != nil {
		out.Progress = append([]ProgressEntry(nil), record.Progress...)
	}
	if record.Result != nil {
		result := *record.Result
		out.Result = &result
	}
	return out
}
