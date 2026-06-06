package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// readOutlineEnvelope is the wire shape returned for an outline read.
// turns_total is the authoritative entry count (the same number range accepts).
// truncated/elided_turns report the budget elision honestly.
// This is a flat envelope (no nested meta) per spec §"format: outline".
type readOutlineEnvelope struct {
	TranscriptRef string `json:"transcript_ref"`
	Format        string `json:"format"`
	TurnsTotal    int    `json:"turns_total"`
	Content       string `json:"content"`
	Truncated     bool   `json:"truncated"`
	ElidedTurns   int    `json:"elided_turns"`
	Hint          string `json:"hint"`
}

// outlineHint is the single top-level next-call hint on an outline response.
// Turn numbers shown in the outline are the same numbers range and expand_turn
// accept — no translation step.
const outlineHint = "turn numbers here are what range and expand_turn accept: range=\"7-20\" or range=\"last:40\" to zoom in; expand_turn=N (markdown only) to expand a single turn's results"

// subagentRefInfo is the audit-pivot extract of a subagent lifecycle tool result:
// the child's success/status and its transcript_ref, so the parent can pivot to
// the child without reading the full result body. Extracted by
// extractSubagentResult; transcript rendering uses decodeSubagentResult directly
// for full result-body access.
type subagentRefInfo struct {
	success       bool
	status        string
	transcriptRef string
}

// subagentLifecycleTools are the tool names whose results carry a subagentResult
// body (status/output/success/turns_used/transcript_ref). Their outline lines are
// special-cased to surface the child ref.
var subagentLifecycleTools = map[string]bool{
	"spawn_agent":  true,
	"wait":         true,
	"resume_agent": true,
	"close_agent":  true,
}

// decodeSubagentResult is the single decode path for a subagent lifecycle tool
// result body (JSON subagentResult). It reports false when the body is empty or
// does not parse as JSON, so callers can fall back to ordinary rendering. The
// outline (extractSubagentResult) projects this to its success/status/ref subset;
// the markdown renderer uses the full struct (output, turns_used) — one decode,
// no duplication.
func decodeSubagentResult(body string) (subagentResult, bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return subagentResult{}, false
	}
	var r subagentResult
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return subagentResult{}, false
	}
	return r, true
}

// extractSubagentResult parses a subagent lifecycle tool result body and returns
// its success/status/transcript_ref subset for the outline line. It reports false
// when the body does not decode as a subagentResult, so callers fall back to the
// ordinary result-size rendering.
func extractSubagentResult(body string) (subagentRefInfo, bool) {
	r, ok := decodeSubagentResult(body)
	if !ok {
		return subagentRefInfo{}, false
	}
	return subagentRefInfo{
		success:       r.Success,
		status:        string(r.Status),
		transcriptRef: r.TranscriptRef,
	}, true
}

// renderOutline builds the per-session outline content and its honest elision
// counts. start and end are inclusive bounds over the entry-list index
// (entries[i] → absolute turn number i), matching the numbers range accepts.
// Pass start=0, end=len(entries)-1 for the whole session.
//
// The result index is built over ALL entries so call→result pairing works for
// lifecycle brackets even when the result turn is outside [start, end].
// TOOL_RESULTS turns fold under their owning ASSISTANT turn (no standalone line),
// exactly as the markdown renderer does. The result is bounded by convBudgetChars:
// when the filtered lines exceed it, head + tail are kept and the middle is dropped
// with an honest "… N turns elided …" marker, so the output is never an unbounded wall.
func renderOutline(entries []transcript.Entry, start, end int) (content string, truncated bool, elidedTurns int) {
	idx := buildResultIndex(entries, 0)

	var lines []string
	for i := range entries {
		if i < start || i > end {
			continue // outside requested range
		}
		line, ok := outlineLine(entries, i, &idx)
		if !ok {
			continue // TOOL_RESULTS folds under its owning assistant turn
		}
		lines = append(lines, line)
	}

	return boundOutline(lines)
}

// outlineLine builds the single outline line for entries[seq], or reports false
// when the entry gets no standalone line (TOOL_RESULTS folds under its owning
// ASSISTANT turn). The line is dot-separated:
//
//	<seq> · <Role> · <tool names in call order> · <purpose/first-line> · <status> · <result size> [truncated]
//
// Empty segments are dropped, so a plain user turn is just "<seq> · User · <text>".
func outlineLine(entries []transcript.Entry, seq int, idx *resultIndex) (string, bool) {
	t := entries[seq].Turn
	if t.Kind == schema.TurnToolResults {
		return "", false
	}

	role := outlineRoleLabel(t.Kind)
	segs := []string{strconv.Itoa(seq), role}

	if t.Kind == schema.TurnAssistant {
		segs = append(segs, assistantOutlineSegments(t, idx)...)
		return strings.Join(joinNonEmpty(segs), " · "), true
	}

	// Non-assistant turns: just the first line of their text, if any.
	if text := firstLineClamp(turnPlainText(t), 100); text != "" {
		segs = append(segs, text)
	}
	return strings.Join(joinNonEmpty(segs), " · "), true
}

// assistantOutlineSegments builds the assistant-specific trailing segments of an
// outline line: tool names (in call order), a short purpose/first-line, the
// aggregated tool status, and the result-size note. When the turn contains one or
// more subagent lifecycle calls, each lifecycle call's success/status/child=<ref>
// is surfaced as a compact bracket segment instead of a result-size note, so the
// parent→child audit pivot works even when multiple agents are waited on in
// parallel (the common spawn-then-wait pattern).
func assistantOutlineSegments(t schema.Turn, idx *resultIndex) []string {
	calls := toolCallsInOrder(t)

	if len(calls) == 0 {
		// Pure text/thinking assistant turn (e.g. the result tool, or reasoning).
		if text := firstLineClamp(turnPlainText(t), 100); text != "" {
			return []string{text}
		}
		return nil
	}

	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.Name
	}
	segs := []string{strings.Join(names, ", ")}

	// Subagent lifecycle turn: surface one audit-pivot bracket per lifecycle call.
	// This handles both the single-call case and the parallel case (multiple
	// wait/spawn_agent/resume_agent/close_agent calls in one round).
	if brackets := subagentLifecycleBrackets(calls, idx); len(brackets) > 0 {
		segs = append(segs, brackets...)
		return segs
	}

	if purpose := firstLineClamp(turnPlainText(t), 80); purpose != "" {
		segs = append(segs, purpose)
	}
	segs = append(segs, callStatus(calls, idx), resultSizeNote(calls, idx))
	return segs
}

// toolCallsInOrder returns the tool calls of an assistant turn in recorded
// (call) order.
func toolCallsInOrder(t schema.Turn) []*llm.ToolCallData {
	var calls []*llm.ToolCallData
	for i := range t.Message.Content {
		p := &t.Message.Content[i]
		if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
			calls = append(calls, p.ToolCall)
		}
	}
	return calls
}

// subagentLifecycleBrackets returns one compact bracket string per subagent
// lifecycle call in calls (in call order), each of the form
// "wait[success=true status=completed child=local:X]". It returns nil when no
// call in the turn is a lifecycle tool, so the caller can fall back to the
// ordinary result-size rendering. Mixed turns (lifecycle + non-lifecycle calls)
// emit brackets only for the lifecycle calls; non-lifecycle calls keep their
// names in the already-built names segment and their results count toward the
// generic size path — but in practice the parallel subagent pattern is
// lifecycle-only per round, so mixed rounds are not expected.
func subagentLifecycleBrackets(calls []*llm.ToolCallData, idx *resultIndex) []string {
	var brackets []string
	for _, c := range calls {
		if !subagentLifecycleTools[c.Name] {
			continue
		}
		paired, ok := idx.byCallID[c.ID]
		if !ok {
			continue
		}
		info, ok := extractSubagentResult(fmt.Sprint(paired.result.Content))
		if !ok {
			continue
		}
		child := info.transcriptRef
		if child == "" {
			child = "(none)"
		}
		brackets = append(brackets, fmt.Sprintf("%s[success=%t status=%s child=%s]", c.Name, info.success, info.status, child))
	}
	return brackets
}

// callStatus aggregates the status of a turn's tool calls into one label: "error"
// if any paired result is an error, "ok" if all calls have non-error results, and
// "pending" if any call has no result yet.
func callStatus(calls []*llm.ToolCallData, idx *resultIndex) string {
	anyPending := false
	for _, c := range calls {
		paired, ok := idx.byCallID[c.ID]
		if !ok {
			anyPending = true
			continue
		}
		if paired.result.IsError {
			return "error"
		}
	}
	if anyPending {
		return "pending"
	}
	return "ok"
}

// resultSizeNote summarizes the size of a turn's tool results as "<N> lines"
// (summed non-empty result lines across the round), with a trailing "[truncated]"
// when any result would be head+tail truncated or width-clamped by the markdown
// renderer (more than resultBodyWholeMax non-empty lines, or any line wider than
// resultLineMaxRunes). Returns "" when no results are paired yet.
func resultSizeNote(calls []*llm.ToolCallData, idx *resultIndex) string {
	total := 0
	anyTruncated := false
	anyResult := false
	for _, c := range calls {
		paired, ok := idx.byCallID[c.ID]
		if !ok {
			continue
		}
		anyResult = true
		content := fmt.Sprint(paired.result.Content)
		n := len(nonEmptyLines(content))
		total += n
		if n > resultBodyWholeMax || anyLineWiderThan(content, resultLineMaxRunes) {
			anyTruncated = true
		}
	}
	if !anyResult {
		return ""
	}
	note := fmt.Sprintf("%d lines", total)
	if anyTruncated {
		note += " [truncated]"
	}
	return note
}

// anyLineWiderThan reports whether any non-empty line of s exceeds limit runes.
func anyLineWiderThan(s string, limit int) bool {
	for _, line := range nonEmptyLines(s) {
		if len([]rune(line)) > limit {
			return true
		}
	}
	return false
}

// outlineRoleLabel maps a turn kind to its capitalized outline role label,
// matching the markdown renderer's headings (User/Assistant/Steering/…).
func outlineRoleLabel(kind schema.TurnKind) string {
	switch kind {
	case schema.TurnUserInput:
		return "User"
	case schema.TurnAssistant:
		return "Assistant"
	case schema.TurnSteering:
		return "Steering"
	case schema.TurnSummary:
		return "Summary"
	case schema.TurnCheckpoint:
		return "Checkpoint"
	case schema.TurnSystem:
		return "System"
	case schema.TurnToolResults, schema.TurnTool:
		return "ToolResults"
	default:
		return string(kind)
	}
}

// turnPlainText returns the concatenated text content of a turn (assistant/user
// text and thinking), used for the short purpose/first-line outline segment.
func turnPlainText(t schema.Turn) string {
	var parts []string
	for i := range t.Message.Content {
		p := &t.Message.Content[i]
		switch p.Kind {
		case llm.ContentText:
			if p.Text != "" {
				parts = append(parts, p.Text)
			}
		case llm.ContentThinking:
			if p.Thinking != nil && p.Thinking.Text != "" {
				parts = append(parts, p.Thinking.Text)
			}
		}
	}
	return strings.Join(parts, " ")
}

// joinNonEmpty drops empty strings from segs, preserving order.
func joinNonEmpty(segs []string) []string {
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// boundOutline joins outline lines into a single text block bounded by
// convBudgetChars. When the joined lines fit, it returns them whole. Otherwise it
// keeps a head and a tail of lines and drops the middle, splicing an honest
// "… N turns elided — read a range to see them …" marker where the drop occurred,
// and returns truncated=true with the dropped-line count. The kept head/tail are
// sized so the final content (including the marker) stays within the budget.
func boundOutline(lines []string) (content string, truncated bool, elidedTurns int) {
	full := strings.Join(lines, "\n")
	if len([]rune(full)) <= convBudgetChars {
		return full, false, 0
	}

	// Budget the kept lines: grow head and tail alternately until adding the next
	// line would push the rendered block (head + marker + tail) over the budget.
	n := len(lines)
	head, tail := 0, 0
	used := 0
	// Reserve room for the marker; its exact elided count is known only at the end,
	// so reserve a generous fixed width that bounds the marker length.
	const markerReserve = 80
	budget := convBudgetChars - markerReserve
	if budget < 0 {
		budget = 0
	}

	for head+tail < n {
		// Prefer growing the head, then the tail, alternately, so both ends survive.
		grewThisPass := false
		if head <= tail && head < n-tail {
			cost := len([]rune(lines[head])) + 1 // +1 for newline
			if used+cost > budget {
				break
			}
			used += cost
			head++
			grewThisPass = true
		}
		if tail < n-head {
			cost := len([]rune(lines[n-1-tail])) + 1
			if used+cost > budget {
				break
			}
			used += cost
			tail++
			grewThisPass = true
		}
		if !grewThisPass {
			break
		}
	}

	elided := n - head - tail
	if elided <= 0 {
		// Everything fit after all (only possible if markerReserve over-reserved);
		// return the whole block.
		return full, false, 0
	}

	var b strings.Builder
	for i := 0; i < head; i++ {
		b.WriteString(lines[i])
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "… %d turns elided — read a range to see them …\n", elided)
	for i := n - tail; i < n; i++ {
		b.WriteString(lines[i])
		if i < n-1 {
			b.WriteByte('\n')
		}
	}
	return b.String(), true, elided
}
