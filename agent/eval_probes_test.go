package agent

import (
	"context"
	"fmt"
	"testing"

	"primeradiant.com/serf/llm"
)

// stubProbeAdapter captures requests and returns canned responses for probe tests.
// It tracks call count to alternate between agent response and judge response.
type stubProbeAdapter struct {
	name      string
	callCount int
	responses []llm.Response
	errors    []error
}

func (a *stubProbeAdapter) Name() string { return a.name }
func (a *stubProbeAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	idx := a.callCount
	a.callCount++
	if idx < len(a.errors) && a.errors[idx] != nil {
		return llm.Response{}, a.errors[idx]
	}
	if idx < len(a.responses) {
		return a.responses[idx], nil
	}
	return llm.Response{Message: llm.Assistant("")}, nil
}
func (a *stubProbeAdapter) Stream(_ context.Context, _ llm.Request) (llm.Stream, error) {
	return nil, fmt.Errorf("stream not implemented")
}

func TestRunRetentionProbes_SingleQuestion(t *testing.T) {
	// For a single probe question, there are 2 LLM calls:
	// 1. Agent responds to the probe question
	// 2. Judge scores the response
	adapter := &stubProbeAdapter{
		name: "openai",
		responses: []llm.Response{
			{Message: llm.Assistant("I encountered a type error in main.go when modifying the handler function.")},
			{Message: llm.Assistant("4")},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("Fix the bug in main.go")},
		{Kind: TurnAssistant, Message: llm.Assistant("I'll read main.go first.")},
	}

	score, err := RunRetentionProbes(context.Background(), client, profile,
		[]string{"What error did you encounter when modifying main.go?"},
		history,
	)
	if err != nil {
		t.Fatalf("RunRetentionProbes: %v", err)
	}
	// Judge scored 4 out of 5 = 0.8
	if score != 0.8 {
		t.Errorf("expected score 0.8, got %f", score)
	}
}

func TestRunRetentionProbes_MultipleQuestions(t *testing.T) {
	adapter := &stubProbeAdapter{
		name: "openai",
		responses: []llm.Response{
			{Message: llm.Assistant("response 1")},
			{Message: llm.Assistant("3")},
			{Message: llm.Assistant("response 2")},
			{Message: llm.Assistant("5")},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("task")},
	}

	score, err := RunRetentionProbes(context.Background(), client, profile,
		[]string{"question 1", "question 2"},
		history,
	)
	if err != nil {
		t.Fatalf("RunRetentionProbes: %v", err)
	}
	// (3/5 + 5/5) / 2 = (0.6 + 1.0) / 2 = 0.8
	if score != 0.8 {
		t.Errorf("expected score 0.8, got %f", score)
	}
}

func TestRunRetentionProbes_ParsesWhitespace(t *testing.T) {
	adapter := &stubProbeAdapter{
		name: "openai",
		responses: []llm.Response{
			{Message: llm.Assistant("some answer")},
			{Message: llm.Assistant("  4  ")},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	score, err := RunRetentionProbes(context.Background(), client, profile,
		[]string{"q1"},
		[]Turn{{Kind: TurnUserInput, Message: llm.User("task")}},
	)
	if err != nil {
		t.Fatalf("RunRetentionProbes: %v", err)
	}
	if score != 0.8 {
		t.Errorf("expected score 0.8, got %f", score)
	}
}

func TestRunRetentionProbes_NoQuestions(t *testing.T) {
	client := llm.NewClient()
	profile := NewOpenAIProfile("gpt-5.2")

	score, err := RunRetentionProbes(context.Background(), client, profile,
		[]string{},
		[]Turn{{Kind: TurnUserInput, Message: llm.User("task")}},
	)
	if err != nil {
		t.Fatalf("RunRetentionProbes: %v", err)
	}
	if score != 0.0 {
		t.Errorf("expected score 0.0 for empty questions, got %f", score)
	}
}

func TestRunRetentionProbes_AgentCallFails(t *testing.T) {
	adapter := &stubProbeAdapter{
		name:   "openai",
		errors: []error{fmt.Errorf("rate limited")},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	_, err := RunRetentionProbes(context.Background(), client, profile,
		[]string{"question"},
		[]Turn{{Kind: TurnUserInput, Message: llm.User("task")}},
	)
	if err == nil {
		t.Fatal("expected error when agent call fails")
	}
}

func TestRunRetentionProbes_JudgeCallFails(t *testing.T) {
	adapter := &stubProbeAdapter{
		name: "openai",
		responses: []llm.Response{
			{Message: llm.Assistant("some answer")},
		},
		errors: []error{nil, fmt.Errorf("judge failed")},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	_, err := RunRetentionProbes(context.Background(), client, profile,
		[]string{"question"},
		[]Turn{{Kind: TurnUserInput, Message: llm.User("task")}},
	)
	if err == nil {
		t.Fatal("expected error when judge call fails")
	}
}

func TestRunRetentionProbes_JudgeReturnsNonNumeric(t *testing.T) {
	adapter := &stubProbeAdapter{
		name: "openai",
		responses: []llm.Response{
			{Message: llm.Assistant("some answer")},
			{Message: llm.Assistant("not a number")},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	_, err := RunRetentionProbes(context.Background(), client, profile,
		[]string{"question"},
		[]Turn{{Kind: TurnUserInput, Message: llm.User("task")}},
	)
	if err == nil {
		t.Fatal("expected error when judge returns non-numeric score")
	}
}

func TestParseJudgeScore(t *testing.T) {
	tests := []struct {
		input string
		want  int
		err   bool
	}{
		{"3", 3, false},
		{"  4  ", 4, false},
		{"0", 0, false},
		{"5", 5, false},
		{" 2 \n", 2, false},
		{"not a number", 0, true},
		{"6", 0, true},  // out of range
		{"-1", 0, true}, // out of range
		{"", 0, true},
	}

	for _, tt := range tests {
		got, err := parseJudgeScore(tt.input)
		if tt.err && err == nil {
			t.Errorf("parseJudgeScore(%q): expected error, got nil", tt.input)
		}
		if !tt.err && err != nil {
			t.Errorf("parseJudgeScore(%q): unexpected error: %v", tt.input, err)
		}
		if !tt.err && got != tt.want {
			t.Errorf("parseJudgeScore(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
