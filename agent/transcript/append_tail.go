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
type appendTail struct {
	mu sync.Mutex
	// nextSeq is the sequence number the next appended entry takes.
	nextSeq int
	// lastWriter is the writer whose append (or resume) last moved the file's
	// end. Any other writer's handle position may be behind that end.
	lastWriter *Writer

	// info identifies the file (os.SameFile) and refs counts the open writers
	// sharing this tail; both are guarded by openTails.mu.
	info os.FileInfo
	refs int
}

// openTails holds the tail of every transcript file some writer in this
// process has open. A process holds few transcripts open at once, so a linear
// os.SameFile scan on open is cheap and follows the file however it was named.
var openTails struct {
	mu    sync.Mutex
	tails []*appendTail
}

// acquireAppendTail returns the tail shared by every open writer on f's file,
// creating it for the first. A file os.SameFile cannot identify — one on an
// in-memory test filesystem — gets a tail of its own.
func acquireAppendTail(f afero.File) (*appendTail, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat transcript file: %w", err)
	}
	if !os.SameFile(info, info) {
		return &appendTail{}, nil
	}
	openTails.mu.Lock()
	defer openTails.mu.Unlock()
	for _, tail := range openTails.tails {
		if os.SameFile(tail.info, info) {
			tail.refs++
			return tail, nil
		}
	}
	tail := &appendTail{info: info, refs: 1}
	openTails.tails = append(openTails.tails, tail)
	return tail, nil
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
