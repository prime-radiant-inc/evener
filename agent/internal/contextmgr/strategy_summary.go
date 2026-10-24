package contextmgr

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// strategyCheckpointAdditionalContext excludes carried originals. A cumulative
// log adds only bytes not already retained in a full canonical source record.
// Truncated detail, opaque or partially covered snapshots prove no coverage.
func strategyCheckpointAdditionalContext(originals []strategyHistoryRecord, checkpoint string) []strategyHistoryRecord {
	current, ok := parseStrategyCheckpoint(checkpoint)
	if !ok {
		// A deterministic fallback adds no independently generated context.
		return nil
	}
	text := current.modelText
	label := "Additional predicted context: "
	if current.isLogSnapshot {
		label = "Additional session log context: "
		for _, original := range originals {
			previous, ok := parseStrategyCheckpoint(original.text)
			if !ok || !previous.isLogSnapshot || previous.modelText == "" {
				continue
			}
			if before, after, covered := strings.Cut(text, previous.modelText); covered {
				text = before + after
			}
		}
	}
	if text == "" {
		return nil
	}
	return []strategyHistoryRecord{{label: label, text: text}}
}

// strategySummaryInputLimit sizes every batch for both the configured cheap
// route and session fallback, using the same reserve and reply room as the
// eligibility check. Unknown routes retain the existing bounded-text target.
func strategySummaryInputLimit(prof *provider.Profile) int {
	limit := 0
	if prof != nil {
		for _, route := range [][2]int{
			{prof.ContextWindowSize(), prof.MaxInputTokens()},
			{prof.CheapContextWindowSize(), prof.CheapMaxInputTokens()},
		} {
			if input := summarizerInputLimit(route[0], route[1]); input > 0 && (limit == 0 || input < limit) {
				limit = input
			}
		}
	}
	if limit == 0 {
		return 80_000 / 4
	}
	return limit
}

func strategySummaryPromptFits(text, instructions string, limit int) bool {
	req := llm.Request{Messages: []llm.Message{llm.User(buildSummaryPrompt(text, instructions))}}
	return llm.EstimateInputTokens(req).Tokens <= limit
}

// strategySummaryParts keeps oversized source records attributed in every
// overlapping part. The half-body target lets two parts share a bounded batch.
func strategySummaryParts(records []strategyHistoryRecord, instructions string, limit int) []strategyHistoryRecord {
	partSize := max((4*limit-len(buildSummaryPrompt("", instructions))-256)/2, 1)
	var parts []strategyHistoryRecord
	for _, record := range records {
		if strategySummaryPromptFits(renderStrategyHistoryRecords([]strategyHistoryRecord{record}), instructions, limit) {
			parts = append(parts, record)
			continue
		}
		chunks := splitMessage(record.text, partSize, 400)
		for i, chunk := range chunks {
			label := fmt.Sprintf("%s(part %d of %d of one record): ", record.label, i+1, len(chunks))
			parts = append(parts, strategyHistoryRecord{label: label, text: chunk})
		}
	}
	return parts
}

// condenseStrategyHistory uses ordinary summary validation for mixed sources,
// never the human-only batch prompt. Every full rendered request fits the
// selected routes before dispatch. Non-shrinking replies fail rather than cut
// source text; the caller then retains its checkpoint, as before.
func (cm *Manager) condenseStrategyHistory(ctx context.Context, prof *provider.Profile, records []strategyHistoryRecord, instructions string) (string, error) {
	const maxRounds = 4
	limit := strategySummaryInputLimit(prof)
	var route summarizationRoute
	var role string
	for round := 0; ; round++ {
		text := renderStrategyHistoryRecords(records)
		if strategySummaryPromptFits(text, instructions, limit) {
			return text, nil
		}
		if round == maxRounds {
			return "", summaryFailure(errSummaryUncondensable, route, role)
		}
		records = strategySummaryParts(records, instructions, limit)
		var summaries []strategyHistoryRecord
		for start := 0; start < len(records); {
			end := start
			// Find the same maximal fitting prefix without rebuilding every
			// growing prefix. Exponential bounds avoid rendering distant batches.
			for step := 1; end < len(records); step *= 2 {
				upper := min(start+step, len(records))
				if !strategySummaryPromptFits(renderStrategyHistoryRecords(records[start:upper]), instructions, limit) {
					lower := end
					end = lower + sort.Search(upper-lower, func(i int) bool {
						return !strategySummaryPromptFits(renderStrategyHistoryRecords(records[start:lower+i+1]), instructions, limit)
					})
					break
				}
				end = upper
			}
			if end == start {
				return "", summaryFailure(errSummaryUncondensable, route, role)
			}
			batch := renderStrategyHistoryRecords(records[start:end])
			resp, err := cm.completeSummarization(ctx, prof, buildSummaryPrompt(batch, instructions), func(reply llm.Response) error {
				if err := checkSummaryReply(reply.Text(), instructions); err != nil {
					return err
				}
				return checkNotCutOff(reply)
			})
			if err != nil {
				return "", err
			}
			route, role = resp.route, resp.role
			summaries = append(summaries, strategyHistoryRecord{label: "Summary of earlier conversation: ", text: resp.Text()})
			start = end
		}
		records = summaries
	}
}
