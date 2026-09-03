package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
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
	appTurnIncarnationSerial atomic.Uint64
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
		return positionAppItems(apptranscript.ProjectTurn(turnID, entryIndex, turn, toolNames, nil, apptranscript.ToolResultOutputImages), turnID, uint64(entryIndex-1))
	})
	positionAppTurns(turns)
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
		return positionAppItems(apptranscript.ProjectTurn(turnID, entryIndex, turn, toolNames, nil, apptranscript.ToolResultOutputImages), turnID, uint64(entryIndex-1))
	})
	positionAppTurns(turns)
	return turns, highest, err
}

func positionAppItems(items []appwire.ThreadItem, turnID string, entry uint64) []appwire.ThreadItem {
	for i := range items {
		position := appwire.ThreadItemPosition{Entry: entry, Item: uint32(i)}
		items[i].Position = &position
		items[i].TranscriptKey = appitempaging.TranscriptItemKey(turnID, position)
	}
	return items
}

// positionAppTurns repairs the prelude coordinate shift after the shared
// legacy projector has produced the complete turn slice. Entry zero is reserved
// for the synthetic prelude whenever one exists.
func positionAppTurns(turns []appwire.Turn) {
	prelude := len(turns) > 0 && turns[0].ID == appwire.SystemPreludeTurnID
	for ti := range turns {
		for ii := range turns[ti].Items {
			item := &turns[ti].Items[ii]
			if item.Position == nil {
				position := appwire.ThreadItemPosition{Entry: uint64(ti), Item: uint32(ii)}
				item.Position = &position
			}
			position := *item.Position
			if prelude && ti == 0 {
				position = appwire.ThreadItemPosition{Entry: 0, Item: uint32(ii)}
			} else if prelude {
				position.Entry++
			}
			item.Position = &position
			item.TranscriptKey = appitempaging.TranscriptItemKey(turns[ti].ID, position)
		}
	}
}

func positionMissingAppItems(turns []appwire.Turn) {
	for ti := range turns {
		for ii := range turns[ti].Items {
			item := &turns[ti].Items[ii]
			if item.Position == nil {
				entry := uint64(ti)
				if turns[ti].ID == appwire.SystemPreludeTurnID {
					entry = 0
				}
				position := appwire.ThreadItemPosition{Entry: entry, Item: uint32(ii)}
				item.Position = &position
			}
			if item.TranscriptKey == "" {
				item.TranscriptKey = appitempaging.TranscriptItemKey(turns[ti].ID, *item.Position)
			}
		}
	}
}

type appTurnSnapshot struct {
	mu                    sync.Mutex
	threadID              string
	threadRef             string
	transcriptIncarnation string
	incarnationEpoch      uint64
	turns                 []appwire.Turn
	turnIndex             map[string]int
	itemPositions         map[string]appwire.ThreadItemPosition
	turnEntries           map[string]uint64
	nextLiveEntry         uint64
	itemProjection        *appItemProjection
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

// appItemProjection is the stable, ordered index for item-mode paging. It
// deliberately stores only source coordinates and scalar identity metadata;
// item and turn values are cloned from turns only for the requested page.
type appItemProjection struct {
	items      []appItemProjectionItem
	byPosition map[appwire.ThreadItemPosition]int
	err        error
}

type appItemProjectionItem struct {
	turnIndex int
	itemIndex int
	turnID    string
	position  appwire.ThreadItemPosition
}

type appTurnSeed struct {
	Turns                 []appwire.Turn
	ThreadRef             string
	TranscriptIncarnation string
	NextEntry             uint64
}

// Seed installs a full projection as the snapshot's starting state, replacing
// anything already reduced. The caller keeps ownership of turns: every turn and
// nested item is deep-cloned, so later mutation of the argument cannot reach
// installed state.
func (s *appTurnSnapshot) Seed(value any) {
	seed := appTurnSeed{}
	switch typed := value.(type) {
	case appTurnSeed:
		seed = typed
	case []appwire.Turn: // compatibility for rejoin tests and legacy callers
		seed.Turns = typed
		seed.NextEntry = uint64(len(typed))
	default:
		panic(fmt.Sprintf("unsupported app turn seed type %T", value))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.itemProjection = nil
	if seed.ThreadRef != "" {
		s.threadRef = seed.ThreadRef
	}
	if seed.TranscriptIncarnation != "" {
		s.transcriptIncarnation = seed.TranscriptIncarnation
	}
	if s.threadRef == "" && s.threadID != "" {
		s.threadRef = "local:" + s.threadID
	}
	if s.transcriptIncarnation == "" {
		s.transcriptIncarnation = fmt.Sprintf("appwire-live-v%d", appTurnIncarnationSerial.Add(1))
	}
	s.nextLiveEntry = seed.NextEntry
	s.incarnationEpoch = 0

	s.turns = make([]appwire.Turn, len(seed.Turns))
	s.turnIndex = make(map[string]int, len(seed.Turns))
	s.itemPositions = map[string]appwire.ThreadItemPosition{}
	s.turnEntries = map[string]uint64{}
	s.activeTurnID = ""
	for i := range seed.Turns {
		s.turns[i] = cloneAppTurn(seed.Turns[i])
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
	positionMissingAppItems(s.turns)
	for i, turn := range s.turns {
		if turn.ID != appwire.SystemPreludeTurnID {
			entry := uint64(i)
			for _, item := range turn.Items {
				if item.Position != nil {
					entry = item.Position.Entry
					break
				}
			}
			s.turnEntries[turn.ID] = entry
		}
		for _, item := range turn.Items {
			if item.Position != nil {
				s.itemPositions[item.TranscriptKey] = *item.Position
			}
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
	// Every nonempty committed batch can change item content, order, identity,
	// or cursor incarnation. Invalidate once here so append, delta, completion,
	// reset, prelude insertion, steering, and any future reducer mutation cannot
	// accidentally leave a stale item projection behind.
	s.itemProjection = nil
	if s.turnIndex == nil {
		s.turnIndex = map[string]int{}
	}
	if s.itemPositions == nil {
		s.itemPositions = map[string]appwire.ThreadItemPosition{}
	}
	if s.turnEntries == nil {
		s.turnEntries = map[string]uint64{}
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
			for i := range s.turns {
				for j := range s.turns[i].Items {
					item := &s.turns[i].Items[j]
					if item.Position == nil {
						continue
					}
					oldKey := item.TranscriptKey
					position := *item.Position
					position.Entry++
					item.Position = &position
					item.TranscriptKey = appitempaging.TranscriptItemKey(s.turns[i].ID, position)
					if oldKey != "" {
						delete(s.itemPositions, oldKey)
					}
				}
			}
			for turnID, entry := range s.turnEntries {
				s.turnEntries[turnID] = entry + 1
			}
			s.nextLiveEntry++
			s.turns = append([]appwire.Turn{{ID: id, ItemsView: "full", Status: appwire.TurnStatusInProgress}}, s.turns...)
			s.turnIndex = make(map[string]int, len(s.turns))
			s.itemPositions = make(map[string]appwire.ThreadItemPosition)
			for i := range s.turns {
				s.turnIndex[s.turns[i].ID] = i
				for _, item := range s.turns[i].Items {
					if item.Position != nil && item.TranscriptKey != "" {
						s.itemPositions[item.TranscriptKey] = *item.Position
					}
				}
			}
			s.rotateIncarnationLocked()
			return &s.turns[0]
		}
		s.turns = append(s.turns, appwire.Turn{ID: id, ItemsView: "full", Status: appwire.TurnStatusInProgress})
		s.turnIndex[id] = len(s.turns) - 1
		s.turnEntries[id] = s.nextLiveEntry
		s.nextLiveEntry++
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
			if appThreadItemIdentityMatches(turn.Items[i], item) {
				turn.Items[i] = mergeAppThreadItem(turn.Items[i], item)
				if turn.Items[i].Position != nil {
					s.itemPositions[turn.Items[i].TranscriptKey] = *turn.Items[i].Position
				}
				return
			}
		}
		position := s.allocateItemPositionLocked(*turn)
		item.Position = &position
		item.TranscriptKey = appitempaging.TranscriptItemKey(turn.ID, position)
		s.itemPositions[item.TranscriptKey] = position
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
		item := appwire.ThreadItem{Type: itemType, ID: itemID, TurnID: turnID, Status: appwire.TurnStatusInProgress}
		upsertItem(turnID, item)
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
					if turn.Items[i].TranscriptKey != "" {
						delete(s.itemPositions, turn.Items[i].TranscriptKey)
					}
					turn.Items = append(turn.Items[:i], turn.Items[i+1:]...)
					s.rotateIncarnationLocked()
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
			upsertItem(s.activeTurnID, appwire.ThreadItem{
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

func (s *appTurnSnapshot) allocateItemPositionLocked(turn appwire.Turn) appwire.ThreadItemPosition {
	if turn.ID == appwire.SystemPreludeTurnID {
		return appwire.ThreadItemPosition{Entry: 0, Item: uint32(len(turn.Items))}
	}
	if len(turn.Items) > 0 {
		last := turn.Items[len(turn.Items)-1]
		if last.Position != nil {
			return appwire.ThreadItemPosition{Entry: last.Position.Entry, Item: last.Position.Item + 1}
		}
	}
	entry, ok := s.turnEntries[turn.ID]
	if !ok {
		entry = s.nextLiveEntry
		s.nextLiveEntry++
		if s.turnEntries == nil {
			s.turnEntries = map[string]uint64{}
		}
		s.turnEntries[turn.ID] = entry
	}
	return appwire.ThreadItemPosition{Entry: entry}
}

func (s *appTurnSnapshot) rotateIncarnationLocked() {
	s.itemProjection = nil
	s.incarnationEpoch++
	base := s.transcriptIncarnation
	if base == "" {
		base = fmt.Sprintf("appwire-live-v%d", appTurnIncarnationSerial.Add(1))
	}
	s.transcriptIncarnation = fmt.Sprintf("%s:%d", base, s.incarnationEpoch)
}

func (s *appTurnSnapshot) Snapshot() []appwire.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *appTurnSnapshot) Latest(limit int) ([]appwire.Turn, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	turns, cursor := appwire.WindowTurns(s.turns, limit)
	return cloneAppTurns(turns), cursor
}

func (s *appTurnSnapshot) Page(cursor string, limit int) appwire.ThreadTurnsListResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := appwire.PageTurns(s.turns, cursor, limit)
	page.Data = cloneAppTurns(page.Data)
	return page
}

func (s *appTurnSnapshot) LatestItemCandidates(limit int) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error) {
	limit, err := appwire.NormalizeTranscriptItemLimit(limit)
	if err != nil {
		return appitempaging.TranscriptItemWindow{}, appitempaging.CursorIdentity{}, err
	}
	s.mu.Lock()
	window, identity, err := s.itemWindowLocked(nil, limit)
	s.mu.Unlock()
	return window, identity, err
}

func (s *appTurnSnapshot) PreviousItemCandidates(cursor string, limit int) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error) {
	limit, err := appwire.NormalizeTranscriptItemLimit(limit)
	if err != nil {
		return appitempaging.TranscriptItemWindow{}, appitempaging.CursorIdentity{}, err
	}
	s.mu.Lock()
	projection, identity := s.itemProjectionLocked()
	if projection.err != nil {
		s.mu.Unlock()
		return appitempaging.TranscriptItemWindow{}, identity, projection.err
	}
	before, err := appitempaging.DecodeCursor(cursor, identity)
	if err != nil {
		s.mu.Unlock()
		return appitempaging.TranscriptItemWindow{}, identity, err
	}
	window, _, err := s.itemWindowLocked(&before, limit)
	s.mu.Unlock()
	return window, identity, err
}

func (s *appTurnSnapshot) itemWindowLocked(before *appwire.ThreadItemPosition, limit int) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error) {
	projection, identity := s.itemProjectionLocked()
	if projection.err != nil {
		return appitempaging.TranscriptItemWindow{}, identity, projection.err
	}
	hi := len(projection.items)
	if before != nil {
		var ok bool
		hi, ok = projection.byPosition[*before]
		if !ok {
			return appitempaging.TranscriptItemWindow{}, identity, appwire.TranscriptItemCursorStale()
		}
	}
	lo := max(0, hi-limit)
	selected := make([]appitempaging.TranscriptItemCandidate, 0, hi-lo)
	for i := lo; i < hi; i++ {
		entry := projection.items[i]
		sourceTurn := s.turns[entry.turnIndex]
		turn := cloneAppTurnWithoutItems(sourceTurn)
		item := cloneAppThreadItem(sourceTurn.Items[entry.itemIndex])
		selected = append(selected, appitempaging.TranscriptItemCandidate{
			TurnID:          entry.turnID,
			Turn:            turn,
			Item:            item,
			Position:        entry.position,
			HasEarlierItems: i > 0 && projection.items[i-1].turnID == entry.turnID,
			HasLaterItems:   i+1 < len(projection.items) && projection.items[i+1].turnID == entry.turnID,
		})
	}
	window := appitempaging.TranscriptItemWindow{Candidates: selected}
	if lo > 0 {
		var err error
		window.OlderCursor, err = appitempaging.EncodeCursor(identity, selected[0].Position)
		if err != nil {
			return appitempaging.TranscriptItemWindow{}, identity, err
		}
	}
	return window, identity, nil
}

func (s *appTurnSnapshot) itemProjectionLocked() (*appItemProjection, appitempaging.CursorIdentity) {
	if s.threadRef == "" && s.threadID != "" {
		s.threadRef = "local:" + s.threadID
	}
	if s.transcriptIncarnation == "" {
		s.transcriptIncarnation = fmt.Sprintf("appwire-live-v%d", appTurnIncarnationSerial.Add(1))
	}
	identity := appitempaging.CursorIdentity{ThreadRef: s.threadRef, Incarnation: s.transcriptIncarnation, ProjectionVersion: appitempaging.TranscriptItemProjectionVersion}
	if s.itemProjection != nil {
		return s.itemProjection, identity
	}
	positionMissingAppItems(s.turns)
	if s.itemPositions == nil {
		s.itemPositions = map[string]appwire.ThreadItemPosition{}
	}
	projection := &appItemProjection{byPosition: make(map[appwire.ThreadItemPosition]int)}
	for _, turn := range s.turns {
		for _, item := range turn.Items {
			if item.Position != nil && item.TranscriptKey != "" {
				s.itemPositions[item.TranscriptKey] = *item.Position
			}
		}
	}
	seenKeys := make(map[string]struct{})
	for turnIndex, turn := range s.turns {
		for itemIndex, item := range turn.Items {
			if item.Position == nil || item.TranscriptKey == "" {
				continue
			}
			position := *item.Position
			entry := appItemProjectionItem{turnIndex: turnIndex, itemIndex: itemIndex, turnID: turn.ID, position: position}
			if entry.turnID == "" {
				projection.err = fmt.Errorf("candidate %d has empty turn id", len(projection.items))
				break
			}
			if _, exists := seenKeys[item.TranscriptKey]; exists {
				projection.err = fmt.Errorf("candidate %d repeats transcript key", len(projection.items))
				break
			}
			if len(projection.items) > 0 {
				previous := projection.items[len(projection.items)-1].position
				if position.Entry < previous.Entry || (position.Entry == previous.Entry && position.Item <= previous.Item) {
					projection.err = fmt.Errorf("candidate positions are not strictly increasing at %d", len(projection.items))
					break
				}
			}
			seenKeys[item.TranscriptKey] = struct{}{}
			projection.byPosition[position] = len(projection.items)
			projection.items = append(projection.items, entry)
		}
		if projection.err != nil {
			break
		}
	}
	s.itemProjection = projection
	return projection, identity
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
	clone := cloneAppTurnWithoutItems(turn)
	clone.Items = make([]appwire.ThreadItem, len(turn.Items))
	for i := range turn.Items {
		clone.Items[i] = cloneAppThreadItem(turn.Items[i])
	}
	return clone
}

func cloneAppTurnWithoutItems(turn appwire.Turn) appwire.Turn {
	clone := turn
	clone.Items = nil
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
	return clone
}

func cloneAppThreadItem(item appwire.ThreadItem) appwire.ThreadItem {
	clone := item
	if item.Position != nil {
		position := *item.Position
		clone.Position = &position
	}
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

func appThreadItemIdentityMatches(existing, incoming appwire.ThreadItem) bool {
	if existing.TranscriptKey != "" && incoming.TranscriptKey != "" {
		return existing.TranscriptKey == incoming.TranscriptKey
	}
	return existing.ID == incoming.ID
}

// applyLifecycleAndReturn reduces a lifecycle notification before it is
// recorded, then returns the reduced item with the position and transcript key
// allocated by the snapshot. This keeps the notification payload and the live
// snapshot on the same identity, including when an incoming stable key names an
// existing item whose display ID changed across resume.
func (s *appTurnSnapshot) applyLifecycleAndReturn(method string, params any) (any, bool) {
	if method != appwire.NotifyItemStarted && method != appwire.NotifyItemCompleted {
		return params, false
	}
	lifecycle, ok := params.(appwire.ItemLifecycleParams)
	if !ok {
		return params, false
	}
	payload, err := json.Marshal(lifecycle)
	if err != nil {
		return params, false
	}
	s.Apply([]appserver.SequencedNotification{{Notification: appwire.Notification{Method: method, Params: payload}}})
	s.mu.Lock()
	defer s.mu.Unlock()
	authoritativeTurnID := strings.TrimSpace(lifecycle.Item.TurnID)
	if authoritativeTurnID == "" {
		authoritativeTurnID = strings.TrimSpace(lifecycle.TurnID)
	}
	for _, turn := range s.turns {
		if authoritativeTurnID == "" || turn.ID != authoritativeTurnID {
			continue
		}
		for _, item := range turn.Items {
			if appThreadItemIdentityMatches(item, lifecycle.Item) {
				lifecycle.Item = cloneAppThreadItem(item)
				return lifecycle, true
			}
		}
	}
	return params, true
}

func mergeAppThreadItem(existing, incoming appwire.ThreadItem) appwire.ThreadItem {
	if incoming.TranscriptKey == "" {
		incoming.TranscriptKey = existing.TranscriptKey
	}
	if incoming.Position == nil || (existing.Position != nil && *incoming.Position != *existing.Position) {
		incoming.Position = existing.Position
	}
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
