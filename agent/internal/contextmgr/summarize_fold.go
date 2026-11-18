package contextmgr

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/internal/runetrim"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// foldCutoff is where a compaction of history that keeps preserveRecent
// recent turns cuts it: history[:cutoff] is folded. It is -1 when no safe cut
// exists. The checkpoint and the summarizer both cut with it.
func foldCutoff(history []schema.Turn, preserveRecent int) int {
	return safeCutoff(history, attentionTransparentRecentCutoff(history, preserveRecent))
}

// preservePendingAskTail keeps the existing source records and every later
// boundary needed to restore an unresolved question. Older history remains
// foldable. Once admission clears the pending owner, the ordinary cutoff wins.
func preservePendingAskTail(history []schema.Turn, preserveRecent int, meta *CompactionMeta) int {
	if meta != nil && (len(meta.PendingAskTurnIDs) > 0 || len(meta.PendingAskCallIDs) > 0) {
		for i, turn := range history {
			if turn.TurnID != "" && slices.Contains(meta.PendingAskTurnIDs, turn.TurnID) {
				return max(preserveRecent, attentionTransparentTurnCount(history[i:]))
			}
			for _, part := range turn.Message.Content {
				if call := part.ToolCall; call != nil && call.ID != "" && slices.Contains(meta.PendingAskCallIDs, call.ID) {
					return max(preserveRecent, attentionTransparentTurnCount(history[i:]))
				}
			}
		}
	}
	return preserveRecent
}

// summaryAccept is the conversation summary's reply validation, shared with
// the retry tests so they exercise the production rule: a summary section
// with content that was not cut off at the model's output limit. The cut-off
// check decides first, so a reply the model truncated — even one that came
// back empty or section-less — is an output-limit rejection, which no route
// resamples, and not a resample-worthy bad sample.
func summaryAccept(instructions string) func(llm.Response) error {
	return func(reply llm.Response) error {
		if err := checkNotCutOff(reply); err != nil {
			return err
		}
		return checkSummaryReply(reply.Text(), instructions)
	}
}

// summarizeFold calls the cheap model to summarize old history, replacing it
// with a single user message that holds the summary and preserving the most
// recent turns. A non-empty instructions replaces the default
// mandatory-sections prompt. For a summary that completes a fold a checkpoint
// began this pass, checkpointed is what that checkpoint, at history[0],
// folded: the checkpoint sheds conversation under its own budget,
// which earlier summaries and agent replies share, so the partner's messages
// the summary carries are read from those turns, plus every later turn the
// summary folds, under the carry's own cap. With checkpointed nil they are
// read from the turns the summary folds.
func (cm *Manager) summarizeFold(ctx context.Context, history []schema.Turn, preserveRecent int, instructions string, checkpointed []schema.Turn) ([]schema.Turn, error) {
	preserveRecent = preservePendingAskTail(history, preserveRecent, cm.metaFor(ctx))
	if attentionTransparentTurnCount(history) <= preserveRecent {
		return history, nil
	}
	cutoff := foldCutoff(history, preserveRecent)
	if cutoff < 0 {
		return history, nil
	}

	// One profile serves the whole fold: the window check and every request
	// it sends, so a model switch mid-fold cannot redirect its requests to a
	// model the check never saw.
	sumProfile := cm.currentProfile()
	if !summarizingWindowUsable(sumProfile) {
		// The checkpoint already at history[0] stands in place of a summary.
		return history, nil
	}

	// Cap the history text: ~80k chars ≈ 20k tokens, which with the
	// instruction prefix and the reply fits every route summarizingWindowUsable
	// admits.
	const maxHistoryChars = 80_000

	// The checkpoint at history[0] stands for checkpointed; the summary also
	// folds whatever follows it up to cutoff, including turns the pre-compact
	// hook pushed out of the preserved tail.
	partnerSource := history[:cutoff]
	if checkpointed != nil {
		partnerSource = append(slices.Clip(checkpointed), history[1:cutoff]...)
	}
	reminder, carry, summarized := cm.partnerCarry(ctx, partnerSource, len(partnerSource), partnerCarryCeiling(sumProfile))

	var b strings.Builder
	// The partner's messages the turn will not quote come first, so the
	// summarizer reads every one of them whatever else the budget cuts; past
	// half the history budget they arrive condensed in bounded batches
	// (condensePartnerMessages).
	if len(summarized) > 0 {
		// Each message goes in whole; one longer than a part is split into
		// overlapping, numbered parts rather than cut short, small enough that
		// two share a batch.
		var lines []string
		for _, entry := range summarized {
			parts := splitMessage(entry.Text, maxHistoryChars/6, 400)
			for i, part := range parts {
				label := "User: "
				if len(parts) > 1 {
					label = fmt.Sprintf("User (part %d of %d of one message): ", i+1, len(parts))
				}
				lines = append(lines, label+part+"\n")
			}
		}
		condensed, err := cm.condensePartnerMessages(ctx, sumProfile, lines, maxHistoryChars/2)
		if err != nil {
			return nil, err
		}
		b.WriteString("Your human partner's messages that the handoff will not quote, oldest first; summarize them:\n")
		b.WriteString(condensed)
		b.WriteString("\nThe conversation:\n")
	}
historyLoop:
	for i := range cutoff {
		t := history[i]
		switch t.Kind {
		case schema.TurnUserInput:
			// Preserve user messages verbatim. Cap at 5k to protect the cheap
			// model's context — normal messages are well under this. On the
			// MaybeCompact and ForceCompact paths the checkpoint layer has
			// already folded these turns, so their user messages arrive inside
			// the checkpoint (a previous compaction) instead of here.
			b.WriteString("User: ")
			b.WriteString(truncate(t.Message.Text(), 5000))
			b.WriteString("\n")
		case schema.TurnCheckpoint, schema.TurnSummary:
			// A previous compaction is the only record of the conversation it
			// folded. It gets up to a checkpoint's size, so a deterministic
			// checkpoint (earlier summaries first, then the newest folded
			// conversation) always arrives whole; the rest of the budget stays
			// for the turns it precedes. A longer one keeps its head, where
			// both summary prompts put the permissions and holds it quotes.
			// Left-out partner messages written first take their share of
			// the budget from it.
			b.WriteString("Previous compaction: ")
			b.WriteString(truncate(t.Message.Text(), min(maxCheckpointChars, max(maxHistoryChars-b.Len(), 0))))
			b.WriteString("\n")
		case schema.TurnAssistant:
			// Extract communicate calls so the summarizer sees how the agent
			// talked to the user, while still distinguishing questions from
			// non-await replies.
			text := t.Message.Text()
			if text != "" {
				b.WriteString("Assistant: ")
				b.WriteString(truncate(text, 500))
				b.WriteString("\n")
			}
			truncated := false
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
					if p.ToolCall.Name == cm.resultToolName() {
						endTurn, msg := parseCommunicateArgs(p.ToolCall.Arguments)
						if msg == "" {
							continue
						}
						var entry string
						switch {
						case endTurn:
							entry = fmt.Sprintf("Agent Message: %s\n", msg)
						default:
							entry = fmt.Sprintf("Agent Status: %s\n", msg)
						}
						if b.Len()+len(entry) > maxHistoryChars {
							b.WriteString("\n[... truncated ...]\n")
							truncated = true
							break
						}
						b.WriteString(entry)
					} else if instructions != "" {
						entry := fmt.Sprintf("Tool Call: %s(%s)\n", p.ToolCall.Name, truncate(string(p.ToolCall.Arguments), 500))
						if b.Len()+len(entry) > maxHistoryChars {
							b.WriteString("\n[... truncated ...]\n")
							truncated = true
							break
						}
						b.WriteString(entry)
					}
				}
			}
			if truncated {
				break historyLoop
			}
		case schema.TurnTool, schema.TurnToolResults:
			truncated := false
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					entry := fmt.Sprintf("Tool(%s): %s\n", p.ToolResult.Name, formatToolResult(fmt.Sprint(p.ToolResult.Content), instructions))
					if b.Len()+len(entry) > maxHistoryChars {
						b.WriteString("\n[... truncated ...]\n")
						truncated = true
						break
					}
					b.WriteString(entry)
				}
			}
			if truncated {
				break historyLoop
			}
		case schema.TurnSteering:
			// Labeled for the summary prompt's quoting rule.
			if isPartnerSteering(t) {
				b.WriteString("User: ")
			} else {
				b.WriteString("System: ")
			}
			b.WriteString(t.Message.Text())
			b.WriteString("\n")
		}
		// The cap is checked after each turn, so the history can run past it
		// by the last turn written. Every turn kind but steering has its own
		// cap (a previous compaction's is maxCheckpointChars), which
		// bounds that overrun; it is a soft limit protecting the summarizer's
		// context, not a hard one.
		if b.Len() > maxHistoryChars {
			b.WriteString("\n[... truncated ...]\n")
			break
		}
	}

	// When compaction instructions are supplied and the fold began with a
	// deterministic checkpoint, extract tool calls and bounded tool results
	// from checkpointed newest-first into any remaining budget. Older tool
	// results yield to newer ones, while the checkpoint baseline above
	// guarantees that all file-write and partner-conversation metadata is
	// preserved regardless of tool output volume.
	if instructions != "" && len(checkpointed) > 0 && b.Len() < maxHistoryChars {
		var excerpts []string
		excerptsLen := 0
		const header = "\nFolded tool excerpts:\n"
		remaining := maxHistoryChars - b.Len() - len(header)
		if remaining > 0 {
		collect:
			for _, t := range slices.Backward(checkpointed) {
				switch t.Kind {
				case schema.TurnTool, schema.TurnToolResults:
					for _, p := range slices.Backward(t.Message.Content) {
						if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
							entry := fmt.Sprintf("Tool(%s): %s\n", p.ToolResult.Name, formatToolResult(fmt.Sprint(p.ToolResult.Content), instructions))
							if excerptsLen+len(entry) > remaining {
								break collect
							}
							excerpts = append(excerpts, entry)
							excerptsLen += len(entry)
						}
					}
				case schema.TurnAssistant:
					for _, p := range slices.Backward(t.Message.Content) {
						if p.Kind == llm.ContentToolCall && p.ToolCall != nil && p.ToolCall.Name != cm.resultToolName() {
							entry := fmt.Sprintf("Tool Call: %s(%s)\n", p.ToolCall.Name, truncate(string(p.ToolCall.Arguments), 500))
							if excerptsLen+len(entry) > remaining {
								break collect
							}
							excerpts = append(excerpts, entry)
							excerptsLen += len(entry)
						}
					}
				}
			}
			if len(excerpts) > 0 {
				slices.Reverse(excerpts)
				b.WriteString(header)
				for _, e := range excerpts {
					b.WriteString(e)
				}
			}
		}
	}

	prompt := buildSummaryPrompt(b.String(), instructions)

	resp, err := cm.completeSummarization(ctx, sumProfile, prompt, summaryAccept(instructions))
	if err != nil {
		return nil, err
	}

	// The turn is the model's reply, bounded by the provider's output limit,
	// plus the partner's messages, bounded by maxCheckpointChars; the preserved
	// recent turns sit outside it. One combined budget would mean truncating
	// the reply or shedding partner messages below the cap the #4173 ruling
	// chose for them.
	summaryText := summaryTurnStart + cm.metaFor(ctx).compactionPointer("summary") + reminder +
		renderCompactionModelText(summaryModelTextHeading, resp.Text()) + carry + summaryMarkerEnd
	summaryTurn := schema.NewTurn(schema.TurnSummary, llm.User(summaryText))

	result := make([]schema.Turn, 0, 1+preserveRecent)
	result = append(result, summaryTurn)
	result = append(result, history[cutoff:]...)
	return result, nil
}

// safeCutoff adjusts a cutoff index so the preserved turns don't start with a
// TurnTool or TurnSteering, which would produce invalid message ordering.
// While tracing a tool result back to its assistant tool call, the scan crosses
// presentational markers and steering turns persisted inside that exchange; a
// standalone marker remains a valid cutoff position unless the lookahead also
// crosses steering. TurnTool without a preceding assistant tool_call is invalid.
// TurnSteering after a checkpoint/summary (both user-role) produces consecutive
// user messages that some APIs reject.
// Returns -1 if no safe position exists; callers should skip compaction.
func safeCutoff(history []schema.Turn, cutoff int) int {
	tracingToolResult := false
	crossedSteering := false
	for i := cutoff; i < len(history); i++ {
		k := history[i].Kind
		if k == schema.TurnHookCompleted || k == schema.TurnAttentionResolution {
			continue
		}
		if k == schema.TurnSteering {
			crossedSteering = true
			continue
		}
		tracingToolResult = k == schema.TurnTool || k == schema.TurnToolResults
		break
	}

	for cutoff > 0 && cutoff < len(history) {
		k := history[cutoff].Kind
		if k == schema.TurnTool || k == schema.TurnToolResults {
			tracingToolResult = true
			cutoff--
			continue
		}
		if k == schema.TurnSteering || ((k == schema.TurnHookCompleted || k == schema.TurnAttentionResolution) && (tracingToolResult || crossedSteering)) {
			cutoff--
			continue
		}
		break
	}
	if cutoff <= 0 {
		return -1
	}
	return cutoff
}

// formatToolResult formats a tool result content string for summarization.
// When instructions are empty, it uses the conservative 200-char truncation.
// When instructions are non-empty, it preserves up to 4,000 chars using a
// UTF-8-safe head+tail excerpt so errors and discrepancies near the end of
// multi-line tool outputs survive into the prompt.
func formatToolResult(content string, instructions string) string {
	if instructions == "" {
		return truncate(content, 200)
	}
	const maxChars = 4_000
	const edge = 2_000
	if len(content) <= maxChars {
		return content
	}
	head := runetrim.Cut(content, edge)
	tail := string(runetrim.TrimLeadingPartial([]byte(content[len(content)-edge:])))
	return head + "\n...[truncated]...\n" + tail
}
