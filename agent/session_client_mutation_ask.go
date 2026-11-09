package agent

import (
	"slices"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// clientMutationAskCapture is immutable acceptance evidence. Replacement
// records retain every source capture rather than sampling the later live set.
type clientMutationAskCapture struct {
	BootGeneration string               `json:"boot_generation"`
	Revision       uint64               `json:"revision"`
	Resolution     schema.AskResolution `json:"resolution"`
}

func captureAskFence(question *appwire.PendingQuestion, state *appwire.PendingAskState, expected *uint64, generation string) (clientMutationAskCapture, error) {
	if err := appwire.ValidateAskFence(expected, generation); err != nil {
		return clientMutationAskCapture{}, err
	}
	if expected == nil {
		return clientMutationAskCapture{}, nil
	}
	if question == nil || state == nil || state.BootGeneration != generation || state.Revision != *expected || (len(state.Calls) == 0 && state.CalllessCount == 0) {
		return clientMutationAskCapture{}, appwire.WireError{Code: appwire.CodeConflict, Message: "pending question set changed", Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorAskFenceMismatch}}
	}
	return clientMutationAskCapture{BootGeneration: state.BootGeneration, Revision: state.Revision, Resolution: schema.AskResolution{Calls: slices.Clone(state.Calls), LegacyCount: state.CalllessCount}}, nil
}

func cloneAskCaptures(src []clientMutationAskCapture) []clientMutationAskCapture {
	out := slices.Clone(src)
	for i := range out {
		out[i].Resolution.Calls = slices.Clone(out[i].Resolution.Calls)
	}
	return out
}

// askResolution returns nil for an unfenced record. Named calls are a union,
// while unnamed restored populations are nested, so their count takes MAX.
func (p clientMutationPreconditions) askResolution() *schema.AskResolution {
	if len(p.AskCaptures) == 0 {
		return nil
	}
	out := &schema.AskResolution{Calls: []appwire.PendingAskCall{}}
	seen := make(map[appwire.PendingAskCall]bool)
	for _, capture := range p.AskCaptures {
		out.LegacyCount = max(out.LegacyCount, capture.Resolution.LegacyCount)
		for _, call := range capture.Resolution.Calls {
			if !seen[call] {
				seen[call] = true
				out.Calls = append(out.Calls, call)
			}
		}
	}
	return out
}

func (s *Session) clientMutationAskResolution(id string) *schema.AskResolution {
	if id == "" || s.clientMutations == nil {
		return nil
	}
	store := s.clientMutations
	store.stateMu.RLock()
	preconditions := cloneClientMutationPreconditions(store.state.Journal[id].Preconditions)
	store.stateMu.RUnlock()
	return preconditions.askResolution()
}

func askResolutionTurnIDs(resolution *schema.AskResolution, exclude string) []string {
	var ids []string
	for _, call := range resolution.Calls {
		if call.TurnID != exclude {
			ids = appendAnswerTurnIDs(ids, call.TurnID)
		}
	}
	return ids
}

func (s *Session) setClientMutationAskEvidence(turn *schema.Turn, id, exclude string) {
	turn.AskResolution = s.clientMutationAskResolution(id)
	if turn.AskResolution != nil {
		turn.AnswerToTurnIDs = askResolutionTurnIDs(turn.AskResolution, exclude)
	} else {
		turn.AnswerToTurnIDs = s.pendingAskTurnIDs()
	}
}

// carryAskCaptures keeps unfenced dominance, including fresh text added by a
// drain. That text is a new unfenced user input, not a captured source entry.
func carryAskCaptures(snapshot *clientMutationSnapshot, record *clientMutationRecord, entries []clientMutationQueueEntry, extraInput bool) {
	record.Preconditions.AskCaptures = nil
	if extraInput || len(entries) == 0 {
		return
	}
	var captures []clientMutationAskCapture
	for _, entry := range entries {
		source := snapshot.Journal[entry.ClientMutationID].Preconditions.AskCaptures
		if len(source) == 0 {
			return
		}
		captures = append(captures, cloneAskCaptures(source)...)
	}
	record.Preconditions.AskCaptures = captures
}
