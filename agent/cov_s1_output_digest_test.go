package agent

import (
	"fmt"
	"strings"
	"testing"
)

// s1cov_windowReader returns a readWindow closure that hands back headSnap on the
// head read and tailSnap on the tail read; a nil snapshot with err makes that
// read fail.
func s1cov_windowReader(headSnap, tailSnap jobReadOutputSnapshot, headErr, tailErr error) func(int, bool) (jobReadOutputSnapshot, error) {
	return func(_ int, fromHead bool) (jobReadOutputSnapshot, error) {
		if fromHead {
			return headSnap, headErr
		}
		return tailSnap, tailErr
	}
}

func TestS1Cov_readJobOutputDigest(t *testing.T) {
	// Head read error propagates.
	if _, err := readJobOutputDigest(s1cov_windowReader(jobReadOutputSnapshot{}, jobReadOutputSnapshot{}, fmt.Errorf("boom"), nil), 1, 1); err == nil {
		t.Fatal("head read error must propagate")
	}

	// Whole output fits and is a single line → returned untouched.
	whole := jobReadOutputSnapshot{Content: "only line no newline"}
	got, err := readJobOutputDigest(s1cov_windowReader(whole, jobReadOutputSnapshot{}, nil, nil), 5, 5)
	if err != nil {
		t.Fatalf("single-line: %v", err)
	}
	if got.Content != whole.Content || got.Truncated {
		t.Fatalf("single-line must return whole snapshot untouched; got %+v", got)
	}

	// Whole output fits, multi-line, but head+tail windows overlap the whole
	// content → returned untouched.
	overlap := jobReadOutputSnapshot{Content: "a\nb\nc\n"}
	got, err = readJobOutputDigest(s1cov_windowReader(overlap, jobReadOutputSnapshot{}, nil, nil), 2, 2)
	if err != nil {
		t.Fatalf("overlap: %v", err)
	}
	if got.Content != overlap.Content || got.Truncated {
		t.Fatalf("overlapping windows must return whole content; got %+v", got)
	}

	// Whole output fits, multi-line, no overlap → stitched head+elision+tail.
	var big strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&big, "line%02d\n", i)
	}
	fits := jobReadOutputSnapshot{Content: big.String(), TotalBytes: int64(big.Len())}
	got, err = readJobOutputDigest(s1cov_windowReader(fits, jobReadOutputSnapshot{}, nil, nil), 1, 1)
	if err != nil {
		t.Fatalf("no-overlap: %v", err)
	}
	if !got.Truncated || !strings.Contains(got.Content, "elided") {
		t.Fatalf("no-overlap must assemble an elided digest; got %+v", got)
	}
	if !strings.HasPrefix(got.Content, "line00\n") || !strings.Contains(got.Content, "line09") {
		t.Fatalf("digest must keep head and tail lines; got %q", got.Content)
	}

	// Head is truncated → a separate tail read is stitched in.
	head := jobReadOutputSnapshot{Content: "line00\nline01\n", Truncated: true}
	tail := jobReadOutputSnapshot{Content: "line08\nline09\n", TotalBytes: 999, DroppedBytes: 7}
	got, err = readJobOutputDigest(s1cov_windowReader(head, tail, nil, nil), 1, 1)
	if err != nil {
		t.Fatalf("two-read: %v", err)
	}
	if !got.Truncated || got.TotalBytes != 999 {
		t.Fatalf("two-read digest must come from the tail snapshot; got %+v", got)
	}
	if !strings.Contains(got.Content, "line00") || !strings.Contains(got.Content, "line09") || !strings.Contains(got.Content, "permanently dropped") {
		t.Fatalf("two-read digest must stitch head+tail and note dropped bytes; got %q", got.Content)
	}

	// Tail read error propagates.
	if _, err := readJobOutputDigest(s1cov_windowReader(head, jobReadOutputSnapshot{}, nil, fmt.Errorf("tail boom")), 1, 1); err == nil {
		t.Fatal("tail read error must propagate")
	}
}
