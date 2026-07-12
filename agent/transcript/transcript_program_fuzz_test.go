//go:build serffuzz

package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/fuzz/fault"
	"primeradiant.com/serf/llm"
)

// FuzzTranscriptWriterProgram drives the real transcript lifecycle against a
// fresh t.TempDir: create, append, durable append, API records, close/reopen,
// and crash-tail recovery. It also injects deterministic errors only at the
// filesystem boundary, layered over the same temp-directory OS filesystem, so
// the real writer and reader execute their durability, rollback, and cleanup
// paths without a provider, network, shell, Git, or ambient state dependency.
//
// The main program's oracle verifies that every persisted complete line is a
// valid header, entry, or API call and that writer-assigned sequence numbers are
// strictly increasing across reopen boundaries. A dedicated 0xff program mode
// runs the full fault matrix; the committed sentinel seed keeps that coverage in replay
// while ordinary mutations focus on the faster lifecycle state machine.
func FuzzTranscriptWriterProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0x00},
		{0x01, 0x02, 0x03, 0x04, 0x05},
		{0xff, 0x11, 0x72, 0x00, 0x5a, 0x9c},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		r := transcriptProgramReader{data: program}
		mode := r.next()
		root := t.TempDir()

		transcriptRunLifecycleProgram(t, root, &r)
		if mode == 0xff {
			transcriptAssertConstructorPaths(t, root, &r)
			transcriptAssertWriterFailurePaths(t, root, &r)
			transcriptAssertReaderRecoveryPaths(t, root, &r)
		}
	})
}

type transcriptProgramReader struct {
	data []byte
	pos  int
}

func (r *transcriptProgramReader) next() byte {
	if len(r.data) == 0 {
		return 0
	}
	b := r.data[r.pos%len(r.data)]
	r.pos++
	return b
}

func transcriptProgramHeader(b byte) Header {
	return Header{
		SessionID:    fmt.Sprintf("fuzz-session-%02x", b),
		CreatedAt:    time.Unix(int64(b), 0).UTC(),
		ProfileID:    "fuzz-profile",
		Model:        "fuzz-model",
		WorkingDir:   "/fixture/work",
		SystemPrompt: fmt.Sprintf("system-%02x", b),
		BuildVersion: "fuzz",
		Depth:        int(b % 3),
	}
}

func transcriptProgramTurn(b byte) schema.Turn {
	kinds := []schema.TurnKind{
		schema.TurnUserInput,
		schema.TurnAssistant,
		schema.TurnToolResults,
		schema.TurnSummary,
	}
	turn := schema.Turn{
		Kind:       kinds[int(b)%len(kinds)],
		Message:    llm.Assistant(fmt.Sprintf("turn-%02x", b)),
		Timestamp:  time.Unix(int64(b), 0).UTC(),
		ResponseID: fmt.Sprintf("response-%02x", b),
	}
	if b&1 != 0 {
		turn.Message = llm.ToolResult(
			fmt.Sprintf("call-%02x", b),
			map[string]any{"value": int(b), "text": fmt.Sprintf("payload-%02x", b)},
			b&2 != 0,
		)
	}
	return turn
}

func transcriptProgramAPICall(b byte) APICall {
	return APICall{
		Round:                  int(b),
		AttemptGroupID:         fmt.Sprintf("attempt-%02x", b),
		AttemptIndex:           int(b % 3),
		AttemptCount:           int(b%3) + 1,
		Timestamp:              time.Unix(int64(b), 0).UTC().Format(time.RFC3339),
		LatencyMs:              int64(b),
		SystemPrompt:           fmt.Sprintf("prompt-%02x", b),
		ContextHistoryTurns:    int(b % 5),
		SystemPromptBytes:      int(b),
		PreviousResponseIDHash: fmt.Sprintf("previous-%02x", b),
		ConversationIDHash:     fmt.Sprintf("conversation-%02x", b),
		Request: llm.APILogRequest{
			Model:    "fuzz-model",
			Provider: "fuzz-provider",
		},
		Response: &llm.APILogResponse{
			ID:    fmt.Sprintf("response-%02x", b),
			Model: "fuzz-model",
			Raw:   map[string]any{"round": int(b)},
		},
	}
}

func transcriptRunLifecycleProgram(t *testing.T, root string, r *transcriptProgramReader) {
	t.Helper()
	path := filepath.Join(root, "lifecycle", "transcript.jsonl")
	w, err := NewWriter(path, transcriptProgramHeader(r.next()))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// The fault matrix below exercises every fsync error arm. Keep ordinary fuzz
	// programs stateful rather than I/O-bound so mutations can explore longer
	// append/reopen sequences in a bounded test budget.
	w.SyncInterval = time.Hour

	const maxOps = 16
	for i := 0; i < maxOps; i++ {
		switch r.next() % 6 {
		case 0:
			if err := w.Append(transcriptProgramTurn(r.next())); err != nil {
				t.Fatalf("Append: %v", err)
			}
		case 1:
			if err := w.AppendDurable(transcriptProgramTurn(r.next())); err != nil {
				t.Fatalf("AppendDurable: %v", err)
			}
		case 2:
			if err := w.AppendAPICall(transcriptProgramAPICall(r.next())); err != nil {
				t.Fatalf("AppendAPICall: %v", err)
			}
		case 3:
			if err := w.Close(); err != nil {
				t.Fatalf("Close before reopen: %v", err)
			}
			w, err = OpenWriter(path)
			if err != nil {
				t.Fatalf("OpenWriter: %v", err)
			}
			w.SyncInterval = time.Hour
		case 4:
			w.SyncInterval = time.Hour
			if err := w.Append(transcriptProgramTurn(r.next())); err != nil {
				t.Fatalf("interval Append: %v", err)
			}
			if err := w.AppendDurable(transcriptProgramTurn(r.next())); err != nil {
				t.Fatalf("interval AppendDurable: %v", err)
			}
		case 5:
			if err := w.Close(); err != nil {
				t.Fatalf("Close before partial recovery: %v", err)
			}
			if err := transcriptAppendRaw(path, []byte(fmt.Sprintf("partial-%02x", r.next()))); err != nil {
				t.Fatalf("append partial tail: %v", err)
			}
			w, err = OpenWriter(path)
			if err != nil {
				t.Fatalf("OpenWriter after partial tail: %v", err)
			}
			w.SyncInterval = time.Hour
		}
		transcriptAssertCompleteFile(t, path)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("final Close: %v", err)
	}
	transcriptAssertCompleteFile(t, path)
}

func transcriptAssertConstructorPaths(t *testing.T, root string, r *transcriptProgramReader) {
	t.Helper()
	header := transcriptProgramHeader(r.next())

	basePath := filepath.Join(root, "with-fs", "transcript.jsonl")
	w, err := NewWriterWithFS(afero.NewOsFs(), basePath, header)
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("NewWriterWithFS Close: %v", err)
	}

	blocked := filepath.Join(root, "blocked-parent")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed blocked parent: %v", err)
	}
	if _, err := NewWriter(filepath.Join(blocked, "child", "transcript.jsonl"), header); err == nil || !strings.Contains(err.Error(), "create transcript dir") {
		t.Fatalf("NewWriter blocked parent error = %v", err)
	}

	directoryPath := filepath.Join(root, "existing-directory")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatalf("seed directory target: %v", err)
	}
	if _, err := NewWriter(directoryPath, header); err == nil || !strings.Contains(err.Error(), "create transcript file") {
		t.Fatalf("NewWriter directory target error = %v", err)
	}

	invalidTime := header
	invalidTime.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := NewWriter(filepath.Join(root, "invalid-header.jsonl"), invalidTime); err == nil || !strings.Contains(err.Error(), "marshal transcript header") {
		t.Fatalf("NewWriter invalid header error = %v", err)
	}

	for _, tc := range []struct {
		name   string
		fault  int
		prefix string
	}{
		{name: "mkdir", fault: 0, prefix: "create transcript dir"},
		{name: "create", fault: 1, prefix: "create transcript file"},
		{name: "header-write", fault: 2, prefix: "write transcript header"},
		{name: "header-sync", fault: 3, prefix: "sync transcript header"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := transcriptFaultFS(transcriptFaultPlan(tc.fault))
			w, err := newWriterFS(fs, filepath.Join(root, "constructor-fault-"+tc.name+".jsonl"), header)
			transcriptRequireInjectedError(t, err, tc.prefix)
			if w != nil {
				t.Fatalf("newWriterFS(%s) returned writer after error", tc.name)
			}
		})
	}
}

func transcriptAssertWriterFailurePaths(t *testing.T, root string, r *transcriptProgramReader) {
	t.Helper()
	header := transcriptProgramHeader(r.next())

	transcriptAssertMarshalFailures(t, root, header)
	transcriptAssertNilAndClosedNoOps(t, root, header)
	transcriptAssertFaultedAppendPaths(t, root, header, r.next())
	transcriptAssertZeroProgressWrite(t, root, header)
	transcriptAssertCloseRaceNoOps(t, root, header)
	transcriptAssertCloseFailurePaths(t, root, header, r.next())
}

type transcriptZeroWriteFS struct{ afero.Fs }

func (fs transcriptZeroWriteFS) Create(name string) (afero.File, error) {
	f, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &transcriptZeroWriteFile{File: f}, nil
}

type transcriptZeroWriteFile struct {
	afero.File
	armed bool
}

func (f *transcriptZeroWriteFile) Write(p []byte) (int, error) {
	if f.armed {
		f.armed = false
		return 0, nil
	}
	return f.File.Write(p)
}

func transcriptAssertZeroProgressWrite(t *testing.T, root string, header Header) {
	t.Helper()
	fs := transcriptZeroWriteFS{Fs: afero.NewBasePathFs(afero.NewOsFs(), root)}
	w, err := newWriterFS(fs, "/zero-write.jsonl", header)
	if err != nil {
		t.Fatalf("newWriterFS zero write: %v", err)
	}
	w.file.(*transcriptZeroWriteFile).armed = true
	if err := w.Append(transcriptProgramTurn(1)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero-progress Append error = %v, want io.ErrShortWrite", err)
	}
	_ = w.Close()
}

func transcriptAssertCloseRaceNoOps(t *testing.T, root string, header Header) {
	t.Helper()
	path := filepath.Join(root, "close-race", "transcript.jsonl")
	w, err := NewWriter(path, header)
	if err != nil {
		t.Fatalf("NewWriter close race: %v", err)
	}

	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)
	for _, invoke := range []func() error{
		func() error { return w.Append(transcriptProgramTurn(2)) },
		func() error { return w.AppendAPICall(transcriptProgramAPICall(3)) },
	} {
		w.closed.Store(false)
		w.mu.Lock()
		started := make(chan struct{})
		done := make(chan error, 1)
		go func(invoke func() error) {
			close(started)
			done <- invoke()
		}(invoke)
		<-started
		// With one P, yielding lets the caller run until it blocks on w.mu,
		// after its unlocked closed check and before the locked re-check.
		runtime.Gosched()
		w.closed.Store(true)
		w.mu.Unlock()
		if err := <-done; err != nil {
			t.Fatalf("close-raced append: %v", err)
		}
	}
	if err := w.file.Close(); err != nil {
		t.Fatalf("close race file: %v", err)
	}
}

func transcriptAssertMarshalFailures(t *testing.T, root string, header Header) {
	t.Helper()
	path := filepath.Join(root, "marshal", "transcript.jsonl")
	w, err := NewWriter(path, header)
	if err != nil {
		t.Fatalf("NewWriter for marshal failures: %v", err)
	}
	before := transcriptReadFile(t, path)

	unsupportedTurn := schema.Turn{
		Kind:      schema.TurnToolResults,
		Message:   llm.ToolResult("bad-call", func() {}, false),
		Timestamp: time.Unix(0, 0).UTC(),
	}
	if err := w.Append(unsupportedTurn); err == nil || !strings.Contains(err.Error(), "marshal transcript entry") {
		t.Fatalf("Append unsupported turn error = %v", err)
	}
	if got := transcriptReadFile(t, path); !bytes.Equal(got, before) {
		t.Fatalf("failed entry marshal changed transcript: got %q, want %q", got, before)
	}

	unsupportedCall := APICall{Response: &llm.APILogResponse{Raw: map[string]any{"bad": func() {}}}}
	if err := w.AppendAPICall(unsupportedCall); err == nil || !strings.Contains(err.Error(), "marshal transcript api_call") {
		t.Fatalf("AppendAPICall unsupported response error = %v", err)
	}
	if got := transcriptReadFile(t, path); !bytes.Equal(got, before) {
		t.Fatalf("failed api marshal changed transcript: got %q, want %q", got, before)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close after marshal failures: %v", err)
	}
}

func transcriptAssertNilAndClosedNoOps(t *testing.T, root string, header Header) {
	t.Helper()
	var nilWriter *Writer
	if err := nilWriter.Append(transcriptProgramTurn(1)); err != nil {
		t.Fatalf("nil Append: %v", err)
	}
	if err := nilWriter.AppendDurable(transcriptProgramTurn(2)); err != nil {
		t.Fatalf("nil AppendDurable: %v", err)
	}
	if err := nilWriter.AppendAPICall(transcriptProgramAPICall(3)); err != nil {
		t.Fatalf("nil AppendAPICall: %v", err)
	}
	if err := nilWriter.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
	if err := (&Writer{}).Close(); err != nil {
		t.Fatalf("empty Writer Close: %v", err)
	}

	path := filepath.Join(root, "closed", "transcript.jsonl")
	w, err := NewWriter(path, header)
	if err != nil {
		t.Fatalf("NewWriter for closed no-ops: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close for closed no-ops: %v", err)
	}
	before := transcriptReadFile(t, path)
	if err := w.Append(transcriptProgramTurn(4)); err != nil {
		t.Fatalf("closed Append: %v", err)
	}
	if err := w.AppendDurable(transcriptProgramTurn(5)); err != nil {
		t.Fatalf("closed AppendDurable: %v", err)
	}
	if err := w.AppendAPICall(transcriptProgramAPICall(6)); err != nil {
		t.Fatalf("closed AppendAPICall: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := transcriptReadFile(t, path); !bytes.Equal(got, before) {
		t.Fatalf("closed writes changed transcript: got %q, want %q", got, before)
	}
}

func transcriptAssertFaultedAppendPaths(t *testing.T, root string, header Header, payload byte) {
	t.Helper()

	for _, tc := range []struct {
		name           string
		faults         []int
		invoke         func(*Writer) error
		prefix         string
		wantRolledBack bool
	}{
		{
			name:   "append-write",
			faults: []int{4},
			invoke: func(w *Writer) error { return w.Append(transcriptProgramTurn(payload)) },
			prefix: "write transcript entry",
		},
		{
			name:   "append-sync",
			faults: []int{5},
			invoke: func(w *Writer) error { return w.Append(transcriptProgramTurn(payload)) },
			prefix: "sync transcript entry",
		},
		{
			name:   "durable-seek",
			faults: []int{4},
			invoke: func(w *Writer) error { return w.AppendDurable(transcriptProgramTurn(payload)) },
			prefix: "seek transcript append start",
		},
		{
			name:           "durable-write-rollback",
			faults:         []int{5},
			invoke:         func(w *Writer) error { return w.AppendDurable(transcriptProgramTurn(payload)) },
			prefix:         "write transcript entry",
			wantRolledBack: true,
		},
		{
			name:   "durable-write-rollback-truncate",
			faults: []int{5, 6},
			invoke: func(w *Writer) error { return w.AppendDurable(transcriptProgramTurn(payload)) },
			prefix: "write transcript entry",
		},
		{
			name:   "durable-write-rollback-seek",
			faults: []int{5, 7},
			invoke: func(w *Writer) error { return w.AppendDurable(transcriptProgramTurn(payload)) },
			prefix: "write transcript entry",
		},
		{
			name:   "durable-write-rollback-both",
			faults: []int{5, 6, 7},
			invoke: func(w *Writer) error { return w.AppendDurable(transcriptProgramTurn(payload)) },
			prefix: "write transcript entry",
		},
		{
			name:   "durable-write-rollback-sync",
			faults: []int{5, 8},
			invoke: func(w *Writer) error { return w.AppendDurable(transcriptProgramTurn(payload)) },
			prefix: "write transcript entry",
		},
		{
			name:           "durable-sync-rollback",
			faults:         []int{6},
			invoke:         func(w *Writer) error { return w.AppendDurable(transcriptProgramTurn(payload)) },
			prefix:         "sync transcript entry",
			wantRolledBack: true,
		},
		{
			name:   "durable-sync-rollback-truncate",
			faults: []int{6, 7},
			invoke: func(w *Writer) error { return w.AppendDurable(transcriptProgramTurn(payload)) },
			prefix: "sync transcript entry",
		},
		{
			name:   "durable-sync-rollback-seek",
			faults: []int{6, 8},
			invoke: func(w *Writer) error { return w.AppendDurable(transcriptProgramTurn(payload)) },
			prefix: "sync transcript entry",
		},
		{
			name:   "durable-sync-rollback-both",
			faults: []int{6, 7, 8},
			invoke: func(w *Writer) error { return w.AppendDurable(transcriptProgramTurn(payload)) },
			prefix: "sync transcript entry",
		},
		{
			name:   "durable-sync-rollback-sync",
			faults: []int{6, 9},
			invoke: func(w *Writer) error { return w.AppendDurable(transcriptProgramTurn(payload)) },
			prefix: "sync transcript entry",
		},
		{
			name:   "api-write",
			faults: []int{4},
			invoke: func(w *Writer) error { return w.AppendAPICall(transcriptProgramAPICall(payload)) },
			prefix: "write transcript api_call",
		},
		{
			name:   "api-sync",
			faults: []int{5},
			invoke: func(w *Writer) error { return w.AppendAPICall(transcriptProgramAPICall(payload)) },
			prefix: "sync transcript api_call",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := transcriptNewFaultWriter(t, root, tc.name, header, tc.faults...)
			path := filepath.Join(root, "fault-writer-"+tc.name+".jsonl")
			before := transcriptReadFile(t, path)
			err := tc.invoke(w)
			transcriptRequireInjectedError(t, err, tc.prefix)
			if tc.wantRolledBack {
				transcriptRequireSeq(t, w, 0, tc.name)
				if got := transcriptReadFile(t, path); !bytes.Equal(got, before) {
					t.Fatalf("%s rollback bytes = %q, want %q", tc.name, got, before)
				}
			}
			_ = w.Close()
		})
	}
}

func transcriptAssertCloseFailurePaths(t *testing.T, root string, header Header, payload byte) {
	t.Helper()

	t.Run("dirty-sync", func(t *testing.T) {
		w := transcriptNewFaultWriter(t, root, "close-sync", header, 5)
		w.SyncInterval = time.Hour
		if err := w.Append(transcriptProgramTurn(payload)); err != nil {
			t.Fatalf("Append before Close: %v", err)
		}
		transcriptRequireInjectedError(t, w.Close(), "sync transcript on close")
	})

	t.Run("file-close", func(t *testing.T) {
		path := filepath.Join(root, "close-file", "transcript.jsonl")
		w, err := NewWriter(path, header)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		if err := w.AppendDurable(transcriptProgramTurn(payload)); err != nil {
			t.Fatalf("AppendDurable: %v", err)
		}
		w.mu.Lock()
		err = w.file.Close()
		w.mu.Unlock()
		if err != nil {
			t.Fatalf("pre-close transcript file: %v", err)
		}
		if err := w.Close(); err == nil || !strings.Contains(err.Error(), "close transcript file") {
			t.Fatalf("Close after pre-close error = %v", err)
		}
	})
}

func transcriptAssertReaderRecoveryPaths(t *testing.T, root string, r *transcriptProgramReader) {
	t.Helper()
	header := transcriptProgramHeader(r.next())
	header.Kind = "header"
	header.FormatVersion = 1
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal reader header: %v", err)
	}

	missing := filepath.Join(root, "reader", "missing.jsonl")
	if _, err := OpenWriter(missing); err == nil || !strings.Contains(err.Error(), "open transcript for resume") {
		t.Fatalf("OpenWriter missing error = %v", err)
	}

	noLines := filepath.Join(root, "reader", "no-lines.jsonl")
	if err := os.MkdirAll(filepath.Dir(noLines), 0o755); err != nil {
		t.Fatalf("create reader dir: %v", err)
	}
	if err := os.WriteFile(noLines, []byte("partial-without-newline"), 0o600); err != nil {
		t.Fatalf("write no-lines transcript: %v", err)
	}
	if _, err := OpenWriter(noLines); err == nil || err.Error() != "transcript has no complete lines" {
		t.Fatalf("OpenWriter no complete lines error = %v", err)
	}

	entryJSON, err := json.Marshal(Entry{Kind: "entry", Seq: 0, Turn: transcriptProgramTurn(r.next())})
	if err != nil {
		t.Fatalf("marshal recovery entry: %v", err)
	}
	partialPrefix := append(append(append([]byte{}, headerJSON...), '\n'), entryJSON...)
	partialPrefix = append(partialPrefix, '\n')
	partialPath := filepath.Join(root, "reader", "partial.jsonl")
	if err := os.WriteFile(partialPath, append(partialPrefix, []byte("crash-tail")...), 0o600); err != nil {
		t.Fatalf("write partial transcript: %v", err)
	}
	w, err := OpenWriter(partialPath)
	if err != nil {
		t.Fatalf("OpenWriter partial: %v", err)
	}
	if got := transcriptReadFile(t, partialPath); !bytes.Equal(got, partialPrefix) {
		t.Fatalf("partial recovery bytes = %q, want %q", got, partialPrefix)
	}
	transcriptRequireSeq(t, w, 1, "partial recovery")
	if err := w.Append(transcriptProgramTurn(r.next())); err != nil {
		t.Fatalf("Append after partial recovery: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close partial recovery: %v", err)
	}
	transcriptAssertCompleteFile(t, partialPath)

	apiJSON, err := json.Marshal(APICall{Kind: "api_call", Seq: 9, Round: 2})
	if err != nil {
		t.Fatalf("marshal recovery API call: %v", err)
	}
	mixedPath := filepath.Join(root, "reader", "mixed.jsonl")
	mixed := bytes.Join([][]byte{
		headerJSON,
		entryJSON,
		apiJSON,
		[]byte("not-json"),
		[]byte(`{"kind":"unknown","seq":99}`),
		{},
	}, []byte{'\n'})
	if err := os.WriteFile(mixedPath, mixed, 0o600); err != nil {
		t.Fatalf("write mixed transcript: %v", err)
	}
	w, err = OpenWriter(mixedPath)
	if err != nil {
		t.Fatalf("OpenWriter mixed: %v", err)
	}
	transcriptRequireSeq(t, w, 10, "mixed recovery")
	if err := w.Append(transcriptProgramTurn(r.next())); err != nil {
		t.Fatalf("Append after mixed recovery: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close mixed recovery: %v", err)
	}
	transcriptAssertLastWriterSeq(t, mixedPath, 10)

	emptyPath := filepath.Join(root, "reader", "empty.jsonl")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatalf("write empty transcript: %v", err)
	}
	w, err = OpenWriter(emptyPath)
	if err != nil {
		t.Fatalf("OpenWriter empty: %v", err)
	}
	transcriptRequireSeq(t, w, 0, "empty recovery")
	if err := w.AppendAPICall(transcriptProgramAPICall(r.next())); err != nil {
		t.Fatalf("AppendAPICall after empty recovery: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close empty recovery: %v", err)
	}
	transcriptAssertLastAPICallSeq(t, emptyPath, 0)

	transcriptAssertFaultedReaderPaths(t, root, append(partialPrefix, []byte("crash-tail")...))

	oversizedPath := filepath.Join(root, "reader", "oversized.jsonl")
	oversized := append(bytes.Repeat([]byte{'x'}, transcriptJSONLMaxLineBytes+1), '\n')
	if err := os.WriteFile(oversizedPath, oversized, 0o600); err != nil {
		t.Fatalf("write oversized transcript: %v", err)
	}
	if _, err := OpenWriter(oversizedPath); err == nil || !strings.Contains(err.Error(), "scanning transcript entries") {
		t.Fatalf("OpenWriter oversized error = %v", err)
	}
}

func transcriptAssertFaultedReaderPaths(t *testing.T, root string, data []byte) {
	t.Helper()
	seen := map[string]bool{}
	for faultAt := 0; faultAt < 16; faultAt++ {
		path := filepath.Join(root, "reader", fmt.Sprintf("fault-%02d.jsonl", faultAt))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write reader fault fixture %d: %v", faultAt, err)
		}
		w, err := openWriterFS(transcriptFaultFS(transcriptFaultPlan(faultAt)), path)
		if err == nil {
			if err := w.Close(); err != nil {
				t.Fatalf("Close reader fault writer %d: %v", faultAt, err)
			}
			continue
		}
		switch {
		case strings.HasPrefix(err.Error(), "open transcript for resume"):
			seen["open"] = true
		case strings.HasPrefix(err.Error(), "read transcript for resume"):
			seen["read"] = true
		case strings.HasPrefix(err.Error(), "truncate partial line"):
			seen["truncate"] = true
		case strings.HasPrefix(err.Error(), "seek to end of transcript"):
			seen["seek"] = true
		default:
			t.Fatalf("reader fault %d returned unexpected error: %v", faultAt, err)
		}
	}
	for _, operation := range []string{"open", "read", "truncate", "seek"} {
		if !seen[operation] {
			t.Fatalf("reader fault sweep never reached %s failure", operation)
		}
	}
}

func transcriptNewFaultWriter(t *testing.T, root, name string, header Header, faults ...int) *Writer {
	t.Helper()
	w, err := newWriterFS(transcriptFaultFS(transcriptFaultPlan(faults...)), filepath.Join(root, "fault-writer-"+name+".jsonl"), header)
	if err != nil {
		t.Fatalf("newWriterFS %s: %v", name, err)
	}
	return w
}

func transcriptFaultFS(plan []byte) afero.Fs {
	return fault.FS(afero.NewOsFs(), fault.FromBytes(plan))
}

func transcriptFaultPlan(faults ...int) []byte {
	plan := bytes.Repeat([]byte{0x01}, 64)
	for _, index := range faults {
		if index >= 0 && index < len(plan) {
			plan[index] = 0x00
		}
	}
	return plan
}

func transcriptRequireInjectedError(t *testing.T, err error, prefix string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected injected error with prefix %q", prefix)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("error = %q, want prefix %q", err, prefix)
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("error = %v, want wrapped fault.ErrInjected", err)
	}
}

func transcriptAppendRaw(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func transcriptReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript %q: %v", path, err)
	}
	return data
}

func transcriptRequireSeq(t *testing.T, w *Writer, want int, context string) {
	t.Helper()
	w.mu.Lock()
	got := w.seq
	w.mu.Unlock()
	if got != want {
		t.Fatalf("%s next sequence = %d, want %d", context, got, want)
	}
}

func transcriptAssertLastWriterSeq(t *testing.T, path string, want int) {
	t.Helper()
	data := transcriptReadFile(t, path)
	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	if len(lines) == 0 {
		t.Fatal("mixed transcript has no lines")
	}
	var last Entry
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
		t.Fatalf("decode last writer entry: %v", err)
	}
	if last.Kind != "entry" || last.Seq != want {
		t.Fatalf("last writer entry = %#v, want entry sequence %d", last, want)
	}
}

func transcriptAssertLastAPICallSeq(t *testing.T, path string, want int) {
	t.Helper()
	data := transcriptReadFile(t, path)
	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("empty recovery lines = %d, want one API record", len(lines))
	}
	var call APICall
	if err := json.Unmarshal(lines[0], &call); err != nil {
		t.Fatalf("decode empty recovery API call: %v", err)
	}
	if call.Kind != "api_call" || call.Seq != want {
		t.Fatalf("empty recovery API call = %#v, want sequence %d", call, want)
	}
}

func transcriptAssertCompleteFile(t *testing.T, path string) {
	t.Helper()
	data := transcriptReadFile(t, path)
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("transcript %q does not end with a complete JSONL line: %q", path, data)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	var header Header
	if err := json.Unmarshal(lines[0], &header); err != nil {
		t.Fatalf("decode transcript header: %v", err)
	}
	if header.Kind != "header" || header.FormatVersion != 1 {
		t.Fatalf("transcript header = %#v", header)
	}

	previousSeq := -1
	for _, line := range lines[1:] {
		var record struct {
			Kind string `json:"kind"`
			Seq  int    `json:"seq"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode transcript record %q: %v", line, err)
		}
		if record.Kind != "entry" && record.Kind != "api_call" {
			t.Fatalf("transcript record kind = %q", record.Kind)
		}
		if record.Seq <= previousSeq {
			t.Fatalf("transcript sequence = %d after %d", record.Seq, previousSeq)
		}
		previousSeq = record.Seq
	}
}
