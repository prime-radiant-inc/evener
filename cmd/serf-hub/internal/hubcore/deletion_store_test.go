package hubcore

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/spf13/afero"
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
