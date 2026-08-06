package apilog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

var ErrPartialTail = errors.New("partial API-log tail")

// DecodeMode selects how much of a record's body content a decode
// validates. DecodeStrict (the default) decodes and revalidates every body's
// bytes; DecodeMetadataOnly leaves EncodedBody fields in their encoded form
// and skips body byte-count/UTF-8 revalidation, for callers that only need
// scalar fields (model, tokens, TextLength, ...).
type DecodeMode int

const (
	DecodeStrict DecodeMode = iota
	DecodeMetadataOnly
)

type Decoder struct {
	reader       *bufio.Reader
	maxLineBytes int
	mode         DecodeMode
	line         int
	offset       int64
	done         bool
	recordLine   int
	recordOffset int64
}

// DecoderOption configures a Decoder at construction.
type DecoderOption func(*Decoder)

// WithMetadataOnly decodes records without materializing or validating body
// bytes (see DecodeMetadataOnly). Full-decode (DecodeStrict) remains the
// default.
func WithMetadataOnly() DecoderOption {
	return func(d *Decoder) { d.mode = DecodeMetadataOnly }
}

func NewDecoder(r io.Reader, maxLineBytes int, opts ...DecoderOption) *Decoder {
	d := &Decoder{
		reader:       bufio.NewReader(r),
		maxLineBytes: maxLineBytes,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

func (d *Decoder) Next() (APILogRecord, error) {
	if d.done {
		return nil, io.EOF
	}
	lineNumber := d.line + 1
	lineOffset := d.offset
	d.recordLine = lineNumber
	d.recordOffset = lineOffset
	line, complete, tooLong, err := d.readLine()
	if err != nil {
		return nil, fmt.Errorf("API log line %d at offset %d: %w", lineNumber, lineOffset, err)
	}
	if !complete {
		d.done = true
		if len(line) == 0 && !tooLong {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("API log line %d at offset %d: %w", lineNumber, lineOffset, ErrPartialTail)
	}
	d.line++
	if tooLong {
		return nil, fmt.Errorf("API log line %d at offset %d exceeds %d bytes", lineNumber, lineOffset, d.maxLineBytes)
	}
	record, err := decodeRecord(line, d.mode)
	if err != nil {
		return nil, fmt.Errorf("API log line %d at offset %d: %w", lineNumber, lineOffset, err)
	}
	return record, nil
}

// RecordOffset returns the byte offset of the record most recently returned
// or failed by Next: the start of a successfully decoded record, or the
// start of the record Next failed on (a rejected complete record, or a
// partial final fragment). It is meaningless before the first call to Next.
func (d *Decoder) RecordOffset() int64 { return d.recordOffset }

// RecordLine returns the 1-based record number matching RecordOffset.
func (d *Decoder) RecordLine() int { return d.recordLine }

func (d *Decoder) readLine() (line []byte, complete, tooLong bool, err error) {
	if d.maxLineBytes <= 0 {
		return nil, false, false, errors.New("maximum line bytes must be positive")
	}
	for {
		fragment, readErr := d.reader.ReadSlice('\n')
		d.offset += int64(len(fragment))
		content := fragment
		if len(content) > 0 && content[len(content)-1] == '\n' {
			content = content[:len(content)-1]
		}
		if !tooLong {
			if len(line)+len(content) > d.maxLineBytes {
				tooLong = true
			} else {
				line = append(line, content...)
			}
		}

		switch {
		case readErr == nil:
			return line, true, tooLong, nil
		case errors.Is(readErr, bufio.ErrBufferFull):
			continue
		case errors.Is(readErr, io.EOF):
			return line, false, tooLong, nil
		default:
			return nil, false, tooLong, readErr
		}
	}
}

const recoveryScanBlockBytes = 64 << 10

// ScanRecovery validates the canonical append boundary within a suffix bounded
// by maxLineBytes and reports the byte offset after its last complete record. A
// final unterminated fragment is reported separately so the file owner can
// truncate it without treating a corrupt or oversized boundary as recoverable.
func ScanRecovery(r io.ReadSeeker, maxLineBytes int) (lastCompleteOffset int64, partialTail bool, err error) {
	if maxLineBytes <= 0 {
		return 0, false, errors.New("maximum line bytes must be positive")
	}
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, false, fmt.Errorf("seek API log end for recovery: %w", err)
	}
	if size == 0 {
		return 0, false, nil
	}

	finalByte, err := readRecoveryRange(r, size-1, 1)
	if err != nil {
		return 0, false, err
	}
	if finalByte[0] == '\n' {
		lineStart, err := validateRecoveryBoundaryRecord(r, size-1, maxLineBytes)
		if err != nil {
			return lineStart, false, err
		}
		return size, false, nil
	}

	partialStart, err := findRecoveryLineStart(r, size, maxLineBytes)
	if err != nil {
		return 0, false, err
	}
	if size-partialStart > int64(maxLineBytes) {
		return 0, false, fmt.Errorf("partial API-log tail at offset %d exceeds %d bytes", partialStart, maxLineBytes)
	}
	if partialStart == 0 {
		return 0, true, nil
	}
	lineStart, err := validateRecoveryBoundaryRecord(r, partialStart-1, maxLineBytes)
	if err != nil {
		return lineStart, false, err
	}
	return partialStart, true, nil
}

func validateRecoveryBoundaryRecord(r io.ReadSeeker, lineEnd int64, maxLineBytes int) (int64, error) {
	lineStart, err := findRecoveryLineStart(r, lineEnd, maxLineBytes)
	if err != nil {
		return lineStart, err
	}
	lineBytes := lineEnd - lineStart
	if lineBytes > int64(maxLineBytes) {
		return lineStart, fmt.Errorf("API-log boundary record at offset %d exceeds %d bytes", lineStart, maxLineBytes)
	}
	line, err := readRecoveryRange(r, lineStart, lineBytes)
	if err != nil {
		return lineStart, err
	}
	if _, err := DecodeRecord(line); err != nil {
		return lineStart, fmt.Errorf("decode API-log boundary record at offset %d: %w", lineStart, err)
	}
	return lineStart, nil
}

func findRecoveryLineStart(r io.ReadSeeker, lineEnd int64, maxLineBytes int) (int64, error) {
	cursor := lineEnd
	remaining := int64(maxLineBytes) + 1
	buffer := make([]byte, min(recoveryScanBlockBytes, maxLineBytes+1))
	for cursor > 0 && remaining > 0 {
		readBytes := min(cursor, remaining, int64(len(buffer)))
		readOffset := cursor - readBytes
		chunk := buffer[:int(readBytes)]
		if err := readRecoveryRangeInto(r, readOffset, chunk); err != nil {
			return 0, err
		}
		if newline := bytes.LastIndexByte(chunk, '\n'); newline >= 0 {
			return readOffset + int64(newline) + 1, nil
		}
		cursor = readOffset
		remaining -= readBytes
	}
	if cursor == 0 {
		return 0, nil
	}
	return cursor, fmt.Errorf("API-log boundary ending at offset %d exceeds %d bytes", lineEnd, maxLineBytes)
}

func readRecoveryRange(r io.ReadSeeker, offset, length int64) ([]byte, error) {
	data := make([]byte, int(length))
	if err := readRecoveryRangeInto(r, offset, data); err != nil {
		return nil, err
	}
	return data, nil
}

func readRecoveryRangeInto(r io.ReadSeeker, offset int64, data []byte) error {
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("seek API log recovery range at offset %d: %w", offset, err)
	}
	if _, err := io.ReadFull(r, data); err != nil {
		return fmt.Errorf("read API log recovery range at offset %d: %w", offset, err)
	}
	return nil
}

func DecodeRecord(line []byte) (APILogRecord, error) {
	return decodeRecord(line, DecodeStrict)
}

func decodeRecord(line []byte, mode DecodeMode) (APILogRecord, error) {
	if !utf8.Valid(line) {
		return nil, errors.New("API-log record is not valid UTF-8")
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(line, &kind); err != nil {
		return nil, fmt.Errorf("decode record kind: %w", err)
	}

	var record APILogRecord
	switch kind.Kind {
	case attemptRecordKind:
		var attempt APIAttemptRecord
		if err := decodeStrict(line, &attempt); err != nil {
			return nil, fmt.Errorf("decode %s record: %w", attemptRecordKind, err)
		}
		record = attempt
	case settlementRecordKind:
		var settlement APIAttemptGroupSettlement
		if err := decodeStrict(line, &settlement); err != nil {
			return nil, fmt.Errorf("decode %s record: %w", settlementRecordKind, err)
		}
		record = settlement
	default:
		return nil, fmt.Errorf("unknown API-log record kind %q", kind.Kind)
	}
	if err := record.validateRecord(mode); err != nil {
		return nil, fmt.Errorf("invalid %s record: %w", record.RecordKind(), err)
	}
	return record, nil
}

// MarshalRecord validates and encodes one concrete canonical API-log record.
func MarshalRecord(record APILogRecord) ([]byte, error) {
	switch typed := record.(type) {
	case APIAttemptRecord:
		if err := typed.validateProviderEvidence(); err != nil {
			return nil, fmt.Errorf("API-log record credential admission failed: %w", err)
		}
	case APIAttemptGroupSettlement:
	default:
		return nil, fmt.Errorf("unsupported API-log record type %T", record)
	}
	if err := record.validateRecord(DecodeStrict); err != nil {
		return nil, fmt.Errorf("invalid %s record: %w", record.RecordKind(), err)
	}
	line, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("marshal %s record: %w", record.RecordKind(), err)
	}
	return line, nil
}

func decodeStrict(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
