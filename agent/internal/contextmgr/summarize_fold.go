package contextmgr

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// foldCutoff is where a compaction of history that keeps preserveRecent
// recent turns cuts it: history[:cutoff] is folded. It is -1 when no safe cut
// exists. The checkpoint and the summarizer both cut with it.
func foldCutoff(history []schema.Turn, preserveRecent int) int {
	return safeCutoff(history, attentionTransparentRecentCutoff(history, preserveRecent))
}

// summarizeWithLLM calls the cheap model to generate a narrative summary of
// old history. Replaces old history with a single user message containing the
// summary, preserving the most recent turns.
func (cm *Manager) summarizeWithLLM(ctx context.Context, history []schema.Turn, preserveRecent int) ([]schema.Turn, error) {
	return cm.summarizeWithLLMSteered(ctx, history, preserveRecent, "")
}

// summarizeWithLLMSteered is like summarizeWithLLM but accepts optional caller
// instructions that replace the default mandatory-sections prompt when non-empty.
func (cm *Manager) summarizeWithLLMSteered(ctx context.Context, history []schema.Turn, preserveRecent int, instructions string) ([]schema.Turn, error) {
	return cm.summarizeFold(ctx, history, preserveRecent, instructions, nil)
}

// summarizeFold is summarizeWithLLMSteered for a summary that completes a
// fold a checkpoint began this pass. checkpointed is what that checkpoint, at
// history[0], folded: the checkpoint sheds conversation under its own budget,
// which earlier summaries and agent replies share, so the partner's messages
// the summary carries are read from those turns, plus every later turn the
// summary folds, under the carry's own cap. With checkpointed nil they are
// read from the turns the summary folds.
func (cm *Manager) summarizeFold(ctx context.Context, history []schema.Turn, preserveRecent int, instructions string, checkpointed []schema.Turn) ([]schema.Turn, error) {
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
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil && p.ToolCall.Name == cm.resultToolName() {
					endTurn, msg := parseCommunicateArgs(p.ToolCall.Arguments)
					if msg == "" {
						continue
					}
					switch {
					case endTurn:
						fmt.Fprintf(&b, "Agent Message: %s\n", msg)
					default:
						fmt.Fprintf(&b, "Agent Status: %s\n", msg)
					}
				}
			}
		case schema.TurnTool, schema.TurnToolResults:
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					fmt.Fprintf(&b, "Tool(%s): %s\n", p.ToolResult.Name, truncate(fmt.Sprint(p.ToolResult.Content), 200))
				}
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

	prompt := buildSummaryPrompt(b.String(), instructions)

	resp, err := cm.completeSummarization(ctx, sumProfile, prompt, func(reply llm.Response) error {
		if err := checkSummaryReply(reply.Text(), instructions); err != nil {
			return err
		}
		return checkNotCutOff(reply)
	})
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
