package hubcore

import (
	"errors"
	"os"
	"testing"

	"github.com/spf13/afero"
)

// faultFile wraps an afero.File and injects errors at specific operations.
type faultFile struct {
	afero.File
	writeErr error
	syncErr  error
	closeErr error
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.File.Write(p)
}

func (f *faultFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.File.Sync()
}

func (f *faultFile) Close() error {
	if f.closeErr != nil {
		return f.closeErr
	}
	return f.File.Close()
}

// faultCreateFs wraps an afero.Fs whose OpenFile returns a faultFile.
type faultCreateFs struct {
	afero.Fs
	writeErr error
	syncErr  error
	closeErr error
}

func (f *faultCreateFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: file, writeErr: f.writeErr, syncErr: f.syncErr, closeErr: f.closeErr}, nil
}

// faultRenameFs wraps an afero.Fs and injects a Rename error.
type faultRenameFs struct {
	afero.Fs
	renameErr error
}

func (f *faultRenameFs) Rename(oldname, newname string) error {
	if f.renameErr != nil {
		return f.renameErr
	}
	return f.Fs.Rename(oldname, newname)
}

// TestSaveDeletionSnapshotWriteError covers the temp.Write error path.
func TestSaveDeletionSnapshotWriteError(t *testing.T) {
	fs := &faultCreateFs{
		Fs:       afero.NewMemMapFs(),
		writeErr: errors.New("disk full"),
	}
	state := deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
		{ProjectID: "project-delete-0123456789", Generation: 1, State: DeletionStateDeleting, Targets: []DeletionTarget{{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}}},
	}}
	_, err := saveDeletionSnapshotFS(fs, "/state", state, deletionStoreFaults{})
	if err == nil || !errContains(err, "write temp deletion state") {
		t.Fatalf("saveDeletionSnapshotFS write error = %v, want write temp deletion state", err)
	}
}

// TestSaveDeletionSnapshotSyncError covers the temp.Sync error path.
func TestSaveDeletionSnapshotSyncError(t *testing.T) {
	fs := &faultCreateFs{
		Fs:      afero.NewMemMapFs(),
		syncErr: errors.New("sync failed"),
	}
	state := deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
		{ProjectID: "project-delete-0123456789", Generation: 1, State: DeletionStateDeleting, Targets: []DeletionTarget{{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}}},
	}}
	_, err := saveDeletionSnapshotFS(fs, "/state", state, deletionStoreFaults{})
	if err == nil || !errContains(err, "sync temp deletion state") {
		t.Fatalf("saveDeletionSnapshotFS sync error = %v, want sync temp deletion state", err)
	}
}

// TestSaveDeletionSnapshotCloseError covers the temp.Close error path.
func TestSaveDeletionSnapshotCloseError(t *testing.T) {
	fs := &faultCreateFs{
		Fs:       afero.NewMemMapFs(),
		closeErr: errors.New("close failed"),
	}
	state := deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
		{ProjectID: "project-delete-0123456789", Generation: 1, State: DeletionStateDeleting, Targets: []DeletionTarget{{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}}},
	}}
	_, err := saveDeletionSnapshotFS(fs, "/state", state, deletionStoreFaults{})
	if err == nil || !errContains(err, "close temp deletion state") {
		t.Fatalf("saveDeletionSnapshotFS close error = %v, want close temp deletion state", err)
	}
}

// TestSaveDeletionSnapshotRenameError covers the Rename error path.
func TestSaveDeletionSnapshotRenameError(t *testing.T) {
	fs := &faultRenameFs{Fs: afero.NewMemMapFs(), renameErr: errors.New("cross-device link")}
	state := deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
		{ProjectID: "project-delete-0123456789", Generation: 1, State: DeletionStateDeleting, Targets: []DeletionTarget{{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}}},
	}}
	_, err := saveDeletionSnapshotFS(fs, "/state", state, deletionStoreFaults{})
	if err == nil || !errContains(err, "rename deletion state") {
		t.Fatalf("saveDeletionSnapshotFS rename error = %v, want rename deletion state", err)
	}
}

// TestSaveDeletionSnapshotBeforeRenameError covers the BeforeRename fault path.
func TestSaveDeletionSnapshotBeforeRenameError(t *testing.T) {
	fs := afero.NewMemMapFs()
	state := deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
		{ProjectID: "project-delete-0123456789", Generation: 1, State: DeletionStateDeleting, Targets: []DeletionTarget{{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}}},
	}}
	_, err := saveDeletionSnapshotFS(fs, "/state", state, deletionStoreFaults{
		BeforeRename: func() error { return errors.New("before rename fault") },
	})
	if err == nil || !errContains(err, "before rename fault") {
		t.Fatalf("saveDeletionSnapshotFS BeforeRename error = %v", err)
	}
}

// TestCloneDeletionSnapshot covers the cloneDeletionSnapshot function.
func TestCloneDeletionSnapshot(t *testing.T) {
	original := deletionSnapshot{
		Version: deletionSnapshotVersion,
		Records: []DeletionRecord{
			{ProjectID: "project-delete-0123456789", Generation: 1, State: DeletionStateDeleting, Targets: []DeletionTarget{{Ref: "local:" + covTestThreadID, ThreadID: covTestThreadID}}},
		},
	}
	clone := cloneDeletionSnapshot(original)
	if clone.Version != original.Version {
		t.Fatalf("clone version = %d, want %d", clone.Version, original.Version)
	}
	if len(clone.Records) != 1 {
		t.Fatalf("clone records = %d, want 1", len(clone.Records))
	}
	clone.Records[0].ProjectID = "modified"
	if original.Records[0].ProjectID == "modified" {
		t.Fatalf("clone is not a deep copy")
	}
}

// TestValidateDeletionSnapshotInvalidTarget covers the normalizeDeletionTargets
// error path in validateDeletionSnapshot.
func TestValidateDeletionSnapshotInvalidTarget(t *testing.T) {
	badTarget := deletionSnapshot{Version: deletionSnapshotVersion, Records: []DeletionRecord{
		{ProjectID: "project-delete-0123456789", Generation: 1, State: DeletionStateDeleting, Targets: []DeletionTarget{{Ref: "", ThreadID: ""}}},
	}}
	if err := validateDeletionSnapshot(badTarget); err == nil {
		t.Fatalf("validateDeletionSnapshot with empty target should error")
	}
}

func errContains(err error, sub string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for i := 0; i <= len(msg)-len(sub); i++ {
		if msg[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
