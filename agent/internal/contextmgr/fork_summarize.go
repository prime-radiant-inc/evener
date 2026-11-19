package contextmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/runetrim"
	"primeradiant.com/evener/agent/internal/sessionlog"
	"primeradiant.com/evener/agent/internal/sysmsg"
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
//
// The prompt is a user-role message, and the model's own reply is quoted into
// it: a reserved frame the reply opens would read there as a frame rather than
// as the reply's text. Every model-authored excerpt is therefore escaped, after
// its truncation — a cut can drop the delimiter that made a frame quoted — and
// on the boundaries of the rendered prompt, so a fence that one excerpt leaves
// open and a later one closes is read as the summarizer reads it. The human
// partner's messages, the tool results and the stored turns keep every byte.
func buildSummarizePrompt(turns []schema.Turn) string {
	var b strings.Builder
	// modelSpans records where the model-authored excerpts land in the finished
	// prompt, so the escape can read the whole prompt and rewrite only those.
	var modelSpans [][2]int
	// writeLabel writes an excerpt's label. A model-authored excerpt's label
	// takes its own line: sharing the line with the excerpt's first line hides
	// an opening fence delimiter from the scan, so the example's real closing
	// delimiter opens a new fence and the frame below it survives raw.
	writeLabel := func(label string, modelAuthored bool) {
		if modelAuthored {
			label += "\n"
		} else {
			label += " "
		}
		b.WriteString(label)
	}
	writeSegment := func(text string, modelAuthored bool) {
		start := b.Len()
		b.WriteString(text)
		if modelAuthored {
			modelSpans = append(modelSpans, [2]int{start, b.Len()})
		}
	}
	// writeExcerpt renders one excerpt: its label, then its text escaped after
	// the cap so a cut that exposes a frame is covered too.
	writeExcerpt := func(label, text string, modelAuthored bool) {
		start := b.Len()
		writeLabel(label, modelAuthored)
		b.WriteString(text)
		b.WriteString("\n")
		if modelAuthored {
			modelSpans = append(modelSpans, [2]int{start, b.Len()})
		}
	}
	b.WriteString("Summarize the most recent action in this coding agent session.\n\n")
	b.WriteString("Recent conversation:\n")

	for _, t := range turns {
		switch t.Kind {
		case schema.TurnUserInput:
			// An input turn another agent wrote reads in the partner's channel
			// without being their words; an unstamped or human-stamped turn is
			// their own and keeps its bytes.
			writeExcerpt("User:", truncate(t.Message.Text(), 500), isAgentAuthored(t))
		case schema.TurnAssistant:
			writeExcerpt("Assistant:", truncate(t.Message.Text(), 500), true)
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
					writeSegment("  [tool_call: "+p.ToolCall.Name+"("+truncate(string(p.ToolCall.Arguments), 200)+")]\n", true)
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
			// A parent agent's steer or an agent-authored one is model text too;
			// the human partner's steer is theirs.
			writeExcerpt("System:", truncate(t.Message.Text(), 200), isParentSteering(t) || isAgentAuthored(t))
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
	return escapePromptModelSpans(b.String(), modelSpans)
}

// escapePromptModelSpans returns prompt with the model-authored spans escaped
// and every other byte kept.
//
// The escape is computed once over the whole prompt, so it reads the fence and
// list state the summarizer will read, and so "an answer line follows" is
// answered for the prompt rather than for one excerpt. Only the lines inside
// modelSpans are taken from its result: the human partner's messages and the
// tool results are the authority and keep their bytes.
//
// The escape rewrites a frame's opening character and never adds or removes a
// line break, so the escaped prompt has exactly the lines the original has and
// the two are read together, line by line.
func escapePromptModelSpans(prompt string, modelSpans [][2]int) string {
	escaped := sysmsg.EscapeModelAuthoredFrames(prompt)
	if escaped == prompt {
		return prompt
	}
	original := strings.SplitAfter(prompt, "\n")
	lines := strings.SplitAfter(escaped, "\n")
	if len(lines) != len(original) {
		// The scan changed the line structure; keep the prompt as rendered
		// rather than splice lines that no longer correspond.
		return prompt
	}
	var b strings.Builder
	b.Grow(len(escaped))
	offset := 0
	for i, line := range original {
		if inModelSpan(modelSpans, offset) {
			b.WriteString(lines[i])
		} else {
			b.WriteString(line)
		}
		offset += len(line)
	}
	return b.String()
}

// inModelSpan reports whether the line starting at offset is inside one of the
// model-authored spans. Spans start and end on line boundaries.
func inModelSpan(spans [][2]int, offset int) bool {
	for _, span := range spans {
		if offset >= span[0] && offset < span[1] {
			return true
		}
	}
	return false
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
