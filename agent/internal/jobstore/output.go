package jobstore

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/runetrim"
	"primeradiant.com/evener/execsupport/fsatomic"
)

// ErrOutputPruned is returned by output reads when the durable record remains
// but the bytes were pruned by retention policy. Callers translate this to the
// model-facing output_unavailable / retention_pruned signal in a later phase.
var ErrOutputPruned = errors.New("jobstore: output pruned")

// ErrInvalidLimit is returned when an output read limit is negative.
var ErrInvalidLimit = errors.New("jobstore: invalid limit")

// ErrInvalidOffset is returned when an output read offset is negative or past
// the lifetime end of the output stream.
var ErrInvalidOffset = errors.New("jobstore: invalid offset")

// errOutputPendingHandoff reports the prune protocol's transient handoff:
// pending metadata names a newer retained tail while the old final metadata
// and output are still published. Snapshot readers translate it to a
// concurrent change; durable reopeners surface it to their caller.
var errOutputPendingHandoff = errors.New("jobstore: output pending metadata handoff")

// outputMetaSyncInterval paces an append's barriers. Append publishes metadata
// content on every write, because readers of a running job take the log's
// lifetime counters from it, but it fsyncs the log and that metadata only this
// often. A power crash can then lose the log's unsynced tail while the sidecar
// rename survives, so metadata records the log as of the last barrier and
// readers fall back to it rather than refusing the job (Jesse's 2026-11-16
// ruling: lose the tail, never corrupt).
const outputMetaSyncInterval = time.Second

// SearchOptions bounds a retained-output line search. All offsets are lifetime
// byte offsets, even when the retained file begins after offset zero.
type SearchOptions struct {
	StartOffset        int64
	MaxMatches         int
	MaxSerializedBytes int
	ContextLines       int
	SkipPartialPrefix  bool
	DeferEOFFragment   bool
}

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
	// retainedStartPartial says the first retained byte continues a line whose
	// prefix was pruned. It is persisted so durable readers need not guess.
	retainedStartPartial bool
	// visibleStart and visiblePartial are what readers see: the first byte of
	// the last capBytes, which can sit past retainedStart while the file still
	// holds older bytes awaiting compaction.
	visibleStart   int64
	visiblePartial bool
	// retainedHash is the running SHA-256 of the retained file, fed by each
	// append and re-seeded at compaction, so writing the metadata checksum
	// never re-reads the file.
	retainedHash hash.Hash
	disableSync  bool
	// metaSyncInterval paces append barriers; lastMetaSync anchors that pace.
	metaSyncInterval time.Duration
	lastMetaSync     time.Time
	// durableFileSize and durableSHA256 are the retained log as of the last
	// barrier. A reader that finds the log shorter than its metadata uses them
	// to tell a lost unsynced tail from a file something replaced.
	durableFileSize int64
	durableSHA256   string
	// synced says the log's durable anchor describes every byte appended.
	synced bool
	closed bool
}

type outputMeta struct {
	TotalBytes           int64  `json:"total_bytes"`
	RetainedStart        int64  `json:"retained_start"`
	RetainedStartPartial *bool  `json:"retained_start_partial,omitempty"`
	RetainedSHA256       string `json:"retained_sha256"`
	// DurableFileSize and DurableSHA256 are the retained log as of the last
	// barrier that made this metadata durable, so a reader can accept a log
	// whose unsynced tail a power crash lost. Sidecars written before these
	// fields existed omit them, and a reader that finds them absent keeps the
	// strict byte-count check.
	DurableFileSize *int64 `json:"durable_file_size,omitempty"`
	DurableSHA256   string `json:"durable_sha256,omitempty"`
	// VisibleStart, when set, is the first lifetime offset readers may see. It
	// is past RetainedStart when the file still holds bytes older than the
	// retention cap that compaction has not yet dropped.
	VisibleStart        *int64 `json:"visible_start,omitempty"`
	VisibleStartPartial *bool  `json:"visible_start_partial,omitempty"`
}

// outputView places a retained output file in lifetime offsets: fileStart is
// the offset of the file's first byte, and readers see only from visibleStart
// on, which is never before fileStart. visiblePartial says the first visible
// byte continues a line that began before it.
type outputView struct {
	total          int64
	fileStart      int64
	visibleStart   int64
	visiblePartial bool
}

// visibleOffset is the file offset of the first visible byte.
func (v outputView) visibleOffset() int64 {
	return v.visibleStart - v.fileStart
}

// verifiedLength is the number of file bytes the view vouches for: the length of
// the retained window its metadata describes. A power crash can leave the log
// physically longer than that window while its contents past the window are torn
// or zeroed, so readers bound their reads here rather than by the file's size.
// Presenting the excess would hand a reader unverified bytes as output (Jesse's
// 2026-11-16 ruling: lose the tail, never corrupt).
func (v outputView) verifiedLength() int64 {
	return v.total - v.fileStart
}

func outputViewOf(meta outputMeta) outputView {
	view := outputView{total: meta.TotalBytes, fileStart: meta.RetainedStart, visibleStart: meta.RetainedStart, visiblePartial: outputMetaRetainedStartPartial(meta)}
	if meta.VisibleStart != nil && *meta.VisibleStart > meta.RetainedStart && *meta.VisibleStart <= meta.TotalBytes {
		view.visibleStart = *meta.VisibleStart
		view.visiblePartial = meta.VisibleStartPartial == nil || *meta.VisibleStartPartial
	}
	return view
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
	view := outputView{total: total}
	retainedStartPartial := false
	if !created {
		meta, err := readOutputMetaRecordForFile(fs, metaPath, path, info.Size())
		if err != nil {
			cleanupCreated()
			return nil, err
		}
		view, retainedStartPartial = outputViewOf(meta), outputMetaRetainedStartPartial(meta)
		// A writable reopen becomes the file's only writer, so it must not carry
		// a suffix the sidecar never vouched for: hashing the file as it stands
		// would adopt a torn suffix into the new sidecar, and appending would
		// build on top of it. Drop it now, while nothing reads the file yet, so
		// the log this store owns is exactly the bytes its metadata describes
		// (Jesse's 2026-11-16 ruling: lose the tail, never corrupt). The retained
		// window ends where the file does on every intact reopen, so this is a
		// no-op except after a crash that left the log longer than its metadata.
		if verified := view.verifiedLength(); verified < info.Size() {
			if err := f.Truncate(verified); err != nil {
				cleanupCreated()
				return nil, fmt.Errorf("jobstore: truncate output to metadata: %w", err)
			}
		}
	}
	o := &OutputStore{path: path, metaPath: metaPath, fs: fs, f: f, capBytes: capBytes, total: view.total, retainedStart: view.fileStart, retainedStartPartial: retainedStartPartial, visibleStart: view.visibleStart, visiblePartial: view.visiblePartial, disableSync: disableSync}
	o.metaSyncInterval = outputMetaSyncInterval
	if o.retainedHash, err = outputFileHash(fs, path); err != nil {
		cleanupCreated()
		return nil, err
	}
	if err := o.pruneLocked(); err != nil {
		cleanupCreated()
		return nil, err
	}
	if err := o.refreshVisibleLocked(); err != nil {
		cleanupCreated()
		return nil, err
	}
	if err := o.persistMetaLocked(); err != nil {
		cleanupCreated()
		return nil, err
	}
	o.lastMetaSync = time.Now()
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
		path + ".tmp",
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
	if o.closed {
		return 0, fmt.Errorf("jobstore: append output: %w", os.ErrClosed)
	}
	n, err := o.f.Write(b)
	o.total += int64(n)
	if n > 0 {
		o.synced = false
	}
	if o.retainedHash != nil {
		o.retainedHash.Write(b[:n])
	}
	if err != nil {
		return n, fmt.Errorf("jobstore: append output: %w", err)
	}
	if err := o.pruneLocked(); err != nil {
		return n, err
	}
	if err := o.refreshVisibleLocked(); err != nil {
		return n, err
	}
	// Publish metadata content so a reader of the running job sees the log's
	// current counters, then pay the barriers at most once per interval.
	if err := o.writeMetaLocked(false); err != nil {
		return n, err
	}
	if err := o.syncMetaTickLocked(); err != nil {
		return n, err
	}
	return n, nil
}

// writeMetaLocked replaces the metadata file. synced also fsyncs the log before
// the sidecar that describes it, so metadata is never durable ahead of the log
// bytes whose anchor it carries.
func (o *OutputStore) writeMetaLocked(synced bool) error {
	if o.metaPath == "" {
		return nil
	}
	syncWrites := synced && !o.disableSync
	var meta outputMeta
	var err error
	if syncWrites {
		meta, err = o.outputMetaLocked()
	} else {
		meta, err = o.outputMetaContentLocked()
	}
	if err != nil {
		return err
	}
	if syncWrites {
		// This write follows the log's fsync, so it names the log as it stands:
		// the sidecar and its anchor describe the same bytes.
		meta.DurableFileSize, meta.DurableSHA256 = new(o.total-o.retainedStart), meta.RetainedSHA256
	} else if o.hasDurableAnchor() {
		meta.DurableFileSize, meta.DurableSHA256 = new(o.durableFileSize), o.durableSHA256
	} else {
		meta.DurableFileSize, meta.DurableSHA256 = nil, ""
	}
	// The bytes are synced before every rename, not only at a barrier: a rename
	// that survives a power loss while the file's contents do not would replace
	// durable metadata with an empty or malformed file. The parent-directory
	// barrier is the barrier's job alone — a rewrite over an existing name only
	// has to make ITS contents durable, and the next barrier commits the rename
	// along with the log (#6402).
	if err := writeOutputMetaFileFsSyncDir(o.fs, o.metaPath, meta, !o.disableSync, syncWrites); err != nil {
		return err
	}
	if syncWrites {
		o.durableFileSize = o.total - o.retainedStart
		o.durableSHA256 = meta.RetainedSHA256
		o.synced = true
		o.lastMetaSync = time.Now()
	}
	if err := o.fs.Remove(outputPendingMetaPath(o.metaPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("jobstore: remove pending output metadata: %w", err)
	}
	return nil
}

// syncMetaTickLocked pays the log and metadata barriers at most once per
// interval. A compaction writes its own pending journal, so batching here never
// leaves a prune unrecorded.
func (o *OutputStore) syncMetaTickLocked() error {
	if o.disableSync || o.synced {
		return nil
	}
	if !o.lastMetaSync.IsZero() && time.Since(o.lastMetaSync) < o.metaSyncInterval {
		return nil
	}
	return o.persistMetaLocked()
}

// Flush pays the barriers now instead of at the next tick. Job finalization
// calls it before recording a terminal output byte count, so a durable record
// never names bytes the durable log lacks.
func (o *OutputStore) Flush() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	return o.flushLocked()
}

func (o *OutputStore) flushLocked() error {
	if o.synced || o.metaPath == "" {
		return nil
	}
	return o.persistMetaLocked()
}

// refreshVisibleLocked moves the visible start forward to the first byte of
// the last capBytes when the file still holds older bytes; it never moves
// back. The cap is a raw byte count, so like compaction it skips orphaned
// UTF-8 continuation bytes, and it reads the byte before the visible start to
// tell whether a line continues there.
func (o *OutputStore) refreshVisibleLocked() error {
	if o.visibleStart <= o.retainedStart {
		o.visibleStart, o.visiblePartial = o.retainedStart, o.retainedStartPartial
	}
	if o.capBytes <= 0 || o.total-o.capBytes <= o.visibleStart {
		return nil
	}
	start := o.total - o.capBytes
	edge := make([]byte, utf8.UTFMax)
	n, err := o.f.ReadAt(edge, start-o.retainedStart-1)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("jobstore: read output visible boundary: %w", err)
	}
	if n == 0 {
		// The file is shorter than total says it should be.
		return errors.New("jobstore: output visible boundary is past the end of the file")
	}
	previous, rest := edge[0], edge[1:n]
	if dropped := len(rest) - len(runetrim.TrimLeadingPartial(rest)); dropped > 0 {
		start += int64(dropped)
		previous = rest[dropped-1]
	}
	o.visibleStart, o.visiblePartial = start, previous != '\n'
	return nil
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

// RetainedStart returns the first lifetime offset readers may see: the number
// of bytes the retention cap hides off the head (0 when nothing has been
// dropped).
func (o *OutputStore) RetainedStart() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.visibleStart
}

// RetainedStartPartial reports whether the first visible byte continues a
// line that began before RetainedStart.
func (o *OutputStore) RetainedStartPartial() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.visiblePartial
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
	start = max(start, earliest)
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
	start, end = WindowBounds(beforeBytes, int64(maxBytes), o.total, o.visibleStart)
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
	fileEnd = min(fileEnd, info.Size())
	if _, err := f.Seek(fileStart, 0); err != nil {
		return nil, 0, 0, total, err
	}
	buf = make([]byte, fileEnd-fileStart)
	if len(buf) > 0 {
		if _, err := io.ReadFull(f, buf); err != nil {
			return nil, 0, 0, total, fmt.Errorf("jobstore: read output: %w", err)
		}
	}
	if start > o.visibleStart {
		// Same mid-rune rule as Tail: the window only ever SHRINKS, and start
		// advances so it still names the first byte actually returned.
		before := len(buf)
		buf = runetrim.TrimLeadingPartial(buf)
		start += int64(before - len(buf))
	}
	return buf, start, end, total, nil
}

// ReadWindow returns a raw forward window beginning at lifetime offset. Unlike
// Window, it does not realign UTF-8 boundaries: callers can concatenate
// successive End offsets to reconstruct the retained bytes exactly. The
// content and all metadata are captured while holding the store mutex.
func (o *OutputStore) ReadWindow(offset int64, maxBytes int) (snapshot OutputWindowSnapshot, err error) {
	if maxBytes < 0 {
		return OutputWindowSnapshot{}, fmt.Errorf("%w: maxBytes=%d", ErrInvalidLimit, maxBytes)
	}
	if offset < 0 {
		return OutputWindowSnapshot{}, fmt.Errorf("%w: offset=%d", ErrInvalidOffset, offset)
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	snapshot.TotalBytes = o.total
	snapshot.RetainedStart = o.visibleStart
	snapshot.RetainedStartPartial = o.visiblePartial
	snapshot.Start = offset
	snapshot.End = offset
	if offset < o.visibleStart {
		return snapshot, fmt.Errorf("%w: offset=%d first_available=%d", ErrOutputPruned, offset, o.visibleStart)
	}
	if offset > o.total {
		return snapshot, fmt.Errorf("%w: offset=%d total=%d", ErrInvalidOffset, offset, o.total)
	}

	end := addWindowLimit(offset, maxBytes, o.total)
	return o.readRangeLocked(offset, end)
}

// ReadPage returns the raw page ending at beforeBytes, or at the current total
// when beforeBytes is nil. Selection and reading share the store mutex.
func (o *OutputStore) ReadPage(beforeBytes *int64, maxBytes int) (OutputWindowSnapshot, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	start, end, err := outputPageBounds(beforeBytes, maxBytes, o.total, o.visibleStart)
	if err != nil {
		return OutputWindowSnapshot{TotalBytes: o.total, RetainedStart: o.visibleStart, RetainedStartPartial: o.visiblePartial}, err
	}
	return o.readRangeLocked(start, end)
}

func outputPageBounds(beforeBytes *int64, maxBytes int, totalBytes, retainedStart int64) (start, end int64, err error) {
	if maxBytes <= 0 {
		return 0, 0, fmt.Errorf("%w: maxBytes=%d", ErrInvalidOffset, maxBytes)
	}
	end = totalBytes
	if beforeBytes != nil {
		end = *beforeBytes
	}
	if end < 0 || end > totalBytes {
		return 0, 0, fmt.Errorf("%w: beforeBytes=%d total=%d", ErrInvalidOffset, end, totalBytes)
	}
	if end < retainedStart {
		return 0, 0, fmt.Errorf("%w: beforeBytes=%d first_available=%d", ErrOutputPruned, end, retainedStart)
	}
	start = retainedStart
	if end-retainedStart > int64(maxBytes) {
		start = end - int64(maxBytes)
	}
	return start, end, nil
}

func outputWindowBounds(offset int64, maxBytes int, totalBytes, retainedStart int64) (start, end int64, err error) {
	if offset < retainedStart {
		return offset, offset, fmt.Errorf("%w: offset=%d first_available=%d", ErrOutputPruned, offset, retainedStart)
	}
	if offset > totalBytes {
		return offset, offset, fmt.Errorf("%w: offset=%d total=%d", ErrInvalidOffset, offset, totalBytes)
	}
	return offset, addWindowLimit(offset, maxBytes, totalBytes), nil
}

func (o *OutputStore) readRangeLocked(offset, end int64) (snapshot OutputWindowSnapshot, err error) {
	snapshot.TotalBytes = o.total
	snapshot.RetainedStart = o.visibleStart
	snapshot.RetainedStartPartial = o.visiblePartial
	snapshot.Start = offset
	snapshot.End = end
	snapshot.Truncated = o.visibleStart > 0 || offset > o.visibleStart || end < o.total
	if end == offset {
		return snapshot, nil
	}
	info, err := o.fs.Stat(o.path)
	if err != nil {
		return OutputWindowSnapshot{}, fmt.Errorf("jobstore: stat output: %w", err)
	}
	fileStart := offset - o.retainedStart
	fileEnd := end - o.retainedStart
	if fileStart < 0 || fileEnd < fileStart || fileEnd > info.Size() {
		return OutputWindowSnapshot{}, errors.New("jobstore: retained output coordinates do not match output file")
	}
	f, err := o.fs.Open(o.path)
	if err != nil {
		return OutputWindowSnapshot{}, fmt.Errorf("jobstore: open output: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close output: %w", closeErr)
		}
	}()
	if fileStart > 0 {
		if _, err := f.Seek(fileStart, io.SeekStart); err != nil {
			return OutputWindowSnapshot{}, fmt.Errorf("jobstore: seek output: %w", err)
		}
	}
	snapshot.Content = make([]byte, int(fileEnd-fileStart))
	if _, err := io.ReadFull(f, snapshot.Content); err != nil {
		return OutputWindowSnapshot{}, fmt.Errorf("jobstore: read output: %w", err)
	}
	return snapshot, nil
}

func addWindowLimit(offset int64, maxBytes int, total int64) int64 {
	if maxBytes == 0 || offset == total {
		return offset
	}
	limit := int64(maxBytes)
	if limit > total-offset {
		return total
	}
	return offset + limit
}

// Tail returns the last maxBytes bytes of the log, the total byte count, and
// whether the returned slice is a truncated tail of a larger log.
func (o *OutputStore) Tail(maxBytes int) (buf []byte, total int64, truncated bool, err error) {
	return o.readEdge(maxBytes, false)
}

// Head returns the first maxBytes bytes of the retained log, the total byte
// count, and whether the returned slice is a truncated prefix of a larger log.
// When retention has pruned the lifetime prefix, the returned bytes start at the
// earliest still-retained byte (the start of the retained tail).
func (o *OutputStore) Head(maxBytes int) (buf []byte, total int64, truncated bool, err error) {
	return o.readEdge(maxBytes, true)
}

// readEdge reads at most maxBytes of the visible output from its head or its
// tail. The window is cut at a raw byte offset, so it can open or end
// mid-rune; trimOutputSnapshotWindow drops the dangling bytes, so the window
// only ever SHRINKS, which keeps the caller's retained-start arithmetic (total
// minus the bytes returned) naming the first byte actually handed over.
func (o *OutputStore) readEdge(maxBytes int, fromHead bool) (buf []byte, total int64, truncated bool, err error) {
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
	visibleAt := min(max(o.visibleOffsetLocked(), 0), retained)
	start, n := outputSnapshotWindow(retained, visibleAt, maxBytes, fromHead)
	truncated = o.visibleStart > 0 || n < retained-visibleAt
	f, err := o.fs.Open(o.path)
	if err != nil {
		return nil, total, truncated, fmt.Errorf("jobstore: open output: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close output: %w", closeErr)
		}
	}()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, total, truncated, fmt.Errorf("jobstore: seek output: %w", err)
	}
	buf = make([]byte, n)
	if len(buf) > 0 {
		if _, err := io.ReadFull(f, buf); err != nil {
			return nil, total, truncated, fmt.Errorf("jobstore: read output: %w", err)
		}
	}
	return trimOutputSnapshotWindow(buf, retained, visibleAt, start, fromHead), total, truncated, nil
}

// visibleOffsetLocked is the file offset of the first visible byte.
func (o *OutputStore) visibleOffsetLocked() int64 {
	return o.visibleStart - o.retainedStart
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
	if scan, err := grepLimitScans(limitBytes); !scan {
		return nil, err
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
	if _, err := f.Seek(o.visibleOffsetLocked(), io.SeekStart); err != nil {
		return nil, fmt.Errorf("jobstore: seek output: %w", err)
	}
	// 64 KiB buffer (matching the scanner seed in store.go) cuts read
	// iterations ~16x on MB-scale logs. Buffer size is not wire-visible.
	matches, err = grepReaderLimit(bufio.NewReaderSize(f, 64*1024), re, limitBytes, maxMatches, maxLineBytes)
	if err != nil {
		return nil, err
	}
	shiftMatches(matches, o.visibleStart)
	return matches, nil
}

// grepLimitScans reports whether a grep with limitBytes scans anything: an
// empty budget scans nothing, and a negative one is an error.
func grepLimitScans(limitBytes int) (bool, error) {
	if limitBytes < 0 {
		return false, fmt.Errorf("%w: limitBytes=%d", ErrInvalidLimit, limitBytes)
	}
	return limitBytes > 0, nil
}

// CommitOutput pays a recovered output file's barriers and commits its metadata,
// then reports the durable lifetime byte count. Recovery calls it before
// recording a terminal byte count: batched appends can still sit in the page
// cache after a process-only crash, and a recorded count the log cannot yet
// back makes checkOutputTotalForRecord reject the output forever after a later
// power loss.
func CommitOutput(path string) (int64, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, err
	}
	store, err := OpenOutput(path, 0)
	if err != nil {
		return 0, err
	}
	if err := store.Flush(); err != nil {
		_ = store.Close()
		return 0, err
	}
	if err := store.Close(); err != nil {
		return 0, err
	}
	total, _, err := OutputFileStats(path)
	return total, err
}

// OutputFileStats returns durable lifetime output metadata for a closed output
// file: the total bytes ever written and the first lifetime offset readers may
// see. If no metadata exists, the file is treated as unpruned.
func OutputFileStats(path string) (total int64, visibleStart int64, err error) {
	view, err := readOutputFileView(path)
	if err != nil {
		return 0, 0, err
	}
	return view.total, view.visibleStart, nil
}

func readOutputFileView(path string) (outputView, error) {
	info, err := os.Stat(path)
	if err != nil {
		return outputView{}, fmt.Errorf("jobstore: stat output: %w", err)
	}
	return readOutputViewForFile(afero.NewOsFs(), outputMetaPath(path), path, info.Size())
}

// GrepOutputFileLimitFromFile greps the visible bytes of an already-open
// closed output file, reporting lifetime offsets, with the same bounded line
// handling as OutputStore.GrepLimitLineBytes. The caller owns f and must close
// it; path locates the metadata sidecars. checkTotal sees the file's lifetime
// total before any scanning and can refuse it. A file that changes between
// opening and reading its metadata returns ErrOutputChangedDuringRead; callers
// that retry must reopen the output first.
func GrepOutputFileLimitFromFile(path string, f *os.File, re *regexp.Regexp, limitBytes int, maxMatches int, maxLineBytes int, checkTotal func(total int64) error) ([]Match, error) {
	if scan, err := grepLimitScans(limitBytes); !scan {
		return nil, err
	}
	if f == nil {
		return nil, errors.New("jobstore: output file is nil")
	}
	view, err := readOpenOutputFileView(f, path, checkTotal)
	if err != nil {
		return nil, err
	}
	// Scan only the bytes the metadata vouches for. A crash can leave the log
	// holding a torn suffix past that window, and those bytes are not output.
	verified := view.verifiedLength() - view.visibleOffset()
	matches, err := grepReaderLimit(bufio.NewReaderSize(io.LimitReader(f, verified), 64*1024), re, limitBytes, maxMatches, maxLineBytes)
	if err != nil {
		return nil, err
	}
	shiftMatches(matches, view.visibleStart)
	return matches, nil
}

// readOpenOutputFileView reads the view of the open output file f, lets
// checkTotal refuse it, and seeks f to its visible start. It returns
// ErrOutputChangedDuringRead when path no longer names f, or f's size no
// longer matches the view.
func readOpenOutputFileView(f *os.File, path string, checkTotal func(total int64) error) (outputView, error) {
	opened, err := f.Stat()
	if err != nil {
		return outputView{}, fmt.Errorf("jobstore: stat output: %w", err)
	}
	openedSize := opened.Size()
	view, err := readOutputViewForFile(afero.NewOsFs(), outputMetaPath(path), path, openedSize)
	if err != nil {
		return outputView{}, err
	}
	if err := checkTotal(view.total); err != nil {
		return outputView{}, err
	}
	info, err := f.Stat()
	if err != nil {
		return outputView{}, fmt.Errorf("jobstore: stat output: %w", err)
	}
	current, err := os.Stat(path)
	// Fence on the size this read observed, not on the verified window: the file
	// may legitimately hold a torn suffix past that window, so a stable longer
	// file is not a change. Growth or shrinkage since the view is, and the
	// caller's bounded read keeps the suffix out of the scan.
	if err != nil || !os.SameFile(info, current) || info.Size() != openedSize || openedSize < view.verifiedLength() {
		return outputView{}, ErrOutputChangedDuringRead
	}
	if _, err := f.Seek(view.visibleOffset(), io.SeekStart); err != nil {
		return outputView{}, fmt.Errorf("jobstore: seek output: %w", err)
	}
	return view, nil
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
	// Compaction rewrites and fsyncs the whole retained tail, so it waits until
	// the file holds twice the cap and then drops back to the cap: one rewrite
	// per capBytes written rather than one per append (#3808). Until then the
	// file keeps older bytes, and refreshVisibleLocked hides them from readers.
	if info.Size() <= 2*o.capBytes {
		o.retainedStart = o.total - info.Size()
		return nil
	}
	keep := o.capBytes
	tail := make([]byte, keep)
	// The log is synced before the journal is published below. The journal
	// describes a tail this file only holds once the log reaches disk, so a crash
	// between publishing the journal and replacing the file must find the bytes
	// the journal names: otherwise the pending sidecar describes more than the
	// log holds, carries no anchor to fall back to, and recovery refuses output
	// that survived intact.
	if !o.disableSync {
		if err := o.f.Sync(); err != nil {
			return fmt.Errorf("jobstore: sync output before compaction journal: %w", err)
		}
	}
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
	rawTail := tail
	tail = runetrim.TrimLeadingPartial(rawTail)
	droppedLeading := len(rawTail) - len(tail)
	keep = int64(len(tail))
	retainedStart := o.total - keep
	retainedStartPartial := false
	if keep > 0 && retainedStart > 0 {
		var previous byte
		if droppedLeading > 0 {
			previous = rawTail[droppedLeading-1]
		} else {
			if _, err := o.f.Seek(info.Size()-int64(len(rawTail))-1, io.SeekStart); err != nil {
				return fmt.Errorf("jobstore: seek output prune boundary: %w", err)
			}
			var previousByte [1]byte
			if _, err := io.ReadFull(o.f, previousByte[:]); err != nil {
				return fmt.Errorf("jobstore: read output prune boundary: %w", err)
			}
			previous = previousByte[0]
		}
		retainedStartPartial = previous != '\n'
	}
	if err := writeOutputMetaFileFsSync(o.fs, outputPendingMetaPath(o.metaPath), outputMeta{
		TotalBytes:           o.total,
		RetainedStart:        retainedStart,
		RetainedStartPartial: &retainedStartPartial,
		RetainedSHA256:       outputBytesSHA256(tail),
	}, !o.disableSync); err != nil {
		return err
	}
	// The tail lands in a sibling file that atomically replaces the output, so
	// no moment — mid-crash or mid-read — exists where the retained bytes live
	// only in this process. The new handle is opened before the rename and kept
	// as the store's own: a rename never detaches an open descriptor, and the
	// unlinked old file stays reachable through the old handle until it closes.
	tmpPath := o.path + ".tmp"
	nf, err := o.fs.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("jobstore: open output tail rewrite: %w", err)
	}
	abandonRewrite := func() {
		_ = nf.Close()
		_ = o.fs.Remove(tmpPath)
	}
	if n, err := nf.Write(tail); err != nil {
		abandonRewrite()
		return fmt.Errorf("jobstore: rewrite output tail: %w", err)
	} else if n != len(tail) {
		abandonRewrite()
		return fmt.Errorf("jobstore: rewrite output tail: %w", io.ErrShortWrite)
	}
	if !o.disableSync {
		if err := nf.Sync(); err != nil {
			abandonRewrite()
			return fmt.Errorf("jobstore: sync output tail rewrite: %w", err)
		}
	}
	if err := o.fs.Rename(tmpPath, o.path); err != nil {
		abandonRewrite()
		return fmt.Errorf("jobstore: replace output with retained tail: %w", err)
	}
	oldFile := o.f
	o.f = nf
	o.retainedStart = retainedStart
	o.retainedStartPartial = retainedStartPartial
	o.retainedHash = sha256.New()
	o.retainedHash.Write(tail)
	// The compaction replaced the retained file, so the previous anchor names
	// bytes that are no longer in it. The replacement was synced above, which
	// makes the compacted tail itself durable: seed the anchor from it so an
	// append before the next barrier cannot publish metadata with no anchor.
	o.durableFileSize, o.durableSHA256 = keep, outputBytesSHA256(tail)
	o.synced = false
	if err := oldFile.Close(); err != nil {
		return fmt.Errorf("jobstore: close replaced output: %w", err)
	}
	if o.disableSync {
		return nil
	}
	if err := syncParentDir(o.fs, o.path); err != nil {
		return err
	}
	// The replacement log's bytes and the rename that installed it are durable
	// now, so the handoff below has no log barrier left to pay. Withdraw the
	// claim if the handoff fails, so a later flush repays it.
	o.synced = true
	// The compaction is a handoff, and it has to be durable as one step. Until
	// the replacement FINAL sidecar and the removal of the journal are both
	// committed, a crash leaves the pre-compaction sidecar bound to the
	// compacted log — a state in which the anchor names bytes the log no longer
	// holds and recovery refuses output that survived intact. Paying both here
	// keeps it to one round per compaction rather than one per append.
	if err := o.writeMetaLocked(true); err != nil {
		o.synced = false
		return err
	}
	// writeMetaLocked removed the journal. This barrier commits that removal,
	// which is what keeps a crash from restoring a stale journal against the
	// compacted log.
	if err := syncParentDir(o.fs, o.metaPath); err != nil {
		return err
	}
	return nil
}

// persistMetaLocked writes metadata and pays the barriers that make the log and
// its sidecar survive a power crash together.
func (o *OutputStore) persistMetaLocked() error {
	return o.writeMetaLocked(true)
}

func (o *OutputStore) outputMetaLocked() (outputMeta, error) {
	if !o.disableSync && !o.synced {
		if err := o.f.Sync(); err != nil {
			return outputMeta{}, fmt.Errorf("jobstore: sync output before metadata: %w", err)
		}
	}
	return o.outputMetaContentLocked()
}

// outputMetaContentLocked builds metadata for the log as it stands, without
// making the log durable first.
func (o *OutputStore) outputMetaContentLocked() (outputMeta, error) {
	meta := outputMeta{
		TotalBytes:           o.total,
		RetainedStart:        o.retainedStart,
		RetainedStartPartial: new(o.retainedStartPartial),
	}
	if o.visibleStart > o.retainedStart {
		meta.VisibleStart, meta.VisibleStartPartial = new(o.visibleStart), new(o.visiblePartial)
	}
	if o.retainedHash == nil {
		// Seeded on first use, for a store not built by open.
		h, err := outputFileHash(o.fs, o.path)
		if err != nil {
			return outputMeta{}, err
		}
		o.retainedHash = h
	}
	meta.RetainedSHA256 = hex.EncodeToString(o.retainedHash.Sum(nil))
	return meta, nil
}

// hasDurableAnchor reports whether the log's durable anchor describes bytes the
// log still holds. A compaction replaces the log, so an anchor from before it
// no longer names the retained file.
func (o *OutputStore) hasDurableAnchor() bool {
	return o.durableSHA256 != "" && o.durableFileSize <= o.total-o.retainedStart
}

// writeOutputMetaFile atomically writes meta on the OS filesystem.
// writeOutputMetaFileFs carries the injectable seam; this preserves the
// os-backed signature the package's callers and tests already use.
//
//go:fix inline
func writeOutputMetaFile(path string, meta outputMeta) error {
	return writeOutputMetaFileFs(afero.NewOsFs(), path, meta)
}

func writeOutputMetaFileFs(fs afero.Fs, path string, meta outputMeta) error {
	return writeOutputMetaFileFsSync(fs, path, meta, true)
}

func writeOutputMetaFileFsSync(fs afero.Fs, path string, meta outputMeta, syncWrites bool) error {
	return writeOutputMetaFileFsSyncDir(fs, path, meta, syncWrites, syncWrites)
}

// writeOutputMetaFileFsSyncDir writes meta atomically. syncWrites fsyncs the
// bytes before the rename; syncDir fsyncs the parent directory after it, which
// only a write that creates this directory entry needs.
func writeOutputMetaFileFsSyncDir(fs afero.Fs, path string, meta outputMeta, syncWrites, syncDir bool) error {
	return writeOutputMetaFileFsSyncMarshalDir(fs, path, meta, syncWrites, syncDir, json.Marshal)
}

func writeOutputMetaFileFsSyncMarshalDir(fs afero.Fs, path string, meta outputMeta, syncWrites, syncDir bool, marshal func(any) ([]byte, error)) error {
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
	if syncDir {
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
	if err := dir.Sync(); err != nil && !fsatomic.SyncUnsupported(err) {
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

// readOutputViewForFile reads a closed output file's validated metadata as
// readers see it: the lifetime total, where the file starts, and where the
// visible output begins.
func readOutputViewForFile(fs afero.Fs, path string, outputPath string, retained int64) (outputView, error) {
	meta, err := readOutputMetaRecordForFile(fs, path, outputPath, retained)
	if err != nil {
		return outputView{}, err
	}
	return outputViewOf(meta), nil
}

func readOutputMetaRecordForFile(fs afero.Fs, path string, outputPath string, retained int64) (outputMeta, error) {
	pending, ok, err := readValidPendingOutputMeta(fs, outputPendingMetaPath(path), path, outputPath, retained)
	if err != nil {
		return outputMeta{}, err
	}
	if ok {
		return pending, nil
	}
	meta, ok, err := readValidOutputMetaFs(fs, path, outputPath, retained)
	if ok {
		return meta, nil
	}
	if err != nil {
		return outputMeta{}, err
	}
	return outputMeta{TotalBytes: retained}, nil
}

func outputMetaRetainedStartPartial(meta outputMeta) bool {
	if meta.RetainedStartPartial != nil {
		return *meta.RetainedStartPartial
	}
	return meta.RetainedStart > 0
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
		return pendingOverOldFile(meta, finalMeta, retained, hash), true, nil
	}
	hash, err := outputFileSHA256(fs, outputPath)
	if err != nil {
		return outputMeta{}, false, err
	}
	// The tail rewrite replaces the output atomically, so the file is never
	// shorter than the pending metadata's retained tail; a shortfall here is
	// out-of-contract tampering unless the pending metadata's durable anchor
	// says the log's unsynced tail was lost.
	if metaRetained > retained {
		return acceptLostOutputTail(fs, outputPath, meta, retained)
	}
	if meta.RetainedSHA256 != hash {
		info, err := fs.Stat(outputPath)
		if err != nil {
			return outputMeta{}, false, fmt.Errorf("jobstore: stat output metadata: %w", err)
		}
		if info.Size() > retained {
			return readExpandedPendingOutputMeta(fs, finalMetaPath, outputPath, meta, retained, info.Size())
		}
		finalMeta, ok, err := readOutputMeta(fs, finalMetaPath)
		if err != nil {
			return outputMeta{}, false, err
		}
		if ok && pendingOutputMetadataHandoff(finalMeta, meta, retained, hash) {
			return outputMeta{}, false, errOutputPendingHandoff
		}
		return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
	}
	return meta, true, nil
}

func readExpandedPendingOutputMeta(fs afero.Fs, finalMetaPath string, outputPath string, pendingMeta outputMeta, retained, expandedSize int64) (outputMeta, bool, error) {
	// Append writes through the current handle before prune publishes pending
	// metadata, so a reader can observe the append-expanded file while the
	// pending tail still describes the capped successor.
	finalMeta, ok, err := readOutputMeta(fs, finalMetaPath)
	if err != nil {
		return outputMeta{}, false, err
	}
	if !ok {
		return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
	}
	finalRetained, err := outputMetaRetainedBytes(finalMeta)
	if err != nil {
		return outputMeta{}, false, err
	}
	appendBytes := expandedSize - retained
	if finalRetained != retained ||
		pendingMeta.TotalBytes < expandedSize ||
		pendingMeta.TotalBytes-expandedSize != finalMeta.RetainedStart ||
		pendingMeta.RetainedStart != finalMeta.RetainedStart+appendBytes {
		return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
	}
	if ok, err := outputFileHasSuffixSHA256(fs, outputPath, appendBytes, retained, pendingMeta.RetainedSHA256); err != nil {
		if info, statErr := fs.Stat(outputPath); statErr == nil && info.Size() != expandedSize {
			return outputMeta{}, false, errOutputPendingHandoff
		}
		return outputMeta{}, false, err
	} else if !ok {
		return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
	}
	if ok, err := outputFileHasPrefixSHA256(fs, outputPath, finalRetained, finalMeta.RetainedSHA256); err != nil {
		if info, statErr := fs.Stat(outputPath); statErr == nil && info.Size() != expandedSize {
			return outputMeta{}, false, errOutputPendingHandoff
		}
		return outputMeta{}, false, err
	} else if !ok {
		return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
	}
	info, err := fs.Stat(outputPath)
	if err != nil {
		return outputMeta{}, false, fmt.Errorf("jobstore: stat output metadata: %w", err)
	}
	if info.Size() != expandedSize {
		return outputMeta{}, false, errOutputPendingHandoff
	}
	hash, err := outputFileSHA256(fs, outputPath)
	if err != nil {
		return outputMeta{}, false, err
	}
	return pendingOverOldFile(pendingMeta, finalMeta, expandedSize, hash), true, nil
}

// pendingOverOldFile describes the file a compaction has not yet replaced,
// using the pending metadata for its successor: the old file still starts
// where its final metadata says, so its first byte and line-partial flag come
// from there, and readers see only from where the compacted file will start.
func pendingOverOldFile(pending, final outputMeta, fileSize int64, hash string) outputMeta {
	visibleStart, visiblePartial := pending.RetainedStart, outputMetaRetainedStartPartial(pending)
	// The visible start never moves back: a compaction under a larger cap than
	// hid the old file's bytes keeps more than readers were already shown.
	if previous := outputViewOf(final); previous.visibleStart > visibleStart {
		visibleStart, visiblePartial = previous.visibleStart, previous.visiblePartial
	}
	pending.RetainedStart = pending.TotalBytes - fileSize
	pending.RetainedStartPartial = new(outputMetaRetainedStartPartial(final))
	pending.RetainedSHA256 = hash
	// Both callers validated that the compaction drops a non-empty prefix, so
	// the visible start is always past the old file's first byte.
	pending.VisibleStart, pending.VisibleStartPartial = new(visibleStart), new(visiblePartial)
	return pending
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
	// An anchored sidecar whose window no longer matches the log's contents names
	// bytes something did vouch for: its anchor. Report that prefix instead of
	// failing, which is what an equal-length crash (size survived, contents did
	// not) leaves behind, and failing there aborts session restore over a tail the
	// ruling lets us lose (Jesse's 2026-11-16 ruling: lose the tail, never
	// corrupt, and never fail restore for it).
	if hasDurableAnchorMeta(meta) && metaRetained == retained {
		if ok, err := outputFileHasPrefixSHA256(fs, outputPath, metaRetained, meta.RetainedSHA256); err != nil {
			return outputMeta{}, false, err
		} else if ok {
			// The window is the whole file here, so the prefix hash just
			// verified it; rehashing it below would read the file twice on
			// every ordinary read of a finished job.
			return meta, true, nil
		}
		return acceptLostOutputTail(fs, outputPath, meta, retained)
	}
	// A sidecar describes a prefix of the log, and a longer log is output the
	// sidecar had not recorded yet.
	if metaRetained < retained {
		if ok, err := outputFileHasPrefixSHA256(fs, outputPath, metaRetained, meta.RetainedSHA256); err != nil {
			return outputMeta{}, false, err
		} else if !ok {
			// The window the sidecar names is damaged, but an anchor may still
			// vouch for an intact prefix. Refusing here fails OutputFileStats and
			// aborts session restore over output that is recoverable, so try the
			// anchor the way the equal-length path does.
			if hasDurableAnchorMeta(meta) {
				return acceptLostOutputTail(fs, outputPath, meta, retained)
			}
			return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
		}
		if hasDurableAnchorMeta(meta) {
			// Bytes are output only when a checksum vouches for them, and the
			// sidecar's own checksum just vouched for exactly this window. A
			// suffix past it was never recorded, so it is the lost tail.
			// Newline termination is not evidence: a torn page can zero a block
			// and leave the trailing newline, and validating on it would present
			// corruption as output.
			meta.TotalBytes = meta.RetainedStart + metaRetained
			return meta, true, nil
		}
		// An unanchored sidecar predates the anchor contract, so it keeps the
		// older adopt-whole behavior rather than lose bytes nothing can vouch for
		// or against.
		hash, err := outputFileSHA256(fs, outputPath)
		if err != nil {
			return outputMeta{}, false, err
		}
		meta.TotalBytes = meta.RetainedStart + retained
		meta.RetainedSHA256 = hash
		return meta, true, nil
	}
	if metaRetained > retained {
		// The metadata names more bytes than the log holds. That is what a power
		// crash leaves when the sidecar rename reached the disk and the log's
		// unsynced tail did not, so fall back to the durable anchor the sidecar
		// carries and report the bytes that survived (Jesse's 2026-11-16 ruling:
		// lose the tail, never corrupt). Metadata without an anchor, or a log
		// that does not hold the anchored prefix, is still a mismatch.
		return acceptLostOutputTail(fs, outputPath, meta, retained)
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

func pendingOutputMetadataHandoff(finalMeta, pendingMeta outputMeta, retained int64, outputHash string) bool {
	finalRetained, err := outputMetaRetainedBytes(finalMeta)
	if err != nil || finalRetained != retained {
		return false
	}
	pendingRetained, err := outputMetaRetainedBytes(pendingMeta)
	if err != nil || pendingRetained != retained {
		return false
	}
	totalDelta := pendingMeta.TotalBytes - finalMeta.TotalBytes
	retainedStartDelta := pendingMeta.RetainedStart - finalMeta.RetainedStart
	if totalDelta <= 0 || retainedStartDelta <= 0 {
		return false
	}
	if totalDelta != retainedStartDelta {
		return false
	}
	if pendingMeta.RetainedSHA256 != outputHash && finalMeta.RetainedSHA256 != outputHash {
		return false
	}
	return true
}

func outputMetaRetainedBytes(meta outputMeta) (int64, error) {
	if meta.RetainedStart < 0 || meta.TotalBytes < meta.RetainedStart {
		return 0, errors.New("jobstore: output metadata does not match retained output")
	}
	return meta.TotalBytes - meta.RetainedStart, nil
}

// hasDurableAnchorMeta reports whether a sidecar names a prefix of its log that
// reached the disk. Without one, recovery cannot tell a log's untrusted suffix
// from output its sidecar had not recorded.
func hasDurableAnchorMeta(meta outputMeta) bool {
	return meta.DurableFileSize != nil && meta.DurableSHA256 != ""
}

// acceptLostOutputTail reports the surviving bytes of a log whose metadata
// describes more. It accepts only when the sidecar carries a durable anchor and
// the log still holds the prefix that anchor names, which is the state a power
// crash leaves when the sidecar rename reached the disk and the log's unsynced
// tail did not. The returned metadata describes the bytes that survived, so
// every reader places them in the same lifetime offsets the anchor used.
func acceptLostOutputTail(fs afero.Fs, outputPath string, meta outputMeta, retained int64) (outputMeta, bool, error) {
	if !hasDurableAnchorMeta(meta) {
		return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
	}
	anchored := *meta.DurableFileSize
	if anchored < 0 || anchored > retained {
		return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
	}
	if ok, err := outputFileHasPrefixSHA256(fs, outputPath, anchored, meta.DurableSHA256); err != nil {
		return outputMeta{}, false, err
	} else if !ok {
		return outputMeta{}, false, errors.New("jobstore: output metadata does not match retained output")
	}
	if anchored == 0 {
		// A store that crashed before its first barrier has a genuine zero-length
		// anchor, and the checksum above vouches for no byte. Report the log as
		// empty rather than refuse it: refusing makes recovery abort session
		// restore, which turns a tail the ruling allows us to lose into a dead
		// session, and reporting the surviving bytes would present bytes nothing
		// verified as output. Losing the whole tail is the honest answer.
		meta.TotalBytes = meta.RetainedStart
		meta.RetainedSHA256 = outputBytesSHA256(nil)
		meta.VisibleStart, meta.VisibleStartPartial = nil, nil
		return meta, true, nil
	}
	// Only the anchored prefix is bytes something vouched for. Whatever survives
	// past it is the log the crash could not save, and a torn page can leave
	// arbitrary content there, so report the anchored prefix as the output
	// rather than present unverified bytes (Jesse's 2026-11-16 ruling: lose the
	// tail, never corrupt).
	meta.TotalBytes = meta.RetainedStart + anchored
	meta.RetainedSHA256 = meta.DurableSHA256
	if meta.VisibleStart != nil && *meta.VisibleStart > meta.TotalBytes {
		meta.VisibleStart = new(meta.TotalBytes)
	}
	return meta, true, nil
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
	h, err := outputFileHash(fs, path)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// outputFileHash is the SHA-256 state after reading the whole output file.
func outputFileHash(fs afero.Fs, path string) (hash.Hash, error) {
	f, err := fs.Open(path)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open output for metadata hash: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, fmt.Errorf("jobstore: hash output metadata: %w", err)
	}
	return h, nil
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

// Close pays any outstanding barriers, so the closed log and its metadata are
// durable together, then closes the file. A closed store is inert: a second
// Close reports no error.
func (o *OutputStore) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	flushErr := o.flushLocked()
	if flushErr != nil {
		// The barrier is still owed, so the store stays open and retryable.
		return flushErr
	}
	o.closed = true
	return o.f.Close()
}
