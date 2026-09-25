// Package transcript defines the on-disk JSONL transcript format and the
// append-only writer that records an agent session's semantic turns.
// Readers live with their consumers; this package owns the write side and the
// shared line schema (Header and Entry).
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
)

// DefaultMaxLineBytes is the maximum transcript record payload. The trailing
// newline is framing and does not count toward this limit.
const DefaultMaxLineBytes = 128 << 20

const transcriptJSONLMaxLineBytes = DefaultMaxLineBytes

// FormatVersion is the only transcript format this package writes or accepts.
const FormatVersion = 2

// ErrUnsupportedFormat marks transcripts that are not semantic-only format v2.
var ErrUnsupportedFormat = errors.New("unsupported transcript format")

// ErrInvalidRecordBoundary marks a transcript line that is not one complete
// JSON object from which a record kind can be determined.
var ErrInvalidRecordBoundary = errors.New("invalid transcript record boundary")

// ErrLineTooLong marks a complete transcript record whose payload exceeds the
// configured framing limit.
var ErrLineTooLong = errors.New("transcript line too long")

// ErrRollbackFailed marks a durable append whose sync failed and whose rollback
// could not take the bytes back out. It is an internal detail of how the writer
// classifies such a failure, not a signal callers act on: the writer settles
// the outcome itself. A whole line left behind is a retained record — the
// append returns nil (recorded) and the debt is surfaced as a warning — while a
// partial line poisons the writer. Callers of Append/AppendDurable see
// recorded-or-error and never reconcile; a caller that needs durability uses
// AppendSynced.
var ErrRollbackFailed = errors.New("rollback failed")

// ErrWriterPoisoned marks a writer that refuses further appends. An append
// that failed partway and could not be rolled back leaves bytes at the tail
// that are not a record: appending after them would run the next entry onto
// the remains of the last and make the whole file unreadable. The writer stops
// rather than produce that, so the failure surfaces on every later append
// instead of being discovered by whoever next reads the transcript. Recovery is
// to reopen the transcript, which rebuilds the writer from the complete records
// the file still holds.
var ErrWriterPoisoned = errors.New("transcript writer refuses further appends after an unresolved partial append")

// ErrWriterClosed marks a synced write to a closed writer. The ordinary doors
// treat a closed (or nil) writer as a no-op that returns nil, because a session
// with no state directory writes into a closed writer for its whole life; a
// durability owner (AppendSynced) instead fails closed, so it never reads a
// dropped write as durable.
var ErrWriterClosed = errors.New("transcript writer is closed")

// ErrRetainedUnsynced marks an AppendSynced whose whole record is in the file
// but could be made durable by neither its own fsync nor the recovery barrier.
// The record IS a record — a returning reader finds it — so the caller must
// adopt it (record it in history) rather than re-append, which would duplicate
// the line on restart; the next successful fsync settles its durability. Match
// it with errors.Is; RetainedUnsyncedError carries the record's sequence.
var ErrRetainedUnsynced = errors.New("transcript entry recorded but not synced")

// RetainedUnsyncedError is the concrete ErrRetainedUnsynced, carrying the
// sequence number the recorded-but-unsynced entry took.
type RetainedUnsyncedError struct {
	Seq   int
	Cause error
}

func (e *RetainedUnsyncedError) Error() string {
	return fmt.Sprintf("transcript entry seq %d recorded but not synced: %v", e.Seq, e.Cause)
}

func (e *RetainedUnsyncedError) Unwrap() error { return e.Cause }

// Is reports a match against the ErrRetainedUnsynced sentinel so callers can
// errors.Is without depending on the concrete type.
func (e *RetainedUnsyncedError) Is(target error) bool { return target == ErrRetainedUnsynced }

// Header is the first line of a transcript JSONL file.
type Header struct {
	Kind          string `json:"kind"`           // Always "header"
	FormatVersion int    `json:"format_version"` // Currently 2
	SessionID     string `json:"session_id"`     // ID of the session this transcript records
	// ParentSessionID and ParentToolCallID are set only for spawned subagent
	// transcripts: the parent session and the tool call that spawned this run.
	ParentSessionID  string    `json:"parent_session_id,omitempty"`
	ParentToolCallID string    `json:"parent_tool_call_id,omitempty"`
	Task             string    `json:"task,omitempty"`          // task description for a spawned subagent
	CreatedAt        time.Time `json:"created_at"`              // when the session was created
	ProfileID        string    `json:"profile_id"`              // provider profile ID at creation
	Model            string    `json:"model"`                   // model name at creation
	WorkingDir       string    `json:"working_dir,omitempty"`   // the agent's working directory
	Depth            int       `json:"depth,omitempty"`         // subagent nesting depth (0 for root)
	BuildVersion     string    `json:"build_version,omitempty"` // evener build version that wrote the file
	SystemPrompt     string    `json:"system_prompt,omitempty"` // initial system prompt
	// AgentTasks is the full task list the agent started with (from the
	// agent's YAML frontmatter for root sessions, or from the parent's
	// task_list parameter for spawned subagents). Captured at session
	// creation so the transcript is self-describing even for runs that
	// never call task_list(action="view") or fail before all STEERING
	// messages are emitted.
	AgentTasks []task.Task `json:"agent_tasks,omitempty"`
}

// Entry is a single turn in the transcript JSONL file. Fields MUST stay
// declared in alphabetical JSON-key order: the public line projection
// (agent's publicTranscriptLine) re-marshals through string-keyed maps,
// which sort keys alphabetically, and relies on the struct's field order
// matching that sort so a projected line stays byte-identical to the
// persisted one. TestReadSessionTranscriptExpansionLosslesslyReturnsEverySemanticTurn
// pins the round trip.
//
// MachineryFlagged is set on EVERY entry this build writes — not only
// entries carrying machinery parts — by deliberate decision: decode's
// pre-flag inference must never run on a current-build entry, because an
// unflagged block-shaped part in a new entry is most commonly a user's
// verbatim paste, and the sharp case is a pre-flag session resumed by this
// build, whose new turns would otherwise have their pastes inferred and
// hidden. The cost is the wf7e one-way door (see decodeStrictJSON): an
// older build fails to decode any entry carrying this unknown field, so
// every post-upgrade transcript is unreadable to pre-change binaries, not
// only the machinery-bearing ones. That trade is accepted for schema
// evolution per kata wf7e; narrower keying — a build version in the
// header — breaks on dev builds with empty versions and on resumed
// old-header sessions.
type Entry struct {
	Kind string `json:"kind"` // Always "entry"
	// MachineryFlagged marks an entry written by a build that flags
	// machinery parts at construction: its parts' Machinery flags are
	// authoritative. Entries lacking the marker predate the flag, so
	// DecodeEntry infers machinery from exact-block text shape for them —
	// an unflagged block-shaped part in a marked entry is a user's
	// verbatim paste and must stay unflagged.
	MachineryFlagged bool        `json:"machinery_flagged,omitempty"`
	Seq              int         `json:"seq"`  // monotonically increasing line sequence number
	Turn             schema.Turn `json:"turn"` // the recorded conversation turn
}

// ValidateHeader enforces the hard transcript-v2 boundary shared by writers
// and semantic readers.
func ValidateHeader(header Header) error {
	if header.Kind != "header" || header.FormatVersion != FormatVersion {
		return fmt.Errorf("%w: require transcript header with format_version %d", ErrUnsupportedFormat, FormatVersion)
	}
	return nil
}

// ValidateRecordKind rejects every non-semantic record after the v2 header.
func ValidateRecordKind(kind string) error {
	if kind != "entry" {
		return fmt.Errorf("%w: record kind %q is not valid in transcript format %d", ErrUnsupportedFormat, kind, FormatVersion)
	}
	return nil
}

// DecodeHeader strictly decodes the v2 transcript header. Unknown fields and
// trailing JSON values are corruption, while a non-v2 boundary is classified
// as an unsupported transcript format.
func DecodeHeader(line []byte) (Header, error) {
	var boundary struct {
		Kind          string `json:"kind"`
		FormatVersion int    `json:"format_version"`
	}
	if err := json.Unmarshal(line, &boundary); err != nil {
		return Header{}, fmt.Errorf("decode transcript header boundary: %w", err)
	}
	if err := ValidateHeader(Header{Kind: boundary.Kind, FormatVersion: boundary.FormatVersion}); err != nil {
		return Header{}, err
	}
	var header Header
	if err := decodeStrictJSON(line, &header); err != nil {
		return Header{}, fmt.Errorf("decode transcript header: %w", err)
	}
	return header, nil
}

// DecodeEntry strictly decodes one semantic v2 entry. Raw entry bytes are safe
// to pass to a projector only after this function succeeds.
func DecodeEntry(line []byte) (Entry, error) {
	var boundary struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(line, &boundary); err != nil {
		return Entry{}, fmt.Errorf("%w: %w", ErrInvalidRecordBoundary, err)
	}
	if err := ValidateRecordKind(boundary.Kind); err != nil {
		return Entry{}, err
	}
	var entry Entry
	if err := decodeStrictJSON(line, &entry); err != nil {
		return Entry{}, fmt.Errorf("decode transcript entry: %w", err)
	}
	if !entry.MachineryFlagged {
		inferPreFlagMachinery(&entry.Turn)
	}
	return entry, nil
}

// inferPreFlagMachinery flags block-shaped machinery parts on turns recorded
// before part flags existed, so those transcripts keep filtering their
// machinery notes under flag-only user-facing projection. It never runs on
// marked entries: there an unflagged block-shaped part is a user pasting the
// block verbatim, and the user's own words must survive.
func inferPreFlagMachinery(turn *schema.Turn) {
	for i := range turn.Message.Content {
		p := &turn.Message.Content[i]
		if p.Kind == llm.ContentText && !p.Machinery && llm.IsMachineryNotificationText(p.Text) {
			p.Machinery = true
		}
	}
}

func decodeStrictJSON(line []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	// DisallowUnknownFields is deliberate and applies to the nested turn, not
	// just this record's own envelope: a typo'd or renamed field in schema.Turn
	// (or anything it embeds, like llm.Message) fails loudly here instead of
	// vanishing silently, which is the failure mode kata kq8c spent real effort
	// tracking down after it went unnoticed for months. The cost of that choice
	// is a one-way door: a transcript written by a build with a field this
	// build's schema.Turn does not declare fails to decode at all, and because
	// every reader (thread/read, resume, fork, doctor) decodes a whole
	// transcript's records in one pass and aborts on the first error, one such
	// record makes the ENTIRE transcript unreadable, not just the turn that
	// carries the new field. See kata wf7e for the investigation: the failure
	// is real and reachable (a long-running evener-hub is not restarted when the
	// evener CLI it talks to is upgraded, and every past transcript on disk can
	// have been written by a different historical build), but it is also
	// self-healing (the file on disk is untouched; a version-matched reader
	// recovers full fidelity) and, unlike kq8c, cannot be silently wrong: a
	// build only ever fails to decode a field IT does not know about, never
	// drops a field it does know how to decode. wf7e closed wontfix on that
	// basis; TestPastThreadReadFailsWholeSessionOnOneUnknownTurnField in
	// cmd/evener-hub pins the resulting behavior so a future change to it is a
	// decision, not an accident.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return fmt.Errorf("trailing JSON data: %w", err)
	}
	return nil
}

// ReadLine reads one newline-framed transcript record. maxLineBytes applies to
// the payload only, excluding the newline. Complete oversized records are
// drained before ErrLineTooLong is returned. An unterminated final tail is
// always drained and discarded without retaining it, regardless of its size.
func ReadLine(reader *bufio.Reader, maxLineBytes int) (line []byte, complete bool, bytesRead int64, err error) {
	if maxLineBytes <= 0 {
		maxLineBytes = DefaultMaxLineBytes
	}
	overLimit := false
	for {
		fragment, readErr := reader.ReadSlice('\n')
		bytesRead += int64(len(fragment))
		payload := fragment
		if readErr == nil && len(payload) > 0 {
			payload = payload[:len(payload)-1]
		}
		if !overLimit {
			if len(payload) > maxLineBytes-len(line) {
				line = nil
				overLimit = true
			} else {
				line = append(line, payload...)
			}
		}

		switch {
		case readErr == nil:
			if overLimit {
				return nil, false, bytesRead, fmt.Errorf("%w: transcript line exceeds %d bytes", ErrLineTooLong, maxLineBytes)
			}
			return line, true, bytesRead, nil
		case errors.Is(readErr, bufio.ErrBufferFull):
			continue
		case errors.Is(readErr, io.EOF):
			return nil, false, bytesRead, nil
		default:
			return nil, false, bytesRead, fmt.Errorf("read transcript line: %w", readErr)
		}
	}
}

// Writer appends turns to an immutable JSONL transcript file.
type Writer struct {
	fs   afero.Fs
	file afero.File
	mu   sync.Mutex
	tail *appendTail
	// tailMove is the tail's move this writer's handle position reflects:
	// the file's end as of this writer's own last append or open.
	tailMove uint64
	// releaseTail gives the tail back if the writer is dropped without Close.
	releaseTail runtime.Cleanup
	closeOnce   sync.Once
	closed      atomic.Bool
	// header is the validated header of the resumed transcript, retained
	// from the resume scan so callers that already hold the decoded entries
	// can project them without re-reading the file for its header.
	// newWriterFS records the header it wrote, so Header() returns the real
	// header for both fresh and resumed writers; only a writer whose header
	// was never set — the zero-value Writer — reports a zero Header from it.
	header Header

	// SyncInterval controls how often Append calls fsync.
	// If 0, every Append fsyncs (backward-compatible default for tests).
	// If >0, Append only fsyncs when this duration has elapsed since the last sync.
	SyncInterval time.Duration

	dirty    bool
	lastSync time.Time

	// poisoned records a write that stopped midway and could not be rolled
	// back: the file's tail is the remains of a record rather than a record,
	// and nothing this writer could append after it would be readable. It is
	// never cleared — no fsync makes half a line whole. A whole line that
	// landed but did not sync is NOT this: it is a record, tracked as dirty
	// debt the next fsync settles, and the writer stays usable. See the two
	// append doors for the full contract.
	poisoned bool

	// retainedWarnings holds the diagnostics — wrapped errors carrying the sync
	// cause and ErrRollbackFailed — of ordinary appends whose whole line landed
	// but did not sync: the entry IS a record (the append returned nil), but
	// the sync failure must not be lost. The session drains them and surfaces
	// each once, outside the locks a warning's notification hook needs.
	// pendingWarnings lets a caller skip the lock when the queue is empty, which
	// it almost always is.
	retainedWarnings []error
	pendingWarnings  atomic.Int32

	// positionUnknown records a rollback that removed the entry but could not
	// seek back to the file's new end, leaving this writer's position past it.
	// The file is consistent and the writer stays usable, but the next append
	// has to re-establish the end before writing or its record lands past it,
	// behind a gap the filesystem zero-fills.
	positionUnknown bool

	// failures counts the session's failed tool calls as they are written, for
	// the live figure a running session reports. Nil until TrackFailures
	// installs it, and a nil counter reports ABSENT rather than zero: a writer
	// nobody asked to count has measured nothing, and "0 failed" from a
	// producer that never looked is the false all-clear the count exists to
	// prevent.
	failures *FailureCounter
}

// TrackFailures installs a running count of the session's failed tool calls,
// seeded from the entries already on disk and advanced by every entry this
// writer appends from here on.
//
// That split is what makes the live figure whole-session rather than
// since-restart: seed carries the run before this process, and the writer sees
// every entry after it, because a turn reaches the transcript before any client
// can ask about it. The alternative — re-deriving from the file on demand —
// reads a transcript still being appended to and returns a stale floor, and the
// alternative to THAT — counting the session's in-memory history — sheds
// everything compaction summarizes away. Both under-report, which for failures
// is worse than reporting nothing.
//
// fromEntryOrdinal bounds the seed to the session's own span; see
// NewFailureCounter.
func (w *Writer) TrackFailures(seed []Entry, fromEntryOrdinal int) {
	if w == nil {
		return
	}
	counter := NewFailureCounter(fromEntryOrdinal)
	for _, entry := range seed {
		counter.Observe(entry.Turn)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failures = counter
}

// FailedToolCalls is how many of the session's tool calls have failed so far,
// and whether anyone counted. It stays readable after Close so a session that
// ends while someone is watching keeps reporting its settled figure.
func (w *Writer) FailedToolCalls() (int, bool) {
	if w == nil {
		return 0, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failures == nil {
		return 0, false
	}
	return w.failures.Count(), true
}

// NewWriter creates a transcript file at path, writes the header as the first line,
// and returns a writer that keeps the file handle open for subsequent Append calls.
func NewWriter(path string, header Header) (*Writer, error) {
	return newWriterFS(afero.NewOsFs(), path, header, true)
}

// NewWriterWithFS creates a transcript writer over fs. It has the same behavior
// as NewWriter, but allows callers that already own a filesystem boundary to
// keep transcript persistence on that filesystem. As with
// OpenWriterForSessionWithFS, writers share a file's append tail only through
// a filesystem that names files as they are on the real disk.
func NewWriterWithFS(fs afero.Fs, path string, header Header) (*Writer, error) {
	return newWriterFS(fs, path, header, true)
}

// NewWriterNoSync creates a transcript file exactly like NewWriter, on the
// real OS filesystem, but skips the header fsync. Every other durability
// property (file exists at path, header bytes present, subsequent Append
// behavior) is identical — only the guarantee that the header survives a
// crash before the first fsync is given up. For tests whose contract is not
// crash durability; production always calls NewWriter.
func NewWriterNoSync(path string, header Header) (*Writer, error) {
	return newWriterFS(afero.NewOsFs(), path, header, false)
}

// newWriterFS is the filesystem-injecting seam behind NewWriter. Production
// passes afero.NewOsFs() (byte-identical to direct os calls); tests and the
// persistence fuzzer inject an in-memory or sandboxed filesystem. sync
// controls whether the header write is fsynced before return.
func newWriterFS(fs afero.Fs, path string, header Header, sync bool) (*Writer, error) {
	header.Kind = "header"
	header.FormatVersion = FormatVersion

	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create transcript dir: %w", err)
	}

	f, tail, err := createAppendTail(path, func() (afero.File, error) { return fs.Create(path) })
	if err != nil {
		return nil, err
	}
	w, err := writeTranscriptHeader(fs, f, tail, header, sync)
	if err != nil {
		_ = f.Close() // cleanup on error path; the header error is what matters
		tail.release()
		return nil, err
	}
	return w, nil
}

// writeTranscriptHeader writes a created transcript's header. Its tail is
// registered first, so a writer that opens the file once the header lands
// joins that tail and may append before this writer does. This writer starts
// at move 0, the new tail's count before any writer positioned on it, so its
// first append re-seeks to the end if one has since.
func writeTranscriptHeader(fs afero.Fs, f afero.File, tail *appendTail, header Header, sync bool) (*Writer, error) {
	data, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("marshal transcript header: %w", err)
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		return nil, fmt.Errorf("write transcript header: %w", err)
	}

	if sync {
		if err := f.Sync(); err != nil {
			return nil, fmt.Errorf("sync transcript header: %w", err)
		}
	}

	tail.mu.Lock()
	defer tail.mu.Unlock()
	return newWriterOnTail(fs, f, tail, header, 0), nil
}

// newWriterOnTail builds a writer on its shared tail whose handle position
// reflects the tail's move tailMove. The caller holds tail.mu.
func newWriterOnTail(fs afero.Fs, f afero.File, tail *appendTail, header Header, tailMove uint64) *Writer {
	w := &Writer{fs: fs, file: f, tail: tail, tailMove: tailMove, lastSync: time.Now(), header: header}
	w.releaseTail = runtime.AddCleanup(w, (*appendTail).release, tail)
	return w
}

// Header returns the transcript's validated header: the header this writer
// wrote (NewWriter), or the one the resume scan validated
// (OpenWriterForSession). Callers that already hold the decoded entries of
// the same scan use it to project them without re-reading the file.
func (w *Writer) Header() Header {
	if w == nil {
		return Header{}
	}
	return w.header
}

// Append writes a turn as an Entry to the JSONL file.
// Safe for concurrent use. No-op if the receiver is nil.
//
// The nil no-op exists so a session with no state directory can write without
// every call site nil-checking, and that is the common case. Its cost is that
// "I wrote this" and "there was nowhere to write it" are the same answer to the
// caller — nil. A caller that reaches a writer which does not exist YET, rather
// than one that will never exist, therefore loses the turn in silence, with no
// error to report and nothing for a test to catch. That is exactly what
// happened to SessionStart hook exits in kata qm9y; kata d4es is the hazard.
//
// Do not add nil-checks at call sites to compensate: they cannot tell the two
// cases apart either. Evener's agent package instead routes every write through
// Session.writeTranscript/writeTranscriptDurable, which hold turns until
// Session.attachTranscript has settled whether a writer exists at all. Any new
// consumer of this package that can write before it has opened its writer needs
// the same gate; a bare Append there reports success and drops the turn.
// THE CONTRACT (stated here once; other append doors reference it).
//
// A whole line that reached the file is a RECORD — every returning reader finds
// it — even if the fsync that would make it durable failed. Append and
// AppendDurable therefore return nil for a retained record: recorded-or-error
// is their whole contract, and an ordinary producer's `if err != nil { not
// recorded }` is correct by construction. A retained record is not yet durable;
// the writer tracks that exactly as it tracks any buffered append — as `dirty`
// debt the next successful fsync (any later AppendDurable, or
// EstablishDurability) settles — so the writer stays usable, and the durable
// door's sync failure is reported once through DrainWarnings rather than as an
// error. The ONLY thing that stops the writer is a PARTIAL line whose rollback
// could not take it back out: half a record is not a record, no fsync makes it
// whole, and an append run onto it would be unreadable — so the writer is
// poisoned permanently (ErrWriterPoisoned).
//
// A caller that needs a turn RECORDED AND DURABLE — the environment producer,
// the delegate-attention side-writes — uses AppendSynced, which returns an
// error unless the line is both.
func (w *Writer) Append(turn schema.Turn) error {
	_, _, err := w.appendBatch([]schema.Turn{turn}, false, true, false)
	return err
}

// AppendDurable writes a turn and fsyncs it before returning; a whole line that
// landed but could not be synced is recorded (returns nil) with the sync
// failure queued for DrainWarnings. See the contract on Append. No-op if the
// receiver is nil — see Append for why that makes a write into a not-yet-open
// writer silently succeed.
func (w *Writer) AppendDurable(turn schema.Turn) error {
	_, _, err := w.appendBatch([]schema.Turn{turn}, true, true, false)
	return err
}

// AppendSynced records a turn and establishes its durability. It has three
// outcomes, because a durability owner must tell them apart or it duplicates
// records across a crash:
//
//   - nil: the record is durable (its own fsync, or the recovery barrier,
//     succeeded). The owner commits.
//   - *RetainedUnsyncedError (errors.Is ErrRetainedUnsynced): the WHOLE record
//     is in the file — a returning reader finds it — but neither its fsync nor
//     the barrier could make it durable. The owner must ADOPT it (record it in
//     history, never re-append: re-appending duplicates the line on restart)
//     and let the next successful fsync settle the debt. The diagnostic is
//     queued for the session to surface.
//   - any other error: nothing was recorded (a partial line, or a clean
//     rollback). The owner keeps its obligation pending and may retry.
//
// It is the door for the durability owners that raise their own barrier;
// ordinary producers use AppendDurable and never inspect durability. It raises
// the recovery barrier ONLY when the append's own fsync failed (retained !=
// nil); a clean durable append is not fsynced twice.
func (w *Writer) AppendSynced(turn schema.Turn) error {
	if w == nil {
		return nil // no writer to record into — see Append's nil no-op
	}
	// failClosed=true: a closed writer records nothing, and this door must not
	// read that as durable. The decision is made under appendBatch's lock, so a
	// Close concurrent with this call cannot leave AppendSynced returning nil
	// for a write that landed nowhere. The nil no-op stays only for a writer
	// that never existed (w == nil, above).
	firstSeq, retained, err := w.appendBatch([]schema.Turn{turn}, true, false, true)
	if err != nil {
		return err // not recorded (includes ErrWriterClosed)
	}
	if retained == nil {
		return nil // recorded and its own fsync succeeded: durable
	}
	// Recorded but unsynced: the record is in the file, so a barrier that
	// fsyncs the whole file settles it.
	barrierErr := w.EstablishDurability()
	if barrierErr == nil {
		return nil
	}
	// The record is durable neither by its own fsync nor the barrier. It is
	// still a record: queue its diagnostic for the session to surface, and tell
	// the owner to adopt it rather than re-append.
	w.mu.Lock()
	w.queueWarningLocked(errors.Join(retained, fmt.Errorf("establish durability: %w", barrierErr)))
	w.mu.Unlock()
	return &RetainedUnsyncedError{Seq: firstSeq, Cause: retained}
}

// AppendBatch writes every turn as one write and one fsync, all-or-nothing:
// either every line is in the file at contiguous sequence numbers, or a
// rollback to the batch's start offset leaves none of them and spends no
// sequence number. It returns the sequence number the first turn took. A batch
// whose whole buffer landed but did not sync is a retained record, per the
// contract on Append. No caller until A2's fold; kept here as the exported
// entry to the one write primitive. No-op returning (0, nil) for a nil or
// closed writer, matching Append.
func (w *Writer) AppendBatch(turns []schema.Turn) (int, error) {
	firstSeq, _, err := w.appendBatch(turns, true, true, false)
	return firstSeq, err
}

// appendBatch is the locked entry to the sole write primitive. append() is a
// batch of one through it. queueRetained puts a retained record's sync-failure
// diagnostic on the warning queue for the session to surface (the ordinary
// doors); AppendSynced passes false and takes the diagnostic through the
// returned error instead, so it neither queues nor drains the shared channel.
// failClosed decides what a closed writer means to the caller. The ordinary
// doors pass false: a closed (or nil) writer is a silent nil no-op, because a
// session with no state directory writes into one for its whole life.
// AppendSynced passes true: it must never read a dropped write as durable, so a
// closed writer is ErrWriterClosed. The check is made UNDER THE LOCK, so Close
// cannot slip in between a caller's own closed check and the append — the race
// that let AppendSynced return nil (durable) for a write that recorded nothing.
func (w *Writer) appendBatch(turns []schema.Turn, forceSync, queueRetained, failClosed bool) (firstSeq int, retained, err error) {
	if w == nil {
		// A writer that never existed (a session with no state directory) is a
		// nil no-op for every door, synced or not — there is nothing to record
		// and nothing to lose. Only a CLOSED writer fails closed.
		return 0, nil, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed.Load() {
		if failClosed {
			return 0, nil, ErrWriterClosed
		}
		return 0, nil, nil
	}
	return w.appendBatchLocked(turns, forceSync, queueRetained)
}

// appendBatchLocked encodes every turn into one buffer, seeks to the end once,
// writes once, optionally fsyncs once, and rolls back to the start offset on
// any failure. Every failure is classified in one place, settleFailedWriteLocked.
// It returns retained — the sync-failure diagnostic of a whole line left
// unsynced in the file — separately from err, a hard failure that recorded
// nothing.
func (w *Writer) appendBatchLocked(turns []schema.Turn, forceSync, queueRetained bool) (firstSeq int, retained, err error) {
	if w.poisoned {
		return 0, nil, ErrWriterPoisoned
	}
	// The tail is held to the end of the append, rollback included; see
	// appendTail.
	w.tail.mu.Lock()
	defer w.tail.mu.Unlock()
	firstSeq = w.tail.nextSeq
	if len(turns) == 0 {
		return firstSeq, nil, nil
	}
	if w.tailMove != w.tail.move {
		// Another writer on this file moved its end since this one last wrote,
		// so this handle's position is behind it.
		w.positionUnknown = true
	}
	if w.positionUnknown {
		// Write nothing until the end is known again. A seek that fails here
		// leaves the flag set, so the next attempt re-establishes it rather
		// than writing into the gap.
		if _, seekErr := w.file.Seek(0, io.SeekEnd); seekErr != nil {
			return firstSeq, nil, fmt.Errorf("seek transcript append position: %w", seekErr)
		}
		w.positionUnknown = false
	}
	w.tailMove = w.tail.moved()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf) // Encode writes the trailing newline per entry
	for i, turn := range turns {
		if encErr := enc.Encode(Entry{Kind: "entry", Seq: firstSeq + i, Turn: turn, MachineryFlagged: true}); encErr != nil {
			return firstSeq, nil, fmt.Errorf("marshal transcript entry: %w", encErr)
		}
	}
	data := buf.Bytes()

	// Only the durable door rolls back, and only a rollback needs the start
	// offset. The buffered door leaves a failed write's bytes where they are,
	// so it skips the seek — and its filesystem-op sequence stays what it was
	// before this became the one primitive.
	var startOffset int64
	if forceSync {
		var seekErr error
		if startOffset, seekErr = w.file.Seek(0, io.SeekEnd); seekErr != nil {
			return firstSeq, nil, fmt.Errorf("seek transcript append start: %w", seekErr)
		}
	}

	previousDirty := w.dirty
	if written, writeErr := w.writeLineLocked(data); writeErr != nil {
		retained, hard := w.settleFailedWriteLocked("write transcript entry", writeErr, startOffset, turns, written, len(data), forceSync, previousDirty, queueRetained)
		return firstSeq, retained, hard
	}
	w.dirty = true
	if forceSync || w.SyncInterval == 0 || time.Since(w.lastSync) >= w.SyncInterval {
		if syncErr := w.file.Sync(); syncErr != nil {
			retained, hard := w.settleFailedWriteLocked("sync transcript entry", syncErr, startOffset, turns, len(data), len(data), forceSync, previousDirty, queueRetained)
			return firstSeq, retained, hard
		}
		w.lastSync = time.Now()
		w.dirty = false
	}
	w.commitBatchLocked(turns)
	return firstSeq, nil, nil
}

// settleFailedWriteLocked is the single classification of a batch whose write
// or sync failed. attemptRollback (the durable door) tries to take the bytes
// back out first. It returns (retained, hard): retained is the sync-failure
// diagnostic of a whole line left in the file — a record — with hard nil; hard
// is a failure that recorded nothing (a clean rollback, or a partial line). The
// outcomes:
//   - a clean rollback removed the batch: nothing recorded, hard = the error;
//   - the whole buffer is in the file (a run of records): count the turns, keep
//     the dirty debt, and return it as retained — queued for DrainWarnings when
//     queueRetained, else handed to the caller — the writer stays usable;
//   - only part of the buffer is in the file (a partial line): the writer is
//     poisoned permanently, hard = the error.
func (w *Writer) settleFailedWriteLocked(operation string, cause error, startOffset int64, turns []schema.Turn, written, bufLen int, attemptRollback, previousDirty, queueRetained bool) (retained, hard error) {
	if attemptRollback {
		removed, rollbackErr := w.rollbackAppendLocked(startOffset)
		if rollbackErr == nil {
			w.dirty = previousDirty
			return nil, fmt.Errorf("%s: %w", operation, cause)
		}
		if removed {
			// The bytes are gone; a later rollback step failed, but nothing is
			// recorded and the file's end is what rollbackAppendLocked settled.
			return nil, fmt.Errorf("%s: %w; %w: %w", operation, cause, ErrRollbackFailed, rollbackErr)
		}
		cause = fmt.Errorf("%w; %w: %w", cause, ErrRollbackFailed, rollbackErr)
	}
	if written == 0 {
		// Nothing landed: the file and the position are as they were, so the
		// writer stays usable and a retry still lands.
		return nil, fmt.Errorf("%s: %w", operation, cause)
	}
	w.dirty = true // Close flushes only what it is told is dirty.
	if written != bufLen {
		// A partial line is the remains of a record, not a record. No fsync
		// makes it whole, and an append onto it would be unreadable.
		w.poisoned = true
		return nil, fmt.Errorf("%s: %w", operation, cause)
	}
	// The whole buffer is a record every reader will find: count it, keep it as
	// unsynced debt the next fsync settles, and report the failure as a warning.
	w.commitBatchLocked(turns)
	retained = fmt.Errorf("%s: %w", operation, cause)
	if queueRetained {
		w.queueWarningLocked(retained)
	}
	return retained, nil
}

// commitBatchLocked spends each turn's sequence number and counts the failures
// it settles — the bookkeeping a later reader of the file would do.
func (w *Writer) commitBatchLocked(turns []schema.Turn) {
	for _, turn := range turns {
		w.countAppendedEntryLocked(turn)
	}
}

// EstablishDurability fsyncs the transcript's current complete contents
// without appending another entry. Recovery callers use it before treating a
// readable record from an earlier ambiguous write as authoritative.
func (w *Writer) EstablishDurability() error {
	if w == nil || w.file == nil {
		return errors.New("transcript writer is nil")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed.Load() {
		return errors.New("transcript writer is closed")
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("sync transcript durability barrier: %w", err)
	}
	w.lastSync = time.Now()
	w.dirty = false
	return nil
}

// Poisoned reports whether this writer has stopped accepting appends after an
// append it could not resolve. Nil-safe, like the append doors themselves: a
// session with no state directory has no writer and so has nothing to refuse.
// Callers use it to fail closed before doing work whose records would be lost —
// see ErrWriterPoisoned.
func (w *Writer) Poisoned() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.poisoned
}

// Closed reports whether this writer has been closed and its ordinary appends
// have become silent no-ops. Nil-safe, like the append doors: a session with no
// state directory has no writer and so nothing to refuse.
func (w *Writer) Closed() bool {
	if w == nil {
		return false
	}
	return w.closed.Load()
}

// WhileHealthy runs f under the writer's write door when the writer still
// accepts records, and returns nil when it ran. This is an admission decision,
// not a sample of one: an append holds this same door across its write, and a
// write it cannot resolve records the poison under the door, so an append that
// poisons is either already visible here -- f does not run -- or it has not
// started, which orders the poison after f. A caller that checks Poisoned()
// outside the door can promise neither, and publishing work between such a check
// and its commit leaves a window for a poisoning to land in.
//
// A closed writer is refused too, with its own reason rather than the poisoned
// one: its ordinary appends are silent no-ops, so work published against it
// would record nothing, but nothing was poisoned.
//
// f runs while the door is held, so it must not append to this writer and must
// not take a lock whose holder appends: it is the announcement that the work is
// about to be done, not the work itself. Nil-safe, like the append doors: a
// session with no state directory has no writer and so nothing to refuse.
func (w *Writer) WhileHealthy(f func()) error {
	if w == nil {
		f()
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.poisoned {
		return ErrWriterPoisoned
	}
	if w.closed.Load() {
		return ErrWriterClosed
	}
	f()
	return nil
}

// DrainWarnings removes and returns every pending retained-entry diagnostic —
// wrapped errors carrying the sync cause and ErrRollbackFailed. A retained entry
// (whole line in the file, no fsync) is recorded, so the append returned nil;
// its sync failure reaches a client only here. The session drains after its
// write returns, where emitting is safe. The pending count lets a caller skip
// the lock entirely when the queue is empty, which is the overwhelming case.
func (w *Writer) DrainWarnings() []error {
	if w == nil || w.pendingWarnings.Load() == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	warnings := w.retainedWarnings
	w.retainedWarnings = nil
	w.pendingWarnings.Store(0)
	return warnings
}

// queueWarningLocked records the diagnostic of an append whose whole line
// landed but did not sync. The append itself returned nil — the entry is a
// record — so DrainWarnings is the only channel the sync failure has.
func (w *Writer) queueWarningLocked(err error) {
	w.retainedWarnings = append(w.retainedWarnings, err)
	w.pendingWarnings.Store(int32(len(w.retainedWarnings)))
}

// countAppendedEntryLocked spends the entry's sequence number and counts the
// failures the entry settles. Both figures are statements about the transcript,
// so they move for exactly the entries a later reader of that file would see —
// which is why a rollback that could not take a written entry back out spends
// them too, and a rollback that removed the entry does not.
func (w *Writer) countAppendedEntryLocked(turn schema.Turn) {
	w.tail.nextSeq++
	w.failures.Observe(turn)
}

// writeLineLocked writes the whole line, reporting how much of it reached the
// file. The count is what decides whether a failed write left a record behind
// or only the remains of one.
func (w *Writer) writeLineLocked(line []byte) (int, error) {
	written := 0
	for len(line) > 0 {
		n, err := w.file.Write(line)
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
		line = line[n:]
	}
	return written, nil
}

// rollbackAppendLocked takes the entry written at startOffset back out of the
// file. It reports whether the entry is gone, which only the truncate decides:
// a truncate that succeeded has removed the entry even when the seek or sync
// after it fail, and a truncate that failed leaves the entry where a later
// reader will find it. It also records on the writer whether the file's end is
// still known, since the seek that restores it is the step that can fail on its
// own.
func (w *Writer) rollbackAppendLocked(startOffset int64) (removed bool, err error) {
	truncateErr := w.file.Truncate(startOffset)
	_, seekErr := w.file.Seek(0, io.SeekEnd)
	removed = truncateErr == nil
	// Only a truncate that moved the end and a seek that could not follow it
	// leave the position wrong. A truncate that failed left the entry in the
	// file, so the position this append reached is still the file's end. Set
	// only: no rollback outcome establishes an end this writer had already lost.
	if removed && seekErr != nil {
		w.positionUnknown = true
	}
	if truncateErr != nil && seekErr != nil {
		return removed, fmt.Errorf("truncate to %d: %w; seek eof: %w", startOffset, truncateErr, seekErr)
	}
	if truncateErr != nil {
		return removed, fmt.Errorf("truncate to %d: %w", startOffset, truncateErr)
	}
	if seekErr != nil {
		return removed, fmt.Errorf("seek eof: %w", seekErr)
	}
	if syncErr := w.file.Sync(); syncErr != nil {
		return removed, fmt.Errorf("sync rollback truncate: %w", syncErr)
	}
	return removed, nil
}

// Close syncs and closes the underlying file. Idempotent: safe to call multiple times.
// No-op if the receiver is nil.
func (w *Writer) Close() error {
	if w == nil || w.file == nil {
		return nil
	}

	// Acquire mu so any in-flight Append finishes before we close.
	w.mu.Lock()
	w.closed.Store(true)
	w.mu.Unlock()

	var closeErr error
	w.closeOnce.Do(func() {
		// Flush any dirty writes before closing.
		if w.dirty {
			if err := w.file.Sync(); err != nil {
				closeErr = fmt.Errorf("sync transcript on close: %w", err)
			}
			w.dirty = false
		}
		if err := w.file.Close(); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("close transcript file: %w", err)
		}
		w.releaseTail.Stop()
		runtime.KeepAlive(w) // Stop removes the cleanup only if w is reachable across it
		w.tail.release()
	})
	return closeErr
}

// OpenWriter opens an existing transcript file for appending.
// Reads the file once to count valid entries and determine the next seq number.
// Truncates any partial last line for crash recovery. Uses a single file handle
// for the entire read-truncate-append sequence to avoid TOCTOU races.
func OpenWriter(path string) (*Writer, error) {
	w, _, err := openWriter(path, "")
	return w, err
}

// OpenWriterForSession opens a transcript for resume, requiring its header to
// belong to expectedSessionID and returning the validated semantic entries.
func OpenWriterForSession(path, expectedSessionID string) (*Writer, []Entry, error) {
	return openWriter(path, expectedSessionID)
}

// OpenWriterForSessionWithFS is the filesystem-injecting form of
// OpenWriterForSession. It preserves the same identity validation and semantic
// entry return while allowing a caller that already owns a filesystem boundary
// to resume through it.
//
// Writers on one file share its append tail (see appendTail) only when fs
// names files as they are on the real disk, as afero.NewOsFs does: the tail
// pins the file by that name. Through any other filesystem each writer gets a
// tail of its own, so two writers on one file through it are not coordinated.
func OpenWriterForSessionWithFS(fs afero.Fs, path, expectedSessionID string) (*Writer, []Entry, error) {
	return resumeWriter(fs, func() (afero.File, error) { return fs.OpenFile(path, os.O_RDWR, 0o644) }, expectedSessionID)
}

func openWriter(path, expectedSessionID string) (*Writer, []Entry, error) {
	return resumeWriter(afero.NewOsFs(), func() (afero.File, error) { return openTranscriptAppendFile(path) }, expectedSessionID)
}

// openWriterFS is the filesystem-injecting seam used by tests and the
// persistence fuzzer. Production uses openWriter so it can refuse symlinks at
// the operating-system open boundary.
func openWriterFS(fs afero.Fs, path string) (*Writer, error) {
	w, _, err := resumeWriter(fs, func() (afero.File, error) { return fs.OpenFile(path, os.O_RDWR, 0o644) }, "")
	return w, err
}

// resumeWriter opens an existing transcript through open and rebuilds a writer
// from its complete records.
func resumeWriter(fs afero.Fs, open func() (afero.File, error), expectedSessionID string) (*Writer, []Entry, error) {
	f, tail, err := openAppendTail(open)
	if err != nil {
		return nil, nil, err
	}
	// Scan under the tail so another writer's append is either wholly before
	// the scan or wholly after it: never a crash tail to truncate.
	tail.mu.Lock()
	defer tail.mu.Unlock()
	header, entries, nextSeq, err := scanForResume(f, expectedSessionID)
	if err != nil {
		_ = f.Close() // cleanup on error path; the scan error is what matters
		tail.release()
		return nil, nil, err
	}
	// Another writer still open on the file may have used more of the sequence
	// than the file shows; never go back below it.
	tail.nextSeq = max(tail.nextSeq, nextSeq)
	return newWriterOnTail(fs, f, tail, header, tail.moved()), entries, nil
}

// scanForResume validates the transcript, truncates any crash tail, positions
// f at the end, and returns the header, the entries, and the next sequence
// number.
func scanForResume(f afero.File, expectedSessionID string) (Header, []Entry, int, error) {
	// Validate complete v2 records while finding the next sequence and the byte
	// boundary before any crash tail. The shared framer drains an arbitrarily
	// large unterminated tail without retaining the file in memory.
	maxSeq := -1
	// entries is non-nil even for a header-only transcript: the
	// delegate-attention fold keys on nilness to decide whether it can fold
	// in memory or must re-read the file.
	entries := make([]Entry, 0)
	reader := bufio.NewReaderSize(f, 64*1024)
	var validLen int64
	hasPartialTail := false
	headerRead := false
	var header Header
	for {
		line, complete, bytesRead, readErr := ReadLine(reader, transcriptJSONLMaxLineBytes)
		if readErr != nil {
			return Header{}, nil, 0, fmt.Errorf("read transcript for resume: %w", readErr)
		}
		if !complete {
			hasPartialTail = bytesRead > 0
			break
		}
		validLen += bytesRead
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !headerRead {
			var err error
			header, err = DecodeHeader(line)
			if err != nil {
				return Header{}, nil, 0, fmt.Errorf("parse transcript header: %w", err)
			}
			if expectedSessionID != "" && header.SessionID != expectedSessionID {
				return Header{}, nil, 0, fmt.Errorf("transcript header session ID %q does not match requested session ID %q", header.SessionID, expectedSessionID)
			}
			headerRead = true
			continue
		}
		entry, err := DecodeEntry(line)
		if err != nil {
			return Header{}, nil, 0, fmt.Errorf("parse transcript entry: %w", err)
		}
		entries = append(entries, entry)
		if entry.Seq > maxSeq {
			maxSeq = entry.Seq
		}
	}
	if !headerRead {
		if hasPartialTail && validLen == 0 {
			return Header{}, nil, 0, errors.New("transcript has no complete lines")
		}
		return Header{}, nil, 0, fmt.Errorf("%w: missing transcript header", ErrUnsupportedFormat)
	}

	if hasPartialTail {
		if err := f.Truncate(validLen); err != nil {
			return Header{}, nil, 0, fmt.Errorf("truncate partial line: %w", err)
		}
	}

	// Use max(seq)+1 so resumed writes never collide with existing entries,
	// even if earlier entries were lost to crash recovery.
	nextSeq := 0
	if maxSeq >= 0 {
		nextSeq = maxSeq + 1
	}

	// Seek to end for subsequent appends.
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return Header{}, nil, 0, fmt.Errorf("seek to end of transcript: %w", err)
	}

	return header, entries, nextSeq, nil
}
