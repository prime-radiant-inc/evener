package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

var errInjectedTranscriptWrite = errors.New("injected transcript write failure")

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
	if len(lines) != 2 {
		t.Fatalf("transcript lines = %d, want header plus user input", len(lines))
	}
	entry, decodeErr := transcript.DecodeEntry(lines[1])
	if decodeErr != nil {
		t.Fatalf("decode persisted user input: %v", decodeErr)
	}
	if entry.Turn.Kind != schema.TurnUserInput {
		t.Fatalf("persisted turn kind = %s, want %s", entry.Turn.Kind, schema.TurnUserInput)
	}
}
