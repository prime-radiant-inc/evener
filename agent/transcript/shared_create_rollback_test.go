package transcript

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// afterFirstWriteFs is the real filesystem with a callback after the first
// write through each file it creates: for a new transcript, that write is the
// header. It lets a test put another writer's step exactly between creating
// a transcript's header and the creating writer becoming usable.
type afterFirstWriteFs struct {
	afero.Fs
	afterFirstWrite func()
}

func (fs afterFirstWriteFs) Create(name string) (afero.File, error) {
	f, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &afterFirstWriteFile{File: f, afterFirstWrite: fs.afterFirstWrite}, nil
}

type afterFirstWriteFile struct {
	afero.File
	afterFirstWrite func()
}

func (f *afterFirstWriteFile) Write(p []byte) (int, error) {
	n, err := f.File.Write(p)
	if hook := f.afterFirstWrite; hook != nil {
		f.afterFirstWrite = nil
		hook()
	}
	return n, err
}

// A writer that opens a transcript while it is being created — after its
// header is on disk — shares the creating writer's tail, so the creator's
// first append lands after the opener's record instead of over it.
func TestOpeningATranscriptWhileItIsCreatedSharesItsTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	var opener *Writer
	fs := afterFirstWriteFs{Fs: afero.NewOsFs(), afterFirstWrite: func() {
		var err error
		opener, _, err = OpenWriterForSession(path, sharedFileHeader.SessionID)
		if err != nil {
			t.Fatalf("open transcript while it is created: %v", err)
		}
		if err := opener.AppendDurable(steeringTurn("opener")); err != nil {
			t.Fatalf("opener AppendDurable: %v", err)
		}
	}}
	creator, err := newWriterFS(fs, path, sharedFileHeader, false)
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	defer creator.Close() //nolint:errcheck // assertion fixture
	defer opener.Close()  //nolint:errcheck // assertion fixture
	if err := creator.Append(steeringTurn("creator")); err != nil {
		t.Fatalf("creator Append: %v", err)
	}
	requireOneSequenceInFileOrder(t, path, []string{"opener", "creator"})
}

// Two creates of one transcript racing each other: the one that loses is
// refused without touching the file the winner is writing.
func TestConcurrentCreatesOfOneTranscriptLetOneWin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	fs := afterFirstWriteFs{Fs: afero.NewOsFs(), afterFirstWrite: func() {
		if second, err := NewWriterNoSync(path, sharedFileHeader); err == nil {
			_ = second.Close()
			t.Error("a second create of a transcript being created succeeded")
		}
	}}
	first, err := newWriterFS(fs, path, sharedFileHeader, false)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	defer first.Close() //nolint:errcheck // assertion fixture
	if err := first.Append(steeringTurn("first")); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	requireOneSequenceInFileOrder(t, path, []string{"first"})
}

// syncFailsFile is a real transcript file whose fsync fails, so a durable
// append through it rolls back.
type syncFailsFile struct{ afero.File }

var errInjectedSync = errors.New("injected fsync failure")

func (syncFailsFile) Sync() error { return errInjectedSync }

// One writer's durable append fails and rolls back while another writer is
// open on the file: the rolled-back record is gone, its sequence number is
// taken by the next append from either writer, and both writers keep
// appending at the file's end.
func TestRollbackThenAnotherWriterAppendsKeepsOneSequence(t *testing.T) {
	path := newSharedFileTranscript(t)
	failing := openSharedFileWriter(t, path)
	defer failing.Close() //nolint:errcheck // assertion fixture
	other := openSharedFileWriter(t, path)
	defer other.Close() //nolint:errcheck // assertion fixture

	if err := failing.Append(steeringTurn("failing before")); err != nil {
		t.Fatalf("failing Append: %v", err)
	}
	if err := other.Append(steeringTurn("other before")); err != nil {
		t.Fatalf("other Append: %v", err)
	}
	realFile := failing.file
	failing.file = syncFailsFile{realFile}
	if err := failing.AppendDurable(steeringTurn("rolled back")); !errors.Is(err, errInjectedSync) {
		t.Fatalf("AppendDurable with failing fsync = %v, want the injected failure", err)
	}
	failing.file = realFile
	if err := other.Append(steeringTurn("other after")); err != nil {
		t.Fatalf("other Append after rollback: %v", err)
	}
	if err := failing.Append(steeringTurn("failing after")); err != nil {
		t.Fatalf("failing Append after rollback: %v", err)
	}
	requireOneSequenceInFileOrder(t, path, []string{"before resume", "failing before", "other before", "other after", "failing after"})
}
