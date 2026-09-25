package transcript

import (
	"fmt"
	"os"
	"path/filepath"
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

	// path and info identify the file and refs counts the open writers
	// sharing this tail; all are guarded by openTails.mu.
	path string
	info os.FileInfo
	refs int
}

// openTails holds the tail of every transcript file some writer in this
// process has open. A process holds few transcripts open at once, so a linear
// scan on open is cheap.
//
// A tail matches on the file's canonical path as well as os.SameFile. A writer
// dropped without Close gives its hold back only when it is collected, and the
// runtime may close its handle first; once its file is removed the filesystem
// can give the same inode to a new transcript, which must not inherit the dead
// file's sequence. Hard links to one transcript get separate tails; nothing
// links transcripts.
var openTails struct {
	mu    sync.Mutex
	tails []*appendTail
}

// acquireAppendTail returns the tail shared by every open writer on f's file,
// creating it for the first. f.Name() is the path f was opened by. A file os.SameFile cannot identify — one on an
// in-memory test filesystem — gets a tail of its own.
func acquireAppendTail(f afero.File) (*appendTail, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat transcript file: %w", err)
	}
	// os.SameFile is false for any FileInfo that did not come from the os
	// package, even compared with itself: such a file cannot be matched, so
	// it is never shared.
	if !os.SameFile(info, info) {
		return &appendTail{}, nil
	}
	path := canonicalPath(f.Name())
	openTails.mu.Lock()
	defer openTails.mu.Unlock()
	for _, tail := range openTails.tails {
		if tail.path == path && os.SameFile(tail.info, info) {
			tail.refs++
			return tail, nil
		}
	}
	tail := &appendTail{path: path, info: info, refs: 1}
	openTails.tails = append(openTails.tails, tail)
	return tail, nil
}

// canonicalPath names a file by an absolute path with its symlinks resolved,
// so every spelling of the same path matches. A path that cannot be resolved
// keeps the form it has; at worst its writer does not share a tail.
func canonicalPath(name string) string {
	abs, err := filepath.Abs(name)
	if err != nil {
		return filepath.Clean(name)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// moved records a writer taking the file's end and returns the new move.
// Callers hold t.mu.
func (t *appendTail) moved() uint64 {
	t.move++
	return t.move
}

// release drops one writer's hold on the tail, forgetting it after the last.
func (t *appendTail) release() {
	if t.info == nil {
		return
	}
	openTails.mu.Lock()
	defer openTails.mu.Unlock()
	t.refs--
	if t.refs == 0 {
		openTails.tails = slices.DeleteFunc(openTails.tails, func(other *appendTail) bool { return other == t })
	}
}
