package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// respondFakeAdapter is a minimal llm.ProviderAdapter that answers every
// Complete call with a fixed JSON {"answer": ...} object, matching
// respondAnswerSchema, so callRespondModel's real GenerateObject call runs
// against a fake only at the LLM boundary (AGENTS.md's testing-boundary
// rule), never a live request.
type respondFakeAdapter struct {
	name   string
	answer string
	// requests records every request this adapter completed, so a test can
	// assert on what respond actually sent the model.
	requests []llm.Request
}

func (a *respondFakeAdapter) Name() string { return a.name }

func (a *respondFakeAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.requests = append(a.requests, req)
	body, _ := json.Marshal(map[string]string{"answer": a.answer})
	return llm.Response{
		Message: llm.Assistant(string(body)),
		Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
	}, nil
}

func (a *respondFakeAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func installRespondFakeAdapter(t *testing.T, adapter *respondFakeAdapter) {
	t.Helper()
	old := runnerLoadClient
	t.Cleanup(func() { runnerLoadClient = old })
	runnerLoadClient = func(string) (*llm.Client, error) {
		client := llm.NewClient()
		client.Register(adapter)
		client.SetDefaultProvider(adapter.Name())
		return client, nil
	}
}

// TestRunRespondAnswersFromBriefWithFakeModel: respond reads the brief file
// and the questions JSON on stdin, calls the model once, prints its answer,
// and logs the question/answer pair to --log.
func TestRunRespondAnswersFromBriefWithFakeModel(t *testing.T) {
	adapter := &respondFakeAdapter{name: "fakeprovider", answer: "The launch date is fixed at March 3rd."}
	installRespondFakeAdapter(t, adapter)

	dir := t.TempDir()
	briefPath := filepath.Join(dir, "brief.txt")
	if err := os.WriteFile(briefPath, []byte("You are Alex, the product owner. The launch date is fixed at March 3rd."), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "asks.jsonl")

	stdin := `{"questions":[{"question":"Can the launch date move?","options":[{"label":"Yes","detail":"Slip it"},{"label":"No","detail":"Keep it"}]}]}`

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })
	go func() {
		defer w.Close()
		w.WriteString(stdin)
	}()

	stdout := captureStdout(t, func() error {
		return run([]string{"respond", "--brief-file", briefPath, "--model", "fakeprovider/fake-model", "--log", logPath})
	})
	if got := strings.TrimSpace(stdout); got != "The launch date is fixed at March 3rd." {
		t.Fatalf("stdout = %q, want the model's answer", got)
	}

	if len(adapter.requests) != 1 {
		t.Fatalf("model called %d times, want 1", len(adapter.requests))
	}
	sent := adapter.requests[0].Messages
	var sawBrief, sawQuestion bool
	for _, m := range sent {
		if strings.Contains(m.Text(), "March 3rd") {
			sawBrief = true
		}
		if strings.Contains(m.Text(), "Can the launch date move?") {
			sawQuestion = true
		}
	}
	if !sawBrief {
		t.Errorf("model request never carried the brief: %+v", sent)
	}
	if !sawQuestion {
		t.Errorf("model request never carried the question: %+v", sent)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var logged askExchange
	if err := json.Unmarshal(bytes.TrimSpace(logData), &logged); err != nil {
		t.Fatalf("log line %q: %v", logData, err)
	}
	if logged.Question != "Can the launch date move?" || logged.Answer != "The launch date is fixed at March 3rd." {
		t.Errorf("logged = %+v", logged)
	}
}

// TestRunRespondRequiresBriefFileAndModel: missing required flags fail
// loudly instead of calling the model with nothing to say.
func TestRunRespondRequiresBriefFileAndModel(t *testing.T) {
	if err := run([]string{"respond", "--model", "fakeprovider/m"}); err == nil {
		t.Fatal("want an error with no --brief-file")
	}
	dir := t.TempDir()
	briefPath := filepath.Join(dir, "brief.txt")
	if err := os.WriteFile(briefPath, []byte("brief"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"respond", "--brief-file", briefPath}); err == nil {
		t.Fatal("want an error with no --model")
	}
}
