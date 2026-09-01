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
	"strings"
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

// Entry is a single turn in the transcript JSONL file.
type Entry struct {
	Kind string      `json:"kind"` // Always "entry"
	Seq  int         `json:"seq"`  // monotonically increasing line sequence number
	Turn schema.Turn `json:"turn"` // the recorded conversation turn
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
	return entry, nil
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
	fs        afero.Fs
	file      afero.File
	mu        sync.Mutex
	seq       int
	closeOnce sync.Once
	closed    atomic.Bool
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

	// sidecarEntryCount counts entries appended so far, seeded by
	// the resume constructors (prefix + suffix for a windowed read, the
	// whole list for a full scan, zero for a fresh writer) and advanced by
	// every append. WriteSidecarFromWriter reports it as the sidecar's
	// EntryCount. Guarded by mu.
	sidecarEntryCount int

	// lastCheckpointStart is the byte offset where the most recently appended
	// CHECKPOINT/SUMMARY entry BEGINS, or -1 before one exists. It is the
	// writer-side mirror of the resume scan's lastCheckpointOffset: the resume
	// window starts AT the checkpoint entry (ResumeHistory returns
	// [checkpoint, ...rest]), so a sidecar written at compaction must point
	// at that entry's start, not at the append position after it.
	// checkpointPrefixSeq and checkpointPrefixCount record, at the moment
	// that entry was appended, the facts the sidecar's prefix half needs:
	// the seq of the last entry BEFORE the checkpoint and the count of those
	// entries — the same prefix-relative facts the post-full-scan anchor
	// derives. Guarded by mu; -1/-0 mean "no anchor yet" (count 0 is only
	// valid with seq -1: a checkpoint-first transcript has no prefix to skip,
	// and the scan anchor refuses that case).
	lastCheckpointStart   int64
	checkpointPrefixSeq   int
	checkpointPrefixCount int
	// lastAppendedSeq is the seq of the most recently appended entry (-1
	// before any): the checkpoint capture reads it to learn the LAST PREFIX
	// entry's seq — the boundary record the sidecar cross-checks the suffix's
	// first entry against. Guarded by mu.
	lastAppendedSeq int
	// checkpointFailureFloor is the writer's failure count at the moment the
	// checkpoint was appended — the floor over the prefix entries, the same
	// figure the post-full-scan anchor derives by counting the prefix.
	checkpointFailureFloor int

	// writePos is the writer's byte position at the end of the last line it
	// wrote — the seek-end the append path would otherwise have to ask the
	// file for. Seeding it from the constructors' scans avoids a Seek per
	// append (which would also shift the fault-injection op indices the
	// fault tests count on) and keeps lastCheckpointStart's captured entry
	// starts exact. Guarded by mu.
	writePos int64

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

// TrackFailuresSeeded is TrackFailures for a windowed resume: the counter
// starts at the sidecar's prefix floor (positioned at prefixEntryCount so
// suffix ordinals continue the sequence) and then observes the suffix
// entries the full seed would have counted. A floor below zero — the
// sidecar's "not computed" sentinel — is an error the caller must answer by
// falling back to the full scan; a silent zero would report a session-wide
// clean the transcript cannot vouch for.
func (w *Writer) TrackFailuresSeeded(suffix []Entry, floor, prefixEntryCount, fromEntryOrdinal int) error {
	if w == nil {
		return nil
	}
	counter, err := NewFailureCounterSeeded(floor, prefixEntryCount, fromEntryOrdinal)
	if err != nil {
		return err
	}
	for _, entry := range suffix {
		counter.Observe(entry.Turn)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failures = counter
	return nil
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
// keep transcript persistence on that filesystem.
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

	f, err := fs.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create transcript file: %w", err)
	}

	data, err := json.Marshal(header)
	if err != nil {
		_ = f.Close() // cleanup on error path; the marshal error is what matters
		return nil, fmt.Errorf("marshal transcript header: %w", err)
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close() // cleanup on error path; the write error is what matters
		return nil, fmt.Errorf("write transcript header: %w", err)
	}

	if sync {
		if err := f.Sync(); err != nil {
			_ = f.Close() // cleanup on error path; the sync error is what matters
			return nil, fmt.Errorf("sync transcript header: %w", err)
		}
	}

	return &Writer{fs: fs, file: f, lastSync: time.Now(), header: header, lastCheckpointStart: -1, lastAppendedSeq: -1, writePos: int64(len(data)) + 1}, nil
}

// isCheckpointTurn reports whether a turn defines a compaction boundary —
// the kinds ResumeHistory restarts live history from. The sidecar's offset
// keys at the START of the entry carrying such a turn.
func isCheckpointTurn(turn schema.Turn) bool {
	return turn.Kind == schema.TurnCheckpoint || turn.Kind == schema.TurnSummary
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
func (w *Writer) Append(turn schema.Turn) error {
	return w.append(turn, false)
}

// AppendDurable writes a turn and fsyncs it before returning.
// Safe for concurrent use. No-op if the receiver is nil — see Append for why
// that makes a write into a not-yet-open writer silently succeed.
func (w *Writer) AppendDurable(turn schema.Turn) error {
	return w.append(turn, true)
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

// WriteSidecarFromWriter writes the resume sidecar for this writer's file,
// anchoring at the START of the last appended CHECKPOINT/SUMMARY entry — the
// same boundary the post-full-scan anchor uses, so both anchors define the
// identical resume window: the suffix a windowed resume decodes is
// [checkpoint, ...rest], exactly ResumeHistory's window. The prefix is
// everything before that entry. It is the compaction anchor. Floor facts
// (entry count, max seq, failure floor) come from the writer's own running
// state; the caller supplies the fold snapshots it can vouch for, with
// complete reporting whether they cover every prefix entry. Best-effort: a
// failure is returned but must never block the calling session operation.
func (w *Writer) WriteSidecarFromWriter(path string, pending []SidecarPendingAttention, commits []SidecarDeliveryCommit, mutations map[string]string, complete bool) error {
	if w == nil || w.file == nil {
		return errors.New("transcript writer is nil")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed.Load() {
		return errors.New("transcript writer is closed")
	}
	// One stat covers both facts (identity and size): fsync changes neither
	// the size nor the mtime, so the post-sync re-stat the first draft carried
	// was redundant — unlike writeSidecarAfterFullScan, whose second stat IS
	// load-bearing (the scan truncated a crash tail between them).
	info, err := w.file.Stat()
	if err != nil {
		return fmt.Errorf("stat transcript for sidecar: %w", err)
	}
	// Seek-end reports the append position; a dirty tail is not yet durable,
	// so establish the barrier first — the sidecar must never vouch for
	// bytes the file may still lose.
	if w.dirty {
		if err := w.file.Sync(); err != nil {
			return fmt.Errorf("sync transcript for sidecar: %w", err)
		}
		w.dirty = false
		w.lastSync = time.Now()
	}
	if w.lastCheckpointStart < 0 {
		return errors.New("transcript has no checkpoint to anchor the sidecar at")
	}
	offset := w.lastCheckpointStart
	if w.checkpointPrefixCount == 0 || w.checkpointPrefixSeq < 0 {
		// A checkpoint-first transcript: nothing before the boundary to skip.
		// The post-full-scan anchor refuses the same case.
		return errors.New("transcript has no prefix before the checkpoint to anchor")
	}
	sidecar := ResumeSidecar{
		Version:                 resumeSidecarVersion,
		TranscriptFormatVersion: FormatVersion,
		SessionID:               w.header.SessionID,
		TranscriptSize:          info.Size(),
		ValidBytes:              info.Size(),
		Offset:                  offset,
		MaxSeq:                  w.checkpointPrefixSeq,
		EntryCount:              w.checkpointPrefixCount,
		PrefixTurnCount:         -1, // not computable without the prefix entries
		FailureFloor:            w.checkpointFailureFloor,
		FileIdentity:            sidecarFileIdentity(info),
		ModTimeUnixNS:           info.ModTime().UnixNano(),
		BoundarySeq:             w.checkpointPrefixSeq,
		SnapshotsComplete:       complete,
		PendingAttention:        pending,
		DeliveryCommits:         commits,
		ClientMutationTurns:     mutations,
	}
	sidecar.FirstAnchor, sidecar.TailAnchor = sidecarAnchors(w.file, offset)
	if sidecar.FirstAnchor.Length <= 0 || sidecar.TailAnchor.Length <= 0 {
		return errors.New("transcript anchors are empty")
	}
	return WriteSidecar(path, sidecar)
}

func (w *Writer) append(turn schema.Turn, forceSync bool) error {
	if w == nil || w.closed.Load() {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Re-check after acquiring lock: Close may have raced between the
	// fast-path check above and the lock acquisition.
	if w.closed.Load() {
		return nil
	}

	entry := Entry{
		Kind: "entry",
		Seq:  w.seq,
		Turn: turn,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal transcript entry: %w", err)
	}

	var startOffset int64
	if forceSync {
		var err error
		startOffset, err = w.file.Seek(0, io.SeekEnd)
		if err != nil {
			return fmt.Errorf("seek transcript append start: %w", err)
		}
		w.writePos = startOffset
	} else {
		startOffset = w.writePos
	}

	previousDirty := w.dirty
	if err := w.writeLineLocked(append(data, '\n')); err != nil {
		if forceSync {
			return w.appendFailureLocked("write transcript entry", err, startOffset)
		}
		return fmt.Errorf("write transcript entry: %w", err)
	}

	w.dirty = true
	if forceSync || w.SyncInterval == 0 || time.Since(w.lastSync) >= w.SyncInterval {
		if err := w.file.Sync(); err != nil {
			if forceSync {
				if rollbackErr := w.rollbackAppendLocked(startOffset); rollbackErr != nil {
					return fmt.Errorf("sync transcript entry: %w; rollback failed: %w", err, rollbackErr)
				}
				w.dirty = previousDirty
				return fmt.Errorf("sync transcript entry: %w", err)
			}
			return fmt.Errorf("sync transcript entry: %w", err)
		}
		w.lastSync = time.Now()
		w.dirty = false
	}

	w.seq++
	if isCheckpointTurn(turn) {
		// The prefix facts are the state BEFORE this checkpoint entry:
		// sidecarEntryCount and seq have not yet counted it, so capturing here
		// (before the increment below) records exactly the entries the
		// sidecar's prefix covers — the same facts the post-full-scan anchor
		// derives as len(entries[:checkpointIdx]) and prefix[n-1].Seq.
		w.lastCheckpointStart = startOffset
		w.checkpointPrefixSeq = w.lastAppendedSeq
		w.checkpointPrefixCount = w.sidecarEntryCount
		// The absent floor is -1, not 0: a nil counter has measured nothing,
		// and "zero failures over the prefix" from a producer that never
		// counted is the false all-clear the -1 sentinel exists to prevent
		// (the same convention FailureFloor documents).
		w.checkpointFailureFloor = -1
		if w.failures != nil {
			w.checkpointFailureFloor = w.failures.Count()
		}
	}
	// The entry just written carried seq w.seq-1 (Entry.Seq is assigned
	// before the increment above).
	w.lastAppendedSeq = w.seq - 1
	w.sidecarEntryCount++
	// Counted only once the entry is on its way to the file and no rollback can
	// take it back: the figure is a statement about the transcript, so it moves
	// for exactly the entries a later reader of that transcript would see.
	w.failures.Observe(turn)
	w.writePos = startOffset + int64(len(data)) + 1
	return nil
}

func (w *Writer) writeLineLocked(line []byte) error {
	for len(line) > 0 {
		n, err := w.file.Write(line)
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

func (w *Writer) appendFailureLocked(operation string, err error, startOffset int64) error {
	if rollbackErr := w.rollbackAppendLocked(startOffset); rollbackErr != nil {
		return fmt.Errorf("%s: %w; rollback failed: %w", operation, err, rollbackErr)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func (w *Writer) rollbackAppendLocked(startOffset int64) error {
	// The in-memory position moves with the file: a rollback that truncates
	// must not leave writePos (or a checkpoint anchor captured inside the
	// rolled-back range) claiming bytes the file no longer holds.
	if w.lastCheckpointStart >= startOffset {
		w.lastCheckpointStart = -1
		w.checkpointPrefixSeq = 0
		w.checkpointPrefixCount = 0
		w.checkpointFailureFloor = 0
	}
	w.writePos = startOffset
	truncateErr := w.file.Truncate(startOffset)
	_, seekErr := w.file.Seek(0, io.SeekEnd)
	if truncateErr != nil && seekErr != nil {
		return fmt.Errorf("truncate to %d: %w; seek eof: %w", startOffset, truncateErr, seekErr)
	}
	if truncateErr != nil {
		return fmt.Errorf("truncate to %d: %w", startOffset, truncateErr)
	}
	if seekErr != nil {
		return fmt.Errorf("seek eof: %w", seekErr)
	}
	if syncErr := w.file.Sync(); syncErr != nil {
		return fmt.Errorf("sync rollback truncate: %w", syncErr)
	}
	return nil
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
	w, scan, err := openWriter(path, expectedSessionID)
	return w, scan.entries, err
}

// OpenWriterForSessionWithFS is the filesystem-injecting form of
// OpenWriterForSession. It preserves the same identity validation and semantic
// entry return while allowing a caller that already owns a filesystem boundary
// to resume through it.
func OpenWriterForSessionWithFS(fs afero.Fs, path, expectedSessionID string) (*Writer, []Entry, error) {
	f, err := fs.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("open transcript for resume: %w", err)
	}
	w, scan, err := resumeWriter(fs, f, expectedSessionID)
	return w, scan.entries, err
}

// RefreshSidecarFromFullScan re-derives and rewrites the resume sidecar by
// scanning the whole transcript — the opportunistic anchor, exported for the
// one caller that pays a full decode anyway: serve's file-form fallback for
// a compaction-anchored sidecar (whose PrefixTurnCount is -1, so windowed
// turn paging cannot be armed from it). That fallback re-reads the whole
// file for the identity projection; this refresh converts the same cost
// into the sidecar the NEXT resume windows on, instead of leaving the
// session re-reading the file on every resume forever.
//
// Every refusal writeSidecarAfterFullScan applies (no checkpoint, no prefix,
// inconsistent fold, unreadable anchors) leaves the sidecar untouched — the
// caller's read already succeeded, and a refresh that cannot vouch for the
// file must not replace what an earlier anchor wrote. Best-effort by
// contract: an error is reported but never blocks the caller's operation.
func RefreshSidecarFromFullScan(path, expectedSessionID string) error {
	f, err := openTranscriptAppendFile(path)
	if err != nil {
		return fmt.Errorf("open transcript for sidecar refresh: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat transcript for sidecar refresh: %w", err)
	}
	writer, scan, err := resumeWriter(afero.NewOsFs(), f, expectedSessionID)
	if err != nil {
		return fmt.Errorf("scan transcript for sidecar refresh: %w", err)
	}
	// The anchor write needs the file open (its second stat and its anchors
	// read through it); the writer owns it, so close only after.
	writeSidecarAfterFullScan(path, f, info, scan, expectedSessionID)
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close transcript after sidecar refresh: %w", err)
	}
	return nil
}

func openWriter(path, expectedSessionID string) (*Writer, scanResult, error) {
	f, err := openTranscriptAppendFile(path)
	if err != nil {
		return nil, scanResult{}, fmt.Errorf("open transcript for resume: %w", err)
	}
	return resumeWriter(afero.NewOsFs(), f, expectedSessionID)
}

// ResumeView is what a windowed resume learned about the transcript prefix it
// did not decode. It is populated only by OpenWriterForResume; the full-scan
// fallback also fills it (with PrefixEntryCount covering the whole file and
// empty snapshots), so the restore path has one shape regardless of which
// read actually ran.
type ResumeView struct {
	// Entries are the decoded entries of the suffix. When a sidecar was
	// used this is only the entries after the validated offset; otherwise it
	// is every entry in the file (identical to the legacy
	// OpenWriterForSession return).
	Entries []Entry
	// PrefixEntryCount is the number of entries before the first entry of
	// Entries. Entry i of Entries has global 1-based position
	// PrefixEntryCount + i + 1 — the position its turn id is minted from.
	PrefixEntryCount int
	// PrefixTurnCount is the sidecar's count of turn positions the full
	// AppWire projection holds below Entries (-1 when the anchor did not
	// compute it — the compaction anchor cannot without the prefix entries).
	// A caller arming windowed turn paging needs it; without it the only
	// honest fallback is the full projection.
	PrefixTurnCount int
	// SidecarUsed reports whether the windowed read validated a sidecar.
	// False means the full scan ran (including the fallback paths).
	SidecarUsed bool
	// Sidecar carries the validated prefix snapshot. It is the zero value
	// when SidecarUsed is false; restore consults its fold snapshots only
	// when it needs them, and falls back to a full scan when a needed
	// snapshot is incomplete.
	Sidecar ResumeSidecar
}

// OpenWriterForResume opens a transcript for resume the way
// OpenWriterForSession does, but windowed: when a validated sidecar exists,
// only the entries after its offset are decoded. Every mismatch — missing,
// corrupt, stale, truncated, or boundary-violating sidecar — falls back to
// the same full scan OpenWriterForSession performs, never an error.
//
// expectedSessionID is validated against the transcript header on BOTH
// paths: the windowed read still decodes the header line itself.
func OpenWriterForResume(path, expectedSessionID string) (*Writer, ResumeView, error) {
	f, err := openTranscriptAppendFile(path)
	if err != nil {
		return nil, ResumeView{}, fmt.Errorf("open transcript for resume: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, ResumeView{}, fmt.Errorf("stat transcript for resume: %w", err)
	}
	if sidecar, ok := ReadSidecar(path); ok {
		if view, writer, ok := resumeWindowed(f, info, sidecar, expectedSessionID); ok {
			return writer, view, nil
		}
		// Validation failed after the file was read past its header; reopen
		// for the full scan so the shared reader starts at byte zero.
		_ = f.Close()
		f, err = openTranscriptAppendFile(path)
		if err != nil {
			return nil, ResumeView{}, fmt.Errorf("open transcript for resume: %w", err)
		}
	}
	writer, scan, err := resumeWriter(afero.NewOsFs(), f, expectedSessionID)
	if err != nil {
		return nil, ResumeView{}, err
	}
	// Opportunistic anchor: the full scan decoded every entry (and recorded
	// the last checkpoint's offset), so the sidecar it writes carries
	// complete snapshots and the NEXT resume of this session can be
	// windowed. A write failure is non-fatal — this resume already
	// succeeded, and the next one simply falls back to another full scan.
	writeSidecarAfterFullScan(path, f, info, scan, expectedSessionID)
	return writer, ResumeView{Entries: scan.entries, PrefixEntryCount: 0, SidecarUsed: false}, nil
}

// writeSidecarAfterFullScan writes the opportunistic resume sidecar from a
// completed full scan. It is best-effort: every failure is swallowed because
// the sidecar is an optimization, never a correctness dependency.
//
// The offset is CHECKPOINT-RELATIVE: it points at the start of the last
// checkpoint entry (TurnCheckpoint/TurnSummary), so the suffix a windowed
// resume decodes is exactly ResumeHistory's window ([last checkpoint,
// ...rest]) — the live history every resume consumer derives from. A
// transcript with no checkpoint gets NO sidecar: all of its entries are live
// history, so a windowed read would skip exactly the entries restore needs.
//
// The snapshots ARE complete for this anchor — the full scan decoded every
// entry — so they are computed here: the attention fold, delivery commits,
// client-mutation turns, and the divergence-bounded failure floor. fromEntry
// ordinal is 0 (the anchor is written by the transcript package, which has
// no fork knowledge; a fork child's file never validates this sidecar
// anyway, because its file identity differs from the parent's).
func writeSidecarAfterFullScan(path string, f *os.File, info os.FileInfo, scan scanResult, expectedSessionID string) {
	entries := scan.entries
	// The sidecar must only be written over a file whose current state this
	// scan vouches for. A partial tail was truncated by resumeWriter; stat
	// again so TranscriptSize reflects the post-truncation size.
	current, err := f.Stat()
	if err != nil {
		return
	}
	// The scan recorded where the last checkpoint entry starts; no second
	// file pass. No checkpoint means every entry is live history — no
	// sidecar.
	offset := scan.lastCheckpointOffset
	if offset <= 0 {
		return
	}
	checkpointIdx := lastCheckpointEntry(entries)
	if checkpointIdx < 0 {
		return
	}
	prefix := entries[:checkpointIdx]
	if len(prefix) == 0 {
		// A checkpoint-first transcript (nothing before the checkpoint)
		// has no prefix to skip either.
		return
	}
	boundarySeq := prefix[len(prefix)-1].Seq
	// The exact prefix-turn count: the prelude rule is header-derived (a
	// non-empty SystemPrompt projects one), and the per-kind emission rule is
	// the same one the projection applies per entry. Counted over the prefix
	// because a windowed turn snapshot pages in the full projection's cursor
	// space, and only this anchor holds the prefix entries to count.
	turnsBelow := prefixTurnCount(scan.header, prefix)
	sidecar := ResumeSidecar{
		Version:                 resumeSidecarVersion,
		TranscriptFormatVersion: FormatVersion,
		SessionID:               expectedSessionID,
		TranscriptSize:          current.Size(),
		ValidBytes:              current.Size(),
		Offset:                  offset,
		MaxSeq:                  boundarySeq,
		EntryCount:              len(prefix),
		PrefixTurnCount:         turnsBelow,
		FileIdentity:            sidecarFileIdentity(info),
		ModTimeUnixNS:           current.ModTime().UnixNano(),
		BoundarySeq:             boundarySeq,
		SnapshotsComplete:       true,
	}
	// The failure floor is computed exactly the way a TrackFailures seed
	// would count the PREFIX entries — same counter, same rule — so the
	// windowed resume that draws from it reports the figure the full scan
	// would have seeded.
	counter := NewFailureCounter(0)
	for _, entry := range prefix {
		counter.Observe(entry.Turn)
	}
	sidecar.FailureFloor = counter.Count()
	// Fold snapshots over the WHOLE entry list (the fold is a file-wide
	// invariant; the boundary only decides which entries the next resume
	// re-decodes): pending attentions with content, delivery commits, and
	// the client-mutation identity index. A fold inconsistency is reported,
	// not swallowed: the anchor then writes no sidecar at all, so the resume
	// falls back to the full scan instead of trusting an empty snapshot a
	// broken fold produced.
	pending, commits, mutations, foldOK := foldSnapshotForSidecar(entries)
	if !foldOK {
		return
	}
	sidecar.PendingAttention, sidecar.DeliveryCommits, sidecar.ClientMutationTurns = pending, commits, mutations
	sidecar.FirstAnchor, sidecar.TailAnchor = sidecarAnchors(f, offset)
	if sidecar.FirstAnchor.Length <= 0 || sidecar.TailAnchor.Length <= 0 {
		return
	}
	_ = WriteSidecar(path, sidecar)
}

// lastCheckpointEntry returns the index of the first entry of the last
// TurnCheckpoint/TurnSummary turn, or -1 when none exists. The FIRST entry of
// the checkpoint matters because the offset must point AT it: ResumeHistory
// returns [checkpoint, ...subsequent], so the suffix must include the
// checkpoint entry itself.
func lastCheckpointEntry(entries []Entry) int {
	last := -1
	for i := range entries {
		if isCheckpointTurn(entries[i].Turn) {
			last = i
		}
	}
	return last
}

// prefixTurnCount counts the turn positions a full AppWire projection holds
// over one entry list plus its header: the prelude (a non-empty
// SystemPrompt projects one) plus one position per entry whose turn projects
// at least one thread item. It mirrors internal/apptranscript's rules
// (PreludeTurn, ProjectTurn) because this package cannot import that one;
// the differential test that keeps the two honest is
// TestWindowedSnapshotMatchesFullProjectionDifferential, which fails if the
// count and the projection disagree over the same entries.
func prefixTurnCount(header Header, entries []Entry) int {
	count := 0
	if strings.TrimSpace(header.SystemPrompt) != "" {
		count++
	}
	for i := range entries {
		if projectsThreadItems(entries[i].Turn) {
			count++
		}
	}
	return count
}

// projectsThreadItems reports whether the AppWire projector emits at least
// one thread item for a turn — whether the entry carrying it occupies a turn
// position. The kind rules restated from ProjectTurn: the marker kinds emit
// nothing when they carry no text (a checkpoint with empty text renders no
// row), while every conversation kind (user, assistant, steering, tool
// results, failure, resolution) always projects.
func projectsThreadItems(turn schema.Turn) bool {
	switch turn.Kind {
	case schema.TurnCheckpoint, schema.TurnSummary, schema.TurnModelSwitch, schema.TurnEnvironment, schema.TurnHookCompleted:
		if strings.TrimSpace(turn.Message.Text()) != "" {
			return true
		}
		return turn.Kind == schema.TurnHookCompleted && turn.Hook != nil
	default:
		return true
	}
}

// foldSnapshotForSidecar computes the fold snapshots a full-scan anchor can
// vouch for: the delegate-attention fold over every entry, projected into
// the sidecar's snapshot types. The agent's file fold is the authority; this
// restates its rules rule-for-rule, in file order, because the agent imports
// this package and the reverse edge would be a cycle. Every entry shape the
// file fold rejects — a non-steering attention turn, a conflicting or
// unmarshalable re-send, a resolution turn with no resolution, a resolution
// before its append, an unknown disposition, a conflicting re-resolution, a
// resume generation claimed twice, a delivery commit off a tool-results turn
// or referencing an absent or conflicting tool call — is refused here too
// (ok=false, nothing written), so the anchor never writes a snapshot the
// seeded fold would accept over a file the full read errors on.
//
// The attention rows key on the attention ID, not the entry: a re-sent
// steering turn with identical content is a legal transcript (a second
// sighting adds nothing), so the snapshot records the attention once —
// first sighting's content, its resolution and resume generation carried
// when one exists — with the ids in first-sighting order, matching the
// fold's order slice.
func foldSnapshotForSidecar(entries []Entry) (pending []SidecarPendingAttention, commits []SidecarDeliveryCommit, mutations map[string]string, ok bool) {
	pending = nil
	commits = nil
	mutations = map[string]string{}
	commitIDs := map[string]string{}
	// The reverse of commitIDs (tool call → delivery), so the per-commit
	// conflict check is a lookup instead of a scan over every prior commit —
	// the scan made the snapshot quadratic over delivery-heavy transcripts.
	commitToolCalls := map[string]string{}
	for i := range entries {
		turn := entries[i].Turn
		if turn.ClientMutationID != "" {
			mutations[turn.ClientMutationID] = turn.StableTurnID
		}
		if len(turn.DelegateDeliveryCommits) > 0 {
			// The fold's own rules for delivery commits, mirrored: commits ride
			// only on tool-results turns, name both identities, reference a
			// tool result the turn's message actually carries, and never
			// conflict with a delivery or tool call claimed earlier in the
			// file. A commit violating any of these is a file the full fold
			// errors on — the snapshot must refuse it, not carry it.
			if turn.Kind != schema.TurnToolResults {
				return nil, nil, nil, false
			}
			resultIDs := map[string]bool{}
			for _, part := range turn.Message.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID != "" {
					resultIDs[part.ToolResult.ToolCallID] = true
				}
			}
			for _, commit := range turn.DelegateDeliveryCommits {
				if commit.DeliveryID == "" || commit.ToolCallID == "" || !resultIDs[commit.ToolCallID] {
					return nil, nil, nil, false
				}
				if _, seen := commitIDs[commit.DeliveryID]; seen {
					// A delivery named twice: the fold accepts it only when
					// the repeat names the same tool call.
					if commitIDs[commit.DeliveryID] != commit.ToolCallID {
						return nil, nil, nil, false
					}
					continue
				}
				if other, claimed := commitToolCalls[commit.ToolCallID]; claimed && other != commit.DeliveryID {
					return nil, nil, nil, false
				}
				commitIDs[commit.DeliveryID] = commit.ToolCallID
				commitToolCalls[commit.ToolCallID] = commit.DeliveryID
				commits = append(commits, SidecarDeliveryCommit{DeliveryID: commit.DeliveryID, ToolCallID: commit.ToolCallID})
			}
		}
	}
	// Attention pass, in file order: rows is the fold state at the end of the
	// entry list, order the attention IDs in first-sighting order (what the
	// fold's order slice holds).
	rows := map[string]SidecarPendingAttention{}
	order := []string{}
	generations := map[uint64]string{}
	for i := range entries {
		turn := entries[i].Turn
		if turn.AttentionID != "" {
			// The fold admits an attention only as a steering turn carrying no
			// resolution of its own; anything else it rejects on sight.
			if turn.Kind != schema.TurnSteering || turn.AttentionResolution != nil {
				return nil, nil, nil, false
			}
			// A message the seeded fold could not reconstruct is a state the
			// snapshot must refuse, not carry.
			encoded, err := json.Marshal(turn.Message)
			if err != nil {
				return nil, nil, nil, false
			}
			if row, sighted := rows[turn.AttentionID]; sighted {
				// A re-send with identical content is a no-op (the fold
				// keeps the first sighting); different content is the fold's
				// "conflicting content" error.
				if !bytes.Equal(row.Message, encoded) {
					return nil, nil, nil, false
				}
				continue
			}
			rows[turn.AttentionID] = SidecarPendingAttention{AttentionID: turn.AttentionID, Message: JSONMessage(encoded)}
			order = append(order, turn.AttentionID)
		}
		r := turn.AttentionResolution
		if r == nil {
			// A resolution-kind turn with no resolution pointer is the fold's
			// "attention resolution turn has no resolution" error.
			if turn.Kind == schema.TurnAttentionResolution {
				return nil, nil, nil, false
			}
			continue
		}
		// The fold admits a resolution only as a resolution turn whose
		// pointer names the attention.
		if turn.Kind != schema.TurnAttentionResolution || turn.AttentionID != "" || r.AttentionID == "" {
			return nil, nil, nil, false
		}
		row, sighted := rows[r.AttentionID]
		if !sighted {
			// "Resolved before it was appended" — order matters to the
			// fold, so it matters here.
			return nil, nil, nil, false
		}
		if r.Disposition != schema.AttentionDispositionConsumed && r.Disposition != schema.AttentionDispositionDiscarded {
			return nil, nil, nil, false
		}
		if r.Disposition == schema.AttentionDispositionDiscarded && r.ResumeGeneration != 0 {
			return nil, nil, nil, false
		}
		if row.Resolution != "" {
			// An identical re-resolution is the fold's no-op; anything
			// else is its "conflicting resolutions" error.
			if row.Resolution != r.Disposition || row.ResumeGeneration != r.ResumeGeneration {
				return nil, nil, nil, false
			}
			continue
		}
		if r.ResumeGeneration != 0 {
			if other, claimed := generations[r.ResumeGeneration]; claimed && other != r.AttentionID {
				return nil, nil, nil, false
			}
			generations[r.ResumeGeneration] = r.AttentionID
		}
		row.Resolution = r.Disposition
		row.ResumeGeneration = r.ResumeGeneration
		rows[r.AttentionID] = row
	}
	pending = make([]SidecarPendingAttention, 0, len(order))
	for _, id := range order {
		pending = append(pending, rows[id])
	}
	return pending, commits, mutations, true
}

// resumeWindowed validates a sidecar against the opened file and, on
// success, decodes the header line plus the suffix after the offset. It
// reports ok=false (with the file closed) on any mismatch; the caller falls
// back to the full scan.
func resumeWindowed(f *os.File, info os.FileInfo, sidecar ResumeSidecar, expectedSessionID string) (ResumeView, *Writer, bool) {
	// Structural bounds first: the sidecar must describe this session, a
	// prefix inside the valid bytes of a file at least as large.
	if sidecar.SessionID != "" && sidecar.SessionID != expectedSessionID {
		_ = f.Close()
		return ResumeView{}, nil, false
	}
	if sidecar.Offset <= 0 || sidecar.Offset > sidecar.ValidBytes || sidecar.ValidBytes > sidecar.TranscriptSize || sidecar.TranscriptSize > info.Size() {
		_ = f.Close()
		return ResumeView{}, nil, false
	}
	// Append-only identity: the same file incarnation (fork children and
	// replaced files fail here), grown or equal. This mirrors
	// usableTurnIndex's sameFile/appendOnly gate.
	sameFile := sidecar.FileIdentity != "" && sidecar.FileIdentity == sidecarFileIdentity(info)
	if !sameFile {
		_ = f.Close()
		return ResumeView{}, nil, false
	}
	// Anchors bind the prefix bytes: first window of the file, and the
	// window ending at the offset.
	if !sidecarAnchorsMatch(f, sidecar.FirstAnchor, sidecar.TailAnchor) {
		_ = f.Close()
		return ResumeView{}, nil, false
	}

	// Decode the header line: identity validation must still run on the
	// windowed path.
	header, err := readHeaderAt(f)
	if err != nil {
		_ = f.Close()
		return ResumeView{}, nil, false
	}
	if expectedSessionID != "" && header.SessionID != expectedSessionID {
		_ = f.Close()
		return ResumeView{}, nil, false
	}

	// Read the suffix from the offset. validSuffixEnd bounds the crash tail:
	// bytes beyond the last complete line are drained and truncated, exactly
	// like the full scan.
	reader := io.NewSectionReader(f, sidecar.Offset, info.Size()-sidecar.Offset)
	buffered := bufio.NewReaderSize(reader, 64*1024)
	entries := make([]Entry, 0)
	maxSeq := sidecar.MaxSeq
	var validLen int64
	hasPartialTail := false
	for {
		line, complete, bytesRead, readErr := ReadLine(buffered, transcriptJSONLMaxLineBytes)
		if readErr != nil {
			_ = f.Close()
			return ResumeView{}, nil, false
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
		entry, err := DecodeEntry(line)
		if err != nil {
			// A corrupt suffix entry is a transcript the full scan must
			// judge (its wf7e posture is the full reader's decision, not the
			// windowed reader's).
			_ = f.Close()
			return ResumeView{}, nil, false
		}
		entries = append(entries, entry)
		if entry.Seq > maxSeq {
			maxSeq = entry.Seq
		}
	}
	// Boundary cross-check: the suffix's first entry must follow the
	// sidecar's boundary seq. A lower seq means the offset is not where the
	// sidecar says the prefix ends.
	//
	// A stale anchor pointing at an OLDER checkpoint than the file's last
	// (a later compaction's anchor write failed, or commits landed after it)
	// passes every gate here by design: the suffix it decodes is a SUPERSET
	// of ResumeHistory's window — extra pre-last-checkpoint entries the file
	// legitimately holds, every downstream invariant intact. The failure
	// direction of a stale anchor is a slower resume, never a wrong one,
	// which is why nothing here detects it.
	if len(entries) > 0 && entries[0].Seq <= sidecar.BoundarySeq {
		_ = f.Close()
		return ResumeView{}, nil, false
	}
	if hasPartialTail {
		if err := f.Truncate(sidecar.Offset + validLen); err != nil {
			_ = f.Close()
			return ResumeView{}, nil, false
		}
	}
	nextSeq := 0
	if maxSeq >= 0 {
		nextSeq = maxSeq + 1
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		_ = f.Close()
		return ResumeView{}, nil, false
	}
	writer := &Writer{fs: afero.NewOsFs(), file: f, seq: nextSeq, lastSync: time.Now(), header: header, sidecarEntryCount: sidecar.EntryCount + len(entries), lastCheckpointStart: -1, lastAppendedSeq: maxSeq, writePos: sidecar.Offset + validLen}
	view := ResumeView{
		Entries:          entries,
		PrefixEntryCount: sidecar.EntryCount,
		PrefixTurnCount:  sidecar.PrefixTurnCount,
		SidecarUsed:      true,
		Sidecar:          sidecar,
	}
	return view, writer, true
}

// readHeaderAt decodes the first line of the file without disturbing the
// caller's read position for the suffix (it reads through a section reader).
func readHeaderAt(f *os.File) (Header, error) {
	reader := bufio.NewReaderSize(io.NewSectionReader(f, 0, defaultHeaderReadBytes), 64*1024)
	line, complete, _, err := ReadLine(reader, transcriptJSONLMaxLineBytes)
	if err != nil || !complete {
		return Header{}, fmt.Errorf("read transcript header: %v", err)
	}
	line = bytes.TrimSpace(line)
	return DecodeHeader(line)
}

// defaultHeaderReadBytes bounds the section the header is read from. The
// header must be a single complete line; transcripts with larger headers
// fail the line-size contract in ReadLine anyway.
const defaultHeaderReadBytes = 1 << 20

// openWriterFS is the filesystem-injecting seam used by tests and the
// persistence fuzzer. Production uses openWriter so it can refuse symlinks at
// the operating-system open boundary.
func openWriterFS(fs afero.Fs, path string) (*Writer, error) {
	f, err := fs.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open transcript for resume: %w", err)
	}
	w, _, err := resumeWriter(fs, f, "")
	return w, err
}

func resumeWriter(fs afero.Fs, f afero.File, expectedSessionID string) (*Writer, scanResult, error) {
	// Validate complete v2 records while finding the next sequence and the byte
	// boundary before any crash tail. The shared framer drains an arbitrarily
	// large unterminated tail without retaining the file in memory.
	maxSeq := -1
	// lastCheckpointOffset is the byte offset of the last CHECKPOINT/SUMMARY
	// entry observed during the scan — the boundary the opportunistic sidecar
	// anchors at, recorded here so it needs no second file pass.
	lastCheckpointOffset := int64(-1)
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
			_ = f.Close()
			return nil, scanResult{}, fmt.Errorf("read transcript for resume: %w", readErr)
		}
		if !complete {
			hasPartialTail = bytesRead > 0
			break
		}
		validLen += bytesRead
		// The byte offset where the entry this iteration decodes STARTS:
		// validLen already counted the line's bytes, so subtracting this
		// line's length gives its start. Recorded for checkpoint-kind entries
		// so the opportunistic sidecar write needs no second file pass.
		entryStartOffset := validLen - int64(bytesRead)
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !headerRead {
			var err error
			header, err = DecodeHeader(line)
			if err != nil {
				_ = f.Close()
				return nil, scanResult{}, fmt.Errorf("parse transcript header: %w", err)
			}
			if expectedSessionID != "" && header.SessionID != expectedSessionID {
				_ = f.Close()
				return nil, scanResult{}, fmt.Errorf("transcript header session ID %q does not match requested session ID %q", header.SessionID, expectedSessionID)
			}
			headerRead = true
			continue
		}
		entry, err := DecodeEntry(line)
		if err != nil {
			_ = f.Close()
			return nil, scanResult{}, fmt.Errorf("parse transcript entry: %w", err)
		}
		entries = append(entries, entry)
		if entry.Seq > maxSeq {
			maxSeq = entry.Seq
		}
		if isCheckpointTurn(entry.Turn) {
			lastCheckpointOffset = entryStartOffset
		}
	}
	if !headerRead {
		_ = f.Close()
		if hasPartialTail && validLen == 0 {
			return nil, scanResult{}, errors.New("transcript has no complete lines")
		}
		return nil, scanResult{}, fmt.Errorf("%w: missing transcript header", ErrUnsupportedFormat)
	}

	if hasPartialTail {
		if err := f.Truncate(validLen); err != nil {
			_ = f.Close() // cleanup on error path; the truncate error is what matters
			return nil, scanResult{}, fmt.Errorf("truncate partial line: %w", err)
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
		_ = f.Close() // cleanup on error path; the seek error is what matters
		return nil, scanResult{}, fmt.Errorf("seek to end of transcript: %w", err)
	}

	return &Writer{fs: fs, file: f, seq: nextSeq, lastSync: time.Now(), header: header, sidecarEntryCount: len(entries), lastCheckpointStart: lastCheckpointOffset, lastAppendedSeq: maxSeq, writePos: validLen}, scanResult{entries: entries, lastCheckpointOffset: lastCheckpointOffset, header: header}, nil
}

// scanResult is what a full resume scan learned besides the entries: the
// byte offset of the last checkpoint entry, recorded during the scan so the
// opportunistic sidecar write needs no second file pass, and the validated
// header (for the prelude rule the prefix-turn count applies).
type scanResult struct {
	entries              []Entry
	lastCheckpointOffset int64
	header               Header
}
