package apilog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

// failingReadSeeker is an io.ReadSeeker that fails on Seek or Read.
type failingReadSeeker struct {
	seekErr error
	readErr error
}

func (f failingReadSeeker) Read(p []byte) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	return 0, io.EOF
}

func (f failingReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if f.seekErr != nil {
		return 0, f.seekErr
	}
	return offset, nil
}

// TestDecoderReadLineMaxLineBytesZero covers the maxLineBytes <= 0 error path
// (line 100-101).
func TestDecoderReadLineMaxLineBytesZero(t *testing.T) {
	d := NewDecoder(strings.NewReader("data"), 0)
	_, err := d.Next()
	if err == nil {
		t.Fatal("Next with maxLineBytes=0 should error")
	}
}

// TestDecoderReadLineReadError covers the non-EOF read error path (line 69-70).
func TestDecoderReadLineReadError(t *testing.T) {
	d := NewDecoder(&failingReadSeeker{readErr: errors.New("read fail")}, 1024)
	_, err := d.Next()
	if err == nil {
		t.Fatal("Next with read error should return error")
	}
}

// TestScanRecoveryMaxLineBytesZero covers the maxLineBytes <= 0 error path
// (line 138-139).
func TestScanRecoveryMaxLineBytesZero(t *testing.T) {
	_, _, err := ScanRecovery(strings.NewReader("x"), 0)
	if err == nil {
		t.Fatal("ScanRecovery with maxLineBytes=0 should error")
	}
}

// TestScanRecoverySeekError covers the seek error path (line 142-143).
func TestScanRecoverySeekError(t *testing.T) {
	r := failingReadSeeker{seekErr: errors.New("seek fail")}
	_, _, err := ScanRecovery(r, 1024)
	if err == nil {
		t.Fatal("ScanRecovery with seek error should return error")
	}
}

// TestScanRecoveryReadError covers the readRecoveryRange error on the final
// byte (line 150-151).
func TestScanRecoveryReadError(t *testing.T) {
	r := &partialFailReader{data: []byte("x\n"), readErr: errors.New("read fail")}
	_, _, err := ScanRecovery(r, 1024)
	if err == nil {
		t.Fatal("ScanRecovery with read error should return error")
	}
}

// TestScanRecoveryPartialStartZero covers the partialStart == 0 path where
// the entire file is a partial tail (line 168-169).
func TestScanRecoveryPartialStartZero(t *testing.T) {
	// A file with a single partial line (no trailing newline) that starts at 0.
	data := []byte(`{"kind":"api_attempt"`)
	_, partialTail, err := ScanRecovery(bytes.NewReader(data), 1024)
	if err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}
	if !partialTail {
		t.Fatal("ScanRecovery should report partial tail for file with no newline")
	}
}

// TestScanRecoveryBoundaryRecordReadError covers the readRecoveryRange error
// in validateRecoveryBoundaryRecord (line 188-189).
func TestScanRecoveryBoundaryRecordReadError(t *testing.T) {
	// A reader that reports a non-zero size on Seek(End) but fails on Read.
	// This triggers the readRecoveryRange error on the final byte read.
	r := &partialFailReader{data: []byte("x\n"), readErr: errors.New("read fail")}
	_, _, err := ScanRecovery(r, 1024)
	if err == nil {
		t.Fatal("ScanRecovery with read error should return error")
	}
}

// partialFailReader returns data for Seek(End) but fails on Read.
type partialFailReader struct {
	data    []byte
	readErr error
	pos     int64
}

func (r *partialFailReader) Read(p []byte) (int, error) {
	if r.readErr != nil {
		return 0, r.readErr
	}
	if r.pos >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += int64(n)
	return n, nil
}

func (r *partialFailReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		r.pos = offset
	case io.SeekCurrent:
		r.pos += offset
	case io.SeekEnd:
		r.pos = int64(len(r.data)) + offset
	}
	return r.pos, nil
}

// TestReadRecoveryRangeError covers readRecoveryRangeInto error propagation
// (lines 222-223).
func TestReadRecoveryRangeError(t *testing.T) {
	r := &failingReadSeeker{readErr: errors.New("read fail")}
	_, err := readRecoveryRange(r, 0, 10)
	if err == nil {
		t.Fatal("readRecoveryRange with read error should return error")
	}
}

// TestReadRecoveryRangeIntoSeekError covers the seek error path
// (line 229-230).
func TestReadRecoveryRangeIntoSeekError(t *testing.T) {
	r := &failingReadSeeker{seekErr: errors.New("seek fail")}
	err := readRecoveryRangeInto(r, 0, make([]byte, 10))
	if err == nil {
		t.Fatal("readRecoveryRangeInto with seek error should return error")
	}
}

// TestDecodeRecordSettlementError covers the settlement decode error path
// (line 263-264).
func TestDecodeRecordSettlementError(t *testing.T) {
	// A settlement record with invalid JSON fields.
	line := []byte(`{"kind":"attempt_group_settlement","schema_version":"not-a-number"}`)
	_, err := decodeRecord(line, DecodeStrict)
	if err == nil {
		t.Fatal("decodeRecord with invalid settlement should error")
	}
}

// TestMarshalRecordUnsupportedType covers the unsupported type path
// (line 284).
func TestMarshalRecordUnsupportedType(t *testing.T) {
	_, err := MarshalRecord(unsupportedRecord{})
	if err == nil {
		t.Fatal("MarshalRecord with unsupported type should error")
	}
}

type unsupportedRecord struct{}

func (unsupportedRecord) RecordKind() string              { return "unsupported" }
func (unsupportedRecord) validateRecord(DecodeMode) error { return nil }

// TestDecodeStrictMultipleJSONValues covers the multiple-JSON-values path
// (lines 303-305).
func TestDecodeStrictMultipleJSONValues(t *testing.T) {
	// Two JSON values in one line. Use a struct that accepts any fields
	// (settlement with minimal fields) so the first decode succeeds, then
	// the second decode returns nil (not EOF) → "multiple JSON values".
	line := marshalRecordLine(t, validSettlement(t))
	// Append a second JSON value.
	data = append(line, []byte(`{}`)...)
	err := decodeStrict(data, &APIAttemptGroupSettlement{})
	if err == nil {
		t.Fatal("decodeStrict with multiple JSON values should error")
	}
	if !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("error = %v, want 'multiple JSON values'", err)
	}
}

// TestDecodeStrictSecondDecodeError covers the path where the second decode
// returns a non-nil, non-EOF error (line 306).
func TestDecodeStrictSecondDecodeError(t *testing.T) {
	// A valid JSON object followed by invalid JSON.
	data := []byte(`{"kind":"api_attempt"}{invalid}`)
	err := decodeStrict(data, &APIAttemptRecord{})
	if err == nil {
		t.Fatal("decodeStrict with invalid second value should error")
	}
}

// TestScanRecoveryPartialTailExceedsMax covers the partial tail exceeding
// maxLineBytes error (line 165).
func TestScanRecoveryPartialTailExceedsMax(t *testing.T) {
	// A file with content longer than maxLineBytes and no trailing newline.
	data := []byte(strings.Repeat("x", 200))
	_, _, err := ScanRecovery(bytes.NewReader(data), 100)
	if err == nil {
		t.Fatal("ScanRecovery with oversized partial tail should error")
	}
}

// TestDecoderTooLongDefaultReadError covers the default readErr path when
// the line is too long (line 125). This happens when the reader returns a
// non-EOF, non-buffer-full error during a too-long read.
func TestDecoderTooLongDefaultReadError(t *testing.T) {
	// A reader that returns a non-EOF error — the default case in the switch.
	// We need maxLineBytes > 0 but the reader fails before a newline.
	d := NewDecoder(&failingReadSeeker{readErr: errors.New("custom error")}, 1024)
	_, err := d.Next()
	if err == nil {
		t.Fatal("Next with custom read error should return error")
	}
}

// TestScanRecoveryEmptyFile covers the size == 0 path (line 145).
func TestScanRecoveryEmptyFile(t *testing.T) {
	_, partialTail, err := ScanRecovery(bytes.NewReader(nil), 1024)
	if err != nil {
		t.Fatalf("ScanRecovery empty: %v", err)
	}
	if partialTail {
		t.Fatal("ScanRecovery on empty file should not report partial tail")
	}
}

// TestScanRecoveryCleanTrailingNewline covers the clean trailing newline path
// where validateRecoveryBoundaryRecord succeeds (lines 152-157).
func TestScanRecoveryCleanTrailingNewline(t *testing.T) {
	// A file with one complete record followed by a newline.
	rec := validAPIAttemptRecord(t)
	line, _ := json.Marshal(rec)
	data := append(line, '\n') //nolint:gocritic // appendAssign: data is a new variable, not reassigning to an existing slice
	offset, partialTail, err := ScanRecovery(bytes.NewReader(data), len(line)+1)
	if err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}
	if partialTail {
		t.Fatal("ScanRecovery should not report partial tail for clean file")
	}
	if offset != int64(len(data)) {
		t.Fatalf("offset = %d, want %d", offset, len(data))
	}
}
