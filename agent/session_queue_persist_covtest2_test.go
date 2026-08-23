package agent

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// TestSaveQueuesFS_RemoveNonNotExistError covers the Remove error path
// (session_queue_persist.go:57-58): a Remove error that is not IsNotExist
// surfaces an error.
func TestSaveQueuesFS_RemoveNonNotExistError(t *testing.T) {
	// Pre-create the queue file so Remove is attempted, then fails because
	// the FS is read-only.
	base := afero.NewMemMapFs()
	dir := "/state/sessions/sid1"
	if err := base.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	queuesPath := queuesFilePath("/state", "sid1")
	if err := afero.WriteFile(base, queuesPath, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	roFs := afero.NewReadOnlyFs(base)
	err := saveQueuesFS(roFs, "/state", "sid1", nil, nil)
	if err == nil {
		t.Fatal("expected remove error")
	}
	if !strings.Contains(err.Error(), "remove empty queue snapshot") {
		t.Fatalf("error = %v, want remove error", err)
	}
}

// TestSaveQueuesFS_WriteTempError covers the write-temp-file error path
// (session_queue_persist.go:72-73): writing the temp file fails.
func TestSaveQueuesFS_WriteTempError(t *testing.T) {
	base := afero.NewMemMapFs()
	if err := base.MkdirAll("/state/sessions/sid1", 0o755); err != nil {
		t.Fatal(err)
	}
	fs := &errorWriteFileFs{Fs: base}
	steering := []steeringMessage{{Text: "steer"}}
	err := saveQueuesFS(fs, "/state", "sid1", steering, nil)
	if err == nil {
		t.Fatal("expected write temp error")
	}
	if !strings.Contains(err.Error(), "write temp queues file") {
		t.Fatalf("error = %v, want write temp error", err)
	}
}

// errorWriteFileFs wraps an afero.Fs to make OpenFile fail (afero.WriteFile
// uses OpenFile with O_CREATE|O_WRONLY|O_TRUNC).
type errorWriteFileFs struct {
	afero.Fs
}

func (fs *errorWriteFileFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	return nil, errors.New("injected openfile failure")
}

// TestSaveQueuesFS_RenameError covers the rename error path
// (session_queue_persist.go:75-77): renaming the temp file on a read-only FS
// fails. We need the WriteFile to succeed but Rename to fail. Use a custom FS.
func TestSaveQueuesFS_RenameError(t *testing.T) {
	base := afero.NewMemMapFs()
	if err := base.MkdirAll("/state/sessions/sid1", 0o755); err != nil {
		t.Fatal(err)
	}
	fs := &noRenameFs{Fs: base}
	steering := []steeringMessage{{Text: "steer"}}
	err := saveQueuesFS(fs, "/state", "sid1", steering, nil)
	if err == nil {
		t.Fatal("expected rename error")
	}
	if !strings.Contains(err.Error(), "rename queues file") {
		t.Fatalf("error = %v, want rename error", err)
	}
}

// noRenameFs wraps an afero.Fs to make Rename fail.
type noRenameFs struct {
	afero.Fs
}

func (fs *noRenameFs) Rename(old, new string) error {
	return errors.New("injected rename failure")
}

// TestLoadQueuesFS_ReadError covers the read error path
// (session_queue_persist.go:99-100): a ReadFile error that is not IsNotExist
// surfaces an error.
func TestLoadQueuesFS_ReadError(t *testing.T) {
	// Use a custom wrapper where Open fails with a non-NotExist error.
	errFs := &errorOpenFs{Fs: afero.NewMemMapFs()}
	_, _, err := loadQueuesFS(errFs, "/state", "sid1")
	if err == nil {
		t.Fatal("expected read error")
	}
	if !strings.Contains(err.Error(), "read queues") {
		t.Fatalf("error = %v, want read queues error", err)
	}
}

// errorOpenFs wraps an afero.Fs to make Open fail with a non-NotExist error.
type errorOpenFs struct {
	afero.Fs
}

func (fs *errorOpenFs) Open(name string) (afero.File, error) {
	return nil, errors.New("injected open failure")
}
