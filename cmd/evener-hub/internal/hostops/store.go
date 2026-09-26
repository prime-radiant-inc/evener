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
	// without a later slice having to know the id is a number.
	allocatorIDWidth = 20
)

// storeFaults are the failure-path seams of the atomic write, the same shape
// the repo's other durable stores expose to their tests.
type storeFaults struct {
	beforeRename func() error
}

// snapshot is the store file: the version, the durable state-transition
// sequence (§4), the controller-assigned row id allocator's high-water mark,
// and every live record. Retention and compaction (§4) are a later slice, so
// nothing here yet removes a record or bounds the set.
type snapshot struct {
	Version                uint64   `json:"version"`
	Sequence               uint64   `json:"sequence"`
	AllocatorHighWaterMark uint64   `json:"allocatorHighWaterMark"`
	Records                []Record `json:"records"`
}

// Store is the operation store: one file, one in-process mutex, one atomic
// write discipline.
//
// mu is the spec §4 store mutex: it serializes every read-modify-write of the
// store within the process, so two racing writers cannot interleave on the same
// temp path or drop each other's record. Lock order is fixed and this package
// only ever holds the innermost lock: the process-wide mutation lock is
// outermost, the store mutex innermost, and no path holding the store mutex
// acquires the mutation lock (this package has no access to it at all).
type Store struct {
	path   string
	fs     afero.Fs
	faults storeFaults

	mu    sync.Mutex
	state snapshot
}

// StorePath is the operation store's file under stateRoot, beside the hub's
// other durable stores.
func StorePath(stateRoot string) string {
	return filepath.Join(stateRoot, storeSubdir, storeFilename)
}

// Open loads the operation store at path. A missing file is an empty store: a
// fresh install has nothing to recover, and Open writes nothing.
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
	state, err := loadFS(fs, path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, fs: fs, state: state, faults: faults}, nil
}

// Path is the file the store reads and writes.
func (s *Store) Path() string { return s.path }

// Sequence is the durable state-transition sequence: the value every terminal
// transition advances, persisted in the store file in the same write.
func (s *Store) Sequence() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Sequence
}

// Record returns a copy of the stored record with that controller-assigned id.
func (s *Store) Record(id string) (Record, bool) {
	if s == nil {
		return Record{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := slices.IndexFunc(s.state.Records, func(record Record) bool { return record.ID == id })
	if index < 0 {
		return Record{}, false
	}
	return cloneRecord(s.state.Records[index]), true
}

// Records returns copies of every stored record, in stored order: ascending id
// order, which is the order spec §8 sorts and resumes by.
func (s *Store) Records() []Record {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSnapshot(s.state).Records
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
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowUTC()
	next := cloneSnapshot(s.state)
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
	if err := s.commitLocked(next); err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

// Transition moves one stored record to state to and commits it atomically.
// change, when non-nil, is applied to the record first, inside the same locked
// read-modify-write, so progress, the terminal result, the fencing epoch and
// the host-removed mark land with the state that makes them meaningful.
//
// Entering a terminal state advances the durable state-transition sequence and
// stamps the record with the value it advanced to (spec §4). A transition to
// any other state stamps nothing.
//
// A refusal — an unknown record, an unknown state, or a change that takes the
// record outside the schema — leaves the store untouched.
func (s *Store) Transition(id string, to State, change func(*Record)) (Record, error) {
	if s == nil {
		return Record{}, errors.New("hostops: store is not configured")
	}
	if !to.Valid() {
		return Record{}, fmt.Errorf("%w: %q", ErrInvalidState, to)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneSnapshot(s.state)
	index := slices.IndexFunc(next.Records, func(record Record) bool { return record.ID == id })
	if index < 0 {
		return Record{}, fmt.Errorf("%w: %q", ErrRecordNotFound, id)
	}
	record := &next.Records[index]
	if change != nil {
		change(record)
	}
	record.State = to
	record.UpdatedAt = nowUTC()
	if to.Terminal() {
		next.advanceSequence(record)
	}
	if err := validateRecord(*record); err != nil {
		return Record{}, err
	}
	if err := s.commitLocked(next); err != nil {
		return Record{}, err
	}
	return cloneRecord(*record), nil
}

// commitLocked persists next and adopts it in memory, but only once the write
// landed: memory that leads the file would answer a later read with a record
// the durable store does not hold. Callers must hold mu.
func (s *Store) commitLocked(next snapshot) error {
	if err := saveFS(s.fs, s.path, next, s.faults); err != nil {
		return err
	}
	s.state = next
	return nil
}

// advanceSequence moves the durable state-transition sequence forward by one and
// stamps record with the value it advanced to. Spec §4 states this once for
// every atomic write that moves a record into a terminal state, so both the
// transition helper and the boot pass go through here and cannot drift apart.
func (next *snapshot) advanceSequence(record *Record) {
	next.Sequence++
	record.Sequence = next.Sequence
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
	var state snapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return snapshot{}, fmt.Errorf("%w: decode %s: %w", ErrStoreCorrupt, path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return snapshot{}, fmt.Errorf("%w: %s carries a trailing JSON value", ErrStoreCorrupt, path)
		}
		return snapshot{}, fmt.Errorf("%w: decode %s trailing data: %w", ErrStoreCorrupt, path, err)
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
func saveFS(fs afero.Fs, path string, state snapshot, faults storeFaults) (err error) {
	if err := validateSnapshot(state); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("hostops: marshal store: %w", err)
	}
	dir := filepath.Dir(path)
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("hostops: create store directory: %w", err)
	}
	temp, err := afero.TempFile(fs, dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("hostops: create temp store: %w", err)
	}
	tempPath := temp.Name()
	renamed := false
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
		if !renamed {
			_ = fs.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("hostops: write temp store: %w", err)
	}
	if err := temp.Sync(); err != nil && !fsdurability.SyncUnsupported(err) {
		return fmt.Errorf("hostops: sync temp store: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("hostops: close temp store: %w", err)
	}
	temp = nil
	// Spec §4: "Replacements preserve the mode." The temp file is created 0600;
	// when the store it replaces already carried a stricter owner-only,
	// owner-readable mode, that mode is what the replacement lands with.
	if perm, ok := preservedMode(fs, path); ok {
		if err := fs.Chmod(tempPath, perm); err != nil {
			return fmt.Errorf("hostops: preserve store mode: %w", err)
		}
	}
	if faults.beforeRename != nil {
		if err := faults.beforeRename(); err != nil {
			return err
		}
	}
	if err := fs.Rename(tempPath, path); err != nil {
		return fmt.Errorf("hostops: rename store: %w", err)
	}
	renamed = true
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
// schema it holds the two cross-record invariants the durable sequence and the
// id allocator rest on: a record never carries a sequence value the store has
// not reached, and no record carries an id above the allocator's high-water
// mark (fresh operations allocate above it and never reuse an id).
func validateSnapshot(state snapshot) error {
	if state.Version != storeVersion {
		return fmt.Errorf("%w: unsupported store version %d", ErrInvalidRecord, state.Version)
	}
	seen := make(map[string]struct{}, len(state.Records))
	for _, record := range state.Records {
		if err := validateRecord(record); err != nil {
			return err
		}
		if _, duplicate := seen[record.ID]; duplicate {
			return fmt.Errorf("%w: duplicate record id %q", ErrInvalidRecord, record.ID)
		}
		seen[record.ID] = struct{}{}
		if record.Sequence > state.Sequence {
			return fmt.Errorf("%w: record %q carries sequence %d above the store's %d",
				ErrInvalidRecord, record.ID, record.Sequence, state.Sequence)
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

// parseAllocatorID reads a controller-assigned id back. Any string that is not
// one of this store's allocation ids is refused, which is what keeps a
// hand-edited or foreign record out of the store.
func parseAllocatorID(id string) (uint64, error) {
	return strconv.ParseUint(id, 10, 64)
}

// nowUTC is the one clock the store reads: display-only timestamps.
func nowUTC() time.Time { return time.Now().UTC() }
