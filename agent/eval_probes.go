package agent

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// probeQuestion represents a retention probe question with its expected answer
// and metadata for evaluation.
type probeQuestion struct {
	Question   string `json:"question"`
	Expected   string `json:"expected"`
	Difficulty string `json:"difficulty"` // "easy", "medium", "hard", "distractor"
	Type       string `json:"type"`       // "factual", "distractor"
}

// probeResult captures the outcome of a single retention probe.
type probeResult struct {
	Question   string `json:"question"`
	Expected   string `json:"expected"`
	Difficulty string `json:"difficulty"`
	Correct    bool   `json:"correct"`
}

// runRetentionProbes injects probe questions about earlier decisions into a
// completed session and judges the agent's answers against expected values.
// Returns the fraction of correct answers (0.0-1.0) and per-question results.
func runRetentionProbes(ctx context.Context, client *llm.Client, profile *provider.Profile, probeQuestions []probeQuestion, sessionHistory []schema.Turn) (float64, []probeResult, error) {
	if len(probeQuestions) == 0 {
		return 0.0, nil, nil
	}

	// Build message history from session turns.
	history := turnsToMessages(sessionHistory)

	var correct int
	results := make([]probeResult, 0, len(probeQuestions))
	for _, pq := range probeQuestions {
		isCorrect, err := runOneProbe(ctx, client, profile, history, pq)
		if err != nil {
			return 0, nil, fmt.Errorf("retention probe %q: %w", pq.Question, err)
		}
		results = append(results, probeResult{
			Question:   pq.Question,
			Expected:   pq.Expected,
			Difficulty: pq.Difficulty,
			Correct:    isCorrect,
		})
		if isCorrect {
			correct++
		}
	}

	return float64(correct) / float64(len(probeQuestions)), results, nil
}

// runOneProbe sends a single probe question to the agent, then judges the
// response against the expected answer. Returns true if the judge says correct.
func runOneProbe(ctx context.Context, client *llm.Client, profile *provider.Profile, history []llm.Message, pq probeQuestion) (bool, error) {
	// Step 1: Ask the agent the probe question with the session history.
	agentMessages := append(append([]llm.Message{}, history...), llm.User(pq.Question))
	agentReq := llm.Request{
		Model:    profile.Model(),
		Provider: profile.ID(),
		Messages: agentMessages,
	}

	agentResp, err := client.Complete(ctx, agentReq)
	if err != nil {
		return false, fmt.Errorf("agent call: %w", err)
	}
	agentAnswer := strings.TrimSpace(agentResp.Text())

	// Step 2: Judge the response using the cheap model with binary scoring.
	judgePrompt := buildBinaryJudgePrompt(pq.Question, pq.Expected, agentAnswer)
	judgeReq := llm.Request{
		Model:    profile.CheapModel(),
		Provider: profile.ID(),
		Messages: []llm.Message{llm.User(judgePrompt)},
	}

	judgeResp, err := client.Complete(ctx, judgeReq)
	if err != nil {
		return false, fmt.Errorf("judge call: %w", err)
	}

	return parseBinaryJudge(judgeResp.Text()), nil
}

// buildBinaryJudgePrompt constructs a YES/NO evaluation prompt.
func buildBinaryJudgePrompt(question, expected, response string) string {
	return fmt.Sprintf(`You are evaluating whether an AI agent's response matches the expected answer.

Question: %s
Expected answer: %s
Agent's response: %s

Does the agent's response contain or match the expected answer? The response does not need to be exact — it just needs to convey the same information.

Respond with just YES or NO.`, question, expected, response)
}

// parseBinaryJudge extracts a YES/NO verdict from the judge response.
// Defaults to false (NO) for ambiguous responses.
func parseBinaryJudge(raw string) bool {
	trimmed := strings.TrimSpace(strings.ToUpper(raw))
	if idx := strings.IndexByte(trimmed, '\n'); idx >= 0 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	return trimmed == "YES"
}

// turnsToMessages converts session Turn history into llm.Message slice
// suitable for sending to the LLM.
func turnsToMessages(turns []schema.Turn) []llm.Message {
	msgs := make([]llm.Message, 0, len(turns))
	for _, t := range turns {
		switch t.Kind {
		case schema.TurnSteering:
			msgs = append(msgs, llm.User(t.Message.Text()))
		case schema.TurnToolResults:
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					msgs = append(msgs, llm.ToolResultNamed(
						p.ToolResult.ToolCallID,
						p.ToolResult.Name,
						p.ToolResult.Content,
						p.ToolResult.IsError,
					))
				}
			}
		default:
			msgs = append(msgs, t.Message)
		}
	}
	return msgs
}
