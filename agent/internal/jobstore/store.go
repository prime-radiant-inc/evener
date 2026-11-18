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
	mu   sync.Mutex
	path string
	fs   afero.Fs
	f    afero.File
	seq  int64
	// unusable is sticky after an append error whose rollback could not
	// establish the previous durable prefix. The journal bytes are then
	// uncertain, so no operation may acknowledge or consume more state until
	// a fresh store is opened against the on-disk bytes.
	unusable error
	closed   bool

	// foldGen advances every time the durable journal bytes the store has
	// accounted for change: on every successful append or batch append, on
	// every rollback or trailing-line repair that rewrites the file, and on
	// every load that observes bytes the cursor had not yet accounted for
	// (including foreign appends). It is the fold cache's invalidation key:
	// a cached fold is served only when its generation still matches.
	foldGen uint64
	folds   foldCache

	// journalSize and journalMod are the file this store last accounted for, and
	// journalGen counts the times that content was replaced by bytes the store
	// did not write. The baseline is sampled at Open, so even the first boundary
	// read can tell a journal that was cut in place from one this store grew.
	//
	// The rules, all guarded by mu:
	//   - an append moves the end forward from the length this store left, which
	//     is growth: same generation;
	//   - a stat shorter than journalSize is a truncation: new generation;
	//   - a stat of the same length with a different modification time is an
	//     in-place rewrite: new generation;
	//   - any of those checks that runs before an append or a rollback folds the
	//     answer in BEFORE the store moves the file itself, because afterwards the
	//     moved size and time are the store's own and cannot be told from a
	//     foreign writer's.
	//
	// The advance is permanent: the generation is a property of the journal's
	// content, not a one-shot flag a reader consumes. A fold built for the old
	// bytes stays invalid until it is rebuilt against the new ones, even if every
	// reader that noticed the change went away before publishing.
	journalSize int64
	journalMod  time.Time
	journalGen  uint64
	sampled     bool
	// laggingAppendEnd, when non-zero, is the end of an append this store made
	// durable that the filesystem's own metadata still under-reports. The
	// committed boundary reports it instead of the stale size, so a job that
	// just committed is not omitted from live activity until the attributes
	// catch up. It is cleared as soon as a stat agrees with what was written, and
	// never applied to a replacement: that is a different file, and this number
	// describes this one.
	laggingAppendEnd int64
	// named is the file the journal's NAME reached when the store last
	// accounted for it. It is compared with the next stat of the name, so a
	// replacement is one change rather than a change on every read: the new
	// file's own identity is stable, and it is the transition that moves the
	// content generation. Sampled at Open and after every write and repair.
	named os.FileInfo
	// contentTracked arms the pre-write content check.
	//
	// It is armed by Open, not by the first CommittedJournal call, and the
	// difference is a data-loss bug: a torn tail left by an out-of-band write
	// AFTER the store opened would otherwise meet an append that still thought
	// this journal was untouched. That append joins its event to the fragment
	// and fsyncs one malformed line, which every later read and every reopen
	// then fails on. Opening samples a baseline already, so the check costs one
	// comparison against a stat the append path takes anyway — and an unchanged
	// journal still takes the stat-only path.
	//
	// It is cleared only for a store that was never given a position to start
	// from, which is the fault-injection shape a handful of tests construct.
	contentTracked bool

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

	// pending, when set, is an append whose bytes were made durable but whose
	// arrival at the named journal could not be confirmed. The sequence number
	// it consumed is not handed out again until the journal itself answers
	// whether the record is there. See pendingWrite.
	pending    pendingWrite
	pendingSet bool
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
	// identity is the file the size and modification time above describe. It is
	// recorded from the sample the store vouched for — the post-sync sample on
	// an append, the load's own stat on a read — and never from a fresh stat of
	// the path, because a replacement installed in between would supply its own
	// numbers and the cursor would trust a prefix decoded from another file.
	// A nil identity means the cursor was recorded by a path that could not
	// supply one (a directly constructed store), and the size and time are then
	// the only evidence, as they were before.
	identity os.FileInfo
	valid    bool
	// disabled is sticky: set when this filesystem proved it cannot tell one of
	// the store's own writes from no write at all, after which the store always
	// re-reads the whole file.
	disabled bool
}

// pendingWrite is an append whose bytes this store made durable but could not
// confirm reached the named journal: the name was absent, or it reached a
// different file, when the post-sync check asked.
//
// It exists because the sequence number is a fact about the BYTES, not about the
// verification. The record is on disk with that Seq, so handing the same number
// to the next append puts two records with one sequence in the same journal
// whenever the file comes back — and the fold, which orders by Seq, then keeps
// whichever it met first. Remembering the write lets a later append ask the
// journal itself whether the record arrived.
type pendingWrite struct {
	// file is the journal the bytes were written to, as it was identified
	// before the write. A name that no longer reaches it cannot be asked.
	file os.FileInfo
	// offset is where the record starts, and line is every byte written there.
	offset int64
	// firstSeq and lastSeq bound the sequences this write assigned. They are
	// equal for a single append and differ for a batch, and the difference is
	// what a retry needs: a batch must restart at its own first number, not at
	// its last.
	firstSeq int64
	lastSeq  int64
	line     []byte
}

// reconcilePendingLocked resolves an append whose verification failed, before
// this store writes another byte.
//
// The record is durable somewhere with its sequence already assigned. Two
// questions follow, and only the journal can answer them:
//
//   - Does the named journal ALREADY hold this record? Then the append did
//     reach it after all — the name came back, carrying the same file — and the
//     sequence belongs to that record. Advance past it and let the caller
//     proceed with the next number.
//   - Is the record absent, in the file the name now reaches? Then the bytes
//     went somewhere a reader does not look. The caller's retry must write the
//     event, but it must write it with the sequence the first attempt already
//     used, or the journal ends up with two records sharing one number and the
//     fold keeps whichever it met first.
//
// A name that reaches a different file than the failed write went to is the
// second case: the bytes are not in the journal readers see, so the retry owns
// the sequence the failed attempt used. A name that is absent again can answer
// nothing, and the pending write is kept for the next attempt.
func (s *Store) reconcilePendingLocked() error {
	if !s.pendingSet {
		return nil
	}
	named, err := s.fs.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			// Still absent: nothing can be learned yet, and the sequence stays
			// reserved for whichever attempt learns the answer.
			return nil
		}
		return fmt.Errorf("%w: %s: %w", ErrJournalNameUnavailable, s.path, err)
	}
	// The bytes went to s.pending.file. Only that file can be asked whether the
	// record is there, and only when the name still reaches it.
	reached := s.pending.file != nil && sameJournalFile(s.pending.file, named)
	pending := s.pending
	s.pendingSet = false
	s.pending = pendingWrite{}
	if reached {
		if present, err := s.pendingRecordIsDurableLocked(named, pending); err != nil {
			return err
		} else if present {
			// The record arrived. Its sequence is spent on it, and the next
			// append continues past it rather than reusing the number.
			if pending.lastSeq > s.seq {
				s.seq = pending.lastSeq
			}
			return nil
		}
	}
	// The record is not in the journal readers see, so the retry must carry the
	// same sequence: it is the number this journal has not yet used.
	if s.seq >= pending.firstSeq {
		s.seq = pending.firstSeq - 1
	}
	return nil
}

// pendingRecordIsDurableLocked reports whether the journal the name reaches
// holds the record a failed append wrote, comparing the bytes at the offset
// that append recorded.
//
// The comparison is on bytes rather than on a decoded event: the question is
// whether THIS write landed, and two events can be equal as values while being
// different writes. A journal that has since been rewritten is asked about the
// bytes at that offset, which is why a mismatch means "not this record" rather
// than "corrupt".
func (s *Store) pendingRecordIsDurableLocked(named os.FileInfo, pending pendingWrite) (bool, error) {
	if pending.offset < 0 || int64(len(pending.line)) == 0 {
		return false, nil
	}
	if named.Size() < pending.offset+int64(len(pending.line)) {
		return false, nil
	}
	f, err := s.fs.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("jobstore: read %s: %w", s.path, err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, len(pending.line))
	if _, err := f.ReadAt(buf, pending.offset); err != nil {
		return false, nil
	}
	return bytes.Equal(buf, pending.line), nil
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
	watches   map[string]*WatchRecord
	sends     WatchSendRecord
	jobsOK    bool
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
	// Establish the content baseline before anything reads it. Opening repairs a
	// torn tail itself (readAllLocked below), so a journal that was cut
	// mid-record while no store was open must not look untouched to the first
	// boundary read after this one: without a baseline sampled here, that read
	// would have no earlier length to compare against, skip the repair, and let
	// the next append write onto the fragment.
	s.journalSize, s.journalMod, s.sampled = info.Size(), info.ModTime(), true
	s.named = info
	// This store now holds a position in a file it did not create on this call,
	// so an unexpected change to it is its business from the very next write.
	// See contentTracked.
	s.contentTracked = true
	if info.Size() == 0 {
		return s, nil
	}
	existing, err := s.readAllLocked()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	// readAllLocked repaired the tail if it needed repairing, so re-sample the
	// file the store will actually append to.
	if repaired, err := f.Stat(); err == nil {
		s.journalSize, s.journalMod = repaired.Size(), repaired.ModTime()
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
	named, namedErr := s.fs.Stat(s.path)
	s.verifyCursorBeforeAppendLocked(named, namedErr)
	adopted, err := s.followNamedJournalLocked(named, namedErr)
	if err != nil {
		return err
	}
	if !adopted {
		// The name still reaches the file this store holds, so a change it did
		// not write is this store's to repair before appending onto it.
		// Adoption re-samples its own baseline against the file it just read.
		if err := s.noteContentBeforeStoreWriteLocked(named, namedErr); err != nil {
			return err
		}
	}
	// An append that could not confirm its own arrival left a durable record
	// with a sequence already assigned. That sequence must not be handed to this
	// event until the journal answers whether the record is there.
	if err := s.reconcilePendingLocked(); err != nil {
		return err
	}
	nextSeq := s.seq + 1
	e.Seq = nextSeq
	// The identity of the journal this append will actually write to, sampled
	// before it writes a byte so the post-sync check can tell whether the name
	// moved while the bytes were in flight. Sampling it afterwards cannot answer
	// that: by then the name reaches whatever replaced the journal.
	//
	// It must be sampled AFTER adoption, not before it. Adoption can install a
	// second replacement — the name moved again while the first candidate was
	// being examined — and a sample taken before it describes the file that was
	// moved aside. Verifying against that sample reports "the journal was
	// replaced" for an append that landed in exactly the file the name reaches,
	// leaves s.seq unadvanced, and lets the next append reuse its sequence.
	appendedName := named
	if adopted {
		appendedName = s.named
	}
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
	verified, err := s.verifyAppendReachedTheNameLocked(appendedName)
	if err != nil {
		// The bytes are durable, so the sequence they carry is spent whether or
		// not a reader of the name can see them. Remember the write so the next
		// attempt asks the journal instead of reusing the number.
		s.pending = pendingWrite{file: appendedName, offset: startOffset, firstSeq: nextSeq, lastSeq: nextSeq, line: append([]byte(nil), b...)}
		s.pendingSet = true
		return err
	}
	s.seq = nextSeq
	s.noteAppendedLocked(startOffset, startOffset+int64(len(b)), verified)
	return nil
}

// verifyAppendReachedTheNameLocked checks, after the bytes are durable, that the
// journal's NAME still reaches the file they were written to.
//
// An append is only worth its success report if a reader of the name can see the
// record. A journal renamed aside and replaced while this append was syncing
// leaves the event in the moved file: readers follow the name to the replacement
// and never find it, and the store's own post-write stat would adopt the
// replacement as its baseline without noticing the jump. Reporting success there
// loses the event silently.
//
// So the name is re-resolved against the handle that was synced. A replacement,
// or a name that has gone, is a retryable failure: the bytes this append wrote
// describe content the name no longer reaches, so the event is reported as failed
// and the caller retries against whatever the name now holds. The generation
// advances here as well, because any fold built on the old file is now serving
// bytes a reader will never see.
//
// A name that cannot be examined at all is NOT treated as a replacement: there is
// no evidence the bytes went somewhere unread, and failing an append whose record
// is durable would be the worse error.
//
// The sample it verified is returned, and it is the one the caller books the
// append against. Re-statting after this returns asks the same question again a
// moment later, and the answer is allowed to have changed: a replacement landing
// in that gap would be installed as the append's own baseline without advancing
// the generation, and the next activity read would extend a fold built on the
// old bytes from an offset into the new file. A nil return means the name could
// not be examined, and the caller books no new baseline.
func (s *Store) verifyAppendReachedTheNameLocked(opened os.FileInfo) (os.FileInfo, error) {
	named, err := s.fs.Stat(s.path)
	if err != nil {
		// The name cannot be examined AFTER the bytes are durable.
		//
		// This is not the pre-write question. By the time this runs,
		// followNamedJournalLocked has established that the name reaches the
		// journal this store holds, so a stat that fails now is evidence about
		// the filesystem's responsiveness rather than about the journal's
		// identity: no observation has placed the bytes anywhere a reader will
		// not look.
		//
		// Reporting a retryable failure here is the worse error, and it is worse
		// in a way that loses data. A caller that retries re-derives the
		// terminal generation of the event it is writing, so the durable fold
		// keeps the FIRST terminal record and ignores the retry that carries the
		// second: the job is committed, the append was told it failed, and the
		// completion notification is dropped. A store that holes a journal for
		// one flaky stat trades a lost notification for a risk no evidence
		// supports.
		//
		// So the outcome is reconciled rather than assumed: one delayed re-stat
		// still catches a replacement that is merely slow to appear under the
		// name, and anything left is acknowledged. A store with no sample at all
		// — a fault-injection fixture, or one constructed around a handle it was
		// given — has never followed a name and has nothing to reconcile.
		retried, retryErr := s.fs.Stat(s.path)
		if retryErr == nil {
			if !sameJournalFile(opened, retried) {
				s.journalGen++
				s.sampled = false
				s.laggingAppendEnd = 0
				return nil, fmt.Errorf("%w: %s: the journal was replaced while the event was being written", ErrJournalReplaced, s.path)
			}
			return retried, nil
		}
		// The delayed re-stat failed too, and its failure splits in two.
		//
		// ABSENT is a definite answer, and it is the rename-aside case: the
		// journal was moved and its replacement has not been installed yet. The
		// bytes this append made durable are in the file nobody will open, so
		// acknowledging loses the event — and a shell finalizer told "written"
		// stops retrying a completion no reader will ever see. Absence is
		// retryable precisely because it says where the bytes went.
		//
		// Any OTHER failure is inconclusive: no observation has placed the bytes
		// anywhere a reader will not look. There the append is acknowledged with
		// no new baseline, so the next boundary read samples for itself rather
		// than trusting a comparison this call could not make. A store that has
		// never followed a name lands here too, for the same reason — there is
		// nothing to compare the bytes against.
		if os.IsNotExist(retryErr) {
			return nil, fmt.Errorf("%w: %s: %w", ErrJournalNameUnavailable, s.path, retryErr)
		}
		return nil, nil
	}
	// The name is compared with ITSELF, sampled before the write under the same
	// lock that serialized it. That is the only comparison that answers this
	// question without inventing one: a handle's metadata read next to the
	// name's disagrees on an ordinary append wherever the filesystem reports the
	// two through different paths (attributes that lag the data, timestamps too
	// coarse to move), and the answer this needs is "did the name move", not
	// "do two reporters agree".
	if opened == nil || sameJournalFile(opened, named) {
		return named, nil
	}
	// The name reaches a different file than the one this append synced to. The
	// bytes are durable in the old file — that is why this is retryable rather
	// than a rollback — but no reader of the name will find them.
	s.journalGen++
	s.sampled = false
	s.laggingAppendEnd = 0
	return nil, fmt.Errorf("%w: %s: the journal was replaced while the event was being written", ErrJournalReplaced, s.path)
}

// AppendBatch assigns contiguous Seq values to events, writes them as one
// append, and fsyncs once. If any marshal, write, or sync fails while the
// process is alive, the file is truncated back to the pre-batch offset and the
// store seq is left unchanged.
//
// That is returned-error atomicity, not crash atomicity: the events are
// independent newline-framed records, so a crash can leave any prefix of the
// batch complete. A surviving prefix is a valid log with its Seq values already
// assigned, and the next append continues after it. A caller whose batch has
// several effects must therefore be idempotent under re-run from any prefix;
// delegate shell repair is the production caller and completes a torn terminal
// prefix: it clears a terminal job's watches whether or not the notification
// record landed, without inventing a notification a kept-sync job never
// wanted. A reader must fold each record independently and never assume a
// batch's records arrive together.
func (s *Store) AppendBatch(events []Event) error {
	if len(events) == 0 {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			return nil
		}
		if s.unusable != nil {
			return s.unusable
		}
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	named, namedErr := s.fs.Stat(s.path)
	s.verifyCursorBeforeAppendLocked(named, namedErr)
	adopted, err := s.followNamedJournalLocked(named, namedErr)
	if err != nil {
		return err
	}
	if !adopted {
		if err := s.noteContentBeforeStoreWriteLocked(named, namedErr); err != nil {
			return err
		}
	}
	// See Append: an unconfirmed write already spent the sequences it carries.
	if err := s.reconcilePendingLocked(); err != nil {
		return err
	}
	startOffset, err := s.f.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("jobstore: seek append start: %w", err)
	}
	// The name's identity before this batch writes a byte; see Append.
	appendedName := named
	if adopted {
		appendedName = s.named
	}
	nextSeq := s.seq
	firstBatchSeq := s.seq + 1
	written := int64(0)
	var batchLine []byte
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
		batchLine = append(batchLine, b...)
		written += int64(len(b))
	}
	if err := s.syncLocked(); err != nil {
		return s.appendFailureLocked("sync event", err, startOffset)
	}
	verified, err := s.verifyAppendReachedTheNameLocked(appendedName)
	if err != nil {
		// The whole batch is durable under the sequences it was given; the next
		// attempt asks the journal whether it arrived rather than reusing them.
		s.pending = pendingWrite{file: appendedName, offset: startOffset, firstSeq: firstBatchSeq, lastSeq: nextSeq, line: batchLine}
		s.pendingSet = true
		return err
	}
	s.seq = nextSeq
	s.noteAppendedLocked(startOffset, startOffset+written, verified)
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
	// Fold copies and stable-sorts this view itself and retains none of it, and
	// the records it builds are deep-copied below before they leave this lock,
	// so the whole-history event copy readAllLocked makes buys nothing here.
	events, err := s.readEventsLocked()
	if err != nil {
		return nil, err
	}
	return s.noteFoldedJobsLocked(Fold(events)), nil
}

// CommittedJournalState is one snapshot of a store's committed journal.
type CommittedJournalState struct {
	// Path is the journal the store appends to.
	Path string
	// Size is the byte length of its committed prefix.
	Size int64
	// Generation identifies the journal CONTENT this snapshot describes. It
	// advances whenever the file stops being the one the store's own appends
	// produced — a truncation, or a same-length in-place rewrite — and never on
	// an ordinary append, so two snapshots of a growing journal share a
	// generation and a fold may be extended from one to the other.
	//
	// It is the cache key, not a flag. A reader that samples a new generation
	// and then dies before rebuilding has still made every later reader
	// compare against the new value, so the stale fold can never be served
	// again. A one-shot "this shrank" boolean cannot say that: the first
	// reader consumes it, and its canceled rebuild leaves the old fold
	// looking current to everyone who comes after.
	Generation uint64
	// MetadataResolvesWrites reports whether this store's own appends can be
	// told apart from someone else's write by length and modification time.
	//
	// It is false once the store has observed its own append change the file's
	// length while the filesystem reported the same modification time twice:
	// timestamps there cannot resolve a write, which is exactly what makes an
	// incremental reader unsafe. The store answers its own question by
	// re-reading (see fileCursor.disabled); a reader outside the store has no
	// cursor to disable, and its cache compares a size and a generation. On such
	// a filesystem neither moves for a same-length rewrite that lands in the
	// same timestamp tick, so a reader holding that flag must not reuse a cached
	// prefix — it has to refold.
	MetadataResolvesWrites bool
}

// CommittedJournal returns this store's journal path and the byte length of its
// committed prefix, or the same closed/unusable error every other operation
// reports.
//
// The length is the file's end as this store left it: Append and AppendBatch
// hold the store lock across their write, their fsync and any rollback
// truncation, so no append is ever observable at a length between the one this
// call returns and the one a later call returns. A reader that scans the file
// itself, rather than through the store, must fold no further than this offset:
// bytes past it belong to an append that has not committed, and one whose fsync
// then fails is truncated away without the store ever having acknowledged it.
// Folding through the boundary is what keeps a stale record from a failed
// append out of a page and out of the continuation that page minted.
//
// It reads nothing, so a caller that reads this journal repeatedly folds it
// incrementally instead of paying a clone and fold of the whole history per
// call — with one exception on the truncation path, described below.
//
// A truncation is repaired before it is reported. Something that cut this file
// in place can leave a torn trailing record: the bytes are there, the scanner
// omits the incomplete line, and the next Append would append onto its tail and
// fsync one malformed line where two records should be. So when the stat below
// shows the file is no longer the one this store left, the store runs its
// locked tail recovery — the same repair every full load performs — and reports
// the repaired length. The repair is confined to that path: a journal that is
// merely longer than the last sample is an ordinary append, and taking the fast
// path there is the whole reason this call reads nothing.
func (s *Store) CommittedJournal() (CommittedJournalState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return CommittedJournalState{}, err
	}
	// The NAMED journal, not the open handle. The handle describes the file the
	// store opened, which is not the file this path names once the path was
	// removed or atomically replaced: a reader scanning the path would then be
	// bounded by the old file's length, and a warm fold would keep serving rows
	// the path no longer has. A stat of the name answers for what a scan of the
	// name will read.
	info, err := s.fs.Stat(s.path)
	if err != nil {
		// The journal is gone from the name. Report it as an empty prefix for
		// the new generation rather than failing the read: the activity page
		// still has the session's live rows, and refusing the whole load for a
		// journal that is legitimately absent (a session that never wrote one,
		// or one being replaced) would hide them.
		if os.IsNotExist(err) {
			if s.sampled {
				s.journalGen++
			}
			s.sampled = false
			return CommittedJournalState{Path: s.path, Size: 0, Generation: s.journalGen, MetadataResolvesWrites: !s.cursor.disabled}, nil
		}
		return CommittedJournalState{}, fmt.Errorf("jobstore: stat %s: %w", s.path, err)
	}

	// Repairing is only this store's business while the name still reaches the
	// file it holds: recovery truncates through the handle, so on a replacement
	// it would cut the journal that was moved aside and leave the replacement
	// torn. Identity decides that before anything else.
	handle, handleErr := s.f.Stat()
	sameFile := handleErr == nil && sameJournalFile(handle, info)

	size, mod := info.Size(), info.ModTime()
	// Reconcile the reported length with what this store already made durable
	// BEFORE asking whether the content changed.
	//
	// An append records its own end, and a filesystem whose attributes lag
	// reports less than that for as long as the lag lasts. Comparing the raw
	// report against the recorded end therefore looks like a change on every
	// read of the same bytes — and treating that as a new generation invalidates
	// the fold each time, so a live session rebuilds its whole history on every
	// poll until the metadata catches up. The order is what fixes it: settle the
	// lag first, then compare. A sample that is merely behind the append is the
	// same content, and repeated lagging samples are the same content as each
	// other.
	//
	// The confirmation is the file's own length, the same arbiter the tail of
	// this function uses: metadata trailing the data and a real truncation both
	// report a smaller number, and only the file can say which one happened.
	lagging := false
	if s.laggingAppendEnd > size {
		onDisk, haveLength := s.journalRealSizeLocked()
		if haveLength && onDisk >= s.laggingAppendEnd {
			size, lagging = s.laggingAppendEnd, true
		} else {
			s.laggingAppendEnd = 0
		}
	}
	// One question decides both the generation and the repair: are these the
	// bytes this store last accounted for? Identity answers for the file, the
	// recorded length and time for its content. Anything else — a replacement,
	// a foreign append, a truncation, a rewrite — is content the store did not
	// write, and neither an incremental fold nor the cursor may extend across
	// it.
	//
	// The name's identity is compared with the file it reached LAST time, not
	// with the store's handle. A journal renamed aside and re-created under this
	// name is a different file, and the store's handle still describes the one
	// it opened; but comparing against the handle would call every later read of
	// that same replacement a new change and rebuild the fold on every poll. It
	// is the transition from one file to another that moves the generation.
	renamed := s.named == nil || !sameJournalFile(s.named, info)
	unexpected := renamed || !s.sampled || size != s.journalSize || !mod.Equal(s.journalMod)
	if renamed {
		// Whatever an earlier append said about its own end describes the bytes
		// that append wrote. A replacement is a different file, so that
		// statement is about content the name no longer reaches and must not
		// raise this boundary.
		s.laggingAppendEnd = 0
		lagging = false
	}
	if unexpected {
		// The generation moves for every unexplained change, whether the file
		// grew, shrank or stayed the same length. Growth is the case worth
		// naming: a rewrite that REPLACED the prefix and left the file longer
		// than the last recorded length looks like an append by length alone,
		// and resuming an incremental fold at the old offset would keep the rows
		// the rewrite removed and decode the rest from the middle of a record.
		// The cursor answers the same question the same way: anything short of
		// "same length, same modification time" is reread from zero.
		s.journalGen++
	}
	if sameFile && unexpected {
		// The name still reaches this store's file, so its bytes are this
		// store's to repair. A foreign writer can leave a partial record at the
		// end, and a partial record is poisonous here: the scanner withholds the
		// line it cannot decode, and the next Append joins a new event onto the
		// fragment and fsyncs one malformed line that every later read then
		// fails on. Repair the tail before reporting a length, the way a full
		// load always has.
		//
		// This runs on the unexpected-change path only. A live session's own
		// appends are recorded by noteAppendedLocked, so the stat above matches
		// what the store wrote and this branch is not taken: the incremental
		// read stays a stat.
		if err := s.recoverTrailingPartialLineLocked(); err != nil {
			return CommittedJournalState{}, err
		}
		if repaired, statErr := s.fs.Stat(s.path); statErr == nil {
			info = repaired
			size, mod = repaired.Size(), repaired.ModTime()
		}
	}
	s.journalSize, s.journalMod, s.sampled = size, mod, true
	s.named = info
	s.contentTracked = true
	// The append's recorded end stays authoritative while the path still reports
	// less than it. Once the path catches up it is no longer the better
	// evidence, and keeping it would let a later, genuine truncation be reported
	// at the old length — so it is dropped, and the comparison above falls back
	// to the path's own numbers.
	//
	// A lagging sample that the FILE contradicts was already cleared above,
	// before the generation was decided: the bytes are gone, and the generation
	// moved for it there.
	if !lagging {
		s.laggingAppendEnd = 0
	}
	return CommittedJournalState{Path: s.path, Size: size, Generation: s.journalGen, MetadataResolvesWrites: !s.cursor.disabled}, nil
}

// journalRealSizeLocked reports the journal's actual length, asking the file
// rather than the path's attributes. It is how a boundary resolves the one
// question a stat cannot: whether a length shorter than a confirmed append
// means the attributes have not caught up or the file was cut. ok is false when
// the file cannot be opened, in which case the caller has no evidence and must
// not invent a length.
func (s *Store) journalRealSizeLocked() (int64, bool) {
	f, err := s.fs.Open(s.path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	onDisk, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, false
	}
	return onDisk, true
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

// CheckRetirementReady validates that the primary journal is readable and every
// durable event decodes. It invalidates the tail cursor first so the check reads
// the original file bytes rather than a cached prefix, then rereads through the
// same path LoadEvents uses (never a cached fold), under the store's existing
// serialization boundary. It neither closes the store nor appends an event.
func (s *Store) CheckRetirementReady() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	s.resetCursorLocked()
	_, err := s.readAllLocked()
	return err
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

// readEventsLocked returns every durable event in the log as a view the store
// owns and the caller must only read, never mutate or retain past the call. It
// reads and decodes only the bytes appended since the previous call, replaying
// the rest from the tail cursor; the result is what a full reread of the file
// would produce.
//
// The view may alias the cursor's decoded events and any unterminated tail, so
// it is usable only while s.mu is held, and neither the slice nor the events in
// it may be mutated. Fold is the one production consumer. It copies the slice
// before sorting and so keeps no reference to it, but the records it returns
// still share the events' reference fields: applyEvent copies ExitCode,
// EndedAt, StructuredResult and StructuredResultValid out of a job_finished
// event by reference and clones none of them. A caller of Fold may therefore
// read those values but not write through them, and Fold's result may not reach
// a caller until noteFoldedJobsLocked deep-copies it. Every reader that hands
// events out uses readAllLocked instead.
func (s *Store) readEventsLocked() ([]Event, error) {
	tail, err := s.refreshCursorLocked()
	if err != nil {
		return nil, err
	}
	if len(s.cursor.events) == 0 && len(tail) == 0 {
		return nil, nil
	}
	// A tail is a fresh slice from advanceCursorLocked, so it is already the
	// caller's; only combining it with the prefix needs a new slice.
	if len(tail) == 0 {
		return s.cursor.events, nil
	}
	if len(s.cursor.events) == 0 {
		return tail, nil
	}
	events := make([]Event, 0, len(s.cursor.events)+len(tail))
	events = append(events, s.cursor.events...)
	events = append(events, tail...)
	return events, nil
}

// readAllLocked returns every durable event in the log as a private snapshot:
// every pointer, slice and map reachable from a returned event is its own, so a
// caller may mutate what it got without changing what a later read reports.
// This is the wrapper every public event reader uses; Load folds a borrowed
// view instead (see readEventsLocked).
func (s *Store) readAllLocked() ([]Event, error) {
	view, err := s.readEventsLocked()
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, nil
	}
	events := make([]Event, len(view))
	for i, e := range view {
		events[i] = cloneEvent(e)
	}
	return events, nil
}

// HasEvent reports whether any durable event satisfies match. It reads the same
// bytes LoadEvents would but visits the cached events in place instead of
// cloning the journal, so match must not retain or mutate the event.
func (s *Store) HasEvent(match func(*Event) bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return false, err
	}
	tail, err := s.refreshCursorLocked()
	if err != nil {
		return false, err
	}
	for _, events := range [][]Event{s.cursor.events, tail} {
		for i := range events {
			if match(&events[i]) {
				return true, nil
			}
		}
	}
	return false, nil
}

// refreshCursorLocked brings the tail cursor up to the end of the file, reading
// and decoding only the bytes appended since the previous call. Afterward
// cursor.events plus the returned unterminated trailing events hold every
// durable event, exactly what a full reread would produce.
func (s *Store) refreshCursorLocked() ([]Event, error) {
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
	s.cursor.identity = info
	s.cursor.valid = true
	return tail, nil
}

// cursorTrustedLocked reports whether the file is still the one the cursor was
// built from: same length, same modification time. Anything else — including a
// foreign append, which is cheap enough to reread whole — is not accounted for,
// so the cursor is dropped rather than extended over unverified bytes.
func (s *Store) cursorTrustedLocked(info os.FileInfo) bool {
	return s.cursor.valid && !s.cursor.disabled && s.cursorReachesLocked(info) &&
		info.Size() == s.cursor.size && info.ModTime().Equal(s.cursor.mod)
}

// cursorReachesLocked reports whether info still describes the file the cursor's
// prefix was read from.
//
// Length and modification time alone answer "did anything change", and on this
// filesystem they are the whole answer they can be — but they cannot answer
// "is this the same file". A replacement of exactly the expected length with a
// preserved timestamp is indistinguishable from no write at all by those
// numbers, and the prefix they vouch for was decoded from the file that was
// moved aside. Identity answers it where the filesystem exposes one, and
// sameJournalFile inherits the documented length-and-time residual where it
// does not.
func (s *Store) cursorReachesLocked(info os.FileInfo) bool {
	if s.cursor.identity == nil {
		return true
	}
	return sameJournalFile(s.cursor.identity, info)
}

// sameJournalFile reports whether named is the file handle refers to.
//
// It is the one question a boundary read has to answer before it repairs or
// reports anything: the store's handle describes the file it opened, and a
// journal that was renamed aside and re-created under the same path is two
// different files — one the store can still write, one every reader will read.
// Repairing through the handle in that case truncates the journal that was
// moved aside and leaves the replacement torn, so the identity comes first.
//
// Where the filesystem exposes a file identity (an OS-backed one does) this is
// that identity. Where it does not — afero's in-memory filesystems carry no
// Sys — it falls back to the length and modification time the cursor already
// relies on, and inherits the same documented residual: a replacement that
// reproduces both is indistinguishable from no change at all.
func sameJournalFile(handle, named os.FileInfo) bool {
	if handle == nil || named == nil {
		return false
	}
	if handle.Sys() != nil && named.Sys() != nil {
		return os.SameFile(handle, named)
	}
	return handle.Size() == named.Size() && handle.ModTime().Equal(named.ModTime())
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

// noteContentBeforeStoreWriteLocked advances the content generation when the
// file the store is about to write to is not the one it last accounted for.
//
// It runs before Append, AppendBatch and rollbackAppendLocked, for one reason:
// after any of them the file's size and modification time are the store's own,
// and a foreign rewrite that happened since the last boundary read becomes
// indistinguishable from the store's own change. A rewrite followed by an
// append before the next activity read is the concrete case — the fold would
// resume at its old offset over replaced content, keeping a job that was
// rewritten away and missing the one that replaced it.
//
// A failure to stat is not reported: the append below reports its own error, and
// an unreadable file simply cannot be shown to be unchanged, so the generation
// moves.
//
// A file this store did not write may end in a partial record. Appending at EOF
// would join the new event onto that fragment and fsync one malformed line,
// reporting success for an event no reader can decode and a record the file
// cannot recover. So a detected change is repaired the way a load repairs it,
// before the write and before the new baseline is recorded: the baseline may
// only describe bytes the store has accounted for, and a torn tail it has not
// yet cut is not one of them.
//
// Ordinary appends take the fast path: the store's own write is recorded by
// noteAppendedLocked, so the stat here matches what it wrote, `changed` is
// false, and no repair is attempted.
func (s *Store) noteContentBeforeStoreWriteLocked(info os.FileInfo, err error) error {
	if !s.contentTracked {
		return nil
	}
	if err != nil {
		if s.sampled {
			s.journalGen++
		}
		s.sampled = false
		return nil
	}
	size, mod := info.Size(), info.ModTime()
	// Reconcile a reported length that still trails a confirmed append BEFORE
	// asking whether the content changed, exactly as the boundary read does.
	//
	// An append records its own end, and a filesystem whose attributes lag
	// reports less than that for as long as the lag lasts. Comparing that raw
	// number with the recorded end therefore looks like a rewrite on every
	// append of the same bytes: the generation would advance each time and a
	// live session would refold its whole history after every append while the
	// lag lasts. The order is what fixes it — settle the lag first, then
	// compare.
	//
	// The confirmation is the file's own length, the same arbiter the boundary
	// uses: attributes trailing the data and a real truncation both report a
	// smaller number, and only the file can say which one happened. A length
	// the file contradicts clears the claim, so a genuine truncation still
	// moves the generation below.
	if s.laggingAppendEnd > size {
		if onDisk, haveLength := s.journalRealSizeLocked(); haveLength && onDisk >= s.laggingAppendEnd {
			size = s.laggingAppendEnd
		} else {
			s.laggingAppendEnd = 0
		}
	}
	changed := !s.sampled || size != s.journalSize || !mod.Equal(s.journalMod)
	if changed {
		// The file moved since the store last accounted for it. Anything that is
		// not this store's own append is a replacement of its content. The
		// pre-append check ran before this write, so a rewrite that happened
		// between them is the one case this cannot catch — and it cannot happen
		// under the store lock that serializes every writer this store knows
		// about.
		s.journalGen++
		// Repair before writing, and leave the baseline unrecorded if the repair
		// fails: a caller that retries must see the change again rather than a
		// baseline that claims bytes the store never verified.
		if err := s.recoverTrailingPartialLineLocked(); err != nil {
			s.sampled = false
			return err
		}
		if repaired, statErr := s.fs.Stat(s.path); statErr == nil {
			size, mod = repaired.Size(), repaired.ModTime()
			s.named = repaired
		} else {
			s.sampled = false
			return nil
		}
	}
	// The store's own write is about to move the file; the append path records
	// where it ends once that is known.
	s.journalSize, s.journalMod = size, mod
	s.sampled = true
	return nil
}

// verifyCursorBeforeAppendLocked drops the cursor unless the file is still the
// one it was built from. It runs BEFORE the store appends, because an append
// moves the file's identity forward: without this check a foreign rewrite that
// happened since the last load would be laundered into the cursor's own history
// and the stale prefix trusted forever. Agent tests rewrite a delegate's log in
// place and then append to it, so this is a live path, not a hypothetical.
func (s *Store) verifyCursorBeforeAppendLocked(info os.FileInfo, err error) {
	if !s.cursor.valid {
		return
	}
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
//
// verified is the sample the post-sync check resolved and vouched for, and it is
// the only one this may install as the append's baseline. Statting the path here
// would ask the same question a moment later, and a replacement arriving in
// between would be recorded as the append's own end: the generation would not
// move, so the next activity read would extend a fold built on the old bytes from
// an offset into the new file, and pages would blend two files. A nil sample
// means nothing was verified, so no baseline is recorded and the next read
// re-samples for itself.
func (s *Store) noteAppendedLocked(startOffset, endOffset int64, verified os.FileInfo) {
	// Every append changes the durable bytes, so the fold generation moves on
	// all paths below — including the early returns that drop the cursor. A fold
	// cached before this append must never be served after it, whether or not
	// the cursor survived. The CONTENT generation is different: this write is
	// the store's own growth of a file it already accounted for, so a fold built
	// on the previous length stays valid and is extended, not rebuilt.
	s.foldGen++
	s.folds = foldCache{gen: s.foldGen}
	if verified != nil {
		s.journalSize, s.journalMod, s.sampled = verified.Size(), verified.ModTime(), true
		s.named = verified
		// The append computed endOffset from the bytes it wrote; the stat above
		// asked a filesystem whose attributes may lag its data. Those two are
		// not equally good evidence about THIS write: the path says "as of some
		// moment that may predate my sync", the append says "exactly here". When
		// the path reports less than the store just made durable, the boundary
		// must report the append's end, or the record that just committed is
		// omitted until the metadata catches up.
		s.laggingAppendEnd = 0
		if verified.Size() < endOffset {
			s.journalSize = endOffset
			s.laggingAppendEnd = endOffset
		}
	} else {
		// Nothing was verified after the bytes were made durable under the
		// handle. That is not this append's failure — the bytes are on disk — but
		// it does mean the next boundary read has nothing to compare against, so
		// it must not pretend the file is unchanged.
		s.sampled = false
		s.laggingAppendEnd = endOffset
	}
	if !s.cursor.valid {
		return
	}
	if startOffset != s.cursor.size {
		// The file grew or shrank behind the store's back before this append.
		s.cursor.valid = false
		return
	}
	// The sample the post-sync check RESOLVED and vouched for is the only one
	// that may become the cursor's baseline. Statting the path here would ask
	// the same question a moment later, and a same-length replacement installed
	// in between would be recorded as this append's own end: the identity would
	// not move, so the next load would trust a prefix decoded from the file that
	// was moved aside and parse the replacement's suffix onto it.
	if verified == nil {
		// Nothing was verified, so there is no sample that describes the bytes
		// this cursor's prefix belongs to. Drop it rather than adopt whatever
		// the path says now.
		s.cursor.valid = false
		return
	}
	if verified.Size() != endOffset {
		// The filesystem does not report the bytes just written — stale or cached
		// metadata, or a concurrent writer. Its numbers cannot be used to decide
		// what has already been read, so the cursor goes.
		s.cursor.valid = false
		return
	}
	if endOffset != startOffset && verified.ModTime().Equal(s.cursor.mod) {
		// This append changed the file's length and the filesystem reported the
		// same modification time as before it: timestamps here cannot resolve a
		// write, so they cannot be used to tell the store's own bytes from
		// someone else's. Give the cursor up for good rather than trust it.
		s.cursor.disabled = true
		s.cursor.valid = false
		return
	}
	s.cursor.size = verified.Size()
	s.cursor.mod = verified.ModTime()
	s.cursor.identity = verified
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
	// Identity FIRST, size second, and both from the file this function is about
	// to probe and possibly cut.
	//
	// Reading the PATH's size and then seeking the HELD handle is what fails on
	// an empty journal whose name has been replaced by a nonempty one: the
	// path reports a positive size, the held handle is empty, and the seek to
	// one byte before the end fails — so a load refuses a perfectly healthy
	// replacement for a journal it never needed to touch. The two numbers have to
	// describe the same file, and only the handle describes the file this
	// function can repair.
	named, statErr := s.fs.Stat(s.path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return nil
		}
		return fmt.Errorf("%w: %s: %w", ErrJournalNameUnavailable, s.path, statErr)
	}
	f := s.f
	info, handleErr := f.Stat()
	if handleErr != nil {
		return fmt.Errorf("jobstore: stat %s: %w", s.path, handleErr)
	}
	if !sameJournalFile(info, named) {
		// The name reaches a different file. This store's tail is not the one a
		// reader will see, so repairing it would edit a file nobody reads, and
		// the replacement is someone else's to examine.
		return nil
	}
	if info.Size() == 0 {
		return nil
	}
	// Both halves of a repair are performed on the handle the store appends
	// through, never on the path.
	//
	// Reading the path for evidence and then truncating the handle is how a
	// second replacement gets its bytes used to justify a cut performed on the
	// first one. A replacement holding a single torn record supplies offset
	// zero, and offset zero erases every committed record from the journal that
	// was moved aside — a journal that had made all of it durable.
	//
	// So the evidence and the repair are the same file by construction, and the
	// name is rechecked before the cut: bytes repaired in a file the name no
	// longer reaches are bytes no reader will ever see, and the replacement
	// needs examining on its own terms instead.
	if _, err := f.Seek(-1, io.SeekEnd); err != nil {
		return fmt.Errorf("jobstore: inspect trailing byte: %w", err)
	}
	last := make([]byte, 1)
	if _, err := io.ReadFull(f, last); err != nil {
		return fmt.Errorf("jobstore: read trailing byte: %w", err)
	}
	// The read left the handle off its end; every exit from here restores it,
	// because Append seeks to the end before writing and a half-positioned
	// handle is the kind of state a later writer trusts.
	defer func() {
		if _, seekErr := s.f.Seek(0, io.SeekEnd); seekErr != nil && err == nil {
			err = fmt.Errorf("jobstore: seek after trailing recovery: %w", seekErr)
		}
	}()
	if last[0] == '\n' {
		return nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("jobstore: inspect %s: %w", s.path, err)
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("jobstore: read %s: %w", s.path, err)
	}
	// The whole tail is now in memory and the cut below is about to happen. The
	// name was resolved against this handle at the top of this function, and the
	// handle is the same file it was then: every read above is through it, and
	// nothing between there and here can move the name onto another file without
	// that file being a different handle. That single check is what keeps a
	// second replacement from supplying the evidence for a cut on the first one.
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
	if s.unusable != nil {
		return s.unusable
	}
	return nil
}

// ErrJournalReplaced is returned by Append and AppendBatch when the journal's
// name reaches a file that is not a usable journal, so writing would either
// land in a file nobody reads or corrupt the one that was put there.
//
// It is the backstop on the adoption path, not the common case: a replacement
// that is a well-formed journal is adopted and the append proceeds, because
// following the name the store was opened with is the whole guarantee. A
// replacement that cannot be decoded is not adoptable — appending to it would
// write after a record no reader can parse — and the store will not silently
// keep writing to the file it holds instead, because that write is invisible to
// every reader of the name.
var ErrJournalReplaced = errors.New("jobstore: journal name refers to a different file that is not a readable journal")

// ErrJournalNameUnavailable is returned by Append and AppendBatch when the
// journal's path does not currently name a reachable file, so the append has
// nowhere to land that any reader would find.
//
// It is retryable and the event is not consumed: rename-and-replace is not
// atomic, and a writer that looks in the gap between the rename and the
// creation of the replacement sees no name at all. Failing here is the only
// answer that preserves the guarantee this store makes — that a successful
// append is readable from the journal's name. Succeeding on the handle the
// store still holds would report a job completion that never reached the file
// activity and reopen read, which is a lost record, not a delayed one.
var ErrJournalNameUnavailable = errors.New("jobstore: journal name is not reachable")

// followNamedJournalLocked makes the store write to the file its journal name
// reaches, adopting a replacement that arrived while the store was open.
//
// A journal renamed aside and re-created under its path is two files: the store
// can still write the one it opened, and every reader reads the other. Writing
// to the held file would append where no reader looks — every later event lost
// from activity and from every load — and the store's tail recovery would
// truncate the copy that was moved aside. So the name wins.
//
// The name must exist for the append to proceed at all. A rename-and-replace
// leaves a window where the name reaches nothing; an append that took the
// handle in that window would report success for an event the name never holds.
// That window is retryable, and saying so is what makes waiting it out safe.
//
// Reassigning the handle is not a new hazard: every read and write of s.f
// happens under s.mu, this call included, so an append can never be seeking a
// handle that is being swapped. Nothing outside this file holds s.f.
//
// The caller passes the stat it already took for the pre-append checks, so
// following the name costs the append path no extra filesystem call. A store
// whose name still reaches the file it holds does no further work here.
//
// It reports whether it adopted a replacement, because adoption re-samples the
// content baseline itself and the caller must not then apply a comparison made
// against the file that is no longer named.
func (s *Store) followNamedJournalLocked(named os.FileInfo, err error) (bool, error) {
	if err != nil {
		// The name reaches nothing, or cannot be examined. Either way there is no
		// known file for this event to land in that a reader would see, so the
		// append must not proceed on the held handle.
		//
		// This is never first use: opening a store creates the name, so a name
		// that has stopped answering was removed or made unreadable after this
		// store opened. The event is preserved by failing — the caller retries
		// once the path is back — because the alternative is reporting success
		// for a record no reader of the named journal will ever see.
		return false, fmt.Errorf("%w: %s: %w", ErrJournalNameUnavailable, s.path, err)
	}
	if s.journalNameIsHeldLocked(named) {
		return false, nil
	}
	// A rename-and-replace is not atomic, and a second one can land while this
	// adoption is being prepared. Each attempt is reconciled against the name
	// before anything is installed, so a candidate that stopped being the named
	// file is dropped rather than appended through; the retry then follows
	// whatever the name reaches now.
	const adoptionAttempts = 3
	for range adoptionAttempts {
		// The name's identity at the moment this candidate was resolved, so the
		// check after repair can tell whether the name moved while the candidate
		// was being examined.
		opened, openedErr := s.fs.Stat(s.path)
		if openedErr != nil {
			return false, fmt.Errorf("%w: %s: %w", ErrJournalNameUnavailable, s.path, openedErr)
		}
		f, err := s.fs.OpenFile(s.path, os.O_RDWR|os.O_APPEND, 0o644)
		if err != nil {
			return false, fmt.Errorf("%w: open %s: %w", ErrJournalReplaced, s.path, err)
		}
		// Validation and repair both run on this handle, never through the path.
		// Reading the path for evidence and then repairing through the handle is
		// how a second replacement gets its bytes used to decide a truncation
		// performed on the first one — a journal that committed every byte of it
		// loses content it had already made durable.
		events, err := inspectAdoptableJournal(f, s.disableSync)
		if err != nil {
			_ = f.Close()
			return false, fmt.Errorf("%w: %s: %w", ErrJournalReplaced, s.path, err)
		}
		// Repair has already written through this handle, so the name must still
		// identify it before the store appends through it: bytes repaired in a
		// file the name no longer reaches are bytes no reader will ever see.
		named, statErr := s.fs.Stat(s.path)
		if statErr != nil {
			_ = f.Close()
			return false, fmt.Errorf("%w: %s: %w", ErrJournalNameUnavailable, s.path, statErr)
		}
		// The name is compared with itself, not with the handle. The question is
		// whether the name still reaches the file it reached when this candidate
		// was opened, and a filesystem without file identity can only answer it
		// by way of the metadata it reported then. Comparing the name's metadata
		// against the handle's would instead ask whether two reporting paths
		// agree, which they do not on a store whose attributes lag or whose
		// timestamps are coarse.
		if !sameJournalFile(named, opened) {
			_ = f.Close()
			continue
		}
		old := s.f
		s.f = f
		s.seq = 0
		for _, e := range events {
			if e.Seq > s.seq {
				s.seq = e.Seq
			}
		}
		// Everything cached describes the file that is no longer named. Reset
		// the cursor, move both generations, and let the live folds refold from
		// zero rather than resume at an offset into different bytes.
		s.resetCursorLocked()
		s.foldGen++
		s.folds = foldCache{gen: s.foldGen}
		s.journalGen++
		// The stat is taken after the repair above, so it describes the bytes
		// this store is about to append after rather than the torn tail.
		s.journalSize, s.journalMod, s.sampled = named.Size(), named.ModTime(), true
		s.named = named
		if err := old.Close(); err != nil {
			return true, fmt.Errorf("jobstore: close replaced %s: %w", s.path, err)
		}
		return true, nil
	}
	return false, fmt.Errorf("%w: %s: the name kept reaching a new file while it was being adopted", ErrJournalReplaced, s.path)
}

// journalNameIsHeldLocked reports whether the journal name still reaches the
// file the store holds.
//
// The handle's own identity answers this wherever the filesystem exposes one.
// An in-memory filesystem exposes none, so there the name is compared against
// the store's own last sample of that name — the same comparison a boundary read
// uses to decide the journal was replaced. Both questions have to be asked in
// the filesystem's own terms: comparing a handle's metadata against a name's
// metadata from two different reporting paths invents a difference that is not
// a replacement, and acting on it re-opens the path and swaps the handle this
// store appends through on every write.
//
// A store with no sample at all — one constructed directly rather than opened,
// which is what a fault-injection fixture does — has nothing to compare the
// name with except the handle it holds. Asking the handle is the honest answer
// there: it is the only evidence of which file this store is following, and it
// keeps the append on the handle the caller handed over instead of silently
// replacing it with a fresh open.
func (s *Store) journalNameIsHeldLocked(named os.FileInfo) bool {
	handle, err := s.f.Stat()
	if err != nil {
		return true
	}
	if handle.Sys() != nil && named.Sys() != nil {
		return os.SameFile(handle, named)
	}
	if s.named != nil {
		return sameJournalFile(s.named, named)
	}
	return sameJournalFile(handle, named)
}

// inspectAdoptableJournal validates the open candidate f and repairs the one
// thing a journal may legitimately need repaired, reading and writing that same
// handle throughout. It returns every durable event f holds.
//
// The rule is about which bytes are evidence. Every newline-TERMINATED record is
// content the journal claims to have committed, so a record that does not decode
// condemns the file: it is not a journal, and adopting it would append after a
// line no reader can parse. The unterminated LAST line is a write that was cut
// off — terminated when it is a complete record, cut back to the last committed
// newline when it is a fragment of one.
//
// The order matters because repair mutates. Every committed record is decoded
// before a byte is written, so refusing an unreadable replacement never rewrites
// the file it refused. And the handle, not the path, is what both steps see: a
// replacement installed while this candidate was being examined would otherwise
// supply the evidence for a truncation performed through this handle, and the
// journal that was moved aside would lose content it had committed.
func inspectAdoptableJournal(f afero.File, disableSync bool) ([]Event, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek %s: %w", f.Name(), err)
	}
	sc := bufio.NewScanner(f)
	sc.Split(scanLinesKeepingTerminator)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var (
		events []Event
		// committed is the offset just past the last newline, which is both the
		// end of the last trusted record and where a fragment gets cut back to.
		committed int64
		terminate bool
		truncate  int64 = -1
		lineNo    int
	)
	for sc.Scan() {
		raw := sc.Bytes()
		lineNo++
		line := trimLineTerminator(raw)
		terminated := len(raw) > 0 && raw[len(raw)-1] == '\n'
		if len(line) == 0 {
			if terminated {
				committed += int64(len(raw))
			}
			continue
		}
		var e Event
		err := json.Unmarshal(line, &e)
		switch {
		case err == nil && terminated:
			events = append(events, e)
			committed += int64(len(raw))
		case err == nil:
			// The record reached the file but its terminator did not. Adding the
			// newline is what commits it, exactly as tail recovery does.
			events = append(events, e)
			terminate = true
		case !terminated && isIncompleteTrailingJSON(line, err):
			truncate = committed
		default:
			return nil, fmt.Errorf("parse event line %d: %w", lineNo, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", f.Name(), err)
	}
	if truncate >= 0 {
		if err := f.Truncate(truncate); err != nil {
			return nil, fmt.Errorf("truncate trailing partial line: %w", err)
		}
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return nil, fmt.Errorf("seek end %s: %w", f.Name(), err)
	}
	if truncate >= 0 || terminate {
		if terminate {
			if _, err := f.Write([]byte{'\n'}); err != nil {
				return nil, fmt.Errorf("terminate trailing event: %w", err)
			}
		}
		if !disableSync {
			if err := f.Sync(); err != nil {
				return nil, fmt.Errorf("sync trailing recovery: %w", err)
			}
		}
	}
	return events, nil
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
		s.unusable = fmt.Errorf("jobstore: store unusable after failed rollback: %w", errors.Join(err, rollbackErr))
		return fmt.Errorf("jobstore: %s: %w; rollback failed: %w", operation, err, rollbackErr)
	}
	return fmt.Errorf("jobstore: %s: %w", operation, err)
}

func (s *Store) rollbackAppendLocked(startOffset int64) error {
	// The file's length and modification time both moved in ways the cursor did
	// not record; the next load re-reads from byte zero.
	s.invalidateCursorLocked()
	// invalidateCursorLocked moves the FOLD generation, which is right: a load
	// cached past a rollback must re-read. The CONTENT generation is a different
	// question, and the answer is that a rollback replaces nothing: it restores
	// the committed prefix the reader was already shown at startOffset. So the
	// baseline goes back to the pre-append length rather than forward, and no
	// reader sees a new generation, a rebuilt fold, or a refused continuation
	// for rows that never moved. The modification time is re-sampled after the
	// truncation below, because the rollback itself moves it.
	s.journalSize = startOffset
	truncateErr := s.f.Truncate(startOffset)
	_, seekErr := s.f.Seek(0, io.SeekEnd)
	syncErr := error(nil)
	if truncateErr == nil && seekErr == nil {
		syncErr = s.syncLocked()
	}
	if info, err := s.fs.Stat(s.path); err == nil {
		// The rollback truncated THIS store's handle. Recording the path's
		// numbers as the restored baseline is only honest while the path still
		// reaches that handle: a journal replaced during the rollback is a
		// different file, and adopting its length and time leaves a baseline
		// that describes content no reader will see — with the generation
		// unmoved, so a fold built on the old bytes stays citable.
		if handle, handleErr := s.f.Stat(); handleErr == nil && sameJournalFile(handle, info) {
			s.journalSize, s.journalMod, s.sampled = info.Size(), info.ModTime(), true
			s.named = info
		} else {
			s.sampled = false
			s.named = nil
		}
	} else {
		s.sampled = false
		s.named = nil
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
	if s.closed {
		return ErrStoreClosed
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
// decoded by encoding/json (see cloneJSONValue). Background is copied with
// the struct value. Phase and LastActivity are live-only; LastActivity is
// cloned defensively in case a caller sets it on a returned record.
func cloneFoldJobs(in map[string]*JobRecord) map[string]*JobRecord {
	if in == nil {
		return nil
	}
	out := make(map[string]*JobRecord, len(in))
	for k, r := range in {
		out[k] = r.Clone()
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
