package linecap

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadLine_TerminatedLine(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("hello\nworld\n"))
	line, terminated, consumed, err := ReadLine(reader, 100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(line) != "hello" || !terminated || consumed != 6 {
		t.Fatalf("got line=%q terminated=%v consumed=%d, want %q true 6", line, terminated, consumed, "hello")
	}
	line2, terminated2, consumed2, err := ReadLine(reader, 100)
	if err != nil {
		t.Fatalf("second err = %v", err)
	}
	if string(line2) != "world" || !terminated2 || consumed2 != 6 {
		t.Fatalf("got line2=%q terminated2=%v consumed2=%d, want %q true 6", line2, terminated2, consumed2, "world")
	}
}

func TestReadLine_UnterminatedTrailingLine(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("complete\npartial"))
	if _, _, _, err := ReadLine(reader, 100); err != nil {
		t.Fatalf("first line err = %v", err)
	}
	line, terminated, consumed, err := ReadLine(reader, 100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(line) != "partial" || terminated || consumed != 7 {
		t.Fatalf("got line=%q terminated=%v consumed=%d, want %q false 7", line, terminated, consumed, "partial")
	}
}

func TestReadLine_CleanEOFBetweenLines(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("one\n"))
	if _, _, _, err := ReadLine(reader, 100); err != nil {
		t.Fatalf("first line err = %v", err)
	}
	_, _, _, err := ReadLine(reader, 100)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestReadLine_EmptyInputIsCleanEOF(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	_, _, _, err := ReadLine(reader, 100)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestReadLine_EmptyLineBetweenTerminators(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\n\nx\n"))
	line, terminated, consumed, err := ReadLine(reader, 100)
	if err != nil || len(line) != 0 || !terminated || consumed != 1 {
		t.Fatalf("got line=%q terminated=%v consumed=%d err=%v, want empty true 1 nil", line, terminated, consumed, err)
	}
}

// TestReadLine_OverLimitTerminatedLineIsRefusedAndDrained proves the core
// property: a terminated line longer than maxLineBytes is refused (ErrTooLong),
// its content is discarded (line is nil, not a truncated prefix), and the
// NEXT ReadLine call starts cleanly at the following line -- proving the
// reader was fully drained past the oversized line, not left mid-line.
func TestReadLine_OverLimitTerminatedLineIsRefusedAndDrained(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("toolongvalue\nshort\n"))
	line, _, _, err := ReadLine(reader, 5)
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}
	if line != nil {
		t.Fatalf("line = %q, want nil (discarded, not truncated)", line)
	}
	next, terminated, _, err := ReadLine(reader, 100)
	if err != nil {
		t.Fatalf("next line err = %v", err)
	}
	if string(next) != "short" || !terminated {
		t.Fatalf("next line = %q terminated=%v, want %q true -- reader must be positioned right after the oversized line", next, terminated, "short")
	}
}

// TestReadLine_OverLimitUnterminatedTrailingLineIsRefused covers the EOF-
// while-over-limit path specifically.
func TestReadLine_OverLimitUnterminatedTrailingLineIsRefused(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("toolongtrailingfragment"))
	_, _, _, err := ReadLine(reader, 5)
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}
}

// TestReadLine_ExactlyAtLimitIsAccepted is the boundary case: a line whose
// length equals maxLineBytes exactly must NOT be refused.
func TestReadLine_ExactlyAtLimitIsAccepted(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("12345\n"))
	line, terminated, _, err := ReadLine(reader, 5)
	if err != nil {
		t.Fatalf("err = %v, want nil for a line exactly at the cap", err)
	}
	if string(line) != "12345" || !terminated {
		t.Fatalf("line = %q terminated=%v, want %q true", line, terminated, "12345")
	}
}

// TestReadLine_BoundedMemoryAcrossManyReadSliceFragments proves the
// bufio.ErrBufferFull accumulation loop works correctly across a line that
// spans many internal buffer refills, using a bufio.Reader with a
// deliberately tiny internal buffer to force many ErrBufferFull iterations
// without needing a multi-megabyte test fixture.
func TestReadLine_BoundedMemoryAcrossManyReadSliceFragments(t *testing.T) {
	content := strings.Repeat("x", 500) + "\n" + "next\n"
	reader := bufio.NewReaderSize(strings.NewReader(content), 16) // tiny buffer forces many ReadSlice fragments
	line, terminated, consumed, err := ReadLine(reader, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(line) != 500 || !terminated || consumed != 501 {
		t.Fatalf("got len(line)=%d terminated=%v consumed=%d, want 500 true 501", len(line), terminated, consumed)
	}
	next, _, _, err := ReadLine(reader, 1000)
	if err != nil || string(next) != "next" {
		t.Fatalf("next = %q err = %v, want %q nil", next, err, "next")
	}
}

// TestReadLine_TinyBufferOverLimitStillRefusedAndDrained combines the tiny
// internal buffer (many ErrBufferFull fragments) with a cap that fires
// partway through accumulation, proving the discard-but-keep-draining
// behavior holds across multiple fragments, not just a single ReadSlice
// call.
func TestReadLine_TinyBufferOverLimitStillRefusedAndDrained(t *testing.T) {
	content := strings.Repeat("y", 500) + "\n" + "ok\n"
	reader := bufio.NewReaderSize(strings.NewReader(content), 16)
	_, _, _, err := ReadLine(reader, 50)
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}
	next, terminated, _, err := ReadLine(reader, 50)
	if err != nil || string(next) != "ok" || !terminated {
		t.Fatalf("next = %q terminated=%v err=%v, want %q true nil", next, terminated, err, "ok")
	}
}
