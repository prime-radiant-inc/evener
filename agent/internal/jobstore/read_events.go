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
)

// ScanLimits bounds a context-aware journal scan. A zero value performs an
// unbounded scan (matching ReadEvents' behavior).
type ScanLimits struct {
	MaxBytes  int64 // raw bytes read from the file; 0 means unlimited
	MaxEvents int   // decoded events retained; 0 means unlimited
}

// ErrScanLimitExceeded reports that ScanEvents stopped because a journal
// exceeded a configured raw byte or event ceiling before reaching the end of
// the file.
var ErrScanLimitExceeded = errors.New("jobstore: journal exceeds scan limit")

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

// ScanEvents is ReadEvents' context-aware, budget-enforcing counterpart. It
// streams the journal line by line instead of reading it whole, so a raw
// byte or event ceiling in limits is enforced BEFORE the rest of an oversized
// file is retained or decoded, and it checks ctx between each decoded record
// so a canceled request stops before finishing a large journal. Line-
// tolerance behavior — a missing file yielding no events, and an
// unterminated, syntactically incomplete final line from an in-flight append
// being tolerated rather than treated as corruption — matches ReadEvents
// exactly.
//
// When a byte or event ceiling is hit, ScanEvents returns the events
// successfully decoded before the limit fired ALONGSIDE ErrScanLimitExceeded
// (a non-nil slice with a non-nil error), not nil — so a caller that
// specifically recognizes ErrScanLimitExceeded can degrade to a truncated-
// but-honest partial result instead of discarding everything already read.
// Ordinary callers, which check err before touching the result (every
// existing caller of ReadEvents does), are unaffected: they never look at
// the partial slice.
func ScanEvents(ctx context.Context, path string, limits ScanLimits) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("jobstore: read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	// Bound the underlying reader too, not just the per-chunk byte count
	// below: bufio.Reader.ReadBytes has no size cap of its own (unlike
	// Scanner's MaxScanTokenSize) — it keeps growing its fragment buffer
	// until it finds '\n' or hits EOF, so a single pathologically long
	// unterminated line would otherwise be buffered in full before the
	// MaxBytes check ever runs. Capping the source at MaxBytes+1 means the
	// worst a single line can force us to hold is MaxBytes+1 bytes, and the
	// check below still sees and reports the overage on the next read.
	var src io.Reader = f
	if limits.MaxBytes > 0 {
		src = io.LimitReader(f, limits.MaxBytes+1)
	}
	reader := bufio.NewReader(src)
	events := make([]Event, 0)
	var totalBytes int64
	lineNum := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return nil, fmt.Errorf("jobstore: read %s: %w", path, readErr)
		}
		terminated := readErr == nil
		if !terminated && len(chunk) == 0 {
			break // clean EOF, nothing left to process
		}
		lineNum++
		totalBytes += int64(len(chunk))
		if limits.MaxBytes > 0 && totalBytes > limits.MaxBytes {
			return events, fmt.Errorf("%w: %s exceeds %d raw bytes", ErrScanLimitExceeded, path, limits.MaxBytes)
		}
		line := chunk
		if terminated {
			line = chunk[:len(chunk)-1] // drop the trailing '\n'
		}
		if len(bytes.TrimSpace(line)) == 0 {
			if !terminated {
				break // trailing blank/whitespace-only unterminated tail
			}
			continue
		}
		if limits.MaxEvents > 0 && len(events) >= limits.MaxEvents {
			return events, fmt.Errorf("%w: %s exceeds %d events", ErrScanLimitExceeded, path, limits.MaxEvents)
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			if !terminated && isIncompleteTrailingJSON(line, err) {
				// Tolerate only an unterminated, syntactically incomplete final
				// line from an in-flight append. A newline-terminated or
				// definitively malformed final record is durable corruption.
				break
			}
			return nil, fmt.Errorf("jobstore: parse event line %d in %s: %w", lineNum, path, err)
		}
		events = append(events, e)
		if !terminated {
			break // consumed the final unterminated line; EOF follows
		}
	}
	return events, nil
}
