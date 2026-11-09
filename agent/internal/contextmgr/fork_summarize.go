package contextmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/runetrim"
	"primeradiant.com/evener/agent/internal/sessionlog"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// forkSummarize makes a side LLM call using a cheap model to produce a
// sessionlog.SessionLogEntry summarizing the most recent action in turns. The
// prompt explicitly preserves failure signals so errors are not lost during
// summarization. A reply that does not parse as a JSON entry — an empty reply
// never does — is one bad sample, and is resampled within the sample budget
// the conversation summary uses (summarizerRouteAttempts keeps both surfaces
// in step) before the failure surfaces; the exhausted error never embeds the
// raw reply, because it reaches the user as a warning and warnings never
// carry conversation-adjacent text.
func forkSummarize(ctx context.Context, cheap *cheapmodel.Caller, profile *provider.Profile, turns []schema.Turn, turnNumber int) (sessionlog.SessionLogEntry, error) {
	prompt := buildSummarizePrompt(turns)

	req := llm.Request{
		Messages: []llm.Message{llm.User(prompt)},
	}

	for sample := 1; ; sample++ {
		resp, err := cheap.Complete(ctx, profile, req)
		if err != nil {
			return sessionlog.SessionLogEntry{}, fmt.Errorf("fork summarize: %w", err)
		}
		if resp.Finish.Reason == llm.FinishReasonLength {
			// A truncated reply hit the model's output cap, which a fresh
			// sample hits again: like the conversation summary's cut-off
			// replies, it gets no resample.
			return sessionlog.SessionLogEntry{}, fmt.Errorf("fork summarize: %w", errSummaryForkReply)
		}

		text := strings.TrimSpace(resp.Text())
		// Strip markdown code fences if the model wraps JSON in them.
		text = stripCodeFence(text)

		var entry sessionlog.SessionLogEntry
		if err := json.Unmarshal([]byte(text), &entry); err == nil {
			entry.Turn = turnNumber
			return entry, nil
		}
		if sample >= summarizerRouteAttempts || ctx.Err() != nil {
			return sessionlog.SessionLogEntry{}, fmt.Errorf("fork summarize: %w", errSummaryForkReply)
		}
	}
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
			b.WriteString("User: ")
			b.WriteString(truncate(t.Message.Text(), 500))
			b.WriteString("\n")
		case schema.TurnAssistant:
			b.WriteString("Assistant: ")
			b.WriteString(truncate(t.Message.Text(), 500))
			b.WriteString("\n")
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
					fmt.Fprintf(&b, "  [tool_call: %s(%s)]\n", p.ToolCall.Name, truncate(string(p.ToolCall.Arguments), 200))
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
					fmt.Fprintf(&b, "Tool(%s)%s: %s\n", p.ToolResult.Name, errTag, truncate(content, 300))
				}
			}
		case schema.TurnSteering:
			b.WriteString("System: ")
			b.WriteString(truncate(t.Message.Text(), 200))
			b.WriteString("\n")
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
		return runetrim.Cut(s, limit) + "..."
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
