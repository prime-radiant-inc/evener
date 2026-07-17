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
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/task"
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
	BuildVersion     string    `json:"build_version,omitempty"` // serf build version that wrote the file
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

	// SyncInterval controls how often Append calls fsync.
	// If 0, every Append fsyncs (backward-compatible default for tests).
	// If >0, Append only fsyncs when this duration has elapsed since the last sync.
	SyncInterval time.Duration

	dirty    bool
	lastSync time.Time
}

// NewWriter creates a transcript file at path, writes the header as the first line,
// and returns a writer that keeps the file handle open for subsequent Append calls.
func NewWriter(path string, header Header) (*Writer, error) {
	return NewWriterWithFS(afero.NewOsFs(), path, header)
}

// NewWriterWithFS creates a transcript writer over fs. It has the same behavior
// as NewWriter, but allows callers that already own a filesystem boundary to
// keep transcript persistence on that filesystem.
func NewWriterWithFS(fs afero.Fs, path string, header Header) (*Writer, error) {
	return newWriterFS(fs, path, header)
}

// newWriterFS is the filesystem-injecting seam behind NewWriter. Production
// passes afero.NewOsFs() (byte-identical to direct os calls); tests and the
// persistence fuzzer inject an in-memory or sandboxed filesystem.
func newWriterFS(fs afero.Fs, path string, header Header) (*Writer, error) {
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

	if err := f.Sync(); err != nil {
		_ = f.Close() // cleanup on error path; the sync error is what matters
		return nil, fmt.Errorf("sync transcript header: %w", err)
	}

	return &Writer{fs: fs, file: f, lastSync: time.Now()}, nil
}

// Append writes a turn as an Entry to the JSONL file.
// Safe for concurrent use. No-op if the receiver is nil.
func (w *Writer) Append(turn schema.Turn) error {
	return w.append(turn, false)
}

// AppendDurable writes a turn and fsyncs it before returning.
// Safe for concurrent use. No-op if the receiver is nil.
func (w *Writer) AppendDurable(turn schema.Turn) error {
	return w.append(turn, true)
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
	return openWriterFS(afero.NewOsFs(), path)
}

// openWriterFS is the filesystem-injecting seam behind OpenWriter. Production
// passes afero.NewOsFs() (byte-identical to direct os calls); tests and the
// persistence fuzzer inject an in-memory or sandboxed filesystem.
func openWriterFS(fs afero.Fs, path string) (*Writer, error) {
	f, err := fs.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open transcript for resume: %w", err)
	}

	// Validate complete v2 records while finding the next sequence and the byte
	// boundary before any crash tail. The shared framer drains an arbitrarily
	// large unterminated tail without retaining the file in memory.
	maxSeq := -1
	reader := bufio.NewReaderSize(f, 64*1024)
	var validLen int64
	hasPartialTail := false
	headerRead := false
	for {
		line, complete, bytesRead, readErr := ReadLine(reader, transcriptJSONLMaxLineBytes)
		if readErr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("read transcript for resume: %w", readErr)
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
			if _, err := DecodeHeader(line); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("parse transcript header: %w", err)
			}
			headerRead = true
			continue
		}
		entry, err := DecodeEntry(line)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("parse transcript entry: %w", err)
		}
		if entry.Seq > maxSeq {
			maxSeq = entry.Seq
		}
	}
	if !headerRead {
		_ = f.Close()
		if hasPartialTail && validLen == 0 {
			return nil, errors.New("transcript has no complete lines")
		}
		return nil, fmt.Errorf("%w: missing transcript header", ErrUnsupportedFormat)
	}

	if hasPartialTail {
		if err := f.Truncate(validLen); err != nil {
			_ = f.Close() // cleanup on error path; the truncate error is what matters
			return nil, fmt.Errorf("truncate partial line: %w", err)
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
		return nil, fmt.Errorf("seek to end of transcript: %w", err)
	}

	return &Writer{fs: fs, file: f, seq: nextSeq, lastSync: time.Now()}, nil
}
