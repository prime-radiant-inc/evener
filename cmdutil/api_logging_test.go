package cmdutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

type loggingTestAdapter struct{}

func (loggingTestAdapter) Name() string { return "test" }

func (loggingTestAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{
		Provider:        req.Provider,
		Model:           req.Model,
		Message:         llm.Assistant("ok"),
		Finish:          llm.FinishReason{Reason: llm.FinishReasonStop},
		Usage:           llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
		Raw:             map[string]any{"endpoint_url": "https://example.test/v1/responses"},
		RawRequestBody:  `{"input":"hi"}`,
		RawResponseBody: `{"output":"ok"}`,
	}, nil
}

func (loggingTestAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, nil
}

func TestAttachAPILoggerWritesAPIJSONL(t *testing.T) {
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(loggingTestAdapter{})

	closeLog, err := AttachAPILogger(client, dir, nil)
	if err != nil {
		t.Fatalf("AttachAPILogger: %v", err)
	}

	_, err = client.Complete(llm.WithAPILogContext(context.Background(), "sess-1", 7), llm.Request{
		Provider: "test",
		Model:    "m",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := closeLog(); err != nil {
		t.Fatalf("closeLog: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "api.jsonl"))
	if err != nil {
		t.Fatalf("read api.jsonl: %v", err)
	}
	for _, want := range []string{`"session_id":"sess-1"`, `"round":7`, `"endpoint_url":"https://example.test/v1/responses"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("api.jsonl missing %s:\n%s", want, string(data))
		}
	}
}

func TestAttachAPILoggerEnablesRawWhenProcessEnvSet(t *testing.T) {
	if os.Getenv("SERF_ATTACH_API_LOGGER_RAW_HELPER") == "1" {
		runAttachAPILoggerRawHelper(t)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=TestAttachAPILoggerEnablesRawWhenProcessEnvSet", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"SERF_ATTACH_API_LOGGER_RAW_HELPER=1",
		"SERF_LOG_RAW_HTTP=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("raw logging subprocess failed: %v\n%s", err, string(out))
	}
}

func runAttachAPILoggerRawHelper(t *testing.T) {
	t.Helper()
	if !llm.RawBodyEnabled() {
		t.Fatalf("RawBodyEnabled is false despite process env")
	}

	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(loggingTestAdapter{})

	closeLog, err := AttachAPILogger(client, dir, nil)
	if err != nil {
		t.Fatalf("AttachAPILogger: %v", err)
	}
	_, err = client.Complete(llm.WithAPILogContext(context.Background(), "sess-raw", 2), llm.Request{
		Provider: "test",
		Model:    "m",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := closeLog(); err != nil {
		t.Fatalf("closeLog: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "api-raw.jsonl"))
	if err != nil {
		t.Fatalf("read api-raw.jsonl: %v", err)
	}
	for _, want := range []string{`"session_id":"sess-raw"`, `"request_body":"{\"input\":\"hi\"}"`, `"response_body":"{\"output\":\"ok\"}"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("api-raw.jsonl missing %s:\n%s", want, string(data))
		}
	}
}
