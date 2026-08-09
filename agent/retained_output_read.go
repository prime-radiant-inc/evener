package agent

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"

	"primeradiant.com/serf/agent/internal/jobstore"
)

const (
	retainedOutputPageBytes          = 16 << 10
	retainedSearchMaxMatches         = 100
	retainedSearchMaxSerializedBytes = 64 << 10
	retainedSearchMaxLineBytes       = 64 << 10
	retainedSearchMaxSkippedLines    = 100
)

var (
	errRetainedOffsetOutOfRange  = errors.New("retained output offset out of range")
	errRetainedOffsetUnavailable = errors.New("retained output offset unavailable")
)

type retainedContinuation struct {
	OffsetBytes int64 `json:"offset_bytes"`
}

type retainedPage struct {
	OffsetBytes   int64                 `json:"offset_bytes"`
	BytesReturned int64                 `json:"bytes_returned"`
	TotalBytes    int64                 `json:"total_bytes"`
	Encoding      string                `json:"encoding"`
	Data          string                `json:"data"`
	Continuation  *retainedContinuation `json:"continuation,omitempty"`
}

// readRetainedPage reads one fixed 16 KiB raw page. ReaderAt offsets are
// relative to the retained bytes, while every offset accepted and returned by
// this helper is in the lifetime byte coordinate space.
func readRetainedPage(r io.ReaderAt, retainedStart, total, offset int64) (retainedPage, error) {
	if r == nil {
		return retainedPage{}, errors.New("retained output reader is nil")
	}
	if retainedStart < 0 || total < retainedStart {
		return retainedPage{}, fmt.Errorf("invalid retained output bounds [%d,%d)", retainedStart, total)
	}
	if offset < 0 {
		return retainedPage{}, fmt.Errorf("%w: offset=%d valid=[%d,%d]", errRetainedOffsetOutOfRange, offset, retainedStart, total)
	}
	if offset < retainedStart {
		return retainedPage{}, fmt.Errorf("%w: offset=%d first_available=%d", errRetainedOffsetUnavailable, offset, retainedStart)
	}
	if offset > total {
		return retainedPage{}, fmt.Errorf("%w: offset=%d valid=[%d,%d]", errRetainedOffsetOutOfRange, offset, retainedStart, total)
	}

	page := retainedPage{OffsetBytes: offset, TotalBytes: total, Encoding: "utf8"}
	n := min(int64(retainedOutputPageBytes), total-offset)
	content := make([]byte, int(n))
	if n > 0 {
		read, err := r.ReadAt(content, offset-retainedStart)
		if err != nil && !(errors.Is(err, io.EOF) && read == len(content)) {
			return retainedPage{}, fmt.Errorf("read retained output page: %w", err)
		}
		if read != len(content) {
			return retainedPage{}, fmt.Errorf("read retained output page: %w", io.ErrUnexpectedEOF)
		}
	}
	page.BytesReturned = n
	if utf8.Valid(content) {
		page.Data = string(content)
	} else {
		page.Encoding = "base64"
		page.Data = base64.StdEncoding.EncodeToString(content)
	}
	if end := offset + n; end < total {
		page.Continuation = &retainedContinuation{OffsetBytes: end}
	}
	return page, nil
}

// searchSource supplies stable, forward, lifetime-offset raw windows. Concrete
// job sources use OutputStore.ReadWindow while live and
// jobstore.ReadOutputWindowSnapshot after closure; artifacts use ReaderAt.
type searchSource interface {
	ReadWindow(offset int64, maxBytes int) (jobstore.OutputWindowSnapshot, error)
}

type retainedSearchOptions struct {
	Regexp *regexp.Regexp
	jobstore.SearchOptions
}

type retainedSearchMatch struct {
	LineStartByte int64    `json:"line_start_byte"`
	Before        []string `json:"before"`
	Line          string   `json:"line"`
	After         []string `json:"after"`
}

type retainedSearchSkippedLine struct {
	StartByte int64 `json:"start_byte"`
	EndByte   int64 `json:"end_byte"`
}

type retainedSearchEnvelope struct {
	OffsetBytes          int64                       `json:"offset_bytes"`
	RetainedStartBytes   int64                       `json:"retained_start_bytes"`
	TotalBytes           int64                       `json:"total_bytes"`
	SearchComplete       bool                        `json:"search_complete"`
	SkippedPartialPrefix bool                        `json:"skipped_partial_prefix"`
	Matches              []retainedSearchMatch       `json:"matches"`
	SkippedOversized     []retainedSearchSkippedLine `json:"skipped_oversized_lines,omitempty"`
	Continuation         *retainedContinuation       `json:"continuation,omitempty"`
}

func searchRetainedOutput(source searchSource, opts retainedSearchOptions) (retainedSearchEnvelope, error) {
	if source == nil {
		return retainedSearchEnvelope{}, errors.New("retained search source is nil")
	}
	if opts.Regexp == nil {
		return retainedSearchEnvelope{}, errors.New("retained search regexp is nil")
	}
	if opts.StartOffset < 0 {
		return retainedSearchEnvelope{}, fmt.Errorf("%w: start offset=%d", jobstore.ErrInvalidOffset, opts.StartOffset)
	}
	if opts.MaxMatches < 0 {
		return retainedSearchEnvelope{}, fmt.Errorf("%w: max matches=%d", jobstore.ErrInvalidLimit, opts.MaxMatches)
	}
	if opts.MaxSerializedBytes < 0 {
		return retainedSearchEnvelope{}, fmt.Errorf("%w: max serialized bytes=%d", jobstore.ErrInvalidLimit, opts.MaxSerializedBytes)
	}
	if opts.ContextLines < 0 || opts.ContextLines > 10 {
		return retainedSearchEnvelope{}, fmt.Errorf("invalid context lines %d: want 0..10", opts.ContextLines)
	}

	maxMatches := retainedSearchMaxMatches
	if opts.MaxMatches > 0 {
		maxMatches = min(maxMatches, opts.MaxMatches)
	}
	maxSerialized := retainedSearchMaxSerializedBytes
	if opts.MaxSerializedBytes > 0 {
		maxSerialized = min(maxSerialized, opts.MaxSerializedBytes)
	}

	stream, first, err := newRetainedWindowStream(source, opts.StartOffset)
	if err != nil {
		return retainedSearchEnvelope{}, err
	}
	envelope := retainedSearchEnvelope{
		OffsetBytes:        opts.StartOffset,
		RetainedStartBytes: first.RetainedStart,
		TotalBytes:         first.TotalBytes,
		Matches:            make([]retainedSearchMatch, 0),
	}
	scanner := retainedLineScanner{
		r:      bufio.NewReaderSize(stream, 4096),
		offset: opts.StartOffset,
	}

	if opts.SkipPartialPrefix {
		line, ok, err := scanner.next()
		if err != nil {
			return retainedSearchEnvelope{}, err
		}
		if ok {
			if !line.complete && opts.DeferEOFFragment {
				envelope.SearchComplete = true
				envelope.Continuation = &retainedContinuation{OffsetBytes: line.start}
				return envelope, nil
			}
			envelope.SkippedPartialPrefix = true
		}
	}

	var (
		history []string
		pending []retainedSearchLine
		eof     bool
	)
	for {
		line, ok, err := nextRetainedSearchLine(&scanner, &pending, &eof)
		if err != nil {
			return retainedSearchEnvelope{}, err
		}
		if !ok {
			envelope.SearchComplete = true
			return envelope, nil
		}
		if !line.complete && opts.DeferEOFFragment {
			envelope.SearchComplete = true
			envelope.Continuation = &retainedContinuation{OffsetBytes: line.start}
			return envelope, nil
		}
		if line.oversized {
			if len(envelope.SkippedOversized) >= retainedSearchMaxSkippedLines {
				envelope.Continuation = &retainedContinuation{OffsetBytes: line.start}
				return envelope, nil
			}
			envelope.SkippedOversized = append(envelope.SkippedOversized, retainedSearchSkippedLine{StartByte: line.start, EndByte: line.end})
			history = history[:0]
			continue
		}
		if !opts.Regexp.Match(line.content) {
			history = appendRetainedHistory(history, string(line.content), opts.ContextLines)
			continue
		}
		if len(envelope.Matches) >= maxMatches {
			envelope.Continuation = &retainedContinuation{OffsetBytes: line.start}
			return envelope, nil
		}

		if err := fillRetainedLookahead(&scanner, &pending, &eof, opts.ContextLines); err != nil {
			return retainedSearchEnvelope{}, err
		}
		match := retainedSearchMatch{
			LineStartByte: line.start,
			Before:        append([]string(nil), history...),
			Line:          string(line.content),
			After:         retainedAfterContext(pending, opts.ContextLines, opts.DeferEOFFragment),
		}
		candidateBytes, err := json.Marshal(match)
		if err != nil {
			return retainedSearchEnvelope{}, fmt.Errorf("marshal retained search match: %w", err)
		}
		// A match/context record that cannot fit in an otherwise empty response
		// under the effective cap cannot be recovered by returning continuation at
		// the same line. Report its interval as oversized and evaluate later lines
		// so every call either finishes or advances.
		if retainedMatchesSerializedSize(nil, len(candidateBytes)) > maxSerialized {
			if len(envelope.SkippedOversized) >= retainedSearchMaxSkippedLines {
				envelope.Continuation = &retainedContinuation{OffsetBytes: line.start}
				return envelope, nil
			}
			envelope.SkippedOversized = append(envelope.SkippedOversized, retainedSearchSkippedLine{StartByte: line.start, EndByte: line.end})
			history = history[:0]
			continue
		}
		if retainedMatchesSerializedSize(envelope.Matches, len(candidateBytes)) > maxSerialized {
			envelope.Continuation = &retainedContinuation{OffsetBytes: line.start}
			return envelope, nil
		}
		envelope.Matches = append(envelope.Matches, match)
		history = appendRetainedHistory(history, string(line.content), opts.ContextLines)
	}
}

func retainedMatchesSerializedSize(matches []retainedSearchMatch, candidateBytes int) int {
	size := 2 + candidateBytes // surrounding JSON array brackets
	for i := range matches {
		encoded, err := json.Marshal(matches[i])
		if err != nil {
			// The values contain only strings and integers, so this is unreachable.
			return retainedSearchMaxSerializedBytes + 1
		}
		size += len(encoded) + 1 // comma before each existing/candidate neighbor
	}
	return size
}

func appendRetainedHistory(history []string, line string, contextLines int) []string {
	if contextLines == 0 {
		return history[:0]
	}
	history = append(history, line)
	if len(history) > contextLines {
		copy(history, history[len(history)-contextLines:])
		history = history[:contextLines]
	}
	return history
}

func retainedAfterContext(pending []retainedSearchLine, contextLines int, deferEOF bool) []string {
	if contextLines == 0 {
		return nil
	}
	after := make([]string, 0, contextLines)
	for _, line := range pending[:min(len(pending), contextLines)] {
		if line.oversized || (!line.complete && deferEOF) {
			continue
		}
		after = append(after, string(line.content))
	}
	return after
}

func nextRetainedSearchLine(scanner *retainedLineScanner, pending *[]retainedSearchLine, eof *bool) (retainedSearchLine, bool, error) {
	if len(*pending) > 0 {
		line := (*pending)[0]
		copy(*pending, (*pending)[1:])
		*pending = (*pending)[:len(*pending)-1]
		return line, true, nil
	}
	if *eof {
		return retainedSearchLine{}, false, nil
	}
	line, ok, err := scanner.next()
	if !ok && err == nil {
		*eof = true
	}
	return line, ok, err
}

func fillRetainedLookahead(scanner *retainedLineScanner, pending *[]retainedSearchLine, eof *bool, contextLines int) error {
	for len(*pending) < contextLines && !*eof {
		line, ok, err := scanner.next()
		if err != nil {
			return err
		}
		if !ok {
			*eof = true
			break
		}
		*pending = append(*pending, line)
	}
	return nil
}

type retainedSearchLine struct {
	start     int64
	end       int64
	content   []byte
	complete  bool
	oversized bool
}

type retainedLineScanner struct {
	r      *bufio.Reader
	offset int64
}

func (s *retainedLineScanner) next() (retainedSearchLine, bool, error) {
	line := retainedSearchLine{start: s.offset}
	raw := make([]byte, 0, min(retainedSearchMaxLineBytes+2, 4096))
	var rawBytes int64
	for {
		fragment, err := s.r.ReadSlice('\n')
		if len(fragment) > 0 {
			rawBytes += int64(len(fragment))
			s.offset += int64(len(fragment))
			if !line.oversized {
				available := retainedSearchMaxLineBytes + 2 - len(raw)
				if available > 0 {
					take := min(available, len(fragment))
					raw = append(raw, fragment[:take]...)
				}
				if int64(len(raw)) < rawBytes {
					line.oversized = true
				}
			}
			if fragment[len(fragment)-1] == '\n' {
				line.complete = true
				line.end = s.offset
				return finishRetainedSearchLine(line, raw), true, nil
			}
		}
		if err != nil {
			switch {
			case errors.Is(err, bufio.ErrBufferFull):
				continue
			case errors.Is(err, io.EOF):
				if rawBytes == 0 {
					return retainedSearchLine{}, false, nil
				}
				line.end = s.offset
				return finishRetainedSearchLine(line, raw), true, nil
			default:
				return retainedSearchLine{}, false, fmt.Errorf("read retained output line: %w", err)
			}
		}
	}
}

func finishRetainedSearchLine(line retainedSearchLine, raw []byte) retainedSearchLine {
	if line.oversized {
		return line
	}
	content := raw
	if line.complete && len(content) > 0 && content[len(content)-1] == '\n' {
		content = content[:len(content)-1]
		if len(content) > 0 && content[len(content)-1] == '\r' {
			content = content[:len(content)-1]
		}
	}
	if len(content) > retainedSearchMaxLineBytes {
		line.oversized = true
		return line
	}
	line.content = append([]byte(nil), content...)
	return line
}

type retainedWindowStream struct {
	source  searchSource
	next    int64
	total   int64
	pending []byte
}

func newRetainedWindowStream(source searchSource, offset int64) (*retainedWindowStream, jobstore.OutputWindowSnapshot, error) {
	first, err := source.ReadWindow(offset, retainedOutputPageBytes)
	if err != nil {
		return nil, first, err
	}
	if err := validateRetainedWindow(first, offset); err != nil {
		return nil, first, err
	}
	return &retainedWindowStream{
		source:  source,
		next:    first.End,
		total:   first.TotalBytes,
		pending: first.Content,
	}, first, nil
}

func (r *retainedWindowStream) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.next >= r.total {
			return 0, io.EOF
		}
		maxBytes := retainedOutputPageBytes
		if remaining := r.total - r.next; remaining < int64(maxBytes) {
			maxBytes = int(remaining)
		}
		window, err := r.source.ReadWindow(r.next, maxBytes)
		if err != nil {
			return 0, err
		}
		if err := validateRetainedWindow(window, r.next); err != nil {
			return 0, err
		}
		if window.End > r.total {
			return 0, errors.New("retained output window exceeded initial snapshot total")
		}
		if len(window.Content) == 0 {
			return 0, io.ErrUnexpectedEOF
		}
		r.next = window.End
		r.pending = window.Content
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func validateRetainedWindow(window jobstore.OutputWindowSnapshot, requested int64) error {
	if window.RetainedStart < 0 || window.TotalBytes < window.RetainedStart || window.Start != requested || window.End < window.Start || window.End > window.TotalBytes {
		return fmt.Errorf("invalid retained output window: %+v", window)
	}
	if int64(len(window.Content)) != window.End-window.Start {
		return fmt.Errorf("retained output window length %d does not match [%d,%d)", len(window.Content), window.Start, window.End)
	}
	if requested < window.RetainedStart {
		return fmt.Errorf("%w: offset=%d first_available=%d", jobstore.ErrOutputPruned, requested, window.RetainedStart)
	}
	return nil
}
