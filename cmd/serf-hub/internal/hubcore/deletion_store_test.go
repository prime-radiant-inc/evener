package hubcore

import (
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

func TestDeletionStateRejectsMalformedPersistentAuthority(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := fs.MkdirAll("/state/deletions", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := afero.WriteFile(fs, "/state/deletions/state.json", []byte(`{"version":1,"records":[{"projectId":"project-delete-0123456789","generation":1,"state":"live","targets":[]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{}); err == nil {
		t.Fatal("malformed deletion authority loaded successfully")
	}
}
