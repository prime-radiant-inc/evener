package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/llm"
)

func (s *Server) AppServer() *appserver.Server {
	return s.appServer
}

func (s *Server) SetAppIdentity(sourceID, threadID string) {
	if sourceID == "" {
		sourceID = "local"
	}
	ref := appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
	s.mu.Lock()
	s.appSourceID = sourceID
	s.appThreadID = threadID
	s.appProjector = NewAppEventProjector(threadID, ref)
	s.appActiveTurnID = ""
	s.appReservedTurnID = ""
	s.mu.Unlock()
}

func (s *Server) SetTranscriptPathFunc(fn func() string) {
	s.mu.Lock()
	s.transcriptPathFn = fn
	s.mu.Unlock()
}

func (s *Server) AppNotificationsAfter(cursor uint64, threadID string) []appserver.SequencedNotification {
	return s.appNotifier.ReplayAfter(cursor, threadID)
}

func (s *Server) RecordAppEvent(event agent.SessionEvent) {
	s.mu.Lock()
	if !s.acceptsSessionEventLocked(event.SessionID) {
		s.mu.Unlock()
		return
	}
	s.ensureAppProjectorLocked(event.SessionID)
	projected := s.appProjector.Project(event)
	s.appActiveTurnID = s.appProjector.ActiveTurnID()
	s.mu.Unlock()

	for _, item := range projected {
		s.appNotifier.Record(item.ThreadID, item.Method, item.Params)
		s.appServer.Broadcast(item.ThreadID, item.Method, item.Params)
	}
}

func (s *Server) acceptsSessionEvent(sessionID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.acceptsSessionEventLocked(sessionID)
}

func (s *Server) acceptsSessionEventLocked(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return true
	}
	if s.appThreadID != "" {
		return sessionID == s.appThreadID || s.status.SessionID == ""
	}
	if s.status.SessionID != "" {
		return sessionID == s.status.SessionID
	}
	return true
}

func (s *Server) registerAppWireHandlers() {
	router := s.appServer.Router()
	appserver.HandleTyped(router, appwire.MethodThreadList, s.handleAppThreadList)
	appserver.HandleTyped(router, appwire.MethodThreadRead, s.handleAppThreadRead)
	appserver.HandleTyped(router, appwire.MethodTurnStart, s.handleAppTurnStart)
	appserver.HandleTyped(router, appwire.MethodTurnSteer, s.handleAppTurnSteer)
	appserver.HandleTyped(router, appwire.MethodTurnInterrupt, s.handleAppTurnInterrupt)
	appserver.HandleTyped(router, appwire.MethodTurnQueue, s.handleAppTurnQueue)
	appserver.HandleTyped(router, appwire.MethodTurnDrainAsSteer, s.handleAppTurnDrainAsSteer)
	appserver.HandleTyped(router, appwire.MethodThreadCompactStart, s.handleAppThreadCompactStart)
	appserver.HandleTyped(router, appwire.MethodThreadShutdown, s.handleAppThreadShutdown)
	appserver.HandleTyped(router, appwire.MethodThreadClear, s.handleAppThreadClear)
	appserver.HandleTyped(router, appwire.MethodThreadModelSet, s.handleAppThreadModelSet)
	appserver.HandleTyped(router, appwire.MethodSerfTasksList, s.handleAppTasksList)
	appserver.HandleTyped(router, appwire.MethodModelList, s.handleAppModelList)
}

func (s *Server) handleAppThreadList(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.appThread()}}, nil
}

func (s *Server) handleAppThreadRead(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	thread := s.appThread()
	if params.Subscribe {
		appserver.Subscribe(ctx, thread.ID)
	}
	if params.IncludeTurns {
		notificationTurns := appTurnsFromNotifications(s.AppNotificationsAfter(0, thread.ID))
		transcriptTurns := s.appTurnsFromTranscript()
		if useTranscriptTurns(transcriptTurns, notificationTurns) {
			thread.Turns = transcriptTurns
		} else {
			thread.Turns = notificationTurns
		}
	}
	return appwire.ThreadReadResponse{Thread: thread}, nil
}

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

func (s *Server) appTurnsFromTranscript() []appwire.Turn {
	s.mu.RLock()
	fn := s.transcriptPathFn
	s.mu.RUnlock()
	if fn == nil {
		return nil
	}
	path := strings.TrimSpace(fn())
	if path == "" {
		return nil
	}
	return appTurnsFromTranscriptFile(path)
}

func appTurnsFromTranscriptFile(path string) []appwire.Turn {
	header, firstCall := scanTranscriptPrelude(path, 128<<20)
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 128<<20)
	toolNames := map[string]string{}
	var turns []appwire.Turn
	preludeEmitted := false
	entryIndex := 0
	emitPrelude := func() {
		if preludeEmitted {
			return
		}
		preludeEmitted = true
		if prelude := transcriptPreludeTurn(header, firstCall); prelude != nil {
			turns = append(turns, *prelude)
		}
	}
	for scanner.Scan() {
		raw := scanner.Bytes()
		var head struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			continue
		}
		if head.Kind == "header" {
			continue
		}
		if head.Kind == "api_call" {
			var call agent.TranscriptAPICall
			if err := json.Unmarshal(raw, &call); err == nil {
				if strings.TrimSpace(call.Error) != "" {
					emitPrelude()
					info := diagnostic.FromFields(call.Source, call.Title, call.Hint, call.Error)
					entryIndex++
					turns = append(turns, appwire.Turn{
						ID:        fmt.Sprintf("turn_%d", entryIndex),
						ItemsView: "full",
						Status:    appwire.TurnStatusFailed,
						Error: &appwire.TurnError{
							Message: call.Error,
							Source:  string(info.Source),
							Title:   info.Title,
							Hint:    info.Hint,
						},
					})
				}
			}
			continue
		}
		if head.Kind != "entry" {
			continue
		}
		emitPrelude()
		var entry agent.TranscriptEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			continue
		}
		entryIndex++
		turnID := fmt.Sprintf("turn_%d", entryIndex)
		items := appItemsFromTranscriptTurn(turnID, entryIndex, entry.Turn, toolNames)
		if len(items) == 0 {
			continue
		}
		turns = append(turns, appwire.Turn{ID: turnID, Items: items, ItemsView: "full", Status: appwire.TurnStatusCompleted})
	}
	emitPrelude()
	return turns
}

func scanTranscriptPrelude(path string, maxLineBytes int) (agent.TranscriptHeader, *agent.TranscriptAPICall) {
	f, err := os.Open(path)
	if err != nil {
		return agent.TranscriptHeader{}, nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	var header agent.TranscriptHeader
	for scanner.Scan() {
		raw := scanner.Bytes()
		var head struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			continue
		}
		switch head.Kind {
		case "header":
			_ = json.Unmarshal(raw, &header)
		case "api_call":
			var call agent.TranscriptAPICall
			if err := json.Unmarshal(raw, &call); err == nil {
				return header, &call
			}
		}
	}
	return header, nil
}

func transcriptPreludeTurn(header agent.TranscriptHeader, firstCall *agent.TranscriptAPICall) *appwire.Turn {
	var items []appwire.ThreadItem
	systemPrompt := strings.TrimSpace(header.SystemPrompt)
	if systemPrompt == "" && firstCall != nil {
		systemPrompt = strings.TrimSpace(firstCall.SystemPrompt)
	}
	if systemPrompt != "" {
		items = append(items, appwire.ThreadItem{
			Type:        "systemMessage",
			ID:          "item_system_prompt",
			TurnID:      "turn_system",
			Description: "System prompt",
			Text:        systemPrompt,
			Status:      "completed",
		})
	}
	if firstCall != nil && len(firstCall.Request.ToolNames) > 0 {
		items = append(items, appwire.ThreadItem{
			Type:        "systemMessage",
			ID:          "item_tools",
			TurnID:      "turn_system",
			Description: fmt.Sprintf("Tools (%d)", len(firstCall.Request.ToolNames)),
			Text:        formatTranscriptToolNames(firstCall.Request.ToolNames),
			Status:      "completed",
		})
	}
	if len(items) == 0 {
		return nil
	}
	return &appwire.Turn{ID: "turn_system", Items: items, ItemsView: "full", Status: appwire.TurnStatusCompleted}
}

func formatTranscriptToolNames(names []string) string {
	var b strings.Builder
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		b.WriteString(name)
	}
	return b.String()
}

func appItemsFromTranscriptTurn(turnID string, turnIndex int, turn agent.Turn, toolNames map[string]string) []appwire.ThreadItem {
	switch turn.Kind {
	case agent.TurnCheckpoint, agent.TurnSummary:
		text := strings.TrimSpace(turn.Message.Text())
		if text == "" {
			return nil
		}
		return []appwire.ThreadItem{{
			Type:                 "systemMessage",
			ID:                   fmt.Sprintf("item_compaction_%d", turnIndex),
			TurnID:               turnID,
			TranscriptEntryIndex: turnIndex,
			Description:          compactionDescription(string(turn.Kind)),
			Text:                 text,
			Status:               "completed",
		}}
	case agent.TurnUserInput:
		images := appInputImagesFromContent(turn.Message.Content)
		return []appwire.ThreadItem{{
			Type:                 "userMessage",
			ID:                   fmt.Sprintf("item_user_%d", turnIndex),
			TurnID:               turnID,
			TranscriptEntryIndex: turnIndex,
			Text:                 turn.Message.Text(),
			Images:               images,
			Status:               "completed",
		}}
	case agent.TurnSteering:
		images := appInputImagesFromContent(turn.Message.Content)
		text := turn.Message.Text()
		if text == "" && len(images) > 0 {
			text = imagePreviewText(len(images))
		}
		return []appwire.ThreadItem{{
			Type:   "steering",
			ID:     fmt.Sprintf("item_steering_%d", turnIndex),
			TurnID: turnID,
			Text:   text,
			Images: images,
			Status: "completed",
		}}
	case agent.TurnAssistant:
		var items []appwire.ThreadItem
		for i, part := range turn.Message.Content {
			switch part.Kind {
			case llm.ContentText:
				if part.Text != "" {
					items = append(items, appwire.ThreadItem{
						Type:   "agentMessage",
						ID:     fmt.Sprintf("item_assistant_%d_%d", turnIndex, i),
						TurnID: turnID,
						Text:   part.Text,
						Status: "completed",
					})
				}
			case llm.ContentToolCall:
				if part.ToolCall == nil {
					continue
				}
				toolNames[part.ToolCall.ID] = part.ToolCall.Name
				if part.ToolCall.Name == "communicate" {
					if text := communicateMessageFromArguments(part.ToolCall.Arguments); text != "" {
						items = append(items, appwire.ThreadItem{
							Type:   "agentMessage",
							ID:     fmt.Sprintf("item_assistant_%d_%d", turnIndex, i),
							TurnID: turnID,
							Text:   text,
							Status: "completed",
						})
					}
					continue
				}
				items = append(items, appwire.ThreadItem{
					Type:          "commandExecution",
					ID:            fmt.Sprintf("item_tool_%d_%d", turnIndex, i),
					TurnID:        turnID,
					ToolName:      part.ToolCall.Name,
					CallID:        part.ToolCall.ID,
					ArgumentsJSON: string(part.ToolCall.Arguments),
					Status:        appwire.TurnStatusInProgress,
				})
			}
		}
		return items
	case agent.TurnTool, agent.TurnToolResults:
		var items []appwire.ThreadItem
		for i, part := range turn.Message.Content {
			if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
				continue
			}
			name := part.ToolResult.Name
			if name == "" {
				name = toolNames[part.ToolResult.ToolCallID]
			}
			if name == "communicate" {
				delete(toolNames, part.ToolResult.ToolCallID)
				continue
			}
			item := appwire.ThreadItem{
				Type:     "commandExecution",
				ID:       fmt.Sprintf("item_tool_result_%d_%d", turnIndex, i),
				TurnID:   turnID,
				ToolName: name,
				CallID:   part.ToolResult.ToolCallID,
				Status:   "completed",
			}
			if part.ToolResult.IsError {
				item.Error = stringifyToolContent(part.ToolResult.Content)
			} else {
				item.Output = stringifyToolContent(part.ToolResult.Content)
			}
			items = append(items, item)
		}
		return items
	default:
		return nil
	}
}

func appInputImagesFromContent(parts []llm.ContentPart) []appwire.InputItem {
	var images []appwire.InputItem
	for _, part := range parts {
		if part.Kind != llm.ContentImage || part.Image == nil || len(part.Image.Data) == 0 {
			continue
		}
		images = append(images, appwire.InputItem{
			Type:      "input_image",
			MediaType: part.Image.MediaType,
			Name:      "",
		})
	}
	return images
}

func communicateMessageFromArguments(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var args struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	return strings.TrimSpace(args.Message)
}

func stringifyToolContent(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

func appTurnsFromNotifications(records []appserver.SequencedNotification) []appwire.Turn {
	var turns []appwire.Turn
	turnIndex := map[string]int{}

	ensureTurn := func(id string) *appwire.Turn {
		if strings.TrimSpace(id) == "" {
			return nil
		}
		if idx, ok := turnIndex[id]; ok {
			return &turns[idx]
		}
		turns = append(turns, appwire.Turn{ID: id, ItemsView: "full", Status: appwire.TurnStatusInProgress})
		turnIndex[id] = len(turns) - 1
		return &turns[len(turns)-1]
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
		turn.Items = append(turn.Items, appwire.ThreadItem{
			Type:   itemType,
			ID:     itemID,
			TurnID: turnID,
			Status: appwire.TurnStatusInProgress,
		})
		return &turn.Items[len(turn.Items)-1]
	}

	for _, record := range records {
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
		case appwire.NotifyItemStarted, appwire.NotifyItemCompleted:
			var params struct {
				TurnID string             `json:"turnId"`
				Item   appwire.ThreadItem `json:"item"`
			}
			if json.Unmarshal(record.Notification.Params, &params) != nil {
				continue
			}
			upsertItem(params.TurnID, params.Item)
		case appwire.NotifyAgentMessageDelta:
			var params appwire.AgentMessageDeltaParams
			if json.Unmarshal(record.Notification.Params, &params) != nil {
				continue
			}
			item := itemForDelta(params.TurnID, params.ItemID, "agentMessage")
			if item != nil {
				item.Text += params.Delta
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
			item := itemForDelta(params.TurnID, itemID, "commandExecution")
			if item != nil {
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
			turn.Error = params.Turn.Error
			for _, item := range params.Turn.Items {
				upsertItem(params.Turn.ID, item)
			}
		}
	}
	return turns
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

func (s *Server) handleAppTurnStart(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	text, images := inputFromItems("", params.Input)
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return appwire.TurnStartResponse{}, appwire.InvalidParams("input is required")
	}

	turnID, err := s.reserveAppTurnIDForStart()
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	select {
	case s.inputCh <- InputMessage{Text: text, Images: images}:
	default:
		s.releaseAppTurnID(turnID)
		return appwire.TurnStartResponse{}, appwire.Conflict("input buffer full")
	}
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: turnID, Status: appwire.TurnStatusInProgress}}, nil
}

func (s *Server) handleAppTurnSteer(_ context.Context, params appwire.TurnSteerParams) (appwire.EmptyResponse, error) {
	text, images := inputFromItems("", params.Input)
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return appwire.EmptyResponse{}, appwire.InvalidParams("input is required")
	}
	turnID := strings.TrimSpace(params.ExpectedTurnID)
	if turnID == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("expectedTurnId is required")
	}
	s.mu.RLock()
	fn := s.steerFunc
	imgFn := s.steerWithImagesFunc
	activeTurnID := s.appActiveTurnID
	reservedTurnID := s.appReservedTurnID
	processing := s.processing
	s.mu.RUnlock()
	if fn == nil && imgFn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("steer not available")
	}
	if len(images) > 0 && imgFn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("steer with images not available")
	}
	if !processing && strings.TrimSpace(reservedTurnID) == "" {
		return appwire.EmptyResponse{}, appwire.Conflict("turn is not active")
	}
	if turnID != activeTurnID {
		return appwire.EmptyResponse{}, appwire.Conflict("turn is not active")
	}
	if imgFn != nil {
		imgFn(text, images)
	} else {
		fn(text)
	}
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppTurnInterrupt(_ context.Context, params appwire.TurnInterruptParams) (appwire.EmptyResponse, error) {
	turnID := strings.TrimSpace(params.ExpectedTurnID)
	if turnID == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("expectedTurnId is required")
	}
	s.mu.RLock()
	cancel := s.cancelFunc
	activeTurnID := s.appActiveTurnID
	s.mu.RUnlock()
	if turnID != activeTurnID {
		return appwire.EmptyResponse{}, appwire.Conflict("turn is not active")
	}
	if cancel != nil {
		cancel()
	}
	return appwire.EmptyResponse{}, nil
}

// handleAppTurnQueue handles turn/queue (kata 111a). The session must be
// processing for the call to be meaningful — calling on an idle session is
// rejected with Conflict so callers fall back to turn/start instead.
// When params.Input carries image attachments (kata t5j6), the request is
// routed through queueWithImagesFunc when available so the queued entry
// preserves the image bytes for the eventual user turn.
func (s *Server) handleAppTurnQueue(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
	text, images := inputFromItems("", params.Input)
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return appwire.EmptyResponse{}, appwire.InvalidParams("input required")
	}
	s.mu.RLock()
	fn := s.queueFunc
	imgFn := s.queueWithImagesFunc
	processing := s.processing
	reservedTurnID := s.appReservedTurnID
	closed := appStatus(s.status.State, processing) == appwire.ThreadStatusClosed
	s.mu.RUnlock()
	if closed {
		return appwire.EmptyResponse{}, appwire.Conflict("session is closed")
	}
	if !processing && strings.TrimSpace(reservedTurnID) == "" {
		return appwire.EmptyResponse{}, appwire.Conflict("no active turn to queue against")
	}
	if len(images) > 0 {
		if imgFn == nil {
			return appwire.EmptyResponse{}, appwire.Unavailable("image queue not available")
		}
		if err := imgFn(text, images); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, nil
	}
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("queue not available")
	}
	if err := fn(text); err != nil {
		return appwire.EmptyResponse{}, err
	}
	return appwire.EmptyResponse{}, nil
}

// handleAppTurnDrainAsSteer handles turn/drainAsSteer (kata 0bq1).
func (s *Server) handleAppTurnDrainAsSteer(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.EmptyResponse, error) {
	text, images := inputFromItems("", params.Input)
	hasInput := strings.TrimSpace(text) != "" || len(images) > 0
	s.mu.RLock()
	fn := s.drainSteerFunc
	inputFn := s.drainSteerInputFunc
	depthFn := s.queueDepthFn
	processing := s.processing
	closed := appStatus(s.status.State, processing) == appwire.ThreadStatusClosed
	s.mu.RUnlock()
	if closed {
		return appwire.EmptyResponse{}, appwire.Conflict("session is closed")
	}
	if !processing {
		return appwire.EmptyResponse{}, appwire.Conflict("no active turn to steer")
	}
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("drain-as-steer not available")
	}
	if hasInput && inputFn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("drain-as-steer with input not available")
	}
	if !hasInput && depthFn != nil && depthFn() == 0 {
		return appwire.EmptyResponse{}, appwire.Conflict("queue is empty")
	}
	var err error
	if hasInput {
		err = inputFn(text, images)
	} else {
		err = fn()
	}
	if err != nil {
		return appwire.EmptyResponse{}, err
	}
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppThreadCompactStart(ctx context.Context, _ appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
	s.mu.RLock()
	fn := s.compactFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("compact not available")
	}
	return appwire.EmptyResponse{}, fn(ctx)
}

func (s *Server) handleAppThreadShutdown(context.Context, appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
	s.mu.RLock()
	fn := s.shutdownFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("shutdown not available")
	}
	go fn()
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppThreadClear(ctx context.Context, _ appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	s.mu.RLock()
	processing := s.processing
	fn := s.clearFunc
	s.mu.RUnlock()
	if processing {
		return appwire.ThreadClearResponse{}, appwire.Conflict("session is processing")
	}
	if fn == nil {
		return appwire.ThreadClearResponse{}, appwire.Unavailable("clear not available")
	}
	if err := fn(ctx); err != nil {
		return appwire.ThreadClearResponse{}, err
	}
	thread := s.appThread()
	return appwire.ThreadClearResponse{Thread: thread, Ref: thread.Serf.Ref}, nil
}

func (s *Server) handleAppThreadModelSet(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
	model := strings.TrimSpace(params.Model)
	if model == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("model is required")
	}
	if provider := strings.TrimSpace(params.ModelProvider); provider != "" && !strings.Contains(model, "/") {
		model = provider + "/" + model
	}
	s.mu.RLock()
	fn := s.modelFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("model change not available")
	}
	fn(model)
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppTasksList(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	s.mu.RLock()
	fn := s.tasksFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TaskListResponse{}, nil
	}
	return appwire.TaskListResponse{Data: fn()}, nil
}

func (s *Server) handleAppModelList(ctx context.Context, _ appwire.ModelListParams) (appwire.ModelListResponse, error) {
	s.mu.RLock()
	fn := s.listModelsFunc
	provider := s.status.Profile
	s.mu.RUnlock()
	if fn == nil {
		return appwire.ModelListResponse{}, nil
	}
	models, err := fn(ctx)
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	out := make([]appwire.ModelDescriptor, 0, len(models))
	for _, model := range models {
		out = append(out, appwire.ModelDescriptor{Provider: provider, Model: model.ID})
	}
	return appwire.ModelListResponse{Data: out}, nil
}

func (s *Server) appThread() appwire.Thread {
	s.mu.RLock()
	status := s.status
	sourceID := s.appSourceID
	threadID := s.appThreadID
	processing := s.processing
	pfn := s.pressureFn
	cmfn := s.contextMetricsFn
	dfn := s.detailedStatusFn
	qpfn := s.queuePreviewFn
	qdfn := s.queueDepthFn
	s.mu.RUnlock()

	if sourceID == "" {
		sourceID = "local"
	}
	if threadID == "" {
		threadID = status.SessionID
	}
	ref := appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
	pressure := status.ContextPressure
	if pfn != nil {
		pressure = pfn()
	}
	metrics := ContextMetrics{
		Used:      status.ContextUsed,
		Window:    status.ContextWindow,
		Remaining: status.ContextRemaining,
	}
	if cmfn != nil {
		metrics = cmfn()
	}
	var diagnostics *appwire.SerfDiagnostics
	if dfn != nil {
		ds := dfn()
		diagnostics = appDiagnosticsFromDetailedStatus(ds)
	}
	queue := appwire.QueueState{}
	if qpfn != nil {
		if preview := qpfn(); len(preview) > 0 {
			queue.Preview = append([]string(nil), preview...)
			queue.Depth = len(preview)
		}
	}
	// Fall back to depthFn when preview isn't wired (some tests stub only
	// the depth callback). Without this we'd silently drop authoritative
	// depth information.
	if queue.Depth == 0 && qdfn != nil {
		queue.Depth = qdfn()
	}
	return appwire.Thread{
		ID:            threadID,
		SessionID:     status.SessionID,
		Preview:       status.SessionID,
		ModelProvider: status.Model,
		Status:        appwire.ThreadStatus{Type: appStatus(status.State, processing)},
		CWD:           status.WorkingDir,
		Path:          filepath.Base(status.WorkingDir),
		Source:        sourceID,
		Serf: appwire.SerfThread{
			Ref:              ref,
			Profile:          status.Profile,
			ContextPressure:  pressure,
			ContextUsed:      metrics.Used,
			ContextWindow:    metrics.Window,
			ContextRemaining: metrics.Remaining,
			Capabilities:     s.appCapabilities(status.State, processing),
			Diagnostics:      diagnostics,
			Queue:            queue,
		},
	}
}

func appDiagnosticsFromDetailedStatus(ds DetailedStatus) *appwire.SerfDiagnostics {
	out := &appwire.SerfDiagnostics{
		Hooks: make(map[string]int, len(ds.Hooks)),
	}
	for _, tool := range ds.Tools {
		out.Tools = append(out.Tools, appwire.SerfToolInfo{Name: tool.Name, Source: tool.Source})
	}
	for _, srv := range ds.MCP {
		out.MCP = append(out.MCP, appwire.SerfMCPServerInfo{Name: srv.Name, Tools: append([]string(nil), srv.Tools...)})
	}
	for _, skill := range ds.Skills {
		out.Skills = append(out.Skills, appwire.SerfSkillInfo{Name: skill.Name, Description: skill.Description})
	}
	for _, plugin := range ds.Plugins {
		out.Plugins = append(out.Plugins, appwire.SerfPluginInfo{
			Name:       plugin.Name,
			Version:    plugin.Version,
			SkillCount: plugin.SkillCount,
			AgentCount: plugin.AgentCount,
			HookCount:  plugin.HookCount,
			MCPCount:   plugin.MCPCount,
		})
	}
	for event, count := range ds.Hooks {
		out.Hooks[event] = count
	}
	for _, sub := range ds.Subagents {
		out.Subagents = append(out.Subagents, appwire.SerfSubagentInfo{ID: sub.ID, Status: sub.Status, TurnsUsed: sub.TurnsUsed})
	}
	out.Agents = append(out.Agents, ds.Agents...)
	return out
}

func (s *Server) appCapabilities(state string, processing bool) appwire.ThreadCapabilities {
	s.mu.RLock()
	defer s.mu.RUnlock()
	closed := appStatus(state, processing) == appwire.ThreadStatusClosed
	active := processing || strings.TrimSpace(s.appReservedTurnID) != ""
	steerAvailable := s.steerFunc != nil || s.steerWithImagesFunc != nil
	return appwire.ThreadCapabilities{
		Send:         !active && !closed,
		Steer:        steerAvailable && active && !closed,
		Interrupt:    s.cancelFunc != nil,
		Compact:      s.compactFunc != nil && !closed,
		Clear:        s.clearFunc != nil && !processing && !closed,
		ForkFromTurn: false,
		Shutdown:     s.shutdownFunc != nil,
		ChangeModel:  s.modelFunc != nil && !closed,
		// Queue mirrors Steer's "active turn" gate: only meaningful while
		// a turn is in flight or reserved by turn/start (kata 111a).
		Queue: s.queueFunc != nil && active && !closed,
	}
}

func (s *Server) ensureAppProjectorLocked(threadID string) {
	if s.appProjector != nil {
		return
	}
	if threadID == "" {
		threadID = s.appThreadID
	}
	ref := appwire.Ref{SourceID: s.appSourceID, ThreadID: threadID}.String()
	s.appProjector = NewAppEventProjector(threadID, ref)
}

func (s *Server) reserveAppTurnID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAppProjectorLocked("")
	turnID := s.appProjector.ReserveTurnID()
	s.appActiveTurnID = turnID
	s.appReservedTurnID = turnID
	return turnID
}

func (s *Server) reserveAppTurnIDForStart() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	processing := s.processing
	closed := appStatus(s.status.State, processing) == appwire.ThreadStatusClosed
	if closed {
		return "", appwire.Conflict("session is closed")
	}
	if processing || strings.TrimSpace(s.appReservedTurnID) != "" {
		return "", appwire.Conflict("session is processing")
	}
	s.ensureAppProjectorLocked("")
	turnID := s.appProjector.ReserveTurnID()
	s.appActiveTurnID = turnID
	s.appReservedTurnID = turnID
	return turnID, nil
}

func (s *Server) releaseAppTurnID(turnID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appProjector != nil {
		s.appProjector.ReleaseReservedTurnID(turnID)
		s.appActiveTurnID = s.appProjector.ActiveTurnID()
	}
	if s.appReservedTurnID == turnID {
		s.appReservedTurnID = ""
	}
}

func appStatus(state string, processing bool) string {
	if processing {
		return appwire.ThreadStatusActive
	}
	switch strings.TrimSpace(state) {
	case appwire.ThreadStatusIdle:
		return appwire.ThreadStatusIdle
	case appwire.ThreadStatusActive:
		return appwire.ThreadStatusActive
	case appwire.ThreadStatusAwaiting:
		return appwire.ThreadStatusAwaiting
	case appwire.ThreadStatusWarning:
		return appwire.ThreadStatusWarning
	case appwire.ThreadStatusSystemError:
		return appwire.ThreadStatusSystemError
	case appwire.ThreadStatusClosed:
		return appwire.ThreadStatusClosed
	case appwire.ThreadStatusNotLoaded:
		return appwire.ThreadStatusNotLoaded
	default:
		return appwire.ThreadStatusIdle
	}
}

func inputFromItems(prompt string, items []appwire.InputItem) (string, []ImageAttachment) {
	parts := []string{}
	if strings.TrimSpace(prompt) != "" {
		parts = append(parts, prompt)
	}
	images := make([]ImageAttachment, 0)
	for _, item := range items {
		switch item.Type {
		case "text", "input_text", "":
			if strings.TrimSpace(item.Text) != "" {
				parts = append(parts, item.Text)
			}
		case "image", "input_image":
			images = append(images, ImageAttachment{
				MediaType: item.MediaType,
				Data:      item.Data,
				Name:      item.Name,
			})
		}
	}
	return strings.Join(parts, "\n"), images
}
