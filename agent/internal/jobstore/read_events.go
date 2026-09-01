package jobstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"primeradiant.com/evener/agent/internal/linecap"
)

// DefaultMaxLineBytes is the per-line cap ScanEvents/ScanEventsFrom apply
// when a call leaves ScanLimits.MaxLineBytes at 0. 128 MiB matches
// agent's transcriptJSONLMaxLineBytes: jobstore.Event carries only modest
// string fields (command, description, task, paths), so no legitimate line
// is expected to come remotely close to this — it exists to bound a single
// pathologically long or corrupt line's memory cost, independent of
// however large the surrounding file legitimately is.
const DefaultMaxLineBytes = 128 << 20

// ScanLimits bounds a context-aware journal scan. A zero value performs an
// unbounded scan (matching ReadEvents' behavior), except for MaxLineBytes,
// which always applies (defaulting to DefaultMaxLineBytes) — a single line's
// memory cost is bounded unconditionally, not opt-in.
type ScanLimits struct {
	// MaxBytes and MaxEvents are optional, whole-file safety valves — 0
	// means unlimited. #448's incremental-fold round stopped setting these
	// from the job-activity loader: an append-only journal folding to O(new
	// events) per request has no remaining reason to cut a legitimate
	// session's history short at a fixed size, so they are not treated as
	// pathology tripwires here the way MaxLineBytes is. They remain
	// available for a caller that still wants a hard whole-file ceiling.
	MaxBytes  int64
	MaxEvents int
	// MaxLineBytes bounds a single line's memory cost independently of
	// MaxBytes/MaxEvents — see DefaultMaxLineBytes. Unlike those two, this
	// is not a truncation knob: exceeding it always reports ErrLineTooLong
	// and returns no partial result, because a single line that long is
	// corruption, not a legitimately large journal.
	MaxLineBytes int64
}

// ErrScanLimitExceeded reports that ScanEvents stopped because a journal
// exceeded a configured raw byte or event ceiling before reaching the end of
// the file.
var ErrScanLimitExceeded = errors.New("jobstore: journal exceeds scan limit")

// ErrLineTooLong reports that a single line exceeded MaxLineBytes. Re-
// exported from linecap so callers can check it without importing that
// package themselves.
var ErrLineTooLong = linecap.ErrTooLong

// ReadEvents reads and decodes every event from a jobs.jsonl file WITHOUT
// opening a Store for append. It is the read-only forensic path: a caller that
// only inspects settled state (evener-doctor) uses this instead of Open, which
// opens read-write and creates the file. A missing file yields no events (not an
// error). An unterminated, syntactically incomplete trailing line — an
// in-flight append racing the read — is tolerated, but durable or definitively
// malformed input is reported as corruption.
//
// ReadEvents performs an unbounded scan; ScanEvents is the context-aware,
// budget-enforcing counterpart used by callers (like the persisted
// job-activity loader) that must bound how much of a journal they will
// decode.
func ReadEvents(path string) ([]Event, error) {
	return ScanEvents(context.Background(), path, ScanLimits{})
}

// ScanEvents is ReadEvents' context-aware, budget-enforcing counterpart: it
// is ScanEventsFrom starting at byte 0, discarding the returned offset. See
// ScanEventsFrom for the full contract.
func ScanEvents(ctx context.Context, path string, limits ScanLimits) ([]Event, error) {
	events, _, err := ScanEventsFrom(ctx, path, 0, limits)
	return events, err
}

// ScanEventsFrom is ScanEvents' incremental counterpart: it seeks to
// fromOffset before reading, so a caller that already decoded everything up
// to fromOffset (foldcache.Cache, the incremental job-activity loader) reads
// and decodes only the events appended since. It streams the journal line by
// line instead of reading it whole, checking ctx between records so a
// canceled request stops before finishing a large delta, and bounds a
// single line's memory cost via MaxLineBytes independently of the file's
// total size (see ScanLimits).
//
// toOffset is where this call actually got to: just past the last complete,
// newline-terminated line it consumed. It is never mid-line, and it never
// counts a genuinely unterminated trailing line (an in-flight append racing
// the read, tolerated exactly as ReadEvents/ScanEvents have always tolerated
// it) — that line's events ARE included in the returned slice for this one
// call, matching the existing tolerance contract, but toOffset stops short
// of it so the NEXT ScanEventsFrom call picks the same bytes back up once
// they are complete, rather than silently skipping or double-counting them.
//
// When MaxBytes or MaxEvents is hit, ScanEventsFrom returns the events
// successfully decoded before the limit fired ALONGSIDE ErrScanLimitExceeded
// (a non-nil slice with a non-nil error, toOffset 0), so a caller that
// specifically recognizes ErrScanLimitExceeded can degrade to a truncated-
// but-honest partial result instead of discarding everything already read.
// MaxLineBytes is different: exceeding it returns ErrLineTooLong with NO
// partial result (nil events, toOffset 0) — a single line that long is
// corruption serious enough that there is nothing safe to salvage from
// this scan.
func ScanEventsFrom(ctx context.Context, path string, fromOffset int64, limits ScanLimits) ([]Event, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("jobstore: read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if fromOffset > 0 {
		if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
			return nil, 0, fmt.Errorf("jobstore: seek %s: %w", path, err)
		}
	}

	maxLineBytes := int(limits.MaxLineBytes)
	if maxLineBytes <= 0 {
		maxLineBytes = DefaultMaxLineBytes
	}
	reader := bufio.NewReader(f)
	events := make([]Event, 0)
	totalBytes := fromOffset
	offset := fromOffset
	lineNum := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		line, terminated, consumed, readErr := linecap.ReadLine(reader, maxLineBytes)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			if errors.Is(readErr, linecap.ErrTooLong) {
				return nil, 0, fmt.Errorf("%w: %s line %d", ErrLineTooLong, path, lineNum+1)
			}
			return nil, 0, fmt.Errorf("jobstore: read %s: %w", path, readErr)
		}
		if errors.Is(readErr, io.EOF) {
			break // clean EOF, nothing left to process
		}
		lineNum++
		totalBytes += consumed
		if limits.MaxBytes > 0 && totalBytes > limits.MaxBytes {
			// A cancellation landing during the read just above, on the
			// very line that also pushed totalBytes over the limit, would
			// otherwise never get a chance to be seen: the next
			// iteration's top-of-loop ctx check never runs, because this
			// return happens first. Check here too so cancellation always
			// wins over a coincident limit, not just when the two are
			// spaced further apart (roborev finding on #448).
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
			return events, 0, fmt.Errorf("%w: %s exceeds %d raw bytes", ErrScanLimitExceeded, path, limits.MaxBytes)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			if !terminated {
				break // trailing blank/whitespace-only unterminated tail
			}
			offset += consumed
			continue
		}
		if limits.MaxEvents > 0 && len(events) >= limits.MaxEvents {
			// Same coincidence risk as the byte-limit check above.
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
			return events, 0, fmt.Errorf("%w: %s exceeds %d events", ErrScanLimitExceeded, path, limits.MaxEvents)
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			if !terminated && isIncompleteTrailingJSON(line, err) {
				// Tolerate only an unterminated, syntactically incomplete final
				// line from an in-flight append. A newline-terminated or
				// definitively malformed final record is durable corruption.
				break
			}
			return nil, 0, fmt.Errorf("jobstore: parse event line %d in %s: %w", lineNum, path, err)
		}
		events = append(events, e)
		if !terminated {
			break // consumed the final unterminated line; EOF follows, offset stays short of it
		}
		offset += consumed
	}
	return events, offset, nil
}
