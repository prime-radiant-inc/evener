package hostops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/spf13/afero"

	"primeradiant.com/evener/cmd/evener-hub/internal/fsdurability"
)

const (
	// storeVersion is the on-disk store-file version. A file carrying any other
	// version is schema-invalid, never a guess.
	storeVersion = 1
	// storeSubdir and storeFilename place the store under the hub's state root
	// beside the other durable stores. The spec's quarantine intent and custody
	// files land beside it in the same directory.
	storeSubdir   = "hostops"
	storeFilename = "operations.json"
	// allocatorIDWidth is the fixed width controller-assigned ids are written
	// with. Spec §8 sorts and resumes records by `id` ascending, and the wire
	// carries the id as a string, so allocation order has to equal string order:
	// fixed-width decimal is the one format where it does, for every consumer,
	// without a later slice having to know the id is a number. The loader accepts
	// only this form (parseAllocatorID), because any other width breaks that
	// order.
	allocatorIDWidth = 20
)

// storeFaults are the failure-path seams of the atomic write, the same shape
// the repo's other durable stores expose to their tests.
type storeFaults struct {
	beforeRename func() error
	// syncDir, when set, replaces the directory sync that follows the rename —
	// the one failure point at which the write has already replaced the store
	// file, so a test can drive the post-rename path deterministically.
	syncDir func(afero.Fs, string) error
	// The quarantine seams below mark the crash windows of §4's custody-first
	// order, each firing after the named write has landed: afterIntentWrite
	// between the intent and the custody write, afterCustodyWrite between the
	// custody write and the corrupt-file rename, afterRename between the rename
	// and the replacement-store open, and beforeIntentClear between the
	// replacement write and the intent's removal.
	afterIntentWrite  func() error
	afterCustodyWrite func() error
	afterRename       func() error
	beforeIntentClear func() error
}

// postRenameError marks a write failure that followed the rename replacing the
// store file: the new state is already the file's contents, and only the
// directory sync that makes the rename durable failed. A caller must treat it as
// a write that landed — never as a refusal that wrote nothing — because a store
// whose in-memory state is left behind the file would rewrite the file from the
// stale snapshot on its next write, silently reverting what the rename
// committed. The repo's host sidecar store handles the same class the same way.
type postRenameError struct{ err error }

func (e *postRenameError) Error() string { return e.err.Error() }
func (e *postRenameError) Unwrap() error { return e.err }

// RenameLanded reports whether err is a write failure whose rename had already
// replaced the store file. A caller that sees it must reconcile with the durable
// state instead of treating the operation as absent: Create and Transition
// return the record such a write committed, and RecoverInterrupted returns the
// count it moved. Retrying as though nothing was written can open a duplicate
// operation.
//
// What the caller can conclude: the store file holds exactly the state the call
// attempted, and the store's in-memory state agrees with it. What it cannot: that
// the rename is crash-durable — the failure was the directory sync that would
// have made it so, so a crash could still roll the file back to its previous
// contents, which the next boot's load then reads.
func RenameLanded(err error) bool {
	var post *postRenameError
	return errors.As(err, &post)
}

// snapshot is the store file: the version, the durable state-transition
// sequence (§4), the controller-assigned row id allocator's high-water mark,
// and every live record. Retention and compaction (§4) are a later slice, so
// nothing here yet removes a record or bounds the set. Every field is required
// in the file: the kernel of the store is that a file missing one is
// schema-invalid, never silently an empty store. Boundaries is the one
// exception — it arrived after the store shipped, so a file without it (or with
// it null) is a store that has mirrored no boundary yet; see its own comment.
type snapshot struct {
	Version                uint64   `json:"version"`
	Sequence               uint64   `json:"sequence"`
	AllocatorHighWaterMark uint64   `json:"allocatorHighWaterMark"`
	Records                []Record `json:"records"`
	// CompactSeq is §4's durable compaction sequence: advanced by every
	// compacting write, pinned by every minted cursor (§8). Optional on read
	// for the same reason as Boundaries: the key arrived after the store
	// shipped, so a file without it is a store no write has compacted.
	CompactSeq uint64 `json:"compactSeq"`
	// Tombstones is §4's bounded dedup-tombstone set: one per compacted
	// terminal record, the replay source that answers a lost-response retry
	// with `compacted: true`. Optional on read for the same reason.
	Tombstones []Tombstone `json:"tombstones"`
	// CompactionMarks is the bounded compaction ledger §8's refusal reads
	// independently of the dedup tombstones' own bound (see
	// MaxCompactionMarks). Optional on read for the same reason.
	CompactionMarks []CompactionMark `json:"compactionMarks"`
	// CompactionFloor is the highest compacting-write sequence whose ledger
	// marks were evicted by the bound: evidence for a cursor pinned below it is
	// incomplete, and §8's check refuses coarsely rather than skipping. Zero
	// means no mark has ever been dropped.
	CompactionFloor uint64 `json:"compactionFloor"`
	// RemovedHosts records, per name, when the registry's removal tombstone
	// was seen and the pair it tombstoned: §4 compacts a removed host's history
	// first once its removal is past the `tombstoneRetention` horizon, and a
	// tombstone replays for a removed host only while its pinned pair equals
	// this active removed pair. Optional on read for the same reason.
	RemovedHosts map[string]RemovedHost `json:"removedHosts"`
	// Boundaries is the per-host boundary record the registry mirrors (spec 08
	// §7): the {generation, incarnationId, presenceEpoch} triple per host name.
	// Unlike every other field it is optional on read: this key arrived after
	// the store shipped, so a file without it — or with it null — is a store
	// that has mirrored no boundary yet, not a schema-invalid file. Every write
	// emits it as an object.
	Boundaries map[string]Boundary `json:"boundaries"`
	// Tokens is the outstanding confirmation-token row set (deploy-pipeline §3):
	// at most one row per host name, because minting supersedes. Like Boundaries
	// it is optional on read — the key arrived after the store shipped, so a
	// file without it, or with it null, is a store that has minted nothing —
	// and every write emits it as an array.
	Tokens []Token `json:"tokens"`
	// ProbeEpochs is the durable probe-epoch row set (deploy-pipeline §6 step 2):
	// at most one row per host name, superseded by the host's next persist or its
	// token mint and deleted silently at boot. Optional on read for the same
	// reason as Tokens.
	ProbeEpochs []ProbeEpoch `json:"probeEpochs"`
	// ProbeEpochSeq is the durable per-host op-sequence high-water mark the probe
	// epochs are minted from. It survives the rows' supersede and reap, so a
	// sequence is never reused within a controller boot. Optional on read.
	ProbeEpochSeq map[string]uint64 `json:"probeEpochSeq"`
	// GuardEpoch is the fencing epoch the serving hub last admitted from its
	// caller (§10). Optional on read: absent means no epoch was ever presented.
	GuardEpoch *GuardEpoch `json:"guardEpoch"`
	// Compensations is §9's `pendingCompensation` record set: the token-row
	// preimages a removal's commit captured before its purge, keyed by host
	// name, each carrying the phase machine and the stash reference the
	// hub.toml restore applies. A record still open at boot means a swap
	// compensation has not converged, and the boot pass re-runs it by phase.
	// Optional on read for the same reason as Tokens: the key arrived after the
	// store shipped, so a file without it — or with it null — is a store that
	// has armed no compensation.
	Compensations map[string]Compensation `json:"pendingCompensation"`
	// WallClockHighWaterMark is the durable high-water wall clock (§3's rollback
	// guard): the greatest wall-clock value any token pass has observed. It
	// never moves backward, and the zero Time means no pass has observed a clock
	// yet. It is optional on read for the same reason as Tokens.
	WallClockHighWaterMark time.Time `json:"wallClockHighWaterMark"`
}

// storeCell is the lock-and-state cell one store file's handlers share.
//
// mu is the spec §4 store mutex: it serializes every read-modify-write of the
// store within the process, so two racing writers cannot interleave on the same
// temp path or drop each other's record. It is per file, not per handle:
// separate handles for one file would each serialize only their own writes over
// their own snapshot, and the whole-file rewrite every write performs would then
// drop the other handle's records. Lock order is fixed and this package only
// ever holds the innermost lock: the process-wide mutation lock is outermost,
// the store mutex innermost, and no path holding the store mutex acquires the
// mutation lock (this package has no access to it at all).
type storeCell struct {
	mu    sync.Mutex
	state snapshot
	// hasFile records whether a landed write has ever put bytes at the cell's
	// path, or whether a file was there when the cell was created. It is what
	// lets a cached open tell a store that was never written from one whose file
	// has since disappeared.
	hasFile atomic.Bool
	// quarantineEpoch is the durable §4 counter this path's store was opened at:
	// or validates (§8 compares it before any boundary comparison).
	quarantineEpoch uint64
	// quarantine is the operator-visible health signal §4 requires a quarantined
	// store to boot with, nil when no custody file exists for this path.
	quarantine *QuarantineSignal
}

// storeCells holds the process's one cell per store file path. The key is the
// canonical path: Open is the real-filesystem entry point, and one path has one
// filesystem in a process. The afero seam beneath Open serves tests, which drop
// the cell to model the process restart a fresh load belongs to.
var storeCells sync.Map

// storeOpenLocks serializes the first open of each canonical store path. The
// load a first open runs can perform §4's quarantine, so two racing openers of
// one corrupt file must resolve it once: the loser adopts the winner's cell
// instead of writing a second set of artifacts and racing the rename.
var storeOpenLocks sync.Map

// storeOpenLock returns the per-path open lock, creating it on first use.
func storeOpenLock(key string) *sync.Mutex {
	lock, _ := storeOpenLocks.LoadOrStore(key, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// Store is a handle on the operation store: one file, one shared cell, one
// atomic write discipline.
type Store struct {
	path   string
	fs     afero.Fs
	faults storeFaults
	cell   *storeCell
	// clock is the token paths' clock seam (token.go): nil reads the real
	// clock. It is per handle, like faults, so a test can drive token expiry,
	// the wall-clock high-water mark and the rollback guard deterministically.
	// The record paths keep reading nowUTC directly: their timestamps are
	// display-only and never decide a race.
	clock func() time.Time
	// retention is the §4 owner-knob family this handle's compacting writes
	// apply. The zero value is every shipped default (RetentionPolicy's
	// withDefaults), so a plain Open gets the spec's numbers.
	retention RetentionPolicy
}

// StorePath is the operation store's file under stateRoot, beside the hub's
// other durable stores.
func StorePath(stateRoot string) string {
	return filepath.Join(stateRoot, storeSubdir, storeFilename)
}

// Open loads the operation store at path. A missing file is an empty store: a
// fresh install has nothing to recover, and Open writes nothing.
//
// A second Open of a path in the same process shares the first handle's store
// mutex and state, so both handles serialize on one lock over one snapshot.
//
// The call refuses a store file readable beyond its owner, and refuses a file
// that is corrupt or schema-invalid (ErrStoreCorrupt) rather than serving a
// half-understood store.
func Open(path string) (*Store, error) {
	return openFSWithRetention(afero.NewOsFs(), path, storeFaults{}, RetentionPolicy{})
}

// OpenWithRetention opens the operation store at path under one §4
// owner-knob set. Every knob's zero value takes its documented default; a
// store opened with the zero policy behaves exactly like Open.
func OpenWithRetention(path string, policy RetentionPolicy) (*Store, error) {
	return openFSWithRetention(afero.NewOsFs(), path, storeFaults{}, policy)
}

// openFS is the construction seam beneath Open: it builds a Store over an
// injected afero.Fs so tests can drive persistence and the write's failure
// paths.
func openFS(fs afero.Fs, path string, faults storeFaults) (*Store, error) {
	return openFSWithRetention(fs, path, faults, RetentionPolicy{})
}

// openFSWithRetention is openFS with §4's owner knobs threaded through.
func openFSWithRetention(fs afero.Fs, path string, faults storeFaults, policy RetentionPolicy) (*Store, error) {
	key, err := canonicalStorePath(path)
	if err != nil {
		return nil, err
	}
	// One first open per path at a time, across the cell lookup and the whole
	// load: the load can perform §4's quarantine (intent, custody, epoch, rename
	// and replacement writes), and a racing opener that arrives second must find
	// the winner's cell rather than resolve the same corrupt file again.
	openLock := storeOpenLock(key)
	openLock.Lock()
	defer openLock.Unlock()
	if existing, held := storeCells.Load(key); held {
		cell := existing.(*storeCell)
		// A cached cell makes no load, but Open still enforces the path rules a
		// fresh open enforces, on the same file the handle will write: the handle
		// must not be handed out for a path that has become a link or a fifo (its
		// rename would replace the link rather than what it names), nor for a
		// store file made readable beyond its owner since the cell was created.
		info, err := lstat(fs, path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if cell.hasFile.Load() {
				// The file the cell was built from is gone — removed, or renamed
				// aside by a quarantine. Serving the records it once held would
				// answer from a store that no longer exists, and the next write
				// would put them back; the durable store is what the file says,
				// and there is no file, so this path starts over. The per-path
				// open lock is not reentrant, so the fresh load runs inline below
				// rather than in a recursive call.
				storeCells.Delete(key)
			} else {
				return &Store{path: path, fs: fs, faults: faults, cell: cell, retention: policy}, nil
			}
		case err != nil:
			return nil, fmt.Errorf("hostops: stat store %s: %w", path, err)
		default:
			if err := rejectNonStoreFileKind(path, info); err != nil {
				return nil, err
			}
			if perm := info.Mode().Perm(); !ownerOnly(perm) {
				return nil, fmt.Errorf("%w: %s has mode %04o", ErrStoreReadableBeyondOwner, path, perm)
			}
			return &Store{path: path, fs: fs, faults: faults, cell: cell, retention: policy}, nil
		}
	}
	fileExists := true
	if _, err := lstat(fs, path); errors.Is(err, os.ErrNotExist) {
		fileExists = false
	}
	state, epoch, signal, err := resolveStoreFS(fs, path, faults)
	if err != nil {
		return nil, err
	}
	if _, err := lstat(fs, path); err == nil {
		// The quarantine paths write a replacement store at the path, so a file
		// that was absent before the load can exist after it.
		fileExists = true
	}
	cell := &storeCell{state: state, quarantineEpoch: epoch, quarantine: signal}
	cell.hasFile.Store(fileExists)
	// Two racing first opens of one path must adopt one cell, never two.
	if existing, loaded := storeCells.LoadOrStore(key, cell); loaded {
		cell = existing.(*storeCell)
	}
	return &Store{path: path, fs: fs, faults: faults, cell: cell, retention: policy}, nil
}

// retentionPolicy is the handle's §4 knob set with every unset value floored
// to its shipped default.
func (s *Store) retentionPolicy() RetentionPolicy {
	return s.retention.withDefaults()
}

// canonicalStorePath is the key two handles for one store file collide on: two
// spellings of one store path — a relative and an absolute one, a state root
// reached through a symlink — must meet on one key, or the two handles would each
// hold their own store mutex and overwrite each other's records.
//
// Every path component that exists is resolved as it is walked, so a link is
// followed wherever it sits in the chain. A component that does not exist ends the
// walk and the tail is kept verbatim (a store whose file, or directory, has not
// been created yet has no second spelling to meet). A link whose target does not
// exist is refused rather than kept as a spelling of its own: the file it points
// at would otherwise be reachable two ways and hold two store mutexes.
func canonicalStorePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("hostops: resolve store path %s: %w", path, err)
	}
	absolute = filepath.Clean(absolute)

	volume := filepath.VolumeName(absolute)
	current := volume + string(filepath.Separator)
	tail := strings.Split(strings.TrimPrefix(absolute, current), string(filepath.Separator))
	for i, name := range tail {
		if name == "" {
			continue
		}
		next := filepath.Join(current, name)
		info, err := os.Lstat(next)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Nothing below this component exists, so the rest of the path is
				// kept exactly as written.
				return filepath.Join(append([]string{current, name}, tail[i+1:]...)...), nil
			}
			return "", fmt.Errorf("hostops: resolve store path %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(next)
			if err != nil {
				return "", fmt.Errorf("hostops: store path %s runs through the link %s, which does not resolve", path, next)
			}
			next = resolved
		}
		current = next
	}
	return current, nil
}

// forgetStore forgets a path's shared cell, so the next Open loads the file from
// disk again. It models a process restart; the package's own tests use it to
// keep their reload assertions honest.
func forgetStore(path string) error {
	key, err := canonicalStorePath(path)
	if err != nil {
		return err
	}
	storeCells.Delete(key)
	return nil
}

// Path is the file the store reads and writes.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Sequence is the durable state-transition sequence: the value every terminal
// transition advances, persisted in the store file in the same write.
func (s *Store) Sequence() uint64 {
	if s == nil {
		return 0
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	return s.cell.state.Sequence
}

// Record returns a copy of the stored record with that controller-assigned id.
func (s *Store) Record(id string) (Record, bool) {
	if s == nil {
		return Record{}, false
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	index := slices.IndexFunc(s.cell.state.Records, func(record Record) bool { return record.ID == id })
	if index < 0 {
		return Record{}, false
	}
	return cloneRecord(s.cell.state.Records[index]), true
}

// RecordOrReplay returns the retained record with that controller-assigned id:
// the live record when it is retained, or the replay rebuilt from its dedup
// tombstone when the record has compacted. Store.Record answers live records
// only; the resolve's replay contract (§5: "retrying the already-resolved
// record's id replays the resolved OperationRecord ... from the persisted
// resolution, never a second transition and never a refusal") needs the
// tombstone-aware form, or a compacted resolved id would read as not-found.
// The rebuilt replay carries Compacted: true, exactly as the read path's
// replays do.
func (s *Store) RecordOrReplay(id string) (Record, bool) {
	if s == nil {
		return Record{}, false
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	record, ok := anchorRecordLocked(&s.cell.state, id)
	if !ok {
		return Record{}, false
	}
	return cloneRecord(record), true
}

// Records returns copies of every stored record, in stored order: ascending id
// order, which is the order spec §8 sorts and resumes by.
func (s *Store) Records() []Record {
	if s == nil {
		return nil
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	return cloneSnapshot(s.cell.state).Records
}

// Create assigns the next controller-assigned id and persists a fresh `pending`
// record for the operation, in one atomic write. Creation is not a terminal
// transition, so it moves no sequence value.
//
// Dedup — recognizing a replay by (host, kind, client operation ID, pinned
// generation, pinned incarnation id) — belongs to the pipeline slice that reads
// the registry's current pair; this is only the durable append beneath it.
func (s *Store) Create(newRecord NewRecord) (Record, error) {
	if s == nil {
		return Record{}, errors.New("hostops: store is not configured")
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	now := nowUTC()
	next := cloneSnapshot(s.cell.state)
	next.AllocatorHighWaterMark++
	record := Record{
		ID:                formatAllocatorID(next.AllocatorHighWaterMark),
		ClientOperationID: newRecord.ClientOperationID,
		Host:              newRecord.Host,
		Kind:              newRecord.Kind,
		State:             StatePending,
		Generation:        newRecord.Generation,
		IncarnationID:     newRecord.IncarnationID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	next.Records = append(next.Records, record)
	adopted, err := s.commitLocked(next)
	if err != nil && !adopted {
		// Nothing was written: the refusal reports no record.
		return Record{}, err
	}
	// A write whose rename landed is the operation's durable record even when
	// the directory sync behind it failed, so the caller gets the id it must
	// reconcile with (see RenameLanded).
	return cloneRecord(record), err
}

// Transition moves one stored record to state to and commits it atomically.
// change, when non-nil, is applied to the record first, inside the same locked
// read-modify-write, so progress, the terminal result, the fencing epoch and
// the host-removed mark land with the state that makes them meaningful.
//
// A terminal record is finished: no transition leaves a terminal state, so an
// operation's outcome can never be rewritten and its sequence stamp can never be
// replaced. The one resolution the spec defines into a terminal state from a
// non-terminal one — `orphan-unverified`→`interrupted` (§4) — is an ordinary
// allowed transition.
//
// Entering a terminal state advances the durable state-transition sequence and
// stamps the record with the value it advanced to (spec §4). A transition to
// any other state stamps nothing.
//
// A refusal — an unknown record, an unknown state, a finished record, an
// `orphan-unverified` record resolving anywhere but `interrupted`, a change that
// rewrites the record's immutable identity, or a change that takes the record
// outside the schema — leaves the store untouched.
func (s *Store) Transition(id string, to State, change func(*Record)) (Record, error) {
	if s == nil {
		return Record{}, errors.New("hostops: store is not configured")
	}
	if !to.Valid() {
		return Record{}, fmt.Errorf("%w: %q", ErrInvalidState, to)
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	next := cloneSnapshot(s.cell.state)
	index := slices.IndexFunc(next.Records, func(record Record) bool { return record.ID == id })
	if index < 0 {
		return Record{}, fmt.Errorf("%w: %q", ErrRecordNotFound, id)
	}
	// The record the callback sees is this call's own value, never a pointer into
	// the snapshot the store may adopt: a callback that retains the pointer could
	// otherwise mutate store state after this call returned, with no lock held
	// and no write, leaving memory and the file disagreeing.
	record := next.Records[index]
	if record.State.Terminal() {
		return Record{}, fmt.Errorf("%w: record %q is already %q", ErrRecordTerminal, id, record.State)
	}
	// An `orphan-unverified` record is historical data this build never creates
	// or resolves; the only edge the substrate still refuses by type is the exit
	// the prior fencing rule named — a record whose boundary was never verified
	// must not read as a success. The rest of the graph (pending→running,
	// in-flight→terminal) is the deploy slice's to drive.
	if record.State == StateOrphanUnverified && to != StateInterrupted {
		return Record{}, fmt.Errorf("%w: orphan-unverified record %q resolves only to %q",
			ErrInvalidTransition, id, StateInterrupted)
	}
	identity := identityOf(record)
	if change != nil {
		change(&record)
	}
	if changed := identityOf(record); changed != identity {
		// A change may carry progress, the terminal result, the fencing epoch
		// and the host-removed mark. The record's durable
		// identity and the store's stamp are not a caller's to rewrite: a record
		// that changes host, kind, pinned pair or id would corrupt the dedup
		// scope §4 keys on, and the sequence stamp is what race scans compare.
		return Record{}, fmt.Errorf("%w: the change rewrote record %q's immutable fields", ErrInvalidRecord, id)
	}
	record.State = to
	record.UpdatedAt = nowUTC()
	if to.Terminal() {
		next.advanceSequence(&record)
	}
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	// The store owns every value it adopts: the record's mutable fields are
	// copied, so progress slices, results and raw messages the callback assigned
	// (or still holds) cannot alias into store state.
	next.Records[index] = cloneRecord(record)
	adopted, err := s.commitLocked(next)
	if err != nil && !adopted {
		return Record{}, err
	}
	// See Create: a landed rename is a durable transition even when the
	// directory sync behind it failed.
	return cloneRecord(record), err
}

// recordIdentity is the part of a record a transition may never change: its
// durable identity (the fields §4's dedup scope and the §10 wire treat as fixed
// for the operation's life) and the store-owned stamp. Comparing one of these
// across a transition's change callback keeps the callback free to mutate
// everything legitimately mutable without a second list to maintain.
type recordIdentity struct {
	id                string
	clientOperationID string
	host              string
	kind              Kind
	generation        uint64
	incarnationID     string
	createdAt         time.Time
	sequence          uint64
}

// identityOf reads the immutable fields off a record.
func identityOf(record Record) recordIdentity {
	return recordIdentity{
		id:                record.ID,
		clientOperationID: record.ClientOperationID,
		host:              record.Host,
		kind:              record.Kind,
		generation:        record.Generation,
		incarnationID:     record.IncarnationID,
		createdAt:         record.CreatedAt,
		sequence:          record.Sequence,
	}
}

// commitLocked persists next and adopts it in memory in step with the file. The
// returned bool says whether the write's rename landed, which is exactly when
// memory adopted next: a failure before the rename wrote nothing and leaves
// memory as it was, while a postRenameError means the rename already replaced
// the file, so memory follows the file even though the write reports the
// failure. Callers must hold mu, and a caller that sees err with landed true must
// reconcile with the state it passed in rather than report the operation absent
// (see RenameLanded).
func (s *Store) commitLocked(next snapshot) (landed bool, err error) {
	// Every mutating path meets §4's retention in this same atomic write: no
	// commit — record, token, probe epoch, boundary or removal-marker mirror —
	// may leave the store over a bound that a later record write would then have
	// to repair. A pass that removes nothing changes nothing.
	s.compactLocked(&next)
	landed, err = saveFS(s.fs, s.path, next, s.faults)
	if landed {
		s.cell.state = next
		s.cell.hasFile.Store(true)
	}
	return landed, err
}

// advanceSequence moves the durable state-transition sequence forward by one and
// stamps record with the value it advanced to. Spec §4 states this once for
// every atomic write that moves a record into a terminal state, so both the
// transition helper and the boot pass go through here and cannot drift apart.
func (next *snapshot) advanceSequence(record *Record) {
	next.Sequence++
	record.Sequence = next.Sequence
}

// storeFile is the decode shape of the store file's top level. Every field is a
// pointer so a file that omits one — or carries null — is refused rather than
// decoded as a zero-valued store: a truncated or hand-edited file must never be
// read as "no records", because the next write would then replace real history
// with nothing.
type storeFile struct {
	Version                *uint64       `json:"version"`
	Sequence               *uint64       `json:"sequence"`
	AllocatorHighWaterMark *uint64       `json:"allocatorHighWaterMark"`
	Records                *[]recordFile `json:"records"`
	// CompactSeq, Tombstones and RemovedHosts are optional on read (see
	// snapshot's comments): absent and null both decode to the pre-S6 state,
	// which is "no compacting write yet".
	CompactSeq      uint64                 `json:"compactSeq"`
	Tombstones      *[]tombstoneFile       `json:"tombstones"`
	CompactionMarks *[]CompactionMark      `json:"compactionMarks"`
	CompactionFloor uint64                 `json:"compactionFloor"`
	RemovedHosts    map[string]RemovedHost `json:"removedHosts"`
	// Boundaries is optional on read (see snapshot.Boundaries): absent and null
	// both decode to nil, which is "no boundary mirrored yet".
	Boundaries map[string]Boundary `json:"boundaries"`
	// FencingQuarantines is retired (comp08 2b): prior-build files carry the
	// per-host fencing-quarantine marker set, so it stays decodable here and is
	// deliberately never mapped into snapshot or emitted by a write.
	FencingQuarantines map[string]json.RawMessage `json:"fencingQuarantines"`
	// Tokens is optional on read (see snapshot.Tokens): absent and null both
	// decode to nil, which is "no token minted yet".
	Tokens *[]tokenFile `json:"tokens"`
	// ProbeEpochs is optional on read (see snapshot.ProbeEpochs).
	ProbeEpochs *[]ProbeEpoch `json:"probeEpochs"`
	// ProbeEpochSeq is optional on read (see snapshot.ProbeEpochSeq).
	ProbeEpochSeq map[string]uint64 `json:"probeEpochSeq"`
	// GuardEpoch is optional on read (see snapshot.GuardEpoch).
	GuardEpoch *GuardEpoch `json:"guardEpoch"`
	// Compensations is optional on read (see snapshot.Compensations): absent and
	// null both decode to nil, which is "no compensation armed yet".
	Compensations map[string]compensationFile `json:"pendingCompensation"`
	// WallClockHighWaterMark is optional on read: absent, null and the zero
	// instant all mean no pass has observed a clock yet.
	WallClockHighWaterMark time.Time `json:"wallClockHighWaterMark"`
}

// compensationFile is the decode shape of one pendingCompensation record. Every
// field is a pointer so a record that omits one — or carries null — is refused
// rather than decoded as a zero-valued record, exactly as the token shape does
// it; rows decode through tokenFile so a preimage row carries the whole token
// schema.
type compensationFile struct {
	Host       *string      `json:"host"`
	Phase      *string      `json:"phase"`
	Rows       *[]tokenFile `json:"rows"`
	Stash      *string      `json:"stashReference"`
	Generation *uint64      `json:"generation"`
}

// compensation maps the decode shape to a record, refusing every omitted field
// and normalizing the retired `compensating-sidecar` spelling to its live
// alias.
func (f compensationFile) compensation() (Compensation, error) {
	for _, required := range []struct {
		present bool
		what    string
	}{
		{f.Host != nil, "host"},
		{f.Phase != nil, "phase"},
		{f.Rows != nil, "rows"},
		{f.Stash != nil, "stash reference"},
		{f.Generation != nil, "generation"},
	} {
		if !required.present {
			return Compensation{}, fmt.Errorf("a compensation record carries no %s", required.what)
		}
	}
	rows := make([]Token, len(*f.Rows))
	for i, encoded := range *f.Rows {
		mapped, err := encoded.token()
		if err != nil {
			return Compensation{}, err
		}
		rows[i] = mapped
	}
	record := Compensation{
		Host:       *f.Host,
		Phase:      CompensationPhase(*f.Phase),
		Rows:       rows,
		Stash:      *f.Stash,
		Generation: *f.Generation,
	}
	if err := validateCompensation(record); err != nil {
		return Compensation{}, err
	}
	return record.normalized(), nil
}

// tombstoneFile is the decode shape of one dedup tombstone. It carries the
// same fields as Tombstone, with a present result decoded through resultFile so
// a tombstone whose result lacks its outcome — a shape the writer never
// produces — is refused rather than read as a failed outcome.
type tombstoneFile struct {
	Tombstone
	Result   json.RawMessage `json:"result"`
	Progress json.RawMessage `json:"progress"`
	// The retired resolved marker and attestation (comp08 2b): prior-build
	// files carry them; decoded for tolerance, never mapped and never emitted.
	OrphanResolved bool            `json:"orphanResolved"`
	Attestation    json.RawMessage `json:"attestation"`
}

// tombstone maps the decode shape to a tombstone, applying the same
// omitted-field rules records carry.
func (f tombstoneFile) tombstone() (Tombstone, error) {
	tombstone := f.Tombstone
	if len(f.Progress) > 0 {
		if jsonFieldIsNull(f.Progress) {
			return Tombstone{}, fmt.Errorf("tombstone %q carries a null progress list", f.ID)
		}
		var progress []ProgressEntry
		decoder := json.NewDecoder(bytes.NewReader(f.Progress))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&progress); err != nil {
			return Tombstone{}, fmt.Errorf("tombstone %q carries an unparseable progress list", f.ID)
		}
		tombstone.Progress = progress
	}
	if len(f.Result) > 0 {
		if jsonFieldIsNull(f.Result) {
			return Tombstone{}, fmt.Errorf("tombstone %q carries a null terminal result", f.ID)
		}
		var result resultFile
		decoder := json.NewDecoder(bytes.NewReader(f.Result))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&result); err != nil {
			return Tombstone{}, fmt.Errorf("tombstone %q carries an unparseable terminal result", f.ID)
		}
		if result.OK == nil {
			return Tombstone{}, fmt.Errorf("tombstone %q carries a terminal result with no outcome", f.ID)
		}
		tombstone.Result = &Result{OK: *result.OK, Message: result.Message}
	}
	return tombstone, nil
}

// recordFile is the decode shape of one record. It carries the same fields as
// Record, except that the fields whose zero value is a legitimate value are
// pointers here: the writer always emits `hostRemoved`, and always emits both
// halves of a present `result`, so a file that omits either is not one this store
// produced and must be refused rather than silently read as `false`. The record
// fields validation already refuses at their zero value (id, host, kind, state,
// the pinned pair, the timestamps) need no pointer.
type recordFile struct {
	Record
	HostRemoved *bool           `json:"hostRemoved"`
	Result      json.RawMessage `json:"result"`
	Progress    json.RawMessage `json:"progress"`
	// The retired crash-fencing fields (comp08 2b): prior-build files carry an
	// orphan boundary, a resolved marker, an operator attestation and the open
	// pending-spawn intent set on a record. They are decoded here — never
	// refused — and deliberately never mapped into Record, so every rewrite
	// drops them.
	OrphanBoundary json.RawMessage `json:"orphanBoundary"`
	OrphanResolved bool            `json:"orphanResolved"`
	Attestation    json.RawMessage `json:"attestation"`
	PendingSpawns  json.RawMessage `json:"pendingSpawns"`
}

// resultFile is the decode shape of a record's terminal result: `ok` is a
// pointer because an absent outcome and a failed outcome are different things,
// while §10's wire carries both halves of a present result.
type resultFile struct {
	OK      *bool  `json:"ok"`
	Message string `json:"message"`
}

// record maps the decode shape to a record, refusing the two omitted-field
// shapes the writer never produces.
func (f recordFile) record() (Record, error) {
	if f.HostRemoved == nil {
		return Record{}, fmt.Errorf("record %q carries no host-removed mark", f.ID)
	}
	record := f.Record
	record.HostRemoved = *f.HostRemoved
	if len(f.Progress) > 0 {
		if jsonFieldIsNull(f.Progress) {
			return Record{}, fmt.Errorf("record %q carries a null progress list", f.ID)
		}
		var progress []ProgressEntry
		decoder := json.NewDecoder(bytes.NewReader(f.Progress))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&progress); err != nil {
			return Record{}, fmt.Errorf("record %q carries an unparseable progress list", f.ID)
		}
		record.Progress = progress
	}
	if len(f.Result) > 0 {
		raw := f.Result
		if jsonFieldIsNull(raw) {
			return Record{}, fmt.Errorf("record %q carries a null terminal result", f.ID)
		}
		var result resultFile
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&result); err != nil {
			return Record{}, fmt.Errorf("record %q carries an unparseable terminal result", f.ID)
		}
		if result.OK == nil {
			return Record{}, fmt.Errorf("record %q carries a terminal result with no outcome", f.ID)
		}
		record.Result = &Result{OK: *result.OK, Message: result.Message}
	}
	return record, nil
}

// loadFS reads and validates the store file. A missing file is an empty store;
// every other failure is reported, never papered over. The whole-file checks
// live in readStoreFS; this adds the store-level invariants.
func loadFS(fs afero.Fs, path string) (snapshot, error) {
	state, err := readStoreFS(fs, path)
	if err != nil {
		return snapshot{}, err
	}
	if err := validateSnapshot(state); err != nil {
		return snapshot{}, fmt.Errorf("%w: validate %s: %w", ErrStoreCorrupt, path, err)
	}
	return state, nil
}

// readStoreFS reads and decodes the store file with every whole-file rule this
// store applies — the byte-level checks, the strict decode, and the per-record
// mapping — without the store-level validation. The quarantine path reads the
// same decode: a corrupt file whose bytes this function refuses cannot yield a
// custody snapshot, because a region of it was left unparsed or discarded.
func readStoreFS(fs afero.Fs, path string) (snapshot, error) {
	empty := snapshot{Version: storeVersion}
	// The kind check runs on the path itself (never following a final link) and
	// before the missing-file case: a dangling symlink reports "missing" to a
	// following stat, and reading it as a fresh empty store is exactly what lets
	// the next write replace the link instead of writing through it.
	info, err := lstat(fs, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil
		}
		return snapshot{}, fmt.Errorf("hostops: stat store %s: %w", path, err)
	}
	if err := rejectNonStoreFileKind(path, info); err != nil {
		return snapshot{}, err
	}
	if perm := info.Mode().Perm(); !ownerOnly(perm) {
		return snapshot{}, fmt.Errorf("%w: %s has mode %04o", ErrStoreReadableBeyondOwner, path, perm)
	}
	raw, err := afero.ReadFile(fs, path)
	if err != nil {
		return snapshot{}, fmt.Errorf("hostops: read store %s: %w", path, err)
	}
	if err := checkStoreBytes(raw); err != nil {
		return snapshot{}, fmt.Errorf("%w: %s: %w", ErrStoreCorrupt, path, err)
	}
	var file storeFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return snapshot{}, fmt.Errorf("%w: decode %s: %w", ErrStoreCorrupt, path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return snapshot{}, fmt.Errorf("%w: %s carries a trailing JSON value", ErrStoreCorrupt, path)
		}
		return snapshot{}, fmt.Errorf("%w: decode %s trailing data: %w", ErrStoreCorrupt, path, err)
	}
	switch {
	case file.Version == nil:
		return snapshot{}, fmt.Errorf("%w: %s carries no version", ErrStoreCorrupt, path)
	case file.Sequence == nil:
		return snapshot{}, fmt.Errorf("%w: %s carries no state-transition sequence", ErrStoreCorrupt, path)
	case file.AllocatorHighWaterMark == nil:
		return snapshot{}, fmt.Errorf("%w: %s carries no allocator high-water mark", ErrStoreCorrupt, path)
	case file.Records == nil:
		return snapshot{}, fmt.Errorf("%w: %s carries no record list", ErrStoreCorrupt, path)
	}
	records := make([]Record, len(*file.Records))
	for i, record := range *file.Records {
		mapped, err := record.record()
		if err != nil {
			return snapshot{}, fmt.Errorf("%w: %s: %w", ErrStoreCorrupt, path, err)
		}
		records[i] = mapped
	}
	var tokens []Token
	if file.Tokens != nil {
		tokens = make([]Token, len(*file.Tokens))
		for i, row := range *file.Tokens {
			mapped, err := row.token()
			if err != nil {
				return snapshot{}, fmt.Errorf("%w: %s: %w", ErrStoreCorrupt, path, err)
			}
			tokens[i] = mapped
		}
	}
	var compensations map[string]Compensation
	if file.Compensations != nil {
		compensations = make(map[string]Compensation, len(file.Compensations))
		for host, encoded := range file.Compensations {
			record, err := encoded.compensation()
			if err != nil {
				return snapshot{}, fmt.Errorf("%w: %s: %w", ErrStoreCorrupt, path, err)
			}
			if record.Host != host {
				return snapshot{}, fmt.Errorf("%w: compensation[%q] names %q in its record", ErrStoreCorrupt, host, record.Host)
			}
			compensations[host] = record
		}
	}
	state := snapshot{
		Version:                *file.Version,
		Sequence:               *file.Sequence,
		AllocatorHighWaterMark: *file.AllocatorHighWaterMark,
		Records:                records,
		CompactSeq:             file.CompactSeq,
		Boundaries:             file.Boundaries,
		RemovedHosts:           file.RemovedHosts,
		Tokens:                 tokens,
		Compensations:          compensations,
		ProbeEpochSeq:          file.ProbeEpochSeq,
		GuardEpoch:             file.GuardEpoch,
		// The mark is normalized like every stored timestamp: an offset form
		// converts to UTC, and anything before the Unix epoch — including the
		// year-one string a zero mark marshals to — reads as "no mark yet"
		// (wallClockMark).
		WallClockHighWaterMark: wallClockMark(file.WallClockHighWaterMark),
	}
	if file.Tombstones != nil {
		state.Tombstones = make([]Tombstone, len(*file.Tombstones))
		for i, encoded := range *file.Tombstones {
			tombstone, err := encoded.tombstone()
			if err != nil {
				return snapshot{}, fmt.Errorf("%w: %s: %w", ErrStoreCorrupt, path, err)
			}
			tombstone.CreatedAt = tombstone.CreatedAt.UTC()
			tombstone.UpdatedAt = tombstone.UpdatedAt.UTC()
			tombstone.CompactedAt = tombstone.CompactedAt.UTC()
			state.Tombstones[i] = tombstone
		}
	}
	if file.CompactionMarks != nil {
		state.CompactionMarks = append([]CompactionMark(nil), (*file.CompactionMarks)...)
	}
	state.CompactionFloor = file.CompactionFloor
	for name, removed := range state.RemovedHosts {
		removed.RemovedAt = removed.RemovedAt.UTC()
		state.RemovedHosts[name] = removed
	}
	if file.ProbeEpochs != nil {
		state.ProbeEpochs = make([]ProbeEpoch, len(*file.ProbeEpochs))
		for i, row := range *file.ProbeEpochs {
			// The row's creation timestamp is normalized like every other stored
			// timestamp, so a hand-edited offset form never survives a rewrite.
			row.CreatedAt = row.CreatedAt.UTC()
			state.ProbeEpochs[i] = row
		}
	}
	// Spec §8: "`createdAt`/`updatedAt` are stored UTC-normalized (`Z`-suffixed
	// RFC3339; a stored offset form converts at write time)". Values this store
	// originates are already UTC; a record read from a hand-edited, migrated or
	// custody-imported file is normalized here, so its offset form never survives
	// into a later write.
	for i := range state.Records {
		state.Records[i].CreatedAt = state.Records[i].CreatedAt.UTC()
		state.Records[i].UpdatedAt = state.Records[i].UpdatedAt.UTC()
	}
	return state, nil
}

// saveFS writes the store atomically: a temp file in the store's own directory,
// fsynced, renamed over the store, then the directory entry the rename landed
// in fsynced — the discipline every durable store in this repo follows. The
// temp file is created owner-only and is removed on every path that does not
// rename it.
//
// The rename is the write's commit point, so the return value says whether it
// landed: a caller must adopt the state it passed in whenever renamed is true,
// even when err is non-nil (a postRenameError whose file already holds that
// state).
func saveFS(fs afero.Fs, path string, state snapshot, faults storeFaults) (renamed bool, err error) {
	if err := validateSnapshot(state); err != nil {
		return false, err
	}
	if state.Boundaries == nil {
		// A store that has mirrored nothing writes an empty object, never null:
		// the key is always present in a file this store wrote, so absent/null
		// stays what it is — the pre-boundary file shape.
		state.Boundaries = map[string]Boundary{}
	}
	if state.Tokens == nil {
		// Same rule for the token rows: a store that has minted nothing writes an
		// empty array, never null.
		state.Tokens = []Token{}
	}
	if state.Tombstones == nil {
		// Same rule for the dedup tombstones: a store that has compacted nothing
		// writes an empty array, never null.
		state.Tombstones = []Tombstone{}
	}
	if state.CompactionMarks == nil {
		// Same rule for the compaction ledger.
		state.CompactionMarks = []CompactionMark{}
	}
	if state.RemovedHosts == nil {
		// Same rule for the removal markers: a store that has seen no removal
		// writes an empty object, never null.
		state.RemovedHosts = map[string]RemovedHost{}
	}
	if state.Compensations == nil {
		// Same rule for the armed compensation records: a store that has armed
		// none writes an empty object, never null.
		state.Compensations = map[string]Compensation{}
	} else {
		// A record's preimage is a list: an empty one writes an empty array,
		// never null, so the key's shape never depends on whether the purge had
		// rows to carry.
		for host, record := range state.Compensations {
			if record.Rows == nil {
				record.Rows = []Token{}
				state.Compensations[host] = record
			}
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return false, fmt.Errorf("hostops: marshal store: %w", err)
	}
	// The bytes about to be committed are held to the loader's own rules, so a
	// write can never land a file this store's next boot refuses: every rule the
	// load runs lives in checkStoreBytes and runs here too.
	if err := checkStoreBytes(data); err != nil {
		return false, fmt.Errorf("hostops: refuse to write a store this store cannot load: %w", err)
	}
	dir := filepath.Dir(path)
	sync := syncDirFS
	if faults.syncDir != nil {
		sync = faults.syncDir
	}
	if err := ensureStoreDir(fs, dir, sync); err != nil {
		return false, err
	}
	temp, err := afero.TempFile(fs, dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("hostops: create temp store: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
		if !renamed {
			_ = fs.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		return false, fmt.Errorf("hostops: write temp store: %w", err)
	}
	// Spec §4: "Replacements preserve the mode." The temp file is created 0600;
	// when the store it replaces already carried a stricter owner-only,
	// owner-readable mode, that mode is what the replacement lands with — applied
	// before the sync below, so the mode the replacement carries is covered by the
	// fsync the write already pays for.
	if perm, ok := preservedMode(fs, path); ok {
		if err := fs.Chmod(tempPath, perm); err != nil {
			return false, fmt.Errorf("hostops: preserve store mode: %w", err)
		}
	}
	if err := temp.Sync(); err != nil && !fsdurability.SyncUnsupported(err) {
		return false, fmt.Errorf("hostops: sync temp store: %w", err)
	}
	if err := temp.Close(); err != nil {
		return false, fmt.Errorf("hostops: close temp store: %w", err)
	}
	temp = nil
	if faults.beforeRename != nil {
		if err := faults.beforeRename(); err != nil {
			return false, err
		}
	}
	// The fail-closed kind rule is re-run here, immediately before the rename: a
	// path that was a regular store file when it was opened can be swapped for a
	// link or a fifo since, and the rename would replace it instead of refusing.
	// The gap between this check and the rename cannot be closed on the afero
	// seam — there is no portable rename-onto-a-regular-file-only — so this
	// narrows the window to that gap and makes every non-racy case refuse.
	kind, err := lstat(fs, path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return false, fmt.Errorf("hostops: stat store %s: %w", path, err)
	default:
		if err := rejectNonStoreFileKind(path, kind); err != nil {
			return false, err
		}
	}
	if err := fs.Rename(tempPath, path); err != nil {
		return false, fmt.Errorf("hostops: rename store: %w", err)
	}
	renamed = true
	if err := sync(fs, dir); err != nil {
		return true, &postRenameError{err: err}
	}
	return true, nil
}

// ensureStoreDir creates the store's directory and every missing level above it,
// syncing the parent entry of each level as it goes: a fresh state root can need
// several levels at once, and a crash before a level's parent entry is durable can
// lose that level, the store and the record with it. Each level's parent is synced
// on every write, not only on creation, so a directory an earlier attempt created
// before its parent sync failed converges instead of staying unprovable.
//
// This is the discipline the hub's recovery store already follows
// (createRecoveryDirectory); the chain is walked top-down and stops at the deepest
// level that exists.
func ensureStoreDir(fs afero.Fs, dir string, sync func(afero.Fs, string) error) error {
	parent := filepath.Dir(dir)
	if parent != dir {
		if _, err := lstat(fs, parent); errors.Is(err, os.ErrNotExist) {
			if err := ensureStoreDir(fs, parent, sync); err != nil {
				return err
			}
		} else if err != nil {
			return fmt.Errorf("hostops: stat store directory %s: %w", parent, err)
		}
	}
	info, err := lstat(fs, dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := fs.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("hostops: create store directory: %w", err)
		}
	case err != nil:
		return fmt.Errorf("hostops: stat store directory %s: %w", dir, err)
	default:
		if err := ensureDirectory(fs, dir, info); err != nil {
			return err
		}
	}
	// The parent entry that carries each level of the store's own chain is synced
	// on every write, not only on creation: a level an earlier attempt created
	// before its parent's sync landed has to converge, and a crash before that
	// entry is durable loses the level and the store with it. The chain is bounded
	// by the store's own shape — the `hostops` directory and the state root that
	// carries it — and levels above that belong to whoever created the state root.
	levels := []string{dir}
	if parent := filepath.Dir(dir); parent != dir {
		levels = append(levels, parent)
	}
	for _, level := range levels {
		parent := filepath.Dir(level)
		if parent == level {
			continue
		}
		if _, err := lstat(fs, parent); err != nil {
			continue
		}
		if err := sync(fs, parent); err != nil {
			return err
		}
	}
	return nil
}

// ensureDirectory refuses a path that exists but is not a directory this store can
// write in. A symlink that resolves to a directory counts as one: the temporary
// file and the rename both resolve through it and the link itself is never
// replaced — the difference between the store directory (allowed) and the store
// file (refused, because there the rename would replace the link).
func ensureDirectory(fs afero.Fs, dir string, info os.FileInfo) error {
	if info.IsDir() {
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		followed, err := fs.Stat(dir)
		if err != nil {
			return fmt.Errorf("hostops: store directory %s does not resolve to a directory", dir)
		}
		if followed.IsDir() {
			return nil
		}
	}
	return fmt.Errorf("hostops: store parent %q is not a directory", dir)
}

// syncDirFS opens dir, syncs it and closes it: the durability half of the
// atomic-rename idiom, so a crash right after the rename cannot lose it. Some
// filesystems cannot sync a directory at all, and failing the whole store there
// would turn a durability nicety into a hard outage; every other sync failure is
// real and reported.
func syncDirFS(fs afero.Fs, dir string) error {
	directory, err := fs.Open(dir)
	if err != nil {
		return fmt.Errorf("hostops: open store directory: %w", err)
	}
	if err := directory.Sync(); err != nil && !fsdurability.SyncUnsupported(err) {
		_ = directory.Close()
		return fmt.Errorf("hostops: sync store directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("hostops: close store directory: %w", err)
	}
	return nil
}

// preservedMode returns the mode a replacement has to land with, when the store
// file already exists with an owner-only, owner-readable mode: 0600 needs no
// chmod, a wider mode is normalized to 0600 rather than preserved.
func preservedMode(fs afero.Fs, path string) (os.FileMode, bool) {
	info, err := fs.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	perm := info.Mode().Perm()
	if !ownerOnly(perm) || perm&0o400 == 0 || perm == 0o600 {
		return 0, false
	}
	return perm, true
}

// ownedObjectKeys is the canonical field set of every object this store itself
// decodes, keyed by the object's path inside the file. An object not named here is
// opaque — a raw field's interior, whose schema this store does not own — so
// its keys are not this store's to judge.
//
// The crash-fencing keys (fencingQuarantines, orphanBoundary, orphanResolved,
// attestation, pendingSpawns and their sub-objects) are retired as of comp08 2b:
// the names stay here so a prior-build file still loads, while the decode-only
// fields that read them are never mapped into the domain model and every
// rewrite drops them.
var ownedObjectKeys = map[string]map[string]struct{}{
	"": keysOf("version", "sequence", "allocatorHighWaterMark", "records", "boundaries",
		"tokens", "probeEpochs", "probeEpochSeq", "guardEpoch", "wallClockHighWaterMark",
		"compactSeq", "tombstones", "compactionMarks", "compactionFloor", "removedHosts",
		"pendingCompensation", "fencingQuarantines"),
	"records[]": keysOf("id", "clientOperationId", "host", "kind", "state", "generation",
		"incarnationId", "fencingEpoch", "orphanBoundary", "orphanResolved", "attestation",
		"progress", "result", "pendingSpawns", "createdAt", "updatedAt", "hostRemoved", "sequence"),
	// A prior-build record's pending-spawn intents and attestation stay decodable
	// so their key spellings are judged rather than silently rewritten.
	"records[].pendingSpawns[]": keysOf("nonce", "platform", "cgroupId", "pgid", "sessionId", "pid", "startTime"),
	"records[].attestation":     keysOf("operator", "statement", "recordId", "boundaryRef", "observedAt", "unattributed"),
	"records[].result":          keysOf("ok", "message"),
	"records[].progress[]":      keysOf("ts", "message"),
	"tombstones[]": keysOf("id", "clientOperationId", "host", "kind", "state", "generation",
		"incarnationId", "orphanResolved", "attestation", "progress", "result", "createdAt", "updatedAt",
		"hostRemoved", "compactedAt", "compactedSeq"),
	"tombstones[].attestation": keysOf("operator", "statement", "recordId", "boundaryRef", "observedAt", "unattributed"),
	"tombstones[].result":      keysOf("ok", "message"),
	"tombstones[].progress[]":  keysOf("ts", "message"),
	"compactionMarks[]":        keysOf("seq", "hosts"),
	// The per-name removal markers are objects this store decodes, so their
	// keys are canonical too: a case variant (Go matches JSON field names
	// case-insensitively) would be silently rewritten on the next save.
	"removedHosts[]":       keysOf("removedAt", "generation", "incarnationId"),
	"boundaries[]":         keysOf("generation", "incarnationId", "presenceEpoch"),
	"fencingQuarantines[]": keysOf("recordId", "quarantinedAt"),
	"tokens[]": keysOf("host", "value", "generation", "incarnationId", "entryHash",
		"hubTomlFingerprint", "factsRevision", "factsCapturedAt", "targetPath",
		"controllerRevision", "runningVersion", "runningHealthy", "processStartTime",
		"freshnessBoundSec", "mintedAt", "expiresAt"),
	"probeEpochs[]": keysOf("host", "bootId", "opSeq", "generation", "incarnationId", "createdAt"),
	"guardEpoch":    keysOf("bootId", "opSeq"),
	// The armed compensation records are objects this store decodes, and their
	// preimage rows carry the whole token schema, so both key sets are
	// canonical too.
	"pendingCompensation[]": keysOf("host", "phase", "rows", "stashReference", "generation"),
	"pendingCompensation[].rows[]": keysOf("host", "value", "generation", "incarnationId", "entryHash",
		"hubTomlFingerprint", "factsRevision", "factsCapturedAt", "targetPath",
		"controllerRevision", "runningVersion", "runningHealthy", "processStartTime",
		"freshnessBoundSec", "mintedAt", "expiresAt"),
}

// keysOf builds one canonical key set.
func keysOf(names ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

// checkStoreBytes applies the store file's byte-level rules: the bytes must be
// UTF-8, carry no unpaired surrogate escape, and name no key twice (with only the
// canonical names in the objects this store decodes). It is one rule set on
// purpose, run by loadFS over the bytes a file holds and by saveFS over the bytes a
// write is about to commit — so a value the store accepts can never be one its own
// next load refuses.
func checkStoreBytes(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("the store file is not valid UTF-8")
	}
	if err := rejectLoneSurrogateEscapes(raw); err != nil {
		return err
	}
	return validateStoreKeys(raw)
}

// rejectLoneSurrogateEscapes refuses a JSON document carrying a \u escape for an
// unpaired UTF-16 surrogate. The decoder replaces such an escape with U+FFFD, so
// the value the file denotes would come back changed on the next write — and the
// raw-byte UTF-8 check cannot see it, because the escape's own bytes are ASCII.
// A valid surrogate pair (an astral character) is fine: it decodes to the value
// the file denotes.
func rejectLoneSurrogateEscapes(raw []byte) error {
	backslashes := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' {
			backslashes++
			continue
		}
		escaped := backslashes%2 == 1
		backslashes = 0
		if !escaped || raw[i] != 'u' || i+5 > len(raw) {
			continue
		}
		code, ok := hex4(raw[i+1 : i+5])
		if !ok {
			continue
		}
		switch {
		case code >= 0xD800 && code <= 0xDBFF:
			if _, ok := lowSurrogateAfter(raw, i+5); !ok {
				return fmt.Errorf("the document carries an unpaired surrogate escape \\u%04X", code)
			}
			// Step over the pair only: the loop's own increment then lands on the
			// byte that follows it, which may start the next escape.
			i += 10
		case code >= 0xDC00 && code <= 0xDFFF:
			return fmt.Errorf("the document carries an unpaired surrogate escape \\u%04X", code)
		}
	}
	return nil
}

// lowSurrogateAfter reports whether the escape for a low surrogate follows at
// index, which is what makes a high surrogate a valid pair.
func lowSurrogateAfter(raw []byte, index int) (uint32, bool) {
	if index+6 > len(raw) || raw[index] != '\\' || raw[index+1] != 'u' {
		return 0, false
	}
	code, ok := hex4(raw[index+2 : index+6])
	if !ok || code < 0xDC00 || code > 0xDFFF {
		return 0, false
	}
	return code, true
}

// hex4 reads four hexadecimal digits.
func hex4(raw []byte) (uint32, bool) {
	var value uint32
	for _, digit := range raw {
		var nibble uint32
		switch {
		case digit >= '0' && digit <= '9':
			nibble = uint32(digit - '0')
		case digit >= 'a' && digit <= 'f':
			nibble = uint32(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			nibble = uint32(digit-'A') + 10
		default:
			return 0, false
		}
		value = value<<4 | nibble
	}
	return value, true
}

// validateStoreKeys is the file-level walk: the objects this store decodes must
// carry only their canonical keys.
func validateStoreKeys(raw []byte) error {
	return validateKeys(raw, ownedObjectKeys)
}

// validateRawFieldKeys is the walk over one opaque raw field: the store does not
// judge its keys, but it must still refuse one that names a key twice, because the
// bytes are written verbatim and the file must mean one thing.
func validateRawFieldKeys(raw json.RawMessage) error {
	return validateKeys(raw, nil)
}

// validateKeys walks a JSON document's token stream and refuses two shapes a
// writer of this store never produces:
//
//   - a key in an object this store decodes that is not the canonical name — the
//     decoder matches struct fields case-insensitively, so `Records` would
//     otherwise decode into `records` and be rewritten in the canonical spelling,
//     and two case variants of one name could overwrite each other; and
//   - any object that names a key twice, which the decoder silently collapses to
//     the last occurrence, so a file carrying two `records` fields would be read
//     as the reduced state and rewritten that way.
//
// A raw field's interior is left alone: those bytes are written and read verbatim,
// so nothing the store does can reinterpret them.
func validateKeys(raw []byte, owned map[string]map[string]struct{}) error {
	type frame struct {
		object    bool
		expectKey bool
		path      string
		lastKey   string
		seen      map[string]struct{}
	}
	top := func(stack []*frame) *frame {
		if len(stack) == 0 {
			return nil
		}
		return stack[len(stack)-1]
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var stack []*frame
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			// A document this walk cannot read is corrupt at the load, exactly as
			// the decoder would report it; there is nothing to answer about keys
			// in a document that has no shape.
			return err
		}
		switch value := token.(type) {
		case json.Delim:
			switch value {
			case '{', '[':
				parent := top(stack)
				nested := &frame{object: value == '{', expectKey: value == '{', seen: map[string]struct{}{}}
				if parent == nil {
					nested.path = ""
				} else if parent.object {
					nested.path = joinKeyPath(parent.path, parent.lastKey)
				} else {
					nested.path = parent.path + "[]"
				}
				stack = append(stack, nested)
			case '}', ']':
				if len(stack) == 0 {
					continue
				}
				stack = stack[:len(stack)-1]
				// The container that just closed was the parent object's value, so
				// the parent's next token is a key again.
				if parent := top(stack); parent != nil && parent.object {
					parent.expectKey = true
				}
			}
		case string:
			parent := top(stack)
			if parent == nil || !parent.object {
				continue
			}
			if !parent.expectKey {
				// A string value, so the object's next token is a key again.
				parent.expectKey = true
				continue
			}
			if canonical, isOwned := ownedKeysFor(owned, parent.path); isOwned {
				if _, ok := canonical[value]; !ok {
					return fmt.Errorf("object %s carries the key %q, which is not one this store writes",
						pathLabel(parent.path), value)
				}
			}
			if _, duplicate := parent.seen[value]; duplicate {
				return fmt.Errorf("object %s names the key %q twice", pathLabel(parent.path), value)
			}
			parent.seen[value] = struct{}{}
			parent.lastKey = value
			parent.expectKey = false
		default:
			// A scalar in an object is a value, so the object's next token is a
			// key again.
			if parent := top(stack); parent != nil && parent.object {
				parent.expectKey = true
			}
		}
	}
}

// ownedKeysFor resolves an object path to the canonical key set this store
// decodes it with. A map's per-key object carries the key in its path —
// "boundaries.<name>", "removedHosts.<name>" — so the store's map-valued
// records are matched by their "<key>[]" template; every other keyed object
// (the hand-written file shapes a test or an operator might produce) is opaque
// to this rule.
func ownedKeysFor(owned map[string]map[string]struct{}, path string) (map[string]struct{}, bool) {
	if canonical, ok := owned[path]; ok {
		return canonical, true
	}
	if key, ok := strings.CutPrefix(path, "boundaries."); ok && key != "" {
		canonical, ok := owned["boundaries[]"]
		return canonical, ok
	}
	if key, ok := strings.CutPrefix(path, "removedHosts."); ok && key != "" {
		canonical, ok := owned["removedHosts[]"]
		return canonical, ok
	}
	if key, ok := strings.CutPrefix(path, "pendingCompensation."); ok && key != "" {
		// The record's preimage rows are objects this store decodes too, and the
		// walk reaches them as "<map>.<name>.rows[]" (the array itself carries
		// no keys).
		if strings.HasSuffix(key, ".rows[]") {
			return owned["pendingCompensation[].rows[]"], true
		}
		if strings.HasSuffix(key, ".rows") {
			return nil, false
		}
		return owned["pendingCompensation[]"], true
	}
	return nil, false
}

// joinKeyPath extends a parent object's path with the key naming a nested value.
func joinKeyPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// pathLabel names an object path in a refusal.
func pathLabel(path string) string {
	if path == "" {
		return "the store file"
	}
	return path
}

// rejectNonStoreFileKind refuses a path that is not a store file this store may
// serve or write: a directory, a symlink, a fifo, a socket or a device. A link is
// refused because the atomic rename replaces the link itself, not what it points
// at, so a store behind one would silently move on its first write; a fifo in
// particular would block the read forever, turning a stray file into a boot that
// never finishes. A regular file — and nothing at all, which is a fresh store —
// passes.
func rejectNonStoreFileKind(path string, info os.FileInfo) error {
	switch {
	case info.IsDir():
		return fmt.Errorf("hostops: store %s is a directory, not a store file", path)
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("hostops: store %s is a symlink; the store file must be a regular file", path)
	case !info.Mode().IsRegular():
		return fmt.Errorf("hostops: store %s is not a regular file", path)
	}
	return nil
}

// lstat stats a path without following a final symlink, through the filesystem
// seam: afero's Fs has no Lstat, but every filesystem the hub uses implements the
// optional Lstater, and one that does not is asked to Stat instead (its answer is
// the target's kind, which is the best it can say).
func lstat(fs afero.Fs, path string) (os.FileInfo, error) {
	if lstater, ok := fs.(afero.Lstater); ok {
		info, _, err := lstater.LstatIfPossible(path)
		return info, err
	}
	return fs.Stat(path)
}

// ownerOnly reports whether a permission set lets nobody but the owner read the
// store. Both halves of spec §4's mode rule rest on this one predicate: startup
// refuses to load a store that is not owner-only, and a replacement preserves a
// stricter owner-only mode instead of widening it.
func ownerOnly(perm os.FileMode) bool { return perm&0o077 == 0 }

// validateSnapshot checks the store file as a whole. Besides the per-record
// schema it holds the invariants the durable sequence, the id allocator and
// spec §8's ordering rest on: records are stored in strictly ascending id order
// (the order §8 sorts and resumes by, and the order Records documents), a record
// never carries a sequence value the store has not reached, and no record
// carries an id above the allocator's high-water mark (fresh operations allocate
// above it and never reuse an id).
func validateSnapshot(state snapshot) error {
	if state.Version != storeVersion {
		return fmt.Errorf("%w: unsupported store version %d", ErrInvalidRecord, state.Version)
	}
	seen := make(map[string]struct{}, len(state.Records))
	stamps := make(map[uint64]struct{}, len(state.Records))
	previous := ""
	for _, record := range state.Records {
		if err := validateRecord(record); err != nil {
			return err
		}
		if _, duplicate := seen[record.ID]; duplicate {
			return fmt.Errorf("%w: duplicate record id %q", ErrInvalidRecord, record.ID)
		}
		seen[record.ID] = struct{}{}
		if previous != "" && record.ID <= previous {
			return fmt.Errorf("%w: record %q is out of ascending id order after %q",
				ErrInvalidRecord, record.ID, previous)
		}
		previous = record.ID
		if record.Sequence > state.Sequence {
			return fmt.Errorf("%w: record %q carries sequence %d above the store's %d",
				ErrInvalidRecord, record.ID, record.Sequence, state.Sequence)
		}
		if record.Sequence > 0 {
			// Every terminal transition advances the sequence once and stamps
			// the record it moved, so one stamp belongs to exactly one record.
			// Gaps are legitimate — retention and compaction will leave them —
			// but a shared stamp would make the race scans that compare sequence
			// values only read two different transitions as one.
			if _, duplicate := stamps[record.Sequence]; duplicate {
				return fmt.Errorf("%w: sequence stamp %d is carried by more than one record",
					ErrInvalidRecord, record.Sequence)
			}
			stamps[record.Sequence] = struct{}{}
		}
		allocated, err := parseAllocatorID(record.ID)
		if err != nil {
			return fmt.Errorf("%w: record id %q is not a controller-assigned id", ErrInvalidRecord, record.ID)
		}
		if allocated > state.AllocatorHighWaterMark {
			return fmt.Errorf("%w: record %q was allocated above the allocator's high-water mark %d",
				ErrInvalidRecord, record.ID, state.AllocatorHighWaterMark)
		}
	}
	// Boundary records are validated like every other value this store persists:
	// no triple outside the writers' schema enters the file, so the boot that
	// reconciles against these values (a later slice) never has to guess what a
	// malformed one meant.
	for name, boundary := range state.Boundaries {
		if err := validateBoundary(name, boundary); err != nil {
			return err
		}
	}
	// Tombstones are the replay source §4 promises a lost-response retry: a
	// value outside the compacting writers' schema is refused like every other
	// persisted value, and a tombstone's id can never collide with a retained
	// record's (a compacted record is gone).
	tombstones := make(map[string]struct{}, len(state.Tombstones))
	for _, tombstone := range state.Tombstones {
		if err := validateTombstone(tombstone, state.CompactSeq); err != nil {
			return err
		}
		if _, duplicate := tombstones[tombstone.ID]; duplicate {
			return fmt.Errorf("%w: duplicate tombstone id %q", ErrInvalidRecord, tombstone.ID)
		}
		tombstones[tombstone.ID] = struct{}{}
		if _, collides := seen[tombstone.ID]; collides {
			return fmt.Errorf("%w: tombstone %q collides with a retained record", ErrInvalidRecord, tombstone.ID)
		}
	}
	// Removed hosts date §4's horizon check and pin the active removed pair a
	// tombstone replay compares against; a zero instant or an incomplete pair
	// would make every pass read the host as freshly removed (or as infinitely
	// old) or match the wrong incarnation, so no writer emits one.
	for name, removed := range state.RemovedHosts {
		if err := validateBoundaryName(name); err != nil {
			return err
		}
		if err := validateRemovedHost(name, removed); err != nil {
			return err
		}
	}
	// The compaction ledger is the evidence §8's refusal reads when a tombstone
	// has been evicted: one mark per compacting write, in write order, each at
	// or below the store's compactSeq.
	markSeqs := make(map[uint64]struct{}, len(state.CompactionMarks))
	for _, mark := range state.CompactionMarks {
		if err := validateCompactionMark(mark, state.CompactSeq); err != nil {
			return err
		}
		if _, duplicate := markSeqs[mark.Seq]; duplicate {
			return fmt.Errorf("%w: compaction seq %d is carried by more than one mark", ErrInvalidRecord, mark.Seq)
		}
		markSeqs[mark.Seq] = struct{}{}
	}
	if state.CompactionFloor > state.CompactSeq {
		return fmt.Errorf("%w: compaction floor %d is above the store's compactSeq %d",
			ErrInvalidRecord, state.CompactionFloor, state.CompactSeq)
	}
	// Token rows carry the same refuse-always rule: a row outside the schema a
	// mint writes is never served, and the set-level rules (one row per host
	// name, one row per value) hold for hand-edited files too. Where a row's
	// capture timestamps sit relative to the durable mark is deliberately not a
	// load rule: §3 makes that the read path's arm, which reads such a row
	// expired instead of corrupt.
	if err := validateTokenRows(state.Tokens); err != nil {
		return err
	}
	// Armed compensation records carry the same refuse-always rule: a record
	// outside the phase machine or carrying a row outside the token schema is
	// never served, and its key must be the host it names.
	for host, record := range state.Compensations {
		if record.Host != host {
			return fmt.Errorf("%w: compensation[%q] names %q in its record", ErrInvalidRecord, host, record.Host)
		}
		if err := validateCompensation(record); err != nil {
			return err
		}
	}
	// Probe epochs and the guard epoch carry the same refuse-always rule: a row
	// outside the schema a persist writes never enters the file, and the
	// set-level rules (one probe row per host, a row's sequence at or below its
	// host's counter) hold for hand-edited files too.
	epochHosts := make(map[string]struct{}, len(state.ProbeEpochs))
	for _, row := range state.ProbeEpochs {
		if err := validateProbeEpoch(row); err != nil {
			return err
		}
		if _, duplicate := epochHosts[row.Host]; duplicate {
			return fmt.Errorf("%w: host %q carries more than one probe epoch", ErrInvalidRecord, row.Host)
		}
		epochHosts[row.Host] = struct{}{}
		if seq := state.ProbeEpochSeq[row.Host]; row.OpSeq > seq {
			return fmt.Errorf("%w: probe epoch for %q carries op sequence %d above its host's counter %d",
				ErrInvalidRecord, row.Host, row.OpSeq, seq)
		}
	}
	for host, seq := range state.ProbeEpochSeq {
		if host == "" || !utf8.ValidString(host) {
			return fmt.Errorf("%w: a probe-epoch sequence is keyed by an invalid host", ErrInvalidRecord)
		}
		if seq == 0 {
			return fmt.Errorf("%w: host %q carries a zero probe-epoch sequence", ErrInvalidRecord, host)
		}
	}
	if state.GuardEpoch != nil {
		if err := validateGuardEpoch(*state.GuardEpoch); err != nil {
			return err
		}
	}
	return nil
}

// cloneSnapshot copies the store state deeply: a caller mutating the copy it
// got from cloneSnapshot cannot reach the store's in-memory records.
func cloneSnapshot(state snapshot) snapshot {
	out := state
	out.Records = make([]Record, len(state.Records))
	for i, record := range state.Records {
		out.Records[i] = cloneRecord(record)
	}
	out.Boundaries = maps.Clone(state.Boundaries)
	out.Tombstones = make([]Tombstone, len(state.Tombstones))
	for i, tombstone := range state.Tombstones {
		out.Tombstones[i] = cloneTombstone(tombstone)
	}
	out.CompactionMarks = append([]CompactionMark(nil), state.CompactionMarks...)
	out.RemovedHosts = maps.Clone(state.RemovedHosts)
	out.Tokens = cloneTokens(state.Tokens)
	out.Compensations = cloneCompensations(state.Compensations)
	out.ProbeEpochs = slices.Clone(state.ProbeEpochs)
	out.ProbeEpochSeq = maps.Clone(state.ProbeEpochSeq)
	out.GuardEpoch = cloneGuardEpoch(state.GuardEpoch)
	return out
}

// formatAllocatorID renders a controller-assigned id at the fixed allocator
// width, so string order equals allocation order (spec §8).
func formatAllocatorID(n uint64) string {
	return fmt.Sprintf("%0*d", allocatorIDWidth, n)
}

// parseAllocatorID reads a controller-assigned id back. Only the store's own
// allocation form is accepted — exactly the allocator width, and nonzero —
// because string order equals allocation order only for that form, and §8's
// pagination sorts and resumes by the id string. A record whose id is any other
// width is not one this store assigned, and accepting it would let the store
// hold a record set whose stored order is not id order.
func parseAllocatorID(id string) (uint64, error) {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return 0, err
	}
	if n == 0 || id != formatAllocatorID(n) {
		return 0, fmt.Errorf("not a controller-assigned id: %q", id)
	}
	return n, nil
}

// nowUTC is the one clock the store reads: display-only timestamps.
func nowUTC() time.Time { return time.Now().UTC() }

// now is the clock the token paths read: the handle's own seam when a test set
// one, the real clock otherwise. Token deadlines, facts ages and the wall-clock
// high-water mark all read here, so one handle's passes cannot disagree about
// what time it is.
func (s *Store) now() time.Time {
	if s.clock != nil {
		return s.clock().UTC()
	}
	return nowUTC()
}
