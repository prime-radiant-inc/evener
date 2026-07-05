package server

import (
	"context"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appprojector"
	"primeradiant.com/serf/internal/appserver"
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
	s.appProjector = appprojector.NewAppEventProjector(threadID, ref)
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

func (s *Server) RecordAppEvent(event events.SessionEvent) {
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
	appserver.HandleTyped(router, appwire.MethodGoalSet, s.handleAppGoalSet)
	appserver.HandleTyped(router, appwire.MethodThreadCompactStart, s.handleAppThreadCompactStart)
	appserver.HandleTyped(router, appwire.MethodThreadShutdown, s.handleAppThreadShutdown)
	appserver.HandleTyped(router, appwire.MethodThreadClear, s.handleAppThreadClear)
	appserver.HandleTyped(router, appwire.MethodThreadModelSet, s.handleAppThreadModelSet)
	appserver.HandleTyped(router, appwire.MethodSerfThreadNameSet, s.handleAppThreadNameSet)
	appserver.HandleTyped(router, appwire.MethodThreadReasoningEffortSet, s.handleAppThreadReasoningEffortSet)
	appserver.HandleTyped(router, appwire.MethodSerfTasksList, s.handleAppTasksList)
	appserver.HandleTyped(router, appwire.MethodModelList, s.handleAppModelList)
	appserver.HandleTyped(router, appwire.MethodThreadTurnsList, s.handleAppThreadTurnsList)
}

func (s *Server) handleAppThreadList(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.appThread()}}, nil
}

func (s *Server) handleAppThreadRead(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	thread := s.appThread()
	if params.Subscribe {
		appserver.Subscribe(ctx, thread.ID)
	}
	var olderCursor string
	if params.IncludeTurns {
		thread.Turns, olderCursor = appwire.WindowTurns(s.appAllTurns(thread.ID), params.TurnLimit)
	}
	return appwire.ThreadReadResponse{Thread: thread, OlderCursor: olderCursor}, nil
}

// appAllTurns materializes the full ordered turn list (oldest-first), choosing
// the transcript-file turns over the notification-derived turns when richer.
func (s *Server) appAllTurns(threadID string) []appwire.Turn {
	notificationTurns := appTurnsFromNotifications(s.AppNotificationsAfter(0, threadID))
	transcriptTurns := s.appTurnsFromTranscript()
	if useTranscriptTurns(transcriptTurns, notificationTurns) {
		return transcriptTurns
	}
	return notificationTurns
}

// handleAppThreadTurnsList pages turns backward (older) for lazy transcript
// loading. The web client seeds the latest window via thread/read(TurnLimit)
// and walks back with this as the user scrolls up.
func (s *Server) handleAppThreadTurnsList(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	return appwire.PageTurns(s.appAllTurns(s.appThread().ID), params.Cursor, params.Limit), nil
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

// handleAppGoalSet handles goal/set. An empty objective clears the goal; both
// set and clear route through the single goalFunc callback (the callback maps an
// empty objective to ClearGoal). Started reports whether the goal loop began
// immediately (idle session) versus after the current turn (a turn is running,
// whose gate is the backstop).
func (s *Server) handleAppGoalSet(_ context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	s.mu.RLock()
	fn := s.goalFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.GoalSetResponse{}, appwire.Unavailable("goal not available")
	}
	started, err := fn(params.Objective)
	if err != nil {
		return appwire.GoalSetResponse{}, err
	}
	return appwire.GoalSetResponse{Started: started}, nil
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

func (s *Server) handleAppThreadNameSet(_ context.Context, params appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("name is required")
	}
	s.mu.RLock()
	fn := s.nameFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("rename not available")
	}
	fn(name)
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppThreadReasoningEffortSet(_ context.Context, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
	s.mu.RLock()
	fn := s.reasoningEffortFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("reasoning effort change not available")
	}
	// Normalize disable-aliases to "" and reject unknown vocabulary, so a typo or
	// direct API call can't persist a provider-rejected effort that breaks later
	// requests.
	effort := llm.NormalizeReasoningEffort(params.ReasoningEffort)
	if effort != "" && llm.ReasoningEffortRank(effort) == 0 {
		return appwire.EmptyResponse{}, appwire.InvalidParams("invalid reasoning effort: " + params.ReasoningEffort)
	}
	fn(effort)
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
	gsfn := s.goalStatusFn
	wmfn := s.workMetricsFn
	activeTurnID := s.appActiveTurnID
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
	var goalState *appwire.GoalState
	if gsfn != nil {
		if status, iterations, ok := gsfn(); ok {
			goalState = &appwire.GoalState{Status: status, Iterations: iterations}
		}
	}
	var workMillis int64
	var usage *appwire.SerfUsage
	var activeTurnStartedAt int64
	if wmfn != nil {
		workMillis, usage, activeTurnStartedAt = wmfn()
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
			Ref:                 ref,
			Profile:             status.Profile,
			ActiveTurnID:        activeTurnID,
			ContextPressure:     pressure,
			ContextUsed:         metrics.Used,
			ContextWindow:       metrics.Window,
			ContextRemaining:    metrics.Remaining,
			Capabilities:        s.appCapabilities(status.State, processing),
			Diagnostics:         diagnostics,
			Queue:               queue,
			Goal:                goalState,
			Usage:               usage,
			WorkMillis:          workMillis,
			ActiveTurnStartedAt: activeTurnStartedAt,
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
		out.MCP = append(out.MCP, appwire.SerfMCPServerInfo{Name: srv.Name, Tools: append([]string(nil), srv.Tools...), Status: srv.Status, Error: srv.Error})
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
	for _, job := range ds.Jobs {
		out.Jobs = append(out.Jobs, appwire.SerfJobInfo{
			JobID:         job.JobID,
			JobType:       job.JobType,
			Status:        job.Status,
			Reason:        job.Reason,
			ExitCode:      job.ExitCode,
			OutputBytes:   job.OutputBytes,
			TranscriptRef: job.TranscriptRef,
		})
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
		Rename:       s.nameFunc != nil && !closed,
		// Queue mirrors Steer's "active turn" gate: only meaningful while
		// a turn is in flight or reserved by turn/start (kata 111a).
		Queue: s.queueFunc != nil && active && !closed,
		// Goal is available whenever the engine is wired and the session is
		// open. It is intentionally NOT gated on !active: a goal may be set
		// mid-turn (it arms for the next continuation), unlike Send.
		Goal: s.goalFunc != nil && !closed,
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
	s.appProjector = appprojector.NewAppEventProjector(threadID, ref)
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
