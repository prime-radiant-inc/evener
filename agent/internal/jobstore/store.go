package jobstore

import (
	"bufio"
	"bytes"
	"context"
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
	//     in-place rewrite: new generation.
	//
	// The advance is permanent: the generation is a property of the journal's
	// content, not a one-shot flag a reader consumes. A fold built for the old
	// bytes stays invalid until it is rebuilt against the new ones, even if every
	// reader that noticed the change went away before publishing.
	journalSize int64
	journalMod  time.Time
	journalGen  uint64
	sampled     bool
	// observedSize, observedMod and observed are the READER's baseline: the name's
	// last accounted-for length and time, kept independently of whether the
	// writer's handle could be sampled.
	//
	// They are separate from sampled on purpose. sampled answers "may this store's
	// own append be told apart from a foreign write", which is a claim about the
	// HANDLE and is what the append path spends. Whether two boundary samples
	// describe the same content is a claim about the NAME, and it must not inherit
	// the handle's failures: a handle stat that cannot be taken leaves the writer
	// unverified without saying anything at all about whether the journal's bytes
	// changed. Inferring "changed" from "unverified" makes every read invalidate
	// the fold, so a reader on a journal that is perfectly readable spins until
	// its request is canceled.
	observedSize int64
	observedMod  time.Time
	observed     bool
	// laggingAppendEnd, when non-zero, is the end of an append this store made
	// durable that the filesystem's own metadata still under-reports. The
	// committed boundary reports it instead of the stale size, so a job that
	// just committed is not omitted from live activity until the attributes
	// catch up. It is cleared as soon as a stat agrees with what was written, and
	// never applied to a replacement: that is a different file, and this number
	// describes this one.
	laggingAppendEnd int64
	// named is the file the journal's NAME reached when the store last accounted
	// for it. It is compared with the next stat of the name, so a replacement is
	// one change rather than a change on every read: the new file's own identity
	// is stable, and it is the transition that moves the content generation.
	named os.FileInfo
	// metadataLagging records that this filesystem has DEMONSTRATED it cannot
	// resolve a write with its metadata: an append reported a length that did not
	// include the bytes that append had just made durable, or changed the length
	// without moving the modification time.
	//
	// It is separate from cursor.disabled on purpose. The cursor is one consumer
	// of that judgement, and it can be invalidated for unrelated reasons —
	// including by the very lagging sample that establishes this — so inferring
	// the judgement from the cursor's survival loses it exactly when it was just
	// proven. A fold reader asks whether a length difference can vouch for a
	// prefix's bytes, and that answer must not depend on whether the cursor lived.
	metadataLagging bool

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
	// Establish the content baseline before anything reads it. Opening repairs a
	// torn tail itself (readAllLocked below), so a journal that was cut
	// mid-record while no store was open must not look untouched to the first
	// boundary read after this one: without a baseline sampled here, that read
	// would have no earlier length to compare against and would report the cut
	// as ordinary growth.
	s.journalSize, s.journalMod, s.sampled = info.Size(), info.ModTime(), true
	s.observedSize, s.observedMod, s.observed = info.Size(), info.ModTime(), true
	s.named = info
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
		s.observedSize, s.observedMod = repaired.Size(), repaired.ModTime()
		s.named = repaired
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
	// The file as this append found it, before any of its own bytes exist. The
	// boundary books its baseline from the POST-write sample, so without this
	// one an intervening truncation or in-place rewrite would be overwritten
	// rather than noticed and the next boundary read would compare against
	// bytes this append wrote. See noteCommittedBoundaryLocked.
	pre, _ := s.f.Stat()
	b = append(b, '\n')
	if err := s.writeLineLocked(b); err != nil {
		return s.appendFailureLocked("write event", err, startOffset)
	}
	if err := s.syncLocked(); err != nil {
		return s.appendFailureLocked("sync event", err, startOffset)
	}
	s.seq = nextSeq
	// The handle's own sample, taken now that the bytes are durable. It is the
	// boundary's baseline, and it is weaker evidence than a name verification
	// would be: it says what the file this store holds reports, not that the
	// journal's NAME still reaches that file. See CommittedJournal.
	post, _ := s.f.Stat()
	named, _ := s.fs.Stat(s.path)
	s.noteAppendedLocked(startOffset, startOffset+int64(len(b)))
	s.noteCommittedBoundaryLocked(startOffset, startOffset+int64(len(b)), appendSamples{pre: pre, post: post, named: named})
	return nil
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
	s.verifyCursorBeforeAppendLocked()
	startOffset, err := s.f.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("jobstore: seek append start: %w", err)
	}
	// See Append: the pre-write sample is what lets the boundary tell this
	// append from one that landed after someone else changed the file.
	pre, _ := s.f.Stat()
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
	// See Append: the handle's own sample books the boundary's baseline.
	post, _ := s.f.Stat()
	named, _ := s.fs.Stat(s.path)
	s.noteAppendedLocked(startOffset, startOffset+written)
	s.noteCommittedBoundaryLocked(startOffset, startOffset+written, appendSamples{pre: pre, post: post, named: named})
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

// CommittedJournalState is one snapshot of a store's committed journal.
type CommittedJournalState struct {
	// Path is the journal the store appends to.
	Path string
	// Size is the byte length of its committed prefix.
	Size int64
	// Generation identifies the journal CONTENT this snapshot describes. It
	// advances whenever the file stops being the one the store's own appends
	// produced — a truncation, a same-length in-place rewrite, or a
	// replacement — and never on an ordinary append, so two snapshots of a
	// growing journal share a generation and a fold may be extended from one
	// to the other.
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
	// length while the filesystem reported the same modification time twice, or
	// report a length that did not include the bytes just made durable:
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
// call — with one exception on the unexpected-change path, described below.
//
// A torn tail is repaired before it is reported. Something that cut this file
// can leave a partial trailing record: the bytes are there, the scanner omits
// the incomplete line, and the next Append would append onto its tail and fsync
// one malformed line where two records should be. So when the stat below shows
// the file is no longer the one this store left, the store runs its locked tail
// recovery — the same repair every full load performs — and reports the
// repaired length. The repair is confined to that path: a journal that is
// merely longer than the last sample is an ordinary append, and taking the fast
// path there is the whole reason this call reads nothing.
//
// THE BASELINE IS THE HANDLE'S OWN, AND THAT IS WEAKER THAN NAME VERIFICATION.
// The baseline this call books is the sample the append path took of the file
// the store writes through, and this call compares the NAME's stat against it.
// That is enough to detect a truncation, an in-place rewrite, or a replacement
// between two boundary reads — every one of them moves the length, the
// modification time, or the file identity of the name. It is NOT enough to
// detect a journal renamed aside and replaced by a file with the same length
// and modification time during a single append, because nothing here re-checks
// after the sync that the name still reaches the synced handle, and nothing
// remembers a sequence number whose bytes went somewhere a reader will not
// look. That stronger property is the separately filed bug; this seam
// deliberately does not take it on, and the repair below only runs while the
// name still reaches this store's handle for the same reason.
//
// ctx bounds the one operation here that is not a stat: repairing a torn tail on
// a changed journal, which searches backward from EOF for the last newline. Work
// under this lock blocks every append to the journal, so a caller that abandons a
// read must be able to stop it. A nil ctx is treated as unbounded, which is what
// a caller with no request to respect has.
func (s *Store) CommittedJournal(ctx context.Context) (CommittedJournalState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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
			return CommittedJournalState{
				Path: s.path, Size: 0, Generation: s.journalGen,
				MetadataResolvesWrites: !s.cursor.disabled && !s.metadataLagging,
			}, nil
		}
		return CommittedJournalState{}, fmt.Errorf("jobstore: stat %s: %w", s.path, err)
	}

	// Repairing is only this store's business while the name still reaches the
	// file it holds: recovery truncates through the handle, so on a replacement
	// it would cut the journal that was moved aside and leave the replacement
	// torn.
	handle, handleErr := s.f.Stat()
	handleSampled := handleErr == nil
	sameFile := handleSampled && sameJournalFile(handle, info)

	size, mod := info.Size(), info.ModTime()
	// Reconcile the reported length with what this store already made durable
	// BEFORE asking whether the content changed.
	//
	// An append records its own end, and a filesystem whose attributes lag
	// reports less than that for as long as the lag lasts. Comparing the raw
	// report against the recorded end therefore looks like a change on every
	// read of the same bytes — and treating that as a new generation
	// invalidates the fold each time, so a live session rebuilds its whole
	// history on every poll until the metadata catches up. The order is what
	// fixes it: settle the lag first, then compare. A sample that is merely
	// behind the append is the same content, and repeated lagging samples are
	// the same content as each other.
	//
	// The confirmation is the file's own length: metadata trailing the data and
	// a real truncation both report a smaller number, and only the file can say
	// which one happened.
	lagging := false
	// measured is the file's real EOF, when this call took it. It outranks the
	// path's attributes for the rest of this sample, repair included: the repair
	// path re-stats the NAME, and a name whose attributes lag reports the length
	// from before the append — so a restat after a partial truncation would put
	// back the pre-append size over a file that is genuinely shorter, hiding the
	// rows that survived and resurrecting ones that did not.
	measured, haveMeasured := int64(0), false
	if s.laggingAppendEnd > size {
		onDisk, haveLength := s.journalRealSizeLocked()
		measured, haveMeasured = onDisk, haveLength
		switch {
		case !haveLength:
			// Nothing measured the file, so nothing here can tell lag from a
			// truncation. Drop the claim and let the ordinary change test run:
			// an unsampled answer must not be spent as a favourable one.
			s.laggingAppendEnd = 0
		case onDisk == s.laggingAppendEnd:
			// The file is exactly as long as the append confirmed. The attributes
			// trail it and the bytes are there: substitution is honest.
			size, lagging = s.laggingAppendEnd, true
		default:
			// The file is a different length from the append's confirmed end.
			// That is not lag — lag reports the SAME bytes with older attributes
			// — so this is an external change, and it must be reported and
			// repaired as one rather than hidden behind the confirmed end. The
			// measured length wins and the claim is dropped, so the change test
			// below sees a difference and inspects the tail.
			//
			// The measured length keeps winning through the repair below. A partial
			// truncation of an appended batch lands exactly here: the EOF sits
			// between the path's stale pre-append size and the batch's confirmed
			// end, and the surviving prefix of the batch is still durable and must
			// stay foldable.
			//
			// The claim is RE-ANCHORED to the measured end rather than cleared. The
			// name is still reporting less than the file holds, and that stays true
			// on the next read: clearing the claim makes each later sample trust the
			// stale attribute again, so the boundary oscillates between the real
			// length and the pre-batch one — and every read that answers with the
			// stale length hides the rows the truncation left behind. The file's EOF
			// is the evidence, and it stays the evidence until the name agrees with
			// it.
			s.laggingAppendEnd = onDisk
			lagging = true
			size = onDisk
		}
	}
	// One question decides both the generation and the repair: are these the
	// bytes this store last accounted for? Identity answers for the file, the
	// recorded length and time for its content. Anything else — a replacement,
	// a foreign append, a truncation, a rewrite — is content the store did not
	// write, and an incremental fold may not extend across it.
	renamed := s.named == nil || !sameJournalFile(s.named, info)
	if renamed && lagging {
		// The name reports less than the append confirmed, but opening the name
		// returned exactly the confirmed byte count: the name still reaches this
		// store's file and only its attributes trail the data. That is lag, not a
		// replacement, and reading it as one would throw away the confirmed end
		// and cap the boundary at the stale length — hiding a job the append
		// already made durable until the attributes caught up.
		renamed = false
	}
	// The change test is the READER's: the name's own last-accounted-for length
	// and time. It deliberately does not consult s.sampled.
	//
	// s.sampled answers whether the writer's handle could be verified, and that
	// question fails for reasons that say nothing about the journal's content — a
	// handle stat that errors, a torn tail, a write the filesystem cannot resolve.
	// Folding it into this test makes every such read look like a new generation,
	// so a reader of a perfectly stable journal discards each scan it completes
	// and retries until its request is canceled. Content is decided by content.
	unexpected := renamed || !s.observed || size != s.observedSize || !mod.Equal(s.observedMod)
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
		s.journalGen++
	}
	if sameFile && unexpected {
		// The name still reaches this store's file, so its bytes are this
		// store's to repair. A foreign writer can leave a partial record at the
		// end, and a partial record is poisonous here: the scanner withholds the
		// line it cannot decode, and the next Append joins a new event onto the
		// fragment and fsyncs one malformed line that every later read then
		// fails on. Repair the tail before reporting a length.
		//
		// This runs on the unexpected-change path only. A live session's own
		// appends are recorded by noteAppendedLocked, so the stat above matches
		// what the store wrote and this branch is not taken: the incremental
		// read stays a stat.
		//
		// A tail that is not a record cannot be repaired, and the bytes are
		// preserved as evidence. The reader still answers: it reports the length
		// it can decode and withholds the line it cannot. What it withholds is
		// the SETTLED conclusion, which is why this falls through to the
		// unsampled branch below rather than returning early.
		settled, err := s.recoverTrailingLineForBoundaryLocked(ctx)
		if err != nil {
			return CommittedJournalState{}, err
		}
		if repaired, statErr := s.fs.Stat(s.path); statErr == nil {
			info = repaired
			size, mod = repaired.Size(), repaired.ModTime()
			// The repair path re-stats the NAME, and the name is the sample that
			// lags. Do not let it raise the boundary back over a length the file
			// itself was measured at, and do not let it lower a boundary whose
			// bytes an append confirmed: either direction hides or invents rows.
			//
			// The EOF is measured again HERE rather than reused from the lag
			// check above, because the repair may just have cut a torn tail: the
			// pre-repair length describes bytes that are no longer in the file,
			// and reporting it would hand a fold a boundary past EOF.
			if afterRepair, ok := s.journalRealSizeLocked(); ok {
				size = afterRepair
				haveMeasured, measured = true, afterRepair
			} else if haveMeasured {
				size = measured
			}
		}
		if !settled {
			s.sampled = false
			s.named = info
			s.observedSize, s.observedMod, s.observed = size, mod, true
			if !lagging {
				s.laggingAppendEnd = 0
			}
			return CommittedJournalState{
				Path: s.path, Size: size, Generation: s.journalGen,
				MetadataResolvesWrites: !s.cursor.disabled && !s.metadataLagging,
			}, nil
		}
	}
	if !handleSampled {
		// The question "does this name reach the file this store appends
		// through" was never answered, so nothing here established where this
		// store's bytes end. The read still answers with the bounded view it can
		// decode — an activity page needs it, and refusing would fail an
		// ordinary poll. The WRITER's baseline is a different claim: leave it
		// unsampled so the next append inspects the tail instead of comparing
		// against a sample that never happened.
		s.sampled = false
		s.named = info
		s.observedSize, s.observedMod, s.observed = size, mod, true
		if !lagging {
			s.laggingAppendEnd = 0
		}
		return CommittedJournalState{
			Path: s.path, Size: size, Generation: s.journalGen,
			MetadataResolvesWrites: !s.cursor.disabled && !s.metadataLagging,
		}, nil
	}
	s.journalSize, s.journalMod, s.sampled = size, mod, true
	s.observedSize, s.observedMod, s.observed = size, mod, true
	s.named = info
	// The append's recorded end stays authoritative while the path still reports
	// less than it. Once the path catches up it is no longer the better
	// evidence, and keeping it would let a later, genuine truncation be reported
	// at the old length — so it is dropped.
	if !lagging {
		s.laggingAppendEnd = 0
	}
	return CommittedJournalState{
		Path: s.path, Size: size, Generation: s.journalGen,
		MetadataResolvesWrites: !s.cursor.disabled && !s.metadataLagging,
	}, nil
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

// sameJournalFile reports whether named is the file handle refers to.
//
// It uses the underlying file identity where the filesystem supplies one, and
// falls back to length and modification time where it does not — afero's
// in-memory filesystems carry no Sys — inheriting the documented residual that
// a replacement reproducing both is indistinguishable from no change at all.
func sameJournalFile(handle, named os.FileInfo) bool {
	if handle == nil || named == nil {
		return false
	}
	if handle.Sys() != nil && named.Sys() != nil {
		return os.SameFile(handle, named)
	}
	return handle.Size() == named.Size() && handle.ModTime().Equal(named.ModTime())
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
	s.cursor.valid = true
	return tail, nil
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

// appendSamples is what one append observed about the journal around its own
// write: the handle before the bytes existed, the handle after they were
// durable, and the NAME right after the sync.
//
// All three are optional. A nil entry means that stat failed, which is not the
// append's failure — the bytes are on disk — but it is a question left
// unanswered, and the boundary must not spend an unanswered question as a
// favourable one.
type appendSamples struct {
	pre   os.FileInfo
	post  os.FileInfo
	named os.FileInfo
}

// noteCommittedBoundaryLocked books the append's own post-sync sample as the
// committed boundary's baseline, advances the content generation when the file
// changed between two boundary reads, and records when a sample proves this
// filesystem's metadata cannot resolve a write.
//
// The pre-write sample is what makes the FIRST of those possible. Booking only
// the post-write sample would overwrite the baseline the previous boundary read
// reported, and a truncation or a same-length rewrite that landed between the
// two reads would then be indistinguishable from this append's own growth: the
// next boundary read would compare its stat against bytes this append wrote,
// see them agree, and keep the generation — serving a fold built from content
// the rewrite removed, indefinitely, because a stable file never contradicts it
// again. Comparing the pre-write sample against the baseline is what notices
// the intervening change, and it is the only place the comparison can be made:
// by the time the append finishes, the evidence is gone.
//
// It is deliberately separate from noteAppendedLocked. That function's sample
// is the CURSOR's, and its timestamp comparison is the calibration that decides
// whether the cursor may be trusted at all — a pre-existing judgement this
// change must leave exactly as it was. This one answers a different question:
// what length may a reader fold up to.
//
// samples.post is the HANDLE's sample rather than a statement about the name:
// whether the name still reaches this file is a separate, stronger question
// (name verification) that this change does not answer. Booking it is still the
// honest thing to do — it says what the file this store writes through reports
// — and it is what lets the next boundary read tell an ordinary append from a
// truncation or a rewrite. See CommittedJournal.
func (s *Store) noteCommittedBoundaryLocked(startOffset, endOffset int64, samples appendSamples) {
	verified := samples.post
	// What this store believed about the file BEFORE this write. The append's
	// own sample overwrites those fields below, and the question that needs a
	// "before" — did the length move while the timestamp did not — cannot be
	// asked afterwards.
	beforeSize, beforeMod, wasSampled := s.journalSize, s.journalMod, s.sampled
	// Did the file change between the boundary read that booked this baseline
	// and the moment this append began writing? An ordinary append answers no:
	// the handle is the length and time the previous boundary read recorded, and
	// the bytes this append is about to add are the only difference. A
	// truncation, an in-place rewrite, or a replacement answers yes, and those
	// are content this store did not write — the generation must move for them
	// even though this append is about to make the numbers agree again.
	//
	// The comparison is made against the PRE-write handle rather than the name.
	// That is the weaker evidence the seam documents above, applied where it is
	// available: the handle is the file this append writes through, so its
	// length before the write is exactly where the append's own bytes begin.
	//
	// There are two independent pieces of evidence about the length this append
	// found, and the append itself produced the better one: startOffset is where
	// its own seek landed at the end of the file, which is a fact about the file
	// rather than an attribute a caching layer may report late. The pre-write stat
	// is the other. Either can invalidate the recorded baseline, and BOTH are
	// consulted — a truncation followed by an append whose pre-write stat failed
	// or returned stale attributes is otherwise indistinguishable from ordinary
	// growth, because the append's post-sync sample makes the numbers agree again.
	//
	// An UNSAMPLED baseline counts as a change. There is no recorded length or
	// time here to compare against, which means nothing in this store vouches for
	// the prefix the fold would extend: the last boundary read declined to vouch
	// for it, and this append cannot supply that evidence by writing past it.
	// Advancing the generation costs one rebuild on a path that is already
	// exceptional — a torn tail, a vanished write, a handle that could not be
	// sampled — and it keeps the O(delta) path reserved for journals whose prefix
	// this store actually accounted for.
	//
	// With the lengths agreeing, the pre-write sample still has to account for the
	// time: a same-length rewrite moves only that. A sample that could not be
	// taken leaves the prefix unverified, and unverified is not preserved.
	intervened := !wasSampled || startOffset != beforeSize ||
		samples.pre == nil || !samples.pre.ModTime().Equal(beforeMod)
	if verified == nil {
		// Nothing was verified after the bytes were made durable under the
		// handle. That is not this append's failure — the bytes are on disk —
		// but the next boundary read has nothing to compare against, so it must
		// not pretend the file is unchanged.
		if intervened {
			s.journalGen++
		}
		s.sampled = false
		// The reader's baseline is left alone here on purpose: with no sample of
		// the file after its own bytes were written, this store cannot say what the
		// name now holds, and inventing a value would make the next read see a
		// change that is this append's. Leaving the old baseline makes that read
		// report the append as the change it is.
		s.laggingAppendEnd = endOffset
		return
	}
	if intervened {
		s.journalGen++
	}
	s.journalSize, s.journalMod, s.sampled = verified.Size(), verified.ModTime(), true
	// The append's own bytes are accounted for, so the reader's baseline moves with
	// them. Without this the next boundary read sees a longer name than the reader
	// last reported and counts this store's own append as foreign content — one
	// generation per append, which is the whole-history rebuild this seam exists to
	// prevent. Both baselines track the same write; they differ only in what they
	// do when a failure leaves one of them unknown.
	s.observedSize, s.observedMod, s.observed = verified.Size(), verified.ModTime(), true
	s.named = verified
	// The append computed endOffset from the bytes it wrote; the stat above
	// asked a filesystem whose attributes may lag its data. Those two are not
	// equally good evidence about THIS write: the path says "as of some moment
	// that may predate my sync", the append says "exactly here". When the path
	// reports less than the store just made durable, the boundary must report
	// the append's end, or the record that just committed is omitted until the
	// metadata catches up.
	//
	// The claim is kept when the NAME still reports less than the append
	// confirmed, even though the handle already reports the full length. Those
	// two samples describe one file through two paths, and the path's attributes
	// are the ones that lag: a handle stat that has caught up and a name stat
	// that has not is the ordinary shape of attribute caching, not evidence that
	// the lag is over. Dropping the claim on that shape would let the next
	// boundary read trust the stale name length as the boundary, capping the
	// scan short of a row the append already made durable until the path's
	// attributes caught up. CommittedJournal keeps the claim until a name sample
	// agrees, and it checks the file's own EOF meanwhile so a real truncation is
	// still reported as one.
	s.laggingAppendEnd = 0
	if samples.named != nil && samples.named.Size() < endOffset {
		s.laggingAppendEnd = endOffset
		s.metadataLagging = true
	}
	if verified.Size() < endOffset {
		// A short sample has two causes and they need opposite answers. If the
		// FILE is at least endOffset long, the bytes this append confirmed are
		// there and only the attributes trail them: it is lag, and the append
		// succeeded. If the file itself is shorter, the bytes are GONE, and
		// respecting metadata lag must not become a licence to report a boundary
		// over a vanished write. The file arbitrates.
		onDisk, haveLength := s.journalRealSizeLocked()
		if !haveLength || onDisk < endOffset {
			s.sampled = false
			s.metadataLagging = true
		} else {
			s.journalSize = endOffset
			s.laggingAppendEnd = endOffset
			// The filesystem under-reported bytes this append just made durable.
			// That is metadata that cannot resolve a write, and it is recorded
			// HERE, before anything else can return early on this sample.
			s.metadataLagging = true
		}
	} else if verified.Size() > endOffset {
		// The sample is longer than the bytes this append made durable. Those
		// extra bytes are not this store's write, so the baseline is left
		// unsampled and the append's own confirmed end is kept as the place to
		// inspect from.
		s.journalSize = endOffset
		s.laggingAppendEnd = endOffset
		s.sampled = false
		s.metadataLagging = true
	}
	s.noteUnresolvableTimestampLocked(startOffset, endOffset, verified, beforeSize, beforeMod, wasSampled)
}

// noteUnresolvableTimestampLocked records that this filesystem cannot tell one
// of the store's own writes from no write at all.
//
// The evidence is an append that changed the file's LENGTH while its
// modification time did not move. Length and timestamp both advance on a write
// the filesystem can see, so a length that moved alone means the timestamp
// cannot resolve a write.
//
// It is recorded away from the cursor checks because those return early for
// exactly this sample, and cursor validity has nothing to do with whether the
// filesystem can resolve a write. A later same-length rewrite under this
// condition leaves the reconciled size and timestamp unchanged, so a boundary
// still advertising trustworthy metadata would let a live fold serve the rows
// that rewrite removed.
func (s *Store) noteUnresolvableTimestampLocked(startOffset, endOffset int64, verified os.FileInfo, beforeSize int64, beforeMod time.Time, wasSampled bool) {
	if verified == nil || !wasSampled || endOffset == startOffset {
		return
	}
	if verified.Size() == beforeSize {
		return
	}
	if verified.ModTime().Equal(beforeMod) {
		s.metadataLagging = true
	}
}

// DecodeJournalEvent is how this package turns one journal line into an Event.
//
// It is a variable for the same reason openScanEventsFile beside it is: a
// caller measuring what a read COSTS has to be able to count the records
// actually decoded, and a returned slice cannot say that. Store.LoadOrdered
// serves its cached fold without decoding anything, so counting the records it
// RETURNS prices a refresh of an unchanged journal as a full decode of the
// history — the very cost the incremental path is compared against. Replacing
// this is a measurement seam, not a policy one: the store decodes by unmarshalling,
// exactly as below, and a replacement must do the same.
var DecodeJournalEvent = json.Unmarshal

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
			if err := DecodeJournalEvent(line, &e); err != nil {
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

// trailingLineChunk is how far the backward search reads at a time. It is
// generous enough that an ordinary record is found in one read, so the common
// torn tail costs a single bounded read rather than a scan.
const trailingLineChunk = 64 * 1024

// trailingLineFromHandleLocked returns the offset and bytes of the final line of
// the file this store appends through. The last byte is known NOT to be a
// newline (the caller checked), so there is a line to find.
//
// It walks backward a bounded chunk at a time and returns as soon as a newline
// is in the window, which makes the work proportional to the last line's length
// rather than the file's. A ctx that is done stops the walk before the next
// read: this runs under the store mutex, and an abandoned activity refresh must
// not keep the writer waiting for it.
//
// The caller restores the handle to EOF; this function leaves it where its last
// read finished.
func (s *Store) trailingLineFromHandleLocked(ctx context.Context, size int64) (int64, []byte, error) {
	// window is how many bytes ending at EOF have been read so far.
	window := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		// Grow the window by one chunk and read it whole, so the search result is
		// always one contiguous slice.
		window = min(size, window+trailingLineChunk)
		start := size - window
		if _, err := s.f.Seek(start, io.SeekStart); err != nil {
			return 0, nil, fmt.Errorf("jobstore: seek trailing line: %w", err)
		}
		raw := make([]byte, window)
		if _, err := io.ReadFull(s.f, raw); err != nil {
			return 0, nil, fmt.Errorf("jobstore: read trailing line: %w", err)
		}
		if cut := bytes.LastIndexByte(raw, '\n'); cut >= 0 {
			// The tail is everything after that newline. It may be empty, which the
			// caller's blank check settles.
			return start + int64(cut) + 1, raw[cut+1:], nil
		}
		if start == 0 {
			// No newline anywhere in the file: the whole file is the final line.
			return 0, raw, nil
		}
	}
}

// recoverTrailingLineForBoundaryLocked is CommittedJournal's trimmed tail
// recovery: enough that a torn trailing line cannot corrupt the boundary read
// or the append that follows it, and no more.
//
// settled reports whether this call ESTABLISHED what the tail holds. It is
// false when the question could not be put to the file at all — the name is
// gone, or it reaches a different file than the handle this store appends
// through. Neither is a finding that the tail is clean, so a caller that only
// wants a boundary can still answer, but it must not book the sample as a
// writer's baseline: joining a new record onto a fragment nobody examined is
// how one malformed line reaches the journal and the next reopen fails on it.
//
// Unlike recoverTrailingPartialLineLocked, every read and every repair here is
// performed on the handle the store appends through. Reading the path for
// evidence and then truncating the handle is how a second replacement's bytes
// get used to justify a cut on the first one: a replacement holding a single
// torn record supplies offset zero, and offset zero erases every committed
// record from the journal that was moved aside. Keeping evidence and repair on
// one file makes that impossible, and the name is rechecked against the handle
// before the cut so a file the name no longer reaches is never edited.
//
// A tail that is not a truncated record is left exactly where it is: cutting
// it would destroy evidence of real corruption and terminating it would turn
// that corruption into a record. It reports not-settled, and the boundary still
// answers with the prefix it can decode.
//
// This is deliberately NOT the durability spine's recovery. It does not
// remember a sequence number whose bytes went somewhere a reader will not look,
// does not adopt a replacement journal, and does not refuse an append on an
// unexamined tail. Those are the separately filed bug; see CommittedJournal's
// baseline note.
func (s *Store) recoverTrailingLineForBoundaryLocked(ctx context.Context) (settled bool, err error) {
	named, statErr := s.fs.Stat(s.path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return false, nil
		}
		return false, fmt.Errorf("jobstore: stat %s: %w", s.path, statErr)
	}
	info, handleErr := s.f.Stat()
	if handleErr != nil {
		return false, fmt.Errorf("jobstore: stat %s: %w", s.path, handleErr)
	}
	if !sameJournalFile(info, named) && info.Sys() != nil && named.Sys() != nil {
		// The name reaches a different file. This store's tail is not the one a
		// reader will see, so repairing it would edit a file nobody reads.
		return false, nil
	}
	if info.Size() == 0 {
		return true, nil
	}
	if _, err := s.f.Seek(-1, io.SeekEnd); err != nil {
		return false, fmt.Errorf("jobstore: inspect trailing byte: %w", err)
	}
	last := make([]byte, 1)
	if _, err := io.ReadFull(s.f, last); err != nil {
		return false, fmt.Errorf("jobstore: read trailing byte: %w", err)
	}
	// Every exit from here restores the handle to its end, because Append seeks
	// there before writing and a half-positioned handle is state a later writer
	// trusts.
	defer func() {
		if _, seekErr := s.f.Seek(0, io.SeekEnd); seekErr != nil && err == nil {
			err = fmt.Errorf("jobstore: seek after trailing recovery: %w", seekErr)
		}
	}()
	if last[0] == '\n' {
		return true, nil
	}
	// Find the start of the trailing line by walking BACKWARD in bounded chunks.
	//
	// Reading the whole file to find one newline is the defect this issue exists
	// to remove, wearing a different hat: the read happens under the store mutex,
	// cannot be canceled, and holds up every append for as long as it takes. A
	// journal that grew large is exactly where a torn tail is most likely and
	// where that cost is worst, so the walk is over a window at the end and grows
	// only while the window holds no newline — one record's length, in practice,
	// rather than the session's history.
	offset, line, err := s.trailingLineFromHandleLocked(ctx, info.Size())
	if err != nil {
		return false, err
	}
	if len(bytes.TrimSpace(line)) == 0 {
		// A tail with no record in it — a lone carriage return, stray spaces, the
		// whitespace an interrupted writer left. Nothing here can be decoded and
		// nothing is being withheld: the scanner skips a blank line whether or not
		// it is terminated, so the records before it are all readable either way.
		//
		// Terminating it is what settles it. Left unterminated, this function
		// answers "unsettled", the boundary clears its baseline, and every later
		// read finds an unsampled file, advances the generation again and
		// discards the scan it just completed — a fold that can never publish
		// anything while the file sits still. One newline makes the tail an
		// ordinary blank line, which is what the file's next reader already
		// ignores, and the boundary becomes stable.
		if err := s.finishTrailingJSONLineLocked(); err != nil {
			return false, err
		}
		return true, nil
	}
	var e Event
	if decodeErr := json.Unmarshal(line, &e); decodeErr == nil {
		// The record is complete and only its newline is missing. Terminating it
		// makes it a record every reader can decode.
		return true, s.finishTrailingJSONLineLocked()
	} else if !isIncompleteTrailingJSON(line, decodeErr) {
		return false, nil
	}
	// Repairing the tail changes the file under the cursor: re-read from zero.
	s.invalidateCursorLocked()
	if err := s.f.Truncate(offset); err != nil {
		return false, fmt.Errorf("jobstore: truncate trailing partial line: %w", err)
	}
	if err := s.syncLocked(); err != nil {
		return false, fmt.Errorf("jobstore: sync trailing recovery: %w", err)
	}
	return true, nil
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

func cloneFoldOrdered(in []*JobRecord) []*JobRecord {
	if in == nil {
		return nil
	}
	out := make([]*JobRecord, len(in))
	for i, r := range in {
		out[i] = r.Clone()
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
