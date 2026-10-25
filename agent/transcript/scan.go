package transcript

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
)

// ErrMissingHeader marks a transcript with no complete, non-blank line to hold
// its header: an empty file, or one holding only a torn first line.
var ErrMissingHeader = errors.New("missing transcript header")

// LineError locates a failure in a transcript read at the physical line
// (counting blanks, 1-based) that caused it. Err keeps its own identity, so
// errors.Is still reaches ErrLineTooLong, ErrUnsupportedFormat and the rest.
type LineError struct {
	Line int
	Err  error
}

func (e *LineError) Error() string { return fmt.Sprintf("transcript line %d: %v", e.Line, e.Err) }

func (e *LineError) Unwrap() error { return e.Err }

// ScanResult is what a whole-transcript read learned beyond the entries it
// handed to the visitor.
type ScanResult struct {
	Header Header
	// TornTail reports an unterminated final line that was skipped: a write
	// the crash cut short, not a record.
	TornTail bool
}

// Scan reads a whole transcript: the header, then every complete entry in
// order, strictly decoded. It is the one reader loop for callers that want the
// whole file; limits and error policy are theirs to choose.
//
//   - maxLineBytes bounds one record's payload (non-positive means
//     DefaultMaxLineBytes); an over-limit record fails the scan with
//     ErrLineTooLong.
//   - An unterminated final line is skipped and reported as TornTail. Any
//     corrupt complete line fails the scan.
//   - Read, header and entry failures are returned as *LineError. A transcript
//     with no header is ErrUnsupportedFormat and ErrMissingHeader.
//   - visit receives each entry with its trimmed raw line, which the caller
//     owns and may retain. An error from visit stops the scan and is returned
//     as is. visit may be nil.
//   - ctx is checked before each line.
//
// Callers that must track byte offsets, resume past an over-limit record, or
// read a range of the file keep their own loop over ReadLine.
func Scan(ctx context.Context, r io.Reader, maxLineBytes int, visit func(line []byte, entry Entry) error) (ScanResult, error) {
	reader := bufio.NewReaderSize(r, 64*1024)
	var result ScanResult
	headerRead := false
	lineNumber := 0
	for {
		if err := ctx.Err(); err != nil {
			return ScanResult{}, err
		}
		line, complete, bytesRead, err := ReadLine(reader, maxLineBytes)
		lineNumber++
		if err != nil {
			return ScanResult{}, &LineError{Line: lineNumber, Err: err}
		}
		if !complete {
			result.TornTail = bytesRead > 0
			break
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !headerRead {
			result.Header, err = DecodeHeader(line)
			if err != nil {
				return ScanResult{}, &LineError{Line: lineNumber, Err: err}
			}
			headerRead = true
			continue
		}
		entry, err := DecodeEntry(line)
		if err != nil {
			return ScanResult{}, &LineError{Line: lineNumber, Err: err}
		}
		if visit != nil {
			if err := visit(line, entry); err != nil {
				return ScanResult{}, err
			}
		}
	}
	if !headerRead {
		return ScanResult{}, fmt.Errorf("%w: %w", ErrUnsupportedFormat, ErrMissingHeader)
	}
	return result, nil
}
