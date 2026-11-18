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
// agent's transcript.DefaultMaxLineBytes: jobstore.Event carries only modest
// string fields (command, description, task, paths), so no legitimate line
// is expected to come remotely close to this — it exists to bound a single
// pathologically long or corrupt line's memory cost, independent of
// however large the surrounding file legitimately is.
const DefaultMaxLineBytes = 128 << 20

// openScanEventsFile opens a journal for scanning; a test swaps it to
// observe the reads a scan issues. Production never assigns it.
var openScanEventsFile = func(path string) (io.ReadSeekCloser, error) {
	return os.Open(path)
}

// ScanLimits bounds a context-aware journal scan. A zero value performs an
// unbounded scan (matching ReadEvents' behavior), except for MaxLineBytes,
// which always applies (defaulting to DefaultMaxLineBytes) — a single line's
// memory cost is bounded unconditionally, not opt-in.
type ScanLimits struct {
	// MaxBytes and MaxEvents are optional, whole-file safety valves — 0
	// means unlimited. The job-activity loader (the incremental-fold path)
	// does not set them: an append-only journal folding to O(new events)
	// per request has no reason to cut a legitimate session's history
	// short at a fixed size, so these are not pathology tripwires the way
	// MaxLineBytes is. They remain available for a caller that wants a
	// hard whole-file ceiling regardless.
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

// ReadDiagnostics reports what a job-journal read could not make whole. It is
// deliberately the same shape as delegatestore.ReadDiagnostics: retained job
// history and retained delegate history answer the same question — was the
// history returned complete — so a consumer that already reports one store's
// diagnostics reports the other's the same way.
type ReadDiagnostics struct {
	// TornTail is set when the scan stopped at an unterminated, syntactically
	// incomplete final line: an in-flight append racing the read — or an
	// append that will never finish. The events before it are returned
	// normally; the torn line is not part of them.
	TornTail bool
}

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

// ScanEventsWithDiagnostics is ScanEvents starting at byte 0 that also returns
// the read's diagnostics. See ScanEventsFromWithDiagnostics for the contract.
func ScanEventsWithDiagnostics(ctx context.Context, path string, limits ScanLimits) ([]Event, ReadDiagnostics, error) {
	events, _, diagnostics, err := ScanEventsFromWithDiagnostics(ctx, path, 0, limits)
	return events, diagnostics, err
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
// toOffset is where this call actually got to: just past the last line it
// successfully decoded, whether or not that line's trailing newline had
// landed yet. A successful decode makes a record complete and final on its
// own: JSON's brace/bracket balancing means a valid decode can never be a
// byte-prefix of a longer, still-arriving record on the same line, so once
// json.Unmarshal succeeds there is nothing left pending for it but the
// newline formality (or subsequent, separate lines) — toOffset advances
// past it exactly like a terminated line, and its event IS included.
// toOffset stops short of a line, and that line's event is excluded, only
// when the line fails to decode at all — a genuinely incomplete in-flight
// append racing the read, tolerated exactly as ReadEvents/ScanEvents have
// always tolerated it — so the NEXT ScanEventsFrom call picks the same
// bytes back up once they are complete, rather than silently skipping or
// double-counting them.
//
// When MaxBytes or MaxEvents is hit, ScanEventsFrom returns the events
// successfully decoded before the limit fired ALONGSIDE ErrScanLimitExceeded
// (a non-nil slice with a non-nil error, toOffset 0), so a caller that
// specifically recognizes ErrScanLimitExceeded can degrade to a truncated-
// but-honest partial result instead of discarding everything already read.
//
// A malformed JSON line returns its partial result the same way and for the
// same reason: the events decoded BEFORE the bad line are still true statements
// about this journal, so they are returned alongside the parse error, with
// toOffset 0 because the scan did not reach a resumable record boundary. A
// caller that recognizes neither sentinel and treats any non-nil error as a
// failed read ignores the slice, which stays correct: the read did fail, and
// reusing toOffset would resume inside the damaged line.
//
// Which errors carry a decoded prefix is a property of the ERROR, not of the
// slice: match ErrScanLimitExceeded and ErrLineTooLong explicitly rather than
// inferring the case from a non-empty result.
//
//   - ErrScanLimitExceeded: the prefix. The limit fired after those events.
//   - a malformed-JSON error: the prefix. The scan stopped at one bad line.
//   - ErrLineTooLong: NOTHING (nil events, toOffset 0). A single line that long
//     is corruption serious enough that there is nothing safe to salvage.
//   - every other failure — open, seek, a read error, a cancelled context, and
//     the missing-file case that returns no error at all — also carries NO
//     prefix, but for an unrelated reason: the scan never got far enough to
//     decode one. An empty slice is therefore NOT evidence that the journal is
//     oversized or corrupt; only matching ErrLineTooLong says that.
func ScanEventsFrom(ctx context.Context, path string, fromOffset int64, limits ScanLimits) ([]Event, int64, error) {
	events, offset, _, err := ScanEventsFromWithDiagnostics(ctx, path, fromOffset, limits)
	return events, offset, err
}

// ScanEventsFromWithDiagnostics is ScanEventsFrom plus the scan's diagnostics:
// whether it stopped at a torn trailing line instead of at a clean record
// boundary. Every other outcome (clean EOF, a hard parse error, a limit, a
// cancellation) leaves ReadDiagnostics at its zero value — a limit is already
// reported by the error, and a torn tail is reported in addition to the events
// the scan did keep.
func ScanEventsFromWithDiagnostics(ctx context.Context, path string, fromOffset int64, limits ScanLimits) ([]Event, int64, ReadDiagnostics, error) {
	f, err := openScanEventsFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, ReadDiagnostics{}, nil
		}
		return nil, 0, ReadDiagnostics{}, fmt.Errorf("jobstore: read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if fromOffset > 0 {
		if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
			return nil, 0, ReadDiagnostics{}, fmt.Errorf("jobstore: seek %s: %w", path, err)
		}
	}

	maxLineBytes := int(limits.MaxLineBytes)
	if maxLineBytes <= 0 {
		maxLineBytes = DefaultMaxLineBytes
	}
	// 64 KiB matches the doctor's bounded readers.
	reader := bufio.NewReaderSize(f, 64*1024)
	events := make([]Event, 0)
	var diagnostics ReadDiagnostics
	totalBytes := fromOffset
	offset := fromOffset
	lineNum := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, ReadDiagnostics{}, err
		}
		// Checked before reading the next line, not just before decoding
		// it: a MaxEvents-only scan (MaxLineBytes left at its generous
		// default) could otherwise still pay the cost of buffering an
		// oversized next line — up to MaxLineBytes — only to discover
		// afterward the budget was already spent. jobstore is one event
		// per line (unlike delegatestore's batches), so this single
		// check, run once per line before it is read, is also sufficient
		// on its own — no line can ever contribute more than the one
		// event this check already accounts for.
		if limits.MaxEvents > 0 && len(events) >= limits.MaxEvents {
			if err := ctx.Err(); err != nil {
				return nil, 0, ReadDiagnostics{}, err
			}
			return events, 0, ReadDiagnostics{}, fmt.Errorf("%w: %s exceeds %d events", ErrScanLimitExceeded, path, limits.MaxEvents)
		}
		line, terminated, consumed, readErr := linecap.ReadLine(ctx, reader, maxLineBytes)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			if errors.Is(readErr, linecap.ErrTooLong) {
				return nil, 0, ReadDiagnostics{}, fmt.Errorf("%w: %s line %d", ErrLineTooLong, path, lineNum+1)
			}
			return nil, 0, ReadDiagnostics{}, fmt.Errorf("jobstore: read %s: %w", path, readErr)
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
			// spaced further apart.
			if err := ctx.Err(); err != nil {
				return nil, 0, ReadDiagnostics{}, err
			}
			return events, 0, ReadDiagnostics{}, fmt.Errorf("%w: %s exceeds %d raw bytes", ErrScanLimitExceeded, path, limits.MaxBytes)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			if !terminated {
				// A whitespace-only final line with no newline is the same
				// in-flight append a torn record is: bytes landed, the line
				// did not finish. Report it as torn rather than as a clean
				// boundary, so a caller cannot mistake a half-written append
				// for a complete read.
				diagnostics.TornTail = true
				break
			}
			offset += consumed
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			if !terminated && isIncompleteTrailingJSON(line, err) {
				// Tolerate only an unterminated, syntactically incomplete final
				// line from an in-flight append. A newline-terminated or
				// definitively malformed final record is durable corruption.
				diagnostics.TornTail = true
				break
			}
			// Durable corruption: the scan cannot get past this line, and it says
			// so with an error. The records decoded BEFORE it are still true
			// statements about the journal, so they are returned alongside that
			// error exactly as a limit returns its partial result — a caller that
			// must answer a question one of those records settles (does this
			// journal hold a start for job X?) can use them, and a caller that
			// treats any error as fatal ignores them as before. offset stays 0
			// because the scan did NOT reach a resumable record boundary.
			return events, 0, diagnostics, fmt.Errorf("jobstore: parse event line %d in %s: %w", lineNum, path, err)
		}
		// A successful decode makes this record complete and final
		// regardless of whether its trailing newline has landed yet (see
		// ScanEventsFrom's doc comment on toOffset): include it and
		// advance offset past it exactly like a terminated line,
		// unconditionally on terminated. Advancing offset only when
		// terminated while always including the event would let a later
		// incremental call, resuming from the stale offset once the
		// newline landed, re-decode and duplicate this same event for a
		// caller that concatenates its prior fold with the new delta
		// (extendHistoricalJobFold's exact shape).
		events = append(events, e)
		offset += consumed
	}
	return events, offset, diagnostics, nil
}
