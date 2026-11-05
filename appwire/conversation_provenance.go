package appwire

import "slices"

func CloneConversationProvenance(state *ConversationProvenanceState) *ConversationProvenanceState {
	if state == nil {
		return nil
	}
	cloned := *state
	cloned.AdmittedInputs = slices.Clone(state.AdmittedInputs)
	cloned.RetiredInputs = slices.Clone(state.RetiredInputs)
	cloned.Frontiers = slices.Clone(state.Frontiers)
	for i := range cloned.Frontiers {
		cloned.Frontiers[i].AddedInputs = slices.Clone(cloned.Frontiers[i].AddedInputs)
		cloned.Frontiers[i].RemovedInputs = slices.Clone(cloned.Frontiers[i].RemovedInputs)
	}
	return &cloned
}

// ResolveConversationFrontier resolves exact coverage from the canonical delta
// chain. A node must extend the immediately preceding canonical node. Repeated
// requests reference an existing node rather than appending a redundant node.
// Validation uses one live set, not a cumulative set per historical request.
func ResolveConversationFrontier(state *ConversationProvenanceState, frontierID string) ([]string, bool) {
	if state == nil || state.Version != 1 || !state.Authoritative || state.InstanceID == "" || frontierID == "" {
		return nil, false
	}
	seen := make(map[string]bool, len(state.Frontiers))
	covered := make(map[string]bool)
	previous := ""
	var result []string
	found := false
	for _, node := range state.Frontiers {
		if node.ID == "" || seen[node.ID] || node.BaseID != previous {
			return nil, false
		}
		seen[node.ID] = true
		changes := make(map[string]bool, len(node.AddedInputs)+len(node.RemovedInputs))
		for _, id := range node.RemovedInputs {
			if id == "" || changes[id] || !covered[id] {
				return nil, false
			}
			changes[id] = true
			delete(covered, id)
		}
		for _, id := range node.AddedInputs {
			if id == "" || changes[id] || covered[id] {
				return nil, false
			}
			changes[id] = true
			covered[id] = true
		}
		previous = node.ID
		if node.ID == frontierID {
			found = true
			result = make([]string, 0, len(covered))
			for id := range covered {
				result = append(result, id)
			}
		}
	}
	slices.Sort(result)
	return result, found
}

// ConversationOutputEligible never treats missing, old or malformed metadata as
// current. Every admitted, non-retired input must occur in this exact request.
func ConversationOutputEligible(state *ConversationProvenanceState, output *ConversationOutputProvenance) bool {
	if state == nil || output == nil || output.Version != 1 || output.InstanceID != state.InstanceID || output.RunGeneration == 0 || output.RequestID == "" || output.SourceItemID == "" {
		return false
	}
	coverage, ok := ResolveConversationFrontier(state, output.FrontierID)
	if !ok {
		return false
	}
	admitted := make(map[string]bool, len(state.AdmittedInputs))
	for _, id := range state.AdmittedInputs {
		if id == "" || admitted[id] {
			return false
		}
		admitted[id] = true
	}
	retired := make(map[string]bool, len(state.RetiredInputs))
	for _, id := range state.RetiredInputs {
		if !admitted[id] || retired[id] {
			return false
		}
		retired[id] = true
	}
	for id := range admitted {
		if !retired[id] {
			if _, ok := slices.BinarySearch(coverage, id); !ok {
				return false
			}
		}
	}
	return true
}
