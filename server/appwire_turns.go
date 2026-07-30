package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/apptranscript"
)

func useTranscriptTurns(transcriptTurns, notificationTurns []appwire.Turn) bool {
	if len(transcriptTurns) == 0 {
		return false
	}
	if len(notificationTurns) == 0 {
		return true
	}
	if len(transcriptTurns) > len(notificationTurns) {
		return true
	}
	return notificationTurns[0].ID != "turn_1"
}

// transcriptTurnCache memoizes transcript-file parsing by file identity so the
// repeated reads driven by lazy turn paging don't re-parse the whole transcript
// each page. One daemon serves one session, so a small cache suffices.
var transcriptTurnCache = apptranscript.NewTurnCache()

var (
	appTurnsEnsureTurnHook   func(string) bool
	appTurnsItemForDeltaHook func(*appwire.ThreadItem)
)

func appTurnsFromTranscriptFile(path string) ([]appwire.Turn, error) {
	toolNames := map[string]string{}
	return transcriptTurnCache.TurnsFromFile(path, 128<<20, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		return projectTranscriptTurn(turn, turnID, entryIndex, toolNames)
	})
}

// projectTranscriptTurn projects an already-decoded transcript turn (decoded
// once by apptranscript's own reader, not here — kata j13r) into AppWire
// items.
func projectTranscriptTurn(turn schema.Turn, turnID string, entryIndex int, toolNames map[string]string) []appwire.ThreadItem {
	return apptranscript.ProjectTurn(turnID, entryIndex, turn, toolNames, nil, nil)
}

func projectBoundedDaemonTranscriptTurn(turn schema.Turn, turnID string, entryIndex int, toolNames map[string]string) []appwire.ThreadItem {
	return projectTranscriptTurn(turn, turnID, entryIndex, toolNames)
}

type appTurnSnapshot struct {
	mu            sync.Mutex
	threadID      string
	limit         int
	cursor        uint64
	retainedLower uint64
	records       []appserver.SequencedNotification
	turns         []appwire.Turn
	turnIndex     map[string]int
	// activeTurnID names the turn steering attaches to. Steering is the one
	// notification that does not carry its own turn ID, so the reducer has to
	// remember which turn is in flight.
	activeTurnID string
}

// Seed installs a full projection as the snapshot's starting state, replacing
// anything already reduced. The caller keeps ownership of turns: every turn and
// nested item is deep-cloned, so later mutation of the argument cannot reach
// installed state.
//
// The replay bookkeeping is reset alongside the turns. Until Task 4 removes it,
// applyLocked rebuilds turn state from the retained record window whenever that
// window trims or a record arrives out of order -- which would discard the
// seeded turns entirely and leave activeTurnID naming a turn no longer in the
// index, after which every steer is silently dropped.
func (s *appTurnSnapshot) Seed(turns []appwire.Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.records = nil
	s.cursor = 0
	s.retainedLower = 0
	s.turns = make([]appwire.Turn, len(turns))
	s.turnIndex = make(map[string]int, len(turns))
	s.activeTurnID = ""
	for i := range turns {
		s.turns[i] = cloneAppTurn(turns[i])
		s.turnIndex[s.turns[i].ID] = i
		// The last in-progress turn wins. This does not arise from a transcript
		// projection -- apptranscript stamps every turn completed or failed, so
		// a transcript seed always leaves activeTurnID empty -- but a seed taken
		// from a wire snapshot (thread.turns) can carry one, and there the most
		// recent in-progress turn is the one still streaming.
		//
		// Note this does not consult thread.serf.activeTurnId, which the daemon
		// publishes separately and which can name a reserved turn absent from
		// turns entirely; the frontend prefers that field and falls back to the
		// FIRST in-progress turn.
		if s.turns[i].Status == appwire.TurnStatusInProgress {
			s.activeTurnID = s.turns[i].ID
		}
	}
}

func (s *appTurnSnapshot) Apply(records []appserver.SequencedNotification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyLocked(records)
}

func (s *appTurnSnapshot) applyLocked(records []appserver.SequencedNotification) {
	var appended []appserver.SequencedNotification
	rebuild := false
	for _, record := range records {
		if s.retainedLower > 0 && record.Seq < s.retainedLower {
			continue
		}
		if record.Seq <= s.cursor {
			found := false
			for _, retained := range s.records {
				if retained.Seq == record.Seq {
					found = true
					break
				}
			}
			if found {
				continue
			}
			rebuild = true
		} else {
			s.cursor = record.Seq
		}
		s.records = append(s.records, record)
		appended = append(appended, record)
	}
	if len(appended) == 0 {
		return
	}
	apply := appended
	if rebuild {
		sort.Slice(s.records, func(i, j int) bool { return s.records[i].Seq < s.records[j].Seq })
		s.turns = nil
		s.turnIndex = nil
		apply = s.records
	}
	if s.limit > 0 && len(s.records) > s.limit {
		s.records = append([]appserver.SequencedNotification(nil), s.records[len(s.records)-s.limit:]...)
		s.turns = nil
		s.turnIndex = nil
		apply = s.records
	}
	if s.turnIndex == nil {
		s.turnIndex = map[string]int{}
	}

	ensureTurn := func(id string) *appwire.Turn {
		if strings.TrimSpace(id) == "" {
			return nil
		}
		if appTurnsEnsureTurnHook != nil && appTurnsEnsureTurnHook(id) {
			return nil
		}
		if idx, ok := s.turnIndex[id]; ok {
			return &s.turns[idx]
		}
		s.turns = append(s.turns, appwire.Turn{ID: id, ItemsView: "full", Status: appwire.TurnStatusInProgress})
		s.turnIndex[id] = len(s.turns) - 1
		return &s.turns[len(s.turns)-1]
	}
	upsertItem := func(turnID string, item appwire.ThreadItem) {
		if item.ID == "" {
			return
		}
		if item.TurnID == "" {
			item.TurnID = turnID
		}
		turn := ensureTurn(item.TurnID)
		if turn == nil {
			return
		}
		for i := range turn.Items {
			if turn.Items[i].ID == item.ID {
				turn.Items[i] = mergeAppThreadItem(turn.Items[i], item)
				return
			}
		}
		turn.Items = append(turn.Items, item)
	}
	itemForDelta := func(turnID, itemID, itemType string) *appwire.ThreadItem {
		if strings.TrimSpace(itemID) == "" {
			return nil
		}
		turn := ensureTurn(turnID)
		if turn == nil {
			return nil
		}
		for i := range turn.Items {
			if turn.Items[i].ID == itemID {
				if appTurnsItemForDeltaHook != nil {
					appTurnsItemForDeltaHook(&turn.Items[i])
				}
				if turn.Items[i].TurnID == "" {
					turn.Items[i].TurnID = turnID
				}
				if turn.Items[i].Type == "" {
					turn.Items[i].Type = itemType
				}
				if turn.Items[i].Status == "" {
					turn.Items[i].Status = appwire.TurnStatusInProgress
				}
				return &turn.Items[i]
			}
		}
		turn.Items = append(turn.Items, appwire.ThreadItem{Type: itemType, ID: itemID, TurnID: turnID, Status: appwire.TurnStatusInProgress})
		return &turn.Items[len(turn.Items)-1]
	}

	for _, record := range apply {
		switch record.Notification.Method {
		case appwire.NotifyTurnStarted:
			var params appwire.TurnStartedParams
			if json.Unmarshal(record.Notification.Params, &params) != nil || params.Turn.ID == "" {
				continue
			}
			turn := ensureTurn(params.Turn.ID)
			if turn == nil {
				continue
			}
			s.activeTurnID = params.Turn.ID
			if params.Turn.ItemsView != "" {
				turn.ItemsView = params.Turn.ItemsView
			}
			if params.Turn.Status != "" {
				turn.Status = params.Turn.Status
			}
			if params.Turn.StartedAt != nil {
				turn.StartedAt = params.Turn.StartedAt
			}
		case appwire.NotifyItemStarted, appwire.NotifyItemCompleted:
			var params appwire.ItemLifecycleParams
			if json.Unmarshal(record.Notification.Params, &params) == nil {
				upsertItem(params.TurnID, params.Item)
			}
		case appwire.NotifyAgentMessageDelta:
			var params appwire.AgentMessageDeltaParams
			if json.Unmarshal(record.Notification.Params, &params) == nil {
				if item := itemForDelta(params.TurnID, params.ItemID, "agentMessage"); item != nil {
					item.Text += params.Delta
				}
			}
		case appwire.NotifyReasoningSummaryDelta:
			var params appwire.ReasoningSummaryDeltaParams
			if json.Unmarshal(record.Notification.Params, &params) == nil {
				if item := itemForDelta(params.TurnID, params.ItemID, "reasoning"); item != nil {
					item.Text += params.Delta
				}
			}
		case appwire.NotifyToolOutputDelta:
			var params appwire.ToolOutputDeltaParams
			if json.Unmarshal(record.Notification.Params, &params) != nil {
				continue
			}
			itemID := params.ItemID
			if itemID == "" {
				itemID = params.CallID
			}
			if item := itemForDelta(params.TurnID, itemID, "commandExecution"); item != nil {
				if item.CallID == "" {
					item.CallID = params.CallID
				}
				item.Output += params.Delta
			}
		case appwire.NotifyTurnCompleted:
			var params appwire.TurnCompletedParams
			if json.Unmarshal(record.Notification.Params, &params) != nil || params.Turn.ID == "" {
				continue
			}
			turn := ensureTurn(params.Turn.ID)
			if turn == nil {
				continue
			}
			if params.Turn.ItemsView != "" {
				turn.ItemsView = params.Turn.ItemsView
			}
			if params.Turn.Status != "" {
				turn.Status = params.Turn.Status
			}
			if params.Turn.CompletedAt != nil {
				turn.CompletedAt = params.Turn.CompletedAt
			}
			if params.Turn.DurationMS != nil {
				turn.DurationMS = params.Turn.DurationMS
			}
			turn.Error = params.Turn.Error
			if s.activeTurnID == params.Turn.ID {
				s.activeTurnID = ""
			}
			for _, item := range params.Turn.Items {
				upsertItem(params.Turn.ID, item)
			}
		case appwire.NotifyAgentMessageReset:
			// A retried model call discards the partial it already streamed.
			// Only the named item on the named turn goes; an unknown turn is
			// left alone rather than fabricated, since there would be nothing
			// to remove from it.
			//
			// This trusts params.TurnID, where the frontend's findItemTurnId
			// falls back to the active turn and then scans every turn. The
			// stricter lookup is safe because the projector stamps the reset
			// with its own activeTurnID and only emits one while an assistant
			// item is open (internal/appprojector/appwire_projection.go), and
			// every site that clears activeTurnID clears that item too -- so a
			// reset naming an absent or empty turn is not reachable.
			var params appwire.AgentMessageResetParams
			if json.Unmarshal(record.Notification.Params, &params) != nil || params.ItemID == "" {
				continue
			}
			idx, ok := s.turnIndex[params.TurnID]
			if !ok {
				continue
			}
			turn := &s.turns[idx]
			for i := range turn.Items {
				if turn.Items[i].ID == params.ItemID {
					turn.Items = append(turn.Items[:i], turn.Items[i+1:]...)
					break
				}
			}
		case appwire.NotifySerfSteeringInjected:
			// Steering carries no turn ID: the daemon only injects into the
			// turn already in flight. With no active turn there is nowhere
			// wire-true to put it, and inventing one would publish a turn the
			// daemon never started -- that race is recovered by the next
			// authoritative snapshot instead.
			var params appwire.SerfSteeringInjectedParams
			if json.Unmarshal(record.Notification.Params, &params) != nil {
				continue
			}
			idx, ok := s.turnIndex[s.activeTurnID]
			if s.activeTurnID == "" || !ok {
				continue
			}
			turn := &s.turns[idx]
			// Index per turn, not globally, matching the frontend reducer
			// (cmd/serf-hub/frontend/src/protocol/reducer.ts:777-790) and the
			// transcript reload shape it mirrors.
			steeringCount := 0
			for i := range turn.Items {
				if turn.Items[i].Type == "steering" {
					steeringCount++
				}
			}
			turn.Items = append(turn.Items, appwire.ThreadItem{
				Type:             "steering",
				ID:               fmt.Sprintf("item_steering_live_%s_%d", s.activeTurnID, steeringCount),
				TurnID:           s.activeTurnID,
				Text:             params.Text,
				Images:           params.Images,
				Status:           appwire.TurnStatusCompleted,
				Source:           params.Source,
				SteeringKind:     params.Kind,
				ClientMutationID: params.ClientMutationID,
			})
		}
	}
}

func (s *appTurnSnapshot) Cursor() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}

func (s *appTurnSnapshot) ReconcileAndSnapshot(lowerSeq uint64, records []appserver.SequencedNotification) []appwire.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retainedLower = lowerSeq
	exact := len(s.records) == len(records)
	if exact {
		for i := range records {
			if s.records[i].Seq != records[i].Seq {
				exact = false
				break
			}
		}
	}
	if !exact {
		s.cursor = 0
		s.records = nil
		s.turns = nil
		s.turnIndex = nil
		s.applyLocked(records)
	}
	return s.snapshotLocked()
}

func (s *appTurnSnapshot) Snapshot() []appwire.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *appTurnSnapshot) snapshotLocked() []appwire.Turn {
	turns := make([]appwire.Turn, len(s.turns))
	for i := range turns {
		turns[i] = cloneAppTurn(s.turns[i])
	}
	return turns
}

func cloneAppTurn(turn appwire.Turn) appwire.Turn {
	clone := turn
	clone.StartedAt = cloneInt64(turn.StartedAt)
	clone.CompletedAt = cloneInt64(turn.CompletedAt)
	clone.DurationMS = cloneInt64(turn.DurationMS)
	if turn.Usage != nil {
		usage := *turn.Usage
		clone.Usage = &usage
	}
	if turn.Error != nil {
		errorCopy := *turn.Error
		if turn.Error.Cause != nil {
			cause := *turn.Error.Cause
			errorCopy.Cause = &cause
		}
		errorCopy.CodexErrorInfo = cloneJSONCompatible(turn.Error.CodexErrorInfo)
		clone.Error = &errorCopy
	}
	clone.Items = make([]appwire.ThreadItem, len(turn.Items))
	for i := range turn.Items {
		clone.Items[i] = cloneAppThreadItem(turn.Items[i])
	}
	return clone
}

func cloneAppThreadItem(item appwire.ThreadItem) appwire.ThreadItem {
	clone := item
	clone.StartedAt = cloneInt64(item.StartedAt)
	clone.CompletedAt = cloneInt64(item.CompletedAt)
	clone.DurationMS = cloneInt64(item.DurationMS)
	clone.ExitCode = cloneInt64(item.ExitCode)
	clone.Raw = append(json.RawMessage(nil), item.Raw...)
	clone.OutputImages = append([]appwire.OutputImage(nil), item.OutputImages...)
	clone.Images = make([]appwire.InputItem, len(item.Images))
	for i := range item.Images {
		clone.Images[i] = item.Images[i]
		clone.Images[i].Data = append([]byte(nil), item.Images[i].Data...)
		if item.Images[i].Metadata != nil {
			clone.Images[i].Metadata = make(map[string]string, len(item.Images[i].Metadata))
			maps.Copy(clone.Images[i].Metadata, item.Images[i].Metadata)
		}
	}
	return clone
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneJSONCompatible(value any) any {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var clone any
	if json.Unmarshal(data, &clone) != nil {
		return value
	}
	return clone
}

func appTurnsFromNotifications(records []appserver.SequencedNotification) []appwire.Turn {
	// This is the legacy/reference input-order projector. Supplied sequence values
	// are irrelevant here; production sequenced reduction uses Apply directly.
	sequenced := append([]appserver.SequencedNotification(nil), records...)
	for i := range sequenced {
		sequenced[i].Seq = uint64(i + 1)
	}
	snapshot := &appTurnSnapshot{}
	snapshot.Apply(sequenced)
	return snapshot.Snapshot()
}

func mergeAppThreadItem(existing, incoming appwire.ThreadItem) appwire.ThreadItem {
	if incoming.Type == "" {
		incoming.Type = existing.Type
	}
	if incoming.TurnID == "" {
		incoming.TurnID = existing.TurnID
	}
	if incoming.Text == "" {
		incoming.Text = existing.Text
	}
	if incoming.Delta == "" {
		incoming.Delta = existing.Delta
	}
	if len(incoming.Images) == 0 {
		incoming.Images = existing.Images
	}
	if incoming.ToolName == "" {
		incoming.ToolName = existing.ToolName
	}
	if incoming.CallID == "" {
		incoming.CallID = existing.CallID
	}
	if incoming.ArgumentsJSON == "" {
		incoming.ArgumentsJSON = existing.ArgumentsJSON
	}
	if incoming.Description == "" {
		incoming.Description = existing.Description
	}
	if incoming.Output == "" {
		incoming.Output = existing.Output
	}
	if incoming.Error == "" {
		incoming.Error = existing.Error
	}
	if incoming.Status == "" {
		incoming.Status = existing.Status
	}
	if incoming.StartedAt == nil {
		incoming.StartedAt = existing.StartedAt
	}
	if incoming.CompletedAt == nil {
		incoming.CompletedAt = existing.CompletedAt
	}
	if len(incoming.Raw) == 0 {
		incoming.Raw = existing.Raw
	}
	return incoming
}
