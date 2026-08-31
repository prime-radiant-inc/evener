package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/apptranscript"
)

// appTranscriptMaxLineBytes bounds a single transcript line. It is the same
// ceiling the hub's reader uses; a line beyond it is a corrupt file, not a
// large turn.
const appTranscriptMaxLineBytes = 128 << 20

// transcriptHeaderReadBufferBytes bounds the prefetch for an ordinary header
// line. The parser stops at that line; keeping this buffer finite prevents a
// large historical transcript from crossing the reader boundary merely because
// identity validation needs its header. Oversized headers are still governed
// by the maxLineBytes limit passed to transcript.ReadLine.
const transcriptHeaderReadBufferBytes = 64 * 1024

var (
	appTurnsEnsureTurnHook   func(string) bool
	appTurnsItemForDeltaHook func(*appwire.ThreadItem)
)

// appTurnsFromTranscriptFile projects a whole session transcript file into
// AppWire turns. This runs once per identity, at PrepareAppIdentity time, and
// never on a read: the installed snapshot is the sole authority for every
// daemon turn read, so nothing reopens this file to answer an RPC.
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
		return apptranscript.ProjectTurn(turnID, entryIndex, turn, toolNames, nil, apptranscript.ToolResultOutputImages)
	})
	return turns, entries, err
}

// appTurnsFromEntries projects already-decoded transcript entries into
// AppWire turns. It is the in-memory form of appTurnsFromTranscriptFile: the
// resume path strict-decodes the transcript once (OpenWriterForSession) and
// hands the retained entries here rather than re-reading the file.
//
// The shared projector below guarantees the two forms agree: entry indexing
// (and therefore every persisted turn id) is 1-based over the entries in
// order, identical to the file scan's callback indexing, and the header is
// the same header the file scan read.
func appTurnsFromEntries(header transcript.Header, entries []transcript.Entry) ([]appwire.Turn, int, error) {
	toolNames := map[string]string{}
	highest := 0
	turns, err := apptranscript.TurnsFromEntries(header, entries, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		if entryIndex > highest {
			highest = entryIndex
		}
		return apptranscript.ProjectTurn(turnID, entryIndex, turn, toolNames, nil, apptranscript.ToolResultOutputImages)
	})
	return turns, highest, err
}

type appTurnSnapshot struct {
	mu        sync.Mutex
	threadID  string
	turns     []appwire.Turn
	turnIndex map[string]int
	// prefixTurnCount is the number of turn positions a windowed seed holds
	// BELOW s.turns (zero for a full seed). Cursors Latest and Page hand out
	// are expressed in the FULL projection's position space — the same
	// space the hub's file-backed paging uses — so a client can page across
	// the seam. Pages that fall entirely below the window return no turns;
	// the hub serves them from the transcript. Guarded by mu.
	prefixTurnCount int
	// activeTurnID names the turn steering ITEMS attach to. Steering is the one
	// notification that does not carry its own turn ID, so the reducer has to
	// remember which turn is in flight.
	//
	// This deliberately answers a different question from the daemon's
	// s.appActiveTurnID, published as thread.evener.activeTurnId. That field
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
	s.SeedWindowed(turns, 0)
}

// SeedWindowed is Seed for a suffix projection: prefixTurnCount is the number
// of turn positions the full projection holds BELOW these turns. Turn ids are
// global either way (the caller minted them with global entry positions), so
// only paging is affected: Latest and Page express their cursors in the
// full-projection position space, and pages below the window return no turns
// — the hub serves those from its own file-backed paging of the same
// transcript.
func (s *appTurnSnapshot) SeedWindowed(turns []appwire.Turn, prefixTurnCount int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.prefixTurnCount = prefixTurnCount
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
		if id == appwire.SystemPreludeTurnID {
			// A windowed seed's prefix is unseen turn positions served by
			// the hub from the transcript: the prelude already lives there
			// (its position is the oldest by definition), so inserting a
			// fresh copy here would double it and shift the window's
			// positions into the prefix's cursor space. Report nothing; the
			// client reads the prelude from the prefix pages.
			if s.prefixTurnCount > 0 {
				return nil
			}
			// The prelude turn is the one turn whose id fixes its position:
			// it holds content from before the session's first real turn by
			// definition (apptranscript.PreludeTurn, appprojector's bundled
			// SESSION_START announcements), so it belongs at the front
			// however late its first notification arrives. Nothing orders a
			// session's first turn-starting request ahead of its startup
			// announcements (PrepareAppIdentity says so), so an append here
			// pins the "N system events" group a reader expects at the top
			// to the END of the transcript instead. Inserting shifts every
			// existing turn's position, so the index is rebuilt rather than
			// patched; this happens at most once per identity.
			s.turns = append([]appwire.Turn{{ID: id, ItemsView: "full", Status: appwire.TurnStatusInProgress}}, s.turns...)
			s.turnIndex = make(map[string]int, len(s.turns))
			for i := range s.turns {
				s.turnIndex[s.turns[i].ID] = i
			}
			return &s.turns[0]
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
		case appwire.NotifyEvenerSteeringInjected:
			// Steering carries no turn ID: the daemon only injects into the
			// turn already in flight. With no active turn there is nowhere
			// wire-true to put it, and inventing one would publish a turn the
			// daemon never started -- that race is recovered by the next
			// authoritative snapshot instead.
			var params appwire.EvenerSteeringInjectedParams
			if json.Unmarshal(record.Notification.Params, &params) != nil {
				continue
			}
			idx, ok := s.turnIndex[s.activeTurnID]
			if s.activeTurnID == "" || !ok {
				continue
			}
			turn := &s.turns[idx]
			// Index per turn, not globally, matching the frontend reducer
			// (cmd/evener-hub/frontend/src/protocol/reducer.ts:777-790) and the
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

func (s *appTurnSnapshot) Latest(limit int) ([]appwire.Turn, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prefixTurnCount > 0 {
		// Windowed seed: page in the full-projection position space. A
		// window smaller than the limit extends below the window; the
		// cursor then points at the prefix boundary and the hub serves the
		// older turns from its file-backed paging.
		total := s.prefixTurnCount + len(s.turns)
		if limit <= 0 || total <= limit {
			return cloneAppTurns(s.turns), strconv.Itoa(s.prefixTurnCount)
		}
		lo := len(s.turns) - limit
		return cloneAppTurns(s.turns[lo:]), strconv.Itoa(total - limit)
	}
	turns, cursor := appwire.WindowTurns(s.turns, limit)
	return cloneAppTurns(turns), cursor
}

func (s *appTurnSnapshot) Page(cursor string, limit int) appwire.ThreadTurnsListResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prefixTurnCount > 0 {
		// Cursor is an exclusive upper bound in the full position space.
		// Pages above the prefix boundary serve from the window; pages at or
		// below it return empty data (NextCursor preserves the client's
		// position) — the hub's fallback answers those from the transcript.
		hi := s.prefixTurnCount + len(s.turns)
		if cursor != "" {
			if c, err := strconv.Atoi(cursor); err == nil {
				hi = c
			}
		}
		if hi > s.prefixTurnCount+len(s.turns) {
			hi = s.prefixTurnCount + len(s.turns)
		}
		if hi <= s.prefixTurnCount {
			return appwire.ThreadTurnsListResponse{Data: nil, NextCursor: cursor}
		}
		// Window-local bounds: the window's turns occupy global positions
		// [prefixTurnCount, prefixTurnCount+len(turns)). The page's lower
		// bound never reaches below the prefix boundary: the turns there
		// belong to the hub's file-backed pages, and the next cursor hands
		// the client off at exactly that boundary — the position the full
		// projection would point to when its own page runs out of window
		// turns.
		localHi := hi - s.prefixTurnCount
		localLo := max(localHi-limit, 0)
		next := ""
		if s.prefixTurnCount > 0 {
			next = strconv.Itoa(s.prefixTurnCount)
		} else if localLo > 0 {
			next = strconv.Itoa(localLo)
		}
		return appwire.ThreadTurnsListResponse{Data: cloneAppTurns(s.turns[localLo:localHi]), NextCursor: next}
	}
	page := appwire.PageTurns(s.turns, cursor, limit)
	page.Data = cloneAppTurns(page.Data)
	return page
}

func (s *appTurnSnapshot) snapshotLocked() []appwire.Turn {
	return cloneAppTurns(s.turns)
}

func cloneAppTurns(source []appwire.Turn) []appwire.Turn {
	turns := make([]appwire.Turn, len(source))
	for i := range turns {
		turns[i] = cloneAppTurn(source[i])
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
	if len(incoming.OutputImages) == 0 {
		incoming.OutputImages = existing.OutputImages
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
