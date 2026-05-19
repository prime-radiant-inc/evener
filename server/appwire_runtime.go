package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
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
	appserver.Subscribe(ctx, thread.ID)
	if params.IncludeTurns {
		thread.Turns = appTurnsFromNotifications(s.AppNotificationsAfter(0, thread.ID))
	}
	return appwire.ThreadReadResponse{Thread: thread}, nil
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
		turns = append(turns, appwire.Turn{ID: id, ItemsView: "full", Status: appwire.TurnStatusRunning})
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
					turn.Items[i].Status = appwire.TurnStatusRunning
				}
				return &turn.Items[i]
			}
		}
		turn.Items = append(turn.Items, appwire.ThreadItem{
			Type:   itemType,
			ID:     itemID,
			TurnID: turnID,
			Status: appwire.TurnStatusRunning,
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
			item := itemForDelta(params.TurnID, params.ItemID, "agent_message")
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
			item := itemForDelta(params.TurnID, itemID, "tool_call")
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
	text, images := inputFromItems(params.Prompt, params.Items)
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return appwire.TurnStartResponse{}, appwire.InvalidParams("prompt or items required")
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
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: turnID, Status: appwire.TurnStatusRunning}}, nil
}

func (s *Server) handleAppTurnSteer(_ context.Context, params appwire.TurnSteerParams) (appwire.EmptyResponse, error) {
	if strings.TrimSpace(params.Text) == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("text is required")
	}
	turnID := strings.TrimSpace(params.TurnID)
	if turnID == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("turnId is required")
	}
	s.mu.RLock()
	fn := s.steerFunc
	activeTurnID := s.appActiveTurnID
	reservedTurnID := s.appReservedTurnID
	processing := s.processing
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("steer not available")
	}
	if !processing && strings.TrimSpace(reservedTurnID) == "" {
		return appwire.EmptyResponse{}, appwire.Conflict("turn is not active")
	}
	if turnID != activeTurnID {
		return appwire.EmptyResponse{}, appwire.Conflict("turn is not active")
	}
	fn(params.Text)
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppTurnInterrupt(_ context.Context, params appwire.TurnInterruptParams) (appwire.EmptyResponse, error) {
	turnID := strings.TrimSpace(params.TurnID)
	if turnID == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("turnId is required")
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
// When params.Items carries image attachments (kata t5j6), the request is
// routed through queueWithImagesFunc when available so the queued entry
// preserves the image bytes for the eventual user turn.
func (s *Server) handleAppTurnQueue(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
	text, images := inputFromItems(params.Text, params.Items)
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return appwire.EmptyResponse{}, appwire.InvalidParams("text or items required")
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
func (s *Server) handleAppTurnDrainAsSteer(_ context.Context, _ appwire.TurnDrainAsSteerParams) (appwire.EmptyResponse, error) {
	s.mu.RLock()
	fn := s.drainSteerFunc
	depthFn := s.queueDepthFn
	processing := s.processing
	reservedTurnID := s.appReservedTurnID
	closed := appStatus(s.status.State, processing) == appwire.ThreadStatusClosed
	s.mu.RUnlock()
	if closed {
		return appwire.EmptyResponse{}, appwire.Conflict("session is closed")
	}
	if !processing && strings.TrimSpace(reservedTurnID) == "" {
		return appwire.EmptyResponse{}, appwire.Conflict("no active turn to steer")
	}
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("drain-as-steer not available")
	}
	if depthFn != nil && depthFn() == 0 {
		return appwire.EmptyResponse{}, appwire.Conflict("queue is empty")
	}
	if err := fn(); err != nil {
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
			Ref:             ref,
			Profile:         status.Profile,
			ContextPressure: pressure,
			Capabilities:    s.appCapabilities(status.State, processing),
			Diagnostics:     diagnostics,
			Queue:           queue,
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
	return appwire.ThreadCapabilities{
		Send:         !active && !closed,
		Steer:        s.steerFunc != nil && active && !closed,
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
		return appwire.ThreadStatusProcessing
	}
	switch strings.ToUpper(state) {
	case "IDLE":
		return appwire.ThreadStatusIdle
	case "PROCESSING":
		return appwire.ThreadStatusProcessing
	case "AWAITING", "AWAITING_INPUT", "AWAITING_REPLY":
		return appwire.ThreadStatusAwaiting
	case "WARNING":
		return appwire.ThreadStatusWarning
	case "ERRORED", "ERROR":
		return appwire.ThreadStatusError
	case "CLOSED":
		return appwire.ThreadStatusClosed
	case "ENDED":
		return appwire.ThreadStatusEnded
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
