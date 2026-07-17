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

type Decoder struct {
	reader             *bufio.Reader
	maxLineBytes       int
	line               int
	offset             int64
	lastCompleteOffset int64
	done               bool
}

func NewDecoder(r io.Reader, maxLineBytes int) *Decoder {
	return &Decoder{
		reader:       bufio.NewReader(r),
		maxLineBytes: maxLineBytes,
	}
}

func (d *Decoder) Next() (APILogRecord, error) {
	if d.done {
		return nil, io.EOF
	}
	lineNumber := d.line + 1
	lineOffset := d.offset
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
	record, err := DecodeRecord(line)
	if err != nil {
		return nil, fmt.Errorf("API log line %d at offset %d: %w", lineNumber, lineOffset, err)
	}
	d.lastCompleteOffset = d.offset
	return record, nil
}

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

// ScanRecovery validates a current canonical API log from offset zero and
// reports the byte offset after its last complete record. A final unterminated
// fragment is reported separately so the file owner can truncate it without
// treating corrupt or oversized complete records as recoverable.
func ScanRecovery(r io.ReadSeeker, maxLineBytes int) (lastCompleteOffset int64, partialTail bool, err error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, false, fmt.Errorf("seek API log for recovery: %w", err)
	}
	decoder := NewDecoder(r, maxLineBytes)
	for {
		_, err := decoder.Next()
		switch {
		case err == nil:
			continue
		case errors.Is(err, io.EOF):
			return decoder.lastCompleteOffset, false, nil
		case errors.Is(err, ErrPartialTail):
			return decoder.lastCompleteOffset, true, nil
		default:
			return decoder.lastCompleteOffset, false, err
		}
	}
}

func DecodeRecord(line []byte) (APILogRecord, error) {
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
	if err := record.validateRecord(); err != nil {
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
	if err := record.validateRecord(); err != nil {
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
