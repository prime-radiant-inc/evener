package jobstore

import (
	"bufio"
	"bytes"
	"context"
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
	// MaxBytes/MaxEvents — see DefaultMaxLineBytes. Unlike those two, it is
	// not a truncation knob: a line beyond it returns ErrLineTooLong with no
	// partial result, which establishes only that the line exceeded this
	// scan's budget, not that the journal is damaged — no scan observed a
	// byte it could not decode. See ScanEventsFrom for the per-error contract.
	MaxLineBytes int64
	// EndOffset, when positive, is the last byte this scan may read: the
	// scan behaves as though the file ended there, returning cleanly at that
	// boundary rather than reporting a limit error and rather than reading
	// the bytes past it. A reader that folds a journal being appended to
	// sets it to the committed length the owning store reports
	// (Store.CommittedJournal), so an append still in flight — and one whose
	// fsync then rolls it back — cannot enter the fold. 0 means read to the
	// file's own end, unless Bounded says otherwise.
	EndOffset int64
	// Bounded makes EndOffset authoritative even when it is zero: an empty
	// committed prefix is EOF at byte 0, not an unbounded scan of whatever the
	// file holds. A reader that samples a committed length sets both, because
	// the first append to a journal it opened empty is exactly the append it
	// must not fold. A caller with no boundary to respect leaves both zero and
	// keeps the historical unbounded read.
	Bounded bool
}

// bounded reports whether EndOffset constrains this scan. A positive offset
// always does; Bounded adds the empty-prefix case, which a committed length of
// zero would otherwise turn into "read everything".
func (l ScanLimits) bounded() bool {
	return l.Bounded || l.EndOffset > 0
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
	// Corrupt is set when the scan stopped at a line it could not decode: the
	// journal itself is damaged, and the events before that line are a decoded
	// prefix rather than the whole history. It is the diagnostic counterpart of
	// the parse error, and it exists so a caller that reports integrity can say
	// "this journal is damaged" without also saying it about a journal it could
	// not open at all — an unreadable file is a fact about access, not about the
	// bytes.
	Corrupt bool
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
// the read's diagnostics. See scanEventsFromWithDiagnostics for the contract.
func ScanEventsWithDiagnostics(ctx context.Context, path string, limits ScanLimits) ([]Event, ReadDiagnostics, error) {
	events, _, diagnostics, err := scanEventsFromWithDiagnostics(ctx, path, 0, limits)
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
//   - ErrLineTooLong: NOTHING (nil events, toOffset 0). The line exceeded the
//     caller's own MaxLineBytes, so there is nothing safe to salvage — but no
//     damage was observed, and ReadDiagnostics stays at its zero value.
//   - every other failure — open, seek, a read error, a cancelled context, and
//     the missing-file case that returns no error at all — also carries NO
//     prefix, but for an unrelated reason: the scan never got far enough to
//     decode one. An empty slice is therefore NOT evidence that the journal is
//     oversized or corrupt; only matching ErrLineTooLong says that.
func ScanEventsFrom(ctx context.Context, path string, fromOffset int64, limits ScanLimits) ([]Event, int64, error) {
	events, offset, _, err := scanEventsFromWithDiagnostics(ctx, path, fromOffset, limits)
	return events, offset, err
}

// scanEventsFromWithDiagnostics is ScanEventsFrom plus the scan's diagnostics:
// whether it stopped at a torn trailing line instead of at a clean record
// boundary, and whether it stopped at a line it could not decode. Those are
// different outcomes with different consequences — a torn tail is an append that
// has not finished, a corrupt line is permanent damage — so they are reported
// separately rather than folded into one flag.
//
// A clean EOF, a limit, a cancellation, and every failure to REACH the file
// (open, seek, a read error, the missing-file case) leave ReadDiagnostics at its
// zero value — a limit is already reported by the error, and the file-level
// failures establish nothing about the bytes, so reporting damage for them would
// describe something no scan observed. A torn tail or Corrupt is reported in
// addition to the events the scan did keep.
func scanEventsFromWithDiagnostics(ctx context.Context, path string, fromOffset int64, limits ScanLimits) ([]Event, int64, ReadDiagnostics, error) {
	f, err := openScanEventsFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, ReadDiagnostics{}, nil
		}
		return nil, 0, ReadDiagnostics{}, fmt.Errorf("jobstore: read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	// The file's own length, which is what makes "unterminated" accountable. A
	// record with no newline is one of two different things, and the boundary
	// alone cannot tell them apart:
	//
	//   - bytes still arriving. The boundary sits short of the file, so the
	//     newline may yet land. Withhold the record; the next read picks it up.
	//   - the journal's actual final record. The boundary IS physical EOF: a
	//     complete JSON record with no trailing newline is what a replacement
	//     journal or an out-of-band writer leaves. Nothing more is coming, and
	//     withholding it hides the last completed job on every refresh, forever,
	//     because a stable file never grows a newline.
	//
	// The length comes from the handle rather than a path stat, so the file this
	// scan reads is the file it sizes. A handle that cannot be sized leaves
	// physicalEOF false, which keeps the conservative behaviour: withhold.
	//
	// Sizing seeks, so the read position is always restored to the offset this
	// scan starts at. A scan from zero has no later seek to put it back, and a
	// handle left at EOF would read no events at all.
	var physicalEOF bool
	if limits.bounded() {
		if end, seekErr := f.Seek(0, io.SeekEnd); seekErr == nil {
			physicalEOF = limits.EndOffset >= end
		}
	}
	if fromOffset > 0 || limits.bounded() {
		if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
			return nil, 0, ReadDiagnostics{}, fmt.Errorf("jobstore: seek %s: %w", path, err)
		}
	}

	maxLineBytes := int(limits.MaxLineBytes)
	if maxLineBytes <= 0 {
		maxLineBytes = DefaultMaxLineBytes
	}
	// Cap the source at the committed boundary BEFORE buffering: the boundary
	// is not advice. Bytes past it belong to an append that has not committed,
	// so they must not be read, buffered, or decoded — a buffered reader over
	// the whole file reads them ahead of the scan, and a boundary landing
	// inside a line would then decode that line in full. Capped here, such a
	// line is cut short and fails to decode, which the trailing-line tolerance
	// below already treats as an append still in flight: withheld, never
	// decoded.
	var source io.Reader = f
	if limits.bounded() {
		// A caller resuming past the committed boundary has nothing left to
		// read, which max states without a branch.
		source = io.LimitReader(f, max(limits.EndOffset-fromOffset, 0))
	}
	// 64 KiB matches the doctor's bounded readers.
	reader := bufio.NewReaderSize(source, 64*1024)
	events := make([]Event, 0)
	var diagnostics ReadDiagnostics
	totalBytes := fromOffset
	offset := fromOffset
	lineNum := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, ReadDiagnostics{}, err
		}
		// The committed boundary is EOF for this scan. The capped source above
		// is what keeps uncommitted bytes from being read at all; this check
		// is what makes reaching the boundary a clean completion — every byte
		// in between belongs to a complete line — rather than a limit breach.
		if limits.bounded() && offset >= limits.EndOffset {
			return events, offset, diagnostics, nil
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
				// The scan never decoded this line, so it cannot say the bytes
				// are damaged: the cap is the CALLER's own bound, and the
				// default scan decodes the same journal fine. Corrupt is for
				// damage a scan observed, so this leaves it at its zero value
				// and reports the over-cap condition through ErrLineTooLong
				// alone — with no partial result, because a line over the cap
				// is not safely salvageable.
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
				// A blank or whitespace-only tail is not a record that failed to
				// finish: the writer starts every event with a JSON object, so
				// these bytes cannot be the beginning of one. Treat it as a clean
				// boundary, exactly as a terminated blank line is, because
				// reporting it as torn would turn a readable journal into
				// "unavailable" and mark an intact running row unconfirmed for a
				// newline that has not landed yet.
				break
			}
			offset += consumed
			continue
		}
		var e Event
		// DecodeJournalEvent rather than json.Unmarshal directly: this is the other
		// place in the package that parses a journal line, and a caller measuring
		// what a read costs must be able to count both. Counting only the store's
		// own decode reports a full fold rescan as zero records read.
		if err := DecodeJournalEvent(line, &e); err != nil {
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
			diagnostics.Corrupt = true
			return events, 0, diagnostics, fmt.Errorf("jobstore: parse event line %d in %s: %w", lineNum, path, err)
		}
		if !terminated && limits.bounded() && offset+consumed >= limits.EndOffset && !physicalEOF {
			// The boundary landed exactly on the last byte of a line whose
			// newline has not been committed. The record decodes, so the check
			// below would include it and advance past a byte the store has not
			// acknowledged — and a boundary one byte short of the newline is
			// exactly what a reader sees mid-append. Withhold it: the next read
			// picks it up once the newline is committed, which is the same
			// contract the incomplete-line tolerance above states. An unbounded
			// scan is unaffected — limits.bounded() is false there — so a
			// complete record at a natural EOF is still included.
			//
			// physicalEOF carves out the other reading of the same shape: the
			// boundary is not short of the file, it IS the file. Nothing is
			// still arriving, so withholding the record would hide it from every
			// future read of an unchanged journal.
			break
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
