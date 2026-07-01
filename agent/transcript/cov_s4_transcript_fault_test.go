package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/fuzz/fault"
	"primeradiant.com/serf/llm"
)

// faultTestHeader returns a minimal valid header. newWriterFS overwrites Kind and
// FormatVersion, so those are left unset here.
func faultTestHeader() Header {
	return Header{
		SessionID: "fault-cov",
		CreatedAt: time.Unix(0, 0).UTC(),
		ProfileID: "openai",
		Model:     "gpt-5.5",
	}
}

// faultPlan builds a 128-byte fault plan that trips a fault on exactly the k-th
// filesystem operation (index k) and lets every other op succeed. Plan byte 0x01
// (1 % 4 != 0) means "no fault"; 0x00 (0 % 4 == 0) means "fault", drawing
// injectable[0] == fault.ErrInjected.
func faultPlan(k int) []byte {
	plan := bytes.Repeat([]byte{0x01}, 128)
	plan[k] = 0x00
	return plan
}

const faultTranscriptPath = "/session/transcript.jsonl"

// --- 1. newWriterFS error arms -------------------------------------------------

// The op-index ordering for newWriterFS was established empirically:
//
//	0 = MkdirAll   1 = Create   2 = header Write   3 = header Sync
func TestNewWriterFS_ErrorArms(t *testing.T) {
	cases := []struct {
		name       string
		faultOp    int
		wantPrefix string
	}{
		{"MkdirAll", 0, "create transcript dir"},
		{"Create", 1, "create transcript file"},
		{"HeaderWrite", 2, "write transcript header"},
		{"HeaderSync", 3, "sync transcript header"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := fault.FS(afero.NewMemMapFs(), fault.FromBytes(faultPlan(tc.faultOp)))
			w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader())
			if err == nil {
				t.Fatalf("expected error faulting op %d", tc.faultOp)
			}
			if w != nil {
				t.Fatalf("writer leaked on error path: %+v", w)
			}
			if !strings.HasPrefix(err.Error(), tc.wantPrefix) {
				t.Fatalf("error = %q, want prefix %q", err.Error(), tc.wantPrefix)
			}
			if !errors.Is(err, fault.ErrInjected) {
				t.Fatalf("error = %v, want wrapped fault.ErrInjected", err)
			}
		})
	}
}

// --- 2. Append / append() paths ------------------------------------------------

func TestAppend_HappyReadBack(t *testing.T) {
	base := afero.NewMemMapFs()
	w, err := newWriterFS(base, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}

	turns := []string{"one", "two", "three"}
	for _, msg := range turns {
		if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant(msg))); err != nil {
			t.Fatalf("Append %q: %v", msg, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := afero.ReadFile(base, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'})
	// header + 3 entries
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 4 (header + 3 entries)", len(lines))
	}
	for i, raw := range lines[1:] {
		var entry Entry
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("unmarshal entry %d: %v", i, err)
		}
		if entry.Kind != "entry" {
			t.Fatalf("entry %d kind = %q, want entry", i, entry.Kind)
		}
		if entry.Seq != i {
			t.Fatalf("entry %d seq = %d, want %d (monotonic)", i, entry.Seq, i)
		}
	}
}

func TestAppendDurable_Success(t *testing.T) {
	base := afero.NewMemMapFs()
	w, err := newWriterFS(base, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	// A long interval proves AppendDurable syncs unconditionally: dirty must be
	// false afterward even though the interval has not elapsed.
	w.SyncInterval = time.Hour

	if err := w.AppendDurable(schema.NewTurn(schema.TurnAssistant, llm.Assistant("durable"))); err != nil {
		t.Fatalf("AppendDurable: %v", err)
	}
	w.mu.Lock()
	dirty := w.dirty
	seq := w.seq
	w.mu.Unlock()
	if dirty {
		t.Fatal("dirty = true after durable append, want false")
	}
	if seq != 1 {
		t.Fatalf("seq = %d after one durable append, want 1", seq)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := afero.ReadFile(base, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if lines := bytes.Count(data, []byte{'\n'}); lines != 2 {
		t.Fatalf("newline count = %d, want 2 (header + durable entry)", lines)
	}
}

// AppendDurable op indices after newWriterFS consumes 0-3:
//
//	4 = Seek(end)   5 = entry Write   6 = entry Sync
//
// A faulted Write drives appendFailureLocked -> rollbackAppendLocked, whose
// truncate/seek/sync all land on non-faulting ops so the returned error is just
// the wrapped write failure with no "rollback failed" suffix.
func TestAppendDurable_WriteFailsRollback(t *testing.T) {
	fs := fault.FS(afero.NewMemMapFs(), fault.FromBytes(faultPlan(5)))
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	err = w.AppendDurable(schema.NewTurn(schema.TurnAssistant, llm.Assistant("x")))
	if err == nil {
		t.Fatal("expected error from faulted entry write")
	}
	if !strings.HasPrefix(err.Error(), "write transcript entry") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "write transcript entry")
	}
	if strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("rollback should have succeeded, got %q", err.Error())
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("error = %v, want wrapped fault.ErrInjected", err)
	}
	// seq must not advance on a failed append.
	w.mu.Lock()
	seq := w.seq
	w.mu.Unlock()
	if seq != 0 {
		t.Fatalf("seq = %d after failed durable append, want 0", seq)
	}
}

// A faulted entry Sync (index 6) after a good write also runs rollback, which
// succeeds, so the returned error is the wrapped sync failure.
func TestAppendDurable_SyncFailsRollback(t *testing.T) {
	fs := fault.FS(afero.NewMemMapFs(), fault.FromBytes(faultPlan(6)))
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	err = w.AppendDurable(schema.NewTurn(schema.TurnAssistant, llm.Assistant("x")))
	if err == nil {
		t.Fatal("expected error from faulted entry sync")
	}
	if !strings.HasPrefix(err.Error(), "sync transcript entry") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "sync transcript entry")
	}
	if strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("rollback should have succeeded, got %q", err.Error())
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("error = %v, want wrapped fault.ErrInjected", err)
	}
}

// A faulted Seek(end) at the start of a durable append (index 4) fails before
// any write, surfacing "seek transcript append start".
func TestAppendDurable_SeekStartFails(t *testing.T) {
	fs := fault.FS(afero.NewMemMapFs(), fault.FromBytes(faultPlan(4)))
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	err = w.AppendDurable(schema.NewTurn(schema.TurnAssistant, llm.Assistant("x")))
	if err == nil {
		t.Fatal("expected error from faulted seek-to-end")
	}
	if !strings.HasPrefix(err.Error(), "seek transcript append start") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "seek transcript append start")
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("error = %v, want wrapped fault.ErrInjected", err)
	}
}

// A plain (non-durable) Append with a zero SyncInterval fsyncs every write; a
// faulted Sync (index 5) surfaces "sync transcript entry" with no rollback, since
// rollback only runs on the durable path.
func TestAppend_SyncFailsNoRollback(t *testing.T) {
	fs := fault.FS(afero.NewMemMapFs(), fault.FromBytes(faultPlan(5)))
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	// SyncInterval defaults to 0 => sync every write. Write is index 4, Sync index 5.
	err = w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("x")))
	if err == nil {
		t.Fatal("expected error from faulted plain-append sync")
	}
	if !strings.HasPrefix(err.Error(), "sync transcript entry") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "sync transcript entry")
	}
	if strings.Contains(err.Error(), "rollback") {
		t.Fatalf("plain append must not roll back, got %q", err.Error())
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("error = %v, want wrapped fault.ErrInjected", err)
	}
}

// When the durable-write fault is compounded by a rollback-truncate fault,
// appendFailureLocked reports both: the original write failure plus a
// "rollback failed" suffix. Indices: Write 5 (fault), rollback Truncate 6 (fault).
func TestAppendDurable_WriteFailsRollbackAlsoFails(t *testing.T) {
	plan := bytes.Repeat([]byte{0x01}, 128)
	plan[5] = 0x00 // entry Write
	plan[6] = 0x00 // rollback Truncate
	fs := fault.FS(afero.NewMemMapFs(), fault.FromBytes(plan))
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	err = w.AppendDurable(schema.NewTurn(schema.TurnAssistant, llm.Assistant("x")))
	if err == nil {
		t.Fatal("expected error from faulted write + faulted rollback")
	}
	if !strings.HasPrefix(err.Error(), "write transcript entry") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "write transcript entry")
	}
	if !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("error = %q, want a rollback-failed suffix", err.Error())
	}
}

// When the durable Sync faults (index 6), rollback runs; a compounding fault on a
// rollback op surfaces the "sync transcript entry: ...; rollback failed: ..."
// shape and exercises each failure branch of rollbackAppendLocked.
// Rollback ops after the faulted Sync: Truncate 7, Seek 8, Sync 9.
func TestAppendDurable_SyncFailsRollbackAlsoFails(t *testing.T) {
	cases := []struct {
		name         string
		faultOps     []int
		wantRollback string
	}{
		{"truncateFails", []int{7}, "truncate to"},
		{"seekFails", []int{8}, "seek eof"},
		{"truncateAndSeekFail", []int{7, 8}, "seek eof"},
		{"rollbackSyncFails", []int{9}, "sync rollback truncate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := bytes.Repeat([]byte{0x01}, 128)
			plan[6] = 0x00 // entry Sync
			for _, op := range tc.faultOps {
				plan[op] = 0x00
			}
			fs := fault.FS(afero.NewMemMapFs(), fault.FromBytes(plan))
			w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader())
			if err != nil {
				t.Fatalf("newWriterFS: %v", err)
			}
			err = w.AppendDurable(schema.NewTurn(schema.TurnAssistant, llm.Assistant("x")))
			if err == nil {
				t.Fatal("expected error from faulted sync + faulted rollback")
			}
			if !strings.HasPrefix(err.Error(), "sync transcript entry") {
				t.Fatalf("error = %q, want prefix %q", err.Error(), "sync transcript entry")
			}
			if !strings.Contains(err.Error(), "rollback failed") {
				t.Fatalf("error = %q, want a rollback-failed suffix", err.Error())
			}
			if !strings.Contains(err.Error(), tc.wantRollback) {
				t.Fatalf("error = %q, want rollback detail %q", err.Error(), tc.wantRollback)
			}
		})
	}
}

// --- 3. AppendAPICall ----------------------------------------------------------

func TestAppendAPICall_Success(t *testing.T) {
	base := afero.NewMemMapFs()
	w, err := newWriterFS(base, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	call := APICall{Round: 7, LatencyMs: 42, SystemPrompt: "sp"}
	if err := w.AppendAPICall(call); err != nil {
		t.Fatalf("AppendAPICall: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := afero.ReadFile(base, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (header + api_call)", len(lines))
	}
	var got APICall
	if err := json.Unmarshal(lines[1], &got); err != nil {
		t.Fatalf("unmarshal api_call: %v", err)
	}
	if got.Kind != "api_call" {
		t.Fatalf("kind = %q, want api_call", got.Kind)
	}
	if got.Seq != 0 || got.Round != 7 || got.LatencyMs != 42 {
		t.Fatalf("api_call = %+v, want seq 0 / round 7 / latency 42", got)
	}
}

// AppendAPICall op indices after newWriterFS consumes 0-3 (SyncInterval == 0):
//
//	4 = Write   5 = Sync
func TestAppendAPICall_WriteFails(t *testing.T) {
	fs := fault.FS(afero.NewMemMapFs(), fault.FromBytes(faultPlan(4)))
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	err = w.AppendAPICall(APICall{Round: 1})
	if err == nil {
		t.Fatal("expected error from faulted api_call write")
	}
	if !strings.HasPrefix(err.Error(), "write transcript api_call") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "write transcript api_call")
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("error = %v, want wrapped fault.ErrInjected", err)
	}
}

func TestAppendAPICall_SyncFails(t *testing.T) {
	fs := fault.FS(afero.NewMemMapFs(), fault.FromBytes(faultPlan(5)))
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	err = w.AppendAPICall(APICall{Round: 1})
	if err == nil {
		t.Fatal("expected error from faulted api_call sync")
	}
	if !strings.HasPrefix(err.Error(), "sync transcript api_call") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "sync transcript api_call")
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("error = %v, want wrapped fault.ErrInjected", err)
	}
}

// Nil and closed receivers must swallow every write as a no-op, never panicking
// or erroring.
func TestWriter_NilAndClosedNoOps(t *testing.T) {
	turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant("x"))

	var nilW *Writer
	if err := nilW.Append(turn); err != nil {
		t.Fatalf("nil Append: %v", err)
	}
	if err := nilW.AppendDurable(turn); err != nil {
		t.Fatalf("nil AppendDurable: %v", err)
	}
	if err := nilW.AppendAPICall(APICall{}); err != nil {
		t.Fatalf("nil AppendAPICall: %v", err)
	}

	base := afero.NewMemMapFs()
	w, err := newWriterFS(base, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After Close, further writes are no-ops (closed flag short-circuits).
	if err := w.Append(turn); err != nil {
		t.Fatalf("closed Append: %v", err)
	}
	if err := w.AppendAPICall(APICall{}); err != nil {
		t.Fatalf("closed AppendAPICall: %v", err)
	}
	// The persisted file still holds only the header: the closed writes did nothing.
	data, err := afero.ReadFile(base, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if lines := bytes.Count(data, []byte{'\n'}); lines != 1 {
		t.Fatalf("newline count = %d, want 1 (header only)", lines)
	}
}

// --- 4. Close ------------------------------------------------------------------

func TestClose_FlushesAndIdempotent(t *testing.T) {
	// Nil receiver is a no-op.
	var nilW *Writer
	if err := nilW.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}

	base := afero.NewMemMapFs()
	w, err := newWriterFS(base, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	// Long interval leaves the append un-synced so Close must flush it.
	w.SyncInterval = time.Hour
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("pending"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	w.mu.Lock()
	dirty := w.dirty
	w.mu.Unlock()
	if !dirty {
		t.Fatal("dirty = false before Close, want true (write within interval)")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	w.mu.Lock()
	dirtyAfter := w.dirty
	w.mu.Unlock()
	if dirtyAfter {
		t.Fatal("dirty = true after Close, want false (Close must flush)")
	}
	// Second Close is a no-op.
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	data, err := afero.ReadFile(base, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if lines := bytes.Count(data, []byte{'\n'}); lines != 2 {
		t.Fatalf("newline count = %d, want 2 (header + flushed entry)", lines)
	}
}

// The flush Sync inside Close faults. With a long interval the Append (Write at
// index 4) leaves the writer dirty; Close's Sync at index 5 fails.
func TestClose_SyncFails(t *testing.T) {
	fs := fault.FS(afero.NewMemMapFs(), fault.FromBytes(faultPlan(5)))
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	w.SyncInterval = time.Hour
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("pending"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	err = w.Close()
	if err == nil {
		t.Fatal("expected error from faulted flush sync on Close")
	}
	if !strings.HasPrefix(err.Error(), "sync transcript on close") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "sync transcript on close")
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("error = %v, want wrapped fault.ErrInjected", err)
	}
}

// faultFile does not intercept Close, and MemMapFs double-close returns nil, so a
// real OS file is used to exercise the file.Close() failure branch: pre-closing
// the handle makes the Close inside Writer.Close return "file already closed".
func TestClose_FileCloseFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	w, err := NewWriter(path, faultTestHeader())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// Durable append leaves dirty == false so Close goes straight to file.Close().
	if err := w.AppendDurable(schema.NewTurn(schema.TurnAssistant, llm.Assistant("a"))); err != nil {
		t.Fatalf("AppendDurable: %v", err)
	}
	w.mu.Lock()
	if err := w.file.Close(); err != nil {
		w.mu.Unlock()
		t.Fatalf("pre-close: %v", err)
	}
	w.mu.Unlock()

	err = w.Close()
	if err == nil {
		t.Fatal("expected error from faulted file.Close on Close")
	}
	if !strings.HasPrefix(err.Error(), "close transcript file") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "close transcript file")
	}
}

// --- 5. openWriterFS -----------------------------------------------------------

// makeValidTranscript writes a two-entry transcript (seqs 0 and 1) to a fresh
// MemMapFs and returns the fs plus the persisted bytes.
func makeValidTranscript(t *testing.T) (afero.Fs, []byte) {
	t.Helper()
	base := afero.NewMemMapFs()
	w, err := newWriterFS(base, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("a"))); err != nil {
		t.Fatalf("Append a: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("b"))); err != nil {
		t.Fatalf("Append b: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := afero.ReadFile(base, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return base, data
}

// memWithFile returns a fresh MemMapFs holding data at the transcript path.
func memWithFile(t *testing.T, data []byte) afero.Fs {
	t.Helper()
	mem := afero.NewMemMapFs()
	if err := afero.WriteFile(mem, faultTranscriptPath, data, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	return mem
}

func TestOpenWriterFS_HappyReopen(t *testing.T) {
	base, _ := makeValidTranscript(t)
	w, err := openWriterFS(base, faultTranscriptPath)
	if err != nil {
		t.Fatalf("openWriterFS: %v", err)
	}
	// Two entries at seq 0 and 1 => next seq is 2.
	w.mu.Lock()
	seq := w.seq
	w.mu.Unlock()
	if seq != 2 {
		t.Fatalf("resumed seq = %d, want 2 (maxSeq+1)", seq)
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("c"))); err != nil {
		t.Fatalf("Append after resume: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := afero.ReadFile(base, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'})
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 4 (header + 3 entries)", len(lines))
	}
	var last Entry
	if err := json.Unmarshal(lines[3], &last); err != nil {
		t.Fatalf("unmarshal resumed entry: %v", err)
	}
	if last.Seq != 2 {
		t.Fatalf("resumed entry seq = %d, want 2", last.Seq)
	}
}

// openWriterFS op indices (valid two-entry file, ~578 bytes -> 3 Reads):
//
//	0 = OpenFile   1..3 = ReadAll's Reads   4 = Seek(end)
//
// Partial file adds a Truncate at index 4 and pushes Seek to index 5.
func TestOpenWriterFS_OpenFileFails(t *testing.T) {
	_, data := makeValidTranscript(t)
	fs := fault.FS(memWithFile(t, data), fault.FromBytes(faultPlan(0)))
	w, err := openWriterFS(fs, faultTranscriptPath)
	if err == nil {
		t.Fatal("expected error from faulted OpenFile")
	}
	if w != nil {
		t.Fatalf("writer leaked on error path: %+v", w)
	}
	if !strings.HasPrefix(err.Error(), "open transcript for resume") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "open transcript for resume")
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("error = %v, want wrapped fault.ErrInjected", err)
	}
}

func TestOpenWriterFS_ReadAllFails(t *testing.T) {
	_, data := makeValidTranscript(t)
	// Fault the first Read (index 1); io.ReadAll surfaces it regardless of how
	// many chunked reads the file would otherwise take.
	fs := fault.FS(memWithFile(t, data), fault.FromBytes(faultPlan(1)))
	w, err := openWriterFS(fs, faultTranscriptPath)
	if err == nil {
		t.Fatal("expected error from faulted ReadAll")
	}
	if w != nil {
		t.Fatalf("writer leaked on error path: %+v", w)
	}
	if !strings.HasPrefix(err.Error(), "read transcript for resume") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "read transcript for resume")
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("error = %v, want wrapped fault.ErrInjected", err)
	}
}

func TestOpenWriterFS_PartialLineTruncated(t *testing.T) {
	base, data := makeValidTranscript(t)
	// Append a trailing partial line with no newline to simulate a crash.
	partial := append(append([]byte{}, data...), []byte("partial-junk-no-nl")...)
	mem := memWithFile(t, partial)

	w, err := openWriterFS(mem, faultTranscriptPath)
	if err != nil {
		t.Fatalf("openWriterFS on partial tail: %v", err)
	}
	// Recovery truncates to the last newline: file equals the original valid bytes.
	recovered, err := afero.ReadFile(mem, faultTranscriptPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(recovered, data) {
		t.Fatalf("recovered file = %q, want truncated to %q", recovered, data)
	}
	// Resume still counts the two entries: next seq is 2.
	w.mu.Lock()
	seq := w.seq
	w.mu.Unlock()
	if seq != 2 {
		t.Fatalf("resumed seq = %d, want 2", seq)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_ = base
}

func TestOpenWriterFS_NoCompleteLines(t *testing.T) {
	// A file whose entire content has no newline: recovery cannot find a valid
	// prefix.
	mem := memWithFile(t, []byte("no-newline-anywhere"))
	w, err := openWriterFS(mem, faultTranscriptPath)
	if err == nil {
		t.Fatal("expected error for file with no complete lines")
	}
	if w != nil {
		t.Fatalf("writer leaked on error path: %+v", w)
	}
	if err.Error() != "transcript has no complete lines" {
		t.Fatalf("error = %q, want %q", err.Error(), "transcript has no complete lines")
	}
}

func TestOpenWriterFS_TruncateFails(t *testing.T) {
	_, data := makeValidTranscript(t)
	partial := append(append([]byte{}, data...), []byte("partial-junk-no-nl")...)
	// OpenFile (0) + 3 Reads (1..3) succeed; Truncate lands at index 4.
	fs := fault.FS(memWithFile(t, partial), fault.FromBytes(faultPlan(4)))
	w, err := openWriterFS(fs, faultTranscriptPath)
	if err == nil {
		t.Fatal("expected error from faulted partial-line Truncate")
	}
	if w != nil {
		t.Fatalf("writer leaked on error path: %+v", w)
	}
	if !strings.HasPrefix(err.Error(), "truncate partial line") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "truncate partial line")
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("error = %v, want wrapped fault.ErrInjected", err)
	}
}

func TestOpenWriterFS_SeekToEndFails(t *testing.T) {
	_, data := makeValidTranscript(t)
	// Valid file: no truncate, so Seek(end) lands at index 4.
	fs := fault.FS(memWithFile(t, data), fault.FromBytes(faultPlan(4)))
	w, err := openWriterFS(fs, faultTranscriptPath)
	if err == nil {
		t.Fatal("expected error from faulted Seek-to-end")
	}
	if w != nil {
		t.Fatalf("writer leaked on error path: %+v", w)
	}
	if !strings.HasPrefix(err.Error(), "seek to end of transcript") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "seek to end of transcript")
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("error = %v, want wrapped fault.ErrInjected", err)
	}
}
