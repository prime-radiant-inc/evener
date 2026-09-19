package agent

import (
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

type managedHistoryPair struct {
	toolIndex int
	part      llm.ContentPart
}
type managedHistoryPairs []managedHistoryPair

func (pairs managedHistoryPairs) hasToolIndex(index int) bool {
	for _, pair := range pairs {
		if pair.toolIndex == index {
			return true
		}
	}
	return false
}

// managedResultPairing locates only explicitly annotated recovered outcomes.
// Raw history and its indices stay chronological; orphan repair reserves these
// calls and model expansion projects each result beside its actual assistant.
func managedResultPairing(history []schema.Turn) (map[int]managedHistoryPairs, map[int]map[int]bool) {
	pairs := map[int]managedHistoryPairs{}
	moved := map[int]map[int]bool{}
	for resultIndex, turn := range history {
		if turn.Kind != schema.TurnToolResults {
			continue
		}
		for partIndex, part := range turn.Message.Content {
			r := part.ToolResult
			if r == nil || r.ManagedInvocationID == "" || r.ManagedAttemptGroupID == "" || r.ManagedToolIndex < 0 {
				continue
			}
			for assistantIndex := resultIndex - 1; assistantIndex >= 0; assistantIndex-- {
				assistant := history[assistantIndex]
				if assistant.Kind != schema.TurnAssistant || assistant.AttemptGroupID != r.ManagedAttemptGroupID {
					continue
				}
				calls := assistantToolCalls(assistant.Message)
				if r.ManagedToolIndex >= len(calls) {
					break
				}
				call := calls[r.ManagedToolIndex]
				if call.ID != r.ToolCallID || call.Name != r.Name || pairs[assistantIndex].hasToolIndex(r.ManagedToolIndex) {
					break
				}
				pairs[assistantIndex] = append(pairs[assistantIndex], managedHistoryPair{r.ManagedToolIndex, part})
				if moved[resultIndex] == nil {
					moved[resultIndex] = map[int]bool{}
				}
				moved[resultIndex][partIndex] = true
				break
			}
		}
	}
	return pairs, moved
}
