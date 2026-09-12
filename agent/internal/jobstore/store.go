package jobstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/afero"
)

// ErrStoreClosed is returned when an operation is attempted after Close.
var ErrStoreClosed = errors.New("jobstore: store closed")

// Store is an append-only jobs.jsonl event log for one session. It assigns a
// monotonic Seq to each appended event, fsyncs, and reconstructs records via
// Fold. It is safe for concurrent use.
type Store struct {
	mu     sync.Mutex
	path   string
	fs     afero.Fs
	f      afero.File
	seq    int64
	closed bool

	// foldGen advances every time the durable journal bytes the store has
	// accounted for change: on every successful append or batch append, on
	// every rollback or trailing-line repair that rewrites the file, and on
	// every load that observes bytes the cursor had not yet accounted for
	// (including foreign appends). It is the fold cache's invalidation key:
	// a cached fold is served only when its generation still matches.
	foldGen uint64
	folds   foldCache

	// disableSync, when set, skips the per-append fsync. Production opens via
	// Open, which leaves this false, so the durable write path is unchanged. The
	// on-disk bytes are identical either way.
	disableSync bool

	// The tail cursor: decoded events for the file's leading bytes, so a reload
	// re-reads and re-decodes only what was appended since the last one. The file
	// on disk stays the sole truth for what is durable; the cursor is a cache of
	// bytes already read, and it is trusted only while the file is byte-for-byte
	// the one the store itself last left behind. See cursorTrustedLocked.
	cursor fileCursor
}

// fileCursor caches the events decoded from the log's leading bytes.
//
// COHERENCE. valid means: events are exactly the decode of the file's bytes
// [0, offset), offset lands just past a newline, and the file was size bytes
// long with modification time mod when the store last looked. Every load stats
// the file and keeps the cursor only when size and mod still match, so anything
// the store did not do itself — a rewrite by another process, a test replacing
// the log, a delegate log truncated by hand — forces a full reread of the file
// as it now is, and the store's own appends carry the new size and mod forward.
// A load never trusts bytes it has not accounted for.
//
// Because that identity leans on mtime, the store CALIBRATES it: if one of its
// own appends changes the file's length without moving its mtime, this
// filesystem's timestamps cannot resolve a change (a coarse-granularity or
// attribute-caching mount), and the cursor is disabled for the store's lifetime —
// every load re-reads the whole file, exactly as before this cursor existed. The
// degradation is in the safe direction: slower, never stale. See
// disabled/TestStoreCursorDisablesItselfWhenMTimeCannotResolveWrites.
//
// The residual: a foreign writer that rewrote the log to the SAME length AND
// restored its mtime afterwards is indistinguishable from no write at all. The
// log is append-only by contract and the owning store is its only sanctioned
// writer, so this is a bound on how far out-of-contract writes are tolerated, not
// a durability property. It is stated as a test, not just here:
// TestStoreCursorSameSizeSameMTimeRewriteIsTheDocumentedBoundary. Closing it
// completely means re-reading the prefix on every load, which is the cost this
// cursor exists to remove; a stat-derived identity (inode, change time) does not
// close it either, since a filesystem whose mtime is coarse rounds its ctime the
// same way.
//
// CRASH SAFETY. The cursor is process memory only, never persisted, and no
// durable byte depends on it: appends still write and fsync exactly as before,
// and a crash simply loses it. A fresh process reads the file from byte zero. A
// torn trailing line is handled ahead of the cursor by
// recoverTrailingPartialLineLocked, and any repair it performs (terminating or
// truncating the tail) invalidates the cursor; an unterminated tail is never
// committed to it, since offset only ever advances past a newline.
type fileCursor struct {
	events []Event
	offset int64
	lines  int
	size   int64
	mod    time.Time
	valid  bool
	// disabled is sticky: set when this filesystem proved it cannot tell one of
	// the store's own writes from no write at all, after which the store always
	// re-reads the whole file.
	disabled bool
}

// foldCache caches the last fold of each kind, keyed on the cursor's identity
// of the log: a generation that advances every time the journal bytes the
// store has accounted for change. The next load reuses the cached fold only
// when the generation still matches — anything the store did not do itself
// (a foreign rewrite, a truncation, a trailing-line repair) drops the cursor
// or resets it, and the cached fold goes with it. The store's own appends
// bump the generation on every path that changes the durable bytes
// (Append, AppendBatch, rollbackAppendLocked, the trailing-line repairs), so
// a fold cached before an append is never served after it. The generation
// moves on every append even when the cursor is disabled or invalid, which
// is what keeps callers that depend on seeing foreign appends instantly
// correct: a foreign append is only ever observed through a load, and a
// load that observes new bytes resets the cursor and bumps the generation
// before folding, so the fresh bytes are folded, never skipped.
// The cache is process memory only, like the cursor: a crash loses it, and a
// fresh process folds from byte zero.
//
// Cached values are the same private snapshots a fresh fold builds: every
// load hands back deep copies (see the cloneFold* helpers), so a caller that
// mutates a loaded record cannot corrupt what the next load reports. A
// cached fold must be identical to folding the journal bytes it keys on.
//
// A hit additionally requires the file to still be the one the cursor was
// built from (same length, same modification time — the cursorTrustedLocked
// check, via one stat). The generation alone cannot prove that: a foreign
// writer could have appended bytes since the last load without moving the
// generation, and serving the cached fold would hide them. The stat keeps
// that path honest — any observed change forces the full read-and-fold —
// while still avoiding the open, scan, decode and fold on a quiet log.
type foldCache struct {
	gen       uint64
	jobs      map[string]*JobRecord
	ordered   []*JobRecord
	watches   map[string]*WatchRecord
	sends     WatchSendRecord
	jobsOK    bool
	orderedOK bool
	watchesOK bool
	sendsOK   bool
}

// Open opens (creating if needed) the jobs.jsonl at path and recovers the next
// sequence number from any existing content.
func Open(path string) (*Store, error) {
	return openFs(afero.NewOsFs(), path)
}

// OpenNoSync opens a store with per-append fsync disabled for deterministic
// tests and fuzzers whose contract is append/load semantics, not crash
// durability. Production callers must use Open.
func OpenNoSync(path string) (*Store, error) {
	s, err := Open(path)
	if err != nil {
		return nil, err
	}
	s.disableSync = true
	return s, nil
}

// openFs opens the store on the given filesystem. Open delegates here with
// afero.NewOsFs(), which forwards every call straight to the os package, so
// production behavior is byte-identical to direct os calls. Tests and fuzzers
// inject an in-memory or sandboxed filesystem to drive persistence off real
// disk.
func openFs(fs afero.Fs, path string) (*Store, error) {
	f, err := fs.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open %s: %w", path, err)
	}
	s := &Store{path: path, fs: fs, f: f}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("jobstore: stat %s: %w", path, err)
	}
	if info.Size() == 0 {
		return s, nil
	}
	existing, err := s.readAllLocked()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	for _, e := range existing {
		if e.Seq > s.seq {
			s.seq = e.Seq
		}
	}
	return s, nil
}

// Append assigns the next Seq to e, writes it as a JSON line, and fsyncs.
func (s *Store) Append(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	s.verifyCursorBeforeAppendLocked()
	nextSeq := s.seq + 1
	e.Seq = nextSeq
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("jobstore: marshal event: %w", err)
	}
	startOffset, err := s.f.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("jobstore: seek append start: %w", err)
	}
	b = append(b, '\n')
	if err := s.writeLineLocked(b); err != nil {
		return s.appendFailureLocked("write event", err, startOffset)
	}
	if err := s.syncLocked(); err != nil {
		return s.appendFailureLocked("sync event", err, startOffset)
	}
	s.seq = nextSeq
	s.noteAppendedLocked(startOffset, startOffset+int64(len(b)))
	return nil
}

// AppendBatch assigns contiguous Seq values to events, writes them as one
// all-or-nothing append, and fsyncs once. If any marshal, write, or sync fails,
// the file is truncated back to the pre-batch offset and the store seq is left
// unchanged.
func (s *Store) AppendBatch(events []Event) error {
	if len(events) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	s.verifyCursorBeforeAppendLocked()
	startOffset, err := s.f.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("jobstore: seek append start: %w", err)
	}
	nextSeq := s.seq
	written := int64(0)
	for _, e := range events {
		nextSeq++
		e.Seq = nextSeq
		b, err := json.Marshal(e)
		if err != nil {
			return s.appendFailureLocked("marshal event", err, startOffset)
		}
		b = append(b, '\n')
		if err := s.writeLineLocked(b); err != nil {
			return s.appendFailureLocked("write event", err, startOffset)
		}
		written += int64(len(b))
	}
	if err := s.syncLocked(); err != nil {
		return s.appendFailureLocked("sync event", err, startOffset)
	}
	s.seq = nextSeq
	s.noteAppendedLocked(startOffset, startOffset+written)
	return nil
}

// Load reads every event and folds them to the current records.
func (s *Store) Load() (map[string]*JobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return nil, err
	}
	if folded, ok, err := s.foldJobsHitLocked(); err != nil {
		return nil, err
	} else if ok {
		return folded, nil
	}
	events, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	return s.noteFoldedJobsLocked(Fold(events)), nil
}

// LoadOrdered reads every event and folds them to the current records, returning
// them in durable APPEND ORDER — sorted by the seq of each job's FIRST event.
// Append order is the total order the append-only log defines; callers that must
// resolve "the latest record" (the one appended last) read it here rather than
// from a wall-clock field, which can skew across restore.
func (s *Store) LoadOrdered() ([]*JobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return nil, err
	}
	if folded, ok, err := s.foldOrderedHitLocked(); err != nil {
		return nil, err
	} else if ok {
		return folded, nil
	}
	events, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	return s.noteFoldedOrderedLocked(FoldOrdered(events)), nil
}

// LoadWatches reads every event and folds durable watch registry state.
func (s *Store) LoadWatches() (map[string]*WatchRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return nil, err
	}
	if folded, ok, err := s.foldWatchesHitLocked(); err != nil {
		return nil, err
	} else if ok {
		return folded, nil
	}
	events, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	return s.noteFoldedWatchesLocked(FoldWatches(events)), nil
}

// LoadWatchSends reads every event and folds durable pending watch-send state.
func (s *Store) LoadWatchSends() (WatchSendRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return WatchSendRecord{}, err
	}
	if folded, ok, err := s.foldSendsHitLocked(); err != nil {
		return WatchSendRecord{}, err
	} else if ok {
		return folded, nil
	}
	events, err := s.readAllLocked()
	if err != nil {
		return WatchSendRecord{}, err
	}
	return s.noteFoldedSendsLocked(FoldWatchSends(events)), nil
}

// LoadEvents reads every durable event in append order.
func (s *Store) LoadEvents() ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return nil, err
	}
	return s.readAllLocked()
}

// readAll is the locked-public test/helper variant of readAllLocked.
func (s *Store) readAll() ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return nil, err
	}
	return s.readAllLocked()
}

// readAllLocked returns every durable event in the log. It reads and decodes
// only the bytes appended since the previous call, replaying the rest from the
// tail cursor; the result is what a full reread of the file would produce.
func (s *Store) readAllLocked() ([]Event, error) {
	if err := s.recoverTrailingPartialLineLocked(); err != nil {
		return nil, err
	}
	info, err := s.fs.Stat(s.path)
	if err != nil {
		s.resetCursorLocked()
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("jobstore: stat %s: %w", s.path, err)
	}
	trusted := s.cursorTrustedLocked(info)
	if !trusted {
		s.resetCursorLocked()
	}
	// An unterminated trailing line survived recovery, which means it is durable
	// corruption or a value recovery chose not to touch; it is reported by the
	// decode below but never committed to the cursor.
	//
	// The read is skipped only when the file is the very one already decoded to
	// its end. A reported length is never allowed to suppress a read on its own:
	// a filesystem under-reporting the size would otherwise hide durable bytes,
	// so anything short of "trusted and fully consumed" opens the file and scans
	// to its real EOF, exactly as the full reread always did.
	var tail []Event
	if !trusted || info.Size() != s.cursor.offset {
		var err error
		tail, err = s.advanceCursorLocked()
		if err != nil {
			s.resetCursorLocked()
			return nil, err
		}
		// The load observed bytes the cursor had not yet accounted for — the
		// store's own appends between loads, or a foreign append that reset
		// the cursor above. Fold state cached at the old generation describes
		// the old bytes and must not be served for the new ones.
		s.foldGen++
		s.folds = foldCache{gen: s.foldGen}
	}
	s.cursor.size = info.Size()
	s.cursor.mod = info.ModTime()
	s.cursor.valid = true
	if len(s.cursor.events) == 0 && len(tail) == 0 {
		return nil, nil
	}
	events := make([]Event, 0, len(s.cursor.events)+len(tail))
	for _, e := range s.cursor.events {
		events = append(events, cloneEvent(e))
	}
	for _, e := range tail {
		events = append(events, cloneEvent(e))
	}
	return events, nil
}

// cursorTrustedLocked reports whether the file is still the one the cursor was
// built from: same length, same modification time. Anything else — including a
// foreign append, which is cheap enough to reread whole — is not accounted for,
// so the cursor is dropped rather than extended over unverified bytes.
func (s *Store) cursorTrustedLocked(info os.FileInfo) bool {
	return s.cursor.valid && !s.cursor.disabled &&
		info.Size() == s.cursor.size && info.ModTime().Equal(s.cursor.mod)
}

func (s *Store) resetCursorLocked() {
	disabled := s.cursor.disabled
	s.cursor = fileCursor{disabled: disabled}
	// The journal the cursor described is gone (foreign rewrite, truncation,
	// deleted log, decode error, or a disabled cursor re-reading from zero):
	// no fold cached against it may be served again. Loads that observe new
	// bytes bump the generation again below when the cursor re-advances, so
	// this invalidation is never confused with the fresh state that follows.
	s.folds = foldCache{}
}

// invalidateCursorLocked drops the cursor after the store itself changed the
// file in a way that is not a plain append (a rollback truncation, a
// trailing-line repair), so the next load re-reads the file from byte zero.
func (s *Store) invalidateCursorLocked() {
	s.cursor.valid = false
	// The file changed under every cached fold: bump the generation so a
	// fold cached before the repair is never served after it. The next load
	// re-reads from byte zero and caches fresh folds at the new generation.
	s.foldGen++
	s.folds = foldCache{gen: s.foldGen}
}

// verifyCursorBeforeAppendLocked drops the cursor unless the file is still the
// one it was built from. It runs BEFORE the store appends, because an append
// moves the file's identity forward: without this check a foreign rewrite that
// happened since the last load would be laundered into the cursor's own history
// and the stale prefix trusted forever. Agent tests rewrite a delegate's log in
// place and then append to it, so this is a live path, not a hypothetical.
func (s *Store) verifyCursorBeforeAppendLocked() {
	if !s.cursor.valid {
		return
	}
	info, err := s.fs.Stat(s.path)
	if err != nil || !s.cursorTrustedLocked(info) {
		s.cursor.valid = false
		// The bytes the cached folds describe may not be the file's bytes
		// (foreign rewrite since the last load). Drop them; the append below
		// bumps the generation again, and the next load folds fresh bytes.
		s.foldGen++
		s.folds = foldCache{gen: s.foldGen}
	}
}

// noteAppendedLocked carries the cursor's file identity across the store's own
// append: the cached prefix is still exactly the file's leading bytes, only the
// length and modification time moved. endOffset is where the store's own write
// ended, which is the only trustworthy statement about the file's length here —
// the store computed it from the bytes it wrote, rather than asking the
// filesystem. Bookkeeping failures leave the cursor invalid and are NOT reported
// as append failures: the bytes are already durable, and a dropped cursor costs
// a reread, never a wrong answer.
func (s *Store) noteAppendedLocked(startOffset, endOffset int64) {
	// Every append changes the durable bytes, so the generation moves on all
	// paths below — including the early returns that drop the cursor. A fold
	// cached before this append must never be served after it, whether or not
	// the cursor survived.
	s.foldGen++
	s.folds = foldCache{gen: s.foldGen}
	if !s.cursor.valid {
		return
	}
	if startOffset != s.cursor.size {
		// The file grew or shrank behind the store's back before this append.
		s.cursor.valid = false
		return
	}
	info, err := s.fs.Stat(s.path)
	if err != nil {
		s.cursor.valid = false
		return
	}
	if info.Size() != endOffset {
		// The filesystem does not report the bytes just written — stale or cached
		// metadata, or a concurrent writer. Its numbers cannot be used to decide
		// what has already been read, so the cursor goes.
		s.cursor.valid = false
		return
	}
	if endOffset != startOffset && info.ModTime().Equal(s.cursor.mod) {
		// This append changed the file's length and the filesystem reported the
		// same modification time as before it: timestamps here cannot resolve a
		// write, so they cannot be used to tell the store's own bytes from
		// someone else's. Give the cursor up for good rather than trust it.
		s.cursor.disabled = true
		s.cursor.valid = false
		return
	}
	s.cursor.size = info.Size()
	s.cursor.mod = info.ModTime()
}

// advanceCursorLocked decodes the log from the cursor's offset to the file's real
// EOF, committing every newline-terminated line to the cursor. An unterminated
// final line is decoded and returned separately: it is not durable bytes the
// cursor may keep, but it must be reported exactly as a full reread reports it.
// No length reported by the filesystem bounds this scan.
func (s *Store) advanceCursorLocked() (tail []Event, err error) {
	f, err := s.fs.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("jobstore: read %s: %w", s.path, err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close %s: %w", s.path, closeErr)
		}
	}()
	if s.cursor.offset > 0 {
		if _, err := f.Seek(s.cursor.offset, io.SeekStart); err != nil {
			return nil, fmt.Errorf("jobstore: seek %s: %w", s.path, err)
		}
	}
	sc := bufio.NewScanner(f)
	sc.Split(scanLinesKeepingTerminator)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		raw := sc.Bytes()
		terminated := raw[len(raw)-1] == '\n'
		line := trimLineTerminator(raw)
		lineNo := s.cursor.lines + 1
		var e Event
		decoded := false
		if len(line) > 0 {
			if err := json.Unmarshal(line, &e); err != nil {
				return nil, fmt.Errorf("jobstore: parse event line %d: %w", lineNo, err)
			}
			decoded = true
		}
		if !terminated {
			if decoded {
				tail = append(tail, e)
			}
			break
		}
		s.cursor.lines = lineNo
		s.cursor.offset += int64(len(raw))
		if decoded {
			s.cursor.events = append(s.cursor.events, e)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: scan %s: %w", s.path, err)
	}
	return tail, nil
}

// scanLinesKeepingTerminator is bufio.ScanLines with the newline left on the
// token, so the cursor can account the exact byte length of every line it
// consumes. trimLineTerminator reproduces ScanLines' own trimming, including its
// carriage-return handling, so the bytes handed to the decoder are unchanged.
func scanLinesKeepingTerminator(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i+1], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func trimLineTerminator(raw []byte) []byte {
	line := bytes.TrimSuffix(raw, []byte{'\n'})
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line
}

func (s *Store) recoverTrailingPartialLineLocked() (err error) {
	info, err := s.fs.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("jobstore: stat %s: %w", s.path, err)
	}
	if info.Size() == 0 {
		return nil
	}
	f, err := s.fs.Open(s.path)
	if err != nil {
		return fmt.Errorf("jobstore: inspect %s: %w", s.path, err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close %s: %w", s.path, closeErr)
		}
	}()
	if _, err := f.Seek(-1, io.SeekEnd); err != nil {
		return fmt.Errorf("jobstore: inspect trailing byte: %w", err)
	}
	last := make([]byte, 1)
	if _, err := io.ReadFull(f, last); err != nil {
		return fmt.Errorf("jobstore: read trailing byte: %w", err)
	}
	if last[0] == '\n' {
		return nil
	}
	raw, err := afero.ReadFile(s.fs, s.path)
	if err != nil {
		return fmt.Errorf("jobstore: read %s: %w", s.path, err)
	}
	cut := bytes.LastIndexByte(raw, '\n')
	if cut < 0 {
		return s.recoverTrailingJSONLineLocked(raw, 0)
	}
	return s.recoverTrailingJSONLineLocked(raw[cut+1:], int64(cut+1))
}

func (s *Store) recoverTrailingJSONLineLocked(line []byte, offset int64) error {
	var e Event
	err := json.Unmarshal(line, &e)
	if err == nil {
		return s.finishTrailingJSONLineLocked()
	}
	if !isIncompleteTrailingJSON(line, err) {
		return nil
	}
	// Repairing the tail changes the file under the cursor: re-read from zero.
	s.invalidateCursorLocked()
	if err := s.f.Truncate(offset); err != nil {
		return fmt.Errorf("jobstore: truncate trailing partial line: %w", err)
	}
	if _, err := s.f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("jobstore: seek after trailing recovery: %w", err)
	}
	if err := s.syncLocked(); err != nil {
		return fmt.Errorf("jobstore: sync trailing recovery: %w", err)
	}
	return nil
}

func (s *Store) finishTrailingJSONLineLocked() error {
	// Terminating the tail changes the file under the cursor: re-read from zero.
	s.invalidateCursorLocked()
	if _, err := s.f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("jobstore: seek after trailing recovery: %w", err)
	}
	if err := s.writeLineLocked([]byte{'\n'}); err != nil {
		return fmt.Errorf("jobstore: terminate trailing event: %w", err)
	}
	if err := s.syncLocked(); err != nil {
		return fmt.Errorf("jobstore: sync trailing recovery: %w", err)
	}
	return nil
}

func isIncompleteTrailingJSON(line []byte, err error) bool {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return false
	}
	if err.Error() == "unexpected end of JSON input" {
		return true
	}
	if len(bytes.TrimRight(line, " \t\r\n")) != len(line) {
		return false
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		return false
	}
	if syntaxErr.Offset < int64(len(line)) {
		return false
	}
	last := trimmed[len(trimmed)-1]
	if last == '}' || last == ']' {
		return false
	}
	msg := err.Error()
	// The JSON scanner feeds a synthetic space at EOF, so an incomplete
	// literal or number reports an invalid space. A malformed final byte such
	// as the x in "trx" reports that byte instead and is durable corruption.
	return strings.Contains(msg, "literal") && strings.Contains(msg, "invalid character ' '")
}

func (s *Store) ensureOpenLocked() error {
	if s.closed {
		return ErrStoreClosed
	}
	return nil
}

// syncLocked fsyncs the underlying file, unless the store was opened with sync
// disabled (the fuzz-only fast path). Production stores always sync.
func (s *Store) syncLocked() error {
	if s.disableSync {
		return nil
	}
	return s.f.Sync()
}

func (s *Store) writeLineLocked(line []byte) error {
	for len(line) > 0 {
		n, err := s.f.Write(line)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		line = line[n:]
	}
	return nil
}

func (s *Store) appendFailureLocked(operation string, err error, startOffset int64) error {
	if rollbackErr := s.rollbackAppendLocked(startOffset); rollbackErr != nil {
		return fmt.Errorf("jobstore: %s: %w; rollback failed: %w", operation, err, rollbackErr)
	}
	return fmt.Errorf("jobstore: %s: %w", operation, err)
}

func (s *Store) rollbackAppendLocked(startOffset int64) error {
	// The file's length and modification time both moved in ways the cursor did
	// not record; the next load re-reads from byte zero.
	s.invalidateCursorLocked()
	truncateErr := s.f.Truncate(startOffset)
	_, seekErr := s.f.Seek(0, io.SeekEnd)
	syncErr := error(nil)
	if truncateErr == nil && seekErr == nil {
		syncErr = s.syncLocked()
	}
	if truncateErr != nil && seekErr != nil {
		return fmt.Errorf("truncate to %d: %w; seek eof: %w", startOffset, truncateErr, seekErr)
	}
	if truncateErr != nil {
		return fmt.Errorf("truncate to %d: %w", startOffset, truncateErr)
	}
	if seekErr != nil {
		return fmt.Errorf("seek eof: %w", seekErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync rollback truncate: %w", syncErr)
	}
	return nil
}

// Close closes the underlying file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("jobstore: close %s: %w", s.path, err)
	}
	s.closed = true
	// Release the cached events; a closed store can no longer be loaded.
	s.resetCursorLocked()
	return nil
}

// foldHitLocked reports whether the cached fold of one kind is still current:
// the slot is populated and its generation is the store's. Every path that
// changes the accounted-for journal bytes bumps foldGen and clears the slots
// (Append, AppendBatch, rollback, trailing-line repairs, loads that observe
// new bytes — including foreign appends — and cursor resets), so a hit means
// the journal is byte-for-byte the one that was folded.
// A hit additionally requires the file on disk to still be the file the
// cursor was built from: the helper stats the log and applies the same trust
// check readAllLocked uses. A foreign append between two loads changes the
// size or mtime, fails the check, and forces the full read-and-fold — so
// callers that depend on seeing foreign appends instantly still see them.
// The trust check alone is not the whole story: with stale filesystem
// metadata an earlier load can leave the cursor's consumed offset ahead of
// the size it recorded, and a stat that still reports that size would keep
// passing while foreign appends pile up past it. A hit therefore also
// requires the cursor to have consumed through the reported size — the same
// condition readAllLocked uses to skip its rescan — so any such divergence
// forces the full read-and-fold.
// A store whose cursor is disabled (see fileCursor) never reports a hit:
// its filesystem cannot resolve a write by size and mtime, so a stat cannot
// prove the bytes are unchanged and every load re-reads, as before.
func (s *Store) foldCurrentLocked() (bool, error) {
	if s.cursor.disabled {
		return false, nil
	}
	info, err := s.fs.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("jobstore: stat %s: %w", s.path, err)
	}
	return s.cursorTrustedLocked(info) && info.Size() == s.cursor.offset, nil
}

func (s *Store) foldJobsHitLocked() (map[string]*JobRecord, bool, error) {
	if !s.folds.jobsOK || s.folds.gen != s.foldGen {
		return nil, false, nil
	}
	ok, err := s.foldCurrentLocked()
	if err != nil || !ok {
		return nil, false, err
	}
	return cloneFoldJobs(s.folds.jobs), true, nil
}

func (s *Store) foldOrderedHitLocked() ([]*JobRecord, bool, error) {
	if !s.folds.orderedOK || s.folds.gen != s.foldGen {
		return nil, false, nil
	}
	ok, err := s.foldCurrentLocked()
	if err != nil || !ok {
		return nil, false, err
	}
	return cloneFoldOrdered(s.folds.ordered), true, nil
}

func (s *Store) foldWatchesHitLocked() (map[string]*WatchRecord, bool, error) {
	if !s.folds.watchesOK || s.folds.gen != s.foldGen {
		return nil, false, nil
	}
	ok, err := s.foldCurrentLocked()
	if err != nil || !ok {
		return nil, false, err
	}
	return cloneFoldWatches(s.folds.watches), true, nil
}

func (s *Store) foldSendsHitLocked() (WatchSendRecord, bool, error) {
	if !s.folds.sendsOK || s.folds.gen != s.foldGen {
		return WatchSendRecord{}, false, nil
	}
	ok, err := s.foldCurrentLocked()
	if err != nil || !ok {
		return WatchSendRecord{}, false, err
	}
	return cloneFoldSends(s.folds.sends), true, nil
}

// The noteFolded*Locked helpers cache a freshly computed fold at the current
// generation and hand the caller its own deep copy. The cache keeps one copy
// and the caller gets another, so later caller mutations reach neither the
// cache nor each other — the same independence guarantee loads have always
// carried (see TestStoreIncrementalReloadIndependentSnapshots).
func (s *Store) noteFoldedJobsLocked(folded map[string]*JobRecord) map[string]*JobRecord {
	s.folds.gen = s.foldGen
	s.folds.jobs = cloneFoldJobs(folded)
	s.folds.jobsOK = true
	return cloneFoldJobs(folded)
}

func (s *Store) noteFoldedOrderedLocked(folded []*JobRecord) []*JobRecord {
	s.folds.gen = s.foldGen
	s.folds.ordered = cloneFoldOrdered(folded)
	s.folds.orderedOK = true
	return cloneFoldOrdered(folded)
}

func (s *Store) noteFoldedWatchesLocked(folded map[string]*WatchRecord) map[string]*WatchRecord {
	s.folds.gen = s.foldGen
	s.folds.watches = cloneFoldWatches(folded)
	s.folds.watchesOK = true
	return cloneFoldWatches(folded)
}

func (s *Store) noteFoldedSendsLocked(folded WatchSendRecord) WatchSendRecord {
	s.folds.gen = s.foldGen
	s.folds.sends = cloneFoldSends(folded)
	s.folds.sendsOK = true
	return cloneFoldSends(folded)
}

// cloneFoldJobs deep-copies folded job records. Every field that can carry a
// caller-visible reference is copied: the time and int/bool pointers, the
// provenance chains, and the StructuredResult payload, which is a value
// decoded by encoding/json (see cloneJSONValue). Background, Phase and
// LastActivity are live-only and never set by a fold, so there is nothing to
// copy for them beyond the struct value itself — except LastActivity, which
// is still cloned defensively in case a caller sets it on a returned record.
func cloneFoldJobs(in map[string]*JobRecord) map[string]*JobRecord {
	if in == nil {
		return nil
	}
	out := make(map[string]*JobRecord, len(in))
	for k, r := range in {
		if r == nil {
			out[k] = nil
			continue
		}
		c := *r
		c.LastActivity = clonePtr(r.LastActivity)
		c.EndedAt = clonePtr(r.EndedAt)
		c.ExitCode = clonePtr(r.ExitCode)
		c.StructuredResult = cloneJSONValue(r.StructuredResult)
		c.StructuredResultValid = clonePtr(r.StructuredResultValid)
		c.Provenance = cloneCausal(r.Provenance)
		c.NotificationProvenance = cloneCausal(r.NotificationProvenance)
		out[k] = &c
	}
	return out
}

func cloneFoldOrdered(in []*JobRecord) []*JobRecord {
	if in == nil {
		return nil
	}
	out := make([]*JobRecord, len(in))
	for i, r := range in {
		if r == nil {
			continue
		}
		c := *r
		c.LastActivity = clonePtr(r.LastActivity)
		c.EndedAt = clonePtr(r.EndedAt)
		c.ExitCode = clonePtr(r.ExitCode)
		c.StructuredResult = cloneJSONValue(r.StructuredResult)
		c.StructuredResultValid = clonePtr(r.StructuredResultValid)
		c.Provenance = cloneCausal(r.Provenance)
		c.NotificationProvenance = cloneCausal(r.NotificationProvenance)
		out[i] = &c
	}
	return out
}

// cloneFoldWatches deep-copies folded watch records. WatchRecord is strings,
// ints and bools — no reference fields — so a struct copy per record is a
// full deep copy.
func cloneFoldWatches(in map[string]*WatchRecord) map[string]*WatchRecord {
	if in == nil {
		return nil
	}
	out := make(map[string]*WatchRecord, len(in))
	for k, r := range in {
		if r == nil {
			out[k] = nil
			continue
		}
		c := *r
		out[k] = &c
	}
	return out
}

func cloneFoldSends(in WatchSendRecord) WatchSendRecord {
	out := WatchSendRecord{}
	if in.Pending == nil {
		return out
	}
	out.Pending = make(map[WatchSendKey]*WatchSendState, len(in.Pending))
	for k, v := range in.Pending {
		out.Pending[k] = cloneWatchSend(v)
	}
	return out
}
