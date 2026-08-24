package hubcore

import (
	"encoding/json"
	"errors"
	"syscall"
	"testing"

	"github.com/spf13/afero"
)

const covTestThreadID = "02wMz5Txv1C3Hut0M8GCeB"

// TestDeletionStoreNilGuards covers the nil-store guards on every method.
func TestDeletionStoreNilGuards(t *testing.T) {
	var s *DeletionStore
	if _, err := s.Begin("project-delete-0123456789", nil); err == nil {
		t.Fatalf("nil Begin should error")
	}
	if _, err := s.BeginProject("project-delete-0123456789", nil, true); err == nil {
		t.Fatalf("nil BeginProject should error")
	}
	if err := s.MarkDeleted("project-delete-0123456789", 1); err == nil {
		t.Fatalf("nil MarkDeleted should error")
	}
	if records := s.Deleting(); records != nil {
		t.Fatalf("nil Deleting should return nil")
	}
	if _, ok := s.DeletingProject("project-delete-0123456789"); ok {
		t.Fatalf("nil DeletingProject should return false")
	}
	if _, ok := s.TargetState("local:"+covTestThreadID, covTestThreadID); ok {
		t.Fatalf("nil TargetState should return false")
	}
}

// TestDeletionStoreBeginProjectInvalidID covers the invalid-project-ID path.
func TestDeletionStoreBeginProjectInvalidID(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	target := DeletionTarget{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}
	if _, err := store.BeginProject("invalid-id", []DeletionTarget{target}, true); err == nil {
		t.Fatalf("BeginProject with invalid ID should error")
	}
}

// TestDeletionStoreBeginProjectEmptyTargets covers the empty-target-set path.
func TestDeletionStoreBeginProjectEmptyTargets(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginProject("project-delete-0123456789", nil, true); err == nil {
		t.Fatalf("BeginProject with nil targets should error")
	}
}

// TestDeletionStoreBeginProjectInvalidTarget covers the invalid-target path.
func TestDeletionStoreBeginProjectInvalidTarget(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	// Invalid thread ID.
	badTarget := DeletionTarget{Ref: "local:bad", ThreadID: "bad"}
	if _, err := store.BeginProject("project-delete-0123456789", []DeletionTarget{badTarget}, true); err == nil {
		t.Fatalf("BeginProject with invalid target should error")
	}
}

// TestDeletionStoreMarkDeletedNotFound covers the not-found error path.
func TestDeletionStoreMarkDeletedNotFound(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeleted("project-delete-0123456789", 1); err == nil {
		t.Fatalf("MarkDeleted on nonexistent generation should error")
	}
}

// TestDeletionStoreMarkDeletedAlreadyDeleted covers the idempotent path.
func TestDeletionStoreMarkDeletedAlreadyDeleted(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	target := DeletionTarget{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}
	record, err := store.Begin("project-delete-0123456789", []DeletionTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeleted(record.ProjectID, record.Generation); err != nil {
		t.Fatal(err)
	}
	// Marking already-deleted should be a no-op (return nil).
	if err := store.MarkDeleted(record.ProjectID, record.Generation); err != nil {
		t.Fatalf("MarkDeleted on already-deleted should return nil, got %v", err)
	}
}

// TestDeletionStoreDeletingAndDeletingProject covers the Deleting and
// DeletingProject methods.
func TestDeletionStoreDeletingAndDeletingProject(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	target := DeletionTarget{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}
	record, err := store.Begin("project-delete-0123456789", []DeletionTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	deleting := store.Deleting()
	if len(deleting) != 1 || deleting[0].ProjectID != record.ProjectID {
		t.Fatalf("Deleting = %+v, want 1 record for %s", deleting, record.ProjectID)
	}
	found, ok := store.DeletingProject(record.ProjectID)
	if !ok || found.ProjectID != record.ProjectID {
		t.Fatalf("DeletingProject = %+v, %v", found, ok)
	}
	if _, ok := store.DeletingProject("project-delete-9999999999"); ok {
		t.Fatalf("DeletingProject for nonexistent project should return false")
	}
	if err := store.MarkDeleted(record.ProjectID, record.Generation); err != nil {
		t.Fatal(err)
	}
	if len(store.Deleting()) != 0 {
		t.Fatalf("Deleting after MarkDeleted should be empty")
	}
}

// TestLoadDeletionSnapshotDecodeError covers the decode-error path by writing
// invalid JSON to the state file.
func TestLoadDeletionSnapshotDecodeError(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/state/deletions/state.json", []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{}); err == nil {
		t.Fatalf("loadDeletionSnapshotFS with invalid JSON should error")
	}
}

// TestLoadDeletionSnapshotTrailingJSON covers the trailing-JSON-value path.
func TestLoadDeletionSnapshotTrailingJSON(t *testing.T) {
	fs := afero.NewMemMapFs()
	data := []byte(`{"version":1,"records":[]} {"extra": true}`)
	if err := afero.WriteFile(fs, "/state/deletions/state.json", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{}); err == nil {
		t.Fatalf("loadDeletionSnapshotFS with trailing JSON should error")
	}
}

// TestLoadDeletionSnapshotReadError covers the non-NotExist read error.
func TestLoadDeletionSnapshotReadError(t *testing.T) {
	// Use a path where the file is a directory to trigger a read error.
	fs := afero.NewMemMapFs()
	if err := fs.MkdirAll("/state/deletions/state.json", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{}); err == nil {
		t.Fatalf("loadDeletionSnapshotFS with directory-as-file should error")
	}
}

// TestValidateDeletionSnapshot covers the validateDeletionSnapshot function.
func TestValidateDeletionSnapshot(t *testing.T) {
	// Wrong version.
	if err := validateDeletionSnapshot(deletionSnapshot{Version: 99}); err == nil {
		t.Fatalf("validateDeletionSnapshot wrong version should error")
	}
	// Zero generation.
	badGen := deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
		{ProjectID: "project-delete-0123456789", Generation: 0, State: DeletionStateDeleting, Targets: []DeletionTarget{{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}}},
	}}
	if err := validateDeletionSnapshot(badGen); err == nil {
		t.Fatalf("validateDeletionSnapshot zero generation should error")
	}
	// Invalid state.
	badState := deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
		{ProjectID: "project-delete-0123456789", Generation: 1, State: "bogus", Targets: []DeletionTarget{{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}}},
	}}
	if err := validateDeletionSnapshot(badState); err == nil {
		t.Fatalf("validateDeletionSnapshot invalid state should error")
	}
	// No targets.
	noTargets := deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
		{ProjectID: "project-delete-0123456789", Generation: 1, State: DeletionStateDeleting},
	}}
	if err := validateDeletionSnapshot(noTargets); err == nil {
		t.Fatalf("validateDeletionSnapshot no targets should error")
	}
	// Duplicate generation.
	dup := deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
		{ProjectID: "project-delete-0123456789", Generation: 1, State: DeletionStateDeleting, Targets: []DeletionTarget{{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}}},
		{ProjectID: "project-delete-0123456789", Generation: 1, State: DeletionStateDeleted, Targets: []DeletionTarget{{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}}},
	}}
	if err := validateDeletionSnapshot(dup); err == nil {
		t.Fatalf("validateDeletionSnapshot duplicate generation should error")
	}
	// Invalid project ID.
	badPID := deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
		{ProjectID: "bad", Generation: 1, State: DeletionStateDeleting, Targets: []DeletionTarget{{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}}},
	}}
	if err := validateDeletionSnapshot(badPID); err == nil {
		t.Fatalf("validateDeletionSnapshot invalid project ID should error")
	}
}

// TestDeletionSyncUnsupported covers the deletionSyncUnsupported function.
func TestDeletionSyncUnsupported(t *testing.T) {
	if !deletionSyncUnsupported(syscall.ENOSYS) {
		t.Fatalf("deletionSyncUnsupported(ENOSYS) should be true")
	}
	if !deletionSyncUnsupported(syscall.ENOTSUP) {
		t.Fatalf("deletionSyncUnsupported(ENOTSUP) should be true")
	}
	if !deletionSyncUnsupported(syscall.EINVAL) {
		t.Fatalf("deletionSyncUnsupported(EINVAL) should be true")
	}
	if deletionSyncUnsupported(nil) {
		t.Fatalf("deletionSyncUnsupported(nil) should be false")
	}
	if deletionSyncUnsupported(errors.New("other")) {
		t.Fatalf("deletionSyncUnsupported(other) should be false")
	}
}

// TestSaveDeletionSnapshotEmptyStateRoot covers the empty-stateRoot path.
func TestSaveDeletionSnapshotEmptyStateRoot(t *testing.T) {
	renamed, err := saveDeletionSnapshotFS(afero.NewMemMapFs(), "", deletionSnapshot{Version: deletionSnapshotVersion}, deletionStoreFaults{})
	if err != nil || !renamed {
		t.Fatalf("saveDeletionSnapshotFS with empty stateRoot should return true, nil, got %v, %v", renamed, err)
	}
}

// TestSaveDeletionSnapshotInvalidVersion covers the validate-failure path.
func TestSaveDeletionSnapshotInvalidVersion(t *testing.T) {
	_, err := saveDeletionSnapshotFS(afero.NewMemMapFs(), "/state", deletionSnapshot{Version: 99}, deletionStoreFaults{})
	if err == nil {
		t.Fatalf("saveDeletionSnapshotFS with wrong version should error")
	}
}

// TestNormalizeDeletionLookup covers the normalizeDeletionLookup function.
func TestNormalizeDeletionLookup(t *testing.T) {
	// Valid ref with empty threadID — threadID derived from ref.
	ref, threadID := normalizeDeletionLookup("local:"+covTestThreadID, "")
	if threadID != covTestThreadID {
		t.Fatalf("threadID = %q, want %q", threadID, covTestThreadID)
	}
	if ref != "local:"+covTestThreadID {
		t.Fatalf("ref = %q, want local:%s", ref, covTestThreadID)
	}
	// Empty ref with valid session-id threadID — ref derived from threadID.
	ref, _ = normalizeDeletionLookup("", covTestThreadID)
	if ref != "local:"+covTestThreadID {
		t.Fatalf("ref = %q, want local:%s", ref, covTestThreadID)
	}
}

// TestDeletionStoreCommitLockedError covers the commitLocked error path when
// the AfterRename fault fires after a successful rename.
func TestDeletionStoreCommitLockedAfterRenameError(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{
		AfterRename: func() error { return errors.New("post-rename failure") },
	})
	if err != nil {
		t.Fatal(err)
	}
	target := DeletionTarget{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}
	if _, err := store.Begin("project-delete-0123456789", []DeletionTarget{target}); err == nil {
		t.Fatalf("Begin with AfterRename fault should error")
	}
}

// TestDeletionStoreStateRoundTrip covers that a state written to disk can be
// loaded back with correct field values.
func TestDeletionStoreStateRoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	target := DeletionTarget{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}
	record, err := store.BeginProject("project-roundtrip-0123456789", []DeletionTarget{target}, false)
	if err != nil {
		t.Fatal(err)
	}
	if record.WholeProject {
		t.Fatalf("WholeProject should be false")
	}
	// Reopen and verify.
	reopened, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	found, ok := reopened.DeletingProject("project-roundtrip-0123456789")
	if !ok || found.Generation != record.Generation {
		t.Fatalf("round-trip DeletingProject = %+v, %v", found, ok)
	}
}

// TestDeletionStoreSnapshotWithFilesystem checks that the JSON on disk has
// the expected version field.
func TestDeletionStoreSnapshotVersion(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	target := DeletionTarget{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}
	if _, err := store.Begin("project-delete-0123456789", []DeletionTarget{target}); err != nil {
		t.Fatal(err)
	}
	data, err := afero.ReadFile(fs, "/state/deletions/state.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Version uint64 `json:"version"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Version != deletionSnapshotVersion {
		t.Fatalf("version = %d, want %d", raw.Version, deletionSnapshotVersion)
	}
}

// TestLoadDeletionSnapshotEmptyStateRoot covers the empty-stateRoot path.
func TestLoadDeletionSnapshotEmptyStateRoot(t *testing.T) {
	state, err := loadDeletionSnapshotFS(afero.NewMemMapFs(), "")
	if err != nil {
		t.Fatalf("loadDeletionSnapshotFS with empty stateRoot should not error: %v", err)
	}
	if state.Version != deletionSnapshotVersion {
		t.Fatalf("version = %d, want %d", state.Version, deletionSnapshotVersion)
	}
}

// TestLoadDeletionSnapshotUnknownField covers the DisallowUnknownFields path.
func TestLoadDeletionSnapshotUnknownField(t *testing.T) {
	fs := afero.NewMemMapFs()
	data := []byte(`{"version":1,"records":[],"unknown_field":"value"}`)
	if err := afero.WriteFile(fs, "/state/deletions/state.json", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{}); err == nil {
		t.Fatalf("loadDeletionSnapshotFS with unknown field should error")
	}
}

// TestDeletionStoreTargetStateWithParsedRef covers TargetState with a ref
// that needs normalization (bare session ID with empty threadID).
func TestDeletionStoreTargetStateWithParsedRef(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	target := DeletionTarget{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}
	if _, err := store.Begin("project-delete-0123456789", []DeletionTarget{target}); err != nil {
		t.Fatal(err)
	}
	// Query with full ref and empty threadID — threadID is derived from ref.
	state, ok := store.TargetState("local:"+covTestThreadID, "")
	if !ok || state != DeletionStateDeleting {
		t.Fatalf("TargetState(full ref, empty threadID) = %q, %v, want deleting, true", state, ok)
	}
}
