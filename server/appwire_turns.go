package server

import (
	"encoding/json"
	"strings"
	"sync"

	"primeradiant.com/serf/agent/transcript"
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

func appTurnsFromTranscriptFile(path string) []appwire.Turn {
	toolNames := map[string]string{}
	return transcriptTurnCache.TurnsFromFile(path, 128<<20, func(raw json.RawMessage, turnID string, entryIndex int) []appwire.ThreadItem {
		return projectTranscriptTurn(raw, turnID, entryIndex, toolNames)
	})
}

func projectTranscriptTurn(raw json.RawMessage, turnID string, entryIndex int, toolNames map[string]string) []appwire.ThreadItem {
	var entry transcript.Entry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil
	}
	return apptranscript.ProjectTurn(turnID, entryIndex, entry.Turn, toolNames, nil, nil)
}

func projectBoundedDaemonTranscriptTurn(raw json.RawMessage, turnID string, entryIndex int, toolNames map[string]string) []appwire.ThreadItem {
	return projectTranscriptTurn(raw, turnID, entryIndex, toolNames)
}

type appTurnSnapshot struct {
	mu                       sync.Mutex
	cursor                   uint64
	turns                    []appwire.Turn
	turnIndex                map[string]int
	transcriptAuthorityKnown bool
	transcriptAuthoritative  bool
}

func (s *appTurnSnapshot) Apply(records []appserver.SequencedNotification) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		if record.Seq <= s.cursor {
			continue
		}
		s.cursor = record.Seq
		switch record.Notification.Method {
		case appwire.NotifyTurnStarted:
			var params struct {
				Turn appwire.Turn `json:"turn"`
			}
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
			if params.Turn.StartedAt != nil {
				turn.StartedAt = params.Turn.StartedAt
			}
		case appwire.NotifyItemStarted, appwire.NotifyItemCompleted:
			var params struct {
				TurnID string             `json:"turnId"`
				Item   appwire.ThreadItem `json:"item"`
			}
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
			var params struct {
				TurnID string `json:"turnId"`
				ItemID string `json:"itemId"`
				CallID string `json:"callId"`
				Delta  string `json:"delta"`
			}
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
			var params struct {
				Turn appwire.Turn `json:"turn"`
			}
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
			for _, item := range params.Turn.Items {
				upsertItem(params.Turn.ID, item)
			}
		}
	}
}

func (s *appTurnSnapshot) Cursor() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}

func (s *appTurnSnapshot) Snapshot() []appwire.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	turns := append([]appwire.Turn(nil), s.turns...)
	for i := range turns {
		turns[i].Items = append([]appwire.ThreadItem(nil), turns[i].Items...)
	}
	return turns
}

func (s *appTurnSnapshot) SetTranscriptAuthority(authoritative bool) {
	s.mu.Lock()
	s.transcriptAuthorityKnown = true
	s.transcriptAuthoritative = authoritative
	s.mu.Unlock()
}

func (s *appTurnSnapshot) TranscriptAuthority() (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcriptAuthoritative, s.transcriptAuthorityKnown
}

func appTurnsFromNotifications(records []appserver.SequencedNotification) []appwire.Turn {
	// Unit/reference callers may construct unsequenced records. Production
	// notifier records start at one; assign equivalent local ordinals here so the
	// incremental reducer's cursor rule does not change legacy projection.
	sequenced := append([]appserver.SequencedNotification(nil), records...)
	for i := range sequenced {
		if sequenced[i].Seq == 0 {
			sequenced[i].Seq = uint64(i + 1)
		}
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
