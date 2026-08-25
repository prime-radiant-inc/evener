package hubcore

import (
	"errors"
	"testing"

	"github.com/spf13/afero"
)

// TestNormalizeDeletionLookupSessionIDRef covers the else-if branch (lines
// 242-244) where ParseRef fails (no colon) but the ref is a valid session ID,
// so threadID is set to ref and later the local: prefix is added.
func TestNormalizeDeletionLookupSessionIDRef(t *testing.T) {
	// ParseRef fails (no colon), ValidateSessionID passes, so threadID = ref.
	// ref stays as the bare session ID (the local: prefix is only added when ref == "").
	ref, threadID := normalizeDeletionLookup(covTestThreadID, "")
	if threadID != covTestThreadID {
		t.Fatalf("threadID = %q, want %q", threadID, covTestThreadID)
	}
	if ref != covTestThreadID {
		t.Fatalf("ref = %q, want %q (bare session ID, no local: prefix)", ref, covTestThreadID)
	}
}

// TestNormalizeDeletionTargetsInvalidLocalRef covers line 224: a target where
// the threadID is valid but the ref points to a different (non-local) source
// or a mismatched threadID.
func TestNormalizeDeletionTargetsInvalidLocalRef(t *testing.T) {
	// Valid thread ID but ref points to a different source.
	target := DeletionTarget{Ref: "remote:" + covTestThreadID, ThreadID: covTestThreadID}
	_, err := normalizeDeletionTargets([]DeletionTarget{target})
	if err == nil {
		t.Fatalf("normalizeDeletionTargets with non-local ref should error")
	}
}

// TestNormalizeDeletionTargetsRefThreadIDMismatch covers line 224: ref parses
// as local but the threadID doesn't match the ref's threadID.
func TestNormalizeDeletionTargetsRefThreadIDMismatch(t *testing.T) {
	// Both are valid but the ref's threadID differs from the target's threadID.
	otherThreadID := "02wMz5Txv1C3Hut0M8GCeC"
	target := DeletionTarget{Ref: "local:" + otherThreadID, ThreadID: covTestThreadID}
	_, err := normalizeDeletionTargets([]DeletionTarget{target})
	if err == nil {
		t.Fatalf("normalizeDeletionTargets with mismatched ref/threadID should error")
	}
}

// TestDeletionStoreBeginReturnsExistingDeleting covers lines 112 and 115:
// when a second Begin is called while an existing generation is still
// Deleting, the existing record is returned unchanged.
func TestDeletionStoreBeginReturnsExistingDeleting(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	target := DeletionTarget{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}
	// First Begin creates a generation in Deleting state.
	record1, err := store.Begin("project-delete-0123456789", []DeletionTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	// Second Begin for the same project should return the existing record.
	record2, err := store.Begin("project-delete-0123456789", []DeletionTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	if record2.Generation != record1.Generation {
		t.Fatalf("second Begin should return same generation, got %d vs %d", record2.Generation, record1.Generation)
	}
}

// TestDeletionStoreBeginSkipsOtherProject covers line 112: when iterating
// records, a record for a different projectID is skipped (continue).
func TestDeletionStoreBeginSkipsOtherProject(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newDeletionStoreFS(fs, "/state", deletionStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	target := DeletionTarget{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}
	// Create a deletion for project A.
	_, err = store.Begin("project-aaaaaaaa-0123456789", []DeletionTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	// Begin for project B should skip project A's record and create a new one.
	record, err := store.Begin("project-bbbbbbbb-0123456789", []DeletionTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	if record.Generation != 1 {
		t.Fatalf("new project should get generation 1, got %d", record.Generation)
	}
}

// TestSaveDeletionSnapshotFSMkdirAllError covers line 305-306: MkdirAll fails
// when the FS is read-only.
func TestSaveDeletionSnapshotFSMkdirAllError(t *testing.T) {
	ro := afero.NewReadOnlyFs(afero.NewMemMapFs())
	_, err := saveDeletionSnapshotFS(ro, "/state", deletionSnapshot{Version: deletionSnapshotVersion}, deletionStoreFaults{})
	if err == nil {
		t.Fatalf("saveDeletionSnapshotFS with read-only FS should error on MkdirAll")
	}
}

// TestSaveDeletionSnapshotFSBeforeRenameError covers the BeforeRename fault
// path (line 332).
func TestSaveDeletionSnapshotFSBeforeRenameError(t *testing.T) {
	fs := afero.NewMemMapFs()
	_, err := saveDeletionSnapshotFS(fs, "/state", deletionSnapshot{Version: deletionSnapshotVersion}, deletionStoreFaults{
		BeforeRename: func() error { return errors.New("before-rename failure") },
	})
	if err == nil {
		t.Fatalf("saveDeletionSnapshotFS with BeforeRename fault should error")
	}
}
