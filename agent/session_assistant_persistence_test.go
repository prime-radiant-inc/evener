package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

var errInjectedTranscriptWrite = errors.New("injected transcript write failure")
var errInjectedAttentionReadback = errors.New("injected attention readback failure")

func TestDelegateAttention_AppendIsIdempotentByIdentityAndContent(t *testing.T) {
	sess := newDelegateAttentionTestSession(t)
	if appended, err := sess.appendDelegateNotificationDurably("attention-1", "first"); err != nil || !appended {
		t.Fatalf("first append = %t, %v", appended, err)
	}
	if appended, err := sess.appendDelegateNotificationDurably("attention-1", "first"); err != nil || appended {
		t.Fatalf("duplicate append = %t, %v", appended, err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(sess.stateDir, sess.id), sess.id)
	if err != nil {
		t.Fatalf("read attention fold: %v", err)
	}
	if len(fold.order) != 1 || fold.order[0] != "attention-1" || fold.content["attention-1"].Text() != "first" {
		t.Fatalf("attention fold = %#v", fold)
	}
}

func TestDelegateAttention_ConflictingIdentityIsCorruption(t *testing.T) {
	sess := newDelegateAttentionTestSession(t)
	if _, err := sess.appendDelegateNotificationDurably("attention-1", "first"); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if _, err := sess.appendDelegateNotificationDurably("attention-1", "different"); err == nil {
		t.Fatal("conflicting attention content was accepted")
	}
}

func TestDelegateAttention_FsyncReadbackAmbiguityRetainsAndRepairsExactResidentTurn(t *testing.T) {
	sess := newDelegateAttentionTestSession(t)
	reads := 0
	sess.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
		reads++
		if reads == 2 {
			return delegateAttentionFold{}, errInjectedAttentionReadback
		}
		return readDelegateAttentionFold(path, sessionID)
	}
	if appended, err := sess.appendDelegateNotificationDurably("attention-ambiguous", "durable attention"); appended || !errors.Is(err, errInjectedAttentionReadback) {
		t.Fatalf("ambiguous attention append = appended:%t err:%v", appended, err)
	}
	durable, err := readDelegateAttentionFold(transcriptPath(sess.stateDir, sess.id), sess.id)
	if err != nil {
		t.Fatalf("read durable attention: %v", err)
	}
	want := durable.turns["attention-ambiguous"]
	if want.AttentionID == "" {
		t.Fatal("fsynced attention turn is absent from durable fold")
	}
	sess.mu.Lock()
	resident := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if len(resident) != 1 || !reflect.DeepEqual(resident[0], want) {
		t.Fatalf("resident attention after ambiguous readback = %#v, want exact durable turn %#v", resident, want)
	}

	sess.mu.Lock()
	sess.history = nil
	sess.mu.Unlock()
	sess.cfg.testOnly.delegateAttentionReadFold = nil
	if appended, err := sess.appendDelegateNotificationDurably("attention-ambiguous", "durable attention"); err != nil || appended {
		t.Fatalf("retry existing attention = appended:%t err:%v", appended, err)
	}
	sess.mu.Lock()
	resident = append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if len(resident) != 1 || !reflect.DeepEqual(resident[0], want) {
		t.Fatalf("repaired resident attention = %#v, want exact durable turn %#v", resident, want)
	}
}

func newDelegateAttentionTestSession(t *testing.T) *Session {
	t.Helper()
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, sessionsSubdir), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	const sessionID = "attention-session"
	writer, err := transcript.NewWriter(transcriptPath(stateDir, sessionID), transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	sess := &Session{id: sessionID, stateDir: stateDir}
	sess.attachTranscript(writer)
	return sess
}

type transcriptWriteFailFS struct {
	afero.Fs
	fail bool
}

func (fs *transcriptWriteFailFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &transcriptWriteFailFile{File: file, fs: fs}, nil
}

type transcriptWriteFailFile struct {
	afero.File
	fs *transcriptWriteFailFS
}

func (file *transcriptWriteFailFile) Write(p []byte) (int, error) {
	if file.fs.fail {
		return 0, errInjectedTranscriptWrite
	}
	return file.File.Write(p)
}

func TestSession_AssistantTranscriptFailureStopsBeforeToolDispatch(t *testing.T) {
	fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
	const transcriptPath = "/session.jsonl"
	writer, err := transcript.NewWriterWithFS(fs, transcriptPath, transcript.Header{SessionID: "persist-failure"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	adapter := &fakeAdapter{name: "openai"}
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			fs.fail = true
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "must_not_run",
				Name:      "my_tool",
				Arguments: json.RawMessage(`{}`),
				Type:      "function",
			})
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		}},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.transcript = writer

	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for range sess.Events() {
		}
	}()
	defer func() {
		sess.Close()
		<-eventsDone
	}()

	var toolRuns int
	sess.RegisterTool("my_tool", "must not run after persistence failure", map[string]any{
		"type":                 "object",
		"additionalProperties": false,
	}, func(context.Context, any) (any, error) {
		toolRuns++
		return "ran", nil
	})

	output, err := sess.ProcessInput(context.Background(), "trigger persistence failure", nil)
	if !errors.Is(err, errInjectedTranscriptWrite) {
		t.Fatalf("ProcessInput error = %v, want injected transcript write failure", err)
	}
	if output != "" {
		t.Fatalf("ProcessInput output = %q, want empty", output)
	}
	if toolRuns != 0 {
		t.Fatalf("tool executed %d time(s), want 0", toolRuns)
	}
	if _, ok := findToolCallInHistory(sess.history, "must_not_run"); ok {
		t.Fatal("failed assistant turn entered live history")
	}
	if _, ok := findToolResultInHistory(sess.history, "must_not_run"); ok {
		t.Fatal("tool result entered live history after assistant persistence failure")
	}

	data, readErr := afero.ReadFile(fs, transcriptPath)
	if readErr != nil {
		t.Fatalf("read transcript: %v", readErr)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	// header, the ENVIRONMENT turn maybeAppendEnvironmentContext injects before
	// every user turn, then the user input itself.
	if len(lines) != 3 {
		t.Fatalf("transcript lines = %d, want header plus environment context plus user input", len(lines))
	}
	envEntry, decodeErr := transcript.DecodeEntry(lines[1])
	if decodeErr != nil {
		t.Fatalf("decode persisted environment context: %v", decodeErr)
	}
	if envEntry.Turn.Kind != schema.TurnEnvironment {
		t.Fatalf("persisted turn kind = %s, want %s", envEntry.Turn.Kind, schema.TurnEnvironment)
	}
	entry, decodeErr := transcript.DecodeEntry(lines[2])
	if decodeErr != nil {
		t.Fatalf("decode persisted user input: %v", decodeErr)
	}
	if entry.Turn.Kind != schema.TurnUserInput {
		t.Fatalf("persisted turn kind = %s, want %s", entry.Turn.Kind, schema.TurnUserInput)
	}
}
