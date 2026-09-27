package apptranscript

import (
	"sort"

	"primeradiant.com/evener/agent/argrepair"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// flushUnpairedCommunicateItems renders communicate calls whose CommRawArgs
// were never consumed by a paired tool-result turn as agentMessage items. The
// caller is responsible for positioning and attaching them to the correct
// turn. Both the full-read path (FlushUnpairedCommunicates) and the bounded
// path (projectIndexedGroup) call this helper so the rendering logic —
// RepairJSON, normalization, echo suppression, deterministic ordering — stays
// in one place.
func flushUnpairedCommunicateItems(reg *ToolCallRegistry, turnID string) []appwire.ThreadItem {
	if len(reg.CommRawArgs) == 0 {
		return nil
	}
	// Sort call IDs for deterministic item order across map iterations.
	callIDs := make([]string, 0, len(reg.CommRawArgs))
	for id := range reg.CommRawArgs {
		callIDs = append(callIDs, id)
	}
	sort.Strings(callIDs)
	var items []appwire.ThreadItem
	for _, callID := range callIDs {
		rawArgs := reg.CommRawArgs[callID]
		if rawArgs == "" {
			continue
		}
		repaired := argrepair.RepairJSON([]byte(rawArgs))
		normalized := NormalizeCommunicateArguments(repaired)
		msg := CommunicateMessageFromArguments(normalized)
		if msg == "" || (turnID == reg.LastAssistantTurnID && EchoesAssistantText(reg.LastAssistantText, msg)) {
			continue
		}
		items = append(items, appwire.ThreadItem{
			Type:   "agentMessage",
			ID:     "item_assistant_flushed_" + callID,
			TurnID: turnID,
			CallID: callID,
			Text:   msg,
			// Completed, not InProgress or Failed. The communicate's result
			// never arrived, so InProgress (the live preview's status) is
			// wrong on reload — the session ended, nothing is streaming.
			// Failed is wrong — the call was not rejected. Completed is the
			// honest terminal status: the message was delivered, the result
			// simply was not persisted.
			Status: appwire.TurnStatusCompleted,
		})
	}
	return items
}

// FlushUnpairedCommunicates renders communicate calls whose CommRawArgs were
// never consumed by a paired tool-result turn — the transcript ended with a
// pending communicate call. Each is rendered as an agentMessage and appended
// to the last turn, matching what the live projector surfaced: the live
// EventCommunicatePreview emits an in-progress agentMessage before the tool
// result arrives, and with no result turn that preview is the user-visible
// record. Without this flush, round-6's communicate deferral silently drops
// unpaired communicates on reload, diverging from live.
//
// Safety: at end-of-transcript, any CommRawArgs remaining in reg were seeded by
// an assistant turn whose result never arrived. A paired result would have
// deleted its entry (ProjectTurn's result path calls delete). Remaining entries
// are genuinely unpaired, so flushing them cannot double-render within a single
// reload.
//
// Resume: PrepareAppIdentity seeds the snapshot from this projection, so the
// flushed item enters the live snapshot. If the communicate's result later
// arrives live (the session resumes mid-call), the live projector emits
// EventCommunicate with the same CallID. The flushed item carries CallID so
// appThreadItemIdentityMatches deduplicates the live re-emission against the
// seeded flushed item by CallID — without it, the mismatched IDs
// (item_assistant_flushed_<callID> vs item_assistant_<N>) would double-render.
func FlushUnpairedCommunicates(turns *[]appwire.Turn, reg *ToolCallRegistry) bool {
	if len(*turns) == 0 || len(reg.CommRawArgs) == 0 {
		return false
	}
	last := &(*turns)[len(*turns)-1]
	items := flushUnpairedCommunicateItems(reg, last.ID)
	if len(items) == 0 {
		return false
	}
	nextItem := uint32(len(last.Items))
	var entry uint64
	if n := len(last.Items); n > 0 && last.Items[n-1].Position != nil {
		entry = last.Items[n-1].Position.Entry
	}
	for _, item := range items {
		position := appwire.ThreadItemPosition{Entry: entry, Item: nextItem}
		item.Position = &position
		item.TranscriptKey = appitempaging.TranscriptItemKey(last.ID, position)
		last.Items = append(last.Items, item)
		nextItem++
	}
	return true
}
