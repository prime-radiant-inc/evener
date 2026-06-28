package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
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
	return nil, llm.ErrStreamUnsupported
}

func TestRunRetentionProbes_SingleQuestion_Correct(t *testing.T) {
	t.Parallel()
	// For a single probe question, there are 2 LLM calls:
	// 1. Agent responds to the probe question
	// 2. Judge says YES or NO
	adapter := &stubProbeAdapter{
		name: "openai",
		responses: []llm.Response{
			{Message: llm.Assistant("I was working on django/django")},
			{Message: llm.Assistant("YES")},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("Fix the bug in django")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("I'll fix it.")},
	}

	probes := []probeQuestion{
		{Question: "What repo are you working on?", Expected: "django/django", Difficulty: "easy", Type: "factual"},
	}

	score, results, err := runRetentionProbes(context.Background(), client, profile, probes, history)
	if err != nil {
		t.Fatalf("RunRetentionProbes: %v", err)
	}
	if score != 1.0 {
		t.Errorf("expected score 1.0, got %f", score)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Correct {
		t.Error("expected result to be correct")
	}
	if results[0].Difficulty != "easy" {
		t.Errorf("expected difficulty 'easy', got %q", results[0].Difficulty)
	}
}

func TestRunRetentionProbes_SingleQuestion_Incorrect(t *testing.T) {
	t.Parallel()
	adapter := &stubProbeAdapter{
		name: "openai",
		responses: []llm.Response{
			{Message: llm.Assistant("I don't remember")},
			{Message: llm.Assistant("NO")},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	probes := []probeQuestion{
		{Question: "What repo?", Expected: "django/django", Difficulty: "easy", Type: "factual"},
	}

	score, results, err := runRetentionProbes(context.Background(), client, profile, probes,
		[]schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("task")}},
	)
	if err != nil {
		t.Fatalf("RunRetentionProbes: %v", err)
	}
	if score != 0.0 {
		t.Errorf("expected score 0.0, got %f", score)
	}
	if results[0].Correct {
		t.Error("expected result to be incorrect")
	}
}

func TestRunRetentionProbes_MultipleQuestions(t *testing.T) {
	t.Parallel()
	adapter := &stubProbeAdapter{
		name: "openai",
		responses: []llm.Response{
			{Message: llm.Assistant("django/django")},
			{Message: llm.Assistant("YES")},
			{Message: llm.Assistant("I don't know")},
			{Message: llm.Assistant("NO")},
			{Message: llm.Assistant("python")},
			{Message: llm.Assistant("YES")},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	probes := []probeQuestion{
		{Question: "What repo?", Expected: "django/django", Difficulty: "easy", Type: "factual"},
		{Question: "What function?", Expected: "escape()", Difficulty: "medium", Type: "factual"},
		{Question: "What language?", Expected: "python", Difficulty: "easy", Type: "factual"},
	}

	score, results, err := runRetentionProbes(context.Background(), client, profile, probes,
		[]schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("task")}},
	)
	if err != nil {
		t.Fatalf("RunRetentionProbes: %v", err)
	}
	// 2 out of 3 correct
	expected := 2.0 / 3.0
	if score < expected-0.001 || score > expected+0.001 {
		t.Errorf("expected score ~%f, got %f", expected, score)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if !results[0].Correct {
		t.Error("expected result 0 correct")
	}
	if results[1].Correct {
		t.Error("expected result 1 incorrect")
	}
	if !results[2].Correct {
		t.Error("expected result 2 correct")
	}
}

func TestRunRetentionProbes_NoQuestions(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	profile := NewOpenAIProfile("gpt-5.2")

	score, results, err := runRetentionProbes(context.Background(), client, profile,
		[]probeQuestion{},
		[]schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("task")}},
	)
	if err != nil {
		t.Fatalf("RunRetentionProbes: %v", err)
	}
	if score != 0.0 {
		t.Errorf("expected score 0.0 for empty questions, got %f", score)
	}
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}
}

func TestRunRetentionProbes_AgentCallFails(t *testing.T) {
	t.Parallel()
	adapter := &stubProbeAdapter{
		name:   "openai",
		errors: []error{errors.New("rate limited")},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	_, _, err := runRetentionProbes(context.Background(), client, profile,
		[]probeQuestion{{Question: "q", Expected: "a", Difficulty: "easy", Type: "factual"}},
		[]schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("task")}},
	)
	if err == nil {
		t.Fatal("expected error when agent call fails")
	}
}

func TestRunRetentionProbes_JudgeCallFails(t *testing.T) {
	t.Parallel()
	adapter := &stubProbeAdapter{
		name: "openai",
		responses: []llm.Response{
			{Message: llm.Assistant("some answer")},
		},
		errors: []error{nil, errors.New("judge failed")},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	_, _, err := runRetentionProbes(context.Background(), client, profile,
		[]probeQuestion{{Question: "q", Expected: "a", Difficulty: "easy", Type: "factual"}},
		[]schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("task")}},
	)
	if err == nil {
		t.Fatal("expected error when judge call fails")
	}
}

func TestParseBinaryJudge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  bool
	}{
		{"YES", true},
		{"yes", true},
		{"Yes", true},
		{"  YES  ", true},
		{"YES\nThe response matches", true},
		{"NO", false},
		{"no", false},
		{"No", false},
		{"  NO  ", false},
		{"NO\nThe response does not match", false},
		{"maybe", false},
		{"", false},
		{"YESNO", false},
	}

	for _, tt := range tests {
		got := parseBinaryJudge(tt.input)
		if got != tt.want {
			t.Errorf("parseBinaryJudge(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestBuildBinaryJudgePrompt(t *testing.T) {
	t.Parallel()
	prompt := buildBinaryJudgePrompt("What repo?", "django/django", "I was working on django")

	// Each value must appear under the correct label — not just anywhere in the prompt.
	// This catches argument-order bugs like swapping expected and response.
	if !strings.Contains(prompt, "What repo?") {
		t.Errorf("judge prompt missing question: %s", prompt)
	}
	if !strings.Contains(prompt, "Expected answer: django/django") {
		t.Errorf("judge prompt missing labeled expected answer: %s", prompt)
	}
	if !strings.Contains(prompt, "Agent's response: I was working on django") {
		t.Errorf("judge prompt missing labeled agent response: %s", prompt)
	}
	if !strings.Contains(prompt, "YES or NO") {
		t.Errorf("judge prompt missing verdict instruction: %s", prompt)
	}
}

func TestRunRetentionProbes_DistractorScoring(t *testing.T) {
	t.Parallel()
	// A distractor question where the expected answer is "no" —
	// if the agent correctly says "no", the judge should say YES.
	adapter := &stubProbeAdapter{
		name: "openai",
		responses: []llm.Response{
			{Message: llm.Assistant("No, I did not create any database migrations.")},
			{Message: llm.Assistant("YES")}, // Judge: agent's answer matches expected ("no")
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	probes := []probeQuestion{
		{Question: "Did you create a database migration?", Expected: "no", Difficulty: "distractor", Type: "distractor"},
	}

	score, results, err := runRetentionProbes(context.Background(), client, profile, probes,
		[]schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("task")}},
	)
	if err != nil {
		t.Fatalf("RunRetentionProbes: %v", err)
	}
	if score != 1.0 {
		t.Errorf("expected score 1.0, got %f", score)
	}
	if !results[0].Correct {
		t.Error("expected distractor result to be correct")
	}
	if results[0].Difficulty != "distractor" {
		t.Errorf("expected difficulty 'distractor', got %q", results[0].Difficulty)
	}
}
