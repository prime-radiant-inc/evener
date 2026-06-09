package jobstore

import (
	"bufio"
	"encoding/json"
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
// retained-tail policy. total tracks lifetime bytes, while retainedStart is the
// lifetime offset corresponding to byte 0 in the retained file.
type OutputStore struct {
	mu            sync.Mutex
	path          string
	metaPath      string
	f             *os.File
	capBytes      int64
	total         int64
	retainedStart int64
}

type outputMeta struct {
	TotalBytes    int64 `json:"total_bytes"`
	RetainedStart int64 `json:"retained_start"`
}

// OpenOutput opens (creating if needed) the per-job log at path and enforces the
// retained-tail cap. Existing oversized files are treated as unpruned lifetime
// output and reduced to their capped tail.
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
	metaPath := outputMetaPath(path)
	total, retainedStart, err := readOutputMetaForSize(metaPath, info.Size())
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	o := &OutputStore{path: path, metaPath: metaPath, f: f, capBytes: capBytes, total: total, retainedStart: retainedStart}
	if err := o.pruneLocked(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := o.persistMetaLocked(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return o, nil
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
	if err := o.pruneLocked(); err != nil {
		return n, err
	}
	if err := o.persistMetaLocked(); err != nil {
		return n, err
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
	retained := info.Size()
	total = o.total
	start := int64(0)
	if retained > int64(maxBytes) {
		start = retained - int64(maxBytes)
		truncated = true
	}
	if o.retainedStart > 0 {
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
	buf = make([]byte, retained-start)
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
	matches, err = grepReaderLimit(bufio.NewReader(f), re, limitBytes, maxMatches, maxLineBytes)
	if err != nil {
		return nil, err
	}
	shiftMatches(matches, o.retainedStart)
	return matches, nil
}

// GrepFileLimit scans a closed output log with the same bounded line handling as
// OutputStore.GrepLimitLineBytes.
func GrepFileLimit(path string, re *regexp.Regexp, limitBytes int, maxMatches int, maxLineBytes int) (matches []Match, err error) {
	return GrepFileLimitAt(path, re, limitBytes, maxMatches, maxLineBytes, 0)
}

// GrepFileLimitAt is like GrepFileLimit, but shifts returned offsets by
// retainedStart when the file contains only a retained tail.
func GrepFileLimitAt(path string, re *regexp.Regexp, limitBytes int, maxMatches int, maxLineBytes int, retainedStart int64) (matches []Match, err error) {
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
	matches, err = grepReaderLimit(bufio.NewReader(f), re, limitBytes, maxMatches, maxLineBytes)
	if err != nil {
		return nil, err
	}
	shiftMatches(matches, retainedStart)
	return matches, nil
}

// OutputFileStats returns durable lifetime output metadata for a closed output
// file. If no metadata exists, the file is treated as unpruned.
func OutputFileStats(path string) (total int64, retainedStart int64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, fmt.Errorf("jobstore: stat output: %w", err)
	}
	total, retainedStart, err = readOutputMetaForSize(outputMetaPath(path), info.Size())
	if err != nil {
		return 0, 0, err
	}
	return total, retainedStart, nil
}

func (o *OutputStore) pruneLocked() error {
	if o.capBytes <= 0 {
		return nil
	}
	info, err := o.f.Stat()
	if err != nil {
		return fmt.Errorf("jobstore: stat output: %w", err)
	}
	if info.Size() <= o.capBytes {
		o.retainedStart = o.total - info.Size()
		return nil
	}
	keep := o.capBytes
	tail := make([]byte, keep)
	if _, err := o.f.Seek(info.Size()-keep, 0); err != nil {
		return fmt.Errorf("jobstore: seek output prune tail: %w", err)
	}
	if _, err := io.ReadFull(o.f, tail); err != nil {
		return fmt.Errorf("jobstore: read output prune tail: %w", err)
	}
	if err := o.f.Truncate(0); err != nil {
		return fmt.Errorf("jobstore: truncate output: %w", err)
	}
	if _, err := o.f.Seek(0, 0); err != nil {
		return fmt.Errorf("jobstore: seek output rewrite: %w", err)
	}
	if _, err := o.f.Write(tail); err != nil {
		return fmt.Errorf("jobstore: rewrite output tail: %w", err)
	}
	if err := o.f.Truncate(keep); err != nil {
		return fmt.Errorf("jobstore: trim output tail: %w", err)
	}
	if _, err := o.f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("jobstore: seek output eof: %w", err)
	}
	o.retainedStart = o.total - keep
	return nil
}

func (o *OutputStore) persistMetaLocked() error {
	if o.metaPath == "" {
		return nil
	}
	meta := outputMeta{
		TotalBytes:    o.total,
		RetainedStart: o.retainedStart,
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("jobstore: marshal output metadata: %w", err)
	}
	b = append(b, '\n')
	tmp := o.metaPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("jobstore: write output metadata: %w", err)
	}
	if err := os.Rename(tmp, o.metaPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("jobstore: replace output metadata: %w", err)
	}
	return nil
}

func outputMetaPath(path string) string {
	return path + ".meta.json"
}

func readOutputMetaForSize(path string, retained int64) (total int64, retainedStart int64, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return retained, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("jobstore: read output metadata: %w", err)
	}
	var meta outputMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return 0, 0, fmt.Errorf("jobstore: parse output metadata: %w", err)
	}
	if meta.TotalBytes < retained || meta.RetainedStart < 0 || meta.TotalBytes-meta.RetainedStart != retained {
		return 0, 0, errors.New("jobstore: output metadata does not match retained output")
	}
	return meta.TotalBytes, meta.RetainedStart, nil
}

func shiftMatches(matches []Match, retainedStart int64) {
	if retainedStart == 0 {
		return
	}
	for i := range matches {
		matches[i].ByteOffset += retainedStart
	}
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
