package contextmgr

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"primeradiant.com/evener/agent/internal/runetrim"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// partnerMessageEdgeChars is how much of each end of a human partner's
// message a middle trim keeps. A message no longer than both edges together is
// a short instruction and is never trimmed.
const partnerMessageEdgeChars = 1000

// fitCheckpointConversation fits conversation entries into budget without
// ever dropping the human partner's messages (#4281). It sheds the agent's own
// replies first, oldest first. If the partner's messages alone still exceed
// the budget, it trims the middle of the longest ones (pasted logs, say), each
// at most once and never below partnerMessageEdgeChars at each end, with a
// note saying where the full text is. Short instructions are never touched,
// nor is a message trimming would not shorten, nor any message when the
// session keeps no transcript to recover it from (recoverable false); when
// what is left still exceeds the budget, the budget yields.
//
// conversation must already be cleaned (cleanCheckpointConversation), so the
// rendered slice and the entries line up.
func fitCheckpointConversation(conversation []checkpointConversationEntry, budget int, recoverable bool) []checkpointConversationEntry {
	// The rendering is the section heading plus one block per entry, so its
	// length is the sum of the entries' block lengths and the heading's.
	blockLen := func(entry checkpointConversationEntry) int {
		return len(renderCheckpointConversation([]checkpointConversationEntry{entry})) - len(checkpointConversationHeading+"\n\n")
	}
	rendered := len(renderCheckpointConversation(conversation))
	// Shed the agent's own replies, oldest first, until the rest fits.
	kept := make([]checkpointConversationEntry, 0, len(conversation))
	for _, entry := range conversation {
		if rendered > budget && entry.Role != "user" {
			rendered -= blockLen(entry)
			continue
		}
		kept = append(kept, entry)
	}
	if rendered <= budget || !recoverable {
		return kept
	}
	// Only the partner's messages are left: trim the longest first, each once.
	long := make([]int, 0, len(kept))
	for i, entry := range kept {
		if len(entry.Text) > 2*partnerMessageEdgeChars {
			long = append(long, i)
		}
	}
	slices.SortStableFunc(long, func(a, b int) int { return len(kept[b].Text) - len(kept[a].Text) })
	for _, i := range long {
		if rendered <= budget {
			break
		}
		text := kept[i].Text
		// Keep what the budget allows, leaving room for the note.
		keep := max(2*partnerMessageEdgeChars, len(text)-(rendered-budget)-len(trimmedMessageNote(len(text))))
		trimmed := middleTrim(text, keep/2)
		if len(trimmed) >= len(text) {
			continue // the note would cost what the trim saves
		}
		before := blockLen(kept[i])
		kept[i].Text = trimmed
		rendered += blockLen(kept[i]) - before
	}
	return kept
}

// middleTrim keeps edge characters at each end of text, cut on rune
// boundaries, and puts the trimmed-message note between them.
func middleTrim(text string, edge int) string {
	if 2*edge >= len(text) {
		return text
	}
	head := runetrim.Cut(text, edge)
	tail := string(runetrim.TrimLeadingPartial([]byte(text[len(text)-edge:])))
	return head + trimmedMessageNote(len(text)-len(head)-len(tail)) + tail
}

// partnerMessagesMarkdown renders the human partner's messages from the turns
// a compaction folds (history[:cutoff]), verbatim, under the checkpoint's cap
// and shedding rules. A model-written compaction turn carries them beside its
// own text, so its permissions and holds stay in front of the model whatever
// the model wrote (#4173). It reads them through the checkpoint's own
// collector, so they come from raw user turns and from previous compaction
// turns alike.
//
// "Verbatim" means each message's text unchanged apart from surrounding
// whitespace, which cleanCheckpointConversation trims and the checkpoint's
// own extraction (parseMarkdownBlocks) trims again on every round trip.
// Fitting follows the checkpoint's rule (fitCheckpointConversation): no
// partner message is ever dropped; past the tighter of maxCheckpointChars and
// the ceiling the longest are middle-trimmed with a pointer to the transcript,
// and short ones are never touched.
//
// Only the partner's messages are carried. An earlier summary a folded
// checkpoint holds is input to the new summary, which supersedes it; carrying
// every earlier summary forward as well would grow each summary turn with
// every compaction. Checkpoints written by the opt-in session-log strategy
// keep no conversation section, so nothing is recovered from them here
// (#4181).
//
// Past ceiling characters (partnerCarryCeiling; 0 means none) the oldest
// messages are left out, newest kept, and returned as summarized: the
// summarizer is given each of them to summarize, quoting any permission or
// hold per the summary prompt, and the turn reminds the model where to read
// them verbatim. The newest message is held to the ceiling too: when it alone
// exceeds it, its middle is trimmed (transcript permitting), from the message
// as written, so the note counts what it lost rather than what an earlier trim
// had already taken.
// The ceiling applies with or without a kept transcript: it keeps the session
// inside the model's window, and the left-out messages are still summarized,
// every one (condensePartnerMessages), as the #4309 ruling chose. Without a
// transcript the reminder only says they were summarized.
func partnerMessagesMarkdown(history []schema.Turn, cutoff int, resultToolName string, recoverable bool, ceiling int) (markdown string, summarized []checkpointConversationEntry) {
	var partner []checkpointConversationEntry
	for _, entry := range cleanCheckpointConversation(collectCheckpointData(history, cutoff, resultToolName).conversation) {
		if entry.Role == "user" {
			partner = append(partner, entry)
		}
	}
	// The carry is chosen from the messages as written, newest first, and
	// only then fitted; the messages left to the summary go to it as written.
	kept := partner
	if ceiling > 0 {
		heading := len(checkpointConversationHeading + "\n\n")
		size, n := heading, 0
		for _, entry := range slices.Backward(partner) {
			block := len(renderCheckpointConversation([]checkpointConversationEntry{entry})) - heading
			if n > 0 && size+block > ceiling {
				break
			}
			size += block
			n++
		}
		if size > ceiling && !recoverable {
			// The newest alone overflows and cannot be trimmed: summarize it
			// with the rest.
			n = 0
		}
		summarized = partner[:len(partner)-n]
		kept = partner[len(partner)-n:]
	}
	// The carry is fitted to the tighter of the checkpoint's cap and the
	// ceiling, so a message both bounds would cut is trimmed once, by the bound
	// that binds, and the note it gets counts what the message as written lost.
	budget := maxCheckpointChars
	if ceiling > 0 {
		budget = min(budget, ceiling)
	}
	// The newest message as written, for the ceiling's trim below.
	newestAsWritten := ""
	if len(kept) > 0 {
		newestAsWritten = kept[len(kept)-1].Text
	}
	partner = fitCheckpointConversation(kept, budget, recoverable)
	if len(partner) == 0 {
		return "", summarized
	}
	if size := len(renderCheckpointConversation(partner)); ceiling > 0 && size > ceiling {
		// Only the newest can be over, and only when the fit stopped at its
		// floor for a message's kept edges rather than at the ceiling: trim its
		// middle to fit, note included. The cut comes from the message as
		// written, never from what the fit left of it, and the note's length is
		// the one that count has: cutting an already-cut text would cut the
		// middle of the note it wrote and leave a count that understates what
		// was removed.
		newest := &partner[len(partner)-1]
		edge := (len(newest.Text) - (size - ceiling) - len(trimmedMessageNote(len(newestAsWritten)))) / 2
		if trimmed := middleTrim(newestAsWritten, max(edge, 1)); len(trimmed) < len(newest.Text) {
			newest.Text = trimmed
		}
	}
	return renderCheckpointConversation(partner), summarized
}

// minSummarizingWindow is the smallest context window compaction summarizes
// on. A 50,000-token window leaves a summarizer request about 36,000 tokens
// of input (a quarter of the window for the reply, then the client's safety
// reserve), well above what a request carries: at most maxHistoryChars of
// history (~20,000 tokens) plus the summary instructions. Smaller windows are
// not worth sizing requests for; on them the checkpoint stands (#4321).
const minSummarizingWindow = 50_000

// summarizingWindowUsable reports whether every route a summary may take
// accepts at least the input a minSummarizingWindow-token window allows. Below
// that the deterministic checkpoint, which keeps the partner's messages and
// an earlier summary, stands instead of a summary. Unknown windows impose
// nothing.
func summarizingWindowUsable(prof *provider.Profile) bool {
	if prof == nil {
		return true
	}
	least := summarizerInputLimit(minSummarizingWindow, 0)
	// The session model and the configured cheap model it falls back from.
	for _, route := range [][2]int{
		{prof.ContextWindowSize(), prof.MaxInputTokens()},
		{prof.CheapContextWindowSize(), prof.CheapMaxInputTokens()},
	} {
		if input := summarizerInputLimit(route[0], route[1]); input > 0 && input < least {
			return false
		}
	}
	return true
}

// summarizerInputLimit is how many tokens of input a summarizer request may
// carry on a model with this window and input cap (0 for unknown): what the
// window leaves after a quarter for the reply, held to the input cap, less the
// safety reserve the client keeps on top of the input. It is 0 when neither
// is known.
func summarizerInputLimit(window, maxInput int) int {
	limit := 0
	if window > 0 {
		limit = window - window/4
	}
	if maxInput > 0 && (limit == 0 || maxInput < limit) {
		limit = maxInput
	}
	if limit == 0 {
		return 0
	}
	smallest := window
	if maxInput > 0 && (smallest == 0 || maxInput < smallest) {
		smallest = maxInput
	}
	return max(limit-llm.SafetyReserveTokens(smallest), 1)
}

// condensePartnerMessages fits the partner's messages a summary will not quote
// (each a line of text) into budget characters for the summarizer's input.
// When they overflow it, they are summarized in batches of at most budget
// characters, each batch summary quoting their permissions and holds, and the
// batch summaries are condensed the same way until they fit, so every message
// reaches a summarizer however many there are. If they will not shrink to fit,
// it fails rather than cut any short.
func (cm *Manager) condensePartnerMessages(ctx context.Context, prof *provider.Profile, lines []string, budget int) (string, error) {
	const maxRounds = 4 // each round shrinks the text; four reach any realistic volume
	var route summarizationRoute
	var role string
	for round := 0; ; round++ {
		total := 0
		for _, line := range lines {
			total += len(line)
		}
		if total <= budget {
			return strings.Join(lines, ""), nil
		}
		if round == maxRounds {
			break
		}
		var summaries []string
		for start := 0; start < len(lines); {
			end, size := start, 0
			for end < len(lines) && (end == start || size+len(lines[end]) <= budget) {
				size += len(lines[end])
				end++
			}
			resp, err := cm.completeSummarization(ctx, prof, partnerBatchPrompt+strings.Join(lines[start:end], ""), func(reply llm.Response) error {
				if strings.TrimSpace(reply.Text()) == "" {
					return errSummaryEmptyBatch
				}
				return checkNotCutOff(reply)
			})
			if err != nil {
				return "", err
			}
			route, role = resp.route, resp.role
			// A long summary is split like a long message, so the next round's
			// batches stay within budget.
			parts := splitMessage(strings.TrimSpace(resp.Text()), budget/3, 400)
			for i, part := range parts {
				label := "Summary of earlier messages: "
				if len(parts) > 1 {
					label = fmt.Sprintf("Summary of earlier messages (part %d of %d): ", i+1, len(parts))
				}
				summaries = append(summaries, label+part+"\n")
			}
			start = end
		}
		lines = summaries
	}
	// Summaries that will not shrink to fit are never cut short: failing here
	// fails the summary, and the checkpoint, which keeps every partner
	// message, stands.
	return "", summaryFailure(errSummaryUncondensable, route, role)
}

// splitMessage splits text into parts of at most size bytes, each cut at the
// last paragraph, line or sentence break before the limit when there is one,
// else on a rune boundary, and each repeating the last overlap bytes of the
// part before it, so an instruction that crosses a cut is whole in one part.
func splitMessage(text string, size, overlap int) []string {
	var parts []string
	for len(text) > size {
		part := runetrim.Cut(text, size)
		if part == "" {
			part = text[:size] // not valid UTF-8 at the cut; keep every byte
		}
		for _, br := range []string{"\n\n", "\n", ". "} {
			if i := strings.LastIndex(part, br); i > size/2 {
				part = part[:i+len(br)]
				break
			}
		}
		parts = append(parts, part)
		next := max(len(part)-overlap, 1)
		for next < len(part) && !utf8.RuneStart(text[next]) {
			next++
		}
		text = text[next:]
	}
	return append(parts, text)
}

// partnerBatchPrompt asks the summarizer to condense a batch of the human
// partner's messages that a compaction will not quote.
const partnerBatchPrompt = `You are condensing messages from your human partner that a context compaction will not quote. Summarize what they asked for and told you, as briefly as keeps it usable. ` + partnerAuthorityRule + `

`

// partnerCarryCeiling bounds the partner's messages a model-written
// compaction turn carries verbatim to a quarter of the model's context window,
// in characters at the char/4 token estimate the context manager uses
// elsewhere: a quarter of W tokens is W characters. Compaction starts at 80% of
// the window; a carry of at most a quarter leaves the rest for the system
// prompt and tools, the model's own summary, the preserved recent turns and the
// work until the next compaction, so the carry alone can never push a compacted
// context back over the threshold. It is 0, no ceiling, only when there is no
// profile to read a window from; an unknown window is assumed like the rest of
// the fold (issue #4564), so the assumption bounds the carry too.
func partnerCarryCeiling(prof *provider.Profile) int {
	cw, _ := effectiveWindowOf(prof)
	return cw
}

// partnerCarry renders the partner's messages a model-written compaction turn
// carries (from history[:cutoff]), held to ceiling (0 for none), the reminder
// its opening adds when some are left to the summary instead, and those
// messages, which the summarizer must be given.
func (cm *Manager) partnerCarry(ctx context.Context, history []schema.Turn, cutoff, ceiling int) (reminder, carry string, summarized []checkpointConversationEntry) {
	meta := cm.metaFor(ctx)
	carry, summarized = partnerMessagesMarkdown(history, cutoff, cm.resultToolName(), meta.hasTranscript(), ceiling)
	if len(summarized) > 0 {
		reminder = partnerCarryReminder(len(summarized), meta)
	}
	return reminder, carry, summarized
}
