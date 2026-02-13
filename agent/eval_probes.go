package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"primeradiant.com/serf/llm"
)

// RunRetentionProbes injects probe questions about earlier decisions into a
// completed session and scores the agent's ability to recall information.
// Each probe question is sent to the LLM with the session history, then a
// cheap judge model scores the response on a 0-5 scale. Returns the average
// normalized score (0.0-1.0).
func RunRetentionProbes(ctx context.Context, client *llm.Client, profile ProviderProfile, probeQuestions []string, sessionHistory []Turn) (float64, error) {
	if len(probeQuestions) == 0 {
		return 0.0, nil
	}

	// Build message history from session turns.
	history := turnsToMessages(sessionHistory)

	var totalScore float64
	for _, question := range probeQuestions {
		score, err := runOneProbe(ctx, client, profile, history, question)
		if err != nil {
			return 0, fmt.Errorf("retention probe %q: %w", question, err)
		}
		totalScore += score
	}

	return totalScore / float64(len(probeQuestions)), nil
}

// runOneProbe sends a single probe question to the agent, then judges the response.
func runOneProbe(ctx context.Context, client *llm.Client, profile ProviderProfile, history []llm.Message, question string) (float64, error) {
	// Step 1: Ask the agent the probe question with the session history.
	agentMessages := append(append([]llm.Message{}, history...), llm.User(question))
	agentReq := llm.Request{
		Model:    profile.Model(),
		Provider: profile.ID(),
		Messages: agentMessages,
	}

	agentResp, err := client.Complete(ctx, agentReq)
	if err != nil {
		return 0, fmt.Errorf("agent call: %w", err)
	}
	agentAnswer := strings.TrimSpace(agentResp.Text())

	// Step 2: Judge the response using the cheap model.
	judgePrompt := buildJudgePrompt(question, agentAnswer)
	judgeReq := llm.Request{
		Model:    profile.CheapModel(),
		Provider: profile.ID(),
		Messages: []llm.Message{llm.User(judgePrompt)},
	}

	judgeResp, err := client.Complete(ctx, judgeReq)
	if err != nil {
		return 0, fmt.Errorf("judge call: %w", err)
	}

	score, err := parseJudgeScore(judgeResp.Text())
	if err != nil {
		return 0, fmt.Errorf("parse judge score: %w", err)
	}

	return float64(score) / 5.0, nil
}

// buildJudgePrompt constructs the evaluation prompt for the judge model.
func buildJudgePrompt(question, response string) string {
	return fmt.Sprintf(`You are evaluating whether an AI agent retained information from its earlier work session.

Question asked: %s
Agent's response: %s

Score on a 0-5 scale:
0 = No relevant information, completely wrong or "I don't know"
1 = Vague or mostly incorrect
2 = Some relevant details but significant gaps
3 = Mostly accurate with minor gaps
4 = Accurate and fairly complete
5 = Accurate, complete, and detailed

Respond with just the number.`, question, response)
}

// parseJudgeScore parses a judge model's response into a 0-5 integer score.
func parseJudgeScore(raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, fmt.Errorf("empty judge response")
	}
	score, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("non-numeric judge response: %q", trimmed)
	}
	if score < 0 || score > 5 {
		return 0, fmt.Errorf("judge score %d out of range 0-5", score)
	}
	return score, nil
}

// turnsToMessages converts session Turn history into llm.Message slice
// suitable for sending to the LLM.
func turnsToMessages(turns []Turn) []llm.Message {
	msgs := make([]llm.Message, 0, len(turns))
	for _, t := range turns {
		switch t.Kind {
		case TurnSteering:
			msgs = append(msgs, llm.User(t.Message.Text()))
		case TurnToolResults:
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
