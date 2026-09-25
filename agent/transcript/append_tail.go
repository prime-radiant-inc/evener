package transcript

import (
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/spf13/afero"
)

// appendTail is the one sequence allocator and append point of a transcript
// file within this process. A session's writer and the short-lived writers the
// delegate-attention paths open on the same file share it, so appends from any
// of them take the next sequence number and land at the file's end: two writers
// each counting from what they saw at open duplicated sequence numbers, and a
// buffered append from a handle whose position predated another writer's record
// wrote over it.
//
// mu is held across a whole append — sequence allocation, write, fsync, and any
// rollback — and across a resume scan, so no writer observes a sequence number a
// rollback later returns, and no resume truncates another writer's record as a
// crash tail.
//
// The tail coordinates writers in this process only; another process appending
// to the same file is not coordinated.
type appendTail struct {
	mu sync.Mutex
	// nextSeq is the sequence number the next appended entry takes.
	nextSeq int
	// nextOrdinal is the entry ordinal the next recorded entry takes, and
	// recordedLength the length of the file's recorded prefix: the header and
	// every recorded entry line.
	nextOrdinal    uint64
	recordedLength int64
	// turns is the file's turn placement state: the running execution, the
	// open gap turn and the prelude. See Placement.
	turns turnPlacement
	// onRecorded, when set, is called for each recorded entry; see
	// Writer.OnRecorded.
	onRecorded func(Record)
	// move counts the times a writer positioned its handle at the file's end
	// to append (or opened the file). A writer whose last recorded move is
	// not the current one may have a handle position behind the end.
	move uint64
	// poisoned records a partial line some writer left at the file's end and
	// could not roll back. Every writer on the tail refuses to append after
	// it until a resume's scan cuts it off. It changes only under mu; it is
	// atomic so a writer can report it without waiting out another's append.
	poisoned atomic.Bool

	// info identifies the file and refs counts the open writers sharing this
	// tail; both are guarded by openTails.mu. pin is the tail's own handle on
	// the file: while it is open the filesystem cannot give the file's inode to
	// another file, so a registered tail never matches a different file — even
	// after a writer dropped without Close has had its handle closed by the
	// runtime and the file has been removed.
	info os.FileInfo
	pin  *os.File
	refs int
}

// openTails holds the tail of every transcript file some writer in this
// process has open, matched by os.SameFile so every name of a file (symlinked
// directory, relative path, hard link) finds the same tail. A process holds
// few transcripts open at once, so a linear scan on open is cheap.
var openTails struct {
	mu    sync.Mutex
	tails []*appendTail
}

// acquireAppendTail returns the tail shared by every open writer on f's file,
// creating it for the first. A file that cannot be pinned — one on an
// in-memory test filesystem — gets a tail of its own.
func acquireAppendTail(f afero.File) (*appendTail, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat transcript file: %w", err)
	}
	openTails.mu.Lock()
	defer openTails.mu.Unlock()
	if tail := findTailLocked(info); tail != nil {
		tail.refs++
		return tail, nil
	}
	pin := pinFile(f.Name(), info)
	if pin == nil {
		return &appendTail{}, nil
	}
	tail := &appendTail{info: info, pin: pin, refs: 1}
	openTails.tails = append(openTails.tails, tail)
	return tail, nil
}

// attachMu serializes, within this process, the two ways a writer comes to a
// transcript file. A create's check that no writer has the file open, its
// truncating create, and its tail registration are one step; so are an open's
// open and tail registration. Of two creates on one path exactly one wins, and
// a create never truncates a file a writer in this package has open or is
// opening. It does not cover a create racing the create or open of the same
// file in another process, or code that opens the file other than through
// this package.
var attachMu sync.Mutex

// createAppendTail creates the transcript at path through create and registers
// its tail, refusing if a writer in this process has the file open.
func createAppendTail(path string, create func() (afero.File, error)) (afero.File, *appendTail, error) {
	attachMu.Lock()
	defer attachMu.Unlock()
	if openInProcess(path) {
		return nil, nil, fmt.Errorf("create transcript file: %s is open in this process", path)
	}
	return attachLocked(create, "create transcript file")
}

// openAppendTail opens an existing transcript through open and registers its
// tail, so no create of the file can truncate it between the two.
func openAppendTail(open func() (afero.File, error)) (afero.File, *appendTail, error) {
	attachMu.Lock()
	defer attachMu.Unlock()
	return attachLocked(open, "open transcript for resume")
}

// attachLocked opens a file through open and registers its tail. Callers hold
// attachMu; what names the step in an open error.
func attachLocked(open func() (afero.File, error), what string) (afero.File, *appendTail, error) {
	f, err := open()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", what, err)
	}
	tail, err := acquireAppendTail(f)
	if err != nil {
		_ = f.Close() // cleanup on error path; the stat error is what matters
		return nil, nil, err
	}
	return f, tail, nil
}

// openInProcess reports whether a writer in this process has the file at path
// open. Only a file the os package can name is ever registered (see pinFile),
// so the lookup goes to the operating system whatever filesystem the caller
// writes through.
func openInProcess(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	openTails.mu.Lock()
	defer openTails.mu.Unlock()
	return findTailLocked(info) != nil
}

// findTailLocked returns the registered tail of the file info describes, or
// nil. Callers hold openTails.mu.
func findTailLocked(info os.FileInfo) *appendTail {
	for _, tail := range openTails.tails {
		if os.SameFile(tail.info, info) {
			return tail
		}
	}
	return nil
}

// pinFile opens a read-only handle on the file at name if it is the file info
// describes, or returns nil. os.SameFile is false for any FileInfo that did
// not come from the os package, so a file on another filesystem is never
// pinned; neither is one whose name no longer leads to it.
func pinFile(name string, info os.FileInfo) *os.File {
	if !os.SameFile(info, info) {
		return nil
	}
	pin, err := os.Open(name)
	if err != nil {
		return nil
	}
	if pinInfo, err := pin.Stat(); err != nil || !os.SameFile(pinInfo, info) {
		_ = pin.Close()
		return nil
	}
	return pin
}

// moved records a writer taking the file's end and returns the new move.
// Callers hold t.mu.
func (t *appendTail) moved() uint64 {
	t.move++
	return t.move
}

// release drops one writer's hold on the tail, forgetting it and closing its
// pin after the last.
func (t *appendTail) release() {
	if t.pin == nil {
		return
	}
	openTails.mu.Lock()
	defer openTails.mu.Unlock()
	t.refs--
	if t.refs == 0 {
		openTails.tails = slices.DeleteFunc(openTails.tails, func(other *appendTail) bool { return other == t })
		_ = t.pin.Close() // read-only handle; nothing to flush
	}
}

// AtRecordedBoundary runs fn under the append lock of the transcript at path,
// with its recorded length, when a writer in this process has the file open;
// it reports whether one does. No append records while fn runs, so a caller
// that also takes the lock its recorded-entry hook takes (a projection
// queue's mutex) sees the recorded length and what the hook has handed it as
// one snapshot: every entry up to the length already reached the hook, and
// every later one reaches it afterwards.
//
// The append lock comes first in that order: fn may take only locks the
// recorded-entry hook may take, and AtRecordedBoundary must never be called
// from a recorded-entry hook or while holding any lock an appender or a hook
// takes. It does no I/O under the lock; the stat that finds the file's tail
// happens before.
func AtRecordedBoundary(path string, fn func(recordedLength int64)) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("stat transcript: %w", err)
	}
	openTails.mu.Lock()
	tail := findTailLocked(info)
	openTails.mu.Unlock()
	if tail == nil {
		return false, nil
	}
	tail.mu.Lock()
	defer tail.mu.Unlock()
	fn(tail.recordedLength)
	return true, nil
}
