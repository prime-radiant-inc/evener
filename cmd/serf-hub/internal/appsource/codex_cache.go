package appsource

import (
	"encoding/json"
	"maps"
	"slices"

	"primeradiant.com/serf/appwire"
)

// cloneCodexCachedThread gives each reader ownership of the mutable state that
// mapThread and mapCodexTurn can place in the authoritative Codex cache.
func cloneCodexCachedThread(thread appwire.Thread, includeTurns bool) appwire.Thread {
	clone := thread
	clone.Status.ActiveFlags = slices.Clone(thread.Status.ActiveFlags)
	if !includeTurns {
		clone.Turns = nil
		return clone
	}

	clone.Turns = slices.Clone(thread.Turns)
	for turnIndex := range clone.Turns {
		clone.Turns[turnIndex] = cloneCodexCachedTurn(thread.Turns[turnIndex])
	}
	return clone
}

func cloneCodexCachedTurn(turn appwire.Turn) appwire.Turn {
	clone := turn
	clone.StartedAt = cloneCodexCachedInt64(turn.StartedAt)
	clone.CompletedAt = cloneCodexCachedInt64(turn.CompletedAt)
	clone.DurationMS = cloneCodexCachedInt64(turn.DurationMS)
	if turn.Error != nil {
		errorClone := *turn.Error
		if raw, ok := turn.Error.CodexErrorInfo.(json.RawMessage); ok {
			errorClone.CodexErrorInfo = slices.Clone(raw)
		}
		clone.Error = &errorClone
	}
	clone.Items = slices.Clone(turn.Items)
	for itemIndex := range clone.Items {
		clone.Items[itemIndex] = cloneCodexCachedItem(turn.Items[itemIndex])
	}
	return clone
}

func cloneCodexCachedItem(item appwire.ThreadItem) appwire.ThreadItem {
	clone := item
	clone.StartedAt = cloneCodexCachedInt64(item.StartedAt)
	clone.CompletedAt = cloneCodexCachedInt64(item.CompletedAt)
	clone.DurationMS = cloneCodexCachedInt64(item.DurationMS)
	clone.ExitCode = cloneCodexCachedInt64(item.ExitCode)
	clone.Raw = slices.Clone(item.Raw)
	clone.OutputImages = slices.Clone(item.OutputImages)
	clone.Images = slices.Clone(item.Images)
	for imageIndex := range clone.Images {
		clone.Images[imageIndex].Data = slices.Clone(item.Images[imageIndex].Data)
		clone.Images[imageIndex].Metadata = maps.Clone(item.Images[imageIndex].Metadata)
	}
	return clone
}

func cloneCodexCachedInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
