package schema

import "primeradiant.com/evener/llm"

// ManagedResultAssistantIndex locates the earlier assistant occurrence named by
// an explicitly annotated recovered result, or returns -1. History positions
// are chronological indices, not transcript sequence numbers. This pairing
// preserves model history across recovery and compaction; it grants no authority.
func ManagedResultAssistantIndex(history []Turn, resultIndex int, result *llm.ToolResultData) int {
	if result == nil || result.ManagedInvocationID == "" || result.ManagedAttemptGroupID == "" || result.ManagedToolIndex < 0 || resultIndex < 0 || resultIndex > len(history) {
		return -1
	}
	for index := resultIndex - 1; index >= 0; index-- {
		assistant := history[index]
		if assistant.Kind != TurnAssistant || assistant.AttemptGroupID != result.ManagedAttemptGroupID {
			continue
		}
		ordinal := 0
		for _, part := range assistant.Message.Content {
			call := part.ToolCall
			if part.Kind != llm.ContentToolCall || call == nil || call.ID == "" {
				continue
			}
			if ordinal == result.ManagedToolIndex {
				if call.ID == result.ToolCallID && call.Name == result.Name {
					return index
				}
				return -1
			}
			ordinal++
		}
		return -1
	}
	return -1
}
