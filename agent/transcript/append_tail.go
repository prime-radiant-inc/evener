package transcript

import (
	"fmt"
	"os"
	"slices"
	"sync"

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
	// move counts the times a writer positioned its handle at the file's end
	// to append (or opened the file). A writer whose last recorded move is
	// not the current one may have a handle position behind the end.
	move uint64

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
	for _, tail := range openTails.tails {
		if os.SameFile(tail.info, info) {
			tail.refs++
			return tail, nil
		}
	}
	pin := pinFile(f.Name(), info)
	if pin == nil {
		return &appendTail{}, nil
	}
	tail := &appendTail{info: info, pin: pin, refs: 1}
	openTails.tails = append(openTails.tails, tail)
	return tail, nil
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
