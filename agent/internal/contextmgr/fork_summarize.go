package contextmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/internal/sessionlog"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// forkSummarize makes a side LLM call using a cheap model to produce a
// sessionlog.SessionLogEntry summarizing the most recent action in turns. The
// prompt explicitly preserves failure signals so errors are not lost during
// summarization.
func forkSummarize(ctx context.Context, client *llm.Client, profile *provider.Profile, turns []schema.Turn, turnNumber int) (sessionlog.SessionLogEntry, error) {
	prompt := buildSummarizePrompt(turns)

	cheapProvider, cheapModel := profile.CheapModelRef()
	req := llm.Request{
		Model:    cheapModel,
		Provider: cheapProvider,
		Messages: []llm.Message{llm.User(prompt)},
	}

	resp, err := client.Complete(ctx, req)
	if err != nil {
		return sessionlog.SessionLogEntry{}, fmt.Errorf("fork summarize: %w", err)
	}

	text := strings.TrimSpace(resp.Text())
	// Strip markdown code fences if the model wraps JSON in them.
	text = stripCodeFence(text)

	var entry sessionlog.SessionLogEntry
	if err := json.Unmarshal([]byte(text), &entry); err != nil {
		return sessionlog.SessionLogEntry{}, fmt.Errorf("fork summarize: failed to parse JSON: %w (raw: %s)", err, text)
	}

	entry.Turn = turnNumber
	return entry, nil
}

// buildSummarizePrompt renders the recent turns and the summarization
// instruction into a single prompt string for the cheap model.
func buildSummarizePrompt(turns []schema.Turn) string {
	var b strings.Builder
	b.WriteString("Summarize the most recent action in this coding agent session.\n\n")
	b.WriteString("Recent conversation:\n")

	for _, t := range turns {
		switch t.Kind {
		case schema.TurnUserInput:
			b.WriteString("User: " + truncate(t.Message.Text(), 500) + "\n")
		case schema.TurnAssistant:
			b.WriteString("Assistant: " + truncate(t.Message.Text(), 500) + "\n")
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
					b.WriteString(fmt.Sprintf("  [tool_call: %s(%s)]\n", p.ToolCall.Name, truncate(string(p.ToolCall.Arguments), 200)))
				}
			}
		case schema.TurnTool, schema.TurnToolResults:
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					content := fmt.Sprint(p.ToolResult.Content)
					errTag := ""
					if p.ToolResult.IsError {
						errTag = " ERROR"
					}
					b.WriteString(fmt.Sprintf("Tool(%s)%s: %s\n", p.ToolResult.Name, errTag, truncate(content, 300)))
				}
			}
		case schema.TurnSteering:
			b.WriteString("System: " + truncate(t.Message.Text(), 200) + "\n")
		}
	}

	b.WriteString(`
Produce a JSON object with these fields:
- action: the tool name that was called (e.g., "shell", "edit_file", "read_file"). If no tool was called, use "assistant".
- summary: 1-2 sentence summary of what was done and why (be specific about file paths, errors, decisions)
- outcome: "success" or "failure"
- files_touched: array of file paths that were read or modified (empty array if none)
- failures: if outcome is "failure", array of specific error messages. CRITICAL: you MUST preserve the exact error text, not summarize it.

IMPORTANT: If the action failed, you MUST:
- Set outcome to "failure"
- Include the actual error messages in the failures array
- Do NOT soften or summarize errors. Preserve them exactly.

Respond with ONLY the JSON object, no markdown formatting.
`)
	return b.String()
}

// truncate shortens s to limit characters, appending "..." if truncated.
func truncate(s string, limit int) string {
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}

// stripCodeFence removes markdown code fences from a string. Some models
// wrap JSON output in ```json ... ``` despite being asked not to.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Remove opening fence line.
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		// Remove closing fence.
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}
	return s
}
