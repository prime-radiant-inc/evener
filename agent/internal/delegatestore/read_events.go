package delegatestore

import (
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
	MaxEvents int   // decoded events retained; 0 means unlimited
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
// counterpart: it refuses to read past limits.MaxBytes raw bytes, checks ctx
// between decoded batch lines so a canceled request stops before finishing a
// large journal, and refuses to retain more than limits.MaxEvents events —
// all before Fold ever sees more than that bounded prefix.
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
	if err := ctx.Err(); err != nil {
		return nil, ReadDiagnostics{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ReadDiagnostics{}, nil
		}
		return nil, ReadDiagnostics{}, fmt.Errorf("delegatestore: read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var raw []byte
	var limitErr error
	if limits.MaxBytes > 0 {
		raw, err = io.ReadAll(io.LimitReader(f, limits.MaxBytes+1))
		if err == nil && int64(len(raw)) > limits.MaxBytes {
			limitErr = fmt.Errorf("%w: %s exceeds %d raw bytes", ErrScanLimitExceeded, path, limits.MaxBytes)
		}
	} else {
		raw, err = io.ReadAll(f)
	}
	if err != nil {
		return nil, ReadDiagnostics{}, fmt.Errorf("delegatestore: read %s: %w", path, err)
	}
	diagnostics := ReadDiagnostics{TornTail: len(raw) > 0 && raw[len(raw)-1] != '\n'}
	// A byte-limited read almost always lands mid-batch-line; decode it with
	// the SAME tolerant-trailing-tail handling an in-flight append already
	// gets (tolerateUnterminatedTail=true trims back to the last complete
	// line) rather than treating the arbitrary cutoff as corruption — this
	// is what turns a byte-ceiling hit into a clean partial result instead
	// of a hard failure.
	events, decodeErr := decodeLogContext(ctx, raw, true, limits.MaxEvents)
	if decodeErr != nil {
		switch {
		case errors.Is(decodeErr, ErrScanLimitExceeded):
			// MaxEvents fired inside decode; events holds the valid prefix
			// decoded before that batch line — fall through to Fold it.
			limitErr = decodeErr
		case limitErr != nil:
			// The byte-limited buffer failed to decode at all (e.g. the
			// cutoff landed inside the version header) — nothing to salvage;
			// report the byte ceiling, which is the root cause.
			return nil, diagnostics, limitErr
		default:
			return nil, diagnostics, decodeErr
		}
	}
	if _, err := Fold(events); err != nil {
		return nil, diagnostics, fmt.Errorf("delegatestore: fold: %w", err)
	}
	cloned := cloneEvents(events)
	if limitErr != nil {
		return cloned, diagnostics, limitErr
	}
	return cloned, diagnostics, nil
}
