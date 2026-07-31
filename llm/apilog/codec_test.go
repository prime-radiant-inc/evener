package apilog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func marshalRecordLine(t *testing.T, record APILogRecord) []byte {
	t.Helper()
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func TestAPIAttemptDecoderReturnsAttemptAndSettlementRecords(t *testing.T) {
	attemptLine := marshalRecordLine(t, validAPIAttemptRecord(t))
	settlementLine := marshalRecordLine(t, validSettlement(t))
	data := append(append(append([]byte{}, attemptLine...), '\n'), settlementLine...)
	data = append(data, '\n')
	maxLineBytes := max(len(attemptLine), len(settlementLine))
	decoder := NewDecoder(bytes.NewReader(data), maxLineBytes)

	first, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := first.(APIAttemptRecord); !ok {
		t.Fatalf("first record type = %T, want APIAttemptRecord", first)
	}
	second, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := second.(APIAttemptGroupSettlement); !ok {
		t.Fatalf("second record type = %T, want APIAttemptGroupSettlement", second)
	}
	if _, err := decoder.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final Next() error = %v, want io.EOF", err)
	}
}

func TestPartialTailIsDistinctFromCleanEOF(t *testing.T) {
	complete := marshalRecordLine(t, validAPIAttemptRecord(t))
	partial := marshalRecordLine(t, validSettlement(t))
	data := append(append(append([]byte{}, complete...), '\n'), partial...)
	maxLineBytes := max(len(complete), len(partial))
	decoder := NewDecoder(bytes.NewReader(data), maxLineBytes)

	if _, err := decoder.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Next(); !errors.Is(err, ErrPartialTail) {
		t.Fatalf("partial Next() error = %v, want ErrPartialTail", err)
	} else {
		wantLine := "line 2"
		wantOffset := fmt.Sprintf("offset %d", len(complete)+1)
		if !strings.Contains(err.Error(), wantLine) || !strings.Contains(err.Error(), wantOffset) {
			t.Fatalf("partial-tail error = %q, want %q and %q", err, wantLine, wantOffset)
		}
	}
	if _, err := decoder.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next() after partial tail error = %v, want io.EOF", err)
	}
}

func TestAPIAttemptDecoderRejectsInteriorCorruptionWithLineAndOffset(t *testing.T) {
	complete := marshalRecordLine(t, validAPIAttemptRecord(t))
	data := append(append(append([]byte{}, complete...), '\n'), []byte("{broken}\n")...)
	decoder := NewDecoder(bytes.NewReader(data), len(complete)+1)

	if _, err := decoder.Next(); err != nil {
		t.Fatal(err)
	}
	_, err := decoder.Next()
	if err == nil {
		t.Fatal("Next() accepted corrupt interior record")
	}
	if errors.Is(err, ErrPartialTail) || errors.Is(err, io.EOF) {
		t.Fatalf("interior corruption error = %v", err)
	}
	wantOffset := fmt.Sprintf("offset %d", len(complete)+1)
	if !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), wantOffset) {
		t.Fatalf("interior corruption error = %q, want line 2 and %q", err, wantOffset)
	}
}

// TestDecoderRecordOffsetAndLineTrackMostRecentAttempt proves the structured
// getters agree with the offsets serf-doctor's whole-history validation reads
// (kata 7x84): a consumer that wants offsets programmatically must not have to
// scrape them out of Next()'s formatted error text.
func TestDecoderRecordOffsetAndLineTrackMostRecentAttempt(t *testing.T) {
	first := marshalRecordLine(t, validAPIAttemptRecord(t))
	second := marshalRecordLine(t, validSettlement(t))
	data := append(append(append([]byte{}, first...), '\n'), second...)
	data = append(data, '\n')
	maxLineBytes := max(len(first), len(second))
	decoder := NewDecoder(bytes.NewReader(data), maxLineBytes)

	if _, err := decoder.Next(); err != nil {
		t.Fatal(err)
	}
	if decoder.RecordOffset() != 0 || decoder.RecordLine() != 1 {
		t.Fatalf("first record offset/line = %d/%d, want 0/1", decoder.RecordOffset(), decoder.RecordLine())
	}

	if _, err := decoder.Next(); err != nil {
		t.Fatal(err)
	}
	wantOffset := int64(len(first) + 1)
	if decoder.RecordOffset() != wantOffset || decoder.RecordLine() != 2 {
		t.Fatalf("second record offset/line = %d/%d, want %d/2", decoder.RecordOffset(), decoder.RecordLine(), wantOffset)
	}
}

func TestDecoderRecordOffsetAndLineMatchInteriorCorruptionError(t *testing.T) {
	complete := marshalRecordLine(t, validAPIAttemptRecord(t))
	data := append(append(append([]byte{}, complete...), '\n'), []byte("{broken}\n")...)
	decoder := NewDecoder(bytes.NewReader(data), len(complete)+1)

	if _, err := decoder.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Next(); err == nil {
		t.Fatal("Next() accepted corrupt interior record")
	}
	wantOffset := int64(len(complete) + 1)
	if decoder.RecordOffset() != wantOffset || decoder.RecordLine() != 2 {
		t.Fatalf("corrupt record offset/line = %d/%d, want %d/2", decoder.RecordOffset(), decoder.RecordLine(), wantOffset)
	}
}

func TestDecoderRecordOffsetAndLineMatchPartialTail(t *testing.T) {
	complete := marshalRecordLine(t, validAPIAttemptRecord(t))
	partial := marshalRecordLine(t, validSettlement(t))
	data := append(append(append([]byte{}, complete...), '\n'), partial...)
	maxLineBytes := max(len(complete), len(partial))
	decoder := NewDecoder(bytes.NewReader(data), maxLineBytes)

	if _, err := decoder.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Next(); !errors.Is(err, ErrPartialTail) {
		t.Fatalf("Next() error = %v, want ErrPartialTail", err)
	}
	wantOffset := int64(len(complete) + 1)
	if decoder.RecordOffset() != wantOffset || decoder.RecordLine() != 2 {
		t.Fatalf("partial tail offset/line = %d/%d, want %d/2", decoder.RecordOffset(), decoder.RecordLine(), wantOffset)
	}
}

func TestAPIAttemptDecoderRejectsUnknownRecordKind(t *testing.T) {
	decoder := NewDecoder(strings.NewReader("{\"kind\":\"future_record\",\"schema_version\":1}\n"), 1024)
	if _, err := decoder.Next(); err == nil {
		t.Fatal("Next() accepted unknown record kind")
	} else if errors.Is(err, ErrPartialTail) || !strings.Contains(err.Error(), "future_record") {
		t.Fatalf("unknown-kind error = %v", err)
	}
}

func TestAPIAttemptDecoderEnforcesMaximumLineBytes(t *testing.T) {
	line := marshalRecordLine(t, validAPIAttemptRecord(t))
	decoder := NewDecoder(bytes.NewReader(append(append([]byte{}, line...), '\n')), len(line)-1)
	if _, err := decoder.Next(); err == nil {
		t.Fatal("Next() accepted an oversized record")
	} else if errors.Is(err, ErrPartialTail) || !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "offset 0") {
		t.Fatalf("oversized-line error = %v", err)
	}

	decoder = NewDecoder(bytes.NewReader(append(append([]byte{}, line...), '\n')), len(line))
	if _, err := decoder.Next(); err != nil {
		t.Fatalf("Next() rejected a record exactly at the limit: %v", err)
	}
}

func TestAPIAttemptDecodeRecordRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	record := validAPIAttemptRecord(t)
	line := marshalRecordLine(t, record)
	var fields map[string]any
	if err := json.Unmarshal(line, &fields); err != nil {
		t.Fatal(err)
	}
	fields["unexpected"] = true
	withUnknownField, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRecord(withUnknownField); err == nil {
		t.Fatal("DecodeRecord() accepted an unknown field")
	}
	if _, err := DecodeRecord(append(line, []byte(" {}")...)); err == nil {
		t.Fatal("DecodeRecord() accepted trailing JSON")
	}
}

func TestAPIAttemptDecodeRecordRejectsInvalidUTF8JSON(t *testing.T) {
	line := marshalRecordLine(t, validAPIAttemptRecord(t))
	provider := []byte("openai-primary")
	index := bytes.Index(line, provider)
	if index < 0 {
		t.Fatalf("test record does not contain provider instance %q", provider)
	}
	line[index] = 0xff
	if _, err := DecodeRecord(line); err == nil {
		t.Fatal("DecodeRecord() accepted invalid UTF-8 JSON")
	}
}

func TestPartialTailIsReturnedForOversizedFinalFragment(t *testing.T) {
	decoder := NewDecoder(strings.NewReader(strings.Repeat("x", 32)), 8)
	if _, err := decoder.Next(); !errors.Is(err, ErrPartialTail) {
		t.Fatalf("Next() error = %v, want ErrPartialTail", err)
	}
}

func TestScanRecoveryReturnsLastCompleteOffsetAndPartialTail(t *testing.T) {
	complete := marshalRecordLine(t, validAPIAttemptRecord(t))
	partial := marshalRecordLine(t, validSettlement(t))
	prefix := append(append([]byte(nil), complete...), '\n')
	data := append(append([]byte(nil), prefix...), partial[:len(partial)/2]...)

	lastCompleteOffset, partialTail, err := ScanRecovery(bytes.NewReader(data), len(complete)+len(partial))
	if err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}
	if lastCompleteOffset != int64(len(prefix)) || !partialTail {
		t.Fatalf("ScanRecovery = (%d, %t), want (%d, true)", lastCompleteOffset, partialTail, len(prefix))
	}

	lastCompleteOffset, partialTail, err = ScanRecovery(bytes.NewReader(prefix), len(complete))
	if err != nil {
		t.Fatalf("ScanRecovery clean file: %v", err)
	}
	if lastCompleteOffset != int64(len(prefix)) || partialTail {
		t.Fatalf("clean ScanRecovery = (%d, %t), want (%d, false)", lastCompleteOffset, partialTail, len(prefix))
	}
}

func TestScanRecoveryRejectsInvalidCompleteLinesAtCanonicalBoundary(t *testing.T) {
	complete := marshalRecordLine(t, validAPIAttemptRecord(t))
	prefix := append(append([]byte(nil), complete...), '\n')
	tests := []struct {
		name         string
		line         []byte
		maxLineBytes int
	}{
		{name: "corrupt", line: []byte("{broken}\n"), maxLineBytes: len(complete)},
		{name: "oversized", line: []byte(strings.Repeat("x", len(complete)+1) + "\n"), maxLineBytes: len(complete)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := append(append([]byte(nil), prefix...), tt.line...)
			lastCompleteOffset, partialTail, err := ScanRecovery(bytes.NewReader(data), tt.maxLineBytes)
			if err == nil {
				t.Fatal("ScanRecovery accepted an invalid complete line")
			}
			if lastCompleteOffset != int64(len(prefix)) || partialTail {
				t.Fatalf("ScanRecovery failure boundary = (%d, %t), want (%d, false)", lastCompleteOffset, partialTail, len(prefix))
			}
		})
	}
}

func TestScanRecoveryRejectsInvalidRecordBeforePartialTail(t *testing.T) {
	invalid := []byte("{broken}\n")
	partial := []byte(`{"kind":"api_attempt"`)
	data := append(append([]byte(nil), invalid...), partial...)

	lastCompleteOffset, partialTail, err := ScanRecovery(bytes.NewReader(data), len(invalid)+len(partial))
	if err == nil {
		t.Fatal("ScanRecovery accepted an invalid complete boundary record before a partial tail")
	}
	if lastCompleteOffset != 0 || partialTail {
		t.Fatalf("ScanRecovery invalid boundary = (%d, %t), want (0, false)", lastCompleteOffset, partialTail)
	}
}

func TestScanRecoveryRejectsOversizedPartialTail(t *testing.T) {
	const maxLineBytes = 64
	data := []byte(strings.Repeat("x", maxLineBytes+1))

	lastCompleteOffset, partialTail, err := ScanRecovery(bytes.NewReader(data), maxLineBytes)
	if err == nil {
		t.Fatal("ScanRecovery accepted an oversized partial tail")
	}
	if lastCompleteOffset != 0 || partialTail {
		t.Fatalf("ScanRecovery oversized partial tail = (%d, %t), want (0, false)", lastCompleteOffset, partialTail)
	}
}

func TestScanRecoveryBoundsWorkToCanonicalTail(t *testing.T) {
	complete := marshalRecordLine(t, validAPIAttemptRecord(t))
	partial := []byte(`{"kind":"api_attempt"`)
	tail := append([]byte{'\n'}, complete...)
	tail = append(tail, '\n')
	tail = append(tail, partial...)

	const historicalBytes int64 = 1 << 40
	reader := newSparseTailReadSeeker(historicalBytes, tail)
	maxLineBytes := max(len(complete), len(partial))

	lastCompleteOffset, partialTail, err := ScanRecovery(reader, maxLineBytes)
	if err != nil {
		t.Fatalf("ScanRecovery sparse tail: %v", err)
	}
	wantOffset := historicalBytes + 1 + int64(len(complete)) + 1
	if lastCompleteOffset != wantOffset || !partialTail {
		t.Fatalf("ScanRecovery sparse tail = (%d, %t), want (%d, true)", lastCompleteOffset, partialTail, wantOffset)
	}
	if reader.lowestReadOffset < historicalBytes {
		t.Fatalf("ScanRecovery read historical prefix at offset %d, want offset >= %d", reader.lowestReadOffset, historicalBytes)
	}
	maxReadBytes := int64(3*maxLineBytes + 3)
	if reader.readBytes > maxReadBytes {
		t.Fatalf("ScanRecovery read %d bytes, want at most %d", reader.readBytes, maxReadBytes)
	}
}

type sparseTailReadSeeker struct {
	size             int64
	offset           int64
	tailOffset       int64
	tail             []byte
	readBytes        int64
	lowestReadOffset int64
}

func newSparseTailReadSeeker(tailOffset int64, tail []byte) *sparseTailReadSeeker {
	size := tailOffset + int64(len(tail))
	return &sparseTailReadSeeker{
		size:             size,
		tailOffset:       tailOffset,
		tail:             append([]byte(nil), tail...),
		lowestReadOffset: size,
	}
}

func (r *sparseTailReadSeeker) Read(p []byte) (int, error) {
	if r.offset >= r.size {
		return 0, io.EOF
	}
	readOffset := r.offset
	n := int(min(int64(len(p)), r.size-r.offset))
	for index := range p[:n] {
		p[index] = '\n'
	}
	overlapStart := max(readOffset, r.tailOffset)
	overlapEnd := min(readOffset+int64(n), r.size)
	if overlapStart < overlapEnd {
		copy(
			p[overlapStart-readOffset:overlapEnd-readOffset],
			r.tail[overlapStart-r.tailOffset:overlapEnd-r.tailOffset],
		)
	}
	r.offset += int64(n)
	r.readBytes += int64(n)
	r.lowestReadOffset = min(r.lowestReadOffset, readOffset)
	return n, nil
}

func (r *sparseTailReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = r.offset + offset
	case io.SeekEnd:
		next = r.size + offset
	default:
		return 0, fmt.Errorf("invalid seek whence %d", whence)
	}
	if next < 0 {
		return 0, fmt.Errorf("negative seek offset %d", next)
	}
	r.offset = next
	return next, nil
}
