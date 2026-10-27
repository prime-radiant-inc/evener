package contextmgr

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/internal/runetrim"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// noteElicitationPrompt asks the model, at compaction time, to enumerate the
// details that must survive verbatim — explicitly targeting the kinds a lossy
// summary erodes (opaque tokens, exact numbers, hashes, names). Used by the
// forced-note-at-compaction mechanism: the reply is pinned as the note before the
// compaction folds the history, so those details are re-stamped verbatim rather
// than decaying through successive summaries.
const noteElicitationPrompt = `Your conversation context is about to be COMPACTED — most of it will be replaced by a lossy summary. Before that happens, list everything that MUST survive VERBATIM for the work to continue.

Focus especially on details a summary tends to drop or paraphrase: exact tokens, IDs, hashes, version tags, exact numbers and thresholds, file/column/endpoint names, and specific decisions. Preserve exact strings — do not abbreviate or round.

Output a concise bullet list of the must-keep items, nothing else.`

// noteElicitChars caps the history rendered into the elicitation prompt.
const noteElicitChars = 80_000

// loadedSkillsElicitSection lists the session's successfully loaded skills and
// the selection-block protocol for note elicitation, so the model can say which
// skills to reload after compaction. Empty when nothing is loaded — with no
// inventory there is nothing a selection could authorize.
func loadedSkillsElicitSection(loaded []schema.SkillInventorySummary) string {
	if len(loaded) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n--- SKILLS LOADED IN THIS SESSION ---\n")
	for _, s := range loaded {
		fmt.Fprintf(&b, "- %s — %s\n", s.Name, s.Description)
	}
	b.WriteString("\nCompaction may drop these skills' instruction bodies. After the bullet list, " +
		"emit exactly one <skill-reload-selection> block containing a JSON object with a " +
		"\"reload_skills\" array of the exact names to reload after compaction (use [] to reload none).")
	return b.String()
}

// ElicitNote asks the model to enumerate the must-survive-verbatim details from
// the given history, for pinning as a note before a compaction (Variant B of the
// forced-note-at-compaction mechanism). The caller passes the prefix the
// compaction is about to fold (the recent preserved turns survive verbatim and
// need no rescuing). Returns the model's bullet list. Falls back across the
// configured summarization models on transient errors, like the summarizer.
// loaded carries the session's successfully loaded skills so the model can also
// choose which to reload after compaction.
func (cm *Manager) ElicitNote(ctx context.Context, history []schema.Turn, loaded []schema.SkillInventorySummary) (string, error) {
	if cm.client == nil {
		return "", errors.New("note elicitation requires an LLM client")
	}
	prof := cm.currentProfile()
	if len(summarizationModels(prof)) == 0 {
		return "", errors.New("no model available for note elicitation")
	}
	prompt := noteElicitationPrompt + loadedSkillsElicitSection(loaded) + "\n\n--- CONVERSATION SO FAR ---\n" + renderHistoryForElicit(history, noteElicitChars)
	resp, err := cm.completeSummarization(ctx, prof, prompt, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Text()), nil
}

// renderHistoryForElicit flattens turns into a plain-text transcript for the
// note-elicitation prompt, capped at maxChars and keeping the MOST RECENT content
// (it walks from the tail). Unlike the summarizer's rendering it includes
// tool-call arguments and FULL, untruncated tool-result content: the elicitor's
// whole job is to recover exact opaque values (tokens, IDs, hashes, paths) that
// in a coding agent live in tool output, so per-result truncation would defeat
// it — the maxChars budget is what bounds total size.
func renderHistoryForElicit(history []schema.Turn, maxChars int) string {
	var parts []string
	total := 0
	for _, h := range slices.Backward(history) {
		s := renderTurnForElicit(h)
		if s == "" {
			continue
		}
		if total+len(s) > maxChars {
			if rem := maxChars - total; rem > 0 {
				parts = append(parts, "[... earlier history truncated ...]\n"+string(runetrim.TrimLeadingPartial([]byte(s[len(s)-rem:]))))
			}
			break
		}
		parts = append(parts, s)
		total += len(s)
	}
	// parts were collected tail-first; reverse to chronological order.
	for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
		parts[l], parts[r] = parts[r], parts[l]
	}
	return strings.Join(parts, "")
}

// renderTurnForElicit renders one turn as labeled plain text, including tool-call
// arguments and tool-result content — the parts that carry the exact values the
// elicitor must preserve. Returns "" for turns with no textual content.
func renderTurnForElicit(t schema.Turn) string {
	if t.Kind == schema.TurnAttentionResolution {
		return ""
	}
	var b strings.Builder
	for _, p := range t.Message.Content {
		switch p.Kind {
		case llm.ContentText:
			if p.Text != "" {
				b.WriteString(p.Text)
				b.WriteString("\n")
			}
		case llm.ContentToolCall:
			if p.ToolCall != nil {
				fmt.Fprintf(&b, "Tool call %s(%s)\n", p.ToolCall.Name, strings.TrimSpace(string(p.ToolCall.Arguments)))
			}
		case llm.ContentToolResult:
			if p.ToolResult != nil {
				fmt.Fprintf(&b, "Tool result %s: %s\n", p.ToolResult.Name, fmt.Sprint(p.ToolResult.Content))
			}
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return ""
	}
	label := ""
	switch t.Kind {
	case schema.TurnUserInput:
		label = "User: "
	case schema.TurnAssistant:
		label = "Assistant: "
	case schema.TurnSteering, schema.TurnCheckpoint, schema.TurnSummary:
		label = "Context: "
	}
	return label + b.String()
}
