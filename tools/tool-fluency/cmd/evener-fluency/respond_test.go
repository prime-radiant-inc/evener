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
// Complete call with a fixed JSON {"answers": [...]} object, matching
// respondAnswerSchema, so callRespondModel's real GenerateObject call runs
// against a fake only at the LLM boundary (AGENTS.md's testing-boundary
// rule), never a live request.
type respondFakeAdapter struct {
	name    string
	answers []string
	// requests records every request this adapter completed, so a test can
	// assert on what respond actually sent the model.
	requests []llm.Request
}

func (a *respondFakeAdapter) Name() string { return a.name }

func (a *respondFakeAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.requests = append(a.requests, req)
	body, _ := json.Marshal(map[string][]string{"answers": a.answers})
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
// and the questions JSON on stdin, calls the model once, prints its answer
// numbered, and logs the question/answer pair to --log.
func TestRunRespondAnswersFromBriefWithFakeModel(t *testing.T) {
	adapter := &respondFakeAdapter{name: "fakeprovider", answers: []string{"The launch date is fixed at March 3rd."}}
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
	if got := strings.TrimSpace(stdout); got != "1. The launch date is fixed at March 3rd." {
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

// TestRunRespondPromptListsAllQuestionsNumbered: with two pending
// questions, the prompt the model receives must list both, numbered
// unambiguously ("1. ...", "2. ..."), and the system prompt must instruct
// the model to answer every one of them, in order, one numbered answer
// per question — a real eval run saw the model answer only the first of
// two questions ("What is the new name?" / "Which records should carry
// the new name?"), replying with just the new name. The model returns one
// answer per question; the reply is those answers numbered, and each logged
// pair carries its own question's answer.
func TestRunRespondPromptListsAllQuestionsNumbered(t *testing.T) {
	answers := []string{"Acme Robotics Ltd", "Leave invoices unchanged, per the brief."}
	const fullReply = "1. Acme Robotics Ltd\n2. Leave invoices unchanged, per the brief."
	adapter := &respondFakeAdapter{name: "fakeprovider", answers: answers}
	installRespondFakeAdapter(t, adapter)

	dir := t.TempDir()
	briefPath := filepath.Join(dir, "brief.txt")
	if err := os.WriteFile(briefPath, []byte("You are Alex. The new name is Acme Robotics Ltd. Invoices keep the old name."), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "asks.jsonl")

	stdin := `{"questions":[` +
		`{"question":"What is the new name?","options":[{"label":"Acme Robotics Ltd","detail":"the new legal name"}]},` +
		`{"question":"Which records should carry the new name?","options":[{"label":"All records","detail":""},{"label":"Only new records","detail":""}]}` +
		`]}`

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
	if got := strings.TrimSpace(stdout); got != fullReply {
		t.Fatalf("stdout = %q, want the model's full reply", got)
	}

	if len(adapter.requests) != 1 {
		t.Fatalf("model called %d times, want 1", len(adapter.requests))
	}
	texts := make([]string, len(adapter.requests[0].Messages))
	for i, m := range adapter.requests[0].Messages {
		texts[i] = m.Text()
	}
	promptText := strings.Join(texts, "\n")
	for _, want := range []string{"1.", "What is the new name?", "2.", "Which records should carry the new name?"} {
		if !strings.Contains(promptText, want) {
			t.Errorf("prompt = %q, want it to contain %q", promptText, want)
		}
	}
	var sawEveryQuestionInstruction bool
	for _, m := range adapter.requests[0].Messages {
		if strings.Contains(m.Text(), "every question") {
			sawEveryQuestionInstruction = true
		}
	}
	if !sawEveryQuestionInstruction {
		t.Errorf("system prompt never instructed the model to answer every question: %q", promptText)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != 2 {
		t.Fatalf("log has %d lines, want 2 (one per question)", len(lines))
	}
	wantQuestions := []string{"What is the new name?", "Which records should carry the new name?"}
	for i, line := range lines {
		var logged askExchange
		if err := json.Unmarshal([]byte(line), &logged); err != nil {
			t.Fatalf("log line %d %q: %v", i, line, err)
		}
		if logged.Question != wantQuestions[i] {
			t.Errorf("log line %d question = %q, want %q", i, logged.Question, wantQuestions[i])
		}
		if logged.Answer != answers[i] {
			t.Errorf("log line %d answer = %q, want %q", i, logged.Answer, answers[i])
		}
	}
}

// TestRunRespondRefusesWrongAnswerCount: a model that returns fewer answers
// than questions has skipped one, so respond fails loudly instead of
// replying with a partial answer or logging a question as answered.
func TestRunRespondRefusesWrongAnswerCount(t *testing.T) {
	adapter := &respondFakeAdapter{name: "fakeprovider", answers: []string{"Acme Robotics Ltd"}}
	installRespondFakeAdapter(t, adapter)

	dir := t.TempDir()
	briefPath := filepath.Join(dir, "brief.txt")
	if err := os.WriteFile(briefPath, []byte("You are Alex."), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "asks.jsonl")
	stdin := `{"questions":[{"question":"What is the new name?"},{"question":"Which records change?"}]}`

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

	err = run([]string{"respond", "--brief-file", briefPath, "--model", "fakeprovider/fake-model", "--log", logPath})
	if err == nil || !strings.Contains(err.Error(), "1 answers for 2 questions") {
		t.Fatalf("err = %v, want a refusal naming 1 answers for 2 questions", err)
	}
	if _, statErr := os.Stat(logPath); statErr == nil {
		t.Fatal("a refused round wrote the ask log")
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
