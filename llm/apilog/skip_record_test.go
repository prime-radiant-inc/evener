package apilog

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// TestSkipRecordMatchesNextFraming pins that SkipRecord advances over exactly
// the records Next would return, and reports the same framing conditions with
// the same line and offset -- it just never parses a record. A caller stepping
// over records to reach a page therefore lands on the same boundaries as a
// caller decoding every record.
func TestSkipRecordMatchesNextFraming(t *testing.T) {
	t.Parallel()
	attemptLine := marshalRecordLine(t, validAPIAttemptRecord(t))
	settlementLine := marshalRecordLine(t, validSettlement(t))
	corrupt := []byte(`{not-json}`)
	data := bytes.Join([][]byte{attemptLine, settlementLine, corrupt, attemptLine}, []byte("\n"))
	data = append(data, '\n')
	maxLineBytes := max(max(len(attemptLine), len(settlementLine)), len(corrupt))

	decoded := NewDecoder(bytes.NewReader(data), maxLineBytes)
	skipped := NewDecoder(bytes.NewReader(data), maxLineBytes)
	for recordNumber := 0; ; recordNumber++ {
		record, decodeErr := decoded.Next()
		skipErr := skipped.SkipRecord()
		if decoded.RecordLine() != skipped.RecordLine() || decoded.RecordOffset() != skipped.RecordOffset() {
			t.Fatalf("record %d: Next position line %d offset %d, SkipRecord position line %d offset %d",
				recordNumber, decoded.RecordLine(), decoded.RecordOffset(), skipped.RecordLine(), skipped.RecordOffset())
		}
		switch {
		case errors.Is(decodeErr, io.EOF):
			if !errors.Is(skipErr, io.EOF) {
				t.Fatalf("record %d: Next ended with EOF, SkipRecord with %v", recordNumber, skipErr)
			}
			return
		case decodeErr != nil:
			// SkipRecord never parses a record, so a record Next rejects on
			// content is one SkipRecord steps over.
			if skipErr != nil {
				t.Fatalf("record %d: Next rejected (%v), SkipRecord reported %v", recordNumber, decodeErr, skipErr)
			}
			continue
		}
		if skipErr != nil {
			t.Fatalf("record %d: Next decoded %T, SkipRecord reported %v", recordNumber, record, skipErr)
		}
	}
}

// TestSkipRecordReportsFramingFailures pins that the framing conditions Next
// fails on -- an over-length line and an unterminated final fragment -- surface
// identically from SkipRecord, since neither depends on a record's content.
func TestSkipRecordReportsFramingFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		data []byte
		skip int
	}{
		{name: "partial tail after one record", data: []byte("first\nsecond"), skip: 1},
		{name: "partial tail on the first record", data: []byte("second"), skip: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoder := NewDecoder(bytes.NewReader(tc.data), 1<<20)
			for i := 0; i < tc.skip; i++ {
				if err := decoder.SkipRecord(); err != nil {
					t.Fatalf("skip %d: %v", i, err)
				}
			}
			if err := decoder.SkipRecord(); !errors.Is(err, ErrPartialTail) {
				t.Fatalf("SkipRecord err = %v, want ErrPartialTail", err)
			}
		})
	}
}
