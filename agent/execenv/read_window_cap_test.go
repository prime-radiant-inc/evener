package execenv

import (
	"errors"
	"strings"
	"testing"
)

// TestScanLineWindowCapsTheRenderedWindow covers the cap's blind spot: it used
// to count only line content, so a file of empty lines added 0 per row while
// each row still wrote a number, a tab and a newline into the output. A file of
// empty lines with a large limit therefore never reached the cap and the window
// grew without bound, where the whole-file read of the same file refused.
//
// The cap is shrunk rather than the fixture grown: at the real 128 MiB cap the
// fixture would have to render 128 MiB to prove the same thing.
//
// Not parallel: MaxWholeFileReadBytes is package-wide.
func TestScanLineWindowCapsTheRenderedWindow(t *testing.T) {
	orig := MaxWholeFileReadBytes
	MaxWholeFileReadBytes = 64 << 10
	t.Cleanup(func() { MaxWholeFileReadBytes = orig })

	// 20,000 empty lines: 20 KB of content, but each row renders 6 bytes, so the
	// rendered window passes the 64 KiB cap well before the file ends.
	body := strings.Repeat("\n", 20000)
	_, err := scanLineWindow(t.Context(), strings.NewReader(body), nil, 1, 1<<30)
	rle, capped := errors.AsType[*ReadLimitError](err)
	if !capped {
		t.Fatalf("empty-line window of a %d-byte file = %v, want a *ReadLimitError: the cap counted content only", len(body), err)
	}
	if rle.Limit != MaxWholeFileReadBytes {
		t.Fatalf("ReadLimitError.Limit = %d, want %d", rle.Limit, MaxWholeFileReadBytes)
	}

	// The same cap must not disturb an ordinary window: 2,000 empty lines render
	// ~12 KB, well under it, and must come back as rows.
	got, err := scanLineWindow(t.Context(), strings.NewReader(strings.Repeat("\n", 2000)), nil, 1, DefaultReadLines)
	if err != nil {
		t.Fatalf("ordinary empty-line window: %v", err)
	}
	if rows := strings.Count(got, "\n"); rows != 2000 {
		t.Fatalf("ordinary empty-line window = %d rows, want 2000", rows)
	}
}
