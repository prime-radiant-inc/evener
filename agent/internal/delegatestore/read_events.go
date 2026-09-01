package delegatestore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

type ReadDiagnostics struct {
	TornTail bool
}

// ScanLimits bounds a context-aware journal scan. A zero value performs an
// unbounded scan (matching ReadEventsWithDiagnostics' behavior).
type ScanLimits struct {
	MaxBytes  int64 // raw bytes read from the file; 0 means unlimited
	MaxEvents int   // ceiling on retained events, checked per BATCH LINE not per event (see ScanEvents) — the final count can exceed this by up to one batch's worth; 0 means unlimited
}

// ErrScanLimitExceeded reports that ScanEvents stopped because a journal
// exceeded a configured raw byte or event ceiling before reaching the end of
// the file.
var ErrScanLimitExceeded = errors.New("delegatestore: journal exceeds scan limit")

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
// counterpart: it streams the journal line by line via bufio.Reader instead
// of reading it whole (matching jobstore.ScanEvents' approach), so a raw
// byte or event ceiling in limits is enforced, and ctx is checked, BEFORE
// the rest of an oversized file is retained or decoded — even a MaxEvents: 1
// scan against a 128 MiB journal only ever reads a small prefix of it, and a
// canceled request stops before the next line is read rather than after a
// whole-file decode completes (#448 roborev finding: the previous
// io.ReadAll-then-decode shape could allocate and decode up to MaxBytes
// before either check ran; this also fixes #806, the io.ReadAll
// pre-allocation regression).
//
// When a byte or event ceiling is hit, ScanEvents degrades to partial rather
// than discarding everything read: it returns the events successfully
// decoded before the limit fired ALONGSIDE ErrScanLimitExceeded (a non-nil
// slice with a non-nil error), reusing the same torn-tail tolerance an
// in-flight append already relies on to trim a byte-limited read back to its
// last complete batch line. A caller that specifically recognizes
// ErrScanLimitExceeded can keep the partial result; ordinary callers, which
// check err before touching the result (every existing caller of ReadEvents
// does), are unaffected.
func ScanEvents(ctx context.Context, path string, limits ScanLimits) ([]Event, ReadDiagnostics, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ReadDiagnostics{}, nil
		}
		return nil, ReadDiagnostics{}, fmt.Errorf("delegatestore: read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	// Bound the underlying reader too, not just the per-chunk byte count
	// below: bufio.Reader.ReadBytes has no size cap of its own (unlike
	// Scanner's MaxScanTokenSize) — it keeps growing its fragment buffer
	// until it finds '\n' or hits EOF, so a single pathologically long
	// unterminated line would otherwise be buffered in full before the
	// MaxBytes check below ever runs. Capping the source at MaxBytes+1 means
	// the worst a single line can force us to hold is MaxBytes+1 bytes, and
	// the check below still sees and reports the overage on the next read.
	var src io.Reader = f
	if limits.MaxBytes > 0 {
		src = io.LimitReader(f, limits.MaxBytes+1)
	}
	reader := bufio.NewReader(src)
	var totalBytes int64

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

	if err := ctx.Err(); err != nil {
		return nil, ReadDiagnostics{}, err
	}
	headerChunk, headerErr := reader.ReadBytes('\n')
	if headerErr != nil && headerErr != io.EOF {
		return nil, ReadDiagnostics{}, fmt.Errorf("delegatestore: read %s: %w", path, headerErr)
	}
	totalBytes += int64(len(headerChunk))
	if len(headerChunk) == 0 {
		return nil, ReadDiagnostics{}, errors.New("delegatestore: missing version header")
	}
	if err := checkByteLimit(); err != nil {
		// Whether this is a genuine ErrScanLimitExceeded or a coincident
		// context.Canceled, nothing has been decoded yet (not even the
		// header), so both cases return identically — there is no partial
		// result to preserve either way.
		return nil, ReadDiagnostics{}, err
	}
	if headerErr == io.EOF {
		// The whole file is a single line with no newline anywhere — not
		// even the header is complete. Nothing survives a trim-back-to-
		// last-complete-line here, so this is always a hard error: the
		// tolerant-trailing-tail behavior below only ever applies to the
		// LAST batch line, never to the header itself.
		return nil, ReadDiagnostics{}, errors.New("delegatestore: unterminated version header")
	}
	var header versionRecord
	if err := decodeJSONLine(headerChunk[:len(headerChunk)-1], &header); err != nil {
		return nil, ReadDiagnostics{}, fmt.Errorf("delegatestore: decode version header: %w", err)
	}
	if header.Version != CurrentVersion {
		return nil, ReadDiagnostics{}, fmt.Errorf("delegatestore: unsupported version %d", header.Version)
	}

	var events []Event
	var diagnostics ReadDiagnostics
	var limitErr error
	lineNum := 1
	for {
		if err := ctx.Err(); err != nil {
			return nil, ReadDiagnostics{}, err
		}
		chunk, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return nil, ReadDiagnostics{}, fmt.Errorf("delegatestore: read %s: %w", path, readErr)
		}
		terminated := readErr == nil
		if !terminated && len(chunk) == 0 {
			break // clean EOF right after the last complete line: no torn tail
		}
		lineNum++
		totalBytes += int64(len(chunk))
		if err := checkByteLimit(); err != nil {
			if !errors.Is(err, ErrScanLimitExceeded) {
				// Coincident cancellation: report it directly, discarding
				// the prefix decoded so far, same as the top-of-loop check.
				return nil, ReadDiagnostics{}, err
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
				return nil, ReadDiagnostics{}, err
			}
			limitErr = fmt.Errorf("%w: %s exceeds %d events", ErrScanLimitExceeded, path, limits.MaxEvents)
			break
		}
		line := chunk[:len(chunk)-1]
		var batch batchRecord
		if err := decodeJSONLine(line, &batch); err != nil {
			return nil, ReadDiagnostics{}, fmt.Errorf("delegatestore: decode batch line %d: %w", lineNum, err)
		}
		if len(batch.Events) == 0 {
			return nil, ReadDiagnostics{}, fmt.Errorf("delegatestore: batch line %d has no events", lineNum)
		}
		events = append(events, batch.Events...)
	}

	// No internal Fold call here (roborev finding on #807): scanRootDelegateState,
	// this function's only caller, already folds the returned events itself,
	// so folding here too was pure duplicated work — and worse, it could
	// silently replace a genuine ErrScanLimitExceeded with a Fold error
	// whenever the partial prefix a limit degrades to didn't happen to fold
	// cleanly on its own (e.g. ending mid-relationship, referencing a
	// delegate whose Created event is in a later, never-read batch),
	// defeating the documented degrade-to-partial contract in exactly the
	// cases it exists for. Folding — and deciding what an unfoldable
	// partial prefix means — is the caller's job.
	cloned := cloneEvents(events)
	if limitErr != nil {
		return cloned, diagnostics, limitErr
	}
	return cloned, diagnostics, nil
}
