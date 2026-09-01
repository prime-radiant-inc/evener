package delegatestore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"primeradiant.com/evener/agent/internal/linecap"
)

// DefaultMaxLineBytes is the per-line cap ScanEvents/ScanEventsFrom apply
// when a call leaves ScanLimits.MaxLineBytes at 0. 128 MiB — a
// delegate_created event embeds the full frozen role prompt and skill
// bodies (agent/internal/delegatestore/record.go), so a single legitimate
// batch line is larger than jobstore's, but still nowhere near this: it
// exists to bound a single pathologically long or corrupt line's memory
// cost, independent of however large the surrounding file legitimately is.
const DefaultMaxLineBytes = 128 << 20

type ReadDiagnostics struct {
	TornTail bool
}

// ScanLimits bounds a context-aware journal scan. A zero value performs an
// unbounded scan (matching ReadEventsWithDiagnostics' behavior), except for
// MaxLineBytes, which always applies (defaulting to DefaultMaxLineBytes) —
// a single line's memory cost is bounded unconditionally, not opt-in.
type ScanLimits struct {
	// MaxBytes and MaxEvents are optional, whole-file safety valves — 0
	// means unlimited. #448's incremental-fold round stopped setting these
	// from the job-activity loader: an append-only journal folding to O(new
	// events) per request has no remaining reason to cut a legitimate
	// root's delegate history short at a fixed size, so they are not
	// treated as pathology tripwires here the way MaxLineBytes is. They
	// remain available for a caller that still wants a hard whole-file
	// ceiling. MaxEvents is checked once per BATCH LINE, not per event (see
	// ScanEventsFrom) — a batch that pushes the count over the limit is
	// still retained in full, so the final count can exceed MaxEvents by up
	// to one batch's worth (roborev finding on #807's saturation commit;
	// truncating mid-batch is not safe for fold semantics).
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
var ErrScanLimitExceeded = errors.New("delegatestore: journal exceeds scan limit")

// ErrLineTooLong reports that a single line exceeded MaxLineBytes.
// Re-exported from linecap so callers can check it without importing that
// package themselves.
var ErrLineTooLong = linecap.ErrTooLong

func ReadEvents(path string) ([]Event, error) {
	events, _, err := ReadEventsWithDiagnostics(path)
	return events, err
}

// ReadEventsWithDiagnostics performs an unbounded scan; ScanEvents is the
// context-aware, budget-enforcing counterpart used by callers (like the
// persisted job-activity loader) that must bound how much of a journal they
// will decode.
func ReadEventsWithDiagnostics(path string) ([]Event, ReadDiagnostics, error) {
	return ScanEvents(context.Background(), path, ScanLimits{})
}

// ScanEvents is ReadEventsWithDiagnostics' context-aware, budget-enforcing
// counterpart: it is ScanEventsFrom starting at byte 0, discarding the
// returned offset. See ScanEventsFrom for the full contract.
func ScanEvents(ctx context.Context, path string, limits ScanLimits) ([]Event, ReadDiagnostics, error) {
	events, _, diagnostics, err := ScanEventsFrom(ctx, path, 0, limits)
	return events, diagnostics, err
}

// ScanEventsFrom is ScanEvents' incremental counterpart: it seeks to
// fromOffset before reading, so a caller that already decoded everything up
// to fromOffset (foldcache.Cache, the incremental job-activity loader) reads
// and decodes only the batch lines appended since. fromOffset == 0 reads and
// validates the version header first, exactly as ScanEvents always has;
// fromOffset > 0 skips straight to it, since the header never recurs past
// byte zero — a caller passing a nonzero fromOffset is asserting that byte
// range was already read and validated by an earlier call starting at 0.
//
// It streams the journal line by line via bufio.Reader instead of reading it
// whole, checking ctx between records so a canceled request stops before
// finishing a large delta, and bounds a single line's memory cost via
// MaxLineBytes independently of the file's total size (see ScanLimits).
//
// toOffset is where this call actually got to: just past the last complete,
// newline-terminated batch line it consumed. It is never mid-line, and it
// never counts a genuinely unterminated trailing line (an in-flight append
// racing the read, tolerated exactly as before) — that line contributes no
// events to the returned slice, so the NEXT ScanEventsFrom call picks the
// same bytes back up once they are complete, rather than silently skipping
// or double-counting them.
//
// fromOffset == 0 folds the decoded events (discarding the result) purely to
// validate they form a coherent sequence starting at 1, matching every
// existing caller's expectation; fromOffset > 0 skips that validation, since
// a delta's sequence numbers do not start at 1 — a caller extending a prior
// fold incrementally (agent/internal/foldcache's Extend callbacks) is
// expected to validate sequence continuity itself against whatever it last
// saw, the same way Store.Append assigns and Fold verifies sequence numbers
// but this function does not re-derive "what came before" on its own.
//
// When MaxBytes or MaxEvents is hit, ScanEventsFrom degrades to partial
// rather than discarding everything read: it returns the events
// successfully decoded before the limit fired ALONGSIDE
// ErrScanLimitExceeded (a non-nil slice with a non-nil error, toOffset 0).
// MaxLineBytes is different: exceeding it returns ErrLineTooLong with NO
// partial result (nil events, toOffset 0) — a single line that long is
// corruption serious enough that there is nothing safe to salvage from this
// scan.
func ScanEventsFrom(ctx context.Context, path string, fromOffset int64, limits ScanLimits) ([]Event, int64, ReadDiagnostics, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, ReadDiagnostics{}, nil
		}
		return nil, 0, ReadDiagnostics{}, fmt.Errorf("delegatestore: read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if fromOffset > 0 {
		if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
			return nil, 0, ReadDiagnostics{}, fmt.Errorf("delegatestore: seek %s: %w", path, err)
		}
	}

	maxLineBytes := int(limits.MaxLineBytes)
	if maxLineBytes <= 0 {
		maxLineBytes = DefaultMaxLineBytes
	}
	reader := bufio.NewReader(f)
	totalBytes := fromOffset

	// checkByteLimit folds the ctx-first-on-coincidence rule (roborev
	// finding on #448) into one place shared by the header line and every
	// batch line: a cancellation landing on the very read that also pushes
	// totalBytes over the limit must still be reported as Canceled, not
	// silently swallowed by ErrScanLimitExceeded.
	checkByteLimit := func() error {
		if limits.MaxBytes == 0 || totalBytes <= limits.MaxBytes {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("%w: %s exceeds %d raw bytes", ErrScanLimitExceeded, path, limits.MaxBytes)
	}

	offset := fromOffset
	if fromOffset == 0 {
		if err := ctx.Err(); err != nil {
			return nil, 0, ReadDiagnostics{}, err
		}
		headerLine, headerTerminated, headerConsumed, headerErr := linecap.ReadLine(reader, maxLineBytes)
		if headerErr != nil && !errors.Is(headerErr, io.EOF) {
			if errors.Is(headerErr, linecap.ErrTooLong) {
				return nil, 0, ReadDiagnostics{}, fmt.Errorf("%w: %s version header", ErrLineTooLong, path)
			}
			return nil, 0, ReadDiagnostics{}, fmt.Errorf("delegatestore: read %s: %w", path, headerErr)
		}
		totalBytes += headerConsumed
		if headerConsumed == 0 {
			return nil, 0, ReadDiagnostics{}, errors.New("delegatestore: missing version header")
		}
		if err := checkByteLimit(); err != nil {
			// Whether this is a genuine ErrScanLimitExceeded or a
			// coincident context.Canceled, nothing has been decoded yet
			// (not even the header), so both cases return identically —
			// there is no partial result to preserve either way.
			return nil, 0, ReadDiagnostics{}, err
		}
		if !headerTerminated {
			// The whole file is a single line with no newline anywhere —
			// not even the header is complete. Nothing survives a trim-
			// back-to-last-complete-line here, so this is always a hard
			// error: the tolerant-trailing-tail behavior below only ever
			// applies to the LAST batch line, never to the header itself.
			return nil, 0, ReadDiagnostics{}, errors.New("delegatestore: unterminated version header")
		}
		var header versionRecord
		if err := decodeJSONLine(headerLine, &header); err != nil {
			return nil, 0, ReadDiagnostics{}, fmt.Errorf("delegatestore: decode version header: %w", err)
		}
		if header.Version != CurrentVersion {
			return nil, 0, ReadDiagnostics{}, fmt.Errorf("delegatestore: unsupported version %d", header.Version)
		}
		offset += headerConsumed
	}

	var events []Event
	var diagnostics ReadDiagnostics
	var limitErr error
	lineNum := 1
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, ReadDiagnostics{}, err
		}
		line, terminated, consumed, readErr := linecap.ReadLine(reader, maxLineBytes)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			if errors.Is(readErr, linecap.ErrTooLong) {
				return nil, 0, ReadDiagnostics{}, fmt.Errorf("%w: %s batch line %d", ErrLineTooLong, path, lineNum+1)
			}
			return nil, 0, ReadDiagnostics{}, fmt.Errorf("delegatestore: read %s: %w", path, readErr)
		}
		if errors.Is(readErr, io.EOF) {
			break // clean EOF right after the last complete line: no torn tail
		}
		lineNum++
		totalBytes += consumed
		if err := checkByteLimit(); err != nil {
			if !errors.Is(err, ErrScanLimitExceeded) {
				// Coincident cancellation: report it directly, discarding
				// the prefix decoded so far, same as the top-of-loop check.
				return nil, 0, ReadDiagnostics{}, err
			}
			limitErr = err
			break
		}
		if !terminated {
			// Final, unterminated batch line: an in-flight append racing
			// the read. Discard it — matching decodeLog's blanket tolerance
			// (no content-based heuristic) — and report a genuine torn
			// tail: this branch is only reached once neither ceiling above
			// has fired, so it reflects the real file, not an artificial
			// cutoff (roborev finding on #448).
			diagnostics.TornTail = true
			break
		}
		if limits.MaxEvents > 0 && len(events) >= limits.MaxEvents {
			// Checked BEFORE decoding this line, not after (roborev finding
			// on #807): a batch line can hold many events, so checking only
			// once the line is already fully decoded would still let a
			// MaxEvents: 1 scan materialize an oversized (or malformed —
			// see TestScanEvents_StopsBeforeDecodingOnceEventBudgetExhausted)
			// later batch just to discover afterward that the budget was
			// already spent. The tradeoff (roborev finding on #807's
			// saturation commit): this check is per LINE, not per event, so
			// a line that was allowed to decode because len(events) was
			// still under the limit can push events past MaxEvents by up to
			// that whole batch — deliberately not truncated mid-batch,
			// since a batch's events are not independently safe to split
			// for fold semantics (see ScanLimits.MaxEvents).
			if err := ctx.Err(); err != nil {
				return nil, 0, ReadDiagnostics{}, err
			}
			limitErr = fmt.Errorf("%w: %s exceeds %d events", ErrScanLimitExceeded, path, limits.MaxEvents)
			break
		}
		var batch batchRecord
		if err := decodeJSONLine(line, &batch); err != nil {
			return nil, 0, ReadDiagnostics{}, fmt.Errorf("delegatestore: decode batch line %d: %w", lineNum, err)
		}
		if len(batch.Events) == 0 {
			return nil, 0, ReadDiagnostics{}, fmt.Errorf("delegatestore: batch line %d has no events", lineNum)
		}
		events = append(events, batch.Events...)
		offset += consumed
	}

	// No internal Fold call here (roborev finding on #807, and independently
	// true for a delta too): extendHistoricalDelegateFold, this function's
	// only caller, already validates sequence continuity and folds each
	// event itself via delegatestore.Apply as it extends the cached state —
	// so folding here too, for either a from-the-start scan or a delta, is
	// pure duplicated work. Worse, it could silently replace a genuine
	// ErrScanLimitExceeded with a Fold error whenever the partial prefix a
	// limit degrades to didn't happen to fold cleanly on its own (e.g.
	// ending mid-relationship, referencing a delegate whose Created event is
	// in a later, never-read batch), defeating the documented
	// degrade-to-partial contract in exactly the cases it exists for.
	// Folding — and deciding what an unfoldable partial prefix means — is
	// the caller's job. (A delta, fromOffset > 0, could not have passed
	// Fold's own sequence check anyway: its Seq values do not start at 1.)
	cloned := cloneEvents(events)
	if limitErr != nil {
		return cloned, 0, diagnostics, limitErr
	}
	return cloned, offset, diagnostics, nil
}
