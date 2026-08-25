package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"regexp"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestReadRetainedPage_NilReader covers the nil-reader guard (line 46-47).
func TestReadRetainedPage_NilReader(t *testing.T) {
	_, err := readRetainedPage(nil, 0, 10, 0)
	if err == nil {
		t.Fatal("expected error for nil reader")
	}
}

// TestReadRetainedPage_InvalidBounds covers the invalid-bounds guard (line
// 49-50): retainedStart < 0 and total < retainedStart.
func TestReadRetainedPage_InvalidBounds(t *testing.T) {
	// retainedStart < 0
	_, err := readRetainedPage(bytes.NewReader([]byte("x")), -1, 10, 0)
	if err == nil {
		t.Fatal("expected error for negative retainedStart")
	}
	// total < retainedStart
	_, err = readRetainedPage(bytes.NewReader([]byte("x")), 10, 5, 0)
	if err == nil {
		t.Fatal("expected error for total < retainedStart")
	}
}

// TestReadRetainedPage_ReadError covers the ReadAt error path (line 66-68)
// using a failingReader that always returns an error.
func TestReadRetainedPage_ReadError(t *testing.T) {
	_, err := readRetainedPage(failingReaderAt{}, 0, 10, 0)
	if err == nil {
		t.Fatal("expected error for read failure")
	}
}

// TestReadRetainedPage_ShortRead covers the short-read path (line 70-71)
// using a shortReaderAt that returns fewer bytes than requested.
func TestReadRetainedPage_ShortRead(t *testing.T) {
	_, err := readRetainedPage(shortReaderAt{}, 0, 100, 0)
	if err == nil {
		t.Fatal("expected error for short read")
	}
}

// TestReadRetainedPage_NegativeOffset covers the negative-offset guard
// (line 52-53).
func TestReadRetainedPage_NegativeOffset(t *testing.T) {
	_, err := readRetainedPage(bytes.NewReader([]byte("x")), 0, 10, -5)
	if !errors.Is(err, errRetainedOffsetOutOfRange) {
		t.Fatalf("expected errRetainedOffsetOutOfRange, got %v", err)
	}
}

// TestReadRetainedPage_OffsetBeforeRetainedStart covers the
// offset < retainedStart guard (line 55-56).
func TestReadRetainedPage_OffsetBeforeRetainedStart(t *testing.T) {
	_, err := readRetainedPage(bytes.NewReader([]byte("tail")), 10, 14, 5)
	if !errors.Is(err, errRetainedOffsetUnavailable) {
		t.Fatalf("expected errRetainedOffsetUnavailable, got %v", err)
	}
}

// TestReadRetainedPage_OffsetBeyondTotal covers the offset > total guard
// (line 58-59).
func TestReadRetainedPage_OffsetBeyondTotal(t *testing.T) {
	_, err := readRetainedPage(bytes.NewReader([]byte("abc")), 0, 3, 100)
	if !errors.Is(err, errRetainedOffsetOutOfRange) {
		t.Fatalf("expected errRetainedOffsetOutOfRange, got %v", err)
	}
}

// TestReadRetainedPage_EmptyContent covers the n=0 case (line 64-65) where
// offset equals total.
func TestReadRetainedPage_EmptyContent(t *testing.T) {
	page, err := readRetainedPage(bytes.NewReader([]byte("abc")), 0, 3, 3)
	if err != nil {
		t.Fatalf("readRetainedPage at EOF: %v", err)
	}
	if page.BytesReturned != 0 || page.Data != "" || page.Continuation != nil {
		t.Fatalf("EOF page = %+v", page)
	}
}

// TestSearchRetainedOutput_NilSource covers the nil-source guard (line
// 123-124).
func TestSearchRetainedOutput_NilSource(t *testing.T) {
	_, err := searchRetainedOutput(nil, retainedSearchOptions{Regexp: regexp.MustCompile("x")})
	if err == nil {
		t.Fatal("expected error for nil source")
	}
}

// TestSearchRetainedOutput_NilRegexp covers the nil-regexp guard (line
// 126-127).
func TestSearchRetainedOutput_NilRegexp(t *testing.T) {
	src := &mockSearchSource{}
	_, err := searchRetainedOutput(src, retainedSearchOptions{})
	if err == nil {
		t.Fatal("expected error for nil regexp")
	}
}

// TestSearchRetainedOutput_NegativeStartOffset covers the negative
// StartOffset guard (line 129-130).
func TestSearchRetainedOutput_NegativeStartOffset(t *testing.T) {
	src := &mockSearchSource{}
	opts := retainedSearchOptions{Regexp: regexp.MustCompile("x"), StartOffset: -1}
	_, err := searchRetainedOutput(src, opts)
	if !errors.Is(err, jobstore.ErrInvalidOffset) {
		t.Fatalf("expected ErrInvalidOffset, got %v", err)
	}
}

// TestSearchRetainedOutput_NegativeMaxMatches covers the negative
// MaxMatches guard (line 132-133).
func TestSearchRetainedOutput_NegativeMaxMatches(t *testing.T) {
	src := &mockSearchSource{}
	opts := retainedSearchOptions{Regexp: regexp.MustCompile("x"), MaxMatches: -1}
	_, err := searchRetainedOutput(src, opts)
	if !errors.Is(err, jobstore.ErrInvalidLimit) {
		t.Fatalf("expected ErrInvalidLimit, got %v", err)
	}
}

// TestSearchRetainedOutput_NegativeMaxSerializedBytes covers the negative
// MaxSerializedBytes guard (line 135-136).
func TestSearchRetainedOutput_NegativeMaxSerializedBytes(t *testing.T) {
	src := &mockSearchSource{}
	opts := retainedSearchOptions{Regexp: regexp.MustCompile("x"), MaxSerializedBytes: -1}
	_, err := searchRetainedOutput(src, opts)
	if !errors.Is(err, jobstore.ErrInvalidLimit) {
		t.Fatalf("expected ErrInvalidLimit, got %v", err)
	}
}

// TestSearchRetainedOutput_InvalidContextLines covers the out-of-range
// ContextLines guard (line 138-139).
func TestSearchRetainedOutput_InvalidContextLines(t *testing.T) {
	src := &mockSearchSource{}
	opts := retainedSearchOptions{Regexp: regexp.MustCompile("x"), ContextLines: -1}
	if _, err := searchRetainedOutput(src, opts); err == nil {
		t.Fatal("expected error for negative context lines")
	}
	opts.ContextLines = 11
	if _, err := searchRetainedOutput(src, opts); err == nil {
		t.Fatal("expected error for context lines > 10")
	}
}

// TestAppendRetainedHistory covers the history-append function with various
// context-line settings.
func TestAppendRetainedHistory(t *testing.T) {
	// contextLines == 0: history is truncated to empty.
	h := []string{"a", "b"}
	h = appendRetainedHistory(h, "c", 0)
	if len(h) != 0 {
		t.Fatalf("expected empty history for contextLines=0, got %v", h)
	}

	// contextLines == 2: history accumulates up to 2 entries.
	h = appendRetainedHistory(nil, "a", 2)
	h = appendRetainedHistory(h, "b", 2)
	h = appendRetainedHistory(h, "c", 2)
	if len(h) != 2 || h[0] != "b" || h[1] != "c" {
		t.Fatalf("expected [b c], got %v", h)
	}
}

// TestRetainedAfterContext covers the after-context builder.
func TestRetainedAfterContext(t *testing.T) {
	// contextLines == 0: returns nil.
	if got := retainedAfterContext(nil, 0, false); got != nil {
		t.Fatalf("expected nil for contextLines=0, got %v", got)
	}

	// Normal case with 2 context lines.
	lines := []retainedSearchLine{
		{content: []byte("a"), complete: true},
		{content: []byte("b"), complete: true},
		{content: []byte("c"), complete: true},
	}
	got := retainedAfterContext(lines, 2, false)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("expected [a b], got %v", got)
	}

	// Oversized line should be skipped.
	lines = []retainedSearchLine{
		{content: nil, oversized: true},
		{content: []byte("ok"), complete: true},
	}
	got = retainedAfterContext(lines, 2, false)
	if len(got) != 1 || got[0] != "ok" {
		t.Fatalf("expected [ok], got %v", got)
	}

	// Incomplete line with deferEOF should be skipped.
	lines = []retainedSearchLine{
		{content: []byte("partial"), complete: false},
		{content: []byte("ok"), complete: true},
	}
	got = retainedAfterContext(lines, 2, true)
	if len(got) != 1 || got[0] != "ok" {
		t.Fatalf("expected [ok], got %v", got)
	}
}

// TestValidateRetainedWindow covers the window validation function.
func TestValidateRetainedWindow(t *testing.T) {
	// Valid window.
	w := jobstore.OutputWindowSnapshot{
		RetainedStart: 0, TotalBytes: 100, Start: 0, End: 10, Content: make([]byte, 10),
	}
	if err := validateRetainedWindow(w, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invalid: RetainedStart < 0.
	w.RetainedStart = -1
	if err := validateRetainedWindow(w, 0); err == nil {
		t.Fatal("expected error for negative RetainedStart")
	}
	w.RetainedStart = 0

	// Invalid: TotalBytes < RetainedStart.
	w.TotalBytes = -1
	if err := validateRetainedWindow(w, 0); err == nil {
		t.Fatal("expected error for TotalBytes < RetainedStart")
	}
	w.TotalBytes = 100

	// Invalid: Start != requested.
	if err := validateRetainedWindow(w, 5); err == nil {
		t.Fatal("expected error for Start != requested")
	}

	// Invalid: End < Start.
	w.End = -1
	if err := validateRetainedWindow(w, 0); err == nil {
		t.Fatal("expected error for End < Start")
	}
	w.End = 10

	// Invalid: End > TotalBytes.
	w.End = 200
	if err := validateRetainedWindow(w, 0); err == nil {
		t.Fatal("expected error for End > TotalBytes")
	}
	w.End = 10

	// Invalid: content length mismatch.
	w.Content = make([]byte, 5)
	if err := validateRetainedWindow(w, 0); err == nil {
		t.Fatal("expected error for content length mismatch")
	}
	w.Content = make([]byte, 10)

	// Invalid: requested < RetainedStart (pruned).
	w.RetainedStart = 10
	if err := validateRetainedWindow(w, 0); err == nil {
		t.Fatal("expected error for pruned offset")
	}
}

// TestRetainedMatchesSerializedSize covers the serialized size calculator.
func TestRetainedMatchesSerializedSize(t *testing.T) {
	// Empty matches with a candidate byte count.
	size := retainedMatchesSerializedSize(nil, 100)
	if size <= 0 {
		t.Fatalf("size = %d, want > 0", size)
	}

	// With existing matches.
	matches := []retainedSearchMatch{
		{LineStartByte: 0, Line: "hello"},
	}
	size = retainedMatchesSerializedSize(matches, 50)
	if size <= 0 {
		t.Fatalf("size = %d, want > 0", size)
	}
}

// TestRetainedLineScanner_Next covers the line scanner for edge cases.
func TestRetainedLineScanner_Next(t *testing.T) {
	// Empty input — returns false, no error.
	s := &retainedLineScanner{
		r:      bufio.NewReader(bytes.NewBuffer(nil)),
		offset: 0,
	}
	line, ok, err := s.next()
	if ok || err != nil {
		t.Fatalf("empty input: ok=%v err=%v", ok, err)
	}

	// Single line without newline at EOF.
	s = &retainedLineScanner{
		r:      bufio.NewReader(bytes.NewBufferString("hello")),
		offset: 0,
	}
	line, ok, err = s.next()
	if !ok || err != nil {
		t.Fatalf("no newline: ok=%v err=%v", ok, err)
	}
	if line.complete {
		t.Fatal("expected incomplete line without newline")
	}
	if string(line.content) != "hello" {
		t.Fatalf("content = %q", line.content)
	}
}

// TestNextRetainedSearchLine_PendingAndEOF covers the pending-buffer and EOF
// paths.
func TestNextRetainedSearchLine_PendingAndEOF(t *testing.T) {
	// Pending lines are returned first.
	pending := []retainedSearchLine{{content: []byte("pending"), start: 0, end: 7, complete: true}}
	eof := false
	scanner := &retainedLineScanner{r: bufio.NewReader(bytes.NewBuffer(nil)), offset: 0}
	line, ok, err := nextRetainedSearchLine(scanner, &pending, &eof)
	if !ok || err != nil || string(line.content) != "pending" {
		t.Fatalf("pending: ok=%v err=%v line=%+v", ok, err, line)
	}
	if len(pending) != 0 {
		t.Fatal("pending not drained")
	}

	// EOF returns false, no error.
	eof = true
	_, ok, err = nextRetainedSearchLine(scanner, &pending, &eof)
	if ok || err != nil {
		t.Fatalf("eof: ok=%v err=%v", ok, err)
	}
}

// TestFinishRetainedSearchLine covers the line-finishing function.
func TestFinishRetainedSearchLine(t *testing.T) {
	// Oversized line returns as-is.
	line := retainedSearchLine{oversized: true}
	if got := finishRetainedSearchLine(line, []byte("x")); !got.oversized {
		t.Fatal("oversized line should remain oversized")
	}

	// Complete line with trailing newline strips it.
	line = retainedSearchLine{complete: true}
	got := finishRetainedSearchLine(line, []byte("hello\n"))
	if string(got.content) != "hello" {
		t.Fatalf("content = %q, want 'hello'", got.content)
	}

	// Complete line with \r\n strips both.
	line = retainedSearchLine{complete: true}
	got = finishRetainedSearchLine(line, []byte("hello\r\n"))
	if string(got.content) != "hello" {
		t.Fatalf("content = %q, want 'hello'", got.content)
	}

	// Content exceeding max line bytes becomes oversized.
	line = retainedSearchLine{complete: true}
	longContent := bytes.Repeat([]byte("x"), retainedSearchMaxLineBytes+1)
	got = finishRetainedSearchLine(line, longContent)
	if !got.oversized {
		t.Fatal("expected oversized for content > maxLineBytes")
	}
}

// TestApiLogContextReader covers the context-aware reader.
func TestApiLogContextReader(t *testing.T) {
	// Normal read.
	r := apiLogContextReader{ctx: context.Background(), reader: bytes.NewBufferString("hello")}
	buf := make([]byte, 5)
	n, err := r.Read(buf)
	if err != nil || n != 5 || string(buf) != "hello" {
		t.Fatalf("Read: n=%d err=%v buf=%q", n, err, buf)
	}

	// Context canceled.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = apiLogContextReader{ctx: ctx, reader: bytes.NewBufferString("hello")}
	_, err = r.Read(buf)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

// --- Test helpers ---

type failingReaderAt struct{}

func (failingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	return 0, errors.New("read failed")
}

type shortReaderAt struct{}

func (shortReaderAt) ReadAt(p []byte, off int64) (int, error) {
	return 1, nil // always returns 1 byte, not enough
}

type mockSearchSource struct{}

func (m *mockSearchSource) ReadWindow(offset int64, maxBytes int) (jobstore.OutputWindowSnapshot, error) {
	return jobstore.OutputWindowSnapshot{}, errors.New("not implemented")
}
