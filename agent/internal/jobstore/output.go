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
func (o *OutputStore) Tail(maxBytes int) ([]byte, int64, bool, error) {
	if maxBytes < 0 {
		return nil, 0, false, fmt.Errorf("%w: maxBytes=%d", ErrInvalidLimit, maxBytes)
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	info, err := os.Stat(o.path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("jobstore: stat output: %w", err)
	}
	total := info.Size()
	start := int64(0)
	truncated := false
	if total > int64(maxBytes) {
		start = total - int64(maxBytes)
		truncated = true
	}
	f, err := os.Open(o.path)
	if err != nil {
		return nil, total, truncated, fmt.Errorf("jobstore: open output: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(start, 0); err != nil {
		return nil, total, truncated, err
	}
	buf := make([]byte, total-start)
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
	defer f.Close()
	var matches []Match
	var offset int64
	budget := limitBytes
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if re.MatchString(line) {
			if len(line) > budget {
				break
			}
			matches = append(matches, Match{ByteOffset: offset, Line: line})
			budget -= len(line)
			if budget <= 0 {
				break
			}
		}
		offset += int64(len(line)) + 1 // +1 for the newline the scanner stripped
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: scan output: %w", err)
	}
	return matches, nil
}

// Close closes the underlying file.
func (o *OutputStore) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.f.Close()
}
