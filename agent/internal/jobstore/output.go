package jobstore

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sync"
)

// ErrOutputPruned is returned by output reads when the durable record remains
// but the bytes were pruned by retention policy. Callers translate this to the
// model-facing output_unavailable / retention_pruned signal in a later phase.
var ErrOutputPruned = errors.New("jobstore: output pruned")

// ErrInvalidLimit is returned when an output read limit is negative.
var ErrInvalidLimit = errors.New("jobstore: invalid limit")

// Match is one grep hit: the matching line and its byte offset in the log.
type Match struct {
	ByteOffset int64  `json:"byte_offset"`
	Line       string `json:"line"`
}

// OutputStore is an append-only per-job output file. capBytes records the
// retention policy for later pruning phases; Phase 1 does not enforce it.
type OutputStore struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	capBytes int64
	total    int64
}

// OpenOutput opens (creating if needed) the per-job log at path and records the
// retention policy cap for later pruning phases.
func OpenOutput(path string, capBytes int64) (*OutputStore, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open output %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &OutputStore{path: path, f: f, capBytes: capBytes, total: info.Size()}, nil
}

// Append writes b to the log and returns the number of bytes written.
func (o *OutputStore) Append(b []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.f.Write(b)
	o.total += int64(n)
	if err != nil {
		return n, fmt.Errorf("jobstore: append output: %w", err)
	}
	return n, nil
}

// Tail returns the last maxBytes bytes of the log, the total byte count, and
// whether the returned slice is a truncated tail of a larger log.
func (o *OutputStore) Tail(maxBytes int) (buf []byte, total int64, truncated bool, err error) {
	if maxBytes < 0 {
		return nil, 0, false, fmt.Errorf("%w: maxBytes=%d", ErrInvalidLimit, maxBytes)
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	info, err := os.Stat(o.path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("jobstore: stat output: %w", err)
	}
	total = info.Size()
	start := int64(0)
	if total > int64(maxBytes) {
		start = total - int64(maxBytes)
		truncated = true
	}
	f, err := os.Open(o.path)
	if err != nil {
		return nil, total, truncated, fmt.Errorf("jobstore: open output: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close output: %w", closeErr)
		}
	}()
	if _, err := f.Seek(start, 0); err != nil {
		return nil, total, truncated, err
	}
	buf = make([]byte, total-start)
	if len(buf) > 0 {
		if _, err := io.ReadFull(f, buf); err != nil {
			return nil, total, truncated, fmt.Errorf("jobstore: read output: %w", err)
		}
	}
	return buf, total, truncated, nil
}

// Grep scans the log line by line and returns up to limitBytes worth of lines
// matching re, each with its byte offset.
func (o *OutputStore) Grep(re *regexp.Regexp, limitBytes int) ([]Match, error) {
	return o.GrepLimit(re, limitBytes, 0)
}

// GrepLimit is like Grep, with an optional maxMatches cap. maxMatches <= 0
// means no match-count cap.
func (o *OutputStore) GrepLimit(re *regexp.Regexp, limitBytes int, maxMatches int) (matches []Match, err error) {
	return o.GrepLimitLineBytes(re, limitBytes, maxMatches, limitBytes)
}

// GrepLimitLineBytes is like GrepLimit, but skips individual lines longer than
// maxLineBytes without allocating or regex-matching the whole line.
func (o *OutputStore) GrepLimitLineBytes(re *regexp.Regexp, limitBytes int, maxMatches int, maxLineBytes int) (matches []Match, err error) {
	if limitBytes < 0 {
		return nil, fmt.Errorf("%w: limitBytes=%d", ErrInvalidLimit, limitBytes)
	}
	if limitBytes == 0 {
		return nil, nil
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	f, err := os.Open(o.path)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open output: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close output: %w", closeErr)
		}
	}()
	return grepReaderLimit(bufio.NewReader(f), re, limitBytes, maxMatches, maxLineBytes)
}

// GrepFileLimit scans a closed output log with the same bounded line handling as
// OutputStore.GrepLimitLineBytes.
func GrepFileLimit(path string, re *regexp.Regexp, limitBytes int, maxMatches int, maxLineBytes int) (matches []Match, err error) {
	if limitBytes < 0 {
		return nil, fmt.Errorf("%w: limitBytes=%d", ErrInvalidLimit, limitBytes)
	}
	if limitBytes == 0 {
		return nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open output: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close output: %w", closeErr)
		}
	}()
	return grepReaderLimit(bufio.NewReader(f), re, limitBytes, maxMatches, maxLineBytes)
}

func grepReaderLimit(r *bufio.Reader, re *regexp.Regexp, limitBytes int, maxMatches int, maxLineBytes int) ([]Match, error) {
	if maxLineBytes <= 0 || maxLineBytes > limitBytes {
		maxLineBytes = limitBytes
	}
	lineCap := maxLineBytes
	if lineCap > 4096 {
		lineCap = 4096
	}
	var (
		matches  []Match
		offset   int64
		lineAt   int64
		budget   = limitBytes
		line     = make([]byte, 0, lineCap)
		overlong bool
	)
	for {
		frag, err := r.ReadSlice('\n')
		if len(frag) > 0 {
			if !overlong {
				if len(line)+len(frag) > maxLineBytes {
					overlong = true
					line = line[:0]
				} else {
					line = append(line, frag...)
				}
			}
			offset += int64(len(frag))
			if frag[len(frag)-1] == '\n' {
				stop := appendGrepLine(&matches, re, lineAt, line, &budget, maxMatches, overlong)
				lineAt = offset
				line = line[:0]
				overlong = false
				if stop {
					break
				}
			}
		}
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(err, io.EOF) {
				if len(line) > 0 || overlong {
					appendGrepLine(&matches, re, lineAt, line, &budget, maxMatches, overlong)
				}
				break
			}
			return nil, fmt.Errorf("jobstore: read output line: %w", err)
		}
	}
	return matches, nil
}

func appendGrepLine(matches *[]Match, re *regexp.Regexp, offset int64, raw []byte, budget *int, maxMatches int, overlong bool) (stop bool) {
	if overlong {
		return false
	}
	line := raw
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
	}
	if !re.Match(line) {
		return false
	}
	if len(line) > *budget {
		return true
	}
	*matches = append(*matches, Match{ByteOffset: offset, Line: string(line)})
	if maxMatches > 0 && len(*matches) >= maxMatches {
		return true
	}
	*budget -= len(line)
	return *budget <= 0
}

// Close closes the underlying file.
func (o *OutputStore) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.f.Close()
}
