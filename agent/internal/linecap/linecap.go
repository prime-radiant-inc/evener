// Package linecap reads one newline-delimited line at a time with its memory
// cost bounded independently of how much file surrounds it, and independent
// of any separate ceiling on the file as a whole. It exists so a store's
// per-file scan ceiling can be removed or raised without reopening the
// single-pathological-line hole that ceiling used to close as a side effect
// (agent/internal/jobstore and agent/internal/delegatestore's #448
// journal scanners both need exactly this).
//
// The same technique already exists as agent/transcript.ReadLine, for
// transcript files specifically; this package is the one place jobstore and
// delegatestore — which intentionally do not depend on each other or on the
// transcript package for their own journal formats — both pull it from,
// rather than each carrying its own copy.
package linecap

import (
	"bufio"
	"context"
	"errors"
	"io"
)

// ErrTooLong reports that a single line exceeded the configured cap.
var ErrTooLong = errors.New("linecap: line exceeds max bytes")

// ReadLine reads one line from reader via repeated bufio.Reader.ReadSlice
// calls, so a single pathologically long line costs at most maxLineBytes of
// held memory regardless of how much more data follows in the stream --
// bufio.Reader.ReadBytes/ReadString have no cap of their own; each keeps
// growing its result until it finds '\n' or hits EOF. maxLineBytes <= 0 is
// rejected by the caller's own limits validation, not defaulted here: unlike
// agent/transcript.ReadLine (which always has a real caller-relevant
// default), jobstore/delegatestore's callers compute their own default from
// a package constant, so silently substituting one here would hide a caller
// bug instead of surfacing it.
//
// line is returned WITHOUT its trailing newline, whether that terminator was
// present (terminated=true) or the stream ended first (terminated=false, the
// shape an in-flight, not-yet-flushed final write leaves). consumed is the
// exact number of raw bytes read from reader for this line, INCLUDING the
// terminator when present, so a caller can track a precise byte offset
// regardless of whether the cap fired.
//
// err is ctx.Err() the moment ctx is observed canceled or expired (checked
// once per internal read, including while draining an over-limit line — see
// below); io.EOF when reader had nothing left at all (a clean end between
// lines, not mid-line); ErrTooLong when the accumulated line exceeded
// maxLineBytes, OR when a line already known to be over the limit still has
// not found its terminator after draining maxLineBytes more bytes looking
// for one (roborev's #448 round-2 regression finding: without this second
// cap, a corrupt tail that never yields a newline — a completely realistic
// shape for a truncated or crash-interrupted append, the original #448
// failure mode — drained an unbounded amount of the rest of the stream one
// buffer refill at a time before ever giving up; that case is now treated
// exactly like hitting EOF while over limit, since neither leaves anything
// safe to salvage). On ErrTooLong, the offending line's bytes actually read
// are still fully drained from reader before returning (so the caller's
// next ReadLine call starts cleanly at the following line, PROVIDED the
// drain cap did not itself cut the search short — see maxLineBytes above)
// -- they are simply never retained in line, which is nil in that case.
func ReadLine(ctx context.Context, reader *bufio.Reader, maxLineBytes int) (line []byte, terminated bool, consumed int64, err error) {
	overLimit := false
	var drained int64 // bytes read AFTER overLimit became true; capped at maxLineBytes
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, consumed, ctxErr
		}
		fragment, readErr := reader.ReadSlice('\n')
		consumed += int64(len(fragment))
		payload := fragment
		if readErr == nil && len(payload) > 0 {
			payload = payload[:len(payload)-1] // drop the trailing '\n'
		}
		if !overLimit {
			if len(line)+len(payload) > maxLineBytes {
				line = nil
				overLimit = true
			} else {
				line = append(line, payload...)
			}
		} else {
			drained += int64(len(fragment))
			if drained > int64(maxLineBytes) {
				return nil, false, consumed, ErrTooLong
			}
		}
		switch {
		case readErr == nil:
			if overLimit {
				return nil, false, consumed, ErrTooLong
			}
			return line, true, consumed, nil
		case errors.Is(readErr, bufio.ErrBufferFull):
			continue // fragment is a partial line; keep accumulating
		case errors.Is(readErr, io.EOF):
			if overLimit {
				return nil, false, consumed, ErrTooLong
			}
			if len(line) == 0 {
				return nil, false, consumed, io.EOF
			}
			return line, false, consumed, nil // unterminated trailing line
		default:
			return nil, false, consumed, readErr
		}
	}
}
