// Package hostops owns the hub's durable operation store for host lifecycle
// operations: the controller-side record of every `deploy`/`restart` that must
// outlive the AppWire RPC that asked for it (component spec 08b §4), the
// durable per-store state-transition sequence race scans compare, and the boot
// pass that moves a record a crash left `pending`/`running` to `interrupted`
// (§7).
//
// What this package deliberately does not own, because the spec hands each to a
// later slice: the per-host gate (§5), the custody-first quarantine of a corrupt
// store file (§4, crash-fencing spec), and the live `quarantineEpoch` (S8). Its
// record schema, states, one atomic write discipline, one store mutex, and load
// are the substrate those paths stand on; dedup and the create-from-consume
// write (§6), operations pagination (§8), and retention and compaction with
// the dedup tombstones (§4) have landed on top of it.
//
// The confirmation token's durable half (§3) lives in token.go: the row schema,
// the mint write's supersede rule, the validate/consume pass, the lazy and boot
// reaps, live-remove revocation, and the wall-clock rollback guard. What deploy
// does with a consumed token — dedup, the probe epoch, the operation record —
// belongs to the slices that own those paths.
//
// The per-host boundary record — the registry's current
// {generation, incarnationId, presenceEpoch} triple per host name (§7), written
// by the same hub.toml writes that change the pair — lives in boundary.go. The
// store owns the record's shape and its one atomic mirror write; the boot
// reconciliation of a mirror that trails hub.toml (deploy-pipeline §4's commit
// marker rule) and cursor validation (§8) belong to the slices that read it.
//
// Corrupt or unreadable store at this layer: spec §4 quarantines a corrupt or
// schema-invalid store file in custody-first order, and §7 runs the operation
// store's load before anything else at boot. That custody hand-off belongs to
// the crash-fencing slice; until it lands, Open refuses to load a corrupt or
// unreadable file (ErrStoreCorrupt for a file that parses wrong, the wrapped
// I/O error otherwise) and the caller must not serve hosts from it.
package hostops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// MaxClientOperationIDBytes is spec §1's bound on a client operation ID: an
// opaque, non-empty value of at most 128 bytes with no required structure.
const MaxClientOperationIDBytes = 128

// MaxHostNameBytes bounds a host name this store persists. 255 is the
// conventional host-name ceiling and is far above any operator-chosen name,
// while an unbounded name would let a malformed record or tombstone inflate
// the store file past its byte bound.
const MaxHostNameBytes = 255

// MaxOperationMessageBytes bounds one progress entry's message and a terminal
// result's message. 64 KiB is far above any progress line or outcome summary
// the worker paths emit, while an unbounded message would let a single record
// or tombstone dominate the store file.
const MaxOperationMessageBytes = 64 << 10

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
	// PendingSpawns is crash-fencing §3's `pending-spawn` intent set: one entry
	// per spawned subprocess whose ownership boundary the controller
	// pre-created but has not yet proven clean. Each entry holds the per-spawn
	// nonce, the pre-spawn boundary identity, and — once the launcher observed
	// it — the spawned child's kernel-owned (pid, start time) instance marker.
	// The intent is present exactly while it is open: it is written before the
	// spawn, matched after the spawn, and dropped only by a clean local reap or
	// by orphan-resolve (§3). An empty set marshals as absent, so a record
	// carrying no open intent never keeps the key. See spawnintent.go for the
	// lifecycle and the store APIs.
	PendingSpawns []SpawnIntent   `json:"pendingSpawns,omitempty"`
	Progress      []ProgressEntry `json:"progress,omitempty"`
	Result        *Result         `json:"result,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
	HostRemoved   bool            `json:"hostRemoved"`
	// Sequence is the store's state-transition sequence value the record was
	// stamped with when it entered a terminal state; 0 until then.
	Sequence uint64 `json:"sequence,omitempty"`
	// Compacted marks a read-only replay record rebuilt from a dedup tombstone
	// (§4's "`compacted: true`"): the operation completed and its terminal
	// record was compacted, and this value is the retained replay. It is never
	// a stored record — the file's schema carries no such field — so it is
	// excluded from every marshal.
	Compacted bool `json:"-"`
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

// ErrRecordTerminal reports a transition out of a terminal state. A terminal
// record is finished: allowing it to move again would let an operation's
// outcome be rewritten and its sequence stamp replaced.
var ErrRecordTerminal = errors.New("hostops: record is already terminal")

// ErrInvalidState reports a transition to a state outside the closed set.
var ErrInvalidState = errors.New("hostops: invalid state")

// ErrInvalidTransition reports a transition the spec's state graph forbids:
// today, resolving an `orphan-unverified` record to anything but `interrupted`.
var ErrInvalidTransition = errors.New("hostops: invalid state transition")

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
	if len(record.Host) > MaxHostNameBytes {
		return fmt.Errorf("%w: record %q carries a %d-byte host name, over the %d-byte bound",
			ErrInvalidRecord, record.ID, len(record.Host), MaxHostNameBytes)
	}
	// Every string this store persists must be valid UTF-8: encoding/json
	// replaces invalid bytes with U+FFFD on the way out, so a value the store
	// accepted would come back changed after a reload, and the file would hold
	// something the adopted state never had. Identities are the sharpest case —
	// §4 keys dedup on the client operation ID and pins the host name.
	for _, text := range []struct {
		value string
		what  string
	}{
		{record.ClientOperationID, "client operation id"},
		{record.Host, "host"},
		{record.IncarnationID, "incarnation id"},
	} {
		if !utf8.ValidString(text.value) {
			return fmt.Errorf("%w: record %q %s is not valid UTF-8", ErrInvalidRecord, record.ID, text.what)
		}
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
	// The raw fields are written verbatim, so they carry the same UTF-8 rule as
	// every other persisted string: invalid bytes in one would land in the file
	// and the next load would refuse the store this call just wrote.
	if !utf8.Valid(record.FencingEpoch) {
		return fmt.Errorf("%w: record %q carries a fencing epoch that is not valid UTF-8", ErrInvalidRecord, record.ID)
	}
	if !utf8.Valid(record.OrphanBoundary) {
		return fmt.Errorf("%w: record %q carries an orphan boundary that is not valid UTF-8", ErrInvalidRecord, record.ID)
	}
	// The loader refuses a file that names any key twice, so the write path must
	// refuse a raw field that does: writing one would brick the store on the next
	// boot — the file the store just committed would fail its own load.
	for _, field := range []struct {
		raw  json.RawMessage
		what string
	}{
		{record.FencingEpoch, "fencing epoch"},
		{record.OrphanBoundary, "orphan boundary"},
	} {
		if err := validateRawFieldKeys(field.raw); err != nil {
			return fmt.Errorf("%w: record %q carries a %s that names a key twice: %w",
				ErrInvalidRecord, record.ID, field.what, err)
		}
	}
	if len(record.FencingEpoch) > 0 && !jsonFieldIsObject(record.FencingEpoch) {
		return fmt.Errorf("%w: record %q carries a fencing epoch that is not an object", ErrInvalidRecord, record.ID)
	}
	// Spec §4: "The `orphan-unverified` variant carries the per-member
	// `BoundaryEntry[]` array" — present, and an array. Null, a scalar or an
	// object is not a boundary this store ever wrote, and an empty array is a
	// legitimate boundary, not a missing one: crash-fencing §5's clean rule reads
	// a `local-markerless` boundary as clean only "when demonstrably empty", and
	// §3 records that "an empty boundary is already clean". The fail-closed
	// marker for a lost boundary is the `boundary-unavailable` entry (§9), never
	// an absent array.
	switch {
	case record.State == StateOrphanUnverified && !jsonFieldIsArray(record.OrphanBoundary):
		return fmt.Errorf("%w: orphan-unverified record %q carries no boundary array", ErrInvalidRecord, record.ID)
	case record.State != StateOrphanUnverified && len(record.OrphanBoundary) > 0:
		return fmt.Errorf("%w: record %q carries an orphan boundary in state %q", ErrInvalidRecord, record.ID, record.State)
	}
	// §3's pending-spawn intents are schema-checked like every other persisted
	// value: an intent no writer emits never enters the file, so the boot reap
	// never has to guess what a malformed one meant. The set is checked in every
	// state: an operation can finish while its spawn's boundary is still open,
	// and the intent stays until the reap or resolve drops it.
	if len(record.PendingSpawns) > MaxPendingSpawnsPerRecord {
		return fmt.Errorf("%w: record %q carries %d pending-spawn intents, over the %d bound",
			ErrInvalidRecord, record.ID, len(record.PendingSpawns), MaxPendingSpawnsPerRecord)
	}
	nonces := make(map[string]struct{}, len(record.PendingSpawns))
	for _, intent := range record.PendingSpawns {
		if err := validateSpawnIntent(intent); err != nil {
			return fmt.Errorf("%w: record %q: %w", ErrInvalidRecord, record.ID, err)
		}
		if _, duplicate := nonces[intent.Nonce]; duplicate {
			return fmt.Errorf("%w: record %q carries nonce %q twice", ErrInvalidRecord, record.ID, intent.Nonce)
		}
		nonces[intent.Nonce] = struct{}{}
	}
	for _, entry := range record.Progress {
		if entry.TS.IsZero() || entry.Message == "" {
			return fmt.Errorf("%w: record %q carries an empty progress entry", ErrInvalidRecord, record.ID)
		}
		if len(entry.Message) > MaxOperationMessageBytes {
			return fmt.Errorf("%w: record %q carries a %d-byte progress message, over the %d-byte bound",
				ErrInvalidRecord, record.ID, len(entry.Message), MaxOperationMessageBytes)
		}
		if !utf8.ValidString(entry.Message) {
			return fmt.Errorf("%w: record %q carries a progress entry that is not valid UTF-8", ErrInvalidRecord, record.ID)
		}
	}
	if len(record.Progress) > MaxProgressEntries {
		return fmt.Errorf("%w: record %q carries %d progress entries, over the %d-entry bound",
			ErrInvalidRecord, record.ID, len(record.Progress), MaxProgressEntries)
	}
	// A result is terminal data: it belongs to a record that has finished — and
	// every terminal record carries one (spec §10: "`result` is present exactly
	// on terminal records"), so a terminal record without its outcome is not a
	// value this store writes and never enters the file. A running or pending
	// record carrying one would render a finished outcome for an operation
	// still in flight.
	if record.State.Terminal() && record.Result == nil {
		return fmt.Errorf("%w: terminal record %q carries no terminal result", ErrInvalidRecord, record.ID)
	}
	if record.Result != nil {
		if !record.State.Terminal() {
			return fmt.Errorf("%w: record %q carries a terminal result in state %q",
				ErrInvalidRecord, record.ID, record.State)
		}
		if record.Result.Message == "" {
			return fmt.Errorf("%w: record %q carries an empty terminal result", ErrInvalidRecord, record.ID)
		}
		if len(record.Result.Message) > MaxOperationMessageBytes {
			return fmt.Errorf("%w: record %q carries a %d-byte terminal result message, over the %d-byte bound",
				ErrInvalidRecord, record.ID, len(record.Result.Message), MaxOperationMessageBytes)
		}
		if !utf8.ValidString(record.Result.Message) {
			return fmt.Errorf("%w: record %q carries a terminal result that is not valid UTF-8", ErrInvalidRecord, record.ID)
		}
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

// jsonFieldIsObject reports whether a raw field carries a JSON object: present,
// not the literal null, and an object. The fencing epoch's shape belongs to the
// crash-fencing spec, so this checks the outer form only.
func jsonFieldIsObject(raw json.RawMessage) bool {
	return !jsonFieldIsNull(raw) && json.Unmarshal(raw, &map[string]json.RawMessage{}) == nil
}

// jsonFieldIsArray reports whether a raw field carries a JSON array: the outer
// form of §4's per-member `BoundaryEntry[]`. An empty array is an array — the
// clean rule in crash-fencing §5 reads one as demonstrably empty — while an
// absent field, the literal null, an object and a scalar are not arrays at all.
func jsonFieldIsArray(raw json.RawMessage) bool {
	if jsonFieldIsNull(raw) {
		return false
	}
	var members []json.RawMessage
	return json.Unmarshal(raw, &members) == nil
}

// jsonFieldIsNull reports whether a raw field is the JSON literal null. An
// omitted field is empty raw bytes, which is a different thing: absent means
// absent, and present-null is a value no writer of this store emits.
func jsonFieldIsNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
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
	if record.PendingSpawns != nil {
		out.PendingSpawns = cloneSpawnIntents(record.PendingSpawns)
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
