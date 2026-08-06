package jobstore

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/internal/runetrim"
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
	fs            afero.Fs
	f             afero.File
	capBytes      int64
	total         int64
	retainedStart int64
	disableSync   bool
}

type outputMeta struct {
	TotalBytes     int64  `json:"total_bytes"`
	RetainedStart  int64  `json:"retained_start"`
	RetainedSHA256 string `json:"retained_sha256"`
}

// OpenOutput opens (creating if needed) the per-job log at path and enforces the
// retained-tail cap. Existing oversized files are treated as unpruned lifetime
// output and reduced to their capped tail.
func OpenOutput(path string, capBytes int64) (*OutputStore, error) {
	return openOutputFs(afero.NewOsFs(), path, capBytes)
}

// OpenOutputNoSync opens an output store with durability fsyncs disabled. It is
// for tests whose contract is output behavior, not crash persistence.
func OpenOutputNoSync(path string, capBytes int64) (*OutputStore, error) {
	return openOutputFsNoSync(afero.NewOsFs(), path, capBytes)
}

// CreateOutput exclusively creates a new output store and refuses any existing
// log or metadata artifact belonging to the path.
func CreateOutput(path string, capBytes int64) (*OutputStore, error) {
	return createOutputFsWithSync(afero.NewOsFs(), path, capBytes, false)
}

// CreateOutputNoSync is CreateOutput with durability fsyncs disabled for tests.
func CreateOutputNoSync(path string, capBytes int64) (*OutputStore, error) {
	return createOutputFsWithSync(afero.NewOsFs(), path, capBytes, true)
}

// openOutputFs opens the output store on the given filesystem. OpenOutput
// delegates here with afero.NewOsFs(), which forwards every call straight to the
// os package, so production behavior is byte-identical to direct os calls. Tests
// and fuzzers inject an in-memory or fault-injecting filesystem to drive the
// prune/persist error arms off real disk.
func openOutputFs(fs afero.Fs, path string, capBytes int64) (*OutputStore, error) {
	return openOutputFsWithSync(fs, path, capBytes, false, os.O_RDWR|os.O_CREATE|os.O_APPEND)
}

func openOutputFsNoSync(fs afero.Fs, path string, capBytes int64) (*OutputStore, error) {
	return openOutputFsWithSync(fs, path, capBytes, true, os.O_RDWR|os.O_CREATE|os.O_APPEND)
}

func createOutputFsWithSync(fs afero.Fs, path string, capBytes int64, disableSync bool) (*OutputStore, error) {
	if err := refuseExistingOutputArtifacts(fs, path); err != nil {
		return nil, err
	}
	return openOutputFsWithSync(fs, path, capBytes, disableSync, os.O_RDWR|os.O_CREATE|os.O_EXCL|os.O_APPEND)
}

func openOutputFsWithSync(fs afero.Fs, path string, capBytes int64, disableSync bool, flags int) (*OutputStore, error) {
	created := flags&os.O_EXCL != 0
	f, err := fs.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open output %s: %w", path, err)
	}
	cleanupCreated := func() {
		_ = f.Close()
		if created {
			_ = removeOutputArtifactsFs(fs, path)
		}
	}
	info, err := f.Stat()
	if err != nil {
		cleanupCreated()
		return nil, err
	}
	metaPath := outputMetaPath(path)
	total := info.Size()
	retainedStart := int64(0)
	if !created {
		total, retainedStart, err = readOutputMetaForFile(fs, metaPath, path, info.Size())
		if err != nil {
			cleanupCreated()
			return nil, err
		}
	}
	o := &OutputStore{path: path, metaPath: metaPath, fs: fs, f: f, capBytes: capBytes, total: total, retainedStart: retainedStart, disableSync: disableSync}
	if err := o.pruneLocked(); err != nil {
		cleanupCreated()
		return nil, err
	}
	if err := o.persistMetaLocked(); err != nil {
		cleanupCreated()
		return nil, err
	}
	return o, nil
}

func refuseExistingOutputArtifacts(fs afero.Fs, path string) error {
	for _, artifact := range outputArtifactPaths(path) {
		if _, err := lstatOutputArtifact(fs, artifact); err == nil {
			return fmt.Errorf("jobstore: create output %s: %w", path, os.ErrExist)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("jobstore: lstat output artifact %s: %w", artifact, err)
		}
	}
	return nil
}

func lstatOutputArtifact(fs afero.Fs, path string) (os.FileInfo, error) {
	lstater, ok := fs.(afero.Lstater)
	if !ok {
		return nil, fmt.Errorf("jobstore: non-following lookup unavailable for %s", path)
	}
	info, usedLstat, err := lstater.LstatIfPossible(path)
	if !usedLstat {
		return nil, fmt.Errorf("jobstore: non-following lookup unavailable for %s", path)
	}
	return info, err
}

func outputArtifactPaths(path string) []string {
	metaPath := outputMetaPath(path)
	return []string{
		path,
		metaPath,
		metaPath + ".tmp",
		outputPendingMetaPath(metaPath),
		outputPendingMetaPath(metaPath) + ".tmp",
	}
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

// Len returns the lifetime length of the output stream: the total number of
// bytes ever appended, even when retention has pruned the on-disk prefix. This
// is the offset space the OutputMatcher counts in, so callers feeding the
// matcher pass this post-append value as the chunk's end offset.
func (o *OutputStore) Len() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.total
}

// RetainedStart returns the lifetime offset of byte 0 in the retained file:
// the number of bytes permanently evicted off the head by the retention cap
// (0 when nothing has been pruned).
func (o *OutputStore) RetainedStart() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.retainedStart
}

// WindowBounds maps a paged read onto the lifetime offset space [start, end):
// beforeBytes <= 0 reads the tail window ending at total; beforeBytes > 0
// reads up to maxBytes ending at that offset (exclusive). The window never
// crosses below earliest - bytes evicted by the retention cap are gone, so a
// page ending at or before earliest is empty.
func WindowBounds(beforeBytes, maxBytes, total, earliest int64) (start, end int64) {
	end = total
	if beforeBytes > 0 && beforeBytes < total {
		end = beforeBytes
	}
	if end < earliest {
		end = earliest
	}
	start = end - maxBytes
	if start < earliest {
		start = earliest
	}
	return start, end
}

// Window returns up to maxBytes of the log ending at lifetime offset
// beforeBytes (exclusive), paging backwards from the tail: beforeBytes <= 0
// reads the tail, exactly like Tail(maxBytes). It returns the window bytes
// plus the lifetime offsets [start, end) actually handed over - start is
// after any mid-rune realignment, so it always names the first byte returned.
// Pair with RetainedStart: start > RetainedStart() means an earlier page
// exists.
func (o *OutputStore) Window(beforeBytes int64, maxBytes int) (buf []byte, start, end, total int64, err error) {
	if maxBytes < 0 {
		return nil, 0, 0, 0, fmt.Errorf("%w: maxBytes=%d", ErrInvalidLimit, maxBytes)
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	info, err := o.fs.Stat(o.path)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("jobstore: stat output: %w", err)
	}
	total = o.total
	start, end = WindowBounds(beforeBytes, int64(maxBytes), o.total, o.retainedStart)
	f, err := o.fs.Open(o.path)
	if err != nil {
		return nil, 0, 0, total, fmt.Errorf("jobstore: open output: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close output: %w", closeErr)
		}
	}()
	// Lifetime offsets map onto the retained file by dropping the evicted
	// prefix; the file's own size (not total) bounds the read because appends
	// land through this store before the stat above can see them.
	fileStart := start - o.retainedStart
	fileEnd := end - o.retainedStart
	if fileEnd > info.Size() {
		fileEnd = info.Size()
	}
	if _, err := f.Seek(fileStart, 0); err != nil {
		return nil, 0, 0, total, err
	}
	buf = make([]byte, fileEnd-fileStart)
	if len(buf) > 0 {
		if _, err := io.ReadFull(f, buf); err != nil {
			return nil, 0, 0, total, fmt.Errorf("jobstore: read output: %w", err)
		}
	}
	if fileStart > 0 {
		// Same mid-rune rule as Tail: the window only ever SHRINKS, and start
		// advances so it still names the first byte actually returned.
		before := len(buf)
		buf = runetrim.TrimLeadingPartial(buf)
		start += int64(before - len(buf))
	}
	return buf, start, end, total, nil
}

// Tail returns the last maxBytes bytes of the log, the total byte count, and
// whether the returned slice is a truncated tail of a larger log.
func (o *OutputStore) Tail(maxBytes int) (buf []byte, total int64, truncated bool, err error) {
	if maxBytes < 0 {
		return nil, 0, false, fmt.Errorf("%w: maxBytes=%d", ErrInvalidLimit, maxBytes)
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	info, err := o.fs.Stat(o.path)
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
	f, err := o.fs.Open(o.path)
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
	if start > 0 {
		// The window was cut at a raw byte offset, so it can open mid-rune. Drop the
		// dangling continuation bytes rather than reading further back: the window
		// SHRINKS, which keeps the caller's retained-start arithmetic (total minus the
		// bytes returned) naming the first byte actually handed over. Only our own cut
		// is realigned — at start 0 the first byte is the file's own, and binary output
		// keeps it.
		buf = runetrim.TrimLeadingPartial(buf)
	}
	return buf, total, truncated, nil
}

// Head returns the first maxBytes bytes of the retained log, the total byte
// count, and whether the returned slice is a truncated prefix of a larger log.
// When retention has pruned the lifetime prefix, the returned bytes start at the
// earliest still-retained byte (the start of the retained tail).
func (o *OutputStore) Head(maxBytes int) (buf []byte, total int64, truncated bool, err error) {
	if maxBytes < 0 {
		return nil, 0, false, fmt.Errorf("%w: maxBytes=%d", ErrInvalidLimit, maxBytes)
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	info, err := o.fs.Stat(o.path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("jobstore: stat output: %w", err)
	}
	retained := info.Size()
	total = o.total
	n := retained
	if n > int64(maxBytes) {
		n = int64(maxBytes)
		truncated = true
	}
	if o.retainedStart > 0 {
		truncated = true
	}
	f, err := o.fs.Open(o.path)
	if err != nil {
		return nil, total, truncated, fmt.Errorf("jobstore: open output: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close output: %w", closeErr)
		}
	}()
	buf = make([]byte, n)
	if len(buf) > 0 {
		if _, err := io.ReadFull(f, buf); err != nil {
			return nil, total, truncated, fmt.Errorf("jobstore: read output: %w", err)
		}
	}
	if n < retained {
		// The window was cut at a raw byte offset, so it can end mid-rune. Drop the
		// dangling partial rune: like the tail's start, the window only ever SHRINKS.
		// Only our own cut is realigned — when the window reaches the end of the file
		// the last byte is the file's own, and binary output keeps it.
		buf = runetrim.TrimTrailingPartial(buf)
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
	f, err := o.fs.Open(o.path)
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
	return grepFileLimitAtOpen(path, re, limitBytes, maxMatches, maxLineBytes, retainedStart, func(path string) (io.ReadCloser, error) { return os.Open(path) })
}

func grepFileLimitAtOpen(path string, re *regexp.Regexp, limitBytes int, maxMatches int, maxLineBytes int, retainedStart int64, open func(string) (io.ReadCloser, error)) (matches []Match, err error) {
	if limitBytes < 0 {
		return nil, fmt.Errorf("%w: limitBytes=%d", ErrInvalidLimit, limitBytes)
	}
	if limitBytes == 0 {
		return nil, nil
	}

	f, err := open(path)
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
	total, retainedStart, err = readOutputMetaForFile(afero.NewOsFs(), outputMetaPath(path), path, info.Size())
	if err != nil {
		return 0, 0, err
	}
	return total, retainedStart, nil
}

// RemoveOutputArtifacts removes an output file and the metadata files that
// describe it. Missing artifacts are ignored.
func RemoveOutputArtifacts(path string) error {
	return removeOutputArtifactsFs(afero.NewOsFs(), path)
}

func removeOutputArtifactsFs(fs afero.Fs, path string) error {
	for _, p := range outputArtifactPaths(path) {
		if err := fs.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("jobstore: remove output artifact: %w", err)
		}
	}
	return nil
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
	// The cap is a raw byte count, so it can cut inside a rune. Evict the orphaned
	// continuation bytes too: what survives here becomes the file's OWN first byte,
	// which readers pass through untouched rather than realigning — only a reader's
	// own window cut gets realigned.
	tail = runetrim.TrimLeadingPartial(tail)
	keep = int64(len(tail))
	retainedStart := o.total - keep
	if err := writeOutputMetaFileFsSync(o.fs, outputPendingMetaPath(o.metaPath), outputMeta{
		TotalBytes:     o.total,
		RetainedStart:  retainedStart,
		RetainedSHA256: outputBytesSHA256(tail),
	}, !o.disableSync); err != nil {
		return err
	}
	if err := o.f.Truncate(0); err != nil {
		return fmt.Errorf("jobstore: truncate output: %w", err)
	}
	if _, err := o.f.Seek(0, 0); err != nil {
		return fmt.Errorf("jobstore: seek output rewrite: %w", err)
	}
	if n, err := o.f.Write(tail); err != nil {
		return fmt.Errorf("jobstore: rewrite output tail: %w", err)
	} else if n != len(tail) {
		return fmt.Errorf("jobstore: rewrite output tail: %w", io.ErrShortWrite)
	}
	if err := o.f.Truncate(keep); err != nil {
		return fmt.Errorf("jobstore: trim output tail: %w", err)
	}
	if _, err := o.f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("jobstore: seek output eof: %w", err)
	}
	o.retainedStart = retainedStart
	return nil
}

func (o *OutputStore) persistMetaLocked() error {
	if o.metaPath == "" {
		return nil
	}
	meta, err := o.outputMetaLocked()
	if err != nil {
		return err
	}
	if err := writeOutputMetaFileFsSync(o.fs, o.metaPath, meta, !o.disableSync); err != nil {
		return err
	}
	if err := o.fs.Remove(outputPendingMetaPath(o.metaPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("jobstore: remove pending output metadata: %w", err)
	}
	return nil
}

func (o *OutputStore) outputMetaLocked() (outputMeta, error) {
	if !o.disableSync {
		if err := o.f.Sync(); err != nil {
			return outputMeta{}, fmt.Errorf("jobstore: sync output before metadata: %w", err)
		}
	}
	meta := outputMeta{
		TotalBytes:    o.total,
		RetainedStart: o.retainedStart,
	}
	hash, err := outputFileSHA256(o.fs, o.path)
	if err != nil {
		return outputMeta{}, err
	}
	meta.RetainedSHA256 = hash
	return meta, nil
}

// writeOutputMetaFile atomically writes meta on the OS filesystem.
// writeOutputMetaFileFs carries the injectable seam; this preserves the
// os-backed signature the package's callers and tests already use.
func writeOutputMetaFile(path string, meta outputMeta) error {
	return writeOutputMetaFileFs(afero.NewOsFs(), path, meta)
}

func writeOutputMetaFileFs(fs afero.Fs, path string, meta outputMeta) error {
	return writeOutputMetaFileFsSync(fs, path, meta, true)
}

func writeOutputMetaFileFsSync(fs afero.Fs, path string, meta outputMeta, syncWrites bool) error {
	return writeOutputMetaFileFsSyncMarshal(fs, path, meta, syncWrites, json.Marshal)
}

func writeOutputMetaFileFsSyncMarshal(fs afero.Fs, path string, meta outputMeta, syncWrites bool, marshal func(any) ([]byte, error)) error {
	if path == "" {
		return nil
	}
	b, err := marshal(meta)
	if err != nil {
		return fmt.Errorf("jobstore: marshal output metadata: %w", err)
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	f, err := fs.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("jobstore: open output metadata: %w", err)
	}
	if err := writeOutputMetaBytes(f, b, syncWrites); err != nil {
		_ = fs.Remove(tmp)
		return err
	}
	if err := fs.Rename(tmp, path); err != nil {
		_ = fs.Remove(tmp)
		return fmt.Errorf("jobstore: replace output metadata: %w", err)
	}
	if syncWrites {
		if err := syncParentDir(fs, path); err != nil {
			return err
		}
	}
	return nil
}

func writeOutputMetaBytes(f afero.File, b []byte, syncWrites bool) error {
	if n, err := f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("jobstore: write output metadata: %w", err)
	} else if n != len(b) {
		_ = f.Close()
		return fmt.Errorf("jobstore: write output metadata: %w", io.ErrShortWrite)
	}
	if syncWrites {
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return fmt.Errorf("jobstore: sync output metadata: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("jobstore: close output metadata: %w", err)
	}
	return nil
}

func syncParentDir(fs afero.Fs, path string) error {
	dir, err := fs.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("jobstore: open output metadata directory: %w", err)
	}
	defer func() {
		_ = dir.Close()
	}()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("jobstore: sync output metadata directory: %w", err)
	}
	return nil
}

func outputMetaPath(path string) string {
	return path + ".meta.json"
}

func outputPendingMetaPath(metaPath string) string {
	return metaPath + ".pending"
}

func readOutputMetaForFile(fs afero.Fs, path string, outputPath string, retained int64) (total int64, retainedStart int64, err error) {
	pending, ok, err := readValidPendingOutputMeta(fs, outputPendingMetaPath(path), path, outputPath, retained)
	if err != nil {
		return 0, 0, err
	}
	if ok {
		return pending.TotalBytes, pending.RetainedStart, nil
	}
	meta, ok, err := readValidOutputMetaFs(fs, path, outputPath, retained)
	if ok {
		return meta.TotalBytes, meta.RetainedStart, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return retained, 0, nil
}

func readOutputMeta(fs afero.Fs, path string) (outputMeta, bool, error) {
	b, err := afero.ReadFile(fs, path)
	if errors.Is(err, os.ErrNotExist) {
		return outputMeta{}, false, nil
	}
	if err != nil {
		return outputMeta{}, false, fmt.Errorf("jobstore: read output metadata: %w", err)
	}
	var meta outputMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return outputMeta{}, false, fmt.Errorf("jobstore: parse output metadata: %w", err)
	}
	return meta, true, nil
}

func readValidPendingOutputMeta(fs afero.Fs, path string, finalMetaPath string, outputPath string, retained int64) (outputMeta, bool, error) {
	meta, ok, err := readOutputMeta(fs, path)
	if err != nil || !ok {
		return outputMeta{}, ok, err
	}
	metaRetained, err := outputMetaRetainedBytes(meta)
	if err != nil {
		return outputMeta{}, false, err
	}
	if metaRetained < retained {
		if ok, err := outputFileHasSuffixSHA256(fs, outputPath, retained-metaRetained, metaRetained, meta.RetainedSHA256); err != nil {
			return outputMeta{}, false, err
		} else if !ok {
			return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
		}
		if meta.TotalBytes < retained {
			return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
		}
		finalMeta, ok, err := readOutputMeta(fs, finalMetaPath)
		if err != nil {
			return outputMeta{}, false, err
		}
		if !ok {
			return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
		}
		prefixLen := retained - metaRetained
		finalRetained, err := outputMetaRetainedBytes(finalMeta)
		if err != nil {
			return outputMeta{}, false, err
		}
		if finalRetained > retained ||
			meta.RetainedStart != finalMeta.RetainedStart+prefixLen ||
			meta.TotalBytes-retained != finalMeta.RetainedStart {
			return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
		}
		if ok, err := outputFileHasPrefixSHA256(fs, outputPath, finalRetained, finalMeta.RetainedSHA256); err != nil {
			return outputMeta{}, false, err
		} else if !ok {
			return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
		}
		hash, err := outputFileSHA256(fs, outputPath)
		if err != nil {
			return outputMeta{}, false, err
		}
		meta.RetainedStart = meta.TotalBytes - retained
		meta.RetainedSHA256 = hash
		return meta, true, nil
	}
	if metaRetained != retained {
		return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
	}
	hash, err := outputFileSHA256(fs, outputPath)
	if err != nil {
		return outputMeta{}, false, err
	}
	if meta.RetainedSHA256 != hash {
		return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
	}
	return meta, true, nil
}

// readValidOutputMeta validates final output metadata on the OS filesystem.
// readValidOutputMetaFs carries the injectable seam; this preserves the
// os-backed signature the package's tests already use.
func readValidOutputMeta(path string, outputPath string, retained int64) (outputMeta, bool, error) {
	return readValidOutputMetaFs(afero.NewOsFs(), path, outputPath, retained)
}

func readValidOutputMetaFs(fs afero.Fs, path string, outputPath string, retained int64) (outputMeta, bool, error) {
	meta, ok, err := readOutputMeta(fs, path)
	if err != nil || !ok {
		return outputMeta{}, ok, err
	}
	metaRetained, err := outputMetaRetainedBytes(meta)
	if err != nil {
		return outputMeta{}, false, err
	}
	if metaRetained < retained {
		if ok, err := outputFileHasPrefixSHA256(fs, outputPath, metaRetained, meta.RetainedSHA256); err != nil {
			return outputMeta{}, false, err
		} else if !ok {
			return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
		}
		hash, err := outputFileSHA256(fs, outputPath)
		if err != nil {
			return outputMeta{}, false, err
		}
		meta.TotalBytes = meta.RetainedStart + retained
		meta.RetainedSHA256 = hash
		return meta, true, nil
	}
	if metaRetained != retained {
		return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
	}
	hash, err := outputFileSHA256(fs, outputPath)
	if err != nil {
		return outputMeta{}, false, err
	}
	if meta.RetainedSHA256 != hash {
		return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
	}
	return meta, true, nil
}

func outputMetaRetainedBytes(meta outputMeta) (int64, error) {
	if meta.RetainedStart < 0 || meta.TotalBytes < meta.RetainedStart {
		return 0, errors.New("jobstore: output metadata does not match retained output")
	}
	return meta.TotalBytes - meta.RetainedStart, nil
}

func outputFileHasPrefixSHA256(fs afero.Fs, path string, n int64, want string) (bool, error) {
	f, err := fs.Open(path)
	if err != nil {
		return false, fmt.Errorf("jobstore: open output for metadata hash: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()
	h := sha256.New()
	if _, err := io.CopyN(h, f, n); err != nil {
		return false, fmt.Errorf("jobstore: hash output metadata: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)) == want, nil
}

func outputFileHasSuffixSHA256(fs afero.Fs, path string, start int64, n int64, want string) (bool, error) {
	f, err := fs.Open(path)
	if err != nil {
		return false, fmt.Errorf("jobstore: open output for metadata hash: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()
	if _, err := f.Seek(start, 0); err != nil {
		return false, fmt.Errorf("jobstore: seek output for metadata hash: %w", err)
	}
	h := sha256.New()
	if _, err := io.CopyN(h, f, n); err != nil {
		return false, fmt.Errorf("jobstore: hash output metadata: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)) == want, nil
}

func outputFileSHA256(fs afero.Fs, path string) (string, error) {
	f, err := fs.Open(path)
	if err != nil {
		return "", fmt.Errorf("jobstore: open output for metadata hash: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("jobstore: hash output metadata: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func outputBytesSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
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
	lineCap := min(maxLineBytes, 4096)
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
				contentLen := logicalLineContentLen(line, frag)
				if contentLen > maxLineBytes {
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
					if !overlong && len(line) > maxLineBytes {
						overlong = true
					}
					appendGrepLine(&matches, re, lineAt, line, &budget, maxMatches, overlong)
				}
				break
			}
			return nil, fmt.Errorf("jobstore: read output line: %w", err)
		}
	}
	return matches, nil
}

func logicalLineContentLen(line []byte, frag []byte) int {
	n := len(line) + len(frag)
	if len(frag) == 0 {
		return n
	}
	if frag[len(frag)-1] != '\n' {
		if frag[len(frag)-1] == '\r' {
			return n - 1
		}
		return n
	}
	n--
	if len(frag) > 1 && frag[len(frag)-2] == '\r' {
		return n - 1
	}
	if len(frag) == 1 && len(line) > 0 && line[len(line)-1] == '\r' {
		return n - 1
	}
	return n
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
