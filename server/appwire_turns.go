package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/apptranscript"
)

// appTranscriptMaxLineBytes bounds a single transcript line. It is the same
// ceiling the hub's reader uses; a line beyond it is a corrupt file, not a
// large turn.
const appTranscriptMaxLineBytes = 128 << 20

var (
	appTurnsEnsureTurnHook   func(string) bool
	appTurnsItemForDeltaHook func(*appwire.ThreadItem)
)

// appTurnsFromTranscriptFile projects a whole session transcript into AppWire
// turns. This runs once per identity, at PrepareAppIdentity time, and never on
// a read: the installed snapshot is the sole authority for every daemon turn
// read, so nothing reopens this file to answer an RPC.
//
// It also reports the highest entry index the projection consumed. Persisted
// turn ids are "turn_<entry index>", so that figure is the floor a live
// projector's own turn counter has to start above.
func appTurnsFromTranscriptFile(path string) ([]appwire.Turn, int, error) {
	toolNames := map[string]string{}
	entries := 0
	turns, err := apptranscript.TurnsFromFile(path, appTranscriptMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		if entryIndex > entries {
			entries = entryIndex
		}
		return apptranscript.ProjectTurn(turnID, entryIndex, turn, toolNames, nil, nil)
	})
	return turns, entries, err
}

type appTurnSnapshot struct {
	mu        sync.Mutex
	threadID  string
	turns     []appwire.Turn
	turnIndex map[string]int
	// activeTurnID names the turn steering ITEMS attach to. Steering is the one
	// notification that does not carry its own turn ID, so the reducer has to
	// remember which turn is in flight.
	//
	// This deliberately answers a different question from the daemon's
	// s.appActiveTurnID, published as thread.serf.activeTurnId. That field
	// answers "is a turn in flight or reserved?" and is set from
	// AppEventProjector.ReserveTurnID before any turn/started notification
	// exists, so it can name a RESERVED turn that is absent from turns
	// entirely -- it gates capabilities, not item placement. A steering item
	// cannot attach to a reserved turn: there is no turn object to append to,
	// and fabricating one would publish a turn the daemon never started. Do
	// not collapse the two fields into one.
	activeTurnID string
}

// Seed installs a full projection as the snapshot's starting state, replacing
// anything already reduced. The caller keeps ownership of turns: every turn and
// nested item is deep-cloned, so later mutation of the argument cannot reach
// installed state.
func (s *appTurnSnapshot) Seed(turns []appwire.Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
		if s.turns[i].Status == appwire.TurnStatusInProgress {
			s.activeTurnID = s.turns[i].ID
		}
	}
}

// Apply reduces committed notification records into snapshot state, in the
// order they were committed. Notification retention is transport replay only:
// a record evicted from the notifier's replay window has already been reduced
// here and stays part of the thread.
func (s *appTurnSnapshot) Apply(records []appserver.SequencedNotification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyLocked(records)
}

func (s *appTurnSnapshot) applyLocked(records []appserver.SequencedNotification) {
	if len(records) == 0 {
		return
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

	for _, record := range records {
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

// appTurnsFromNotifications reduces records into a fresh snapshot, in the order
// given. It is the reference reduction used to check a notification stream
// independently of whatever the installed snapshot already holds.
func appTurnsFromNotifications(records []appserver.SequencedNotification) []appwire.Turn {
	snapshot := &appTurnSnapshot{}
	snapshot.Apply(records)
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
