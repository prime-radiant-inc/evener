package appprojector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/serf/agent/diagnostic"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/apptranscript"
	"primeradiant.com/serf/invariant"
	"primeradiant.com/serf/llm"
)

type AppNotification struct {
	ThreadID string
	Method   string
	Params   any
}

type skillActivationCandidate struct {
	turnID         string
	itemID         string
	callID         string
	skill          string
	valid          bool
	activationName string
}

var marshalContextCompaction = json.Marshal

type AppEventProjector struct {
	threadID string
	ref      string

	nextTurn       int
	nextItem       int
	reservedTurnID string
	activeTurnID   string
	// anyTurnStarted records whether a REAL turn has ever started in this
	// projector's life. It is the chronologically honest half of the prelude
	// test in preTurnAnnouncementTurnID: nextTurn alone cannot answer "has
	// anything actually run yet", because ReserveTurnID mints an id (and so
	// bumps nextTurn) for a turn that has not started - turn/start's
	// reservation, or SetProcessing's auto-continuation reservation for a
	// queued initial prompt, both land while the session is still announcing
	// its own startup. Those SESSION_START-time events happened BEFORE any
	// real turn whatever the counter says.
	anyTurnStarted bool
	// historySeeded records whether SeedPersistedTurns ever raised the
	// counter over a resumed session's persisted entries. A resumed session's
	// startup burst happened AFTER the persisted history, not before any
	// real turn, so it must keep minting its own gap id (kata 9ekv) rather
	// than folding into the prelude turn at the very top of the transcript.
	historySeeded bool
	// midSessionAnnouncementTurnID is the shared synthetic turn id for the
	// CURRENT gap between two real turns (nextTurn > 0, activeTurnID == "").
	// See preTurnAnnouncementTurnID's doc comment (kata 9ekv): it is minted
	// once per gap on that gap's first no-active-turn announcement, reused by
	// every announcement in the same gap, and cleared by startTurn so the
	// next gap gets its own fresh id.
	midSessionAnnouncementTurnID string
	assistantItem                string
	assistantText                string
	reasoningItem                string
	toolItemsByKey               map[string]string
	toolArgsByKey                map[string]string
	// toolStartByKey records each open tool call's server-side start time (the
	// EventToolCallStart event's own timestamp) so EventToolCallEnd can stamp
	// the completed item with the call's real StartedAt/DurationMS (issue
	// #37: the web hover meta shows server truth or nothing).
	toolStartByKey  map[string]time.Time
	suppressedTools map[string]struct{}
	// heldToolResultImages keeps, per call id, the completed tool item whose
	// sha-addressed result images no server can serve yet — see
	// holdUnfetchableToolResultImages. Entries leave on the round's
	// TOOL_RESULT_IMAGES_PERSISTED, or with the turn that opened them.
	heldToolResultImages map[string]appwire.ThreadItem
	skillCandidate       skillActivationCandidate

	lastAssistantTurnID string
	lastAssistantText   string

	// pendingTurnID/pendingCompletedAtMillis/pendingDurationMS record the most
	// recent EventTurnEnded's timing until the turn it names is actually
	// completed by one of the existing completion sites (EventUserInput,
	// EventGoalContinuation, EventError, EventSessionEnd). EventTurnEnded fires
	// before those sites on some paths (interrupt/close) and after on others
	// (a failed turn), so this is a stash, not a completion — see
	// applyPendingTiming.
	pendingTurnID            string
	pendingCompletedAtMillis int64
	pendingDurationMS        int64

	// activeTurnUsage/activeTurnModel accumulate the current turn's own
	// (not cumulative-session) usage across every EventAssistantTextEnd
	// since startTurn(), stamped onto the completing Turn at each of the
	// four completion sites. Unlike pendingTurnID/pendingDurationMS, no
	// stash-vs-completion-ordering race exists here: EventAssistantTextEnd
	// always fires chronologically before the turn's own completion event.
	activeTurnUsage llm.Usage
	activeTurnModel string
}

func NewAppEventProjector(threadID, ref string) *AppEventProjector {
	return &AppEventProjector{
		threadID:             threadID,
		ref:                  ref,
		toolItemsByKey:       map[string]string{},
		toolArgsByKey:        map[string]string{},
		toolStartByKey:       map[string]time.Time{},
		suppressedTools:      map[string]struct{}{},
		heldToolResultImages: map[string]appwire.ThreadItem{},
	}
}

// SeedPersistedTurns raises the projector's turn counter so no live turn it
// mints can collide with a turn id the transcript projection already assigned.
//
// Both namespaces are "turn_%d": internal/apptranscript numbers a persisted
// turn by its ENTRY INDEX, and a fresh projector starts at turn_1. Since the
// in-memory snapshot became the daemon's only turn authority, a collision is
// permanent -- the live turn merges into the seeded entry and replaces its
// content for the life of the session, with nothing left to re-derive it from.
//
// It only ever raises the counter, so seeding twice (once from the prepared
// transcript, once from a restored session's own entry count) is safe and the
// higher figure wins.
func (p *AppEventProjector) SeedPersistedTurns(persistedEntries int) {
	if persistedEntries > p.nextTurn {
		p.nextTurn = persistedEntries
	}
	// Zero entries is a fresh session's no-op seed, not history: only a real
	// persisted entry count fences the prelude off (see historySeeded).
	if persistedEntries > 0 {
		p.historySeeded = true
	}
}

func (p *AppEventProjector) clearSkillCandidate() {
	p.skillCandidate = skillActivationCandidate{}
}

func (p *AppEventProjector) Project(event events.SessionEvent) []AppNotification {
	if p.threadID == "" {
		p.threadID = event.SessionID
	}

	switch event.Kind {
	case events.EventSessionStart:
		data := eventData[events.SessionStartData](event.Data)
		// A resumed session's turn ids must not reuse the "turn_%d" namespace
		// the transcript projection (internal/apptranscript) already assigned by
		// entry index to the session's persisted entries (kata eptj). The
		// daemon fences this when it prepares the identity, before any event
		// arrives; this is the same fence for a projector whose seed count only
		// the session knows.
		if data.Restored {
			p.SeedPersistedTurns(data.TranscriptEntries)
		}
		// A restored session carries its re-derived state on the event (spec
		// §5.4's "two touchpoints"); a fresh session's State is empty and
		// defaults to idle, same as an unrecognized value.
		status := appwire.ThreadStatusIdle
		switch data.State {
		case appwire.ThreadStatusAwaiting:
			status = appwire.ThreadStatusAwaiting
		case appwire.ThreadStatusIdle:
			status = appwire.ThreadStatusIdle
		}
		return []AppNotification{
			p.notification(appwire.NotifyThreadStarted, appwire.ThreadStartedParams{
				ThreadID: p.threadID,
				Ref:      p.ref,
				Thread: appwire.Thread{
					ID:            p.threadID,
					SessionID:     p.threadID,
					Source:        "local",
					ModelProvider: data.Model,
					Status:        appwire.ThreadStatus{Type: status},
					Serf: appwire.SerfThread{
						Ref:     p.ref,
						Profile: data.Profile,
					},
				},
			}),
			p.threadStatus(status),
		}
	case events.EventUserInput:
		out := []AppNotification{}
		if p.activeTurnID != "" {
			turnID := p.activeTurnID
			p.activeTurnID = ""
			p.assistantItem = ""
			p.assistantText = ""
			p.reasoningItem = ""
			p.toolItemsByKey = map[string]string{}
			p.toolArgsByKey = map[string]string{}
			p.toolStartByKey = map[string]time.Time{}
			p.suppressedTools = map[string]struct{}{}
			turn := appwire.Turn{ID: turnID, Status: appwire.TurnStatusCompleted}
			p.applyPendingTiming(turnID, &turn)
			p.stampTurnUsage(&turn)
			// Deliberately still map[string]any, not appwire.TurnCompletedParams
			// (kcb5): the declared type is {turnId,turn} but every producer here
			// sends {threadId,ref,turn} with no turnId at all - the type doesn't
			// describe what's actually on the wire. Converting to the CURRENT
			// declaration would silently drop threadId/ref from every
			// turn/completed frame (a real, if likely-harmless, wire change - no
			// consumer reads them, per reducer.test.ts/hub_notifications.go);
			// fixing the declaration to match reality is a coupled Go+TS+test
			// change of its own, left to a separate decision.
			out = append(out, p.notification(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     turn,
			}))
		}
		data := eventData[events.UserInputData](event.Data)
		if data.StableTurnID != "" {
			p.reservedTurnID = data.StableTurnID
		}
		turnID := p.startTurn()
		item := appwire.ThreadItem{
			Type:                 "userMessage",
			ID:                   p.nextItemID("user"),
			TurnID:               turnID,
			TranscriptEntryIndex: data.Turn,
			Text:                 data.Text,
			Images:               projectUserInputImages(data.Images),
			Status:               "completed",
			ClientMutationID:     data.ClientMutationID,
		}
		out = append(out,
			p.notification(appwire.NotifyTurnStarted, appwire.TurnStartedParams{
				ThreadID: p.threadID,
				Ref:      p.ref,
				Turn:     startedTurn(turnID, event.Timestamp),
			}),
			p.notification(appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
				ThreadID: p.threadID,
				Ref:      p.ref,
				TurnID:   turnID,
				Item:     item,
			}),
			p.threadStatus(appwire.ThreadStatusActive),
		)
		return out
	case events.EventGoalContinuation:
		p.clearSkillCandidate()
		// A goal continuation opens a fresh turn just like a user input
		// (close the prior turn, start a new one), but renders its prompt as
		// a systemMessage rather than a userMessage so continuations don't
		// look like the user spoke.
		out := []AppNotification{}
		if p.activeTurnID != "" {
			turnID := p.activeTurnID
			p.activeTurnID = ""
			p.assistantItem = ""
			p.assistantText = ""
			p.reasoningItem = ""
			p.toolItemsByKey = map[string]string{}
			p.toolArgsByKey = map[string]string{}
			p.toolStartByKey = map[string]time.Time{}
			p.suppressedTools = map[string]struct{}{}
			turn := appwire.Turn{ID: turnID, Status: appwire.TurnStatusCompleted}
			p.applyPendingTiming(turnID, &turn)
			p.stampTurnUsage(&turn)
			// Still map[string]any, not TurnCompletedParams - see EventUserInput's own comment above (kcb5).
			out = append(out, p.notification(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     turn,
			}))
		}
		turnID := p.startTurn()
		data := eventData[events.GoalContinuationData](event.Data)
		item := appwire.ThreadItem{
			Type:        "systemMessage",
			ID:          p.nextItemID("goal_continuation"),
			TurnID:      turnID,
			Description: "Goal",
			Text:        data.Text,
			Status:      appwire.TurnStatusCompleted,
		}
		out = append(out,
			p.notification(appwire.NotifyTurnStarted, appwire.TurnStartedParams{
				ThreadID: p.threadID,
				Ref:      p.ref,
				Turn:     startedTurn(turnID, event.Timestamp),
			}),
			p.notification(appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
				ThreadID: p.threadID,
				Ref:      p.ref,
				TurnID:   turnID,
				Item:     item,
			}),
			p.threadStatus(appwire.ThreadStatusActive),
		)
		return out
	case events.EventAssistantTextStart:
		p.skillCandidate = skillActivationCandidate{}
		out := p.ensureTurn(event.Timestamp)
		// The agent message is materialized lazily -- with the first delta, or
		// at ASSISTANT_TEXT_END when the round's whole text arrives there. Every
		// round that answers with tool calls alone emits this same lifecycle
		// (ASSISTANT_TEXT_END carries the round's usage and finish reason), and
		// an empty agent message must not reach the envelope for it.
		p.assistantItem = ""
		p.assistantText = ""
		p.reasoningItem = ""
		return out
	case events.EventAssistantTextDelta:
		created, out := p.ensureAssistantItem(event.Timestamp)
		data := eventData[events.AssistantTextDeltaData](event.Data)
		p.assistantText += data.Delta
		if created {
			out = append(out, p.notification(appwire.NotifyItemStarted, appwire.ItemLifecycleParams{
				ThreadID: p.threadID,
				Ref:      p.ref,
				TurnID:   p.activeTurnID,
				Item: appwire.ThreadItem{
					Type:   "agentMessage",
					ID:     p.assistantItem,
					TurnID: p.activeTurnID,
					Status: appwire.TurnStatusInProgress,
				},
			}))
		}
		return append(out, p.notification(appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			TurnID:   p.activeTurnID,
			ItemID:   p.assistantItem,
			Delta:    data.Delta,
		}))
	case events.EventReasoningSummaryDelta:
		data := eventData[events.ReasoningSummaryDeltaData](event.Data)
		created, out := p.ensureReasoningItem(event.Timestamp)
		if created {
			out = append(out, p.notification(appwire.NotifyItemStarted, appwire.ItemLifecycleParams{
				ThreadID: p.threadID,
				Ref:      p.ref,
				TurnID:   p.activeTurnID,
				Item: appwire.ThreadItem{
					Type:   "reasoning",
					ID:     p.reasoningItem,
					TurnID: p.activeTurnID,
					Status: appwire.TurnStatusInProgress,
				},
			}))
		}
		out = append(out, p.notification(appwire.NotifyReasoningSummaryDelta, appwire.ReasoningSummaryDeltaParams{
			ThreadID:     p.threadID,
			Ref:          p.ref,
			TurnID:       p.activeTurnID,
			ItemID:       p.reasoningItem,
			SummaryIndex: data.SummaryIndex,
			Delta:        data.Delta,
		}))
		return out
	case events.EventAssistantTextEnd:
		p.skillCandidate = skillActivationCandidate{}
		out := p.ensureTurn(event.Timestamp)
		data := eventData[events.AssistantTextEndData](event.Data)
		p.activeTurnUsage = p.activeTurnUsage.Add(data.Usage)
		if data.Model != "" {
			p.activeTurnModel = data.Model
		}
		text := data.Text
		if text == "" {
			text = p.assistantText
		}
		// A round with nothing to say -- tool calls only -- ends the text
		// lifecycle without ever materializing an item. Its usage, accumulated
		// above, is the whole effect it has on the envelope.
		if p.assistantItem == "" && strings.TrimSpace(text) == "" {
			p.assistantText = ""
			return out
		}
		// The turn is already open above, so this can only materialize the
		// item; it has no turn/started of its own left to announce.
		p.ensureAssistantItem(event.Timestamp)
		item := appwire.ThreadItem{
			Type:   "agentMessage",
			ID:     p.assistantItem,
			TurnID: p.activeTurnID,
			Text:   text,
			Status: "completed",
		}
		turnID := p.activeTurnID
		p.recordAssistantMessage(turnID, text)
		p.assistantItem = ""
		p.assistantText = ""
		return append(out, p.notification(appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			TurnID:   turnID,
			Item:     item,
		}))
	case events.EventAssistantTextReset:
		// A retry after partial output: discard the in-progress assistant item
		// so the retry's stream replaces it rather than appending. No-op when
		// nothing was streamed yet (no item to reset).
		if p.assistantItem == "" {
			return nil
		}
		itemID := p.assistantItem
		turnID := p.activeTurnID
		p.assistantItem = ""
		p.assistantText = ""
		return []AppNotification{p.notification(appwire.NotifyAgentMessageReset, appwire.AgentMessageResetParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			TurnID:   turnID,
			ItemID:   itemID,
		})}
	case events.EventModelRetry:
		// Thread-scoped, item-less: the retry is state about the wait in
		// progress, not a fact worth a transcript row (see
		// appwire.ThreadModelRetryParams on why 91 rows is the wrong answer).
		data := eventData[events.ModelRetryData](event.Data)
		return []AppNotification{p.notification(appwire.NotifySerfThreadModelRetry, appwire.ThreadModelRetryParams{
			ThreadID:    p.threadID,
			Ref:         p.ref,
			TurnID:      p.activeTurnID,
			Attempt:     data.Attempt,
			MaxAttempts: data.MaxAttempts,
			DelayMS:     data.DelayMS,
			ErrorClass:  data.ErrorClass,
			StatusCode:  data.StatusCode,
			Message:     data.Message,
			Model:       data.Model,
		})}
	case events.EventCommunicate:
		p.skillCandidate = skillActivationCandidate{}
		data := eventData[events.CommunicateData](event.Data)
		text := strings.TrimSpace(data.Message)
		if text == "" {
			return nil
		}
		out := p.ensureTurn(event.Timestamp)
		if p.matchesLastAssistantMessage(p.activeTurnID, text) {
			return out
		}
		item := appwire.ThreadItem{
			Type:   "agentMessage",
			ID:     p.nextItemID("assistant"),
			TurnID: p.activeTurnID,
			Text:   text,
			Status: appwire.TurnStatusCompleted,
		}
		p.recordAssistantMessage(p.activeTurnID, text)
		return append(out, p.notification(appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			TurnID:   p.activeTurnID,
			Item:     item,
		}))
	case events.EventToolCallStart:
		out := p.ensureTurn(event.Timestamp)
		data := eventData[events.ToolCallStartData](event.Data)
		if data.ToolName != "use_skill" {
			p.skillCandidate = skillActivationCandidate{}
		}
		if data.ToolName == "communicate" {
			p.suppressedTools[data.CallID] = struct{}{}
			return out
		}
		itemID := p.nextItemID("tool")
		p.toolItemsByKey[data.CallID] = itemID
		p.toolArgsByKey[data.CallID] = data.ArgumentsJSON
		startedItem := appwire.ThreadItem{
			Type:          "commandExecution",
			ID:            itemID,
			TurnID:        p.activeTurnID,
			ToolName:      data.ToolName,
			CallID:        data.CallID,
			ArgumentsJSON: data.ArgumentsJSON,
			Description:   data.Description,
			Status:        appwire.TurnStatusInProgress,
		}
		// The event's own timestamp is the server truth for when the call
		// started; a zero timestamp leaves StartedAt unset rather than
		// reporting the Unix epoch (issue #37).
		if !event.Timestamp.IsZero() {
			ms := event.Timestamp.UnixMilli()
			startedItem.StartedAt = &ms
			p.toolStartByKey[data.CallID] = event.Timestamp
		}
		if data.ToolName == "use_skill" {
			skill := useSkillNameFromArgs(data.ArgumentsJSON)
			p.skillCandidate = skillActivationCandidate{
				turnID: p.activeTurnID,
				itemID: itemID,
				callID: data.CallID,
				skill:  skill,
				valid:  skill != "",
			}
		}
		return append(out, p.notification(appwire.NotifyItemStarted, appwire.ItemLifecycleParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			TurnID:   p.activeTurnID,
			Item:     startedItem,
		}))
	case events.EventToolCallOutputDelta:
		data := eventData[events.ToolCallOutputDeltaData](event.Data)
		if _, ok := p.suppressedTools[data.CallID]; ok {
			return nil
		}
		return []AppNotification{p.notification(appwire.NotifyToolOutputDelta, appwire.ToolOutputDeltaParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			TurnID:   p.activeTurnID,
			ItemID:   p.toolItemID(data.CallID),
			CallID:   data.CallID,
			Delta:    data.Delta,
		})}
	case events.EventToolCallEnd:
		data := eventData[events.ToolCallEndData](event.Data)
		if _, ok := p.suppressedTools[data.CallID]; ok {
			delete(p.suppressedTools, data.CallID)
			return nil
		}
		raw := data.ToolState
		if p.skillCandidate.valid && p.skillCandidate.callID == data.CallID && p.skillCandidate.activationName != "" {
			raw = skillActivationRaw(p.skillCandidate.activationName)
		}
		argsJSON := p.toolArgsByKey[data.CallID]
		if argsJSON == "" {
			argsJSON = data.ArgumentsJSON
		}
		item := appwire.ThreadItem{
			Type:          "commandExecution",
			ID:            p.toolItemID(data.CallID),
			TurnID:        p.activeTurnID,
			ToolName:      data.ToolName,
			CallID:        data.CallID,
			ArgumentsJSON: argsJSON,
			Output:        data.Output,
			Error:         data.Error,
			PrevalOnly:    data.PrevalOnly,
			OutputImages:  projectOutputImages(data.OutputImages),
			Status:        apptranscript.SettledToolStatus(data.Error != ""),
			Raw:           raw,
			// Carry the call's purpose onto the completed item too (#26):
			// the started item already has it, and live consumers (the web
			// subagent activity line) render the purpose from Description.
			Description: apptranscript.ToolIntentFromArguments(json.RawMessage(argsJSON)),
			// ExitCode promotes the shell tool's exit code, already riding
			// data.ToolState end to end (agent/session_tools_shell.go:483
			// shellToolResult), onto the settled item (wire-honesty spec Part
			// A). Read from data.ToolState directly rather than raw, which may
			// have been overwritten above with the skill-activation payload.
			ExitCode: apptranscript.ExitCodeFromToolState(data.ToolState),
		}
		// Server-truth timing for the hover meta (issue #37): CompletedAt from
		// this event's own timestamp; StartedAt/DurationMS from the recorded
		// call start. Anything not honestly recorded stays unset.
		if !event.Timestamp.IsZero() {
			ms := event.Timestamp.UnixMilli()
			item.CompletedAt = &ms
			if start, ok := p.toolStartByKey[data.CallID]; ok && !start.IsZero() {
				startMs := start.UnixMilli()
				item.StartedAt = &startMs
				duration := event.Timestamp.Sub(start).Milliseconds()
				if duration >= 0 {
					item.DurationMS = &duration
				}
			}
		}
		delete(p.toolStartByKey, data.CallID)
		if data.ToolName == "use_skill" && data.Error == "" {
			skill := useSkillNameFromArgs(argsJSON)
			activationName := ""
			if p.skillCandidate.callID == data.CallID {
				activationName = p.skillCandidate.activationName
			}
			p.skillCandidate = skillActivationCandidate{
				turnID:         p.activeTurnID,
				itemID:         item.ID,
				callID:         data.CallID,
				skill:          skill,
				valid:          skill != "",
				activationName: activationName,
			}
		} else {
			p.skillCandidate = skillActivationCandidate{}
		}
		delete(p.toolItemsByKey, data.CallID)
		delete(p.toolArgsByKey, data.CallID)
		p.holdUnfetchableToolResultImages(&item)
		return []AppNotification{p.notification(appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			TurnID:   p.activeTurnID,
			Item:     item,
		})}
	case events.EventToolResultImagesPersisted:
		// The bytes behind the descriptors held above have reached the
		// transcript, so the promise they make is now one a server can keep.
		// Re-send each item exactly as it settled, with its images restored:
		// a client that has already seen this id replaces its copy wholesale,
		// so a partial item would erase the call's output.
		data := eventData[events.ToolResultImagesPersistedData](event.Data)
		var released []AppNotification
		for _, callID := range data.CallIDs {
			item, held := p.heldToolResultImages[callID]
			if !held {
				continue
			}
			delete(p.heldToolResultImages, callID)
			released = append(released, p.notification(appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
				ThreadID: p.threadID,
				Ref:      p.ref,
				TurnID:   item.TurnID,
				Item:     item,
			}))
		}
		return released
	case events.EventToolCallRepaired:
		// This fires before EventToolCallStart creates the CallID-keyed tool
		// item (repair runs before PreToolUse hooks, which run before the
		// start event), so there is no item yet to annotate. Render it as a
		// standalone system announcement instead, the same way other
		// out-of-band, no-item-state events (hook end, plugin loaded, ...)
		// are surfaced.
		data := eventData[events.ToolCallRepairedData](event.Data)
		return p.systemAnnouncement(appwire.ThreadItemEventKindToolRepair, "Tool call repaired", toolCallRepairedAnnouncement(data))
	case events.EventWarning:
		p.clearSkillCandidate()
		data := eventData[events.WarningData](event.Data)
		info := diagnostic.FromFields(data.Source, data.Title, data.Hint, data.Message)
		// Still map[string]any, not appwire.WarningParams (kcb5): info.Title/
		// info.Hint/info.Source are frequently "" for a diagnostic.Classify fallback
		// that recognized no known source or keyword - this map always emits those
		// keys regardless, but WarningParams tags them all `omitempty`, so a typed
		// literal would silently drop title/hint/source whenever they're blank. Not
		// provably byte-identical; left as a map.
		return []AppNotification{p.notification(appwire.NotifyWarning, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"message":  data.Message,
			"source":   string(info.Source),
			"title":    info.Title,
			"hint":     info.Hint,
			"warning":  event.Data,
		})}
	case events.EventError:
		p.clearSkillCandidate()
		data := eventData[events.ErrorData](event.Data)
		message := strings.TrimSpace(data.Error)
		if message == "" {
			message = "session error"
		}
		info := diagnostic.FromFields(data.Source, data.Title, data.Hint, message)
		cause := projectErrorCause(data.Cause)

		// A user-cancelled turn is not a failure: surface it as a warning and
		// let the interrupted SessionEnd own the turn's terminal state (do NOT
		// complete the turn as failed here).
		if isContextCanceledError(message) {
			// Still map[string]any, not WarningParams - same omitempty-vs-blank-
			// field risk as EventWarning's own comment above (kcb5).
			return []AppNotification{p.notification(appwire.NotifyWarning, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"message":  message,
				"source":   string(info.Source),
				"title":    info.Title,
				"hint":     info.Hint,
				"cause":    cause,
				"warning": events.WarningData{
					Message: message,
					Source:  string(info.Source),
					Title:   info.Title,
					Hint:    info.Hint,
				},
			})}
		}

		// A genuine turn failure is surfaced exactly once, as a failed turn. The
		// TurnError carries the full diagnostic (message/source/title/hint/cause);
		// emitting a separate NotifyWarning too would make the same error render
		// twice in clients that show both the warning channel and turn errors.
		out := p.ensureTurn(event.Timestamp)
		turnID := p.activeTurnID
		p.activeTurnID = ""
		p.assistantItem = ""
		p.assistantText = ""
		p.toolItemsByKey = map[string]string{}
		p.suppressedTools = map[string]struct{}{}
		turn := appwire.Turn{
			ID:     turnID,
			Status: appwire.TurnStatusFailed,
			Error: &appwire.TurnError{
				Message: message,
				Source:  string(info.Source),
				Title:   info.Title,
				Hint:    info.Hint,
				Cause:   cause,
			},
		}
		// EventTurnEnded runs after EventError on the failure path (see
		// handleModelError), so no pending timing has been recorded yet here;
		// this call is a no-op today but keeps the timing path uniform across
		// all four completion sites.
		p.applyPendingTiming(turnID, &turn)
		p.stampTurnUsage(&turn)
		return append(out,
			// Still map[string]any, not TurnCompletedParams - see EventUserInput's own comment above (kcb5).
			p.notification(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     turn,
			}),
		)
	case events.EventSteeringInjected:
		p.clearSkillCandidate()
		data := eventData[events.SteeringInjectedData](event.Data)
		images := projectUserInputImages(data.Images)
		text := data.Text
		if strings.TrimSpace(text) == "" {
			text = apptranscript.ImagePlaceholder(len(images))
		}
		// Still map[string]any, not appwire.SerfSteeringInjectedParams (kcb5):
		// images is nil whenever a steer carries no images (the common case) -
		// this map always emits "images" anyway (as null), but Images is tagged
		// `omitempty` on the struct, so a typed literal would drop the key
		// entirely instead. Not provably byte-identical; left as a map.
		params := map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"text":     text,
			"images":   images,
		}
		// User-sent steering carries its provenance so the web UI renders it
		// as a user message rather than a system steering divider (issue #24).
		// Empty (system) source is omitted from the wire payload.
		if data.Source != "" {
			params["source"] = data.Source
		}
		// Kind names what the daemon injected, so the UI labels a steer from
		// the wire rather than pattern-matching its prose. Omitted when unset.
		if data.Kind != "" {
			params["kind"] = data.Kind
		}
		if data.ClientMutationID != "" {
			params["clientMutationId"] = data.ClientMutationID
		}
		return []AppNotification{p.notification(appwire.NotifySerfSteeringInjected, params)}
	case events.EventCompactionTurn:
		p.clearSkillCandidate()
		data := eventData[events.CompactionTurnData](event.Data)
		return p.systemAnnouncement(appwire.ThreadItemEventKindCompaction, apptranscript.CompactionDescription(data.Kind), data.Text)
	case events.EventTurnLimit:
		p.clearSkillCandidate()
		data := eventData[events.TurnLimitData](event.Data)
		return p.systemAnnouncement(appwire.ThreadItemEventKindTurnLimit, "Turn limit", turnLimitAnnouncement(data))
	case events.EventLoopDetection:
		p.clearSkillCandidate()
		data := eventData[events.LoopDetectionData](event.Data)
		return p.systemAnnouncement(appwire.ThreadItemEventKindLoopDetection, "Loop detection", data.Message)
	case events.EventGoalEnded:
		p.clearSkillCandidate()
		data := eventData[events.GoalEndedData](event.Data)
		return p.systemAnnouncement(appwire.ThreadItemEventKindGoalEnded, "Goal", goalEndText(data))
	case events.EventSkillActivated:
		data := eventData[events.SkillActivatedData](event.Data)
		name := strings.TrimSpace(data.Name)
		if p.skillCandidate.valid && p.skillCandidate.turnID == p.activeTurnID && p.skillCandidate.skill == name {
			candidate := p.skillCandidate
			if _, inFlight := p.toolItemsByKey[candidate.callID]; inFlight {
				p.skillCandidate.activationName = name
				return nil
			}
			p.skillCandidate = skillActivationCandidate{}
			item := appwire.ThreadItem{
				Type:     "commandExecution",
				ID:       candidate.itemID,
				TurnID:   candidate.turnID,
				ToolName: "use_skill",
				CallID:   candidate.callID,
				Status:   "completed",
				Raw:      skillActivationRaw(name),
			}
			return []AppNotification{p.notification(appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
				ThreadID: p.threadID,
				Ref:      p.ref,
				TurnID:   candidate.turnID,
				Item:     item,
			})}
		}
		p.skillCandidate = skillActivationCandidate{}
		return p.systemAnnouncement(appwire.ThreadItemEventKindSkillActivated, "Skill activated", "Activated skill: "+data.Name)
	case events.EventContextCompaction:
		p.clearSkillCandidate()
		data := eventData[events.ContextCompactionData](event.Data)
		return p.systemAnnouncementWithRaw(appwire.ThreadItemEventKindContextCompaction, "Context compaction", contextCompactionAnnouncement(data), contextCompactionRaw(data))
	case events.EventPluginLoaded:
		p.clearSkillCandidate()
		data := eventData[events.PluginLoadedData](event.Data)
		summary := pluginLoadedAnnouncement(data)
		return p.systemAnnouncementWithRaw(appwire.ThreadItemEventKindPluginLoaded, summary, "", pluginLoadedRaw(data))
	case events.EventHookStart:
		return nil
	case events.EventHookEnd:
		p.clearSkillCandidate()
		data := eventData[events.HookEndData](event.Data)
		return p.systemAnnouncementWithExitCode(appwire.ThreadItemEventKindHookCompleted, "Hook", hookEndAnnouncement(data), data.ExitCode)
	case events.EventForkSummary:
		p.clearSkillCandidate()
		data := eventData[events.ForkSummaryData](event.Data)
		return p.systemAnnouncement(appwire.ThreadItemEventKindForkSummary, "Fork summary", forkSummaryAnnouncement(data))
	case events.EventPromptLoaded:
		p.clearSkillCandidate()
		data := eventData[events.PromptLoadedData](event.Data)
		return p.systemAnnouncement(appwire.ThreadItemEventKindPromptLoaded, "Prompt loaded", promptLoadedAnnouncement(data))
	case events.EventRoundTimings:
		p.clearSkillCandidate()
		data := eventData[events.RoundTimings](event.Data)
		return p.systemAnnouncementWithRaw(appwire.ThreadItemEventKindRoundTimings, "Round timings", roundTimingsAnnouncement(data), roundTimingsRaw(data))
	case events.EventQueueChanged:
		p.clearSkillCandidate()
		data := eventData[events.QueueChangedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyThreadQueueChanged, appwire.ThreadQueueChangedParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Queue: appwire.QueueState{
				Depth:             data.Depth,
				Revision:          data.Revision,
				Preview:           append([]string(nil), data.Preview...),
				IDs:               append([]string(nil), data.IDs...),
				ClientMutationIDs: append([]string(nil), data.ClientMutationIDs...),
				Texts:             append([]string(nil), data.Texts...),
			},
		})}
	case events.EventTaskUpdated:
		p.clearSkillCandidate()
		data := eventData[events.TaskUpdatedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifySerfTaskUpdated, appwire.TaskUpdatedParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Total:    data.Total,
			Done:     data.Done,
		})}
	case events.EventSandboxEscalationRequested:
		// A harness-raised sandbox-exemption approval card (M7). It rides the event
		// stream ONLY — it is never appended to the transcript, so the model can
		// neither observe nor replay it. DeniedPath is the FULL path for informed
		// consent (only non-sensitive containment denials escalate; a sensitive path,
		// which never escalates, would degrade to "<denied>"); file contents never
		// appear. The shell fields are reserved and empty in v1.
		data := eventData[events.SandboxEscalationRequestedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifySerfSandboxEscalationRequested, appwire.SandboxEscalationRequested{
			ThreadID:     p.threadID,
			Ref:          p.ref,
			EscalationID: data.EscalationID,
			Mode:         data.Mode,
			Tool:         data.Tool,
			Kind:         data.Kind,
			DeniedPath:   data.DeniedPath,
			Command:      data.Command,
			OutputSoFar:  data.OutputSoFar,
			PartiallyRan: data.PartiallyRan,
		})}
	case events.EventSandboxEscalationResolved:
		// The pair to EventSandboxEscalationRequested above: a previously-raised
		// escalation left the pending set (resolved, turn-interrupted, or cleared by
		// session close — agent/session_escalation.go's escalateOnSandboxDenial emits
		// this exactly once per escalation from its convergence-point exit). Every
		// OTHER subscribed client uses it to clear its own stale copy of the card. It
		// carries no reason/approved (a review decision, additive later): the sole
		// consumer clears by id identically regardless of outcome, and the producer
		// cannot reliably distinguish close-cancel from interrupt anyway. Like
		// requested, it rides the event stream only and touches no turn/item state.
		data := eventData[events.SandboxEscalationResolvedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifySerfSandboxEscalationResolved, appwire.SandboxEscalationResolved{
			ThreadID:     p.threadID,
			Ref:          p.ref,
			EscalationID: data.EscalationID,
		})}
	case events.EventSessionNameChanged:
		p.clearSkillCandidate()
		data := eventData[events.SessionNameChangedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyThreadNameChanged, appwire.ThreadNameChangedParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Name:     data.Name,
			Source:   data.Source,
		})}
	case events.EventModelChanged:
		p.clearSkillCandidate()
		data := eventData[events.ModelChangedData](event.Data)
		out := []AppNotification{p.notification(appwire.NotifyThreadModelChanged, appwire.ThreadModelChangedParams{
			ThreadID:              p.threadID,
			Ref:                   p.ref,
			ModelProvider:         data.NewProvider,
			Model:                 data.NewModel,
			ReasoningEffortLevels: data.ReasoningEffortLevels,
			SupportsReasoning:     data.SupportsReasoning,
		})}
		// Live-only echo of the persisted TurnModelSwitch marker (N5): the
		// same text SetModel wrote to the transcript, rendered as a
		// systemMessage item so an already-connected client sees the marker
		// immediately rather than waiting for a reload. The transcript still
		// carries the marker independently, which is what a daemon restart
		// seeds its snapshot from; this notification only covers the clients
		// already attached when the switch happened.
		out = append(out, p.systemAnnouncement(appwire.ThreadItemEventKindModelSwitch, "Model switch", data.MarkerText)...)
		return out
	case events.EventReasoningEffortChanged:
		p.clearSkillCandidate()
		data := eventData[events.ReasoningEffortChangedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyThreadReasoningEffortChanged, appwire.ThreadReasoningEffortChangedParams{
			ThreadID:        p.threadID,
			Ref:             p.ref,
			ReasoningEffort: data.ReasoningEffort,
		})}
	// The job lifecycle pair below is the ONLY job push on the wire, and it
	// serves every consumer: the webui folds it into subagent rows and uses it
	// as the jobs panel's refetch trigger, and the TUI applies it to its
	// transcript reducer. Job.JobID/Job.Status carry what a refetch trigger
	// needs, so no lighter-weight second notification exists for the same
	// instants (kata j7y6).
	case events.EventJobStarted:
		p.clearSkillCandidate()
		data := eventData[events.JobStartedData](event.Data)
		out := []AppNotification{p.notification(appwire.NotifySerfJobStarted, appwire.SerfJobParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Job: appwire.SerfJobInfo{
				JobID:            data.JobID,
				JobType:          data.JobType,
				Status:           data.Status,
				FromWatch:        data.FromWatch,
				Background:       data.Background,
				Command:          data.Command,
				DelegateID:       data.DelegateID,
				Task:             data.Task,
				TranscriptRef:    data.TranscriptRef,
				OriginTurnID:     data.OriginTurnID,
				OriginToolCallID: data.OriginToolCallID,
				OriginItemID:     data.OriginItemID,
			},
		})}
		if data.RootSessionID != "" && data.TreeRevision > 0 {
			out = append(out, p.notification(appwire.NotifySerfJobsTreeUpdated, appwire.JobsTreeUpdatedParams{
				ThreadID: data.RootSessionID,
				Ref:      "local:" + data.RootSessionID,
				Revision: data.TreeRevision,
			}))
		}
		return out
	case events.EventJobFinished:
		p.clearSkillCandidate()
		data := eventData[events.JobFinishedData](event.Data)
		out := []AppNotification{p.notification(appwire.NotifySerfJobFinished, appwire.SerfJobParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Job: appwire.SerfJobInfo{
				JobID:            data.JobID,
				JobType:          data.JobType,
				Status:           data.Status,
				Reason:           data.Reason,
				ExhaustionBudget: data.ExhaustionBudget,
				ExhaustionLimit:  data.ExhaustionLimit,
				Resumable:        data.Resumable,
				ExitCode:         data.ExitCode,
				OutputBytes:      data.OutputBytes,
				TranscriptRef:    data.TranscriptRef,
				FromWatch:        data.FromWatch,
				Background:       data.Background,
				Command:          data.Command,
				DelegateID:       data.DelegateID,
				Task:             data.Task,
				OriginTurnID:     data.OriginTurnID,
				OriginToolCallID: data.OriginToolCallID,
				OriginItemID:     data.OriginItemID,
			},
		})}
		if data.RootSessionID != "" && data.TreeRevision > 0 {
			out = append(out, p.notification(appwire.NotifySerfJobsTreeUpdated, appwire.JobsTreeUpdatedParams{
				ThreadID: data.RootSessionID,
				Ref:      "local:" + data.RootSessionID,
				Revision: data.TreeRevision,
			}))
		}
		return out
	case events.EventTurnEnded:
		if p.activeTurnID == "" {
			return nil // turn already completed (e.g. failed via EventError)
		}
		data := eventData[events.TurnEndedData](event.Data)
		p.pendingTurnID = p.activeTurnID
		p.pendingCompletedAtMillis = event.Timestamp.UnixMilli()
		p.pendingDurationMS = data.TurnDurationMS
		return nil
	case events.EventSessionEnd:
		p.clearSkillCandidate()
		data := eventData[events.SessionEndData](event.Data)
		state := appwire.ThreadStatusClosed
		switch data.State {
		case appwire.ThreadStatusIdle:
			state = appwire.ThreadStatusIdle
		case appwire.ThreadStatusAwaiting:
			state = appwire.ThreadStatusAwaiting
		case appwire.ThreadStatusClosed:
			state = appwire.ThreadStatusClosed
		}
		out := []AppNotification{}
		if p.activeTurnID != "" {
			turnStatus := appwire.TurnStatusCompleted
			if state == appwire.ThreadStatusClosed || data.Interrupted {
				turnStatus = appwire.TurnStatusInterrupted
			}
			turnID := p.activeTurnID
			p.activeTurnID = ""
			p.assistantItem = ""
			p.assistantText = ""
			p.reasoningItem = ""
			p.toolItemsByKey = map[string]string{}
			p.toolArgsByKey = map[string]string{}
			p.toolStartByKey = map[string]time.Time{}
			p.suppressedTools = map[string]struct{}{}
			turn := appwire.Turn{ID: turnID, Status: turnStatus}
			p.applyPendingTiming(turnID, &turn)
			p.stampTurnUsage(&turn)
			// Still map[string]any, not TurnCompletedParams - see EventUserInput's own comment above (kcb5).
			out = append(out, p.notification(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     turn,
			}))
		}
		out = append(out, p.threadStatus(state))
		if state == appwire.ThreadStatusClosed {
			// Still map[string]any, not appwire.ThreadClosedParams (kcb5):
			// data.Reason is empty whenever the source reported none (the type's
			// own doc comment), but this map always emits "reason" anyway;
			// Reason is tagged `omitempty` on the struct, so a typed literal
			// would drop the key when blank. Not provably byte-identical; left
			// as a map.
			out = append(out, p.notification(appwire.NotifyThreadClosed, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"reason":   data.Reason,
			}))
		}
		return out
	default:
		return nil
	}
}

func useSkillNameFromArgs(raw string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return ""
	}
	for _, key := range []string{"skill_name", "name"} {
		if v, ok := args[key].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func skillActivationRaw(name string) json.RawMessage {
	payload := struct {
		SkillActivation struct {
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"skillActivation"`
	}{}
	payload.SkillActivation.Name = name
	payload.SkillActivation.Text = "Activated skill: " + name
	raw, _ := json.Marshal(payload)
	return raw
}

func (p *AppEventProjector) notification(method string, params any) AppNotification {
	// Every notification carries an AppWire method name the hub routes on. The
	// callers all pass a non-empty appwire.Notify* constant; an empty method
	// would produce an unroutable wire frame.
	invariant.Hold(method != "", "appprojector: notification with empty method (threadID=%q)", p.threadID)
	return AppNotification{ThreadID: p.threadID, Method: method, Params: params}
}

// startedTurn builds an in-progress turn carrying its start time so the web UI
// can report how long the active turn has been running. A zero timestamp leaves
// StartedAt unset rather than reporting the Unix epoch.
func startedTurn(id string, startedAt time.Time) appwire.Turn {
	turn := appwire.Turn{ID: id, Status: appwire.TurnStatusInProgress}
	if !startedAt.IsZero() {
		ms := startedAt.UnixMilli()
		turn.StartedAt = &ms
	}
	return turn
}

// applyPendingTiming stamps CompletedAt/DurationMS from the most recently
// recorded EventTurnEnded onto turn, but only when that recorded turn is the
// one now completing (turnID matches). A mismatch — or no pending timing at
// all, as at the EventError site where EventTurnEnded has not run yet — leaves
// turn untouched.
func (p *AppEventProjector) applyPendingTiming(turnID string, turn *appwire.Turn) {
	if p.pendingTurnID == "" || p.pendingTurnID != turnID {
		return
	}
	c := p.pendingCompletedAtMillis
	d := p.pendingDurationMS
	turn.CompletedAt = &c
	turn.DurationMS = &d
	p.pendingTurnID = ""
	p.pendingCompletedAtMillis = 0
	p.pendingDurationMS = 0
}

// stampTurnUsage sets turn.Usage/Cost from the projector's per-turn
// accumulator (see activeTurnUsage doc). No turnID match is needed — by
// construction the accumulator always holds the completing turn's own
// totals at the moment each of the four completion sites reads it (the
// accumulator resets only in startTurn(), which the wrap-up sites call
// AFTER building the completing Turn).
func (p *AppEventProjector) stampTurnUsage(turn *appwire.Turn) {
	usage := appwire.SerfUsageFromLLM(p.activeTurnUsage)
	if usage == nil {
		return
	}
	turn.Usage = usage
	turn.Cost = appwire.EstimateCost(p.activeTurnModel, usage)
}

func (p *AppEventProjector) systemAnnouncement(eventKind appwire.ThreadItemEventKind, description, text string) []AppNotification {
	return p.systemAnnouncementItem(eventKind, description, text, nil, nil)
}

// systemAnnouncementWithRaw renders a lifecycle system one-liner like
// systemAnnouncement, additionally attaching structured detail to the item's
// Raw field. The web can then surface that detail (e.g. a compaction
// before→after expand, mockup #17 Alt A) from real numbers instead of
// re-parsing the prose text.
func (p *AppEventProjector) systemAnnouncementWithRaw(eventKind appwire.ThreadItemEventKind, description, text string, raw json.RawMessage) []AppNotification {
	return p.systemAnnouncementItem(eventKind, description, text, raw, nil)
}

// systemAnnouncementWithExitCode renders a lifecycle system one-liner like
// systemAnnouncement, additionally promoting the exit status of the process
// behind it onto the item's typed ExitCode field. Only a hook has such a
// process; the web splits "show every hook exit" from "show clean exits only"
// on this number rather than re-parsing the "... exit N" prose, so a reworded
// announcement can never change which lines a reader has chosen to see.
func (p *AppEventProjector) systemAnnouncementWithExitCode(eventKind appwire.ThreadItemEventKind, description, text string, exitCode int) []AppNotification {
	code := int64(exitCode)
	return p.systemAnnouncementItem(eventKind, description, text, nil, &code)
}

func (p *AppEventProjector) systemAnnouncementItem(eventKind appwire.ThreadItemEventKind, description, text string, raw json.RawMessage, exitCode *int64) []AppNotification {
	description = strings.TrimSpace(description)
	text = strings.TrimSpace(text)
	if text == "" && eventKind != appwire.ThreadItemEventKindPluginLoaded {
		return nil
	}
	if description == "" && text == "" {
		return nil
	}
	turnID := p.activeTurnID
	if turnID == "" {
		turnID = p.preTurnAnnouncementTurnID()
	}
	item := appwire.ThreadItem{
		Type:        "systemMessage",
		ID:          p.nextItemID(string(eventKind)),
		TurnID:      turnID,
		Description: description,
		Text:        text,
		Status:      appwire.TurnStatusCompleted,
		Raw:         raw,
		EventKind:   eventKind,
		ExitCode:    exitCode,
	}
	if p.activeTurnID == "" {
		// Still map[string]any, not TurnCompletedParams - see EventUserInput's own comment above (kcb5).
		return []AppNotification{p.notification(appwire.NotifyTurnCompleted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"turn": appwire.Turn{
				ID:        turnID,
				Items:     []appwire.ThreadItem{item},
				ItemsView: "full",
				Status:    appwire.TurnStatusCompleted,
			},
		})}
	}
	return []AppNotification{p.notification(appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
		ThreadID: p.threadID,
		Ref:      p.ref,
		TurnID:   turnID,
		Item:     item,
	})}
}

func isContextCanceledError(message string) bool {
	return strings.TrimSpace(message) == context.Canceled.Error()
}

func (p *AppEventProjector) threadStatus(status string) AppNotification {
	return p.notification(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: p.threadID,
		Ref:      p.ref,
		Status:   appwire.ThreadStatus{Type: status},
	})
}

func projectUserInputImages(images []events.UserInputImage) []appwire.InputItem {
	if len(images) == 0 {
		return nil
	}
	out := make([]appwire.InputItem, 0, len(images))
	for _, img := range images {
		out = append(out, appwire.InputItem{
			Type:      "image",
			MediaType: img.MediaType,
			Data:      append([]byte(nil), img.Data...),
			Name:      img.Name,
		})
	}
	return out
}

func projectOutputImages(images []events.OutputImage) []appwire.OutputImage {
	if len(images) == 0 {
		return nil
	}
	out := make([]appwire.OutputImage, 0, len(images))
	for _, img := range images {
		if img.URL == "" && img.SHA == "" {
			continue
		}
		out = append(out, appwire.OutputImage{
			Source:    img.Source,
			Name:      img.Name,
			MediaType: img.MediaType,
			Size:      img.Size,
			URL:       img.URL,
			SHA:       img.SHA,
			Path:      img.Path,
		})
	}
	return out
}

// goalEndText renders the terminal /goal report line from a GoalEnded payload.
// A completed goal reads "✓ Goal achieved"; a blocked goal "⊘ Goal blocked"
// (with the reason appended when present); any other terminal status falls back
// to "⊘ Goal stopped".
func goalEndText(data events.GoalEndedData) string {
	switch data.Status {
	case "complete":
		return "✓ Goal achieved"
	case "blocked":
		if reason := strings.TrimSpace(data.Reason); reason != "" {
			return "⊘ Goal blocked: " + reason
		}
		return "⊘ Goal blocked"
	default:
		return "⊘ Goal stopped"
	}
}

func turnLimitAnnouncement(data events.TurnLimitData) string {
	var lines []string
	if data.MaxTurns > 0 {
		lines = append(lines, fmt.Sprintf("Maximum turns reached: %d", data.MaxTurns))
	}
	if data.MaxToolRoundsPerInput > 0 {
		lines = append(lines, fmt.Sprintf("Maximum tool rounds per input reached: %d", data.MaxToolRoundsPerInput))
	}
	if len(lines) == 0 {
		return "Turn limit reached"
	}
	return strings.Join(lines, "\n")
}

// contextCompactionRaw marshals the structured compaction numbers under a
// "compaction" key on the system item's Raw field. The web reads these to draw
// an honest before→after expand (mockup #17 Alt A) from real numbers. Returns
// nil when there is nothing to carry so the item stays clean.
func contextCompactionRaw(data events.ContextCompactionData) json.RawMessage {
	if data.Layer == "" && data.TurnsBefore == 0 && data.TurnsAfter == 0 &&
		data.EstTokensBefore == 0 && data.EstTokensAfter == 0 {
		return nil
	}
	raw, err := marshalContextCompaction(map[string]any{"compaction": data})
	if err != nil {
		return nil
	}
	return raw
}

func contextCompactionAnnouncement(data events.ContextCompactionData) string {
	var lines []string
	if strings.TrimSpace(data.Layer) != "" {
		lines = append(lines, "Layer: "+strings.TrimSpace(data.Layer))
	}
	if data.TurnsBefore > 0 || data.TurnsAfter > 0 {
		lines = append(lines, fmt.Sprintf("Turns: %d -> %d", data.TurnsBefore, data.TurnsAfter))
	}
	if data.EstTokensBefore > 0 || data.EstTokensAfter > 0 {
		lines = append(lines, fmt.Sprintf("Estimated tokens: %d -> %d", data.EstTokensBefore, data.EstTokensAfter))
	}
	if len(lines) == 0 {
		return "Context compaction ran"
	}
	return strings.Join(lines, "\n")
}

func pluginLoadedRaw(data events.PluginLoadedData) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"pluginLoaded": struct {
			Name       string `json:"name"`
			SkillCount int    `json:"skillCount"`
			AgentCount int    `json:"agentCount"`
			MCPCount   int    `json:"mcpCount"`
		}{
			Name:       strings.TrimSpace(data.Name),
			SkillCount: data.SkillCount,
			AgentCount: data.AgentCount,
			MCPCount:   data.MCPCount,
		},
	})
	if err != nil {
		return nil
	}
	return raw
}

func pluginLoadedAnnouncement(data events.PluginLoadedData) string {
	name := strings.TrimSpace(data.Name)
	if name == "" {
		return fmt.Sprintf("Loaded plugin (%d skills, %d agents, %d MCP servers)", data.SkillCount, data.AgentCount, data.MCPCount)
	}
	return fmt.Sprintf("Loaded plugin %s (%d skills, %d agents, %d MCP servers)", name, data.SkillCount, data.AgentCount, data.MCPCount)
}

// hookEndAnnouncement renders a live hook completion through the same builder
// the persisted entry uses (schema.HookInfo.Announcement), so the line a
// watching reader sees and the line a returning one sees cannot drift apart
// (kata qm9y).
func hookEndAnnouncement(data events.HookEndData) string {
	return hookInfoFromEvent(data).Announcement()
}

// hookInfoFromEvent projects the live hook payload onto the persisted shape.
func hookInfoFromEvent(data events.HookEndData) schema.HookInfo {
	return schema.HookInfo{
		Event:      data.Event,
		HookType:   data.HookType,
		Matcher:    data.Matcher,
		PluginName: data.PluginName,
		ExitCode:   data.ExitCode,
		DurationMS: data.DurationMS,
	}
}

// toolCallRepairedAnnouncement reports that a tool call needed a small,
// automatic correction before it ran. The wire's Changes entries are the
// repair engine's own machine format ("kind:field:detail", e.g.
// "drop_unknown:artifacts:dropped artifacts") — telemetry for the CLI's raw
// event trace, never meant for a reader parsing their transcript (kata k4v8).
// This builds the reader-facing sentence instead: what changed, named by the
// tool argument involved, with no internal enum or punctuation leaking through.
func toolCallRepairedAnnouncement(data events.ToolCallRepairedData) string {
	name := fallbackLabel(data.ToolName, "tool call")
	if len(data.Changes) == 0 {
		return "Repaired " + name
	}
	phrases := make([]string, 0, len(data.Changes))
	for _, raw := range data.Changes {
		phrases = append(phrases, repairChangePhrase(raw))
	}
	return fmt.Sprintf("Fixed the %s call: %s.", name, strings.Join(phrases, "; "))
}

// repairChangePhrase turns one "kind:field:detail" repair entry into a plain
// sentence fragment. An unrecognized kind (e.g. a newer daemon's repair
// category this build predates) falls back to naming just the field, never
// the raw encoding.
func repairChangePhrase(raw string) string {
	parts := strings.SplitN(raw, ":", 3)
	kind := parts[0]
	var field string
	if len(parts) > 1 {
		field = parts[1]
	}
	switch kind {
	case "alias":
		if oldName, _, ok := strings.Cut(fieldDetail(parts), "→"); ok && oldName != "" {
			return fmt.Sprintf("renamed %q to %q", oldName, field)
		}
		return fmt.Sprintf("renamed a field to %q", field)
	case "coerce_type":
		return fmt.Sprintf("adjusted the %q field's type", field)
	case "drop_unknown":
		return fmt.Sprintf("removed the unrecognized %q field", field)
	case "unicode_repair":
		return "fixed an invalid character in the arguments"
	default:
		if field == "" {
			return "adjusted the arguments"
		}
		return fmt.Sprintf("adjusted the %q field", field)
	}
}

// fieldDetail returns the third ("detail") segment of a split "kind:field:detail"
// entry, or "" when the entry has fewer than three segments.
func fieldDetail(parts []string) string {
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

func forkSummaryAnnouncement(data events.ForkSummaryData) string {
	if data.Turn > 0 {
		return fmt.Sprintf("Fork summary captured at transcript turn %d", data.Turn)
	}
	return "Fork summary captured"
}

func promptLoadedAnnouncement(data events.PromptLoadedData) string {
	label := fallbackLabel(data.Label, "prompt")
	if data.Size > 0 {
		return fmt.Sprintf("Loaded prompt %s (%d B)", label, data.Size)
	}
	return "Loaded prompt " + label
}

// roundTimingsRaw marshals the structured per-phase durations under a
// "roundTimings" key on the system item's Raw field. The web reads these to
// draw a rounded, prioritized summary (kata 7zkv) instead of re-parsing the
// nanosecond-precision prose roundTimingsAnnouncement produces.
func roundTimingsRaw(data events.RoundTimings) json.RawMessage {
	raw, err := json.Marshal(map[string]any{"roundTimings": data})
	if err != nil {
		return nil
	}
	return raw
}

func roundTimingsAnnouncement(data events.RoundTimings) string {
	parts := []string{
		fmt.Sprintf("Round %d", data.Round),
		"total=" + data.TotalRound.String(),
		"llm=" + data.LLMCall.String(),
		"context=" + data.ContextMgmt.String(),
		"tools=" + data.ToolExec.String(),
		"prompt=" + data.SystemPrompt.String(),
		"history=" + data.HistoryExpand.String(),
		"tool_defs=" + data.ToolDefs.String(),
		"persistence=" + data.Persistence.String(),
		"after_action=" + data.AfterAction.String(),
		"overhead=" + data.LoopOverhead.String(),
	}
	return strings.Join(parts, " ")
}

func fallbackLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return fallback
}

// holdUnfetchableToolResultImages takes off a settling tool item the image
// descriptors nothing can serve yet, keeping the whole item to re-send when
// the round says they can be (kata v3dv).
//
// A descriptor whose bytes came back INSIDE the tool result is addressed by
// sha and carries no URL, because the route belongs to whichever server
// publishes the thread; those bytes reach that server only through the round's
// tool-result turn, and rounds are written whole. So between this item
// settling and that write there is no reader for them — microseconds for a
// single-call round, the length of a build for an image read batched with one
// — and a thumbnail that fails to load in that gap is dropped for good.
//
// A descriptor that already names a URL is left alone: it points at bytes some
// server can re-read on its own (the file-backed /doc/image route for a file
// the call named), so holding it would delay a thumbnail that already works.
func (p *AppEventProjector) holdUnfetchableToolResultImages(item *appwire.ThreadItem) {
	if len(item.OutputImages) == 0 {
		return
	}
	fetchable := make([]appwire.OutputImage, 0, len(item.OutputImages))
	for _, image := range item.OutputImages {
		if image.Source == events.OutputImageSourceToolResult && image.URL == "" {
			continue
		}
		fetchable = append(fetchable, image)
	}
	if len(fetchable) == len(item.OutputImages) {
		return
	}
	p.heldToolResultImages[item.CallID] = *item
	if len(fetchable) == 0 {
		fetchable = nil
	}
	item.OutputImages = fetchable
}

func (p *AppEventProjector) startTurn() string {
	if p.reservedTurnID != "" {
		p.activeTurnID = p.reservedTurnID
		p.reservedTurnID = ""
	} else {
		p.nextTurn++
		p.activeTurnID = fmt.Sprintf("turn_%d", p.nextTurn)
	}
	p.anyTurnStarted = true
	// A real turn just started, ending whatever mid-session announcement gap
	// preceded it — the next no-active-turn announcement belongs to a new
	// gap and must mint its own fresh id (kata 9ekv).
	p.midSessionAnnouncementTurnID = ""
	p.assistantItem = ""
	p.assistantText = ""
	// A round interrupted before its results were written never announces
	// them, so whatever it held belongs to a turn that is over.
	clear(p.heldToolResultImages)
	p.activeTurnUsage = llm.Usage{}
	p.activeTurnModel = ""
	// startTurn always yields a usable turn id (a promoted reservation or a
	// freshly minted turn_N); the item-emitting paths rely on activeTurnID being
	// non-empty after this returns.
	invariant.Hold(p.activeTurnID != "", "appprojector: startTurn left activeTurnID empty")
	return p.activeTurnID
}

// preTurnAnnouncementTurnID returns the turn id a systemMessage announcement
// gets when it arrives with no active turn. Before the session's first real
// turn, every such announcement — SESSION_START's plugin loads, prompt-loaded
// notices, hook/MCP warnings — shares the one synthetic
// appwire.SystemPreludeTurnID, so the client's existing consecutive-run
// grouping (SystemNoticeItem) has one turn's worth of items to fold into a
// single collapsed disclosure instead of rendering a wall of one-line turns
// (kata bz2z). It is the SAME id apptranscript.PreludeTurn uses for the
// persisted-transcript system prompt, deliberately: both mean "before any
// real turn," so a dormant session's live and replayed views agree.
//
// "Before the first real turn" is tested as !anyTurnStarted && !historySeeded,
// NOT nextTurn == 0: a RESERVED turn id (turn/start's reservation, or
// SetProcessing's auto-continuation reservation for a queued initial prompt)
// bumps nextTurn without anything having run, and a spawned session reserves
// exactly that way while plugins, prompts and hooks are still announcing.
// Testing the counter exiled that startup burst to a gap id numbered after
// turn_1 — which is how a session's "25 system events" group came to anchor
// at the END of the transcript instead of the top. The reservation is an
// intent, not a turn; the announcements still happened first.
//
// Once a real turn has started (anyTurnStarted), or the projector was seeded
// over a resumed session's persisted history (historySeeded), a
// no-active-turn announcement falls into the GAP after whichever real turn
// just ended. Announcements landing back-to-back in the same gap (no real
// turn started in between) share ONE turn id — same grouping rationale as
// the prelude, so a burst of hook completions between two turns folds into
// one disclosure instead of a wall of one-line turns (kata 9ekv) — but each
// gap mints its OWN fresh id rather than reusing the prelude's or an earlier
// gap's: it happened AFTER its preceding real turn, not before it, and
// folding two different gaps into one bucket would misrepresent when each
// happened relative to the real turns between them. startTurn clears
// midSessionAnnouncementTurnID whenever a real turn starts, so the next gap
// always gets a fresh id.
func (p *AppEventProjector) preTurnAnnouncementTurnID() string {
	if !p.anyTurnStarted && !p.historySeeded {
		return appwire.SystemPreludeTurnID
	}
	if p.midSessionAnnouncementTurnID != "" {
		return p.midSessionAnnouncementTurnID
	}
	p.nextTurn++
	p.midSessionAnnouncementTurnID = fmt.Sprintf("turn_%d", p.nextTurn)
	return p.midSessionAnnouncementTurnID
}

func (p *AppEventProjector) ReserveTurnID() string {
	if p.reservedTurnID != "" {
		return p.reservedTurnID
	}
	p.nextTurn++
	p.reservedTurnID = fmt.Sprintf("turn_%d", p.nextTurn)
	return p.reservedTurnID
}

func (p *AppEventProjector) ReleaseReservedTurnID(turnID string) {
	if p.reservedTurnID == turnID {
		p.reservedTurnID = ""
	}
}

func (p *AppEventProjector) ActiveTurnID() string {
	if p.activeTurnID != "" {
		return p.activeTurnID
	}
	return p.reservedTurnID
}

// ensureTurn makes sure a turn is open for the round, returning the
// turn/started announcement for a turn it had to open (nil when one was
// already open, so the caller can concatenate unconditionally).
//
// A turn opened here was never asked for by a user input or a goal
// continuation -- it is the round's first event finding nothing open, at
// TEXT_START, TOOL_CALL_START or a bare error. It still announces itself the
// way those two explicit openers do: a client keys "this turn is running" on
// turn/started, and a turn that first appears with its own first item (or, for
// a text-opening round after lazy agent-message materialization, only with its
// first delta) is a turn that client never saw open (kata e5r2).
func (p *AppEventProjector) ensureTurn(startedAt time.Time) []AppNotification {
	if p.activeTurnID != "" {
		return nil
	}
	turnID := p.startTurn()
	return []AppNotification{p.notification(appwire.NotifyTurnStarted, appwire.TurnStartedParams{
		ThreadID: p.threadID,
		Ref:      p.ref,
		Turn:     startedTurn(turnID, startedAt),
	})}
}

// ensureAssistantItem makes sure an agent-message item exists for the active
// turn, returning true when it had to create one (so the caller emits a single
// item/started ahead of the first delta -- consumers key a delta by an item id
// they have already seen) alongside whatever ensureTurn had to announce first.
func (p *AppEventProjector) ensureAssistantItem(startedAt time.Time) (bool, []AppNotification) {
	out := p.ensureTurn(startedAt)
	if p.assistantItem == "" {
		p.assistantItem = p.nextItemID("assistant")
		return true, out
	}
	return false, out
}

// ensureReasoningItem makes sure an in-progress reasoning item exists for the
// active turn, returning true when it had to create one (so the caller emits a
// single item/started before the first delta) alongside whatever ensureTurn had
// to announce first.
func (p *AppEventProjector) ensureReasoningItem(startedAt time.Time) (bool, []AppNotification) {
	out := p.ensureTurn(startedAt)
	if p.reasoningItem == "" {
		p.reasoningItem = p.nextItemID("reasoning")
		return true, out
	}
	return false, out
}

func (p *AppEventProjector) nextItemID(prefix string) string {
	p.nextItem++
	return fmt.Sprintf("item_%s_%d", prefix, p.nextItem)
}

func (p *AppEventProjector) toolItemID(callID string) string {
	if itemID := p.toolItemsByKey[callID]; itemID != "" {
		return itemID
	}
	itemID := p.nextItemID("tool")
	p.toolItemsByKey[callID] = itemID
	return itemID
}

func (p *AppEventProjector) recordAssistantMessage(turnID, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	p.lastAssistantTurnID = turnID
	p.lastAssistantText = text
}

func (p *AppEventProjector) matchesLastAssistantMessage(turnID, text string) bool {
	return turnID != "" &&
		turnID == p.lastAssistantTurnID &&
		strings.TrimSpace(text) == p.lastAssistantText
}

// projectErrorCause maps the agent-side structured cause attached to
// EventError (kata ts0x) to its wire-level appwire shape (kata cmfz).
// Returns nil when the caller did not attach a cause so the warning
// envelope's "cause" field stays omitempty-eligible on the wire.
func projectErrorCause(cause *events.ErrorCause) *appwire.DiagnosticCause {
	if cause == nil {
		return nil
	}
	return &appwire.DiagnosticCause{
		Kind:     cause.Kind,
		Provider: cause.Provider,
		Model:    cause.Model,
		Status:   cause.Status,
	}
}

// eventData returns the concrete payload carried by a SessionEvent.Data value.
// Data is now the sealed events.EventData interface holding the exact payload
// the emit site constructed, so a direct type assertion is authoritative and
// the former JSON marshal/unmarshal round-trip is gone. A mismatched T (a
// projector bug) yields the zero value rather than panicking.
func eventData[T events.EventData](data events.EventData) T {
	typed, _ := data.(T)
	return typed
}
