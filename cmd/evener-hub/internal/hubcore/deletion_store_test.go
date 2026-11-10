package hubcore

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

const deletionStoreTestThreadID = "02wMz5Txv1C3Hut0M8GCeB"

func TestDeletionStateCommitBoundaryIsIrrevocable(t *testing.T) {
	fs := afero.NewMemMapFs()
	injected := errors.New("stop before deletion commit")
	failBeforeRename := true
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{
		BeforeRename: func() error {
			if failBeforeRename {
				return injected
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := DeletionTarget{
		Ref:      "local:" + deletionStoreTestThreadID,
		ThreadID: deletionStoreTestThreadID,
	}
	if _, err := store.Begin("project-delete-0123456789", []DeletionTarget{target}); !errors.Is(err, injected) {
		t.Fatalf("Begin before commit error = %v, want %v", err, injected)
	}
	if state, ok := store.TargetState(target.Ref, target.ThreadID); ok {
		t.Fatalf("failed pre-commit write published target state %q", state)
	}

	failBeforeRename = false
	record, err := store.Begin("project-delete-0123456789", []DeletionTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	if record.Generation != 1 || record.State != DeletionStateDeleting {
		t.Fatalf("first committed record = %+v", record)
	}
	reopened, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	if state, ok := reopened.TargetState(target.Ref, target.ThreadID); !ok || state != DeletionStateDeleting {
		t.Fatalf("reopened target state = %q, %v, want deleting", state, ok)
	}
	if err := reopened.MarkDeleted(record.ProjectID, record.Generation); err != nil {
		t.Fatal(err)
	}
	reopened, err = newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	if state, ok := reopened.TargetState(target.Ref, target.ThreadID); !ok || state != DeletionStateDeleted {
		t.Fatalf("completed target state = %q, %v, want deleted", state, ok)
	}
	second := DeletionTarget{
		Ref:      "local:02wMz5Txv2enqVTitaig6F",
		ThreadID: "02wMz5Txv2enqVTitaig6F",
	}
	next, err := reopened.BeginProject(record.ProjectID, []DeletionTarget{second}, false)
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation != 2 || next.State != DeletionStateDeleting || next.WholeProject {
		t.Fatalf("next deletion generation = %+v", next)
	}
}

func TestDeletionStateRenameOutcomeRemainsFencedWhenCallerSeesError(t *testing.T) {
	fs := afero.NewMemMapFs()
	injected := errors.New("outcome unknown after rename")
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{
		AfterRename: func() error { return injected },
	})
	if err != nil {
		t.Fatal(err)
	}
	target := DeletionTarget{
		Ref:      "local:" + deletionStoreTestThreadID,
		ThreadID: deletionStoreTestThreadID,
	}
	if _, err := store.Begin("project-delete-0123456789", []DeletionTarget{target}); !errors.Is(err, injected) {
		t.Fatalf("Begin after rename error = %v, want %v", err, injected)
	}
	if state, ok := store.TargetState(target.Ref, target.ThreadID); !ok || state != DeletionStateDeleting {
		t.Fatalf("in-process target state = %q, %v, want deleting", state, ok)
	}
	reopened, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	if state, ok := reopened.TargetState(target.Ref, target.ThreadID); !ok || state != DeletionStateDeleting {
		t.Fatalf("reopened target state = %q, %v, want deleting", state, ok)
	}
}

func TestDeletionStateCompletionFailureRemainsResumable(t *testing.T) {
	fs := afero.NewMemMapFs()
	injected := errors.New("stop before deleted commit")
	failBeforeRename := false
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{
		BeforeRename: func() error {
			if failBeforeRename {
				return injected
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := DeletionTarget{
		Ref:      "local:" + deletionStoreTestThreadID,
		ThreadID: deletionStoreTestThreadID,
	}
	record, err := store.Begin("project-delete-0123456789", []DeletionTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	failBeforeRename = true
	if err := store.MarkDeleted(record.ProjectID, record.Generation); !errors.Is(err, injected) {
		t.Fatalf("MarkDeleted before commit error = %v, want %v", err, injected)
	}
	if state, ok := store.TargetState(target.Ref, target.ThreadID); !ok || state != DeletionStateDeleting {
		t.Fatalf("failed completion state = %q, %v, want deleting", state, ok)
	}
	failBeforeRename = false
	if err := store.MarkDeleted(record.ProjectID, record.Generation); err != nil {
		t.Fatal(err)
	}
	if state, ok := store.TargetState(target.Ref, target.ThreadID); !ok || state != DeletionStateDeleted {
		t.Fatalf("retried completion state = %q, %v, want deleted", state, ok)
	}
}

// TestDeletionStateMarshalsAndDecodesSnakeCaseFields proves the on-disk wire
// spelling of project_id/whole_project/thread_id directly, rather than only
// proving the store can read back its own output. WholeProject and ThreadID
// are the two fields no other test in this file observes post-decode:
// TargetState never returns the decoded ThreadID, and no other test reopens
// a record whose WholeProject is true.
//
// A round trip through newDeletionStoreFS alone cannot catch a misspelled
// tag: encode and decode both read the same struct tag, so renaming it moves
// both sides together and the round trip stays green (verified by
// misspelling ThreadID's tag and watching a store-only round trip keep
// passing). Decoding the raw bytes into a schema-agnostic map, independent
// of whatever the struct currently says, is what actually pins the literal
// key spelling — that half of this test fails on a misspelled tag; the
// second half then confirms the typed decode path agrees.
//
// Both assertions use non-zero-value data on purpose — a field that silently
// decodes to its Go zero value would still pass a test built on zero-value
// expectations.
func TestDeletionStateMarshalsAndDecodesSnakeCaseFields(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	target := DeletionTarget{
		Ref:      "local:" + deletionStoreTestThreadID,
		ThreadID: deletionStoreTestThreadID,
	}
	if _, err := store.BeginProject("project-roundtrip-0123456789", []DeletionTarget{target}, true); err != nil {
		t.Fatal(err)
	}

	raw, err := afero.ReadFile(fs, "/state/deletions/state.json")
	if err != nil {
		t.Fatal(err)
	}
	var generic struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if len(generic.Records) != 1 {
		t.Fatalf("records = %d, want 1: %s", len(generic.Records), raw)
	}
	record := generic.Records[0]
	if record["project_id"] != "project-roundtrip-0123456789" {
		t.Fatalf(`record["project_id"] = %v, raw = %s`, record["project_id"], raw)
	}
	if record["whole_project"] != true {
		t.Fatalf(`record["whole_project"] = %v, raw = %s`, record["whole_project"], raw)
	}
	targets, _ := record["targets"].([]any)
	if len(targets) != 1 {
		t.Fatalf(`record["targets"] = %v, raw = %s`, record["targets"], raw)
	}
	targetMap, _ := targets[0].(map[string]any)
	if targetMap["thread_id"] != deletionStoreTestThreadID {
		t.Fatalf(`record["targets"][0]["thread_id"] = %v, raw = %s`, targetMap["thread_id"], raw)
	}

	reopened, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	records := reopened.Deleting()
	if len(records) != 1 {
		t.Fatalf("Deleting() after reopen = %d records, want 1: %+v", len(records), records)
	}
	decoded := records[0]
	if !decoded.WholeProject {
		t.Fatalf("WholeProject after typed decode = %+v, want true", decoded)
	}
	if len(decoded.Targets) != 1 || decoded.Targets[0].ThreadID != deletionStoreTestThreadID {
		t.Fatalf("Targets after typed decode = %+v, want ThreadID %q", decoded.Targets, deletionStoreTestThreadID)
	}
}

func TestDeletionStateRejectsMalformedPersistentAuthority(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := fs.MkdirAll("/state/deletions", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := afero.WriteFile(fs, "/state/deletions/state.json", []byte(`{"version":1,"records":[{"project_id":"project-delete-0123456789","generation":1,"state":"live","targets":[]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{}); err == nil {
		t.Fatal("malformed deletion authority loaded successfully")
	}
}

func deletionTestTarget() DeletionTarget {
	return DeletionTarget{Ref: "local:" + deletionStoreTestThreadID, ThreadID: deletionStoreTestThreadID}
}

func newMemDeletionStore(t *testing.T) *DeletionStore {
	t.Helper()
	store, err := newDeletionStoreFS(afero.NewMemMapFs(), "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// An unconfigured (nil) store refuses writes and reports nothing in progress.
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
	if _, ok := s.TargetState("local:"+deletionStoreTestThreadID, deletionStoreTestThreadID); ok {
		t.Fatalf("nil TargetState should return false")
	}
}

// TestDeletionStoreBeginProjectRejectsInvalidInput pins that a deletion with a
// malformed project ID or target set is refused before anything is fenced.
func TestDeletionStoreBeginProjectRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name      string
		projectID string
		targets   []DeletionTarget
	}{
		{name: "invalid project ID", projectID: "invalid-id", targets: []DeletionTarget{deletionTestTarget()}},
		{name: "no targets", projectID: "project-delete-0123456789"},
		{name: "invalid thread ID", projectID: "project-delete-0123456789", targets: []DeletionTarget{{Ref: "local:bad", ThreadID: "bad"}}},
		{name: "non-local ref", projectID: "project-delete-0123456789", targets: []DeletionTarget{{Ref: "remote:" + deletionStoreTestThreadID, ThreadID: deletionStoreTestThreadID}}},
		{name: "ref names another thread", projectID: "project-delete-0123456789", targets: []DeletionTarget{{Ref: "local:02wMz5Txv1C3Hut0M8GCeC", ThreadID: deletionStoreTestThreadID}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemDeletionStore(t)
			if _, err := store.BeginProject(tc.projectID, tc.targets, true); err == nil {
				t.Fatal("BeginProject accepted invalid input")
			}
			if records := store.Deleting(); records != nil {
				t.Fatalf("a refused BeginProject fenced %+v", records)
			}
		})
	}
}

func TestDeletionStoreBeginReturnsExistingDeleting(t *testing.T) {
	store := newMemDeletionStore(t)
	first, err := store.Begin("project-delete-0123456789", []DeletionTarget{deletionTestTarget()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Begin("project-delete-0123456789", []DeletionTarget{deletionTestTarget()})
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation != first.Generation {
		t.Fatalf("second Begin generation = %d, want the in-progress generation %d", second.Generation, first.Generation)
	}
}

// Generations are numbered per project: another project's records do not
// advance this one's.
func TestDeletionStoreBeginSkipsOtherProject(t *testing.T) {
	store := newMemDeletionStore(t)
	if _, err := store.Begin("project-aaaaaaaa-0123456789", []DeletionTarget{deletionTestTarget()}); err != nil {
		t.Fatal(err)
	}
	record, err := store.Begin("project-bbbbbbbb-0123456789", []DeletionTarget{deletionTestTarget()})
	if err != nil {
		t.Fatal(err)
	}
	if record.ProjectID != "project-bbbbbbbb-0123456789" || record.Generation != 1 {
		t.Fatalf("Begin = %+v, want a first generation for the second project", record)
	}
}

// A session-only record fences its target without becoming the generation a
// project delete resumes. Both scopes share per-project generation numbering.
func TestDeletionStoreBeginSessionSeparatesFromProjectDeletion(t *testing.T) {
	store := newMemDeletionStore(t)
	const projectID = "project-delete-0123456789"
	target := deletionTestTarget()
	sessionRecord, err := store.BeginSession(projectID, target)
	if err != nil {
		t.Fatal(err)
	}
	if !sessionRecord.SessionOnly || sessionRecord.WholeProject || len(sessionRecord.Targets) != 1 {
		t.Fatalf("BeginSession = %+v, want one session-only target", sessionRecord)
	}
	retry, err := store.BeginSession(projectID, target)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Generation != sessionRecord.Generation {
		t.Fatalf("retry BeginSession generation = %d, want the in-progress %d", retry.Generation, sessionRecord.Generation)
	}
	if _, ok := store.DeletingProject(projectID); ok {
		t.Fatal("DeletingProject resumed a session-only generation")
	}
	second := DeletionTarget{Ref: "local:02wMz5Txv1C3Hut0M8GCeC", ThreadID: "02wMz5Txv1C3Hut0M8GCeC"}
	projectRecord, err := store.Begin(projectID, []DeletionTarget{target, second})
	if err != nil {
		t.Fatal(err)
	}
	if projectRecord.SessionOnly || projectRecord.Generation != sessionRecord.Generation+1 {
		t.Fatalf("Begin after a session generation = %+v, want the next project generation", projectRecord)
	}
	found, ok := store.DeletingProject(projectID)
	if !ok || found.Generation != projectRecord.Generation || found.SessionOnly {
		t.Fatalf("DeletingProject = %+v, %v, want the project generation", found, ok)
	}
	if state, ok := store.TargetState(target.Ref, target.ThreadID); !ok || state != DeletionStateDeleting {
		t.Fatalf("TargetState = %q, %v, want deleting", state, ok)
	}
	if err := store.MarkDeleted(projectRecord.ProjectID, projectRecord.Generation); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.DeletingProject(projectID); ok {
		t.Fatal("DeletingProject after MarkDeleted = true, want false")
	}
	deleting := store.Deleting()
	if len(deleting) != 1 || !deleting[0].SessionOnly {
		t.Fatalf("Deleting = %+v, want only the unfinished session generation", deleting)
	}
}

// A session generation keeps its scope through the persisted snapshot, so a
// reopened store still refuses to resume it as a project deletion.
func TestDeletionStoreSessionRecordRoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.BeginSession("project-session-0123456789", deletionTestTarget())
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	deleting := reopened.Deleting()
	if len(deleting) != 1 || !deleting[0].SessionOnly || deleting[0].Generation != record.Generation {
		t.Fatalf("reopened Deleting = %+v, want the session generation", deleting)
	}
	if _, ok := reopened.DeletingProject(record.ProjectID); ok {
		t.Fatal("reopened store resumed a session-only generation as a project deletion")
	}
}

func TestDeletionStoreMarkDeletedNotFound(t *testing.T) {
	store, err := NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewDeletionStore: %v", err)
	}
	if err := store.MarkDeleted("project-delete-0123456789", 1); err == nil {
		t.Fatal("MarkDeleted of a generation that was never begun succeeded")
	}
}

// Marking a generation deleted twice is idempotent and leaves nothing in
// progress.
func TestDeletionStoreMarkDeletedAlreadyDeleted(t *testing.T) {
	store, err := NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewDeletionStore: %v", err)
	}
	record, err := store.Begin("project-delete-0123456789", []DeletionTarget{deletionTestTarget()})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := store.MarkDeleted(record.ProjectID, record.Generation); err != nil {
		t.Fatalf("first MarkDeleted: %v", err)
	}
	if err := store.MarkDeleted(record.ProjectID, record.Generation); err != nil {
		t.Fatalf("second MarkDeleted: %v", err)
	}
	if got := store.Deleting(); got != nil {
		t.Fatalf("Deleting after idempotent MarkDeleted = %#v, want nil", got)
	}
}

func TestDeletionStoreDeletingAndDeletingProject(t *testing.T) {
	store := newMemDeletionStore(t)
	record, err := store.Begin("project-delete-0123456789", []DeletionTarget{deletionTestTarget()})
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

// A begun generation survives reopening the store from disk.
func TestDeletionStoreStateRoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.BeginProject("project-roundtrip-0123456789", []DeletionTarget{deletionTestTarget()}, false)
	if err != nil {
		t.Fatal(err)
	}
	if record.WholeProject {
		t.Fatalf("WholeProject should be false")
	}
	reopened, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	found, ok := reopened.DeletingProject("project-roundtrip-0123456789")
	if !ok || found.Generation != record.Generation {
		t.Fatalf("round-trip DeletingProject = %+v, %v", found, ok)
	}
}

// A target can be looked up by its ref alone or by its thread ID alone; the
// lookup normalizes either to the stored local ref.
func TestDeletionStoreTargetStateWithParsedRef(t *testing.T) {
	store := newMemDeletionStore(t)
	if _, err := store.Begin("project-delete-0123456789", []DeletionTarget{deletionTestTarget()}); err != nil {
		t.Fatal(err)
	}
	state, ok := store.TargetState("local:"+deletionStoreTestThreadID, "")
	if !ok || state != DeletionStateDeleting {
		t.Fatalf("TargetState(full ref, empty threadID) = %q, %v, want deleting, true", state, ok)
	}
	state, ok = store.TargetState("", deletionStoreTestThreadID)
	if !ok || state != DeletionStateDeleting {
		t.Fatalf("TargetState(empty ref, threadID) = %q, %v, want deleting, true", state, ok)
	}
}

// TestLoadDeletionSnapshotRejectsUnreadableState pins that a state file the
// store cannot read back exactly fails the open instead of loading as empty.
func TestLoadDeletionSnapshotRejectsUnreadableState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(fs afero.Fs) error
	}{
		{"not JSON", func(fs afero.Fs) error {
			return afero.WriteFile(fs, "/state/deletions/state.json", []byte("not json"), 0o600)
		}},
		{"trailing JSON value", func(fs afero.Fs) error {
			return afero.WriteFile(fs, "/state/deletions/state.json", []byte(`{"version":1,"records":[]} {"extra": true}`), 0o600)
		}},
		{"unknown field", func(fs afero.Fs) error {
			return afero.WriteFile(fs, "/state/deletions/state.json", []byte(`{"version":1,"records":[],"unknown_field":"value"}`), 0o600)
		}},
		{"state path is a directory", func(fs afero.Fs) error {
			return fs.MkdirAll("/state/deletions/state.json", 0o700)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			if err := tc.write(fs); err != nil {
				t.Fatal(err)
			}
			if _, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{}); err == nil {
				t.Fatal("the deletion store opened over an unreadable state file")
			}
		})
	}
}

// With no state root the store keeps nothing on disk: loading yields an empty
// current-version snapshot and saving succeeds without writing.
func TestDeletionSnapshotWithoutStateRoot(t *testing.T) {
	state, err := loadDeletionSnapshotFS(afero.NewMemMapFs(), "")
	if err != nil {
		t.Fatalf("loadDeletionSnapshotFS: %v", err)
	}
	if state.Version != deletionSnapshotVersion {
		t.Fatalf("version = %d, want %d", state.Version, deletionSnapshotVersion)
	}
	fs := afero.NewMemMapFs()
	renamed, err := saveDeletionSnapshotFS(fs, "", deletionSnapshot{Version: deletionSnapshotVersion}, deletionStoreFaults{})
	if err != nil || !renamed {
		t.Fatalf("saveDeletionSnapshotFS = %v, %v; want true, nil", renamed, err)
	}
	if entries, _ := afero.ReadDir(fs, "/"); len(entries) != 0 {
		t.Fatalf("saving with no state root wrote %d entries", len(entries))
	}
}

func TestValidateDeletionSnapshot(t *testing.T) {
	target := []DeletionTarget{deletionTestTarget()}
	record := func(projectID string, generation uint64, state DeletionState, targets []DeletionTarget) DeletionRecord {
		return DeletionRecord{ProjectID: projectID, Generation: generation, State: state, Targets: targets}
	}
	for _, tc := range []struct {
		name     string
		snapshot deletionSnapshot
	}{
		{"unsupported version", deletionSnapshot{Version: 99}},
		{"zero generation", deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
			record("project-delete-0123456789", 0, DeletionStateDeleting, target),
		}}},
		{"invalid state", deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
			record("project-delete-0123456789", 1, "bogus", target),
		}}},
		{"no targets", deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
			record("project-delete-0123456789", 1, DeletionStateDeleting, nil),
		}}},
		{"duplicate generation", deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
			record("project-delete-0123456789", 1, DeletionStateDeleting, target),
			record("project-delete-0123456789", 1, DeletionStateDeleted, target),
		}}},
		{"invalid project ID", deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
			record("bad", 1, DeletionStateDeleting, target),
		}}},
		{"session-only whole project", deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{{
			ProjectID: "project-delete-0123456789", Generation: 1, State: DeletionStateDeleting,
			WholeProject: true, SessionOnly: true, Targets: target,
		}}}},
		{"session-only multiple targets", deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{{
			ProjectID: "project-delete-0123456789", Generation: 1, State: DeletionStateDeleting,
			SessionOnly: true, Targets: []DeletionTarget{
				deletionTestTarget(),
				{Ref: "local:02wMz5Txv1C3Hut0M8GCeC", ThreadID: "02wMz5Txv1C3Hut0M8GCeC"},
			},
		}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateDeletionSnapshot(tc.snapshot); err == nil {
				t.Fatal("validateDeletionSnapshot accepted an invalid snapshot")
			}
		})
	}
}

// Saving refuses a snapshot that would not load back, and surfaces a failure
// to create the state directory.
func TestSaveDeletionSnapshotRefusesWhatItCannotPersist(t *testing.T) {
	if _, err := saveDeletionSnapshotFS(afero.NewMemMapFs(), "/state", deletionSnapshot{Version: 99}, deletionStoreFaults{}); err == nil {
		t.Fatal("saveDeletionSnapshotFS accepted an unsupported version")
	}
	errMkdir := errors.New("mkdir failed")
	broken := failingFs{Fs: afero.NewMemMapFs(), mkdirErr: errMkdir}
	if _, err := saveDeletionSnapshotFS(broken, "/state", deletionSnapshot{Version: deletionSnapshotVersion}, deletionStoreFaults{}); !errors.Is(err, errMkdir) {
		t.Fatalf("saveDeletionSnapshotFS with no state directory = %v, want the directory failure", err)
	}
}

// The persisted snapshot names its format version under the "version" key.
func TestDeletionStoreSnapshotVersion(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin("project-delete-0123456789", []DeletionTarget{deletionTestTarget()}); err != nil {
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

func TestNormalizeDeletionLookup(t *testing.T) {
	for _, tc := range []struct {
		name, ref, threadID   string
		wantRef, wantThreadID string
	}{
		{name: "ref only", ref: "local:" + deletionStoreTestThreadID, wantRef: "local:" + deletionStoreTestThreadID, wantThreadID: deletionStoreTestThreadID},
		{name: "thread ID only", threadID: deletionStoreTestThreadID, wantRef: "local:" + deletionStoreTestThreadID, wantThreadID: deletionStoreTestThreadID},
		{name: "bare session ID as ref", ref: deletionStoreTestThreadID, wantRef: deletionStoreTestThreadID, wantThreadID: deletionStoreTestThreadID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, threadID := normalizeDeletionLookup(tc.ref, tc.threadID)
			if ref != tc.wantRef || threadID != tc.wantThreadID {
				t.Fatalf("normalizeDeletionLookup(%q, %q) = %q, %q; want %q, %q", tc.ref, tc.threadID, ref, threadID, tc.wantRef, tc.wantThreadID)
			}
		})
	}
}

// Targets normalize to one entry per ref, sorted by ref.
func TestNormalizeDeletionTargetsSortsAndDedups(t *testing.T) {
	firstID := hubtest.SessionID(t)
	secondID := hubtest.SessionID(t)
	targets := []DeletionTarget{
		{ThreadID: secondID, Ref: "local:" + secondID},
		{ThreadID: firstID, Ref: "local:" + firstID},
		{ThreadID: secondID, Ref: "local:" + secondID},
	}
	normalized, err := normalizeDeletionTargets(targets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []DeletionTarget{
		{ThreadID: firstID, Ref: "local:" + firstID},
		{ThreadID: secondID, Ref: "local:" + secondID},
	}
	if strings.Compare(want[0].Ref, want[1].Ref) > 0 {
		want[0], want[1] = want[1], want[0]
	}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("normalized = %#v, want sorted unique targets %#v", normalized, want)
	}
}
