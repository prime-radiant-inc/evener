package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func TestReadRetainedPageReconstructsExactBytes(t *testing.T) {
	want := bytes.Repeat([]byte("0123456789abcdef"), 2560) // exactly 40 KiB
	reader := bytes.NewReader(want)
	var got []byte
	var offset int64
	var pageSizes []int64
	for {
		page, err := readRetainedPage(reader, 0, int64(len(want)), offset)
		if err != nil {
			t.Fatalf("readRetainedPage(%d): %v", offset, err)
		}
		decoded := decodeRetainedPage(t, page)
		got = append(got, decoded...)
		pageSizes = append(pageSizes, page.BytesReturned)
		if page.Continuation == nil {
			break
		}
		if page.Continuation.OffsetBytes != offset+int64(len(decoded)) {
			t.Fatalf("continuation = %d, want %d", page.Continuation.OffsetBytes, offset+int64(len(decoded)))
		}
		offset = page.Continuation.OffsetBytes
	}
	if !bytes.Equal(got, want) {
		t.Fatal("page gap or overlap")
	}
	wantSizes := []int64{16 << 10, 16 << 10, 8 << 10}
	if fmt.Sprint(pageSizes) != fmt.Sprint(wantSizes) {
		t.Fatalf("page sizes = %v, want fixed pages %v", pageSizes, wantSizes)
	}
}

func TestReadRetainedPageUsesBase64WhenFixedBoundaryIsInvalidUTF8(t *testing.T) {
	want := append(bytes.Repeat([]byte{'a'}, (16<<10)-1), []byte("é-tail")...)
	page, err := readRetainedPage(bytes.NewReader(want), 0, int64(len(want)), 0)
	if err != nil {
		t.Fatalf("readRetainedPage: %v", err)
	}
	if page.Encoding != "base64" || page.BytesReturned != 16<<10 {
		t.Fatalf("page = %+v, want fixed 16 KiB base64 page", page)
	}
	if got := decodeRetainedPage(t, page); !bytes.Equal(got, want[:16<<10]) {
		t.Fatal("base64 page did not preserve boundary bytes")
	}
	if page.Continuation == nil || page.Continuation.OffsetBytes != 16<<10 {
		t.Fatalf("continuation = %+v, want 16384", page.Continuation)
	}
}

func TestReadRetainedPageValidatesEOFAndLifetimeOffsets(t *testing.T) {
	t.Run("retained lifetime offset", func(t *testing.T) {
		const retainedStart = int64(73)
		data := []byte("retained tail")
		page, err := readRetainedPage(bytes.NewReader(data), retainedStart, retainedStart+int64(len(data)), retainedStart)
		if err != nil {
			t.Fatalf("readRetainedPage: %v", err)
		}
		if page.OffsetBytes != retainedStart || page.TotalBytes != 86 || page.Data != string(data) || page.Continuation != nil {
			t.Fatalf("page = %+v, want lifetime-offset retained tail", page)
		}
	})

	t.Run("exact EOF", func(t *testing.T) {
		page, err := readRetainedPage(bytes.NewReader([]byte("abc")), 0, 3, 3)
		if err != nil {
			t.Fatalf("readRetainedPage at EOF: %v", err)
		}
		if page.OffsetBytes != 3 || page.BytesReturned != 0 || page.Continuation != nil {
			t.Fatalf("EOF page = %+v", page)
		}
	})

	for _, offset := range []int64{-1, 4, math.MaxInt64} {
		_, err := readRetainedPage(bytes.NewReader([]byte("abc")), 0, 3, offset)
		if !errors.Is(err, errRetainedOffsetOutOfRange) {
			t.Fatalf("offset %d error = %v, want errRetainedOffsetOutOfRange", offset, err)
		}
	}
	_, err := readRetainedPage(bytes.NewReader([]byte("tail")), 10, 14, 9)
	if !errors.Is(err, errRetainedOffsetUnavailable) {
		t.Fatalf("pruned offset error = %v, want errRetainedOffsetUnavailable", err)
	}
}

func TestSearchRetainedContextLinesZeroOneAndTen(t *testing.T) {
	var lines []string
	for i := 0; i < 25; i++ {
		line := fmt.Sprintf("line-%02d", i)
		if i == 12 {
			line += " HIT"
		}
		lines = append(lines, line)
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	wantBefore := func(n int) []string { return append([]string(nil), lines[12-n:12]...) }
	wantAfter := func(n int) []string { return append([]string(nil), lines[13:13+n]...) }

	for _, contextLines := range []int{0, 1, 10} {
		t.Run(fmt.Sprintf("context_%d", contextLines), func(t *testing.T) {
			envelope, err := searchRetainedOutput(newMemorySearchSource(data, 0), retainedSearchOptions{
				Regexp:        regexp.MustCompile(`HIT`),
				SearchOptions: jobstore.SearchOptions{ContextLines: contextLines},
			})
			if err != nil {
				t.Fatalf("searchRetainedOutput: %v", err)
			}
			if len(envelope.Matches) != 1 {
				t.Fatalf("matches = %+v", envelope.Matches)
			}
			match := envelope.Matches[0]
			if match.LineStartByte != lineStartOffset(lines, 12) || match.Line != lines[12] {
				t.Fatalf("match = %+v", match)
			}
			if fmt.Sprint(match.Before) != fmt.Sprint(wantBefore(contextLines)) || fmt.Sprint(match.After) != fmt.Sprint(wantAfter(contextLines)) {
				t.Fatalf("context = before %v after %v, want %v / %v", match.Before, match.After, wantBefore(contextLines), wantAfter(contextLines))
			}
			if !envelope.SearchComplete || envelope.Continuation != nil {
				t.Fatalf("completion = %v continuation=%+v", envelope.SearchComplete, envelope.Continuation)
			}
		})
	}
}

func TestSearchRetainedStopsBeforeThe101stMatch(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 101; i++ {
		fmt.Fprintf(&b, "hit-%03d\n", i)
	}
	data := []byte(b.String())
	source := newMemorySearchSource(data, 0)
	envelope, err := searchRetainedOutput(source, retainedSearchOptions{Regexp: regexp.MustCompile(`^hit-`)})
	if err != nil {
		t.Fatalf("searchRetainedOutput: %v", err)
	}
	if len(envelope.Matches) != 100 {
		t.Fatalf("matches = %d, want 100", len(envelope.Matches))
	}
	wantContinuation := int64(100 * len("hit-000\n"))
	if envelope.SearchComplete || envelope.Continuation == nil || envelope.Continuation.OffsetBytes != wantContinuation {
		t.Fatalf("completion=%v continuation=%+v, want %d", envelope.SearchComplete, envelope.Continuation, wantContinuation)
	}

	last, err := searchRetainedOutput(source, retainedSearchOptions{
		Regexp:        regexp.MustCompile(`^hit-`),
		SearchOptions: jobstore.SearchOptions{StartOffset: envelope.Continuation.OffsetBytes},
	})
	if err != nil {
		t.Fatalf("continued search: %v", err)
	}
	if len(last.Matches) != 1 || last.Matches[0].Line != "hit-100" || !last.SearchComplete {
		t.Fatalf("continued envelope = %+v", last)
	}
}

func TestSearchRetainedHonorsSmallerSuppliedMatchCap(t *testing.T) {
	data := []byte("hit-0\nignore\nhit-1\nignore\nhit-2\n")
	envelope, err := searchRetainedOutput(newMemorySearchSource(data, 0), retainedSearchOptions{
		Regexp:        regexp.MustCompile(`^hit-`),
		SearchOptions: jobstore.SearchOptions{MaxMatches: 2},
	})
	if err != nil {
		t.Fatalf("searchRetainedOutput: %v", err)
	}
	want := int64(len("hit-0\nignore\nhit-1\nignore\n"))
	if len(envelope.Matches) != 2 || envelope.Continuation == nil || envelope.Continuation.OffsetBytes != want {
		t.Fatalf("envelope = %+v, want two matches and continuation %d", envelope, want)
	}
}

func TestSearchRetainedHonorsSmallerSuppliedSerializedCap(t *testing.T) {
	line := "HIT-" + strings.Repeat("x", 220)
	data := []byte(line + "\n" + line + "\n")
	envelope, err := searchRetainedOutput(newMemorySearchSource(data, 0), retainedSearchOptions{
		Regexp:        regexp.MustCompile(`^HIT-`),
		SearchOptions: jobstore.SearchOptions{MaxSerializedBytes: 400},
	})
	if err != nil {
		t.Fatalf("searchRetainedOutput: %v", err)
	}
	if len(envelope.Matches) != 1 || envelope.Continuation == nil || envelope.Continuation.OffsetBytes != int64(len(line)+1) {
		t.Fatalf("envelope = %+v, want one match under supplied serialized cap", envelope)
	}
	serialized, err := json.Marshal(envelope.Matches)
	if err != nil {
		t.Fatalf("marshal matches: %v", err)
	}
	if len(serialized) > 400 {
		t.Fatalf("serialized matches = %d bytes, want <= 400", len(serialized))
	}
}

func TestSearchRetainedStopsBefore64KiBOfSerializedMatchContext(t *testing.T) {
	contextLine := strings.Repeat("x", 1800)
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("before-%02d-%s", i, contextLine))
	}
	lines = append(lines, "HIT-one")
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("middle-%02d-%s", i, contextLine))
	}
	secondIndex := len(lines)
	lines = append(lines, "HIT-two")
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("after-%02d-%s", i, contextLine))
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	source := newMemorySearchSource(data, 0)
	envelope, err := searchRetainedOutput(source, retainedSearchOptions{
		Regexp:        regexp.MustCompile(`^HIT-`),
		SearchOptions: jobstore.SearchOptions{ContextLines: 10},
	})
	if err != nil {
		t.Fatalf("searchRetainedOutput: %v", err)
	}
	serialized, err := json.Marshal(envelope.Matches)
	if err != nil {
		t.Fatalf("marshal matches: %v", err)
	}
	wantContinuation := lineStartOffset(lines, secondIndex)
	if len(envelope.Matches) != 1 || len(serialized) > 64<<10 || envelope.Continuation == nil || envelope.Continuation.OffsetBytes != wantContinuation {
		t.Fatalf("matches=%d bytes=%d continuation=%+v, want one <=64KiB and continuation %d", len(envelope.Matches), len(serialized), envelope.Continuation, wantContinuation)
	}

	next, err := searchRetainedOutput(source, retainedSearchOptions{
		Regexp:        regexp.MustCompile(`^HIT-`),
		SearchOptions: jobstore.SearchOptions{StartOffset: wantContinuation, ContextLines: 10},
	})
	if err != nil {
		t.Fatalf("continued search: %v", err)
	}
	if len(next.Matches) != 1 || next.Matches[0].Line != "HIT-two" {
		t.Fatalf("continued matches = %+v", next.Matches)
	}
}

func TestSearchRetainedContinuationIsFirstLineNotEvaluatedAsMatch(t *testing.T) {
	data := []byte("hit-one\nboring-a\nboring-b\nhit-two\n")
	envelope, err := searchRetainedOutput(newMemorySearchSource(data, 0), retainedSearchOptions{
		Regexp:        regexp.MustCompile(`^hit-`),
		SearchOptions: jobstore.SearchOptions{MaxMatches: 1},
	})
	if err != nil {
		t.Fatalf("searchRetainedOutput: %v", err)
	}
	want := int64(len("hit-one\nboring-a\nboring-b\n"))
	if len(envelope.Matches) != 1 || envelope.Continuation == nil || envelope.Continuation.OffsetBytes != want {
		t.Fatalf("envelope = %+v, want continuation at second match %d", envelope, want)
	}
}

func TestSearchRetainedLookaheadContextDoesNotSkipLaterMatches(t *testing.T) {
	data := []byte("hit-one\nhit-two\nhit-three\n")
	source := newMemorySearchSource(data, 0)
	var got []string
	var offset int64
	for i := 0; i < 3; i++ {
		envelope, err := searchRetainedOutput(source, retainedSearchOptions{
			Regexp:        regexp.MustCompile(`^hit-`),
			SearchOptions: jobstore.SearchOptions{StartOffset: offset, MaxMatches: 1, ContextLines: 1},
		})
		if err != nil {
			t.Fatalf("search %d: %v", i, err)
		}
		if len(envelope.Matches) != 1 {
			t.Fatalf("search %d matches = %+v", i, envelope.Matches)
		}
		got = append(got, envelope.Matches[0].Line)
		if i < 2 {
			if envelope.Continuation == nil || envelope.Continuation.OffsetBytes <= offset {
				t.Fatalf("search %d made no continuation progress: %+v", i, envelope.Continuation)
			}
			offset = envelope.Continuation.OffsetBytes
		} else if envelope.Continuation != nil || !envelope.SearchComplete {
			t.Fatalf("final envelope = %+v", envelope)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint([]string{"hit-one", "hit-two", "hit-three"}) {
		t.Fatalf("matches = %v", got)
	}
}

func TestSearchRetainedReportsOversizedLineIntervalAndContinues(t *testing.T) {
	overlong := strings.Repeat("x", retainedSearchMaxLineBytes+1) + "\n"
	data := []byte(overlong + "HIT\n")
	envelope, err := searchRetainedOutput(newMemorySearchSource(data, 0), retainedSearchOptions{Regexp: regexp.MustCompile(`HIT`)})
	if err != nil {
		t.Fatalf("searchRetainedOutput: %v", err)
	}
	if len(envelope.SkippedOversized) != 1 {
		t.Fatalf("skipped oversized = %+v", envelope.SkippedOversized)
	}
	skipped := envelope.SkippedOversized[0]
	if skipped.StartByte != 0 || skipped.EndByte != int64(len(overlong)) {
		t.Fatalf("skipped interval = %+v, want [0,%d)", skipped, len(overlong))
	}
	if len(envelope.Matches) != 1 || envelope.Matches[0].LineStartByte != int64(len(overlong)) || envelope.Matches[0].Line != "HIT" {
		t.Fatalf("matches = %+v", envelope.Matches)
	}
}

func TestSearchRetainedSkipsPrunedInitialPartialFragment(t *testing.T) {
	const retainedStart = int64(100)
	data := []byte("fragment\nHIT\n")
	envelope, err := searchRetainedOutput(newMemorySearchSource(data, retainedStart), retainedSearchOptions{
		Regexp:        regexp.MustCompile(`fragment|HIT`),
		SearchOptions: jobstore.SearchOptions{StartOffset: retainedStart, SkipPartialPrefix: true},
	})
	if err != nil {
		t.Fatalf("searchRetainedOutput: %v", err)
	}
	if !envelope.SkippedPartialPrefix || len(envelope.Matches) != 1 || envelope.Matches[0].Line != "HIT" || envelope.Matches[0].LineStartByte != retainedStart+int64(len("fragment\n")) {
		t.Fatalf("envelope = %+v", envelope)
	}
}

func TestSearchRetainedRunningDefersEOFFragment(t *testing.T) {
	data := []byte("HIT-complete\nHIT-growing")
	fragmentAt := int64(len("HIT-complete\n"))
	envelope, err := searchRetainedOutput(newMemorySearchSource(data, 0), retainedSearchOptions{
		Regexp:        regexp.MustCompile(`HIT`),
		SearchOptions: jobstore.SearchOptions{DeferEOFFragment: true},
	})
	if err != nil {
		t.Fatalf("searchRetainedOutput: %v", err)
	}
	if len(envelope.Matches) != 1 || envelope.Matches[0].Line != "HIT-complete" || !envelope.SearchComplete || envelope.Continuation == nil || envelope.Continuation.OffsetBytes != fragmentAt {
		t.Fatalf("running envelope = %+v", envelope)
	}
}

func TestSearchRetainedTerminalEvaluatesUnterminatedEOF(t *testing.T) {
	data := []byte("ignore\nHIT-terminal")
	envelope, err := searchRetainedOutput(newMemorySearchSource(data, 0), retainedSearchOptions{
		Regexp: regexp.MustCompile(`HIT`),
	})
	if err != nil {
		t.Fatalf("searchRetainedOutput: %v", err)
	}
	if len(envelope.Matches) != 1 || envelope.Matches[0].Line != "HIT-terminal" || envelope.Matches[0].LineStartByte != int64(len("ignore\n")) || !envelope.SearchComplete || envelope.Continuation != nil {
		t.Fatalf("terminal envelope = %+v", envelope)
	}
}

func TestSearchRetainedValidatesOptionsAndBoundsWindowReads(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts jobstore.SearchOptions
	}{
		{name: "negative start", opts: jobstore.SearchOptions{StartOffset: -1}},
		{name: "negative matches", opts: jobstore.SearchOptions{MaxMatches: -1}},
		{name: "negative bytes", opts: jobstore.SearchOptions{MaxSerializedBytes: -1}},
		{name: "negative context", opts: jobstore.SearchOptions{ContextLines: -1}},
		{name: "too much context", opts: jobstore.SearchOptions{ContextLines: 11}},
		{name: "offset beyond EOF", opts: jobstore.SearchOptions{StartOffset: 99}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := searchRetainedOutput(newMemorySearchSource([]byte("line\n"), 0), retainedSearchOptions{Regexp: regexp.MustCompile(`line`), SearchOptions: tc.opts})
			if err == nil {
				t.Fatal("search succeeded, want validation error")
			}
		})
	}
	_, err := searchRetainedOutput(newMemorySearchSource([]byte("line\n"), 0), retainedSearchOptions{})
	if err == nil {
		t.Fatal("nil regexp succeeded")
	}

	data := []byte(strings.Repeat("ordinary line\n", 5000))
	source := newMemorySearchSource(data, 0)
	_, err = searchRetainedOutput(source, retainedSearchOptions{Regexp: regexp.MustCompile(`never`)})
	if err != nil {
		t.Fatalf("bounded search: %v", err)
	}
	if source.largestWindow > retainedOutputPageBytes {
		t.Fatalf("largest source window = %d, want <= %d", source.largestWindow, retainedOutputPageBytes)
	}
}

func decodeRetainedPage(t *testing.T, page retainedPage) []byte {
	t.Helper()
	switch page.Encoding {
	case "utf8":
		return []byte(page.Data)
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(page.Data)
		if err != nil {
			t.Fatalf("decode page: %v", err)
		}
		return decoded
	default:
		t.Fatalf("unknown page encoding %q", page.Encoding)
		return nil
	}
}

func lineStartOffset(lines []string, index int) int64 {
	var offset int64
	for _, line := range lines[:index] {
		offset += int64(len(line) + 1)
	}
	return offset
}

type memorySearchSource struct {
	data          []byte
	retainedStart int64
	largestWindow int
}

func newMemorySearchSource(data []byte, retainedStart int64) *memorySearchSource {
	return &memorySearchSource{data: append([]byte(nil), data...), retainedStart: retainedStart}
}

func (s *memorySearchSource) ReadWindow(offset int64, maxBytes int) (jobstore.OutputWindowSnapshot, error) {
	if maxBytes < 0 {
		return jobstore.OutputWindowSnapshot{}, jobstore.ErrInvalidLimit
	}
	if maxBytes > s.largestWindow {
		s.largestWindow = maxBytes
	}
	total := s.retainedStart + int64(len(s.data))
	if offset < s.retainedStart {
		return jobstore.OutputWindowSnapshot{TotalBytes: total, RetainedStart: s.retainedStart}, jobstore.ErrOutputPruned
	}
	if offset > total {
		return jobstore.OutputWindowSnapshot{TotalBytes: total, RetainedStart: s.retainedStart}, jobstore.ErrInvalidOffset
	}
	end := offset + int64(maxBytes)
	if end < offset || end > total {
		end = total
	}
	startIndex := offset - s.retainedStart
	endIndex := end - s.retainedStart
	return jobstore.OutputWindowSnapshot{
		Content:       append([]byte(nil), s.data[startIndex:endIndex]...),
		Start:         offset,
		End:           end,
		TotalBytes:    total,
		RetainedStart: s.retainedStart,
		Truncated:     s.retainedStart > 0 || offset > s.retainedStart || end < total,
	}, nil
}
