package hubcore

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/appwire"
)

const (
	creationSnapshotVersion    uint64 = 1
	creationStateSubdir               = "creations"
	creationStateFilename             = "state.json"
	creationRetention                 = 7 * 24 * time.Hour
	creationMaxTerminalRecords        = 200
)

// CreationState is the durable lifecycle state of one keyed thread/start.
type CreationState string

const (
	CreationStateSpawning  CreationState = "spawning"
	CreationStateSpawned   CreationState = "spawned"
	CreationStateCompleted CreationState = "completed"
	CreationStateUncertain CreationState = "uncertain"
)

// CreationRecord persists deduplication state for one keyed thread/start
// without storing prompt text or image bytes.
type CreationRecord struct {
	ClientMutationID      string          `json:"client_mutation_id"`
	PayloadHash           string          `json:"payload_hash"`
	HasInitialInput       bool            `json:"has_initial_input,omitempty"`
	InitialTurnMutationID string          `json:"initial_turn_mutation_id,omitempty"`
	State                 CreationState   `json:"state"`
	CreatedRef            string          `json:"created_ref,omitempty"`
	CreatedSessionID      string          `json:"created_session_id,omitempty"`
	CreatedInstanceID     string          `json:"created_instance_id,omitempty"`
	ThreadSnapshot        *appwire.Thread `json:"thread_snapshot,omitempty"`
	TurnSnapshot          *appwire.Turn   `json:"turn_snapshot,omitempty"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

type creationSnapshot struct {
	Version uint64           `json:"version"`
	Records []CreationRecord `json:"records"`
}

type creationStoreFaults struct {
	BeforeRename func() error
	AfterRename  func() error
	OnWait       func()
}

// CreationOwner represents the active in-flight executor for one keyed
// thread/start so concurrent duplicates coalesce onto it.
type CreationOwner struct {
	id          string
	payloadHash string
	done        chan struct{}
	released    bool
	waiters     int
	resp        appwire.ThreadStartResponse
	err         error
}

// CreationAcquireResult is the outcome of CreationStore.Acquire.
type CreationAcquireResult struct {
	Owner    *CreationOwner
	Record   *CreationRecord
	Response *appwire.ThreadStartResponse
}

// CreationStore durably tracks keyed thread/start mutations under
// <HubStateRoot>/creations/state.json and coalesces concurrent retries.
type CreationStore struct {
	mu      sync.Mutex
	fs      afero.Fs
	root    string
	records map[string]CreationRecord
	owners  map[string]*CreationOwner
	faults  creationStoreFaults
	now     func() time.Time
}

// NewCreationStore opens the creation journal under stateRoot (in-memory only
// when stateRoot is empty). Startup normalizes persisted spawning records to
// uncertain and leaves corrupt files untouched.
func NewCreationStore(stateRoot string) (*CreationStore, error) {
	return newCreationStoreFS(afero.NewOsFs(), stateRoot, creationStoreFaults{}, time.Now)
}

// NewCreationStoreForTest opens the creation journal with an optional hook
// invoked when a concurrent Acquire blocks waiting on an active owner.
func NewCreationStoreForTest(stateRoot string, onWait func()) (*CreationStore, error) {
	return newCreationStoreFS(afero.NewOsFs(), stateRoot, creationStoreFaults{OnWait: onWait}, time.Now)
}

// SetFaultsForTest replaces atomic-write fault hooks for tests.
func (s *CreationStore) SetFaultsForTest(beforeRename, afterRename func() error) {
	s.mu.Lock()
	s.faults.BeforeRename = beforeRename
	s.faults.AfterRename = afterRename
	s.mu.Unlock()
}

func newCreationStoreFS(fs afero.Fs, stateRoot string, faults creationStoreFaults, now func() time.Time) (*CreationStore, error) {
	if now == nil {
		now = time.Now
	}
	records, err := loadCreationRecordsFS(fs, stateRoot)
	if err != nil {
		return nil, err
	}
	return &CreationStore{
		fs: fs, root: stateRoot, records: records, owners: map[string]*CreationOwner{}, faults: faults, now: now,
	}, nil
}

// CreationPayloadHash computes the canonical SHA-256 hex digest over normalized
// thread/start parameters before filesystem or launch resolution.
func CreationPayloadHash(params appwire.ThreadStartParams) (string, error) {
	params.ClientMutationID = ""
	params.Source = ""
	params.Harness = cmp.Or(params.Harness, "evener")
	raw, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshal canonical thread/start payload: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Acquire claims ownership of a new or spawned keyed thread/start, waits on an
// active in-flight owner, replays a completed record, or refuses conflicts and
// uncertain outcomes.
func (s *CreationStore) Acquire(ctx context.Context, id, payloadHash string, hasInitialInput bool, initialTurnMutationID string) (CreationAcquireResult, error) {
	if s == nil {
		return CreationAcquireResult{}, appwire.MutationUnknown(id, "creation store is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return CreationAcquireResult{}, err
	}

	s.mu.Lock()
	if res, owner, onWait, handled, err := s.acquireExistingLocked(id, payloadHash); handled {
		s.mu.Unlock()
		if owner != nil {
			return waitOnCreationOwner(ctx, id, owner, onWait)
		}
		return res, err
	}

	rec := CreationRecord{
		ClientMutationID: id, PayloadHash: payloadHash,
		HasInitialInput: hasInitialInput, InitialTurnMutationID: initialTurnMutationID,
		State: CreationStateSpawning, UpdatedAt: s.now(),
	}
	next := maps.Clone(s.records)
	next[id] = rec
	if committed, err := s.commitLocked(next); err != nil {
		if committed {
			delete(s.records, id)
			_, _ = s.commitLocked(maps.Clone(s.records))
		}
		s.mu.Unlock()
		return CreationAcquireResult{}, err
	}
	owner := &CreationOwner{id: id, payloadHash: payloadHash, done: make(chan struct{})}
	s.owners[id] = owner
	cloned := cloneCreationRecord(rec)
	s.mu.Unlock()
	return CreationAcquireResult{Owner: owner, Record: &cloned}, nil
}

// LookupExisting resolves an active owner or persisted CreationRecord for id
// without creating a record when id is unseen.
func (s *CreationStore) LookupExisting(ctx context.Context, id, payloadHash string) (CreationAcquireResult, bool, error) {
	if s == nil {
		return CreationAcquireResult{}, false, nil
	}
	if err := ctx.Err(); err != nil {
		return CreationAcquireResult{}, false, err
	}
	s.mu.Lock()
	res, owner, onWait, handled, err := s.acquireExistingLocked(id, payloadHash)
	s.mu.Unlock()
	if !handled {
		return CreationAcquireResult{}, false, nil
	}
	if owner != nil {
		res, err = waitOnCreationOwner(ctx, id, owner, onWait)
	}
	return res, true, err
}

func (s *CreationStore) acquireExistingLocked(id, payloadHash string) (res CreationAcquireResult, waitOwner *CreationOwner, onWait func(), handled bool, err error) {
	if owner := s.owners[id]; owner != nil {
		if owner.payloadHash != payloadHash {
			return CreationAcquireResult{}, nil, nil, true, conflictingCreationError(id)
		}
		owner.waiters++
		return CreationAcquireResult{}, owner, s.faults.OnWait, true, nil
	}
	rec, ok := s.records[id]
	if !ok {
		return CreationAcquireResult{}, nil, nil, false, nil
	}
	if rec.PayloadHash != payloadHash {
		return CreationAcquireResult{}, nil, nil, true, conflictingCreationError(id)
	}
	switch rec.State {
	case CreationStateCompleted:
		resp := creationReplayResponse(id, rec)
		return CreationAcquireResult{Response: &resp}, nil, nil, true, nil
	case CreationStateSpawned:
		owner := &CreationOwner{id: id, payloadHash: payloadHash, done: make(chan struct{})}
		s.owners[id] = owner
		cloned := cloneCreationRecord(rec)
		return CreationAcquireResult{Owner: owner, Record: &cloned}, nil, nil, true, nil
	default:
		return CreationAcquireResult{}, nil, nil, true, appwire.MutationUnknown(id, "thread/start outcome is unknown; verify whether the session started before retrying")
	}
}

func waitOnCreationOwner(ctx context.Context, id string, owner *CreationOwner, onWait func()) (CreationAcquireResult, error) {
	if onWait != nil {
		onWait()
	}
	select {
	case <-owner.done:
		if owner.err != nil {
			return CreationAcquireResult{}, owner.err
		}
		resp := cloneThreadStartResponse(owner.resp)
		resp.ClientMutationID = id
		resp.Disposition = appwire.MutationDispositionReplayed
		return CreationAcquireResult{Response: &resp}, nil
	case <-ctx.Done():
		return CreationAcquireResult{}, ctx.Err()
	}
}

// CommitSpawned persists the spawned ref, session ID, and instance ID before ReadThread/StartTurn.
// published is true once the state file rename succeeded even if dir fsync failed.
func (s *CreationStore) CommitSpawned(id, createdRef, sessionID, instanceID string) (bool, error) {
	return s.updateRecord(id, func(rec *CreationRecord) {
		rec.State = CreationStateSpawned
		rec.CreatedRef = createdRef
		rec.CreatedSessionID = sessionID
		rec.CreatedInstanceID = instanceID
	})
}

// CommitCompleted records the finished creation with a turn-stripped Thread
// snapshot and ID/Status-only Turn snapshot for future idempotent replay.
func (s *CreationStore) CommitCompleted(id string, resp appwire.ThreadStartResponse) error {
	_, err := s.updateRecord(id, func(rec *CreationRecord) {
		thread := appwire.CloneThread(sanitizeThreadSnapshot(resp.Thread))
		turn := appwire.Turn{ID: resp.Turn.ID, Status: resp.Turn.Status}
		rec.State = CreationStateCompleted
		rec.CreatedRef = cmp.Or(rec.CreatedRef, thread.Evener.Ref)
		rec.CreatedSessionID = cmp.Or(rec.CreatedSessionID, thread.SessionID, thread.ID)
		rec.CreatedInstanceID = cmp.Or(rec.CreatedInstanceID, thread.Evener.InstanceID)
		rec.ThreadSnapshot = &thread
		rec.TurnSnapshot = &turn
	})
	return err
}

// MarkUncertain transitions an existing record to CreationStateUncertain.
func (s *CreationStore) MarkUncertain(id string) error {
	_, err := s.updateRecord(id, func(rec *CreationRecord) {
		rec.State = CreationStateUncertain
	})
	return err
}

// RollbackSpawning deletes a CreationStateSpawning record refused before creating any session.
func (s *CreationStore) RollbackSpawning(id string) error {
	if s == nil {
		return errors.New("creation store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return nil
	}
	if rec.State != CreationStateSpawning {
		return fmt.Errorf("cannot rollback creation %q in state %q", id, rec.State)
	}
	next := maps.Clone(s.records)
	delete(next, id)
	_, err := s.commitLocked(next)
	return err
}

// ReleaseOwner publishes the owner's outcome to coalesced waiters and clears the active owner.
func (s *CreationStore) ReleaseOwner(owner *CreationOwner, resp appwire.ThreadStartResponse, err error) {
	if s == nil || owner == nil {
		return
	}
	s.mu.Lock()
	if !owner.released {
		owner.released = true
		if owner.waiters > 0 {
			owner.resp = cloneThreadStartResponse(resp)
		}
		owner.err = err
		if s.owners[owner.id] == owner {
			delete(s.owners, owner.id)
		}
		close(owner.done)
	}
	s.mu.Unlock()
}

func (s *CreationStore) updateRecord(id string, edit func(*CreationRecord)) (bool, error) {
	if s == nil {
		return false, errors.New("creation store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return false, fmt.Errorf("creation record %q not found", id)
	}
	edit(&rec)
	rec.UpdatedAt = s.now()
	next := maps.Clone(s.records)
	next[id] = rec
	return s.commitLocked(next)
}

func (s *CreationStore) commitLocked(next map[string]CreationRecord) (bool, error) {
	pruneCreationRecords(next, s.now())
	records := slices.Collect(maps.Values(next))
	slices.SortFunc(records, func(a, b CreationRecord) int {
		return cmp.Compare(a.ClientMutationID, b.ClientMutationID)
	})
	data, err := json.Marshal(creationSnapshot{Version: creationSnapshotVersion, Records: records})
	if err != nil {
		return false, fmt.Errorf("marshal creation state: %w", err)
	}
	if s.root == "" {
		s.records = next
		return true, nil
	}
	renamed, err := writeStateFileAtomic(s.fs, creationStatePath(s.root), "creation", data, s.faults.BeforeRename, s.faults.AfterRename)
	if renamed {
		s.records = next
	}
	return renamed, err
}

func pruneCreationRecords(records map[string]CreationRecord, now time.Time) {
	cutoff := now.Add(-creationRetention)
	var terminal []CreationRecord
	for id, rec := range records {
		if rec.State != CreationStateCompleted && rec.State != CreationStateUncertain {
			continue
		}
		if rec.UpdatedAt.Before(cutoff) {
			delete(records, id)
			continue
		}
		terminal = append(terminal, rec)
	}
	if len(terminal) <= creationMaxTerminalRecords {
		return
	}
	slices.SortFunc(terminal, func(a, b CreationRecord) int {
		if c := a.UpdatedAt.Compare(b.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ClientMutationID, b.ClientMutationID)
	})
	for _, rec := range terminal[:len(terminal)-creationMaxTerminalRecords] {
		delete(records, rec.ClientMutationID)
	}
}

func creationStatePath(stateRoot string) string {
	return filepath.Join(stateRoot, creationStateSubdir, creationStateFilename)
}

func loadCreationRecordsFS(fs afero.Fs, stateRoot string) (map[string]CreationRecord, error) {
	records := map[string]CreationRecord{}
	if stateRoot == "" {
		return records, nil
	}
	data, ok, err := readStateFile(fs, creationStatePath(stateRoot), "creation")
	if err != nil {
		return nil, err
	}
	if !ok || len(bytes.TrimSpace(data)) == 0 {
		return records, nil
	}
	var snap creationSnapshot
	if err := decodeStateFileStrict(data, "creation", &snap); err != nil {
		return nil, err
	}
	if snap.Version != creationSnapshotVersion {
		return nil, fmt.Errorf("unsupported creation state version %d", snap.Version)
	}
	for _, rec := range snap.Records {
		if rec.ClientMutationID == "" {
			return nil, errors.New("decode creation state: empty client_mutation_id")
		}
		if _, exists := records[rec.ClientMutationID]; exists {
			return nil, fmt.Errorf("decode creation state: duplicate client_mutation_id %q", rec.ClientMutationID)
		}
		switch rec.State {
		case CreationStateSpawning:
			rec.State = CreationStateUncertain
		case CreationStateSpawned, CreationStateCompleted, CreationStateUncertain:
		default:
			return nil, fmt.Errorf("decode creation state: invalid state %q", rec.State)
		}
		records[rec.ClientMutationID] = rec
	}
	return records, nil
}

func conflictingCreationError(id string) error {
	return appwire.ConflictingMutationID("clientMutationId is already used by another thread/start payload").NotAccepted(id)
}

func sanitizeThreadSnapshot(thread appwire.Thread) appwire.Thread {
	thread.Turns = nil
	thread.Preview = cmp.Or(thread.Name, thread.SessionID, thread.ID)
	thread.Evener.PendingMutations = nil
	thread.Evener.Queue = appwire.QueueState{}
	thread.Evener.MutationStateAuthoritative = false
	thread.Evener.LastMessage = ""
	thread.Evener.PendingAsk = nil
	return thread
}

func creationReplayResponse(id string, rec CreationRecord) appwire.ThreadStartResponse {
	var resp appwire.ThreadStartResponse
	if rec.ThreadSnapshot != nil {
		resp.Thread = sanitizeThreadSnapshot(appwire.CloneThread(*rec.ThreadSnapshot))
	}
	if rec.TurnSnapshot != nil {
		resp.Turn = appwire.Turn{ID: rec.TurnSnapshot.ID, Status: rec.TurnSnapshot.Status}
	}
	resp.ClientMutationID = id
	resp.Disposition = appwire.MutationDispositionReplayed
	return resp
}

func cloneThreadStartResponse(resp appwire.ThreadStartResponse) appwire.ThreadStartResponse {
	resp.Thread = appwire.CloneThread(resp.Thread)
	resp.Turn = appwire.CloneTurn(resp.Turn)
	return resp
}

func cloneCreationRecord(rec CreationRecord) CreationRecord {
	if rec.ThreadSnapshot != nil {
		cloned := appwire.CloneThread(*rec.ThreadSnapshot)
		rec.ThreadSnapshot = &cloned
	}
	if rec.TurnSnapshot != nil {
		cloned := appwire.CloneTurn(*rec.TurnSnapshot)
		rec.TurnSnapshot = &cloned
	}
	return rec
}
