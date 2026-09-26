package hostops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

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
// schema-invalid, never silently an empty store.
type snapshot struct {
	Version                uint64   `json:"version"`
	Sequence               uint64   `json:"sequence"`
	AllocatorHighWaterMark uint64   `json:"allocatorHighWaterMark"`
	Records                []Record `json:"records"`
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
}

// storeCells holds the process's one cell per store file path. The key is the
// canonical path: Open is the real-filesystem entry point, and one path has one
// filesystem in a process. The afero seam beneath Open serves tests, which drop
// the cell to model the process restart a fresh load belongs to.
var storeCells sync.Map

// Store is a handle on the operation store: one file, one shared cell, one
// atomic write discipline.
type Store struct {
	path   string
	fs     afero.Fs
	faults storeFaults
	cell   *storeCell
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
	return openFS(afero.NewOsFs(), path, storeFaults{})
}

// openFS is the construction seam beneath Open: it builds a Store over an
// injected afero.Fs so tests can drive persistence and the write's failure
// paths.
func openFS(fs afero.Fs, path string, faults storeFaults) (*Store, error) {
	key, err := canonicalStorePath(path)
	if err != nil {
		return nil, err
	}
	if existing, held := storeCells.Load(key); held {
		return &Store{path: path, fs: fs, faults: faults, cell: existing.(*storeCell)}, nil
	}
	state, err := loadFS(fs, path)
	if err != nil {
		return nil, err
	}
	cell := &storeCell{state: state}
	// Two racing first opens of one path must adopt one cell, never two.
	if existing, loaded := storeCells.LoadOrStore(key, cell); loaded {
		cell = existing.(*storeCell)
	}
	return &Store{path: path, fs: fs, faults: faults, cell: cell}, nil
}

// canonicalStorePath is the key two handles for one store file collide on.
func canonicalStorePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("hostops: resolve store path %s: %w", path, err)
	}
	return filepath.Clean(absolute), nil
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
	record := &next.Records[index]
	if record.State.Terminal() {
		return Record{}, fmt.Errorf("%w: record %q is already %q", ErrRecordTerminal, id, record.State)
	}
	// Spec §4 and §7 name exactly one exit from the fencing state: "the
	// `orphan-unverified`→`interrupted` resolution" — a record whose boundary has
	// not been verified must never become a success. The rest of the graph
	// (pending→running, in-flight→terminal) is the deploy slice's to drive; this
	// substrate refuses only the edges the spec forbids outright.
	if record.State == StateOrphanUnverified && to != StateInterrupted {
		return Record{}, fmt.Errorf("%w: orphan-unverified record %q resolves only to %q",
			ErrInvalidTransition, id, StateInterrupted)
	}
	identity := identityOf(*record)
	if change != nil {
		change(record)
	}
	if changed := identityOf(*record); changed != identity {
		// A change may carry progress, the terminal result, the fencing epoch,
		// the orphan boundary and the host-removed mark. The record's durable
		// identity and the store's stamp are not a caller's to rewrite: a record
		// that changes host, kind, pinned pair or id would corrupt the dedup
		// scope §4 keys on, and the sequence stamp is what race scans compare.
		return Record{}, fmt.Errorf("%w: the change rewrote record %q's immutable fields", ErrInvalidRecord, id)
	}
	// The store adopts the snapshot it just wrote, so it must own every value in
	// it: a callback that hands in a progress slice, a result or a raw message it
	// still holds could otherwise rewrite in-memory state after this call
	// returned, with no write and no lock, leaving memory and the file
	// disagreeing. The record's mutable fields are copied here, once, before
	// validation.
	*record = cloneRecord(*record)
	record.State = to
	record.UpdatedAt = nowUTC()
	if to.Terminal() {
		next.advanceSequence(record)
	}
	if err := validateRecord(*record); err != nil {
		return Record{}, err
	}
	adopted, err := s.commitLocked(next)
	if err != nil && !adopted {
		return Record{}, err
	}
	// See Create: a landed rename is a durable transition even when the
	// directory sync behind it failed.
	return cloneRecord(*record), err
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
	landed, err = saveFS(s.fs, s.path, next, s.faults)
	if landed {
		s.cell.state = next
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
	Version                *uint64   `json:"version"`
	Sequence               *uint64   `json:"sequence"`
	AllocatorHighWaterMark *uint64   `json:"allocatorHighWaterMark"`
	Records                *[]Record `json:"records"`
}

// loadFS reads and validates the store file. A missing file is an empty store;
// every other failure is reported, never papered over.
func loadFS(fs afero.Fs, path string) (snapshot, error) {
	empty := snapshot{Version: storeVersion}
	info, err := fs.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil
		}
		return snapshot{}, fmt.Errorf("hostops: stat store %s: %w", path, err)
	}
	if info.IsDir() {
		return snapshot{}, fmt.Errorf("hostops: store %s is a directory, not a store file", path)
	}
	if perm := info.Mode().Perm(); !ownerOnly(perm) {
		return snapshot{}, fmt.Errorf("%w: %s has mode %04o", ErrStoreReadableBeyondOwner, path, perm)
	}
	raw, err := afero.ReadFile(fs, path)
	if err != nil {
		return snapshot{}, fmt.Errorf("hostops: read store %s: %w", path, err)
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
	state := snapshot{
		Version:                *file.Version,
		Sequence:               *file.Sequence,
		AllocatorHighWaterMark: *file.AllocatorHighWaterMark,
		Records:                *file.Records,
	}
	if err := validateSnapshot(state); err != nil {
		return snapshot{}, fmt.Errorf("%w: validate %s: %w", ErrStoreCorrupt, path, err)
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
	data, err := json.Marshal(state)
	if err != nil {
		return false, fmt.Errorf("hostops: marshal store: %w", err)
	}
	dir := filepath.Dir(path)
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("hostops: create store directory: %w", err)
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
	if err := temp.Sync(); err != nil && !fsdurability.SyncUnsupported(err) {
		return false, fmt.Errorf("hostops: sync temp store: %w", err)
	}
	if err := temp.Close(); err != nil {
		return false, fmt.Errorf("hostops: close temp store: %w", err)
	}
	temp = nil
	// Spec §4: "Replacements preserve the mode." The temp file is created 0600;
	// when the store it replaces already carried a stricter owner-only,
	// owner-readable mode, that mode is what the replacement lands with.
	if perm, ok := preservedMode(fs, path); ok {
		if err := fs.Chmod(tempPath, perm); err != nil {
			return false, fmt.Errorf("hostops: preserve store mode: %w", err)
		}
	}
	if faults.beforeRename != nil {
		if err := faults.beforeRename(); err != nil {
			return false, err
		}
	}
	if err := fs.Rename(tempPath, path); err != nil {
		return false, fmt.Errorf("hostops: rename store: %w", err)
	}
	renamed = true
	sync := syncDirFS
	if faults.syncDir != nil {
		sync = faults.syncDir
	}
	if err := sync(fs, dir); err != nil {
		return true, &postRenameError{err: err}
	}
	return true, nil
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
