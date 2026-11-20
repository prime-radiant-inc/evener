package contextmgr

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/events"
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
		label, body, spans := renderElicitTurn(h)
		s := label + body
		if s == "" {
			continue
		}
		// The turn's ranges arrive in body coordinates, and label+body is the
		// copy this prompt carries.
		turnSpans := spansInTurn(label, spans)
		// Escaping never shrinks text, so a raw turn that already exceeds the
		// remaining budget skips full-turn escaping and clips directly.
		var escaped string
		if total+len(s) <= maxChars {
			escaped = escapeElicitTurn(label, body, spans)
		}
		if escaped == "" || total+len(escaped) > maxChars {
			if rem := maxChars - total; rem > 0 {
				clipped := string(runetrim.TrimLeadingPartial([]byte(s[len(s)-rem:])))
				// The clip keeps a suffix of the turn as written and escapes it
				// once, here, so no cut splits an escaped opener. A clip can drop
				// the opening delimiter of a fenced example the model wrote,
				// which exposes the frames its body quotes at line start: the
				// ranges the model wrote are intersected with the kept window, so
				// that exposure is defused, while an example the clip left whole
				// still carries its delimiters and keeps its bytes.
				clipped = escapeSpansInSlice(clipped, len(s)-len(clipped), turnSpans)
				parts = append(parts, "[... earlier history truncated ...]\n"+clipped)
			}
			break
		}
		parts = append(parts, escaped)
		total += len(escaped)
	}
	// parts were collected tail-first; reverse to chronological order.
	for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
		parts[l], parts[r] = parts[r], parts[l]
	}
	return strings.Join(parts, "")
}

// spansInTurn returns spans, which are in body's coordinates, moved into the
// coordinates of label+body.
func spansInTurn(label string, spans []textSpan) []textSpan {
	moved := make([]textSpan, 0, len(spans))
	for _, span := range spans {
		moved = append(moved, textSpan{start: len(label) + span.start, end: len(label) + span.end})
	}
	return moved
}

// escapeElicitTurn escapes the model-authored ranges of a full rendered turn.
func escapeElicitTurn(label, body string, spans []textSpan) string {
	return escapeSpansInSlice(label+body, 0, spansInTurn(label, spans))
}

// renderElicitTurn renders one turn as the label the elicitation prompt puts
// ahead of it, the body that follows as written, and the ranges of that body
// the model wrote, in the body's own coordinates. The caller escapes those
// ranges once, on the copy it keeps, so a clip that exposes a frame is defused
// without touching text the model did not write.
func renderElicitTurn(t schema.Turn) (label, body string, spans []textSpan) {
	if t.Kind == schema.TurnAttentionResolution {
		return "", "", nil
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
		return "", "", nil
	}
	raw := b.String()
	switch t.Kind {
	case schema.TurnUserInput:
		label = "User: "
	case schema.TurnAssistant:
		label = "Assistant: "
	case schema.TurnSteering, schema.TurnCheckpoint, schema.TurnSummary:
		label = "Context: "
	}
	// A compaction turn is a user-role message whose own layout names the text
	// the model wrote. A previous compaction's harness markers and the partner's
	// messages keep every byte, and the body the layout names is escaped by the
	// caller, so the elicitor cannot read a fabricated decision as context and
	// restate it in the note.
	compaction := t.Kind == schema.TurnCheckpoint || t.Kind == schema.TurnSummary
	modelAuthored := !compaction && isModelAuthoredTurn(t)
	if compaction {
		// The layout is read from the turn as stored, not from the copy rendered
		// here: every text part gets a line break appended below, and a summary's
		// recognition compares its whole text against the bytes the renderer
		// wrote, so the extra line would make a current-layout turn read as one
		// whose framing cannot be read. The spans it answers lie inside the
		// stored text, which is this copy's prefix; a turn whose parts this
		// renderer joined differently is classified from the copy itself.
		stored := t.Message.Text()
		if !strings.HasPrefix(raw, stored) {
			stored = raw
		}
		body, spans = raw, retainedCompactionModelSpans(stored, t.Kind)
	} else if modelAuthored {
		// The elicitor's prompt is a user-role message, and its renderer puts
		// the label only at the START of the turn. A model-authored turn whose
		// later lines open a reserved frame therefore copied those lines into
		// that user-role prompt verbatim, so the elicitor could read a
		// fabricated decision as its own context and restate it in the note.
		// The partner's own turns are the authority and the tool output is data,
		// neither of which this escape may rewrite.
		body, spans = raw, []textSpan{{start: 0, end: len(raw)}}
	} else {
		body = raw
	}
	// A model-authored turn's label takes a block of its own for the same reason
	// an assistant turn's does, and for one more: a body whose first line is an
	// indented code block is code only where no paragraph runs into it, and the
	// blank line after the label is what leaves the paragraph closed. Sharing the
	// line hides the opening delimiter of a fenced example from the scan, and the
	// example's real closing delimiter then opens a fence that spares the frame
	// below it. A compaction turn's label takes its own block for the same
	// reasons and one more: its layout is read from its opening marker, which has
	// to start a line for the escape to recognize the model's ranges at all.
	if label != "" && (compaction || modelAuthored) {
		label = strings.TrimSuffix(label, " ") + "\n\n"
	}
	return label, body, spans
}

// isModelAuthoredTurn reports whether a turn's text a model wrote, even though a
// request may carry it in a user-role channel: the assistant's reply, an input
// turn another agent wrote (a delegate's task from its parent), a steer from the
// parent agent or written by an agent, and a note handoff publishing the note
// this session's own model pinned before a compaction. Every surface that quotes
// a turn into its own user-role request asks this one question, so they agree on
// whose words they are. The human partner's own input and steer are the
// authority and answer false.
func isModelAuthoredTurn(t schema.Turn) bool {
	if t.Kind == schema.TurnAssistant || isAgentAuthored(t) || isParentSteering(t) {
		return true
	}
	return t.Kind == schema.TurnSteering && t.SteeringKind == events.SteeringKindNoteHandoff
}
