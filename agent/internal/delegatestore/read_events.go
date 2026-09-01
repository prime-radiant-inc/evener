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
// all before Fold ever sees the input.
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
	if limits.MaxBytes > 0 {
		raw, err = io.ReadAll(io.LimitReader(f, limits.MaxBytes+1))
		if err == nil && int64(len(raw)) > limits.MaxBytes {
			return nil, ReadDiagnostics{}, fmt.Errorf("%w: %s exceeds %d raw bytes", ErrScanLimitExceeded, path, limits.MaxBytes)
		}
	} else {
		raw, err = io.ReadAll(f)
	}
	if err != nil {
		return nil, ReadDiagnostics{}, fmt.Errorf("delegatestore: read %s: %w", path, err)
	}
	diagnostics := ReadDiagnostics{TornTail: len(raw) > 0 && raw[len(raw)-1] != '\n'}
	events, err := decodeLogContext(ctx, raw, true, limits.MaxEvents)
	if err != nil {
		return nil, diagnostics, err
	}
	if _, err := Fold(events); err != nil {
		return nil, diagnostics, fmt.Errorf("delegatestore: fold: %w", err)
	}
	return cloneEvents(events), diagnostics, nil
}
