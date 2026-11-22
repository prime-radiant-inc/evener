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
// Not parallel: MaxWholeFileReadBytes and DefaultReadMaxBytes are package-wide.
func TestScanLineWindowCapsTheRenderedWindow(t *testing.T) {
	orig := MaxWholeFileReadBytes
	MaxWholeFileReadBytes = 64 << 10
	t.Cleanup(func() { MaxWholeFileReadBytes = orig })

	// With DefaultReadMaxBytes (48 KB) active, 20,000 empty lines stop cleanly
	// on a line boundary inside DefaultReadMaxBytes instead of growing past 64 KiB.
	body := strings.Repeat("\n", 20000)
	out, meta, err := scanLineWindowRich(t.Context(), strings.NewReader(body), int64(len(body)), "empty.txt", nil, new(1), new(1<<30))
	if err != nil {
		t.Fatalf("empty-line window with DefaultReadMaxBytes: %v", err)
	}
	if !meta.Truncated || meta.TruncatedBy != "chars" || len(out) > DefaultReadMaxBytes+512 {
		t.Fatalf("expected DefaultReadMaxBytes head stop: len=%d meta=%+v", len(out), meta)
	}

	// When DefaultReadMaxBytes is lifted above MaxWholeFileReadBytes, the
	// rendered-window MaxWholeFileReadBytes safety cap still catches 20,000 empty
	// lines before the file ends.
	origMaxBytes := DefaultReadMaxBytes
	DefaultReadMaxBytes = 1 << 20
	t.Cleanup(func() { DefaultReadMaxBytes = origMaxBytes })
	_, _, err = scanLineWindowRich(t.Context(), strings.NewReader(body), int64(len(body)), "empty.txt", nil, new(1), new(1<<30))
	rle, capped := errors.AsType[*ReadLimitError](err)
	if !capped {
		t.Fatalf("empty-line window of a %d-byte file = %v, want a *ReadLimitError: the cap counted content only", len(body), err)
	}
	if rle.Limit != MaxWholeFileReadBytes {
		t.Fatalf("ReadLimitError.Limit = %d, want %d", rle.Limit, MaxWholeFileReadBytes)
	}
	DefaultReadMaxBytes = origMaxBytes

	// The same cap must not disturb an ordinary window: 2,000 empty lines render
	// ~12 KB, well under it, and must come back as rows.
	got, meta, err := scanLineWindowRich(t.Context(), strings.NewReader(strings.Repeat("\n", 2000)), 2000, "empty.txt", nil, new(1), new(DefaultReadLines))
	if err != nil {
		t.Fatalf("ordinary empty-line window: %v", err)
	}
	if meta.StartLine != 1 || meta.EndLine != 2000 || meta.TotalLines != 2000 {
		t.Fatalf("ordinary empty-line meta = %+v, want 1..2000 of 2000", meta)
	}
	if !strings.HasPrefix(got, "1\t\n") || !strings.Contains(got, "2000\t\n") {
		t.Fatalf("ordinary empty-line window missing expected rows: %.200q", got)
	}
}
