package agent

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/transcript"
)

// syncFailFS lets the header write's fsync succeed, then fails every later
// Sync once armed. That mirrors the session's transcript writer (SyncInterval
// 1s), where the inherited-context record is appended after construction.
type syncFailFS struct {
	afero.Fs
	armed *atomic.Bool
}

type syncFailFile struct {
	afero.File
	armed *atomic.Bool
}

func (f syncFailFile) Sync() error {
	if f.armed.Load() {
		return errors.New("injected sync failure")
	}
	return f.File.Sync()
}

func (fs syncFailFS) Create(name string) (afero.File, error) {
	f, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return syncFailFile{File: f, armed: fs.armed}, nil
}

func (fs syncFailFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return syncFailFile{File: f, armed: fs.armed}, nil
}

// The inherited-context manifest is what lets a restarted fork resume the
// parent's retained tail instead of its summary alone. It must be durable
// before NewSession returns: a buffered Append with SyncInterval=1s can return
// success for a record an immediate crash loses. A fsync failure during that
// write must fail initialization rather than look like success.
func TestWriteInheritedContextRecordFailsWhenNotDurable(t *testing.T) {
	armed := &atomic.Bool{}
	fs := syncFailFS{Fs: afero.NewMemMapFs(), armed: armed}
	w, err := transcript.NewWriterWithFS(fs, "/child.jsonl", transcript.Header{SessionID: "child"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	w.SyncInterval = time.Second
	armed.Store(true)

	if err := writeInheritedContextRecord(w, "parent", []int{0}); err == nil {
		t.Fatal("writeInheritedContextRecord = nil, want an error when the manifest cannot be made durable")
	}
}
