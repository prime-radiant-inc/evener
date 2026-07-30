package hubcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

	"github.com/spf13/afero"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/identifier"
)

const (
	deletionSnapshotVersion = 1
	deletionStateSubdir     = "deletions"
	deletionStateFilename   = "state.json"
)

// DeletionState is the host-authoritative lifecycle of a deleted target.
type DeletionState string

const (
	DeletionStateDeleting DeletionState = "deleting"
	DeletionStateDeleted  DeletionState = "deleted"
)

// DeletionTarget is the stable identity of one session governed by a project
// deletion. No target-directory path is retained as deletion authority.
type DeletionTarget struct {
	Ref      string `json:"ref"`
	ThreadID string `json:"thread_id"`
}

// DeletionRecord is one irrevocable project deletion generation.
type DeletionRecord struct {
	ProjectID    string           `json:"project_id"`
	Generation   uint64           `json:"generation"`
	State        DeletionState    `json:"state"`
	WholeProject bool             `json:"whole_project"`
	Targets      []DeletionTarget `json:"targets"`
}

type deletionSnapshot struct {
	Version uint64           `json:"version"`
	Records []DeletionRecord `json:"records"`
}

type deletionStoreFaults struct {
	BeforeRename func() error
	AfterRename  func() error
}

// DeletionStore persists host deletion fences outside project/session state.
type DeletionStore struct {
	mu     sync.Mutex
	fs     afero.Fs
	root   string
	state  deletionSnapshot
	faults deletionStoreFaults
}

// NewDeletionStore opens the host deletion store under stateRoot.
func NewDeletionStore(stateRoot string) (*DeletionStore, error) {
	return newDeletionStoreFS(afero.NewOsFs(), stateRoot, deletionStoreFaults{})
}

func newDeletionStoreFS(fs afero.Fs, stateRoot string, faults deletionStoreFaults) (*DeletionStore, error) {
	state, err := loadDeletionSnapshotFS(fs, stateRoot)
	if err != nil {
		return nil, err
	}
	return &DeletionStore{fs: fs, root: stateRoot, state: state, faults: faults}, nil
}

// Begin durably fences every target in a new project deletion generation.
// An existing incomplete generation is returned unchanged for retry.
func (s *DeletionStore) Begin(projectID string, targets []DeletionTarget) (DeletionRecord, error) {
	return s.BeginProject(projectID, targets, true)
}

// BeginProject durably fences targets and records whether they still
// constitute the complete project deletion set.
func (s *DeletionStore) BeginProject(projectID string, targets []DeletionTarget, wholeProject bool) (DeletionRecord, error) {
	if s == nil {
		return DeletionRecord{}, errors.New("deletion store is not configured")
	}
	if err := identifier.ValidateProjectID(projectID); err != nil {
		return DeletionRecord{}, fmt.Errorf("invalid deletion project ID: %w", err)
	}
	normalized, err := normalizeDeletionTargets(targets)
	if err != nil {
		return DeletionRecord{}, err
	}
	if len(normalized) == 0 {
		return DeletionRecord{}, errors.New("deletion target set is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var generation uint64
	for _, record := range s.state.Records {
		if record.ProjectID != projectID {
			continue
		}
		if record.State == DeletionStateDeleting {
			return cloneDeletionRecord(record), nil
		}
		if record.Generation > generation {
			generation = record.Generation
		}
	}
	record := DeletionRecord{
		ProjectID:    projectID,
		Generation:   generation + 1,
		State:        DeletionStateDeleting,
		WholeProject: wholeProject,
		Targets:      normalized,
	}
	next := cloneDeletionSnapshot(s.state)
	next.Records = append(next.Records, record)
	if err := s.commitLocked(next); err != nil {
		return DeletionRecord{}, err
	}
	return cloneDeletionRecord(record), nil
}

// MarkDeleted advances one generation only after its governed cleanup is done.
func (s *DeletionStore) MarkDeleted(projectID string, generation uint64) error {
	if s == nil {
		return errors.New("deletion store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneDeletionSnapshot(s.state)
	for i := range next.Records {
		record := &next.Records[i]
		if record.ProjectID != projectID || record.Generation != generation {
			continue
		}
		if record.State == DeletionStateDeleted {
			return nil
		}
		record.State = DeletionStateDeleted
		return s.commitLocked(next)
	}
	return fmt.Errorf("deletion generation %s/%d not found", projectID, generation)
}

// Deleting returns every incomplete generation for startup recovery.
func (s *DeletionStore) Deleting() []DeletionRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []DeletionRecord
	for _, record := range s.state.Records {
		if record.State == DeletionStateDeleting {
			records = append(records, cloneDeletionRecord(record))
		}
	}
	return records
}

// DeletingProject returns the incomplete generation for projectID, if any.
func (s *DeletionStore) DeletingProject(projectID string) (DeletionRecord, bool) {
	if s == nil {
		return DeletionRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.state.Records) - 1; i >= 0; i-- {
		record := s.state.Records[i]
		if record.ProjectID == projectID && record.State == DeletionStateDeleting {
			return cloneDeletionRecord(record), true
		}
	}
	return DeletionRecord{}, false
}

// TargetState returns the retained authoritative state for a stable target.
func (s *DeletionStore) TargetState(ref, threadID string) (DeletionState, bool) {
	if s == nil {
		return "", false
	}
	ref, _ = normalizeDeletionLookup(ref, threadID)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.state.Records) - 1; i >= 0; i-- {
		record := s.state.Records[i]
		for _, target := range record.Targets {
			if target.Ref == ref {
				return record.State, true
			}
		}
	}
	return "", false
}

func (s *DeletionStore) commitLocked(next deletionSnapshot) error {
	renamed, err := saveDeletionSnapshotFS(s.fs, s.root, next, s.faults)
	if renamed {
		s.state = next
	}
	return err
}

func normalizeDeletionTargets(targets []DeletionTarget) ([]DeletionTarget, error) {
	unique := make(map[string]DeletionTarget, len(targets))
	for _, target := range targets {
		ref, threadID := normalizeDeletionLookup(target.Ref, target.ThreadID)
		if err := identifier.ValidateSessionID(threadID); err != nil {
			return nil, fmt.Errorf("invalid deletion thread ID: %w", err)
		}
		parsed, err := appwire.ParseRef(ref)
		if err != nil || parsed.SourceID != "local" || parsed.ThreadID != threadID {
			return nil, fmt.Errorf("invalid local deletion ref %q", ref)
		}
		unique[ref] = DeletionTarget{Ref: ref, ThreadID: threadID}
	}
	out := make([]DeletionTarget, 0, len(unique))
	for _, target := range unique {
		out = append(out, target)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

func normalizeDeletionLookup(ref, threadID string) (string, string) {
	if parsed, err := appwire.ParseRef(ref); err == nil {
		if threadID == "" {
			threadID = parsed.ThreadID
		}
		ref = parsed.String()
	} else if threadID == "" && identifier.ValidateSessionID(ref) == nil {
		threadID = ref
	}
	if ref == "" && threadID != "" {
		ref = appwire.Ref{SourceID: "local", ThreadID: threadID}.String()
	}
	return ref, threadID
}

func deletionStatePath(stateRoot string) string {
	return filepath.Join(stateRoot, deletionStateSubdir, deletionStateFilename)
}

func loadDeletionSnapshotFS(fs afero.Fs, stateRoot string) (deletionSnapshot, error) {
	empty := deletionSnapshot{Version: deletionSnapshotVersion}
	if stateRoot == "" {
		return empty, nil
	}
	data, err := afero.ReadFile(fs, deletionStatePath(stateRoot))
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return deletionSnapshot{}, fmt.Errorf("read deletion state: %w", err)
	}
	var state deletionSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return deletionSnapshot{}, fmt.Errorf("decode deletion state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return deletionSnapshot{}, errors.New("decode deletion state: trailing JSON value")
		}
		return deletionSnapshot{}, fmt.Errorf("decode deletion state trailing data: %w", err)
	}
	if err := validateDeletionSnapshot(state); err != nil {
		return deletionSnapshot{}, fmt.Errorf("validate deletion state: %w", err)
	}
	return state, nil
}

func saveDeletionSnapshotFS(
	fs afero.Fs,
	stateRoot string,
	state deletionSnapshot,
	faults deletionStoreFaults,
) (renamed bool, err error) {
	if stateRoot == "" {
		return true, nil
	}
	if err := validateDeletionSnapshot(state); err != nil {
		return false, err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return false, fmt.Errorf("marshal deletion state: %w", err)
	}
	path := deletionStatePath(stateRoot)
	dir := filepath.Dir(path)
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create deletion state directory: %w", err)
	}
	temp, err := afero.TempFile(fs, dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("create temp deletion state: %w", err)
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
		return false, fmt.Errorf("write temp deletion state: %w", err)
	}
	if err := temp.Sync(); err != nil && !deletionSyncUnsupported(err) {
		return false, fmt.Errorf("sync temp deletion state: %w", err)
	}
	if err := temp.Close(); err != nil {
		return false, fmt.Errorf("close temp deletion state: %w", err)
	}
	temp = nil
	if faults.BeforeRename != nil {
		if err := faults.BeforeRename(); err != nil {
			return false, err
		}
	}
	if err := fs.Rename(tempPath, path); err != nil {
		return false, fmt.Errorf("rename deletion state: %w", err)
	}
	renamed = true
	directory, err := fs.Open(dir)
	if err != nil {
		return true, fmt.Errorf("open deletion state directory: %w", err)
	}
	if err := directory.Sync(); err != nil && !deletionSyncUnsupported(err) {
		_ = directory.Close()
		return true, fmt.Errorf("sync deletion state directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return true, fmt.Errorf("close deletion state directory: %w", err)
	}
	if faults.AfterRename != nil {
		if err := faults.AfterRename(); err != nil {
			return true, err
		}
	}
	return true, nil
}

func validateDeletionSnapshot(state deletionSnapshot) error {
	if state.Version != deletionSnapshotVersion {
		return fmt.Errorf("unsupported deletion state version %d", state.Version)
	}
	seen := make(map[string]struct{}, len(state.Records))
	for _, record := range state.Records {
		if err := identifier.ValidateProjectID(record.ProjectID); err != nil {
			return fmt.Errorf("invalid project ID %q: %w", record.ProjectID, err)
		}
		if record.Generation == 0 {
			return fmt.Errorf("deletion project %q has zero generation", record.ProjectID)
		}
		switch record.State {
		case DeletionStateDeleting, DeletionStateDeleted:
		default:
			return fmt.Errorf("deletion project %q has invalid state %q", record.ProjectID, record.State)
		}
		if len(record.Targets) == 0 {
			return fmt.Errorf("deletion project %q has no targets", record.ProjectID)
		}
		key := fmt.Sprintf("%s/%d", record.ProjectID, record.Generation)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate deletion generation %s", key)
		}
		seen[key] = struct{}{}
		if _, err := normalizeDeletionTargets(record.Targets); err != nil {
			return err
		}
	}
	return nil
}

func cloneDeletionSnapshot(state deletionSnapshot) deletionSnapshot {
	out := state
	out.Records = make([]DeletionRecord, len(state.Records))
	for i, record := range state.Records {
		out.Records[i] = cloneDeletionRecord(record)
	}
	return out
}

func cloneDeletionRecord(record DeletionRecord) DeletionRecord {
	record.Targets = append([]DeletionTarget(nil), record.Targets...)
	return record
}

func deletionSyncUnsupported(err error) bool {
	return errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EINVAL)
}
